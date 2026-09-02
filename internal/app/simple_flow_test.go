package app

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func TestToolConfigurationAgentsIncludesSavedInactiveToolsInStableOrder(t *testing.T) {
	cfg := state.Config{
		Agents: []string{"grok"},
		Profiles: []state.Profile{
			{ID: "grok-live", Agents: []string{"grok"}},
			{ID: "codex-saved", Agents: []string{"codex"}},
			{ID: "gemini-saved", Agents: []string{"gemini"}},
		},
	}
	got := strings.Join(toolConfigurationAgents(cfg), ",")
	if got != "codex,gemini,grok" {
		t.Fatalf("tool order = %q, want codex,gemini,grok", got)
	}
}

func TestProfileNamesAreUniquePerToolRatherThanGlobally(t *testing.T) {
	profiles := []state.Profile{{ID: "grok-daily", Name: "日常", Agents: []string{"grok"}}}
	if profileNameExistsForAgent(profiles, "codex", "日常") {
		t.Fatal("a name used by Grok incorrectly blocked the same name for Codex")
	}
	if !profileNameExistsForAgent(profiles, "grok", "日常") {
		t.Fatal("a duplicate name for the same tool was not detected")
	}
}

func TestSelectSingleAgentRejectsMultiToolInput(t *testing.T) {
	input := strings.NewReader("codex,grok\n3\n")
	var output bytes.Buffer
	r := &runner{in: input, reader: bufio.NewReader(input), out: &output, errOut: &output}
	agent, err := r.selectSingleAgent([]environment{
		{Agent: "claude"}, {Agent: "grok"}, {Agent: "codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent != "codex" {
		t.Fatalf("selected agent = %q, want codex", agent)
	}
	if !strings.Contains(output.String(), "请选择一个有效的工具") {
		t.Fatalf("multi-tool input was not rejected clearly:\n%s", output.String())
	}
}

func TestReasoningSelectionUsesEachToolsNativeAdapter(t *testing.T) {
	credential := credentialMaterial{
		ID: "reasoning-key", ModelOptionsAuthoritative: true,
		ModelOptions: []beeapi.ModelOption{{ID: "gpt-5.6-sol", Capabilities: []string{"reasoning"}}},
	}
	for _, test := range []struct {
		agent string
		input string
		want  string
	}{
		{agent: "claude", input: "3\n", want: "high"},
		{agent: "codex", input: "4\n", want: "high"},
		{agent: "gemini", input: "2\n", want: "low"},
		{agent: "grok", input: "4\n", want: "xhigh"},
		{agent: "opencode", input: "3\n", want: "high"},
		{agent: "openclaw", input: "5\n", want: "xhigh"},
		{agent: "hermes", input: "1\n", want: "minimal"},
	} {
		t.Run(test.agent, func(t *testing.T) {
			input := strings.NewReader(test.input)
			var output bytes.Buffer
			r := &runner{in: input, reader: bufio.NewReader(input), out: &output, errOut: &output}
			selected, err := r.selectReasoningEfforts(
				[]string{test.agent}, []credentialMaterial{credential},
				map[string]string{test.agent: "reasoning-key"}, map[string]string{test.agent: "gpt-5.6-sol"}, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if selected[test.agent] != test.want {
				t.Fatalf("reasoning effort = %q, want %q", selected[test.agent], test.want)
			}
			if !strings.Contains(output.String(), agentLabel(test.agent)+" · gpt-5.6-sol · 选择思考等级") {
				t.Fatalf("tool-specific reasoning prompt missing:\n%s", output.String())
			}
		})
	}

	input := strings.NewReader("")
	var output bytes.Buffer
	r := &runner{in: input, reader: bufio.NewReader(input), out: &output, errOut: &output}
	desktop, err := r.selectReasoningEfforts(
		[]string{"claude-desktop"}, []credentialMaterial{credential},
		map[string]string{"claude-desktop": "reasoning-key"}, map[string]string{"claude-desktop": "gpt-5.6-sol"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(desktop) != 0 || output.Len() != 0 {
		t.Fatalf("Claude Desktop exposed an undocumented reasoning setting: %#v, %q", desktop, output.String())
	}
}

func TestSavedCompatibleKeyCanBeReusedWithoutAnotherExport(t *testing.T) {
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: t.TempDir()}
	backend, err := store.SaveNamedCredential("saved-key", "sk-saved")
	if err != nil {
		t.Fatal(err)
	}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), Request: request,
			Body: io.NopCloser(strings.NewReader(`{"code":0,"message":"success","data":{"models":[{"id":"gpt-5.6-sol","protocols":["openai/responses"],"recommended_for":["codex"],"priority":100}]}}`)),
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	input := strings.NewReader("\n")
	var output bytes.Buffer
	r := &runner{ctx: context.Background(), in: input, reader: bufio.NewReader(input), out: &output, errOut: &output, store: store}
	credential, selected, err := r.chooseSavedCredentialForAgent(state.Config{
		Endpoint: "https://beeapi.test", Credentials: []state.Credential{{
			ID: "saved-key", Name: "工作 Key", Prefix: "sk-save", Backend: backend,
		}},
	}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !selected || credential.ID != "saved-key" {
		t.Fatalf("saved Key was not reused: selected=%v credential=%#v", selected, credential)
	}
	text := output.String()
	if !strings.Contains(text, "工作 Key") || !strings.Contains(text, "粘贴新的 API Key") {
		t.Fatalf("saved/new Key choices were not clear:\n%s", text)
	}
}

func TestApplyProfileForOneToolPreservesAnotherToolsActiveConfiguration(t *testing.T) {
	stateDir := t.TempDir()
	targetHome := t.TempDir()
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	t.Setenv("GETBEE_TARGET_HOME", targetHome)
	store := &state.Store{Dir: stateDir}
	codexBackend, err := store.SaveNamedCredential("codex-key", "sk-codex-secret")
	if err != nil {
		t.Fatal(err)
	}
	grokBackend, err := store.SaveNamedCredential("grok-key", "sk-grok-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(targetHome, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetHome, ".codex", "config.toml"), []byte("approval_policy = \"never\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	grokProfile := state.Profile{
		ID: "grok-main", Name: "Grok 主力", Endpoint: "https://beeapi.ai", Agents: []string{"grok"},
		Models: map[string]string{"grok": "grok-4.6"}, AgentCredentials: map[string]string{"grok": "grok-key"},
	}
	codexProfile := state.Profile{
		ID: "codex-work", Name: "Codex 工作", Endpoint: "https://beeapi.dev", Agents: []string{"codex"},
		Models: map[string]string{"codex": "gpt-5.6-sol"}, ReasoningEfforts: map[string]string{"codex": "high"},
		AgentCredentials: map[string]string{"codex": "codex-key"},
	}
	cfg := state.Config{
		Endpoint: "https://beeapi.ai", InitializedAt: time.Now().UTC(), Agents: []string{"grok"},
		Models: map[string]string{"grok": "grok-4.6"}, AgentCredentials: map[string]string{"grok": "grok-key"},
		AgentEndpoints: map[string]string{"grok": "https://beeapi.ai"}, ActiveProfiles: map[string]string{"grok": "grok-main"},
		Credentials: []state.Credential{
			{ID: "codex-key", Name: "Codex Key", Backend: codexBackend},
			{ID: "grok-key", Name: "Grok Key", Backend: grokBackend},
		},
		Profiles: []state.Profile{grokProfile, codexProfile}, BinaryPath: "beeapi",
	}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r := &runner{ctx: context.Background(), store: store}
	if _, err := r.applyProfileForAgent(&cfg, codexProfile, "codex"); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if saved.ActiveProfiles["grok"] != "grok-main" || saved.AgentCredentials["grok"] != "grok-key" || saved.Models["grok"] != "grok-4.6" {
		t.Fatalf("activating Codex changed Grok: %#v", saved)
	}
	if saved.ActiveProfiles["codex"] != "codex-work" || saved.AgentCredentials["codex"] != "codex-key" || saved.Models["codex"] != "gpt-5.6-sol" {
		t.Fatalf("Codex profile was not activated: %#v", saved)
	}
	raw, err := os.ReadFile(filepath.Join(targetHome, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`approval_policy = "never"`, `model = "gpt-5.6-sol"`, `model_reasoning_effort = "high"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("Codex config is missing %q:\n%s", want, raw)
		}
	}
}
