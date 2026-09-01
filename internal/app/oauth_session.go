package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

const oauthTokenCredentialID = "oauth-account-v1"

type oauthTokenSecret struct {
	Issuer         string    `json:"issuer"`
	AccessToken    string    `json:"access_token"`
	RefreshToken   string    `json:"refresh_token,omitempty"`
	TokenType      string    `json:"token_type"`
	Scope          string    `json:"scope,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
	DPoPPrivateJWK string    `json:"dpop_private_jwk"`
}

func (r *runner) saveOAuthSession(metadata beeapi.OAuthMetadata, token beeapi.OAuthToken, client *beeapi.Client) (state.OAuthAccount, error) {
	if client == nil {
		return state.OAuthAccount{}, errors.New(r.text("OAuth 客户端未初始化", "OAuth client is not initialized"))
	}
	expectedIssuer, err := beeapi.OAuthIssuerForEntrance(client.BaseURL)
	if err != nil || !beeapi.IsTrustedOAuthIssuer(metadata.Issuer) || metadata.Issuer != expectedIssuer {
		return state.OAuthAccount{}, errors.New(r.text("OAuth issuer 与所选入口不匹配", "The OAuth issuer does not match the selected entrance"))
	}
	privateJWK, err := client.ExportDPoPPrivateJWK()
	if err != nil {
		return state.OAuthAccount{}, err
	}
	secret := oauthTokenSecret{
		Issuer:      metadata.Issuer,
		AccessToken: strings.TrimSpace(token.AccessToken), RefreshToken: strings.TrimSpace(token.RefreshToken),
		TokenType: strings.TrimSpace(token.TokenType), Scope: strings.TrimSpace(token.Scope),
		ExpiresAt: time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second), DPoPPrivateJWK: privateJWK,
	}
	if secret.AccessToken == "" || !strings.EqualFold(secret.TokenType, "DPoP") || secret.DPoPPrivateJWK == "" {
		return state.OAuthAccount{}, errors.New(r.text("BeeAPI OAuth 会话不完整", "The BeeAPI OAuth session is incomplete"))
	}
	b, err := json.Marshal(secret)
	if err != nil {
		return state.OAuthAccount{}, err
	}
	backend, err := r.store.SaveNamedCredential(oauthTokenCredentialID, string(b))
	if err != nil {
		return state.OAuthAccount{}, fmt.Errorf(r.text("安全保存 OAuth 会话: %w", "Store OAuth session securely: %w"), err)
	}
	account, loadErr := r.store.LoadOAuthAccount()
	if loadErr != nil {
		return state.OAuthAccount{}, loadErr
	}
	if account.Issuer != metadata.Issuer || account.ClientID != beeapi.OAuthClientID {
		account = state.OAuthAccount{}
	}
	account.Protocol = beeapi.OAuthAccountProtocol
	account.Issuer = metadata.Issuer
	account.ClientID = beeapi.OAuthClientID
	account.Scope = secret.Scope
	account.TokenCredentialID = oauthTokenCredentialID
	account.TokenBackend = backend
	if err := r.store.SaveOAuthAccount(account); err != nil {
		return state.OAuthAccount{}, err
	}
	return r.store.LoadOAuthAccount()
}

func (r *runner) updateOAuthAccountIdentity(account state.OAuthAccount, subject, username, email string) (state.OAuthAccount, error) {
	account.Subject = strings.TrimSpace(subject)
	account.Username = strings.TrimSpace(username)
	account.Email = strings.TrimSpace(email)
	if err := r.store.SaveOAuthAccount(account); err != nil {
		return account, err
	}
	return r.store.LoadOAuthAccount()
}

func (r *runner) loadOAuthSession() (state.OAuthAccount, oauthTokenSecret, error) {
	account, err := r.store.LoadOAuthAccount()
	if err != nil {
		return account, oauthTokenSecret{}, err
	}
	if strings.TrimSpace(account.TokenCredentialID) == "" || strings.TrimSpace(account.TokenBackend) == "" {
		return account, oauthTokenSecret{}, errors.New(r.text("尚未连接 BeeAPI OAuth 账户", "No BeeAPI OAuth account is connected"))
	}
	if account.SchemaVersion != state.CurrentOAuthAccountVersion || account.Protocol != beeapi.OAuthAccountProtocol || !beeapi.IsTrustedOAuthIssuer(account.Issuer) || account.ClientID != beeapi.OAuthClientID {
		return account, oauthTokenSecret{}, errors.New(r.text("本地 OAuth 账户元数据不可信，请重新连接", "The local OAuth account metadata is not trusted; reconnect"))
	}
	raw, err := r.store.LoadNamedCredential(account.TokenBackend, account.TokenCredentialID)
	if err != nil {
		return account, oauthTokenSecret{}, err
	}
	var secret oauthTokenSecret
	if err := json.Unmarshal([]byte(raw), &secret); err != nil {
		return account, secret, fmt.Errorf(r.text("解析 OAuth 会话: %w", "Decode OAuth session: %w"), err)
	}
	if strings.TrimSpace(secret.Issuer) != account.Issuer {
		return account, oauthTokenSecret{}, errors.New(r.text("本地 OAuth Token 与 issuer 不匹配，请重新连接", "The local OAuth token does not match its issuer; reconnect"))
	}
	if !strings.HasPrefix(strings.TrimSpace(secret.AccessToken), "boa_") || !strings.EqualFold(strings.TrimSpace(secret.TokenType), "DPoP") || strings.TrimSpace(secret.DPoPPrivateJWK) == "" {
		return account, oauthTokenSecret{}, errors.New(r.text("本地 OAuth 会话不完整，请重新连接", "The local OAuth session is incomplete; reconnect"))
	}
	if secret.RefreshToken != "" && !strings.HasPrefix(strings.TrimSpace(secret.RefreshToken), "bor_") {
		return account, oauthTokenSecret{}, errors.New(r.text("本地 OAuth Refresh Token 无效，请重新连接", "The local OAuth Refresh Token is invalid; reconnect"))
	}
	return account, secret, nil
}

func (r *runner) oauthAccountClient(ctx context.Context) (*beeapi.Client, state.OAuthAccount, error) {
	account, secret, err := r.loadOAuthSession()
	if err != nil {
		return nil, account, err
	}
	client := beeapi.New(account.Issuer)
	if err := client.ImportDPoPPrivateJWK(secret.DPoPPrivateJWK); err != nil {
		return nil, account, fmt.Errorf(r.text("恢复 OAuth 设备密钥: %w", "Restore OAuth device key: %w"), err)
	}
	if time.Until(secret.ExpiresAt) <= time.Minute {
		if strings.TrimSpace(secret.RefreshToken) == "" {
			return nil, account, errors.New(r.text("BeeAPI OAuth 登录已过期，请重新连接", "The BeeAPI OAuth login expired; reconnect"))
		}
		metadata, metadataErr := client.OAuthMetadata(ctx)
		if metadataErr != nil {
			return nil, account, metadataErr
		}
		refreshed, refreshErr := r.refreshOAuthAccessToken(ctx, client, metadata, secret.RefreshToken)
		if refreshErr != nil {
			return nil, account, refreshErr
		}
		if strings.TrimSpace(refreshed.RefreshToken) == "" {
			return nil, account, errors.New(r.text("BeeAPI OAuth 续期未轮换 Refresh Token，请重新连接", "BeeAPI OAuth refresh did not rotate the Refresh Token; reconnect"))
		}
		if containsScope(refreshed.Scope, "api_keys:export") {
			return nil, account, errors.New(r.text("BeeAPI OAuth 续期错误地保留了 Key 导出权限，已拒绝该会话", "BeeAPI OAuth refresh incorrectly retained API Key export permission; the session was rejected"))
		}
		for _, requiredScope := range beeapi.OAuthAccountScopes {
			if requiredScope == "api_keys:export" {
				continue
			}
			if !containsScope(refreshed.Scope, requiredScope) {
				return nil, account, fmt.Errorf(r.text("BeeAPI OAuth 续期缺少必要权限 %s", "BeeAPI OAuth refresh is missing required scope %s"), requiredScope)
			}
		}
		account, err = r.saveOAuthSession(metadata, refreshed, client)
		if err != nil {
			return nil, account, err
		}
		secret.AccessToken = refreshed.AccessToken
		secret.RefreshToken = refreshed.RefreshToken
		secret.TokenType = refreshed.TokenType
		secret.Scope = refreshed.Scope
		secret.ExpiresAt = time.Now().UTC().Add(time.Duration(refreshed.ExpiresIn) * time.Second)
	}
	client.Token = secret.AccessToken
	return client, account, nil
}

func (r *runner) revokeOAuthSession(ctx context.Context, account state.OAuthAccount, secret oauthTokenSecret) error {
	if strings.TrimSpace(account.Issuer) == "" || strings.TrimSpace(secret.DPoPPrivateJWK) == "" {
		return nil
	}
	client := beeapi.New(account.Issuer)
	if err := client.ImportDPoPPrivateJWK(secret.DPoPPrivateJWK); err != nil {
		return err
	}
	metadata, err := client.OAuthMetadata(ctx)
	if err != nil {
		return err
	}
	token, hint := strings.TrimSpace(secret.RefreshToken), "refresh_token"
	if token == "" {
		token, hint = strings.TrimSpace(secret.AccessToken), "access_token"
	}
	if token == "" {
		return nil
	}
	return client.RevokeOAuthToken(ctx, metadata, token, hint)
}

func (r *runner) clearOAuthAccountForCompatibility() {
	if r.store == nil {
		return
	}
	metadata, metadataErr := r.store.LoadOAuthAccount()
	if metadataErr != nil || strings.TrimSpace(metadata.TokenCredentialID) == "" {
		return
	}
	account, secret, err := r.loadOAuthSession()
	if err != nil {
		_ = r.store.DeleteNamedCredential(metadata.TokenBackend, metadata.TokenCredentialID)
		_ = r.store.ClearOAuthAccount()
		r.line(r.out, "  已清理之前不完整的 OAuth 账户连接，当前改用 API Key 兼容模式。", "  Removed an incomplete previous OAuth account connection; using API Key compatibility mode.")
		return
	}
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	revokeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_ = r.revokeOAuthSession(revokeCtx, account, secret)
	cancel()
	_ = r.store.DeleteNamedCredential(account.TokenBackend, account.TokenCredentialID)
	_ = r.store.ClearOAuthAccount()
	r.line(r.out, "  已断开之前的 OAuth 账户连接，当前改用 API Key 兼容模式。", "  The previous OAuth account connection was removed; using API Key compatibility mode.")
}

func (r *runner) disconnectOAuthAccount() error {
	if r.store == nil {
		return errors.New(r.text("本地存储未初始化", "Local storage is not initialized"))
	}
	metadata, metadataErr := r.store.LoadOAuthAccount()
	if metadataErr != nil {
		return metadataErr
	}
	if strings.TrimSpace(metadata.TokenCredentialID) == "" {
		r.line(r.out, "尚未连接 BeeAPI OAuth 账户。", "No BeeAPI OAuth account is connected.")
		return nil
	}
	account, secret, err := r.loadOAuthSession()
	if err != nil {
		_ = r.store.DeleteNamedCredential(metadata.TokenBackend, metadata.TokenCredentialID)
		if clearErr := r.store.ClearOAuthAccount(); clearErr != nil {
			return clearErr
		}
		r.clearUsageCache()
		r.line(r.out, "✓ 已清理不完整的 BeeAPI OAuth 账户连接；已保存的 API Key 与工具配置保持不变。", "✓ Removed the incomplete BeeAPI OAuth account connection. Saved API Keys and tool configurations were kept.")
		return nil
	}
	baseCtx := r.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	revokeCtx, cancel := context.WithTimeout(baseCtx, 10*time.Second)
	revokeErr := r.revokeOAuthSession(revokeCtx, account, secret)
	cancel()
	if err := r.store.DeleteNamedCredential(account.TokenBackend, account.TokenCredentialID); err != nil {
		return err
	}
	if err := r.store.ClearOAuthAccount(); err != nil {
		return err
	}
	r.clearUsageCache()
	if revokeErr != nil {
		r.format(r.errOut, "OAuth 连接已从本机移除，但服务端撤销暂未完成：%v\n", "The OAuth connection was removed locally, but server revocation did not complete: %v\n", revokeErr)
		r.line(r.errOut, "可在 BeeAPI 的授权设备页面手动撤销。", "You can revoke it manually on BeeAPI's authorized-devices page.")
		return nil
	}
	r.line(r.out, "✓ 已撤销 BeeAPI OAuth 账户连接；已保存的 API Key 与工具配置保持不变。", "✓ BeeAPI OAuth account connection revoked. Saved API Keys and tool configurations were kept.")
	return nil
}
