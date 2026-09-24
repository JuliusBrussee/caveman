package middleware

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

const clientFeatures = "http_status_v2, revision_tolerant" // what the 1.1 SDKs send

type parityV11 struct {
	Constants struct {
		Features struct {
			Known []string `json:"known"`
		} `json:"features"`
		Headers map[string]string `json:"headers"`
	} `json:"constants"`
	ServerErrorCodes []struct {
		Code         string `json:"code"`
		Status       int    `json:"status"`
		LegacyStatus *int   `json:"legacy_status"`
		RetryAfter   bool   `json:"retry_after"`
	} `json:"server_error_codes"`
	LegacyConditions []struct {
		Condition    string `json:"condition"`
		Status       int    `json:"status"`
		Code         string `json:"code"`
		LegacyStatus int    `json:"legacy_status"`
		LegacyCode   string `json:"legacy_code"`
	} `json:"legacy_conditions"`
}

func loadParity(t *testing.T) parityV11 {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "sdk", "parity", "middleware-v1_1.fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p parityV11
	if err := json.Unmarshal(b, &p); err != nil || len(p.ServerErrorCodes) == 0 || len(p.LegacyConditions) == 0 {
		t.Fatalf("unreadable parity fixture: %v", err)
	}
	return p
}

func features(v2 bool) string {
	if v2 {
		return clientFeatures
	}
	return ""
}

// send POSTs body as principal, with the features header when given.
func send(t *testing.T, r *Runtime, path string, body any, principal, features string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", RoutePrefix+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+principal)
	if features != "" {
		req.Header.Set(HeaderFeatures, features)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func capabilities(t *testing.T, r *Runtime, features string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", RoutePrefix+"capabilities", nil)
	req.Header.Set("Authorization", "Bearer alice")
	if features != "" {
		req.Header.Set(HeaderFeatures, features)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil || w.Code != 200 {
		t.Fatalf("capabilities: %d %s", w.Code, w.Body)
	}
	return w, doc
}

func withRuntime(t *testing.T, f fixture, change func(*Config)) *Runtime {
	t.Helper()
	cfg := f.runtime.cfg
	change(&cfg)
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func decodePlan(t *testing.T, w *httptest.ResponseRecorder) OptimizeResponse {
	t.Helper()
	var plan OptimizeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || w.Code != 200 {
		t.Fatalf("optimize: %d %s", w.Code, w.Body)
	}
	return plan
}

// A body that never finishes arriving, over a real connection, since only a
// real connection has a read deadline.
func slowBody(t *testing.T, r *Runtime, features string) *httptest.ResponseRecorder {
	t.Helper()
	server := httptest.NewServer(r)
	defer server.Close()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	head := "POST " + RoutePrefix + "optimize HTTP/1.1\r\nHost: middleware\r\nAuthorization: Bearer alice\r\nContent-Type: application/json\r\nContent-Length: 64\r\n"
	if features != "" {
		head += HeaderFeatures + ": " + features + "\r\n"
	}
	if _, err := io.WriteString(conn, head+"\r\n{"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w := httptest.NewRecorder()
	maps.Copy(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	return w
}

// notSmallerOverhead declares a recovery tool costing more than any saving.
func notSmallerOverhead() string {
	var b strings.Builder
	for i := 0; b.Len() < 32000; i++ {
		fmt.Fprintf(&b, "%d ", i*7919)
	}
	return b.String()
}

// condition reproduces one legacy_conditions row of the parity fixture.
func condition(t *testing.T, name string, v2 bool) *httptest.ResponseRecorder {
	t.Helper()
	f := newFixture(t)
	h := features(v2)
	req := requestFor(f.runtime)
	switch name {
	case "slow_request_body":
		return slowBody(t, withRuntime(t, f, func(c *Config) { c.Limits.DeadlineMS = 200 }), h)
	case "retrieve_limit_out_of_range":
		plan := optimizeOK(t, f.runtime, req)
		return send(t, f.runtime, "retrieve", RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: plan.Replacements[0].RecoveryHandle, Limit: 3}, "alice", h)
	case "queue_wait_exceeded":
		r := withRuntime(t, f, func(c *Config) { c.Limits.DeadlineMS = 50 })
		for range cap(r.queue) {
			r.queue <- struct{}{}
		}
		return send(t, r, "optimize", req, "alice", h)
	case "malformed_recovery_binding":
		req.RecoveryBinding.Kind = "bogus"
	case "optimize_not_smaller":
		req.RecoveryBinding.OverheadText = notSmallerOverhead()
	case "optimize_cache_state_unavailable", "optimize_recovery_unavailable":
		optimizeOK(t, f.runtime, req)
		statement := `UPDATE middleware_choices SET payload=x'7b'`
		if name == "optimize_recovery_unavailable" {
			statement = `DELETE FROM middleware_originals`
		}
		if _, err := f.db(t).Exec(statement); err != nil {
			t.Fatal(err)
		}
		req.RequestID, req.IdempotencyKey = "again", "again"
	case "optimize_store_capacity":
		f.runtime = withRuntime(t, f, func(c *Config) { c.Capacity.Bytes = 1 })
	case "storage_error":
		plan := optimizeOK(t, f.runtime, req)
		_ = f.state.Close()
		return send(t, f.runtime, "retrieve", RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: plan.Replacements[0].RecoveryHandle}, "alice", h)
	default:
		t.Fatalf("no reproduction for legacy condition %s", name)
	}
	return send(t, f.runtime, "optimize", req, "alice", h)
}

func TestLegacyConditionsFromParityFixture(t *testing.T) {
	for _, c := range loadParity(t).LegacyConditions {
		for _, v2 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/http_status_v2=%t", c.Condition, v2), func(t *testing.T) {
				w := condition(t, c.Condition, v2)
				status, code := c.LegacyStatus, c.LegacyCode
				if v2 {
					status, code = c.Status, c.Code
				}
				if w.Code != status {
					t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body)
				}
				if status == 200 {
					plan := decodePlan(t, w)
					if plan.Status != "bypassed" || plan.Reason != code || len(plan.Replacements) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].SegmentID != "tool-1" {
						t.Fatalf("decision is not a bypass plan with reason %s: %s", code, w.Body)
					}
				} else if got := failureCode(t, w.Body.Bytes()); got != code {
					t.Fatalf("code %s, want %s", got, code)
				}
				if retry := w.Header().Get(HeaderRetryAfter); (status == 429 || status == 503) != (retry != "") || retry == "0" {
					t.Fatalf("Retry-After %q on status %d", retry, status)
				}
			})
		}
	}
}

func TestServerErrorCodesFromParityFixture(t *testing.T) {
	p := loadParity(t)
	if !slices.Equal(p.Constants.Features.Known, serverFeatures) || p.Constants.Headers["features"] != HeaderFeatures ||
		p.Constants.Headers["client"] != HeaderClient || p.Constants.Headers["retry_after"] != HeaderRetryAfter {
		t.Fatal("feature or header constants drifted from the parity fixture")
	}
	for _, e := range p.ServerErrorCodes {
		for _, v2 := range []bool{true, false} {
			w := httptest.NewRecorder()
			status, code, _ := writeError(w, Failure{e.Code}, v2)
			switch {
			case v2 && (status != e.Status || code != e.Code):
				t.Errorf("%s: 1.1 answer %d %s, want %d", e.Code, status, code, e.Status)
			case !v2 && e.LegacyStatus != nil && (status != *e.LegacyStatus || code != e.Code):
				t.Errorf("%s: 1.0 answer %d %s, want %d", e.Code, status, code, *e.LegacyStatus)
			case !v2 && e.LegacyStatus == nil && (status == 408 || status == 429):
				t.Errorf("%s: 1.0 client got a status 1.0 never sent: %d", e.Code, status)
			}
			if hasRetry := w.Header().Get(HeaderRetryAfter) != ""; hasRetry != (status == 429 || status == 503) || (v2 && hasRetry != e.RetryAfter) {
				t.Errorf("%s (http_status_v2=%t): Retry-After present=%t on %d", e.Code, v2, hasRetry, status)
			}
			if failureCode(t, w.Body.Bytes()) != code {
				t.Errorf("%s: envelope does not carry its code", e.Code)
			}
		}
	}
}

// flow drives every route the way an SDK does and returns each 200 body by
// golden name. Without the features header it replays SDK 1.1.0 exactly.
func flow(t *testing.T, v2 bool) map[string][]byte {
	t.Helper()
	f := newFixture(t)
	h := features(v2)
	tag := map[bool]string{false: "v1_0", true: "v1_1"}[v2]
	out := map[string][]byte{}
	w, caps := capabilities(t, f.runtime, h)
	out["capabilities--"+tag] = w.Body.Bytes()
	limits := caps["limits"].(map[string]any)
	if v2 {
		if fmt.Sprint(caps["features"]) != fmt.Sprint(serverFeatures) || fmt.Sprint(caps["protocol"]) != "map[max:1 min:1]" ||
			caps["max_retention_seconds"] != float64(604800) || limits["retrieve_deadline_ms"] != float64(DefaultRetrieveDeadlineMS) || limits["queue_depth"] == nil {
			t.Fatalf("1.1 capabilities view incomplete: %s", w.Body)
		}
	} else {
		if caps["features"] != nil || caps["protocol"] != nil || caps["max_retention_seconds"] != nil || len(limits) != 4 {
			t.Fatalf("SDK 1.1.0 must get the exact 1.0 capabilities document: %s", w.Body)
		}
	}
	req := requestFor(f.runtime)
	req.Policy.Revision = caps["policy_revision"].(string)
	plan := decodePlan(t, send(t, f.runtime, "optimize", req, "alice", h))
	if plan.Status != "optimized" || len(plan.Replacements) != 1 {
		t.Fatalf("no replacement: %+v", plan)
	}
	w = send(t, f.runtime, "optimize", req, "alice", h)
	if replay := decodePlan(t, w); replay.Replacements[0].Text != plan.Replacements[0].Text || replay.ReplacementSetID != plan.ReplacementSetID {
		t.Fatal("idempotent replay changed the plan")
	}
	out["plan--optimized-"+tag], out["plan--replay-"+tag] = send(t, f.runtime, "optimize", req, "alice", h).Body.Bytes(), w.Body.Bytes()
	record := req
	record.Mode, record.RequestID, record.IdempotencyKey = "record", "record", "record"
	out["plan--record-"+tag] = send(t, f.runtime, "optimize", record, "alice", h).Body.Bytes()
	retrieve := RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: plan.Replacements[0].RecoveryHandle}
	w = send(t, f.runtime, "retrieve", retrieve, "alice", h)
	var page RetrieveResponse
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || !page.Complete || page.Text != noisy() {
		t.Fatalf("exact recovery failed: %d %s", w.Code, w.Body)
	}
	out["page--original-"+tag] = w.Body.Bytes()
	retrieve.Query = "074"
	out["page--excerpt-"+tag] = send(t, f.runtime, "retrieve", retrieve, "alice", h).Body.Bytes()
	w = send(t, f.runtime, "receipts", Receipt{SchemaVersion: 1, Scope: req.Scope, LogicalCallID: req.LogicalCallID, AttemptID: req.AttemptID,
		EventKind: "dispatch_intent", PlanID: &plan.ReplacementSetID}, "alice", h)
	if w.Code != 200 {
		t.Fatalf("receipt: %d %s", w.Code, w.Body)
	}
	out["receipt-response--"+tag] = w.Body.Bytes()
	w = send(t, f.runtime, "sessions/delete", SessionDeleteRequest{SchemaVersion: 1, Scope: req.Scope}, "alice", h)
	var deleted SessionDeleteResponse
	if json.Unmarshal(w.Body.Bytes(), &deleted) != nil || w.Code != 200 || !deleted.OriginalsDeleted ||
		*deleted.Deleted != (DeleteCounts{Scopes: 1, Choices: 1, Grants: 1, Originals: 1}) || f.originals(t) != 0 {
		t.Fatalf("delete did not remove the session's original: %d %s", w.Code, w.Body)
	}
	out["session-delete-response--"+tag] = w.Body.Bytes()
	retrieve.Query = ""
	if w = send(t, f.runtime, "retrieve", retrieve, "alice", h); w.Code != 410 || failureCode(t, w.Body.Bytes()) != "deleted" {
		t.Fatalf("deleted grant: %d %s", w.Code, w.Body)
	}
	return out
}

func TestProtocol10ClientReplay(t *testing.T) { flow(t, false) }
func TestProtocol11ClientFlow(t *testing.T)   { flow(t, true) }

// Every response the runtime can emit, checked against the contract schemas.
func TestMiddlewareGoldenResponsesMatchContractSchemas(t *testing.T) {
	goldens := map[string][]byte{}
	for _, v2 := range []bool{false, true} {
		maps.Copy(goldens, flow(t, v2))
	}
	p := loadParity(t)
	for _, c := range p.LegacyConditions {
		for _, v2 := range []bool{false, true} {
			w := condition(t, c.Condition, v2)
			schema := "error"
			if w.Code == 200 {
				schema = "plan"
			}
			goldens[fmt.Sprintf("%s--%s-v2_%t", schema, c.Condition, v2)] = w.Body.Bytes()
		}
	}
	for _, e := range p.ServerErrorCodes {
		for _, v2 := range []bool{false, true} {
			w := httptest.NewRecorder()
			writeError(w, Failure{e.Code}, v2)
			goldens[fmt.Sprintf("error--%s-v2_%t", e.Code, v2)] = w.Body.Bytes()
		}
	}
	dir := os.Getenv("CAVEMAN_MIDDLEWARE_GOLDEN_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range goldens {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node not on PATH: %d goldens in %s not schema-validated", len(goldens), dir)
	}
	output, err := exec.Command(node, filepath.Join("testdata", "validate-goldens.mjs"), dir).CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 3 {
		t.Skipf("contract validator unavailable (%s): run pnpm install", strings.TrimSpace(string(output)))
	}
	if err != nil {
		t.Fatalf("goldens violate the contract schemas: %v\n%s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
}

func TestRevisionTolerantAndTolerantReader(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	req.Policy.Revision = "middleware-v1:cached-before-an-upgrade"
	for _, header := range []string{"", "http_status_v2"} {
		if w := send(t, f.runtime, "optimize", req, "alice", header); w.Code != 400 || failureCode(t, w.Body.Bytes()) != "unknown_capability" {
			t.Fatalf("header %q accepted a stale revision without revision_tolerant: %d %s", header, w.Code, w.Body)
		}
	}
	if plan := decodePlan(t, send(t, f.runtime, "optimize", req, "alice", clientFeatures)); plan.PolicyRevision != f.runtime.caps.PolicyRevision {
		t.Fatal("plan does not carry the server's current revision")
	}
	wire, _ := json.Marshal(req)
	var future map[string]any
	_ = json.Unmarshal(wire, &future)
	future["future_field"] = map[string]any{"nested": true}
	future["segments"].([]any)[0].(map[string]any)["future_segment_field"] = 1
	future["request_id"], future["idempotency_key"] = "future", "future"
	if w := send(t, f.runtime, "optimize", future, "alice", ""); w.Code != 400 {
		t.Fatalf("1.0 client's unknown field accepted: %d", w.Code)
	}
	if w := send(t, f.runtime, "optimize", future, "alice", clientFeatures); w.Code != 200 {
		t.Fatalf("tolerant reader rejected unknown fields: %d %s", w.Code, w.Body)
	}
	delete(future, "sequence")
	if w := send(t, f.runtime, "optimize", future, "alice", clientFeatures); w.Code != 400 || failureCode(t, w.Body.Bytes()) != "invalid_request" {
		t.Fatalf("tolerant reader dropped a required field: %d %s", w.Code, w.Body)
	}
}

// Scope identity no longer includes the policy revision, so an upgrade that
// changes the revision keeps every persisted choice and its exact bytes.
func TestPersistedChoicesSurviveARevisionChange(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	req.Policy.Revision = "middleware-v1:before-upgrade"
	first := decodePlan(t, send(t, f.runtime, "optimize", req, "alice", clientFeatures)).Replacements[0]
	upgraded := withRuntime(t, f, func(*Config) {})
	req.Policy.Revision = "middleware-v1:after-upgrade"
	req.RequestID, req.IdempotencyKey, req.Sequence = "after-upgrade", "after-upgrade", 1
	req.Segments[0].CacheRegion = "frozen_prefix"
	req.ContextManifest = append(req.ContextManifest, ManifestItem{"msg-2", digest([]byte("next turn"))})
	plan := decodePlan(t, send(t, upgraded, "optimize", req, "alice", clientFeatures))
	if len(plan.Replacements) != 1 || !plan.Replacements[0].Reused || plan.Replacements[0].Text != first.Text || plan.Replacements[0].RecoveryHandle != first.RecoveryHandle {
		t.Fatalf("revision change re-randomized a persisted choice: %+v", plan)
	}
}

func TestReusedChoiceNeedsItsTransformInPolicy(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	optimizeOK(t, f.runtime, req)
	req.RequestID, req.IdempotencyKey, req.Sequence = "narrowed", "narrowed", 1
	req.Segments[0].CacheRegion = "frozen_prefix"
	req.Policy.Transforms = []string{}
	plan := optimizeOK(t, f.runtime, req)
	if len(plan.Replacements) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].Reason != "unknown_capability" {
		t.Fatalf("a choice was reused under a policy without its transform: %+v", plan)
	}
}

func TestOriginalsFollowTheirScope(t *testing.T) {
	t.Run("not_smaller_stores_nothing", func(t *testing.T) {
		f := newFixture(t)
		req := requestFor(f.runtime)
		req.RecoveryBinding.OverheadText = notSmallerOverhead()
		if code, body := call(t, f.runtime, "optimize", req, "alice"); code != 503 || failureCode(t, body) != "not_smaller" {
			t.Fatalf("fixture is not not_smaller: %d %s", code, body)
		}
		if f.originals(t) != 0 {
			t.Fatal("an unpublished plan left its original behind")
		}
	})
	t.Run("expiry_deletes", func(t *testing.T) {
		f := newFixture(t)
		now := time.Now()
		f.runtime.cfg.Now = func() time.Time { return now }
		req := requestFor(f.runtime)
		plan := optimizeOK(t, f.runtime, req)
		if f.originals(t) != 1 {
			t.Fatal("original not in the middleware store")
		}
		now = now.Add(25 * time.Hour)
		if code, body := call(t, f.runtime, "retrieve", RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: plan.Replacements[0].RecoveryHandle}, "alice"); code != 410 || failureCode(t, body) != "expired" {
			t.Fatalf("expired grant: %d %s", code, body)
		}
		if err := f.runtime.sweep(t.Context()); err != nil || f.originals(t) != 0 {
			t.Fatalf("sweep kept an expired original (%v)", err)
		}
	})
}

func TestMaxRetentionCapsSlidingRenewal(t *testing.T) {
	f := newFixture(t)
	start := time.Unix(2_000_000_000, 0)
	now := start
	r := withRuntime(t, f, func(c *Config) {
		c.Retention, c.MaxRetention, c.Now = time.Hour, 2*time.Hour, func() time.Time { return now }
	})
	req := requestFor(r)
	first := optimizeOK(t, r, req)
	if first.Recovery.ExpiresAt != start.Add(time.Hour).Unix() {
		t.Fatalf("creation expiry %d", first.Recovery.ExpiresAt)
	}
	for turn, at := range []struct{ now, expires time.Duration }{{50 * time.Minute, 110 * time.Minute}, {100 * time.Minute, 2 * time.Hour}} {
		now = start.Add(at.now)
		req.RequestID, req.IdempotencyKey, req.Sequence = fmt.Sprint("turn-", turn+2), fmt.Sprint("turn-", turn+2), int64(turn+1)
		req.ContextManifest = append(req.ContextManifest, ManifestItem{fmt.Sprint("msg-", turn+2), digest([]byte(fmt.Sprint(turn)))})
		if plan := optimizeOK(t, r, req); plan.Recovery.ExpiresAt != start.Add(at.expires).Unix() {
			t.Fatalf("turn %d expires %d, want start+%s (max retention caps the slide)", turn+2, plan.Recovery.ExpiresAt-start.Unix(), at.expires)
		}
	}
	now = start.Add(110 * time.Minute)
	retrieve := RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: first.Replacements[0].RecoveryHandle}
	recovered(t, r, req.Scope, retrieve.Handle, "alice")
	if err := r.flushRenewals(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = start.Add(2*time.Hour + time.Second)
	if code, body := call(t, r, "retrieve", retrieve, "alice"); code != 410 || failureCode(t, body) != "expired" {
		t.Fatalf("scope outlived max retention: %d %s", code, body)
	}
}

func TestOriginalsSealedAtRestAndKeysRotate(t *testing.T) {
	key := func() string {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		return base64.StdEncoding.EncodeToString(b)
	}
	k1, k2 := key(), key()
	keyring := func(inline, path string) *Keyring {
		k, err := LoadKeyring(inline, path)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	f := newFixture(t)
	sealed := withRuntime(t, f, func(c *Config) { c.Keys = keyring(k1, "") })
	req := requestFor(sealed)
	plan := optimizeOK(t, sealed, req)
	var body []byte
	var keyID string
	if err := f.db(t).QueryRow(`SELECT body,key_id FROM middleware_originals`).Scan(&body, &keyID); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("exact-value")) || keyID != keyring(k1, "").active {
		t.Fatalf("original stored in plaintext or without its key id %q", keyID)
	}
	path := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(path, []byte(k2+"\n"+k1+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotated := withRuntime(t, f, func(c *Config) { c.Keys = keyring("", path) })
	if page := recovered(t, rotated, req.Scope, plan.Replacements[0].RecoveryHandle, "alice"); page.Text != noisy() {
		t.Fatal("rotated keyring cannot open the previous key's original")
	}
	for _, keys := range []*Keyring{keyring(k2, ""), nil} {
		r := withRuntime(t, f, func(c *Config) { c.Keys = keys })
		if code, body := call(t, r, "retrieve", RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: plan.Replacements[0].RecoveryHandle}, "alice"); code != 503 || failureCode(t, body) != "recovery_unavailable" {
			t.Fatalf("original opened without its key: %d %s", code, body)
		}
	}
	if _, err := LoadKeyring("not-a-key", ""); err == nil || strings.Contains(err.Error(), "not-a-key") {
		t.Fatalf("bad key accepted or echoed: %v", err)
	}
}

// Grants a protocol 1.0 runtime issued point at CCR; they keep recovering until
// their scope expires, and delete says their originals were not deleted.
func TestProtocol10GrantStillRetrievesFromCCR(t *testing.T) {
	f := newFixture(t)
	content := noisy()
	handle, err := f.recovery.Put(ccr.Recovery{Original: []byte(content)})
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"app", "legacy-session", "main", "0"}
	grant := "cmw_" + strings.Repeat("ab", 24)
	choice, _ := json.Marshal(Replacement{SegmentID: "tool-1", SourceID: "document-1", OriginalSHA256: digest([]byte(content))})
	now := time.Now().Unix()
	if err := f.state.WithMiddleware(t.Context(), func(tx *store.MiddlewareTx) error {
		if err := tx.SaveScope(store.MiddlewareScope{ID: "scope-from-1.0", Authority: authority("alice", scope), Manifest: []byte("[]"), ExpiresAt: now + 3600, CreatedAt: now}); err != nil {
			return err
		}
		return tx.SaveChoice("scope-from-1.0", "choice", grant, handle, choice)
	}); err != nil {
		t.Fatal(err)
	}
	if page := recovered(t, f.runtime, scope, grant, "alice"); page.Text != content {
		t.Fatal("1.0 grant lost its CCR original")
	}
	var deleted SessionDeleteResponse
	w := send(t, f.runtime, "sessions/delete", SessionDeleteRequest{SchemaVersion: 1, Scope: scope}, "alice", clientFeatures)
	if json.Unmarshal(w.Body.Bytes(), &deleted) != nil || deleted.OriginalsDeleted || deleted.Deleted.Grants != 1 {
		t.Fatalf("delete claimed to remove a CCR original it cannot delete: %s", w.Body)
	}
}

func TestAuditLogAndMetricsCarryNoContent(t *testing.T) {
	var log bytes.Buffer
	f := newFixture(t)
	r := withRuntime(t, f, func(c *Config) { c.Logger = slog.New(slog.NewJSONHandler(&log, nil)) })
	req := requestFor(r)
	plan := optimizeOK(t, r, req)
	recovered(t, r, req.Scope, plan.Replacements[0].RecoveryHandle, "alice")
	wire, _ := json.Marshal(RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: plan.Replacements[0].RecoveryHandle})
	withClient := httptest.NewRequest("POST", RoutePrefix+"retrieve", bytes.NewReader(wire))
	withClient.Header.Set("Content-Type", "application/json")
	withClient.Header.Set("Authorization", "Bearer alice")
	withClient.Header.Set(HeaderClient, "caveman-sdk-python/1.2.0 \"injected\":1")
	r.ServeHTTP(httptest.NewRecorder(), withClient)
	send(t, r, "sessions/delete", SessionDeleteRequest{SchemaVersion: 1, Scope: req.Scope}, "alice", "")
	send(t, r, "optimize", req, "mallory", "")
	for _, secret := range []string{"exact-value", req.Scope.SessionID, plan.Replacements[0].RecoveryHandle, "mallory", "Bearer"} {
		if strings.Contains(log.String(), secret) {
			t.Fatalf("audit log disclosed %q:\n%s", secret, log.String())
		}
	}
	for _, want := range []string{`"client":"caveman-sdk-python/1.2.0 ?injected??1"`, `"route":"optimize"`, `"plan_status":"optimized"`, `"route":"retrieve"`, `"deleted_originals":1`, `"status":401`, `"principal":"alice"`} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("audit log lacks %s:\n%s", want, log.String())
		}
	}
	var metrics strings.Builder
	r.WriteMetrics(&metrics)
	for _, want := range []string{
		"# TYPE caveman_middleware_requests_total counter", "# TYPE caveman_middleware_request_duration_seconds histogram",
		"# TYPE caveman_middleware_queue_depth gauge", "# TYPE caveman_middleware_unauthorized_total counter",
		"# TYPE caveman_middleware_decisions_total counter", "# TYPE caveman_middleware_store_bytes gauge",
		`caveman_middleware_requests_total{route="optimize",status="200",code="ok"} 1`, `caveman_middleware_unauthorized_total 1`,
		`caveman_middleware_decisions_total{status="optimized",reason="eligible"} 1`, `caveman_middleware_request_duration_seconds_count{route="retrieve"} 2`,
	} {
		if !strings.Contains(metrics.String(), want) {
			t.Fatalf("metrics lack %s:\n%s", want, metrics.String())
		}
	}
}

func TestRequestQuotaIsPerPrincipal(t *testing.T) {
	f := newFixture(t)
	r := withRuntime(t, f, func(c *Config) { c.Limits.QuotaRequestsPerMinute = 1 })
	if _, caps := capabilities(t, r, clientFeatures); caps["limits"].(map[string]any)["quota_requests_per_minute"] != float64(1) {
		t.Fatal("quota not advertised")
	}
	req := requestFor(r)
	if w := send(t, r, "optimize", req, "alice", clientFeatures); w.Code != 200 {
		t.Fatalf("first request: %d", w.Code)
	}
	w := send(t, r, "optimize", req, "alice", clientFeatures)
	if w.Code != 429 || failureCode(t, w.Body.Bytes()) != "quota_exceeded" || w.Header().Get(HeaderRetryAfter) == "" {
		t.Fatalf("quota: %d %s", w.Code, w.Body)
	}
	if w = send(t, r, "optimize", req, "alice", ""); w.Code != 503 || failureCode(t, w.Body.Bytes()) != "capacity" {
		t.Fatalf("1.0 client got a code it does not know: %d %s", w.Code, w.Body)
	}
	if w = send(t, r, "optimize", req, "bob", clientFeatures); w.Code != 200 {
		t.Fatalf("one principal's quota limited another: %d", w.Code)
	}
}

func TestRetrieveHasItsOwnQueue(t *testing.T) {
	f := newFixture(t)
	req := requestFor(f.runtime)
	plan := optimizeOK(t, f.runtime, req)
	for range cap(f.runtime.queue) {
		f.runtime.queue <- struct{}{}
	}
	defer func() {
		for range cap(f.runtime.queue) {
			<-f.runtime.queue
		}
	}()
	recovered(t, f.runtime, req.Scope, plan.Replacements[0].RecoveryHandle, "alice")
}
