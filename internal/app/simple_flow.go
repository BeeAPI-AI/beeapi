package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/configurator"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func (r *runner) showOAuthAccountSummary(client *beeapi.Client, account state.OAuthAccount) error {
	baseCtx := r.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, 15*time.Second)
	profile, err := client.OAuthAccount(ctx)
	cancel()
	if err != nil {
		return fmt.Errorf(r.text("读取 BeeAPI 账户信息: %w", "Read BeeAPI account information: %w"), err)
	}
	account, err = r.updateOAuthAccountIdentity(account, profile.StableSubject(), profile.Username, profile.Email)
	if err != nil {
		return err
	}
	identity := strings.TrimSpace(profile.Username)
	if identity == "" {
		identity = strings.TrimSpace(profile.Email)
	}
	if identity == "" {
		identity = r.text("当前账户", "Current account")
	}
	ctx, cancel = context.WithTimeout(baseCtx, 15*time.Second)
	balance, balanceErr := client.OAuthAccountBalance(ctx)
	cancel()
	if balanceErr == nil {
		r.format(r.out, "  ✓ 已登录 %s · 账户余额 %s\n", "  ✓ Signed in as %s · Account balance %s\n", identity, formatBalance(balance.Current(), balance.CurrencyCode()))
	} else {
		r.format(r.out, "  ✓ 已登录 %s · 余额暂时无法读取\n", "  ✓ Signed in as %s · Balance is temporarily unavailable\n", identity)
	}
	return nil
}

func (r *runner) connectOAuthAccountOnly(endpoint string, noOpen, assumeYes bool) (authorizationResult, error) {
	existing, loadErr := r.store.LoadOAuthAccount()
	if loadErr != nil {
		return authorizationResult{}, loadErr
	}
	selectedIssuer, issuerErr := beeapi.OAuthIssuerForEntrance(endpoint)
	if issuerErr != nil {
		return authorizationResult{}, issuerErr
	}
	hasExisting := strings.TrimSpace(existing.TokenCredentialID) != "" && strings.TrimSpace(existing.TokenBackend) != "" && existing.Issuer == selectedIssuer
	if hasExisting {
		identity := strings.TrimSpace(existing.Username)
		if identity == "" {
			identity = strings.TrimSpace(existing.Email)
		}
		if identity == "" {
			identity = r.text("已连接账户", "Connected account")
		}
		r.format(r.out, "  检测到 BeeAPI OAuth 账户：%s\n", "  BeeAPI OAuth account detected: %s\n", identity)
		choice := "1"
		if !assumeYes {
			r.line(r.out, "  1. 继续使用此账户（推荐）", "  1. Continue with this account (recommended)")
			r.line(r.out, "  2. 在网页重新授权", "  2. Authorize again in the browser")
			r.line(r.out, "  3. 改用粘贴 API Key", "  3. Paste an API Key instead")
			answer, err := r.askLocalized("  请选择 [1]: ", "  Select [1]: ")
			if err != nil && !errors.Is(err, io.EOF) {
				return authorizationResult{}, err
			}
			if strings.TrimSpace(answer) != "" {
				choice = strings.TrimSpace(answer)
			}
		}
		switch choice {
		case "1":
			client, account, err := r.oauthAccountClient(r.ctx)
			if err == nil {
				return authorizationResult{}, r.showOAuthAccountSummary(client, account)
			}
			r.format(r.errOut, "  现有 OAuth 连接无法继续，将重新打开网页授权：%v\n", "  The existing OAuth connection cannot continue; opening browser authorization again: %v\n", err)
		case "2":
		case "3":
			credentials, err := r.pasteAPIKey(endpoint)
			if err == nil {
				r.clearOAuthAccountForCompatibility()
			}
			return authorizationResult{Credentials: credentials}, err
		default:
			return authorizationResult{}, errors.New(r.text("登录方式只能选择 1、2 或 3", "Login method must be 1, 2, or 3"))
		}
	} else if !assumeYes {
		r.line(r.out, "  1. 跳转网站授权登录（推荐）", "  1. Authorize in your browser (recommended)")
		r.line(r.out, "  2. 直接粘贴 API Key（兼容回退）", "  2. Paste one API Key (compatibility fallback)")
		choice, err := r.askLocalized("  请选择 [1]: ", "  Select [1]: ")
		if err != nil && !errors.Is(err, io.EOF) {
			return authorizationResult{}, err
		}
		if strings.TrimSpace(choice) == "2" {
			credentials, pasteErr := r.pasteAPIKey(endpoint)
			if pasteErr == nil {
				r.clearOAuthAccountForCompatibility()
			}
			return authorizationResult{Credentials: credentials}, pasteErr
		}
		if strings.TrimSpace(choice) != "" && strings.TrimSpace(choice) != "1" {
			return authorizationResult{}, errors.New(r.text("登录方式只能选择 1 或 2", "Login method must be 1 or 2"))
		}
	}

	client, _, account, err := r.authorizeOAuthAccount(endpoint, noOpen)
	if err != nil {
		return authorizationResult{}, err
	}
	if err := r.showOAuthAccountSummary(client, account); err != nil {
		return authorizationResult{}, err
	}
	return authorizationResult{}, nil
}

func (r *runner) setup(args []string) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(r.errOut)
	var endpointFlag, apiKeyFlag, agentsFlag string
	var assumeYes, noOpen bool
	flags.StringVar(&endpointFlag, "endpoint", "", r.text("指定 BeeAPI 入口", "BeeAPI endpoint"))
	flags.StringVar(&apiKeyFlag, "api-key", "", r.text("直接提供 API Key（也可使用 BEEAPI_API_KEY）", "provide an API Key directly (or use BEEAPI_API_KEY)"))
	flags.StringVar(&agentsFlag, "agents", "", r.text("首次连接后要配置的工具（保留兼容）", "tool to configure after first connection (compatibility)"))
	flags.BoolVar(&assumeYes, "yes", false, r.text("接受安全默认值", "accept safe defaults"))
	flags.BoolVar(&noOpen, "no-open", false, r.text("不自动打开授权网页", "do not open the authorization page automatically"))
	if err := flags.Parse(args); err != nil {
		return err
	}

	r.showLogo()
	r.line(r.out, "\n首次设置 · 连接 BeeAPI", "\nFirst-time setup · Connect BeeAPI")
	fmt.Fprintln(r.out, "────────────────────────────────────────")
	endpoint, err := r.resolveEndpoint(endpointFlag, assumeYes)
	if err != nil {
		return err
	}
	r.line(r.out, "\n[2/3] 连接 BeeAPI 账户", "\n[2/3] Connect your BeeAPI account")

	apiKey := strings.TrimSpace(apiKeyFlag)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("BEEAPI_API_KEY"))
	}
	var connected authorizationResult
	if apiKey != "" {
		discovery, discoverErr := r.modelsForCredential(endpoint, apiKey)
		if discoverErr != nil {
			return discoverErr
		}
		connected.Credentials = []credentialMaterial{{
			ID: stableCredentialID(apiKey), Name: r.text("手动 API Key", "Manual API Key"), Prefix: safeKeyPrefix(apiKey), Secret: apiKey,
			Models: discovery.Models, ModelOptions: discovery.Options, ModelOptionsAuthoritative: discovery.Authoritative,
		}}
		r.clearOAuthAccountForCompatibility()
	} else {
		connected, err = r.connectOAuthAccountOnly(endpoint, noOpen, assumeYes)
		if err != nil {
			return err
		}
	}

	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	cfg.Language = r.language
	cfg.Endpoint = endpoint
	cfg.InitializedAt = time.Now().UTC()
	if strings.TrimSpace(cfg.BinaryPath) == "" {
		cfg.BinaryPath, _ = os.Executable()
	}
	if len(connected.Credentials) > 0 {
		stored, saveErr := r.saveCredentialMaterials(connected.Credentials)
		if saveErr != nil {
			return saveErr
		}
		cfg.Credentials = mergeStoredCredentials(cfg, stored)
		cfg.CredentialBackend = ""
		cfg.KeyName = credentialSummaryName(cfg.Credentials, r.language)
	}
	if err := r.store.SaveConfig(cfg); err != nil {
		return err
	}
	if err := r.store.ClearPendingSetup(); err != nil {
		return err
	}
	r.line(r.out, "\n[3/3] 已连接，准备配置 AI 工具", "\n[3/3] Connected and ready to configure AI tools")
	r.line(r.out, "  ✓ 账户连接与网络入口已保存。", "  ✓ The account connection and network endpoint are saved.")
	if strings.TrimSpace(agentsFlag) != "" {
		r.line(r.out, "  工具配置已改为按工具独立创建；请从主页选择“配置 AI 工具”。", "  Tool configurations are now created independently; choose Configure an AI tool from the home page.")
	}
	return nil
}

func (r *runner) selectSingleAgent(environments []environment) (string, error) {
	for {
		answer, err := r.askLocalized("输入编号（回车=1）: ", "Enter one number (Enter=1): ")
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		answer = strings.TrimSpace(answer)
		if answer == "" {
			answer = "1"
		}
		if number, convErr := strconv.Atoi(answer); convErr == nil {
			if number >= 1 && number <= len(environments) {
				return environments[number-1].Agent, nil
			}
		} else if agents, parseErr := parseAgents(answer); parseErr == nil && len(agents) == 1 {
			return agents[0], nil
		}
		r.line(r.errOut, "  请选择一个有效的工具编号或名称。", "  Choose one valid tool number or name.")
	}
}

func (r *runner) chooseSavedCredentialForAgent(cfg state.Config, agent string) (credentialMaterial, bool, error) {
	if len(cfg.Credentials) == 0 && strings.TrimSpace(cfg.CredentialBackend) == "" {
		return credentialMaterial{}, false, nil
	}
	credentials, err := r.loadCredentialMaterialsAt(cfg, cfg.Endpoint, true)
	if err != nil {
		if r.hasSavedOAuthConnection() {
			r.format(r.errOut, "  已保存 Key 暂时无法读取模型，将从 BeeAPI 账户选择：%v\n", "  Saved Keys could not load models; choose from the BeeAPI account instead: %v\n", err)
			return credentialMaterial{}, false, nil
		}
		return credentialMaterial{}, false, err
	}
	compatible := compatibleCredentialIndexes(agent, credentials)
	hidden := len(credentials) - len(compatible)
	if hidden > 0 {
		r.format(r.out, "  已隐藏 %d 个不可用于 %s 的本机 API Key。\n", "  Hidden %d saved API Key(s) that cannot be used with %s.\n", hidden, agentLabel(agent))
	}
	if len(compatible) == 0 {
		r.format(r.out, "  本机已保存的 Key 暂无适用于 %s 的模型。\n", "  No saved Key currently has a model compatible with %s.\n", agentLabel(agent))
		return credentialMaterial{}, false, nil
	}

	defaultNumber := 1
	currentID := strings.TrimSpace(cfg.AgentCredentials[agent])
	r.line(r.out, "\n  选择要用于这个方案的 BeeAPI API Key", "\n  Choose the BeeAPI API Key for this configuration")
	for number, index := range compatible {
		credential := credentials[index]
		if credential.ID == currentID {
			defaultNumber = number + 1
		}
		labels := make([]string, 0, 1)
		if credential.ID == currentID {
			labels = append(labels, r.text("当前", "Current"))
		}
		suffix := ""
		if len(labels) > 0 {
			suffix = " · " + strings.Join(labels, " · ")
		}
		fmt.Fprintf(r.out, "    %d. ✓ %s · %s · %d %s%s\n", number+1, credential.Name, credential.Prefix,
			compatibleModelCount(agent, credential), r.text("个兼容模型", "compatible model(s)"), suffix)
	}
	extraLabel := r.text("从 BeeAPI 账户选择其他 Key", "Choose another Key from the BeeAPI account")
	if !r.hasSavedOAuthConnection() {
		extraLabel = r.text("粘贴新的 API Key", "Paste a new API Key")
	}
	extraNumber := len(compatible) + 1
	fmt.Fprintf(r.out, "    %d. + %s\n", extraNumber, extraLabel)
	for {
		answer, askErr := r.ask(fmt.Sprintf(r.text("  输入编号 [%d]: ", "  Enter a number [%d]: "), defaultNumber))
		if askErr != nil && !errors.Is(askErr, io.EOF) {
			return credentialMaterial{}, false, askErr
		}
		answer = strings.TrimSpace(answer)
		choice := defaultNumber
		if answer != "" {
			number, convErr := strconv.Atoi(answer)
			if convErr != nil || number < 1 || number > extraNumber {
				r.line(r.errOut, "  API Key 编号无效，请重新选择。", "  Invalid API Key number; choose again.")
				continue
			}
			choice = number
		}
		if choice == extraNumber {
			return credentialMaterial{}, false, nil
		}
		return credentials[compatible[choice-1]], true, nil
	}
}

func (r *runner) acquireCredentialForAgent(cfg *state.Config, agent string) (credentialMaterial, error) {
	if cfg == nil {
		return credentialMaterial{}, errors.New(r.text("本机配置未初始化", "Local configuration is not initialized"))
	}

	pending, resume, err := r.pendingSetupForMode(pendingModeProfile, false)
	if err != nil {
		return credentialMaterial{}, err
	}
	var authorized authorizationResult
	if resume {
		credentials, restoreErr := r.restorePendingCredentialMaterials(pending)
		if restoreErr != nil {
			return credentialMaterial{}, restoreErr
		}
		credentials, restoreErr = r.discoverCredentialModels(cfg.Endpoint, credentials)
		if restoreErr != nil {
			return credentialMaterial{}, restoreErr
		}
		authorized = authorizationResult{Credentials: credentials, Stored: pending.Credentials}
	} else {
		saved, selected, selectErr := r.chooseSavedCredentialForAgent(*cfg, agent)
		if selectErr != nil {
			return credentialMaterial{}, selectErr
		}
		if selected {
			return saved, nil
		}
		if !r.hasSavedOAuthConnection() && (len(cfg.Credentials) > 0 || strings.TrimSpace(cfg.CredentialBackend) != "") {
			credentials, pasteErr := r.pasteAPIKey(cfg.Endpoint)
			if pasteErr != nil {
				return credentialMaterial{}, pasteErr
			}
			authorized = authorizationResult{Credentials: credentials}
		} else if r.hasSavedOAuthConnection() {
			client, account, sessionErr := r.oauthAccountClient(r.ctx)
			if sessionErr == nil && containsScope(account.Scope, "api_keys:export") {
				authorized, err = r.selectAndExportOAuthCredentials(client, account, cfg.Endpoint, pendingModeProfile, agent)
			} else {
				if sessionErr != nil {
					r.format(r.errOut, "  现有 OAuth 连接需要重新确认：%v\n", "  The existing OAuth connection needs renewed approval: %v\n", sessionErr)
				} else {
					r.line(r.out, "  持续登录令牌不保留 Key 导出权限，需要在网页确认本次选择。", "  Persistent sign-in does not retain API Key export permission; approve this selection in the browser.")
				}
				authorized, err = r.authorizeAndSelectOAuthCredentials(cfg.Endpoint, false, pendingModeProfile, agent)
			}
		} else {
			authorized, err = r.authorize(cfg.Endpoint, false, pendingModeProfile, agent)
		}
	}
	if err != nil {
		return credentialMaterial{}, err
	}
	for _, credential := range authorized.Credentials {
		if len(credential.Models) == 0 {
			authorized.Credentials, err = r.discoverCredentialModels(cfg.Endpoint, authorized.Credentials)
			if err != nil {
				return credentialMaterial{}, err
			}
			break
		}
	}
	if len(authorized.Credentials) > 1 {
		// v0.5.x could checkpoint several Keys before the tool was chosen. Keep
		// that recovery path usable, but project it to exactly one Key now.
		assignments, selectErr := r.selectCredentialAssignments([]string{agent}, authorized.Credentials, cfg.AgentCredentials, false)
		if selectErr != nil {
			return credentialMaterial{}, selectErr
		}
		selected, ok := credentialForID(authorized.Credentials, assignments[agent])
		if !ok {
			return credentialMaterial{}, fmt.Errorf(r.text("%s 没有对应的已保存凭据", "%s has no matching saved credential"), agentLabel(agent))
		}
		authorized.Credentials = []credentialMaterial{selected}
	}
	if len(authorized.Credentials) != 1 {
		return credentialMaterial{}, errors.New(r.text("本次配置必须且只能选择一个 API Key", "Exactly one API Key must be selected for this configuration"))
	}
	stored := authorized.Stored
	if len(stored) == 0 {
		stored, err = r.checkpointCredentialMaterials(pendingModeProfile, cfg.Endpoint, authorized.Credentials)
		if err != nil {
			return credentialMaterial{}, err
		}
	}
	cfg.Credentials = mergeStoredCredentials(*cfg, stored)
	cfg.CredentialBackend = ""
	cfg.KeyName = credentialSummaryName(cfg.Credentials, r.language)
	if err := r.store.SaveConfig(*cfg); err != nil {
		r.updatePendingSetup(pendingModeProfile, cfg.Endpoint, stored, err)
		return credentialMaterial{}, err
	}
	if err := r.store.ClearPendingSetup(); err != nil {
		return credentialMaterial{}, err
	}
	r.clearUsageCache()
	return authorized.Credentials[0], nil
}

func profileNameExistsForAgent(profiles []state.Profile, agent, name string) bool {
	for _, profile := range profiles {
		if profileContainsAgent(profile, agent) && strings.EqualFold(strings.TrimSpace(profile.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func defaultToolProfileName(profiles []state.Profile, agent string) string {
	base := agentLabel(agent)
	if !profileNameExistsForAgent(profiles, agent, base) {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s %d", base, suffix)
		if !profileNameExistsForAgent(profiles, agent, candidate) {
			return candidate
		}
	}
}

func (r *runner) askToolProfileName(profiles []state.Profile, agent string) (string, error) {
	defaultName := defaultToolProfileName(profiles, agent)
	for {
		answer, err := r.ask(fmt.Sprintf(r.text("\n  配置方案名称 [%s]（输入 0 返回）: ", "\n  Configuration name [%s] (enter 0 to go back): "), defaultName))
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		answer = strings.TrimSpace(answer)
		if answer == "0" || (errors.Is(err, io.EOF) && answer == "") {
			return "", nil
		}
		if answer == "" {
			answer = defaultName
		}
		name, nameErr := validateProfileName(answer)
		if nameErr != nil {
			fmt.Fprintln(r.errOut, "  "+r.localizedErrorMessage(nameErr))
			continue
		}
		if profileNameExistsForAgent(profiles, agent, name) {
			r.line(r.errOut, "  此工具已存在同名方案，请换一个名称。", "  This tool already has a configuration with that name. Choose another name.")
			continue
		}
		return name, nil
	}
}

func (r *runner) configureToolInteractive() error {
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Initialized() {
		return errors.New(r.text("尚未连接 BeeAPI，请先完成首次设置", "BeeAPI is not connected; complete first-time setup"))
	}
	if ensureProfileState(&cfg) {
		if err := r.store.SaveConfig(cfg); err != nil {
			return err
		}
	}

	r.line(r.out, "\n配置 AI 工具", "\nConfigure an AI tool")
	r.line(r.out, "\n先检查本机 AI 工具环境", "\nFirst, check the local AI tool environment")
	environments, err := detectEnvironments()
	if err != nil {
		return err
	}
	r.printEnvironments(environments)
	agent, err := r.selectSingleAgent(environments)
	if err != nil {
		return err
	}

	credential, err := r.acquireCredentialForAgent(&cfg, agent)
	if err != nil {
		return err
	}
	assignments := map[string]string{agent: credential.ID}
	credentials := []credentialMaterial{credential}
	models, err := r.selectModelsForAssignments([]string{agent}, credentials, assignments, false)
	if err != nil {
		return err
	}
	reasoningEfforts, err := r.selectReasoningEfforts([]string{agent}, credentials, assignments, models, nil)
	if err != nil {
		return err
	}
	name, err := r.askToolProfileName(cfg.Profiles, agent)
	if err != nil || name == "" {
		return err
	}

	now := time.Now().UTC()
	profile := state.Profile{
		ID: nextProfileID(name, cfg.Profiles), Name: name, Endpoint: cfg.Endpoint,
		Models: models, ReasoningEfforts: reasoningEfforts, Agents: []string{agent}, AgentCredentials: assignments,
		CreatedAt: now, UpdatedAt: now,
	}
	setProfileDefaultModel(&profile)
	r.format(r.out, "\n  方案    %s\n", "\n  Configuration  %s\n", profile.Name)
	r.format(r.out, "  工具    %s\n", "  Tool           %s\n", agentLabel(agent))
	r.format(r.out, "  Key     %s · %s\n", "  API Key        %s · %s\n", credential.Name, credential.Prefix)
	r.format(r.out, "  模型    %s\n", "  Model          %s\n", models[agent])
	if effort := reasoningEfforts[agent]; effort != "" {
		r.format(r.out, "  思考    %s\n", "  Reasoning      %s\n", effort)
	}
	answer, askErr := r.askLocalized("\n保存并立即启用这个方案？[Y/n]: ", "\nSave and activate this configuration now? [Y/n]: ")
	if askErr != nil && !errors.Is(askErr, io.EOF) {
		return askErr
	}
	cfg.Profiles = append(cfg.Profiles, profile)
	if strings.EqualFold(strings.TrimSpace(answer), "n") {
		if err := r.store.SaveConfig(cfg); err != nil {
			return err
		}
		r.format(r.out, "✓ 已保存 %s；当前配置未改变。\n", "✓ Saved %s; the active configuration was not changed.\n", profile.Name)
		return nil
	}
	result, err := r.applyProfile(&cfg, profile)
	if err != nil {
		return err
	}
	r.printProfileApplied(profile, result)
	return nil
}

func profilesForAgent(profiles []state.Profile, agent string) []state.Profile {
	result := make([]state.Profile, 0)
	for _, profile := range profiles {
		if profileContainsAgent(profile, agent) {
			result = append(result, profile)
		}
	}
	return result
}

// toolConfigurationAgents returns every tool that has either an active
// configuration or at least one saved configuration. Keep the public tool
// order stable so menu numbers do not jump around as profiles are added.
func toolConfigurationAgents(cfg state.Config) []string {
	seen := make(map[string]bool)
	for _, agent := range cfg.Agents {
		seen[agent] = true
	}
	for _, profile := range cfg.Profiles {
		for _, agent := range profile.Agents {
			seen[agent] = true
		}
	}
	result := make([]string, 0, len(seen))
	for _, agent := range configurator.SupportedAgents {
		if seen[agent] {
			result = append(result, agent)
			delete(seen, agent)
		}
	}
	// Preserve unknown legacy adapters instead of hiding their saved profiles.
	for _, agent := range cfg.Agents {
		if seen[agent] {
			result = append(result, agent)
			delete(seen, agent)
		}
	}
	for _, profile := range cfg.Profiles {
		for _, agent := range profile.Agents {
			if seen[agent] {
				result = append(result, agent)
				delete(seen, agent)
			}
		}
	}
	return result
}

func profileProjection(profile state.Profile, agent string) state.Profile {
	return state.Profile{
		ID: profile.ID, Name: profile.Name, Endpoint: profile.Endpoint,
		DefaultModel: profile.Models[agent], Models: map[string]string{agent: profile.Models[agent]},
		ReasoningEfforts: map[string]string{agent: profile.ReasoningEfforts[agent]},
		Agents:           []string{agent}, AgentCredentials: map[string]string{agent: profile.AgentCredentials[agent]},
		CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt,
	}
}

func (r *runner) applyProfileForAgent(cfg *state.Config, profile state.Profile, agent string) (configurator.Result, error) {
	selected := profileProjection(profile, agent)
	selected.Endpoint = beeapi.NormalizeBaseURL(selected.Endpoint)
	if err := validateProfile(selected); err != nil {
		return configurator.Result{}, errors.New(r.localizedErrorMessage(err))
	}
	credentials, err := r.loadCredentialMaterials(*cfg, false)
	if err != nil {
		return configurator.Result{}, err
	}
	apiKeys, err := apiKeysForAssignments(selected.Agents, credentials, selected.AgentCredentials)
	if err != nil {
		return configurator.Result{}, err
	}
	binaryPath := cfg.BinaryPath
	if strings.TrimSpace(binaryPath) == "" {
		binaryPath, _ = os.Executable()
	}
	result, err := configurator.Apply(r.store, configurator.Options{
		Endpoint: selected.Endpoint, APIKeys: apiKeys, Models: selected.Models, ReasoningEfforts: selected.ReasoningEfforts,
		Agents: selected.Agents, BinaryPath: binaryPath,
	})
	if err != nil {
		return configurator.Result{}, err
	}
	cfg.BinaryPath = binaryPath
	activateProfileFields(cfg, selected)
	if err := r.store.SaveConfig(*cfg); err != nil {
		_, _ = r.store.Rollback(result.BackupID)
		return configurator.Result{}, err
	}
	r.clearUsageCache()
	return result, nil
}

func (r *runner) printCurrentToolConfigurations(cfg state.Config, agents []string) {
	r.line(r.out, "\n当前 AI 工具配置", "\nCurrent AI tool configurations")
	if len(agents) == 0 {
		r.line(r.out, "  尚未配置任何 AI 工具。", "  No AI tool is configured yet.")
		return
	}
	for index, agent := range agents {
		profiles := profilesForAgent(cfg.Profiles, agent)
		activeID := strings.TrimSpace(cfg.ActiveProfiles[agent])
		if activeID == "" {
			r.format(r.out, "  %d. %-16s 未启用 · 已保存 %d 个方案\n", "  %d. %-16s Not active · %d saved configuration(s)\n", index+1, agentLabel(agent), len(profiles))
			continue
		}
		profileName := r.text("未命名方案", "Unnamed configuration")
		if profile, ok := profileByID(cfg.Profiles, activeID); ok && strings.TrimSpace(profile.Name) != "" {
			profileName = profile.Name
		}
		credentialName := configCredentialName(cfg, cfg.AgentCredentials[agent])
		parts := []string{profileName}
		if credentialName != "" {
			parts = append(parts, credentialName)
		}
		if model := cfg.Models[agent]; model != "" {
			parts = append(parts, model)
		}
		if effort := cfg.ReasoningEfforts[agent]; effort != "" {
			parts = append(parts, r.text("思考 ", "reasoning ")+effort)
		}
		fmt.Fprintf(r.out, "  %d. %-16s %s\n", index+1, agentLabel(agent), strings.Join(parts, " · "))
	}
}

func (r *runner) currentToolConfigurationsInteractive() error {
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	if ensureProfileState(&cfg) {
		if err := r.store.SaveConfig(cfg); err != nil {
			return err
		}
	}
	agents := toolConfigurationAgents(cfg)
	r.printCurrentToolConfigurations(cfg, agents)
	if len(agents) == 0 {
		return nil
	}
	answer, err := r.askLocalized("\n输入工具编号切换方案，回车返回: ", "\nEnter a tool number to switch configurations, or press Enter to go back: ")
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return nil
	}
	number, convErr := strconv.Atoi(answer)
	if convErr != nil || number < 1 || number > len(agents) {
		return errors.New(r.text("工具编号无效", "Invalid tool number"))
	}
	agent := agents[number-1]
	profiles := profilesForAgent(cfg.Profiles, agent)
	if len(profiles) == 0 {
		return fmt.Errorf(r.text("%s 暂无可切换方案", "%s has no configuration to switch to"), agentLabel(agent))
	}
	r.format(r.out, "\n%s 配置方案\n", "\n%s configurations\n", agentLabel(agent))
	for index, profile := range profiles {
		marker := " "
		if cfg.ActiveProfiles[agent] == profile.ID {
			marker = "✓"
		}
		credentialName := configCredentialName(cfg, profile.AgentCredentials[agent])
		fmt.Fprintf(r.out, "  %d. %s %s · %s · %s\n", index+1, marker, profile.Name, credentialName, profile.Models[agent])
	}
	choice, askErr := r.askLocalized("选择要启用的方案，回车返回: ", "Choose a configuration to activate, or press Enter to go back: ")
	if askErr != nil && !errors.Is(askErr, io.EOF) {
		return askErr
	}
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return nil
	}
	selectedNumber, selectErr := strconv.Atoi(choice)
	if selectErr != nil || selectedNumber < 1 || selectedNumber > len(profiles) {
		return errors.New(r.text("配置方案编号无效", "Invalid configuration number"))
	}
	selected := profiles[selectedNumber-1]
	if cfg.ActiveProfiles[agent] == selected.ID {
		r.format(r.out, "  %s 已在使用 %s。\n", "  %s is already using %s.\n", agentLabel(agent), selected.Name)
		return nil
	}
	result, err := r.applyProfileForAgent(&cfg, selected, agent)
	if err != nil {
		return err
	}
	r.format(r.out, "\n✓ %s 已切换到 %s · 备份 %s\n", "\n✓ %s switched to %s · Backup %s\n", agentLabel(agent), selected.Name, result.BackupID)
	return nil
}
