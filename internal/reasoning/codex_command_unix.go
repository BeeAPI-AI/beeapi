//go:build !windows

package reasoning

import (
	"context"
	"os/exec"
)

func codexSchemaCommand(ctx context.Context, path, dir string) *exec.Cmd {
	return exec.CommandContext(ctx, path, "app-server", "generate-json-schema", "--out", dir)
}
