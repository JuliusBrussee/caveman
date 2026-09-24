package identity

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// OIDC verifies JWT bearers (JWS compact, RS256 or ES256) issued by one issuer.
type OIDC struct {
	Issuer   string
	Audience string
	// JWKSURL must be https: the key set is the trust anchor.
	JWKSURL string
	// Algorithms narrows RS256/ES256; empty allows both. "none" and every HMAC
	// algorithm are never accepted: with a public key set, HMAC would let anyone
	// holding the public key sign.
	Algorithms []string
	// ClockSkew tolerates clock drift on exp and nbf; zero means 60 s.
	ClockSkew time.Duration
	// PrincipalClaim names the principal; empty means "sub".
	PrincipalClaim string
	// NamespacesClaim, when set, is a claim holding namespace globs (an array
	// of strings, or one space-separated string). A token without it falls back
	// to the token map entry named like the principal.
	NamespacesClaim string
}

var supportedAlgorithms = []string{"RS256", "ES256"}

const (
	maxJWTBytes = 16 << 10
	// jwksMaxAge refreshes a key set in use; jwksMinRefresh bounds refreshes, so
	// tokens with made-up key IDs cannot turn into a fetch storm.
	jwksMaxAge     = time.Hour
	jwksMinRefresh = 10 * time.Second
)

type oidcVerifier struct {
	cfg    OIDC
	client *http.Client
	logger *slog.Logger
	now    func() time.Time

	mu        sync.Mutex
	keys      map[string]jwk
	fetched   time.Time
	attempted time.Time
}

type jwk struct {
	alg string
	key crypto.PublicKey
}

func newOIDCVerifier(cfg OIDC, client *http.Client, logger *slog.Logger, now func() time.Time) (*oidcVerifier, error) {
	if cfg.Audience == "" {
		return nil, errors.New("oidc: audience is required")
	}
	if u, err := url.Parse(cfg.JWKSURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("oidc: jwks_url must be an https URL")
	}
	if len(cfg.Algorithms) == 0 {
		cfg.Algorithms = supportedAlgorithms
	}
	for _, alg := range cfg.Algorithms {
		if !slices.Contains(supportedAlgorithms, alg) {
			return nil, fmt.Errorf("oidc: algorithm %q is not supported; use RS256 or ES256", alg)
		}
	}
	if cfg.ClockSkew <= 0 {
		cfg.ClockSkew = time.Minute
	}
	if cfg.PrincipalClaim == "" {
		cfg.PrincipalClaim = "sub"
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	v := &oidcVerifier{cfg: cfg, client: client, logger: logger, now: now, keys: map[string]jwk{}}
	// An identity provider that is down at startup is retried on first use.
	v.mu.Lock()
	v.refresh()
	v.mu.Unlock()
	return v, nil
}

// refresh fetches the key set; on failure the previous keys stay. Callers hold mu.
func (v *oidcVerifier) refresh() {
	v.attempted = v.now()
	keys, err := v.fetch()
	if err != nil {
		if v.logger != nil {
			v.logger.Warn("oidc key set unavailable; keeping the previous keys", "jwks_url", v.cfg.JWKSURL, "error", err)
		}
		return
	}
	v.keys, v.fetched = keys, v.attempted
}

func (v *oidcVerifier) fetch() (map[string]jwk, error) {
	response, err := v.client.Get(v.cfg.JWKSURL)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", response.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty, Kid, Use, Alg, Crv, N, E, X, Y string
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&set); err != nil {
		return nil, err
	}
	keys := map[string]jwk{}
	for _, k := range set.Keys {
		if k.Kid == "" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		var parsed jwk
		switch k.Kty {
		case "RSA":
			n, errN := base64.RawURLEncoding.DecodeString(k.N)
			e, errE := base64.RawURLEncoding.DecodeString(k.E)
			exponent := new(big.Int).SetBytes(e)
			if errN != nil || errE != nil || len(n)*8 < 2048 || !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > 1<<31-1 {
				continue
			}
			parsed = jwk{"RS256", &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent.Int64())}}
		case "EC":
			x, errX := base64.RawURLEncoding.DecodeString(k.X)
			y, errY := base64.RawURLEncoding.DecodeString(k.Y)
			if k.Crv != "P-256" || errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
				continue
			}
			key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
			if err != nil {
				continue
			}
			parsed = jwk{"ES256", key}
		default:
			continue
		}
		// A key names the one algorithm it verifies; the token's header cannot
		// pick another (algorithm confusion).
		if k.Alg != "" && k.Alg != parsed.alg {
			continue
		}
		keys[k.Kid] = parsed
	}
	return keys, nil
}

// key returns the key for kid, refreshing a stale set, and refreshing once more
// for an unknown kid (the issuer rotated) when the last attempt is not recent.
func (v *oidcVerifier) key(kid string) (jwk, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	recent := now.Sub(v.attempted) < jwksMinRefresh
	if now.Sub(v.fetched) > jwksMaxAge && !recent {
		v.refresh()
		recent = true
	}
	k, ok := v.keys[kid]
	if !ok && !recent {
		v.refresh()
		k, ok = v.keys[kid]
	}
	return k, ok
}

// verify checks the signature and the registered claims and returns the claims.
func (v *oidcVerifier) verify(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(token) > maxJWTBytes || len(parts) != 3 {
		return nil, errors.New("oidc: malformed token")
	}
	var header struct {
		Alg  string          `json:"alg"`
		Kid  string          `json:"kid"`
		Crit json.RawMessage `json:"crit"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &header) != nil {
		return nil, errors.New("oidc: malformed header")
	}
	// RFC 7515 §4.1.11: an extension this verifier does not understand fails.
	if header.Crit != nil || !slices.Contains(v.cfg.Algorithms, header.Alg) || header.Kid == "" {
		return nil, errors.New("oidc: unacceptable header")
	}
	key, ok := v.key(header.Kid)
	if !ok || key.alg != header.Alg {
		return nil, errors.New("oidc: no key for token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("oidc: malformed signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch pub := key.key.(type) {
	case *rsa.PublicKey:
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], signature)
	case *ecdsa.PublicKey:
		// JWS ES256 signatures are r||s, 32 bytes each, not ASN.1.
		if len(signature) != 64 || !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
			err = errors.New("bad signature")
		}
	}
	if err != nil {
		return nil, errors.New("oidc: signature does not verify")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("oidc: malformed payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var claims map[string]any
	if decoder.Decode(&claims) != nil || claims == nil {
		return nil, errors.New("oidc: malformed claims")
	}
	if issuer, _ := claims["iss"].(string); issuer != v.cfg.Issuer {
		return nil, errors.New("oidc: wrong issuer")
	}
	audienceOK := false
	switch audience := claims["aud"].(type) {
	case string:
		audienceOK = audience == v.cfg.Audience
	case []any:
		audienceOK = slices.Contains(audience, any(v.cfg.Audience))
	}
	if !audienceOK {
		return nil, errors.New("oidc: wrong audience")
	}
	now := v.now()
	expires, ok := numericDate(claims["exp"])
	if !ok || !now.Before(expires.Add(v.cfg.ClockSkew)) {
		return nil, errors.New("oidc: expired or no exp")
	}
	if value, present := claims["nbf"]; present {
		notBefore, ok := numericDate(value)
		if !ok || now.Add(v.cfg.ClockSkew).Before(notBefore) {
			return nil, errors.New("oidc: not yet valid")
		}
	}
	return claims, nil
}

func numericDate(value any) (time.Time, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return time.Time{}, false
	}
	seconds, err := number.Float64()
	if err != nil || seconds < 0 || seconds > 1<<53 {
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), 0), true
}
