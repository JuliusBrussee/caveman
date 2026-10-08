package gateway

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// offeringCloud is a fake link that offers Auto.
type offeringCloud struct {
	*fakeCloud
	offered bool
}

func (c offeringCloud) AutoOffered() bool { return c.offered }

// The shape of a codex-cli 0.160.0 catalog entry (codex debug models), cut down.
const chatGPTCatalog = `{"models":[` +
	`{"slug":"gpt-6-astra","display_name":"GPT-6-Astra","description":"Frontier.","default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"low","description":"l"},{"effort":"high","description":"h"}],"visibility":"list","priority":2,"context_window":272000},` +
	`{"slug":"gpt-6.1-sol","display_name":"GPT-6.1-Sol","description":"Workhorse.","default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"low","description":"l"},{"effort":"medium","description":"m"},{"effort":"xhigh","description":"x"}],"visibility":"list","priority":3,"context_window":272000,"max_context_window":872000,"upgrade":{"model":"gpt-7"},"availability_nux":{"message":"try"}}]}`

func TestChatGPTCatalogListsAutoWhileOffered(t *testing.T) {
	var mu sync.Mutex
	var seen http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("ETag", `"e1"`)
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, chatGPTCatalog)
	}))
	defer upstream.Close()
	for _, offered := range []bool{true, false} {
		srv, _, _ := chatgptTestServer(t, upstream.URL)
		srv.cloud = offeringCloud{&fakeCloud{}, offered}
		req := httptest.NewRequest(http.MethodGet, "/chatgpt/models?client_version=0.160.0", nil)
		req.Header.Set("authorization", "Bearer chatgpt-access")
		req.Header.Set("if-none-match", `"e0"`)
		req.Header.Set("accept-encoding", "gzip")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if !offered {
			if rec.Body.String() != chatGPTCatalog || rec.Header().Get("ETag") == "" {
				t.Errorf("not offered: the catalog changed: %s", rec.Body.String())
			}
			continue
		}
		mu.Lock()
		if seen.Get("if-none-match") != "" || seen.Get("authorization") != "Bearer chatgpt-access" {
			t.Errorf("upstream headers %v", seen)
		}
		mu.Unlock()
		var catalog struct {
			Models []map[string]any `json:"models"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &catalog); err != nil || len(catalog.Models) != 3 || rec.Header().Get("ETag") != "" {
			t.Fatalf("offered: %v %s", err, rec.Body.String())
		}
		auto := catalog.Models[2]
		if auto["slug"] != AutoModel || auto["display_name"] != "Auto" || auto["description"] != autoDescription ||
			auto["priority"] != 4.0 || auto["context_window"] != 872000.0 || auto["max_context_window"] != 872000.0 || auto["default_reasoning_level"] != "medium" ||
			auto["upgrade"] != nil || auto["availability_nux"] != nil {
			t.Errorf("Auto entry = %v", auto)
		}
		if levels, _ := auto["supported_reasoning_levels"].([]any); len(levels) != 3 {
			t.Errorf("Auto efforts = %v, want the fallback model's", auto["supported_reasoning_levels"])
		}
	}
}

// chatGPTAutoUpstream answers /responses with an SSE stream naming the model
// it was sent, refusing refuse with a 400, and records each request.
type chatGPTAutoUpstream struct {
	mu       sync.Mutex
	bodies   []map[string]any
	headers  []http.Header
	refuse   string
	upstream *httptest.Server
}

func newChatGPTAutoUpstream(t *testing.T, refuse string) *chatGPTAutoUpstream {
	u := &chatGPTAutoUpstream{refuse: refuse}
	u.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.mu.Lock()
		u.bodies, u.headers = append(u.bodies, body), append(u.headers, r.Header.Clone())
		u.mu.Unlock()
		model, _ := body["model"].(string)
		if model == u.refuse {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"detail":"model not supported on this plan"}`)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, `event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r","model":"`+model+`"}}`+"\n\n"+
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r","model":"`+model+`","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`+"\n\n")
	}))
	t.Cleanup(u.upstream.Close)
	return u
}

func sendChatGPTAuto(t *testing.T, srv *Server, path string, body []byte, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("authorization", "Bearer chatgpt-access")
	req.Header.Set("ChatGPT-Account-ID", "acct-1")
	for name, value := range header {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

const chatGPTAutoBodyText = `{"model":"caveman-auto","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"fix the bug"}]}]}`

// Auto on a ChatGPT login asks without a pool, runs the answered model and
// effort on the agent's own login, and the agent reads Auto.
func TestChatGPTAutoRoutesOnTheLogin(t *testing.T) {
	u := newChatGPTAutoUpstream(t, "")
	srv, sink, _ := chatgptTestServer(t, u.upstream.URL)
	cloud := &fakeCloud{answer: RouteAnswer{Model: "gpt-6-astra", Effort: "high", Outcome: "routed"}}
	srv.cloud = cloud
	rec := sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "gpt-6-astra") || strings.Count(rec.Body.String(), `"model":"caveman-auto"`) != 2 {
		t.Fatalf("agent read %d %s", rec.Code, rec.Body.String())
	}
	if len(cloud.asks) != 1 || !cloud.asks[0].NoPool || cloud.asks[0].Model != "gpt-6.1-sol" || cloud.asks[0].Provider != "openai" {
		t.Fatalf("asks %+v", cloud.asks)
	}
	got := u.bodies[0]
	reasoning, _ := got["reasoning"].(map[string]any)
	if got["model"] != "gpt-6-astra" || reasoning["effort"] != "high" || u.headers[0].Get("authorization") != "Bearer chatgpt-access" || u.headers[0].Get("ChatGPT-Account-ID") != "acct-1" {
		t.Errorf("upstream got %v with %v", got, u.headers[0])
	}
	row := sink.rows[len(sink.rows)-1]
	if row.RouteFrom != AutoModel || row.RouteTo != "gpt-6-astra" || row.RouteOutcome != "routed" || row.InputTokens != 10 {
		t.Errorf("row %+v", row)
	}
}

// A model the login refuses replays on the fallback model's bytes; without a
// Cloud link, and from OpenCode's ChatGPT login, Auto runs on the fallback.
func TestChatGPTAutoFallsBack(t *testing.T) {
	u := newChatGPTAutoUpstream(t, "gpt-6-astra")
	srv, sink, _ := chatgptTestServer(t, u.upstream.URL)
	rejected := 0
	srv.cloud = &fakeCloud{answer: RouteAnswer{Model: "gpt-6-astra", Effort: "high", Outcome: "routed", Reject: func() { rejected++ }}}
	rec := sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), nil)
	if rec.Code != 200 || len(u.bodies) != 2 || u.bodies[1]["model"] != "gpt-6.1-sol" || u.bodies[1]["reasoning"] != nil || rejected != 1 {
		t.Fatalf("%d %s; upstream %v; rejected %d", rec.Code, rec.Body.String(), u.bodies, rejected)
	}
	if !strings.Contains(rec.Body.String(), `"model":"caveman-auto"`) || sink.rows[len(sink.rows)-1].RouteOutcome != "degraded" {
		t.Errorf("agent read %s, row %+v", rec.Body.String(), sink.rows[len(sink.rows)-1])
	}

	plain, _, _ := chatgptTestServer(t, u.upstream.URL)
	var zbody []byte
	encoder, _ := zstd.NewWriter(nil)
	zbody = encoder.EncodeAll([]byte(chatGPTAutoBodyText), nil)
	rec = sendChatGPTAuto(t, plain, "/chatgpt/responses", zbody, map[string]string{"content-encoding": "zstd"})
	last := len(u.bodies) - 1
	if rec.Code != 200 || u.bodies[last]["model"] != "gpt-6.1-sol" || u.headers[last].Get("content-encoding") != "" || !strings.Contains(rec.Body.String(), `"model":"caveman-auto"`) {
		t.Fatalf("zstd Auto without a link: %d %s, upstream %v", rec.Code, rec.Body.String(), u.bodies[last])
	}
	rec = sendChatGPTAuto(t, plain, "/w/opencode/openai/v1/responses", []byte(chatGPTAutoBodyText), map[string]string{"x-cave-agent": "opencode"})
	last = len(u.bodies) - 1
	if rec.Code != 200 || u.bodies[last]["model"] != "gpt-6.1-sol" || !strings.Contains(rec.Body.String(), `"model":"caveman-auto"`) {
		t.Fatalf("OpenCode on a ChatGPT login: %d %s, upstream %v", rec.Code, rec.Body.String(), u.bodies[last])
	}
}

// An answer naming a model outside AutoOpenAIModels runs gpt-6.1-sol at that
// answer's effort; the ask offered Cloud the three alone.
func TestChatGPTAutoRefusesOtherModels(t *testing.T) {
	u := newChatGPTAutoUpstream(t, "")
	srv, sink, _ := chatgptTestServer(t, u.upstream.URL)
	srv.cloud = &fakeCloud{answer: RouteAnswer{Model: "gpt-6-sol", Effort: "xhigh", Outcome: "routed"}}
	rec := sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), nil)
	reasoning, _ := u.bodies[0]["reasoning"].(map[string]any)
	if rec.Code != 200 || u.bodies[0]["model"] != "gpt-6.1-sol" || reasoning["effort"] != "xhigh" {
		t.Fatalf("%d; upstream %v", rec.Code, u.bodies[0])
	}
	if row := sink.rows[len(sink.rows)-1]; row.RouteOutcome != "degraded" || row.RouteReason != "auto_model_refused" {
		t.Errorf("row %+v", row)
	}
}

// Any other POST naming Auto (Codex compaction) runs gpt-6.1-sol unasked; a
// body over CAVE_MAX_REQUEST_BYTES streams with only its model changed; a
// POST not naming Auto passes through byte for byte.
func TestChatGPTAutoOnEveryPathAndSize(t *testing.T) {
	u := newChatGPTAutoUpstream(t, "")
	srv, _, _ := chatgptTestServer(t, u.upstream.URL)
	cloud := &fakeCloud{answer: RouteAnswer{Model: "gpt-6-astra", Outcome: "routed"}}
	srv.cloud = cloud
	rec := sendChatGPTAuto(t, srv, "/chatgpt/responses/compact", []byte(`{"model":"caveman-auto","input":[]}`), nil)
	if rec.Code != 200 || u.bodies[0]["model"] != "gpt-6.1-sol" || len(cloud.asks) != 0 || !strings.Contains(rec.Body.String(), `"model":"caveman-auto"`) {
		t.Fatalf("compact: %d %s, upstream %v, asks %d", rec.Code, rec.Body.String(), u.bodies[0], len(cloud.asks))
	}
	t.Setenv("CAVE_MAX_REQUEST_BYTES", "1024")
	big := `{"model":"caveman-auto","stream":true,"input":[{"role":"user","content":"` + strings.Repeat("lorem ipsum dolor ", 5000) + `"}]}`
	rec = sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(big), nil)
	if last := u.bodies[len(u.bodies)-1]; rec.Code != 200 || last["model"] != "gpt-6.1-sol" || len(cloud.asks) != 0 || !strings.Contains(rec.Body.String(), `"model":"caveman-auto"`) {
		t.Fatalf("big: %d, upstream model %v, asks %d", rec.Code, last["model"], len(cloud.asks))
	}
	plain := `{"model":"gpt-6-astra","stream":true,"input":[{"role":"user","content":"` + strings.Repeat("lorem ipsum dolor ", 5000) + `"}]}`
	rec = sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(plain), nil)
	if last := u.bodies[len(u.bodies)-1]; rec.Code != 200 || last["model"] != "gpt-6-astra" || strings.Contains(rec.Body.String(), "caveman-auto") {
		t.Fatalf("plain: %d, upstream model %v", rec.Code, last["model"])
	}
}

func TestPrefixModelFindsOnlyTheTopLevelModel(t *testing.T) {
	for body, want := range map[string]string{
		`{"model":"caveman-auto","x":1}`:           `"caveman-auto"`,
		`{"input":[{"model":"a"}], "model" : "b"}`: `"b"`,
		`{"input":"\"model\":\"a\"","model":"c"}`:  `"c"`,
		`{"metadata":{"model":"a"},"model":"d"`:    `"d"`,
		`{"input":[{"model":"a"}]`:                 "",
		`{"model":"caveman-a`:                      "",
		`{"tags":["model"],"model":"e"}`:           `"e"`,
	} {
		start, end, ok := prefixModel([]byte(body))
		if got := map[bool]string{true: body[start:end], false: ""}[ok]; got != want {
			t.Errorf("prefixModel(%s) = %q, want %q", body, got, want)
		}
	}
}

// encodedBody compresses a request body the way an agent would send it;
// "raw-deflate" is a deflate stream without the zlib wrapper, which some
// clients send as Content-Encoding: deflate.
func encodedBody(t *testing.T, encoding string, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.WriteCloser
	switch encoding {
	case "gzip":
		w = gzip.NewWriter(&buf)
	case "deflate":
		w = zlib.NewWriter(&buf)
	case "raw-deflate":
		w, _ = flate.NewWriter(&buf, flate.DefaultCompression)
	case "zstd":
		w, _ = zstd.NewWriter(&buf)
	}
	_, _ = w.Write(raw)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// noisyText is text an encoder shrinks only a few times over, so an encoded
// body stays over a small CAVE_MAX_REQUEST_BYTES while its first zstd block
// still fits the sniffed head.
func noisyText(n int) string {
	words := strings.Fields("fix the bug in parser when token stream ends early and retry with backoff after timeout")
	random := rand.New(rand.NewSource(1))
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(words[random.Intn(len(words))] + " ")
	}
	return b.String()
}

// goroutinesSettleAt waits for the goroutine count to fall back to limit.
func goroutinesSettleAt(limit int) int {
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > limit && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

// The sniff stops 64 KiB into the decoded head; a zstd decoder left open
// there must not outlive the call.
func TestChatGPTAutoSniffLeavesNoDecoderBehind(t *testing.T) {
	head := encodedBody(t, "zstd", []byte(`{"model":"caveman-auto","input":"`+strings.Repeat("lorem ipsum dolor ", 60000)+`"}`))
	if !namesAutoHead(head, "zstd") {
		t.Fatal("the zstd head names Auto")
	}
	before := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		namesAutoHead(head, "zstd")
	}
	if after := goroutinesSettleAt(before + 2); after > before+2 {
		t.Fatalf("goroutines %d -> %d after 50 sniffs", before, after)
	}
}

// An encoded body over CAVE_MAX_REQUEST_BYTES streams decoded on the fallback
// model: the literal id never goes upstream, no stale Content-Encoding or
// Content-Length rides along, and no decoder is left behind.
func TestChatGPTAutoOversizedEncodedBodies(t *testing.T) {
	text := noisyText(600 << 10)
	body := []byte(`{"model":"caveman-auto","stream":true,"input":"` + text + `"}`)
	for _, encoding := range []string{"gzip", "zstd", "deflate", "raw-deflate"} {
		t.Run(encoding, func(t *testing.T) {
			u := newChatGPTAutoUpstream(t, "")
			srv, sink, _ := chatgptTestServer(t, u.upstream.URL)
			cloud := &fakeCloud{answer: RouteAnswer{Model: "gpt-6-astra", Outcome: "routed"}}
			srv.cloud = cloud
			t.Setenv("CAVE_MAX_REQUEST_BYTES", "1024")
			header := map[string]string{"Content-Encoding": strings.TrimPrefix(encoding, "raw-")}
			sent := encodedBody(t, encoding, body)
			sendChatGPTAuto(t, srv, "/chatgpt/responses", sent, header) // opens the upstream connection
			before := runtime.NumGoroutine()
			for i := 0; i < 10; i++ {
				rec := sendChatGPTAuto(t, srv, "/chatgpt/responses", sent, header)
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"model":"caveman-auto"`) {
					t.Fatalf("agent read %d %s", rec.Code, rec.Body.String())
				}
			}
			if after := goroutinesSettleAt(before + 3); after > before+3 {
				t.Errorf("goroutines %d -> %d after 10 requests", before, after)
			}
			u.mu.Lock()
			defer u.mu.Unlock()
			got, gotHeader := u.bodies[len(u.bodies)-1], u.headers[len(u.headers)-1]
			if got["model"] != "gpt-6.1-sol" || got["input"] != text || len(cloud.asks) != 0 {
				t.Errorf("upstream model %v, input intact %v, asks %d", got["model"], got["input"] == text, len(cloud.asks))
			}
			if gotHeader.Get("Content-Encoding") != "" || gotHeader.Get("Content-Length") != "" {
				t.Errorf("upstream headers %v", gotHeader)
			}
			if row := sink.rows[len(sink.rows)-1]; row.RouteFrom != AutoModel || row.RouteReason != "auto_body_too_large" {
				t.Errorf("row %+v", row)
			}
		})
	}
}

// Content-Encoding: deflate without the zlib wrapper is still read: the turn
// is routed like any other.
func TestChatGPTAutoReadsRawDeflate(t *testing.T) {
	u := newChatGPTAutoUpstream(t, "")
	srv, _, _ := chatgptTestServer(t, u.upstream.URL)
	cloud := &fakeCloud{answer: RouteAnswer{Model: "gpt-6-astra", Outcome: "routed"}}
	srv.cloud = cloud
	for _, encoding := range []string{"deflate", "raw-deflate"} {
		rec := sendChatGPTAuto(t, srv, "/chatgpt/responses", encodedBody(t, encoding, []byte(chatGPTAutoBodyText)), map[string]string{"Content-Encoding": "deflate"})
		if last := u.bodies[len(u.bodies)-1]; rec.Code != 200 || last["model"] != "gpt-6-astra" || u.headers[len(u.headers)-1].Get("Content-Encoding") != "" {
			t.Fatalf("%s: %d, upstream %v", encoding, rec.Code, last)
		}
	}
	if len(cloud.asks) != 2 {
		t.Fatalf("asks %d", len(cloud.asks))
	}
}

// A body that fits but does not decode whole is not "too large": it records
// its own reason, and its decoder is closed when the upstream send aborts.
func TestChatGPTAutoBrokenEncodingHasItsOwnReason(t *testing.T) {
	u := newChatGPTAutoUpstream(t, "")
	srv, sink, _ := chatgptTestServer(t, u.upstream.URL)
	body := encodedBody(t, "zstd", []byte(`{"model":"caveman-auto","stream":true,"input":"`+noisyText(600<<10)+`"}`))
	cut := body[:len(body)-100]
	before := runtime.NumGoroutine()
	for i := 0; i < 10; i++ {
		if rec := sendChatGPTAuto(t, srv, "/chatgpt/responses", cut, map[string]string{"Content-Encoding": "zstd"}); rec.Code == 200 {
			t.Fatalf("a cut body answered %d", rec.Code)
		}
	}
	if after := goroutinesSettleAt(before + 3); after > before+3 {
		t.Errorf("goroutines %d -> %d after 10 aborted sends", before, after)
	}
	if row := sink.rows[len(sink.rows)-1]; row.RouteReason != "auto_body_decode_failed" {
		t.Errorf("row %+v", row)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, got := range u.bodies {
		if got["model"] == AutoModel {
			t.Fatalf("the literal id went upstream: %v", got["model"])
		}
	}
}

// A zstd block that barely compresses is up to 128 KiB as sent: the head read
// covers one whole block, so the model at its start is still seen.
func TestChatGPTAutoSniffReadsAWholeZstdBlock(t *testing.T) {
	// Random base64-like text: about three quarters of its size once encoded.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	random := rand.New(rand.NewSource(1))
	image := make([]byte, 600<<10)
	for i := range image {
		image[i] = alphabet[random.Intn(len(alphabet))]
	}
	sent := encodedBody(t, "zstd", []byte(`{"model":"caveman-auto","input":"`+string(image)+`"}`))
	if len(sent) <= autoSniffRaw {
		t.Fatalf("body of %d bytes fits the head; the test proves nothing", len(sent))
	}
	if namesAutoHead(sent[:autoSniffBytes], "zstd") {
		t.Skip("this encoder's first block fits 64 KiB; nothing to prove")
	}
	if !namesAutoHead(sent[:autoSniffRaw], "zstd") {
		t.Fatal("the head read does not reach the end of the first zstd block")
	}
}

// A login that refuses a configuration_update: one retry at Cloud's effort
// top-level, then the session is latched and later requests go out once.
func TestChatGPTAutoRefusedUpdateHealsAndLatches(t *testing.T) {
	var bodies [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, raw)
		w.Header().Set("content-type", "application/json")
		if bytes.Contains(raw, []byte(`"configuration_update"`)) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"The 'configuration_update' item type is not supported with pro or tournament models."}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"r","object":"response","model":"gpt-6.1-sol","output":[],"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	defer upstream.Close()
	srv, _, _ := chatgptTestServer(t, upstream.URL)
	rejected := 0
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message", Reject: func() { rejected++ }}}
	srv.cloud = cloud
	turn := func(effort string, items ...string) string {
		return strings.Replace(thread(effort, items...), "gpt-6-sol", "gpt-6.1-sol", 1)
	}
	send := func(items ...string) int {
		before := len(bodies)
		body := strings.Replace(turn("high", items...), "gpt-6.1-sol", AutoModel, 1)
		if rec := sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(body), map[string]string{"session_id": "codex-1"}); rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		return len(bodies) - before
	}
	if sends := send(rA, rB, rC); sends != 2 || string(bodies[0]) != turn("high", rA, rB, update("low"), rC) || string(bodies[1]) != turn("low", rA, rB, rC) {
		t.Fatalf("heal: %d sends, %s", sends, bodies)
	}
	if sends := send(rA, rB, rC, rE, rF); sends != 1 || string(bodies[2]) != turn("low", rA, rB, rC, rE, rF) || !cloud.asks[len(cloud.asks)-1].PerMessageOff || rejected != 0 {
		t.Fatalf("after the latch: %d sends, last %s, latched %v, rejected %d", sends, bodies[len(bodies)-1], cloud.asks[len(cloud.asks)-1].PerMessageOff, rejected)
	}
}

// A rate-limit 429 or an auth failure on routed bytes is returned, never
// replayed: the fallback model's bytes cannot beat the limit or the login.
func TestChatGPTAutoReturnsRateLimitsAndAuthFailures(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusForbidden} {
		calls := 0
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"detail":"no"}`)
		}))
		srv, _, _ := chatgptTestServer(t, upstream.URL)
		rejected := 0
		srv.cloud = &fakeCloud{answer: RouteAnswer{Model: "gpt-6-astra", Effort: "high", Outcome: "routed", Reject: func() { rejected++ }}}
		rec := sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), nil)
		upstream.Close()
		if rec.Code != status || calls != 1 || rejected != 0 {
			t.Errorf("status %d: agent read %d after %d upstream requests, rejected %d; want it returned once", status, rec.Code, calls, rejected)
		}
	}
}

// A refusal the fallback model's own bytes get too was not the route stage's:
// the ask keeps its decision.
func TestChatGPTAutoKeepsTheDecisionWhenTheReplayFailsToo(t *testing.T) {
	u := newChatGPTAutoUpstream(t, "")
	u.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = io.WriteString(w, `{"detail":"too large"}`)
	})
	srv, _, _ := chatgptTestServer(t, u.upstream.URL)
	rejected := 0
	srv.cloud = &fakeCloud{answer: RouteAnswer{Model: "gpt-6-astra", Outcome: "routed", Reject: func() { rejected++ }}}
	if rec := sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), nil); rec.Code != http.StatusRequestEntityTooLarge || rejected != 0 {
		t.Fatalf("agent read %d, rejected %d; want the 413 and the decision kept", rec.Code, rejected)
	}
}

// A transient Cloud failure runs the model the session was last served by;
// a limit or a refused login runs the fallback.
func TestChatGPTAutoCloudFailureKeepsTheSessionsModel(t *testing.T) {
	for _, tc := range []struct {
		answer RouteAnswer
		want   string
	}{
		{RouteAnswer{Outcome: "degraded", Reason: "timeout"}, "gpt-6-astra"},
		{RouteAnswer{Outcome: "degraded", Reason: "cloud_503"}, "gpt-6-astra"},
		{RouteAnswer{Outcome: "paused", Reason: "allowance"}, "gpt-6.1-sol"},
		{RouteAnswer{Outcome: "degraded", Reason: "cloud_401"}, "gpt-6.1-sol"},
		{RouteAnswer{Outcome: "off"}, "gpt-6.1-sol"},
	} {
		u := newChatGPTAutoUpstream(t, "")
		srv, sink, _ := chatgptTestServer(t, u.upstream.URL)
		cloud := &fakeCloud{answer: RouteAnswer{Model: "gpt-6-astra", Outcome: "routed"}}
		srv.cloud = cloud
		session := map[string]string{"session_id": "codex-1"}
		sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), session)
		cloud.mu.Lock()
		cloud.answer = tc.answer
		cloud.mu.Unlock()
		rec := sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), session)
		if rec.Code != 200 || len(u.bodies) != 2 || u.bodies[1]["model"] != tc.want || strings.Contains(rec.Body.String(), "gpt-6") {
			t.Errorf("%s %s: upstream %v, want %s; agent read %s", tc.answer.Outcome, tc.answer.Reason, u.bodies, tc.want, rec.Body.String())
		}
		if row := sink.rows[len(sink.rows)-1]; row.RouteOutcome != tc.answer.Outcome || row.RouteReason != tc.answer.Reason {
			t.Errorf("%s %s: row outcome %q reason %q", tc.answer.Outcome, tc.answer.Reason, row.RouteOutcome, row.RouteReason)
		}
	}
	// A session with no previous request runs the fallback.
	u := newChatGPTAutoUpstream(t, "")
	srv, _, _ := chatgptTestServer(t, u.upstream.URL)
	srv.cloud = &fakeCloud{answer: RouteAnswer{Outcome: "degraded", Reason: "timeout"}}
	sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), map[string]string{"session_id": "codex-2"})
	if u.bodies[0]["model"] != "gpt-6.1-sol" {
		t.Errorf("first request of a session: upstream %v", u.bodies[0])
	}
}

// The Auto id with Claude Code's [1m] suffix is Auto: the fallback model goes
// upstream and the agent reads the id it sent.
func TestChatGPTAutoTakesThe1MSuffix(t *testing.T) {
	u := newChatGPTAutoUpstream(t, "")
	srv, _, _ := chatgptTestServer(t, u.upstream.URL)
	body := strings.Replace(chatGPTAutoBodyText, AutoModel, AutoModel+"[1m]", 1)
	rec := sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(body), nil)
	if rec.Code != 200 || len(u.bodies) != 1 || u.bodies[0]["model"] != "gpt-6.1-sol" || strings.Count(rec.Body.String(), `"model":"caveman-auto[1m]"`) != 2 {
		t.Fatalf("%d %s; upstream %v", rec.Code, rec.Body.String(), u.bodies)
	}
}

// An accepted replay of the fallback model's own bytes pins the conversation
// raw, as on a named model: the provider cached those bytes.
func TestChatGPTAutoAcceptedReplayPinsTheConversationRaw(t *testing.T) {
	first, second, third := strings.Repeat("codex first output ", 40), strings.Repeat("codex second output ", 40), strings.Repeat("codex third output ", 40)
	turn := func(model string, texts ...string) string {
		return strings.Replace(chatGPTTurn(texts...), "gpt-5.5", model, 1)
	}
	rt := &captureTransport{statuses: []int{http.StatusOK, http.StatusBadRequest, http.StatusOK, http.StatusOK}}
	srv := chatGPTCompressServer(rt)
	serveChatGPT(srv, turn(AutoModel, first))
	serveChatGPT(srv, turn(AutoModel, first, second))
	serveChatGPT(srv, turn(AutoModel, first, second, third))
	if len(rt.bodies) != 4 || bytes.Contains(rt.bodies[0], []byte(first)) || bytes.Contains(rt.bodies[1], []byte(first)) || string(rt.bodies[2]) != turn("gpt-6.1-sol", first, second) {
		t.Fatalf("test setup: want turn 1 compressed and turn 2 replayed raw, got %d calls", len(rt.bodies))
	}
	if string(rt.bodies[3]) != turn("gpt-6.1-sol", first, second, third) {
		t.Fatalf("the turn after an accepted replay must go out as sent:\n%s", rt.bodies[3])
	}
}

// A 429 without Retry-After on the routed model and on the replay: the
// decision is rejected, so the agent's own retries send one request each.
func TestChatGPTAutoRateLimitedReplayDoesNotDoubleTheAgentsRetries(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"detail":"usage limit"}`)
	}))
	defer upstream.Close()
	srv, _, _ := chatgptTestServer(t, upstream.URL)
	srv.cloud = rejectingCloud(RouteAnswer{Model: "gpt-6-astra", Outcome: "routed"})
	for attempt, want := range []int{2, 3, 4} {
		if rec := sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), nil); rec.Code != http.StatusTooManyRequests || calls != want {
			t.Fatalf("attempt %d: status %d, %d upstream sends so far, want %d", attempt+1, rec.Code, calls, want)
		}
	}
}

// A held model that does not serve is not held again, and a side request is
// never held on the session's model.
func TestChatGPTAutoHeldModelGivesWay(t *testing.T) {
	session := map[string]string{"session_id": "codex-1"}
	held := func() (*chatGPTAutoUpstream, *Server) {
		u := newChatGPTAutoUpstream(t, "")
		srv, _, _ := chatgptTestServer(t, u.upstream.URL)
		cloud := &fakeCloud{answer: RouteAnswer{Model: "gpt-6-astra", Outcome: "routed"}}
		srv.cloud = cloud
		sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), session)
		cloud.mu.Lock()
		cloud.answer = RouteAnswer{Outcome: "degraded", Reason: "timeout"}
		cloud.mu.Unlock()
		return u, srv
	}
	u, srv := held()
	inner := u.upstream.Config.Handler
	u.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if bytes.Contains(raw, []byte("gpt-6-astra")) {
			u.mu.Lock()
			u.bodies = append(u.bodies, map[string]any{"model": "gpt-6-astra"})
			u.mu.Unlock()
			w.WriteHeader(529)
			return
		}
		inner.ServeHTTP(w, r)
	})
	sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), session)
	sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), session)
	if len(u.bodies) != 3 || u.bodies[1]["model"] != "gpt-6-astra" || u.bodies[2]["model"] != "gpt-6.1-sol" {
		t.Errorf("held model answering 529: upstream %v, want it once and then the fallback", u.bodies)
	}

	u, srv = held()
	side := map[string]string{"session_id": "codex-1", "x-openai-subagent": "title"}
	sendChatGPTAuto(t, srv, "/chatgpt/responses", []byte(chatGPTAutoBodyText), side)
	if len(u.bodies) != 2 || u.bodies[1]["model"] != "gpt-6.1-sol" {
		t.Errorf("side request: upstream %v, want the fallback", u.bodies)
	}
}
