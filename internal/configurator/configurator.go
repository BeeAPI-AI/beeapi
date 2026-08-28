package configurator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	shared := map[string]struct{ key, model string }{}
	home, err := targetHome()
	if err != nil {
		return Result{}, err
	}
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
	paths := targetPaths(home, agents)
	backup, err := store.CreateBackup(paths)
	if err != nil {
		return Result{}, fmt.Errorf("创建配置备份: %w", err)
	}
	result := Result{BackupID: backup.ID}
	written := map[string]bool{}
	for _, agent := range agents {
		path := pathForAgent(home, agent)
		if written[path] {
			appendHint(&result, agent)
			continue
		}
		if err := writeAgent(path, agent, options); err != nil {
			_, _ = store.Rollback(backup.ID)
			return Result{}, fmt.Errorf("写入 %s 配置失败（已自动回滚）: %w", agent, err)
		}
		written[path] = true
		result.Files = append(result.Files, path)
		appendHint(&result, agent)
	}
	return result, nil
}

func appendHint(result *Result, agent string) {
	switch agent {
	case "claude-desktop":
		result.Hints = append(result.Hints, "Claude Desktop: 打开 Code 标签页（与 Claude Code 共享配置）")
	case "codex":
		result.Hints = append(result.Hints, "Codex: codex --profile beeapi（或 beeapi run codex）")
	case "gemini":
		result.Hints = append(result.Hints, "Gemini CLI: beeapi run gemini")
	case "grok":
		result.Hints = append(result.Hints, "Grok Build: beeapi run grok")
	case "hermes":
		result.Hints = append(result.Hints, "Hermes: beeapi run hermes")
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
	paths := make([]string, 0, len(agents))
	for _, agent := range agents {
		paths = append(paths, pathForAgent(home, agent))
	}
	sort.Strings(paths)
	return paths
}

func pathForAgent(home, agent string) string {
	switch agent {
	case "claude":
		return filepath.Join(home, ".claude", "settings.json")
	case "claude-desktop":
		// Claude Desktop Code sessions and Claude Code share this settings file.
		return filepath.Join(home, ".claude", "settings.json")
	case "codex":
		return filepath.Join(home, ".codex", "beeapi.config.toml")
	case "gemini":
		return filepath.Join(home, ".config", "getbeeapi", "gemini.env")
	case "grok":
		return filepath.Join(home, ".config", "getbeeapi", "grok", "config.toml")
	case "opencode":
		return filepath.Join(home, ".config", "opencode", "opencode.json")
	case "openclaw":
		return filepath.Join(home, ".openclaw", "openclaw.json")
	case "hermes":
		return filepath.Join(home, ".config", "getbeeapi", "hermes", "config.yaml")
	default:
		return filepath.Join(home, ".config", "getbeeapi", agent+".json")
	}
}

func writeAgent(path, agent string, options Options) error {
	model := modelForAgent(options, agent)
	apiKey := apiKeyForAgent(options, agent)
	switch agent {
	case "claude", "claude-desktop":
		return mergeJSON(path, map[string]any{
			"env": map[string]any{
				"ANTHROPIC_AUTH_TOKEN":           apiKey,
				"ANTHROPIC_BASE_URL":             options.Endpoint + "/anthropic",
				"ANTHROPIC_MODEL":                model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":  model,
				"ANTHROPIC_DEFAULT_SONNET_MODEL": model,
				"ANTHROPIC_DEFAULT_OPUS_MODEL":   model,
			},
		})
	case "codex":
		binary := strings.TrimSpace(options.BinaryPath)
		if binary == "" {
			binary = "beeapi"
		}
		content := `# Managed by GetBeeAPI. The official ChatGPT login in auth.json is untouched.
model_provider = "beeapi"
model = ` + strconv.Quote(model) + `

[model_providers.beeapi]
name = "BeeAPI"
base_url = ` + strconv.Quote(options.Endpoint+"/v1") + `
wire_api = "responses"

[model_providers.beeapi.auth]
command = ` + strconv.Quote(binary) + `
args = ["token", "print", "--agent", "codex"]
refresh_interval_ms = 0
timeout_ms = 5000
`
		return secureWrite(path, []byte(content))
	case "gemini":
		content := "# Managed by GetBeeAPI; use with: beeapi run gemini\n" +
			"GOOGLE_GEMINI_BASE_URL=" + shellQuote(options.Endpoint) + "\n" +
			"GEMINI_API_KEY=" + shellQuote(apiKey) + "\n" +
			"GEMINI_MODEL=" + shellQuote(model) + "\n"
		return secureWrite(path, []byte(content))
	case "grok":
		content := `# Managed by GetBeeAPI; use with: beeapi run grok
[model.beeapi]
model = ` + strconv.Quote(model) + `
base_url = ` + strconv.Quote(options.Endpoint+"/v1") + `
name = ` + strconv.Quote("BeeAPI · "+model) + `
env_key = "BEEAPI_API_KEY"
api_backend = "responses"

[models]
default = "beeapi"
`
		return secureWrite(path, []byte(content))
	case "opencode":
		return mergeJSON(path, map[string]any{
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
	case "openclaw":
		return mergeJSON(path, map[string]any{
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
	case "hermes":
		content := "# Managed by GetBeeAPI; use with: beeapi run hermes\n" +
			"model:\n" +
			"  default: " + strconv.Quote(model) + "\n" +
			"  provider: custom\n" +
			"  base_url: " + strconv.Quote(options.Endpoint+"/v1") + "\n" +
			"  api_mode: chat_completions\n"
		return secureWrite(path, []byte(content))
	default:
		return fmt.Errorf("未知智能体: %s", agent)
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

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
