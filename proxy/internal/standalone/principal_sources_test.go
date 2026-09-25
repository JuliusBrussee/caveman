package standalone

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/identity"
	"github.com/JuliusBrussee/caveman/proxy/internal/middleware"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// S2: principals are keyed by source, so a certificate CN or a JWT subject
// spelling a token principal's name gets 404 on that principal's handle, even
// in a namespace it is allowed.
func TestSourcePrefixedPrincipalsCannotReachTokenPrincipalSessions(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, content []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	issue := func(serial int64, cn string, usage x509.ExtKeyUsage) (*x509.Certificate, []byte, []byte) {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{usage}}, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, _ := x509.ParseCertificate(der)
		keyDER, _ := x509.MarshalECPrivateKey(key)
		return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	}
	_, serverCert, serverKey := issue(2, "server", x509.ExtKeyUsageServerAuth)
	clientCert, _, _ := issue(3, "team-a", x509.ExtKeyUsageClientAuth)
	serverTLS, err := identity.NewServerTLS(write("server.crt", serverCert), write("server.key", serverKey),
		write("ca.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})))
	if err != nil {
		t.Fatal(err)
	}

	b64 := base64.RawURLEncoding.EncodeToString
	idpKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	point, _ := idpKey.PublicKey.Bytes()
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "EC", "kid": "e1", "crv": "P-256", "x": b64(point[1:33]), "y": b64(point[33:])}}})
	}))
	t.Cleanup(idp.Close)
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": "e1"})
	claims, _ := json.Marshal(map[string]any{"iss": "https://idp.test", "aud": "caveman", "sub": "team-a", "exp": time.Now().Unix() + 600})
	input := b64(header) + "." + b64(claims)
	digest := sha256.Sum256([]byte(input))
	r, s, _ := ecdsa.Sign(rand.Reader, idpKey, digest[:])
	jwt := input + "." + b64(append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...))

	// The prefixed entries allow team-a/* too, so only the principal keeps the
	// certificate and the JWT out of the token principal's session.
	const tokenA = "team-a-token-0123456789"
	sum := sha256.Sum256([]byte(tokenA))
	tokens := write("tokens.yaml", []byte("principals:\n"+
		"  - {name: team-a, namespaces: [\"team-a/*\"], token_sha256: ["+hex.EncodeToString(sum[:])+"]}\n"+
		"  - {name: \"mtls:cn:team-a\", namespaces: [\"team-a/*\"]}\n"+
		"  - {name: \"oidc:https://idp.test#team-a\", namespaces: [\"team-a/*\"]}\n"))
	ids, err := identity.New(identity.Config{TokenMapFile: tokens, MTLS: true, MTLSCommonName: true, HTTPClient: idp.Client(),
		OIDC: identity.OIDC{Issuer: "https://idp.test", Audience: "caveman", JWKSURL: idp.URL + "/jwks"}})
	if err != nil {
		t.Fatal(err)
	}
	ids.UseServerTLS(serverTLS)
	state, err := store.Open(filepath.Join(dir, "state.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	runtime, err := NewMiddleware(config.Config{Mode: "compress"}, state, nil, "test", nil, ids)
	if err != nil {
		t.Fatal(err)
	}

	asToken := func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+tokenA) }
	asJWT := func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+jwt) }
	asCert := func(req *http.Request) {
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{clientCert}, VerifiedChains: [][]*x509.Certificate{{clientCert, caCert}}}
	}
	send := func(method, route string, body any, as func(*http.Request)) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reader = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, middleware.RoutePrefix+route, reader)
		req.Header.Set("Content-Type", "application/json")
		as(req)
		rec := httptest.NewRecorder()
		runtime.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	_, body := send(http.MethodGet, "capabilities", nil, asToken)
	var caps struct {
		PolicyRevision string `json:"policy_revision"`
		Transforms     []struct {
			TransformID string `json:"transform_id"`
		} `json:"transforms"`
	}
	if err := json.Unmarshal(body, &caps); err != nil {
		t.Fatal(err)
	}
	transforms := []string{}
	for _, c := range caps.Transforms {
		transforms = append(transforms, c.TransformID)
	}
	hash := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	content := strings.Repeat("[INFO] reading row: exact-value with verbose repeated details\r\n", 150) + "[ERROR] preserve this diagnostic exactly\r\n"
	scope := middleware.Scope{Namespace: "team-a/app", SessionID: "shared-1", BranchID: "main", CacheEpoch: "0"}
	status, body := send(http.MethodPost, "optimize", middleware.OptimizeRequest{SchemaVersion: 1, RequestID: "r1", LogicalCallID: "c1", AttemptID: "a1",
		IdempotencyKey: "i1", Scope: scope, Adapter: middleware.Adapter{ID: "test", Version: "1", FrameworkVersion: "1", SerializationRevision: "test-v1"},
		Mode: "compress", Policy: middleware.Policy{Revision: caps.PolicyRevision, Transforms: transforms},
		Segments:        []middleware.Segment{{ID: "tool-1", Kind: "tool_result", CacheRegion: "live_zone", Content: content, SHA256: hash(content), SourceID: "doc-1"}},
		ContextManifest: []middleware.ManifestItem{{ID: "m0", SHA256: hash("u")}, {ID: "m1", SHA256: hash(content)}},
		RecoveryBinding: &middleware.Binding{ID: "b1", Kind: "host_tool", ToolName: middleware.RecoveryToolName, OverheadText: "tool"}}, asToken)
	var out middleware.OptimizeResponse
	if err := json.Unmarshal(body, &out); err != nil || status != http.StatusOK || len(out.Replacements) == 0 || out.Replacements[0].RecoveryHandle == "" {
		t.Fatalf("optimize as team-a: %d %s", status, body)
	}
	retrieve := middleware.RetrieveRequest{SchemaVersion: 1, Scope: scope, Handle: out.Replacements[0].RecoveryHandle}
	for _, tc := range []struct {
		name string
		as   func(*http.Request)
		want int
	}{{"token principal team-a", asToken, 200}, {"certificate CN=team-a", asCert, 404}, {"JWT sub=team-a", asJWT, 404}} {
		if status, body := send(http.MethodPost, "retrieve", retrieve, tc.as); status != tc.want {
			t.Errorf("%s: retrieve got %d %.80s, want %d", tc.name, status, body, tc.want)
		}
	}
}

type nopReloader struct{}

func (nopReloader) Reload(bool) (bool, error) { return false, nil }

// S12: /metrics carries the token map and TLS reload series.
func TestMetricsIncludeIdentityReloads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go identity.Watch(ctx, slog.New(slog.DiscardHandler), time.Hour, map[string]identity.Reloader{"metrics_test": nopReloader{}})
	handler := New(config.Config{Mode: "record"}, nil, Options{}).Handler()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if strings.Contains(get(t, handler, "/metrics").Body.String(), `caveman_identity_reload_failures_total{source="metrics_test"} 0`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("/metrics has no identity reload series")
		}
	}
}
