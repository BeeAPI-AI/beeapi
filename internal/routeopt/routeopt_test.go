package routeopt

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCFSTArgsProbeTheBeeAPIBusinessEndpoint(t *testing.T) {
	args := cfstArgs("/tmp/ip.txt", "/tmp/result.csv", "beeapi.ai")
	joined := strings.Join(args, " ")
	wantURL := "https://beeapi.ai/healthz"
	if !slices.Contains(args, wantURL) {
		t.Fatalf("CFST args are missing business probe URL %q: %#v", wantURL, args)
	}
	for _, want := range []string{"-httping", "-httping-code", "200", "-dd"} {
		if !slices.Contains(args, want) {
			t.Fatalf("CFST args are missing %q: %#v", want, args)
		}
	}
	if slices.Contains(args, "https://beeapi.ai/cdn-cgi/trace") || slices.Contains(args, "-dn") || slices.Contains(args, "-dt") {
		t.Fatalf("CFST args still depend on the unavailable trace or download probe: %#v", args)
	}
	if !strings.Contains(joined, "-p 0") {
		t.Fatalf("CFST should suppress its misleading download-speed table: %#v", args)
	}
}

func TestParseResultExplainsMissingCFSTOutput(t *testing.T) {
	_, err := parseResult(filepath.Join(t.TempDir(), "missing.csv"))
	if err == nil || !strings.Contains(err.Error(), "没有生成测速结果") {
		t.Fatalf("unexpected missing result error: %v", err)
	}
}

func TestCFSTPermissionErrorExplainsNoExecCache(t *testing.T) {
	err := cfstRunError(os.ErrPermission)
	if !strings.Contains(err.Error(), "noexec") || !strings.Contains(err.Error(), "GETBEE_CACHE") {
		t.Fatalf("unexpected permission error: %v", err)
	}
}

func TestCFSTDownloadSourcesPreferFixedEdgeCache(t *testing.T) {
	sources := cfstDownloadSources(
		"v2.3.5",
		"cfst_linux_amd64.tar.gz",
		"https://github.com/XIU2/CloudflareSpeedTest/releases/download/v2.3.5/cfst_linux_amd64.tar.gz",
	)
	want := []string{
		"https://getbeeapi.com/releases/cfst/v2.3.5/cfst_linux_amd64.tar.gz",
		"https://github.com/XIU2/CloudflareSpeedTest/releases/download/v2.3.5/cfst_linux_amd64.tar.gz",
	}
	if !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %#v, want %#v", sources, want)
	}

	sources = cfstDownloadSources("../../bad", "cfst_linux_amd64.tar.gz", want[1])
	if !reflect.DeepEqual(sources, []string{want[1]}) {
		t.Fatalf("unsafe tag should disable the mirror URL: %#v", sources)
	}
}

func TestParseLatencyResultOmitsUnmeasuredSpeedAndUnknownColo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.csv")
	data := "IP,Sent,Received,Loss,Latency,Speed,Colo\n127.0.0.1,4,4,0.00,1.25,0.00,N/A\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := parseResult(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.SpeedMB != "" || result.Colo != "" || result.LatencyMS != "1.25" {
		t.Fatalf("unexpected latency-only result: %#v", result)
	}
}

func TestManagedHostsPreservesUnrelatedEntries(t *testing.T) {
	original := "127.0.0.1 localhost\n10.0.0.2 internal.example\n"
	updated := UpdateManagedHosts(original, "beeapi.ai", "104.16.1.2")
	updated = UpdateManagedHosts(updated, "beeapi.dev", "104.16.1.3")
	updated = UpdateManagedHosts(updated, "beeapi.ai", "104.16.1.4")

	for _, want := range []string{
		"127.0.0.1 localhost",
		"10.0.0.2 internal.example",
		"104.16.1.4 beeapi.ai",
		"104.16.1.3 beeapi.dev",
	} {
		if !strings.Contains(updated, want) {
			t.Fatalf("managed hosts is missing %q:\n%s", want, updated)
		}
	}
	if strings.Contains(updated, "104.16.1.2") {
		t.Fatalf("old managed IP was not replaced:\n%s", updated)
	}

	restored := RemoveManagedHosts(updated, "beeapi.ai")
	if strings.Contains(restored, "beeapi.ai") {
		t.Fatalf("beeapi.ai managed block was not removed:\n%s", restored)
	}
	if !strings.Contains(restored, "beeapi.dev") || !strings.Contains(restored, "internal.example") {
		t.Fatalf("restoring one host damaged other entries:\n%s", restored)
	}
}

func TestApplyAndRestoreHostsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyHosts(path, "beeapi.ai", "104.16.1.2"); err != nil {
		t.Fatal(err)
	}
	if err := RestoreHosts(path, "beeapi.ai"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "127.0.0.1 localhost\n" {
		t.Fatalf("unexpected restored hosts file: %q", got)
	}
}

func TestMalformedManagedBlockNeverDropsTrailingHosts(t *testing.T) {
	original := "127.0.0.1 localhost\n# >>> getbeeapi managed: beeapi.ai\n10.0.0.9 important.internal\n"
	if got := RemoveManagedHosts(original, "beeapi.ai"); got != original {
		t.Fatalf("malformed marker should be preserved exactly:\n%s", got)
	}
}
