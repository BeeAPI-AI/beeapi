package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

type oauthCallbackResult struct {
	Code string
	Err  error
}

func (r *runner) authorizeOAuthAccount(endpoint string, noOpen bool) (*beeapi.Client, beeapi.OAuthMetadata, state.OAuthAccount, error) {
	var oldAccount state.OAuthAccount
	var oldSecret oauthTokenSecret
	oldSessionErr := errors.New("no previous OAuth session")
	if r.store != nil {
		oldAccount, oldSecret, oldSessionErr = r.loadOAuthSession()
	}
	client := beeapi.New(endpoint)
	metadataCtx, cancel := context.WithTimeout(r.ctx, 8*time.Second)
	metadata, err := client.OAuthMetadata(metadataCtx)
	cancel()
	if err != nil {
		return nil, metadata, state.OAuthAccount{}, err
	}

	var token beeapi.OAuthToken
	if isHeadlessTerminal() {
		if !metadata.SupportsDeviceGrant() {
			return nil, metadata, state.OAuthAccount{}, errors.New(r.text("BeeAPI OAuth 尚未为 SSH 开启设备授权", "BeeAPI OAuth has not enabled the device grant required for SSH"))
		}
		token, err = r.authorizeOAuthDevice(client, metadata, noOpen)
	} else {
		token, err = r.authorizeOAuthDesktop(client, metadata, noOpen)
	}
	if err != nil {
		return nil, metadata, state.OAuthAccount{}, err
	}
	for _, requiredScope := range beeapi.OAuthAccountScopes {
		if !containsScope(token.Scope, requiredScope) {
			return nil, metadata, state.OAuthAccount{}, fmt.Errorf(r.text("BeeAPI OAuth 未授予必要权限 %s", "BeeAPI OAuth did not grant required scope %s"), requiredScope)
		}
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		return nil, metadata, state.OAuthAccount{}, errors.New(r.text("BeeAPI OAuth 未返回持续登录所需的 Refresh Token", "BeeAPI OAuth did not return the Refresh Token required for persistent sign-in"))
	}
	account, err := r.saveOAuthSession(metadata, token, client)
	if err != nil {
		return nil, metadata, account, err
	}
	account, err = r.updateOAuthAccountIdentity(account, "", "", "")
	if err != nil {
		return nil, metadata, account, err
	}
	client.BaseURL = strings.TrimRight(metadata.Issuer, "/")
	client.Token = token.AccessToken
	if oldSessionErr == nil {
		baseCtx := r.ctx
		if baseCtx == nil {
			baseCtx = context.Background()
		}
		revokeCtx, revokeCancel := context.WithTimeout(baseCtx, 5*time.Second)
		if revokeErr := r.revokeOAuthSession(revokeCtx, oldAccount, oldSecret); revokeErr != nil {
			r.format(r.errOut, "  提示：之前的 OAuth 连接未能自动撤销，可在 BeeAPI 授权设备页手动撤销。\n", "  Note: the previous OAuth connection could not be revoked automatically; you can revoke it on BeeAPI's authorized-devices page.\n")
		}
		revokeCancel()
	}
	r.line(r.out, "  ✓ BeeAPI OAuth 账户已连接并安全保存。", "  ✓ The BeeAPI OAuth account is connected and stored securely.")
	return client, metadata, account, nil
}

func (r *runner) authorizeOAuthDesktop(client *beeapi.Client, metadata beeapi.OAuthMetadata, noOpen bool) (beeapi.OAuthToken, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return beeapi.OAuthToken{}, fmt.Errorf(r.text("启动本机 OAuth 回调: %w", "Start local OAuth callback: %w"), err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/oauth/callback", port)
	stateValue, err := randomBase64URL(32)
	if err != nil {
		return beeapi.OAuthToken{}, err
	}
	verifier, err := randomBase64URL(48)
	if err != nil {
		return beeapi.OAuthToken{}, err
	}
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	authorizeURL, err := beeapi.BuildAuthorizationURL(metadata, redirectURI, stateValue, challenge, beeapi.OAuthAccountScopes)
	if err != nil {
		return beeapi.OAuthToken{}, err
	}

	resultCh := make(chan oauthCallbackResult, 1)
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler:           oauthCallbackHandler(stateValue, metadata.Issuer, resultCh),
	}
	go func() {
		_ = server.Serve(listener)
	}()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = server.Shutdown(shutdownCtx)
		shutdownCancel()
	}()

	r.printOAuthPermissions()
	r.format(r.out, "  OAuth 授权网址: %s\n", "  OAuth authorization URL: %s\n", authorizeURL)
	if noOpen {
		r.line(r.out, "  已关闭自动打开；请在当前电脑的浏览器打开以上网址。", "  Automatic browser launch is disabled; open the URL in a browser on this computer.")
	} else {
		opener := r.openBrowser
		if opener == nil {
			opener = openURL
		}
		if openErr := opener(authorizeURL); openErr != nil {
			r.format(r.errOut, "  自动打开浏览器失败：%v\n", "  Could not open a browser automatically: %v\n", openErr)
			r.line(r.out, "  请复制以上 OAuth 授权网址继续。", "  Copy the OAuth authorization URL above to continue.")
		} else {
			r.line(r.out, "  ✓ 已尝试打开浏览器，正在等待授权…", "  ✓ Browser launch requested; waiting for authorization…")
		}
	}

	waitCtx, waitCancel := context.WithTimeout(r.ctx, 10*time.Minute)
	defer waitCancel()
	select {
	case <-waitCtx.Done():
		return beeapi.OAuthToken{}, errors.New(r.text("OAuth 授权等待超时，可重新运行 beeapi 生成新链接", "OAuth authorization timed out; run beeapi again to generate a new link"))
	case result := <-resultCh:
		if result.Err != nil {
			return beeapi.OAuthToken{}, result.Err
		}
		return r.exchangeOAuthAuthorizationCode(client, metadata, result.Code, redirectURI, verifier)
	}
}

func (r *runner) exchangeOAuthAuthorizationCode(client *beeapi.Client, metadata beeapi.OAuthMetadata, code, redirectURI, verifier string) (beeapi.OAuthToken, error) {
	baseCtx := r.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		exchangeCtx, cancel := context.WithTimeout(baseCtx, 15*time.Second)
		token, err := client.ExchangeAuthorizationCode(exchangeCtx, metadata, code, redirectURI, verifier)
		cancel()
		if err == nil {
			return token, nil
		}
		lastErr = err
		if attempt == 2 || baseCtx.Err() != nil || !oauthRequestRetryable(err) {
			break
		}
		if attempt == 0 {
			r.line(r.out, "  令牌响应暂时中断，正在使用同一授权码安全恢复…", "  The token response was interrupted; safely recovering with the same authorization code…")
		}
		if err := waitOAuthRetry(baseCtx, time.Duration(attempt+1)*250*time.Millisecond); err != nil {
			return beeapi.OAuthToken{}, err
		}
	}
	return beeapi.OAuthToken{}, lastErr
}

func (r *runner) refreshOAuthAccessToken(ctx context.Context, client *beeapi.Client, metadata beeapi.OAuthMetadata, refreshToken string) (beeapi.OAuthToken, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		token, err := client.RefreshOAuthToken(refreshCtx, metadata, refreshToken)
		cancel()
		if err == nil {
			return token, nil
		}
		lastErr = err
		if attempt == 2 || ctx.Err() != nil || !oauthRequestRetryable(err) {
			break
		}
		if attempt == 0 {
			r.line(r.out, "  登录续期响应暂时中断，正在安全恢复…", "  The sign-in refresh response was interrupted; safely recovering…")
		}
		if err := waitOAuthRetry(ctx, time.Duration(attempt+1)*250*time.Millisecond); err != nil {
			return beeapi.OAuthToken{}, err
		}
	}
	return beeapi.OAuthToken{}, lastErr
}

func oauthRequestRetryable(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *beeapi.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusRequestTimeout || apiErr.Status == http.StatusTooEarly || apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= 500
	}
	return !errors.Is(err, context.Canceled)
}

func waitOAuthRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func oauthCallbackHandler(expectedState, expectedIssuer string, resultCh chan<- oauthCallbackResult) http.Handler {
	var delivered sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if request.Method != http.MethodGet || request.URL.Path != "/oauth/callback" {
			http.NotFound(w, request)
			return
		}
		if request.URL.Query().Get("state") != expectedState {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			return
		}
		if request.URL.Query().Get("iss") != expectedIssuer {
			http.Error(w, "Invalid OAuth issuer", http.StatusBadRequest)
			return
		}
		if oauthError := strings.TrimSpace(request.URL.Query().Get("error")); oauthError != "" {
			delivered.Do(func() { resultCh <- oauthCallbackResult{Err: fmt.Errorf("OAuth authorization failed: %s", oauthError)} })
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, oauthCallbackFailureHTML)
			return
		}
		code := strings.TrimSpace(request.URL.Query().Get("code"))
		if code == "" {
			http.Error(w, "Missing OAuth authorization code", http.StatusBadRequest)
			return
		}
		delivered.Do(func() { resultCh <- oauthCallbackResult{Code: code} })
		_, _ = io.WriteString(w, oauthCallbackSuccessHTML)
	})
}

const oauthCallbackSuccessHTML = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>BeeAPI OAuth</title><body style="font:16px system-ui;max-width:520px;margin:12vh auto;padding:24px"><h1>网页批准已完成</h1><p>请返回终端，并等待 BeeAPI CLI 显示“账户已连接”后再关闭流程。</p><hr><p>Browser approval is complete. Return to the terminal and wait until BeeAPI CLI confirms the account is connected.</p></body></html>`

const oauthCallbackFailureHTML = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>BeeAPI OAuth</title><body style="font:16px system-ui;max-width:520px;margin:12vh auto;padding:24px"><h1>授权未完成</h1><p>请返回终端重试或选择其他登录方式。</p><hr><p>Authorization was not completed. Return to the terminal to try again.</p></body></html>`

func (r *runner) authorizeOAuthDevice(client *beeapi.Client, metadata beeapi.OAuthMetadata, noOpen bool) (beeapi.OAuthToken, error) {
	deviceCtx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	code, err := client.StartOAuthDeviceAuth(deviceCtx, metadata, beeapi.OAuthAccountScopes)
	cancel()
	if err != nil {
		return beeapi.OAuthToken{}, err
	}
	if strings.TrimSpace(code.DeviceCode) == "" || strings.TrimSpace(code.UserCode) == "" {
		return beeapi.OAuthToken{}, errors.New(r.text("BeeAPI OAuth 没有返回设备码", "BeeAPI OAuth did not return a device code"))
	}
	r.printOAuthPermissions()
	if err := validateOAuthDeviceVerificationURL(metadata.Issuer, code); err != nil {
		return beeapi.OAuthToken{}, err
	}
	if err := r.presentDeviceAuthorization(metadata.Issuer, code, noOpen); err != nil {
		return beeapi.OAuthToken{}, err
	}
	r.line(r.out, "  正在等待网页确认…", "  Waiting for browser approval…")
	return r.pollOAuthDeviceToken(client, metadata, code)
}

func validateOAuthDeviceVerificationURL(issuer string, code beeapi.DeviceCode) error {
	verificationURL, err := deviceVerificationURL(issuer, code)
	if err != nil {
		return err
	}
	issuerURL, issuerErr := url.Parse(issuer)
	verification, verificationErr := url.Parse(verificationURL)
	if issuerErr != nil || verificationErr != nil || !strings.EqualFold(issuerURL.Host, verification.Host) {
		return errors.New("BeeAPI OAuth verification URL does not use the canonical issuer")
	}
	return nil
}

func (r *runner) pollOAuthDeviceToken(client *beeapi.Client, metadata beeapi.OAuthMetadata, code beeapi.DeviceCode) (beeapi.OAuthToken, error) {
	interval := code.Interval
	if interval < 5 {
		interval = 5
	}
	expires := code.ExpiresIn
	if expires <= 0 {
		expires = 600
	}
	deadline := time.NewTimer(time.Duration(expires) * time.Second)
	defer deadline.Stop()
	consecutiveNetworkErrors := 0
	for {
		timer := time.NewTimer(time.Duration(interval) * time.Second)
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return beeapi.OAuthToken{}, r.ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return beeapi.OAuthToken{}, errors.New(r.text("OAuth 设备授权已过期，可重新生成授权链接", "OAuth device authorization expired; generate a new authorization link"))
		case <-timer.C:
			pollCtx, cancel := context.WithTimeout(r.ctx, 12*time.Second)
			token, err := client.PollOAuthDeviceToken(pollCtx, metadata, code.DeviceCode)
			cancel()
			if err == nil {
				return token, nil
			}
			var apiErr *beeapi.APIError
			if errors.As(err, &apiErr) {
				switch apiErr.Reason {
				case "authorization_pending":
					consecutiveNetworkErrors = 0
					continue
				case "slow_down":
					interval += 5
					continue
				case "access_denied", "expired_token", "invalid_grant":
					return beeapi.OAuthToken{}, err
				}
				if apiErr.Status < 500 {
					return beeapi.OAuthToken{}, err
				}
			}
			consecutiveNetworkErrors++
			if consecutiveNetworkErrors == 1 {
				r.line(r.out, "  网络暂时中断，保留本次授权并继续等待…", "  The network was interrupted; keeping this authorization and continuing to wait…")
			}
			continue
		}
	}
}

func (r *runner) printOAuthPermissions() {
	r.line(r.out, "  本次 OAuth 将申请：", "  This OAuth request asks to:")
	r.line(r.out, "    ✓ 查看账户基本信息和当前余额", "    ✓ Read basic account information and current balance")
	r.line(r.out, "    ✓ 查看 API Key 名称、状态与模型能力", "    ✓ Read API Key names, status, and model capabilities")
	r.line(r.out, "    ✓ 一次性导出随后明确选择的 API Key", "    ✓ Export only the API Keys explicitly selected next")
	r.line(r.out, "    × 不会创建、删除 API Key 或调用模型", "    × Will not create/delete API Keys or invoke models")
}

func randomBase64URL(size int) (string, error) {
	if size < 16 {
		return "", errors.New("random value size is too small")
	}
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func containsScope(raw, want string) bool {
	for _, scope := range strings.Fields(raw) {
		if scope == want {
			return true
		}
	}
	return false
}
