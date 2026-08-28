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
	"github.com/BeeAPI-AI/beeapi/internal/routeopt"
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

func TestStableCredentialIDDeduplicatesRepeatedExistingKeyExports(t *testing.T) {
	first := stableCredentialID("sk-existing-secret")
	second := stableCredentialID("  sk-existing-secret\n")
	if first != second || !strings.HasPrefix(first, "key-") {
		t.Fatalf("stable credential IDs differ: %q != %q", first, second)
	}
	if first == stableCredentialID("sk-other-secret") {
		t.Fatal("different API keys received the same local credential ID")
	}
}

func TestDeviceAuthorizationPrintsCompleteURLForHeadlessTerminal(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "192.0.2.10 12345 192.0.2.20 22")
	var output bytes.Buffer
	opened := false
	r := &runner{
		out:    &output,
		errOut: &output,
		openBrowser: func(string) error {
			opened = true
			return nil
		},
	}
	err := r.presentDeviceAuthorization("https://beeapi.dev", beeapi.DeviceCode{
		UserCode:        "BEE7-K9Q2",
		VerificationURI: "https://beeapi.ai/cli/authorize",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Fatal("headless SSH terminal unexpectedly attempted to open a browser")
	}
	text := output.String()
	for _, want := range []string{
		"授权网址: https://beeapi.ai/cli/authorize?user_code=BEE7-K9Q2",
		"设备授权码: BEE7-K9Q2",
		"SSH 或无桌面终端",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("device authorization output is missing %q:\n%s", want, text)
		}
	}
}

func TestDeviceAuthorizationReportsBrowserOpenFailure(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("DISPLAY", ":99")
	var output bytes.Buffer
	r := &runner{
		out:    &output,
		errOut: &output,
		openBrowser: func(string) error {
			return errors.New("no browser")
		},
	}
	err := r.presentDeviceAuthorization("https://beeapi.dev", beeapi.DeviceCode{
		UserCode:    "BEE7-K9Q2",
		CompleteURI: "https://beeapi.dev/cli/authorize?user_code=BEE7-K9Q2",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "自动打开浏览器失败") || !strings.Contains(text, "请复制以上授权网址") {
		t.Fatalf("browser failure fallback is missing:\n%s", text)
	}
}

func TestDeviceAuthorizationRejectsUntrustedVerificationURL(t *testing.T) {
	_, err := deviceVerificationURL("https://beeapi.dev", beeapi.DeviceCode{
		UserCode:    "BEE7-K9Q2",
		CompleteURI: "https://beeapi.ai.attacker.test/cli/authorize?user_code=BEE7-K9Q2",
	})
	if err == nil || !strings.Contains(err.Error(), "受信任") {
		t.Fatalf("unexpected verification URL result: %v", err)
	}
}

func TestUnavailableDeviceAuthorizationShowsLoginPageWithoutClaimingItCanApprove(t *testing.T) {
	previousTransport := http.DefaultTransport
	http.DefaultTransport = appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":404,"message":"not found"}`)),
			Request:    request,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	input := strings.NewReader("1\nn\n")
	var output bytes.Buffer
	r := &runner{
		ctx:    context.Background(),
		in:     input,
		reader: bufio.NewReader(input),
		out:    &output,
		errOut: &output,
	}
	_, err := r.authorize("https://beeapi.dev", false)
	if err == nil || !strings.Contains(err.Error(), "网站授权未完成") {
		t.Fatalf("unexpected unavailable authorization result: %v", err)
	}
	text := output.String()
	for _, want := range []string{
		"BeeAPI 账户登录页: https://beeapi.dev/login",
		"当前没有能批准本次 CLI 的授权网址",
		"单独登录账户不会完成 CLI 授权",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("unavailable authorization output is missing %q:\n%s", want, text)
		}
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

func TestUnavailableEndpointChoiceFallsBackToReachableEndpoint(t *testing.T) {
	previousTransport := http.DefaultTransport
	http.DefaultTransport = appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "beeapi.ai" {
			return nil, errors.New("blocked")
		}
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

	input := strings.NewReader("2\nn\n")
	var output bytes.Buffer
	r := &runner{
		ctx:    context.Background(),
		in:     input,
		reader: bufio.NewReader(input),
		out:    &output,
		errOut: &output,
	}
	endpoint, err := r.resolveEndpoint("", false)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://beeapi.dev" {
		t.Fatalf("endpoint = %q, want reachable fallback", endpoint)
	}
	if !strings.Contains(output.String(), "已改用当前可访问入口 https://beeapi.dev") {
		t.Fatalf("fallback notice missing:\n%s", output.String())
	}
}

func TestFailedOptimizationFallsBackToReachableEndpoint(t *testing.T) {
	previousTransport := http.DefaultTransport
	http.DefaultTransport = appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "beeapi.ai" {
			return nil, errors.New("blocked")
		}
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

	input := strings.NewReader("2\ny\n")
	var output bytes.Buffer
	r := &runner{
		ctx:    context.Background(),
		in:     input,
		reader: bufio.NewReader(input),
		out:    &output,
		errOut: &output,
		optimize: func(string, bool, bool) (routeopt.Result, error) {
			return routeopt.Result{}, errors.New("CFST 没有生成测速结果")
		},
	}
	endpoint, err := r.resolveEndpoint("", false)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://beeapi.dev" {
		t.Fatalf("endpoint = %q, want reachable fallback", endpoint)
	}
	text := output.String()
	for _, want := range []string{"优选未完成", "已自动回退到可访问入口 https://beeapi.dev"} {
		if !strings.Contains(text, want) {
			t.Fatalf("optimization fallback notice missing %q:\n%s", want, text)
		}
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

func TestCodexModelSelectionUsesBeeAPIServerPriority(t *testing.T) {
	credentials := []credentialMaterial{{
		ID: "openai-plus", Name: "OpenAI-Plus key", Models: []string{"gpt-5.5", "claude-sonnet", "gpt-5.6"},
		ModelOptionsAuthoritative: true,
		ModelOptions: []beeapi.ModelOption{
			{ID: "gpt-5.5", Protocols: []string{"openai/responses"}, Capabilities: []string{"tools"}, RecommendedFor: []string{"codex"}, Priority: 90},
			{ID: "claude-sonnet", Protocols: []string{"anthropic/messages"}, Capabilities: []string{"tools"}, RecommendedFor: []string{"claude_code"}, Priority: 100},
			{ID: "gpt-5.6", Protocols: []string{"openai/responses"}, Capabilities: []string{"tools"}, RecommendedFor: []string{"codex"}, Priority: 91},
		},
	}}
	var output bytes.Buffer
	r := &runner{out: &output, errOut: &output}
	models, err := r.selectModelsForAssignments(
		[]string{"codex"}, credentials, map[string]string{"codex": "openai-plus"}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if models["codex"] != "gpt-5.6" {
		t.Fatalf("Codex model = %q, want highest BeeAPI priority", models["codex"])
	}
	text := output.String()
	if !strings.Contains(text, "OpenAI Responses") || !strings.Contains(text, "客户端适配") {
		t.Fatalf("protocol match details missing:\n%s", text)
	}
}

func TestAuthoritativeModelSelectionDoesNotOverrideBeeAPIServerPriority(t *testing.T) {
	credential := credentialMaterial{
		ID: "server-ranked", Name: "Server-ranked key", ModelOptionsAuthoritative: true,
		ModelOptions: []beeapi.ModelOption{
			{ID: "gpt-5.5", Protocols: []string{"openai/responses"}, RecommendedFor: []string{"codex"}, Priority: 100},
			{ID: "gpt-5.6", Protocols: []string{"openai/responses"}, Capabilities: []string{"tools"}, RecommendedFor: []string{"codex"}, Priority: 90, ContextWindowTokens: intPointer(400000)},
		},
	}
	model, _, err := matchedModel("codex", credential)
	if err != nil {
		t.Fatal(err)
	}
	if model != "gpt-5.5" {
		t.Fatalf("model = %q, want BeeAPI's higher-priority model", model)
	}
}

func TestAuthoritativeModelSelectionRejectsProtocolMismatch(t *testing.T) {
	credential := credentialMaterial{
		ID: "chat-only", Name: "Chat-only key", Models: []string{"gpt-chat"}, ModelOptionsAuthoritative: true,
		ModelOptions: []beeapi.ModelOption{{ID: "gpt-chat", Protocols: []string{"openai/chat_completions"}, RecommendedFor: []string{"openai_compatible"}, Priority: 100}},
	}
	_, _, err := matchedModel("codex", credential)
	if err == nil || !strings.Contains(err.Error(), "OpenAI Responses") {
		t.Fatalf("unexpected protocol mismatch result: %v", err)
	}
}

func TestAuthoritativeModelSelectionRejectsClientRestrictionMismatch(t *testing.T) {
	credential := credentialMaterial{
		ID: "codex-only", Name: "Codex-only key", ModelOptionsAuthoritative: true,
		ModelOptions: []beeapi.ModelOption{{
			ID: "gpt-5.6", Protocols: []string{"openai/responses"}, RecommendedFor: []string{"codex"}, Priority: 100,
		}},
	}
	_, _, err := matchedModel("openclaw", credential)
	if err == nil || !strings.Contains(err.Error(), "请选择其他 Key") {
		t.Fatalf("unexpected client restriction result: %v", err)
	}
}

func TestCredentialAssignmentRepromptsWhenChosenKeyHasNoCompatibleModel(t *testing.T) {
	credentials := credentialSelectionFixtures()
	input := strings.NewReader("1\n3\n")
	var output bytes.Buffer
	r := &runner{in: input, reader: bufio.NewReader(input), out: &output, errOut: &output}

	assignments, err := r.selectCredentialAssignments([]string{"codex"}, credentials, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if assignments["codex"] != "responses-b" {
		t.Fatalf("assignment = %q, want replacement key", assignments["codex"])
	}
	text := output.String()
	for _, want := range []string{"× Chat key", "Chat key 不能用于 Codex", "请重新选择带 ✓ 的 API Key"} {
		if !strings.Contains(text, want) {
			t.Fatalf("replacement prompt missing %q:\n%s", want, text)
		}
	}
}

func TestCredentialAssignmentReplacesExistingIncompatibleKey(t *testing.T) {
	credentials := credentialSelectionFixtures()
	input := strings.NewReader("\n")
	var output bytes.Buffer
	r := &runner{in: input, reader: bufio.NewReader(input), out: &output, errOut: &output}

	assignments, err := r.selectCredentialAssignments(
		[]string{"codex"}, credentials, map[string]string{"codex": "chat-only"}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if assignments["codex"] != "responses-a" {
		t.Fatalf("assignment = %q, want first compatible key", assignments["codex"])
	}
	if !strings.Contains(output.String(), "Codex 当前的 Key Chat key 不可用") {
		t.Fatalf("existing-key replacement notice missing:\n%s", output.String())
	}
}

func TestCredentialAssignmentStillConfirmsOnlyCompatibleKey(t *testing.T) {
	credentials := credentialSelectionFixtures()[:2]
	var output bytes.Buffer
	r := &runner{reader: bufio.NewReader(strings.NewReader("")), out: &output, errOut: &output}

	assignments, err := r.selectCredentialAssignments([]string{"codex"}, credentials, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if assignments["codex"] != "responses-a" {
		t.Fatalf("assignment = %q, want only compatible key", assignments["codex"])
	}
	text := output.String()
	for _, want := range []string{"Codex · 选择 API Key", "2. ✓ Responses A", "请选择 API Key [2]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("explicit key confirmation missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "已自动选择") {
		t.Fatalf("interactive flow unexpectedly auto-selected the only compatible key:\n%s", text)
	}
}

func TestInteractiveModelSelectionListsChoicesAndUsesSelectedNumber(t *testing.T) {
	credential := credentialMaterial{
		ID: "responses", Name: "OpenAI-Plus key", Models: []string{"gpt-5.5", "gpt-5.6-sol"},
		ModelOptionsAuthoritative: true,
		ModelOptions: []beeapi.ModelOption{
			{ID: "gpt-5.5", Protocols: []string{"openai/responses"}, RecommendedFor: []string{"codex"}, Priority: 90},
			{ID: "gpt-5.6-sol", Protocols: []string{"openai/responses"}, RecommendedFor: []string{"codex"}, Priority: 100},
		},
	}
	input := strings.NewReader("2\n")
	var output bytes.Buffer
	r := &runner{in: input, reader: bufio.NewReader(input), out: &output, errOut: &output}

	models, err := r.selectModelsForAssignments(
		[]string{"codex"}, []credentialMaterial{credential}, map[string]string{"codex": "responses"}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if models["codex"] != "gpt-5.5" {
		t.Fatalf("selected model = %q, want numbered second choice", models["codex"])
	}
	text := output.String()
	for _, want := range []string{
		"Codex · OpenAI-Plus key · 选择模型",
		"1. gpt-5.6-sol · BeeAPI 推荐",
		"2. gpt-5.5",
		"请选择模型编号或名称 [1]",
		"✓ Codex 使用 OpenAI-Plus key · gpt-5.5",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("explicit model selection missing %q:\n%s", want, text)
		}
	}
}

func credentialSelectionFixtures() []credentialMaterial {
	return []credentialMaterial{
		{
			ID: "chat-only", Name: "Chat key", Prefix: "sk-chat", ModelOptionsAuthoritative: true,
			Models: []string{"gpt-chat"}, ModelOptions: []beeapi.ModelOption{{
				ID: "gpt-chat", Protocols: []string{"openai/chat_completions"}, RecommendedFor: []string{"openai_compatible"}, Priority: 100,
			}},
		},
		{
			ID: "responses-a", Name: "Responses A", Prefix: "sk-a", ModelOptionsAuthoritative: true,
			Models: []string{"gpt-5.6"}, ModelOptions: []beeapi.ModelOption{{
				ID: "gpt-5.6", Protocols: []string{"openai/responses"}, RecommendedFor: []string{"codex"}, Priority: 100,
			}},
		},
		{
			ID: "responses-b", Name: "Responses B", Prefix: "sk-b", ModelOptionsAuthoritative: true,
			Models: []string{"gpt-5.5"}, ModelOptions: []beeapi.ModelOption{{
				ID: "gpt-5.5", Protocols: []string{"openai/responses"}, RecommendedFor: []string{"codex"}, Priority: 90,
			}},
		},
	}
}

func intPointer(value int) *int { return &value }

func TestModelDiscoveryFallsBackOnlyWhenCapabilityEndpointIsUnavailable(t *testing.T) {
	previousTransport := http.DefaultTransport
	requests := make([]string, 0, 2)
	http.DefaultTransport = appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.RequestURI())
		if request.URL.Path == "/api/v1/client/model-options" {
			return &http.Response{
				StatusCode: http.StatusNotFound, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"code":404,"message":"not found"}`)), Request: request,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-5.5"}]}`)), Request: request,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	r := &runner{ctx: context.Background()}
	discovery, err := r.modelsForCredential("https://beeapi.test", "sk-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Authoritative || !reflect.DeepEqual(discovery.Models, []string{"gpt-5.5"}) {
		t.Fatalf("unexpected fallback discovery: %#v", discovery)
	}
	wantRequests := []string{"/api/v1/client/model-options", "/v1/models?include_aliases=false"}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Fatalf("requests = %#v, want %#v", requests, wantRequests)
	}
}

func TestModelDiscoveryDoesNotHideCapabilityEndpointAuthErrors(t *testing.T) {
	previousTransport := http.DefaultTransport
	requests := 0
	http.DefaultTransport = appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusUnauthorized, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"code":401,"message":"invalid key"}`)), Request: request,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	r := &runner{ctx: context.Background()}
	_, err := r.modelsForCredential("https://beeapi.test", "sk-invalid")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("unexpected authentication error: %v", err)
	}
	if requests != 1 {
		t.Fatalf("auth failure unexpectedly fell back to /v1/models: %d requests", requests)
	}
}

func TestCredentialModelDiscoverySkipsOneUnusableKeyWithoutStoppingOthers(t *testing.T) {
	previousTransport := http.DefaultTransport
	http.DefaultTransport = appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") == "Bearer sk-empty" {
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"code":0,"message":"success","data":{"models":[]}}`)), Request: request,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"code":0,"message":"success","data":{"models":[{"id":"gpt-5.6","protocols":["openai/responses"],"recommended_for":["codex"],"priority":100}]}}`)), Request: request,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	var output bytes.Buffer
	r := &runner{ctx: context.Background(), out: &output, errOut: &output}
	credentials, err := r.discoverCredentialModels("https://beeapi.test", []credentialMaterial{
		{ID: "empty", Name: "无路由 Key", Secret: "sk-empty"},
		{ID: "usable", Name: "Codex Key", Secret: "sk-usable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 || credentials[0].ID != "usable" || !credentials[0].ModelOptionsAuthoritative {
		t.Fatalf("unexpected usable credentials: %#v", credentials)
	}
	if !strings.Contains(output.String(), "无路由 Key · 已跳过") {
		t.Fatalf("unusable-key notice missing:\n%s", output.String())
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
