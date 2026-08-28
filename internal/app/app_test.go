package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

type appRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn appRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

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
	credentials, err := r.authorize("https://beeapi.dev", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 || credentials[0].Secret != "sk-test-key" || credentials[0].Name != "手动 API Key" {
		t.Fatalf("unexpected fallback result: %#v", credentials)
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

func TestRunWithoutArgumentsOpensHomeAfterInitialSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GETBEE_HOME", home)
	store := &state.Store{Dir: home}
	if err := store.SaveConfig(state.Config{
		Endpoint:          "https://beeapi.dev",
		KeyName:           "日常开发",
		Agents:            []string{"codex", "gemini"},
		CredentialBackend: "protected-file",
	}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := Run(context.Background(), nil, "v0-test", strings.NewReader("0\n"), &output, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"____", "BeeAPI CLI v0-test", "欢迎回来", "启动已配置的 AI 工具", "配置或更新 AI 工具", "已退出 BeeAPI CLI"} {
		if !strings.Contains(text, want) {
			t.Fatalf("returning-user shell is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\n首次设置 ·") || strings.Contains(text, "[1/3] 检测 BeeAPI") {
		t.Fatalf("returning user unexpectedly entered first-run setup:\n%s", text)
	}
}

func TestRunWithoutArgumentsStartsThreeStepGuideOnlyWhenUninitialized(t *testing.T) {
	t.Setenv("GETBEE_HOME", t.TempDir())
	previousTransport := http.DefaultTransport
	http.DefaultTransport = appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"status":"ok"}`
		if request.URL.Path == "/api/v1/public/api-endpoints" {
			body = `{"code":0,"message":"success","data":{"items":[]}}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	var output bytes.Buffer
	err := Run(context.Background(), nil, "v0-test", strings.NewReader("\n2\n\n"), &output, &output)
	if err == nil || !strings.Contains(err.Error(), "API Key 不能为空") {
		t.Fatalf("first setup should stop on the intentionally empty fallback key, got %v", err)
	}
	text := output.String()
	for _, want := range []string{"BeeAPI CLI v0-test", "首次设置", "[1/3] 检测 BeeAPI", "[2/3] 连接 BeeAPI"} {
		if !strings.Contains(text, want) {
			t.Fatalf("first-run guide is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "欢迎回来") {
		t.Fatalf("uninitialized user unexpectedly entered the daily shell:\n%s", text)
	}
}

func TestTokenPrintUsesCredentialAssignedToAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GETBEE_HOME", home)
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: home}
	firstBackend, err := store.SaveNamedCredential("credential-one", "sk-first")
	if err != nil {
		t.Fatal(err)
	}
	secondBackend, err := store.SaveNamedCredential("credential-two", "sk-codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(state.Config{
		Endpoint: "https://beeapi.ai",
		Credentials: []state.Credential{
			{ID: "credential-one", Name: "general", Backend: firstBackend},
			{ID: "credential-two", Name: "coding", Backend: secondBackend},
		},
		AgentCredentials: map[string]string{"codex": "credential-two"},
	}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := Run(context.Background(), []string{"token", "print", "--agent", "codex"}, "test", strings.NewReader(""), &output, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "sk-codex\n" {
		t.Fatalf("token output = %q", output.String())
	}
}

func TestMultiCredentialAssignmentKeepsSharedClaudeConfigurationTogether(t *testing.T) {
	input := strings.NewReader("2\n\n")
	var output bytes.Buffer
	r := &runner{in: input, reader: bufio.NewReader(input), out: &output, errOut: &output}
	credentials := []credentialMaterial{
		{ID: "one", Name: "general", Prefix: "sk-one", Models: []string{"gpt-5"}},
		{ID: "two", Name: "coding", Prefix: "sk-two", Models: []string{"claude-sonnet"}},
	}
	assignments, err := r.selectCredentialAssignments(
		[]string{"claude", "claude-desktop", "codex"}, credentials, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if assignments["claude"] != "two" || assignments["claude-desktop"] != "two" || assignments["codex"] != "one" {
		t.Fatalf("unexpected assignments: %#v", assignments)
	}
	if !strings.Contains(output.String(), "共享本地配置") {
		t.Fatalf("shared configuration notice missing:\n%s", output.String())
	}
}

func TestCredentialClaimRetriesOneInterruptedResponse(t *testing.T) {
	client := beeapi.New("https://beeapi.test")
	client.Token = "bclt-test"
	requests := 0
	client.HTTP = &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return nil, errors.New("connection reset")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"code":0,"message":"success","data":{"credentials":[{"credential_id":"opaque","profile_name":"coding","device_key_prefix":"sk-device","api_key":"sk-device-secret"}],"retry_until":"2026-08-28T12:00:00Z"}}`,
			)),
			Request: request,
		}, nil
	})}
	var output bytes.Buffer
	r := &runner{ctx: context.Background(), out: &output}
	claim, err := r.claimDeviceCredentials(client)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(claim.Credentials) != 1 || claim.Credentials[0].APIKey != "sk-device-secret" {
		t.Fatalf("unexpected retry result: requests=%d claim=%#v", requests, claim)
	}
	if !strings.Contains(output.String(), "幂等窗口") {
		t.Fatalf("retry notice missing: %s", output.String())
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
