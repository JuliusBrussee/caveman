package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
			auto["priority"] != 4.0 || auto["context_window"] != 272000.0 || auto["default_reasoning_level"] != "medium" ||
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
