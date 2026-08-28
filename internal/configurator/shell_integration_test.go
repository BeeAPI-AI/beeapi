package configurator

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func TestApplyInstallsIdempotentDirectLaunchCommands(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	store := &state.Store{Dir: filepath.Join(root, "state")}
	t.Setenv("GETBEE_TARGET_HOME", home)
	t.Setenv("SHELL", "/bin/bash")

	profilePath := filepath.Join(home, ".bashrc")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, []byte("# user setting\nexport EDITOR=vim\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := Apply(store, Options{
		Endpoint: "https://beeapi.dev",
		APIKey:   "sk-secret",
		Models: map[string]string{
			"codex": "gpt-5.6-sol", "gemini": "gemini-2.5-pro",
		},
		Agents: []string{"codex", "gemini"}, DirectLaunch: true,
		BinaryPath: "/usr/local/bin/beeapi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ShellProfile != profilePath {
		t.Fatalf("shell profile = %q, want %q", first.ShellProfile, profilePath)
	}

	profile, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	profileText := string(profile)
	if !strings.Contains(profileText, "# user setting\nexport EDITOR=vim") {
		t.Fatalf("existing shell settings were not preserved:\n%s", profileText)
	}
	if strings.Count(profileText, directCommandsStart) != 1 || strings.Count(profileText, directCommandsEnd) != 1 {
		t.Fatalf("managed shell hook is missing or duplicated:\n%s", profileText)
	}

	commandsPath := filepath.Join(store.Dir, "shell", "commands.sh")
	commands, err := os.ReadFile(commandsPath)
	if err != nil {
		t.Fatal(err)
	}
	commandsText := string(commands)
	for _, want := range []string{"codex() {", `command beeapi run codex "$@"`, "gemini() {", `command beeapi run gemini "$@"`} {
		if !strings.Contains(commandsText, want) {
			t.Fatalf("direct commands are missing %q:\n%s", want, commandsText)
		}
	}
	if strings.Contains(commandsText, "sk-secret") {
		t.Fatalf("direct commands leaked the API Key:\n%s", commandsText)
	}
	for _, want := range []string{"Codex: codex", "Gemini CLI: gemini", "新终端生效", "当前终端立即启用: . '" + profilePath + "'"} {
		if !containsSubstring(first.Hints, want) {
			t.Fatalf("direct launch hint is missing %q: %#v", want, first.Hints)
		}
	}

	second, err := Apply(store, Options{
		Endpoint: "https://beeapi.dev", APIKey: "sk-secret", Model: "gpt-5.6-sol",
		Agents: []string{"codex"}, DirectLaunch: true, BinaryPath: "/usr/local/bin/beeapi",
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err = os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(profile), directCommandsStart) != 1 {
		t.Fatalf("a repeated configuration duplicated the shell hook:\n%s", profile)
	}
	commands, err = os.ReadFile(commandsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "gemini()") {
		t.Fatalf("an unconfigured tool kept its direct command:\n%s", commands)
	}

	if _, err := store.Rollback(second.BackupID); err != nil {
		t.Fatal(err)
	}
	commands, err = os.ReadFile(commandsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), "gemini()") {
		t.Fatalf("rollback did not restore the prior direct commands:\n%s", commands)
	}
}

func TestGeneratedPOSIXCommandPreservesArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell test")
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(root, "args.txt")
	beeapiPath := filepath.Join(binDir, "beeapi")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GETBEE_CAPTURE\"\n"
	if err := os.WriteFile(beeapiPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	commandsPath := filepath.Join(root, "commands.sh")
	if err := os.WriteFile(commandsPath, []byte(renderDirectCommands("posix", []string{"codex"})), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", "-c", `. "$1"; codex --help "two words"`, "test", commandsPath)
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "GETBEE_CAPTURE="+capturePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated direct command failed: %v\n%s", err, output)
	}
	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(captured), "run\ncodex\n--help\ntwo words\n"; got != want {
		t.Fatalf("forwarded arguments = %q, want %q", got, want)
	}
}

func TestDirectCommandRenderersCoverFishAndPowerShell(t *testing.T) {
	fish := renderDirectCommands("fish", []string{"grok"})
	for _, want := range []string{"function grok", "command beeapi run grok $argv", "end"} {
		if !strings.Contains(fish, want) {
			t.Fatalf("Fish integration is missing %q:\n%s", want, fish)
		}
	}
	powerShell := renderDirectCommands("powershell", []string{"hermes"})
	for _, want := range []string{"function global:hermes", "& beeapi run hermes @args"} {
		if !strings.Contains(powerShell, want) {
			t.Fatalf("PowerShell integration is missing %q:\n%s", want, powerShell)
		}
	}
}

func TestManagedShellBlockRejectsBrokenMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".profile")
	original := "export PATH=/bin\n" + directCommandsStart + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updateManagedShellBlock(path, renderShellHook("posix", "/tmp/commands.sh")); err == nil {
		t.Fatal("broken managed block was unexpectedly overwritten")
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
