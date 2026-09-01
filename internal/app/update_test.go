package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/state"
	"github.com/BeeAPI-AI/beeapi/internal/updater"
)

func TestStartupUpdateCheckIsCachedAndOnlyNotifiesOnce(t *testing.T) {
	store := &state.Store{Dir: t.TempDir()}
	calls := 0
	client := &updater.Client{
		HTTP: &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header), Request: request,
				Body: io.NopCloser(strings.NewReader(`{"tag_name":"v0.3.0","published_at":"2026-09-01T00:00:00Z"}`)),
			}, nil
		})},
		MetadataURLs: []string{"https://updates.test/latest.json"},
	}
	var output bytes.Buffer
	r := &runner{ctx: context.Background(), version: "v0.2.2", language: languageChinese, store: store, out: &output, updateClient: client}
	r.notifyUpdateIfAvailable()
	r.notifyUpdateIfAvailable()
	if calls != 1 {
		t.Fatalf("metadata requests = %d, want 1", calls)
	}
	if strings.Count(output.String(), "有新版本 v0.3.0") != 1 || !strings.Contains(output.String(), "beeapi update") {
		t.Fatalf("unexpected update notice: %s", output.String())
	}
}

func TestDevelopmentVersionDoesNotMakeStartupNetworkRequest(t *testing.T) {
	store := &state.Store{Dir: t.TempDir()}
	client := &updater.Client{HTTP: &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatal("development build made an update request")
		return nil, nil
	})}, MetadataURLs: []string{"https://updates.test/latest.json"}}
	r := &runner{ctx: context.Background(), version: "dev", store: store, out: io.Discard, updateClient: client}
	r.notifyUpdateIfAvailable()
}
