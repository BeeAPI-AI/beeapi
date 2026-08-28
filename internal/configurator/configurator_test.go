package configurator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func TestApplyMergesBacksUpAndRollsBack(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)

	claudePath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"permissions\": {\"allow\": [\"Read\"]}\n}\n")
	if err := os.WriteFile(claudePath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(store, Options{
		Endpoint:   "https://beeapi.dev/",
		APIKey:     "sk-secret-value",
		Models:     map[string]string{"claude": "claude-sonnet", "codex": "gpt-5-codex"},
		Agents:     []string{"claude", "codex"},
		BinaryPath: "/usr/local/bin/beeapi",
	})
	if err != nil {
		t.Fatal(err)
	}

	var claude map[string]any
	b, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &claude); err != nil {
		t.Fatal(err)
	}
	if claude["permissions"] == nil || claude["env"] == nil {
		t.Fatalf("existing Claude settings were not preserved: %s", b)
	}
	if info, err := os.Stat(claudePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("Claude settings permissions = %v, %v; want 0600", info, err)
	}

	codexPath := filepath.Join(home, ".codex", "beeapi.config.toml")
	codex, err := os.ReadFile(codexPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(codex), "sk-secret-value") {
		t.Fatal("Codex profile contains the API key instead of command-backed auth")
	}
	for _, want := range []string{"wire_api = \"responses\"", "command = \"/usr/local/bin/beeapi\"", "args = [\"token\", \"print\", \"--agent\", \"codex\"]"} {
		if !strings.Contains(string(codex), want) {
			t.Fatalf("Codex profile is missing %q:\n%s", want, codex)
		}
	}
	for _, unsupported := range []string{"disable_response_storage", "model_reasoning_effort"} {
		if strings.Contains(string(codex), unsupported) {
			t.Fatalf("Codex profile contains an unnecessary or unsupported setting %q:\n%s", unsupported, codex)
		}
	}

	if _, err := store.Rollback(result.BackupID); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("Claude settings did not roll back exactly:\n%s", restored)
	}
	if _, err := os.Stat(codexPath); !os.IsNotExist(err) {
		t.Fatalf("new Codex profile was not removed during rollback: %v", err)
	}
}

func TestApplySupportsDesktopGrokAndHermesWithoutWritingKeysToProfiles(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)

	claudePath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"permissions\": {\"allow\": [\"Read\"]}\n}\n")
	if err := os.WriteFile(claudePath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(store, Options{
		Endpoint: "https://beeapi.dev/",
		APIKey:   "sk-secret-value",
		Models: map[string]string{
			"claude-desktop": "claude-sonnet",
			"grok":           "gpt-5-codex",
			"hermes":         "hermes-4",
		},
		Agents: []string{"claude-desktop", "grok", "hermes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 3 {
		t.Fatalf("configured files = %d, want 3: %#v", len(result.Files), result.Files)
	}

	claude, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"permissions", "ANTHROPIC_BASE_URL", "claude-sonnet"} {
		if !strings.Contains(string(claude), want) {
			t.Fatalf("Claude Desktop shared settings missing %q:\n%s", want, claude)
		}
	}

	grokPath := filepath.Join(home, ".config", "getbeeapi", "grok", "config.toml")
	grok, err := os.ReadFile(grokPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[model.beeapi]", `base_url = "https://beeapi.dev/v1"`, `env_key = "BEEAPI_API_KEY"`, `default = "beeapi"`} {
		if !strings.Contains(string(grok), want) {
			t.Fatalf("Grok profile missing %q:\n%s", want, grok)
		}
	}
	if strings.Contains(string(grok), "sk-secret-value") {
		t.Fatal("Grok profile contains the API key")
	}

	hermesPath := filepath.Join(home, ".config", "getbeeapi", "hermes", "config.yaml")
	hermes, err := os.ReadFile(hermesPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`default: "hermes-4"`, "provider: custom", `base_url: "https://beeapi.dev/v1"`, "api_mode: chat_completions"} {
		if !strings.Contains(string(hermes), want) {
			t.Fatalf("Hermes profile missing %q:\n%s", want, hermes)
		}
	}
	if strings.Contains(string(hermes), "sk-secret-value") {
		t.Fatal("Hermes profile contains the API key")
	}

	if _, err := store.Rollback(result.BackupID); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("Claude Desktop settings did not roll back exactly:\n%s", restored)
	}
	for _, path := range []string{grokPath, hermesPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("new profile was not removed during rollback: %s: %v", path, err)
		}
	}
}

func TestApplyDeduplicatesClaudeSharedSettings(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)

	result, err := Apply(store, Options{
		Endpoint: "https://beeapi.ai",
		APIKey:   "sk-test",
		Models: map[string]string{
			"claude":         "claude-sonnet",
			"claude-desktop": "claude-sonnet",
		},
		Agents: []string{"claude", "claude-desktop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("shared Claude configuration should be written once, got %#v", result.Files)
	}
	if len(result.Hints) != 1 || !strings.Contains(result.Hints[0], "Claude Desktop") {
		t.Fatalf("desktop hint missing: %#v", result.Hints)
	}
}

func TestApplyWritesEverySupportedToolAdapter(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)

	models := map[string]string{}
	for _, agent := range SupportedAgents {
		models[agent] = "bee-model-" + agent
	}
	models["claude-desktop"] = models["claude"]
	result, err := Apply(store, Options{
		Endpoint:   "https://beeapi.ai",
		APIKey:     "sk-all-tools",
		Models:     models,
		Agents:     append([]string(nil), SupportedAgents...),
		BinaryPath: "/opt/getbeeapi/beeapi",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Claude Code and Claude Desktop intentionally share one settings file.
	if len(result.Files) != len(SupportedAgents)-1 {
		t.Fatalf("configured files = %d, want %d: %#v", len(result.Files), len(SupportedAgents)-1, result.Files)
	}

	checks := map[string][]string{
		filepath.Join(home, ".claude", "settings.json"): {
			"ANTHROPIC_BASE_URL", "https://beeapi.ai/anthropic", "sk-all-tools", "bee-model-claude",
		},
		filepath.Join(home, ".codex", "beeapi.config.toml"): {
			`model_provider = "beeapi"`, `base_url = "https://beeapi.ai/v1"`, `command = "/opt/getbeeapi/beeapi"`,
		},
		filepath.Join(home, ".config", "getbeeapi", "gemini.env"): {
			"GOOGLE_GEMINI_BASE_URL='https://beeapi.ai'", "GEMINI_API_KEY='sk-all-tools'", "bee-model-gemini",
		},
		filepath.Join(home, ".config", "getbeeapi", "grok", "config.toml"): {
			"[model.beeapi]", `env_key = "BEEAPI_API_KEY"`, "bee-model-grok",
		},
		filepath.Join(home, ".config", "opencode", "opencode.json"): {
			`"beeapi"`, `"baseURL": "https://beeapi.ai/v1"`, `"apiKey": "sk-all-tools"`, "bee-model-opencode",
		},
		filepath.Join(home, ".openclaw", "openclaw.json"): {
			`"baseUrl": "https://beeapi.ai/v1"`, `"apiKey": "sk-all-tools"`, `"api": "openai-responses"`, "bee-model-openclaw",
		},
		filepath.Join(home, ".config", "getbeeapi", "hermes", "config.yaml"): {
			"provider: custom", `base_url: "https://beeapi.ai/v1"`, "bee-model-hermes",
		},
	}
	for path, fragments := range checks {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(content), fragment) {
				t.Errorf("%s is missing %q:\n%s", path, fragment, content)
			}
		}
	}

	for _, path := range []string{
		filepath.Join(home, ".codex", "beeapi.config.toml"),
		filepath.Join(home, ".config", "getbeeapi", "grok", "config.toml"),
		filepath.Join(home, ".config", "getbeeapi", "hermes", "config.yaml"),
	} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(content), "sk-all-tools") {
			t.Errorf("wrapper-backed adapter leaked the API Key into %s", path)
		}
	}
}

func TestApplyUsesCredentialAssignedToEachTool(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)

	_, err := Apply(store, Options{
		Endpoint: "https://beeapi.ai",
		APIKeys: map[string]string{
			"claude":   "sk-claude-only",
			"opencode": "sk-opencode-only",
		},
		Models: map[string]string{
			"claude":   "claude-sonnet",
			"opencode": "gpt-5-codex",
		},
		Agents: []string{"claude", "opencode"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claude, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	opencode, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(claude), "sk-claude-only") || strings.Contains(string(claude), "sk-opencode-only") {
		t.Fatalf("Claude received the wrong credential:\n%s", claude)
	}
	if !strings.Contains(string(opencode), "sk-opencode-only") || strings.Contains(string(opencode), "sk-claude-only") {
		t.Fatalf("OpenCode received the wrong credential:\n%s", opencode)
	}
}
