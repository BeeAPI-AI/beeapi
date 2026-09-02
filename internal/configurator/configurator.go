package configurator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/BeeAPI-AI/beeapi/internal/state"
)

const (
	claudeDesktopProfileID = "00000000-0000-4000-8000-000000157210"
	GeminiBeeAPIAlias      = "getbeeapi"
)

var SupportedAgents = []string{
	"claude",
	"claude-desktop",
	"codex",
	"gemini",
	"grok",
	"opencode",
	"openclaw",
	"hermes",
}

type Options struct {
	Endpoint         string
	APIKey           string
	APIKeys          map[string]string
	Model            string
	Models           map[string]string
	ReasoningEfforts map[string]string
	Agents           []string
	BinaryPath       string
}

type Result struct {
	BackupID string
	Files    []string
	Hints    []string
}

func Apply(store *state.Store, options Options) (Result, error) {
	if store == nil {
		return Result{}, errors.New("缺少本地状态目录")
	}
	options.Endpoint = strings.TrimRight(strings.TrimSpace(options.Endpoint), "/")
	options.APIKey = strings.TrimSpace(options.APIKey)
	options.Model = strings.TrimSpace(options.Model)
	if options.Endpoint == "" {
		return Result{}, errors.New("入口不能为空")
	}
	agents, err := normalizeAgents(options.Agents)
	if err != nil {
		return Result{}, err
	}
	home, err := targetHome()
	if err != nil {
		return Result{}, err
	}

	shared := map[string]struct{ key, model string }{}
	for _, agent := range agents {
		key := apiKeyForAgent(options, agent)
		model := modelForAgent(options, agent)
		reasoningEffort := reasoningEffortForAgent(options, agent)
		if key == "" {
			return Result{}, fmt.Errorf("没有为 %s 选择 API Key", agent)
		}
		if model == "" {
			return Result{}, fmt.Errorf("没有为 %s 选择模型", agent)
		}
		if agent == "claude-desktop" {
			if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
				return Result{}, errors.New("Claude Desktop 3P 配置目前仅支持 Windows 和 macOS；Linux 请配置 Claude Code")
			}
			if !validClaudeDesktopModel(model) {
				return Result{}, fmt.Errorf("Claude Desktop 直连模式仅支持 claude-sonnet-*、claude-opus-* 或 claude-haiku-* 模型，当前模型 %q 不兼容", model)
			}
		}
		if reasoningEffort != "" {
			if !agentSupportsReasoningEffort(agent) {
				return Result{}, fmt.Errorf("%s 不支持由 GetBeeAPI 写入思考等级", agent)
			}
			if !validReasoningEffort(agent, reasoningEffort) {
				return Result{}, fmt.Errorf("%s 思考等级 %q 无效", agent, reasoningEffort)
			}
		}
		path := pathForAgent(home, agent)
		if previous, ok := shared[path]; ok && (previous.key != key || previous.model != model) {
			return Result{}, fmt.Errorf("%s 与另一个工具共享配置文件，必须使用相同凭据和模型", agent)
		}
		shared[path] = struct{ key, model string }{key: key, model: model}
	}

	legacyProfiles, err := legacyShellProfiles(home)
	if err != nil {
		return Result{}, fmt.Errorf("检查旧版 Shell 启动入口: %w", err)
	}
	paths := append(targetPaths(home, agents), legacyProfiles...)
	backup, err := store.CreateBackup(paths)
	if err != nil {
		return Result{}, fmt.Errorf("创建配置备份: %w", err)
	}

	result := Result{BackupID: backup.ID}
	written := map[string]bool{}
	for _, agent := range agents {
		primaryPath := pathForAgent(home, agent)
		if written[primaryPath] {
			appendHint(&result, agent)
			continue
		}
		files, writeErr := writeAgent(home, agent, options)
		if writeErr != nil {
			_, _ = store.Rollback(backup.ID)
			return Result{}, fmt.Errorf("写入 %s 配置失败（已自动回滚）: %w", agent, writeErr)
		}
		written[primaryPath] = true
		result.Files = appendUnique(result.Files, files...)
		appendHint(&result, agent)
	}
	if err := cleanupLegacyShellProfiles(legacyProfiles); err != nil {
		_, _ = store.Rollback(backup.ID)
		return Result{}, fmt.Errorf("移除旧版 Shell 启动入口失败（已自动回滚）: %w", err)
	}
	if len(legacyProfiles) > 0 {
		result.Files = appendUnique(result.Files, legacyProfiles...)
		result.Hints = append(result.Hints, "旧版 Shell 命令注入已移除；重新打开终端后生效")
	}
	result.Hints = append(result.Hints, "仅更新 BeeAPI 连接字段；原配置已完整备份，可用 beeapi rollback "+backup.ID+" 恢复")
	return result, nil
}

func appendHint(result *Result, agent string) {
	switch agent {
	case "claude":
		result.Hints = append(result.Hints, "Claude Code: claude")
	case "claude-desktop":
		result.Hints = append(result.Hints, "Claude Desktop: 已切换到独立 3P 配置，完全退出并重新打开后生效")
	case "codex":
		result.Hints = append(result.Hints, "Codex: codex")
	case "gemini":
		result.Hints = append(result.Hints, "Gemini CLI: gemini")
	case "grok":
		result.Hints = append(result.Hints, "Grok Build: grok")
	case "opencode":
		result.Hints = append(result.Hints, "OpenCode: opencode")
	case "openclaw":
		result.Hints = append(result.Hints, "OpenClaw: openclaw")
	case "hermes":
		result.Hints = append(result.Hints, "Hermes: hermes")
	}
}

func normalizeAgents(input []string) ([]string, error) {
	if len(input) == 0 {
		input = []string{"claude", "codex", "opencode"}
	}
	allowed := map[string]bool{}
	for _, agent := range SupportedAgents {
		allowed[agent] = true
	}
	seen := map[string]bool{}
	var result []string
	for _, raw := range input {
		for _, part := range strings.Split(raw, ",") {
			agent := strings.ToLower(strings.TrimSpace(part))
			if agent == "all" {
				return append([]string(nil), SupportedAgents...), nil
			}
			if agent == "" {
				continue
			}
			if !allowed[agent] {
				return nil, fmt.Errorf("不支持的智能体 %q，可选: %s", agent, strings.Join(SupportedAgents, ", "))
			}
			if !seen[agent] {
				seen[agent] = true
				result = append(result, agent)
			}
		}
	}
	if len(result) == 0 {
		return nil, errors.New("至少选择一个智能体")
	}
	return result, nil
}

func targetHome() (string, error) {
	if override := strings.TrimSpace(os.Getenv("GETBEE_TARGET_HOME")); override != "" {
		return override, nil
	}
	return os.UserHomeDir()
}

func targetPaths(home string, agents []string) []string {
	var paths []string
	for _, agent := range agents {
		paths = appendUnique(paths, pathsForAgent(home, agent)...)
	}
	sort.Strings(paths)
	return paths
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	for _, value := range values {
		seen[filepath.Clean(value)] = true
	}
	for _, value := range additions {
		clean := filepath.Clean(value)
		if value == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		values = append(values, clean)
	}
	return values
}

func hermesDir(home string) string {
	if runtime.GOOS == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			return filepath.Join(localAppData, "hermes")
		}
		return filepath.Join(home, "AppData", "Local", "hermes")
	}
	return filepath.Join(home, ".hermes")
}

func pathsForAgent(home, agent string) []string {
	switch agent {
	case "claude":
		return []string{filepath.Join(home, ".claude", "settings.json")}
	case "claude-desktop":
		return claudeDesktopPaths(home, runtime.GOOS)
	case "codex":
		return []string{filepath.Join(home, ".codex", "config.toml")}
	case "gemini":
		return []string{
			filepath.Join(home, ".gemini", ".env"),
			filepath.Join(home, ".gemini", "settings.json"),
		}
	case "grok":
		return []string{filepath.Join(home, ".grok", "config.toml")}
	case "opencode":
		return []string{filepath.Join(home, ".config", "opencode", "opencode.json")}
	case "openclaw":
		return []string{filepath.Join(home, ".openclaw", "openclaw.json")}
	case "hermes":
		dir := hermesDir(home)
		return []string{filepath.Join(dir, "config.yaml"), filepath.Join(dir, ".env")}
	default:
		return []string{filepath.Join(home, ".config", "getbeeapi", agent+".json")}
	}
}

func claudeDesktopPaths(home, goos string) []string {
	var appRoot, thirdPartyRoot string
	switch goos {
	case "darwin":
		applicationSupport := filepath.Join(home, "Library", "Application Support")
		appRoot = filepath.Join(applicationSupport, "Claude")
		thirdPartyRoot = filepath.Join(applicationSupport, "Claude-3p")
	case "windows":
		localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		if localAppData == "" {
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		appRoot = filepath.Join(localAppData, "Claude")
		thirdPartyRoot = filepath.Join(localAppData, "Claude-3p")
	default:
		// The caller rejects Linux writes. Returning isolated paths keeps path
		// discovery deterministic without ever treating Claude Code as Desktop.
		appRoot = filepath.Join(home, ".config", "Claude")
		thirdPartyRoot = filepath.Join(home, ".config", "Claude-3p")
	}
	configLibrary := filepath.Join(thirdPartyRoot, "configLibrary")
	return []string{
		filepath.Join(appRoot, "claude_desktop_config.json"),
		filepath.Join(thirdPartyRoot, "claude_desktop_config.json"),
		filepath.Join(configLibrary, "_meta.json"),
		filepath.Join(configLibrary, claudeDesktopProfileID+".json"),
	}
}

func pathForAgent(home, agent string) string {
	return pathsForAgent(home, agent)[0]
}

func writeAgent(home, agent string, options Options) ([]string, error) {
	model := modelForAgent(options, agent)
	apiKey := apiKeyForAgent(options, agent)
	paths := pathsForAgent(home, agent)
	switch agent {
	case "claude":
		patch := map[string]any{
			"env": map[string]any{
				"ANTHROPIC_AUTH_TOKEN":           apiKey,
				"ANTHROPIC_BASE_URL":             options.Endpoint + "/anthropic",
				"ANTHROPIC_MODEL":                model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":  model,
				"ANTHROPIC_DEFAULT_SONNET_MODEL": model,
				"ANTHROPIC_DEFAULT_OPUS_MODEL":   model,
			},
		}
		managed := []jsonPathPatch{{Path: []string{"effortLevel"}, Remove: true}}
		if effort := reasoningEffortForAgent(options, agent); effort != "" {
			managed[0] = jsonPathPatch{Path: []string{"effortLevel"}, Value: effort}
		}
		err := mergeJSONPaths(paths[0], patch, managed)
		return paths[:1], err
	case "claude-desktop":
		if err := writeClaudeDesktop(paths, options.Endpoint, apiKey, model); err != nil {
			return nil, err
		}
		return paths, nil
	case "codex":
		binary := strings.TrimSpace(options.BinaryPath)
		if binary == "" {
			binary = "beeapi"
		}
		topFields := []tomlFieldPatch{
			setTOMLField("model_provider", strconv.Quote("beeapi")),
			setTOMLField("model", strconv.Quote(model)),
			removeTOMLField("model_reasoning_effort"),
		}
		if effort := reasoningEffortForAgent(options, agent); effort != "" {
			topFields[2] = setTOMLField("model_reasoning_effort", strconv.Quote(effort))
		}
		err := patchTOMLFile(paths[0],
			topFields,
			[]tomlSectionPatch{
				{Name: "model_providers.beeapi", Fields: []tomlFieldPatch{
					setTOMLField("name", strconv.Quote("BeeAPI")),
					setTOMLField("base_url", strconv.Quote(options.Endpoint+"/v1")),
					setTOMLField("wire_api", strconv.Quote("responses")),
					setTOMLField("requires_openai_auth", "false"),
					removeTOMLField("experimental_bearer_token"),
					removeTOMLField("env_key"),
					removeTOMLField("auth"),
				}},
				{Name: "model_providers.beeapi.auth", Fields: []tomlFieldPatch{
					setTOMLField("command", strconv.Quote(binary)),
					setTOMLField("args", `["token", "print", "--agent", "codex"]`),
					setTOMLField("refresh_interval_ms", "0"),
					setTOMLField("timeout_ms", "5000"),
				}},
			})
		return paths[:1], err
	case "gemini":
		geminiModel := model
		managed := []jsonPathPatch{{Path: []string{"modelConfigs", "customAliases", GeminiBeeAPIAlias}, Remove: true}}
		if effort := reasoningEffortForAgent(options, agent); effort != "" {
			geminiModel = GeminiBeeAPIAlias
			managed[0] = jsonPathPatch{Path: []string{"modelConfigs", "customAliases", GeminiBeeAPIAlias}, Value: map[string]any{
				"modelConfig": map[string]any{
					"model": model,
					"generateContentConfig": map[string]any{
						"thinkingConfig": geminiThinkingConfig(model, effort),
					},
				},
			}}
		}
		if err := patchEnvFile(paths[0], []envFieldPatch{
			{Key: "GOOGLE_GEMINI_BASE_URL", Value: options.Endpoint},
			{Key: "GEMINI_API_KEY", Value: apiKey},
			{Key: "GEMINI_MODEL", Value: geminiModel},
		}); err != nil {
			return nil, err
		}
		if err := mergeJSONPaths(paths[1], map[string]any{
			"security": map[string]any{"auth": map[string]any{"selectedType": "gemini-api-key"}},
		}, managed); err != nil {
			return nil, err
		}
		return paths, nil
	case "grok":
		modelFields := []tomlFieldPatch{
			setTOMLField("model", strconv.Quote(model)),
			setTOMLField("base_url", strconv.Quote(options.Endpoint+"/v1")),
			setTOMLField("name", strconv.Quote("BeeAPI · "+model)),
			setTOMLField("api_key", strconv.Quote(apiKey)),
			removeTOMLField("env_key"),
			removeTOMLField("auth_provider"),
			setTOMLField("api_backend", strconv.Quote("responses")),
			removeTOMLField("reasoning_effort"),
			removeTOMLField("supports_reasoning_effort"),
			removeTOMLField("reasoning_efforts"),
		}
		if effort := reasoningEffortForAgent(options, agent); effort != "" {
			modelFields[7] = setTOMLField("reasoning_effort", strconv.Quote(effort))
			modelFields[8] = setTOMLField("supports_reasoning_effort", "true")
			modelFields[9] = setTOMLField("reasoning_efforts", `["low", "medium", "high", "xhigh"]`)
		}
		err := patchTOMLFile(paths[0], nil, []tomlSectionPatch{
			{Name: "model.beeapi", Fields: modelFields},
			{Name: "models", Fields: []tomlFieldPatch{setTOMLField("default", strconv.Quote("beeapi"))}},
		})
		return paths[:1], err
	case "opencode":
		modelConfig := map[string]any{"name": model}
		if effort := reasoningEffortForAgent(options, agent); effort != "" {
			modelConfig["reasoning"] = true
			modelConfig["options"] = map[string]any{"reasoningEffort": effort}
		}
		err := mergeJSONPaths(paths[0], map[string]any{
			"$schema": "https://opencode.ai/config.json",
			"model":   "beeapi/" + model,
			"provider": map[string]any{
				"beeapi": map[string]any{
					"npm":  "@ai-sdk/openai-compatible",
					"name": "BeeAPI",
					"options": map[string]any{
						"baseURL": options.Endpoint + "/v1",
						"apiKey":  apiKey,
					},
				},
			},
		}, []jsonPathPatch{{Path: []string{"provider", "beeapi", "models", model}, Value: modelConfig}})
		return paths[:1], err
	case "openclaw":
		modelConfig := map[string]any{"id": model, "name": model}
		managed := []jsonPathPatch{{Path: []string{"agents", "defaults", "thinkingDefault"}, Remove: true}}
		if effort := reasoningEffortForAgent(options, agent); effort != "" {
			managed[0] = jsonPathPatch{Path: []string{"agents", "defaults", "thinkingDefault"}, Value: effort}
			modelConfig["reasoning"] = true
			modelConfig["compat"] = map[string]any{
				"supportedReasoningEfforts": []any{"minimal", "low", "medium", "high", "xhigh"},
			}
		}
		err := mergeJSONPaths(paths[0], map[string]any{
			"agents": map[string]any{"defaults": map[string]any{"model": map[string]any{"primary": "beeapi/" + model}}},
			"models": map[string]any{
				"mode": "merge",
				"providers": map[string]any{
					"beeapi": map[string]any{
						"baseUrl": options.Endpoint + "/v1",
						"apiKey":  apiKey,
						"api":     "openai-responses",
						"models":  []any{modelConfig},
					},
				},
			},
		}, managed)
		return paths[:1], err
	case "hermes":
		if err := patchYAMLMappingFile(paths[0], "model", []yamlFieldPatch{
			{Key: "default", Value: strconv.Quote(model)},
			{Key: "provider", Value: strconv.Quote("custom")},
			{Key: "base_url", Value: strconv.Quote(options.Endpoint + "/v1")},
		}); err != nil {
			return nil, err
		}
		agentReasoning := yamlFieldPatch{Key: "reasoning_effort", Remove: true}
		if effort := reasoningEffortForAgent(options, agent); effort != "" {
			agentReasoning = yamlFieldPatch{Key: "reasoning_effort", Value: strconv.Quote(effort)}
		}
		if err := patchYAMLMappingFile(paths[0], "agent", []yamlFieldPatch{agentReasoning}); err != nil {
			return nil, err
		}
		if err := patchEnvFile(paths[1], []envFieldPatch{
			{Key: "OPENAI_API_KEY", Value: apiKey},
			{Key: "OPENAI_BASE_URL", Value: options.Endpoint + "/v1"},
			{Key: "HERMES_INFERENCE_MODEL", Value: model},
		}); err != nil {
			return nil, err
		}
		return paths, nil
	default:
		return nil, fmt.Errorf("未知智能体: %s", agent)
	}
}

func apiKeyForAgent(options Options, agent string) string {
	if options.APIKeys != nil {
		if key := strings.TrimSpace(options.APIKeys[agent]); key != "" {
			return key
		}
	}
	return strings.TrimSpace(options.APIKey)
}

func modelForAgent(options Options, agent string) string {
	if options.Models != nil {
		if model := strings.TrimSpace(options.Models[agent]); model != "" {
			return model
		}
	}
	return strings.TrimSpace(options.Model)
}

func reasoningEffortForAgent(options Options, agent string) string {
	if options.ReasoningEfforts == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(options.ReasoningEfforts[agent]))
}

func reasoningEffortValues(agent string) []string {
	switch agent {
	case "claude":
		return []string{"low", "medium", "high", "xhigh"}
	case "codex":
		return []string{"minimal", "low", "medium", "high", "xhigh"}
	case "gemini":
		return []string{"minimal", "low", "medium", "high"}
	case "grok":
		return []string{"low", "medium", "high", "xhigh"}
	case "opencode":
		return []string{"low", "medium", "high"}
	case "openclaw", "hermes":
		return []string{"minimal", "low", "medium", "high", "xhigh"}
	default:
		return nil
	}
}

func agentSupportsReasoningEffort(agent string) bool {
	return len(reasoningEffortValues(agent)) > 0
}

func validReasoningEffort(agent, value string) bool {
	wanted := strings.ToLower(strings.TrimSpace(value))
	for _, allowed := range reasoningEffortValues(agent) {
		if wanted == allowed {
			return true
		}
	}
	return false
}

func geminiThinkingConfig(model, effort string) map[string]any {
	model = strings.ToLower(strings.TrimSpace(model))
	effort = strings.ToLower(strings.TrimSpace(effort))
	if strings.HasPrefix(model, "gemini-2.5-") {
		budget := 8192
		switch effort {
		case "minimal":
			if strings.Contains(model, "-pro") {
				budget = 128
			} else {
				budget = 0
			}
		case "low":
			budget = 1024
		case "high":
			if strings.Contains(model, "-pro") {
				budget = 32768
			} else {
				budget = 24576
			}
		}
		return map[string]any{"thinkingBudget": budget}
	}
	return map[string]any{"thinkingLevel": strings.ToUpper(effort)}
}

func validClaudeDesktopModel(value string) bool {
	model := strings.ToLower(strings.TrimSpace(value))
	model = strings.TrimPrefix(model, "anthropic/")
	return strings.HasPrefix(model, "claude-sonnet-") || strings.HasPrefix(model, "claude-opus-") || strings.HasPrefix(model, "claude-haiku-")
}

func writeClaudeDesktop(paths []string, endpoint, apiKey, model string) error {
	if len(paths) != 4 {
		return errors.New("Claude Desktop 配置路径不完整")
	}
	if err := mergeJSON(paths[0], map[string]any{"deploymentMode": "3p"}); err != nil {
		return err
	}
	if err := mergeJSON(paths[1], map[string]any{"deploymentMode": "3p"}); err != nil {
		return err
	}
	if err := mergeClaudeDesktopMeta(paths[2]); err != nil {
		return err
	}
	return mergeJSON(paths[3], map[string]any{
		"inferenceProvider":          "gateway",
		"inferenceGatewayBaseUrl":    strings.TrimRight(endpoint, "/") + "/anthropic",
		"inferenceGatewayAuthScheme": "bearer",
		"inferenceGatewayApiKey":     apiKey,
		"inferenceModels":            []any{model},
	})
}

func mergeClaudeDesktopMeta(path string) error {
	current := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &current); err != nil {
				return fmt.Errorf("现有 Claude Desktop _meta.json 无法解析: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries := make([]any, 0)
	if existing, ok := current["entries"]; ok {
		var valid bool
		entries, valid = existing.([]any)
		if !valid {
			return errors.New("现有 Claude Desktop _meta.json 的 entries 不是数组")
		}
	}
	updated := false
	for index, entry := range entries {
		item, ok := entry.(map[string]any)
		if !ok || item["id"] != claudeDesktopProfileID {
			continue
		}
		item["name"] = "BeeAPI"
		item["provider"] = "gateway"
		entries[index] = item
		updated = true
		break
	}
	if !updated {
		entries = append(entries, map[string]any{
			"id": claudeDesktopProfileID, "name": "BeeAPI", "provider": "gateway",
		})
	}
	current["appliedId"] = claudeDesktopProfileID
	current["entries"] = entries
	raw, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return secureWrite(path, append(raw, '\n'))
}

type jsonPathPatch struct {
	Path   []string
	Value  any
	Remove bool
}

func mergeJSON(path string, patch map[string]any) error {
	return mergeJSONPaths(path, patch, nil)
}

func mergeJSONPaths(path string, patch map[string]any, managed []jsonPathPatch) error {
	current := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if len(strings.TrimSpace(string(b))) > 0 {
			if err := json.Unmarshal(b, &current); err != nil {
				return fmt.Errorf("现有 JSON 无法解析: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	deepMerge(current, patch)
	for _, item := range managed {
		if len(item.Path) == 0 {
			continue
		}
		if item.Remove {
			deleteJSONPath(current, item.Path)
			continue
		}
		setJSONPath(current, item.Path, item.Value)
	}
	b, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return secureWrite(path, append(b, '\n'))
}

func setJSONPath(root map[string]any, path []string, value any) {
	current := root
	for _, key := range path[:len(path)-1] {
		next, ok := current[key].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[key] = next
		}
		current = next
	}
	current[path[len(path)-1]] = value
}

func deleteJSONPath(root map[string]any, path []string) {
	current := root
	for _, key := range path[:len(path)-1] {
		next, ok := current[key].(map[string]any)
		if !ok {
			return
		}
		current = next
	}
	delete(current, path[len(path)-1])
}

func secureWrite(path string, data []byte) error {
	if err := state.AtomicWrite(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func deepMerge(dst, src map[string]any) {
	for key, value := range src {
		srcMap, srcOK := value.(map[string]any)
		dstMap, dstOK := dst[key].(map[string]any)
		if srcOK && dstOK {
			deepMerge(dstMap, srcMap)
			continue
		}
		dst[key] = value
	}
}
