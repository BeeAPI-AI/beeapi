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
	ctx     context.Context
	version string
	in      io.Reader
	reader  *bufio.Reader
	out     io.Writer
	errOut  io.Writer
	store   *state.Store
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
		return r.setup(nil)
	}
	switch args[0] {
	case "setup", "init":
		return r.setup(args[1:])
	case "detect":
		return r.detect()
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
	fmt.Fprintln(out, `beeapi — BeeAPI 网络修复与多智能体配置工具

用法:
  beeapi                         启动完整向导
  beeapi setup                   重新运行完整向导
  beeapi detect                  检查本机已安装的 AI CLI
  beeapi configure               使用已保存凭据重新配置
  beeapi network status          检测内置 API 域名
  beeapi network optimize        使用 CloudflareSpeedTest 优选 IP
  beeapi network restore         移除 beeapi 管理的 Hosts 记录
  beeapi rollback [latest|编号]  恢复配置备份
  beeapi run <工具> [参数...]    用 BeeAPI 配置启动目标 CLI
  beeapi token print             仅向标准输出打印已保存的 API Key

支持: Claude Code、Codex、Gemini CLI、OpenCode、OpenClaw`)
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

	fmt.Fprintln(r.out, "\nBeeAPI 本地配置向导")
	fmt.Fprintln(r.out, "────────────────────────────────────────")
	endpoint, err := r.resolveEndpoint(endpointFlag, assumeYes)
	if err != nil {
		return err
	}

	apiKey := strings.TrimSpace(apiKeyFlag)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("BEEAPI_API_KEY"))
	}
	keyName := ""
	if apiKey == "" {
		apiKey, keyName, err = r.authorize(endpoint, noOpen)
		if err != nil {
			return err
		}
	}
	environments, err := detectEnvironments()
	if err != nil {
		return err
	}
	printEnvironments(r.out, environments)
	agents, err := r.selectAgents(environments, agentsFlag, assumeYes)
	if err != nil {
		return err
	}

	client := beeapi.New(endpoint)
	modelCtx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	models, err := client.Models(modelCtx, apiKey)
	cancel()
	if err != nil {
		return fmt.Errorf("API Key 校验或模型发现失败: %w", err)
	}
	if len(models) == 0 {
		return errors.New("该 API Key 当前没有可用模型")
	}
	selectedModels, err := r.selectModels(agents, models, assumeYes)
	if err != nil {
		return err
	}
	backend, err := r.store.SaveCredential(apiKey)
	if err != nil {
		return err
	}
	binaryPath, _ := os.Executable()
	result, err := configurator.Apply(r.store, configurator.Options{
		Endpoint: endpoint, APIKey: apiKey, Models: selectedModels,
		Agents: agents, BinaryPath: binaryPath,
	})
	if err != nil {
		return err
	}
	cfg := state.Config{
		Endpoint: endpoint, KeyName: keyName, Models: selectedModels, Agents: agents,
		BinaryPath: binaryPath, CredentialBackend: backend,
	}
	if model := selectedModels["codex"]; model != "" {
		cfg.DefaultModel = model
	} else if len(agents) > 0 {
		cfg.DefaultModel = selectedModels[agents[0]]
	}
	if err := r.store.SaveConfig(cfg); err != nil {
		return err
	}

	fmt.Fprintln(r.out, "\n配置完成")
	fmt.Fprintf(r.out, "  API 入口  %s\n", endpoint)
	fmt.Fprintf(r.out, "  已配置    %s\n", strings.Join(agents, "、"))
	fmt.Fprintf(r.out, "  备份编号  %s\n", result.BackupID)
	for _, hint := range result.Hints {
		fmt.Fprintln(r.out, "  "+hint)
	}
	fmt.Fprintln(r.out, "\n以后可运行 beeapi 重新检测，或运行 beeapi rollback latest 回滚。")
	return nil
}

func (r *runner) resolveEndpoint(explicit string, assumeYes bool) (string, error) {
	fmt.Fprintln(r.out, "\n[1/4] 检测 BeeAPI 网络入口")
	var endpoints []beeapi.Endpoint
	if explicit != "" {
		endpoints = beeapi.ProbeEndpoints(r.ctx, []beeapi.Endpoint{{Name: "指定入口", BaseURL: strings.TrimRight(explicit, "/")}})
	} else {
		endpoints = beeapi.DiscoverEndpoints(r.ctx, nil)
	}
	printEndpoints(r.out, endpoints)
	best, err := beeapi.BestEndpoint(endpoints)
	if err != nil {
		fmt.Fprintln(r.out, "  两个内置域名均不可用，开始 Cloudflare IP 优选与 TLS 校验。")
		if _, recoverErr := r.optimizeAndMaybeApply("beeapi.ai", true, assumeYes); recoverErr != nil {
			return "", fmt.Errorf("自动修复网络失败: %w", recoverErr)
		}
		endpoints = beeapi.DiscoverEndpoints(r.ctx, nil)
		printEndpoints(r.out, endpoints)
		best, err = beeapi.BestEndpoint(endpoints)
		if err != nil {
			return "", errors.New("Hosts 修复后仍无法访问 BeeAPI，请运行 beeapi network restore 后检查代理、防火墙或运营商网络")
		}
		return best.BaseURL, nil
	}
	if !assumeYes && explicit == "" {
		answer, readErr := r.ask("  回车使用最快可用域名；输入 o 继续优选 Cloudflare IP: ")
		if readErr != nil {
			return "", readErr
		}
		if strings.EqualFold(strings.TrimSpace(answer), "o") {
			if _, optErr := r.optimizeAndMaybeApply(hostFromURL(best.BaseURL), false, false); optErr != nil {
				return "", optErr
			}
			endpoints = beeapi.DiscoverEndpoints(r.ctx, nil)
			if optimized, bestErr := beeapi.BestEndpoint(endpoints); bestErr == nil {
				best = optimized
			}
		}
	}
	fmt.Fprintf(r.out, "  已选择 %s（%s）\n", best.BaseURL, durationLabel(best.Latency))
	return best.BaseURL, nil
}

func printEndpoints(out io.Writer, endpoints []beeapi.Endpoint) {
	for _, endpoint := range endpoints {
		if endpoint.Reachable {
			fmt.Fprintf(out, "  ✓ %-12s %-24s %s\n", endpoint.Name, endpoint.BaseURL, durationLabel(endpoint.Latency))
		} else {
			fmt.Fprintf(out, "  × %-12s %-24s 不可用\n", endpoint.Name, endpoint.BaseURL)
		}
	}
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
		{Agent: "codex", Label: "Codex", Executable: "codex", Config: filepath.Join(home, ".codex", "config.toml")},
		{Agent: "gemini", Label: "Gemini CLI", Executable: "gemini", Config: filepath.Join(home, ".gemini", "settings.json")},
		{Agent: "opencode", Label: "OpenCode", Executable: "opencode", Config: filepath.Join(home, ".config", "opencode", "opencode.json")},
		{Agent: "openclaw", Label: "OpenClaw", Executable: "openclaw", Config: filepath.Join(home, ".openclaw", "openclaw.json")},
	}
	for index := range definitions {
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

func printEnvironments(out io.Writer, environments []environment) {
	fmt.Fprintln(out, "\n[3/4] 检查本地 AI CLI 环境")
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
	aliases := map[string]string{"claude-code": "claude", "gemini-cli": "gemini", "open-code": "opencode", "open-claw": "openclaw"}
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

func (r *runner) authorize(endpoint string, noOpen bool) (string, string, error) {
	fmt.Fprintln(r.out, "\n[2/4] 登录 BeeAPI")
	fmt.Fprintln(r.out, "  1. 跳转网站授权登录（推荐；支持现有登录、OAuth 与 2FA）")
	fmt.Fprintln(r.out, "  2. 直接粘贴 API Key（兼容回退）")
	choice, err := r.ask("  请选择 [1]: ")
	if err != nil && !errors.Is(err, io.EOF) {
		return "", "", err
	}
	choice = strings.TrimSpace(choice)
	if choice == "2" {
		return r.pasteAPIKey(endpoint)
	}
	if choice != "" && choice != "1" {
		return "", "", errors.New("登录方式只能选择 1 或 2")
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
		key, keyName, pollErr := r.pollDevice(client, code)
		if pollErr == nil {
			return key, keyName, nil
		}
		return "", "", pollErr
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
		return "", "", askErr
	}
	if strings.EqualFold(strings.TrimSpace(fallback), "n") {
		return "", "", errors.New("网站授权未完成")
	}
	return r.pasteAPIKey(endpoint)
}

func (r *runner) pasteAPIKey(endpoint string) (string, string, error) {
	fmt.Fprintln(r.out, "  兼容模式：请从 BeeAPI 控制台复制 API Key。")
	fmt.Fprintf(r.out, "  控制台: %s/api-keys\n", endpoint)
	secret, readErr := r.readSecret("  粘贴 API Key: ")
	return strings.TrimSpace(secret), "manual", readErr
}

func (r *runner) pollDevice(client *beeapi.Client, code beeapi.DeviceCode) (string, string, error) {
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
			return "", "", r.ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return "", "", errors.New("设备授权已过期，请重新运行 beeapi")
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
				return "", "", err
			}
			if token.Pending {
				continue
			}
			if token.Error != "" {
				return "", "", errors.New(token.Error)
			}
			accessToken := token.AccessToken
			if accessToken == "" {
				accessToken = token.Token
			}
			if accessToken == "" {
				return "", "", errors.New("设备授权完成，但 BeeAPI 没有返回 CLI 登录令牌")
			}
			client.Token = accessToken
			key, name, chooseErr := r.chooseAccountAPIKey(client)
			client.Token = ""
			return key, name, chooseErr
		}
	}
}

func (r *runner) chooseAccountAPIKey(client *beeapi.Client) (string, string, error) {
	ctx, cancel := context.WithTimeout(r.ctx, 12*time.Second)
	keys, err := client.CLIAPIKeys(ctx)
	cancel()
	if err != nil {
		return "", "", fmt.Errorf("读取 BeeAPI API Key 列表: %w", err)
	}
	if len(keys) == 0 {
		return "", "", errors.New("BeeAPI 账户中没有可导出的 API Key，请先在控制台创建")
	}
	fmt.Fprintln(r.out, "  网站授权成功。请选择这次用于本地工具的 BeeAPI API Key:")
	selectable := 0
	defaultSelection := -1
	for index, key := range keys {
		prefix := key.KeyPrefix
		if prefix == "" {
			prefix = key.Prefix
		}
		parts := []string{}
		for _, part := range []string{prefix, key.GroupName, key.Status} {
			if strings.TrimSpace(part) != "" {
				parts = append(parts, strings.TrimSpace(part))
			}
		}
		status := ""
		if !key.Exportable {
			status = " [不可导出]"
		} else {
			selectable++
			if defaultSelection == -1 {
				defaultSelection = index
			}
		}
		fmt.Fprintf(r.out, "    %d. %s%s", index+1, key.Name, status)
		if len(parts) > 0 {
			fmt.Fprintf(r.out, " (%s)", strings.Join(parts, " · "))
		}
		fmt.Fprintln(r.out)
	}
	if selectable == 0 {
		return "", "", errors.New("账户中的 API Key 都不可导出；请改用手动粘贴或在控制台创建可导出的 Key")
	}
	answer, readErr := r.ask(fmt.Sprintf("  Key 编号 [%d]: ", defaultSelection+1))
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", "", readErr
	}
	selected := defaultSelection
	if strings.TrimSpace(answer) != "" {
		number, convErr := strconv.Atoi(strings.TrimSpace(answer))
		if convErr != nil || number < 1 || number > len(keys) {
			return "", "", errors.New("API Key 编号无效")
		}
		selected = number - 1
	}
	if !keys[selected].Exportable {
		return "", "", errors.New("所选 API Key 不允许导出，请选择其他 Key 或使用手动粘贴")
	}
	exportCtx, exportCancel := context.WithTimeout(r.ctx, 12*time.Second)
	secret, err := client.ExportCLIAPIKey(exportCtx, keys[selected].ID)
	exportCancel()
	if err != nil {
		return "", "", fmt.Errorf("领取所选 API Key: %w", err)
	}
	return secret, keys[selected].Name, nil
}

func (r *runner) selectModels(agents, models []string, assumeYes bool) (map[string]string, error) {
	selected := map[string]string{}
	for _, agent := range agents {
		selected[agent] = recommendedModel(agent, models)
	}
	fmt.Fprintln(r.out, "\n[4/4] 匹配模型并写入配置")
	for _, agent := range agents {
		fmt.Fprintf(r.out, "  %-10s %s\n", agent, selected[agent])
	}
	if assumeYes {
		return selected, nil
	}
	answer, err := r.ask("  使用以上推荐模型？[Y/n]: ")
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(answer), "n") {
		fmt.Fprintf(r.out, "  可用模型示例: %s\n", strings.Join(models[:min(len(models), 12)], ", "))
		for _, agent := range agents {
			value, readErr := r.ask(fmt.Sprintf("  %s 模型 [%s]: ", agent, selected[agent]))
			if readErr != nil {
				return nil, readErr
			}
			if strings.TrimSpace(value) != "" {
				selected[agent] = strings.TrimSpace(value)
			}
		}
	}
	return selected, nil
}

func recommendedModel(agent string, models []string) string {
	preferences := map[string][]string{
		"claude":   {"claude-sonnet", "claude", "sonnet", "opus"},
		"codex":    {"codex", "gpt-5", "gpt"},
		"gemini":   {"gemini"},
		"opencode": {"gpt-5", "codex", "claude", "gemini"},
		"openclaw": {"gpt-5", "codex", "claude", "gemini"},
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
	key, err := r.store.LoadCredential(cfg.CredentialBackend)
	if err != nil {
		return err
	}
	result, err := configurator.Apply(r.store, configurator.Options{
		Endpoint: cfg.Endpoint, APIKey: key, Models: models, Agents: agents, BinaryPath: cfg.BinaryPath,
	})
	if err != nil {
		return err
	}
	cfg.Agents, cfg.Models = agents, models
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
	fmt.Fprintf(r.out, "  最优 IP %s，延迟 %s ms，速度 %s MB/s，节点 %s\n", result.IP, result.LatencyMS, result.SpeedMB, result.Colo)
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
	validated := []string{}
	for _, candidate := range []string{"beeapi.ai", "beeapi.dev"} {
		validateCtx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
		validateErr := routeopt.ValidatePinnedIP(validateCtx, candidate, result.IP)
		cancel()
		if validateErr == nil {
			validated = append(validated, candidate)
		}
	}
	if len(validated) == 0 {
		return result, errors.New("优选 IP 没有通过任一 BeeAPI 域名的 TLS 与业务接口校验")
	}
	for _, candidate := range validated {
		if err := routeopt.ApplyHosts(path, candidate, result.IP); err != nil {
			_, _ = r.store.Rollback(backup.ID)
			return result, err
		}
	}
	fmt.Fprintf(r.out, "  已为 %s 写入受管 Hosts；备份编号 %s\n", strings.Join(validated, "、"), backup.ID)
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
	if len(args) != 1 || args[0] != "print" {
		return errors.New("用法: beeapi token print")
	}
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	secret, err := r.store.LoadCredential(cfg.CredentialBackend)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(r.out, secret)
	return err
}

func (r *runner) runAgent(args []string) error {
	if len(args) == 0 {
		return errors.New("用法: beeapi run <claude|codex|gemini|opencode|openclaw> [参数...]")
	}
	agent := strings.ToLower(args[0])
	commandName := map[string]string{"claude": "claude", "codex": "codex", "gemini": "gemini", "opencode": "opencode", "openclaw": "openclaw"}[agent]
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
	secret, err := r.store.LoadCredential(cfg.CredentialBackend)
	if err != nil {
		return err
	}
	commandArgs := append([]string(nil), args[1:]...)
	if agent == "codex" {
		commandArgs = append([]string{"--profile", "beeapi"}, commandArgs...)
	}
	cmd := exec.CommandContext(r.ctx, path, commandArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r.in, r.out, r.errOut
	cmd.Env = append(os.Environ(), agentEnvironment(agent, cfg, secret)...)
	return cmd.Run()
}

func agentEnvironment(agent string, cfg state.Config, secret string) []string {
	model := cfg.Models[agent]
	switch agent {
	case "claude":
		return []string{"ANTHROPIC_AUTH_TOKEN=" + secret, "ANTHROPIC_BASE_URL=" + cfg.Endpoint + "/anthropic", "ANTHROPIC_MODEL=" + model}
	case "gemini":
		return []string{"GOOGLE_GEMINI_BASE_URL=" + cfg.Endpoint, "GEMINI_API_KEY=" + secret, "GEMINI_MODEL=" + model}
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
