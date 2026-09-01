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
	OAuthCanonicalIssuer = "https://beeapi.dev"
	OAuthDeviceGrant     = "urn:ietf:params:oauth:grant-type:device_code"
)

// ErrOAuthDiscoveryUnavailable marks an official endpoint that serves its SPA
// fallback (or an empty metadata document) because OAuth discovery has not
// been deployed there yet. Callers may use the legacy device flow for this
// narrow rollout case; untrusted or partially invalid metadata still fails
// closed through validateOAuthMetadata.
var ErrOAuthDiscoveryUnavailable = errors.New("BeeAPI OAuth discovery is unavailable")

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
	target := strings.TrimRight(c.BaseURL, "/") + "/.well-known/oauth-authorization-server"
	if err := c.oauthJSONRequest(ctx, http.MethodGet, target, nil, "", &metadata); err != nil {
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			return OAuthMetadata{}, fmt.Errorf("%w: endpoint did not return JSON", ErrOAuthDiscoveryUnavailable)
		}
		return metadata, err
	}
	if strings.TrimSpace(metadata.Issuer) == "" &&
		strings.TrimSpace(metadata.AuthorizationEndpoint) == "" &&
		strings.TrimSpace(metadata.TokenEndpoint) == "" &&
		strings.TrimSpace(metadata.DeviceAuthorizationEndpoint) == "" &&
		strings.TrimSpace(metadata.RevocationEndpoint) == "" {
		return OAuthMetadata{}, fmt.Errorf("%w: metadata is empty", ErrOAuthDiscoveryUnavailable)
	}
	if err := validateOAuthMetadata(c.BaseURL, metadata); err != nil {
		return OAuthMetadata{}, err
	}
	return metadata, nil
}

func validateOAuthMetadata(baseURL string, metadata OAuthMetadata) error {
	if strings.TrimRight(strings.TrimSpace(metadata.Issuer), "/") != OAuthCanonicalIssuer || strings.TrimSpace(metadata.Issuer) != OAuthCanonicalIssuer {
		return fmt.Errorf("BeeAPI OAuth issuer must be %s", OAuthCanonicalIssuer)
	}
	if err := validateTrustedOAuthURL(baseURL, metadata.Issuer, "issuer"); err != nil {
		return err
	}
	for label, raw := range map[string]string{
		"authorization_endpoint":        metadata.AuthorizationEndpoint,
		"token_endpoint":                metadata.TokenEndpoint,
		"device_authorization_endpoint": metadata.DeviceAuthorizationEndpoint,
		"revocation_endpoint":           metadata.RevocationEndpoint,
	} {
		if strings.TrimSpace(raw) == "" && label == "device_authorization_endpoint" {
			continue
		}
		if err := validateTrustedOAuthURL(baseURL, raw, label); err != nil {
			return err
		}
		parsed, _ := url.Parse(strings.TrimSpace(raw))
		if parsed.Scheme+"://"+parsed.Host != OAuthCanonicalIssuer {
			return fmt.Errorf("BeeAPI OAuth %s must use canonical issuer %s", label, OAuthCanonicalIssuer)
		}
	}
	if !containsOAuthValue(metadata.ResponseTypesSupported, "code") || !containsOAuthValue(metadata.GrantTypesSupported, "authorization_code") {
		return errors.New("BeeAPI OAuth metadata does not support authorization code")
	}
	if !containsOAuthValue(metadata.CodeChallengeMethods, "S256") {
		return errors.New("BeeAPI OAuth metadata does not require PKCE S256")
	}
	if len(metadata.TokenAuthMethods) > 0 && !containsOAuthValue(metadata.TokenAuthMethods, "none") {
		return errors.New("BeeAPI OAuth metadata does not support a public CLI client")
	}
	if !containsOAuthValue(metadata.GrantTypesSupported, "refresh_token") {
		return errors.New("BeeAPI OAuth metadata does not support refresh token rotation")
	}
	if !containsOAuthValue(metadata.DPoPSigningAlgorithms, "ES256") {
		return errors.New("BeeAPI OAuth metadata does not support DPoP ES256")
	}
	for _, scope := range OAuthAccountScopes {
		if !containsOAuthValue(metadata.ScopesSupported, scope) {
			return fmt.Errorf("BeeAPI OAuth metadata does not support required scope %s", scope)
		}
	}
	return nil
}

func validateTrustedOAuthURL(baseURL, raw, label string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Port() != "" {
		return fmt.Errorf("BeeAPI OAuth %s is not a valid HTTPS URL", label)
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Hostname() == "" {
		return errors.New("BeeAPI base URL is invalid")
	}
	for _, endpoint := range BootstrapEndpoints {
		trusted, parseErr := url.Parse(endpoint)
		if parseErr == nil && strings.EqualFold(trusted.Hostname(), parsed.Hostname()) {
			return nil
		}
	}
	_ = base
	return fmt.Errorf("BeeAPI OAuth %s host is not trusted", label)
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
	return c.doOAuthRequest(req, out)
}

func (c *Client) oauthJSONRequest(ctx context.Context, method, target string, body io.Reader, accessToken string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "beeapi-cli/2")
	if accessToken != "" {
		req.Header.Set("Authorization", "DPoP "+accessToken)
		signer, signerErr := c.ensureDPoP()
		if signerErr != nil {
			return signerErr
		}
		proof, proofErr := signer.proof(method, target, accessToken, time.Now())
		if proofErr != nil {
			return proofErr
		}
		req.Header.Set("DPoP", proof)
	}
	return c.doOAuthRequest(req, out)
}

func (c *Client) doOAuthRequest(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
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
		return &APIError{Status: resp.StatusCode, Message: message, Reason: strings.TrimSpace(failure.Error)}
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
				return &APIError{Status: resp.StatusCode, Code: env.Code, Message: env.Message, Reason: env.Reason}
			}
			if len(env.Data) == 0 || string(env.Data) == "null" {
				return nil
			}
			return json.Unmarshal(env.Data, out)
		}
	}
	return json.Unmarshal(b, out)
}

func SortedOAuthScopes(scope string) []string {
	result := strings.Fields(scope)
	sort.Strings(result)
	return result
}
