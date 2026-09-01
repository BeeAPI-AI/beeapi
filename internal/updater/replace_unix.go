//go:build !windows

package updater

import (
	"fmt"
	"os"
)

func replaceExecutable(staging, target string) (bool, error) {
	if err := os.Rename(staging, target); err != nil {
		return false, fmt.Errorf("replace current executable: %w", err)
	}
	return false, nil
}
