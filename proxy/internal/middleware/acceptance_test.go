package middleware

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// These records follow assertions against the real Engine, stores and HTTP
// handler. They are scoped criterion observations, not native-framework or
// provider-quality certificates. A separate runner captures build and replay.
func acceptance(t *testing.T, component string, ids []string, observation map[string]any) {
	t.Helper()
	nonce := os.Getenv("CAVEMAN_MIDDLEWARE_ACCEPTANCE_NONCE")
	if nonce != "" && !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(nonce) {
		t.Fatal("invalid acceptance execution nonce")
	}
	observation["outcome"] = "observed"
	observation["evidence_scope"] = "actual_go_runtime_fixture"
	for _, id := range ids {
		data, err := json.Marshal(map[string]any{"acceptance_id": id, "component_id": id + "::" + component,
			"assertion": component, "test_id": "proxy/internal/middleware/acceptance_test.go::" + t.Name(),
			"run_nonce": nonce, "process_id": os.Getpid(), "observation": observation})
		if err != nil {
			t.Fatal(err)
		}
		t.Log("CAVEMAN_MIDDLEWARE_OBSERVATION " + string(data))
	}
}

func failureCode(t *testing.T, body []byte) string {
	t.Helper()
	var parsed struct {
		SchemaVersion int     `json:"schema_version"`
		Error         Failure `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.SchemaVersion != 1 || parsed.Error.Code == "" || len(body) > 128 {
		t.Fatalf("not a bounded typed failure: %q (%v)", body, err)
	}
	return parsed.Error.Code
}

func recovered(t *testing.T, r *Runtime, scope Scope, handle, principal string) RetrieveResponse {
	t.Helper()
	status, body := call(t, r, "retrieve", RetrieveRequest{SchemaVersion: 1, Scope: scope, Handle: handle}, principal)
	var page RetrieveResponse
	if err := json.Unmarshal(body, &page); status != 200 || err != nil || !page.Complete || !utf8.ValidString(page.Text) || digest([]byte(page.Text)) != page.OriginalSHA256 {
		t.Fatalf("exact original recovery failed: %d %s (%v)", status, body, err)
	}
	return page
}

func TestMiddlewareAcceptanceCapabilitiesAndSemanticCorpus(t *testing.T) {
	f := newFixture(t)
	cfg := f.runtime.cfg
	cfg.Limits = Limits{}
	defaults, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	request := httptest.NewRequest("GET", RoutePrefix+"capabilities", nil)
	request.Header.Set("Authorization", "Bearer alice")
	defaults.ServeHTTP(w, request)
	var caps Capabilities
	if err = json.Unmarshal(w.Body.Bytes(), &caps); err != nil || w.Code != 200 {
		t.Fatal("capability discovery failed", err)
	}
	if caps.SchemaVersion != 1 || caps.RuntimeBuild == "" || !caps.Persistent || !caps.Recovery || caps.PolicyRevision == "" || caps.RetentionSeconds <= 0 ||
		caps.Limits != (Limits{DeadlineMS: 500, RequestBytes: 2 << 20, SegmentBytes: 512 << 10, PageBytes: 256 << 10}) {
		t.Fatalf("incomplete or incorrect capabilities: %+v", caps)
	}
	engineCaps := engine.New(f.recovery, nil).Capabilities()
	if !reflect.DeepEqual(caps.Transforms, engineCaps) {
		t.Fatal("middleware advertises transforms outside the actual Engine")
	}
	advertised := map[string]bool{}
	for _, transform := range caps.Transforms {
		advertised[transform.TransformID] = true
	}
	invalid := requestFor(f.runtime)
	invalid.Policy.Transforms = []string{"invented.lossless.v1"}
	status, body := call(t, f.runtime, "optimize", invalid, "alice")
	if status != 400 || failureCode(t, body) != "unknown_capability" {
		t.Fatal("unknown Engine capability accepted")
	}
	acceptance(t, "capability_discovery", []string{"runtime.R3.AC02"}, map[string]any{"runtime_build": caps.RuntimeBuild, "protocol": caps.SchemaVersion,
		"policy_revision": caps.PolicyRevision, "transform_count": len(caps.Transforms), "limits": caps.Limits,
		"persistent": caps.Persistent, "recovery": caps.Recovery, "retention_seconds": caps.RetentionSeconds})

	jsonRows, csv, yaml := []map[string]any{}, "id,amount,status\r\n", "environments:\n"
	for i := 0; i < 150; i++ {
		amount := i * 3
		if i == 74 {
			amount = -991
		}
		jsonRows = append(jsonRows, map[string]any{"id": i, "amount": amount, "active": i%3 != 0})
		csv += fmt.Sprintf("%d,%d,%s\r\n", i, amount, []string{"ready", "blocked"}[i%2])
		yaml += fmt.Sprintf("  environment-%d:\n    replicas: %d\n    endpoint: service-%d.example\n", i, i%4+1, i)
	}
	jsonBody, _ := json.Marshal(jsonRows)
	code := "def exact_total(rows):\n    return sum(row['amount'] for row in rows if row['active'])\n" + strings.Repeat("# Keep every source line and café 🌍.\r\n", 150)
	patch := "diff --git a/service.py b/service.py\n--- a/service.py\n+++ b/service.py\n@@ -1,2 +1,2 @@\n-if count < 74:\n+if count <= 74:\n     return count\n" + strings.Repeat(" context line remains exact\n", 100)
	citedSource := "Policy effective 2026-01-01.\n" + noisy() + "The café 🌍 retention exception is 37 days.\n"
	citedQuote := "café 🌍 retention exception is 37 days"
	citationOffset := strings.Index(citedSource, citedQuote)
	if citationOffset < 0 || citedSource[citationOffset:citationOffset+len(citedQuote)] != citedQuote {
		t.Fatal("invalid original-byte citation fixture")
	}
	corpus := []struct {
		name, text string
		protected  bool
	}{
		{"json_enumeration_and_arithmetic", string(jsonBody), false}, {"exact_code_copy", code, false},
		{"patch_generation", patch, false}, {"csv_anomaly", csv, false}, {"yaml_drift", yaml, false},
		{"long_logs_missing_fact", noisy(), false}, {"unicode_crlf", strings.Repeat("[INFO] café 🌍 中文 עברית exact bytes\r\n", 140), false},
		{"original_offset_citation", citedSource, true}, {"identical_document_a", noisy(), false}, {"identical_document_b", noisy(), false},
	}
	observations := []map[string]any{}
	lossy := 0
	for i, item := range corpus {
		req := requestFor(f.runtime)
		req.Scope.SessionID, req.RequestID, req.IdempotencyKey = "semantic-"+item.name, fmt.Sprint("request-", i), fmt.Sprint("request-", i)
		req.Segments[0].Content, req.Segments[0].SHA256, req.Segments[0].SourceID = item.text, digest([]byte(item.text)), item.name
		req.Segments[0].Protected = item.protected
		req.ContextManifest[1].SHA256 = req.Segments[0].SHA256
		originalWire, _ := json.Marshal(req)
		plan := optimizeOK(t, f.runtime, req)
		observed := map[string]any{"case": item.name, "source_sha256": req.Segments[0].SHA256, "utf8_bytes": len(item.text), "status": plan.Status, "reason": plan.Reason}
		if len(plan.Replacements) == 1 {
			lossy++
			view := plan.Replacements[0]
			page := recovered(t, f.runtime, req.Scope, view.RecoveryHandle, "alice")
			if page.Text != item.text || page.SourceID != item.name || !advertised[view.TransformID] || item.protected {
				t.Fatal("corpus source, identity or protected content changed", item.name)
			}
			if plan.Measurement.TokensBefore-plan.Measurement.TokensAfter <= plan.Measurement.RecoveryOverheadTokens ||
				plan.Measurement.Scope != "segment" || plan.Measurement.OverheadCoverage != "segment_and_declared_recovery_tool" ||
				plan.Measurement.Tokenizer == "" || plan.Measurement.Basis != "inferred" || plan.Measurement.VerifiedSavedUSD != 0 {
				t.Fatal("invalid net reduction/accounting evidence", item.name)
			}
			// Random scoped handle spelling affects tokenization. Assert the actual
			// measurements above, then record stable predicates for independent replay.
			observed["recovered_sha256"], observed["transform_id"] = page.OriginalSHA256, view.TransformID
			observed["measurement"] = map[string]any{"tokens_before": plan.Measurement.TokensBefore, "tokenizer": plan.Measurement.Tokenizer,
				"scope": plan.Measurement.Scope, "overhead_coverage": plan.Measurement.OverheadCoverage, "net_reduction_positive": true,
				"basis": plan.Measurement.Basis, "verified_saved_usd": plan.Measurement.VerifiedSavedUSD}
			if item.name == "long_logs_missing_fact" && strings.Contains(view.Text, "exact-value-074") {
				t.Fatal("log fixture did not actually omit the required fact")
			}
		} else if len(plan.Replacements) != 0 || plan.Reason == "" {
			t.Fatal("corpus needs a defined unchanged fallback", item.name)
		}
		unchangedWire, _ := json.Marshal(req)
		if !bytes.Equal(originalWire, unchangedWire) {
			t.Fatal("runtime mutated original source request")
		}
		if item.protected {
			if plan.Status != "bypassed" || plan.Reason != "protected" || req.Segments[0].Content[citationOffset:citationOffset+len(citedQuote)] != citedQuote {
				t.Fatal("original-byte citation no longer identifies its exact quote")
			}
			observed["original_quote_sha256"], observed["original_byte_offset"] = digest([]byte(citedQuote)), citationOffset
		}
		observations = append(observations, observed)
	}
	if lossy == 0 {
		t.Fatal("semantic test was only no-op traffic")
	}
	acceptance(t, "engine_semantic_corpus", []string{"runtime.R4.AC01", "runtime.R4.AC04", "runtime.R4.AC05", "proof.R3.AC01"},
		map[string]any{"cases": observations, "actual_compressed_cases": lossy, "unknown_transform_http_status": status})
}

func TestMiddlewareAcceptanceOneHundredAuthenticatedScopes(t *testing.T) {
	f := newFixture(t)
	type saved struct {
		scope                  Scope
		principal, handle, sha string
		request                OptimizeRequest
		view                   Replacement
	}
	sessions := make([]saved, 100)
	for i := range sessions {
		principal := []string{"alice", "bob"}[i%2]
		req := requestFor(f.runtime)
		req.Scope = Scope{fmt.Sprint("app-", i%4), fmt.Sprint("session-", i), "main", "0"}
		req.Segments[0].Content += fmt.Sprintf("[ERROR] only session %d has sentinel %d\n", i, i*17)
		req.Segments[0].SHA256 = digest([]byte(req.Segments[0].Content))
		req.ContextManifest[1].SHA256 = req.Segments[0].SHA256
		code, body := call(t, f.runtime, "optimize", req, principal)
		var plan OptimizeResponse
		if err := json.Unmarshal(body, &plan); err != nil || code != 200 || len(plan.Replacements) != 1 {
			t.Fatalf("scope %d not optimized: %d %s", i, code, body)
		}
		sessions[i] = saved{req.Scope, principal, plan.Replacements[0].RecoveryHandle, req.Segments[0].SHA256, req, plan.Replacements[0]}
	}
	denials, unknown, namespaceAttempts := 0, 0, 0
	ownReuses, principalReuseDenials, namespaceReuseDenials := 0, 0, 0
	for i, item := range sessions {
		if page := recovered(t, f.runtime, item.scope, item.handle, item.principal); page.OriginalSHA256 != item.sha {
			t.Fatal("own scoped original changed")
		}
		other := sessions[(i+1)%len(sessions)]
		for _, request := range []RetrieveRequest{
			{SchemaVersion: 1, Scope: other.scope, Handle: item.handle},
			{SchemaVersion: 1, Scope: item.scope, Handle: item.handle},
			{SchemaVersion: 1, Scope: other.scope, Handle: "ccr_" + item.sha},
		} {
			code, body := call(t, f.runtime, "retrieve", request, other.principal)
			if code != 404 || failureCode(t, body) != "not_found" || bytes.Contains(body, []byte("sentinel")) {
				t.Fatal("cross-principal or global hash access", i, code, string(body))
			}
			denials++
		}
		changed := item.scope
		changed.Namespace = other.scope.Namespace
		code, body := call(t, f.runtime, "retrieve", RetrieveRequest{SchemaVersion: 1, Scope: changed, Handle: item.handle}, item.principal)
		if code != 404 || failureCode(t, body) != "not_found" {
			t.Fatal("namespace change granted content")
		}
		namespaceAttempts++
		code, body = call(t, f.runtime, "retrieve", RetrieveRequest{SchemaVersion: 1, Scope: item.scope, Handle: item.handle}, "unauthenticated")
		if code != 401 || failureCode(t, body) != "unauthorized" {
			t.Fatal("unauthenticated runtime content access")
		}
		unknown++
		reuse := item.request
		reuse.Segments = append([]Segment(nil), reuse.Segments...)
		reuse.Segments[0].CacheRegion = "frozen_prefix"
		reuse.RequestID, reuse.IdempotencyKey, reuse.AttemptID = "reuse", "reuse", "reuse"
		code, body = call(t, f.runtime, "optimize", reuse, item.principal)
		var plan OptimizeResponse
		if err := json.Unmarshal(body, &plan); err != nil || code != 200 || len(plan.Replacements) != 1 || !plan.Replacements[0].Reused ||
			plan.Replacements[0].Text != item.view.Text || plan.Replacements[0].RecoveryHandle != item.handle || plan.Measurement.UniqueTokensReduced != 0 {
			t.Fatal("authorized frozen replacement failed to reuse its own choice", i, code, string(body))
		}
		ownReuses++
		for _, attempt := range []struct {
			scope     Scope
			principal string
		}{{item.scope, other.principal}, {changed, item.principal}} {
			reuse.Scope = attempt.scope
			code, body = call(t, f.runtime, "optimize", reuse, attempt.principal)
			plan = OptimizeResponse{}
			if err := json.Unmarshal(body, &plan); err != nil || code != 200 || len(plan.Replacements) != 0 || plan.Status != "bypassed" || plan.Reason != "cache_state_unavailable" || bytes.Contains(body, []byte(item.handle)) {
				t.Fatal("another trust scope reused a frozen replacement", i, code, string(body))
			}
			if attempt.principal != item.principal {
				principalReuseDenials++
			} else {
				namespaceReuseDenials++
			}
		}
	}
	acceptance(t, "authenticated_scope_isolation", []string{"runtime.R9.AC01", "runtime.R9.AC04", "proof.R3.AC04"}, map[string]any{
		"interleaved_scopes": len(sessions), "authenticated_principals": 2, "own_exact_recoveries": len(sessions),
		"cross_principal_or_global_hash_denials": denials, "same_principal_namespace_denials": namespaceAttempts, "unauthenticated_denials": unknown,
		"own_frozen_reuses": ownReuses, "cross_principal_frozen_reuse_denials": principalReuseDenials, "same_principal_namespace_reuse_denials": namespaceReuseDenials})
}

func TestMiddlewareAcceptanceBrowserAndFetchURLRefusal(t *testing.T) {
	f := newFixture(t)
	observed := []map[string]any{}
	for _, header := range []struct{ key, value string }{{"Origin", "https://untrusted.example"}, {"Sec-Fetch-Site", "cross-site"}} {
		request := httptest.NewRequest("GET", RoutePrefix+"capabilities", nil)
		request.Header.Set("Authorization", "Bearer alice")
		request.Header.Set(header.key, header.value)
		response := httptest.NewRecorder()
		f.runtime.ServeHTTP(response, request)
		if response.Code != 403 || failureCode(t, response.Body.Bytes()) != "forbidden_origin" || response.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("browser access was not refused")
		}
		observed = append(observed, map[string]any{"header": header.key, "status": response.Code})
	}
	for _, field := range []string{"url", "fetch_url", "tenant_id", "authorization", "api_key"} {
		request := requestFor(f.runtime)
		wire, _ := json.Marshal(request)
		var object map[string]any
		_ = json.Unmarshal(wire, &object)
		object[field] = "https://127.0.0.1/secret-source?token=must-not-appear"
		status, body := call(t, f.runtime, "optimize", object, "alice")
		if status != 400 || failureCode(t, body) != "invalid_request" || bytes.Contains(body, []byte("must-not-appear")) {
			t.Fatal("untrusted optimization argument accepted or reflected", field)
		}
		observed = append(observed, map[string]any{"field": field, "status": status, "body_bytes": len(body)})
	}
	if _, err := New(Config{Store: f.state, Recovery: f.recovery, Mode: "compress"}); err == nil {
		t.Fatal("runtime was constructed without authenticated authority resolver")
	}
	acceptance(t, "browser_and_fetch_url_refusal", []string{"runtime.R3.AC04", "runtime.R8.AC06", "runtime.R9.AC07"}, map[string]any{"refusals": observed, "missing_principal_resolver_rejected": true})
}

func TestMiddlewareAcceptanceDefaultDeadlineIncludesQueue(t *testing.T) {
	f := newFixture(t)
	cfg := f.runtime.cfg
	cfg.Limits = Limits{}
	runtime, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(runtime.queue); i++ {
		runtime.queue <- struct{}{}
	}
	budget := time.Duration(runtime.cfg.Limits.DeadlineMS) * time.Millisecond
	start := time.Now()
	status, body := call(t, runtime, "optimize", requestFor(runtime), "alice")
	elapsed := time.Since(start)
	if status != 504 || failureCode(t, body) != "deadline" || elapsed > budget+25*time.Millisecond || elapsed < budget-10*time.Millisecond {
		t.Fatalf("queue multiplied or escaped default shared deadline: %s, %d %s", elapsed, status, body)
	}
	for i := 0; i < cap(runtime.queue); i++ {
		<-runtime.queue
	}
	// No queue slot, store plan, or background Engine work was added by timeout.
	stats, err := f.recovery.Summary()
	if err != nil || stats.StorageBytes != 0 || len(runtime.queue) != 0 {
		t.Fatal("expired queued request retained work or content")
	}
	acceptance(t, "queued_deadline", []string{"runtime.R10.AC02"}, map[string]any{"configured_deadline_ms": runtime.cfg.Limits.DeadlineMS,
		"queue_capacity": cap(runtime.queue), "result_status": status, "reason": "deadline", "within_25ms_scheduler_tolerance": true,
		"remaining_queue_slots_used": len(runtime.queue), "stored_original_bytes": stats.StorageBytes})
}

func TestMiddlewareAcceptanceCapacityRetainsLiveOriginals(t *testing.T) {
	f := openFixture(t, t.TempDir(), "compress")
	_ = f.recovery.Close()
	limited, err := ccr.OpenWithBudget(filepath.Join(f.dir, "ccr.db"), 128<<10)
	if err != nil {
		f.close()
		t.Fatal(err)
	}
	f.recovery = limited
	cfg := f.runtime.cfg
	cfg.Recovery = limited
	f.runtime, err = New(cfg)
	defer f.close()
	if err != nil {
		t.Fatal(err)
	}
	firstRequest := requestFor(f.runtime)
	first := optimizeOK(t, f.runtime, firstRequest)
	if len(first.Replacements) != 1 {
		t.Fatal("capacity fixture has no initial retained original")
	}
	capacityStatus, accepted := 0, 1
	for i := 1; i < 40; i++ {
		req := requestFor(f.runtime)
		req.Scope.SessionID = fmt.Sprint("capacity-", i)
		req.Segments[0].Content += fmt.Sprint("[ERROR] unique capacity sentinel ", i)
		req.Segments[0].SHA256 = digest([]byte(req.Segments[0].Content))
		req.ContextManifest[1].SHA256 = req.Segments[0].SHA256
		status, body := call(t, f.runtime, "optimize", req, "alice")
		if status != 200 {
			if failureCode(t, body) != "capacity" {
				t.Fatalf("unexpected capacity result: %d %s", status, body)
			}
			capacityStatus = status
			break
		}
		accepted++
	}
	if capacityStatus == 0 {
		t.Fatal("fixture did not reach actual SQLite/CCR storage capacity")
	}
	page := recovered(t, f.runtime, firstRequest.Scope, first.Replacements[0].RecoveryHandle, "alice")
	if page.Text != noisy() {
		t.Fatal("capacity evicted a live original")
	}
	_ = f.recovery.Close()
	f.recovery, err = ccr.OpenWithBudget(filepath.Join(f.dir, "ccr.db"), 128<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer f.recovery.Close()
	cfg.Recovery = f.recovery
	f.runtime, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reopened := recovered(t, f.runtime, firstRequest.Scope, first.Replacements[0].RecoveryHandle, "alice")
	if reopened.Text != page.Text {
		t.Fatal("live source disappeared after reopening full storage")
	}
	acceptance(t, "retained_capacity_original", []string{"runtime.R5.AC07"}, map[string]any{"retention_seconds": f.runtime.caps.RetentionSeconds,
		"configured_ccr_bytes": 128 << 10, "accepted_originals_before_capacity": accepted, "capacity_http_status": capacityStatus,
		"first_original_sha256": page.OriginalSHA256, "reopened_original_sha256": reopened.OriginalSHA256,
		"store_reopened": true, "process_restarted": false, "retention_elapsed": false})
}

func TestMiddlewareAcceptancePreparedPlansAndReceipts(t *testing.T) {
	f := newFixture(t)
	request := requestFor(f.runtime)
	plan := optimizeOK(t, f.runtime, request)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(f.dir, "spend.db"))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	count := func() int {
		var value int
		if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM middleware_receipts").Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if count() != 0 || plan.Measurement.Basis != "inferred" || plan.Measurement.VerifiedSavedUSD != 0 {
		t.Fatal("prepared plan recorded provider usage or verified saving")
	}
	intent := Receipt{SchemaVersion: 1, Scope: request.Scope, LogicalCallID: request.LogicalCallID, AttemptID: request.AttemptID,
		EventKind: "dispatch_intent", PlanID: &plan.ReplacementSetID}
	if code, body := call(t, f.runtime, "receipts", intent, "alice"); code != 200 {
		t.Fatalf("dispatch intent refused: %s", body)
	}
	input, output := int64(321), int64(17)
	intent.Usage = &Usage{Provenance: "client_observed_sdk", Complete: true, InputTokens: &input, OutputTokens: &output}
	if code, _ := call(t, f.runtime, "receipts", intent, "alice"); code != 400 || count() != 1 {
		t.Fatal("dispatch intent invented provider usage")
	}
	complete := intent
	complete.EventKind = "completed"
	for i := 0; i < 2; i++ {
		if code, body := call(t, f.runtime, "receipts", complete, "alice"); code != 200 {
			t.Fatalf("completed usage refused: %s", body)
		}
	}
	if count() != 2 {
		t.Fatal("duplicate callback added another usage receipt")
	}
	complete.Usage = &Usage{Provenance: "client_observed_sdk", Complete: true, InputTokens: &input}
	if code, _ := call(t, f.runtime, "receipts", complete, "alice"); code != 400 {
		t.Fatal("missing output usage falsely completed")
	}
	incomplete := Receipt{SchemaVersion: 1, Scope: request.Scope, LogicalCallID: request.LogicalCallID, AttemptID: "attempt-incomplete",
		EventKind: "cancelled", PlanID: &plan.ReplacementSetID}
	if code, body := call(t, f.runtime, "receipts", incomplete, "alice"); code != 200 {
		t.Fatalf("incomplete attempt refused: %s", body)
	}
	rows, err := db.QueryContext(context.Background(), "SELECT payload FROM middleware_receipts ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	observed := []map[string]any{}
	for rows.Next() {
		var raw []byte
		var record Receipt
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		if record.EventKind != "completed" && record.Usage != nil {
			t.Fatal("unknown provider usage became measured zero")
		}
		if record.PlanID == nil || *record.PlanID != plan.ReplacementSetID || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(*record.PlanID) ||
			record.LogicalCallID != request.LogicalCallID || record.Scope != request.Scope {
			t.Fatal("receipt lost its actual plan, call or scope identity")
		}
		observed = append(observed, map[string]any{"event_kind": record.EventKind, "attempt_id": record.AttemptID,
			"logical_call_id": record.LogicalCallID, "usage": record.Usage, "plan_reference_matches": true, "scope_matches": true})
	}
	if err = rows.Err(); err != nil || len(observed) != 3 {
		t.Fatal("unexpected receipt rows", err)
	}
	acceptance(t, "receipt_storage", []string{"runtime.R11.AC01", "runtime.R11.AC02", "runtime.R11.AC05", "runtime.R11.AC06"}, map[string]any{
		"prepared_plan_provider_receipts": 0, "tokenizer": plan.Measurement.Tokenizer, "measurement_scope": plan.Measurement.Scope,
		"basis": plan.Measurement.Basis, "verified_saved_usd": plan.Measurement.VerifiedSavedUSD, "rows": observed,
		"duplicate_completed_calls": 2, "persisted_completed_rows": 1, "missing_usage_rejected_as_complete": true})
}

func TestMiddlewareAcceptanceBoundedExactRecoveryPages(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	plan := optimizeOK(t, f.runtime, req)
	if len(plan.Replacements) != 1 || strings.Contains(plan.Replacements[0].Text, "exact-value-074") {
		t.Fatal("paging fixture did not remove the fact requiring exact recovery")
	}
	request := RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: plan.Replacements[0].RecoveryHandle, Limit: 91}
	read := func(request RetrieveRequest) RetrieveResponse {
		t.Helper()
		code, body := call(t, f.runtime, "retrieve", request, "alice")
		var page RetrieveResponse
		if err := json.Unmarshal(body, &page); code != 200 || err != nil {
			t.Fatalf("page refused: %d %s (%v)", code, body, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil || fields["complete"] == nil || fields["next_offset"] == nil ||
			page.OriginalSHA256 != req.Segments[0].SHA256 || page.SourceID != req.Segments[0].SourceID ||
			page.TotalBytes != len(noisy()) || !utf8.ValidString(page.Text) || len(page.Text) > request.Limit {
			t.Fatal("page omitted its original identity, byte bound, or explicit continuation field")
		}
		return page
	}
	var joined strings.Builder
	pages, boundaryAdjustments := 0, 0
	for {
		page := read(request)
		if page.Kind != "original_page" || page.Complete || page.Offset != joined.Len() || len(page.Text) == 0 ||
			page.Text != noisy()[page.Offset:page.Offset+len(page.Text)] {
			t.Fatal("partial page changed bytes or claimed a complete original")
		}
		joined.WriteString(page.Text)
		pages++
		if page.NextOffset == nil {
			if joined.Len() != len(noisy()) {
				t.Fatal("missing continuation before the end of the original")
			}
			break
		}
		if *page.NextOffset != joined.Len() || *page.NextOffset <= request.Offset {
			t.Fatal("page continuation skipped or repeated original bytes")
		}
		if len(page.Text) < request.Limit {
			boundaryAdjustments++
		}
		request.Offset = *page.NextOffset
	}
	if joined.String() != noisy() || boundaryAdjustments == 0 {
		t.Fatal("pagination did not exercise exact UTF-8 boundary adjustment")
	}
	request.Offset, request.Limit = 0, DefaultPageBytes
	full := read(request)
	if !full.Complete || full.NextOffset != nil || full.Text != noisy() {
		t.Fatal("complete original differs from joined pages")
	}
	// Query the unique row term; shared words such as "exact-value" also rank
	// earlier rows and can legitimately consume a bounded excerpt first.
	request.Query, request.Limit = "074", 512
	excerpt := read(request)
	if excerpt.Kind != "excerpt" || excerpt.Complete || excerpt.NextOffset == nil || *excerpt.NextOffset != 0 ||
		!strings.Contains(excerpt.Text, "exact-value-074") {
		t.Fatal("bounded search omitted its match or advertised a complete original")
	}
	invalid := []struct {
		name          string
		offset, limit int
		query, code   string
	}{
		{"over_page_cap", 0, DefaultPageBytes + 1, "", "payload_limit"},
		{"under_one_rune", 0, 3, "", "payload_limit"},
		{"negative_offset", -1, 91, "", "invalid_request"},
		{"middle_of_rune", strings.Index(noisy(), "🌍") + 1, 91, "", "invalid_range"},
		{"past_original", len(noisy()) + 1, 91, "", "invalid_range"},
		{"over_query_cap", 0, 91, strings.Repeat("q", 1025), "invalid_request"},
		{"query_offset", 1, 91, "exact-value-074", "invalid_range"},
	}
	refusals := []map[string]any{}
	for _, row := range invalid {
		bad := request
		bad.Offset, bad.Limit, bad.Query = row.offset, row.limit, row.query
		status, body := call(t, f.runtime, "retrieve", bad, "alice")
		code := failureCode(t, body)
		if status == 200 || code != row.code || bytes.Contains(body, []byte("exact-value")) {
			t.Fatalf("unsafe range %s: %d %s", row.name, status, body)
		}
		refusals = append(refusals, map[string]any{"case": row.name, "code": code})
	}
	acceptance(t, "bounded_exact_recovery_pages", []string{"runtime.R5.AC05"}, map[string]any{
		"source_sha256": req.Segments[0].SHA256, "joined_sha256": digest([]byte(joined.String())), "total_bytes": len(noisy()),
		"page_limit": 91, "page_cap": DefaultPageBytes, "pages": pages, "utf8_boundary_adjustments": boundaryAdjustments,
		"full_original_complete": full.Complete, "excerpt_kind": excerpt.Kind, "excerpt_complete": excerpt.Complete,
		"excerpt_continuation": *excerpt.NextOffset, "query_limit": 512, "query_match_present": strings.Contains(excerpt.Text, "exact-value-074"), "refusals": refusals})
}

func TestMiddlewareAcceptanceSourceIdentityAndUniqueCredit(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	req.Segments[0].SourceID = "document-a"
	second := req.Segments[0]
	second.ID, second.SourceID = "tool-2", "document-b"
	req.Segments = append(req.Segments, second)
	first := optimizeOK(t, f.runtime, req)
	if len(first.Replacements) != 2 || first.Replacements[0].RecoveryHandle == first.Replacements[1].RecoveryHandle {
		t.Fatal("distinct sources merged their grants")
	}
	sources := []map[string]any{}
	for i, replacement := range first.Replacements {
		page := recovered(t, f.runtime, req.Scope, replacement.RecoveryHandle, "alice")
		if page.Text != noisy() || replacement.SourceID != req.Segments[i].SourceID || page.SourceID != req.Segments[i].SourceID || page.OriginalSHA256 != req.Segments[i].SHA256 {
			t.Fatal("equal text lost its distinct original citation identity")
		}
		sources = append(sources, map[string]any{"source_id": page.SourceID, "original_sha256": page.OriginalSHA256, "recovered_sha256": digest([]byte(page.Text)), "utf8_bytes": len(page.Text)})
	}
	if !first.Replacements[0].UniqueOriginal || first.Replacements[1].UniqueOriginal || first.Measurement.UniqueTokensReduced != first.Replacements[0].TokensBefore-first.Replacements[0].TokensAfter {
		t.Fatal("same original bytes booked more than one content reduction")
	}
	req.RequestID, req.IdempotencyKey, req.AttemptID = "reuse", "reuse", "attempt-reuse"
	reused := optimizeOK(t, f.runtime, req)
	if len(reused.Replacements) != 2 || reused.Measurement.UniqueTokensReduced != 0 {
		t.Fatal("reused replacements rebooked unique reduction")
	}
	for i, replacement := range reused.Replacements {
		if !replacement.Reused || replacement.UniqueOriginal || replacement.Text != first.Replacements[i].Text {
			t.Fatal("reuse changed bytes or booked another original")
		}
	}
	req.RequestID, req.IdempotencyKey, req.AttemptID = "new-source", "new-source", "attempt-new-source"
	req.Segments = []Segment{second}
	req.Segments[0].ID, req.Segments[0].SourceID = "tool-3", "document-c"
	later := optimizeOK(t, f.runtime, req)
	if len(later.Replacements) != 1 || later.Replacements[0].Reused || later.Replacements[0].UniqueOriginal || later.Measurement.UniqueTokensReduced != 0 {
		t.Fatal("new citation source rebooked identical original bytes")
	}
	for _, plan := range []OptimizeResponse{first, reused, later} {
		if plan.Measurement.Basis != "inferred" || plan.Measurement.Scope != "segment" || plan.Measurement.Tokenizer == "" || plan.Measurement.VerifiedSavedUSD != 0 ||
			plan.Measurement.OverheadCoverage != "segment_and_declared_recovery_tool" || plan.Measurement.TokensBefore <= plan.Measurement.TokensAfter {
			t.Fatal("segment reuse measurement exceeds its evidence")
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(f.dir, "spend.db"))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var originalRows int
	if err := db.QueryRow("SELECT count(*) FROM middleware_originals").Scan(&originalRows); err != nil || originalRows != 1 {
		t.Fatal("persistent unique-content ledger duplicated an original", err)
	}
	acceptance(t, "distinct_recovered_source_identity", []string{"runtime.R12.AC04"}, map[string]any{"sources": sources, "distinct_scoped_handles": true})
	acceptance(t, "unique_content_credit_on_reuse", []string{"runtime.R11.AC03"}, map[string]any{
		"initial_sources": 2, "reused_replacements": len(reused.Replacements), "initial_unique_credit_positive": first.Measurement.UniqueTokensReduced > 0,
		"reused_unique_tokens_reduced": reused.Measurement.UniqueTokensReduced, "new_source_unique_tokens_reduced": later.Measurement.UniqueTokensReduced,
		"persisted_original_credit_rows": originalRows, "measurement_scope": reused.Measurement.Scope, "tokenizer": reused.Measurement.Tokenizer,
		"overhead_coverage": reused.Measurement.OverheadCoverage, "basis": reused.Measurement.Basis, "verified_saved_usd": reused.Measurement.VerifiedSavedUSD})
}

func TestMiddlewareAcceptanceScopedIdempotency(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	first := optimizeOK(t, f.runtime, req)
	if len(first.Replacements) != 1 {
		t.Fatal("idempotency fixture produced no replacement")
	}
	repeated := 0
	for i := 0; i < 3; i++ {
		next := optimizeOK(t, f.runtime, req)
		if next.ReplacementSetID != first.ReplacementSetID || !reflect.DeepEqual(next.Replacements, first.Replacements) {
			t.Fatal("identical scoped replay changed chosen replacements")
		}
		repeated++
	}
	changed := req
	changed.Segments = append([]Segment(nil), req.Segments...)
	changed.ContextManifest = append([]ManifestItem(nil), req.ContextManifest...)
	changed.Segments[0].Content += "[ERROR] different original\r\n"
	changed.Segments[0].SHA256 = digest([]byte(changed.Segments[0].Content))
	changed.ContextManifest[1].SHA256 = changed.Segments[0].SHA256
	status, body := call(t, f.runtime, "optimize", changed, "alice")
	if status != 409 || failureCode(t, body) != "identity_conflict" {
		t.Fatalf("changed original reused an idempotency key: %d %s", status, body)
	}
	page := recovered(t, f.runtime, req.Scope, first.Replacements[0].RecoveryHandle, "alice")
	if page.Text != req.Segments[0].Content {
		t.Fatal("rejected conflicting input damaged the accepted original")
	}
	acceptance(t, "scoped_idempotent_replacement", []string{"runtime.R2.AC04"}, map[string]any{
		"identical_scoped_replays": repeated, "replacement_bytes_equal": true, "replacement_set_equal": true,
		"changed_original_http_status": status, "changed_original_error": failureCode(t, body),
		"original_sha256": req.Segments[0].SHA256, "recovered_after_rejection_sha256": digest([]byte(page.Text))})
}

// Observe the committed choice before forwarding replacement bytes to a caller.
// This independent read-only connection cannot see another connection's pending
// transaction, unlike checking the database after the handler has returned.
type acceptanceDurableRecorder struct {
	*httptest.ResponseRecorder
	db        *sql.DB
	persisted bool
	err       error
}

func (r *acceptanceDurableRecorder) Write(body []byte) (int, error) {
	if r.Code == 200 {
		var plan OptimizeResponse
		r.err = json.Unmarshal(body, &plan)
		if r.err == nil && len(plan.Replacements) == 1 {
			var raw []byte
			r.err = r.db.QueryRow("SELECT payload FROM middleware_choices WHERE grant_id=?", plan.Replacements[0].RecoveryHandle).Scan(&raw)
			if r.err == nil {
				var durable Replacement
				r.err = json.Unmarshal(raw, &durable)
				r.persisted = r.err == nil && durable.Text == plan.Replacements[0].Text && durable.SHA256 == plan.Replacements[0].SHA256
			}
		}
	}
	return r.ResponseRecorder.Write(body)
}

func TestMiddlewareAcceptanceConcurrentDurableWinner(t *testing.T) {
	f := newFixture(t)
	other := openFixture(t, f.dir, "compress")
	defer other.close()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(f.dir, "spend.db"))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	type result struct {
		plan      OptimizeResponse
		persisted bool
		err       error
	}
	results := make(chan result, 12)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(12)
	for i := 0; i < 12; i++ {
		go func(i int) {
			runtime := f.runtime
			if i%2 == 1 {
				runtime = other.runtime
			}
			req := requestFor(runtime)
			req.RequestID, req.IdempotencyKey, req.AttemptID = fmt.Sprint("concurrent-", i), fmt.Sprint("concurrent-", i), fmt.Sprint("attempt-", i)
			body, err := json.Marshal(req)
			ready.Done()
			<-start
			if err != nil {
				results <- result{err: err}
				return
			}
			httpReq := httptest.NewRequest("POST", RoutePrefix+"optimize", bytes.NewReader(body))
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("Authorization", "Bearer alice")
			response := &acceptanceDurableRecorder{ResponseRecorder: httptest.NewRecorder(), db: db}
			runtime.ServeHTTP(response, httpReq)
			var plan OptimizeResponse
			if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil || response.Code != 200 || len(plan.Replacements) != 1 {
				results <- result{err: fmt.Errorf("concurrent optimize failed: %d %s", response.Code, response.Body.String())}
				return
			}
			results <- result{plan: plan, persisted: response.persisted, err: response.err}
		}(i)
	}
	ready.Wait()
	close(start)
	first, persisted, unique := "", 0, 0
	for i := 0; i < 12; i++ {
		row := <-results
		if row.err != nil {
			t.Fatal(row.err)
		}
		text := row.plan.Replacements[0].Text
		if first != "" && text != first {
			t.Fatal("concurrent requests received competing replacements")
		}
		first = text
		if !row.persisted {
			t.Fatal("response preceded its durable choice")
		}
		persisted++
		if !row.plan.Replacements[0].Reused {
			unique++
		}
	}
	var choices, plans int
	if err := db.QueryRow("SELECT count(*) FROM middleware_choices").Scan(&choices); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM middleware_plans").Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if choices != 1 || plans != 12 || unique != 1 {
		t.Fatalf("durable winner mismatch: choices=%d plans=%d unique=%d", choices, plans, unique)
	}
	acceptance(t, "concurrent_durable_replacement", []string{"runtime.R6.AC03"}, map[string]any{
		"concurrent_requests": 12, "runtime_instances": 2, "separate_processes": false, "equal_replacement_bytes": true,
		"responses_with_existing_durable_choice": persisted, "observed_before_response_write": true,
		"persisted_choice_rows": choices, "persisted_plan_rows": plans, "first_publications": unique})
}

const restartChildPrefix = "CAVEMAN_MIDDLEWARE_RESTART_CHILD "

type restartCommand struct {
	Action         string `json:"action"`
	Turn           int    `json:"turn"`
	Challenge      string `json:"challenge"`
	ParentSequence int    `json:"parent_sequence"`
}

type restartView struct {
	Replacement
	RecoveredSHA256 string `json:"recovered_sha256"`
	RecoveredBytes  int    `json:"recovered_bytes"`
}

type restartEvent struct {
	Kind             string         `json:"kind"`
	Role             string         `json:"role"`
	ProcessID        int            `json:"process_id"`
	ParentProcessID  int            `json:"parent_process_id"`
	ParentNonce      string         `json:"parent_nonce"`
	Nonce            string         `json:"nonce"`
	ExecutableSHA256 string         `json:"executable_sha256"`
	Turn             int            `json:"turn"`
	Challenge        string         `json:"challenge"`
	Manifest         []ManifestItem `json:"manifest,omitempty"`
	Views            []restartView  `json:"views,omitempty"`
}

func restartChallenge(t *testing.T) string {
	t.Helper()
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}

func restartRequest(r *Runtime, role string, turn int) OptimizeRequest {
	req := requestFor(r)
	req.Scope.SessionID = "twenty-turn-process-restart"
	req.Sequence = int64(turn)
	req.RequestID = fmt.Sprintf("%s-turn-%02d", role, turn)
	req.IdempotencyKey, req.AttemptID = req.RequestID, req.RequestID
	req.Segments = nil
	req.ContextManifest = req.ContextManifest[:1]
	for index := 0; index <= turn; index++ {
		content := noisy() + fmt.Sprintf("[ERROR] original appended at turn %02d\r\n", index)
		segment := Segment{ID: fmt.Sprintf("tool-turn-%02d", index), Kind: "tool_result", CacheRegion: "frozen_prefix",
			Content: content, SHA256: digest([]byte(content)), SourceID: fmt.Sprintf("document-turn-%02d", index)}
		if index == turn {
			segment.CacheRegion = "live_zone"
		}
		req.Segments = append(req.Segments, segment)
		req.ContextManifest = append(req.ContextManifest, ManifestItem{fmt.Sprintf("msg-%02d", index+1), segment.SHA256})
	}
	return req
}

// This helper runs only as a separately executed native test process. The parent
// supplies every prepare/release gate through stdin; neither stored output nor
// a claimed PID substitutes for starting and waiting on the real executable.
func TestMiddlewareRestartWorkerProcess(t *testing.T) {
	role := os.Getenv("CAVEMAN_MIDDLEWARE_RESTART_ROLE")
	if role == "" {
		t.Skip("only the parent acceptance test starts this child protocol")
	}
	if role != "seed" && role != "worker-a" && role != "worker-b" {
		t.Fatal("invalid restart child role")
	}
	nonce := os.Getenv("CAVEMAN_MIDDLEWARE_RESTART_NONCE")
	parentPID, err := strconv.Atoi(os.Getenv("CAVEMAN_MIDDLEWARE_RESTART_PARENT_PID"))
	if err != nil || parentPID != os.Getppid() || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(nonce) {
		t.Fatal("child protocol has no actual parent or fresh challenge")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	f := openFixture(t, os.Getenv("CAVEMAN_MIDDLEWARE_RESTART_DIRECTORY"), "compress")
	emit := func(event restartEvent) {
		event.Role, event.ProcessID, event.ParentProcessID = role, os.Getpid(), os.Getppid()
		event.ParentNonce, event.Nonce = os.Getenv("CAVEMAN_MIDDLEWARE_ACCEPTANCE_NONCE"), nonce
		event.ExecutableSHA256 = digest(program)
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println(restartChildPrefix + string(data))
	}
	emit(restartEvent{Kind: "ready", Turn: -1})
	decoder := json.NewDecoder(os.Stdin)
	first, last := 0, 9
	if role != "seed" {
		first, last = 10, 19
	}
	for turn := first; turn <= last; turn++ {
		var prepare, release restartCommand
		if err := decoder.Decode(&prepare); err != nil || prepare.Action != "prepare" || prepare.Turn != turn ||
			!regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(prepare.Challenge) {
			t.Fatal("invalid child prepare command", err)
		}
		req := restartRequest(f.runtime, role, turn)
		emit(restartEvent{Kind: "prepared", Turn: turn, Challenge: prepare.Challenge, Manifest: req.ContextManifest})
		if err := decoder.Decode(&release); err != nil || release.Action != "release" || release.Turn != turn || release.Challenge != prepare.Challenge {
			t.Fatal("child ran without its matching release gate", err)
		}
		plan := optimizeOK(t, f.runtime, req)
		if len(plan.Replacements) != turn+1 {
			t.Fatal("child did not recover every earlier frozen choice", turn, len(plan.Replacements))
		}
		views := make([]restartView, len(plan.Replacements))
		for index, view := range plan.Replacements {
			original := req.Segments[index]
			page := recovered(t, f.runtime, req.Scope, view.RecoveryHandle, "alice")
			if view.SegmentID != original.ID || view.SourceID != original.SourceID || view.OriginalSHA256 != original.SHA256 ||
				view.SHA256 != digest([]byte(view.Text)) || !strings.Contains(view.Text, "handle="+view.RecoveryHandle) ||
				page.Text != original.Content || page.SourceID != original.SourceID || index < turn && (!view.Reused || view.UniqueOriginal) {
				t.Fatal("child changed a frozen marker, original identity, or exact recovery", turn, index)
			}
			views[index] = restartView{view, digest([]byte(page.Text)), len(page.Text)}
		}
		emit(restartEvent{Kind: "completed", Turn: turn, Challenge: prepare.Challenge, Views: views})
	}
	var stop restartCommand
	if err := decoder.Decode(&stop); err != nil || stop.Action != "stop" || stop.Turn != last || stop.Challenge != nonce {
		t.Fatal("invalid child close command", err)
	}
	f.close()
	emit(restartEvent{Kind: "closed", Turn: last, Challenge: nonce})
}

type restartReceived struct {
	Kind           string `json:"kind"`
	Turn           int    `json:"turn"`
	Challenge      string `json:"challenge"`
	ParentSequence int    `json:"parent_sequence"`
}

type restartProcessRecord struct {
	TestID           string            `json:"test_id"`
	Role             string            `json:"role"`
	ProcessID        int               `json:"process_id"`
	ParentProcessID  int               `json:"parent_process_id"`
	ParentNonce      string            `json:"parent_nonce"`
	Nonce            string            `json:"nonce"`
	ExecutableSHA256 string            `json:"executable_sha256"`
	StartSequence    int               `json:"start_sequence"`
	ExitSequence     int               `json:"exit_sequence"`
	ExitCode         int               `json:"exit_code"`
	Stdout           string            `json:"stdout"`
	Stderr           string            `json:"stderr"`
	Commands         []restartCommand  `json:"commands"`
	Received         []restartReceived `json:"received"`
}

type restartProcess struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	scanner  *bufio.Scanner
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	record   restartProcessRecord
	sequence func() int
}

func startRestartProcess(t *testing.T, executable, executableSHA, directory, role string, sequence func() int) *restartProcess {
	t.Helper()
	p := &restartProcess{sequence: sequence}
	p.record = restartProcessRecord{TestID: "proxy/internal/middleware/acceptance_test.go::" + t.Name(), Role: role,
		ParentProcessID: os.Getpid(), ParentNonce: os.Getenv("CAVEMAN_MIDDLEWARE_ACCEPTANCE_NONCE"),
		Nonce: restartChallenge(t), ExecutableSHA256: executableSHA, StartSequence: sequence()}
	p.cmd = exec.Command(executable, "-test.v", "-test.count=1", "-test.run=^TestMiddlewareRestartWorkerProcess$", "-test.timeout=45s")
	p.cmd.Env = append(os.Environ(), "CAVEMAN_MIDDLEWARE_RESTART_ROLE="+role, "CAVEMAN_MIDDLEWARE_RESTART_NONCE="+p.record.Nonce,
		"CAVEMAN_MIDDLEWARE_RESTART_PARENT_PID="+strconv.Itoa(os.Getpid()), "CAVEMAN_MIDDLEWARE_RESTART_DIRECTORY="+directory)
	var err error
	p.stdin, err = p.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.scanner = bufio.NewScanner(stdout)
	p.scanner.Buffer(make([]byte, 4096), 1<<20)
	p.cmd.Stderr = &p.stderr
	if err = p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p.record.ProcessID = p.cmd.Process.Pid
	t.Cleanup(func() {
		_ = p.stdin.Close()
		if p.cmd.ProcessState == nil {
			_ = p.cmd.Process.Kill()
			_ = p.cmd.Wait()
		}
	})
	p.next(t, "ready", -1, "")
	return p
}

func (p *restartProcess) send(t *testing.T, action string, turn int, challenge string) {
	t.Helper()
	command := restartCommand{action, turn, challenge, p.sequence()}
	if err := json.NewEncoder(p.stdin).Encode(command); err != nil {
		t.Fatal(err)
	}
	p.record.Commands = append(p.record.Commands, command)
}

func (p *restartProcess) next(t *testing.T, kind string, turn int, challenge string) restartEvent {
	t.Helper()
	for p.scanner.Scan() {
		line := p.scanner.Text()
		p.stdout.WriteString(line + "\n")
		if !strings.HasPrefix(line, restartChildPrefix) {
			continue
		}
		var event restartEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, restartChildPrefix)), &event); err != nil || event.Kind != kind || event.Turn != turn || event.Challenge != challenge ||
			event.ProcessID != p.record.ProcessID || event.ParentProcessID != os.Getpid() || event.ParentNonce != p.record.ParentNonce || event.Nonce != p.record.Nonce ||
			event.Role != p.record.Role || event.ExecutableSHA256 != p.record.ExecutableSHA256 {
			t.Fatal("child result escaped its actual process, executable or gate", err, line)
		}
		p.record.Received = append(p.record.Received, restartReceived{kind, turn, challenge, p.sequence()})
		return event
	}
	t.Fatal("child ended before its expected result", p.scanner.Err(), p.stdout.String(), p.stderr.String())
	return restartEvent{}
}

func (p *restartProcess) finish(t *testing.T, lastTurn int) restartProcessRecord {
	t.Helper()
	p.send(t, "stop", lastTurn, p.record.Nonce)
	p.next(t, "closed", lastTurn, p.record.Nonce)
	_ = p.stdin.Close()
	for p.scanner.Scan() {
		p.stdout.WriteString(p.scanner.Text() + "\n")
	}
	err := p.cmd.Wait()
	p.record.ExitSequence, p.record.ExitCode = p.sequence(), p.cmd.ProcessState.ExitCode()
	p.record.Stdout, p.record.Stderr = p.stdout.String(), p.stderr.String()
	if err != nil || p.scanner.Err() != nil || p.record.ExitCode != 0 || p.record.Stderr != "" ||
		!strings.Contains(p.record.Stdout, "--- PASS: TestMiddlewareRestartWorkerProcess ") || !strings.HasSuffix(p.record.Stdout, "\nPASS\n") {
		t.Fatal("native child did not complete successfully", err, p.record.Stdout, p.record.Stderr)
	}
	return p.record
}

func TestMiddlewareAcceptanceTwentyTurnsAcrossProcesses(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	sequenceNumber := 0
	sequence := func() int { sequenceNumber++; return sequenceNumber }
	seed := startRestartProcess(t, executable, digest(program), directory, "seed", sequence)
	var chosen []restartView
	priorComparisons, exactRecoveries := 0, 0
	round := func(turn int, workers ...*restartProcess) {
		challenge := restartChallenge(t)
		for _, worker := range workers {
			worker.send(t, "prepare", turn, challenge)
		}
		for _, worker := range workers {
			event := worker.next(t, "prepared", turn, challenge)
			if len(event.Manifest) != turn+2 {
				t.Fatal("child did not append to its original history")
			}
		}
		// Both workers have independently reopened the stores and reached this
		// turn's prepare barrier before either receives permission to optimize.
		for _, worker := range workers {
			worker.send(t, "release", turn, challenge)
		}
		for _, worker := range workers {
			event := worker.next(t, "completed", turn, challenge)
			if len(event.Views) != turn+1 {
				t.Fatal("child omitted an earlier view")
			}
			for index, view := range event.Views {
				if index < len(chosen) {
					before := chosen[index]
					if view.Text != before.Text || view.SHA256 != before.SHA256 || view.RecoveryHandle != before.RecoveryHandle || view.OriginalSHA256 != before.OriginalSHA256 {
						t.Fatal("a restarted worker changed chosen bytes or a recovery marker", turn, index)
					}
				} else {
					chosen = append(chosen, view)
				}
				if index < turn {
					priorComparisons++
				}
				exactRecoveries++
			}
		}
	}
	for turn := 0; turn < 10; turn++ {
		round(turn, seed)
	}
	seedRecord := seed.finish(t, 9) // The actual process has exited before restart.
	workerA := startRestartProcess(t, executable, digest(program), directory, "worker-a", sequence)
	workerB := startRestartProcess(t, executable, digest(program), directory, "worker-b", sequence)
	for turn := 10; turn < 20; turn++ {
		round(turn, workerA, workerB)
	}
	workerARecord, workerBRecord := workerA.finish(t, 19), workerB.finish(t, 19)
	if seedRecord.ProcessID == workerARecord.ProcessID || seedRecord.ProcessID == workerBRecord.ProcessID || workerARecord.ProcessID == workerBRecord.ProcessID ||
		seedRecord.ExitSequence >= workerARecord.StartSequence || workerBRecord.StartSequence >= workerARecord.ExitSequence || priorComparisons != 335 || exactRecoveries != 365 {
		t.Fatal("fixture did not perform an actual restart and overlapping independent workers")
	}
	for _, record := range []restartProcessRecord{seedRecord, workerARecord, workerBRecord} {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		t.Log("CAVEMAN_MIDDLEWARE_RESTART_PROCESS " + string(data))
	}
	sources := make([]string, len(chosen))
	for index, view := range chosen {
		sources[index] = view.OriginalSHA256
	}
	acceptance(t, "twenty_turn_process_restart", []string{"runtime.R6.AC02", "runtime.R5.AC07", "proof.R3.AC03"}, map[string]any{
		"append_only_turns": 20, "seed_turns": 10, "resumed_turns": 10, "separate_processes": 3, "concurrent_workers": 2,
		"gated_concurrent_turns": 10, "earlier_byte_comparisons": priorComparisons, "exact_original_recoveries": exactRecoveries,
		"original_sha256s": sources, "retained_originals": len(chosen), "seed_exited_before_workers_started": true,
		"replacement_bytes_and_markers_equal": true, "both_stores_reopened_in_each_worker": true, "retention_elapsed": false})
}
