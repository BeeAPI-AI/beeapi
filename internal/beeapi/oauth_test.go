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

func oauthTestMetadataFor(issuer string) OAuthMetadata {
	return OAuthMetadata{
		Issuer:                      issuer,
		AuthorizationEndpoint:       issuer + "/oauth/authorize",
		TokenEndpoint:               issuer + "/oauth/token",
		DeviceAuthorizationEndpoint: issuer + "/oauth/device/code",
		RevocationEndpoint:          issuer + "/oauth/revoke",
		ResponseTypesSupported:      []string{"code"},
		GrantTypesSupported:         []string{"authorization_code", "refresh_token", OAuthDeviceGrant},
		CodeChallengeMethods:        []string{"S256"},
		TokenAuthMethods:            []string{"none"},
		ScopesSupported:             append([]string(nil), OAuthAccountScopes...),
		DPoPSigningAlgorithms:       []string{"ES256"},
		AuthorizationResponseIssuer: true,
	}
}

func oauthTestMetadata() OAuthMetadata {
	return oauthTestMetadataFor(OAuthIssuerDev)
}

func TestOAuthMetadataFollowsOnlyTheMatchingDiscoveryAlias(t *testing.T) {
	client := New("https://api.beeapi.ai")
	requests := 0
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Header.Get("Authorization") != "" || r.Header.Get("DPoP") != "" {
			t.Fatalf("discovery leaked credentials: %#v", r.Header)
		}
		switch r.URL.Host {
		case "api.beeapi.ai":
			return &http.Response{StatusCode: http.StatusPermanentRedirect, Header: http.Header{"Location": {OAuthIssuerAI + "/.well-known/oauth-authorization-server"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		case "beeapi.ai":
			body := `{"issuer":"https://beeapi.ai","authorization_endpoint":"https://beeapi.ai/oauth/authorize","token_endpoint":"https://beeapi.ai/oauth/token","device_authorization_endpoint":"https://beeapi.ai/oauth/device/code","revocation_endpoint":"https://beeapi.ai/oauth/revoke","response_types_supported":["code"],"grant_types_supported":["authorization_code","refresh_token","urn:ietf:params:oauth:grant-type:device_code"],"code_challenge_methods_supported":["S256"],"token_endpoint_auth_methods_supported":["none"],"dpop_signing_alg_values_supported":["ES256"],"authorization_response_iss_parameter_supported":true}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
		default:
			t.Fatalf("unexpected metadata host: %s", r.URL.Host)
			return nil, nil
		}
	})}
	metadata, err := client.OAuthMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Issuer != OAuthIssuerAI || !metadata.SupportsDeviceGrant() || requests != 2 {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

func TestOAuthMetadataRejectsTheOtherOfficialIssuer(t *testing.T) {
	client := New(OAuthIssuerAI)
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"issuer":"https://beeapi.dev","authorization_endpoint":"https://beeapi.dev/oauth/authorize","token_endpoint":"https://beeapi.dev/oauth/token","device_authorization_endpoint":"https://beeapi.dev/oauth/device/code","revocation_endpoint":"https://beeapi.dev/oauth/revoke","response_types_supported":["code"],"grant_types_supported":["authorization_code","refresh_token","urn:ietf:params:oauth:grant-type:device_code"],"code_challenge_methods_supported":["S256"],"token_endpoint_auth_methods_supported":["none"],"dpop_signing_alg_values_supported":["ES256"],"authorization_response_iss_parameter_supported":true}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	if _, err := client.OAuthMetadata(context.Background()); err == nil || !strings.Contains(err.Error(), "selected entrance") {
		t.Fatalf("cross-issuer metadata was accepted: %v", err)
	}
}

func TestOAuthMetadataRecognizesAnOfficialSPAFallbackAsUnavailable(t *testing.T) {
	client := New("https://beeapi.dev")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{
				"Content-Type": {"text/html; charset=utf-8"},
				"X-Request-Id": {"req-spa-123"},
				"Cf-Ray":       {"ray-spa-456-SIN"},
			},
			Body: io.NopCloser(strings.NewReader("<!doctype html><title>BeeAPI</title>")), Request: r,
		}, nil
	})}
	_, err := client.OAuthMetadata(context.Background())
	if !errors.Is(err, ErrOAuthDiscoveryUnavailable) {
		t.Fatalf("SPA fallback was not recognized as unavailable discovery: %v", err)
	}
	for _, want := range []string{
		"BeeAPI 返回 200 (oauth.invalid_response)",
		"GET https://beeapi.dev/.well-known/oauth-authorization-server",
		"request_id=req-spa-123",
		"cf_ray=ray-spa-456-SIN",
		"Content-Type text/html; charset=utf-8",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("discovery error omitted %q: %v", want, err)
		}
	}
}

func TestOAuthMetadataPreservesHTTPDiscoveryFailureDiagnostics(t *testing.T) {
	client := New(OAuthIssuerDev)
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Status:     "503 Service Unavailable",
			Header: http.Header{
				"Content-Type": {"application/json"},
				"X-Request-Id": {"req-discovery-123"},
				"Cf-Ray":       {"ray-discovery-456-SIN"},
			},
			Body:    io.NopCloser(strings.NewReader(`{"error":"temporarily_unavailable","error_description":"edge origin unavailable"}`)),
			Request: r,
		}, nil
	})}

	_, err := client.OAuthMetadata(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable || apiErr.Reason != "temporarily_unavailable" {
		t.Fatalf("structured discovery error was not preserved: %#v", err)
	}
	for _, want := range []string{
		"BeeAPI 返回 503 (temporarily_unavailable): edge origin unavailable",
		"GET https://beeapi.dev/.well-known/oauth-authorization-server",
		"request_id=req-discovery-123",
		"cf_ray=ray-discovery-456-SIN",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("discovery error omitted %q: %v", want, err)
		}
	}
}

func TestOAuthMetadataPreservesTransportFailureTarget(t *testing.T) {
	client := New(OAuthIssuerDev)
	client.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial blocked by test network")
	})}

	_, err := client.OAuthMetadata(context.Background())
	if err == nil || !strings.Contains(err.Error(), "OAuth discovery GET https://beeapi.dev/.well-known/oauth-authorization-server") || !strings.Contains(err.Error(), "dial blocked by test network") {
		t.Fatalf("transport failure omitted the discovery target or cause: %v", err)
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
	if err := validateOAuthMetadata(OAuthIssuerDev, metadata); err == nil || !strings.Contains(err.Error(), "selected issuer") {
		t.Fatalf("unexpected validation result: %v", err)
	}
}

func TestOAuthMetadataRejectsCrossIssuerEndpointsAndNonDefaultPorts(t *testing.T) {
	metadata := oauthTestMetadataFor(OAuthIssuerAI)
	metadata.TokenEndpoint = OAuthIssuerDev + "/oauth/token"
	if err := validateOAuthMetadata(OAuthIssuerAI, metadata); err == nil || !strings.Contains(err.Error(), "selected issuer") {
		t.Fatalf("cross-issuer endpoint was accepted: %v", err)
	}
	metadata = oauthTestMetadata()
	metadata.TokenEndpoint = "https://beeapi.dev:8443/oauth/token"
	if err := validateOAuthMetadata(OAuthIssuerDev, metadata); err == nil || !strings.Contains(err.Error(), "valid HTTPS") {
		t.Fatalf("non-default OAuth port was accepted: %v", err)
	}
}

func TestOAuthMetadataRequiresCallbackIssuerSupportButAllowsOmittedScopeAdvertisement(t *testing.T) {
	metadata := oauthTestMetadata()
	metadata.AuthorizationResponseIssuer = false
	if err := validateOAuthMetadata(OAuthIssuerDev, metadata); err == nil || !strings.Contains(err.Error(), "response issuer") {
		t.Fatalf("metadata without callback issuer support was accepted: %v", err)
	}
	metadata.AuthorizationResponseIssuer = true
	metadata.ScopesSupported = nil
	if err := validateOAuthMetadata(OAuthIssuerDev, metadata); err != nil {
		t.Fatalf("optional scopes_supported was required: %v", err)
	}
}

func TestOAuthIssuerForEntranceMapsOnlyMatchingOfficialDomains(t *testing.T) {
	for entrance, want := range map[string]string{
		OAuthIssuerAI: OAuthIssuerAI, "https://api.beeapi.ai": OAuthIssuerAI,
		OAuthIssuerDev: OAuthIssuerDev, "https://api.beeapi.dev": OAuthIssuerDev,
	} {
		if got, err := OAuthIssuerForEntrance(entrance); err != nil || got != want {
			t.Fatalf("OAuthIssuerForEntrance(%q) = %q, %v; want %q", entrance, got, err, want)
		}
	}
	for _, entrance := range []string{"https://beeapi.ai.attacker.test", "https://api.beeapi.dev:8443", "http://beeapi.ai"} {
		if _, err := OAuthIssuerForEntrance(entrance); err == nil {
			t.Fatalf("untrusted entrance was accepted: %s", entrance)
		}
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

func TestOAuthCredentialRequestsNeverCrossIssuerOrFollowRedirects(t *testing.T) {
	client := New(OAuthIssuerAI)
	requests := 0
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"boa_wrong","token_type":"DPoP","expires_in":600}`)), Request: r}, nil
	})}
	if _, err := client.ExchangeAuthorizationCode(context.Background(), oauthTestMetadata(), "code", "http://127.0.0.1:43127/oauth/callback", "verifier"); err == nil || !errors.Is(err, ErrOAuthIssuerBoundary) {
		t.Fatalf("authorization code was allowed to cross issuers: %v", err)
	}
	if requests != 0 {
		t.Fatalf("cross-issuer token request reached the network: %d", requests)
	}

	metadata := oauthTestMetadataFor(OAuthIssuerAI)
	client = New(OAuthIssuerAI)
	requests = 0
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "beeapi.ai" {
			t.Fatalf("sensitive redirect was followed to %s", r.URL.Host)
		}
		var claims struct {
			HTU string `json:"htu"`
		}
		decodeJWTJSON(t, strings.Split(r.Header.Get("DPoP"), ".")[1], &claims)
		if claims.HTU != OAuthIssuerAI+"/oauth/device/code" {
			t.Fatalf("DPoP htu = %q", claims.HTU)
		}
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header:     http.Header{"Location": {OAuthIssuerDev + "/oauth/device/code"}},
			Body:       io.NopCloser(strings.NewReader("")), Request: r,
		}, nil
	})}
	if _, err := client.StartOAuthDeviceAuth(context.Background(), metadata, OAuthAccountScopes); err == nil {
		t.Fatal("sensitive OAuth POST redirect was accepted")
	}
	if requests != 1 {
		t.Fatalf("sensitive OAuth POST followed a redirect: %d requests", requests)
	}

	metadata.DeviceAuthorizationEndpoint = "https://api.beeapi.ai/oauth/device/code"
	aliasClient := New("https://api.beeapi.ai")
	aliasRequests := 0
	aliasClient.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		aliasRequests++
		return nil, errors.New("unexpected network request")
	})}
	if _, err := aliasClient.StartOAuthDeviceAuth(context.Background(), metadata, OAuthAccountScopes); err == nil || !errors.Is(err, ErrOAuthIssuerBoundary) {
		t.Fatalf("credential-bearing POST to discovery alias was accepted: %v", err)
	}
	if aliasRequests != 0 {
		t.Fatalf("credential-bearing alias request reached the network: %d", aliasRequests)
	}
}

func TestOAuthDeviceRefreshAndRevokeCredentialsNeverCrossIssuers(t *testing.T) {
	client := New(OAuthIssuerAI)
	requests := 0
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected network request")
	})}
	metadata := oauthTestMetadataFor(OAuthIssuerDev)
	checks := []struct {
		name string
		call func() error
	}{
		{name: "device code", call: func() error {
			_, err := client.PollOAuthDeviceToken(context.Background(), metadata, "dev-device-code")
			return err
		}},
		{name: "refresh token", call: func() error {
			_, err := client.RefreshOAuthToken(context.Background(), metadata, "bor_dev")
			return err
		}},
		{name: "revocation token", call: func() error {
			return client.RevokeOAuthToken(context.Background(), metadata, "bor_dev", "refresh_token")
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil || !errors.Is(err, ErrOAuthIssuerBoundary) {
				t.Fatalf("cross-issuer %s was accepted: %v", check.name, err)
			}
		})
	}
	if requests != 0 {
		t.Fatalf("cross-issuer credentials reached the network: %d", requests)
	}
}

func TestOAuthAccountResourcesRejectDiscoveryAliasesBeforeSendingTokens(t *testing.T) {
	client := New("https://api.beeapi.dev")
	client.Token = "boa_dev"
	requests := 0
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected network request")
	})}
	if _, err := client.OAuthAccount(context.Background()); err == nil || !strings.Contains(err.Error(), "discovery aliases") {
		t.Fatalf("account token was accepted on a discovery alias: %v", err)
	}
	if requests != 0 {
		t.Fatalf("account token reached a discovery alias: %d", requests)
	}
}

func TestOAuthAccountResourceDPoPUsesTheCurrentIssuer(t *testing.T) {
	client := New(OAuthIssuerAI)
	client.Token = "boa_ai"
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != OAuthIssuerAI+"/api/v1/oauth/account" {
			t.Fatalf("account request crossed issuer: %s", r.URL)
		}
		parts := strings.Split(r.Header.Get("DPoP"), ".")
		if len(parts) != 3 {
			t.Fatalf("missing DPoP proof: %q", r.Header.Get("DPoP"))
		}
		var claims struct {
			HTU string `json:"htu"`
		}
		decodeJWTJSON(t, parts[1], &claims)
		if claims.HTU != OAuthIssuerAI+"/api/v1/oauth/account" {
			t.Fatalf("account DPoP htu = %q", claims.HTU)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"id":1,"username":"ai-user"}}`)), Request: r}, nil
	})}
	profile, err := client.OAuthAccount(context.Background())
	if err != nil || profile.Username != "ai-user" {
		t.Fatalf("unexpected AI account response: %#v err=%v", profile, err)
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
