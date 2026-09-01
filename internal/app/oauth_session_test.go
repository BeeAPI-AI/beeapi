package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func TestOAuthSessionStoresTokensAndDPoPKeyOutsideMetadata(t *testing.T) {
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: t.TempDir()}
	var output bytes.Buffer
	r := &runner{ctx: context.Background(), reader: bufio.NewReader(strings.NewReader("")), out: &output, errOut: &output, store: store}
	client := beeapi.New("https://beeapi.dev")
	metadata := beeapi.OAuthMetadata{Issuer: "https://beeapi.dev"}
	account, err := r.saveOAuthSession(metadata, beeapi.OAuthToken{
		AccessToken: "boa_secret", RefreshToken: "bor_secret", TokenType: "DPoP", ExpiresIn: 600,
		Scope: "account:profile:read offline_access",
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	if account.Protocol != beeapi.OAuthAccountProtocol || account.TokenBackend != "protected-file" {
		t.Fatalf("unexpected OAuth account: %#v", account)
	}
	loadedAccount, secret, err := r.loadOAuthSession()
	if err != nil {
		t.Fatal(err)
	}
	if loadedAccount.Issuer != metadata.Issuer || secret.AccessToken != "boa_secret" || secret.RefreshToken != "bor_secret" || secret.DPoPPrivateJWK == "" {
		t.Fatalf("unexpected stored OAuth session: account=%#v secret=%#v", loadedAccount, secret)
	}
	rawMetadata, err := os.ReadFile(store.OAuthAccountPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawMetadata), "boa_secret") || strings.Contains(string(rawMetadata), "bor_secret") || strings.Contains(string(rawMetadata), `"d":`) {
		t.Fatalf("OAuth metadata leaked a secret: %s", rawMetadata)
	}
}

func TestDisconnectCleansIncompleteOAuthMetadataWithoutTouchingAPIKeys(t *testing.T) {
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: t.TempDir()}
	apiKeyBackend, err := store.SaveNamedCredential("api-key-one", "sk-stays-local")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOAuthAccount(state.OAuthAccount{
		Protocol: beeapi.OAuthAccountProtocol, Issuer: beeapi.OAuthCanonicalIssuer, ClientID: beeapi.OAuthClientID,
		TokenCredentialID: oauthTokenCredentialID, TokenBackend: "protected-file",
	}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	r := &runner{ctx: context.Background(), out: &output, errOut: &output, store: store}
	if err := r.disconnectOAuthAccount(); err != nil {
		t.Fatal(err)
	}
	account, err := store.LoadOAuthAccount()
	if err != nil || account.TokenCredentialID != "" {
		t.Fatalf("OAuth metadata was not cleared: %#v err=%v", account, err)
	}
	secret, err := store.LoadNamedCredential(apiKeyBackend, "api-key-one")
	if err != nil || secret != "sk-stays-local" {
		t.Fatalf("disconnect removed an API Key: %q err=%v", secret, err)
	}
}

func TestOAuthRefreshRecoversLostResponseAndPersistsRotation(t *testing.T) {
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: t.TempDir()}
	var output bytes.Buffer
	r := &runner{ctx: context.Background(), out: &output, errOut: &output, store: store}
	initialClient := beeapi.New("https://beeapi.dev")
	_, err := r.saveOAuthSession(beeapi.OAuthMetadata{Issuer: beeapi.OAuthCanonicalIssuer}, beeapi.OAuthToken{
		AccessToken: "boa_old", RefreshToken: "bor_old", TokenType: "DPoP", ExpiresIn: 1,
		Scope: beeapi.OAuthScopeString(beeapi.OAuthAccountScopes),
	}, initialClient)
	if err != nil {
		t.Fatal(err)
	}
	_, before, err := r.loadOAuthSession()
	if err != nil {
		t.Fatal(err)
	}

	previousTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = previousTransport }()
	refreshCalls := 0
	http.DefaultTransport = appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := func(status int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		}
		if request.URL.Path == "/.well-known/oauth-authorization-server" {
			return response(http.StatusOK, `{"issuer":"https://beeapi.dev","authorization_endpoint":"https://beeapi.dev/oauth/authorize","token_endpoint":"https://beeapi.dev/oauth/token","device_authorization_endpoint":"https://beeapi.dev/oauth/device/code","revocation_endpoint":"https://beeapi.dev/oauth/revoke","response_types_supported":["code"],"grant_types_supported":["authorization_code","refresh_token","urn:ietf:params:oauth:grant-type:device_code"],"code_challenge_methods_supported":["S256"],"token_endpoint_auth_methods_supported":["none"],"scopes_supported":["account:profile:read","account:balance:read","api_keys:read","api_keys:export","offline_access"],"dpop_signing_alg_values_supported":["ES256"]}`)
		}
		if request.URL.Path != "/oauth/token" {
			t.Fatalf("unexpected refresh request: %s", request.URL.Path)
		}
		refreshCalls++
		if request.Header.Get("DPoP") == "" {
			t.Fatal("refresh request is missing DPoP proof")
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if request.Form.Get("refresh_token") != "bor_old" {
			t.Fatalf("refresh retry changed token: %#v", request.Form)
		}
		if refreshCalls == 1 {
			return nil, errors.New("connection reset after refresh rotation")
		}
		return response(http.StatusOK, `{"access_token":"boa_new","refresh_token":"bor_new","token_type":"DPoP","expires_in":300,"scope":"account:profile:read account:balance:read api_keys:read offline_access"}`)
	})

	connected, account, err := r.oauthAccountClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if refreshCalls != 2 || connected.Token != "boa_new" || containsScope(account.Scope, "api_keys:export") {
		t.Fatalf("unexpected refreshed session: calls=%d token=%q account=%#v", refreshCalls, connected.Token, account)
	}
	_, after, err := r.loadOAuthSession()
	if err != nil {
		t.Fatal(err)
	}
	if after.AccessToken != "boa_new" || after.RefreshToken != "bor_new" || after.DPoPPrivateJWK != before.DPoPPrivateJWK {
		t.Fatalf("refresh rotation was not persisted with the same device key: before=%#v after=%#v", before, after)
	}
	if !strings.Contains(output.String(), "续期响应暂时中断") {
		t.Fatalf("missing refresh recovery notice: %s", output.String())
	}
}

func TestOAuthSessionRejectsTamperedIssuerBeforeNetworkUse(t *testing.T) {
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: t.TempDir()}
	var output bytes.Buffer
	r := &runner{ctx: context.Background(), out: &output, errOut: &output, store: store}
	client := beeapi.New("https://beeapi.dev")
	account, err := r.saveOAuthSession(beeapi.OAuthMetadata{Issuer: beeapi.OAuthCanonicalIssuer}, beeapi.OAuthToken{
		AccessToken: "boa_valid", RefreshToken: "bor_valid", TokenType: "DPoP", ExpiresIn: 600,
		Scope: "account:profile:read offline_access",
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	account.Issuer = "https://attacker.example"
	if err := store.SaveOAuthAccount(account); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.loadOAuthSession(); err == nil || !strings.Contains(err.Error(), "不可信") {
		t.Fatalf("tampered issuer was accepted: %v", err)
	}
}
