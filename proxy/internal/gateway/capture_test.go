package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// readCaptures reads every capture in dir, sorted by seq, and verifies each
// stored body against the hash the file itself recorded. A capture that cannot
// prove its own bytes is worthless as evidence, so every test that reads
// captures enforces it.
func readCaptures(t *testing.T, dir string) []capturedRequest {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read capture dir: %v", err)
	}
	var out []capturedRequest
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read capture %s: %v", e.Name(), err)
		}
		var c capturedRequest
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("decode capture %s: %v", e.Name(), err)
		}
		if c.BodyEncoding != "base64" {
			t.Errorf("capture %s: body_encoding = %q, want base64", e.Name(), c.BodyEncoding)
		}
		verifyCaptureBody(t, e.Name(), "client", c.ClientBody, c.ClientBytes, c.ClientSHA256)
		verifyCaptureBody(t, e.Name(), "upstream", c.UpstreamBody, c.UpstreamBytes, c.UpstreamSHA256)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func verifyCaptureBody(t *testing.T, name, side string, body []byte, size int, sum string) {
	t.Helper()
	if body == nil {
		return
	}
	if len(body) != size {
		t.Errorf("capture %s: %s body is %d bytes, record says %d", name, side, len(body), size)
	}
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != sum {
		t.Errorf("capture %s: %s body fails its own sha256 (got %s, record says %s)",
			name, side, hex.EncodeToString(got[:]), sum)
	}
}

// captureProxyServer is the standard proxy wiring these tests drive through the
// real handler: one adapter, a socket-free upstream, no compressor.
func captureProxyServer(mode string, rt *captureTransport) *Server {
	return New(Config{
		Adapters:   []providers.Adapter{openai.New("https://upstream.test")},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: mode}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       &captureSink{},
		HTTPClient: &http.Client{Transport: rt},
	})
}

func serveCaptureRequest(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	srv.capture.flush()
	return rec
}

// Capture is off by default. A nil capture must be safe to call — the proxy path
// calls it unconditionally.
func TestBodyCaptureDisabledIsNilAndSafe(t *testing.T) {
	c := newBodyCapture("", nil)
	if c != nil {
		t.Fatal("empty dir must disable capture")
	}
	c.record(captureMeta{RequestID: "req", Provider: "anthropic"}, wholeBody([]byte("a")), wholeBody([]byte("b")))
	c.flush()
}

// A transformed request is the case the instrument exists for: both sides must
// be recoverable, byte for byte, from the capture alone.
func TestBodyCaptureRecordsBothSidesWhenTransformed(t *testing.T) {
	dir := t.TempDir()
	c := newBodyCapture(dir, nil)
	if c == nil {
		t.Fatal("capture must be enabled for a writable dir")
	}
	client := []byte(`{"messages":[{"role":"user","content":"original bytes"}]}`)
	upstream := []byte(`{"messages":[{"role":"user","content":"elided"}]}`)
	c.record(captureMeta{
		RequestID:   "req-1",
		Provider:    "anthropic",
		Endpoint:    "/v1/messages",
		RuntimeMode: "compress",
		Optimizers:  "caveman.compress.v1",
	}, wholeBody(client), wholeBody(upstream))
	c.flush()

	got := readCaptures(t, dir)
	if len(got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(got))
	}
	entry := got[0]
	if !bytes.Equal(entry.ClientBody, client) {
		t.Errorf("client body not preserved: %q", entry.ClientBody)
	}
	if !bytes.Equal(entry.UpstreamBody, upstream) {
		t.Errorf("upstream body not preserved: %q", entry.UpstreamBody)
	}
	if !entry.Transformed {
		t.Error("differing bodies must be marked transformed")
	}
	if entry.ClientBytes != len(client) || entry.UpstreamBytes != len(upstream) {
		t.Errorf("sizes wrong: %d/%d", entry.ClientBytes, entry.UpstreamBytes)
	}
	if entry.ClientSHA256 == entry.UpstreamSHA256 {
		t.Error("differing bodies must hash differently")
	}
	if entry.Optimizers != "caveman.compress.v1" {
		t.Errorf("optimizers = %q", entry.Optimizers)
	}
}

// The bodies this instrument diagnoses are arbitrary bytes: a truncating
// compressor cuts mid-rune, and a JSON string would swap the invalid sequence
// for U+FFFD and quietly break the record's own hash. Base64 keeps it exact.
func TestBodyCaptureRoundTripsInvalidUTF8(t *testing.T) {
	dir := t.TempDir()
	c := newBodyCapture(dir, nil)
	client := []byte{'{', '"', 'a', '"', ':', '"', 0xff, 0xfe, 0x80, '"', '}'}
	upstream := []byte{0xff, 0xfe, 'c', 'u', 't'}
	c.record(captureMeta{RequestID: "req-utf8"}, wholeBody(client), wholeBody(upstream))
	c.flush()

	got := readCaptures(t, dir) // verifies both hashes against the decoded bytes
	if len(got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(got))
	}
	if !bytes.Equal(got[0].ClientBody, client) {
		t.Errorf("client bytes mangled: % x", got[0].ClientBody)
	}
	if !bytes.Equal(got[0].UpstreamBody, upstream) {
		t.Errorf("upstream bytes mangled: % x", got[0].UpstreamBody)
	}
	if got[0].ClientBytes != len(client) {
		t.Errorf("client_bytes = %d, want %d", got[0].ClientBytes, len(client))
	}
}

// A pass-through would otherwise write the same payload twice. The hashes still
// have to prove the two sides matched, because "identical" is a claim.
func TestBodyCaptureOmitsDuplicateUpstreamButProvesEquality(t *testing.T) {
	dir := t.TempDir()
	c := newBodyCapture(dir, nil)
	same := []byte(`{"messages":[]}`)
	c.record(captureMeta{RequestID: "req-2", Provider: "anthropic", RuntimeMode: "record"}, wholeBody(same), wholeBody(same))
	c.flush()

	got := readCaptures(t, dir)
	if len(got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(got))
	}
	entry := got[0]
	if entry.UpstreamBody != nil || !entry.UpstreamOmitted {
		t.Error("identical upstream body must be omitted and flagged")
	}
	if entry.Transformed {
		t.Error("identical bodies must not be marked transformed")
	}
	if entry.ClientSHA256 != entry.UpstreamSHA256 {
		t.Error("identical bodies must carry equal hashes")
	}
}

// Over the per-side cap the bytes are dropped entirely rather than stored as a
// prefix that would fail the record's own hash. The hash and length still
// describe the WHOLE body, which is what makes the record usable.
func TestBodyCaptureOversizeBodyOmitsBytesButHashesWholeBody(t *testing.T) {
	dir := t.TempDir()
	c := newBodyCapture(dir, nil)
	big := bytes.Repeat([]byte("x"), captureBodyLimit+1024)
	sum := sha256.Sum256(big)
	c.record(captureMeta{RequestID: "req-big"}, wholeBody(big), wholeBody(big))
	c.flush()

	got := readCaptures(t, dir)
	if len(got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(got))
	}
	entry := got[0]
	if entry.ClientBody != nil {
		t.Errorf("over-cap body must not be stored, got %d bytes", len(entry.ClientBody))
	}
	if entry.ClientOmittedReason != "oversize" {
		t.Errorf("client_body_omitted = %q, want oversize", entry.ClientOmittedReason)
	}
	if entry.ClientBytes != len(big) || entry.ClientSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("over-cap record must describe the whole body: %d bytes, %s", entry.ClientBytes, entry.ClientSHA256)
	}
}

// A caller that streamed the body through without ever holding it whole (the
// ChatGPT over-limit path) can still state the length and hash truthfully. It
// must never store bytes it does not have.
func TestBodyCaptureStreamedBodyRecordsHashWithoutBytes(t *testing.T) {
	dir := t.TempDir()
	c := newBodyCapture(dir, nil)
	sum := sha256.Sum256([]byte("streamed"))
	body := streamedBody(9_000_000, hex.EncodeToString(sum[:]))
	c.record(captureMeta{RequestID: "req-stream"}, body, body)
	c.flush()

	got := readCaptures(t, dir)
	if len(got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(got))
	}
	entry := got[0]
	if entry.ClientBody != nil || entry.ClientOmittedReason != "streamed" {
		t.Errorf("streamed body must be omitted and flagged: %q", entry.ClientOmittedReason)
	}
	if entry.ClientBytes != 9_000_000 || entry.ClientSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("streamed record must carry the caller's length and hash: %d %s", entry.ClientBytes, entry.ClientSHA256)
	}
	if entry.Transformed {
		t.Error("same body on both sides must not read as transformed")
	}
}

// Each request gets its own file, and the sequence is what puts a session's
// requests back in order — the whole point is reading prefix growth turn by turn.
func TestBodyCaptureSequencesEachRequest(t *testing.T) {
	dir := t.TempDir()
	c := newBodyCapture(dir, nil)
	for i := 0; i < 3; i++ {
		c.record(captureMeta{RequestID: "req"}, wholeBody([]byte("x")), wholeBody([]byte("y")))
	}
	c.flush()
	got := readCaptures(t, dir)
	if len(got) != 3 {
		t.Fatalf("want 3 captures, got %d", len(got))
	}
	seen := map[uint64]bool{}
	for _, e := range got {
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
	for _, want := range []uint64{1, 2, 3} {
		if !seen[want] {
			t.Errorf("missing seq %d", want)
		}
	}
}

// The writer is a single goroutine behind a bounded queue: concurrent records
// must be race-free, and every record must be either written or counted as
// dropped — never silently lost.
func TestBodyCaptureConcurrentRecordsAreRaceFree(t *testing.T) {
	dir := t.TempDir()
	c := newBodyCapture(dir, nil)
	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := []byte(strings.Repeat("c", i+1))
			c.record(captureMeta{RequestID: "req-concurrent"}, wholeBody(payload), wholeBody(payload))
		}(i)
	}
	wg.Wait()
	c.flush()

	got := readCaptures(t, dir)
	dropped := int(c.dropped.Load())
	if len(got)+dropped != n {
		t.Fatalf("written %d + dropped %d != %d records", len(got), dropped, n)
	}
	if c.queued.Load() != 0 {
		t.Errorf("queued byte budget leaked: %d", c.queued.Load())
	}
}

// A request id reaches the filename as a string; a separator in it would be a
// traversal. Filter rather than trust.
func TestSanitizeCaptureIDRejectsPathSeparators(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "unknown"},
		{"abc-123_XYZ", "abc-123_XYZ"},
		{"../../etc/passwd", "______etc_passwd"},
		{"a/b", "a_b"},
	} {
		if got := sanitizeCaptureID(tc.in); got != tc.want {
			t.Errorf("sanitizeCaptureID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Default-off, through the real handler: an operator who never set the env var
// gets no instrument at all, and the bytes on the wire are untouched.
func TestCaptureDisabledByDefaultWritesNothing(t *testing.T) {
	t.Setenv("CAVE_CAPTURE_DIR", "")
	dir := t.TempDir()
	rt := &captureTransport{}
	srv := captureProxyServer("record", rt)
	if srv.capture != nil {
		t.Fatal("unset CAVE_CAPTURE_DIR must leave capture nil")
	}
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`
	if rec := serveCaptureRequest(t, srv, "/v1/chat/completions", body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(rt.bodies) != 1 || string(rt.bodies[0]) != body {
		t.Fatalf("upstream body altered with capture off: %q", rt.bodies)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("capture files written while disabled: %v %v", entries, err)
	}
}

// Enabling the instrument may not change one byte of what the upstream receives.
func TestCaptureEnabledLeavesUpstreamBytesIdentical(t *testing.T) {
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`

	t.Setenv("CAVE_CAPTURE_DIR", "")
	offRT := &captureTransport{}
	serveCaptureRequest(t, captureProxyServer("record", offRT), "/v1/chat/completions", body)

	t.Setenv("CAVE_CAPTURE_DIR", t.TempDir())
	onRT := &captureTransport{}
	srv := captureProxyServer("record", onRT)
	if srv.capture == nil {
		t.Fatal("CAVE_CAPTURE_DIR must enable capture")
	}
	serveCaptureRequest(t, srv, "/v1/chat/completions", body)

	if len(offRT.bodies) != 1 || len(onRT.bodies) != 1 {
		t.Fatalf("upstream calls = %d/%d, want 1/1", len(offRT.bodies), len(onRT.bodies))
	}
	if !bytes.Equal(offRT.bodies[0], onRT.bodies[0]) {
		t.Fatalf("capture changed the upstream bytes:\n off %q\n  on %q", offRT.bodies[0], onRT.bodies[0])
	}
}

// End to end through the handler on a body carrying invalid UTF-8: the capture
// must reproduce exactly the bytes the upstream received.
func TestCaptureThroughHandlerPreservesInvalidUTF8Bytes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAVE_CAPTURE_DIR", dir)
	rt := &captureTransport{}
	srv := captureProxyServer("record", rt)

	body := "{\"model\":\"gpt-5.5\",\"messages\":[{\"role\":\"user\",\"content\":\"\xff\xfe cut\"}]}"
	if rec := serveCaptureRequest(t, srv, "/v1/chat/completions", body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if string(rt.bodies[0]) != body {
		t.Fatalf("upstream body altered: %q", rt.bodies[0])
	}
	got := readCaptures(t, dir)
	if len(got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(got))
	}
	if !bytes.Equal(got[0].ClientBody, []byte(body)) {
		t.Fatalf("capture is not byte-exact:\n got % x\nwant % x", got[0].ClientBody, body)
	}
}

// The 4xx fail-open retry sends the ORIGINAL bytes. Without a second record the
// file on disk would claim the transformed bytes served the request — confident
// wrong evidence, which is the failure this instrument exists to remove.
func TestCaptureRecordsRetryWithOriginalBytes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAVE_CAPTURE_DIR", dir)
	comp := &stubCompressor{out: []byte("X"), before: 100, after: 40, handle: "ccr_capture", recovered: []byte(chatReqStream)}
	rt := &captureTransport{
		statuses:  []int{http.StatusTooManyRequests, http.StatusOK},
		responses: []string{`{"type":"error","error":{"type":"rate_limit_error"}}`, chatRespBody},
	}
	srv := New(Config{
		Adapters:       []providers.Adapter{openai.New("https://upstream.test")},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          stubCreds{key: "sk-byok"},
		Sink:           &captureSink{},
		Compressor:     comp,
		RecoveryViaMCP: true,
		HTTPClient:     &http.Client{Transport: rt},
	})

	if rec := serveCaptureRequest(t, srv, "/v1/chat/completions", chatReqStream); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(rt.bodies) != 2 {
		t.Fatalf("upstream calls = %d, want 2 (rejected transform, then original)", len(rt.bodies))
	}
	got := readCaptures(t, dir)
	if len(got) != 2 {
		t.Fatalf("want 2 captures (attempt + retry), got %d", len(got))
	}
	first, retry := got[0], got[1]
	if first.RetryOriginal || !first.Transformed {
		t.Errorf("first record must be the transformed attempt: retry=%v transformed=%v", first.RetryOriginal, first.Transformed)
	}
	if !bytes.Equal(first.UpstreamBody, rt.bodies[0]) {
		t.Errorf("first record must hold the rejected bytes:\n got %q\nwant %q", first.UpstreamBody, rt.bodies[0])
	}
	if !retry.RetryOriginal {
		t.Error("second record must be flagged retry_original")
	}
	if retry.RequestID != first.RequestID {
		t.Errorf("retry record must share the request id: %q vs %q", retry.RequestID, first.RequestID)
	}
	if !bytes.Equal(retry.ClientBody, []byte(chatReqStream)) || retry.Transformed {
		t.Errorf("retry record must hold the original bytes, untransformed: transformed=%v", retry.Transformed)
	}
	if !bytes.Equal(retry.ClientBody, rt.bodies[1]) {
		t.Error("retry record must match what the upstream actually received")
	}
}

// An operator who sets CAVE_CAPTURE_DIR and runs Codex must not get an empty
// directory: the ChatGPT route captures the same two sides.
func TestCaptureRecordsChatGPTRoute(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAVE_CAPTURE_DIR", dir)
	rt := &captureTransport{responses: []string{`{"id":"resp"}`}}
	srv := New(Config{
		Auth:            stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Sink:            &captureSink{},
		ChatGPTUpstream: "https://chatgpt.test/backend-api/codex",
		HTTPClient:      &http.Client{Transport: rt},
	})

	body := `{"model":"gpt-5.5","input":"codex turn"}`
	req := httptest.NewRequest(http.MethodPost, "/chatgpt/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer codex-oauth-secret")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	srv.capture.flush()

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	got := readCaptures(t, dir)
	if len(got) != 1 {
		t.Fatalf("want 1 capture on the chatgpt route, got %d", len(got))
	}
	entry := got[0]
	if entry.Provider != "chatgpt-subscription" || entry.Endpoint != "/responses" {
		t.Errorf("chatgpt capture identity wrong: %s %s", entry.Provider, entry.Endpoint)
	}
	if !bytes.Equal(entry.ClientBody, []byte(body)) {
		t.Errorf("chatgpt capture is not byte-exact: %q", entry.ClientBody)
	}
	if entry.Transformed {
		t.Error("a pass-through chatgpt request must not read as transformed")
	}
}

// The capture directory can disappear under a running proxy. Traffic must not
// notice — the instrument fails open on every error.
func TestCaptureDirDeletedAtRecordTimeLeavesTrafficUnaffected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "captures")
	t.Setenv("CAVE_CAPTURE_DIR", dir)
	rt := &captureTransport{}
	srv := captureProxyServer("record", rt)
	if srv.capture == nil {
		t.Fatal("capture must be enabled")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove capture dir: %v", err)
	}

	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`
	rec := serveCaptureRequest(t, srv, "/v1/chat/completions", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d after the capture dir vanished", rec.Code)
	}
	if len(rt.bodies) != 1 || string(rt.bodies[0]) != body {
		t.Fatalf("upstream body altered: %q", rt.bodies)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("capture must not recreate the directory it was given: %v", err)
	}
}
