package beeapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"sync"
	"time"
)

// dpopSigner is intentionally scoped to one running authorization flow. The
// BeeAPI configuration token is short-lived and has no refresh token, so there
// is no long-lived private key to leave behind in a plaintext config file.
type dpopSigner struct {
	private *ecdsa.PrivateKey
	mu      sync.Mutex
}

func newDPoPSigner() (*dpopSigner, error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &dpopSigner{private: private}, nil
}

func (s *dpopSigner) proof(method, targetURI, accessToken string, now time.Time) (string, error) {
	if s == nil || s.private == nil {
		return "", errors.New("DPoP signer is not initialized")
	}
	targetURI = strings.TrimSpace(targetURI)
	if targetURI == "" {
		return "", errors.New("DPoP target URI is empty")
	}

	x := base64.RawURLEncoding.EncodeToString(paddedP256Coordinate(s.private.PublicKey.X))
	y := base64.RawURLEncoding.EncodeToString(paddedP256Coordinate(s.private.PublicKey.Y))
	header := map[string]any{
		"typ": "dpop+jwt",
		"alg": "ES256",
		"jwk": map[string]string{"kty": "EC", "crv": "P-256", "x": x, "y": y},
	}
	jtiBytes := make([]byte, 24)
	if _, err := rand.Read(jtiBytes); err != nil {
		return "", err
	}
	claims := map[string]any{
		"jti": base64.RawURLEncoding.EncodeToString(jtiBytes),
		"htm": strings.ToUpper(strings.TrimSpace(method)),
		"htu": targetURI,
		"iat": now.Unix(),
	}
	if accessToken != "" {
		digest := sha256.Sum256([]byte(accessToken))
		claims["ath"] = base64.RawURLEncoding.EncodeToString(digest[:])
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := encodedHeader + "." + encodedClaims
	digest := sha256.Sum256([]byte(signingInput))

	// ecdsa.PrivateKey is safe for concurrent signing, but serializing here also
	// makes the lifecycle explicit and leaves room for an OS-backed signer later.
	s.mu.Lock()
	r, ss, err := ecdsa.Sign(rand.Reader, s.private, digest[:])
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	ss.FillBytes(signature[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func paddedP256Coordinate(value *big.Int) []byte {
	result := make([]byte, 32)
	if value == nil {
		return result
	}
	value.FillBytes(result)
	return result
}
