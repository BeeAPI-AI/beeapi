package app

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func TestEnsureProfileStateMigratesLegacyConfiguration(t *testing.T) {
	cfg := state.Config{
		Endpoint:          "https://beeapi.dev",
		DefaultModel:      "gpt-5.6-sol",
		Agents:            []string{"codex"},
		CredentialBackend: "protected-file",
	}
	if !ensureProfileState(&cfg) {
		t.Fatal("legacy configuration was not migrated")
	}
	if len(cfg.Profiles) != 1 || cfg.Profiles[0].Name != defaultProfileName || cfg.ActiveProfile != "default" {
		t.Fatalf("unexpected migrated profile state: %#v", cfg)
	}
	if cfg.AgentCredentials["codex"] != "default" || cfg.Models["codex"] != "gpt-5.6-sol" {
		t.Fatalf("legacy assignment was not retained: %#v", cfg.Profiles[0])
	}
	if cfg.ActiveProfiles["codex"] != "default" || cfg.AgentEndpoints["codex"] != "https://beeapi.dev" {
		t.Fatalf("active profile mapping was not initialized: %#v", cfg)
	}
}

func TestApplyProfileBacksUpAndOnlyChangesManagedCodexFields(t *testing.T) {
	stateDir := t.TempDir()
	targetHome := t.TempDir()
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	t.Setenv("GETBEE_TARGET_HOME", targetHome)
	store := &state.Store{Dir: stateDir}
	backendA, err := store.SaveNamedCredential("key-a", "sk-secret-a")
	if err != nil {
		t.Fatal(err)
	}
	backendB, err := store.SaveNamedCredential("key-b", "sk-secret-b")
	if err != nil {
		t.Fatal(err)
	}
	codexDir := filepath.Join(targetHome, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := "approval_policy = \"never\"\nmodel = \"old-model\"\n\n[features]\nmulti_agent = true\n"
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	first := state.Profile{
		ID: "daily", Name: "日常", Endpoint: "https://beeapi.ai", Agents: []string{"codex"},
		Models: map[string]string{"codex": "gpt-5.5"}, AgentCredentials: map[string]string{"codex": "key-a"},
	}
	second := state.Profile{
		ID: "work", Name: "工作开发", Endpoint: "https://beeapi.dev", Agents: []string{"codex"},
		Models: map[string]string{"codex": "gpt-5.6-sol"}, AgentCredentials: map[string]string{"codex": "key-b"},
	}
	cfg := state.Config{
		Endpoint: "https://beeapi.ai", Agents: []string{"codex"}, Models: first.Models,
		Credentials: []state.Credential{
			{ID: "key-a", Name: "General", Prefix: "sk-a", Backend: backendA},
			{ID: "key-b", Name: "Coding", Prefix: "sk-b", Backend: backendB},
		},
		AgentCredentials: first.AgentCredentials, AgentEndpoints: map[string]string{"codex": first.Endpoint},
		Profiles: []state.Profile{first, second}, ActiveProfile: first.ID,
		ActiveProfiles: map[string]string{"codex": first.ID}, BinaryPath: "beeapi",
	}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r := &runner{ctx: context.Background(), store: store}
	result, err := r.applyProfile(&cfg, second)
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupID == "" {
		t.Fatal("profile switch did not create a backup")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		`approval_policy = "never"`,
		`multi_agent = true`,
		`model = "gpt-5.6-sol"`,
		`base_url = "https://beeapi.dev/v1"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("switched Codex config is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "sk-secret-a") || strings.Contains(text, "sk-secret-b") {
		t.Fatalf("Codex config contains a plaintext API key:\n%s", text)
	}
	saved, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if saved.ActiveProfile != second.ID || saved.AgentCredentials["codex"] != "key-b" || saved.AgentEndpoints["codex"] != second.Endpoint {
		t.Fatalf("profile state did not switch atomically: %#v", saved)
	}
}

func TestManageProfilesRefusesToDeleteActiveProfile(t *testing.T) {
	store := &state.Store{Dir: t.TempDir()}
	active := state.Profile{ID: "active", Name: "当前", Endpoint: "https://beeapi.dev", Agents: []string{"codex"}}
	inactive := state.Profile{ID: "spare", Name: "备用", Endpoint: "https://beeapi.ai", Agents: []string{"codex"}}
	if err := store.SaveConfig(state.Config{
		Endpoint: "https://beeapi.dev", CredentialBackend: "protected-file", Agents: []string{"codex"},
		Profiles: []state.Profile{active, inactive}, ActiveProfile: active.ID,
		ActiveProfiles: map[string]string{"codex": active.ID},
	}); err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader("2\n1\n")
	var output bytes.Buffer
	r := &runner{store: store, in: input, reader: bufio.NewReader(input), out: &output, errOut: &output}
	err := r.manageProfilesInteractive()
	if err == nil || !strings.Contains(err.Error(), "正在使用") {
		t.Fatalf("unexpected active-profile deletion result: %v\n%s", err, output.String())
	}
}

func TestMergeStoredCredentialsKeepsExistingKeysAndUpdatesDuplicates(t *testing.T) {
	cfg := state.Config{Credentials: []state.Credential{
		{ID: "key-xai", Name: "Xai-Heavy号池 Key", Prefix: "sk-xai", Backend: "protected-file"},
	}}
	merged := mergeStoredCredentials(cfg, []state.Credential{
		{ID: "key-2k", Name: "2K-Plus Key", Prefix: "sk-2k", Backend: "protected-file"},
	})
	if len(merged) != 2 || merged[0].ID != "key-xai" || merged[1].ID != "key-2k" {
		t.Fatalf("new Key replaced an existing Key instead of being merged: %#v", merged)
	}
	updated := mergeStoredCredentials(state.Config{Credentials: merged}, []state.Credential{
		{ID: "key-xai", Name: "Xai Updated", Prefix: "sk-new", Backend: "keyring"},
	})
	if len(updated) != 2 || updated[0].Name != "Xai Updated" || updated[1].ID != "key-2k" {
		t.Fatalf("duplicate Key metadata was not updated in place: %#v", updated)
	}
}

func TestMergeStoredCredentialsPreservesLegacyDefaultKey(t *testing.T) {
	merged := mergeStoredCredentials(state.Config{
		KeyName: "旧 Key", CredentialBackend: "protected-file",
	}, []state.Credential{{ID: "key-new", Name: "New Key", Backend: "keyring"}})
	if len(merged) != 2 || merged[0].ID != "default" || merged[0].Backend != "protected-file" || merged[1].ID != "key-new" {
		t.Fatalf("legacy credential was not retained during migration: %#v", merged)
	}
}

func TestNewProfileDefaultsToDetectedUnconfiguredTools(t *testing.T) {
	environments := []environment{
		{Agent: "codex", Detected: true},
		{Agent: "gemini", Detected: true},
		{Agent: "grok", Detected: true},
		{Agent: "opencode", Detected: true},
	}
	defaults := newProfileAgentDefaults(environments, []string{"grok"})
	if strings.Join(defaults, ",") != "codex,gemini,opencode" {
		t.Fatalf("new profile still defaulted to the already configured Grok tool: %v", defaults)
	}
}

func TestActivatingNewToolProfileKeepsExistingGrokConfiguration(t *testing.T) {
	cfg := state.Config{
		Endpoint:         "https://beeapi.ai",
		Agents:           []string{"grok"},
		Models:           map[string]string{"grok": "grok-4.6"},
		AgentCredentials: map[string]string{"grok": "key-xai"},
		AgentEndpoints:   map[string]string{"grok": "https://beeapi.ai"},
		ActiveProfiles:   map[string]string{"grok": "grok-profile"},
	}
	activateProfileFields(&cfg, state.Profile{
		ID: "codex-profile", Endpoint: "https://beeapi.dev", Agents: []string{"codex"},
		Models: map[string]string{"codex": "gpt-5.6-sol"}, AgentCredentials: map[string]string{"codex": "key-2k"},
	})
	if strings.Join(cfg.Agents, ",") != "grok,codex" {
		t.Fatalf("adding Codex replaced the existing Grok tool: %v", cfg.Agents)
	}
	if cfg.AgentCredentials["grok"] != "key-xai" || cfg.Models["grok"] != "grok-4.6" {
		t.Fatalf("existing Grok configuration changed: %#v %#v", cfg.AgentCredentials, cfg.Models)
	}
	if cfg.AgentCredentials["codex"] != "key-2k" || cfg.Models["codex"] != "gpt-5.6-sol" {
		t.Fatalf("new Codex configuration was not activated: %#v %#v", cfg.AgentCredentials, cfg.Models)
	}
}
