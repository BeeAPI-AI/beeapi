package beeapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestDPoPProofIsES256RequestAndTokenBound(t *testing.T) {
	signer, err := newDPoPSigner()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := signer.proof("post", "https://beeapi.dev/api/v1/oauth/api-key-exports", "boa_test", time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected JWT shape: %q", proof)
	}

	var header struct {
		Type string `json:"typ"`
		Alg  string `json:"alg"`
		JWK  struct {
			KTY string `json:"kty"`
			CRV string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"jwk"`
	}
	decodeJWTJSON(t, parts[0], &header)
	if header.Type != "dpop+jwt" || header.Alg != "ES256" || header.JWK.KTY != "EC" || header.JWK.CRV != "P-256" {
		t.Fatalf("unexpected DPoP header: %#v", header)
	}
	var claims map[string]any
	decodeJWTJSON(t, parts[1], &claims)
	if claims["htm"] != "POST" || claims["htu"] != "https://beeapi.dev/api/v1/oauth/api-key-exports" {
		t.Fatalf("unexpected request binding: %#v", claims)
	}
	digest := sha256.Sum256([]byte("boa_test"))
	if claims["ath"] != base64.RawURLEncoding.EncodeToString(digest[:]) {
		t.Fatalf("unexpected access-token hash: %#v", claims["ath"])
	}

	xBytes, _ := base64.RawURLEncoding.DecodeString(header.JWK.X)
	yBytes, _ := base64.RawURLEncoding.DecodeString(header.JWK.Y)
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(xBytes) != 32 || len(yBytes) != 32 || len(signature) != 64 {
		t.Fatalf("unexpected ES256 coordinate/signature lengths: x=%d y=%d sig=%d", len(xBytes), len(yBytes), len(signature))
	}
	public := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(xBytes), Y: new(big.Int).SetBytes(yBytes)}
	signedDigest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(public, signedDigest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		t.Fatal("DPoP ES256 signature did not verify")
	}
}

func TestDPoPPrivateJWKRoundTripKeepsSenderBinding(t *testing.T) {
	client := New("https://beeapi.test")
	raw, err := client.ExportDPoPPrivateJWK()
	if err != nil {
		t.Fatal(err)
	}
	restored := New("https://beeapi.test")
	if err := restored.ImportDPoPPrivateJWK(raw); err != nil {
		t.Fatal(err)
	}

	first, err := client.dpop.proof("GET", "https://beeapi.test/api/v1/oauth/account", "boa_test", time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := restored.dpop.proof("GET", "https://beeapi.test/api/v1/oauth/account", "boa_test", time.Unix(1_700_000_001, 0))
	if err != nil {
		t.Fatal(err)
	}
	var firstHeader, secondHeader struct {
		JWK map[string]string `json:"jwk"`
	}
	decodeJWTJSON(t, strings.Split(first, ".")[0], &firstHeader)
	decodeJWTJSON(t, strings.Split(second, ".")[0], &secondHeader)
	if firstHeader.JWK["x"] != secondHeader.JWK["x"] || firstHeader.JWK["y"] != secondHeader.JWK["y"] {
		t.Fatalf("restored DPoP key changed sender binding: %#v != %#v", firstHeader.JWK, secondHeader.JWK)
	}
}

func decodeJWTJSON(t *testing.T, part string, target any) {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
