package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func TestBalanceMenuShowsAccountAndKeyStatusWithoutLeakingSecrets(t *testing.T) {
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: t.TempDir()}
	goodBackend, err := store.SaveNamedCredential("good", "sk-super-secret-good")
	if err != nil {
		t.Fatal(err)
	}
	badBackend, err := store.SaveNamedCredential("bad", "sk-super-secret-bad")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(state.Config{
		Endpoint: "https://beeapi.dev",
		Credentials: []state.Credential{
			{ID: "good", Name: "Coding", Prefix: "sk-good", Backend: goodBackend},
			{ID: "bad", Name: "备用", Prefix: "sk-bad", Backend: badBackend},
		},
		Agents: []string{"codex"}, AgentCredentials: map[string]string{"codex": "good"},
	}); err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader("\n")
	var output bytes.Buffer
	r := &runner{
		ctx: context.Background(), store: store, in: input, reader: bufio.NewReader(input), out: &output, errOut: &output,
		usageLookup: func(_ context.Context, endpoint, apiKey string) (beeapi.Usage, error) {
			if endpoint != "https://beeapi.dev" {
				t.Fatalf("unexpected endpoint: %s", endpoint)
			}
			if apiKey == "sk-super-secret-bad" {
				return beeapi.Usage{}, errors.New("rejected sk-super-secret-bad")
			}
			return beeapi.Usage{
				IsActive: true, IsValid: true, Balance: 8.75, Currency: "USD",
				KeyName: "Coding", KeyPrefix: "sk-good", PlanName: "OpenAI Plus",
			}, nil
		},
	}
	if err := r.balanceMenu(); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"账户余额  $8.75", "✓ Coding · sk-good", "OpenAI Plus", "? 备用 · sk-bad", "[已隐藏]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("balance output is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "sk-super-secret-good") || strings.Contains(text, "sk-super-secret-bad") {
		t.Fatalf("balance output leaked an API key:\n%s", text)
	}
}

func TestHomeBalanceUsesShortCache(t *testing.T) {
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	store := &state.Store{Dir: t.TempDir()}
	backend, err := store.SaveNamedCredential("daily", "sk-daily-secret")
	if err != nil {
		t.Fatal(err)
	}
	cfg := state.Config{
		Endpoint: "https://beeapi.dev", Agents: []string{"codex"},
		Credentials:      []state.Credential{{ID: "daily", Name: "日常", Backend: backend}},
		AgentCredentials: map[string]string{"codex": "daily"},
	}
	calls := 0
	r := &runner{ctx: context.Background(), store: store, usageLookup: func(context.Context, string, string) (beeapi.Usage, error) {
		calls++
		return beeapi.Usage{IsActive: true, IsValid: true, Balance: 3.2, Currency: "USD"}, nil
	}}
	if first, second := r.homeBalanceLabel(cfg), r.homeBalanceLabel(cfg); first != "$3.20" || second != "$3.20" {
		t.Fatalf("unexpected cached balance labels: %q, %q", first, second)
	}
	if calls != 1 {
		t.Fatalf("usage lookup calls = %d, want 1", calls)
	}
}
