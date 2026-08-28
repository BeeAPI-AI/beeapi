package routeopt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCFSTArgsUseTCPForBroadCandidateScan(t *testing.T) {
	args := cfstArgs("/tmp/ip.txt", "/tmp/result.csv")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-tp", "443", "-tlr", "0.2", "-dd"} {
		if !slices.Contains(args, want) {
			t.Fatalf("CFST args are missing %q: %#v", want, args)
		}
	}
	for _, unwanted := range []string{"-httping", "-httping-code", "-url", "-dn", "-dt"} {
		if slices.Contains(args, unwanted) {
			t.Fatalf("broad candidate scan should not use %q: %#v", unwanted, args)
		}
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

func TestSelectValidatedCandidateUsesFastestHealthyAPIProbe(t *testing.T) {
	candidates := []Result{
		{IP: "104.16.0.1", LatencyMS: "10"},
		{IP: "104.16.0.2", LatencyMS: "11"},
		{IP: "104.16.0.3", LatencyMS: "12"},
	}
	latencies := map[string]time.Duration{
		"104.16.0.1": 80 * time.Millisecond,
		"104.16.0.3": 35 * time.Millisecond,
	}
	result, err := selectValidatedCandidate(context.Background(), "beeapi.ai", candidates, func(_ context.Context, _ string, ip string) (time.Duration, error) {
		latency, ok := latencies[ip]
		if !ok {
			return 0, errors.New("blocked")
		}
		return latency, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IP != "104.16.0.3" || result.LatencyMS != "35" {
		t.Fatalf("unexpected validated result: %#v", result)
	}
}

func TestSelectValidatedCandidateExplainsDomainLevelBlocking(t *testing.T) {
	_, err := selectValidatedCandidate(context.Background(), "beeapi.ai", []Result{{IP: "104.16.0.1"}}, func(context.Context, string, string) (time.Duration, error) {
		return 0, errors.New("tls blocked")
	})
	if err == nil || !strings.Contains(err.Error(), "SNI") || !strings.Contains(err.Error(), "Hosts 无法修复") {
		t.Fatalf("unexpected validation error: %v", err)
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
