package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/configurator"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func (r *runner) showLogo() {
	if r.logoShown {
		return
	}
	r.logoShown = true
	fmt.Fprintln(r.out, `
    ____             ___    ____  ____
   / __ )___  ___   /   |  / __ \/  _/
  / __  / _ \/ _ \ / /| | / /_/ // /
 / /_/ /  __/  __// ___ |/ ____// /
/_____/\___/\___//_/  |_/_/   /___/`)
	fmt.Fprintf(r.out, "  BeeAPI CLI %s\n", r.version)
}

func (r *runner) home() error {
	r.showLogo()
	for {
		cfg, err := r.store.LoadConfig()
		if err != nil {
			return err
		}
		if ensureProfileState(&cfg) {
			if err := r.store.SaveConfig(cfg); err != nil {
				return err
			}
		}
		r.printHomeStatus(cfg, r.homeBalanceLabel(cfg))
		r.line(r.out, `
  1. 快速切换配置方案
  2. 新建配置方案
  3. 编辑配置方案
  4. 管理配置方案
  5. 密钥与余额
  6. 启动已配置的 AI 工具
  7. 重新连接 BeeAPI / 更新密钥
  8. 更多设置
  0. 退出`, `
  1. Quick-switch profile
  2. Create profile
  3. Edit profile
  4. Manage profiles
  5. API Keys and balance
  6. Launch a configured AI tool
  7. Reconnect BeeAPI / update API Keys
  8. More settings
  0. Exit`)
		choice, askErr := r.askLocalized("\n请选择: ", "\nSelect an option: ")
		if errors.Is(askErr, io.EOF) && strings.TrimSpace(choice) == "" {
			return nil
		}
		if askErr != nil {
			return askErr
		}

		var actionErr error
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "1":
			actionErr = r.switchProfileInteractive()
		case "2":
			actionErr = r.createProfileInteractive()
		case "3":
			actionErr = r.editProfileInteractive()
		case "4":
			actionErr = r.manageProfilesInteractive()
		case "5":
			actionErr = r.balanceMenu()
		case "6":
			actionErr = r.launchMenu(cfg)
		case "7":
			actionErr = r.reconnect()
		case "8":
			actionErr = r.moreMenu()
		case "0", "q", "quit", "exit":
			r.line(r.out, "已退出 BeeAPI CLI。", "Exited BeeAPI CLI.")
			return nil
		default:
			actionErr = errors.New(r.text("请输入菜单中的编号", "Enter a number from the menu"))
		}
		if actionErr != nil {
			if errors.Is(actionErr, context.Canceled) || errors.Is(actionErr, context.DeadlineExceeded) {
				return actionErr
			}
			r.format(r.errOut, "  操作未完成: %v\n", "  Operation not completed: %v\n", actionErr)
		}
		fmt.Fprintln(r.out)
	}
}

func (r *runner) moreMenu() error {
	r.line(r.out, `
更多设置
  1. 网络入口与 Cloudflare 优选
  2. 检查本机工具环境
  3. 恢复配置备份
  4. 界面语言
  5. 检查并安装 CLI 更新
  6. 断开 OAuth 账户（保留本机 Key）
  7. 重新运行首次设置
  0. 返回`, `
More settings
  1. Network endpoints and Cloudflare IP selection
  2. Check local tool environment
  3. Restore a configuration backup
  4. Interface language
  5. Check and install a CLI update
  6. Disconnect OAuth account (keep local Keys)
  7. Run first-time setup again
  0. Back`)
	choice, err := r.askLocalized("请选择: ", "Select an option: ")
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	switch strings.TrimSpace(choice) {
	case "", "0":
		return nil
	case "1":
		return r.networkMenu()
	case "2":
		return r.detect()
	case "3":
		return r.rollbackMenu()
	case "4":
		return r.languageMenu()
	case "5":
		return r.updateCLI(nil)
	case "6":
		answer, confirmErr := r.askLocalized("  撤销账户连接但保留已保存 Key 与工具配置，继续？[y/N]: ", "  Revoke the account connection but keep saved Keys and tool configuration. Continue? [y/N]: ")
		if confirmErr != nil && !errors.Is(confirmErr, io.EOF) {
			return confirmErr
		}
		if yes(answer) {
			return r.disconnectOAuthAccount()
		}
		return nil
	case "7":
		answer, confirmErr := r.askLocalized("  将重新选择网络入口、凭据和工具，继续？[y/N]: ", "  This will reselect the endpoint, credentials, and tools. Continue? [y/N]: ")
		if confirmErr != nil && !errors.Is(confirmErr, io.EOF) {
			return confirmErr
		}
		if yes(answer) {
			return r.setupWithRecovery(nil)
		}
		return nil
	default:
		return errors.New(r.text("请输入菜单中的编号", "Enter a number from the menu"))
	}
}

func (r *runner) launchMenu(cfg state.Config) error {
	if len(cfg.Agents) == 0 {
		return errors.New(r.text("尚未配置 AI 工具，请先新建或编辑配置方案", "No AI tool is configured; create or edit a profile first"))
	}
	r.line(r.out, "\n启动 AI 工具", "\nLaunch an AI tool")
	for index, agent := range cfg.Agents {
		model := cfg.Models[agent]
		if model == "" {
			model = cfg.DefaultModel
		}
		credentialName := configCredentialName(cfg, cfg.AgentCredentials[agent])
		if model == "" && credentialName == "" {
			fmt.Fprintf(r.out, "  %d. %s\n", index+1, agentLabel(agent))
		} else {
			parts := []string{agentLabel(agent)}
			if credentialName != "" {
				parts = append(parts, credentialName)
			}
			if model != "" {
				parts = append(parts, model)
			}
			fmt.Fprintf(r.out, "  %d. %s\n", index+1, strings.Join(parts, " · "))
		}
	}
	answer, err := r.askLocalized("选择要启动的工具，输入 0 返回: ", "Select a tool to launch, or enter 0 to go back: ")
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	answer = strings.TrimSpace(answer)
	if answer == "0" || answer == "" {
		return nil
	}
	for index, agent := range cfg.Agents {
		if answer == fmt.Sprintf("%d", index+1) || strings.EqualFold(answer, agent) {
			return r.runAgent([]string{agent})
		}
	}
	return errors.New(r.text("工具编号或名称无效", "Invalid tool number or name"))
}

func (r *runner) printHomeStatus(cfg state.Config, balance string) {
	fmt.Fprintln(r.out, "\n────────────────────────────────────────")
	r.format(r.out, "  当前方案  %s\n", "  Profile      %s\n", activeProfileLabel(cfg))
	r.format(r.out, "  账户余额  %s\n", "  Balance      %s\n", balance)
	fmt.Fprintf(r.out, "  BeeAPI       %s\n", r.commonAgentEndpoint(cfg))
	if len(cfg.Agents) == 0 {
		r.line(r.out, "  工具      尚未配置", "  Tools        Not configured")
	} else {
		for _, agent := range cfg.Agents {
			parts := make([]string, 0, 2)
			if credential := configCredentialName(cfg, cfg.AgentCredentials[agent]); credential != "" {
				parts = append(parts, credential)
			}
			model := cfg.Models[agent]
			if model == "" {
				model = cfg.DefaultModel
			}
			if model != "" {
				parts = append(parts, model)
			}
			if len(parts) == 0 {
				parts = append(parts, r.text("已配置", "Configured"))
			}
			fmt.Fprintf(r.out, "  %-16s %s\n", agentLabel(agent), strings.Join(parts, " · "))
		}
	}
}

func (r *runner) commonAgentEndpoint(cfg state.Config) string {
	common := ""
	for _, agent := range cfg.Agents {
		endpoint := endpointForAgent(cfg, agent)
		if common == "" {
			common = endpoint
			continue
		}
		if endpoint != common {
			return r.text("按工具使用不同入口", "Different endpoint per tool")
		}
	}
	if common != "" {
		return common
	}
	return cfg.Endpoint
}

func configCredentialName(cfg state.Config, id string) string {
	if id == "" && len(cfg.Credentials) == 1 {
		id = cfg.Credentials[0].ID
	}
	for _, credential := range cfg.Credentials {
		if credential.ID == id {
			return credential.Name
		}
	}
	if len(cfg.Credentials) == 0 {
		return cfg.KeyName
	}
	return ""
}

func (r *runner) status() error {
	r.showLogo()
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Initialized() {
		r.line(r.out, "\n尚未完成首次设置，请运行 beeapi setup。", "\nFirst-time setup is not complete. Run beeapi setup.")
		return nil
	}
	if ensureProfileState(&cfg) {
		if err := r.store.SaveConfig(cfg); err != nil {
			return err
		}
	}
	r.printHomeStatus(cfg, r.homeBalanceLabel(cfg))
	return nil
}

func (r *runner) configureInteractive() error {
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Initialized() {
		return errors.New(r.text("尚未完成首次设置，请运行 beeapi setup", "First-time setup is not complete; run beeapi setup"))
	}
	credentials, err := r.loadCredentialMaterials(cfg, true)
	if err != nil {
		return err
	}

	r.format(r.out, "\n配置 AI 工具 · 当前有 %d 个 BeeAPI 密钥配置\n", "\nConfigure AI tools · %d BeeAPI API Key configuration(s) available\n", len(credentials))
	environments, err := detectEnvironments()
	if err != nil {
		return err
	}
	r.printEnvironments(environments)
	agents, err := r.selectAgents(environments, "", false)
	if err != nil {
		return err
	}
	assignments, err := r.selectCredentialAssignments(agents, credentials, cfg.AgentCredentials, false)
	if err != nil {
		return err
	}
	selectedModels, err := r.selectModelsForAssignmentsWithDefaults(agents, credentials, assignments, cfg.Models, false)
	if err != nil {
		return err
	}
	apiKeys, err := apiKeysForAssignments(agents, credentials, assignments)
	if err != nil {
		return err
	}
	binaryPath := cfg.BinaryPath
	if strings.TrimSpace(binaryPath) == "" {
		binaryPath, _ = os.Executable()
	}
	result, err := configurator.Apply(r.store, configurator.Options{
		Endpoint: cfg.Endpoint, APIKeys: apiKeys, Models: selectedModels,
		Agents: agents, BinaryPath: binaryPath,
	})
	if err != nil {
		return err
	}
	cfg.Agents, cfg.Models, cfg.AgentCredentials, cfg.BinaryPath = agents, selectedModels, assignments, binaryPath
	setDefaultModel(&cfg, agents, selectedModels)
	syncCurrentProfile(&cfg)
	if err := r.store.SaveConfig(cfg); err != nil {
		_, _ = r.store.Rollback(result.BackupID)
		return err
	}
	r.clearUsageCache()
	r.format(r.out, "\n配置完成 · %s · 备份 %s\n", "\nConfiguration complete · %s · Backup %s\n", friendlyAgentList(agents), result.BackupID)
	for _, hint := range result.Hints {
		fmt.Fprintln(r.out, "  "+r.localizedHint(hint))
	}
	return nil
}

func (r *runner) reconnect() error {
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	ensureProfileState(&cfg)
	pending, resumePending, err := r.pendingSetupForMode(pendingModeReconnect, false)
	if err != nil {
		return err
	}
	endpoint := cfg.Endpoint
	if resumePending {
		endpoint = pending.Endpoint
	}
	if endpoint != "" {
		probe := beeapi.ProbeEndpoints(r.ctx, []beeapi.Endpoint{{Name: "当前入口", BaseURL: endpoint}})
		if len(probe) == 1 && probe[0].Reachable {
			answer, askErr := r.ask(fmt.Sprintf(r.text("\n当前入口 %s 可用，继续使用？[Y/n]: ", "\nCurrent endpoint %s is reachable. Keep using it? [Y/n]: "), endpoint))
			if askErr != nil && !errors.Is(askErr, io.EOF) {
				return askErr
			}
			if strings.EqualFold(strings.TrimSpace(answer), "n") {
				endpoint = ""
			}
		} else {
			r.format(r.out, "\n当前入口 %s 不可用，将重新选择。\n", "\nCurrent endpoint %s is unreachable; choose another endpoint.\n", endpoint)
			endpoint = ""
		}
	}
	if endpoint == "" {
		endpoint, err = r.resolveEndpoint("", false)
		if err != nil {
			return err
		}
	}

	r.line(r.out, "\n重新连接 BeeAPI · 获取新的密钥配置", "\nReconnect BeeAPI · Load updated API Key configurations")
	var credentials []credentialMaterial
	var storedCredentials []state.Credential
	if resumePending {
		credentials, err = r.restorePendingCredentialMaterials(pending)
		if err != nil {
			return err
		}
		storedCredentials = pending.Credentials
		r.format(r.out, "  ✓ 已恢复上次授权保存的 %d 个 API Key。\n", "  ✓ Restored %d API Key(s) saved by the previous authorization.\n", len(credentials))
		r.updatePendingSetup(pendingModeReconnect, endpoint, storedCredentials, nil)
	} else {
		var authorized authorizationResult
		authorized, err = r.authorize(endpoint, false, pendingModeReconnect)
		if err != nil {
			return err
		}
		credentials = authorized.Credentials
		storedCredentials = authorized.Stored
		if len(storedCredentials) == 0 {
			storedCredentials, err = r.checkpointCredentialMaterials(pendingModeReconnect, endpoint, credentials)
			if err != nil {
				return err
			}
		}
	}
	credentials, err = r.discoverCredentialModels(endpoint, credentials)
	if err != nil {
		r.updatePendingSetup(pendingModeReconnect, endpoint, storedCredentials, err)
		return err
	}
	if cfg.BinaryPath == "" {
		cfg.BinaryPath, _ = os.Executable()
	}
	var appliedBackup string
	var assignments map[string]string
	var selectedModels map[string]string
	if len(cfg.Agents) > 0 {
		var selectErr error
		assignments, selectErr = r.selectCredentialAssignments(cfg.Agents, credentials, cfg.AgentCredentials, false)
		if selectErr != nil {
			r.updatePendingSetup(pendingModeReconnect, endpoint, storedCredentials, selectErr)
			return selectErr
		}
		selectedModels, selectErr = r.selectModelsForAssignmentsWithDefaults(cfg.Agents, credentials, assignments, cfg.Models, false)
		if selectErr != nil {
			r.updatePendingSetup(pendingModeReconnect, endpoint, storedCredentials, selectErr)
			return selectErr
		}
		apiKeys, keyErr := apiKeysForAssignments(cfg.Agents, credentials, assignments)
		if keyErr != nil {
			r.updatePendingSetup(pendingModeReconnect, endpoint, storedCredentials, keyErr)
			return keyErr
		}
		result, applyErr := configurator.Apply(r.store, configurator.Options{
			Endpoint: endpoint, APIKeys: apiKeys, Models: selectedModels,
			Agents: cfg.Agents, BinaryPath: cfg.BinaryPath,
		})
		if applyErr != nil {
			r.updatePendingSetup(pendingModeReconnect, endpoint, storedCredentials, applyErr)
			return applyErr
		}
		appliedBackup = result.BackupID
		r.format(r.out, "  已同步更新现有工具；备份 %s\n", "  Existing tools updated; backup %s\n", result.BackupID)
	}
	cfg.Endpoint = endpoint
	cfg.KeyName = credentialSummaryName(storedCredentials, r.language)
	cfg.Credentials = storedCredentials
	cfg.CredentialBackend = ""
	if len(cfg.Agents) > 0 {
		cfg.Models, cfg.AgentCredentials = selectedModels, assignments
		if cfg.AgentEndpoints == nil {
			cfg.AgentEndpoints = map[string]string{}
		}
		for _, agent := range cfg.Agents {
			cfg.AgentEndpoints[agent] = endpoint
		}
		setDefaultModel(&cfg, cfg.Agents, selectedModels)
		syncActiveProfilesFromCurrent(&cfg)
	} else if len(credentials) > 0 && len(credentials[0].Models) > 0 {
		cfg.DefaultModel = credentials[0].Models[0]
		cfg.AgentCredentials = nil
	}
	if err := r.store.SaveConfig(cfg); err != nil {
		if appliedBackup != "" {
			_, _ = r.store.Rollback(appliedBackup)
		}
		r.updatePendingSetup(pendingModeReconnect, endpoint, storedCredentials, err)
		return err
	}
	if err := r.store.ClearPendingSetup(); err != nil {
		r.format(r.errOut, "  警告：清理设置续接点失败：%v\n", "  Warning: could not clear the setup checkpoint: %v\n", err)
	}
	r.clearUsageCache()
	r.format(r.out, "\n已连接 BeeAPI · %s · %d 个密钥配置\n", "\nConnected to BeeAPI · %s · %d API Key configuration(s)\n", endpoint, len(credentials))
	return nil
}

func (r *runner) modelsForCredential(endpoint, secret string) (credentialModelDiscovery, error) {
	client := beeapi.New(endpoint)
	optionCtx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	options, err := client.ModelOptions(optionCtx, secret)
	cancel()
	if err == nil {
		if len(options) == 0 {
			return credentialModelDiscovery{}, errors.New(r.text("该 API Key 当前没有可用模型", "This API Key currently has no available models"))
		}
		models := make([]string, 0, len(options))
		for _, option := range options {
			models = append(models, option.ID)
		}
		return credentialModelDiscovery{Models: models, Options: options, Authoritative: true}, nil
	}
	if !modelOptionsUnavailable(err) {
		return credentialModelDiscovery{}, fmt.Errorf(r.text("API Key 校验或模型能力发现失败: %w", "API Key validation or model capability discovery failed: %w"), err)
	}

	modelCtx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	defer cancel()
	models, err := client.Models(modelCtx, secret)
	if err != nil {
		return credentialModelDiscovery{}, fmt.Errorf(r.text("API Key 校验或模型发现失败: %w", "API Key validation or model discovery failed: %w"), err)
	}
	if len(models) == 0 {
		return credentialModelDiscovery{}, errors.New(r.text("该 API Key 当前没有可用模型", "This API Key currently has no available models"))
	}
	return credentialModelDiscovery{Models: models}, nil
}

func modelOptionsUnavailable(err error) bool {
	var apiErr *beeapi.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	}
	switch apiErr.Code {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func (r *runner) networkMenu() error {
	for {
		r.line(r.out, `
网络入口
  1. 检测官方域名
  2. 优选主域名 beeapi.ai
  3. 优选备用域名 beeapi.dev
  4. 移除全部受管 Hosts 记录
  0. 返回`, `
Network endpoints
  1. Check official domains
  2. Optimize primary domain beeapi.ai
  3. Optimize alternate domain beeapi.dev
  4. Remove all managed Hosts entries
  0. Back`)
		choice, err := r.askLocalized("请选择: ", "Select an option: ")
		if errors.Is(err, io.EOF) && strings.TrimSpace(choice) == "" {
			return nil
		}
		if err != nil {
			return err
		}
		switch strings.TrimSpace(choice) {
		case "1":
			r.printEndpoints(beeapi.DiscoverEndpoints(r.ctx, nil))
		case "2", "3":
			host := "beeapi.ai"
			if strings.TrimSpace(choice) == "3" {
				host = "beeapi.dev"
			}
			if _, err := r.optimizeAndMaybeApply(host, false, false); err != nil {
				r.format(r.errOut, "  优选未完成: %v\n", "  IP selection did not complete: %v\n", err)
			}
		case "4":
			answer, askErr := r.askLocalized("  确认移除 BeeAPI 管理的 Hosts 记录？[y/N]: ", "  Remove Hosts entries managed by BeeAPI? [y/N]: ")
			if askErr != nil && !errors.Is(askErr, io.EOF) {
				return askErr
			}
			if yes(answer) {
				if err := r.network([]string{"restore", "--host", "all"}); err != nil {
					r.format(r.errOut, "  恢复未完成: %v\n", "  Restore did not complete: %v\n", err)
				}
			}
		case "0", "q":
			return nil
		default:
			r.line(r.errOut, "  请输入菜单中的编号", "  Enter a number from the menu")
		}
	}
}

func (r *runner) rollbackMenu() error {
	items, err := r.store.ListBackups()
	if err != nil {
		return err
	}
	if len(items) == 0 {
		r.line(r.out, "\n没有可恢复的配置备份。", "\nNo configuration backup is available to restore.")
		return nil
	}
	r.line(r.out, "\n最近的配置备份", "\nRecent configuration backups")
	limit := len(items)
	if limit > 8 {
		limit = 8
	}
	for index, item := range items[:limit] {
		r.format(r.out, "  %d. %s · %d 个文件\n", "  %d. %s · %d file(s)\n", index+1, item.CreatedAt.Local().Format("2006-01-02 15:04:05"), len(item.Files))
	}
	answer, askErr := r.askLocalized("选择要恢复的编号 [1]，输入 0 返回: ", "Select a backup to restore [1], or enter 0 to go back: ")
	if askErr != nil && !errors.Is(askErr, io.EOF) {
		return askErr
	}
	answer = strings.TrimSpace(answer)
	if answer == "0" {
		return nil
	}
	selected := 0
	if answer != "" {
		for index := 0; index < limit; index++ {
			if answer == fmt.Sprintf("%d", index+1) {
				selected = index
				answer = ""
				break
			}
		}
		if answer != "" {
			return errors.New(r.text("备份编号无效", "Invalid backup number"))
		}
	}
	manifest, err := r.store.Rollback(items[selected].ID)
	if err != nil {
		return err
	}
	r.format(r.out, "已恢复备份 %s（%d 个文件）\n", "Restored backup %s (%d file(s))\n", manifest.ID, len(manifest.Files))
	return nil
}

func setDefaultModel(cfg *state.Config, agents []string, models map[string]string) {
	if cfg == nil {
		return
	}
	if model := models["codex"]; model != "" {
		cfg.DefaultModel = model
		return
	}
	if len(agents) > 0 {
		cfg.DefaultModel = models[agents[0]]
	}
}

func yes(value string) bool {
	value = strings.TrimSpace(value)
	return strings.EqualFold(value, "y") || strings.EqualFold(value, "yes")
}

func friendlyAgentList(agents []string) string {
	labels := make([]string, 0, len(agents))
	for _, agent := range agents {
		labels = append(labels, agentLabel(agent))
	}
	return strings.Join(labels, "、")
}

func agentLabel(agent string) string {
	switch agent {
	case "claude":
		return "Claude Code"
	case "claude-desktop":
		return "Claude Desktop"
	case "codex":
		return "Codex"
	case "gemini":
		return "Gemini CLI"
	case "grok":
		return "Grok Build"
	case "opencode":
		return "OpenCode"
	case "openclaw":
		return "OpenClaw"
	case "hermes":
		return "Hermes"
	default:
		return agent
	}
}
