package configurator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func TestApplyPreservesUnrelatedNativeConfigurationAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)

	originals := map[string]string{
		filepath.Join(home, ".gemini", ".env"): "# user comment\nCUSTOM_FLAG=keep\nGEMINI_API_KEY=old-key\n",
		filepath.Join(home, ".gemini", "settings.json"): `{
  "theme": "dark",
  "mcpServers": {"docs": {"command": "docs"}},
  "security": {"auth": {"selectedType": "oauth-personal", "otherAuth": "keep"}}
}
`,
		filepath.Join(home, ".grok", "config.toml"): `# user comment
[ui]
theme = "dark"

[models]
default = "old"
web_search = "search-model"

[model.beeapi]
model = "old"
base_url = "https://old.invalid/v1"
env_key = "OLD_KEY"

[model.other]
model = "other-model"
base_url = "https://other.invalid/v1"

[mcp_servers.docs]
command = "docs"
`,
		filepath.Join(home, ".hermes", "config.yaml"): `# user comment
model:
  default: old-model
  provider: old-provider
  base_url: https://old.invalid/v1
  context_length: 200000
agent:
  max_turns: 50
mcp_servers:
  docs:
    command: docs
`,
		filepath.Join(home, ".hermes", ".env"): "# user secret settings\nCUSTOM_SECRET=keep\nOPENAI_API_KEY=old-key\n",
	}
	for path, content := range originals {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	options := Options{
		Endpoint: "https://beeapi.dev",
		APIKey:   "sk-new-key",
		Models: map[string]string{
			"gemini": "gemini-model",
			"grok":   "grok-model",
			"hermes": "hermes-model",
		},
		Agents: []string{"gemini", "grok", "hermes"},
	}
	first, err := Apply(store, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Files) != 5 {
		t.Fatalf("configured files = %d, want 5: %#v", len(first.Files), first.Files)
	}

	assertFileContains(t, filepath.Join(home, ".gemini", ".env"),
		"# user comment", "CUSTOM_FLAG=keep", `GEMINI_API_KEY="sk-new-key"`, `GEMINI_MODEL="gemini-model"`)
	assertFileContains(t, filepath.Join(home, ".gemini", "settings.json"),
		`"theme": "dark"`, `"mcpServers"`, `"otherAuth": "keep"`, `"selectedType": "gemini-api-key"`)
	assertFileContains(t, filepath.Join(home, ".grok", "config.toml"),
		"# user comment", `[ui]`, `theme = "dark"`, `web_search = "search-model"`,
		`[model.other]`, `base_url = "https://other.invalid/v1"`, `[mcp_servers.docs]`,
		`api_key = "sk-new-key"`, `model = "grok-model"`)
	grok, err := os.ReadFile(filepath.Join(home, ".grok", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(grok), `env_key = "OLD_KEY"`) {
		t.Fatalf("stale Grok credential selector was not removed:\n%s", grok)
	}
	assertFileContains(t, filepath.Join(home, ".hermes", "config.yaml"),
		"# user comment", "context_length: 200000", "agent:", "max_turns: 50", "mcp_servers:",
		`default: "hermes-model"`, `provider: "custom"`, `base_url: "https://beeapi.dev/v1"`)
	assertFileContains(t, filepath.Join(home, ".hermes", ".env"),
		"# user secret settings", "CUSTOM_SECRET=keep", `OPENAI_API_KEY="sk-new-key"`,
		`OPENAI_BASE_URL="https://beeapi.dev/v1"`, `HERMES_INFERENCE_MODEL="hermes-model"`)

	afterFirst := map[string]string{}
	for path := range originals {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		afterFirst[path] = string(content)
	}
	second, err := Apply(store, options)
	if err != nil {
		t.Fatal(err)
	}
	for path, expected := range afterFirst {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(content) != expected {
			t.Fatalf("repeated configuration was not idempotent for %s:\n%s", path, content)
		}
	}
	if _, err := store.Rollback(second.BackupID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rollback(first.BackupID); err != nil {
		t.Fatal(err)
	}
	for path, expected := range originals {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(content) != expected {
			t.Fatalf("rollback did not restore %s exactly:\n%s", path, content)
		}
	}
}

func TestApplyRollsBackEarlierNativeWritesWhenLaterConfigIsInvalid(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)

	envPath := filepath.Join(home, ".gemini", ".env")
	settingsPath := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		t.Fatal(err)
	}
	originalEnv := "CUSTOM_FLAG=keep\n"
	originalSettings := "{ invalid json\n"
	if err := os.WriteFile(envPath, []byte(originalEnv), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(originalSettings), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Apply(store, Options{
		Endpoint: "https://beeapi.dev", APIKey: "sk-new", Model: "gemini-model", Agents: []string{"gemini"},
	})
	if err == nil {
		t.Fatal("invalid Gemini settings unexpectedly succeeded")
	}
	for path, expected := range map[string]string{envPath: originalEnv, settingsPath: originalSettings} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(content) != expected {
			t.Fatalf("automatic rollback did not restore %s exactly:\n%s", path, content)
		}
	}
}

func assertFileContains(t *testing.T, path string, fragments ...string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range fragments {
		if !strings.Contains(string(content), fragment) {
			t.Fatalf("%s is missing %q:\n%s", path, fragment, content)
		}
	}
}
