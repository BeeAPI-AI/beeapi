package app

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/configurator"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

const defaultProfileName = "默认配置"

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func profileFromCurrent(cfg state.Config, id, name string, now time.Time) state.Profile {
	endpoint := beeapi.NormalizeBaseURL(cfg.Endpoint)
	if endpoint == "" {
		endpoint = cfg.Endpoint
	}
	return state.Profile{
		ID:               id,
		Name:             name,
		Endpoint:         endpoint,
		DefaultModel:     cfg.DefaultModel,
		Models:           cloneStringMap(cfg.Models),
		Agents:           append([]string(nil), cfg.Agents...),
		AgentCredentials: cloneStringMap(cfg.AgentCredentials),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// ensureProfileState migrates the single active configuration used through
// v0.2.x into one named plan. It changes only GetBeeAPI's local state; native
// tool files are not touched until a user explicitly applies a plan.
func ensureProfileState(cfg *state.Config) bool {
	if cfg == nil || !cfg.Initialized() {
		return false
	}
	changed := false
	if cfg.Models == nil && len(cfg.Agents) > 0 {
		cfg.Models = map[string]string{}
		changed = true
	}
	if cfg.AgentCredentials == nil && len(cfg.Agents) > 0 {
		cfg.AgentCredentials = map[string]string{}
		changed = true
	}
	fallbackCredentialID := ""
	if len(cfg.Credentials) == 1 {
		fallbackCredentialID = cfg.Credentials[0].ID
	} else if len(cfg.Credentials) == 0 && strings.TrimSpace(cfg.CredentialBackend) != "" {
		fallbackCredentialID = "default"
	}
	for _, agent := range cfg.Agents {
		if cfg.Models[agent] == "" && cfg.DefaultModel != "" {
			cfg.Models[agent] = cfg.DefaultModel
			changed = true
		}
		if cfg.AgentCredentials[agent] == "" && fallbackCredentialID != "" {
			cfg.AgentCredentials[agent] = fallbackCredentialID
			changed = true
		}
	}
	if len(cfg.Profiles) == 0 {
		now := cfg.UpdatedAt
		if now.IsZero() {
			now = time.Now().UTC()
		}
		cfg.Profiles = []state.Profile{profileFromCurrent(*cfg, "default", defaultProfileNameForLanguage(cfg.Language), now)}
		cfg.ActiveProfile = "default"
		changed = true
	}
	if cfg.AgentEndpoints == nil && len(cfg.Agents) > 0 {
		cfg.AgentEndpoints = map[string]string{}
		changed = true
	}
	if cfg.ActiveProfiles == nil && len(cfg.Agents) > 0 {
		cfg.ActiveProfiles = map[string]string{}
		changed = true
	}
	activeID := strings.TrimSpace(cfg.ActiveProfile)
	if profileIndexByID(cfg.Profiles, activeID) < 0 {
		activeID = cfg.Profiles[0].ID
		cfg.ActiveProfile = activeID
		changed = true
	}
	for _, agent := range cfg.Agents {
		if strings.TrimSpace(cfg.AgentEndpoints[agent]) == "" {
			cfg.AgentEndpoints[agent] = cfg.Endpoint
			changed = true
		}
		if strings.TrimSpace(cfg.ActiveProfiles[agent]) == "" {
			candidate := activeID
			if profile, ok := profileByID(cfg.Profiles, candidate); !ok || !profileContainsAgent(profile, agent) {
				candidate = ""
				for _, profile := range cfg.Profiles {
					if profileContainsAgent(profile, agent) {
						candidate = profile.ID
						break
					}
				}
			}
			if candidate != "" {
				cfg.ActiveProfiles[agent] = candidate
				changed = true
			}
		}
	}
	common := commonActiveProfile(*cfg)
	if cfg.ActiveProfile != common {
		cfg.ActiveProfile = common
		changed = true
	}
	return changed
}

func profileIndexByID(profiles []state.Profile, id string) int {
	for index := range profiles {
		if profiles[index].ID == id {
			return index
		}
	}
	return -1
}

func profileByID(profiles []state.Profile, id string) (state.Profile, bool) {
	if index := profileIndexByID(profiles, id); index >= 0 {
		return profiles[index], true
	}
	return state.Profile{}, false
}

func profileContainsAgent(profile state.Profile, agent string) bool {
	for _, candidate := range profile.Agents {
		if candidate == agent {
			return true
		}
	}
	return false
}

func commonActiveProfile(cfg state.Config) string {
	if len(cfg.Agents) == 0 {
		if len(cfg.Profiles) == 1 {
			return cfg.Profiles[0].ID
		}
		return strings.TrimSpace(cfg.ActiveProfile)
	}
	common := ""
	for _, agent := range cfg.Agents {
		id := strings.TrimSpace(cfg.ActiveProfiles[agent])
		if id == "" {
			return ""
		}
		if common == "" {
			common = id
		} else if common != id {
			return ""
		}
	}
	return common
}

func activeProfileLabel(cfg state.Config) string {
	id := commonActiveProfile(cfg)
	if id == "" {
		if normalizeLanguage(cfg.Language) == languageEnglish {
			return "Mixed profiles"
		}
		return "混合配置"
	}
	if profile, ok := profileByID(cfg.Profiles, id); ok && strings.TrimSpace(profile.Name) != "" {
		return profile.Name
	}
	return defaultProfileNameForLanguage(cfg.Language)
}

func profileNameExists(profiles []state.Profile, name, exceptID string) bool {
	for _, profile := range profiles {
		if profile.ID != exceptID && strings.EqualFold(strings.TrimSpace(profile.Name), name) {
			return true
		}
	}
	return false
}

func validateProfileName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("配置方案名称不能为空")
	}
	if utf8.RuneCountInString(name) > 40 {
		return "", errors.New("配置方案名称不能超过 40 个字符")
	}
	for _, value := range name {
		if unicode.IsControl(value) {
			return "", errors.New("配置方案名称不能包含控制字符")
		}
	}
	return name, nil
}

func nextProfileID(name string, profiles []state.Profile) string {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(name))))
	base := fmt.Sprintf("profile-%x", digest[:8])
	candidate := base
	for suffix := 2; profileIndexByID(profiles, candidate) >= 0; suffix++ {
		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
	return candidate
}

func (r *runner) askNewProfileName(profiles []state.Profile) (string, error) {
	for {
		answer, err := r.askLocalized("配置方案名称（输入 0 返回）: ", "Profile name (enter 0 to go back): ")
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if strings.TrimSpace(answer) == "0" || (errors.Is(err, io.EOF) && strings.TrimSpace(answer) == "") {
			return "", nil
		}
		name, nameErr := validateProfileName(answer)
		if nameErr != nil {
			fmt.Fprintln(r.errOut, "  "+r.localizedErrorMessage(nameErr))
			continue
		}
		if profileNameExists(profiles, name, "") {
			r.line(r.errOut, "  已存在同名配置方案，请换一个名称。", "  A profile with this name already exists. Choose another name.")
			continue
		}
		return name, nil
	}
}

func (r *runner) selectProfileEndpoint(current string) (string, error) {
	var candidates []string
	seen := map[string]bool{}
	for _, raw := range append([]string{current}, beeapi.BootstrapEndpoints...) {
		endpoint := beeapi.NormalizeBaseURL(raw)
		if endpoint == "" || seen[endpoint] {
			continue
		}
		seen[endpoint] = true
		candidates = append(candidates, endpoint)
	}
	if len(candidates) == 0 {
		return "", errors.New(r.text("没有可选择的 BeeAPI 官方入口", "No official BeeAPI endpoint is available"))
	}
	r.line(r.out, "\n选择 BeeAPI 入口", "\nChoose a BeeAPI endpoint")
	for index, endpoint := range candidates {
		label := ""
		if endpoint == beeapi.NormalizeBaseURL(current) {
			label = r.text(" · 当前", " · Current")
		}
		fmt.Fprintf(r.out, "  %d. %s%s\n", index+1, endpoint, label)
	}
	for {
		answer, err := r.askLocalized("请选择入口 [1]: ", "Select endpoint [1]: ")
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		answer = strings.TrimSpace(answer)
		if answer == "" {
			return candidates[0], nil
		}
		number, convErr := strconv.Atoi(answer)
		if convErr == nil && number >= 1 && number <= len(candidates) {
			return candidates[number-1], nil
		}
		r.line(r.errOut, "  入口编号无效，请重新选择。", "  Invalid endpoint number; try again.")
	}
}

func (r *runner) selectAgentsForProfile(environments []environment, defaults []string) ([]string, error) {
	if len(defaults) == 0 {
		for _, item := range environments {
			if item.Detected {
				defaults = append(defaults, item.Agent)
			}
		}
	}
	if len(defaults) == 0 {
		defaults = []string{"claude", "codex", "opencode"}
	}
	answer, err := r.ask(fmt.Sprintf(r.text("  选择工具编号或名称（逗号分隔，回车=%s）: ", "  Select tool numbers or names (comma-separated; Enter=%s): "), strings.Join(defaults, ",")))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return append([]string(nil), defaults...), nil
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

func (r *runner) collectProfileSelections(cfg state.Config, base *state.Profile, name string) (state.Profile, error) {
	currentEndpoint := cfg.Endpoint
	var defaults []string
	var existingAssignments, existingModels map[string]string
	if base != nil {
		currentEndpoint = base.Endpoint
		defaults = append([]string(nil), base.Agents...)
		existingAssignments = base.AgentCredentials
		existingModels = base.Models
	} else if len(cfg.Agents) > 0 {
		defaults = append([]string(nil), cfg.Agents...)
	}
	endpoint, err := r.selectProfileEndpoint(currentEndpoint)
	if err != nil {
		return state.Profile{}, err
	}
	credentials, err := r.loadCredentialMaterialsAt(cfg, endpoint, true)
	if err != nil {
		return state.Profile{}, err
	}
	environments, err := detectEnvironments()
	if err != nil {
		return state.Profile{}, err
	}
	r.printEnvironments(environments)
	agents, err := r.selectAgentsForProfile(environments, defaults)
	if err != nil {
		return state.Profile{}, err
	}
	assignments, err := r.selectCredentialAssignments(agents, credentials, existingAssignments, false)
	if err != nil {
		return state.Profile{}, err
	}
	models, err := r.selectModelsForAssignmentsWithDefaults(agents, credentials, assignments, existingModels, false)
	if err != nil {
		return state.Profile{}, err
	}
	now := time.Now().UTC()
	profile := state.Profile{
		ID:               nextProfileID(name, cfg.Profiles),
		Name:             name,
		Endpoint:         endpoint,
		Models:           models,
		Agents:           agents,
		AgentCredentials: assignments,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	setProfileDefaultModel(&profile)
	if base != nil {
		profile.ID = base.ID
		profile.CreatedAt = base.CreatedAt
		if profile.CreatedAt.IsZero() {
			profile.CreatedAt = now
		}
	}
	return profile, nil
}

func setProfileDefaultModel(profile *state.Profile) {
	if profile == nil {
		return
	}
	if model := profile.Models["codex"]; model != "" {
		profile.DefaultModel = model
		return
	}
	if len(profile.Agents) > 0 {
		profile.DefaultModel = profile.Models[profile.Agents[0]]
	}
}

func validateProfile(profile state.Profile) error {
	if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Name) == "" {
		return errors.New("配置方案信息不完整")
	}
	if beeapi.NormalizeBaseURL(profile.Endpoint) == "" {
		return errors.New("配置方案的 BeeAPI 入口无效")
	}
	if len(profile.Agents) == 0 {
		return errors.New("配置方案至少需要一个 AI 工具")
	}
	for _, agent := range profile.Agents {
		if strings.TrimSpace(profile.AgentCredentials[agent]) == "" {
			return fmt.Errorf("%s 尚未选择 API Key", agentLabel(agent))
		}
		if strings.TrimSpace(profile.Models[agent]) == "" {
			return fmt.Errorf("%s 尚未选择模型", agentLabel(agent))
		}
	}
	return nil
}

func appendAgentOnce(agents []string, agent string) []string {
	for _, existing := range agents {
		if existing == agent {
			return agents
		}
	}
	return append(agents, agent)
}

func activateProfileFields(cfg *state.Config, profile state.Profile) {
	if cfg.Models == nil {
		cfg.Models = map[string]string{}
	}
	if cfg.AgentCredentials == nil {
		cfg.AgentCredentials = map[string]string{}
	}
	if cfg.AgentEndpoints == nil {
		cfg.AgentEndpoints = map[string]string{}
	}
	if cfg.ActiveProfiles == nil {
		cfg.ActiveProfiles = map[string]string{}
	}
	for _, agent := range profile.Agents {
		cfg.Agents = appendAgentOnce(cfg.Agents, agent)
		cfg.Models[agent] = profile.Models[agent]
		cfg.AgentCredentials[agent] = profile.AgentCredentials[agent]
		cfg.AgentEndpoints[agent] = profile.Endpoint
		cfg.ActiveProfiles[agent] = profile.ID
	}
	cfg.Endpoint = profile.Endpoint
	cfg.ActiveProfile = commonActiveProfile(*cfg)
	setDefaultModel(cfg, cfg.Agents, cfg.Models)
}

// syncCurrentProfile captures a direct/advanced configuration change back into
// one named plan so older command-based workflows cannot leave profile state
// out of sync with the native tool files.
func syncCurrentProfile(cfg *state.Config) {
	ensureProfileState(cfg)
	id := commonActiveProfile(*cfg)
	index := profileIndexByID(cfg.Profiles, id)
	now := time.Now().UTC()
	if index < 0 {
		name := "当前配置"
		if normalizeLanguage(cfg.Language) == languageEnglish {
			name = "Current configuration"
		}
		id = nextProfileID(name, cfg.Profiles)
		cfg.Profiles = append(cfg.Profiles, profileFromCurrent(*cfg, id, name, now))
		index = len(cfg.Profiles) - 1
	}
	old := cfg.Profiles[index]
	updated := profileFromCurrent(*cfg, old.ID, old.Name, now)
	updated.CreatedAt = old.CreatedAt
	if updated.CreatedAt.IsZero() {
		updated.CreatedAt = now
	}
	cfg.Profiles[index] = updated
	cfg.ActiveProfiles = make(map[string]string, len(cfg.Agents))
	cfg.AgentEndpoints = make(map[string]string, len(cfg.Agents))
	for _, agent := range cfg.Agents {
		cfg.ActiveProfiles[agent] = id
		cfg.AgentEndpoints[agent] = cfg.Endpoint
	}
	cfg.ActiveProfile = id
}

// syncActiveProfilesFromCurrent preserves a mixed set of active plans while
// refreshing the endpoint, credential and model selected during re-login.
func syncActiveProfilesFromCurrent(cfg *state.Config) {
	ensureProfileState(cfg)
	now := time.Now().UTC()
	for _, agent := range cfg.Agents {
		id := cfg.ActiveProfiles[agent]
		index := profileIndexByID(cfg.Profiles, id)
		if index < 0 {
			continue
		}
		profile := &cfg.Profiles[index]
		profile.Endpoint = endpointForAgent(*cfg, agent)
		if profile.Models == nil {
			profile.Models = map[string]string{}
		}
		if profile.AgentCredentials == nil {
			profile.AgentCredentials = map[string]string{}
		}
		profile.Models[agent] = cfg.Models[agent]
		profile.AgentCredentials[agent] = cfg.AgentCredentials[agent]
		profile.UpdatedAt = now
		setProfileDefaultModel(profile)
	}
	cfg.ActiveProfile = commonActiveProfile(*cfg)
}

func (r *runner) applyProfile(cfg *state.Config, profile state.Profile) (configurator.Result, error) {
	profile.Endpoint = beeapi.NormalizeBaseURL(profile.Endpoint)
	if err := validateProfile(profile); err != nil {
		return configurator.Result{}, errors.New(r.localizedErrorMessage(err))
	}
	credentials, err := r.loadCredentialMaterials(*cfg, false)
	if err != nil {
		return configurator.Result{}, err
	}
	apiKeys, err := apiKeysForAssignments(profile.Agents, credentials, profile.AgentCredentials)
	if err != nil {
		return configurator.Result{}, err
	}
	binaryPath := cfg.BinaryPath
	if strings.TrimSpace(binaryPath) == "" {
		binaryPath, _ = os.Executable()
	}
	result, err := configurator.Apply(r.store, configurator.Options{
		Endpoint: profile.Endpoint, APIKeys: apiKeys, Models: profile.Models,
		Agents: profile.Agents, BinaryPath: binaryPath,
	})
	if err != nil {
		return configurator.Result{}, err
	}
	cfg.BinaryPath = binaryPath
	if index := profileIndexByID(cfg.Profiles, profile.ID); index >= 0 {
		cfg.Profiles[index] = profile
	}
	activateProfileFields(cfg, profile)
	if err := r.store.SaveConfig(*cfg); err != nil {
		_, _ = r.store.Rollback(result.BackupID)
		return configurator.Result{}, err
	}
	r.clearUsageCache()
	return result, nil
}

func (r *runner) printProfileApplied(profile state.Profile, result configurator.Result) {
	r.format(r.out, "\n✓ 已启用配置方案 %s\n", "\n✓ Profile %s is now active\n", profile.Name)
	r.format(r.out, "  工具    %s\n", "  Tools     %s\n", friendlyAgentList(profile.Agents))
	r.format(r.out, "  入口    %s\n", "  Endpoint  %s\n", profile.Endpoint)
	r.format(r.out, "  备份    %s\n", "  Backup    %s\n", result.BackupID)
	for _, hint := range result.Hints {
		fmt.Fprintln(r.out, "  "+r.localizedHint(hint))
	}
}

func (r *runner) createProfileInteractive() error {
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Initialized() {
		return errors.New(r.text("尚未完成首次设置，请直接运行 beeapi", "First-time setup is not complete; run beeapi"))
	}
	ensureProfileState(&cfg)
	r.line(r.out, "\n新建配置方案", "\nCreate profile")
	name, err := r.askNewProfileName(cfg.Profiles)
	if err != nil || name == "" {
		return err
	}
	profile, err := r.collectProfileSelections(cfg, nil, name)
	if err != nil {
		return err
	}
	cfg.Profiles = append(cfg.Profiles, profile)
	answer, askErr := r.askLocalized("\n保存并立即启用这个方案？[Y/n]: ", "\nSave and activate this profile now? [Y/n]: ")
	if askErr != nil && !errors.Is(askErr, io.EOF) {
		return askErr
	}
	if strings.EqualFold(strings.TrimSpace(answer), "n") {
		if err := r.store.SaveConfig(cfg); err != nil {
			return err
		}
		r.format(r.out, "✓ 已保存配置方案 %s；当前方案未改变。\n", "✓ Saved profile %s; the active profile was not changed.\n", profile.Name)
		return nil
	}
	result, err := r.applyProfile(&cfg, profile)
	if err != nil {
		return err
	}
	r.printProfileApplied(profile, result)
	return nil
}

func profileIsActive(cfg state.Config, id string) bool {
	for _, activeID := range cfg.ActiveProfiles {
		if activeID == id {
			return true
		}
	}
	return false
}

func (r *runner) printProfiles(cfg state.Config) {
	r.line(r.out, "\n配置方案", "\nProfiles")
	for index, profile := range cfg.Profiles {
		marker := " "
		if profileIsActive(cfg, profile.ID) {
			marker = "✓"
		}
		fmt.Fprintf(r.out, "  %d. %s %s · %s · %s\n", index+1, marker, profile.Name, friendlyAgentList(profile.Agents), profile.Endpoint)
	}
}

func (r *runner) selectProfile(cfg state.Config, prompt string, allowCurrent bool) (state.Profile, bool, error) {
	r.printProfiles(cfg)
	for {
		answer, err := r.ask(prompt)
		if err != nil && !errors.Is(err, io.EOF) {
			return state.Profile{}, false, err
		}
		answer = strings.TrimSpace(answer)
		if answer == "0" || (errors.Is(err, io.EOF) && answer == "") {
			return state.Profile{}, false, nil
		}
		if answer == "" && allowCurrent {
			if profile, ok := profileByID(cfg.Profiles, commonActiveProfile(cfg)); ok {
				return profile, true, nil
			}
		}
		number, convErr := strconv.Atoi(answer)
		if convErr == nil && number >= 1 && number <= len(cfg.Profiles) {
			return cfg.Profiles[number-1], true, nil
		}
		r.line(r.errOut, "  配置方案编号无效，请重新选择。", "  Invalid profile number; try again.")
	}
}

func profileAlreadyApplied(cfg state.Config, profile state.Profile) bool {
	for _, agent := range profile.Agents {
		if cfg.ActiveProfiles[agent] != profile.ID || cfg.Models[agent] != profile.Models[agent] ||
			cfg.AgentCredentials[agent] != profile.AgentCredentials[agent] || cfg.AgentEndpoints[agent] != profile.Endpoint {
			return false
		}
	}
	return true
}

func (r *runner) switchProfileInteractive() error {
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	if ensureProfileState(&cfg) {
		if err := r.store.SaveConfig(cfg); err != nil {
			return err
		}
	}
	profile, ok, err := r.selectProfile(cfg, r.text("选择要启用的配置方案，输入 0 返回: ", "Select a profile to activate, or enter 0 to go back: "), false)
	if err != nil || !ok {
		return err
	}
	if profileAlreadyApplied(cfg, profile) {
		r.format(r.out, "\n%s 已经应用到其中的全部工具。\n", "\n%s is already applied to all tools in this profile.\n", profile.Name)
		return nil
	}
	r.format(r.out, "\n将切换到 %s\n", "\nSwitching to %s\n", profile.Name)
	for _, agent := range profile.Agents {
		credentialName := configCredentialName(cfg, profile.AgentCredentials[agent])
		fmt.Fprintf(r.out, "  %-16s %s · %s\n", agentLabel(agent), credentialName, profile.Models[agent])
	}
	r.line(r.out, "  将先完整备份，再只更新 BeeAPI 管理字段。", "  A full backup will be created before updating only BeeAPI-managed fields.")
	answer, askErr := r.askLocalized("确认切换？[Y/n]: ", "Confirm switch? [Y/n]: ")
	if askErr != nil && !errors.Is(askErr, io.EOF) {
		return askErr
	}
	if strings.EqualFold(strings.TrimSpace(answer), "n") {
		return nil
	}
	result, err := r.applyProfile(&cfg, profile)
	if err != nil {
		return err
	}
	r.printProfileApplied(profile, result)
	return nil
}

func removedActiveAgent(cfg state.Config, oldProfile, updated state.Profile) string {
	for _, agent := range oldProfile.Agents {
		if profileContainsAgent(updated, agent) || cfg.ActiveProfiles[agent] != oldProfile.ID {
			continue
		}
		return agent
	}
	return ""
}

func (r *runner) editProfileInteractive() error {
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	ensureProfileState(&cfg)
	profile, ok := profileByID(cfg.Profiles, commonActiveProfile(cfg))
	if len(cfg.Profiles) > 1 || !ok {
		prompt := r.text("选择要编辑的配置方案，输入 0 返回: ", "Select a profile to edit, or enter 0 to go back: ")
		allowCurrent := false
		if ok {
			prompt = fmt.Sprintf(r.text("选择要编辑的配置方案（回车=%s，输入 0 返回）: ", "Select a profile to edit (Enter=%s; 0=Back): "), profile.Name)
			allowCurrent = true
		}
		selected, selectedOK, selectErr := r.selectProfile(cfg, prompt, allowCurrent)
		if selectErr != nil || !selectedOK {
			return selectErr
		}
		profile = selected
	}
	r.format(r.out, "\n编辑配置方案 · %s\n", "\nEdit profile · %s\n", profile.Name)
	updated, err := r.collectProfileSelections(cfg, &profile, profile.Name)
	if err != nil {
		return err
	}
	if agent := removedActiveAgent(cfg, profile, updated); agent != "" {
		return fmt.Errorf(r.text("%s 当前仍在使用方案 %s；请先把该工具切换到其他方案，再从本方案移除", "%s is still using profile %s; switch that tool to another profile before removing it"), agentLabel(agent), profile.Name)
	}
	index := profileIndexByID(cfg.Profiles, profile.ID)
	cfg.Profiles[index] = updated
	if !profileIsActive(cfg, profile.ID) {
		if err := r.store.SaveConfig(cfg); err != nil {
			return err
		}
		r.format(r.out, "✓ 已更新配置方案 %s；当前工具配置未改变。\n", "✓ Updated profile %s; active tool configuration was not changed.\n", profile.Name)
		return nil
	}
	result, err := r.applyProfile(&cfg, updated)
	if err != nil {
		return err
	}
	r.printProfileApplied(updated, result)
	return nil
}

func (r *runner) manageProfilesInteractive() error {
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	ensureProfileState(&cfg)
	r.printProfiles(cfg)
	r.line(r.out, "\n  1. 重命名配置方案\n  2. 删除配置方案\n  0. 返回", "\n  1. Rename profile\n  2. Delete profile\n  0. Back")
	choice, err := r.askLocalized("请选择: ", "Select an option: ")
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	switch strings.TrimSpace(choice) {
	case "", "0":
		return nil
	case "1":
		profile, ok, selectErr := r.selectProfile(cfg, r.text("选择要重命名的方案，输入 0 返回: ", "Select a profile to rename, or enter 0 to go back: "), false)
		if selectErr != nil || !ok {
			return selectErr
		}
		for {
			answer, askErr := r.ask(fmt.Sprintf(r.text("新名称 [%s]: ", "New name [%s]: "), profile.Name))
			if askErr != nil && !errors.Is(askErr, io.EOF) {
				return askErr
			}
			if strings.TrimSpace(answer) == "" {
				return nil
			}
			name, nameErr := validateProfileName(answer)
			if nameErr != nil || profileNameExists(cfg.Profiles, name, profile.ID) {
				r.line(r.errOut, "  名称无效或已存在，请重新输入。", "  The name is invalid or already exists; try again.")
				continue
			}
			index := profileIndexByID(cfg.Profiles, profile.ID)
			cfg.Profiles[index].Name = name
			cfg.Profiles[index].UpdatedAt = time.Now().UTC()
			if err := r.store.SaveConfig(cfg); err != nil {
				return err
			}
			r.format(r.out, "✓ 已重命名为 %s。\n", "✓ Renamed to %s.\n", name)
			return nil
		}
	case "2":
		if len(cfg.Profiles) == 1 {
			return errors.New(r.text("至少保留一个配置方案", "At least one profile must remain"))
		}
		profile, ok, selectErr := r.selectProfile(cfg, r.text("选择要删除的方案，输入 0 返回: ", "Select a profile to delete, or enter 0 to go back: "), false)
		if selectErr != nil || !ok {
			return selectErr
		}
		if profileIsActive(cfg, profile.ID) {
			return fmt.Errorf(r.text("配置方案 %s 正在使用，请先切换后再删除", "Profile %s is in use; switch away from it before deleting"), profile.Name)
		}
		answer, askErr := r.ask(fmt.Sprintf(r.text("确认删除 %s？[y/N]: ", "Delete %s? [y/N]: "), profile.Name))
		if askErr != nil && !errors.Is(askErr, io.EOF) {
			return askErr
		}
		if !yes(answer) {
			return nil
		}
		index := profileIndexByID(cfg.Profiles, profile.ID)
		cfg.Profiles = append(cfg.Profiles[:index], cfg.Profiles[index+1:]...)
		if err := r.store.SaveConfig(cfg); err != nil {
			return err
		}
		r.format(r.out, "✓ 已删除配置方案 %s。\n", "✓ Deleted profile %s.\n", profile.Name)
		return nil
	default:
		return errors.New(r.text("请输入菜单中的编号", "Enter a number from the menu"))
	}
}
