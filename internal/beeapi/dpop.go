package beeapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

// dpopSigner is scoped to one client session. OAuth Account sessions export
// the private key only into the same protected secret record as their refresh
// token; it must never be written into ordinary CLI configuration metadata.
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

type privateDPoPJWK struct {
	KTY string `json:"kty"`
	CRV string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	D   string `json:"d"`
}

func (s *dpopSigner) exportPrivateJWK() (string, error) {
	if s == nil || s.private == nil || s.private.D == nil {
		return "", errors.New("DPoP signer is not initialized")
	}
	jwk := privateDPoPJWK{
		KTY: "EC", CRV: "P-256",
		X: base64.RawURLEncoding.EncodeToString(paddedP256Coordinate(s.private.PublicKey.X)),
		Y: base64.RawURLEncoding.EncodeToString(paddedP256Coordinate(s.private.PublicKey.Y)),
		D: base64.RawURLEncoding.EncodeToString(paddedP256Coordinate(s.private.D)),
	}
	b, err := json.Marshal(jwk)
	return string(b), err
}

func dpopSignerFromPrivateJWK(raw string) (*dpopSigner, error) {
	var jwk privateDPoPJWK
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &jwk); err != nil {
		return nil, fmt.Errorf("decode DPoP private JWK: %w", err)
	}
	if jwk.KTY != "EC" || jwk.CRV != "P-256" || jwk.D == "" {
		return nil, errors.New("unsupported DPoP private JWK")
	}
	dBytes, err := base64.RawURLEncoding.DecodeString(jwk.D)
	if err != nil || len(dBytes) == 0 || len(dBytes) > 32 {
		return nil, errors.New("invalid DPoP private scalar")
	}
	curve := elliptic.P256()
	d := new(big.Int).SetBytes(dBytes)
	if d.Sign() <= 0 || d.Cmp(curve.Params().N) >= 0 {
		return nil, errors.New("invalid DPoP private scalar")
	}
	x, y := curve.ScalarBaseMult(paddedP256Coordinate(d))
	if jwk.X != "" && jwk.X != base64.RawURLEncoding.EncodeToString(paddedP256Coordinate(x)) {
		return nil, errors.New("DPoP JWK public x does not match private key")
	}
	if jwk.Y != "" && jwk.Y != base64.RawURLEncoding.EncodeToString(paddedP256Coordinate(y)) {
		return nil, errors.New("DPoP JWK public y does not match private key")
	}
	return &dpopSigner{private: &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: d}}, nil
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
