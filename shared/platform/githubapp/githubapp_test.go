package githubapp_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/githubapp"
)

var (
	keyOnce sync.Once
	keyPEM  []byte
	keyErr  error
)

func testKeyPEM(t *testing.T) []byte {
	t.Helper()
	keyOnce.Do(func() {
		var key *rsa.PrivateKey
		key, keyErr = rsa.GenerateKey(rand.Reader, 2048)
		if keyErr == nil {
			der := x509.MarshalPKCS1PrivateKey(key)
			keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
		}
	})
	if keyErr != nil {
		t.Fatalf("genkey: %v", keyErr)
	}
	return keyPEM
}

func newTestApp(t *testing.T, base string) *githubapp.App {
	t.Helper()
	app, err := githubapp.New(githubapp.Config{
		AppID:         "12345",
		AppSlug:       "caveman-agent",
		PrivateKeyPEM: testKeyPEM(t),
		WebhookSecret: "whsec",
		BaseURL:       base,
		HTTPClient:    http.DefaultClient, // bypass SSRF for the httptest loopback host
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

func TestAppJWT_IsSignedThreePartToken(t *testing.T) {
	app := newTestApp(t, "https://api.github.com")
	before := time.Now()
	jwt, err := app.AppJWT()
	if err != nil {
		t.Fatalf("AppJWT: %v", err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("expected a 3-part JWT, got %d parts", len(parts))
	}
	var header map[string]string
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(headerRaw, &header); err != nil || header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Fatalf("header = %v, err=%v", header, err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "12345" || claims.Exp-claims.Iat != 11*60 {
		t.Fatalf("claims = %+v", claims)
	}
	if got := time.Unix(claims.Iat, 0); got.Before(before.Add(-65*time.Second)) || got.After(before.Add(-55*time.Second)) {
		t.Fatalf("iat = %v, want about 60s before %v", got, before)
	}
	block, _ := pem.Decode(testKeyPEM(t))
	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&privateKey.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("JWT signature invalid: %v", err)
	}
}

func TestNewValidationAndKeyFormats(t *testing.T) {
	if _, err := githubapp.New(githubapp.Config{}); err == nil {
		t.Fatal("missing app id accepted")
	}
	if _, err := githubapp.New(githubapp.Config{AppID: "1"}); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err := githubapp.New(githubapp.Config{AppID: "1", PrivateKeyPEM: []byte("nope")}); err == nil {
		t.Fatal("invalid PEM accepted")
	}
	if _, err := githubapp.New(githubapp.Config{
		AppID: "1", PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("nope")}),
	}); err == nil {
		t.Fatal("invalid PKCS#8 accepted")
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := githubapp.New(githubapp.Config{
		AppID: "1", PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}),
	}); err == nil {
		t.Fatal("non-RSA PKCS#8 key accepted")
	}

	block, _ := pem.Decode(testKeyPEM(t))
	rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	app, err := githubapp.New(githubapp.Config{
		AppID:         " 123 ",
		AppSlug:       " slug ",
		PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}),
		WebhookSecret: "secret",
		HTTPClient:    http.DefaultClient,
	})
	if err != nil {
		t.Fatalf("valid PKCS#8 rejected: %v", err)
	}
	if app.Slug() != "slug" || app.WebhookSecret() != "secret" {
		t.Fatalf("trimmed metadata = (%q, %q)", app.Slug(), app.WebhookSecret())
	}
}

func TestMintInstallationToken_ScopedAndParsed(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations/777/access_tokens" || r.Method != http.MethodPost {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test","expires_at":"2026-06-27T13:00:00Z"}`))
	}))
	defer srv.Close()

	app := newTestApp(t, srv.URL)
	tok, err := app.MintInstallationToken(context.Background(), 777, []string{"api"}, nil)
	if err != nil {
		t.Fatalf("MintInstallationToken: %v", err)
	}
	if tok.Token != "ghs_test" {
		t.Fatalf("expected token ghs_test, got %q", tok.Token)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Fatalf("expected a Bearer App JWT, got %q", gotAuth)
	}
	// Least-agency: scoped to the one repo + contents/pull_requests write only.
	repos, _ := gotBody["repositories"].([]any)
	if len(repos) != 1 || repos[0] != "api" {
		t.Fatalf("expected repositories=[api], got %v", gotBody["repositories"])
	}
	perms, _ := gotBody["permissions"].(map[string]any)
	if perms["contents"] != "write" || perms["pull_requests"] != "write" {
		t.Fatalf("expected contents+pull_requests write, got %v", perms)
	}
	if _, hasAdmin := perms["administration"]; hasAdmin {
		t.Fatal("token must not request administration permission")
	}
}

func TestRevokeToken_UsesTokenAuth(t *testing.T) {
	var gotAuth, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	app := newTestApp(t, srv.URL)
	if err := app.RevokeToken(context.Background(), "ghs_test"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("expected DELETE, got %s", gotMethod)
	}
	if gotAuth != "Bearer ghs_test" {
		t.Fatalf("revoke must authenticate with the token itself, got %q", gotAuth)
	}
}

func TestInstallationAndRepositoryControlProofAPIs(t *testing.T) {
	proof := []byte("cave-connect:proof-123")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/777":
			_, _ = w.Write([]byte(`{"id":777,"account":{"login":"acme","id":99}}`))
		case "/repos/acme/api/installation":
			_, _ = w.Write([]byte(`{"id":777,"account":{"login":"acme","id":99}}`))
		case "/repos/acme/api/contents/.caveman/connect.txt":
			if r.URL.Query().Get("ref") != "main" {
				t.Fatalf("expected ref=main, got %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "file", "encoding": "base64", "size": len(proof),
				"content": base64.StdEncoding.EncodeToString(proof),
			})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	app := newTestApp(t, srv.URL)

	install, err := app.GetInstallation(context.Background(), 777)
	if err != nil || install.ID != 777 || install.Account.Login != "acme" {
		t.Fatalf("GetInstallation: install=%+v err=%v", install, err)
	}
	repoInstall, err := app.GetRepoInstallation(context.Background(), "acme", "api")
	if err != nil || repoInstall.ID != 777 {
		t.Fatalf("GetRepoInstallation: install=%+v err=%v", repoInstall, err)
	}
	got, err := app.GetFileContent(context.Background(), "ghs_read", "acme", "api", ".caveman/connect.txt", "main")
	if err != nil || string(got) != string(proof) {
		t.Fatalf("GetFileContent: got=%q err=%v", got, err)
	}
}

func TestGetInstallationFailClosedResponses(t *testing.T) {
	app := newTestApp(t, "https://api.github.com")
	if _, err := app.GetInstallation(context.Background(), 0); err == nil {
		t.Fatal("non-positive installation id accepted")
	}
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http failure", status: http.StatusForbidden, body: "denied"},
		{name: "malformed json", status: http.StatusOK, body: `{`},
		{name: "id mismatch", status: http.StatusOK, body: `{"id":8,"account":{"login":"acme","id":9}}`},
		{name: "missing account id", status: http.StatusOK, body: `{"id":7,"account":{"login":"acme"}}`},
		{name: "missing account login", status: http.StatusOK, body: `{"id":7,"account":{"id":9}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			if _, err := newTestApp(t, srv.URL).GetInstallation(context.Background(), 7); err == nil {
				t.Fatal("invalid installation response accepted")
			}
		})
	}
}

func TestMintInstallationTokenCustomPermissionsAndFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http failure", status: http.StatusForbidden, body: "denied"},
		{name: "malformed json", status: http.StatusCreated, body: `{`},
		{name: "missing token", status: http.StatusCreated, body: `{"expires_at":"2026-01-01T00:00:00Z"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			if _, err := newTestApp(t, srv.URL).MintInstallationToken(context.Background(), 7, []string{"api"}, map[string]string{"contents": "read"}); err == nil {
				t.Fatal("invalid token response accepted")
			}
		})
	}

	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"token","expires_at":"2026-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()
	if _, err := newTestApp(t, srv.URL).MintInstallationToken(context.Background(), 7, []string{" api "}, map[string]string{"contents": "read"}); err != nil {
		t.Fatal(err)
	}
	if repos, ok := body["repositories"].([]any); !ok || len(repos) != 1 || repos[0] != "api" {
		t.Fatalf("repository scope = %v", body)
	}
	if body["permissions"].(map[string]any)["contents"] != "read" {
		t.Fatalf("custom permissions replaced: %v", body)
	}
}

func TestMintInstallationTokenRejectsBroadOrInvalidScopeBeforeNetwork(t *testing.T) {
	app := newTestApp(t, "http://127.0.0.1:1")
	for _, tc := range []struct {
		installationID int64
		repos          []string
	}{
		{installationID: 0, repos: []string{"api"}},
		{installationID: 7, repos: nil},
		{installationID: 7, repos: []string{}},
		{installationID: 7, repos: []string{""}},
		{installationID: 7, repos: []string{"api", "web"}},
	} {
		if _, err := app.MintInstallationToken(context.Background(), tc.installationID, tc.repos, nil); err == nil {
			t.Fatalf("accepted installation=%d repos=%q", tc.installationID, tc.repos)
		}
	}
	for _, perms := range []map[string]string{
		{"administration": "write"},
		{"contents": "admin"},
		{"issues": "read"},
	} {
		if _, err := app.MintInstallationToken(context.Background(), 7, []string{"api"}, perms); err == nil {
			t.Fatalf("accepted permissions %v", perms)
		}
	}
}

func TestRepoAPIFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		call   func(*githubapp.App) error
	}{
		{
			name: "get repo status", status: http.StatusNotFound, body: "missing",
			call: func(app *githubapp.App) error {
				_, err := app.GetRepo(context.Background(), "token", "acme", "api")
				return err
			},
		},
		{
			name: "get repo malformed", status: http.StatusOK, body: `{`,
			call: func(app *githubapp.App) error {
				_, err := app.GetRepo(context.Background(), "token", "acme", "api")
				return err
			},
		},
		{
			name: "repo installation status", status: http.StatusForbidden, body: "denied",
			call: func(app *githubapp.App) error {
				_, err := app.GetRepoInstallation(context.Background(), "acme", "api")
				return err
			},
		},
		{
			name: "repo installation malformed", status: http.StatusOK, body: `{`,
			call: func(app *githubapp.App) error {
				_, err := app.GetRepoInstallation(context.Background(), "acme", "api")
				return err
			},
		},
		{
			name: "repo installation missing id", status: http.StatusOK, body: `{"id":0}`,
			call: func(app *githubapp.App) error {
				_, err := app.GetRepoInstallation(context.Background(), "acme", "api")
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			if err := tt.call(newTestApp(t, srv.URL)); err == nil {
				t.Fatal("invalid repo response accepted")
			}
		})
	}
}

func TestGetRepoSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"id":1,"node_id":"R_1","full_name":"acme/api","default_branch":"main"}`))
	}))
	defer srv.Close()
	got, err := newTestApp(t, srv.URL).GetRepo(context.Background(), "token", "acme", "api")
	if err != nil || got.ID != 1 || got.NodeID != "R_1" || got.DefaultBranch != "main" {
		t.Fatalf("GetRepo() = %+v, %v", got, err)
	}
}

func TestGetFileContentValidation(t *testing.T) {
	app := newTestApp(t, "https://api.github.com")
	if _, err := app.GetFileContent(context.Background(), "token", "owner", "repo", "/", ""); err == nil {
		t.Fatal("empty path accepted")
	}
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http failure", status: http.StatusNotFound, body: "missing"},
		{name: "malformed json", status: http.StatusOK, body: `{`},
		{name: "directory", status: http.StatusOK, body: `{"type":"dir","encoding":"base64","size":1,"content":"YQ=="}`},
		{name: "wrong encoding", status: http.StatusOK, body: `{"type":"file","encoding":"utf-8","size":1,"content":"a"}`},
		{name: "negative size", status: http.StatusOK, body: `{"type":"file","encoding":"base64","size":-1,"content":""}`},
		{name: "declared too large", status: http.StatusOK, body: `{"type":"file","encoding":"base64","size":65537,"content":""}`},
		{name: "invalid base64", status: http.StatusOK, body: `{"type":"file","encoding":"base64","size":1,"content":"!"}`},
		{name: "size mismatch", status: http.StatusOK, body: `{"type":"file","encoding":"base64","size":2,"content":"YQ=="}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			if _, err := newTestApp(t, srv.URL).GetFileContent(context.Background(), "token", "owner", "repo", "proof", ""); err == nil {
				t.Fatal("invalid file response accepted")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDoTokenRejectsPathHostEscapeBeforeAuthorization(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("token reached escaped request host %q", r.URL.Host)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	app, err := githubapp.New(githubapp.Config{
		AppID: "1", PrivateKeyPEM: testKeyPEM(t), BaseURL: "https://api.github.com", HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"@attacker.example/x", "//attacker.example/x", `\\attacker.example\x`, "https://attacker.example/x"} {
		if _, _, err := app.DoToken(context.Background(), "ghs_secret", http.MethodGet, path, nil); err == nil {
			t.Fatalf("escaped path accepted: %q", path)
		}
	}
	if called {
		t.Fatal("transport called for escaped path")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingReader) Close() error             { return nil }

func TestDoTokenTransportMarshalBuildAndReadFailures(t *testing.T) {
	transportClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network failed")
	})}
	app, err := githubapp.New(githubapp.Config{AppID: "1", PrivateKeyPEM: testKeyPEM(t), HTTPClient: transportClient})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.DoToken(context.Background(), "token", http.MethodGet, "/x", nil); err == nil || !strings.Contains(err.Error(), "network failed") {
		t.Fatalf("transport error = %v", err)
	}
	if _, _, err := app.DoToken(context.Background(), "token", http.MethodPost, "/x", make(chan int)); err == nil {
		t.Fatal("unmarshalable request body accepted")
	}

	badURLApp, err := githubapp.New(githubapp.Config{
		AppID: "1", PrivateKeyPEM: testKeyPEM(t), BaseURL: "://bad", HTTPClient: http.DefaultClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := badURLApp.DoToken(context.Background(), "token", http.MethodGet, "/x", nil); err == nil {
		t.Fatal("invalid request URL accepted")
	}

	readClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: failingReader{}, Header: make(http.Header)}, nil
	})}
	readApp, err := githubapp.New(githubapp.Config{AppID: "1", PrivateKeyPEM: testKeyPEM(t), HTTPClient: readClient})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := readApp.DoToken(context.Background(), "token", http.MethodGet, "/x", nil); err == nil {
		t.Fatal("response read failure ignored")
	}
}

func TestRevokeTokenFailuresAndDoTokenHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, strings.Repeat("x", 300))
			return
		}
		if r.URL.Path == "/installation/token" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" ||
			r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("headers = %v", r.Header)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()
	app := newTestApp(t, srv.URL)
	if err := app.RevokeToken(context.Background(), "token"); err == nil || !strings.Contains(err.Error(), "HTTP 200") {
		t.Fatalf("revoke error = %v", err)
	}
	status, raw, err := app.DoToken(context.Background(), "token", http.MethodPost, "/ok", map[string]string{"x": "y"})
	if err != nil || status != http.StatusOK || string(raw) != "ok" {
		t.Fatalf("DoToken() = (%d, %q, %v)", status, raw, err)
	}
	status, raw, err = app.DoToken(context.Background(), "token", http.MethodGet, "/fail", nil)
	if err != nil || status != http.StatusInternalServerError || len(raw) != 300 {
		t.Fatalf("DoToken failure response = (%d, %d bytes, %v)", status, len(raw), err)
	}
}
