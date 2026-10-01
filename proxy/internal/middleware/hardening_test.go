package middleware

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
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

// GO-4: a choice whose original no configured key opens keeps its bytes (the
// provider cache keeps its prefix): the request's verified content is sealed
// under the current key, and the old grant recovers again.
func TestOriginalUnderAMissingKeyIsResealed(t *testing.T) {
	eachStore(t, func(t *testing.T, b backend) {
		old, current := newKey(t), newKey(t)
		before := runtimeOn(t, b.store, func(c *Config) { c.Keys = old })
		req := requestFor(before)
		first := optimizeOK(t, before, req).Replacements[0]
		var log bytes.Buffer
		swapped := runtimeOn(t, b.store, func(c *Config) {
			c.Keys, c.Logger = current, slog.New(slog.NewJSONHandler(&log, nil))
		})
		if !strings.Contains(log.String(), old.active) || !strings.Contains(log.String(), "unavailable key") {
			t.Fatalf("startup did not name the unopenable key id %s:\n%s", old.active, log.String())
		}
		turn := nextTurn(req, "after-swap")
		for _, h := range []string{clientFeatures, ""} {
			plan := decodePlan(t, send(t, swapped, "optimize", turn, "alice", h))
			if len(plan.Replacements) != 1 || !plan.Replacements[0].Reused || plan.Replacements[0].SHA256 != first.SHA256 {
				t.Fatalf("a lost original bypassed its reused choice: %+v", plan)
			}
			turn = nextTurn(turn, "after-swap-1.0")
		}
		if n := b.count(t, `SELECT count(*) FROM middleware_originals WHERE key_id='`+current.active+`'`); n != 1 {
			t.Fatalf("%d originals under the current key, want the resealed one", n)
		}
		if page := recovered(t, swapped, req.Scope, first.RecoveryHandle, "alice"); page.Text != req.Segments[0].Content {
			t.Fatal("the resealed original did not recover through the old grant")
		}
	})
}

// A protocol 1.0 choice whose CCR original is gone cannot be recovered from
// this store, so only its segment goes unreplaced: a 1.0 client gets a plan,
// not a 503 on every turn until the scope expires.
func TestLostCCROriginalSkipsOnlyItsSegment(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	seedLostCCRChoice(t, f.state, req)
	w := send(t, f.runtime, "optimize", req, "alice", "")
	plan := decodePlan(t, w)
	if len(plan.Replacements) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].Reason != CodeRecoveryUnavailable {
		t.Fatalf("a lost CCR original: %d %+v", w.Code, plan)
	}
}

// seedLostCCRChoice stores what a protocol 1.0 runtime left for req's first
// segment in alice's scope: a choice whose original CCR no longer holds.
func seedLostCCRChoice(t *testing.T, s store.MiddlewareStore, req OptimizeRequest) {
	t.Helper()
	choice, _ := json.Marshal(Replacement{SegmentID: "tool-1", SourceID: "document-1", OriginalSHA256: req.Segments[0].SHA256,
		Text: "[caveman: shortened]", SHA256: digest([]byte("[caveman: shortened]")), TransformID: req.Policy.Transforms[0], RecoveryHandle: "cmw_" + strings.Repeat("cd", 24)})
	scopeID := identity(authority("alice", req.Scope), req.Adapter.ID, req.Adapter.SerializationRevision)
	now := time.Now().Unix()
	manifest, _ := json.Marshal(req.ContextManifest[:1])
	if err := s.WithMiddleware(t.Context(), func(tx *store.MiddlewareTx) error {
		if err := tx.SaveScope(store.MiddlewareScope{ID: scopeID, Authority: authority("alice", req.Scope), Manifest: manifest, ExpiresAt: now + 3600, CreatedAt: now}); err != nil {
			return err
		}
		return tx.SaveChoice(scopeID, choiceKey(req.Segments[0]), "cmw_"+strings.Repeat("cd", 24), "ccr_evicted", choice)
	}); err != nil {
		t.Fatal(err)
	}
}

// racingWriter runs race once, right after the first snapshot read: another
// writer committing between optimize's snapshot and its write transaction.
type racingWriter struct {
	store.MiddlewareStore
	race func()
}

func (s *racingWriter) ReadMiddleware(ctx context.Context, fn func(*store.MiddlewareTx) error) error {
	err := s.MiddlewareStore.ReadMiddleware(ctx, fn)
	if s.race != nil {
		s.race()
		s.race = nil
	}
	return err
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

// FX2: readiness is unauthenticated and cached, so a caller that hangs up must
// not have its cancellation cached as "store not writable" for every probe,
// and a flood of concurrent probes still costs one write.
func TestCancelledProbeDoesNotMakeTheReplicaUnready(t *testing.T) {
	f := newFixture(t)
	counting := &countingStore{MiddlewareStore: f.state}
	r := withRuntime(t, f, func(c *Config) { c.Store = counting })
	gone, hangUp := context.WithCancel(t.Context())
	hangUp()
	_ = r.Ready(gone)
	if err := r.Ready(t.Context()); err != nil {
		t.Fatalf("a cancelled prober made the next probe fail: %v", err)
	}
	r.readyAt = time.Time{} // the cache expired
	done := make(chan error)
	for range 50 {
		go func() { done <- r.Ready(t.Context()) }()
	}
	for range 50 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if n := counting.writes.Load(); n != 2 {
		t.Fatalf("2 probe rounds ran %d write transactions", n)
	}
}

// FX2: two principals each holding their full share of both queues still
// leave a slot for a principal holding none: its optimize and retrieve answer
// within budget.
func TestTwoPrincipalsCannotFillAQueue(t *testing.T) {
	f := newFixture(t)
	r := withRuntime(t, f, func(c *Config) {
		c.Identify = func(req *http.Request) (ident.Principal, error) {
			return everyNamespace(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
		}
	})
	req := requestFor(r)
	handle := optimizeOK(t, r, req).Replacements[0].RecoveryHandle
	server := httptest.NewServer(r)
	t.Cleanup(server.Close) // after the slow connections close
	for _, attacker := range []string{"bob", "carol"} {
		for range cap(r.queue) {
			holdSlowBody(t, server, "optimize", attacker)
		}
		for range cap(r.retrieveQueue) {
			holdSlowBody(t, server, "retrieve", attacker)
		}
	}
	time.Sleep(500 * time.Millisecond)
	fastRetrieve(t, server, req.Scope, handle)
	body, _ := json.Marshal(uniqueRequest(r, "alice-2", "alice-2"))
	post, _ := http.NewRequest("POST", server.URL+RoutePrefix+"optimize", bytes.NewReader(body))
	post.Header.Set("Content-Type", "application/json")
	post.Header.Set("Authorization", "Bearer alice")
	post.Header.Set(HeaderFeatures, clientFeatures)
	start := time.Now()
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(post)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if elapsed := time.Since(start); resp.StatusCode != 200 || elapsed > time.Second || !strings.Contains(string(answer), `"replacements":[{`) {
		t.Fatalf("a third principal's optimize: %d after %s %s", resp.StatusCode, elapsed, answer)
	}
}

// failingStore fails the failAt-th write transaction (counting from 1) with err.
type failingStore struct {
	store.MiddlewareStore
	writes, failAt int
	err            error
}

func (f *failingStore) WithMiddleware(ctx context.Context, fn func(*store.MiddlewareTx) error) error {
	if f.writes++; f.writes == f.failAt {
		return f.err
	}
	return f.MiddlewareStore.WithMiddleware(ctx, fn)
}

// seedOriginals stores n more originals under req's authority.
func seedOriginals(t *testing.T, s store.MiddlewareStore, req OptimizeRequest, n int) {
	t.Helper()
	originals := make([]store.MiddlewareOriginal, n)
	for i := range originals {
		originals[i] = store.MiddlewareOriginal{Digest: fmt.Sprintf("%064d", i), Body: []byte("original")}
	}
	if err := s.WithMiddleware(t.Context(), func(tx *store.MiddlewareTx) error {
		tx.Principal = "alice"
		_, err := tx.SaveOriginals(authority("alice", req.Scope), originals)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func deleteSession(t *testing.T, r *Runtime, scope Scope) (*httptest.ResponseRecorder, SessionDeleteResponse) {
	t.Helper()
	var out SessionDeleteResponse
	w := send(t, r, "sessions/delete", SessionDeleteRequest{SchemaVersion: 1, Scope: scope}, "alice", clientFeatures)
	if w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("delete body: %s", w.Body)
	}
	return w, out
}

// P1: sessions/delete commits the revocation before it purges in batches. A
// delete that fails midway already answers "deleted" everywhere, and its retry
// finishes the purge. A store conflict is a failed delete: 503, never 409.
func TestInterruptedDeleteIsRevokedAndResumes(t *testing.T) {
	eachStore(t, func(t *testing.T, b backend) {
		faulty := &failingStore{MiddlewareStore: b.store}
		r := runtimeOn(t, faulty, nil)
		req := requestFor(r)
		plan := optimizeOK(t, r, req)
		seedOriginals(t, b.store, req, 2*deleteBatch+100)
		faulty.writes, faulty.failAt, faulty.err = 0, 3, errors.New("injected outage") // revoke, one batch, then fail
		if w, _ := deleteSession(t, r, req.Scope); w.Code != 503 || failureCode(t, w.Body.Bytes()) != CodeRuntimeUnavailable {
			t.Fatalf("interrupted delete: %d %s", w.Code, w.Body)
		}
		retrieve := RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: plan.Replacements[0].RecoveryHandle}
		if w := send(t, r, "retrieve", retrieve, "alice", clientFeatures); w.Code != 410 || failureCode(t, w.Body.Bytes()) != CodeDeleted {
			t.Fatalf("grant after an interrupted delete: %d %s", w.Code, w.Body)
		}
		left := b.count(t, `SELECT count(*) FROM middleware_originals`)
		if left == 0 {
			t.Fatal("the interrupted delete finished anyway; the test proves nothing")
		}
		faulty.writes, faulty.failAt, faulty.err = 0, 1, store.ErrMiddlewareConflict
		if w, _ := deleteSession(t, r, req.Scope); w.Code != 503 || failureCode(t, w.Body.Bytes()) != CodeRuntimeUnavailable {
			t.Fatalf("delete conflict: %d %s", w.Code, w.Body)
		}
		faulty.failAt = 0
		w, out := deleteSession(t, r, req.Scope)
		if w.Code != 200 || !out.OriginalsDeleted || out.Deleted.Originals != left || out.Deleted.Scopes != 0 {
			t.Fatalf("resumed delete: %d %s, want the %d originals left", w.Code, w.Body, left)
		}
		if n := b.count(t, `SELECT (SELECT count(*) FROM middleware_originals)+(SELECT count(*) FROM middleware_choices WHERE length(payload)>0)+(SELECT count(*) FROM middleware_scopes WHERE length(manifest)>0)`); n != 0 {
			t.Fatalf("resumed delete left %d rows of content", n)
		}
	})
}

// P1: a session far larger than one transaction can delete in 500ms is deleted
// by one request under the default limits: delete has retrieve's deadline and
// purges in batches.
func TestLargeSessionDeletesUnderTheDefaultDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 500 MiB of originals")
	}
	eachStore(t, func(t *testing.T, b backend) {
		r := runtimeOn(t, b.store, func(c *Config) { c.Limits = Limits{} })
		req := requestFor(r)
		optimizeOK(t, r, req)
		auth := authority("alice", req.Scope)
		const originals, size = 2000, 256 << 10
		if _, ok := b.store.(*store.PostgresMiddleware); ok {
			// EXTERNAL keeps TOAST from compressing the repeated test bodies away.
			b.exec(t, `ALTER TABLE middleware_originals ALTER COLUMN body SET STORAGE EXTERNAL`)
			b.exec(t, fmt.Sprintf(`INSERT INTO middleware_originals(authority,digest,body,key_id,principal)
SELECT '%s', lpad(v::text,64,'0'), decode(repeat(md5(v::text),%d),'hex'), '', 'alice' FROM generate_series(1,%d) v`, auth, size/16, originals))
		} else {
			b.exec(t, fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_originals(authority,digest,body,key_id,principal) SELECT '%s', printf('%%064d',v), randomblob(%d), '', 'alice' FROM n`, originals, auth, size))
		}
		// A call that runs out of its deadline answers 504 and a retry resumes
		// (§12), so a loaded runner may need a second call; what must never
		// happen again is a delete that cannot finish at all.
		// A timed-out call's committed batches are in no response's counts, so
		// completeness is checked in the store; a single call must count all.
		start, calls := time.Now(), 0
		for ; calls < 3; calls++ {
			w, out := deleteSession(t, r, req.Scope)
			if w.Code == 200 {
				if calls == 0 && out.Deleted.Originals != originals+1 {
					t.Fatalf("large delete counted %d of %d originals", out.Deleted.Originals, originals+1)
				}
				break
			}
			if w.Code != 504 {
				t.Fatalf("large delete call %d: %d %s", calls+1, w.Code, w.Body)
			}
		}
		if left := b.count(t, fmt.Sprintf(`SELECT count(*) FROM middleware_originals WHERE authority='%s'`, auth)); calls == 3 || left != 0 {
			t.Fatalf("large delete after %s and %d calls: %d originals left", time.Since(start), calls, left)
		}
		t.Logf("deleted %d originals of %d KiB in %s over %d call(s)", originals, size>>10, time.Since(start), calls+1)
	})
}

// §12: once deleted, a receipt answers "deleted" and stores nothing.
func TestReceiptAfterDeleteAnswersDeleted(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	optimizeOK(t, f.runtime, req)
	receipt := Receipt{SchemaVersion: 1, Scope: req.Scope, LogicalCallID: req.LogicalCallID, AttemptID: req.AttemptID, EventKind: "dispatch_intent"}
	if w := send(t, f.runtime, "receipts", receipt, "alice", clientFeatures); w.Code != 200 {
		t.Fatalf("receipt: %d %s", w.Code, w.Body)
	}
	if w, _ := deleteSession(t, f.runtime, req.Scope); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	receipt.AttemptID = "attempt-after-delete"
	if w := send(t, f.runtime, "receipts", receipt, "alice", clientFeatures); w.Code != 410 || failureCode(t, w.Body.Bytes()) != CodeDeleted {
		t.Fatalf("receipt after delete: %d %s", w.Code, w.Body)
	}
	var n int
	if err := f.db(t).QueryRow(`SELECT count(*) FROM middleware_receipts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d receipts stored under a revoked authority (%v)", n, err)
	}
}

// A retried receipt is deduplicated on (logical_call_id, attempt_id,
// event_kind): first write wins, so a changed body is not a retryable conflict
// the client could never resolve.
func TestRetriedReceiptWithADifferentBodyKeepsTheFirst(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	receipt := Receipt{SchemaVersion: 1, Scope: req.Scope, LogicalCallID: req.LogicalCallID, AttemptID: req.AttemptID, EventKind: "failed"}
	first := send(t, f.runtime, "receipts", receipt, "alice", clientFeatures)
	sha := strings.Repeat("ab", 32)
	receipt.ProviderRequestSHA256 = &sha
	retried := send(t, f.runtime, "receipts", receipt, "alice", clientFeatures)
	if first.Code != 200 || retried.Code != 200 || first.Body.String() != retried.Body.String() {
		t.Fatalf("receipts: %d %s / %d %s", first.Code, first.Body, retried.Code, retried.Body)
	}
	var n int
	if err := f.db(t).QueryRow(`SELECT count(*) FROM middleware_receipts WHERE CAST(payload AS TEXT) NOT LIKE '%` + sha + `%'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stored receipts other than the first: %d (%v)", n, err)
	}
}

// §1: an unknown route is 404 whatever its body, before it takes a quota count.
func TestUnknownRouteIsNotFoundBeforeQuotaAndBody(t *testing.T) {
	f := newFixture(t)
	r := withRuntime(t, f, func(c *Config) { c.Limits.QuotaRequestsPerMinute = 1 })
	for range 3 {
		req := httptest.NewRequest("POST", RoutePrefix+"no-such-route", strings.NewReader("not json"))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("Authorization", "Bearer alice")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 404 || failureCode(t, w.Body.Bytes()) != CodeNotFound {
			t.Fatalf("unknown route: %d %s", w.Code, w.Body)
		}
	}
	if w := send(t, r, "optimize", requestFor(r), "alice", clientFeatures); w.Code != 200 {
		t.Fatalf("unknown routes spent the quota: %d %s", w.Code, w.Body)
	}
}

// §4: encoding/json would match "Scope" to scope; a key that differs from a
// defined field only by case is refused, at any depth, not given its meaning.
func TestCaseFoldedKeysAreRefused(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	plan := optimizeOK(t, f.runtime, req)
	handle := plan.Replacements[0].RecoveryHandle
	post := func(body string, features string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", RoutePrefix+"retrieve", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer alice")
		if features != "" {
			r.Header.Set(HeaderFeatures, features)
		}
		w := httptest.NewRecorder()
		f.runtime.ServeHTTP(w, r)
		return w
	}
	right := `{"namespace":"app","session_id":"session-1","branch_id":"main","cache_epoch":"0"}`
	wrong := `{"namespace":"app","session_id":"someone-else","branch_id":"main","cache_epoch":"0"}`
	for _, h := range []string{clientFeatures, ""} {
		for _, body := range []string{
			`{"schema_version":1,"scope":` + wrong + `,"Scope":` + right + `,"handle":"` + handle + `","offset":0,"limit":0,"query":""}`,
			`{"schema_version":1,"scope":{"namespace":"app","session_id":"x","Session_ID":"session-1","branch_id":"main","cache_epoch":"0"},"handle":"` + handle + `","offset":0,"limit":0,"query":""}`,
		} {
			if w := post(body, h); w.Code != 400 || failureCode(t, w.Body.Bytes()) != CodeInvalidRequest {
				t.Fatalf("case-folded key (%q): %d %s", h, w.Code, w.Body)
			}
		}
	}
	if w := post(`{"schema_version":1,"scope":`+right+`,"handle":"`+handle+`","offset":0,"limit":0,"query":"","unknown":1}`, clientFeatures); w.Code != 200 {
		t.Fatalf("an unknown field no longer passes the tolerant reader: %d %s", w.Code, w.Body)
	}
}

// §6: an unsupported schema_version is unsupported_version on every route.
func TestDeleteWithAnUnsupportedVersion(t *testing.T) {
	f := newFixture(t)
	w := send(t, f.runtime, "sessions/delete", SessionDeleteRequest{SchemaVersion: 2, Scope: Scope{"app", "s", "main", "0"}}, "alice", clientFeatures)
	if w.Code != 400 || failureCode(t, w.Body.Bytes()) != CodeUnsupportedVersion {
		t.Fatalf("delete schema_version 2: %d %s", w.Code, w.Body)
	}
}

// A plan row exists for idempotent replay only, so it lasts the replay
// window, not the whole retention.
func TestPlanRowsLastTheReplayWindow(t *testing.T) {
	f := newFixture(t)
	now := time.Unix(2_000_000_000, 0)
	r := withRuntime(t, f, func(c *Config) { c.Now = func() time.Time { return now } })
	req := requestFor(r)
	first := optimizeOK(t, r, req)
	var expires int64
	if err := f.db(t).QueryRow(`SELECT expires_at FROM middleware_plans`).Scan(&expires); err != nil || expires != now.Unix()+planReplaySeconds {
		t.Fatalf("plan expires_at=%d (%v), want now+%d", expires, err, planReplaySeconds)
	}
	now = now.Add(planReplaySeconds*time.Second - time.Second)
	if replay := optimizeOK(t, r, req); !reflect.DeepEqual(replay.Replacements, first.Replacements) || replay.ReplacementSetID != first.ReplacementSetID {
		t.Fatalf("replay inside the window changed the plan: %+v", replay)
	}
	// Past the window the plan no longer binds the key, swept or not: another
	// body plans afresh, and from then on that plan is the one that binds.
	now = now.Add(2 * time.Second)
	again := req
	again.RequestID = "after-the-window"
	if replay := optimizeOK(t, r, again); len(replay.Replacements) != 1 || replay.Replacements[0].SHA256 != first.Replacements[0].SHA256 {
		t.Fatalf("a re-plan after the window changed the replacements: %+v", replay)
	}
	if code, body := call(t, r, "optimize", req, "alice"); code != 409 || !bytes.Contains(body, []byte("identity_conflict")) {
		t.Fatalf("the replaced plan still binds: %d %s", code, body)
	}
}
