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
	Endpoint   string
	APIKey     string
	APIKeys    map[string]string
	Model      string
	Models     map[string]string
	Agents     []string
	BinaryPath string
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

	// Claude Code and Claude Desktop deliberately share ~/.claude/settings.json.
	// Refuse ambiguous assignments instead of allowing the last writer to win.
	shared := map[string]struct{ key, model string }{}
	for _, agent := range agents {
		key := apiKeyForAgent(options, agent)
		model := modelForAgent(options, agent)
		if key == "" {
			return Result{}, fmt.Errorf("没有为 %s 选择 API Key", agent)
		}
		if model == "" {
			return Result{}, fmt.Errorf("没有为 %s 选择模型", agent)
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
		result.Hints = append(result.Hints, "Claude Desktop: 直接打开 Code 标签页")
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
	case "claude", "claude-desktop":
		return []string{filepath.Join(home, ".claude", "settings.json")}
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

func pathForAgent(home, agent string) string {
	return pathsForAgent(home, agent)[0]
}

func writeAgent(home, agent string, options Options) ([]string, error) {
	model := modelForAgent(options, agent)
	apiKey := apiKeyForAgent(options, agent)
	paths := pathsForAgent(home, agent)
	switch agent {
	case "claude", "claude-desktop":
		err := mergeJSON(paths[0], map[string]any{
			"env": map[string]any{
				"ANTHROPIC_AUTH_TOKEN":           apiKey,
				"ANTHROPIC_BASE_URL":             options.Endpoint + "/anthropic",
				"ANTHROPIC_MODEL":                model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":  model,
				"ANTHROPIC_DEFAULT_SONNET_MODEL": model,
				"ANTHROPIC_DEFAULT_OPUS_MODEL":   model,
			},
		})
		return paths[:1], err
	case "codex":
		binary := strings.TrimSpace(options.BinaryPath)
		if binary == "" {
			binary = "beeapi"
		}
		err := patchTOMLFile(paths[0],
			[]tomlFieldPatch{
				setTOMLField("model_provider", strconv.Quote("beeapi")),
				setTOMLField("model", strconv.Quote(model)),
			},
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
		if err := patchEnvFile(paths[0], []envFieldPatch{
			{Key: "GOOGLE_GEMINI_BASE_URL", Value: options.Endpoint},
			{Key: "GEMINI_API_KEY", Value: apiKey},
			{Key: "GEMINI_MODEL", Value: model},
		}); err != nil {
			return nil, err
		}
		if err := mergeJSON(paths[1], map[string]any{
			"security": map[string]any{"auth": map[string]any{"selectedType": "gemini-api-key"}},
		}); err != nil {
			return nil, err
		}
		return paths, nil
	case "grok":
		err := patchTOMLFile(paths[0], nil, []tomlSectionPatch{
			{Name: "model.beeapi", Fields: []tomlFieldPatch{
				setTOMLField("model", strconv.Quote(model)),
				setTOMLField("base_url", strconv.Quote(options.Endpoint+"/v1")),
				setTOMLField("name", strconv.Quote("BeeAPI · "+model)),
				setTOMLField("api_key", strconv.Quote(apiKey)),
				removeTOMLField("env_key"),
				removeTOMLField("auth_provider"),
				setTOMLField("api_backend", strconv.Quote("responses")),
			}},
			{Name: "models", Fields: []tomlFieldPatch{setTOMLField("default", strconv.Quote("beeapi"))}},
		})
		return paths[:1], err
	case "opencode":
		err := mergeJSON(paths[0], map[string]any{
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
					"models": map[string]any{model: map[string]any{"name": model}},
				},
			},
		})
		return paths[:1], err
	case "openclaw":
		err := mergeJSON(paths[0], map[string]any{
			"agents": map[string]any{"defaults": map[string]any{"model": map[string]any{"primary": "beeapi/" + model}}},
			"models": map[string]any{
				"mode": "merge",
				"providers": map[string]any{
					"beeapi": map[string]any{
						"baseUrl": options.Endpoint + "/v1",
						"apiKey":  apiKey,
						"api":     "openai-responses",
						"models":  []any{map[string]any{"id": model, "name": model}},
					},
				},
			},
		})
		return paths[:1], err
	case "hermes":
		if err := patchYAMLMappingFile(paths[0], "model", []yamlFieldPatch{
			{Key: "default", Value: strconv.Quote(model)},
			{Key: "provider", Value: strconv.Quote("custom")},
			{Key: "base_url", Value: strconv.Quote(options.Endpoint + "/v1")},
		}); err != nil {
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

func mergeJSON(path string, patch map[string]any) error {
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
	b, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return secureWrite(path, append(b, '\n'))
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
