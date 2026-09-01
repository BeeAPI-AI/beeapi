package beeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OpaqueID accepts either a JSON string or number while keeping the CLI from
// depending on BeeAPI's internal database identifier type.
type OpaqueID string

func (id *OpaqueID) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*id = OpaqueID(strings.TrimSpace(text))
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		*id = OpaqueID(number.String())
		return nil
	}
	return errors.New("invalid opaque identifier")
}

type OAuthAccountProfile struct {
	Subject         string   `json:"sub"`
	ID              OpaqueID `json:"id"`
	Username        string   `json:"username"`
	Email           string   `json:"email"`
	Avatar          string   `json:"avatar"`
	PreferredLocale string   `json:"preferred_locale"`
}

func (p OAuthAccountProfile) StableSubject() string {
	if strings.TrimSpace(p.Subject) != "" {
		return strings.TrimSpace(p.Subject)
	}
	return string(p.ID)
}

type OAuthBalance struct {
	Available float64 `json:"available"`
	Balance   float64 `json:"balance"`
	Currency  string  `json:"currency"`
	Unit      string  `json:"unit"`
}

func (b OAuthBalance) Current() float64 {
	if b.Available != 0 || b.Balance == 0 {
		return b.Available
	}
	return b.Balance
}

func (b OAuthBalance) CurrencyCode() string {
	if strings.TrimSpace(b.Currency) != "" {
		return strings.TrimSpace(b.Currency)
	}
	if strings.TrimSpace(b.Unit) != "" {
		return strings.TrimSpace(b.Unit)
	}
	return "USD"
}

type OAuthAPIKey struct {
	ID                OpaqueID   `json:"id"`
	Name              string     `json:"name"`
	KeyPrefix         string     `json:"key_prefix"`
	Status            string     `json:"status"`
	ExpiresAt         *time.Time `json:"expires_at"`
	LastUsedAt        *time.Time `json:"last_used_at"`
	RouteGroups       []string   `json:"route_groups"`
	Exportable        bool       `json:"exportable"`
	UnavailableReason string     `json:"unavailable_reason"`
	ModelCount        int        `json:"model_count,omitempty"`
}

type OAuthAPIKeyExportCredential struct {
	APIKeyID  OpaqueID `json:"api_key_id"`
	KeyName   string   `json:"key_name"`
	KeyPrefix string   `json:"key_prefix"`
	APIKey    string   `json:"api_key"`
}

type OAuthAPIKeyExportSkip struct {
	APIKeyID  OpaqueID `json:"api_key_id"`
	KeyName   string   `json:"key_name"`
	KeyPrefix string   `json:"key_prefix"`
	Reason    string   `json:"reason"`
}

type OAuthAPIKeyExport struct {
	ExportID    string                        `json:"export_id"`
	Credentials []OAuthAPIKeyExportCredential `json:"credentials"`
	Skipped     []OAuthAPIKeyExportSkip       `json:"skipped"`
	RetryUntil  time.Time                     `json:"retry_until"`
}

func (c *Client) OAuthAccount(ctx context.Context) (OAuthAccountProfile, error) {
	var profile OAuthAccountProfile
	err := c.requestWithProof(ctx, http.MethodGet, "/oauth/account", nil, &profile, proofProtected)
	return profile, err
}

func (c *Client) OAuthAccountBalance(ctx context.Context) (OAuthBalance, error) {
	var balance OAuthBalance
	err := c.requestWithProof(ctx, http.MethodGet, "/oauth/account/balance", nil, &balance, proofProtected)
	return balance, err
}

func (c *Client) OAuthAPIKeys(ctx context.Context) ([]OAuthAPIKey, error) {
	var data struct {
		Items []OAuthAPIKey `json:"items"`
	}
	if err := c.requestWithProof(ctx, http.MethodGet, "/oauth/api-keys", nil, &data, proofProtected); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(data.Items))
	for _, item := range data.Items {
		id := strings.TrimSpace(string(item.ID))
		numericID, err := strconv.Atoi(id)
		if err != nil || numericID <= 0 || seen[id] {
			return nil, errors.New("BeeAPI API Key list contains an invalid or duplicate ID")
		}
		seen[id] = true
	}
	return data.Items, nil
}

func (c *Client) OAuthAPIKeyModelOptions(ctx context.Context, id string) ([]ModelOption, error) {
	id = strings.TrimSpace(id)
	if numericID, err := strconv.Atoi(id); err != nil || numericID <= 0 {
		return nil, errors.New("a positive numeric API Key ID is required")
	}
	var data struct {
		APIKeyID OpaqueID      `json:"api_key_id"`
		Models   []ModelOption `json:"models"`
	}
	path := "/oauth/api-keys/" + url.PathEscape(id) + "/model-options"
	if err := c.requestWithProof(ctx, http.MethodGet, path, nil, &data, proofProtected); err != nil {
		return nil, err
	}
	if returnedID := strings.TrimSpace(string(data.APIKeyID)); returnedID != "" && returnedID != id {
		return nil, errors.New("BeeAPI model options returned a different API Key ID")
	}
	return data.Models, nil
}

func (c *Client) CreateOAuthAPIKeyExport(ctx context.Context, ids []string, idempotencyKey string) (OAuthAPIKeyExport, error) {
	var result OAuthAPIKeyExport
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if len(idempotencyKey) < 16 || len(idempotencyKey) > 200 {
		return result, errors.New("Idempotency-Key must contain 16 to 200 characters")
	}
	clean := make([]int, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			numericID, parseErr := strconv.Atoi(id)
			if parseErr != nil || numericID <= 0 {
				return result, fmt.Errorf("invalid BeeAPI API Key ID %q", id)
			}
			seen[id] = true
			clean = append(clean, numericID)
		}
	}
	if len(clean) == 0 {
		return result, errors.New("at least one API Key ID is required")
	}
	if len(clean) > 10 {
		return result, errors.New("at most 10 API Key IDs may be exported")
	}
	headers := make(http.Header)
	headers.Set("Idempotency-Key", idempotencyKey)
	err := c.requestWithProofHeaders(ctx, http.MethodPost, "/oauth/api-key-exports", map[string]any{"api_key_ids": clean}, &result, proofProtected, headers)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(result.ExportID) == "" {
		return OAuthAPIKeyExport{}, errors.New("BeeAPI API Key export is missing export_id")
	}
	if result.RetryUntil.IsZero() {
		return OAuthAPIKeyExport{}, errors.New("BeeAPI API Key export is missing retry_until")
	}
	for _, credential := range result.Credentials {
		if strings.TrimSpace(string(credential.APIKeyID)) == "" || strings.TrimSpace(credential.APIKey) == "" {
			return OAuthAPIKeyExport{}, fmt.Errorf("BeeAPI API Key export contains incomplete credentials")
		}
	}
	return result, nil
}

func (c *Client) AckOAuthAPIKeyExport(ctx context.Context, exportID string) error {
	exportID = strings.TrimSpace(exportID)
	if exportID == "" {
		return errors.New("export ID is required")
	}
	return c.requestWithProof(ctx, http.MethodPost, "/oauth/api-key-exports/"+url.PathEscape(exportID)+"/ack", map[string]any{}, nil, proofProtected)
}
