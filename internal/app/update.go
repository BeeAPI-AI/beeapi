package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BeeAPI-AI/beeapi/internal/state"
	"github.com/BeeAPI-AI/beeapi/internal/updater"
)

const (
	startupUpdateTimeout = 3 * time.Second
	updateNoticeInterval = 24 * time.Hour
	updateInstallTimeout = 12 * time.Minute
)

func (r *runner) updaterClient() *updater.Client {
	if r.updateClient != nil {
		return r.updateClient
	}
	return updater.DefaultClient()
}

func (r *runner) startupUpdateRelease() (updater.Release, bool) {
	if !versionCanCheck(r.version) || r.store == nil {
		return updater.Release{}, false
	}
	now := time.Now().UTC()
	status, _ := r.store.LoadUpdateStatus()
	baseCtx := r.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, startupUpdateTimeout)
	release, err := r.updaterClient().Latest(ctx)
	cancel()
	if err == nil {
		status.CheckedAt = now
		status.LatestVersion = release.TagName
		_ = r.store.SaveUpdateStatus(status)
	} else {
		release.TagName = status.LatestVersion
	}
	if !updater.IsNewer(release.TagName, r.version) {
		return updater.Release{}, false
	}
	return release, true
}

func (r *runner) markUpdateNotified(version string) {
	if r.store == nil {
		return
	}
	status, _ := r.store.LoadUpdateStatus()
	status.NotifiedVersion = strings.TrimSpace(version)
	status.NotifiedAt = time.Now().UTC()
	_ = r.store.SaveUpdateStatus(status)
}

func (r *runner) promptStartupUpdateIfAvailable() (bool, error) {
	release, available := r.startupUpdateRelease()
	if !available {
		return false, nil
	}
	if !r.interactiveTerminal() {
		status, _ := r.store.LoadUpdateStatus()
		if status.NotifiedVersion != release.TagName || status.NotifiedAt.IsZero() || time.Since(status.NotifiedAt) >= updateNoticeInterval {
			r.format(r.out, "\n  有新版本 %s 可用（当前 %s）；运行 beeapi update 更新。\n", "\n  Version %s is available (current %s). Run beeapi update to install it.\n", release.TagName, r.version)
			r.markUpdateNotified(release.TagName)
		}
		return false, nil
	}

	r.showLogo()
	r.format(r.out, "\n发现新版本 %s（当前 %s）。\n", "\nVersion %s is available (current %s).\n", release.TagName, r.version)
	for {
		answer, err := r.askLocalized("现在更新？[Y/n]: ", "Update now? [Y/n]: ")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "y", "yes":
			target, targetErr := r.currentExecutableTarget()
			if targetErr != nil {
				return false, targetErr
			}
			r.format(r.out, "  当前程序: %s\n", "  Executable: %s\n", target)
			r.format(r.out, "  正在下载 %s 并验证 SHA-256…\n", "  Downloading %s and verifying SHA-256…\n", release.TagName)
			result, installErr := r.installUpdateRelease(release, target)
			if installErr != nil {
				return false, fmt.Errorf(r.text("安装更新失败: %w", "Install update: %w"), installErr)
			}
			r.saveSuccessfulUpdateCheck(result.Version)
			r.printUpdateInstalled(result, target)
			return true, nil
		case "n", "no":
			r.markUpdateNotified(release.TagName)
			return false, nil
		default:
			r.line(r.errOut, "请输入 y 或 n。", "Enter y or n.")
		}
	}
}

func (r *runner) currentExecutableTarget() (string, error) {
	executable := r.executablePath
	if executable == nil {
		executable = os.Executable
	}
	return executable()
}

func (r *runner) installUpdateRelease(release updater.Release, target string) (updater.Result, error) {
	baseCtx := r.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, updateInstallTimeout)
	defer cancel()
	if r.updateInstall != nil {
		return r.updateInstall(ctx, release, target)
	}
	client := *r.updaterClient()
	if client.OnDownload == nil {
		client.OnDownload = r.updateDownloadReporter()
	}
	return client.Install(ctx, release, target)
}

func (r *runner) updateDownloadReporter() func(updater.DownloadEvent) {
	interactive := r.interactiveTerminal()
	progressLine := false
	lastPercent := -1
	finishProgressLine := func() {
		if progressLine {
			fmt.Fprintln(r.out)
			progressLine = false
		}
	}
	return func(event updater.DownloadEvent) {
		switch event.Status {
		case updater.DownloadStarted:
			finishProgressLine()
			lastPercent = -1
			r.format(r.out, "  下载来源: %s\n", "  Download source: %s\n", event.Source)
		case updater.DownloadProgress:
			if !interactive {
				return
			}
			if event.Total > 0 {
				percent := int(event.Downloaded * 100 / event.Total)
				if percent > 100 {
					percent = 100
				}
				if percent == lastPercent {
					return
				}
				lastPercent = percent
				fmt.Fprintf(r.out, "\r  %s %3d%% · %s / %s", r.text("下载进度", "Download progress"), percent,
					formatDownloadSize(event.Downloaded), formatDownloadSize(event.Total))
			} else {
				fmt.Fprintf(r.out, "\r  %s %s", r.text("下载进度", "Download progress"), formatDownloadSize(event.Downloaded))
			}
			progressLine = true
		case updater.DownloadFailed:
			finishProgressLine()
			if event.WillRetry {
				r.format(r.errOut, "  ↷ %s 下载失败：%s；正在尝试下一来源…\n", "  ↷ %s failed: %s; trying the next source…\n", event.Source, event.Error)
			} else {
				r.format(r.errOut, "  × %s 下载失败：%s\n", "  × %s failed: %s\n", event.Source, event.Error)
			}
		case updater.DownloadVerified:
			finishProgressLine()
			r.format(r.out, "  ✓ %s 下载完成并通过 SHA-256 校验。\n", "  ✓ %s downloaded and passed SHA-256 verification.\n", event.Source)
		}
	}
}

func formatDownloadSize(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	const (
		kib = 1 << 10
		mib = 1 << 20
	)
	if bytes >= mib {
		return fmt.Sprintf("%.1f MiB", float64(bytes)/mib)
	}
	if bytes >= kib {
		return fmt.Sprintf("%.0f KiB", float64(bytes)/kib)
	}
	return fmt.Sprintf("%d B", bytes)
}

func (r *runner) printUpdateInstalled(result updater.Result, target string) {
	if result.Scheduled {
		r.format(r.out, "  ✓ %s 已验证，后台将持续重试替换：%s\n", "  ✓ %s verified; background replacement will keep retrying: %s\n", result.Version, target)
		r.line(r.out, "  CLI 现在将退出。请等待约 3 秒再运行 beeapi --version；无需关闭 PowerShell。", "  The CLI will exit now. Wait about 3 seconds before running beeapi --version; PowerShell can stay open.")
		return
	}
	r.format(r.out, "  ✓ 已更新到 %s；请重新运行 beeapi。\n", "  ✓ Updated to %s. Run beeapi again.\n", result.Version)
}

func versionCanCheck(version string) bool {
	return updater.ValidVersion(strings.TrimSpace(version))
}

func (r *runner) updateCLI(args []string) error {
	checkOnly := false
	for _, arg := range args {
		switch arg {
		case "--check", "-n":
			checkOnly = true
		case "--help", "-h":
			r.line(r.out, "用法: beeapi update [--check]", "Usage: beeapi update [--check]")
			return nil
		default:
			return fmt.Errorf(r.text("未知更新选项 %q", "Unknown update option %q"), arg)
		}
	}
	r.showLogo()
	r.format(r.out, "\n检查 BeeAPI CLI 更新 · 当前版本 %s\n", "\nCheck for BeeAPI CLI updates · Current version %s\n", r.version)
	baseCtx := r.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, 20*time.Second)
	release, err := r.updaterClient().Latest(ctx)
	cancel()
	if err != nil {
		return fmt.Errorf(r.text("检查更新失败: %w", "Check for updates: %w"), err)
	}
	if versionCanCheck(r.version) && !updater.IsNewer(release.TagName, r.version) {
		r.format(r.out, "  ✓ 已是最新版本 %s。\n", "  ✓ Already up to date at %s.\n", r.version)
		r.saveSuccessfulUpdateCheck(release.TagName)
		return nil
	}
	if checkOnly {
		r.format(r.out, "  可更新到 %s；运行 beeapi update 安装。\n", "  Version %s is available; run beeapi update to install it.\n", release.TagName)
		r.saveSuccessfulUpdateCheck(release.TagName)
		return nil
	}
	target, err := r.currentExecutableTarget()
	if err != nil {
		return err
	}
	r.format(r.out, "  当前程序: %s\n", "  Executable: %s\n", target)
	r.format(r.out, "  正在下载 %s 并验证 SHA-256…\n", "  Downloading %s and verifying SHA-256…\n", release.TagName)
	result, err := r.installUpdateRelease(release, target)
	if err != nil {
		return fmt.Errorf(r.text("安装更新失败: %w", "Install update: %w"), err)
	}
	r.saveSuccessfulUpdateCheck(result.Version)
	r.printUpdateInstalled(result, target)
	return nil
}

func (r *runner) saveSuccessfulUpdateCheck(version string) {
	if r.store == nil {
		return
	}
	status := state.UpdateStatus{CheckedAt: time.Now().UTC(), LatestVersion: strings.TrimSpace(version)}
	_ = r.store.SaveUpdateStatus(status)
}
