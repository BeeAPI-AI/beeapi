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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestNormalizeBaseURLRejectsUnsafeValues(t *testing.T) {
	for _, raw := range []string{"http://beeapi.ai", "https://user@beeapi.ai", "file:///etc/hosts", "not-a-url"} {
		if got := normalizeBaseURL(raw); got != "" {
			t.Errorf("normalizeBaseURL(%q) = %q, want empty", raw, got)
		}
	}
	if got := normalizeBaseURL("https://beeapi.ai/path?q=1#x"); got != "https://beeapi.ai" {
		t.Fatalf("normalized URL = %q", got)
	}
}

func TestDeviceAuthAcceptsStandardOAuthResponseAndError(t *testing.T) {
	client := New("https://beeapi.test")
	requests := 0
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
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

func TestCLISessionListsAndExportsOneExistingKey(t *testing.T) {
	client := New("https://beeapi.test")
	client.Token = "beecli-session"
	requests := 0
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if got := r.Header.Get("Authorization"); got != "Bearer beecli-session" {
			t.Fatalf("unexpected CLI authorization header: %q", got)
		}
		var body string
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/cli/api-keys":
			body = `{"code":0,"message":"ok","data":{"items":[{"id":42,"name":"daily","key_prefix":"sk-bee-12ab","status":"active","group_name":"default","exportable":true}]}}`
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/cli/api-keys/42/export":
			body = `{"code":0,"message":"ok","data":{"api_key":"sk-existing-secret"}}`
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
	})}

	keys, err := client.CLIAPIKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Name != "daily" || !keys[0].Exportable {
		t.Fatalf("unexpected key summaries: %#v", keys)
	}
	secret, err := client.ExportCLIAPIKey(context.Background(), keys[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "sk-existing-secret" || requests != 2 {
		t.Fatalf("unexpected exported key: %q (requests=%d)", secret, requests)
	}
}
