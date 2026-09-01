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

const updateCheckInterval = 24 * time.Hour

func (r *runner) updaterClient() *updater.Client {
	if r.updateClient != nil {
		return r.updateClient
	}
	return updater.DefaultClient()
}

func (r *runner) notifyUpdateIfAvailable() {
	if !versionCanCheck(r.version) || r.store == nil {
		return
	}
	now := time.Now().UTC()
	status, err := r.store.LoadUpdateStatus()
	if err != nil {
		return
	}
	if status.CheckedAt.IsZero() || now.Sub(status.CheckedAt) >= updateCheckInterval {
		ctx, cancel := context.WithTimeout(r.ctx, 2500*time.Millisecond)
		release, checkErr := r.updaterClient().Latest(ctx)
		cancel()
		status.CheckedAt = now
		if checkErr == nil {
			status.LatestVersion = release.TagName
		}
		_ = r.store.SaveUpdateStatus(status)
	}
	if !updater.IsNewer(status.LatestVersion, r.version) {
		return
	}
	if status.NotifiedVersion == status.LatestVersion && !status.NotifiedAt.IsZero() && now.Sub(status.NotifiedAt) < updateCheckInterval {
		return
	}
	r.format(r.out, "\n  有新版本 %s 可用（当前 %s）；运行 beeapi update 更新。\n", "\n  Version %s is available (current %s). Run beeapi update to install it.\n", status.LatestVersion, r.version)
	status.NotifiedVersion = status.LatestVersion
	status.NotifiedAt = now
	_ = r.store.SaveUpdateStatus(status)
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
	ctx, cancel := context.WithTimeout(r.ctx, 20*time.Second)
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
	r.format(r.out, "  正在下载 %s 并验证 SHA-256…\n", "  Downloading %s and verifying SHA-256…\n", release.TagName)
	executable := r.executablePath
	if executable == nil {
		executable = os.Executable
	}
	target, err := executable()
	if err != nil {
		return err
	}
	installCtx, installCancel := context.WithTimeout(r.ctx, 5*time.Minute)
	result, err := r.updaterClient().Install(installCtx, release, target)
	installCancel()
	if err != nil {
		return fmt.Errorf(r.text("安装更新失败: %w", "Install update: %w"), err)
	}
	r.saveSuccessfulUpdateCheck(result.Version)
	if result.Scheduled {
		r.format(r.out, "  ✓ %s 已验证；退出后将完成替换，请重新打开终端。\n", "  ✓ %s was verified. Replacement will finish after this process exits; reopen your terminal.\n", result.Version)
	} else {
		r.format(r.out, "  ✓ 已更新到 %s；重新运行 beeapi 即可使用。\n", "  ✓ Updated to %s. Run beeapi again to use it.\n", result.Version)
	}
	return nil
}

func (r *runner) saveSuccessfulUpdateCheck(version string) {
	if r.store == nil {
		return
	}
	status := state.UpdateStatus{CheckedAt: time.Now().UTC(), LatestVersion: strings.TrimSpace(version)}
	_ = r.store.SaveUpdateStatus(status)
}
