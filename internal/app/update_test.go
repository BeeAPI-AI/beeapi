package app

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/state"
	"github.com/BeeAPI-AI/beeapi/internal/updater"
)

func TestNonInteractiveStartupChecksEachTimeButOnlyNotifiesOnce(t *testing.T) {
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
	input := strings.NewReader("")
	r := &runner{ctx: context.Background(), version: "v0.2.2", language: languageChinese, store: store, in: input, reader: bufio.NewReader(input), out: &output, updateClient: client}
	if exit, err := r.promptStartupUpdateIfAvailable(); err != nil || exit {
		t.Fatalf("first startup update check = exit %v, err %v", exit, err)
	}
	if exit, err := r.promptStartupUpdateIfAvailable(); err != nil || exit {
		t.Fatalf("second startup update check = exit %v, err %v", exit, err)
	}
	if calls != 2 {
		t.Fatalf("metadata requests = %d, want one silent check per startup", calls)
	}
	if strings.Count(output.String(), "有新版本 v0.3.0") != 1 || !strings.Contains(output.String(), "beeapi update") {
		t.Fatalf("unexpected update notice: %s", output.String())
	}
}

func TestInteractiveStartupOffersUpdateAndDeclineContinues(t *testing.T) {
	store := &state.Store{Dir: t.TempDir()}
	client := &updater.Client{
		HTTP: &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header), Request: request,
				Body: io.NopCloser(strings.NewReader(`{"tag_name":"v0.5.2"}`)),
			}, nil
		})},
		MetadataURLs: []string{"https://updates.test/latest.json"},
	}
	input := strings.NewReader("n\n")
	var output bytes.Buffer
	r := &runner{
		ctx: context.Background(), version: "v0.5.1", language: languageChinese,
		store: store, in: input, reader: bufio.NewReader(input), out: &output, errOut: &output,
		updateClient: client, interactive: func() bool { return true },
	}
	exit, err := r.promptStartupUpdateIfAvailable()
	if err != nil || exit {
		t.Fatalf("declined startup update = exit %v, err %v", exit, err)
	}
	for _, want := range []string{"发现新版本 v0.5.2", "现在更新？[Y/n]"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("startup update prompt is missing %q:\n%s", want, output.String())
		}
	}
}

func TestInteractiveStartupUpdateInstallsAndExitsBeforeHome(t *testing.T) {
	store := &state.Store{Dir: t.TempDir()}
	client := &updater.Client{
		HTTP: &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header), Request: request,
				Body: io.NopCloser(strings.NewReader(`{"tag_name":"v0.5.2"}`)),
			}, nil
		})},
		MetadataURLs: []string{"https://updates.test/latest.json"},
	}
	input := strings.NewReader("\n")
	var output bytes.Buffer
	installed := false
	r := &runner{
		ctx: context.Background(), version: "v0.5.1", language: languageChinese,
		store: store, in: input, reader: bufio.NewReader(input), out: &output, errOut: &output,
		updateClient: client, interactive: func() bool { return true },
		executablePath: func() (string, error) { return `C:\Users\Admin\AppData\Local\GetBeeAPI\bin\beeapi.exe`, nil },
		updateInstall: func(_ context.Context, release updater.Release, target string) (updater.Result, error) {
			installed = release.TagName == "v0.5.2" && strings.HasSuffix(target, `GetBeeAPI\bin\beeapi.exe`)
			return updater.Result{Version: release.TagName, Target: target, Scheduled: true}, nil
		},
	}
	exit, err := r.promptStartupUpdateIfAvailable()
	if err != nil || !exit || !installed {
		t.Fatalf("accepted startup update = exit %v, installed %v, err %v", exit, installed, err)
	}
	for _, want := range []string{"当前程序:", "后台将持续重试替换", "等待约 3 秒", `GetBeeAPI\bin\beeapi.exe`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("startup update result is missing %q:\n%s", want, output.String())
		}
	}
}

func TestDevelopmentVersionDoesNotMakeStartupNetworkRequest(t *testing.T) {
	store := &state.Store{Dir: t.TempDir()}
	client := &updater.Client{HTTP: &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatal("development build made an update request")
		return nil, nil
	})}, MetadataURLs: []string{"https://updates.test/latest.json"}}
	r := &runner{ctx: context.Background(), version: "dev", store: store, out: io.Discard, updateClient: client}
	if exit, err := r.promptStartupUpdateIfAvailable(); err != nil || exit {
		t.Fatalf("development startup update check = exit %v, err %v", exit, err)
	}
}

func TestUpdateDownloadReporterShowsProgressAndFallbackSource(t *testing.T) {
	var output bytes.Buffer
	r := &runner{language: languageChinese, out: &output, errOut: &output, interactive: func() bool { return true }}
	report := r.updateDownloadReporter()
	report(updater.DownloadEvent{Status: updater.DownloadStarted, Source: "getbeeapi.com", Asset: "beeapi_windows_amd64.zip"})
	report(updater.DownloadEvent{Status: updater.DownloadProgress, Source: "getbeeapi.com", Downloaded: 1 << 20, Total: 4 << 20})
	report(updater.DownloadEvent{Status: updater.DownloadFailed, Source: "getbeeapi.com", Error: "timeout", WillRetry: true})
	report(updater.DownloadEvent{Status: updater.DownloadStarted, Source: "github.com", Asset: "beeapi_windows_amd64.zip"})
	report(updater.DownloadEvent{Status: updater.DownloadVerified, Source: "github.com"})

	text := output.String()
	for _, want := range []string{"下载来源: getbeeapi.com", "下载进度", "25%", "正在尝试下一来源", "下载来源: github.com", "通过 SHA-256 校验"} {
		if !strings.Contains(text, want) {
			t.Fatalf("download report is missing %q:\n%s", want, text)
		}
	}
}
