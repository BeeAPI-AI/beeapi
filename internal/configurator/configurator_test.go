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
	for _, want := range []string{"wire_api = \"responses\"", "command = \"/usr/local/bin/beeapi\"", "args = [\"token\", \"print\"]"} {
		if !strings.Contains(string(codex), want) {
			t.Fatalf("Codex profile is missing %q:\n%s", want, codex)
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
