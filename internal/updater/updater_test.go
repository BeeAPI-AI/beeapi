package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type updaterRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn updaterRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func updaterResponse(request *http.Request, status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}
}

func TestSemanticVersionComparison(t *testing.T) {
	for _, test := range []struct {
		latest, current string
		newer           bool
	}{
		{"v0.3.0", "v0.2.9", true},
		{"v1.0.0", "0.99.99", true},
		{"v1.0.0", "v1.0.0-rc.1", true},
		{"v1.0.0-rc.2", "v1.0.0-rc.10", false},
		{"v1.0.0", "v1.0.0", false},
		{"v0.2.0", "dev", false},
	} {
		if got := IsNewer(test.latest, test.current); got != test.newer {
			t.Fatalf("IsNewer(%q, %q) = %v, want %v", test.latest, test.current, got, test.newer)
		}
	}
}

func TestInstallDownloadsChecksumVerifiesAndAtomicallyReplaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synchronous replacement is covered by the Windows locked-executable test")
	}
	archive := tarGzFixture(t, "beeapi", []byte("new-beeapi-binary"))
	digest := sha256.Sum256(archive)
	httpClient := &http.Client{Transport: updaterRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(request.URL.Path, ".tar.gz"):
			return updaterResponse(request, http.StatusOK, archive), nil
		case strings.HasSuffix(request.URL.Path, ".sha256"):
			return updaterResponse(request, http.StatusOK, []byte(fmt.Sprintf("%x\n", digest))), nil
		default:
			return updaterResponse(request, http.StatusNotFound, nil), nil
		}
	})}

	target := filepath.Join(t.TempDir(), "beeapi")
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &Client{
		HTTP: httpClient, DownloadBases: []string{"https://updates.test/releases/{version}/download"},
		GOOS: "linux", GOARCH: "amd64",
	}
	result, err := client.Install(context.Background(), Release{TagName: "v1.2.3"}, target)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "new-beeapi-binary" || result.Version != "v1.2.3" || result.Scheduled {
		t.Fatalf("unexpected install result: result=%#v body=%q", result, body)
	}
}

func TestDefaultClientDoesNotApplyAWholeBodyTimeout(t *testing.T) {
	client := DefaultClient()
	if client.HTTP == nil {
		t.Fatal("default update HTTP client is nil")
	}
	if client.HTTP.Timeout != 0 {
		t.Fatalf("whole-request timeout = %s, want zero so slow response bodies use the per-download context", client.HTTP.Timeout)
	}
	transport, ok := client.HTTP.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default transport type = %T", client.HTTP.Transport)
	}
	if transport.DialContext == nil || transport.TLSHandshakeTimeout != tlsHandshakeTimeout || transport.ResponseHeaderTimeout != responseHeaderTimeout {
		t.Fatalf("network phase timeouts were not configured: %#v", transport)
	}
}

func TestInstallReportsFailedSourceAndFallsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synchronous replacement is covered by the Windows locked-executable test")
	}
	archive := tarGzFixture(t, "beeapi", []byte("fallback-beeapi-binary"))
	digest := sha256.Sum256(archive)
	httpClient := &http.Client{Transport: updaterRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "slow.test" {
			return nil, errors.New("simulated timeout")
		}
		if strings.HasSuffix(request.URL.Path, ".sha256") {
			return updaterResponse(request, http.StatusOK, []byte(fmt.Sprintf("%x\n", digest))), nil
		}
		return updaterResponse(request, http.StatusOK, archive), nil
	})}
	var events []DownloadEvent
	client := &Client{
		HTTP: httpClient,
		DownloadBases: []string{
			"https://slow.test/{version}",
			"https://fallback.test/{version}",
		},
		GOOS: "linux", GOARCH: "amd64",
		OnDownload: func(event DownloadEvent) { events = append(events, event) },
	}
	target := filepath.Join(t.TempDir(), "beeapi")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := client.Install(context.Background(), Release{TagName: "v1.2.3"}, target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Source, "fallback.test") {
		t.Fatalf("fallback source = %q", result.Source)
	}
	var reportedFailure, reportedProgress, reportedVerified bool
	for _, event := range events {
		switch {
		case event.Status == DownloadFailed && event.Source == "slow.test" && event.WillRetry && strings.Contains(event.Error, "simulated timeout"):
			reportedFailure = true
		case event.Status == DownloadProgress && event.Source == "fallback.test":
			reportedProgress = true
		case event.Status == DownloadVerified && event.Source == "fallback.test":
			reportedVerified = true
		}
	}
	if !reportedFailure || !reportedProgress || !reportedVerified {
		t.Fatalf("missing download lifecycle events: %#v", events)
	}
}

func TestInstallFailureNamesEveryAttemptedSource(t *testing.T) {
	httpClient := &http.Client{Transport: updaterRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("cannot reach %s", request.URL.Host)
	})}
	client := &Client{
		HTTP: httpClient,
		DownloadBases: []string{
			"https://getbeeapi.test/{version}",
			"https://github.test/{version}",
		},
		GOOS: "linux", GOARCH: "amd64",
	}
	target := filepath.Join(t.TempDir(), "beeapi")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := client.Install(context.Background(), Release{TagName: "v1.2.3"}, target)
	if err == nil {
		t.Fatal("install unexpectedly succeeded")
	}
	for _, source := range []string{"getbeeapi.test", "github.test"} {
		if !strings.Contains(err.Error(), source) {
			t.Fatalf("aggregate error is missing %s: %v", source, err)
		}
	}
}

func TestInstallRejectsChecksumMismatchAndPreservesExecutable(t *testing.T) {
	archive := tarGzFixture(t, "beeapi", []byte("untrusted-binary"))
	httpClient := &http.Client{Transport: updaterRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, ".sha256") {
			return updaterResponse(request, http.StatusOK, []byte(strings.Repeat("0", 64))), nil
		}
		return updaterResponse(request, http.StatusOK, archive), nil
	})}
	target := filepath.Join(t.TempDir(), "beeapi")
	if err := os.WriteFile(target, []byte("trusted-old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &Client{
		HTTP: httpClient, DownloadBases: []string{"https://updates.test/{version}"},
		GOOS: "linux", GOARCH: "amd64",
	}
	if _, err := client.Install(context.Background(), Release{TagName: "v1.2.3"}, target); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("unexpected mismatch result: %v", err)
	}
	body, _ := os.ReadFile(target)
	if string(body) != "trusted-old-binary" {
		t.Fatalf("checksum failure replaced the executable: %q", body)
	}
}

func TestArchiveRequiresExactRootExecutable(t *testing.T) {
	archive := tarGzFixture(t, "../../beeapi", []byte("malicious"))
	path := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	err := extractTarGzBinary(path, "beeapi", filepath.Join(t.TempDir(), "beeapi"))
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("unsafe archive entry was accepted: %v", err)
	}
}

func TestLatestFallsBackToSecondMetadataSource(t *testing.T) {
	httpClient := &http.Client{Transport: updaterRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/first" {
			return updaterResponse(request, http.StatusBadGateway, []byte("unavailable")), nil
		}
		return updaterResponse(request, http.StatusOK, []byte(`{"tag_name":"v2.0.1","published_at":"2026-09-01T00:00:00Z"}`)), nil
	})}
	client := &Client{HTTP: httpClient, MetadataURLs: []string{"https://updates.test/first", "https://updates.test/second"}}
	release, err := client.Latest(context.Background())
	if err != nil || release.TagName != "v2.0.1" {
		t.Fatalf("unexpected release: %#v, err=%v", release, err)
	}
}

func tarGzFixture(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
