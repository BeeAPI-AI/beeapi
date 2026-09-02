package beeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	OAuthAccountProtocol = "beeapi-oauth-account-v1"
	OAuthClientID        = "getbeeapi-cli-v2"
	OAuthIssuerAI        = "https://beeapi.ai"
	OAuthIssuerDev       = "https://beeapi.dev"
	OAuthDeviceGrant     = "urn:ietf:params:oauth:grant-type:device_code"
)

// ErrOAuthDiscoveryUnavailable marks an official endpoint that serves its SPA
// fallback (or an empty metadata document) because OAuth discovery has not
// been deployed there yet. Callers may offer manual API Key entry, but must
// never downgrade to the retired getbeeapi-cli / cli:configure protocol.
// Untrusted or partially invalid metadata still fails closed.
var (
	ErrOAuthDiscoveryUnavailable = errors.New("BeeAPI OAuth discovery is unavailable")
	ErrOAuthIssuerBoundary       = errors.New("BeeAPI OAuth issuer boundary violation")
)

var OAuthAccountScopes = []string{
	"account:profile:read",
	"account:balance:read",
	"api_keys:read",
	"api_keys:export",
	"offline_access",
}

type OAuthMetadata struct {
	Issuer                      string   `json:"issuer"`
	AuthorizationEndpoint       string   `json:"authorization_endpoint"`
	TokenEndpoint               string   `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string   `json:"device_authorization_endpoint"`
	RevocationEndpoint          string   `json:"revocation_endpoint"`
	ResponseTypesSupported      []string `json:"response_types_supported"`
	GrantTypesSupported         []string `json:"grant_types_supported"`
	CodeChallengeMethods        []string `json:"code_challenge_methods_supported"`
	TokenAuthMethods            []string `json:"token_endpoint_auth_methods_supported"`
	ScopesSupported             []string `json:"scopes_supported"`
	DPoPSigningAlgorithms       []string `json:"dpop_signing_alg_values_supported"`
	AuthorizationResponseIssuer bool     `json:"authorization_response_iss_parameter_supported"`
}

type OAuthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

func (c *Client) OAuthMetadata(ctx context.Context) (OAuthMetadata, error) {
	var metadata OAuthMetadata
	expectedIssuer, err := OAuthIssuerForEntrance(c.BaseURL)
	if err != nil {
		return metadata, err
	}
	target := strings.TrimRight(c.BaseURL, "/") + "/.well-known/oauth-authorization-server"
	resp, err := c.oauthDiscoveryRequest(ctx, target, expectedIssuer, &metadata)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Reason == "oauth.invalid_response" {
			return OAuthMetadata{}, fmt.Errorf("%w: %w", ErrOAuthDiscoveryUnavailable, err)
		}
		return metadata, err
	}
	if strings.TrimSpace(metadata.Issuer) == "" &&
		strings.TrimSpace(metadata.AuthorizationEndpoint) == "" &&
		strings.TrimSpace(metadata.TokenEndpoint) == "" &&
		strings.TrimSpace(metadata.DeviceAuthorizationEndpoint) == "" &&
		strings.TrimSpace(metadata.RevocationEndpoint) == "" {
		return OAuthMetadata{}, fmt.Errorf(
			"%w: %w",
			ErrOAuthDiscoveryUnavailable,
			newAPIError(resp, 0, "OAuth metadata document is empty", "oauth.invalid_response"),
		)
	}
	if err := validateOAuthMetadata(c.BaseURL, metadata); err != nil {
		return OAuthMetadata{}, err
	}
	return metadata, nil
}

func validateOAuthMetadata(baseURL string, metadata OAuthMetadata) error {
	expectedIssuer, err := OAuthIssuerForEntrance(baseURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(metadata.Issuer) != expectedIssuer {
		return fmt.Errorf("%w: issuer must match the selected entrance %s", ErrOAuthIssuerBoundary, expectedIssuer)
	}
	for _, endpoint := range []struct {
		label string
		raw   string
		path  string
	}{
		{label: "authorization_endpoint", raw: metadata.AuthorizationEndpoint, path: "/oauth/authorize"},
		{label: "token_endpoint", raw: metadata.TokenEndpoint, path: "/oauth/token"},
		{label: "device_authorization_endpoint", raw: metadata.DeviceAuthorizationEndpoint, path: "/oauth/device/code"},
		{label: "revocation_endpoint", raw: metadata.RevocationEndpoint, path: "/oauth/revoke"},
	} {
		if err := validateOAuthEndpointURL(expectedIssuer, endpoint.raw, endpoint.label, endpoint.path); err != nil {
			return err
		}
	}
	if !containsOAuthValue(metadata.ResponseTypesSupported, "code") || !containsOAuthValue(metadata.GrantTypesSupported, "authorization_code") {
		return errors.New("BeeAPI OAuth metadata does not support authorization code")
	}
	if !containsOAuthValue(metadata.CodeChallengeMethods, "S256") {
		return errors.New("BeeAPI OAuth metadata does not require PKCE S256")
	}
	if !containsOAuthValue(metadata.TokenAuthMethods, "none") {
		return errors.New("BeeAPI OAuth metadata does not support a public CLI client")
	}
	if !containsOAuthValue(metadata.GrantTypesSupported, "refresh_token") {
		return errors.New("BeeAPI OAuth metadata does not support refresh token rotation")
	}
	if !containsOAuthValue(metadata.GrantTypesSupported, OAuthDeviceGrant) {
		return errors.New("BeeAPI OAuth metadata does not support the device grant required for headless clients")
	}
	if !containsOAuthValue(metadata.DPoPSigningAlgorithms, "ES256") {
		return errors.New("BeeAPI OAuth metadata does not support DPoP ES256")
	}
	if !metadata.AuthorizationResponseIssuer {
		return errors.New("BeeAPI OAuth metadata does not require the authorization response issuer parameter")
	}
	if len(metadata.ScopesSupported) > 0 {
		for _, scope := range OAuthAccountScopes {
			if !containsOAuthValue(metadata.ScopesSupported, scope) {
				return fmt.Errorf("BeeAPI OAuth metadata does not support required scope %s", scope)
			}
		}
	}
	return nil
}

// OAuthIssuerForEntrance maps an official web entrance or discovery-only
// api.* alias to its independent OAuth security domain.
func OAuthIssuerForEntrance(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("%w: entrance is not a valid HTTPS root URL", ErrOAuthIssuerBoundary)
	}
	switch strings.ToLower(strings.TrimSuffix(parsed.Hostname(), ".")) {
	case "beeapi.ai", "api.beeapi.ai":
		return OAuthIssuerAI, nil
	case "beeapi.dev", "api.beeapi.dev":
		return OAuthIssuerDev, nil
	default:
		return "", fmt.Errorf("%w: entrance is not an official trusted domain", ErrOAuthIssuerBoundary)
	}
}

func IsTrustedOAuthIssuer(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw == OAuthIssuerAI || raw == OAuthIssuerDev
}

func validateOAuthEndpointURL(issuer, raw, label, expectedPath string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("BeeAPI OAuth %s is not a valid HTTPS URL", label)
	}
	if parsed.Scheme+"://"+parsed.Host != issuer {
		return fmt.Errorf("%w: %s must use selected issuer %s", ErrOAuthIssuerBoundary, label, issuer)
	}
	if parsed.Path != expectedPath {
		return fmt.Errorf("%w: %s must use path %s", ErrOAuthIssuerBoundary, label, expectedPath)
	}
	return nil
}

func validateSensitiveOAuthTarget(baseURL, target string) error {
	issuer, err := OAuthIssuerForEntrance(baseURL)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: credential target is not a valid HTTPS URL", ErrOAuthIssuerBoundary)
	}
	if parsed.Scheme+"://"+parsed.Host != issuer {
		return fmt.Errorf("%w: credentials cannot cross issuers; want %s", ErrOAuthIssuerBoundary, issuer)
	}
	return nil
}

func validateOAuthOperationEndpoint(baseURL string, metadata OAuthMetadata, raw, label, expectedPath string) error {
	issuer, err := OAuthIssuerForEntrance(baseURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(metadata.Issuer) != issuer {
		return fmt.Errorf("%w: operation metadata issuer must be %s", ErrOAuthIssuerBoundary, issuer)
	}
	return validateOAuthEndpointURL(issuer, raw, label, expectedPath)
}

func (m OAuthMetadata) SupportsDeviceGrant() bool {
	return strings.TrimSpace(m.DeviceAuthorizationEndpoint) != "" && containsOAuthValue(m.GrantTypesSupported, OAuthDeviceGrant)
}

func containsOAuthValue(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

func OAuthScopeString(scopes []string) string {
	seen := map[string]bool{}
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope != "" && !seen[scope] {
			seen[scope] = true
			result = append(result, scope)
		}
	}
	return strings.Join(result, " ")
}

func BuildAuthorizationURL(metadata OAuthMetadata, redirectURI, state, codeChallenge string, scopes []string) (string, error) {
	if strings.TrimSpace(state) == "" || strings.TrimSpace(codeChallenge) == "" {
		return "", errors.New("OAuth state and PKCE challenge are required")
	}
	if !IsTrustedOAuthIssuer(strings.TrimSpace(metadata.Issuer)) {
		return "", fmt.Errorf("%w: authorization issuer is not trusted", ErrOAuthIssuerBoundary)
	}
	if err := validateOAuthEndpointURL(metadata.Issuer, metadata.AuthorizationEndpoint, "authorization_endpoint", "/oauth/authorize"); err != nil {
		return "", err
	}
	redirect, err := url.Parse(redirectURI)
	if err != nil || redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" || redirect.Port() == "" || redirect.Path != "/oauth/callback" {
		return "", errors.New("OAuth desktop redirect must use http://127.0.0.1:<port>/oauth/callback")
	}
	authorize, err := url.Parse(metadata.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	query := authorize.Query()
	query.Set("response_type", "code")
	query.Set("client_id", OAuthClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", OAuthScopeString(scopes))
	query.Set("state", state)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	deviceName, _ := os.Hostname()
	if strings.TrimSpace(deviceName) != "" {
		query.Set("device_name", strings.TrimSpace(deviceName))
	}
	query.Set("platform", runtime.GOOS+"/"+runtime.GOARCH)
	authorize.RawQuery = query.Encode()
	return authorize.String(), nil
}

func (c *Client) StartOAuthDeviceAuth(ctx context.Context, metadata OAuthMetadata, scopes []string) (DeviceCode, error) {
	var code DeviceCode
	if err := validateOAuthOperationEndpoint(c.BaseURL, metadata, metadata.DeviceAuthorizationEndpoint, "device_authorization_endpoint", "/oauth/device/code"); err != nil {
		return code, err
	}
	deviceName, _ := os.Hostname()
	values := url.Values{
		"client_id":   {OAuthClientID},
		"scope":       {OAuthScopeString(scopes)},
		"device_name": {strings.TrimSpace(deviceName)},
		"platform":    {runtime.GOOS + "/" + runtime.GOARCH},
	}
	if err := c.oauthFormRequest(ctx, metadata.DeviceAuthorizationEndpoint, values, "", &code); err != nil {
		return code, err
	}
	return code, nil
}

func (c *Client) PollOAuthDeviceToken(ctx context.Context, metadata OAuthMetadata, deviceCode string) (OAuthToken, error) {
	if err := validateOAuthOperationEndpoint(c.BaseURL, metadata, metadata.TokenEndpoint, "token_endpoint", "/oauth/token"); err != nil {
		return OAuthToken{}, err
	}
	values := url.Values{
		"grant_type":  {OAuthDeviceGrant},
		"client_id":   {OAuthClientID},
		"device_code": {strings.TrimSpace(deviceCode)},
	}
	var token OAuthToken
	if err := c.oauthFormRequest(ctx, metadata.TokenEndpoint, values, "", &token); err != nil {
		return token, err
	}
	return validateOAuthToken(token)
}

func (c *Client) ExchangeAuthorizationCode(ctx context.Context, metadata OAuthMetadata, code, redirectURI, verifier string) (OAuthToken, error) {
	if err := validateOAuthOperationEndpoint(c.BaseURL, metadata, metadata.TokenEndpoint, "token_endpoint", "/oauth/token"); err != nil {
		return OAuthToken{}, err
	}
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {OAuthClientID},
		"code":          {strings.TrimSpace(code)},
		"redirect_uri":  {strings.TrimSpace(redirectURI)},
		"code_verifier": {strings.TrimSpace(verifier)},
	}
	var token OAuthToken
	if err := c.oauthFormRequest(ctx, metadata.TokenEndpoint, values, "", &token); err != nil {
		return token, err
	}
	return validateOAuthToken(token)
}

func (c *Client) RefreshOAuthToken(ctx context.Context, metadata OAuthMetadata, refreshToken string) (OAuthToken, error) {
	if err := validateOAuthOperationEndpoint(c.BaseURL, metadata, metadata.TokenEndpoint, "token_endpoint", "/oauth/token"); err != nil {
		return OAuthToken{}, err
	}
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {OAuthClientID},
		"refresh_token": {strings.TrimSpace(refreshToken)},
	}
	var token OAuthToken
	if err := c.oauthFormRequest(ctx, metadata.TokenEndpoint, values, "", &token); err != nil {
		return token, err
	}
	return validateOAuthToken(token)
}

func (c *Client) RevokeOAuthToken(ctx context.Context, metadata OAuthMetadata, token, hint string) error {
	if err := validateOAuthOperationEndpoint(c.BaseURL, metadata, metadata.RevocationEndpoint, "revocation_endpoint", "/oauth/revoke"); err != nil {
		return err
	}
	values := url.Values{"client_id": {OAuthClientID}, "token": {strings.TrimSpace(token)}}
	if strings.TrimSpace(hint) != "" {
		values.Set("token_type_hint", strings.TrimSpace(hint))
	}
	return c.oauthFormRequest(ctx, metadata.RevocationEndpoint, values, "", nil)
}

func validateOAuthToken(token OAuthToken) (OAuthToken, error) {
	token.AccessToken = strings.TrimSpace(token.AccessToken)
	token.RefreshToken = strings.TrimSpace(token.RefreshToken)
	token.TokenType = strings.TrimSpace(token.TokenType)
	token.Scope = OAuthScopeString(strings.Fields(token.Scope))
	if token.AccessToken == "" {
		return OAuthToken{}, errors.New("BeeAPI OAuth token response is missing access_token")
	}
	if !strings.HasPrefix(token.AccessToken, "boa_") {
		return OAuthToken{}, errors.New("BeeAPI OAuth returned an invalid account access token")
	}
	if token.RefreshToken != "" && !strings.HasPrefix(token.RefreshToken, "bor_") {
		return OAuthToken{}, errors.New("BeeAPI OAuth returned an invalid refresh token")
	}
	if !strings.EqualFold(token.TokenType, "DPoP") {
		return OAuthToken{}, fmt.Errorf("BeeAPI OAuth returned unsupported token_type %q", token.TokenType)
	}
	if token.ExpiresIn <= 0 {
		return OAuthToken{}, errors.New("BeeAPI OAuth token response has an invalid expires_in")
	}
	return token, nil
}

func (c *Client) oauthFormRequest(ctx context.Context, target string, values url.Values, accessToken string, out any) error {
	if err := validateSensitiveOAuthTarget(c.BaseURL, target); err != nil {
		return err
	}
	body := strings.NewReader(values.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "beeapi-cli/2")
	signer, err := c.ensureDPoP()
	if err != nil {
		return err
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "DPoP "+accessToken)
	}
	proof, err := signer.proof(http.MethodPost, target, accessToken, time.Now())
	if err != nil {
		return err
	}
	req.Header.Set("DPoP", proof)
	return c.doOAuthRequestNoRedirect(req, out)
}

func (c *Client) oauthDiscoveryRequest(ctx context.Context, target, expectedIssuer string, out any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "beeapi-cli/2")
	client := cloneHTTPClient(c.HTTP)
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		canonicalDiscovery := expectedIssuer + "/.well-known/oauth-authorization-server"
		if len(via) != 1 || next.Method != http.MethodGet || next.URL.String() != canonicalDiscovery {
			return errors.New("BeeAPI OAuth discovery attempted an untrusted redirect")
		}
		return nil
	}
	resp, err := c.doOAuthRequestWithClient(client, req, out)
	if err != nil {
		if resp != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) {
				return resp, err
			}
			return resp, newAPIError(resp, 0, err.Error(), "oauth.discovery_request_failed")
		}
		return nil, fmt.Errorf("BeeAPI OAuth discovery GET %s: %w", target, err)
	}
	return resp, nil
}

func (c *Client) doOAuthRequestNoRedirect(req *http.Request, out any) error {
	resp, err := c.doNoRedirect(req)
	if err != nil {
		return err
	}
	return decodeOAuthResponse(resp, out)
}

func (c *Client) doOAuthRequestWithClient(client *http.Client, req *http.Request, out any) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return resp, err
	}
	return resp, decodeOAuthResponse(resp, out)
}

func (c *Client) doNoRedirect(req *http.Request) (*http.Response, error) {
	client := cloneHTTPClient(c.HTTP)
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client.Do(req)
}

func cloneHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	clone := *client
	return &clone
}

func decodeOAuthResponse(resp *http.Response, out any) error {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
			Message          string `json:"message"`
		}
		_ = json.Unmarshal(b, &failure)
		message := strings.TrimSpace(failure.ErrorDescription)
		if message == "" {
			message = strings.TrimSpace(failure.Message)
		}
		if message == "" {
			message = strings.TrimSpace(string(b))
		}
		if message == "" {
			message = resp.Status
		}
		return newAPIError(resp, 0, message, failure.Error)
	}
	if out == nil || len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err == nil {
		_, hasCode := fields["code"]
		_, hasData := fields["data"]
		if hasCode || hasData {
			var env envelope
			if err := json.Unmarshal(b, &env); err != nil {
				return err
			}
			if env.Code != 0 {
				return newAPIError(resp, env.Code, env.Message, env.Reason)
			}
			if len(env.Data) == 0 || string(env.Data) == "null" {
				return nil
			}
			return json.Unmarshal(env.Data, out)
		}
	}
	if err := json.Unmarshal(b, out); err != nil {
		contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
		message := "OAuth response is not valid JSON"
		if contentType != "" {
			message += " (Content-Type " + contentType + ")"
		}
		message += ": " + err.Error()
		return newAPIError(resp, 0, message, "oauth.invalid_response")
	}
	return nil
}

func SortedOAuthScopes(scope string) []string {
	result := strings.Fields(scope)
	sort.Strings(result)
	return result
}
