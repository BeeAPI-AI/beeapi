package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedCredentialFileForcesPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	store := &Store{Dir: dir}
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	path := filepath.Join(dir, "credential")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	backend, err := store.SaveCredential("sk-secret")
	if err != nil {
		t.Fatal(err)
	}
	if backend != "protected-file" {
		t.Fatalf("backend = %q", backend)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential mode = %o, want 600", got)
	}
}
