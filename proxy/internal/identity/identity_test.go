package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hashOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func bearer(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

const tokenA1, tokenA2, tokenB = "team-a-token-one-0123456789", "team-a-token-two-0123456789", "team-b-token-0123456789abcd"

func mapFile(entries ...string) string { return "principals:\n" + strings.Join(entries, "") }

func entry(name, namespaces string, tokens ...string) string {
	var hashes []string
	for _, token := range tokens {
		hashes = append(hashes, hashOf(token))
	}
	return "  - name: " + name + "\n    namespaces: " + namespaces + "\n    token_sha256: [" + strings.Join(hashes, ", ") + "]\n"
}

func TestTokenMapPrincipalsNamespacesAndLegacyToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	writeFile(t, path, mapFile(entry("team-a", `["team-a", "team-a/*"]`, tokenA1, tokenA2), entry("team-b", `["team-b/*"]`, tokenB))+
		"  - name: quota-only\n    quota: {rows: 10, bytes: 20, requests_per_minute: 30}\n")
	r, err := New(Config{Token: "legacy-shared-token-0123", TokenMapFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if r.Open() || !r.MultiPrincipal() {
		t.Fatal("a configured resolver reported open or single-principal")
	}
	for _, tc := range []struct {
		token, principal, mechanism string
		allowed, denied             []string
	}{
		{tokenA1, "team-a", "token_map", []string{"team-a", "team-a/x", "team-a/x/y"}, []string{"team-ab", "team-b/x", "other"}},
		{tokenA2, "team-a", "token_map", []string{"team-a/y"}, []string{"team-b/y"}},
		{tokenB, "team-b", "token_map", []string{"team-b/x"}, []string{"team-b", "team-a/x"}},
		{"legacy-shared-token-0123", Operator, "token", []string{"team-a/x", "anything:at/all"}, nil},
	} {
		p, err := r.Identify(bearer(tc.token))
		if err != nil || p.Name != tc.principal || p.Mechanism != tc.mechanism {
			t.Fatalf("token for %s resolved to %+v, %v", tc.principal, p, err)
		}
		for _, ns := range tc.allowed {
			if !p.Allows(ns) {
				t.Errorf("%s denied %q", tc.principal, ns)
			}
		}
		for _, ns := range tc.denied {
			if p.Allows(ns) {
				t.Errorf("%s allowed %q", tc.principal, ns)
			}
		}
	}
	// x-cave-api-key carries static tokens too, as it does for the legacy token.
	req := bearer("")
	req.Header.Set("x-cave-api-key", tokenB)
	if p, err := r.Identify(req); err != nil || p.Name != "team-b" {
		t.Fatalf("x-cave-api-key: %+v %v", p, err)
	}
	for _, token := range []string{"", "unknown-token-0123456789", hashOf(tokenA1)} {
		if _, err := r.Identify(bearer(token)); err == nil {
			t.Errorf("credential %q authenticated", token)
		}
	}
}

// Two tokens per principal rotate without an outage; a broken file keeps the
// last good map instead of locking everyone out or opening up.
func TestTokenMapRotationAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	writeFile(t, path, mapFile(entry("team-a", `["*"]`, tokenA1)))
	r, err := New(Config{TokenMapFile: path})
	if err != nil {
		t.Fatal(err)
	}
	works := func(token string) bool { _, err := r.Identify(bearer(token)); return err == nil }
	if !works(tokenA1) || works(tokenA2) {
		t.Fatal("initial map")
	}
	if reloaded, err := r.Reload(false); reloaded || err != nil {
		t.Fatalf("unchanged file reloaded: %v %v", reloaded, err)
	}
	writeFile(t, path, mapFile(entry("team-a", `["*"]`, tokenA1, tokenA2)))
	if reloaded, err := r.Reload(false); !reloaded || err != nil || !works(tokenA1) || !works(tokenA2) {
		t.Fatalf("rotation step 1: %v %v", reloaded, err)
	}
	writeFile(t, path, mapFile(entry("team-a", `["*"]`, tokenA2)))
	if _, err := r.Reload(false); err != nil || works(tokenA1) || !works(tokenA2) {
		t.Fatalf("rotation step 2: old token still works or new one does not (%v)", err)
	}
	writeFile(t, path, "principals: [{name: team-a, token_sha256: [not-a-hash]}]\n")
	if _, err := r.Reload(true); err == nil || strings.Contains(err.Error(), "not-a-hash") || !works(tokenA2) {
		t.Fatalf("broken map: %v, previous map kept: %v", err, works(tokenA2))
	}
}

func TestTokenMapRejectsAmbiguousFiles(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"duplicate principal": mapFile(entry("a", `["*"]`, tokenA1), entry("a", `["*"]`, tokenA2)),
		"duplicate hash":      mapFile(entry("a", `["*"]`, tokenA1), entry("b", `["*"]`, tokenA1)),
		"bad glob":            mapFile(entry("a", `["team a"]`, tokenA1)),
		"bad name":            mapFile(entry(`"a b"`, `["*"]`, tokenA1)),
		"unknown field":       "principals:\n  - name: a\n    tokens: [x]\n",
		"negative quota":      "principals:\n  - name: a\n    quota: {rows: -1}\n",
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_"))
		writeFile(t, path, content)
		if _, err := New(Config{TokenMapFile: path}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := New(Config{TokenMapFile: filepath.Join(dir, "missing")}); err == nil {
		t.Error("missing token map accepted")
	}
}

func TestOpenResolverIsTheOperator(t *testing.T) {
	r, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Identify(bearer(""))
	if !r.Open() || r.MultiPrincipal() || err != nil || p.Name != Operator || !p.Allows("any/namespace") {
		t.Fatalf("open resolver: %+v %v", p, err)
	}
	// The legacy token alone keeps the one-principal behavior, now enforced.
	r, _ = New(Config{Token: "legacy-shared-token-0123"})
	if _, err := r.Identify(bearer("")); err == nil || r.MultiPrincipal() {
		t.Fatal("legacy token did not gate the middleware")
	}
}

func TestNewPrincipalGlobs(t *testing.T) {
	none, _ := NewPrincipal("p", "test", nil, Quota{})
	all, _ := NewPrincipal("p", "test", []string{"*"}, Quota{})
	mid, _ := NewPrincipal("p", "test", []string{"org:*/prod"}, Quota{})
	for _, tc := range []struct {
		p         Principal
		namespace string
		want      bool
	}{
		{none, "a", false}, {all, "a/b:c", true}, {mid, "org:eu/prod", true}, {mid, "org:eu/x/prod", true},
		{mid, "org:eu/prod2", false}, {mid, "xorg:eu/prod", false}, {mid, "org.eu/prod", false},
	} {
		if got := tc.p.Allows(tc.namespace); got != tc.want {
			t.Errorf("Allows(%q) = %v", tc.namespace, got)
		}
	}
	if _, err := NewPrincipal("p", "test", []string{"a(b)"}, Quota{}); err == nil {
		t.Error("regexp metacharacters accepted as a glob")
	}
	if _, err := NewPrincipal("p", "test", []string{strings.Repeat("a", 257)}, Quota{}); err == nil {
		t.Error("overlong glob accepted")
	}
}

func TestWatchReloadsOnFileChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	writeFile(t, path, mapFile(entry("team-a", `["*"]`, tokenA1)))
	r, err := New(Config{TokenMapFile: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Watch(ctx, slog.New(slog.DiscardHandler), 10*time.Millisecond, map[string]Reloader{"token_map": r})
	writeFile(t, path, mapFile(entry("team-a", `["*"]`, tokenA2)))
	// Same size as before: move the mtime so even a coarse clock sees a change.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := r.Identify(bearer(tokenA2)); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("watch did not pick up the rewritten token map")
}
