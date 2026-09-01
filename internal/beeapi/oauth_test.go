package beeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func oauthTestMetadata() OAuthMetadata {
	return OAuthMetadata{
		Issuer:                      "https://beeapi.dev",
		AuthorizationEndpoint:       "https://beeapi.dev/oauth/authorize",
		TokenEndpoint:               "https://beeapi.dev/oauth/token",
		DeviceAuthorizationEndpoint: "https://beeapi.dev/oauth/device/code",
		RevocationEndpoint:          "https://beeapi.dev/oauth/revoke",
		ResponseTypesSupported:      []string{"code"},
		GrantTypesSupported:         []string{"authorization_code", "refresh_token", OAuthDeviceGrant},
		CodeChallengeMethods:        []string{"S256"},
		TokenAuthMethods:            []string{"none"},
		ScopesSupported:             append([]string(nil), OAuthAccountScopes...),
		DPoPSigningAlgorithms:       []string{"ES256"},
	}
}

func TestOAuthMetadataAllowsCanonicalBeeAPIDevFromOfficialAlias(t *testing.T) {
	client := New("https://beeapi.ai")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://beeapi.ai/.well-known/oauth-authorization-server" {
			t.Fatalf("unexpected metadata URL: %s", r.URL)
		}
		body := `{"issuer":"https://beeapi.dev","authorization_endpoint":"https://beeapi.dev/oauth/authorize","token_endpoint":"https://beeapi.dev/oauth/token","device_authorization_endpoint":"https://beeapi.dev/oauth/device/code","revocation_endpoint":"https://beeapi.dev/oauth/revoke","response_types_supported":["code"],"grant_types_supported":["authorization_code","refresh_token","urn:ietf:params:oauth:grant-type:device_code"],"code_challenge_methods_supported":["S256"],"token_endpoint_auth_methods_supported":["none"],"scopes_supported":["account:profile:read","account:balance:read","api_keys:read","api_keys:export","offline_access"],"dpop_signing_alg_values_supported":["ES256"]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
	})}
	metadata, err := client.OAuthMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Issuer != "https://beeapi.dev" || !metadata.SupportsDeviceGrant() {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

func TestOAuthMetadataRecognizesAnOfficialSPAFallbackAsUnavailable(t *testing.T) {
	client := New("https://beeapi.dev")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
			Body: io.NopCloser(strings.NewReader("<!doctype html><title>BeeAPI</title>")), Request: r,
		}, nil
	})}
	if _, err := client.OAuthMetadata(context.Background()); !errors.Is(err, ErrOAuthDiscoveryUnavailable) {
		t.Fatalf("SPA fallback was not recognized as unavailable discovery: %v", err)
	}
}

func TestOAuthMetadataDoesNotDowngradePartiallyInvalidMetadata(t *testing.T) {
	client := New("https://beeapi.dev")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"issuer":"https://attacker.example","authorization_endpoint":"https://attacker.example/oauth/authorize"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	_, err := client.OAuthMetadata(context.Background())
	if err == nil || errors.Is(err, ErrOAuthDiscoveryUnavailable) {
		t.Fatalf("untrusted partial metadata was downgraded to legacy compatibility: %v", err)
	}
}

func TestOAuthMetadataRejectsUntrustedAuthorizationHost(t *testing.T) {
	metadata := oauthTestMetadata()
	metadata.AuthorizationEndpoint = "https://beeapi.dev.attacker.test/oauth/authorize"
	if err := validateOAuthMetadata("https://beeapi.ai", metadata); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("unexpected validation result: %v", err)
	}
}

func TestOAuthMetadataRejectsIssuerAliasesAndNonDefaultPorts(t *testing.T) {
	metadata := oauthTestMetadata()
	metadata.Issuer = "https://beeapi.ai"
	if err := validateOAuthMetadata("https://beeapi.ai", metadata); err == nil || !strings.Contains(err.Error(), "issuer must") {
		t.Fatalf("issuer alias was accepted: %v", err)
	}
	metadata = oauthTestMetadata()
	metadata.TokenEndpoint = "https://beeapi.dev:8443/oauth/token"
	if err := validateOAuthMetadata("https://beeapi.dev", metadata); err == nil || !strings.Contains(err.Error(), "valid HTTPS") {
		t.Fatalf("non-default OAuth port was accepted: %v", err)
	}
}

func TestOAuthTokenRejectsCredentialsFromAnotherDomain(t *testing.T) {
	for _, token := range []OAuthToken{
		{AccessToken: "sk-model-key", TokenType: "DPoP", ExpiresIn: 600},
		{AccessToken: "boa_valid", RefreshToken: "refresh-without-prefix", TokenType: "DPoP", ExpiresIn: 600},
	} {
		if _, err := validateOAuthToken(token); err == nil {
			t.Fatalf("invalid OAuth credential domain was accepted: %#v", token)
		}
	}
}

func TestBuildAuthorizationURLUsesPKCEAndExactLoopback(t *testing.T) {
	raw, err := BuildAuthorizationURL(oauthTestMetadata(), "http://127.0.0.1:49152/oauth/callback", "state-secret", "challenge", OAuthAccountScopes)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"response_type": "code", "client_id": OAuthClientID,
		"redirect_uri": "http://127.0.0.1:49152/oauth/callback",
		"state":        "state-secret", "code_challenge": "challenge", "code_challenge_method": "S256",
	} {
		if query.Get(key) != want {
			t.Fatalf("authorization %s = %q, want %q", key, query.Get(key), want)
		}
	}
	if !reflect.DeepEqual(SortedOAuthScopes(query.Get("scope")), SortedOAuthScopes(OAuthScopeString(OAuthAccountScopes))) {
		t.Fatalf("unexpected scopes: %q", query.Get("scope"))
	}
	if query.Get("platform") == "" {
		t.Fatal("authorization request is missing platform metadata")
	}
	if _, err := BuildAuthorizationURL(oauthTestMetadata(), "https://getbeeapi.com/oauth/callback", "state", "challenge", OAuthAccountScopes); err == nil {
		t.Fatal("non-loopback redirect was accepted for the desktop CLI flow")
	}
}

func TestOAuthDeviceAndTokenRequestsUseStandardFormsAndSameDPoPKey(t *testing.T) {
	client := New("https://beeapi.dev")
	requests := 0
	var proofHeaders []string
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		proofHeaders = append(proofHeaders, r.Header.Get("DPoP"))
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected OAuth request headers: %#v", r.Header)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch requests {
		case 1:
			if r.URL.Path != "/oauth/device/code" || r.Form.Get("client_id") != OAuthClientID || !strings.Contains(r.Form.Get("scope"), "api_keys:read") {
				t.Fatalf("unexpected device form: %s %#v", r.URL.Path, r.Form)
			}
			body := `{"device_code":"device-secret","user_code":"BEE7-K9Q2","verification_uri":"https://beeapi.dev/cli/authorize","verification_uri_complete":"https://beeapi.dev/cli/authorize?user_code=BEE7-K9Q2","expires_in":600,"interval":5}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
		case 2:
			if r.URL.Path != "/oauth/token" || r.Form.Get("grant_type") != OAuthDeviceGrant || r.Form.Get("device_code") != "device-secret" {
				t.Fatalf("unexpected token form: %s %#v", r.URL.Path, r.Form)
			}
			body := `{"access_token":"boa_secret","refresh_token":"bor_secret","token_type":"DPoP","expires_in":600,"scope":"account:profile:read api_keys:read"}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
		default:
			t.Fatalf("unexpected request %d", requests)
			return nil, nil
		}
	})}
	code, err := client.StartOAuthDeviceAuth(context.Background(), oauthTestMetadata(), OAuthAccountScopes)
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.PollOAuthDeviceToken(context.Background(), oauthTestMetadata(), code.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "boa_secret" || token.RefreshToken != "bor_secret" || requests != 2 {
		t.Fatalf("unexpected OAuth token: %#v", token)
	}
	if len(proofHeaders) != 2 || proofHeaders[0] == "" || proofHeaders[1] == "" {
		t.Fatalf("missing DPoP proofs: %#v", proofHeaders)
	}
	var first, second struct {
		JWK map[string]string `json:"jwk"`
	}
	decodeJWTJSON(t, strings.Split(proofHeaders[0], ".")[0], &first)
	decodeJWTJSON(t, strings.Split(proofHeaders[1], ".")[0], &second)
	if first.JWK["x"] != second.JWK["x"] || first.JWK["y"] != second.JWK["y"] {
		t.Fatal("device and token requests did not use the same DPoP key")
	}
}

func TestOAuthProtocolAndAccountResourceErrorsKeepSeparateShapes(t *testing.T) {
	client := New("https://beeapi.dev")
	requests := 0
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			if r.URL.Path != "/oauth/device/code" {
				t.Fatalf("unexpected OAuth protocol path: %s", r.URL.Path)
			}
			body := `{"error":"authorization_pending","error_description":"Waiting for approval"}`
			return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
		case 2:
			if r.URL.Path != "/api/v1/oauth/api-keys" {
				t.Fatalf("unexpected OAuth resource path: %s", r.URL.Path)
			}
			body := `{"code":401,"message":"invalid proof","reason":"oauth.invalid_dpop_proof","data":null}`
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
		default:
			t.Fatalf("unexpected request %d", requests)
			return nil, nil
		}
	})}

	_, err := client.StartOAuthDeviceAuth(context.Background(), oauthTestMetadata(), OAuthAccountScopes)
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Reason != "authorization_pending" || apiErr.Code != 0 {
		t.Fatalf("standard OAuth error shape was not preserved: %#v", err)
	}

	client.Token = "boa_account_secret"
	_, err = client.OAuthAPIKeys(context.Background())
	apiErr, ok = err.(*APIError)
	if !ok || apiErr.Reason != "oauth.invalid_dpop_proof" || apiErr.Code != 401 {
		t.Fatalf("BeeAPI account resource envelope was not preserved: %#v", err)
	}
}

func TestOAuthAccountResourcesUseDPoPAndSelectiveIdempotentExport(t *testing.T) {
	client := New("https://beeapi.dev")
	client.Token = "boa_account_secret"
	requests := 0
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Header.Get("Authorization") != "DPoP boa_account_secret" || r.Header.Get("DPoP") == "" {
			t.Fatalf("missing OAuth account sender constraint: %#v", r.Header)
		}
		switch requests {
		case 1:
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/oauth/api-keys" {
				t.Fatalf("unexpected API Key list request: %s %s", r.Method, r.URL.Path)
			}
			body := `{"code":0,"message":"ok","data":{"items":[{"id":42,"name":"Coding","key_prefix":"sk-live","status":"enabled","exportable":true}]}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
		case 2:
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/oauth/api-key-exports" || r.Header.Get("Idempotency-Key") != "idem-secret-123456" {
				t.Fatalf("unexpected export request: %s %s %#v", r.Method, r.URL.Path, r.Header)
			}
			var body struct {
				IDs []int `json:"api_key_ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(body.IDs, []int{42}) {
				t.Fatalf("unexpected selected IDs: %#v", body.IDs)
			}
			response := `{"code":0,"message":"ok","data":{"export_id":"bke_one","credentials":[{"api_key_id":"42","key_name":"Coding","key_prefix":"sk-live","api_key":"sk-secret"}],"skipped":[],"retry_until":"2026-09-01T12:10:00Z"}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(response)), Request: r}, nil
		default:
			t.Fatalf("unexpected resource request %d", requests)
			return nil, nil
		}
	})}
	keys, err := client.OAuthAPIKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || string(keys[0].ID) != "42" || !keys[0].Exportable {
		t.Fatalf("unexpected API Keys: %#v", keys)
	}
	exported, err := client.CreateOAuthAPIKeyExport(context.Background(), []string{"42", "42"}, "idem-secret-123456")
	if err != nil {
		t.Fatal(err)
	}
	if exported.ExportID != "bke_one" || len(exported.Credentials) != 1 || exported.Credentials[0].APIKey != "sk-secret" {
		t.Fatalf("unexpected export: %#v", exported)
	}
}
