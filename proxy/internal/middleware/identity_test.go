package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ident "github.com/JuliusBrussee/caveman/proxy/internal/identity"
)

const (
	teamAToken  = "team-a-token-0123456789abcdef"
	teamBToken  = "team-b-token-0123456789abcdef"
	legacyToken = "legacy-shared-token-0123456789"
)

// tokenMapRuntime is the fixture runtime behind a real identity resolver: the
// legacy token plus a token map with the given YAML entries.
func tokenMapRuntime(t *testing.T, f fixture, entries string, change func(*Config)) *Runtime {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	if err := os.WriteFile(path, []byte("principals:\n"+entries), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := ident.New(ident.Config{Token: legacyToken, TokenMapFile: path})
	if err != nil {
		t.Fatal(err)
	}
	return withRuntime(t, f, func(c *Config) {
		c.Identify = resolver.Identify
		if change != nil {
			change(c)
		}
	})
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// A1: the namespace is authorized server-side on every route, and a principal
// reaches only what its own authority owns.
func TestNamespaceAuthorizationIsolatesPrincipals(t *testing.T) {
	f := newFixture(t)
	var logs bytes.Buffer
	r := tokenMapRuntime(t, f, "  - {name: team-a, namespaces: [\"team-a/*\"], token_sha256: ["+tokenHash(teamAToken)+"]}\n"+
		"  - {name: team-b, namespaces: [\"team-b/*\"], token_sha256: ["+tokenHash(teamBToken)+"]}\n",
		func(c *Config) { c.Logger = slog.New(slog.NewJSONHandler(&logs, nil)) })
	req := requestFor(r)
	req.Scope.Namespace = "team-a/app"
	w := send(t, r, "optimize", req, teamAToken, clientFeatures)
	plan := decodePlan(t, w)
	if len(plan.Replacements) != 1 {
		t.Fatalf("team-a could not optimize its own namespace: %s", w.Body)
	}
	handle := plan.Replacements[0].RecoveryHandle
	retrieve := RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: handle}
	if w := send(t, r, "retrieve", retrieve, teamAToken, clientFeatures); w.Code != 200 {
		t.Fatalf("owner retrieve: %d %s", w.Code, w.Body)
	}
	forbidden := func(name, path string, body any, token string) {
		t.Helper()
		for _, features := range []string{clientFeatures, ""} {
			w := send(t, r, path, body, token, features)
			if w.Code != 403 || failureCode(t, w.Body.Bytes()) != CodeForbiddenNamespace || bytes.Contains(w.Body.Bytes(), []byte("exact-value")) {
				t.Errorf("%s (features %q): %d %s", name, features, w.Code, w.Body)
			}
		}
	}
	// team-b names team-a's namespace: refused before any lookup, on every route.
	forbidden("foreign retrieve", "retrieve", retrieve, teamBToken)
	forbidden("foreign delete", "sessions/delete", SessionDeleteRequest{SchemaVersion: 1, Scope: req.Scope}, teamBToken)
	forbidden("foreign receipt", "receipts", Receipt{SchemaVersion: 1, Scope: req.Scope, LogicalCallID: "call-1", AttemptID: "attempt-1", EventKind: "dispatch_intent"}, teamBToken)
	forbidden("foreign optimize", "optimize", req, teamBToken)
	outside := req
	outside.Scope.Namespace = "team-b/app"
	forbidden("team-a outside its namespaces", "optimize", outside, teamAToken)
	// Inside its own namespace team-b still cannot use team-a's handle, and the
	// operator token, allowed every namespace, is a different principal too.
	own := retrieve
	own.Scope.Namespace = "team-b/app"
	if w := send(t, r, "retrieve", own, teamBToken, clientFeatures); w.Code != 404 {
		t.Fatalf("foreign handle under team-b's own namespace: %d %s", w.Code, w.Body)
	}
	if w := send(t, r, "retrieve", retrieve, legacyToken, clientFeatures); w.Code != 404 {
		t.Fatalf("operator token reached team-a's handle: %d %s", w.Code, w.Body)
	}
	if w := send(t, r, "retrieve", retrieve, "unknown-token-0123456789", clientFeatures); w.Code != 401 {
		t.Fatalf("unknown token: %d", w.Code)
	}
	// Nothing above deleted team-a's session.
	if w := send(t, r, "retrieve", retrieve, teamAToken, clientFeatures); w.Code != 200 {
		t.Fatalf("team-a lost its session to a refused request: %d %s", w.Code, w.Body)
	}
	if w := send(t, r, "sessions/delete", SessionDeleteRequest{SchemaVersion: 1, Scope: req.Scope}, teamAToken, clientFeatures); w.Code != 200 {
		t.Fatalf("owner delete: %d %s", w.Code, w.Body)
	}
	if w := send(t, r, "retrieve", retrieve, teamAToken, clientFeatures); w.Code != 410 {
		t.Fatalf("after owner delete: %d %s", w.Code, w.Body)
	}
	// The audit trail names who was refused and how they authenticated.
	var refused map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil && entry["code"] == CodeForbiddenNamespace && entry["principal"] == "team-b" {
			refused = entry
			break
		}
	}
	if refused == nil || refused["auth"] != "token_map" || strings.Contains(logs.String(), "exact-value") || strings.Contains(logs.String(), teamBToken) {
		t.Fatalf("audit line for a refused namespace: %v", refused)
	}
}

// Per-principal quota overrides from the token map bound only that principal.
func TestTokenMapQuotaOverridesArePerPrincipal(t *testing.T) {
	f := newFixture(t)
	const tightToken, roomyToken = "tight-token-0123456789abcdef", "roomy-token-0123456789abcdef"
	r := tokenMapRuntime(t, f, "  - {name: tight, namespaces: [\"*\"], token_sha256: ["+tokenHash(tightToken)+"], quota: {requests_per_minute: 1}}\n"+
		"  - {name: small, namespaces: [\"*\"], token_sha256: ["+tokenHash(teamAToken)+"], quota: {rows: 2}}\n"+
		"  - {name: roomy, namespaces: [\"*\"], token_sha256: ["+tokenHash(roomyToken)+"]}\n", nil)
	req := requestFor(r)
	if w := send(t, r, "optimize", req, tightToken, clientFeatures); w.Code != 200 {
		t.Fatalf("first request: %d %s", w.Code, w.Body)
	}
	if w := send(t, r, "optimize", req, tightToken, clientFeatures); w.Code != 429 || failureCode(t, w.Body.Bytes()) != CodeQuotaExceeded {
		t.Fatalf("requests_per_minute override not applied: %d %s", w.Code, w.Body)
	}
	for i := 0; i < 3; i++ {
		if w := send(t, r, "optimize", req, roomyToken, clientFeatures); w.Code != 200 {
			t.Fatalf("one principal's rate quota limited another: %d", w.Code)
		}
	}
	// A plan needs more than two rows: the small principal gets a capacity
	// decision, and the principal next to it is unaffected.
	small := requestFor(r)
	small.Scope.SessionID = "small-session"
	if plan := decodePlan(t, send(t, r, "optimize", small, teamAToken, clientFeatures)); plan.Reason != CodeCapacity || len(plan.Replacements) != 0 {
		t.Fatalf("rows override not applied: %+v", plan)
	}
	small.Scope.SessionID = "roomy-session"
	if plan := decodePlan(t, send(t, r, "optimize", small, roomyToken, clientFeatures)); len(plan.Replacements) != 1 {
		t.Fatalf("one principal's row quota blocked another: %+v", plan)
	}
}

// A5: an operator can declare the store ephemeral. It then reports
// persistent:false and compresses nothing it could not recover after a restart.
func TestEphemeralStoreIsReportedAndNotCompressed(t *testing.T) {
	f := newFixture(t)
	if _, caps := capabilities(t, f.runtime, clientFeatures); caps["persistent"] != true {
		t.Fatal("a file-backed store reported ephemeral")
	}
	r := withRuntime(t, f, func(c *Config) { c.Ephemeral = true })
	if _, caps := capabilities(t, r, clientFeatures); caps["persistent"] != false {
		t.Fatalf("ephemeral store reported persistent: %v", caps["persistent"])
	}
	plan := decodePlan(t, send(t, r, "optimize", requestFor(r), "alice", clientFeatures))
	if len(plan.Replacements) != 0 || plan.Skipped[0].Reason != CodeRecoveryUnavailable {
		t.Fatalf("ephemeral store compressed: %+v", plan)
	}
}

// A query retrieve ranks a middleware-owned original directly: no CCR, not even
// a scratch one, is involved.
func TestQueryRetrieveNarrowsOwnedOriginalWithoutCCR(t *testing.T) {
	f := newFixture(t)
	r := withRuntime(t, f, func(c *Config) { c.Recovery = nil })
	req := requestFor(r)
	handle := optimizeOK(t, r, req).Replacements[0].RecoveryHandle
	w := send(t, r, "retrieve", RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: handle, Query: "ERROR diagnostic"}, "alice", clientFeatures)
	var page RetrieveResponse
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != http.StatusOK {
		t.Fatalf("query retrieve: %d %s", w.Code, w.Body)
	}
	if page.Kind != "excerpt" || !strings.Contains(page.Text, "[ERROR] preserve this diagnostic exactly") || len(page.Text) >= len(req.Segments[0].Content) {
		t.Fatalf("query retrieve did not narrow: kind=%s len=%d", page.Kind, len(page.Text))
	}
}
