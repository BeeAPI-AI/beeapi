package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func TestOAuthKeySelectionExportsOnlyChosenKeyBeforeAcknowledging(t *testing.T) {
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: t.TempDir()}
	if err := store.SaveOAuthAccount(state.OAuthAccount{
		Protocol: beeapi.OAuthAccountProtocol, Issuer: "https://beeapi.dev", ClientID: beeapi.OAuthClientID,
		TokenCredentialID: oauthTokenCredentialID, TokenBackend: "protected-file",
	}); err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader("2\n")
	var output bytes.Buffer
	r := &runner{
		ctx: context.Background(), in: input, reader: bufio.NewReader(input),
		out: &output, errOut: &output, store: store,
	}
	client := beeapi.New("https://beeapi.dev")
	client.Token = "boa-account"
	var exportedIDs []int
	acknowledged := false
	client.HTTP = &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := func(status int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		}
		if request.Header.Get("Authorization") != "DPoP boa-account" || request.Header.Get("DPoP") == "" {
			t.Fatalf("missing sender-constrained account token: %#v", request.Header)
		}
		switch request.URL.Path {
		case "/api/v1/oauth/account":
			return response(http.StatusOK, `{"code":0,"data":{"sub":"usr-1","username":"alice","email":"alice@example.test"}}`)
		case "/api/v1/oauth/account/balance":
			return response(http.StatusOK, `{"code":0,"data":{"available":12.5,"currency":"USD"}}`)
		case "/api/v1/oauth/api-keys":
			return response(http.StatusOK, `{"code":0,"data":{"items":[{"id":1,"name":"Claude","key_prefix":"sk-one","status":"enabled","exportable":true},{"id":2,"name":"Coding","key_prefix":"sk-two","status":"enabled","exportable":true}]}}`)
		case "/api/v1/oauth/api-keys/1/model-options":
			return response(http.StatusOK, `{"code":0,"data":{"models":[{"id":"claude-sonnet-4","protocols":["anthropic/messages"],"priority":100}]}}`)
		case "/api/v1/oauth/api-keys/2/model-options":
			return response(http.StatusOK, `{"code":0,"data":{"models":[{"id":"gpt-5.6-sol","protocols":["openai/responses"],"priority":100}]}}`)
		case "/api/v1/oauth/api-key-exports":
			if request.Header.Get("Idempotency-Key") == "" {
				t.Fatal("missing Idempotency-Key")
			}
			var body struct {
				IDs []int `json:"api_key_ids"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			exportedIDs = body.IDs
			return response(http.StatusOK, `{"code":0,"data":{"export_id":"export-1","retry_until":"2099-01-01T00:00:00Z","credentials":[{"api_key_id":2,"key_name":"Coding","key_prefix":"sk-two","api_key":"sk-secret-two"}]}}`)
		case "/api/v1/oauth/api-key-exports/export-1/ack":
			storedSecret, loadErr := store.LoadNamedCredential("protected-file", stableCredentialID("sk-secret-two"))
			if loadErr != nil || storedSecret != "sk-secret-two" {
				t.Fatal("server export was acknowledged before local secret storage")
			}
			acknowledged = true
			return response(http.StatusNoContent, "")
		default:
			t.Fatalf("unexpected OAuth resource request: %s %s", request.Method, request.URL.Path)
			return nil, nil
		}
	})}

	authorized, err := r.selectAndExportOAuthCredentials(client, state.OAuthAccount{
		Protocol: beeapi.OAuthAccountProtocol, Issuer: "https://beeapi.dev", ClientID: beeapi.OAuthClientID,
		TokenCredentialID: oauthTokenCredentialID, TokenBackend: "protected-file",
	}, "https://beeapi.dev", pendingModeSetup)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(exportedIDs, []int{2}) || !acknowledged {
		t.Fatalf("selection/export mismatch: ids=%#v ack=%v", exportedIDs, acknowledged)
	}
	if len(authorized.Credentials) != 1 || authorized.Credentials[0].Secret != "sk-secret-two" || authorized.Credentials[0].Models[0] != "gpt-5.6-sol" {
		t.Fatalf("unexpected authorized credentials: %#v", authorized)
	}
	pending, err := store.LoadPendingSetup()
	if err != nil {
		t.Fatal(err)
	}
	if pending.OAuthExportID != "" || len(pending.Credentials) != 1 {
		t.Fatalf("unexpected post-ack checkpoint: %#v", pending)
	}
	raw, err := os.ReadFile(store.PendingSetupPath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("sk-secret-two")) || bytes.Contains(raw, []byte("boa-account")) {
		t.Fatalf("checkpoint leaked a secret: %s", raw)
	}
}

func TestNormalizeOAuthModelOptionsPreservesServerRanking(t *testing.T) {
	options := normalizeModelOptions([]beeapi.ModelOption{
		{ID: "low", Priority: 10}, {ID: " high ", Priority: 100}, {ID: "high", Priority: 1}, {ID: "", Priority: 999},
	})
	if len(options) != 2 || options[0].ID != "low" || options[1].ID != "high" {
		t.Fatalf("unexpected normalized options: %#v", options)
	}
}

func TestExpiredOAuthExportCanResumeWithoutAccountToken(t *testing.T) {
	store := &state.Store{Dir: t.TempDir()}
	if err := store.SavePendingSetup(state.PendingSetup{
		Mode: pendingModeSetup, Endpoint: "https://beeapi.dev",
		Credentials: []state.Credential{{
			ID: "key-one", Name: "Coding", Backend: "protected-file",
		}},
		OAuthExportID:         "export-expired",
		OAuthExportRetryUntil: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	r := &runner{ctx: context.Background(), out: &output, errOut: &output, store: store}
	pending, err := store.LoadPendingSetup()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.resumePendingOAuthExport(pending); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.LoadPendingSetup()
	if err != nil {
		t.Fatal(err)
	}
	if resumed.OAuthExportID != "" || !resumed.OAuthExportRetryUntil.IsZero() {
		t.Fatalf("expired export checkpoint was not cleared: %#v", resumed)
	}
	if !strings.Contains(output.String(), "本地保存的 Key 可继续使用") {
		t.Fatalf("missing expired-export recovery notice: %s", output.String())
	}
}

func TestPendingOAuthExportDoesNotBlockSavedKeysWhenAccountSessionIsGone(t *testing.T) {
	store := &state.Store{Dir: t.TempDir()}
	if err := store.SavePendingSetup(state.PendingSetup{
		Mode: pendingModeSetup, Endpoint: "https://beeapi.dev",
		Credentials:   []state.Credential{{ID: "key-one", Name: "Coding", Backend: "protected-file"}},
		OAuthExportID: "export-awaiting-ack", OAuthExportRetryUntil: time.Now().Add(3 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	r := &runner{ctx: context.Background(), out: &output, errOut: &output, store: store}
	pending, err := store.LoadPendingSetup()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.resumePendingOAuthExport(pending); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.LoadPendingSetup()
	if err != nil {
		t.Fatal(err)
	}
	if resumed.OAuthExportID != "" {
		t.Fatalf("unacknowledgeable export still blocks setup: %#v", resumed)
	}
	if !strings.Contains(output.String(), "服务端短期密文会自动过期") {
		t.Fatalf("missing safe cleanup explanation: %s", output.String())
	}
}

func TestPendingOAuthExportIsNotClearedBeforeLocalKeysAreVerified(t *testing.T) {
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: t.TempDir()}
	if err := store.SavePendingSetup(state.PendingSetup{
		Mode: pendingModeSetup, Endpoint: "https://beeapi.dev",
		Credentials:   []state.Credential{{ID: "missing-key", Name: "Coding", Backend: "protected-file"}},
		OAuthExportID: "export-still-recoverable", OAuthExportRetryUntil: time.Now().Add(3 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	r := &runner{ctx: context.Background(), out: io.Discard, errOut: io.Discard, store: store}
	pending, err := store.LoadPendingSetup()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.restorePendingCredentialMaterials(pending); err == nil {
		t.Fatal("missing local credential unexpectedly resumed")
	}
	resumed, err := store.LoadPendingSetup()
	if err != nil {
		t.Fatal(err)
	}
	if resumed.OAuthExportID != "export-still-recoverable" {
		t.Fatalf("server export was cleared before local credentials were verified: %#v", resumed)
	}
}

func TestStaleOAuthResumeCannotClearANewerExportCheckpoint(t *testing.T) {
	store := &state.Store{Dir: t.TempDir()}
	if err := store.SavePendingSetup(state.PendingSetup{
		Mode: pendingModeSetup, Endpoint: "https://beeapi.dev",
		Credentials:   []state.Credential{{ID: "key-one", Name: "Coding", Backend: "protected-file"}},
		OAuthExportID: "export-new", OAuthExportRetryUntil: time.Now().Add(3 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	r := &runner{store: store}
	if err := r.clearPendingOAuthExport(pendingModeSetup, "export-old"); err != nil {
		t.Fatal(err)
	}
	pending, err := store.LoadPendingSetup()
	if err != nil {
		t.Fatal(err)
	}
	if pending.OAuthExportID != "export-new" {
		t.Fatalf("stale process cleared the newer export checkpoint: %#v", pending)
	}
}

func TestOAuthKeySelectionRetriesInvalidInputInPlace(t *testing.T) {
	input := strings.NewReader("99\n2\n")
	var output bytes.Buffer
	r := &runner{
		ctx: context.Background(), in: input, reader: bufio.NewReader(input),
		out: &output, errOut: &output,
	}
	choices := []oauthKeyChoice{
		{Key: beeapi.OAuthAPIKey{ID: "1", Name: "One", Exportable: true}, Models: []string{"model-one"}},
		{Key: beeapi.OAuthAPIKey{ID: "2", Name: "Two", Exportable: true}, Models: []string{"model-two"}},
	}
	selected, err := r.selectOAuthKeyChoices(choices)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Key.ID != "2" {
		t.Fatalf("unexpected selection after retry: %#v", selected)
	}
	if !strings.Contains(output.String(), "编号无效，请重新选择") {
		t.Fatalf("missing in-place retry notice: %s", output.String())
	}
}

func TestOAuthKeySelectionForToolShowsOnlyCompatibleKeysAndSelectsOne(t *testing.T) {
	input := strings.NewReader("2\n")
	var output bytes.Buffer
	r := &runner{
		ctx: context.Background(), in: input, reader: bufio.NewReader(input),
		out: &output, errOut: &output,
	}
	choices := []oauthKeyChoice{
		{
			Key:    beeapi.OAuthAPIKey{ID: "chat", Name: "Chat only", KeyPrefix: "sk-chat", Exportable: true},
			Models: []string{"chat-model"}, Options: []beeapi.ModelOption{{
				ID: "chat-model", Protocols: []string{"openai/chat_completions"}, RecommendedFor: []string{"openai_compatible"}, Priority: 100,
			}},
		},
		{
			Key:    beeapi.OAuthAPIKey{ID: "codex-a", Name: "Codex A", KeyPrefix: "sk-a", Exportable: true},
			Models: []string{"gpt-a"}, Options: []beeapi.ModelOption{{
				ID: "gpt-a", Protocols: []string{"openai/responses"}, RecommendedFor: []string{"codex"}, Priority: 100,
			}},
		},
		{
			Key:    beeapi.OAuthAPIKey{ID: "codex-b", Name: "Codex B", KeyPrefix: "sk-b", Exportable: true},
			Models: []string{"gpt-b"}, Options: []beeapi.ModelOption{{
				ID: "gpt-b", Protocols: []string{"openai/responses"}, RecommendedFor: []string{"codex"}, Priority: 90,
			}},
		},
	}
	selected, err := r.selectOAuthKeyChoiceForAgent(choices, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Key.ID != "codex-b" {
		t.Fatalf("unexpected single-tool Key selection: %#v", selected)
	}
	text := output.String()
	if strings.Contains(text, "Chat only") || strings.Contains(text, "sk-chat") {
		t.Fatalf("incompatible Key was shown in the selectable list:\n%s", text)
	}
	if !strings.Contains(text, "已隐藏 1 个不可用于 Codex 的 API Key") {
		t.Fatalf("hidden incompatible Key count was not explained:\n%s", text)
	}
	if strings.Contains(text, "可逗号多选") {
		t.Fatalf("single-tool flow still offered multi-select:\n%s", text)
	}
}

func TestAuthorizeReusesConnectedOAuthSessionAndRecoversLostExportResponse(t *testing.T) {
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: t.TempDir()}
	input := strings.NewReader("\n\n")
	var output bytes.Buffer
	r := &runner{
		ctx: context.Background(), in: input, reader: bufio.NewReader(input),
		out: &output, errOut: &output, store: store,
	}
	client := beeapi.New("https://beeapi.dev")
	_, err := r.saveOAuthSession(beeapi.OAuthMetadata{Issuer: beeapi.OAuthIssuerDev}, beeapi.OAuthToken{
		AccessToken: "boa_reusable", RefreshToken: "bor_reusable", TokenType: "DPoP", ExpiresIn: 600,
		Scope: beeapi.OAuthScopeString(beeapi.OAuthAccountScopes),
	}, client)
	if err != nil {
		t.Fatal(err)
	}

	previousTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = previousTransport }()
	exportCalls := 0
	var idempotencyKeys []string
	http.DefaultTransport = appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := func(status int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		}
		if request.Header.Get("Authorization") != "DPoP boa_reusable" || request.Header.Get("DPoP") == "" {
			t.Fatalf("missing reusable DPoP session: %#v", request.Header)
		}
		switch request.URL.Path {
		case "/api/v1/oauth/account":
			return response(http.StatusOK, `{"code":0,"data":{"id":7,"username":"alice","email":"alice@example.test"}}`)
		case "/api/v1/oauth/account/balance":
			return response(http.StatusOK, `{"code":0,"data":{"available":8.5,"currency":"USD"}}`)
		case "/api/v1/oauth/api-keys":
			return response(http.StatusOK, `{"code":0,"data":{"items":[{"id":9,"name":"Coding","key_prefix":"sk-nine","status":"enabled","exportable":true}]}}`)
		case "/api/v1/oauth/api-keys/9/model-options":
			return response(http.StatusOK, `{"code":0,"data":{"models":[{"id":"gpt-5.6-sol","protocols":["openai/responses"],"priority":100}]}}`)
		case "/api/v1/oauth/api-key-exports":
			exportCalls++
			idempotencyKeys = append(idempotencyKeys, request.Header.Get("Idempotency-Key"))
			if exportCalls == 1 {
				return nil, errors.New("connection reset after request")
			}
			return response(http.StatusOK, `{"code":0,"data":{"export_id":"export-recovered","retry_until":"2099-01-01T00:00:00Z","credentials":[{"api_key_id":9,"key_name":"Coding","key_prefix":"sk-nine","api_key":"sk-recovered"}]}}`)
		case "/api/v1/oauth/api-key-exports/export-recovered/ack":
			return response(http.StatusOK, `{"code":0,"data":{"acknowledged":true}}`)
		default:
			t.Fatalf("unexpected OAuth request: %s %s", request.Method, request.URL.Path)
			return nil, nil
		}
	})

	opened := false
	r.openBrowser = func(string) error { opened = true; return nil }
	authorized, err := r.authorize("https://beeapi.dev", false, pendingModeSetup)
	if err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Fatal("a reusable OAuth session unexpectedly opened a new browser authorization")
	}
	if len(authorized.Credentials) != 1 || authorized.Credentials[0].Secret != "sk-recovered" {
		t.Fatalf("unexpected recovered credentials: %#v", authorized)
	}
	if exportCalls != 2 || len(idempotencyKeys) != 2 || idempotencyKeys[0] == "" || idempotencyKeys[0] != idempotencyKeys[1] {
		t.Fatalf("export retry did not preserve idempotency: calls=%d keys=%#v", exportCalls, idempotencyKeys)
	}
	text := output.String()
	if !strings.Contains(text, "继续使用此账户") || !strings.Contains(text, "使用同一请求安全恢复") {
		t.Fatalf("missing OAuth resume/recovery output: %s", text)
	}
}

func TestAuthorizeRequiresFreshOAuthWhenTheSelectedIssuerChanges(t *testing.T) {
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: t.TempDir()}
	input := strings.NewReader("9\n")
	var output bytes.Buffer
	r := &runner{
		ctx: context.Background(), in: input, reader: bufio.NewReader(input),
		out: &output, errOut: &output, store: store,
	}
	client := beeapi.New(beeapi.OAuthIssuerDev)
	if _, err := r.saveOAuthSession(beeapi.OAuthMetadata{Issuer: beeapi.OAuthIssuerDev}, beeapi.OAuthToken{
		AccessToken: "boa_dev", RefreshToken: "bor_dev", TokenType: "DPoP", ExpiresIn: 600,
		Scope: beeapi.OAuthScopeString(beeapi.OAuthAccountScopes),
	}, client); err != nil {
		t.Fatal(err)
	}

	if _, err := r.authorize(beeapi.OAuthIssuerAI, false, pendingModeSetup); err == nil || !strings.Contains(err.Error(), "1 或 2") {
		t.Fatalf("issuer switch did not enter fresh authorization: %v", err)
	}
	text := output.String()
	if !strings.Contains(text, "必须重新进行网页授权") || strings.Contains(text, "继续使用此账户") {
		t.Fatalf("issuer switch reused the old OAuth session:\n%s", text)
	}
	account, secret, err := r.loadOAuthSession()
	if err != nil || account.Issuer != beeapi.OAuthIssuerDev || secret.Issuer != beeapi.OAuthIssuerDev {
		t.Fatalf("an aborted issuer switch destroyed the prior session: account=%#v secret=%#v err=%v", account, secret, err)
	}
}

func TestOAuthExportUnavailableAfterLocalSaveIsTerminalSuccess(t *testing.T) {
	var output bytes.Buffer
	r := &runner{ctx: context.Background(), out: &output, errOut: &output}
	client := beeapi.New("https://beeapi.dev")
	client.Token = "boa-account"
	client.HTTP = &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"code":410,"message":"expired","reason":"oauth.export_unavailable"}`
		return &http.Response{StatusCode: http.StatusGone, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	if err := r.ackOAuthExport(client, "export-expired"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "本地 Key 已安全保存") {
		t.Fatalf("missing terminal export recovery notice: %s", output.String())
	}
}
