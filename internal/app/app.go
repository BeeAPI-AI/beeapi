package app

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/configurator"
	"github.com/BeeAPI-AI/beeapi/internal/routeopt"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

type runner struct {
	ctx       context.Context
	version   string
	in        io.Reader
	reader    *bufio.Reader
	out       io.Writer
	errOut    io.Writer
	store     *state.Store
	logoShown bool
}

type credentialMaterial struct {
	ID           string
	Name         string
	Prefix       string
	SourcePrefix string
	Secret       string
	Models       []string
}

func Run(ctx context.Context, args []string, version string, in io.Reader, out, errOut io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			fmt.Fprintln(out, version)
			return nil
		case "help", "--help", "-h":
			printHelp(out)
			return nil
		}
	}
	store, err := state.Open()
	if err != nil {
		return err
	}
	r := &runner{ctx: ctx, version: version, in: in, reader: bufio.NewReader(in), out: out, errOut: errOut, store: store}
	if len(args) == 0 {
		cfg, loadErr := store.LoadConfig()
		if loadErr != nil {
			return loadErr
		}
		if !cfg.Initialized() {
			return r.setup(nil)
		}
		return r.home()
	}
	switch args[0] {
	case "setup", "init":
		return r.setup(args[1:])
	case "detect":
		return r.detect()
	case "status":
		return r.status()
	case "login", "connect":
		return r.reconnect()
	case "configure", "config":
		return r.configure(args[1:])
	case "network":
		return r.network(args[1:])
	case "rollback":
		return r.rollback(args[1:])
	case "token":
		return r.token(args[1:])
	case "run":
		return r.runAgent(args[1:])
	default:
		return fmt.Errorf("未知命令 %q；运行 beeapi help 查看帮助", args[0])
	}
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, `beeapi — 为现有 AI 工具快速配置 BeeAPI

用法:
  beeapi                         首次运行初始化；以后打开功能主页
  beeapi setup                   重新运行首次设置
  beeapi status                  查看当前连接与工具配置
  beeapi login                   重新授权或更新密钥配置
  beeapi detect                  检查本机已安装的 AI CLI
  beeapi configure               使用已保存凭据重新配置
  beeapi network status          检测内置 API 域名
  beeapi network optimize        使用 CloudflareSpeedTest 优选 IP
  beeapi network restore         移除 beeapi 管理的 Hosts 记录
  beeapi rollback [latest|编号]  恢复配置备份
  beeapi run <工具> [参数...]    用 BeeAPI 配置启动目标工具
  beeapi token print [--agent 工具] 仅向目标工具提供已保存的 API Key

支持: Claude Code、Claude Desktop（Code）、Codex、Gemini CLI、Grok Build、OpenCode、OpenClaw、Hermes`)
}

func (r *runner) setup(args []string) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(r.errOut)
	var endpointFlag, apiKeyFlag, agentsFlag string
	var assumeYes, noOpen bool
	flags.StringVar(&endpointFlag, "endpoint", "", "指定 BeeAPI 入口")
	flags.StringVar(&apiKeyFlag, "api-key", "", "直接提供 API Key（也可使用 BEEAPI_API_KEY）")
	flags.StringVar(&agentsFlag, "agents", "", "逗号分隔的目标工具")
	flags.BoolVar(&assumeYes, "yes", false, "接受安全默认值")
	flags.BoolVar(&noOpen, "no-open", false, "不自动打开授权网页")
	if err := flags.Parse(args); err != nil {
		return err
	}

	r.showLogo()
	fmt.Fprintln(r.out, "\n首次设置 · 连接 BeeAPI 并配置现有 AI 工具")
	fmt.Fprintln(r.out, "────────────────────────────────────────")
	endpoint, err := r.resolveEndpoint(endpointFlag, assumeYes)
	if err != nil {
		return err
	}

	fmt.Fprintln(r.out, "\n[2/3] 连接 BeeAPI 并读取可用配置")
	apiKey := strings.TrimSpace(apiKeyFlag)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("BEEAPI_API_KEY"))
	}
	var credentials []credentialMaterial
	if apiKey == "" {
		credentials, err = r.authorize(endpoint, noOpen)
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintln(r.out, "  使用命令行或环境变量提供的单个 API Key（兼容模式）。")
		credentials = []credentialMaterial{{ID: "manual", Name: "手动 API Key", Prefix: safeKeyPrefix(apiKey), Secret: apiKey}}
	}
	credentials, err = r.discoverCredentialModels(endpoint, credentials)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "  ✓ 已连接 BeeAPI · 已领取 %d 个配置\n", len(credentials))

	fmt.Fprintln(r.out, "\n[3/3] 选择工具、密钥配置与模型")
	environments, err := detectEnvironments()
	if err != nil {
		return err
	}
	printEnvironments(r.out, environments)
	agents, err := r.selectAgents(environments, agentsFlag, assumeYes)
	if err != nil {
		return err
	}
	assignments, err := r.selectCredentialAssignments(agents, credentials, nil, assumeYes)
	if err != nil {
		return err
	}
	selectedModels, err := r.selectModelsForAssignments(agents, credentials, assignments, assumeYes)
	if err != nil {
		return err
	}
	apiKeys, err := apiKeysForAssignments(agents, credentials, assignments)
	if err != nil {
		return err
	}
	binaryPath, _ := os.Executable()
	result, err := configurator.Apply(r.store, configurator.Options{
		Endpoint: endpoint, APIKeys: apiKeys, Models: selectedModels,
		Agents: agents, BinaryPath: binaryPath,
	})
	if err != nil {
		return err
	}
	storedCredentials, err := r.saveCredentialMaterials(credentials)
	if err != nil {
		_, _ = r.store.Rollback(result.BackupID)
		return err
	}
	cfg := state.Config{
		Endpoint: endpoint, KeyName: credentialSummaryName(storedCredentials), Models: selectedModels, Agents: agents,
		Credentials: storedCredentials, AgentCredentials: assignments, BinaryPath: binaryPath,
	}
	setDefaultModel(&cfg, agents, selectedModels)
	if err := r.store.SaveConfig(cfg); err != nil {
		_, _ = r.store.Rollback(result.BackupID)
		return err
	}

	fmt.Fprintln(r.out, "\n配置完成")
	fmt.Fprintf(r.out, "  API 入口  %s\n", endpoint)
	fmt.Fprintf(r.out, "  已配置    %s\n", strings.Join(agents, "、"))
	fmt.Fprintf(r.out, "  备份编号  %s\n", result.BackupID)
	for _, hint := range result.Hints {
		fmt.Fprintln(r.out, "  "+hint)
	}
	fmt.Fprintln(r.out, "\n以后直接输入 beeapi 打开功能主页；不会再次自动运行首次设置。")
	return nil
}

func (r *runner) resolveEndpoint(explicit string, assumeYes bool) (string, error) {
	fmt.Fprintln(r.out, "\n[1/3] 检测 BeeAPI 官方入口")
	var endpoints []beeapi.Endpoint
	if explicit != "" {
		normalized := beeapi.NormalizeBaseURL(explicit)
		if normalized == "" {
			return "", errors.New("指定入口必须是有效的 HTTPS 根地址")
		}
		endpoints = beeapi.ProbeEndpoints(r.ctx, []beeapi.Endpoint{{Name: "指定入口", BaseURL: normalized}})
	} else {
		endpoints = beeapi.DiscoverEndpoints(r.ctx, nil)
	}
	printEndpoints(r.out, endpoints)
	best, err := beeapi.BestEndpoint(endpoints)
	if err != nil {
		if len(endpoints) == 0 {
			return "", err
		}
		target := endpoints[0]
		if !assumeYes && explicit == "" && len(endpoints) > 1 {
			answer, readErr := r.ask("  所有入口均不可用；选择要尝试修复的域名 [1]: ")
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return "", readErr
			}
			if index, ok := endpointChoice(answer, len(endpoints)); ok {
				target = endpoints[index]
			} else if strings.TrimSpace(answer) != "" {
				return "", errors.New("入口编号无效")
			}
		}
		fmt.Fprintf(r.out, "  %s 当前不可用，开始 Cloudflare IP 优选与 TLS 校验。\n", target.BaseURL)
		if _, recoverErr := r.optimizeAndMaybeApply(hostFromURL(target.BaseURL), true, assumeYes); recoverErr != nil {
			return "", fmt.Errorf("自动修复网络失败: %w", recoverErr)
		}
		rechecked := beeapi.ProbeEndpoints(r.ctx, []beeapi.Endpoint{{Name: target.Name, BaseURL: target.BaseURL}})
		if len(rechecked) != 1 || !rechecked[0].Reachable {
			return "", errors.New("Hosts 修复后仍无法访问 BeeAPI，请运行 beeapi network restore 后检查代理、防火墙或运营商网络")
		}
		fmt.Fprintf(r.out, "  已选择 %s（%s）\n", rechecked[0].BaseURL, durationLabel(rechecked[0].Latency))
		return rechecked[0].BaseURL, nil
	}
	if !assumeYes && explicit == "" {
		answer, readErr := r.ask("  回车使用最快可用入口；也可输入编号选择其他入口: ")
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", readErr
		}
		if index, ok := endpointChoice(answer, len(endpoints)); ok {
			selected := endpoints[index]
			if !selected.Reachable {
				confirm, confirmErr := r.ask(fmt.Sprintf("  %s 不可访问，尝试优选 IP 并写入 Hosts？[Y/n]: ", selected.BaseURL))
				if confirmErr != nil && !errors.Is(confirmErr, io.EOF) {
					return "", confirmErr
				}
				if strings.EqualFold(strings.TrimSpace(confirm), "n") {
					return "", errors.New("所选入口不可用")
				}
				// The user already approved the Hosts change in the prompt above.
				if _, optErr := r.optimizeAndMaybeApply(hostFromURL(selected.BaseURL), true, true); optErr != nil {
					return "", optErr
				}
				rechecked := beeapi.ProbeEndpoints(r.ctx, []beeapi.Endpoint{selected})
				if len(rechecked) != 1 || !rechecked[0].Reachable {
					return "", errors.New("优选后所选入口仍不可用")
				}
				selected = rechecked[0]
			}
			best = selected
		} else if strings.TrimSpace(answer) != "" {
			return "", errors.New("入口编号无效")
		}
	}
	fmt.Fprintf(r.out, "  已选择 %s（%s）\n", best.BaseURL, durationLabel(best.Latency))
	return best.BaseURL, nil
}

func printEndpoints(out io.Writer, endpoints []beeapi.Endpoint) {
	for index, endpoint := range endpoints {
		if endpoint.Reachable {
			fmt.Fprintf(out, "  %d. ✓ %-10s %-24s %s\n", index+1, endpoint.Name, endpoint.BaseURL, durationLabel(endpoint.Latency))
		} else {
			fmt.Fprintf(out, "  %d. × %-10s %-24s 不可用\n", index+1, endpoint.Name, endpoint.BaseURL)
		}
	}
}

func endpointChoice(value string, total int) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 || number > total {
		return 0, false
	}
	return number - 1, true
}

func durationLabel(value time.Duration) string {
	if value <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d ms", value.Milliseconds())
}

type environment struct {
	Agent      string
	Label      string
	Executable string
	Config     string
	Detected   bool
	Reason     string
}

func detectEnvironments() ([]environment, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	definitions := []environment{
		{Agent: "claude", Label: "Claude Code", Executable: "claude", Config: filepath.Join(home, ".claude", "settings.json")},
		{Agent: "claude-desktop", Label: "Claude Desktop", Config: filepath.Join(home, ".claude", "settings.json")},
		{Agent: "codex", Label: "Codex", Executable: "codex", Config: filepath.Join(home, ".codex", "config.toml")},
		{Agent: "gemini", Label: "Gemini CLI", Executable: "gemini", Config: filepath.Join(home, ".gemini", "settings.json")},
		{Agent: "grok", Label: "Grok Build", Executable: "grok", Config: filepath.Join(home, ".grok", "config.toml")},
		{Agent: "opencode", Label: "OpenCode", Executable: "opencode", Config: filepath.Join(home, ".config", "opencode", "opencode.json")},
		{Agent: "openclaw", Label: "OpenClaw", Executable: "openclaw", Config: filepath.Join(home, ".openclaw", "openclaw.json")},
		{Agent: "hermes", Label: "Hermes", Executable: "hermes", Config: filepath.Join(home, ".hermes", "config.yaml")},
	}
	for index := range definitions {
		if definitions[index].Agent == "claude-desktop" {
			if path := findClaudeDesktop(home); path != "" {
				definitions[index].Detected = true
				definitions[index].Reason = path
			}
			continue
		}
		if path, lookupErr := exec.LookPath(definitions[index].Executable); lookupErr == nil {
			definitions[index].Detected = true
			definitions[index].Reason = path
		} else if _, statErr := os.Stat(definitions[index].Config); statErr == nil {
			definitions[index].Detected = true
			definitions[index].Reason = "发现本地配置"
		}
	}
	return definitions, nil
}

func findClaudeDesktop(home string) string {
	candidates := []string{
		filepath.Join(home, "Applications", "Claude.app"),
		filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"),
		filepath.Join(home, ".config", "Claude", "claude_desktop_config.json"),
		filepath.Join(home, ".config", "claude", "claude_desktop_config.json"),
		"/Applications/Claude.app",
	}
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		candidates = append(candidates,
			filepath.Join(localAppData, "Programs", "Claude", "Claude.exe"),
			filepath.Join(localAppData, "AnthropicClaude", "Claude.exe"),
		)
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func printEnvironments(out io.Writer, environments []environment) {
	fmt.Fprintln(out, "  本机工具环境")
	for index, env := range environments {
		status := "未检测到（仍可预配置）"
		mark := "○"
		if env.Detected {
			status = env.Reason
			mark = "✓"
		}
		fmt.Fprintf(out, "  %d. %s %-14s %s\n", index+1, mark, env.Label, status)
	}
	fmt.Fprintln(out, "  检测只用于推荐，最终由你选择；不会因为未检测到而隐藏工具。")
}

func (r *runner) selectAgents(environments []environment, explicit string, assumeYes bool) ([]string, error) {
	if explicit != "" {
		return parseAgents(explicit)
	}
	var defaults []string
	for _, env := range environments {
		if env.Detected {
			defaults = append(defaults, env.Agent)
		}
	}
	if len(defaults) == 0 {
		defaults = []string{"claude", "codex", "opencode"}
	}
	if assumeYes {
		return defaults, nil
	}
	answer, err := r.ask(fmt.Sprintf("  选择工具编号或名称（逗号分隔，回车=%s）: ", strings.Join(defaults, ",")))
	if err != nil {
		return nil, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return defaults, nil
	}
	var names []string
	for _, part := range strings.Split(answer, ",") {
		trimmed := strings.TrimSpace(part)
		if number, convErr := strconv.Atoi(trimmed); convErr == nil && number >= 1 && number <= len(environments) {
			names = append(names, environments[number-1].Agent)
		} else {
			names = append(names, trimmed)
		}
	}
	return parseAgents(strings.Join(names, ","))
}

func parseAgents(raw string) ([]string, error) {
	allowed := map[string]bool{}
	for _, agent := range configurator.SupportedAgents {
		allowed[agent] = true
	}
	aliases := map[string]string{
		"claude-code": "claude", "claude_desktop": "claude-desktop",
		"gemini-cli": "gemini", "grok-build": "grok",
		"open-code": "opencode", "open-claw": "openclaw", "hermes-agent": "hermes",
	}
	seen := map[string]bool{}
	var agents []string
	for _, part := range strings.Split(raw, ",") {
		agent := strings.ToLower(strings.TrimSpace(part))
		if aliases[agent] != "" {
			agent = aliases[agent]
		}
		if agent == "all" {
			return append([]string(nil), configurator.SupportedAgents...), nil
		}
		if !allowed[agent] {
			return nil, fmt.Errorf("不支持的工具 %q", part)
		}
		if !seen[agent] {
			seen[agent] = true
			agents = append(agents, agent)
		}
	}
	if len(agents) == 0 {
		return nil, errors.New("至少选择一个工具")
	}
	return agents, nil
}

func (r *runner) authorize(endpoint string, noOpen bool) ([]credentialMaterial, error) {
	fmt.Fprintln(r.out, "  1. 跳转网站授权登录（推荐；可在网页选择 1–10 个密钥配置）")
	fmt.Fprintln(r.out, "  2. 直接粘贴 API Key（兼容回退）")
	choice, err := r.ask("  请选择 [1]: ")
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	choice = strings.TrimSpace(choice)
	if choice == "2" {
		return r.pasteAPIKey(endpoint)
	}
	if choice != "" && choice != "1" {
		return nil, errors.New("登录方式只能选择 1 或 2")
	}
	client := beeapi.New(endpoint)
	deviceCtx, cancel := context.WithTimeout(r.ctx, 8*time.Second)
	code, err := client.StartDeviceAuth(deviceCtx)
	cancel()
	if err == nil && code.DeviceCode != "" && code.UserCode != "" {
		verifyURL := code.CompleteURI
		if verifyURL == "" {
			verifyURL = code.VerificationURI
		}
		fmt.Fprintf(r.out, "  在浏览器打开 %s\n", verifyURL)
		fmt.Fprintf(r.out, "  输入授权码: %s\n", code.UserCode)
		if !noOpen && verifyURL != "" {
			_ = openURL(verifyURL)
		}
		credentials, pollErr := r.pollDevice(client, code)
		if pollErr == nil {
			return credentials, nil
		}
		return nil, pollErr
	}
	var apiErr *beeapi.APIError
	if err != nil && (!errors.As(err, &apiErr) || (apiErr.Status != 404 && apiErr.Status != 405 && apiErr.Status != 501)) {
		fmt.Fprintf(r.out, "  网站授权暂时不可用：%v\n", err)
	} else {
		fmt.Fprintln(r.out, "  当前 BeeAPI 服务端尚未开启 CLI 设备授权。")
	}
	fmt.Fprintln(r.out, "  CLI 不会要求或接收 BeeAPI 账户密码。")
	fallback, askErr := r.ask("  是否改用粘贴 API Key？[Y/n]: ")
	if askErr != nil && !errors.Is(askErr, io.EOF) {
		return nil, askErr
	}
	if strings.EqualFold(strings.TrimSpace(fallback), "n") {
		return nil, errors.New("网站授权未完成")
	}
	return r.pasteAPIKey(endpoint)
}

func (r *runner) pasteAPIKey(endpoint string) ([]credentialMaterial, error) {
	fmt.Fprintln(r.out, "  兼容模式：请从 BeeAPI 控制台复制 API Key。")
	fmt.Fprintf(r.out, "  控制台: %s/api-keys\n", endpoint)
	secret, readErr := r.readSecret("  粘贴 API Key: ")
	if readErr != nil {
		return nil, readErr
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, errors.New("API Key 不能为空")
	}
	return []credentialMaterial{{ID: "manual", Name: "手动 API Key", Prefix: safeKeyPrefix(secret), Secret: secret}}, nil
}

func (r *runner) pollDevice(client *beeapi.Client, code beeapi.DeviceCode) ([]credentialMaterial, error) {
	interval := code.Interval
	if interval < 2 {
		interval = 5
	}
	expires := code.ExpiresIn
	if expires <= 0 {
		expires = 600
	}
	deadline := time.NewTimer(time.Duration(expires) * time.Second)
	defer deadline.Stop()
	for {
		timer := time.NewTimer(time.Duration(interval) * time.Second)
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return nil, r.ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return nil, errors.New("设备授权已过期，请重新运行 beeapi")
		case <-timer.C:
			pollCtx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
			token, err := client.PollDeviceAuth(pollCtx, code.DeviceCode)
			cancel()
			if err != nil {
				var apiErr *beeapi.APIError
				if errors.As(err, &apiErr) && apiErr.Reason == "authorization_pending" {
					continue
				}
				if errors.As(err, &apiErr) && apiErr.Reason == "slow_down" {
					interval += 5
					continue
				}
				return nil, err
			}
			if token.Pending {
				continue
			}
			if token.Error != "" {
				return nil, errors.New(token.Error)
			}
			accessToken := token.AccessToken
			if accessToken == "" {
				accessToken = token.Token
			}
			if accessToken == "" {
				return nil, errors.New("设备授权完成，但 BeeAPI 没有返回 CLI 登录令牌")
			}
			if token.TokenType != "" && !strings.EqualFold(token.TokenType, "DPoP") {
				return nil, fmt.Errorf("BeeAPI 返回了不支持的 CLI 令牌类型 %q", token.TokenType)
			}
			client.Token = accessToken
			claim, claimErr := r.claimDeviceCredentials(client)
			client.Token = ""
			if claimErr != nil {
				return nil, fmt.Errorf("领取 BeeAPI 设备凭据: %w", claimErr)
			}
			credentials := make([]credentialMaterial, 0, len(claim.Credentials))
			fmt.Fprintln(r.out, "  ✓ 网站授权完成，已领取设备专用配置:")
			for _, item := range claim.Credentials {
				name := strings.TrimSpace(item.ProfileName)
				if name == "" {
					name = strings.TrimSpace(item.DeviceKeyName)
				}
				if name == "" {
					name = "BeeAPI 配置"
				}
				fmt.Fprintf(r.out, "    • %s · %s\n", name, item.DeviceKeyPrefix)
				credentials = append(credentials, credentialMaterial{
					ID: item.CredentialID, Name: name, Prefix: item.DeviceKeyPrefix,
					SourcePrefix: item.SourceKeyPrefix, Secret: item.APIKey,
				})
			}
			return credentials, nil
		}
	}
}

func (r *runner) claimDeviceCredentials(client *beeapi.Client) (beeapi.CLICredentialClaimResult, error) {
	var claim beeapi.CLICredentialClaimResult
	for attempt := 0; attempt < 2; attempt++ {
		claimCtx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
		result, err := client.ClaimCLICredentials(claimCtx)
		cancel()
		if err == nil {
			return result, nil
		}
		claim = result
		if attempt != 0 || r.ctx.Err() != nil || !retryableClaimError(err) {
			return claim, err
		}
		fmt.Fprintln(r.out, "  领取响应中断，正在使用幂等窗口安全重试…")
	}
	return claim, errors.New("领取 BeeAPI 设备凭据失败")
}

func retryableClaimError(err error) bool {
	var apiErr *beeapi.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= 500
	}
	return !errors.Is(err, context.Canceled)
}

func safeKeyPrefix(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	visible := min(len(secret), 8)
	return secret[:visible] + "…"
}

func (r *runner) discoverCredentialModels(endpoint string, credentials []credentialMaterial) ([]credentialMaterial, error) {
	if len(credentials) == 0 {
		return nil, errors.New("没有可用的 BeeAPI 凭据")
	}
	seen := map[string]bool{}
	for index := range credentials {
		credentials[index].ID = strings.TrimSpace(credentials[index].ID)
		credentials[index].Secret = strings.TrimSpace(credentials[index].Secret)
		if credentials[index].ID == "" || credentials[index].Secret == "" {
			return nil, errors.New("BeeAPI 返回的设备凭据不完整")
		}
		if seen[credentials[index].ID] {
			return nil, errors.New("BeeAPI 返回了重复的设备凭据")
		}
		seen[credentials[index].ID] = true
		models, err := r.modelsForCredential(endpoint, credentials[index].Secret)
		if err != nil {
			return nil, fmt.Errorf("配置 %q: %w", credentials[index].Name, err)
		}
		credentials[index].Models = models
		fmt.Fprintf(r.out, "    %s · 可用模型 %d 个\n", credentials[index].Name, len(models))
	}
	return credentials, nil
}

func (r *runner) selectCredentialAssignments(agents []string, credentials []credentialMaterial, existing map[string]string, assumeYes bool) (map[string]string, error) {
	if len(credentials) == 0 {
		return nil, errors.New("没有可分配的 BeeAPI 配置")
	}
	assignments := map[string]string{}
	if len(credentials) > 1 {
		fmt.Fprintln(r.out, "\n  为每个工具选择 BeeAPI 密钥配置")
		for index, credential := range credentials {
			fmt.Fprintf(r.out, "    %d. %s · %s · %d 个模型\n", index+1, credential.Name, credential.Prefix, len(credential.Models))
		}
	}
	for _, agent := range agents {
		if peer := sharedClaudePeer(agent); peer != "" && assignments[peer] != "" {
			assignments[agent] = assignments[peer]
			fmt.Fprintf(r.out, "    %s 与 %s 共享本地配置，使用同一密钥配置\n", agentLabel(agent), agentLabel(peer))
			continue
		}
		defaultIndex := 0
		if id := strings.TrimSpace(existing[agent]); id != "" {
			if index := credentialIndex(credentials, id); index >= 0 {
				defaultIndex = index
			}
		}
		if len(credentials) == 1 || assumeYes {
			assignments[agent] = credentials[defaultIndex].ID
			continue
		}
		answer, err := r.ask(fmt.Sprintf("  %s 使用哪个配置？[%d]: ", agentLabel(agent), defaultIndex+1))
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		index := defaultIndex
		if strings.TrimSpace(answer) != "" {
			number, convErr := strconv.Atoi(strings.TrimSpace(answer))
			if convErr != nil || number < 1 || number > len(credentials) {
				return nil, fmt.Errorf("%s 的配置编号无效", agentLabel(agent))
			}
			index = number - 1
		}
		assignments[agent] = credentials[index].ID
	}
	return assignments, nil
}

func sharedClaudePeer(agent string) string {
	switch agent {
	case "claude":
		return "claude-desktop"
	case "claude-desktop":
		return "claude"
	default:
		return ""
	}
}

func credentialIndex(credentials []credentialMaterial, id string) int {
	for index, credential := range credentials {
		if credential.ID == id {
			return index
		}
	}
	return -1
}

func credentialForID(credentials []credentialMaterial, id string) (credentialMaterial, bool) {
	if index := credentialIndex(credentials, id); index >= 0 {
		return credentials[index], true
	}
	return credentialMaterial{}, false
}

func (r *runner) selectModelsForAssignments(agents []string, credentials []credentialMaterial, assignments map[string]string, assumeYes bool) (map[string]string, error) {
	selected := map[string]string{}
	fmt.Fprintln(r.out, "\n  根据所选密钥配置匹配模型")
	for _, agent := range agents {
		if peer := sharedClaudePeer(agent); peer != "" && selected[peer] != "" {
			selected[agent] = selected[peer]
			continue
		}
		credential, ok := credentialForID(credentials, assignments[agent])
		if !ok || len(credential.Models) == 0 {
			return nil, fmt.Errorf("%s 没有可用的密钥配置或模型", agentLabel(agent))
		}
		selected[agent] = recommendedModel(agent, credential.Models)
		fmt.Fprintf(r.out, "  %-16s %-24s %s\n", agentLabel(agent), credential.Name, selected[agent])
	}
	if assumeYes {
		return selected, nil
	}
	answer, err := r.ask("  使用以上推荐模型？[Y/n]: ")
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "n") {
		return selected, nil
	}
	customized := map[string]bool{}
	for _, agent := range agents {
		if peer := sharedClaudePeer(agent); peer != "" && customized[peer] {
			selected[agent] = selected[peer]
			customized[agent] = true
			continue
		}
		credential, _ := credentialForID(credentials, assignments[agent])
		fmt.Fprintf(r.out, "  %s 可用模型: %s\n", agentLabel(agent), strings.Join(credential.Models[:min(len(credential.Models), 12)], ", "))
		value, readErr := r.ask(fmt.Sprintf("  %s 模型 [%s]: ", agentLabel(agent), selected[agent]))
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		value = strings.TrimSpace(value)
		if value == "" {
			customized[agent] = true
			continue
		}
		if !containsExact(credential.Models, value) {
			return nil, fmt.Errorf("模型 %q 不在 %s 的可用列表中", value, credential.Name)
		}
		selected[agent] = value
		customized[agent] = true
	}
	return selected, nil
}

func containsExact(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (r *runner) saveCredentialMaterials(credentials []credentialMaterial) ([]state.Credential, error) {
	stored := make([]state.Credential, 0, len(credentials))
	for _, credential := range credentials {
		backend, err := r.store.SaveNamedCredential(credential.ID, credential.Secret)
		if err != nil {
			return nil, fmt.Errorf("保存配置 %q: %w", credential.Name, err)
		}
		stored = append(stored, state.Credential{
			ID: credential.ID, Name: credential.Name, Prefix: credential.Prefix,
			SourcePrefix: credential.SourcePrefix, Backend: backend,
		})
	}
	return stored, nil
}

func (r *runner) loadCredentialMaterials(cfg state.Config, withModels bool) ([]credentialMaterial, error) {
	var credentials []credentialMaterial
	if len(cfg.Credentials) == 0 {
		secret, err := r.store.LoadCredential(cfg.CredentialBackend)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(cfg.KeyName)
		if name == "" {
			name = "旧版默认配置"
		}
		credentials = []credentialMaterial{{ID: "default", Name: name, Prefix: safeKeyPrefix(secret), Secret: secret}}
	} else {
		credentials = make([]credentialMaterial, 0, len(cfg.Credentials))
		seen := map[string]bool{}
		for _, stored := range cfg.Credentials {
			if strings.TrimSpace(stored.ID) == "" || strings.TrimSpace(stored.Backend) == "" || seen[stored.ID] {
				return nil, errors.New("本地 BeeAPI 凭据索引损坏，请重新登录")
			}
			seen[stored.ID] = true
			secret, err := r.store.LoadNamedCredential(stored.Backend, stored.ID)
			if err != nil {
				return nil, fmt.Errorf("读取配置 %q: %w", stored.Name, err)
			}
			credentials = append(credentials, credentialMaterial{
				ID: stored.ID, Name: stored.Name, Prefix: stored.Prefix,
				SourcePrefix: stored.SourcePrefix, Secret: secret,
			})
		}
	}
	if withModels {
		return r.discoverCredentialModels(cfg.Endpoint, credentials)
	}
	return credentials, nil
}

func apiKeysForAssignments(agents []string, credentials []credentialMaterial, assignments map[string]string) (map[string]string, error) {
	keys := map[string]string{}
	for _, agent := range agents {
		credential, ok := credentialForID(credentials, assignments[agent])
		if !ok || strings.TrimSpace(credential.Secret) == "" {
			return nil, fmt.Errorf("%s 没有对应的本地凭据", agentLabel(agent))
		}
		keys[agent] = credential.Secret
	}
	return keys, nil
}

func credentialSummaryName(credentials []state.Credential) string {
	if len(credentials) == 0 {
		return ""
	}
	if len(credentials) == 1 {
		return credentials[0].Name
	}
	return fmt.Sprintf("%d 个密钥配置", len(credentials))
}

func recommendedModel(agent string, models []string) string {
	preferences := map[string][]string{
		"claude":         {"claude-sonnet", "claude", "sonnet", "opus"},
		"claude-desktop": {"claude-sonnet", "claude", "sonnet", "opus"},
		"codex":          {"codex", "gpt-5", "gpt"},
		"gemini":         {"gemini"},
		"grok":           {"grok", "codex", "gpt-5", "claude"},
		"opencode":       {"gpt-5", "codex", "claude", "gemini"},
		"openclaw":       {"gpt-5", "codex", "claude", "gemini"},
		"hermes":         {"hermes", "gpt-5", "codex", "claude", "gemini"},
	}
	for _, preference := range preferences[agent] {
		for _, model := range models {
			if strings.Contains(strings.ToLower(model), preference) {
				return model
			}
		}
	}
	return models[0]
}

func (r *runner) detect() error {
	fmt.Fprintln(r.out, "\n检查本机 AI 工具环境")
	environments, err := detectEnvironments()
	if err != nil {
		return err
	}
	printEnvironments(r.out, environments)
	return nil
}

func (r *runner) configure(args []string) error {
	flags := flag.NewFlagSet("configure", flag.ContinueOnError)
	flags.SetOutput(r.errOut)
	var agentsFlag, modelFlag string
	flags.StringVar(&agentsFlag, "agents", "", "逗号分隔的目标工具")
	flags.StringVar(&modelFlag, "model", "", "对所选工具统一使用此模型")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.Endpoint == "" {
		return errors.New("尚未完成首次设置，请直接运行 beeapi")
	}
	agents := cfg.Agents
	if agentsFlag != "" {
		agents, err = parseAgents(agentsFlag)
		if err != nil {
			return err
		}
	}
	models := cfg.Models
	if models == nil {
		models = map[string]string{}
	}
	if modelFlag != "" {
		for _, agent := range agents {
			models[agent] = modelFlag
		}
	}
	for _, agent := range agents {
		if models[agent] == "" {
			models[agent] = cfg.DefaultModel
		}
	}
	credentials, err := r.loadCredentialMaterials(cfg, false)
	if err != nil {
		return err
	}
	assignments, err := r.selectCredentialAssignments(agents, credentials, cfg.AgentCredentials, true)
	if err != nil {
		return err
	}
	apiKeys, err := apiKeysForAssignments(agents, credentials, assignments)
	if err != nil {
		return err
	}
	result, err := configurator.Apply(r.store, configurator.Options{
		Endpoint: cfg.Endpoint, APIKeys: apiKeys, Models: models, Agents: agents, BinaryPath: cfg.BinaryPath,
	})
	if err != nil {
		return err
	}
	cfg.Agents, cfg.Models, cfg.AgentCredentials = agents, models, assignments
	if err := r.store.SaveConfig(cfg); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "已配置 %s；备份编号 %s\n", strings.Join(agents, "、"), result.BackupID)
	return nil
}

func (r *runner) network(args []string) error {
	command := "status"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "status":
		printEndpoints(r.out, beeapi.DiscoverEndpoints(r.ctx, nil))
		return nil
	case "optimize":
		flags := flag.NewFlagSet("network optimize", flag.ContinueOnError)
		flags.SetOutput(r.errOut)
		var host string
		var apply, yes bool
		flags.StringVar(&host, "host", "beeapi.ai", "要优选的域名")
		flags.BoolVar(&apply, "apply-hosts", false, "把验证通过的 IP 写入 Hosts")
		flags.BoolVar(&yes, "yes", false, "跳过确认")
		if err := flags.Parse(args); err != nil {
			return err
		}
		_, err := r.optimizeAndMaybeApply(host, apply, yes)
		return err
	case "restore":
		flags := flag.NewFlagSet("network restore", flag.ContinueOnError)
		flags.SetOutput(r.errOut)
		var host string
		flags.StringVar(&host, "host", "all", "要恢复的域名或 all")
		if err := flags.Parse(args); err != nil {
			return err
		}
		path := routeopt.HostsPath()
		backup, err := r.store.CreateBackup([]string{path})
		if err != nil {
			return err
		}
		hosts := []string{host}
		if host == "all" {
			hosts = []string{"beeapi.ai", "beeapi.dev"}
		}
		for _, item := range hosts {
			if err := routeopt.RestoreHosts(path, item); err != nil {
				return err
			}
		}
		fmt.Fprintf(r.out, "已移除受管 Hosts 记录；备份编号 %s\n", backup.ID)
		return nil
	default:
		return fmt.Errorf("未知 network 子命令 %q", command)
	}
}

func (r *runner) optimizeAndMaybeApply(host string, forceApply, assumeYes bool) (routeopt.Result, error) {
	binary, version, err := routeopt.EnsureCFST(r.ctx, r.out)
	if err != nil {
		return routeopt.Result{}, err
	}
	fmt.Fprintf(r.out, "  使用 CloudflareSpeedTest %s，目标 %s\n", version, host)
	optCtx, cancel := context.WithTimeout(r.ctx, 12*time.Minute)
	defer cancel()
	result, err := routeopt.Optimize(optCtx, binary, host, r.out)
	if err != nil {
		return result, err
	}
	fmt.Fprintln(r.out)
	fmt.Fprintf(r.out, "  最优 IP %s，BeeAPI API 延迟 %s ms", result.IP, result.LatencyMS)
	if result.SpeedMB != "" {
		fmt.Fprintf(r.out, "，速度 %s MB/s", result.SpeedMB)
	}
	if result.Colo != "" {
		fmt.Fprintf(r.out, "，节点 %s", result.Colo)
	}
	fmt.Fprintln(r.out, "（TLS 与业务接口复验通过）")
	apply := forceApply
	if !forceApply && !assumeYes {
		answer, askErr := r.ask("  写入受管 Hosts 记录？会先备份，可随时恢复 [y/N]: ")
		if askErr != nil {
			return result, askErr
		}
		apply = strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
	}
	if forceApply && !assumeYes {
		answer, askErr := r.ask("  网络不可用，需要写入 Hosts 才能继续；确认并请求管理员权限？[Y/n]: ")
		if askErr != nil {
			return result, askErr
		}
		apply = !strings.EqualFold(strings.TrimSpace(answer), "n")
	}
	if !apply {
		return result, nil
	}
	path := routeopt.HostsPath()
	backup, err := r.store.CreateBackup([]string{path})
	if err != nil {
		return result, fmt.Errorf("备份 Hosts: %w", err)
	}
	validateCtx, validateCancel := context.WithTimeout(r.ctx, 10*time.Second)
	validateErr := routeopt.ValidatePinnedIP(validateCtx, host, result.IP)
	validateCancel()
	if validateErr != nil {
		return result, fmt.Errorf("优选 IP 没有通过 %s 的 TLS 与健康检查: %w", host, validateErr)
	}
	if err := routeopt.ApplyHosts(path, host, result.IP); err != nil {
		_, _ = r.store.Rollback(backup.ID)
		return result, err
	}
	fmt.Fprintf(r.out, "  已为 %s 写入受管 Hosts；备份编号 %s\n", host, backup.ID)
	return result, nil
}

func (r *runner) rollback(args []string) error {
	id := "latest"
	if len(args) > 0 {
		id = args[0]
	}
	manifest, err := r.store.Rollback(id)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "已恢复备份 %s（%d 个文件）\n", manifest.ID, len(manifest.Files))
	return nil
}

func (r *runner) token(args []string) error {
	if len(args) == 0 || args[0] != "print" {
		return errors.New("用法: beeapi token print [--agent 工具 | --credential 凭据ID]")
	}
	flags := flag.NewFlagSet("token print", flag.ContinueOnError)
	flags.SetOutput(r.errOut)
	var agent, credentialID string
	flags.StringVar(&agent, "agent", "", "读取分配给此工具的凭据")
	flags.StringVar(&credentialID, "credential", "", "读取指定凭据")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || (agent != "" && credentialID != "") {
		return errors.New("用法: beeapi token print [--agent 工具 | --credential 凭据ID]")
	}
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	credentials, err := r.loadCredentialMaterials(cfg, false)
	if err != nil {
		return err
	}
	if credentialID == "" && agent != "" {
		credentialID = cfg.AgentCredentials[strings.ToLower(strings.TrimSpace(agent))]
	}
	if credentialID == "" {
		if len(credentials) != 1 {
			return errors.New("本地有多个 BeeAPI 凭据，请使用 --agent 或 --credential 指定")
		}
		credentialID = credentials[0].ID
	}
	credential, ok := credentialForID(credentials, credentialID)
	if !ok {
		return errors.New("没有找到目标工具对应的 BeeAPI 凭据，请重新配置该工具")
	}
	_, err = fmt.Fprintln(r.out, credential.Secret)
	return err
}

func (r *runner) runAgent(args []string) error {
	if len(args) == 0 {
		return errors.New("用法: beeapi run <claude|claude-desktop|codex|gemini|grok|opencode|openclaw|hermes> [参数...]")
	}
	agent := strings.ToLower(args[0])
	if agent == "claude-desktop" {
		cfg, err := r.store.LoadConfig()
		if err != nil {
			return err
		}
		if cfg.Endpoint == "" {
			return errors.New("尚未完成首次设置，请先直接运行 beeapi")
		}
		return openURL("claude://code/new")
	}
	commandName := map[string]string{
		"claude": "claude", "codex": "codex", "gemini": "gemini", "grok": "grok",
		"opencode": "opencode", "openclaw": "openclaw", "hermes": "hermes",
	}[agent]
	if commandName == "" {
		return fmt.Errorf("不支持的工具 %q", agent)
	}
	path, err := exec.LookPath(commandName)
	if err != nil {
		return fmt.Errorf("未找到 %s，请先安装该工具", commandName)
	}
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	credentials, err := r.loadCredentialMaterials(cfg, false)
	if err != nil {
		return err
	}
	credentialID := cfg.AgentCredentials[agent]
	if credentialID == "" && len(credentials) == 1 {
		credentialID = credentials[0].ID
	}
	credential, ok := credentialForID(credentials, credentialID)
	if !ok {
		return fmt.Errorf("%s 尚未分配 BeeAPI 密钥配置，请先运行 beeapi configure", agentLabel(agent))
	}
	commandArgs := append([]string(nil), args[1:]...)
	if agent == "codex" {
		commandArgs = append([]string{"--profile", "beeapi"}, commandArgs...)
	}
	cmd := exec.CommandContext(r.ctx, path, commandArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r.in, r.out, r.errOut
	cmd.Env = append(os.Environ(), agentEnvironment(agent, cfg, credential.Secret)...)
	return cmd.Run()
}

func agentEnvironment(agent string, cfg state.Config, secret string) []string {
	model := cfg.Models[agent]
	switch agent {
	case "claude":
		return []string{"ANTHROPIC_AUTH_TOKEN=" + secret, "ANTHROPIC_BASE_URL=" + cfg.Endpoint + "/anthropic", "ANTHROPIC_MODEL=" + model}
	case "gemini":
		return []string{"GOOGLE_GEMINI_BASE_URL=" + cfg.Endpoint, "GEMINI_API_KEY=" + secret, "GEMINI_MODEL=" + model}
	case "grok":
		home, _ := os.UserHomeDir()
		return []string{
			"GROK_HOME=" + filepath.Join(home, ".config", "getbeeapi", "grok"),
			"BEEAPI_API_KEY=" + secret,
		}
	case "hermes":
		home, _ := os.UserHomeDir()
		return []string{
			"HERMES_HOME=" + filepath.Join(home, ".config", "getbeeapi", "hermes"),
			"OPENAI_API_KEY=" + secret,
			"HERMES_INFERENCE_MODEL=" + model,
		}
	default:
		return nil
	}
}

func (r *runner) ask(prompt string) (string, error) {
	fmt.Fprint(r.out, prompt)
	line, err := r.reader.ReadString('\n')
	if errors.Is(err, io.EOF) && line != "" {
		return strings.TrimRight(line, "\r\n"), nil
	}
	return strings.TrimRight(line, "\r\n"), err
}

func (r *runner) readSecret(prompt string) (string, error) {
	fmt.Fprint(r.out, prompt)
	file, ok := r.in.(*os.File)
	if !ok {
		return r.ask("")
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return r.ask("")
	}
	if runtime.GOOS == "windows" {
		powerShell, lookupErr := exec.LookPath("powershell.exe")
		if lookupErr != nil {
			return r.ask("")
		}
		script := `$s=Read-Host -AsSecureString; $b=[Runtime.InteropServices.Marshal]::SecureStringToBSTR($s); try {[Runtime.InteropServices.Marshal]::PtrToStringBSTR($b)} finally {[Runtime.InteropServices.Marshal]::ZeroFreeBSTR($b)}`
		cmd := exec.Command(powerShell, "-NoProfile", "-Command", script)
		cmd.Stdin, cmd.Stderr = file, r.errOut
		out, runErr := cmd.Output()
		fmt.Fprintln(r.out)
		return strings.TrimSpace(string(out)), runErr
	}
	stty, lookupErr := exec.LookPath("stty")
	if lookupErr != nil {
		return r.ask("")
	}
	disable := exec.Command(stty, "-echo")
	disable.Stdin = file
	if err := disable.Run(); err != nil {
		return r.ask("")
	}
	defer func() {
		restore := exec.Command(stty, "echo")
		restore.Stdin = file
		_ = restore.Run()
		fmt.Fprintln(r.out)
	}()
	line, err := r.reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

func openURL(rawURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", rawURL).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start()
	default:
		return exec.Command("xdg-open", rawURL).Start()
	}
}

func hostFromURL(rawURL string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	return strings.Split(trimmed, "/")[0]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
