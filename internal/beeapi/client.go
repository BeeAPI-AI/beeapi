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

func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+apiPrefix+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "beeapi-cli/1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
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
		base := normalizeBaseURL(raw)
		if base == "" {
			continue
		}
		seen[base] = Endpoint{Name: fallbackName(base), BaseURL: base}
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
			item.BaseURL = normalizeBaseURL(item.BaseURL)
			if item.BaseURL != "" {
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
			req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, item.BaseURL+apiPrefix+"/public/api-endpoints", nil)
			if err == nil {
				req.Header.Set("Accept", "application/json")
				resp, doErr := (&http.Client{Timeout: 5 * time.Second}).Do(req)
				err = doErr
				if resp != nil {
					io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
					resp.Body.Close()
					if err == nil && (resp.StatusCode < 200 || resp.StatusCode >= 400) {
						err = fmt.Errorf("HTTP %d", resp.StatusCode)
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

func normalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(strings.TrimRight(raw, "/"))
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = "", "", "", ""
	return strings.TrimRight(u.String(), "/")
}

func fallbackName(base string) string {
	if strings.Contains(base, "beeapi.dev") {
		return "国内备用"
	}
	return "国际"
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
	err := c.request(ctx, http.MethodPost, "/auth/device/code", map[string]any{
		"client_id":   "getbeeapi-cli",
		"scope":       "api-keys:list api-keys:export-one",
		"device_name": deviceName,
		"platform":    runtime.GOOS + "/" + runtime.GOARCH,
	}, &code)
	return code, err
}

func (c *Client) PollDeviceAuth(ctx context.Context, deviceCode string) (DeviceToken, error) {
	var token DeviceToken
	err := c.request(ctx, http.MethodPost, "/auth/device/token", map[string]string{
		"client_id":   "getbeeapi-cli",
		"device_code": deviceCode,
	}, &token)
	return token, err
}

type FlexibleID string

func (id *FlexibleID) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*id = ""
		return nil
	}
	if data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*id = FlexibleID(value)
		return nil
	}
	*id = FlexibleID(string(data))
	return nil
}

type CLIAPIKey struct {
	ID         FlexibleID `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Prefix     string     `json:"prefix"`
	Status     string     `json:"status"`
	GroupName  string     `json:"group_name"`
	ExpiresAt  string     `json:"expires_at"`
	Exportable bool       `json:"exportable"`
}

func (c *Client) CLIAPIKeys(ctx context.Context) ([]CLIAPIKey, error) {
	var result struct {
		Items []CLIAPIKey `json:"items"`
	}
	if err := c.request(ctx, http.MethodGet, "/cli/api-keys", nil, &result); err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (c *Client) ExportCLIAPIKey(ctx context.Context, id FlexibleID) (string, error) {
	var result struct {
		APIKey    string `json:"api_key"`
		Plaintext string `json:"plaintext"`
	}
	if err := c.request(ctx, http.MethodPost, "/cli/api-keys/"+url.PathEscape(string(id))+"/export", map[string]string{}, &result); err != nil {
		return "", err
	}
	if result.APIKey != "" {
		return result.APIKey, nil
	}
	if result.Plaintext != "" {
		return result.Plaintext, nil
	}
	return "", errors.New("BeeAPI 没有返回所选 API Key")
}

type Model struct {
	ID string `json:"id"`
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
