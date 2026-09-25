package middleware

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

type fixture struct {
	runtime  *Runtime
	state    *store.Store
	recovery *ccr.Store
	dir      string
}

func openFixture(t *testing.T, dir, mode string) fixture {
	t.Helper()
	s, err := store.Open(filepath.Join(dir, "spend.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ccr.Open(filepath.Join(dir, "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Config{Store: s, Recovery: c, Mode: mode, Limits: Limits{DeadlineMS: 5000}, Principal: func(req *http.Request) (string, error) {
		if req.Header.Get("Authorization") == "Bearer alice" {
			return "alice", nil
		}
		if req.Header.Get("Authorization") == "Bearer bob" {
			return "bob", nil
		}
		return "", fmt.Errorf("denied")
	}})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{r, s, c, dir}
}
func newFixture(t *testing.T) fixture {
	t.Helper()
	f := openFixture(t, t.TempDir(), "compress")
	t.Cleanup(f.close)
	return f
}
func (f fixture) close() { _ = f.state.Close(); _ = f.recovery.Close() }
func noisy() string {
	var b strings.Builder
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&b, "[INFO] reading row %d: café 🌍 exact-value-%03d with verbose repeated details\r\n", i, i)
	}
	b.WriteString("[ERROR] preserve this diagnostic exactly\r\n")
	return b.String()
}
func requestFor(r *Runtime) OptimizeRequest {
	content := noisy()
	transforms := []string{}
	for _, c := range r.caps.Transforms {
		transforms = append(transforms, c.TransformID)
	}
	return OptimizeRequest{SchemaVersion: 1, RequestID: "req-1", LogicalCallID: "call-1", AttemptID: "attempt-1", IdempotencyKey: "idem-1",
		Scope: Scope{"app", "session-1", "main", "0"}, Sequence: 0, Adapter: Adapter{"test", "1", "1", "test-v1"}, Mode: "compress", Policy: Policy{r.caps.PolicyRevision, transforms},
		Segments:        []Segment{{ID: "tool-1", Kind: "tool_result", CacheRegion: "live_zone", Content: content, SHA256: digest([]byte(content)), SourceID: "document-1"}},
		ContextManifest: []ManifestItem{{"msg-0", digest([]byte("user protected"))}, {"msg-1", digest([]byte(content))}},
		RecoveryBinding: &Binding{ID: "binding-1", Kind: "host_tool", ToolName: RecoveryToolName, OverheadText: "An installed native tool with a registered executor."}}
}
func call(t *testing.T, r *Runtime, path string, body any, principal string) (int, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", RoutePrefix+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+principal)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}
func optimizeOK(t *testing.T, r *Runtime, req OptimizeRequest) OptimizeResponse {
	t.Helper()
	status, body := call(t, r, "optimize", req, "alice")
	if status != 200 {
		t.Fatalf("optimize: %d %s", status, body)
	}
	var out OptimizeResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExactRecoveryAndUTF8Pages(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	plan := optimizeOK(t, f.runtime, req)
	if len(plan.Replacements) != 1 {
		t.Fatalf("no transform: %+v", plan)
	}
	replacement := plan.Replacements[0]
	if strings.Contains(replacement.Text, "exact-value-074") {
		t.Fatal("fixture must require recovering absent information")
	}
	if plan.Measurement.Basis != "inferred" || plan.Measurement.VerifiedSavedUSD != 0 || plan.Measurement.TokensAfter >= plan.Measurement.TokensBefore {
		t.Fatal("false measurements")
	}
	pageReq := RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: replacement.RecoveryHandle, Limit: 91}
	var joined strings.Builder
	for {
		status, body := call(t, f.runtime, "retrieve", pageReq, "alice")
		if status != 200 {
			t.Fatalf("retrieve: %d %s", status, body)
		}
		var page RetrieveResponse
		_ = json.Unmarshal(body, &page)
		if !utf8.ValidString(page.Text) || page.OriginalSHA256 != req.Segments[0].SHA256 || page.SourceID != "document-1" || page.TotalBytes != len(noisy()) || page.Complete {
			t.Fatal("invalid page contract")
		}
		joined.WriteString(page.Text)
		if page.NextOffset == nil {
			break
		}
		pageReq.Offset = *page.NextOffset
	}
	if joined.String() != noisy() {
		t.Fatal("exact original bytes changed")
	}
	pageReq.Offset = 0
	pageReq.Limit = DefaultPageBytes
	status, body := call(t, f.runtime, "retrieve", pageReq, "alice")
	var full RetrieveResponse
	_ = json.Unmarshal(body, &full)
	if status != 200 || !full.Complete || full.Text != noisy() {
		t.Fatalf("full recovery: %s", body)
	}
	pageReq.Query = "exact-value-074"
	_, body = call(t, f.runtime, "retrieve", pageReq, "alice")
	var excerpt RetrieveResponse
	_ = json.Unmarshal(body, &excerpt)
	if excerpt.Kind != "excerpt" || excerpt.Complete || excerpt.NextOffset == nil || *excerpt.NextOffset != 0 {
		t.Fatal("query falsely reports complete original")
	}
}

func TestIdenticalDocumentsKeepSeparateGrantsAndOneUniqueCredit(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	second := req.Segments[0]
	second.ID, second.SourceID = "tool-2", "document-2"
	req.Segments = append(req.Segments, second)
	plan := optimizeOK(t, f.runtime, req)
	if len(plan.Replacements) != 2 {
		t.Fatalf("replacements=%d", len(plan.Replacements))
	}
	a, b := plan.Replacements[0], plan.Replacements[1]
	if a.RecoveryHandle == b.RecoveryHandle || a.SourceID == b.SourceID {
		t.Fatal("distinct document identities were merged")
	}
	if !a.UniqueOriginal || b.UniqueOriginal || plan.Measurement.UniqueTokensReduced != a.TokensBefore-a.TokensAfter {
		t.Fatal("identical text was credited more than once")
	}
	req.RequestID, req.IdempotencyKey, req.AttemptID = "req-2", "idem-2", "attempt-2"
	third := second
	third.ID, third.SourceID = "tool-3", "document-3"
	req.Segments = []Segment{third}
	later := optimizeOK(t, f.runtime, req)
	if len(later.Replacements) != 1 || later.Replacements[0].Reused || later.Replacements[0].UniqueOriginal || later.Measurement.UniqueTokensReduced != 0 {
		t.Fatal("new source identity received duplicate content credit")
	}
	engineResult, err := f.runtime.eng.Compress([]byte(third.Content), engine.Options{Mode: engine.ModeCompress, ExternalRecovery: true})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := f.recovery.Summary()
	if err != nil || stats.Totals.Count != 1 || stats.Totals.TokensAfter != engineResult.TokensAfter {
		t.Fatalf("scoped marker overhead leaked into shared CCR metadata: %+v, %v", stats.Totals, err)
	}
	if later.Replacements[0].TokensAfter <= stats.Totals.TokensAfter {
		t.Fatal("request plan omitted its scoped marker overhead")
	}
}

func TestRequiredFieldsCannotBecomeImplicitZeroValues(t *testing.T) {
	f := newFixture(t)
	b, err := json.Marshal(requestFor(f.runtime))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range [][]string{{"sequence"}, {"model"}, {"segments"}, {"context_manifest"}, {"scope", "branch_id"}, {"adapter", "framework_version"}, {"policy", "transforms"}, {"recovery_binding", "overhead_text"}} {
		var value map[string]any
		if err := json.Unmarshal(b, &value); err != nil {
			t.Fatal(err)
		}
		parent := value
		for _, field := range path[:len(path)-1] {
			parent = parent[field].(map[string]any)
		}
		delete(parent, path[len(path)-1])
		status, _ := call(t, f.runtime, "optimize", value, "alice")
		if status != 400 {
			t.Errorf("missing %v accepted: %d", path, status)
		}
	}
	for _, field := range []string{"protected", "opaque"} {
		var value map[string]any
		_ = json.Unmarshal(b, &value)
		segment := value["segments"].([]any)[0].(map[string]any)
		segment[field] = nil
		status, _ := call(t, f.runtime, "optimize", value, "alice")
		if status != 400 {
			t.Errorf("null %s accepted: %d", field, status)
		}
	}
}

func TestScopeAndBrowserAuthorization(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	plan := optimizeOK(t, f.runtime, req)
	retrieve := RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: plan.Replacements[0].RecoveryHandle}
	for _, principal := range []string{"bob", "missing"} {
		code, body := call(t, f.runtime, "retrieve", retrieve, principal)
		if (principal == "bob" && code != 404) || (principal == "missing" && code != 401) || bytes.Contains(body, []byte("exact-value")) {
			t.Fatalf("cross principal leak: %d %s", code, body)
		}
	}
	for _, scope := range []Scope{{"other", "session-1", "main", "0"}, {"app", "other", "main", "0"}, {"app", "session-1", "fork", "0"}, {"app", "session-1", "main", "1"}} {
		retrieve.Scope = scope
		code, _ := call(t, f.runtime, "retrieve", retrieve, "alice")
		if code != 404 {
			t.Fatalf("cross scope grant accepted: %+v", scope)
		}
	}
	r := httptest.NewRequest("GET", RoutePrefix+"capabilities", nil)
	r.Header.Set("Authorization", "Bearer alice")
	r.Header.Set("Origin", "https://attacker.example")
	w := httptest.NewRecorder()
	f.runtime.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("browser origin allowed")
	}
	retrieve.Scope = req.Scope
	retrieve.Handle = "ccr_" + req.Segments[0].SHA256
	if code, _ := call(t, f.runtime, "retrieve", retrieve, "alice"); code != 404 {
		t.Fatal("global CCR hash authorized")
	}
}

func TestTwentyTurnsAcrossRestartAndWorkers(t *testing.T) {
	dir := t.TempDir()
	f := openFixture(t, dir, "compress")
	req := requestFor(f.runtime)
	first := optimizeOK(t, f.runtime, req).Replacements[0]
	f.close()
	f = openFixture(t, dir, "compress")
	defer f.close()
	other := openFixture(t, dir, "compress")
	defer other.close()
	for turn := 1; turn <= 20; turn++ {
		req.Sequence = int64(turn)
		req.RequestID = fmt.Sprintf("req-%d", turn+1)
		req.IdempotencyKey = req.RequestID
		req.Segments[0].CacheRegion = "frozen_prefix"
		req.ContextManifest = append(req.ContextManifest, ManifestItem{fmt.Sprintf("msg-%d", turn+1), digest([]byte(fmt.Sprint(turn)))})
		r := f.runtime
		if turn%2 == 0 {
			r = other.runtime
		}
		out := optimizeOK(t, r, req)
		if len(out.Replacements) != 1 || out.Replacements[0].Text != first.Text || !out.Replacements[0].Reused || out.Measurement.UniqueTokensReduced != 0 {
			t.Fatalf("unstable turn %d", turn)
		}
	}
	req.RequestID = "edited"
	req.IdempotencyKey = "edited"
	req.ContextManifest[0].SHA256 = digest([]byte("edit"))
	if code, body := call(t, f.runtime, "optimize", req, "alice"); code != 409 || !bytes.Contains(body, []byte("epoch_changed")) {
		t.Fatalf("edit accepted: %d %s", code, body)
	}
	req.Scope.CacheEpoch = "explicit-reset"
	req.Segments[0].CacheRegion = "live_zone"
	if len(optimizeOK(t, f.runtime, req).Replacements) != 1 {
		t.Fatal("explicit epoch could not rebuild")
	}
}

func TestConcurrentWritersChooseOneDurableReplacement(t *testing.T) {
	f := newFixture(t)
	other := openFixture(t, f.dir, "compress")
	defer other.close()
	var wg sync.WaitGroup
	results := make(chan OptimizeResponse, 12)
	errors := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := f.runtime
			if i%2 == 0 {
				r = other.runtime
			}
			req := requestFor(r)
			req.RequestID = fmt.Sprintf("req-%d", i)
			req.IdempotencyKey = req.RequestID
			b, _ := json.Marshal(req)
			out, err := r.optimize(context.Background(), "alice", req, digest(b))
			if err != nil {
				errors <- err
				return
			}
			results <- out
		}(i)
	}
	wg.Wait()
	close(errors)
	close(results)
	for err := range errors {
		t.Fatal(err)
	}
	text := ""
	unique := 0
	for out := range results {
		if len(out.Replacements) != 1 {
			t.Fatal("missing replacement")
		}
		p := out.Replacements[0]
		if text != "" && text != p.Text {
			t.Fatal("competing durable choices")
		}
		text = p.Text
		if !p.Reused {
			unique++
		}
	}
	if unique != 1 {
		t.Fatalf("booked %d unique reductions", unique)
	}
}

func TestIdempotencyRejectsDifferentInput(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	first := optimizeOK(t, f.runtime, req)
	second := optimizeOK(t, f.runtime, req)
	if first.Replacements[0].Text != second.Replacements[0].Text {
		t.Fatal("replay changed")
	}
	req.RequestID = "different-input"
	if code, body := call(t, f.runtime, "optimize", req, "alice"); code != 409 || !bytes.Contains(body, []byte("identity_conflict")) {
		t.Fatalf("idempotency collision: %d %s", code, body)
	}
}

func TestConservativeModesDoNotStoreOriginals(t *testing.T) {
	for _, variant := range []string{"record", "no_executor", "protected", "opaque", "unknown_frozen"} {
		t.Run(variant, func(t *testing.T) {
			f := newFixture(t)
			req := requestFor(f.runtime)
			switch variant {
			case "record":
				req.Mode = "record"
			case "no_executor":
				req.RecoveryBinding = nil
			case "protected":
				req.Segments[0].Protected = true
			case "opaque":
				req.Segments[0].Opaque = true
			case "unknown_frozen":
				req.Segments[0].CacheRegion = "frozen_prefix"
			}
			out := optimizeOK(t, f.runtime, req)
			if len(out.Replacements) != 0 {
				t.Fatal("unsafe shortening")
			}
			if (variant == "unknown_frozen" || variant == "no_executor") && out.Stability.Native != "unavailable" {
				t.Fatal("fallback incorrectly claims persistent cache continuity")
			}
			stats, err := f.recovery.Summary()
			if err != nil || stats.StorageBytes != 0 {
				t.Fatal("original stored in bypass mode")
			}
		})
	}
	t.Run("operator_record_cannot_be_overridden", func(t *testing.T) {
		f := openFixture(t, t.TempDir(), "record")
		defer f.close()
		out := optimizeOK(t, f.runtime, requestFor(f.runtime))
		if out.Status != "record" || len(out.Replacements) > 0 {
			t.Fatal("client overrode operator record gate")
		}
	})
}

func TestExpiryDeletionAndFailedRecoveryStayClosed(t *testing.T) {
	for _, cause := range []string{"expired", "deleted", "unavailable"} {
		t.Run(cause, func(t *testing.T) {
			f := newFixture(t)
			req := requestFor(f.runtime)
			out := optimizeOK(t, f.runtime, req)
			if cause == "expired" {
				now := time.Now().Add(25 * time.Hour)
				f.runtime.cfg.Now = func() time.Time { return now }
			}
			if cause == "deleted" {
				code, _ := call(t, f.runtime, "sessions/delete", map[string]any{"schema_version": 1, "scope": req.Scope}, "alice")
				if code != 200 {
					t.Fatal("delete failed")
				}
			}
			if cause == "unavailable" {
				_ = f.recovery.Close()
			}
			code, body := call(t, f.runtime, "retrieve", RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: out.Replacements[0].RecoveryHandle}, "alice")
			if code == 200 || bytes.Contains(body, []byte("exact-value")) {
				t.Fatal("unavailable grant recovered")
			}
			req.RequestID = "next"
			req.IdempotencyKey = "next"
			code, _ = call(t, f.runtime, "optimize", req, "alice")
			if code == 200 {
				t.Fatal("dangling marker issued")
			}
		})
	}
}

func TestProtocolLimitsAndReceipts(t *testing.T) {
	f := newFixture(t)
	for _, variant := range []string{"digest", "version", "policy", "unknown_field", "request_limit"} {
		t.Run(variant, func(t *testing.T) {
			req := requestFor(f.runtime)
			var body any = req
			switch variant {
			case "digest":
				req.Segments[0].SHA256 = strings.Repeat("0", 64)
				body = req
			case "version":
				req.SchemaVersion = 2
				body = req
			case "policy":
				req.Policy.Revision = "made-up"
				body = req
			case "unknown_field":
				body = map[string]any{"tenant_id": "victim"}
			case "request_limit":
				body = map[string]any{"huge": strings.Repeat("x", DefaultRequestBytes+1)}
			}
			code, b := call(t, f.runtime, "optimize", body, "alice")
			if code == 200 || bytes.Contains(b, []byte("exact-value")) {
				t.Fatalf("invalid input accepted %s", variant)
			}
		})
	}
	req := requestFor(f.runtime)
	out := optimizeOK(t, f.runtime, req)
	input, output := int64(100), int64(20)
	receipt := Receipt{SchemaVersion: 1, Scope: req.Scope, LogicalCallID: req.LogicalCallID, AttemptID: req.AttemptID, EventKind: "completed", PlanID: &out.ReplacementSetID,
		Usage: &Usage{Provenance: "client_observed_sdk", Complete: true, InputTokens: &input, OutputTokens: &output}}
	for i := 0; i < 2; i++ {
		if code, b := call(t, f.runtime, "receipts", receipt, "alice"); code != 200 {
			t.Fatalf("receipt: %d %s", code, b)
		}
	}
	output = 21
	if code, _ := call(t, f.runtime, "receipts", receipt, "alice"); code != 409 {
		t.Fatal("duplicate usage not rejected")
	}
	receipt.EventKind = "dispatch_intent"
	if code, _ := call(t, f.runtime, "receipts", receipt, "alice"); code != 400 {
		t.Fatal("dispatch intent claimed provider usage")
	}
}

// Expiry used to share the request's transaction, so every rejected optimize -
// capacity, not_smaller, epoch_changed - rolled back the batch that would have
// freed room. A store at its row cap could then never drain. Reclamation has to
// commit on its own, independently of whether the request that triggered it won.
func TestExpiryCommitsEvenWhenTheRequestFails(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	optimizeOK(t, f.runtime, req)
	ctx := context.Background()
	if err := f.state.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		if err := tx.SaveScope(store.MiddlewareScope{ID: "elapsed", Authority: "elapsed", Manifest: []byte("[]"), ExpiresAt: 10}); err != nil {
			return err
		}
		return tx.SaveChoice("elapsed", "choice", "elapsed-grant", "handle", []byte("replacement"))
	}); err != nil {
		t.Fatal(err)
	}
	// A manifest shorter than the stored one is a rejected epoch, not a retry.
	stale := requestFor(f.runtime)
	stale.RequestID, stale.IdempotencyKey = "stale", "stale"
	stale.ContextManifest = stale.ContextManifest[:1]
	if code, body := call(t, f.runtime, "optimize", stale, "alice"); code == 200 {
		t.Fatalf("truncated manifest was accepted: %s", body)
	}
	if err := f.state.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		if _, _, err := tx.Choice("elapsed", "choice"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("a failed request rolled back the expiry batch: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
