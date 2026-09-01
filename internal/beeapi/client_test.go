package beeapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
)

func TestModelsDeduplicatesAndRanks(t *testing.T) {
	client := New("https://beeapi.test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"data":[{"id":"gemini-2"},{"id":"gpt-5-codex"},{"id":"claude-sonnet"},{"id":"gpt-5-codex"}]}`)),
			Request:    r,
		}, nil
	})}

	models, err := client.Models(context.Background(), "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude-sonnet", "gpt-5-codex", "gemini-2"}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestUsageUsesAPIKeyAndDecodesWalletStatus(t *testing.T) {
	client := New("https://beeapi.test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/usage" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-usage-secret" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		body := `{"is_active":true,"isValid":true,"balance":12.3456,"remaining":12.3456,"currency":"USD","unit":"USD","key_id":7,"key_name":"Coding","key_prefix":"sk-live","plan_name":"OpenAI Plus"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
	})}

	usage, err := client.Usage(context.Background(), " sk-usage-secret ")
	if err != nil {
		t.Fatal(err)
	}
	if !usage.IsValid || !usage.IsActive || usage.Balance != 12.3456 || usage.KeyName != "Coding" || usage.PlanName != "OpenAI Plus" {
		t.Fatalf("unexpected usage response: %#v", usage)
	}
}

func TestUsageReturnsStructuredAPIError(t *testing.T) {
	client := New("https://beeapi.test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"invalid api key"}}`)),
			Request:    r,
		}, nil
	})}

	_, err := client.Usage(context.Background(), "sk-invalid")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || apiErr.Message != "invalid api key" {
		t.Fatalf("unexpected usage error: %#v", err)
	}
}

func TestModelOptionsUsesBeeAPIEnvelopeAndAPIKeyAuth(t *testing.T) {
	client := New("https://beeapi.test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/client/model-options" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-model-options" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		body := `{"code":0,"message":"success","data":{"models":[{"id":"gpt-5.6","protocols":["openai/responses"],"capabilities":["reasoning","tools"],"recommended_for":["codex"],"priority":98,"context_window_tokens":400000,"max_output_tokens":128000},{"id":"gpt-5.6","protocols":["openai/responses"],"priority":1},{"id":" ","protocols":[]}]}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
	})}

	options, err := client.ModelOptions(context.Background(), " sk-model-options ")
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || options[0].ID != "gpt-5.6" || options[0].Priority != 98 {
		t.Fatalf("unexpected model options: %#v", options)
	}
	if !reflect.DeepEqual(options[0].Protocols, []string{"openai/responses"}) || !reflect.DeepEqual(options[0].RecommendedFor, []string{"codex"}) {
		t.Fatalf("model capability metadata was not decoded: %#v", options[0])
	}
	if options[0].ContextWindowTokens == nil || *options[0].ContextWindowTokens != 400000 {
		t.Fatalf("context window was not decoded: %#v", options[0].ContextWindowTokens)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestNormalizeBaseURLRejectsUnsafeValues(t *testing.T) {
	for _, raw := range []string{"http://beeapi.ai", "https://user@beeapi.ai", "file:///etc/hosts", "not-a-url"} {
		if got := NormalizeBaseURL(raw); got != "" {
			t.Errorf("NormalizeBaseURL(%q) = %q, want empty", raw, got)
		}
	}
	if got := NormalizeBaseURL("https://beeapi.ai/path?q=1#x"); got != "https://beeapi.ai" {
		t.Fatalf("normalized URL = %q", got)
	}
}

func TestProbeEndpointsUsesHealthzAndRequiresHealthyJSON(t *testing.T) {
	previousTransport := http.DefaultTransport
	healthStatus := "ok"
	requestedPath := ""
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requestedPath = r.URL.Path
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"status":"` + healthStatus + `"}`)),
			Request:    r,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	items := ProbeEndpoints(context.Background(), []Endpoint{{Name: "测试入口", BaseURL: "https://beeapi.test"}})
	if len(items) != 1 || !items[0].Reachable {
		t.Fatalf("healthy endpoint was not accepted: %#v", items)
	}
	if requestedPath != "/healthz" {
		t.Fatalf("probe path = %q, want /healthz", requestedPath)
	}

	healthStatus = "degraded"
	items = ProbeEndpoints(context.Background(), []Endpoint{{Name: "异常入口", BaseURL: "https://beeapi.test"}})
	if len(items) != 1 || items[0].Reachable || items[0].Error == "" {
		t.Fatalf("unhealthy endpoint was accepted: %#v", items)
	}
}

func TestEndpointNamesAvoidInternationalLabels(t *testing.T) {
	if got := endpointName("https://beeapi.ai", "International"); got != "主域名" {
		t.Fatalf("beeapi.ai name = %q", got)
	}
	if got := endpointName("https://beeapi.dev", "International backup"); got != "备用域名" {
		t.Fatalf("beeapi.dev name = %q", got)
	}
	if got := endpointName("https://beeapi.ai.attacker.test", "External"); got != "External" {
		t.Fatalf("lookalike domain was mislabeled as official: %q", got)
	}
}
