package state

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestInitializedAcceptsLegacyCompletedConfig(t *testing.T) {
	legacy := Config{Endpoint: "https://beeapi.ai", CredentialBackend: "protected-file"}
	if !legacy.Initialized() {
		t.Fatal("completed pre-schema config should be treated as initialized")
	}
	for _, cfg := range []Config{
		{Endpoint: "https://beeapi.ai"},
		{CredentialBackend: "protected-file"},
		{},
	} {
		if cfg.Initialized() {
			t.Fatalf("incomplete config unexpectedly initialized: %#v", cfg)
		}
	}
}

func TestNamedCredentialsUseSeparateOpaqueStorageSlots(t *testing.T) {
	dir := t.TempDir()
	store := &Store{Dir: dir}
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")

	firstBackend, err := store.SaveNamedCredential("../grant/one", "sk-first-secret")
	if err != nil {
		t.Fatal(err)
	}
	secondBackend, err := store.SaveNamedCredential("grant-two", "sk-second-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		backend, id, want string
	}{
		{firstBackend, "../grant/one", "sk-first-secret"},
		{secondBackend, "grant-two", "sk-second-secret"},
	} {
		got, loadErr := store.LoadNamedCredential(item.backend, item.id)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if got != item.want {
			t.Fatalf("credential %q = %q, want %q", item.id, got, item.want)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "credentials"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("credential files = %d, want 2", len(entries))
	}
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("credential file %s mode = %v, want 0600", entry.Name(), info.Mode().Perm())
		}
		if entry.Name() == "one" || entry.Name() == "grant-two" {
			t.Fatalf("opaque credential ID leaked into filename: %s", entry.Name())
		}
	}
}

func TestInitializedAcceptsMultiCredentialConfig(t *testing.T) {
	cfg := Config{
		Endpoint:    "https://beeapi.ai",
		Credentials: []Credential{{ID: "opaque", Name: "daily", Backend: "protected-file"}},
	}
	if !cfg.Initialized() {
		t.Fatal("multi-credential config should be initialized")
	}
}

func TestSaveConfigRecordsLifecycleMetadata(t *testing.T) {
	store := &Store{Dir: t.TempDir()}
	if err := store.SaveConfig(Config{
		Language:          "en",
		Endpoint:          "https://beeapi.ai",
		CredentialBackend: "protected-file",
	}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(store.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var saved Config
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.SchemaVersion != CurrentSchemaVersion || saved.InitializedAt.IsZero() || saved.UpdatedAt.IsZero() {
		t.Fatalf("missing lifecycle metadata: %#v", saved)
	}
	if saved.Language != "en" {
		t.Fatalf("language = %q, want en", saved.Language)
	}
}

func TestSaveConfigPersistsNamedProfilesWithoutSecrets(t *testing.T) {
	store := &Store{Dir: t.TempDir()}
	want := Profile{
		ID: "profile-work", Name: "工作开发", Endpoint: "https://beeapi.dev",
		Agents: []string{"codex"}, Models: map[string]string{"codex": "gpt-5.6-sol"},
		AgentCredentials: map[string]string{"codex": "credential-one"},
	}
	if err := store.SaveConfig(Config{
		Endpoint: "https://beeapi.dev",
		Credentials: []Credential{{
			ID: "credential-one", Name: "Coding", Backend: "protected-file",
		}},
		Profiles:       []Profile{want},
		ActiveProfile:  want.ID,
		ActiveProfiles: map[string]string{"codex": want.ID},
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if saved.SchemaVersion != CurrentSchemaVersion || !reflect.DeepEqual(saved.Profiles, []Profile{want}) {
		t.Fatalf("unexpected saved profiles: %#v", saved)
	}
	raw, err := os.ReadFile(store.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || bytes.Contains(raw, []byte("sk-secret")) {
		t.Fatalf("config unexpectedly contains a secret: %s", raw)
	}
}
