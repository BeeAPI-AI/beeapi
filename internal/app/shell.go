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
	fmt.Fprintf(r.out, "  BeeAPI CLI %s · AI 工具配置中心\n", r.version)
}

func (r *runner) home() error {
	r.showLogo()
	fmt.Fprintln(r.out, "\n欢迎回来。这里不会重复首次安装流程。")
	for {
		cfg, err := r.store.LoadConfig()
		if err != nil {
			return err
		}
		printHomeStatus(r.out, cfg)
		fmt.Fprintln(r.out, `
  1. 启动已配置的 AI 工具
  2. 配置或更新 AI 工具
  3. 重新连接 BeeAPI / 更新密钥配置
  4. 网络入口与 Cloudflare 优选
  5. 检查本机工具环境
  6. 恢复配置备份
  7. 重新运行首次设置
  0. 退出`)
		choice, askErr := r.ask("\n请选择: ")
		if errors.Is(askErr, io.EOF) && strings.TrimSpace(choice) == "" {
			return nil
		}
		if askErr != nil {
			return askErr
		}

		var actionErr error
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "1":
			actionErr = r.launchMenu(cfg)
		case "2":
			actionErr = r.configureInteractive()
		case "3":
			actionErr = r.reconnect()
		case "4":
			actionErr = r.networkMenu()
		case "5":
			actionErr = r.detect()
		case "6":
			actionErr = r.rollbackMenu()
		case "7":
			answer, confirmErr := r.ask("  将重新选择网络入口、凭据和工具，继续？[y/N]: ")
			if confirmErr != nil && !errors.Is(confirmErr, io.EOF) {
				actionErr = confirmErr
			} else if yes(answer) {
				actionErr = r.setup(nil)
			}
		case "0", "q", "quit", "exit":
			fmt.Fprintln(r.out, "已退出 BeeAPI CLI。")
			return nil
		default:
			actionErr = errors.New("请输入菜单中的编号")
		}
		if actionErr != nil {
			if errors.Is(actionErr, context.Canceled) || errors.Is(actionErr, context.DeadlineExceeded) {
				return actionErr
			}
			fmt.Fprintln(r.errOut, "  操作未完成:", actionErr)
		}
		fmt.Fprintln(r.out)
	}
}

func (r *runner) launchMenu(cfg state.Config) error {
	if len(cfg.Agents) == 0 {
		return errors.New("尚未配置 AI 工具，请先选择“配置或更新 AI 工具”")
	}
	fmt.Fprintln(r.out, "\n启动 AI 工具")
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
	answer, err := r.ask("选择要启动的工具，输入 0 返回: ")
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
	return errors.New("工具编号或名称无效")
}

func printHomeStatus(out io.Writer, cfg state.Config) {
	fmt.Fprintln(out, "\n────────────────────────────────────────")
	fmt.Fprintf(out, "  BeeAPI  %s\n", cfg.Endpoint)
	credentialLabel := strings.TrimSpace(cfg.KeyName)
	if len(cfg.Credentials) > 1 {
		credentialLabel = fmt.Sprintf("%d 个 BeeAPI Key", len(cfg.Credentials))
	} else if len(cfg.Credentials) == 1 && strings.TrimSpace(cfg.Credentials[0].Name) != "" {
		credentialLabel = cfg.Credentials[0].Name
	}
	if credentialLabel != "" {
		fmt.Fprintf(out, "  密钥    %s\n", credentialLabel)
	}
	if len(cfg.Agents) == 0 {
		fmt.Fprintln(out, "  工具    尚未配置")
	} else {
		fmt.Fprintf(out, "  工具    %s\n", friendlyAgentList(cfg.Agents))
	}
	if !cfg.UpdatedAt.IsZero() {
		fmt.Fprintf(out, "  更新    %s\n", cfg.UpdatedAt.Local().Format("2006-01-02 15:04"))
	}
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
		fmt.Fprintln(r.out, "\n尚未完成首次设置，请运行 beeapi setup。")
		return nil
	}
	printHomeStatus(r.out, cfg)
	return nil
}

func (r *runner) configureInteractive() error {
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Initialized() {
		return errors.New("尚未完成首次设置，请运行 beeapi setup")
	}
	credentials, err := r.loadCredentialMaterials(cfg, true)
	if err != nil {
		return err
	}

	fmt.Fprintf(r.out, "\n配置 AI 工具 · 当前有 %d 个 BeeAPI 密钥配置\n", len(credentials))
	environments, err := detectEnvironments()
	if err != nil {
		return err
	}
	printEnvironments(r.out, environments)
	agents, err := r.selectAgents(environments, "", false)
	if err != nil {
		return err
	}
	assignments, err := r.selectCredentialAssignments(agents, credentials, cfg.AgentCredentials, false)
	if err != nil {
		return err
	}
	selectedModels, err := r.selectModelsForAssignments(agents, credentials, assignments, false)
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
		Agents: agents, BinaryPath: binaryPath, DirectLaunch: true,
	})
	if err != nil {
		return err
	}
	cfg.Agents, cfg.Models, cfg.AgentCredentials, cfg.BinaryPath = agents, selectedModels, assignments, binaryPath
	setDefaultModel(&cfg, agents, selectedModels)
	if err := r.store.SaveConfig(cfg); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "\n配置完成 · %s · 备份 %s\n", friendlyAgentList(agents), result.BackupID)
	for _, hint := range result.Hints {
		fmt.Fprintln(r.out, "  "+hint)
	}
	return nil
}

func (r *runner) reconnect() error {
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	endpoint := cfg.Endpoint
	if endpoint != "" {
		probe := beeapi.ProbeEndpoints(r.ctx, []beeapi.Endpoint{{Name: "当前入口", BaseURL: endpoint}})
		if len(probe) == 1 && probe[0].Reachable {
			answer, askErr := r.ask(fmt.Sprintf("\n当前入口 %s 可用，继续使用？[Y/n]: ", endpoint))
			if askErr != nil && !errors.Is(askErr, io.EOF) {
				return askErr
			}
			if strings.EqualFold(strings.TrimSpace(answer), "n") {
				endpoint = ""
			}
		} else {
			fmt.Fprintf(r.out, "\n当前入口 %s 不可用，将重新选择。\n", endpoint)
			endpoint = ""
		}
	}
	if endpoint == "" {
		endpoint, err = r.resolveEndpoint("", false)
		if err != nil {
			return err
		}
	}

	fmt.Fprintln(r.out, "\n重新连接 BeeAPI · 获取新的密钥配置")
	credentials, err := r.authorize(endpoint, false)
	if err != nil {
		return err
	}
	credentials, err = r.discoverCredentialModels(endpoint, credentials)
	if err != nil {
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
		assignments, selectErr = r.selectCredentialAssignments(cfg.Agents, credentials, nil, false)
		if selectErr != nil {
			return selectErr
		}
		selectedModels, selectErr = r.selectModelsForAssignments(cfg.Agents, credentials, assignments, false)
		if selectErr != nil {
			return selectErr
		}
		apiKeys, keyErr := apiKeysForAssignments(cfg.Agents, credentials, assignments)
		if keyErr != nil {
			return keyErr
		}
		result, applyErr := configurator.Apply(r.store, configurator.Options{
			Endpoint: endpoint, APIKeys: apiKeys, Models: selectedModels,
			Agents: cfg.Agents, BinaryPath: cfg.BinaryPath, DirectLaunch: true,
		})
		if applyErr != nil {
			return applyErr
		}
		appliedBackup = result.BackupID
		fmt.Fprintf(r.out, "  已同步更新现有工具；备份 %s\n", result.BackupID)
	}
	storedCredentials, err := r.saveCredentialMaterials(credentials)
	if err != nil {
		if appliedBackup != "" {
			_, _ = r.store.Rollback(appliedBackup)
		}
		return err
	}
	cfg.Endpoint = endpoint
	cfg.KeyName = credentialSummaryName(storedCredentials)
	cfg.Credentials = storedCredentials
	cfg.CredentialBackend = ""
	if len(cfg.Agents) > 0 {
		cfg.Models, cfg.AgentCredentials = selectedModels, assignments
		setDefaultModel(&cfg, cfg.Agents, selectedModels)
	} else if len(credentials) > 0 && len(credentials[0].Models) > 0 {
		cfg.DefaultModel = credentials[0].Models[0]
		cfg.AgentCredentials = nil
	}
	if err := r.store.SaveConfig(cfg); err != nil {
		if appliedBackup != "" {
			_, _ = r.store.Rollback(appliedBackup)
		}
		return err
	}
	fmt.Fprintf(r.out, "\n已连接 BeeAPI · %s · %d 个密钥配置\n", endpoint, len(credentials))
	return nil
}

func (r *runner) modelsForCredential(endpoint, secret string) (credentialModelDiscovery, error) {
	client := beeapi.New(endpoint)
	optionCtx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	options, err := client.ModelOptions(optionCtx, secret)
	cancel()
	if err == nil {
		if len(options) == 0 {
			return credentialModelDiscovery{}, errors.New("该 API Key 当前没有可用模型")
		}
		models := make([]string, 0, len(options))
		for _, option := range options {
			models = append(models, option.ID)
		}
		return credentialModelDiscovery{Models: models, Options: options, Authoritative: true}, nil
	}
	if !modelOptionsUnavailable(err) {
		return credentialModelDiscovery{}, fmt.Errorf("API Key 校验或模型能力发现失败: %w", err)
	}

	modelCtx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	defer cancel()
	models, err := client.Models(modelCtx, secret)
	if err != nil {
		return credentialModelDiscovery{}, fmt.Errorf("API Key 校验或模型发现失败: %w", err)
	}
	if len(models) == 0 {
		return credentialModelDiscovery{}, errors.New("该 API Key 当前没有可用模型")
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
		fmt.Fprintln(r.out, `
网络入口
  1. 检测官方域名
  2. 优选主域名 beeapi.ai
  3. 优选备用域名 beeapi.dev
  4. 移除全部受管 Hosts 记录
  0. 返回`)
		choice, err := r.ask("请选择: ")
		if errors.Is(err, io.EOF) && strings.TrimSpace(choice) == "" {
			return nil
		}
		if err != nil {
			return err
		}
		switch strings.TrimSpace(choice) {
		case "1":
			printEndpoints(r.out, beeapi.DiscoverEndpoints(r.ctx, nil))
		case "2", "3":
			host := "beeapi.ai"
			if strings.TrimSpace(choice) == "3" {
				host = "beeapi.dev"
			}
			if _, err := r.optimizeAndMaybeApply(host, false, false); err != nil {
				fmt.Fprintln(r.errOut, "  优选未完成:", err)
			}
		case "4":
			answer, askErr := r.ask("  确认移除 BeeAPI 管理的 Hosts 记录？[y/N]: ")
			if askErr != nil && !errors.Is(askErr, io.EOF) {
				return askErr
			}
			if yes(answer) {
				if err := r.network([]string{"restore", "--host", "all"}); err != nil {
					fmt.Fprintln(r.errOut, "  恢复未完成:", err)
				}
			}
		case "0", "q":
			return nil
		default:
			fmt.Fprintln(r.errOut, "  请输入菜单中的编号")
		}
	}
}

func (r *runner) rollbackMenu() error {
	items, err := r.store.ListBackups()
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(r.out, "\n没有可恢复的配置备份。")
		return nil
	}
	fmt.Fprintln(r.out, "\n最近的配置备份")
	limit := len(items)
	if limit > 8 {
		limit = 8
	}
	for index, item := range items[:limit] {
		fmt.Fprintf(r.out, "  %d. %s · %d 个文件\n", index+1, item.CreatedAt.Local().Format("2006-01-02 15:04:05"), len(item.Files))
	}
	answer, askErr := r.ask("选择要恢复的编号 [1]，输入 0 返回: ")
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
			return errors.New("备份编号无效")
		}
	}
	manifest, err := r.store.Rollback(items[selected].ID)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "已恢复备份 %s（%d 个文件）\n", manifest.ID, len(manifest.Files))
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
