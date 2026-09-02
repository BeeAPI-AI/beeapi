//go:build windows

package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("GETBEE_UPDATER_LOCK_HELPER") == "1" {
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestScheduledWindowsReplacementRetriesWhileTargetIsLocked(t *testing.T) {
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	target := filepath.Join(directory, "beeapi.exe")
	staging := filepath.Join(directory, ".beeapi-update-test")
	original, err := os.ReadFile(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staging, []byte("new-beeapi-binary"), 0o700); err != nil {
		t.Fatal(err)
	}

	locked := exec.Command(target)
	locked.Env = append(os.Environ(), "GETBEE_UPDATER_LOCK_HELPER=1")
	if err := locked.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduleWindowsReplacement(staging, target, -1); err != nil {
		_ = locked.Process.Kill()
		t.Fatal(err)
	}
	if err := locked.Wait(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		body, readErr := os.ReadFile(target)
		if readErr == nil && string(body) == "new-beeapi-binary" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("background updater did not replace the unlocked target; staging still exists=%v", fileExists(staging))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
