package routeopt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedHostsPreservesUnrelatedEntries(t *testing.T) {
	original := "127.0.0.1 localhost\n10.0.0.2 internal.example\n"
	updated := UpdateManagedHosts(original, "beeapi.ai", "104.16.1.2")
	updated = UpdateManagedHosts(updated, "beeapi.dev", "104.16.1.3")
	updated = UpdateManagedHosts(updated, "beeapi.ai", "104.16.1.4")

	for _, want := range []string{
		"127.0.0.1 localhost",
		"10.0.0.2 internal.example",
		"104.16.1.4 beeapi.ai",
		"104.16.1.3 beeapi.dev",
	} {
		if !strings.Contains(updated, want) {
			t.Fatalf("managed hosts is missing %q:\n%s", want, updated)
		}
	}
	if strings.Contains(updated, "104.16.1.2") {
		t.Fatalf("old managed IP was not replaced:\n%s", updated)
	}

	restored := RemoveManagedHosts(updated, "beeapi.ai")
	if strings.Contains(restored, "beeapi.ai") {
		t.Fatalf("beeapi.ai managed block was not removed:\n%s", restored)
	}
	if !strings.Contains(restored, "beeapi.dev") || !strings.Contains(restored, "internal.example") {
		t.Fatalf("restoring one host damaged other entries:\n%s", restored)
	}
}

func TestApplyAndRestoreHostsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyHosts(path, "beeapi.ai", "104.16.1.2"); err != nil {
		t.Fatal(err)
	}
	if err := RestoreHosts(path, "beeapi.ai"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "127.0.0.1 localhost\n" {
		t.Fatalf("unexpected restored hosts file: %q", got)
	}
}

func TestMalformedManagedBlockNeverDropsTrailingHosts(t *testing.T) {
	original := "127.0.0.1 localhost\n# >>> getbeeapi managed: beeapi.ai\n10.0.0.9 important.internal\n"
	if got := RemoveManagedHosts(original, "beeapi.ai"); got != original {
		t.Fatalf("malformed marker should be preserved exactly:\n%s", got)
	}
}
