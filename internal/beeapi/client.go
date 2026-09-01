package beeapi

import (
	"bytes"
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
	"sync"
	"time"
)

const apiPrefix = "/api/v1"

var BootstrapEndpoints = []string{"https://beeapi.ai", "https://beeapi.dev"}

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	dpopMu  sync.Mutex
	dpop    *dpopSigner
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 12 * time.Second},
	}
}

type APIError struct {
	Status  int
	Code    int
	Message string
	Reason  string
}

func (e *APIError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("BeeAPI 返回 %d (%s): %s", e.Status, e.Reason, e.Message)
	}
	return fmt.Sprintf("BeeAPI 返回 %d: %s", e.Status, e.Message)
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Reason  string          `json:"reason"`
}

type proofMode uint8

const (
	proofNone proofMode = iota
	proofPublic
	proofProtected
)

func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	return c.requestWithProof(ctx, method, path, body, out, proofNone)
}

func (c *Client) requestWithProof(ctx context.Context, method, path string, body, out any, mode proofMode) error {
	return c.requestWithProofHeaders(ctx, method, path, body, out, mode, nil)
}

func (c *Client) requestWithProofHeaders(ctx context.Context, method, path string, body, out any, mode proofMode, headers http.Header) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	targetURI := c.BaseURL + apiPrefix + path
	req, err := http.NewRequestWithContext(ctx, method, targetURI, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "beeapi-cli/1")
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if mode != proofNone {
		signer, signerErr := c.ensureDPoP()
		if signerErr != nil {
			return signerErr
		}
		accessToken := ""
		if mode == proofProtected {
			accessToken = strings.TrimSpace(c.Token)
			if accessToken == "" {
				return errors.New("缺少 BeeAPI CLI 配置令牌")
			}
			req.Header.Set("Authorization", "DPoP "+accessToken)
		}
		proof, proofErr := signer.proof(method, targetURI, accessToken, time.Now())
		if proofErr != nil {
			return proofErr
		}
		req.Header.Set("DPoP", proof)
	} else if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(b))}
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(b, out)
	}
	if rawError, ok := fields["error"]; ok {
		var reason, description string
		_ = json.Unmarshal(rawError, &reason)
		_ = json.Unmarshal(fields["error_description"], &description)
		if description == "" {
			description = reason
		}
		return &APIError{Status: resp.StatusCode, Message: description, Reason: reason}
	}
	_, hasCode := fields["code"]
	_, hasData := fields["data"]
	if !hasCode && !hasData {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return &APIError{Status: resp.StatusCode, Message: resp.Status}
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(b, out)
	}
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || env.Code != 0 {
		message := env.Message
		if message == "" {
			message = resp.Status
		}
		return &APIError{Status: resp.StatusCode, Code: env.Code, Message: message, Reason: env.Reason}
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

func (c *Client) ensureDPoP() (*dpopSigner, error) {
	c.dpopMu.Lock()
	defer c.dpopMu.Unlock()
	if c.dpop != nil {
		return c.dpop, nil
	}
	signer, err := newDPoPSigner()
	if err != nil {
		return nil, fmt.Errorf("创建 DPoP 设备密钥: %w", err)
	}
	c.dpop = signer
	return signer, nil
}

// ExportDPoPPrivateJWK serializes the sender-constraining key so an OAuth
// refresh token can remain bound to the same client installation. Callers
// must store the returned value as a secret, never in normal configuration.
func (c *Client) ExportDPoPPrivateJWK() (string, error) {
	signer, err := c.ensureDPoP()
	if err != nil {
		return "", err
	}
	return signer.exportPrivateJWK()
}

// ImportDPoPPrivateJWK restores a previously protected sender-constraining
// key before refreshing or using an OAuth Account Token.
func (c *Client) ImportDPoPPrivateJWK(raw string) error {
	signer, err := dpopSignerFromPrivateJWK(raw)
	if err != nil {
		return err
	}
	c.dpopMu.Lock()
	c.dpop = signer
	c.dpopMu.Unlock()
	return nil
}

type Endpoint struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	BaseURL   string        `json:"base_url"`
	IsDefault bool          `json:"is_default"`
	Latency   time.Duration `json:"-"`
	Reachable bool          `json:"-"`
	Error     string        `json:"-"`
}

func (c *Client) PublicEndpoints(ctx context.Context) ([]Endpoint, error) {
	var data struct {
		Items []Endpoint `json:"items"`
	}
	if err := c.request(ctx, http.MethodGet, "/public/api-endpoints", nil, &data); err != nil {
		return nil, err
	}
	return data.Items, nil
}

func DiscoverEndpoints(ctx context.Context, bootstraps []string) []Endpoint {
	if len(bootstraps) == 0 {
		bootstraps = BootstrapEndpoints
	}
	seen := map[string]Endpoint{}
	for _, raw := range bootstraps {
		base := NormalizeBaseURL(raw)
		if base == "" {
			continue
		}
		seen[base] = Endpoint{Name: endpointName(base, ""), BaseURL: base}
	}
	initial := make([]Endpoint, 0, len(seen))
	for _, item := range seen {
		initial = append(initial, item)
	}
	initial = ProbeEndpoints(ctx, initial)
	var discoveryBase string
	for _, item := range initial {
		if item.Reachable {
			discoveryBase = item.BaseURL
			break
		}
	}
	if discoveryBase == "" {
		return initial
	}
	for _, bootstrap := range append([]string{discoveryBase}, bootstraps...) {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		items, err := New(bootstrap).PublicEndpoints(probeCtx)
		cancel()
		if err != nil {
			continue
		}
		for _, item := range items {
			item.BaseURL = NormalizeBaseURL(item.BaseURL)
			if item.BaseURL != "" {
				item.Name = endpointName(item.BaseURL, item.Name)
				seen[item.BaseURL] = item
			}
		}
		break
	}
	items := make([]Endpoint, 0, len(seen))
	for _, item := range seen {
		items = append(items, item)
	}
	if len(items) == len(initial) {
		return initial
	}
	return ProbeEndpoints(ctx, items)
}

func ProbeEndpoints(ctx context.Context, items []Endpoint) []Endpoint {
	var wg sync.WaitGroup
	result := make([]Endpoint, len(items))
	for index, item := range items {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := time.Now()
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, item.BaseURL+"/healthz", nil)
			if err == nil {
				req.Header.Set("Accept", "application/json")
				resp, doErr := (&http.Client{Timeout: 5 * time.Second}).Do(req)
				err = doErr
				if resp != nil {
					var health struct {
						Status string `json:"status"`
					}
					decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&health)
					resp.Body.Close()
					if err == nil && resp.StatusCode != http.StatusOK {
						err = fmt.Errorf("HTTP %d", resp.StatusCode)
					} else if err == nil && (decodeErr != nil || !strings.EqualFold(strings.TrimSpace(health.Status), "ok")) {
						err = errors.New("healthz 响应无效")
					}
				}
			}
			item.Latency = time.Since(started)
			item.Reachable = err == nil
			if err != nil {
				item.Error = err.Error()
			}
			result[index] = item
		}()
	}
	wg.Wait()
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Reachable != result[j].Reachable {
			return result[i].Reachable
		}
		if result[i].Reachable && result[i].Latency != result[j].Latency {
			return result[i].Latency < result[j].Latency
		}
		if result[i].IsDefault != result[j].IsDefault {
			return result[i].IsDefault
		}
		return result[i].BaseURL < result[j].BaseURL
	})
	return result
}

func BestEndpoint(items []Endpoint) (Endpoint, error) {
	for _, item := range items {
		if item.Reachable {
			return item, nil
		}
	}
	return Endpoint{}, errors.New("没有检测到可用的 BeeAPI 官方入口")
}

func NormalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(strings.TrimRight(raw, "/"))
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = "", "", "", ""
	return strings.TrimRight(u.String(), "/")
}

func endpointName(base, serverName string) string {
	parsed, _ := url.Parse(base)
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if hostname == "beeapi.dev" {
		return "备用域名"
	}
	if hostname == "beeapi.ai" {
		return "主域名"
	}
	if strings.TrimSpace(serverName) != "" {
		return strings.TrimSpace(serverName)
	}
	return "官方入口"
}

type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	CompleteURI     string `json:"verification_uri_complete"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type DeviceToken struct {
	AccessToken string `json:"access_token"`
	Token       string `json:"token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
	Pending     bool   `json:"pending"`
	Error       string `json:"error"`
	Interval    int    `json:"interval"`
}

func (c *Client) StartDeviceAuth(ctx context.Context) (DeviceCode, error) {
	var code DeviceCode
	deviceName, _ := os.Hostname()
	err := c.requestWithProof(ctx, http.MethodPost, "/auth/device/code", map[string]any{
		"client_id":   "getbeeapi-cli",
		"scope":       "cli:configure",
		"device_name": deviceName,
		"platform":    runtime.GOOS + "/" + runtime.GOARCH,
	}, &code, proofPublic)
	return code, err
}

func (c *Client) PollDeviceAuth(ctx context.Context, deviceCode string) (DeviceToken, error) {
	var token DeviceToken
	err := c.requestWithProof(ctx, http.MethodPost, "/auth/device/token", map[string]string{
		"client_id":   "getbeeapi-cli",
		"device_code": deviceCode,
	}, &token, proofPublic)
	return token, err
}

// CLICredential is an existing account API key exported after the user
// approves the device. The legacy device-key fields remain readable so a
// newer CLI can still consume responses from the v1 device-key rollout.
type CLICredential struct {
	CredentialID string     `json:"credential_id"`
	KeyName      string     `json:"key_name"`
	KeyPrefix    string     `json:"key_prefix"`
	Status       string     `json:"status"`
	ExpiresAt    *time.Time `json:"expires_at"`
	// Legacy device_key_v1 fields.
	ProfileName     string `json:"profile_name"`
	SourceKeyPrefix string `json:"source_key_prefix"`
	DeviceKeyName   string `json:"device_key_name"`
	DeviceKeyPrefix string `json:"device_key_prefix"`
	APIKey          string `json:"api_key"`
}

type CLICredentialSkip struct {
	CredentialID string     `json:"credential_id"`
	KeyName      string     `json:"key_name"`
	KeyPrefix    string     `json:"key_prefix"`
	Status       string     `json:"status"`
	ExpiresAt    *time.Time `json:"expires_at"`
	Reason       string     `json:"reason"`
}

type CLICredentialClaimResult struct {
	CredentialMode string              `json:"credential_mode"`
	Credentials    []CLICredential     `json:"credentials"`
	Skipped        []CLICredentialSkip `json:"skipped"`
	RetryUntil     time.Time           `json:"retry_until"`
}

func (c *Client) ClaimCLICredentials(ctx context.Context) (CLICredentialClaimResult, error) {
	var result CLICredentialClaimResult
	if err := c.requestWithProof(ctx, http.MethodPost, "/cli/credentials/claim", map[string]string{}, &result, proofProtected); err != nil {
		return result, err
	}
	if len(result.Credentials) == 0 {
		if len(result.Skipped) > 0 {
			return result, errors.New("BeeAPI 账户中没有可导出的可用 API Key")
		}
		return result, errors.New("BeeAPI 没有返回可用 API Key")
	}
	for _, credential := range result.Credentials {
		if strings.TrimSpace(credential.CredentialID) == "" || strings.TrimSpace(credential.APIKey) == "" {
			return CLICredentialClaimResult{}, errors.New("BeeAPI 返回的设备凭据不完整")
		}
	}
	return result, nil
}

type Model struct {
	ID string `json:"id"`
}

// Usage is the API-key-scoped wallet and key status returned by /v1/usage.
// Balance is account-level while the validity and metadata fields describe the
// specific API key used for the request.
type Usage struct {
	IsActive       bool    `json:"is_active"`
	IsValid        bool    `json:"isValid"`
	InvalidMessage string  `json:"invalid_message,omitempty"`
	Balance        float64 `json:"balance"`
	Remaining      float64 `json:"remaining"`
	Total          float64 `json:"total,omitempty"`
	Used           float64 `json:"used,omitempty"`
	Currency       string  `json:"currency"`
	Unit           string  `json:"unit"`
	KeyID          int     `json:"key_id"`
	KeyName        string  `json:"key_name,omitempty"`
	KeyPrefix      string  `json:"key_prefix"`
	PlanName       string  `json:"plan_name,omitempty"`
	ExpiresAt      string  `json:"expires_at,omitempty"`
}

func (c *Client) Usage(ctx context.Context, apiKey string) (Usage, error) {
	var usage Usage
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return usage, errors.New("API Key 不能为空")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/usage", nil)
	if err != nil {
		return usage, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", "beeapi-cli/1")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return usage, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return usage, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		var failure struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &failure) == nil {
			if failure.Error.Message != "" {
				message = failure.Error.Message
			} else if failure.Message != "" {
				message = failure.Message
			}
		}
		if message == "" {
			message = resp.Status
		}
		return usage, &APIError{Status: resp.StatusCode, Message: message}
	}
	if err := json.Unmarshal(body, &usage); err != nil {
		return usage, err
	}
	if usage.Currency == "" {
		usage.Currency = usage.Unit
	}
	if usage.Unit == "" {
		usage.Unit = usage.Currency
	}
	if usage.Currency == "" {
		usage.Currency, usage.Unit = "USD", "USD"
	}
	return usage, nil
}

// ModelOption is BeeAPI's API-key-scoped capability view for setup clients.
// It intentionally stays separate from the OpenAI-compatible /v1/models
// response, whose schema cannot describe the wire protocols a route supports.
type ModelOption struct {
	ID                  string   `json:"id"`
	Protocols           []string `json:"protocols"`
	Capabilities        []string `json:"capabilities"`
	RecommendedFor      []string `json:"recommended_for"`
	Priority            int      `json:"priority"`
	ContextWindowTokens *int     `json:"context_window_tokens,omitempty"`
	MaxOutputTokens     *int     `json:"max_output_tokens,omitempty"`
}

func (c *Client) ModelOptions(ctx context.Context, apiKey string) ([]ModelOption, error) {
	scoped := New(c.BaseURL)
	scoped.HTTP = c.HTTP
	scoped.Token = strings.TrimSpace(apiKey)
	if scoped.Token == "" {
		return nil, errors.New("API Key 不能为空")
	}
	var data struct {
		Models []ModelOption `json:"models"`
	}
	if err := scoped.request(ctx, http.MethodGet, "/client/model-options", nil, &data); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(data.Models))
	options := make([]ModelOption, 0, len(data.Models))
	for _, option := range data.Models {
		option.ID = strings.TrimSpace(option.ID)
		if option.ID == "" || seen[option.ID] {
			continue
		}
		seen[option.ID] = true
		options = append(options, option)
	}
	return options, nil
}

func (c *Client) Models(ctx context.Context, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/models?include_aliases=false", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("模型发现失败: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []Model `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	models := make([]string, 0, len(body.Data))
	for _, model := range body.Data {
		id := strings.TrimSpace(model.ID)
		if id != "" && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	sort.SliceStable(models, func(i, j int) bool { return modelRank(models[i]) < modelRank(models[j]) })
	return models, nil
}

func modelRank(id string) string {
	lower := strings.ToLower(id)
	rank := "5"
	switch {
	case strings.Contains(lower, "claude"):
		rank = "1"
	case strings.Contains(lower, "gpt") || strings.Contains(lower, "codex"):
		rank = "2"
	case strings.Contains(lower, "gemini"):
		rank = "3"
	}
	return rank + lower
}
