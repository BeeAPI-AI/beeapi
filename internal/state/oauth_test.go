package state

import (
	"bytes"
	"os"
	"testing"
)

func TestOAuthAccountMetadataNeverContainsTokenSecrets(t *testing.T) {
	store := &Store{Dir: t.TempDir()}
	t.Setenv("GETBEE_DISABLE_KEYRING", "1")
	backend, err := store.SaveNamedCredential("oauth-account-v1", `{"access_token":"boa-secret","refresh_token":"bor-secret","dpop_private_jwk":"secret-key"}`)
	if err != nil {
		t.Fatal(err)
	}
	account := OAuthAccount{
		Protocol: "beeapi-oauth-account-v1", Issuer: "https://beeapi.dev", ClientID: "getbeeapi-cli-v2",
		Username: "bee", Email: "bee@example.test", Scope: "account:profile:read",
		TokenCredentialID: "oauth-account-v1", TokenBackend: backend,
	}
	if err := store.SaveOAuthAccount(account); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadOAuthAccount()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != CurrentOAuthAccountVersion || loaded.Issuer != account.Issuer || loaded.TokenBackend != "protected-file" {
		t.Fatalf("unexpected OAuth account metadata: %#v", loaded)
	}
	raw, err := os.ReadFile(store.OAuthAccountPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte("boa-secret"), []byte("bor-secret"), []byte("secret-key")} {
		if bytes.Contains(raw, secret) {
			t.Fatalf("OAuth account metadata leaked secret %q: %s", secret, raw)
		}
	}
}
