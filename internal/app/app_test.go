package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
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
	authorized, err := r.authorize("https://beeapi.dev", true, pendingModeSetup)
	if err != nil {
		t.Fatal(err)
	}
	credentials := authorized.Credentials
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

func TestOAuthCompatibilityFallbackOnlyAcceptsUnavailableDiscovery(t *testing.T) {
	if !oauthCapabilityUnavailable(beeapi.ErrOAuthDiscoveryUnavailable) {
		t.Fatal("official SPA discovery fallback did not enable compatibility mode")
	}
	if oauthCapabilityUnavailable(errors.New("untrusted OAuth issuer")) {
		t.Fatal("an OAuth trust failure incorrectly enabled compatibility mode")
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

func TestCredentialCheckpointCanResumeWithoutAnotherAuthorization(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: home}
	input := strings.NewReader("\n")
	var output bytes.Buffer
	r := &runner{
		ctx: context.Background(), in: input, reader: bufio.NewReader(input),
		out: &output, errOut: &output, store: store,
	}

	stored, err := r.checkpointCredentialMaterials(pendingModeSetup, "https://beeapi.dev", []credentialMaterial{{
		ID: "key-one", Name: "Coding", Prefix: "sk-test", Secret: "sk-resumable-secret",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Backend != "protected-file" {
		t.Fatalf("unexpected checkpoint credentials: %#v", stored)
	}

	pending, resume, err := r.pendingSetupForMode(pendingModeSetup, false)
	if err != nil {
		t.Fatal(err)
	}
	if !resume || pending.Endpoint != "https://beeapi.dev" {
		t.Fatalf("checkpoint was not resumed: resume=%v pending=%#v", resume, pending)
	}
	credentials, err := r.loadPendingCredentialMaterials(pending)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 || credentials[0].Secret != "sk-resumable-secret" {
		t.Fatalf("resumed credential mismatch: %#v", credentials)
	}
	if raw, err := os.ReadFile(store.PendingSetupPath()); err != nil || bytes.Contains(raw, []byte("sk-resumable-secret")) {
		t.Fatalf("pending checkpoint leaked API Key plaintext: %s, err=%v", raw, err)
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

func TestUnavailableOAuthNeverDowngradesToTheRetiredDeviceProtocol(t *testing.T) {
	previousTransport := http.DefaultTransport
	var requestedPaths []string
	http.DefaultTransport = appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestedPaths = append(requestedPaths, request.URL.Path)
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
	_, err := r.authorize("https://beeapi.dev", false, pendingModeSetup)
	if err == nil || !strings.Contains(err.Error(), "OAuth 授权未完成") {
		t.Fatalf("unexpected unavailable authorization result: %v", err)
	}
	text := output.String()
	for _, want := range []string{
		"旧版 getbeeapi-cli / cli:configure 设备协议已停用",
		"不会降级或跨域重试",
		"重新选择另一个 BeeAPI 入口并重新授权",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("unavailable authorization output is missing %q:\n%s", want, text)
		}
	}
	if !reflect.DeepEqual(requestedPaths, []string{"/.well-known/oauth-authorization-server"}) {
		t.Fatalf("unavailable OAuth unexpectedly called the retired protocol: %#v", requestedPaths)
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

func TestRunEnvironmentsDoNotOverrideNativeConfigHomes(t *testing.T) {
	cfg := state.Config{
		Endpoint: "https://beeapi.dev",
		Models: map[string]string{
			"grok":   "gpt-5-codex",
			"hermes": "hermes-4",
		},
	}

	grok := agentEnvironment("grok", cfg, "sk-test")
	if !containsString(grok, "BEEAPI_API_KEY=sk-test") || containsPrefix(grok, "GROK_HOME=") {
		t.Fatalf("unexpected Grok environment: %#v", grok)
	}

	hermes := agentEnvironment("hermes", cfg, "sk-test")
	for _, want := range []string{"OPENAI_API_KEY=sk-test", "OPENAI_BASE_URL=https://beeapi.dev/v1", "HERMES_INFERENCE_MODEL=hermes-4"} {
		if !containsString(hermes, want) {
			t.Fatalf("Hermes environment missing %q: %#v", want, hermes)
		}
	}
	if containsPrefix(hermes, "HERMES_HOME=") {
		t.Fatalf("Hermes native home was unexpectedly overridden: %#v", hermes)
	}
}

func TestRunWithoutArgumentsOpensHomeAfterInitialSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GETBEE_HOME", home)
	t.Setenv("BEEAPI_LANG", "zh-CN")
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
	for _, want := range []string{"____", "BeeAPI CLI v0-test", "当前方案  默认配置", "快速切换配置方案", "密钥与余额", "启动已配置的 AI 工具", "已退出 BeeAPI CLI"} {
		if !strings.Contains(text, want) {
			t.Fatalf("returning-user shell is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "AI 工具配置中心") || strings.Contains(text, "欢迎回来") {
		t.Fatalf("legacy shell title still appears:\n%s", text)
	}
	if strings.Contains(text, "\n首次设置 ·") || strings.Contains(text, "[1/3] 检测 BeeAPI") || strings.Contains(text, "Choose your language / 选择语言") {
		t.Fatalf("returning user unexpectedly entered first-run setup:\n%s", text)
	}
	saved, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Language != languageChinese {
		t.Fatalf("migrated language = %q, want %q", saved.Language, languageChinese)
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
	err := Run(context.Background(), nil, "v0-test", strings.NewReader("\n\n2\n\n"), &output, &output)
	if err == nil || !strings.Contains(err.Error(), "API Key 不能为空") {
		t.Fatalf("first setup should stop on the intentionally empty fallback key, got %v", err)
	}
	text := output.String()
	for _, want := range []string{"BeeAPI CLI v0-test", "Choose your language / 选择语言", "首次设置", "[1/3] 检测 BeeAPI", "[2/3] 连接 BeeAPI"} {
		if !strings.Contains(text, want) {
			t.Fatalf("first-run guide is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "欢迎回来") {
		t.Fatalf("uninitialized user unexpectedly entered the daily shell:\n%s", text)
	}
}

func TestFirstRunEnglishLanguageChoiceIsPersistedBeforeSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GETBEE_HOME", home)
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
	err := Run(context.Background(), nil, "v0-test", strings.NewReader("2\n\n2\n\n"), &output, &output)
	if err == nil || !strings.Contains(err.Error(), "API Key is required") {
		t.Fatalf("English setup should stop on the intentionally empty fallback key, got %v", err)
	}
	text := output.String()
	for _, want := range []string{"Choose your language / 选择语言", "First-time setup", "[1/3] Check official BeeAPI endpoints", "[2/3] Connect BeeAPI"} {
		if !strings.Contains(text, want) {
			t.Fatalf("English first-run guide is missing %q:\n%s", want, text)
		}
	}
	store := &state.Store{Dir: home}
	cfg, loadErr := store.LoadConfig()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if cfg.Language != languageEnglish {
		t.Fatalf("saved language = %q, want %q", cfg.Language, languageEnglish)
	}
}

func TestSavedEnglishLanguageOpensEnglishHomeWithoutPrompt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GETBEE_HOME", home)
	store := &state.Store{Dir: home}
	if err := store.SaveConfig(state.Config{
		Language:          languageEnglish,
		Endpoint:          "https://beeapi.dev",
		CredentialBackend: "protected-file",
	}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := Run(context.Background(), nil, "v0-test", strings.NewReader("0\n"), &output, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"Profile", "Quick-switch profile", "API Keys and balance", "Exited BeeAPI CLI"} {
		if !strings.Contains(text, want) {
			t.Fatalf("English home is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Choose your language / 选择语言") || strings.Contains(text, "快速切换配置方案") {
		t.Fatalf("saved English home unexpectedly prompted or used Chinese menu text:\n%s", text)
	}
}

func TestLanguageCommandPersistsSelectionAndLocalizesHelp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GETBEE_HOME", home)
	var output bytes.Buffer
	if err := Run(context.Background(), []string{"language", "en"}, "v0-test", strings.NewReader(""), &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Language changed to English") {
		t.Fatalf("language confirmation is not English:\n%s", output.String())
	}
	output.Reset()
	if err := Run(context.Background(), []string{"help"}, "v0-test", strings.NewReader(""), &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "quickly configure BeeAPI") || strings.Contains(output.String(), "为现有 AI 工具") {
		t.Fatalf("saved language was not used for help:\n%s", output.String())
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

func TestEditingProfileKeepsCurrentCompatibleModelAsDefault(t *testing.T) {
	credential := credentialMaterial{
		ID: "responses", Name: "Coding", Models: []string{"gpt-5.5", "gpt-5.6-sol"},
		ModelOptionsAuthoritative: true,
		ModelOptions: []beeapi.ModelOption{
			{ID: "gpt-5.5", Protocols: []string{"openai/responses"}, RecommendedFor: []string{"codex"}, Priority: 90},
			{ID: "gpt-5.6-sol", Protocols: []string{"openai/responses"}, RecommendedFor: []string{"codex"}, Priority: 100},
		},
	}
	input := strings.NewReader("\n")
	var output bytes.Buffer
	r := &runner{in: input, reader: bufio.NewReader(input), out: &output, errOut: &output}
	models, err := r.selectModelsForAssignmentsWithDefaults(
		[]string{"codex"}, []credentialMaterial{credential}, map[string]string{"codex": "responses"},
		map[string]string{"codex": "gpt-5.5"}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if models["codex"] != "gpt-5.5" {
		t.Fatalf("selected model = %q, want current compatible model", models["codex"])
	}
	text := output.String()
	if !strings.Contains(text, "1. gpt-5.5 · 当前") || !strings.Contains(text, "gpt-5.6-sol · BeeAPI 推荐") {
		t.Fatalf("current/recommended model labels are missing:\n%s", text)
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
