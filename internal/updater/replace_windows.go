//go:build windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

func replaceExecutable(staging, target string) (bool, error) {
	return scheduleWindowsReplacement(staging, target, os.Getpid())
}

func scheduleWindowsReplacement(staging, target string, parentPID int) (bool, error) {
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		return false, fmt.Errorf("schedule executable replacement: %w", err)
	}
	// The old executable can remain locked briefly after the parent exits, and
	// antivirus scanners or an immediately restarted CLI can extend that window.
	// Keep retrying instead of reporting success before a single best-effort move.
	script := `$Source=$env:GETBEE_UPDATE_SOURCE; ` +
		`$Target=$env:GETBEE_UPDATE_TARGET; ` +
		`$BeeParent=[int]$env:GETBEE_UPDATE_PARENT; ` +
		`if ($BeeParent -gt 0) { Wait-Process -Id $BeeParent -ErrorAction SilentlyContinue }; ` +
		`$Deadline=(Get-Date).AddMinutes(10); ` +
		`do { ` +
		`try { Move-Item -LiteralPath $Source -Destination $Target -Force -ErrorAction Stop; exit 0 } catch { Start-Sleep -Milliseconds 250 } ` +
		`} while ((Get-Date) -lt $Deadline); exit 1`
	command := exec.Command(powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	command.Env = append(os.Environ(),
		"GETBEE_UPDATE_SOURCE="+staging,
		"GETBEE_UPDATE_TARGET="+target,
		"GETBEE_UPDATE_PARENT="+strconv.Itoa(parentPID),
	)
	if err := command.Start(); err != nil {
		return false, fmt.Errorf("schedule executable replacement: %w", err)
	}
	return true, nil
}
