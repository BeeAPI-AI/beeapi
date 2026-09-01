package app

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
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
	"github.com/BeeAPI-AI/beeapi/internal/updater"
)

type runner struct {
	ctx            context.Context
	version        string
	language       string
	in             io.Reader
	reader         *bufio.Reader
	out            io.Writer
	errOut         io.Writer
	store          *state.Store
	logoShown      bool
	optimize       func(string, bool, bool) (routeopt.Result, error)
	openBrowser    func(string) error
	usageLookup    usageLookupFunc
	usageCache     usageCacheStore
	updateClient   *updater.Client
	executablePath func() (string, error)
}

type credentialMaterial struct {
	ID                        string
	Name                      string
	Prefix                    string
	SourcePrefix              string
	Secret                    string
	Models                    []string
	ModelOptions              []beeapi.ModelOption
	ModelOptionsAuthoritative bool
}

type authorizationResult struct {
	Credentials []credentialMaterial
	Stored      []state.Credential
}

type credentialModelDiscovery struct {
	Models        []string
	Options       []beeapi.ModelOption
	Authoritative bool
}

func Run(ctx context.Context, args []string, version string, in io.Reader, out, errOut io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			fmt.Fprintln(out, version)
			return nil
		}
	}
	store, err := state.Open()
	if err != nil {
		return err
	}
	r := &runner{ctx: ctx, version: version, in: in, reader: bufio.NewReader(in), out: out, errOut: errOut, store: store}
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	promptLanguage := len(args) == 0 || (len(args) > 0 && (args[0] == "setup" || args[0] == "init") &&
		!containsArgument(args[1:], "--yes") && !containsArgument(args[1:], "--help") && !containsArgument(args[1:], "-h"))
	if err := r.initializeLanguage(&cfg, promptLanguage); err != nil {
		return err
	}
	if len(args) > 0 {
		switch args[0] {
		case "help", "--help", "-h":
			printHelp(out, r.language)
			return nil
		}
	}
	if len(args) == 0 {
		r.notifyUpdateIfAvailable()
		if !cfg.Initialized() {
			return r.setupWithRecovery(nil)
		}
		return r.home()
	}
	switch args[0] {
	case "setup", "init":
		return r.setupWithRecovery(args[1:])
	case "detect":
		return r.detect()
	case "status":
		return r.status()
	case "login", "connect":
		return r.reconnectWithRecovery()
	case "logout", "disconnect":
		return r.disconnectOAuthAccount()
	case "configure", "config":
		return r.configure(args[1:])
	case "network":
		return r.network(args[1:])
	case "rollback":
		return r.rollback(args[1:])
	case "token":
		return r.token(args[1:])
	case "update", "upgrade":
		return r.updateCLI(args[1:])
	case "run":
		return r.runAgent(args[1:])
	case "language", "lang":
		if len(args) > 1 {
			language := normalizeLanguage(args[1])
			if language == "" {
				return errors.New(r.text("语言只能是 zh-CN 或 en", "Language must be zh-CN or en"))
			}
			cfg.Language = language
			if err := store.SaveConfig(cfg); err != nil {
				return err
			}
			r.language = language
			r.line(r.out, "✓ 已切换为简体中文。", "✓ Language changed to English.")
			return nil
		}
		return r.languageMenu()
	default:
		return fmt.Errorf(r.text("未知命令 %q；运行 beeapi help 查看帮助", "Unknown command %q; run beeapi help for usage"), args[0])
	}
}

func containsArgument(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func printHelp(out io.Writer, language string) {
	if normalizeLanguage(language) == languageEnglish {
		fmt.Fprintln(out, `beeapi — quickly configure BeeAPI for your existing AI tools

Usage:
  beeapi                         First run: set up; later: open the main menu
  beeapi setup                   Run first-time setup again
  beeapi status                  Show the current connection and tool configuration
  beeapi login                   Authorize again or update API Key configurations
  beeapi logout                  Revoke the OAuth account connection; keep saved API Keys
  beeapi detect                  Detect installed AI CLI tools
  beeapi configure               Reconfigure using saved credentials
  beeapi network status          Check the built-in API domains
  beeapi network optimize        Select an optimized IP with CloudflareSpeedTest
  beeapi network restore         Remove Hosts entries managed by beeapi
  beeapi rollback [latest|ID]    Restore a configuration backup
  beeapi language [zh-CN|en]     Change the interface language
  beeapi update                  Check for and install a verified CLI update
  beeapi run <tool> [args...]    Compatibility/troubleshooting launcher
  beeapi token print [--agent tool] Print the saved API Key for the target tool only

Supported: Claude Code, Claude Desktop (Code), Codex, Gemini CLI, Grok Build, OpenCode, OpenClaw, Hermes`)
		return
	}
	fmt.Fprintln(out, `beeapi — 为现有 AI 工具快速配置 BeeAPI

用法:
  beeapi                         首次运行初始化；以后打开功能主页
  beeapi setup                   重新运行首次设置
  beeapi status                  查看当前连接与工具配置
  beeapi login                   重新授权或更新密钥配置
  beeapi logout                  撤销 OAuth 账户连接；保留已保存 API Key
  beeapi detect                  检查本机已安装的 AI CLI
  beeapi configure               使用已保存凭据重新配置
  beeapi network status          检测内置 API 域名
  beeapi network optimize        使用 CloudflareSpeedTest 优选 IP
  beeapi network restore         移除 beeapi 管理的 Hosts 记录
  beeapi rollback [latest|编号]  恢复配置备份
  beeapi language [zh-CN|en]     切换界面语言
  beeapi update                  检查并安装经过校验的 CLI 更新
  beeapi run <工具> [参数...]    兼容/排障方式启动目标工具
  beeapi token print [--agent 工具] 仅向目标工具提供已保存的 API Key

支持: Claude Code、Claude Desktop（Code）、Codex、Gemini CLI、Grok Build、OpenCode、OpenClaw、Hermes`)
}

func (r *runner) setup(args []string) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(r.errOut)
	var endpointFlag, apiKeyFlag, agentsFlag string
	var assumeYes, noOpen bool
	flags.StringVar(&endpointFlag, "endpoint", "", r.text("指定 BeeAPI 入口", "BeeAPI endpoint"))
	flags.StringVar(&apiKeyFlag, "api-key", "", r.text("直接提供 API Key（也可使用 BEEAPI_API_KEY）", "provide an API Key directly (or use BEEAPI_API_KEY)"))
	flags.StringVar(&agentsFlag, "agents", "", r.text("逗号分隔的目标工具", "comma-separated target tools"))
	flags.BoolVar(&assumeYes, "yes", false, r.text("接受安全默认值", "accept safe defaults"))
	flags.BoolVar(&noOpen, "no-open", false, r.text("不自动打开授权网页", "do not open the authorization page automatically"))
	if err := flags.Parse(args); err != nil {
		return err
	}

	r.showLogo()
	r.line(r.out, "\n首次设置 · 连接 BeeAPI 并配置现有 AI 工具", "\nFirst-time setup · Connect BeeAPI and configure your AI tools")
	fmt.Fprintln(r.out, "────────────────────────────────────────")
	apiKey := strings.TrimSpace(apiKeyFlag)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("BEEAPI_API_KEY"))
	}
	pending, resumePending, err := r.pendingSetupForMode(pendingModeSetup, assumeYes)
	if err != nil {
		return err
	}
	if apiKey != "" {
		resumePending = false
	}
	if resumePending && strings.TrimSpace(endpointFlag) == "" {
		endpointFlag = pending.Endpoint
	}
	endpoint, err := r.resolveEndpoint(endpointFlag, assumeYes)
	if err != nil {
		return err
	}

	r.line(r.out, "\n[2/3] 连接 BeeAPI 并读取可用配置", "\n[2/3] Connect BeeAPI and load available configurations")
	var credentials []credentialMaterial
	var storedCredentials []state.Credential
	if resumePending {
		credentials, err = r.restorePendingCredentialMaterials(pending)
		if err != nil {
			return err
		}
		storedCredentials = pending.Credentials
		r.format(r.out, "  ✓ 已恢复上次授权保存的 %d 个 API Key。\n", "  ✓ Restored %d API Key(s) saved by the previous authorization.\n", len(credentials))
		r.updatePendingSetup(pendingModeSetup, endpoint, storedCredentials, nil)
	} else if apiKey == "" {
		var authorized authorizationResult
		authorized, err = r.authorize(endpoint, noOpen, pendingModeSetup)
		if err != nil {
			return err
		}
		credentials = authorized.Credentials
		storedCredentials = authorized.Stored
	} else {
		r.line(r.out, "  使用命令行或环境变量提供的单个 API Key（兼容模式）。", "  Using one API Key supplied by a flag or environment variable (compatibility mode).")
		credentials = []credentialMaterial{{ID: "manual", Name: r.text("手动 API Key", "Manual API Key"), Prefix: safeKeyPrefix(apiKey), Secret: apiKey}}
		r.clearOAuthAccountForCompatibility()
	}
	if !resumePending && len(storedCredentials) == 0 {
		storedCredentials, err = r.checkpointCredentialMaterials(pendingModeSetup, endpoint, credentials)
		if err != nil {
			return err
		}
	}
	credentials, err = r.discoverCredentialModels(endpoint, credentials)
	if err != nil {
		r.updatePendingSetup(pendingModeSetup, endpoint, storedCredentials, err)
		return err
	}
	r.format(r.out, "  ✓ 已连接 BeeAPI · 已读取 %d 个可用 API Key\n", "  ✓ Connected to BeeAPI · Loaded %d usable API Key(s)\n", len(credentials))

	r.line(r.out, "\n[3/3] 选择工具、密钥配置与模型", "\n[3/3] Choose tools, API Keys, and models")
	environments, err := detectEnvironments()
	if err != nil {
		r.updatePendingSetup(pendingModeSetup, endpoint, storedCredentials, err)
		return err
	}
	r.printEnvironments(environments)
	agents, err := r.selectAgents(environments, agentsFlag, assumeYes)
	if err != nil {
		r.updatePendingSetup(pendingModeSetup, endpoint, storedCredentials, err)
		return err
	}
	assignments, err := r.selectCredentialAssignments(agents, credentials, nil, assumeYes)
	if err != nil {
		r.updatePendingSetup(pendingModeSetup, endpoint, storedCredentials, err)
		return err
	}
	selectedModels, err := r.selectModelsForAssignments(agents, credentials, assignments, assumeYes)
	if err != nil {
		r.updatePendingSetup(pendingModeSetup, endpoint, storedCredentials, err)
		return err
	}
	defaultName := defaultProfileNameForLanguage(r.language)
	profileName := defaultName
	if !assumeYes {
		for {
			answer, askErr := r.ask(fmt.Sprintf(r.text("  配置方案名称 [%s]: ", "  Profile name [%s]: "), defaultName))
			if askErr != nil && !errors.Is(askErr, io.EOF) {
				return askErr
			}
			if strings.TrimSpace(answer) == "" {
				break
			}
			candidate, nameErr := validateProfileName(answer)
			if nameErr == nil {
				profileName = candidate
				break
			}
			fmt.Fprintln(r.errOut, "  "+r.localizedErrorMessage(nameErr))
		}
	}
	apiKeys, err := apiKeysForAssignments(agents, credentials, assignments)
	if err != nil {
		r.updatePendingSetup(pendingModeSetup, endpoint, storedCredentials, err)
		return err
	}
	binaryPath, _ := os.Executable()
	result, err := configurator.Apply(r.store, configurator.Options{
		Endpoint: endpoint, APIKeys: apiKeys, Models: selectedModels,
		Agents: agents, BinaryPath: binaryPath,
	})
	if err != nil {
		r.updatePendingSetup(pendingModeSetup, endpoint, storedCredentials, err)
		return err
	}
	cfg := state.Config{
		Language: r.language, Endpoint: endpoint, KeyName: credentialSummaryName(storedCredentials, r.language), Models: selectedModels, Agents: agents,
		Credentials: storedCredentials, AgentCredentials: assignments, BinaryPath: binaryPath,
	}
	setDefaultModel(&cfg, agents, selectedModels)
	now := time.Now().UTC()
	cfg.AgentEndpoints = make(map[string]string, len(agents))
	cfg.ActiveProfiles = make(map[string]string, len(agents))
	for _, agent := range agents {
		cfg.AgentEndpoints[agent] = endpoint
		cfg.ActiveProfiles[agent] = "default"
	}
	cfg.Profiles = []state.Profile{profileFromCurrent(cfg, "default", profileName, now)}
	cfg.ActiveProfile = "default"
	if err := r.store.SaveConfig(cfg); err != nil {
		_, _ = r.store.Rollback(result.BackupID)
		r.updatePendingSetup(pendingModeSetup, endpoint, storedCredentials, err)
		return err
	}
	if err := r.store.ClearPendingSetup(); err != nil {
		r.format(r.errOut, "  警告：清理设置续接点失败：%v\n", "  Warning: could not clear the setup checkpoint: %v\n", err)
	}

	r.line(r.out, "\n配置完成", "\nSetup complete")
	r.format(r.out, "  API 入口  %s\n", "  API endpoint  %s\n", endpoint)
	r.format(r.out, "  已配置    %s\n", "  Configured    %s\n", friendlyAgentList(agents))
	r.format(r.out, "  备份编号  %s\n", "  Backup ID     %s\n", result.BackupID)
	for _, hint := range result.Hints {
		fmt.Fprintln(r.out, "  "+r.localizedHint(hint))
	}
	r.line(r.out, "\n以后直接输入 beeapi 打开功能主页；不会再次自动运行首次设置。", "\nFrom now on, run beeapi to open the main menu; first-time setup will not run again automatically.")
	return nil
}

func (r *runner) resolveEndpoint(explicit string, assumeYes bool) (string, error) {
	r.line(r.out, "\n[1/3] 检测 BeeAPI 官方入口", "\n[1/3] Check official BeeAPI endpoints")
	var endpoints []beeapi.Endpoint
	if explicit != "" {
		normalized := beeapi.NormalizeBaseURL(explicit)
		if normalized == "" {
			return "", errors.New(r.text("指定入口必须是有效的 HTTPS 根地址", "The endpoint must be a valid HTTPS root URL"))
		}
		endpoints = beeapi.ProbeEndpoints(r.ctx, []beeapi.Endpoint{{Name: "指定入口", BaseURL: normalized}})
	} else {
		endpoints = beeapi.DiscoverEndpoints(r.ctx, nil)
	}
	r.printEndpoints(endpoints)
	best, err := beeapi.BestEndpoint(endpoints)
	if err != nil {
		if len(endpoints) == 0 {
			return "", err
		}
		target := endpoints[0]
		if !assumeYes && explicit == "" && len(endpoints) > 1 {
			answer, readErr := r.askLocalized("  所有入口均不可用；选择要尝试修复的域名 [1]: ", "  No endpoint is reachable; choose a domain to repair [1]: ")
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return "", readErr
			}
			if index, ok := endpointChoice(answer, len(endpoints)); ok {
				target = endpoints[index]
			} else if strings.TrimSpace(answer) != "" {
				return "", errors.New(r.text("入口编号无效", "Invalid endpoint number"))
			}
		}
		r.format(r.out, "  %s 当前不可用，开始 Cloudflare IP 优选与 TLS 校验。\n", "  %s is unreachable; starting Cloudflare IP selection and TLS validation.\n", target.BaseURL)
		if _, recoverErr := r.runRouteOptimization(hostFromURL(target.BaseURL), true, assumeYes); recoverErr != nil {
			return "", fmt.Errorf(r.text("自动修复网络失败: %w", "Automatic network repair failed: %w"), recoverErr)
		}
		rechecked := beeapi.ProbeEndpoints(r.ctx, []beeapi.Endpoint{{Name: target.Name, BaseURL: target.BaseURL}})
		if len(rechecked) != 1 || !rechecked[0].Reachable {
			return "", errors.New(r.text("Hosts 修复后仍无法访问 BeeAPI，请运行 beeapi network restore 后检查代理、防火墙或运营商网络", "BeeAPI is still unreachable after the Hosts repair; run beeapi network restore, then check your proxy, firewall, or network provider"))
		}
		r.format(r.out, "  已选择 %s（%s）\n", "  Selected %s (%s)\n", rechecked[0].BaseURL, durationLabel(rechecked[0].Latency))
		return rechecked[0].BaseURL, nil
	}
	if !assumeYes && explicit == "" {
		answer, readErr := r.askLocalized("  回车使用最快可用入口；也可输入编号选择其他入口: ", "  Press Enter for the fastest reachable endpoint, or enter another number: ")
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", readErr
		}
		if index, ok := endpointChoice(answer, len(endpoints)); ok {
			selected := endpoints[index]
			if !selected.Reachable {
				confirm, confirmErr := r.ask(fmt.Sprintf(r.text("  %s 不可访问，尝试优选 IP 并写入 Hosts？[Y/n]: ", "  %s is unreachable. Select an optimized IP and update Hosts? [Y/n]: "), selected.BaseURL))
				if confirmErr != nil && !errors.Is(confirmErr, io.EOF) {
					return "", confirmErr
				}
				if strings.EqualFold(strings.TrimSpace(confirm), "n") {
					r.format(r.out, "  已改用当前可访问入口 %s（%s），继续设置。\n", "  Switched to reachable endpoint %s (%s); continuing setup.\n", best.BaseURL, durationLabel(best.Latency))
					selected = best
				} else {
					// The user already approved the Hosts change in the prompt above.
					if _, optErr := r.runRouteOptimization(hostFromURL(selected.BaseURL), true, true); optErr != nil {
						r.format(r.errOut, "  优选未完成：%v\n", "  IP selection did not complete: %v\n", optErr)
						r.format(r.out, "  已自动回退到可访问入口 %s（%s），继续设置。\n", "  Automatically fell back to reachable endpoint %s (%s); continuing setup.\n", best.BaseURL, durationLabel(best.Latency))
						selected = best
					} else {
						rechecked := beeapi.ProbeEndpoints(r.ctx, []beeapi.Endpoint{selected})
						if len(rechecked) != 1 || !rechecked[0].Reachable {
							r.line(r.errOut, "  优选后的入口复验失败，未将其用于后续配置。", "  The optimized endpoint failed validation and will not be used.")
							r.format(r.out, "  已自动回退到可访问入口 %s（%s），继续设置。\n", "  Automatically fell back to reachable endpoint %s (%s); continuing setup.\n", best.BaseURL, durationLabel(best.Latency))
							selected = best
						} else {
							selected = rechecked[0]
						}
					}
				}
			}
			best = selected
		} else if strings.TrimSpace(answer) != "" {
			return "", errors.New(r.text("入口编号无效", "Invalid endpoint number"))
		}
	}
	r.format(r.out, "  已选择 %s（%s）\n", "  Selected %s (%s)\n", best.BaseURL, durationLabel(best.Latency))
	return best.BaseURL, nil
}

func (r *runner) runRouteOptimization(host string, forceApply, assumeYes bool) (routeopt.Result, error) {
	if r.optimize != nil {
		return r.optimize(host, forceApply, assumeYes)
	}
	return r.optimizeAndMaybeApply(host, forceApply, assumeYes)
}

func (r *runner) printEndpoints(endpoints []beeapi.Endpoint) {
	for index, endpoint := range endpoints {
		name := localizedEndpointName(endpoint.Name, r.language)
		if endpoint.Reachable {
			fmt.Fprintf(r.out, "  %d. ✓ %-14s %-24s %s\n", index+1, name, endpoint.BaseURL, durationLabel(endpoint.Latency))
		} else {
			r.format(r.out, "  %d. × %-14s %-24s 不可用\n", "  %d. × %-14s %-24s Unreachable\n", index+1, name, endpoint.BaseURL)
		}
	}
}

func localizedEndpointName(name, language string) string {
	if normalizeLanguage(language) != languageEnglish {
		return name
	}
	switch name {
	case "主域名":
		return "Primary"
	case "备用域名":
		return "Alternate"
	case "当前入口":
		return "Current"
	case "指定入口":
		return "Specified"
	default:
		return name
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

func (r *runner) printEnvironments(environments []environment) {
	r.line(r.out, "  本机工具环境", "  Local tool environment")
	for index, env := range environments {
		status := r.text("未检测到（仍可预配置）", "Not detected (preconfiguration available)")
		mark := "○"
		if env.Detected {
			status = strings.ReplaceAll(env.Reason, "发现本地配置", r.text("发现本地配置", "Local configuration found"))
			mark = "✓"
		}
		fmt.Fprintf(r.out, "  %d. %s %-14s %s\n", index+1, mark, env.Label, status)
	}
	r.line(r.out, "  检测只用于推荐，最终由你选择；不会因为未检测到而隐藏工具。", "  Detection only affects recommendations. You can select any tool, including tools not detected yet.")
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
	answer, err := r.ask(fmt.Sprintf(r.text("  选择工具编号或名称（逗号分隔，回车=%s）: ", "  Select tool numbers or names (comma-separated; Enter=%s): "), strings.Join(defaults, ",")))
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

func (r *runner) authorize(endpoint string, noOpen bool, mode string) (authorizationResult, error) {
	if r.store != nil {
		existing, loadErr := r.store.LoadOAuthAccount()
		if loadErr != nil {
			return authorizationResult{}, loadErr
		}
		if strings.TrimSpace(existing.TokenCredentialID) != "" && strings.TrimSpace(existing.TokenBackend) != "" {
			selectedIssuer, issuerErr := beeapi.OAuthIssuerForEntrance(endpoint)
			if issuerErr != nil {
				return authorizationResult{}, issuerErr
			}
			if existing.Issuer == selectedIssuer {
				return r.authorizeWithExistingOAuth(existing, endpoint, noOpen, mode)
			}
			r.line(r.out, "  BeeAPI 入口已切换到另一 OAuth 安全域，必须重新进行网页授权。", "  The BeeAPI endpoint changed to another OAuth security domain; browser authorization is required again.")
		}
	}
	r.line(r.out, "  1. 跳转网站授权登录（推荐；授权此设备读取账户可用 API Key）", "  1. Authorize in your browser (recommended; allows this device to read available account API Keys)")
	r.line(r.out, "  2. 直接粘贴 API Key（兼容回退）", "  2. Paste one API Key (compatibility fallback)")
	choice, err := r.askLocalized("  请选择 [1]: ", "  Select [1]: ")
	if err != nil && !errors.Is(err, io.EOF) {
		return authorizationResult{}, err
	}
	choice = strings.TrimSpace(choice)
	if choice == "2" {
		credentials, pasteErr := r.pasteAPIKey(endpoint)
		if pasteErr == nil {
			r.clearOAuthAccountForCompatibility()
		}
		return authorizationResult{Credentials: credentials}, pasteErr
	}
	if choice != "" && choice != "1" {
		return authorizationResult{}, errors.New(r.text("登录方式只能选择 1 或 2", "Login method must be 1 or 2"))
	}
	oauthClient, _, account, oauthErr := r.authorizeOAuthAccount(endpoint, noOpen)
	if oauthErr == nil {
		result, selectErr := r.selectAndExportOAuthCredentials(oauthClient, account, endpoint, mode)
		if selectErr == nil || !oauthInteractiveAuthorizationRequired(selectErr) {
			return result, selectErr
		}
		r.line(r.out, "  OAuth 会话需要重新确认，正在生成新的网页授权。", "  The OAuth session needs renewed approval; creating a new browser authorization.")
		return r.authorizeAndSelectOAuthCredentials(endpoint, noOpen, mode)
	}
	if !oauthCapabilityUnavailable(oauthErr) {
		return authorizationResult{}, oauthErr
	}
	r.format(r.out, "  所选入口的 OAuth 服务暂时不可用：%v\n", "  OAuth is temporarily unavailable at the selected endpoint: %v\n", oauthErr)
	r.line(r.out, "  旧版 getbeeapi-cli / cli:configure 设备协议已停用，不会降级或跨域重试。", "  The legacy getbeeapi-cli / cli:configure device protocol is retired; the CLI will not downgrade or retry across domains.")
	r.line(r.out, "  可重新选择另一个 BeeAPI 入口并重新授权，或暂时粘贴单个 API Key。", "  Select another BeeAPI endpoint and authorize again, or temporarily paste one API Key.")
	r.line(r.out, "  CLI 不会要求或接收 BeeAPI 账户密码。", "  The CLI never asks for or receives your BeeAPI account password.")
	fallback, askErr := r.askLocalized("  是否改用粘贴 API Key？[Y/n]: ", "  Paste an API Key instead? [Y/n]: ")
	if askErr != nil && !errors.Is(askErr, io.EOF) {
		return authorizationResult{}, askErr
	}
	if strings.EqualFold(strings.TrimSpace(fallback), "n") {
		return authorizationResult{}, errors.New(r.text("OAuth 授权未完成", "OAuth authorization was not completed"))
	}
	credentials, pasteErr := r.pasteAPIKey(endpoint)
	if pasteErr == nil {
		r.clearOAuthAccountForCompatibility()
	}
	return authorizationResult{Credentials: credentials}, pasteErr
}

func (r *runner) presentDeviceAuthorization(endpoint string, code beeapi.DeviceCode, noOpen bool) error {
	verifyURL, err := deviceVerificationURL(endpoint, code)
	if err != nil {
		return err
	}
	r.format(r.out, "  授权网址: %s\n", "  Authorization URL: %s\n", verifyURL)
	r.format(r.out, "  设备授权码: %s\n", "  Device code: %s\n", code.UserCode)
	if noOpen {
		r.line(r.out, "  已关闭自动打开；请复制以上网址到浏览器。", "  Automatic browser launch is disabled; copy the URL above into a browser.")
		return nil
	}
	if isHeadlessTerminal() {
		r.line(r.out, "  检测到 SSH 或无桌面终端；请在你自己的电脑或手机浏览器打开以上网址。", "  SSH or a headless terminal was detected. Open the URL above on your computer or phone.")
		return nil
	}
	opener := r.openBrowser
	if opener == nil {
		opener = openURL
	}
	if err := opener(verifyURL); err != nil {
		r.format(r.errOut, "  自动打开浏览器失败：%v\n", "  Could not open a browser automatically: %v\n", err)
		r.line(r.out, "  请复制以上授权网址到浏览器继续。", "  Copy the authorization URL above into a browser to continue.")
		return nil
	}
	r.line(r.out, "  ✓ 已尝试打开浏览器；如果没有弹出，请复制以上授权网址。", "  ✓ Browser launch requested. If nothing opened, copy the authorization URL above.")
	return nil
}

func deviceVerificationURL(endpoint string, code beeapi.DeviceCode) (string, error) {
	endpointURL, err := url.Parse(endpoint)
	if err != nil || endpointURL.Scheme != "https" || endpointURL.Hostname() == "" {
		return "", errors.New("当前 BeeAPI 入口不是有效的 HTTPS 地址")
	}
	raw := strings.TrimSpace(code.CompleteURI)
	complete := raw != ""
	if raw == "" {
		raw = strings.TrimSpace(code.VerificationURI)
	}
	if raw == "" {
		raw = "/cli/authorize"
	}
	verifyURL, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("BeeAPI 返回了无效的设备授权网址")
	}
	verifyURL = endpointURL.ResolveReference(verifyURL)
	if verifyURL.Scheme != "https" || !trustedAuthorizationHost(endpointURL.Hostname(), verifyURL.Hostname()) {
		return "", errors.New("BeeAPI 返回的设备授权网址不属于受信任的官方域名")
	}
	if !complete {
		query := verifyURL.Query()
		query.Set("user_code", code.UserCode)
		verifyURL.RawQuery = query.Encode()
	}
	verifyURL.Fragment = ""
	return verifyURL.String(), nil
}

func trustedAuthorizationHost(endpointHost, candidateHost string) bool {
	candidateHost = strings.ToLower(strings.TrimSpace(candidateHost))
	if candidateHost == strings.ToLower(strings.TrimSpace(endpointHost)) {
		return true
	}
	for _, raw := range beeapi.BootstrapEndpoints {
		u, err := url.Parse(raw)
		if err == nil && strings.EqualFold(u.Hostname(), candidateHost) {
			return true
		}
	}
	return false
}

func (r *runner) pasteAPIKey(endpoint string) ([]credentialMaterial, error) {
	r.line(r.out, "  兼容模式：请从 BeeAPI 控制台复制 API Key。", "  Compatibility mode: copy an API Key from the BeeAPI dashboard.")
	r.format(r.out, "  控制台: %s/api-keys\n", "  Dashboard: %s/api-keys\n", endpoint)
	secret, readErr := r.readSecret(r.text("  粘贴 API Key: ", "  Paste API Key: "))
	if readErr != nil {
		return nil, readErr
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, errors.New(r.text("API Key 不能为空", "API Key is required"))
	}
	return []credentialMaterial{{ID: "manual", Name: r.text("手动 API Key", "Manual API Key"), Prefix: safeKeyPrefix(secret), Secret: secret}}, nil
}

func stableCredentialID(secret string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	return fmt.Sprintf("key-%x", digest[:16])
}

func (r *runner) credentialSkipReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "disabled":
		return r.text("已停用", "Disabled")
	case "expired":
		return r.text("已过期", "Expired")
	case "auto_disabled":
		return r.text("已自动停用", "Automatically disabled")
	case "plaintext_unavailable":
		return r.text("原始密钥不可恢复", "Original secret cannot be recovered")
	case "plaintext_hash_mismatch":
		return r.text("密钥完整性校验失败", "Secret integrity check failed")
	case "device_managed":
		return r.text("旧版设备专用密钥", "Legacy device-managed Key")
	case "not_found":
		return r.text("已删除或不存在", "Deleted or missing")
	default:
		if strings.TrimSpace(reason) == "" {
			return r.text("不可导出", "Not exportable")
		}
		return reason
	}
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
		return nil, errors.New(r.text("没有可用的 BeeAPI 凭据", "No usable BeeAPI credentials"))
	}
	seen := map[string]bool{}
	usable := make([]credentialMaterial, 0, len(credentials))
	for index := range credentials {
		credentials[index].ID = strings.TrimSpace(credentials[index].ID)
		credentials[index].Secret = strings.TrimSpace(credentials[index].Secret)
		if credentials[index].ID == "" || credentials[index].Secret == "" {
			return nil, errors.New(r.text("BeeAPI 返回的设备凭据不完整", "BeeAPI returned incomplete device credentials"))
		}
		if seen[credentials[index].ID] {
			return nil, errors.New(r.text("BeeAPI 返回了重复的设备凭据", "BeeAPI returned duplicate device credentials"))
		}
		seen[credentials[index].ID] = true
		if credentials[index].ModelOptionsAuthoritative && len(credentials[index].ModelOptions) > 0 {
			credentials[index].ModelOptions = normalizeModelOptions(credentials[index].ModelOptions)
			credentials[index].Models = credentials[index].Models[:0]
			for _, option := range credentials[index].ModelOptions {
				credentials[index].Models = append(credentials[index].Models, option.ID)
			}
			r.format(r.out, "    %s · 可用模型 %d 个 · 已验证协议能力\n", "    %s · %d available model(s) · Protocol capabilities verified\n", credentials[index].Name, len(credentials[index].Models))
			usable = append(usable, credentials[index])
			continue
		}
		discovery, err := r.modelsForCredential(endpoint, credentials[index].Secret)
		if err != nil {
			r.format(r.out, "    ↷ %s · 已跳过：%v\n", "    ↷ %s · Skipped: %v\n", credentials[index].Name, err)
			continue
		}
		credentials[index].Models = discovery.Models
		credentials[index].ModelOptions = discovery.Options
		credentials[index].ModelOptionsAuthoritative = discovery.Authoritative
		capabilityLabel := r.text("旧版兼容发现", "Legacy-compatible discovery")
		if discovery.Authoritative {
			capabilityLabel = r.text("已验证协议能力", "Protocol capabilities verified")
		}
		r.format(r.out, "    %s · 可用模型 %d 个 · %s\n", "    %s · %d available model(s) · %s\n", credentials[index].Name, len(discovery.Models), capabilityLabel)
		usable = append(usable, credentials[index])
	}
	if len(usable) == 0 {
		return nil, errors.New(r.text("所有 API Key 都无法读取可用模型，请在 BeeAPI 检查 Key 状态与路由后重试", "No API Key could load available models; check Key status and routing in BeeAPI, then retry"))
	}
	return usable, nil
}

func (r *runner) selectCredentialAssignments(agents []string, credentials []credentialMaterial, existing map[string]string, assumeYes bool) (map[string]string, error) {
	if len(credentials) == 0 {
		return nil, errors.New(r.text("没有可分配的 BeeAPI 配置", "No BeeAPI configuration is available for assignment"))
	}
	assignments := map[string]string{}
	r.line(r.out, "\n  为每个工具选择 BeeAPI API Key", "\n  Choose a BeeAPI API Key for each tool")
	for _, agent := range agents {
		if peer := sharedClaudePeer(agent); peer != "" && assignments[peer] != "" {
			assignments[agent] = assignments[peer]
			r.format(r.out, "    %s 与 %s 共享本地配置，使用同一密钥配置\n", "    %s and %s share local configuration and will use the same API Key\n", agentLabel(agent), agentLabel(peer))
			continue
		}
		compatible := compatibleCredentialIndexes(agent, credentials)
		if len(compatible) == 0 {
			return nil, fmt.Errorf(r.text("所有已读取的 API Key 都没有支持 %s 所需的 %s 模型", "None of the loaded API Keys has a %s-compatible %s model"),
				agentLabel(agent), agentProtocolLabel(agent))
		}

		defaultIndex := compatible[0]
		existingIndex := -1
		existingID := strings.TrimSpace(existing[agent])
		if existingID != "" {
			existingIndex = credentialIndex(credentials, existingID)
			if credentialIndexIncluded(compatible, existingIndex) {
				defaultIndex = existingIndex
			} else if existingIndex >= 0 {
				_, compatibilityErr := compatibleModelsForAgent(agent, credentials[existingIndex])
				r.format(r.out, "    %s 当前的 Key %s 不可用：%s\n", "    %s's current Key %s is unavailable: %s\n",
					agentLabel(agent), credentials[existingIndex].Name, r.localizedErrorMessage(compatibilityErr))
			} else {
				r.format(r.out, "    %s 当前保存的 Key 已无法读取模型，将选择其他兼容 Key\n", "    %s's saved Key can no longer load models; choose another compatible Key\n", agentLabel(agent))
			}
		}
		if assumeYes {
			assignments[agent] = credentials[defaultIndex].ID
			if existingID != "" && existingID != credentials[defaultIndex].ID {
				r.format(r.out, "    %s 已自动改用 %s\n", "    %s automatically switched to %s\n", agentLabel(agent), credentials[defaultIndex].Name)
			}
			continue
		}

		r.format(r.out, "\n  %s · 选择 API Key（需要 %s）\n", "\n  %s · Choose an API Key (requires %s)\n", agentLabel(agent), agentProtocolLabel(agent))
		for index, credential := range credentials {
			count := compatibleModelCount(agent, credential)
			if count == 0 {
				r.format(r.out, "    %d. × %s · %s · 不可选，没有兼容模型\n", "    %d. × %s · %s · Unavailable; no compatible model\n",
					index+1, credential.Name, credential.Prefix)
				continue
			}
			defaultLabel := ""
			if index == defaultIndex {
				defaultLabel = r.text(" · 默认", " · Default")
			}
			r.format(r.out, "    %d. ✓ %s · %s · %d 个兼容模型%s\n", "    %d. ✓ %s · %s · %d compatible model(s)%s\n",
				index+1, credential.Name, credential.Prefix, count, defaultLabel)
		}
		for {
			answer, err := r.ask(fmt.Sprintf(r.text("    请选择 API Key [%d]: ", "    Select API Key [%d]: "), defaultIndex+1))
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			index := defaultIndex
			if strings.TrimSpace(answer) != "" {
				number, convErr := strconv.Atoi(strings.TrimSpace(answer))
				if convErr != nil || number < 1 || number > len(credentials) {
					r.format(r.out, "    %s 的 Key 编号无效，请重新选择。\n", "    Invalid Key number for %s; try again.\n", agentLabel(agent))
					continue
				}
				index = number - 1
			}
			if !credentialIndexIncluded(compatible, index) {
				_, compatibilityErr := compatibleModelsForAgent(agent, credentials[index])
				r.format(r.out, "    %s 不能用于 %s：%s\n", "    %s cannot be used for %s: %s\n",
					credentials[index].Name, agentLabel(agent), r.localizedErrorMessage(compatibilityErr))
				r.line(r.out, "    请重新选择带 ✓ 的 API Key。", "    Choose an API Key marked with ✓.")
				continue
			}
			assignments[agent] = credentials[index].ID
			break
		}
	}
	return assignments, nil
}

func compatibleCredentialIndexes(agent string, credentials []credentialMaterial) []int {
	indexes := make([]int, 0, len(credentials))
	for index, credential := range credentials {
		if _, err := compatibleModelsForAgent(agent, credential); err == nil {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func credentialIndexIncluded(indexes []int, wanted int) bool {
	for _, index := range indexes {
		if index == wanted {
			return true
		}
	}
	return false
}

func compatibleModelCount(agent string, credential credentialMaterial) int {
	models, err := compatibleModelsForAgent(agent, credential)
	if err != nil {
		return 0
	}
	return len(models)
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
	return r.selectModelsForAssignmentsWithDefaults(agents, credentials, assignments, nil, assumeYes)
}

func (r *runner) selectModelsForAssignmentsWithDefaults(agents []string, credentials []credentialMaterial, assignments, existing map[string]string, assumeYes bool) (map[string]string, error) {
	selected := map[string]string{}
	r.line(r.out, "\n  为每个工具选择模型", "\n  Choose a model for each tool")
	for _, agent := range agents {
		if peer := sharedClaudePeer(agent); peer != "" && selected[peer] != "" {
			selected[agent] = selected[peer]
			continue
		}
		credential, ok := credentialForID(credentials, assignments[agent])
		if !ok || len(credential.Models) == 0 {
			return nil, fmt.Errorf(r.text("%s 没有可用的密钥配置或模型", "%s has no usable API Key configuration or model"), agentLabel(agent))
		}
		model, matchLabel, err := matchedModel(agent, credential)
		if err != nil {
			return nil, fmt.Errorf("%s · %s: %s", agentLabel(agent), credential.Name, r.localizedErrorMessage(err))
		}
		compatibleModels, compatibleErr := compatibleModelsForAgent(agent, credential)
		if compatibleErr != nil {
			return nil, fmt.Errorf("%s · %s: %s", agentLabel(agent), credential.Name, r.localizedErrorMessage(compatibleErr))
		}
		defaultModel := model
		if containsExact(compatibleModels, existing[agent]) {
			defaultModel = existing[agent]
		}
		if assumeYes {
			selected[agent] = defaultModel
			label := r.localizedModelLabel(matchLabel)
			if defaultModel != model {
				label = r.text("沿用当前可用模型", "Keep current compatible model")
			}
			fmt.Fprintf(r.out, "  %-16s %-24s %s · %s\n", agentLabel(agent), credential.Name, defaultModel, label)
			continue
		}

		choices := modelChoicesWithDefault(compatibleModels, defaultModel)
		visibleCount := min(len(choices), 12)
		r.format(r.out, "\n  %s · %s · 选择模型（%s）\n", "\n  %s · %s · Choose a model (%s)\n",
			agentLabel(agent), credential.Name, agentProtocolLabel(agent))
		for index, choice := range choices[:visibleCount] {
			labels := make([]string, 0, 2)
			if choice == existing[agent] && existing[agent] != "" {
				labels = append(labels, r.text("当前", "Current"))
			}
			if choice == model {
				labels = append(labels, r.text("BeeAPI 推荐", "BeeAPI recommended"))
			}
			defaultLabel := ""
			if len(labels) > 0 {
				defaultLabel = " · " + strings.Join(labels, " · ")
			}
			fmt.Fprintf(r.out, "    %d. %s%s\n", index+1, choice, defaultLabel)
		}
		if len(choices) > visibleCount {
			r.format(r.out, "    另有 %d 个兼容模型；可直接输入完整模型名称。\n", "    %d more compatible model(s); you can enter a full model name directly.\n", len(choices)-visibleCount)
		}
		if !credential.ModelOptionsAuthoritative {
			r.line(r.out, "    提示：服务端未提供协议元数据，当前是旧版兼容模型列表。", "    Note: the server did not provide protocol metadata; this is a legacy-compatible model list.")
		}
		for {
			answer, readErr := r.askLocalized("    请选择模型编号或名称 [1]: ", "    Select a model number or name [1]: ")
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return nil, readErr
			}
			answer = strings.TrimSpace(answer)
			choice := choices[0]
			if answer != "" {
				if number, convErr := strconv.Atoi(answer); convErr == nil {
					if number < 1 || number > visibleCount {
						r.line(r.out, "    模型编号无效，请重新选择；未显示的模型可输入完整名称。", "    Invalid model number; try again. You may enter the full name of an unlisted model.")
						continue
					}
					choice = choices[number-1]
				} else if containsExact(compatibleModels, answer) {
					choice = answer
				} else {
					r.format(r.out, "    模型 %q 不在该 Key 的兼容列表中，请重新选择。\n", "    Model %q is not compatible with this Key; try again.\n", answer)
					continue
				}
			}
			selected[agent] = choice
			r.format(r.out, "    ✓ %s 使用 %s · %s\n", "    ✓ %s will use %s · %s\n", agentLabel(agent), credential.Name, choice)
			break
		}
	}
	return selected, nil
}

func modelChoicesWithDefault(models []string, defaultModel string) []string {
	choices := make([]string, 0, len(models))
	if containsExact(models, defaultModel) {
		choices = append(choices, defaultModel)
	}
	for _, model := range models {
		if model != defaultModel {
			choices = append(choices, model)
		}
	}
	return choices
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
			return nil, fmt.Errorf(r.text("保存配置 %q: %w", "Save configuration %q: %w"), credential.Name, err)
		}
		stored = append(stored, state.Credential{
			ID: credential.ID, Name: credential.Name, Prefix: credential.Prefix,
			SourcePrefix: credential.SourcePrefix, Backend: backend,
		})
	}
	return stored, nil
}

func (r *runner) loadCredentialMaterials(cfg state.Config, withModels bool) ([]credentialMaterial, error) {
	return r.loadCredentialMaterialsAt(cfg, cfg.Endpoint, withModels)
}

func (r *runner) loadCredentialMaterialsAt(cfg state.Config, endpoint string, withModels bool) ([]credentialMaterial, error) {
	var credentials []credentialMaterial
	if len(cfg.Credentials) == 0 {
		secret, err := r.store.LoadCredential(cfg.CredentialBackend)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(cfg.KeyName)
		if name == "" {
			name = r.text("旧版默认配置", "Legacy default configuration")
		}
		credentials = []credentialMaterial{{ID: "default", Name: name, Prefix: safeKeyPrefix(secret), Secret: secret}}
	} else {
		credentials = make([]credentialMaterial, 0, len(cfg.Credentials))
		seen := map[string]bool{}
		for _, stored := range cfg.Credentials {
			if strings.TrimSpace(stored.ID) == "" || strings.TrimSpace(stored.Backend) == "" || seen[stored.ID] {
				return nil, errors.New(r.text("本地 BeeAPI 凭据索引损坏，请重新登录", "The local BeeAPI credential index is damaged; sign in again"))
			}
			seen[stored.ID] = true
			secret, err := r.store.LoadNamedCredential(stored.Backend, stored.ID)
			if err != nil {
				return nil, fmt.Errorf(r.text("读取配置 %q: %w", "Read configuration %q: %w"), stored.Name, err)
			}
			credentials = append(credentials, credentialMaterial{
				ID: stored.ID, Name: stored.Name, Prefix: stored.Prefix,
				SourcePrefix: stored.SourcePrefix, Secret: secret,
			})
		}
	}
	if withModels {
		return r.discoverCredentialModels(endpoint, credentials)
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

func credentialSummaryName(credentials []state.Credential, language string) string {
	if len(credentials) == 0 {
		return ""
	}
	if len(credentials) == 1 {
		return credentials[0].Name
	}
	if normalizeLanguage(language) == languageEnglish {
		return fmt.Sprintf("%d API Key configurations", len(credentials))
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

func matchedModel(agent string, credential credentialMaterial) (string, string, error) {
	models, err := compatibleModelsForAgent(agent, credential)
	if err != nil {
		return "", "", err
	}
	if !credential.ModelOptionsAuthoritative {
		return recommendedModel(agent, models), "默认候选", nil
	}
	option, ok := modelOptionForID(credential.ModelOptions, models[0])
	if !ok {
		return "", "", errors.New("模型协议元数据不完整")
	}
	label := agentProtocolLabel(agent)
	label += " · 客户端适配"
	return option.ID, label, nil
}

func compatibleModelsForAgent(agent string, credential credentialMaterial) ([]string, error) {
	if !credential.ModelOptionsAuthoritative {
		if len(credential.Models) == 0 {
			return nil, errors.New("没有可用模型")
		}
		return append([]string(nil), credential.Models...), nil
	}
	requiredProtocol := agentProtocol(agent)
	if requiredProtocol == "" {
		return nil, fmt.Errorf("未定义工具 %q 的协议", agent)
	}
	options := make([]beeapi.ModelOption, 0, len(credential.ModelOptions))
	for _, option := range credential.ModelOptions {
		if containsExact(option.Protocols, requiredProtocol) && optionSupportsAgent(agent, option) {
			options = append(options, option)
		}
	}
	if len(options) == 0 {
		return nil, fmt.Errorf("该 API Key 没有支持 %s 的模型，请选择其他 Key", agentProtocolLabel(agent))
	}
	// BeeAPI priority already combines API-key route order with the model
	// ordering used by its marketplace. Keep equal scores in server order.
	sort.SliceStable(options, func(i, j int) bool { return options[i].Priority > options[j].Priority })
	models := make([]string, 0, len(options))
	for _, option := range options {
		models = append(models, option.ID)
	}
	return models, nil
}

func optionSupportsAgent(agent string, option beeapi.ModelOption) bool {
	for _, tag := range agentRecommendationTags(agent) {
		if containsExact(option.RecommendedFor, tag) {
			return true
		}
	}
	return false
}

func agentRecommendationTags(agent string) []string {
	switch agent {
	case "claude", "claude-desktop":
		return []string{"claude_code"}
	case "codex":
		return []string{"codex"}
	case "gemini":
		return []string{"gemini_cli"}
	case "grok", "openclaw":
		return []string{"chatgpt"}
	case "opencode", "hermes":
		return []string{"openai_compatible"}
	default:
		return nil
	}
}

func agentProtocol(agent string) string {
	switch agent {
	case "claude", "claude-desktop":
		return "anthropic/messages"
	case "codex", "grok", "openclaw":
		return "openai/responses"
	case "gemini":
		return "gemini/contents"
	case "opencode", "hermes":
		return "openai/chat_completions"
	default:
		return ""
	}
}

func agentProtocolLabel(agent string) string {
	switch agentProtocol(agent) {
	case "anthropic/messages":
		return "Anthropic Messages"
	case "openai/responses":
		return "OpenAI Responses"
	case "gemini/contents":
		return "Gemini Contents"
	case "openai/chat_completions":
		return "OpenAI Chat Completions"
	default:
		return "未知"
	}
}

func modelOptionForID(options []beeapi.ModelOption, id string) (beeapi.ModelOption, bool) {
	for _, option := range options {
		if option.ID == id {
			return option, true
		}
	}
	return beeapi.ModelOption{}, false
}

func (r *runner) detect() error {
	r.line(r.out, "\n检查本机 AI 工具环境", "\nCheck local AI tool environment")
	environments, err := detectEnvironments()
	if err != nil {
		return err
	}
	r.printEnvironments(environments)
	return nil
}

func (r *runner) configure(args []string) error {
	flags := flag.NewFlagSet("configure", flag.ContinueOnError)
	flags.SetOutput(r.errOut)
	var agentsFlag, modelFlag string
	flags.StringVar(&agentsFlag, "agents", "", r.text("逗号分隔的目标工具", "comma-separated target tools"))
	flags.StringVar(&modelFlag, "model", "", r.text("对所选工具统一使用此模型", "use this model for all selected tools"))
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.Endpoint == "" {
		return errors.New(r.text("尚未完成首次设置，请直接运行 beeapi", "First-time setup is not complete; run beeapi"))
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
	credentials, err := r.loadCredentialMaterials(cfg, true)
	if err != nil {
		return err
	}
	assignments, err := r.selectCredentialAssignments(agents, credentials, cfg.AgentCredentials, true)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		credential, ok := credentialForID(credentials, assignments[agent])
		if !ok {
			return fmt.Errorf(r.text("%s 没有对应的本地凭据", "%s has no matching local credential"), agentLabel(agent))
		}
		compatible, compatibilityErr := compatibleModelsForAgent(agent, credential)
		if compatibilityErr != nil {
			return fmt.Errorf("%s · %s: %s", agentLabel(agent), credential.Name, r.localizedErrorMessage(compatibilityErr))
		}
		if containsExact(compatible, models[agent]) {
			continue
		}
		if modelFlag != "" {
			return fmt.Errorf(r.text("模型 %q 不适用于 %s 当前选择的 Key %s", "Model %q is not compatible with %s's selected Key %s"), modelFlag, agentLabel(agent), credential.Name)
		}
		model, _, matchErr := matchedModel(agent, credential)
		if matchErr != nil {
			return fmt.Errorf("%s · %s: %w", agentLabel(agent), credential.Name, matchErr)
		}
		models[agent] = model
		r.format(r.out, "%s 当前模型不可用，已按 BeeAPI 优先级改用 %s · %s\n", "%s's current model is unavailable; switched by BeeAPI priority to %s · %s\n",
			agentLabel(agent), credential.Name, model)
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
	setDefaultModel(&cfg, agents, models)
	syncCurrentProfile(&cfg)
	if err := r.store.SaveConfig(cfg); err != nil {
		_, _ = r.store.Rollback(result.BackupID)
		return err
	}
	r.clearUsageCache()
	r.format(r.out, "已配置 %s；备份编号 %s\n", "Configured %s; backup ID %s\n", friendlyAgentList(agents), result.BackupID)
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
		r.printEndpoints(beeapi.DiscoverEndpoints(r.ctx, nil))
		return nil
	case "optimize":
		flags := flag.NewFlagSet("network optimize", flag.ContinueOnError)
		flags.SetOutput(r.errOut)
		var host string
		var apply, yes bool
		flags.StringVar(&host, "host", "beeapi.ai", r.text("要优选的域名", "domain to optimize"))
		flags.BoolVar(&apply, "apply-hosts", false, r.text("把验证通过的 IP 写入 Hosts", "write the validated IP to Hosts"))
		flags.BoolVar(&yes, "yes", false, r.text("跳过确认", "skip confirmation"))
		if err := flags.Parse(args); err != nil {
			return err
		}
		_, err := r.optimizeAndMaybeApply(host, apply, yes)
		return err
	case "restore":
		flags := flag.NewFlagSet("network restore", flag.ContinueOnError)
		flags.SetOutput(r.errOut)
		var host string
		flags.StringVar(&host, "host", "all", r.text("要恢复的域名或 all", "domain to restore, or all"))
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
		r.format(r.out, "已移除受管 Hosts 记录；备份编号 %s\n", "Removed managed Hosts entries; backup ID %s\n", backup.ID)
		return nil
	default:
		return fmt.Errorf(r.text("未知 network 子命令 %q", "Unknown network subcommand %q"), command)
	}
}

func (r *runner) optimizeAndMaybeApply(host string, forceApply, assumeYes bool) (routeopt.Result, error) {
	binary, version, err := routeopt.EnsureCFST(r.ctx, r.out)
	if err != nil {
		return routeopt.Result{}, err
	}
	r.format(r.out, "  使用 CloudflareSpeedTest %s，目标 %s\n", "  Using CloudflareSpeedTest %s; target %s\n", version, host)
	optCtx, cancel := context.WithTimeout(r.ctx, 12*time.Minute)
	defer cancel()
	result, err := routeopt.Optimize(optCtx, binary, host, r.out)
	if err != nil {
		return result, err
	}
	fmt.Fprintln(r.out)
	r.format(r.out, "  最优 IP %s，BeeAPI API 延迟 %s ms", "  Best IP %s, BeeAPI API latency %s ms", result.IP, result.LatencyMS)
	if result.SpeedMB != "" {
		r.format(r.out, "，速度 %s MB/s", ", speed %s MB/s", result.SpeedMB)
	}
	if result.Colo != "" {
		r.format(r.out, "，节点 %s", ", colo %s", result.Colo)
	}
	r.line(r.out, "（TLS 与业务接口复验通过）", " (TLS and API validation passed)")
	apply := forceApply
	if !forceApply && !assumeYes {
		answer, askErr := r.askLocalized("  写入受管 Hosts 记录？会先备份，可随时恢复 [y/N]: ", "  Write a managed Hosts entry? A restorable backup will be created first [y/N]: ")
		if askErr != nil {
			return result, askErr
		}
		apply = strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
	}
	if forceApply && !assumeYes {
		answer, askErr := r.askLocalized("  网络不可用，需要写入 Hosts 才能继续；确认并请求管理员权限？[Y/n]: ", "  The network is unavailable and a Hosts update is required. Continue and request administrator access? [Y/n]: ")
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
		return result, fmt.Errorf(r.text("备份 Hosts: %w", "Back up Hosts: %w"), err)
	}
	validateCtx, validateCancel := context.WithTimeout(r.ctx, 10*time.Second)
	validateErr := routeopt.ValidatePinnedIP(validateCtx, host, result.IP)
	validateCancel()
	if validateErr != nil {
		return result, fmt.Errorf(r.text("优选 IP 没有通过 %s 的 TLS 与健康检查: %w", "The selected IP failed TLS and health checks for %s: %w"), host, validateErr)
	}
	if err := routeopt.ApplyHosts(path, host, result.IP); err != nil {
		_, _ = r.store.Rollback(backup.ID)
		return result, err
	}
	r.format(r.out, "  已为 %s 写入受管 Hosts；备份编号 %s\n", "  Wrote a managed Hosts entry for %s; backup ID %s\n", host, backup.ID)
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
	r.format(r.out, "已恢复备份 %s（%d 个文件）\n", "Restored backup %s (%d file(s))\n", manifest.ID, len(manifest.Files))
	return nil
}

func (r *runner) token(args []string) error {
	if len(args) == 0 || args[0] != "print" {
		return errors.New(r.text("用法: beeapi token print [--agent 工具 | --credential 凭据ID]", "Usage: beeapi token print [--agent tool | --credential credential-ID]"))
	}
	flags := flag.NewFlagSet("token print", flag.ContinueOnError)
	flags.SetOutput(r.errOut)
	var agent, credentialID string
	flags.StringVar(&agent, "agent", "", r.text("读取分配给此工具的凭据", "read the credential assigned to this tool"))
	flags.StringVar(&credentialID, "credential", "", r.text("读取指定凭据", "read the specified credential"))
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || (agent != "" && credentialID != "") {
		return errors.New(r.text("用法: beeapi token print [--agent 工具 | --credential 凭据ID]", "Usage: beeapi token print [--agent tool | --credential credential-ID]"))
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
			return errors.New(r.text("本地有多个 BeeAPI 凭据，请使用 --agent 或 --credential 指定", "Multiple BeeAPI credentials are stored; specify one with --agent or --credential"))
		}
		credentialID = credentials[0].ID
	}
	credential, ok := credentialForID(credentials, credentialID)
	if !ok {
		return errors.New(r.text("没有找到目标工具对应的 BeeAPI 凭据，请重新配置该工具", "No BeeAPI credential was found for the target tool; reconfigure that tool"))
	}
	_, err = fmt.Fprintln(r.out, credential.Secret)
	return err
}

func (r *runner) runAgent(args []string) error {
	if len(args) == 0 {
		return errors.New(r.text("用法: beeapi run <claude|claude-desktop|codex|gemini|grok|opencode|openclaw|hermes> [参数...]", "Usage: beeapi run <claude|claude-desktop|codex|gemini|grok|opencode|openclaw|hermes> [args...]"))
	}
	agent := strings.ToLower(args[0])
	if agent == "claude-desktop" {
		cfg, err := r.store.LoadConfig()
		if err != nil {
			return err
		}
		if cfg.Endpoint == "" {
			return errors.New(r.text("尚未完成首次设置，请先直接运行 beeapi", "First-time setup is not complete; run beeapi first"))
		}
		return openURL("claude://code/new")
	}
	commandName := map[string]string{
		"claude": "claude", "codex": "codex", "gemini": "gemini", "grok": "grok",
		"opencode": "opencode", "openclaw": "openclaw", "hermes": "hermes",
	}[agent]
	if commandName == "" {
		return fmt.Errorf(r.text("不支持的工具 %q", "Unsupported tool %q"), agent)
	}
	path, err := exec.LookPath(commandName)
	if err != nil {
		return fmt.Errorf(r.text("未找到 %s，请先安装该工具", "%s was not found; install it first"), commandName)
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
		return fmt.Errorf(r.text("%s 尚未分配 BeeAPI 密钥配置，请先运行 beeapi configure", "%s has no assigned BeeAPI API Key; run beeapi configure first"), agentLabel(agent))
	}
	commandArgs := append([]string(nil), args[1:]...)
	cmd := exec.CommandContext(r.ctx, path, commandArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r.in, r.out, r.errOut
	agentCfg := cfg
	agentCfg.Endpoint = endpointForAgent(cfg, agent)
	cmd.Env = append(os.Environ(), agentEnvironment(agent, agentCfg, credential.Secret)...)
	return cmd.Run()
}

func endpointForAgent(cfg state.Config, agent string) string {
	if endpoint := strings.TrimSpace(cfg.AgentEndpoints[agent]); endpoint != "" {
		return strings.TrimRight(endpoint, "/")
	}
	return strings.TrimRight(cfg.Endpoint, "/")
}

func agentEnvironment(agent string, cfg state.Config, secret string) []string {
	model := cfg.Models[agent]
	switch agent {
	case "claude":
		return []string{"ANTHROPIC_AUTH_TOKEN=" + secret, "ANTHROPIC_BASE_URL=" + cfg.Endpoint + "/anthropic", "ANTHROPIC_MODEL=" + model}
	case "gemini":
		return []string{"GOOGLE_GEMINI_BASE_URL=" + cfg.Endpoint, "GEMINI_API_KEY=" + secret, "GEMINI_MODEL=" + model}
	case "grok":
		return []string{"BEEAPI_API_KEY=" + secret}
	case "hermes":
		return []string{
			"OPENAI_API_KEY=" + secret,
			"OPENAI_BASE_URL=" + strings.TrimRight(cfg.Endpoint, "/") + "/v1",
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

func isHeadlessTerminal() bool {
	if strings.TrimSpace(os.Getenv("SSH_CONNECTION")) != "" || strings.TrimSpace(os.Getenv("SSH_TTY")) != "" {
		return true
	}
	if runtime.GOOS == "linux" {
		return strings.TrimSpace(os.Getenv("DISPLAY")) == "" && strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) == ""
	}
	return false
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
