package configurator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func TestApplyRemovesLegacyShellHookAndRollbackRestoresIt(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)
	t.Setenv("SHELL", "/bin/bash")

	profilePath := filepath.Join(home, ".bashrc")
	original := "# user setting\nexport EDITOR=vim\n\n" +
		directCommandsStart + "\n" +
		"if [ -r '/tmp/getbeeapi-commands.sh' ]; then\n" +
		"  . '/tmp/getbeeapi-commands.sh'\n" +
		"fi\n" +
		directCommandsEnd + "\n"
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(store, Options{
		Endpoint: "https://beeapi.dev",
		APIKey:   "sk-secret",
		Model:    "gpt-5.6-sol",
		Agents:   []string{"codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(profile), directCommandsStart) || !strings.Contains(string(profile), "export EDITOR=vim") {
		t.Fatalf("legacy hook was not removed cleanly:\n%s", profile)
	}
	if !containsSubstring(result.Hints, "旧版 Shell 命令注入已移除") {
		t.Fatalf("migration hint missing: %#v", result.Hints)
	}

	if _, err := store.Rollback(result.BackupID); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != original {
		t.Fatalf("rollback did not restore the shell profile exactly:\n%s", restored)
	}
}

func TestLegacyShellProfilesRejectsBrokenMarkers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHELL", "/bin/bash")
	path := filepath.Join(home, ".bashrc")
	original := "export PATH=/bin\n" + directCommandsStart + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyShellProfiles(home); err == nil {
		t.Fatal("broken managed block was unexpectedly accepted")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != original {
		t.Fatalf("broken profile changed despite the error:\n%s", current)
	}
}

func containsSubstring(values []string, target string) bool {
	for _, value := range values {
		if strings.Contains(value, target) {
			return true
		}
	}
	return false
}
