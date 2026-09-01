package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
)

func TestOAuthCallbackRequiresExactStateBeforeDeliveringCode(t *testing.T) {
	results := make(chan oauthCallbackResult, 1)
	handler := oauthCallbackHandler("expected-state", "https://beeapi.dev", results)

	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/oauth/callback?code=stolen&state=wrong&iss=https%3A%2F%2Fbeeapi.dev", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad callback status = %d", bad.Code)
	}
	select {
	case result := <-results:
		t.Fatalf("invalid state delivered a callback result: %#v", result)
	default:
	}

	good := httptest.NewRecorder()
	handler.ServeHTTP(good, httptest.NewRequest(http.MethodGet, "/oauth/callback?code=one-time-code&state=expected-state&iss=https%3A%2F%2Fbeeapi.dev", nil))
	if good.Code != http.StatusOK || !strings.Contains(good.Body.String(), "返回终端") {
		t.Fatalf("unexpected successful callback: status=%d body=%s", good.Code, good.Body.String())
	}
	if good.Header().Get("Cache-Control") != "no-store" || !strings.Contains(good.Header().Get("Content-Security-Policy"), "default-src 'none'") || good.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("OAuth callback is missing browser hardening headers: %#v", good.Header())
	}
	result := <-results
	if result.Err != nil || result.Code != "one-time-code" {
		t.Fatalf("unexpected callback result: %#v", result)
	}
}

func TestOAuthCallbackReportsAuthorizationDenialWithoutReflectingDescription(t *testing.T) {
	results := make(chan oauthCallbackResult, 1)
	handler := oauthCallbackHandler("state", "https://beeapi.dev", results)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/oauth/callback?error=access_denied&error_description=%3Cscript%3E&state=state&iss=https%3A%2F%2Fbeeapi.dev", nil))
	result := <-results
	if result.Err == nil || !strings.Contains(result.Err.Error(), "access_denied") {
		t.Fatalf("unexpected denial result: %#v", result)
	}
	if strings.Contains(recorder.Body.String(), "<script>") {
		t.Fatalf("callback reflected untrusted error description: %s", recorder.Body.String())
	}
}

func TestOAuthCallbackRejectsTheOtherOfficialIssuer(t *testing.T) {
	results := make(chan oauthCallbackResult, 1)
	handler := oauthCallbackHandler("state", beeapi.OAuthIssuerAI, results)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code&state=state&iss=https%3A%2F%2Fbeeapi.dev", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("cross-issuer callback status = %d", recorder.Code)
	}
	select {
	case result := <-results:
		t.Fatalf("cross-issuer callback delivered a code: %#v", result)
	default:
	}
}

func TestRandomBase64URLProducesIndependentHighEntropyValues(t *testing.T) {
	first, err := randomBase64URL(32)
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomBase64URL(32)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) < 40 || strings.ContainsAny(first, "+/=") {
		t.Fatalf("unexpected random values: %q %q", first, second)
	}
}

func TestOAuthDeviceVerificationCannotSwitchBackToOtherOfficialAlias(t *testing.T) {
	err := validateOAuthDeviceVerificationURL("https://beeapi.dev", beeapi.DeviceCode{
		UserCode: "BEE7-K9Q2", CompleteURI: "https://beeapi.ai/cli/authorize?user_code=BEE7-K9Q2",
	})
	if err == nil || !strings.Contains(err.Error(), "canonical issuer") {
		t.Fatalf("cross-issuer verification URL was accepted: %v", err)
	}
}

func TestAuthorizationCodeExchangeRecoversLostTokenResponse(t *testing.T) {
	var output bytes.Buffer
	r := &runner{ctx: context.Background(), out: &output, errOut: &output}
	client := beeapi.New("https://beeapi.dev")
	calls := 0
	client.HTTP = &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Header.Get("DPoP") == "" {
			t.Fatal("token exchange is missing DPoP proof")
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if request.Form.Get("code") != "authorization-code" || request.Form.Get("code_verifier") != "pkce-verifier" {
			t.Fatalf("retry changed authorization request: %#v", request.Form)
		}
		if calls == 1 {
			return nil, errors.New("connection reset after token issuance")
		}
		body := `{"access_token":"boa_recovered","refresh_token":"bor_recovered","token_type":"DPoP","expires_in":300,"scope":"account:profile:read offline_access"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	metadata := beeapi.OAuthMetadata{Issuer: beeapi.OAuthIssuerDev, TokenEndpoint: "https://beeapi.dev/oauth/token"}
	token, err := r.exchangeOAuthAuthorizationCode(client, metadata, "authorization-code", "http://127.0.0.1:43127/oauth/callback", "pkce-verifier")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || token.AccessToken != "boa_recovered" || token.RefreshToken != "bor_recovered" {
		t.Fatalf("unexpected recovered token: calls=%d token=%#v", calls, token)
	}
	if !strings.Contains(output.String(), "同一授权码安全恢复") {
		t.Fatalf("missing token recovery notice: %s", output.String())
	}
}

func TestAuthorizationCodeExchangeDoesNotRetryAcrossIssuerBoundary(t *testing.T) {
	var output bytes.Buffer
	r := &runner{ctx: context.Background(), out: &output, errOut: &output}
	client := beeapi.New(beeapi.OAuthIssuerAI)
	requests := 0
	client.HTTP = &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected network request")
	})}
	metadata := beeapi.OAuthMetadata{Issuer: beeapi.OAuthIssuerDev, TokenEndpoint: beeapi.OAuthIssuerDev + "/oauth/token"}
	_, err := r.exchangeOAuthAuthorizationCode(client, metadata, "dev-code", "http://127.0.0.1:43127/oauth/callback", "verifier")
	if err == nil || !errors.Is(err, beeapi.ErrOAuthIssuerBoundary) {
		t.Fatalf("cross-issuer code was not rejected: %v", err)
	}
	if requests != 0 || strings.Contains(output.String(), "安全恢复") {
		t.Fatalf("cross-issuer code entered the retry path: requests=%d output=%s", requests, output.String())
	}
}
