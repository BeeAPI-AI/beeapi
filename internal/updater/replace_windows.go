//go:build windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

func replaceExecutable(staging, target string) (bool, error) {
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		return false, fmt.Errorf("schedule executable replacement: %w", err)
	}
	script := `param([string]$Source,[string]$Target,[int]$BeeParent); Wait-Process -Id $BeeParent -Timeout 30 -ErrorAction SilentlyContinue; Move-Item -LiteralPath $Source -Destination $Target -Force`
	command := exec.Command(powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script, staging, target, strconv.Itoa(os.Getpid()))
	if err := command.Start(); err != nil {
		return false, fmt.Errorf("schedule executable replacement: %w", err)
	}
	return true, nil
}
