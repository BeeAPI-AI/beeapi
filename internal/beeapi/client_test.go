package beeapi

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestDeviceAuthAcceptsStandardOAuthResponseAndError(t *testing.T) {
	client := New("https://beeapi.test")
	requests := 0
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Header.Get("DPoP") == "" {
			t.Fatalf("request %d is missing its DPoP proof", requests)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("public device request unexpectedly has authorization: %q", got)
		}
		if requests == 1 {
			var requestBody map[string]any
			if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
				t.Fatal(err)
			}
			if requestBody["scope"] != "cli:configure" {
				t.Fatalf("unexpected device scope: %#v", requestBody["scope"])
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(`{"device_code":"device-secret","user_code":"BEE7-K9Q2","verification_uri":"https://beeapi.test/cli/authorize","expires_in":600,"interval":5}`)),
				Request:    r,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":"authorization_pending","error_description":"Waiting for user"}`)),
			Request:    r,
		}, nil
	})}

	code, err := client.StartDeviceAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if code.DeviceCode != "device-secret" || code.UserCode != "BEE7-K9Q2" || code.Interval != 5 {
		t.Fatalf("unexpected standard device response: %#v", code)
	}
	_, err = client.PollDeviceAuth(context.Background(), code.DeviceCode)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "authorization_pending" {
		t.Fatalf("unexpected standard OAuth error: %#v", err)
	}
}

func TestCLISessionClaimsDeviceSpecificCredentials(t *testing.T) {
	client := New("https://beeapi.test")
	client.Token = "beecli-session"
	requests := 0
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if got := r.Header.Get("Authorization"); got != "DPoP beecli-session" {
			t.Fatalf("unexpected CLI authorization header: %q", got)
		}
		if r.Header.Get("DPoP") == "" {
			t.Fatal("protected CLI request is missing its DPoP proof")
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/cli/credentials/claim" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body := `{"code":0,"message":"ok","data":{"credentials":[{"credential_id":"bcc_grant-1","profile_name":"daily","source_key_prefix":"sk-source","device_key_name":"CLI · daily","device_key_prefix":"sk-device","api_key":"sk-device-secret"},{"credential_id":"bcc_grant-2","profile_name":"coding","source_key_prefix":"sk-source-2","device_key_name":"CLI · coding","device_key_prefix":"sk-device-2","api_key":"sk-device-secret-2"}],"retry_until":"2026-08-28T12:00:00Z"}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
	})}

	claim, err := client.ClaimCLICredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(claim.Credentials) != 2 || claim.Credentials[0].ProfileName != "daily" {
		t.Fatalf("unexpected credentials: %#v", claim.Credentials)
	}
	if claim.Credentials[0].APIKey != "sk-device-secret" || claim.Credentials[1].CredentialID != "bcc_grant-2" {
		t.Fatalf("claim payload was not decoded: %#v", claim.Credentials)
	}
	if requests != 1 {
		t.Fatalf("claim request count = %d, want 1", requests)
	}
}
