package configurator

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BeeAPI-AI/beeapi/internal/state"
)

const (
	directCommandsStart = "# >>> getbeeapi commands >>>"
	directCommandsEnd   = "# <<< getbeeapi commands <<<"
)

type shellIntegration struct {
	Kind         string
	ProfilePath  string
	CommandsPath string
}

func prepareShellIntegration(store *state.Store, home string) (shellIntegration, error) {
	if store == nil {
		return shellIntegration{}, errors.New("缺少本地状态目录")
	}
	kind, profilePath, err := currentShellProfile(home)
	if err != nil {
		return shellIntegration{}, err
	}
	extension := ".sh"
	if kind == "fish" {
		extension = ".fish"
	} else if kind == "powershell" {
		extension = ".ps1"
	}
	return shellIntegration{
		Kind:         kind,
		ProfilePath:  profilePath,
		CommandsPath: filepath.Join(store.Dir, "shell", "commands"+extension),
	}, nil
}

func currentShellProfile(home string) (string, string, error) {
	if runtime.GOOS == "windows" {
		profile := strings.TrimSpace(os.Getenv("GETBEE_POWERSHELL_PROFILE"))
		if profile == "" {
			profile = discoverPowerShellProfile()
		}
		if profile == "" {
			profile = filepath.Join(home, "Documents", "PowerShell", "profile.ps1")
		}
		if !filepath.IsAbs(profile) {
			return "", "", errors.New("PowerShell Profile 必须是绝对路径")
		}
		return "powershell", resolveShellProfile(profile), nil
	}

	shellName := strings.ToLower(filepath.Base(strings.TrimSpace(os.Getenv("SHELL"))))
	var kind, profile string
	switch shellName {
	case "zsh":
		kind, profile = "posix", filepath.Join(home, ".zshrc")
	case "bash":
		kind = "posix"
		if runtime.GOOS == "darwin" {
			profile = filepath.Join(home, ".bash_profile")
		} else {
			profile = filepath.Join(home, ".bashrc")
		}
	case "fish":
		kind, profile = "fish", filepath.Join(home, ".config", "fish", "conf.d", "getbeeapi.fish")
	default:
		kind, profile = "posix", filepath.Join(home, ".profile")
	}
	return kind, resolveShellProfile(profile), nil
}

func discoverPowerShellProfile() string {
	for _, name := range []string{"pwsh.exe", "powershell.exe"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		command := `[Console]::Out.Write($PROFILE.CurrentUserAllHosts)`
		output, err := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", command).Output()
		if err == nil && filepath.IsAbs(strings.TrimSpace(string(output))) {
			return strings.TrimSpace(string(output))
		}
	}
	return ""
}

func resolveShellProfile(path string) string {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return path
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

func directLaunchAgents(agents []string) []string {
	wrappers := map[string]bool{
		"codex": true, "gemini": true, "grok": true, "hermes": true,
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(agents))
	for _, agent := range agents {
		if wrappers[agent] && !seen[agent] {
			seen[agent] = true
			result = append(result, agent)
		}
	}
	return result
}

func installDirectLaunch(integration shellIntegration, agents []string) error {
	commands := renderDirectCommands(integration.Kind, agents)
	if err := state.AtomicWrite(integration.CommandsPath, []byte(commands), 0o600); err != nil {
		return fmt.Errorf("写入直接启动命令: %w", err)
	}
	if len(directLaunchAgents(agents)) == 0 {
		return nil
	}
	hook := renderShellHook(integration.Kind, integration.CommandsPath)
	if err := updateManagedShellBlock(integration.ProfilePath, hook); err != nil {
		return fmt.Errorf("更新 Shell 启动文件 %s: %w", integration.ProfilePath, err)
	}
	return nil
}

func renderDirectCommands(kind string, agents []string) string {
	wrappers := directLaunchAgents(agents)
	newline := "\n"
	if kind == "powershell" {
		newline = "\r\n"
	}
	lines := []string{"# Managed by GetBeeAPI. Run `beeapi configure` to update these commands."}
	for _, agent := range wrappers {
		command := commandForAgent(agent)
		switch kind {
		case "fish":
			lines = append(lines,
				"function "+command+" --description 'Run "+agentDisplayName(agent)+" with BeeAPI'",
				"    command beeapi run "+agent+" $argv",
				"end",
			)
		case "powershell":
			lines = append(lines,
				"function global:"+command+" {",
				"    & beeapi run "+agent+" @args",
				"}",
			)
		default:
			lines = append(lines,
				command+"() {",
				"  command beeapi run "+agent+" \"$@\"",
				"}",
			)
		}
	}
	return strings.Join(lines, newline) + newline
}

func renderShellHook(kind, commandsPath string) string {
	var lines []string
	switch kind {
	case "fish":
		quoted := shellQuote(commandsPath)
		lines = []string{
			directCommandsStart,
			"if test -r " + quoted,
			"    source " + quoted,
			"end",
			directCommandsEnd,
		}
	case "powershell":
		quoted := "'" + strings.ReplaceAll(commandsPath, "'", "''") + "'"
		lines = []string{
			directCommandsStart,
			"$GetBeeAPICommands = " + quoted,
			"if (Test-Path -LiteralPath $GetBeeAPICommands) { . $GetBeeAPICommands }",
			"Remove-Variable GetBeeAPICommands -ErrorAction SilentlyContinue",
			directCommandsEnd,
		}
	default:
		quoted := shellQuote(commandsPath)
		lines = []string{
			directCommandsStart,
			"if [ -r " + quoted + " ]; then",
			"  . " + quoted,
			"fi",
			directCommandsEnd,
		}
	}
	return strings.Join(lines, "\n")
}

func shellReloadCommand(integration shellIntegration) string {
	if integration.Kind == "powershell" {
		return ". '" + strings.ReplaceAll(integration.ProfilePath, "'", "''") + "'"
	}
	if integration.Kind == "fish" {
		return "source " + shellQuote(integration.ProfilePath)
	}
	return ". " + shellQuote(integration.ProfilePath)
}

func updateManagedShellBlock(path, block string) error {
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	current := string(content)
	startCount := strings.Count(current, directCommandsStart)
	endCount := strings.Count(current, directCommandsEnd)
	if startCount != endCount || startCount > 1 {
		return errors.New("发现不完整或重复的 GetBeeAPI 管理区块，请先手动检查该文件")
	}

	next := current
	if startCount == 1 {
		start := strings.Index(current, directCommandsStart)
		end := strings.Index(current[start:], directCommandsEnd)
		if end < 0 {
			return errors.New("GetBeeAPI 管理区块缺少结束标记")
		}
		end += start + len(directCommandsEnd)
		next = current[:start] + block + current[end:]
	} else {
		if next != "" && !strings.HasSuffix(next, "\n") {
			next += "\n"
		}
		if next != "" && !strings.HasSuffix(next, "\n\n") {
			next += "\n"
		}
		next += block + "\n"
	}
	if next == current {
		return nil
	}
	return state.AtomicWrite(path, []byte(next), 0o600)
}

func commandForAgent(agent string) string {
	return map[string]string{
		"codex": "codex", "gemini": "gemini", "grok": "grok", "hermes": "hermes",
	}[agent]
}

func agentDisplayName(agent string) string {
	return map[string]string{
		"codex": "Codex", "gemini": "Gemini CLI", "grok": "Grok Build", "hermes": "Hermes",
	}[agent]
}
