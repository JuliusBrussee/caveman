package middleware

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ident "github.com/JuliusBrussee/caveman/proxy/internal/identity"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// uniqueRequest is requestFor with its own session and segment content.
func uniqueRequest(r *Runtime, session, tag string) OptimizeRequest {
	req := requestFor(r)
	req.Scope.SessionID = session
	req.Segments[0].Content += "[ERROR] unique sentinel " + tag + "\r\n"
	req.Segments[0].SHA256 = digest([]byte(req.Segments[0].Content))
	req.ContextManifest[1].SHA256 = req.Segments[0].SHA256
	return req
}

// nextTurn makes req the following turn of its scope: a new idempotency key,
// sequence and manifest item.
func nextTurn(req OptimizeRequest, id string) OptimizeRequest {
	req.RequestID, req.IdempotencyKey = id, id
	req.Sequence++
	req.ContextManifest = append(append([]ManifestItem{}, req.ContextManifest...), ManifestItem{"msg-" + id, digest([]byte(id))})
	return req
}

func newKey(t *testing.T) *Keyring {
	t.Helper()
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	k, err := LoadKeyring(base64.StdEncoding.EncodeToString(b), "")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// holdSlowBody starts a request as principal whose body never arrives, and
// keeps its connection open until the test ends.
func holdSlowBody(t *testing.T, server *httptest.Server, route, principal string) {
	t.Helper()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	head := "POST " + RoutePrefix + route + " HTTP/1.1\r\nHost: middleware\r\nAuthorization: Bearer " + principal +
		"\r\nContent-Type: application/json\r\n" + HeaderFeatures + ": " + clientFeatures + "\r\nContent-Length: 100000\r\n\r\n{"
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatal(err)
	}
}

// fastRetrieve requires alice's retrieve to answer 200 within a second.
func fastRetrieve(t *testing.T, server *httptest.Server, scope Scope, handle string) {
	t.Helper()
	body, _ := json.Marshal(RetrieveRequest{SchemaVersion: 1, Scope: scope, Handle: handle})
	req, _ := http.NewRequest("POST", server.URL+RoutePrefix+"retrieve", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer alice")
	req.Header.Set(HeaderFeatures, clientFeatures)
	start := time.Now()
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if elapsed := time.Since(start); resp.StatusCode != 200 || elapsed > time.Second {
		t.Fatalf("another principal's retrieve: %d after %s %s", resp.StatusCode, elapsed, answer)
	}
}

// S1: a principal allowed no namespace can use no scoped route, so it is
// refused before it can hold a queue slot, however slowly it sends.
func TestNamespacelessPrincipalHoldsNoQueueSlot(t *testing.T) {
	nobody, err := ident.NewPrincipal("nobody", "test", nil, ident.Quota{})
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	r := withRuntime(t, f, func(c *Config) {
		c.Identify = func(req *http.Request) (ident.Principal, error) {
			if req.Header.Get("Authorization") == "Bearer nobody" {
				return nobody, nil
			}
			return bearerIdentity(req)
		}
	})
	req := requestFor(r)
	handle := optimizeOK(t, r, req).Replacements[0].RecoveryHandle
	if w := send(t, r, "retrieve", RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: handle}, "nobody", clientFeatures); w.Code != 403 || failureCode(t, w.Body.Bytes()) != CodeForbiddenNamespace {
		t.Fatalf("namespace-less principal: %d %s", w.Code, w.Body)
	}
	server := httptest.NewServer(r)
	t.Cleanup(server.Close) // after the slow connections close
	for range cap(r.retrieveQueue) {
		holdSlowBody(t, server, "retrieve", "nobody")
	}
	time.Sleep(200 * time.Millisecond)
	if held := len(r.retrieveQueue); held != 0 {
		t.Fatalf("a principal with no namespace holds %d retrieve slots", held)
	}
	fastRetrieve(t, server, req.Scope, handle)
}

// S1: one principal's slow bodies take at most its share of a queue, so every
// other principal still gets a slot.
func TestOnePrincipalCannotFillAQueue(t *testing.T) {
	f := newFixture(t)
	r := f.runtime
	req := requestFor(r)
	handle := optimizeOK(t, r, req).Replacements[0].RecoveryHandle
	server := httptest.NewServer(r)
	t.Cleanup(server.Close) // after the slow connections close
	for range cap(r.retrieveQueue) {
		holdSlowBody(t, server, "retrieve", "bob")
	}
	share := cap(r.retrieveQueue) / 2
	for deadline := time.Now().Add(3 * time.Second); len(r.retrieveQueue) < share && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if held := len(r.retrieveQueue); held != share {
		t.Fatalf("one principal holds %d of %d retrieve slots, want its share %d", held, cap(r.retrieveQueue), share)
	}
	fastRetrieve(t, server, req.Scope, handle)
}

// S3: with more than one principal configured, one principal filling its
// default share leaves the store open to the others.
func TestOnePrincipalCannotFillTheStore(t *testing.T) {
	eachStore(t, func(t *testing.T, b backend) {
		r := runtimeOn(t, b.store, func(c *Config) { c.Capacity = store.MiddlewareLimits{Rows: 40} })
		full := false
		for i := 0; i < 20 && !full; i++ {
			plan := decodePlan(t, send(t, r, "optimize", uniqueRequest(r, fmt.Sprint("alice-", i), fmt.Sprint("alice-", i)), "alice", clientFeatures))
			full = plan.Reason == CodeCapacity
		}
		if !full {
			t.Fatal("alice never reached capacity")
		}
		if plan := decodePlan(t, send(t, r, "optimize", uniqueRequest(r, "bob-0", "bob-0"), "bob", clientFeatures)); len(plan.Replacements) != 1 {
			t.Fatalf("one principal filled the store for everyone: %+v", plan)
		}
	})
}

type countingStore struct {
	store.MiddlewareStore
	writes atomic.Int64
}

func (c *countingStore) MiddlewareWritable(ctx context.Context) error {
	c.writes.Add(1)
	return c.MiddlewareStore.MiddlewareWritable(ctx)
}

// S13: readiness is unauthenticated, so a probe flood must not become a flood
// of write transactions.
func TestReadinessProbesTheStoreAtMostOncePerSecond(t *testing.T) {
	f := newFixture(t)
	counting := &countingStore{MiddlewareStore: f.state}
	r := withRuntime(t, f, func(c *Config) { c.Store = counting })
	for range 50 {
		if err := r.Ready(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if n := counting.writes.Load(); n != 1 {
		t.Fatalf("50 probes ran %d write transactions", n)
	}
}

// GO-2: a swept grant keeps answering 410 expired until its grace ends, and
// only then becomes unknown.
func TestSweptGrantAnswersExpiredUntilGraceEnds(t *testing.T) {
	eachStore(t, func(t *testing.T, b backend) {
		now := time.Unix(2_000_000_000, 0)
		r := runtimeOn(t, b.store, func(c *Config) { c.Now = func() time.Time { return now } })
		req := requestFor(r)
		retrieve := RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: optimizeOK(t, r, req).Replacements[0].RecoveryHandle}
		now = now.Add(25 * time.Hour)
		if err := r.sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		if left := b.count(t, `SELECT count(*) FROM middleware_choices WHERE length(payload)>0`); left != 0 {
			t.Fatalf("sweep kept %d choice payloads", left)
		}
		for _, h := range []string{clientFeatures, ""} {
			if w := send(t, r, "retrieve", retrieve, "alice", h); w.Code != 410 || failureCode(t, w.Body.Bytes()) != CodeExpired {
				t.Fatalf("swept grant inside grace: %d %s", w.Code, w.Body)
			}
		}
		now = now.Add(time.Duration(store.MiddlewareGraceSeconds) * time.Second)
		if err := r.sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		if w := send(t, r, "retrieve", retrieve, "alice", clientFeatures); w.Code != 404 {
			t.Fatalf("grant after grace: %d %s", w.Code, w.Body)
		}
	})
}

// GO-3: a revoked session stays revoked when the same authority comes back
// under another adapter or serialization revision (a new scope id).
func TestRevokedSessionStaysRevokedUnderANewScopeID(t *testing.T) {
	eachStore(t, func(t *testing.T, b backend) {
		r := runtimeOn(t, b.store, nil)
		req := requestFor(r)
		optimizeOK(t, r, req)
		if w := send(t, r, "sessions/delete", SessionDeleteRequest{SchemaVersion: 1, Scope: req.Scope}, "alice", clientFeatures); w.Code != 200 {
			t.Fatalf("delete: %d %s", w.Code, w.Body)
		}
		for i, change := range []func(*OptimizeRequest){
			func(q *OptimizeRequest) { q.Adapter.ID = "other-adapter" },
			func(q *OptimizeRequest) { q.Adapter.SerializationRevision = "other-revision" },
		} {
			turn := nextTurn(req, fmt.Sprint("after-delete-", i))
			change(&turn)
			for _, h := range []string{clientFeatures, ""} {
				if w := send(t, r, "optimize", turn, "alice", h); w.Code != 410 || failureCode(t, w.Body.Bytes()) != CodeDeleted {
					t.Fatalf("revoked session resurrected through a new scope id: %d %s", w.Code, w.Body)
				}
			}
		}
		if n := b.count(t, `SELECT count(*) FROM middleware_scopes WHERE expires_at>0`); n != 0 {
			t.Fatalf("%d live scopes under a revoked authority", n)
		}
	})
}

// GO-4: a choice whose original no configured key opens is not reused; a new
// choice for the same content stores it again under the current key.
func TestOriginalUnderAMissingKeyIsNotReused(t *testing.T) {
	eachStore(t, func(t *testing.T, b backend) {
		old := newKey(t)
		before := runtimeOn(t, b.store, func(c *Config) { c.Keys = old })
		req := requestFor(before)
		optimizeOK(t, before, req)
		var log bytes.Buffer
		swapped := runtimeOn(t, b.store, func(c *Config) {
			c.Keys, c.Logger = newKey(t), slog.New(slog.NewJSONHandler(&log, nil))
		})
		if !strings.Contains(log.String(), old.active) || !strings.Contains(log.String(), "unavailable key") {
			t.Fatalf("startup did not name the unopenable key id %s:\n%s", old.active, log.String())
		}
		turn := nextTurn(req, "after-swap")
		turn.Segments[0].CacheRegion = "live_zone"
		plan := decodePlan(t, send(t, swapped, "optimize", turn, "alice", clientFeatures))
		if len(plan.Replacements) != 0 || plan.Reason != CodeRecoveryUnavailable {
			t.Fatalf("reused a choice whose original cannot be opened: %+v", plan)
		}
		sibling := turn
		sibling.Adapter.ID, sibling.RequestID, sibling.IdempotencyKey = "sibling", "sibling", "sibling"
		fresh := decodePlan(t, send(t, swapped, "optimize", sibling, "alice", clientFeatures))
		if len(fresh.Replacements) != 1 || fresh.Replacements[0].Reused {
			t.Fatalf("sibling scope: %+v", fresh)
		}
		if page := recovered(t, swapped, sibling.Scope, fresh.Replacements[0].RecoveryHandle, "alice"); page.Text != req.Segments[0].Content {
			t.Fatal("new grant recovered the wrong original")
		}
	})
}

// S10: once a key is configured, plaintext originals are refused unless the
// operator is migrating, and each refusal is counted.
func TestPlaintextOriginalsRefusedOnceAKeyIsConfigured(t *testing.T) {
	eachStore(t, func(t *testing.T, b backend) {
		plain := runtimeOn(t, b.store, nil)
		req := requestFor(plain)
		retrieve := RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: optimizeOK(t, plain, req).Replacements[0].RecoveryHandle}
		key := newKey(t)
		sealed := runtimeOn(t, b.store, func(c *Config) { c.Keys = key })
		if w := send(t, sealed, "retrieve", retrieve, "alice", clientFeatures); w.Code != 503 || failureCode(t, w.Body.Bytes()) != CodeRecoveryUnavailable {
			t.Fatalf("plaintext original opened under a keyring: %d %s", w.Code, w.Body)
		}
		var metrics strings.Builder
		sealed.WriteMetrics(&metrics)
		if !strings.Contains(metrics.String(), `caveman_middleware_plaintext_originals_total{outcome="refused"} 1`) {
			t.Fatalf("refusal not counted:\n%s", metrics.String())
		}
		migrating := runtimeOn(t, b.store, func(c *Config) { c.Keys, c.PlaintextOriginals = key, true })
		if page := recovered(t, migrating, req.Scope, retrieve.Handle, "alice"); page.Text != req.Segments[0].Content {
			t.Fatal("migration flag did not open the plaintext original")
		}
	})
}

// GO-6: a protocol 1.0 grant's original lives in CCR, which delete cannot
// reach; a retried delete, or one after a sweep, must still say so.
func TestRetriedDeleteStillReportsLegacyOriginals(t *testing.T) {
	eachStore(t, func(t *testing.T, b backend) {
		now := time.Unix(2_000_000_000, 0)
		r := runtimeOn(t, b.store, func(c *Config) { c.Now = func() time.Time { return now } })
		legacy := func(session string) Scope {
			scope := Scope{"app", session, "main", "0"}
			if err := b.store.WithMiddleware(t.Context(), func(tx *store.MiddlewareTx) error {
				if err := tx.SaveScope(store.MiddlewareScope{ID: "legacy-" + session, Authority: authority("alice", scope), Manifest: []byte("[]"), ExpiresAt: now.Unix() + 3600, CreatedAt: now.Unix()}); err != nil {
					return err
				}
				return tx.SaveChoice("legacy-"+session, "choice", "cmw_"+strings.Repeat("0", 40)+session[:8], "ccr_handle", []byte(`{}`))
			}); err != nil {
				t.Fatal(err)
			}
			return scope
		}
		deleted := func(scope Scope) bool {
			var out SessionDeleteResponse
			w := send(t, r, "sessions/delete", SessionDeleteRequest{SchemaVersion: 1, Scope: scope}, "alice", clientFeatures)
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
				t.Fatalf("delete: %d %s", w.Code, w.Body)
			}
			return out.OriginalsDeleted
		}
		retried := legacy("retried1")
		if deleted(retried) || deleted(retried) {
			t.Fatal("a retried delete claimed CCR originals were deleted")
		}
		swept := legacy("swept001")
		now = now.Add(2 * time.Hour)
		if err := r.sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		if deleted(swept) {
			t.Fatal("a delete after a sweep claimed CCR originals were deleted")
		}
	})
}

// GO-8: an original is reclaimed with the last scope that references it, not
// kept alive by a sibling scope of the same authority.
func TestOriginalsDoNotOutliveTheirScopeThroughASibling(t *testing.T) {
	eachStore(t, func(t *testing.T, b backend) {
		start := time.Unix(2_000_000_000, 0)
		now := start
		r := runtimeOn(t, b.store, func(c *Config) {
			c.Retention, c.MaxRetention, c.Now = time.Hour, 2*time.Hour, func() time.Time { return now }
		})
		first := requestFor(r)
		optimizeOK(t, r, first)
		now = start.Add(30 * time.Minute)
		sibling := uniqueRequest(r, first.Scope.SessionID, "sibling")
		sibling.Adapter.ID, sibling.RequestID, sibling.IdempotencyKey = "sibling", "sibling", "sibling"
		optimizeOK(t, r, sibling)
		now = start.Add(61 * time.Minute)
		if err := r.sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		stored := func(content string) int64 {
			return b.count(t, `SELECT count(*) FROM middleware_originals WHERE body IS NOT NULL AND digest='`+digest([]byte(content))+`'`)
		}
		if stored(first.Segments[0].Content) != 0 {
			t.Fatal("an expired scope's original outlived it through a sibling scope")
		}
		if stored(sibling.Segments[0].Content) != 1 {
			t.Fatal("the sweep reclaimed a live scope's original")
		}
		if page := recovered(t, r, sibling.Scope, optimizeOK(t, r, sibling).Replacements[0].RecoveryHandle, "alice"); page.Text != sibling.Segments[0].Content {
			t.Fatal("live sibling lost its original")
		}
	})
}

// GO-9: the sweep stops at its time budget, and whatever it left is reported.
func TestSweepBudgetAndBacklogGauge(t *testing.T) {
	eachStore(t, func(t *testing.T, b backend) {
		now := time.Unix(2_000_000_000, 0)
		r := runtimeOn(t, b.store, func(c *Config) { c.Now = func() time.Time { return now } })
		b.exec(t, fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<999)
INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at,created_at) SELECT 'elapsed-'||v,'auth-'||v,'[]',0,%d,1 FROM n`, now.Unix()-1))
		backlog := func() string {
			var m strings.Builder
			r.WriteMetrics(&m)
			for _, line := range strings.Split(m.String(), "\n") {
				if strings.HasPrefix(line, "caveman_middleware_expiry_backlog ") {
					return strings.TrimPrefix(line, "caveman_middleware_expiry_backlog ")
				}
			}
			t.Fatalf("no backlog gauge:\n%s", m.String())
			return ""
		}
		r.sweepBudget = time.Nanosecond
		if err := r.sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		if left := backlog(); left == "0" {
			t.Fatal("a one-pass sweep reported no backlog over 1000 elapsed scopes")
		}
		r.sweepBudget = time.Minute
		if err := r.sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		if left := backlog(); left != "0" {
			t.Fatalf("a full sweep left backlog %s", left)
		}
	})
}

// GO-10 (A2): a stored plan references its replacements' text through their
// choices; it never keeps a second copy for the plan's lifetime.
func TestStoredPlansKeepNoReplacementText(t *testing.T) {
	eachStore(t, func(t *testing.T, b backend) {
		r := runtimeOn(t, b.store, nil)
		plan := optimizeOK(t, r, requestFor(r))
		plan.Replacements[0].Text = ""
		withoutText, _ := json.Marshal(plan)
		if n := b.count(t, `SELECT count(*) FROM middleware_plans WHERE length(payload)=`+fmt.Sprint(len(withoutText))); n != 1 {
			t.Fatalf("the stored plan is not the plan without its replacement text (%d bytes)", len(withoutText))
		}
	})
}

// PY-3: a trimmed or summarized history (or a nested agent on the same scope)
// starts a new epoch for a 1.1 client instead of failing every later turn;
// its grants keep recovering. A 1.0 client keeps 1.0's epoch_changed.
func TestTrimmedHistoryStartsANewEpoch(t *testing.T) {
	eachStore(t, func(t *testing.T, b backend) {
		r := runtimeOn(t, b.store, nil)
		full := uniqueRequest(r, "trim", "turn-1")
		for i := range 4 {
			full.ContextManifest = append(full.ContextManifest, ManifestItem{fmt.Sprint("history-", i), digest([]byte(fmt.Sprint("history ", i)))})
		}
		full.Sequence = int64(len(full.ContextManifest))
		first := decodePlan(t, send(t, r, "optimize", full, "alice", clientFeatures))
		if first.Status != "optimized" {
			t.Fatalf("full history: %+v", first)
		}
		trimmed := uniqueRequest(r, "trim", "turn-2")
		trimmed.RequestID, trimmed.IdempotencyKey = "trimmed", "trimmed"
		trimmed.ContextManifest = append([]ManifestItem{{"summary", digest([]byte("summary of earlier turns"))}}, full.ContextManifest[len(full.ContextManifest)-2:]...)
		trimmed.ContextManifest = append(trimmed.ContextManifest, ManifestItem{"turn-2", trimmed.Segments[0].SHA256})
		trimmed.Sequence = int64(len(trimmed.ContextManifest))
		if w := send(t, r, "optimize", trimmed, "alice", ""); w.Code != 409 || failureCode(t, w.Body.Bytes()) != CodeEpochChanged {
			t.Fatalf("1.0 client lost epoch_changed: %d %s", w.Code, w.Body)
		}
		if plan := decodePlan(t, send(t, r, "optimize", trimmed, "alice", clientFeatures)); plan.Status != "optimized" {
			t.Fatalf("trimmed history: %+v", plan)
		}
		grown := uniqueRequest(r, "trim", "turn-3")
		grown.RequestID, grown.IdempotencyKey = "grown", "grown"
		grown.ContextManifest = append(append([]ManifestItem{}, trimmed.ContextManifest...), ManifestItem{"turn-3", grown.Segments[0].SHA256})
		grown.Sequence = int64(len(grown.ContextManifest))
		if plan := decodePlan(t, send(t, r, "optimize", grown, "alice", clientFeatures)); plan.Status != "optimized" {
			t.Fatalf("growth after the trim: %+v", plan)
		}
		if page := recovered(t, r, full.Scope, first.Replacements[0].RecoveryHandle, "alice"); page.Text != full.Segments[0].Content {
			t.Fatal("a grant from before the new epoch stopped recovering")
		}
		var metrics strings.Builder
		r.WriteMetrics(&metrics)
		if !strings.Contains(metrics.String(), "caveman_middleware_epoch_rebaselines_total 1\n") {
			t.Fatalf("re-baseline not counted:\n%s", metrics.String())
		}
	})
}
