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
	codexPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	codexOriginal := []byte("# user comment\napproval_policy = \"on-request\"\n\n[mcp_servers.docs]\ncommand = \"docs-server\"\n")
	if err := os.WriteFile(codexPath, codexOriginal, 0o600); err != nil {
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
	for _, want := range []string{"# user comment", `approval_policy = "on-request"`, "[mcp_servers.docs]", `command = "docs-server"`} {
		if !strings.Contains(string(codex), want) {
			t.Fatalf("Codex unrelated configuration was not preserved (%q):\n%s", want, codex)
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
	restoredCodex, err := os.ReadFile(codexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredCodex) != string(codexOriginal) {
		t.Fatalf("Codex config did not roll back exactly:\n%s", restoredCodex)
	}
}

func TestApplyWritesCodexReasoningWithoutChangingUnrelatedSettings(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)
	codexPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "approval_policy = \"never\"\n\n[mcp_servers.local]\ncommand = \"server\"\n"
	if err := os.WriteFile(codexPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(store, Options{
		Endpoint: "https://beeapi.dev", APIKey: "sk-secret", Agents: []string{"codex"},
		Models: map[string]string{"codex": "gpt-5.6-sol"}, ReasoningEfforts: map[string]string{"codex": "high"},
		BinaryPath: "beeapi",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(codexPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`model_reasoning_effort = "high"`, `approval_policy = "never"`, `[mcp_servers.local]`, `command = "server"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("Codex config is missing %q:\n%s", want, raw)
		}
	}
}

func TestApplyRejectsReasoningForUnsupportedTool(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GETBEE_TARGET_HOME", filepath.Join(root, "home"))
	_, err := Apply(&state.Store{Dir: filepath.Join(root, "state")}, Options{
		Endpoint: "https://beeapi.dev", APIKey: "sk-secret", Agents: []string{"opencode"},
		Models: map[string]string{"opencode": "gpt-5.6"}, ReasoningEfforts: map[string]string{"opencode": "xhigh"},
	})
	if err == nil || !strings.Contains(err.Error(), "无效") {
		t.Fatalf("unexpected reasoning validation result: %v", err)
	}
}

func TestApplyWritesNativeReasoningSettingsForEverySupportedCLI(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)

	agents := []string{"claude", "codex", "gemini", "grok", "opencode", "openclaw", "hermes"}
	models := map[string]string{
		"claude": "claude-sonnet-4-6", "codex": "gpt-5.6-sol", "gemini": "gemini-3.1-pro-preview",
		"grok": "gpt-5.6-sol", "opencode": "gpt-5.6", "openclaw": "gpt-5.6-sol", "hermes": "gpt-5.6",
	}
	efforts := map[string]string{
		"claude": "high", "codex": "xhigh", "gemini": "low", "grok": "high",
		"opencode": "medium", "openclaw": "xhigh", "hermes": "minimal",
	}
	if _, err := Apply(store, Options{
		Endpoint: "https://beeapi.dev", APIKey: "sk-reasoning", Agents: agents,
		Models: models, ReasoningEfforts: efforts, BinaryPath: "beeapi",
	}); err != nil {
		t.Fatal(err)
	}

	checks := map[string][]string{
		filepath.Join(home, ".claude", "settings.json"): {
			`"effortLevel": "high"`, `"ANTHROPIC_MODEL": "claude-sonnet-4-6"`,
		},
		filepath.Join(home, ".codex", "config.toml"): {
			`model_reasoning_effort = "xhigh"`,
		},
		filepath.Join(home, ".gemini", ".env"): {
			`GEMINI_MODEL="getbeeapi"`,
		},
		filepath.Join(home, ".gemini", "settings.json"): {
			`"getbeeapi"`, `"model": "gemini-3.1-pro-preview"`, `"thinkingLevel": "LOW"`,
		},
		filepath.Join(home, ".grok", "config.toml"): {
			`reasoning_effort = "high"`, `supports_reasoning_effort = true`, `reasoning_efforts = ["low", "medium", "high", "xhigh"]`,
		},
		filepath.Join(home, ".config", "opencode", "opencode.json"): {
			`"reasoning": true`, `"reasoningEffort": "medium"`,
		},
		filepath.Join(home, ".openclaw", "openclaw.json"): {
			`"thinkingDefault": "xhigh"`, `"supportedReasoningEfforts"`,
		},
		filepath.Join(home, ".hermes", "config.yaml"): {
			`reasoning_effort: "minimal"`,
		},
	}
	for path, wanted := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range wanted {
			if !strings.Contains(string(raw), item) {
				t.Fatalf("%s is missing %q:\n%s", path, item, raw)
			}
		}
	}
}

func TestGemini25ReasoningUsesNativeThinkingBudget(t *testing.T) {
	if got := geminiThinkingConfig("gemini-2.5-pro", "minimal")["thinkingBudget"]; got != 128 {
		t.Fatalf("Gemini 2.5 Pro minimal budget = %#v, want 128", got)
	}
	if got := geminiThinkingConfig("gemini-2.5-flash", "high")["thinkingBudget"]; got != 24576 {
		t.Fatalf("Gemini 2.5 Flash high budget = %#v, want 24576", got)
	}
}

func TestApplyRemovesManagedReasoningWhenNextProfileDoesNotSelectIt(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)
	agents := []string{"claude", "codex", "gemini", "grok", "opencode", "openclaw", "hermes"}
	models := map[string]string{
		"claude": "claude-sonnet-4-6", "codex": "gpt-5.6-sol", "gemini": "gemini-3.1-pro-preview",
		"grok": "gpt-5.6-sol", "opencode": "gpt-5.6", "openclaw": "gpt-5.6-sol", "hermes": "gpt-5.6",
	}
	if _, err := Apply(store, Options{
		Endpoint: "https://beeapi.dev", APIKey: "sk-reasoning", Agents: agents, Models: models,
		ReasoningEfforts: map[string]string{
			"claude": "high", "codex": "high", "gemini": "high", "grok": "high",
			"opencode": "high", "openclaw": "high", "hermes": "high",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(store, Options{
		Endpoint: "https://beeapi.dev", APIKey: "sk-plain", Agents: agents, Models: models,
	}); err != nil {
		t.Fatal(err)
	}

	checks := map[string][]string{
		filepath.Join(home, ".claude", "settings.json"):             {`"effortLevel"`},
		filepath.Join(home, ".codex", "config.toml"):                {"model_reasoning_effort"},
		filepath.Join(home, ".gemini", "settings.json"):             {`"getbeeapi"`, `"thinkingLevel"`},
		filepath.Join(home, ".grok", "config.toml"):                 {"reasoning_effort", "supports_reasoning_effort", "reasoning_efforts"},
		filepath.Join(home, ".config", "opencode", "opencode.json"): {`"reasoning": true`, `"reasoningEffort"`},
		filepath.Join(home, ".openclaw", "openclaw.json"):           {`"thinkingDefault"`, `"supportedReasoningEfforts"`},
		filepath.Join(home, ".hermes", "config.yaml"):               {"reasoning_effort:"},
	}
	for path, forbidden := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range forbidden {
			if strings.Contains(string(raw), item) {
				t.Fatalf("%s retained stale managed reasoning field %q:\n%s", path, item, raw)
			}
		}
	}
	geminiEnv, err := os.ReadFile(filepath.Join(home, ".gemini", ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(geminiEnv), `GEMINI_MODEL="gemini-3.1-pro-preview"`) {
		t.Fatalf("Gemini did not restore the real model after removing its managed alias:\n%s", geminiEnv)
	}
}

func TestApplySupportsGrokAndHermesUsingNativeConfigs(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)

	result, err := Apply(store, Options{
		Endpoint: "https://beeapi.dev/",
		APIKey:   "sk-secret-value",
		Models: map[string]string{
			"grok":   "gpt-5-codex",
			"hermes": "hermes-4",
		},
		Agents: []string{"grok", "hermes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 3 {
		t.Fatalf("configured files = %d, want 3: %#v", len(result.Files), result.Files)
	}

	grokPath := filepath.Join(home, ".grok", "config.toml")
	grok, err := os.ReadFile(grokPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[model.beeapi]", `base_url = "https://beeapi.dev/v1"`, `api_key = "sk-secret-value"`, `default = "beeapi"`} {
		if !strings.Contains(string(grok), want) {
			t.Fatalf("Grok profile missing %q:\n%s", want, grok)
		}
	}

	hermesPath := filepath.Join(home, ".hermes", "config.yaml")
	hermes, err := os.ReadFile(hermesPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`default: "hermes-4"`, `provider: "custom"`, `base_url: "https://beeapi.dev/v1"`} {
		if !strings.Contains(string(hermes), want) {
			t.Fatalf("Hermes profile missing %q:\n%s", want, hermes)
		}
	}
	if strings.Contains(string(hermes), "sk-secret-value") {
		t.Fatal("Hermes config.yaml contains the API key")
	}
	hermesEnvPath := filepath.Join(home, ".hermes", ".env")
	hermesEnv, err := os.ReadFile(hermesEnvPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`OPENAI_API_KEY="sk-secret-value"`, `OPENAI_BASE_URL="https://beeapi.dev/v1"`, `HERMES_INFERENCE_MODEL="hermes-4"`} {
		if !strings.Contains(string(hermesEnv), want) {
			t.Fatalf("Hermes .env missing %q:\n%s", want, hermesEnv)
		}
	}

	if _, err := store.Rollback(result.BackupID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{grokPath, hermesPath, hermesEnvPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("new profile was not removed during rollback: %s: %v", path, err)
		}
	}
}

func TestClaudeDesktopUsesSeparate3PConfiguration(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	localAppData := filepath.Join(home, "AppData", "Local")
	t.Setenv("LOCALAPPDATA", localAppData)
	paths := claudeDesktopPaths(home, "windows")
	if len(paths) != 4 || strings.Contains(paths[0], ".claude") {
		t.Fatalf("Claude Desktop paths are not independent: %#v", paths)
	}
	if err := writeClaudeDesktop(paths, "https://beeapi.ai", "sk-desktop", "claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths[:2] {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"deploymentMode": "3p"`) {
			t.Fatalf("Desktop deployment mode missing from %s:\n%s", path, raw)
		}
	}
	meta, err := os.ReadFile(paths[2])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), claudeDesktopProfileID) || !strings.Contains(string(meta), `"name": "BeeAPI"`) {
		t.Fatalf("Desktop profile registry is incomplete:\n%s", meta)
	}
	profile, err := os.ReadFile(paths[3])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"inferenceProvider": "gateway"`, `"inferenceGatewayBaseUrl": "https://beeapi.ai/anthropic"`, `"inferenceGatewayApiKey": "sk-desktop"`, `"claude-sonnet-4-6"`} {
		if !strings.Contains(string(profile), want) {
			t.Fatalf("Desktop profile missing %q:\n%s", want, profile)
		}
	}
}

func TestApplyWritesEverySupportedCLIAdapter(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)

	models := map[string]string{}
	agents := make([]string, 0, len(SupportedAgents)-1)
	for _, agent := range SupportedAgents {
		if agent == "claude-desktop" {
			continue
		}
		agents = append(agents, agent)
		models[agent] = "bee-model-" + agent
	}
	result, err := Apply(store, Options{
		Endpoint:   "https://beeapi.ai",
		APIKey:     "sk-all-tools",
		Models:     models,
		Agents:     agents,
		BinaryPath: "/opt/getbeeapi/beeapi",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Gemini and Hermes each need one additional native secrets/settings file.
	if len(result.Files) != len(agents)+2 {
		t.Fatalf("configured files = %d, want %d: %#v", len(result.Files), len(agents)+2, result.Files)
	}

	checks := map[string][]string{
		filepath.Join(home, ".claude", "settings.json"): {
			"ANTHROPIC_BASE_URL", "https://beeapi.ai/anthropic", "sk-all-tools", "bee-model-claude",
		},
		filepath.Join(home, ".codex", "config.toml"): {
			`model_provider = "beeapi"`, `base_url = "https://beeapi.ai/v1"`, `command = "/opt/getbeeapi/beeapi"`,
		},
		filepath.Join(home, ".gemini", ".env"): {
			`GOOGLE_GEMINI_BASE_URL="https://beeapi.ai"`, `GEMINI_API_KEY="sk-all-tools"`, "bee-model-gemini",
		},
		filepath.Join(home, ".gemini", "settings.json"): {
			`"selectedType": "gemini-api-key"`,
		},
		filepath.Join(home, ".grok", "config.toml"): {
			"[model.beeapi]", `api_key = "sk-all-tools"`, "bee-model-grok",
		},
		filepath.Join(home, ".config", "opencode", "opencode.json"): {
			`"beeapi"`, `"baseURL": "https://beeapi.ai/v1"`, `"apiKey": "sk-all-tools"`, "bee-model-opencode",
		},
		filepath.Join(home, ".openclaw", "openclaw.json"): {
			`"baseUrl": "https://beeapi.ai/v1"`, `"apiKey": "sk-all-tools"`, `"api": "openai-responses"`, "bee-model-openclaw",
		},
		filepath.Join(home, ".hermes", "config.yaml"): {
			`provider: "custom"`, `base_url: "https://beeapi.ai/v1"`, "bee-model-hermes",
		},
		filepath.Join(home, ".hermes", ".env"): {
			`OPENAI_API_KEY="sk-all-tools"`, `OPENAI_BASE_URL="https://beeapi.ai/v1"`, "bee-model-hermes",
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

	for _, path := range []string{filepath.Join(home, ".codex", "config.toml"), filepath.Join(home, ".hermes", "config.yaml")} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(content), "sk-all-tools") {
			t.Errorf("command/env-backed adapter leaked the API Key into %s", path)
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
