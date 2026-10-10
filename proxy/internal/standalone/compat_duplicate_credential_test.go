package standalone

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// A pi provider with authHeader: true sends one credential twice: as
// `Authorization: Bearer <key>` and as `x-api-key: <key>`. Creds.Resolve reads
// x-api-key before Authorization, so the resolved credential carried no
// "bearer" scheme and the named compat mount deleted the bearer on an
// Anthropic-protocol path. A gateway that accepts only the bearer answered 401,
// and there was no configuration that both routed and authenticated (#1215).
//
// Same value means same principal, so preserving the scheme cannot replace the
// caller's principal with another account — the reason x-api-key is resolved
// first in the general case.
func TestNamedCompatKeepsInboundBearerWhenOneKeyArrivesInBothHeaders(t *testing.T) {
	observed := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"fixture","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()

	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer spend.Close()
	srv := New(config.Config{Mode: "record", Compat: map[string]config.CompatConfig{
		"bearer-only": {BaseURL: upstream.URL},
	}}, spend, Options{HTTPClient: &http.Client{}})

	req := httptest.NewRequest(http.MethodPost, "/compat/bearer-only/v1/messages",
		strings.NewReader(`{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer one-key")
	req.Header.Set("x-api-key", "one-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	captured := <-observed
	if got := captured.Get("Authorization"); got != "Bearer one-key" {
		t.Fatalf("inbound bearer dropped on an Anthropic-protocol compat path: Authorization=%q x-api-key=%q", got, captured.Get("X-Api-Key"))
	}
	if got := captured.Get("X-Api-Key"); got != "" {
		t.Fatalf("upstream received two credentials: Authorization=%q x-api-key=%q", captured.Get("Authorization"), got)
	}
}

// A DIFFERENT x-api-key value beside a bearer is a second principal, not a
// duplicate. x-api-key must keep winning there, or a shared listener could
// swap the caller's account for its own.
func TestNamedCompatPrefersXAPIKeyWhenTheTwoCredentialsDiffer(t *testing.T) {
	observed := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"fixture","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()

	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer spend.Close()
	srv := New(config.Config{Mode: "record", Compat: map[string]config.CompatConfig{
		"two-keys": {BaseURL: upstream.URL},
	}}, spend, Options{HTTPClient: &http.Client{}})

	req := httptest.NewRequest(http.MethodPost, "/compat/two-keys/v1/messages",
		strings.NewReader(`{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer listener-token")
	req.Header.Set("x-api-key", "caller-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	captured := <-observed
	if got := captured.Get("X-Api-Key"); got != "caller-key" {
		t.Fatalf("caller principal lost: x-api-key=%q Authorization=%q", got, captured.Get("Authorization"))
	}
	if got := captured.Get("Authorization"); got != "" {
		t.Fatalf("upstream received two credentials: Authorization=%q x-api-key=%q", got, captured.Get("X-Api-Key"))
	}
}

// Preserving the scheme must not reclassify the caller. A bearer-scheme
// credential falls to AuthModeOAuth in classifyCredential, and the auth mode
// gates live-zone compression and cache economics — so the duplicate-credential
// case is pinned to the mode it had before the scheme was preserved.
// classifyHeaderAuthMode decides first whenever an Authorization header is
// present, which is exactly when the scheme is now set, so the two agree.
func TestDuplicateCredentialDoesNotChangeResolvedAuthMode(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer one-key")
	headers.Set("x-api-key", "one-key")

	without := gateway.ClassifyResolvedAuthMode(headers, providers.Credential{Mode: "ephemeral_header", Key: "one-key"})
	with := gateway.ClassifyResolvedAuthMode(headers, providers.Credential{Mode: "ephemeral_header", Key: "one-key", Scheme: "bearer"})
	if without != with {
		t.Fatalf("preserving the bearer scheme changed the auth mode: %v -> %v", without, with)
	}
}
