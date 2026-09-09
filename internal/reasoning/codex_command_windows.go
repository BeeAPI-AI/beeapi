package reasoning

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func codexSchemaCommand(ctx context.Context, path, dir string) *exec.Cmd {
	if !strings.EqualFold(filepath.Ext(path), ".cmd") && !strings.EqualFold(filepath.Ext(path), ".bat") {
		return exec.CommandContext(ctx, path, "app-server", "generate-json-schema", "--out", dir)
	}
	// npm commonly installs codex.cmd. cmd.exe does not use Go's default
	// CommandLineToArgvW quoting rules. Keep paths out of shell syntax, expand
	// them once inside quotes, disable delayed expansion and AutoRun scripts.
	command := exec.CommandContext(ctx, "cmd.exe")
	command.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /v:off /s /c ""%GETBEE_CODEX_PROBE_EXE%" app-server generate-json-schema --out "%GETBEE_CODEX_PROBE_OUT%""`}
	command.Env = append(os.Environ(), "GETBEE_CODEX_PROBE_EXE="+path, "GETBEE_CODEX_PROBE_OUT="+dir)
	return command
}
