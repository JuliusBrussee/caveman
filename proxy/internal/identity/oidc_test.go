package identity

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var b64 = base64.RawURLEncoding

type idp struct {
	server  *httptest.Server
	fetches atomic.Int32
	mu      sync.Mutex
	keys    []map[string]string
}

func newIDP(t *testing.T) *idp {
	i := &idp{}
	i.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i.fetches.Add(1)
		i.mu.Lock()
		defer i.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": i.keys})
	}))
	t.Cleanup(i.server.Close)
	return i
}

func (i *idp) publish(keys ...map[string]string) {
	i.mu.Lock()
	i.keys = keys
	i.mu.Unlock()
}

func rsaJWK(kid string, key *rsa.PrivateKey) map[string]string {
	return map[string]string{"kty": "RSA", "kid": kid, "use": "sig", "n": b64.EncodeToString(key.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(key.E)).Bytes())}
}

func ecJWK(kid string, key *ecdsa.PrivateKey) map[string]string {
	point, _ := key.PublicKey.Bytes()
	return map[string]string{"kty": "EC", "kid": kid, "crv": "P-256", "x": b64.EncodeToString(point[1:33]), "y": b64.EncodeToString(point[33:])}
}

// sign builds a compact JWS. key is *rsa.PrivateKey, *ecdsa.PrivateKey, a
// []byte HMAC secret, or nil for an unsigned token.
func sign(t *testing.T, header, claims map[string]any, key any) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	digest := sha256.Sum256([]byte(input))
	var signature []byte
	switch k := key.(type) {
	case *rsa.PrivateKey:
		signature, _ = rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, digest[:])
	case *ecdsa.PrivateKey:
		r, s, err := ecdsa.Sign(rand.Reader, k, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		signature = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	case []byte:
		mac := hmac.New(sha256.New, k)
		mac.Write([]byte(input))
		signature = mac.Sum(nil)
	}
	return input + "." + b64.EncodeToString(signature)
}

type clock struct{ now atomic.Int64 }

func (c *clock) Now() time.Time          { return time.Unix(c.now.Load(), 0) }
func (c *clock) advance(d time.Duration) { c.now.Add(int64(d.Seconds())) }

func oidcResolver(t *testing.T, i *idp, c *clock, oidc OIDC, tokenMapFile string) *Resolver {
	t.Helper()
	oidc.Issuer, oidc.Audience, oidc.JWKSURL = "https://idp.example", "caveman", i.server.URL+"/jwks"
	r, err := New(Config{OIDC: oidc, TokenMapFile: tokenMapFile, HTTPClient: i.server.Client(), Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOIDCAcceptsRS256AndES256WithNamespaces(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	i := newIDP(t)
	i.publish(rsaJWK("r1", rsaKey), ecJWK("e1", ecKey))
	c := &clock{}
	c.now.Store(1_800_000_000)
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	writeFile(t, path, "principals:\n  - name: svc-mapped\n    namespaces: [\"mapped/*\"]\n    quota: {requests_per_minute: 7}\n")
	r := oidcResolver(t, i, c, OIDC{NamespacesClaim: "caveman_namespaces"}, path)
	claims := func(sub string, namespaces any) map[string]any {
		out := map[string]any{"iss": "https://idp.example", "aud": []string{"other", "caveman"}, "sub": sub, "exp": c.Now().Unix() + 60}
		if namespaces != nil {
			out["caveman_namespaces"] = namespaces
		}
		return out
	}
	for _, tc := range []struct {
		token, name, allowed, denied string
		rpm                          int
	}{
		{sign(t, map[string]any{"alg": "RS256", "kid": "r1"}, claims("svc-a", []string{"team-a/*"}), rsaKey), "svc-a", "team-a/x", "team-b/x", 0},
		{sign(t, map[string]any{"alg": "ES256", "kid": "e1"}, claims("svc-b", "team-b/* shared"), ecKey), "svc-b", "shared", "team-a/x", 0},
		{sign(t, map[string]any{"alg": "ES256", "kid": "e1"}, claims("svc-mapped", nil), ecKey), "svc-mapped", "mapped/x", "team-a/x", 7},
		{sign(t, map[string]any{"alg": "ES256", "kid": "e1"}, claims("svc-unmapped", nil), ecKey), "svc-unmapped", "", "team-a/x", 0},
		// An explicitly empty claim means no namespace, not "use the mapping".
		{sign(t, map[string]any{"alg": "ES256", "kid": "e1"}, claims("svc-mapped", []string{}), ecKey), "svc-mapped", "", "mapped/x", 7},
	} {
		p, err := r.Identify(bearer(tc.token))
		if err != nil || p.Name != tc.name || p.Mechanism != "oidc" || p.Quota.RequestsPerMinute != tc.rpm {
			t.Fatalf("%s: %+v %v", tc.name, p, err)
		}
		if (tc.allowed != "" && !p.Allows(tc.allowed)) || p.Allows(tc.denied) {
			t.Errorf("%s: namespaces wrong", tc.name)
		}
	}
}

func TestOIDCRejectsForgedAndInvalidTokens(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	i := newIDP(t)
	i.publish(rsaJWK("r1", rsaKey), ecJWK("e1", ecKey))
	c := &clock{}
	c.now.Store(1_800_000_000)
	r := oidcResolver(t, i, c, OIDC{ClockSkew: 30 * time.Second, NamespacesClaim: "caveman_namespaces"}, "")
	now := c.Now().Unix()
	valid := func() map[string]any {
		return map[string]any{"iss": "https://idp.example", "aud": "caveman", "sub": "svc", "exp": now + 60}
	}
	with := func(key string, value any) map[string]any {
		claims := valid()
		if value == nil {
			delete(claims, key)
		} else {
			claims[key] = value
		}
		return claims
	}
	rs := map[string]any{"alg": "RS256", "kid": "r1"}
	good := sign(t, rs, valid(), rsaKey)
	if p, err := r.Identify(bearer(good)); err != nil || p.Name != "svc" {
		t.Fatalf("control token rejected: %v", err)
	}
	publicDER, _ := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	tampered := []byte(good)
	tampered[len(good)/2] ^= 1
	for name, token := range map[string]string{
		"bad signature":           sign(t, rs, valid(), otherKey),
		"tampered payload":        string(tampered),
		"alg none":                sign(t, map[string]any{"alg": "none", "kid": "r1"}, valid(), nil),
		"HS256 with public key":   sign(t, map[string]any{"alg": "HS256", "kid": "r1"}, valid(), publicDER),
		"RS256 header on EC key":  sign(t, map[string]any{"alg": "RS256", "kid": "e1"}, valid(), rsaKey),
		"ES256 header on RSA key": sign(t, map[string]any{"alg": "ES256", "kid": "r1"}, valid(), rsaKey), // validly RSA-signed
		"no kid":                  sign(t, map[string]any{"alg": "RS256"}, valid(), rsaKey),
		"crit header":             sign(t, map[string]any{"alg": "RS256", "kid": "r1", "crit": []string{"exp"}}, valid(), rsaKey),
		"expired past skew":       sign(t, rs, with("exp", now-31), rsaKey),
		"no exp":                  sign(t, rs, with("exp", nil), rsaKey),
		"not yet valid":           sign(t, rs, with("nbf", now+31), rsaKey),
		"wrong audience":          sign(t, rs, with("aud", "someone-else"), rsaKey),
		"wrong audience list":     sign(t, rs, with("aud", []string{"a", "b"}), rsaKey),
		"wrong issuer":            sign(t, rs, with("iss", "https://evil.example"), rsaKey),
		"no principal":            sign(t, rs, with("sub", nil), rsaKey),
		"principal not a string":  sign(t, rs, with("sub", 42), rsaKey),
		"operator principal":      sign(t, rs, with("sub", Operator), rsaKey),
		"principal with space":    sign(t, rs, with("sub", "a b"), rsaKey),
		"namespaces not a list":   sign(t, rs, with("caveman_namespaces", 7), rsaKey),
		"not a JWT":               "a.b.c",
	} {
		if p, err := r.Identify(bearer(token)); err == nil {
			t.Errorf("%s: accepted as %+v", name, p)
		}
	}
	// Inside the skew both ways still verifies.
	if _, err := r.Identify(bearer(sign(t, rs, with("exp", now-29), rsaKey))); err != nil {
		t.Errorf("expiry inside the clock skew rejected: %v", err)
	}
	if _, err := r.Identify(bearer(sign(t, rs, with("nbf", now+29), rsaKey))); err != nil {
		t.Errorf("nbf inside the clock skew rejected: %v", err)
	}
	// An algorithm the operator did not allow is refused even with a good key.
	narrow := oidcResolver(t, i, c, OIDC{Algorithms: []string{"ES256"}}, "")
	if _, err := narrow.Identify(bearer(good)); err == nil {
		t.Error("RS256 accepted with algorithms: [ES256]")
	}
}

func TestOIDCUnknownKidRefreshesOnceThenIsBounded(t *testing.T) {
	oldKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	newKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	i := newIDP(t)
	i.publish(rsaJWK("old", oldKey))
	c := &clock{}
	c.now.Store(1_800_000_000)
	r := oidcResolver(t, i, c, OIDC{}, "")
	if i.fetches.Load() != 1 {
		t.Fatalf("startup fetches = %d", i.fetches.Load())
	}
	claims := func() map[string]any {
		return map[string]any{"iss": "https://idp.example", "aud": "caveman", "sub": "svc", "exp": c.Now().Unix() + 60}
	}
	// The issuer rotates: the new kid appears after startup.
	i.publish(rsaJWK("old", oldKey), rsaJWK("new", newKey))
	c.advance(jwksMinRefresh)
	if _, err := r.Identify(bearer(sign(t, map[string]any{"alg": "RS256", "kid": "new"}, claims(), newKey))); err != nil {
		t.Fatalf("rotated key not picked up: %v", err)
	}
	if i.fetches.Load() != 2 {
		t.Fatalf("fetches after rotation = %d, want exactly one refresh", i.fetches.Load())
	}
	ghost := sign(t, map[string]any{"alg": "RS256", "kid": "ghost"}, claims(), newKey)
	for n := 0; n < 5; n++ {
		if _, err := r.Identify(bearer(ghost)); err == nil {
			t.Fatal("unknown kid accepted")
		}
	}
	if i.fetches.Load() != 2 {
		t.Fatalf("unknown kids caused %d fetches inside the refresh interval", i.fetches.Load()-2)
	}
	c.advance(jwksMinRefresh)
	_, _ = r.Identify(bearer(ghost))
	if i.fetches.Load() != 3 {
		t.Fatalf("fetches after the interval = %d, want one more", i.fetches.Load())
	}
	// An unreachable issuer keeps the last good keys.
	i.server.Close()
	c.advance(jwksMaxAge + time.Minute)
	if _, err := r.Identify(bearer(sign(t, map[string]any{"alg": "RS256", "kid": "old"}, claims(), oldKey))); err != nil {
		t.Fatalf("keys dropped when the issuer became unreachable: %v", err)
	}
}

func TestOIDCKeySetAndConfigValidation(t *testing.T) {
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	strong, _ := rsa.GenerateKey(rand.Reader, 2048)
	i := newIDP(t)
	mislabeled := rsaJWK("mislabeled", strong)
	mislabeled["alg"] = "ES256"
	encryption := rsaJWK("enc", strong)
	encryption["use"] = "enc"
	i.publish(rsaJWK("weak", weak), mislabeled, encryption)
	c := &clock{}
	c.now.Store(1_800_000_000)
	r := oidcResolver(t, i, c, OIDC{}, "")
	claims := map[string]any{"iss": "https://idp.example", "aud": "caveman", "sub": "svc", "exp": c.Now().Unix() + 60}
	for kid, key := range map[string]*rsa.PrivateKey{"weak": weak, "mislabeled": strong, "enc": strong} {
		if _, err := r.Identify(bearer(sign(t, map[string]any{"alg": "RS256", "kid": kid}, claims, key))); err == nil {
			t.Errorf("key %q should not verify tokens", kid)
		}
	}
	for name, oidc := range map[string]OIDC{
		"http jwks":      {Issuer: "i", Audience: "a", JWKSURL: "http://idp.example/jwks"},
		"no audience":    {Issuer: "i", JWKSURL: "https://idp.example/jwks"},
		"hmac algorithm": {Issuer: "i", Audience: "a", JWKSURL: "https://idp.example/jwks", Algorithms: []string{"HS256"}},
		"none algorithm": {Issuer: "i", Audience: "a", JWKSURL: "https://idp.example/jwks", Algorithms: []string{"none"}},
	} {
		if _, err := New(Config{OIDC: oidc, HTTPClient: i.server.Client()}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
