package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const CurrentOAuthAccountVersion = 2

// OAuthAccount contains only non-secret connection metadata. Access tokens,
// refresh tokens, and the DPoP private key are stored together as one opaque
// secret via SaveNamedCredential and referenced by TokenCredentialID.
type OAuthAccount struct {
	SchemaVersion     int       `json:"schema_version"`
	Protocol          string    `json:"protocol"`
	Issuer            string    `json:"issuer"`
	ClientID          string    `json:"client_id"`
	Subject           string    `json:"subject,omitempty"`
	Username          string    `json:"username,omitempty"`
	Email             string    `json:"email,omitempty"`
	Scope             string    `json:"scope,omitempty"`
	TokenCredentialID string    `json:"token_credential_id"`
	TokenBackend      string    `json:"token_backend"`
	ConnectedAt       time.Time `json:"connected_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (s *Store) OAuthAccountPath() string { return filepath.Join(s.Dir, "oauth-account.json") }

func (s *Store) LoadOAuthAccount() (OAuthAccount, error) {
	var account OAuthAccount
	b, err := os.ReadFile(s.OAuthAccountPath())
	if errors.Is(err, os.ErrNotExist) {
		return account, nil
	}
	if err != nil {
		return account, fmt.Errorf("读取 OAuth 账户连接: %w", err)
	}
	if err := json.Unmarshal(b, &account); err != nil {
		return account, fmt.Errorf("解析 OAuth 账户连接: %w", err)
	}
	return account, nil
}

func (s *Store) SaveOAuthAccount(account OAuthAccount) error {
	if strings.TrimSpace(account.Protocol) == "" || strings.TrimSpace(account.Issuer) == "" || strings.TrimSpace(account.ClientID) == "" {
		return errors.New("OAuth 账户连接缺少协议、issuer 或 client_id")
	}
	if strings.TrimSpace(account.TokenCredentialID) == "" || strings.TrimSpace(account.TokenBackend) == "" {
		return errors.New("OAuth 账户连接缺少令牌存储引用")
	}
	account.SchemaVersion = CurrentOAuthAccountVersion
	now := time.Now().UTC()
	if account.ConnectedAt.IsZero() {
		account.ConnectedAt = now
	}
	account.UpdatedAt = now
	b, err := json.MarshalIndent(account, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(s.OAuthAccountPath(), append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(s.OAuthAccountPath(), 0o600)
}

func (s *Store) ClearOAuthAccount() error {
	err := os.Remove(s.OAuthAccountPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
