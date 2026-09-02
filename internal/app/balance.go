package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

type usageLookupFunc func(context.Context, string, string) (beeapi.Usage, error)

type usageCacheEntry struct {
	usage     beeapi.Usage
	err       error
	expiresAt time.Time
}

type usageCacheStore struct {
	mu      sync.Mutex
	entries map[string]usageCacheEntry
}

func (c *usageCacheStore) load(key string, now time.Time) (usageCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	return entry, ok && now.Before(entry.expiresAt)
}

func (c *usageCacheStore) save(key string, entry usageCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]usageCacheEntry{}
	}
	c.entries[key] = entry
}

func (c *usageCacheStore) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = nil
}

func (r *runner) clearUsageCache() {
	r.usageCache.clear()
}

func (r *runner) queryUsage(endpoint, credentialID, secret string, force bool) (beeapi.Usage, error) {
	endpoint = beeapi.NormalizeBaseURL(endpoint)
	if endpoint == "" {
		return beeapi.Usage{}, errors.New("BeeAPI 入口无效")
	}
	if strings.TrimSpace(secret) == "" {
		return beeapi.Usage{}, errors.New("API Key 为空")
	}
	cacheKey := endpoint + "\x00" + credentialID
	now := time.Now()
	if !force {
		if entry, ok := r.usageCache.load(cacheKey, now); ok {
			return entry.usage, entry.err
		}
	}
	baseCtx := r.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
	defer cancel()
	lookup := r.usageLookup
	if lookup == nil {
		lookup = func(ctx context.Context, endpoint, apiKey string) (beeapi.Usage, error) {
			return beeapi.New(endpoint).Usage(ctx, apiKey)
		}
	}
	usage, err := lookup(ctx, endpoint, secret)
	ttl := 30 * time.Second
	if err != nil {
		ttl = 5 * time.Second
		err = errors.New(sanitizeUsageError(err.Error(), secret))
	}
	r.usageCache.save(cacheKey, usageCacheEntry{usage: usage, err: err, expiresAt: now.Add(ttl)})
	return usage, err
}

func sanitizeUsageError(message, secret string) string {
	message = strings.TrimSpace(message)
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[已隐藏]")
	}
	if message == "" {
		return "无法读取余额"
	}
	return message
}

func endpointForCredential(cfg state.Config, credentialID string) string {
	for _, agent := range cfg.Agents {
		if cfg.AgentCredentials[agent] == credentialID {
			return endpointForAgent(cfg, agent)
		}
	}
	return strings.TrimRight(cfg.Endpoint, "/")
}

func activeCredentialID(cfg state.Config) string {
	for _, agent := range cfg.Agents {
		if id := strings.TrimSpace(cfg.AgentCredentials[agent]); id != "" {
			return id
		}
	}
	if len(cfg.Credentials) > 0 {
		return cfg.Credentials[0].ID
	}
	return "default"
}

func formatBalance(value float64, currency string) string {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" || currency == "USD" {
		return fmt.Sprintf("$%.2f", value)
	}
	return fmt.Sprintf("%.2f %s", value, currency)
}

func (r *runner) homeBalanceLabel(cfg state.Config) string {
	if balance, err := r.queryOAuthAccountBalance(false); err == nil {
		return formatBalance(balance.Current(), balance.CurrencyCode())
	}
	credentials, err := r.loadCredentialMaterials(cfg, false)
	if err != nil || len(credentials) == 0 {
		return r.text("暂不可用", "Unavailable")
	}
	id := activeCredentialID(cfg)
	credential, ok := credentialForID(credentials, id)
	if !ok {
		credential = credentials[0]
	}
	usage, err := r.queryUsage(endpointForCredential(cfg, credential.ID), credential.ID, credential.Secret, false)
	if err != nil {
		return r.text("暂不可用", "Unavailable")
	}
	label := formatBalance(usage.Balance, usage.Currency)
	if !usage.IsActive || !usage.IsValid {
		label += r.text(" · 当前 Key 不可用", " · Current Key unavailable")
	}
	return label
}

func (r *runner) queryOAuthAccountBalance(force bool) (beeapi.OAuthBalance, error) {
	account, err := r.store.LoadOAuthAccount()
	if err != nil {
		return beeapi.OAuthBalance{}, err
	}
	if strings.TrimSpace(account.TokenCredentialID) == "" || strings.TrimSpace(account.Issuer) == "" {
		return beeapi.OAuthBalance{}, errors.New("OAuth account is not connected")
	}
	cacheKey := "oauth-account\x00" + account.Issuer
	now := time.Now()
	if !force {
		if entry, ok := r.usageCache.load(cacheKey, now); ok {
			return beeapi.OAuthBalance{Available: entry.usage.Balance, Currency: entry.usage.Currency}, entry.err
		}
	}
	baseCtx := r.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, 8*time.Second)
	defer cancel()
	client, _, err := r.oauthAccountClient(ctx)
	if err == nil {
		balanceCtx, balanceCancel := context.WithTimeout(ctx, 5*time.Second)
		balance, balanceErr := client.OAuthAccountBalance(balanceCtx)
		balanceCancel()
		if balanceErr == nil {
			r.usageCache.save(cacheKey, usageCacheEntry{
				usage:     beeapi.Usage{Balance: balance.Current(), Currency: balance.CurrencyCode()},
				expiresAt: now.Add(30 * time.Second),
			})
			return balance, nil
		}
		err = balanceErr
	}
	r.usageCache.save(cacheKey, usageCacheEntry{err: err, expiresAt: now.Add(5 * time.Second)})
	return beeapi.OAuthBalance{}, err
}

type credentialUsageResult struct {
	credential credentialMaterial
	usage      beeapi.Usage
	err        error
}

func (r *runner) balanceMenu() error {
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	credentials, err := r.loadCredentialMaterials(cfg, false)
	if err != nil {
		return err
	}
	r.line(r.out, "\n密钥与余额", "\nAPI Keys and balance")
	oauthBalance, oauthBalanceErr := r.queryOAuthAccountBalance(true)
	if oauthBalanceErr == nil {
		r.format(r.out, "  账户余额  %s · OAuth 账户\n", "  Account balance  %s · OAuth account\n", formatBalance(oauthBalance.Current(), oauthBalance.CurrencyCode()))
	}
	results := make([]credentialUsageResult, len(credentials))
	var wait sync.WaitGroup
	for index, credential := range credentials {
		wait.Add(1)
		go func() {
			defer wait.Done()
			usage, lookupErr := r.queryUsage(endpointForCredential(cfg, credential.ID), credential.ID, credential.Secret, true)
			results[index] = credentialUsageResult{credential: credential, usage: usage, err: lookupErr}
		}()
	}
	wait.Wait()

	if oauthBalanceErr != nil {
		for _, result := range results {
			if result.err == nil {
				r.format(r.out, "  账户余额  %s\n", "  Account balance  %s\n", formatBalance(result.usage.Balance, result.usage.Currency))
				break
			}
		}
	}
	for index, result := range results {
		prefix := strings.TrimSpace(result.credential.Prefix)
		if prefix == "" {
			prefix = r.text("前缀未知", "Unknown prefix")
		}
		if result.err != nil {
			fmt.Fprintf(r.out, "  %d. ? %s · %s\n", index+1, result.credential.Name, prefix)
			r.format(r.out, "     暂时无法读取：%s\n", "     Temporarily unavailable: %s\n", r.localizedErrorMessage(result.err))
			continue
		}
		marker, status := "✓", r.text("可用", "Available")
		if !result.usage.IsActive || !result.usage.IsValid {
			marker, status = "×", r.text("不可用", "Unavailable")
			if result.usage.InvalidMessage != "" {
				status += " · " + result.usage.InvalidMessage
			}
		}
		name := result.credential.Name
		if strings.TrimSpace(result.usage.KeyName) != "" {
			name = result.usage.KeyName
		}
		fmt.Fprintf(r.out, "  %d. %s %s · %s\n", index+1, marker, name, prefix)
		fmt.Fprintf(r.out, "     %s", status)
		if result.usage.PlanName != "" {
			fmt.Fprintf(r.out, " · %s", result.usage.PlanName)
		}
		if result.usage.ExpiresAt != "" {
			r.format(r.out, " · 到期 %s", " · Expires %s", formatUsageExpiry(result.usage.ExpiresAt))
		}
		fmt.Fprintln(r.out)
	}
	answer, askErr := r.askLocalized("\n回车返回（输入 r 刷新）: ", "\nPress Enter to go back (or r to refresh): ")
	if askErr != nil && !errors.Is(askErr, io.EOF) {
		return askErr
	}
	if strings.EqualFold(strings.TrimSpace(answer), "r") {
		r.redrawInteractiveScreen()
		return r.balanceMenu()
	}
	return nil
}

func formatUsageExpiry(value string) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return parsed.Local().Format("2006-01-02")
}
