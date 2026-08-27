package app

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func TestAuthorizeOffersExplicitAPIKeyFallbackWithoutPassword(t *testing.T) {
	input := strings.NewReader("2\nsk-test-key\n")
	var output bytes.Buffer
	r := &runner{
		ctx:    context.Background(),
		in:     input,
		reader: bufio.NewReader(input),
		out:    &output,
		errOut: &output,
	}
	key, name, err := r.authorize("https://beeapi.dev", true)
	if err != nil {
		t.Fatal(err)
	}
	if key != "sk-test-key" || name != "manual" {
		t.Fatalf("unexpected fallback result: key=%q name=%q", key, name)
	}
	text := output.String()
	if !strings.Contains(text, "跳转网站授权登录") || !strings.Contains(text, "直接粘贴 API Key") {
		t.Fatalf("login choices missing from output:\n%s", text)
	}
	if strings.Contains(text, "账户密码") {
		t.Fatalf("CLI unexpectedly asks for an account password:\n%s", text)
	}
}

func TestParseAgentsIncludesEverySupportedToolAlias(t *testing.T) {
	agents, err := parseAgents("claude-code,claude_desktop,codex,gemini-cli,grok-build,open-code,open-claw,hermes-agent")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "claude-desktop", "codex", "gemini", "grok", "opencode", "openclaw", "hermes"}
	if !reflect.DeepEqual(agents, want) {
		t.Fatalf("agents = %#v, want %#v", agents, want)
	}
}

func TestWrapperEnvironmentsKeepGrokAndHermesSecretsOutOfProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := state.Config{
		Endpoint: "https://beeapi.dev",
		Models: map[string]string{
			"grok":   "gpt-5-codex",
			"hermes": "hermes-4",
		},
	}

	grok := agentEnvironment("grok", cfg, "sk-test")
	wantGrokHome := "GROK_HOME=" + filepath.Join(home, ".config", "getbeeapi", "grok")
	if !containsString(grok, wantGrokHome) || !containsString(grok, "BEEAPI_API_KEY=sk-test") {
		t.Fatalf("unexpected Grok environment: %#v", grok)
	}

	hermes := agentEnvironment("hermes", cfg, "sk-test")
	wantHermesHome := "HERMES_HOME=" + filepath.Join(home, ".config", "getbeeapi", "hermes")
	for _, want := range []string{wantHermesHome, "OPENAI_API_KEY=sk-test", "HERMES_INFERENCE_MODEL=hermes-4"} {
		if !containsString(hermes, want) {
			t.Fatalf("Hermes environment missing %q: %#v", want, hermes)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
