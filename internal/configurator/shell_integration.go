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

// legacyShellProfiles locates only the exact shell startup files that an older
// GetBeeAPI release could have modified. Native tool configuration no longer
// needs these wrappers.
func legacyShellProfiles(home string) ([]string, error) {
	candidates := []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".profile"),
		filepath.Join(home, ".config", "fish", "conf.d", "getbeeapi.fish"),
	}
	if runtime.GOOS == "windows" {
		candidates = append(candidates,
			filepath.Join(home, "Documents", "PowerShell", "profile.ps1"),
			filepath.Join(home, "Documents", "WindowsPowerShell", "profile.ps1"),
		)
	}
	if _, current, err := currentShellProfile(home); err == nil && current != "" {
		candidates = append(candidates, current)
	}

	var result []string
	seen := map[string]bool{}
	for _, candidate := range candidates {
		path := resolveShellProfile(candidate)
		if seen[path] {
			continue
		}
		seen[path] = true
		content, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		startCount := strings.Count(string(content), directCommandsStart)
		endCount := strings.Count(string(content), directCommandsEnd)
		if startCount == 0 && endCount == 0 {
			continue
		}
		if startCount != 1 || endCount != 1 {
			return nil, fmt.Errorf("%s 中的旧版 GetBeeAPI 管理区块不完整或重复", path)
		}
		result = append(result, path)
	}
	return result, nil
}

func cleanupLegacyShellProfiles(paths []string) error {
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(content)
		newline := detectedNewline(text)
		lines, finalNewline := splitTextLines(text)
		inside := false
		removed := false
		result := make([]string, 0, len(lines))
		for _, line := range lines {
			switch strings.TrimSpace(line) {
			case directCommandsStart:
				inside = true
				removed = true
				continue
			case directCommandsEnd:
				inside = false
				continue
			}
			if !inside {
				result = append(result, line)
			}
		}
		if inside {
			return fmt.Errorf("%s 中的旧版 GetBeeAPI 管理区块缺少结束标记", path)
		}
		if !removed {
			continue
		}
		for len(result) >= 2 && strings.TrimSpace(result[len(result)-1]) == "" && strings.TrimSpace(result[len(result)-2]) == "" {
			result = result[:len(result)-1]
		}
		if err := state.AtomicWrite(path, []byte(joinTextLines(result, newline, finalNewline && len(result) > 0)), 0o600); err != nil {
			return err
		}
	}
	return nil
}
