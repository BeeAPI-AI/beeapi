package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const keyringService = "com.getbeeapi.cli"

const CurrentSchemaVersion = 4

const CurrentPendingSetupVersion = 1

type Credential struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Prefix       string `json:"prefix,omitempty"`
	SourcePrefix string `json:"source_prefix,omitempty"`
	Backend      string `json:"backend"`
}

// Profile is a named BeeAPI configuration plan. It contains only fields owned
// by GetBeeAPI; native tool configuration files remain the source of truth for
// unrelated settings such as MCP servers, permissions, themes, and plugins.
// API keys are referenced by credential ID and are never copied into profiles.
type Profile struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Endpoint         string            `json:"endpoint"`
	DefaultModel     string            `json:"default_model,omitempty"`
	Models           map[string]string `json:"models,omitempty"`
	Agents           []string          `json:"agents,omitempty"`
	AgentCredentials map[string]string `json:"agent_credentials,omitempty"`
	CreatedAt        time.Time         `json:"created_at,omitempty"`
	UpdatedAt        time.Time         `json:"updated_at,omitempty"`
}

type Config struct {
	SchemaVersion     int               `json:"schema_version,omitempty"`
	Language          string            `json:"language,omitempty"`
	Endpoint          string            `json:"endpoint"`
	KeyName           string            `json:"key_name,omitempty"`
	DefaultModel      string            `json:"default_model,omitempty"`
	Models            map[string]string `json:"models,omitempty"`
	Agents            []string          `json:"agents,omitempty"`
	Credentials       []Credential      `json:"credentials,omitempty"`
	AgentCredentials  map[string]string `json:"agent_credentials,omitempty"`
	AgentEndpoints    map[string]string `json:"agent_endpoints,omitempty"`
	Profiles          []Profile         `json:"profiles,omitempty"`
	ActiveProfile     string            `json:"active_profile,omitempty"`
	ActiveProfiles    map[string]string `json:"active_profiles,omitempty"`
	BinaryPath        string            `json:"binary_path,omitempty"`
	CredentialBackend string            `json:"credential_backend,omitempty"`
	InitializedAt     time.Time         `json:"initialized_at,omitempty"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// PendingSetup is a resumable checkpoint written immediately after BeeAPI
// credentials have been delivered and stored securely. It deliberately holds
// only credential references; API Key plaintext remains in the OS keyring (or
// the protected credential files used as a fallback).
type PendingSetup struct {
	SchemaVersion         int          `json:"schema_version"`
	Mode                  string       `json:"mode"`
	Endpoint              string       `json:"endpoint"`
	Credentials           []Credential `json:"credentials"`
	OAuthExportID         string       `json:"oauth_export_id,omitempty"`
	OAuthExportRetryUntil time.Time    `json:"oauth_export_retry_until,omitempty"`
	CreatedAt             time.Time    `json:"created_at"`
	UpdatedAt             time.Time    `json:"updated_at"`
	LastError             string       `json:"last_error,omitempty"`
}

// Initialized deliberately accepts pre-schema config files produced by
// GetBeeAPI v0.1.0. Endpoint + credential backend were only written after the
// old setup completed, so they are a safe migration signal for returning users.
func (c Config) Initialized() bool {
	if strings.TrimSpace(c.Endpoint) == "" {
		return false
	}
	for _, credential := range c.Credentials {
		if strings.TrimSpace(credential.ID) != "" && strings.TrimSpace(credential.Backend) != "" {
			return true
		}
	}
	return strings.TrimSpace(c.CredentialBackend) != ""
}

type Store struct {
	Dir string
}

func Open() (*Store, error) {
	base := strings.TrimSpace(os.Getenv("GETBEE_HOME"))
	if base == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("查找配置目录: %w", err)
		}
		base = filepath.Join(configDir, "getbeeapi")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, fmt.Errorf("创建配置目录: %w", err)
	}
	return &Store{Dir: base}, nil
}

func (s *Store) ConfigPath() string { return filepath.Join(s.Dir, "config.json") }

func (s *Store) PendingSetupPath() string { return filepath.Join(s.Dir, "pending-setup.json") }

func (s *Store) LoadConfig() (Config, error) {
	var cfg Config
	b, err := os.ReadFile(s.ConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("读取本地配置: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("解析本地配置: %w", err)
	}
	return cfg, nil
}

func (s *Store) SaveConfig(cfg Config) error {
	if cfg.SchemaVersion < CurrentSchemaVersion {
		cfg.SchemaVersion = CurrentSchemaVersion
	}
	if cfg.Initialized() && cfg.InitializedAt.IsZero() {
		cfg.InitializedAt = time.Now().UTC()
	}
	cfg.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := AtomicWrite(s.ConfigPath(), b, 0o600); err != nil {
		return err
	}
	return os.Chmod(s.ConfigPath(), 0o600)
}

func (s *Store) LoadPendingSetup() (PendingSetup, error) {
	var pending PendingSetup
	b, err := os.ReadFile(s.PendingSetupPath())
	if errors.Is(err, os.ErrNotExist) {
		return pending, nil
	}
	if err != nil {
		return pending, fmt.Errorf("读取未完成设置: %w", err)
	}
	if err := json.Unmarshal(b, &pending); err != nil {
		return pending, fmt.Errorf("解析未完成设置: %w", err)
	}
	return pending, nil
}

func (s *Store) SavePendingSetup(pending PendingSetup) error {
	if strings.TrimSpace(pending.Mode) == "" {
		return errors.New("未完成设置缺少模式")
	}
	if strings.TrimSpace(pending.Endpoint) == "" {
		return errors.New("未完成设置缺少 BeeAPI 入口")
	}
	if len(pending.Credentials) == 0 {
		return errors.New("未完成设置缺少凭据")
	}
	for _, credential := range pending.Credentials {
		if strings.TrimSpace(credential.ID) == "" || strings.TrimSpace(credential.Backend) == "" {
			return errors.New("未完成设置包含无效凭据引用")
		}
	}
	pending.SchemaVersion = CurrentPendingSetupVersion
	now := time.Now().UTC()
	if pending.CreatedAt.IsZero() {
		pending.CreatedAt = now
	}
	pending.UpdatedAt = now
	b, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := AtomicWrite(s.PendingSetupPath(), b, 0o600); err != nil {
		return err
	}
	return os.Chmod(s.PendingSetupPath(), 0o600)
}

func (s *Store) ClearPendingSetup() error {
	err := os.Remove(s.PendingSetupPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) SaveCredential(secret string) (string, error) {
	return s.SaveNamedCredential("default", secret)
}

// SaveNamedCredential stores one secret in an OS keyring when available, or
// in a private per-credential file. The opaque credential ID is never used as
// a path or keyring account directly.
func (s *Store) SaveNamedCredential(id, secret string) (string, error) {
	id = strings.TrimSpace(id)
	secret = strings.TrimSpace(secret)
	if id == "" {
		return "", errors.New("凭据 ID 不能为空")
	}
	if secret == "" {
		return "", errors.New("API Key 不能为空")
	}
	account := credentialAccount(id)
	if os.Getenv("GETBEE_DISABLE_KEYRING") != "1" {
		switch runtime.GOOS {
		case "linux":
			if path, err := exec.LookPath("secret-tool"); err == nil {
				cmd := exec.Command(path, "store", "--label=GetBeeAPI CLI", "service", keyringService, "account", account)
				cmd.Stdin = strings.NewReader(secret)
				if out, err := cmd.CombinedOutput(); err == nil {
					_ = os.Remove(s.credentialPath(id))
					return "secret-service", nil
				} else if len(out) > 0 {
					_ = out // fall through to a protected file
				}
			}
		case "darwin":
			if path, err := exec.LookPath("security"); err == nil {
				cmd := exec.Command(path, "add-generic-password", "-a", account, "-s", keyringService, "-w", secret, "-U")
				if err := cmd.Run(); err == nil {
					_ = os.Remove(s.credentialPath(id))
					return "keychain", nil
				}
			}
		case "windows":
			if err := s.saveWindowsDPAPI(id, secret); err == nil {
				return "dpapi", nil
			}
		}
	}
	path := s.credentialPath(id)
	if err := AtomicWrite(path, []byte(secret+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("保存凭据: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("保护凭据权限: %w", err)
	}
	return "protected-file", nil
}

func (s *Store) LoadCredential(backend string) (string, error) {
	return s.LoadNamedCredential(backend, "default")
}

func (s *Store) LoadNamedCredential(backend, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("凭据 ID 不能为空")
	}
	account := credentialAccount(id)
	switch backend {
	case "secret-service":
		path, err := exec.LookPath("secret-tool")
		if err == nil {
			out, runErr := exec.Command(path, "lookup", "service", keyringService, "account", account).Output()
			if runErr == nil && strings.TrimSpace(string(out)) != "" {
				return strings.TrimSpace(string(out)), nil
			}
		}
	case "keychain":
		path, err := exec.LookPath("security")
		if err == nil {
			out, runErr := exec.Command(path, "find-generic-password", "-a", account, "-s", keyringService, "-w").Output()
			if runErr == nil && strings.TrimSpace(string(out)) != "" {
				return strings.TrimSpace(string(out)), nil
			}
		}
	case "dpapi":
		if secret, err := s.loadWindowsDPAPI(id); err == nil && secret != "" {
			return secret, nil
		}
	}
	b, err := os.ReadFile(s.credentialPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("尚未登录，请先运行 beeapi setup")
		}
		return "", err
	}
	secret := strings.TrimSpace(string(b))
	if secret == "" {
		return "", errors.New("本地 API Key 为空，请重新登录")
	}
	return secret, nil
}

// DeleteNamedCredential removes one secret from its recorded backend and from
// all local fallback locations. It is used when an OAuth account connection is
// explicitly replaced; API Key credentials are otherwise retained because a
// saved profile may still reference them.
func (s *Store) DeleteNamedCredential(backend, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("凭据 ID 不能为空")
	}
	account := credentialAccount(id)
	switch backend {
	case "secret-service":
		if path, err := exec.LookPath("secret-tool"); err == nil {
			_ = exec.Command(path, "clear", "service", keyringService, "account", account).Run()
		}
	case "keychain":
		if path, err := exec.LookPath("security"); err == nil {
			_ = exec.Command(path, "delete-generic-password", "-a", account, "-s", keyringService).Run()
		}
	}
	paths := []string{
		s.credentialPath(id),
		strings.TrimSuffix(s.credentialPath(id), ".key") + ".dpapi",
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func credentialAccount(id string) string {
	if id == "default" {
		return "default"
	}
	digest := sha256.Sum256([]byte(id))
	return fmt.Sprintf("credential-%x", digest[:16])
}

func (s *Store) credentialPath(id string) string {
	if id == "default" {
		return filepath.Join(s.Dir, "credential")
	}
	return filepath.Join(s.Dir, "credentials", credentialAccount(id)+".key")
}

func (s *Store) saveWindowsDPAPI(id, secret string) error {
	if runtime.GOOS != "windows" {
		return errors.New("not windows")
	}
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		return err
	}
	dest := strings.TrimSuffix(s.credentialPath(id), ".key") + ".dpapi"
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	script := `$p=[Console]::In.ReadToEnd(); $s=ConvertTo-SecureString $p -AsPlainText -Force; ConvertFrom-SecureString $s | Set-Content -NoNewline -LiteralPath $args[0]`
	cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", script, dest)
	cmd.Stdin = strings.NewReader(secret)
	return cmd.Run()
}

func (s *Store) loadWindowsDPAPI(id string) (string, error) {
	if runtime.GOOS != "windows" {
		return "", errors.New("not windows")
	}
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		return "", err
	}
	source := strings.TrimSuffix(s.credentialPath(id), ".key") + ".dpapi"
	script := `$s=Get-Content -Raw -LiteralPath $args[0] | ConvertTo-SecureString; $b=[Runtime.InteropServices.Marshal]::SecureStringToBSTR($s); try {[Runtime.InteropServices.Marshal]::PtrToStringBSTR($b)} finally {[Runtime.InteropServices.Marshal]::ZeroFreeBSTR($b)}`
	out, err := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", script, source).Output()
	return strings.TrimSpace(string(out)), err
}

type BackupFile struct {
	Path       string      `json:"path"`
	Existed    bool        `json:"existed"`
	BackupName string      `json:"backup_name,omitempty"`
	Mode       os.FileMode `json:"mode,omitempty"`
}

type BackupManifest struct {
	ID        string       `json:"id"`
	CreatedAt time.Time    `json:"created_at"`
	Files     []BackupFile `json:"files"`
}

func (s *Store) CreateBackup(paths []string) (BackupManifest, error) {
	manifest := BackupManifest{ID: time.Now().UTC().Format("20060102-150405.000000000"), CreatedAt: time.Now().UTC()}
	dir := filepath.Join(s.Dir, "backups", manifest.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return manifest, err
	}
	seen := map[string]bool{}
	for index, path := range paths {
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		entry := BackupFile{Path: path}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			manifest.Files = append(manifest.Files, entry)
			continue
		}
		if err != nil {
			return manifest, fmt.Errorf("备份 %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return manifest, fmt.Errorf("拒绝备份非普通文件: %s", path)
		}
		entry.Existed = true
		entry.Mode = info.Mode().Perm()
		entry.BackupName = fmt.Sprintf("%02d.bin", index)
		if err := copyFile(path, filepath.Join(dir, entry.BackupName), 0o600); err != nil {
			return manifest, err
		}
		manifest.Files = append(manifest.Files, entry)
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, err
	}
	if err := AtomicWrite(filepath.Join(dir, "manifest.json"), append(b, '\n'), 0o600); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func (s *Store) ListBackups() ([]BackupManifest, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, "backups"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []BackupManifest
	for _, entry := range entries {
		if !entry.IsDir() || !validBackupID(entry.Name()) {
			continue
		}
		manifest, err := s.loadManifest(entry.Name())
		if err == nil {
			result = append(result, manifest)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) Rollback(id string) (BackupManifest, error) {
	if id == "latest" {
		items, err := s.ListBackups()
		if err != nil {
			return BackupManifest{}, err
		}
		if len(items) == 0 {
			return BackupManifest{}, errors.New("没有可回滚的备份")
		}
		id = items[0].ID
	}
	manifest, err := s.loadManifest(id)
	if err != nil {
		return BackupManifest{}, err
	}
	dir := filepath.Join(s.Dir, "backups", id)
	for _, file := range manifest.Files {
		if file.Existed {
			if err := copyFile(filepath.Join(dir, file.BackupName), file.Path, file.Mode); err != nil {
				return manifest, fmt.Errorf("恢复 %s: %w", file.Path, err)
			}
		} else if err := os.Remove(file.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return manifest, fmt.Errorf("移除新建文件 %s: %w", file.Path, err)
		}
	}
	return manifest, nil
}

func (s *Store) loadManifest(id string) (BackupManifest, error) {
	if !validBackupID(id) {
		return BackupManifest{}, errors.New("无效的备份编号")
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, "backups", id, "manifest.json"))
	if err != nil {
		return BackupManifest{}, err
	}
	var manifest BackupManifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func validBackupID(id string) bool {
	if id == "" || strings.Contains(id, "..") || strings.ContainsAny(id, `/\\`) {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, in); err != nil {
		return err
	}
	return AtomicWrite(dst, buf.Bytes(), mode)
}

func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".getbeeapi-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(path)
	}
	return os.Rename(tmpName, path)
}
