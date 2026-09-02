package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/BeeAPI-AI/beeapi/internal/state"
)

const (
	pendingModeSetup     = "setup"
	pendingModeReconnect = "reconnect"
	pendingModeProfile   = "profile"
)

func (r *runner) pendingSetupForMode(mode string, automatic bool) (state.PendingSetup, bool, error) {
	pending, err := r.store.LoadPendingSetup()
	if err != nil {
		return pending, false, err
	}
	if strings.TrimSpace(pending.Mode) != mode || strings.TrimSpace(pending.Endpoint) == "" || len(pending.Credentials) == 0 {
		return state.PendingSetup{}, false, nil
	}
	if automatic {
		return pending, true, nil
	}

	r.format(r.out, "\n检测到上次授权已安全保存（%d 个 API Key），可从配置步骤继续。\n", "\nA previous authorization was saved securely (%d API Key(s)); setup can resume.\n", len(pending.Credentials))
	if strings.TrimSpace(pending.LastError) != "" {
		r.format(r.out, "  上次中断: %s\n", "  Previous interruption: %s\n", pending.LastError)
	}
	answer, askErr := r.askLocalized("  回车继续；输入 r 重新授权: ", "  Press Enter to resume, or enter r to authorize again: ")
	if askErr != nil && !errors.Is(askErr, io.EOF) {
		return pending, false, askErr
	}
	if strings.EqualFold(strings.TrimSpace(answer), "r") {
		if err := r.store.ClearPendingSetup(); err != nil {
			return pending, false, err
		}
		return state.PendingSetup{}, false, nil
	}
	return pending, true, nil
}

func (r *runner) checkpointCredentialMaterials(mode, endpoint string, credentials []credentialMaterial) ([]state.Credential, error) {
	return r.checkpointCredentialMaterialsWithExport(mode, endpoint, credentials, "", time.Time{})
}

func (r *runner) checkpointCredentialMaterialsWithExport(mode, endpoint string, credentials []credentialMaterial, exportID string, retryUntil time.Time) ([]state.Credential, error) {
	stored, err := r.saveCredentialMaterials(credentials)
	if err != nil {
		return nil, err
	}
	pending := state.PendingSetup{
		Mode: mode, Endpoint: endpoint, Credentials: stored,
		OAuthExportID: strings.TrimSpace(exportID), OAuthExportRetryUntil: retryUntil,
	}
	if err := r.store.SavePendingSetup(pending); err != nil {
		return nil, fmt.Errorf(r.text("保存设置续接点: %w", "Save setup checkpoint: %w"), err)
	}
	r.line(r.out, "  ✓ API Key 已安全保存；后续步骤失败时可直接继续，无需重新授权。", "  ✓ API Keys were stored securely. If a later step fails, setup can resume without another authorization.")
	return stored, nil
}

func (r *runner) loadPendingCredentialMaterials(pending state.PendingSetup) ([]credentialMaterial, error) {
	credentials := make([]credentialMaterial, 0, len(pending.Credentials))
	seen := make(map[string]bool, len(pending.Credentials))
	for _, stored := range pending.Credentials {
		if strings.TrimSpace(stored.ID) == "" || strings.TrimSpace(stored.Backend) == "" || seen[stored.ID] {
			return nil, errors.New(r.text("未完成设置的本地凭据索引已损坏，请重新授权", "The saved setup checkpoint is damaged; authorize again"))
		}
		seen[stored.ID] = true
		secret, err := r.store.LoadNamedCredential(stored.Backend, stored.ID)
		if err != nil {
			return nil, fmt.Errorf(r.text("读取已保存配置 %q: %w", "Read saved configuration %q: %w"), stored.Name, err)
		}
		credentials = append(credentials, credentialMaterial{
			ID: stored.ID, Name: stored.Name, Prefix: stored.Prefix,
			SourcePrefix: stored.SourcePrefix, Secret: secret,
		})
	}
	return credentials, nil
}

// restorePendingCredentialMaterials verifies that every locally checkpointed
// secret is readable before acknowledging (or abandoning) the short-lived
// server export. Once the export is acknowledged, its ciphertext may no longer
// be recoverable, so this ordering is part of the recovery guarantee.
func (r *runner) restorePendingCredentialMaterials(pending state.PendingSetup) ([]credentialMaterial, error) {
	credentials, err := r.loadPendingCredentialMaterials(pending)
	if err != nil {
		return nil, err
	}
	if err := r.resumePendingOAuthExport(pending); err != nil {
		return nil, err
	}
	return credentials, nil
}

func (r *runner) updatePendingSetup(mode, endpoint string, stored []state.Credential, stepErr error) {
	if len(stored) == 0 {
		return
	}
	pending, err := r.store.LoadPendingSetup()
	if err != nil || pending.Mode != mode {
		pending = state.PendingSetup{Mode: mode, Endpoint: endpoint, Credentials: stored}
	}
	pending.Endpoint = endpoint
	pending.Credentials = stored
	if stepErr == nil {
		pending.LastError = ""
	} else {
		pending.LastError = r.localizedErrorMessage(stepErr)
		if len(pending.LastError) > 500 {
			pending.LastError = pending.LastError[:500]
		}
	}
	_ = r.store.SavePendingSetup(pending)
}

func (r *runner) clearPendingOAuthExport(mode, exportID string) error {
	pending, err := r.store.LoadPendingSetup()
	if err != nil {
		return err
	}
	exportID = strings.TrimSpace(exportID)
	if pending.Mode != mode || exportID == "" || strings.TrimSpace(pending.OAuthExportID) != exportID {
		return nil
	}
	pending.OAuthExportID = ""
	pending.OAuthExportRetryUntil = time.Time{}
	return r.store.SavePendingSetup(pending)
}

func (r *runner) hasSavedOAuthConnection() bool {
	if r.store == nil {
		return false
	}
	account, err := r.store.LoadOAuthAccount()
	return err == nil && strings.TrimSpace(account.TokenCredentialID) != "" && strings.TrimSpace(account.TokenBackend) != ""
}

func (r *runner) setupWithRecovery(args []string) error {
	if containsArgument(args, "--yes") {
		return r.setup(args)
	}
	for {
		err := r.setup(args)
		if err == nil {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		pending, loadErr := r.store.LoadPendingSetup()
		hasCredentialCheckpoint := loadErr == nil && pending.Mode == pendingModeSetup && len(pending.Credentials) > 0
		if !hasCredentialCheckpoint && !r.hasSavedOAuthConnection() {
			return err
		}
		r.format(r.errOut, "\n  当前步骤未完成: %v\n", "\n  The current step was not completed: %v\n", err)
		if hasCredentialCheckpoint {
			r.line(r.out, "  授权与 API Key 已保存，不需要重新登录。", "  Authorization and API Keys are saved; you do not need to sign in again.")
		} else {
			r.line(r.out, "  OAuth 账户连接已保存；重试时可继续此账户，不需要重复绑定。", "  The OAuth account connection is saved. Retry with this account without binding it again.")
		}
		r.line(r.out, "  1. 从已保存步骤重试", "  1. Retry from the saved step")
		if hasCredentialCheckpoint {
			r.line(r.out, "  2. 放弃续接并重新授权", "  2. Discard the checkpoint and authorize again")
		}
		r.line(r.out, "  0. 保存进度并退出", "  0. Save progress and exit")
		answer, askErr := r.askLocalized("  请选择 [1]: ", "  Select [1]: ")
		if askErr != nil {
			if errors.Is(askErr, io.EOF) {
				return err
			}
			return askErr
		}
		switch strings.TrimSpace(answer) {
		case "", "1":
			args = nil
			continue
		case "2", "r", "R":
			if !hasCredentialCheckpoint {
				r.line(r.out, "  当前没有已领取 Key 的续接点；重试后可在连接菜单选择重新授权。", "  No exported-Key checkpoint exists. Retry and choose browser authorization from the connection menu.")
				args = nil
				continue
			}
			if clearErr := r.store.ClearPendingSetup(); clearErr != nil {
				return clearErr
			}
			args = nil
			continue
		case "0", "q", "Q":
			r.line(r.out, "  ✓ 进度已保存；下次输入 beeapi 将继续设置。", "  ✓ Progress saved. Run beeapi next time to continue setup.")
			return nil
		default:
			r.line(r.out, "  请输入 1、2 或 0。", "  Enter 1, 2, or 0.")
		}
	}
}

func (r *runner) reconnectWithRecovery() error {
	for {
		err := r.reconnect()
		if err == nil {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		pending, loadErr := r.store.LoadPendingSetup()
		hasCredentialCheckpoint := loadErr == nil && pending.Mode == pendingModeReconnect && len(pending.Credentials) > 0
		if !hasCredentialCheckpoint && !r.hasSavedOAuthConnection() {
			return err
		}
		r.format(r.errOut, "\n  更新配置未完成: %v\n", "\n  Configuration update was not completed: %v\n", err)
		if hasCredentialCheckpoint {
			r.line(r.out, "  新凭据已保存，可安全重试或稍后继续。", "  The new credentials are saved; retry now or continue later.")
		} else {
			r.line(r.out, "  OAuth 账户连接已保存；可以继续此账户或在连接菜单重新授权。", "  The OAuth account connection is saved; continue it or authorize again from the connection menu.")
		}
		answer, askErr := r.askLocalized("  回车重试；输入 r 重新授权；输入 0 返回: ", "  Press Enter to retry, r to authorize again, or 0 to return: ")
		if askErr != nil {
			if errors.Is(askErr, io.EOF) {
				return err
			}
			return askErr
		}
		switch strings.TrimSpace(answer) {
		case "":
			continue
		case "r", "R":
			if clearErr := r.store.ClearPendingSetup(); clearErr != nil {
				return clearErr
			}
			continue
		case "0", "q", "Q":
			return nil
		default:
			r.line(r.out, "  请输入回车、r 或 0。", "  Press Enter, or enter r or 0.")
		}
	}
}
