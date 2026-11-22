package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

func localTurnEffortAnthropicServer(t *testing.T, respond func(body []byte, hdr http.Header) (int, string)) (*Server, *upstreamLog, *captureSink) {
	t.Helper()
	log := &upstreamLog{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		log.mu.Lock()
		log.bodies = append(log.bodies, raw)
		log.headers = append(log.headers, r.Header.Clone())
		log.mu.Unlock()
		status := http.StatusOK
		answer := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":100,"cache_read_input_tokens":50000,"cache_creation_input_tokens":0,"output_tokens":10}}`
		if strings.HasSuffix(r.URL.Path, "/count_tokens") {
			answer = `{"input_tokens":50100}`
		}
		if respond != nil {
			if code, text := respond(raw, r.Header); code != 0 {
				status, answer = code, text
			}
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(upstream.Close)

	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sink := &captureSink{}
	srv := New(Config{
		Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")},
		Auth: stubAuth{rc: RequestContext{
			Label:       "local",
			RuntimeMode: "compress",
			Optimizers:  map[string]bool{providers.TurnEffortOptimizerID: true},
		}},
		Creds:          stubCreds{key: "sk-ant-test"},
		Sink:           sink,
		Compressor:     &dedupTestCompressor{eng: engine.New(store, nil), store: store},
		PrefixCache:    newMemPrefixCache(),
		RecoveryViaMCP: true,
		HTTPClient:     &http.Client{Transport: toStub(upstream.URL)},
	})
	return srv, log, sink
}

func localTurnEffortOpenAIServer(t *testing.T, respond func(body []byte) (int, string)) (*Server, *upstreamLog, *captureSink) {
	t.Helper()
	log := &upstreamLog{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		log.mu.Lock()
		log.bodies = append(log.bodies, raw)
		log.headers = append(log.headers, r.Header.Clone())
		log.mu.Unlock()
		status := http.StatusOK
		answer := `{"id":"resp_1","object":"response","model":"gpt-6-sol","output":[],"usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010,"input_tokens_details":{"cached_tokens":900}}}`
		if respond != nil {
			if code, text := respond(raw); code != 0 {
				status, answer = code, text
			}
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(upstream.Close)

	sink := &captureSink{}
	srv := New(Config{
		Adapters: []providers.Adapter{openai.New("https://api.openai.com")},
		Auth: stubAuth{rc: RequestContext{
			Label:       "local",
			RuntimeMode: "compress",
			Optimizers:  map[string]bool{providers.TurnEffortOptimizerID: true},
		}},
		Creds:      stubCreds{key: "sk-openai-test"},
		Sink:       sink,
		HTTPClient: &http.Client{Transport: toStub(upstream.URL)},
	})
	return srv, log, sink
}

func postLocalAnthropic(t *testing.T, srv *Server, path, body string, extra map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "sk-ant-test")
	req.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestLocalTurnEffortOpus55IsCacheSafeEndToEnd(t *testing.T) {
	srv, log, sink := localTurnEffortAnthropicServer(t, nil)
	hdrs := map[string]string{"x-cave-session": "opus55-local-1"}

	// Turn 1: initial human prompt -> untouched (no marks, no beta, top-level high preserved).
	turn1 := convo("high", uA)
	if rec := postLocalAnthropic(t, srv, "/v1/messages", turn1, hdrs); rec.Code != http.StatusOK {
		t.Fatalf("turn 1 status=%d body=%s", rec.Code, rec.Body.String())
	}
	sent1, hdr1 := log.last()
	if string(sent1) != turn1 {
		t.Fatalf("turn 1 must remain byte-identical:\n got %s\nwant %s", sent1, turn1)
	}
	if hdr1.Get("anthropic-beta") != "" {
		t.Fatalf("turn 1 must not add beta header, got %q", hdr1.Get("anthropic-beta"))
	}

	// Turn 2: clean mechanical tool_result -> appends mark("low") at the end,
	// top-level output_config.effort stays frozen at "high", beta header added.
	turn2 := convo("high", uA, aD, uTR)
	if rec := postLocalAnthropic(t, srv, "/v1/messages", turn2, hdrs); rec.Code != http.StatusOK {
		t.Fatalf("turn 2 status=%d body=%s", rec.Code, rec.Body.String())
	}
	sent2, hdr2 := log.last()
	want2 := convo("high", uA, aD, uTR, mark("low"))
	if string(sent2) != want2 {
		t.Fatalf("turn 2:\n got %s\nwant %s", sent2, want2)
	}
	if hdr2.Get("anthropic-beta") != perMessageBeta {
		t.Fatalf("turn 2 anthropic-beta=%q want %q", hdr2.Get("anthropic-beta"), perMessageBeta)
	}
	if row2 := sink.last(t); row2.CacheBust {
		t.Fatalf("turn 2 caused cache bust: %q", row2.CacheBustCause)
	}

	// Turn 3: second clean tool_result -> replays mark("low") at exact position,
	// no duplicate mark appended because effort in force is already "low".
	turn3 := convo("high", uA, aD, uTR, aG, uTR2)
	if rec := postLocalAnthropic(t, srv, "/v1/messages", turn3, hdrs); rec.Code != http.StatusOK {
		t.Fatalf("turn 3 status=%d body=%s", rec.Code, rec.Body.String())
	}
	sent3, _ := log.last()
	want3 := convo("high", uA, aD, uTR, mark("low"), aG, uTR2)
	if string(sent3) != want3 {
		t.Fatalf("turn 3:\n got %s\nwant %s", sent3, want3)
	}
	if !bytes.HasPrefix(sent3, sent2[:len(sent2)-2]) {
		t.Fatalf("turn 3 broke prefix from turn 2:\nturn2: %s\nturn3: %s", sent2, sent3)
	}
	if row3 := sink.last(t); row3.CacheBust {
		t.Fatalf("turn 3 caused cache bust: %q", row3.CacheBustCause)
	}

	// count_tokens replays the exact session marks without adding any new mark.
	if rec := postLocalAnthropic(t, srv, "/v1/messages/count_tokens", turn3, hdrs); rec.Code != http.StatusOK {
		t.Fatalf("count_tokens status=%d", rec.Code)
	}
	sentCT, hdrCT := log.last()
	if string(sentCT) != want3 || hdrCT.Get("anthropic-beta") != perMessageBeta {
		t.Fatalf("count_tokens mismatch:\n got %s (beta=%q)\nwant %s", sentCT, hdrCT.Get("anthropic-beta"), want3)
	}

	// Turn 4: failing tool_result (is_error:true) -> replays mark("low") AND
	// appends mark("high") at the end to restore caller's full effort cache-safely!
	uTRFail := `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t3","is_error":true,"content":"--- FAIL: TestAuth"}]}`
	aFailCall := `{"role":"assistant","content":[{"type":"tool_use","id":"t3","name":"Bash","input":{}}]}`
	turn4 := convo("high", uA, aD, uTR, aG, uTR2, aFailCall, uTRFail)
	if rec := postLocalAnthropic(t, srv, "/v1/messages", turn4, hdrs); rec.Code != http.StatusOK {
		t.Fatalf("turn 4 status=%d", rec.Code)
	}
	sent4, _ := log.last()
	want4 := convo("high", uA, aD, uTR, mark("low"), aG, uTR2, aFailCall, uTRFail, mark("high"))
	if string(sent4) != want4 {
		t.Fatalf("turn 4 restore on error:\n got %s\nwant %s", sent4, want4)
	}
	if !bytes.HasPrefix(sent4, sent3[:len(sent3)-2]) {
		t.Fatalf("turn 4 broke prefix from turn 3:\nturn3: %s\nturn4: %s", sent3, sent4)
	}
	if row4 := sink.last(t); row4.CacheBust {
		t.Fatalf("turn 4 caused cache bust: %q", row4.CacheBustCause)
	}

	// Turn 5: new human prompt -> replays both marks identically; effort is already "high" so no third mark needed.
	turn5 := convo("high", uA, aD, uTR, aG, uTR2, aFailCall, uTRFail, aE, uF)
	if rec := postLocalAnthropic(t, srv, "/v1/messages", turn5, hdrs); rec.Code != http.StatusOK {
		t.Fatalf("turn 5 status=%d", rec.Code)
	}
	sent5, _ := log.last()
	want5 := convo("high", uA, aD, uTR, mark("low"), aG, uTR2, aFailCall, uTRFail, mark("high"), aE, uF)
	if string(sent5) != want5 {
		t.Fatalf("turn 5:\n got %s\nwant %s", sent5, want5)
	}
	if !bytes.HasPrefix(sent5, sent4[:len(sent4)-2]) {
		t.Fatalf("turn 5 broke prefix from turn 4")
	}
	if row5 := sink.last(t); row5.CacheBust {
		t.Fatalf("turn 5 caused cache bust: %q", row5.CacheBustCause)
	}
}

func TestLocalTurnEffortOpus55DefaultEffortAndNoSessionHeaderFallback(t *testing.T) {
	srv, log, _ := localTurnEffortAnthropicServer(t, nil)

	// Caller omits top-level output_config AND omits any session header (like Hermes / bare SDK).
	noTop := func(msgs ...string) string {
		return `{"model":"claude-opus-5-5","max_tokens":1024,"system":"You are helpful.","messages":[` + strings.Join(msgs, ",") + `]}`
	}
	// Turn 1: untouched
	postLocalAnthropic(t, srv, "/v1/messages", noTop(uA), nil)
	if sent, _ := log.last(); string(sent) != noTop(uA) {
		t.Fatalf("turn 1 altered: %s", sent)
	}
	// Turn 2: clean tool_result -> steps down from default medium to mark("low") using prefix-derived conv key!
	postLocalAnthropic(t, srv, "/v1/messages", noTop(uA, aD, uTR), nil)
	sent2, hdr2 := log.last()
	if want2 := noTop(uA, aD, uTR, mark("low")); string(sent2) != want2 || hdr2.Get("anthropic-beta") != perMessageBeta {
		t.Fatalf("turn 2 no-header fallback:\n got %s (beta=%q)\nwant %s", sent2, hdr2.Get("anthropic-beta"), want2)
	}
	// Turn 3: human follow-up -> replays mark("low") and restores Opus 5.5 default "medium" via mark("medium")!
	postLocalAnthropic(t, srv, "/v1/messages", noTop(uA, aD, uTR, aE, uF), nil)
	sent3, _ := log.last()
	if want3 := noTop(uA, aD, uTR, mark("low"), aE, mark("medium"), uF); string(sent3) != want3 {
		t.Fatalf("turn 3 restore to default medium:\n got %s\nwant %s", sent3, want3)
	}
}

func TestLocalTurnEffortSkipsUnsupportedModelsAndHealsWithoutTopLevelMutation(t *testing.T) {
	// 1. Claude 4.x models do NOT support per-message marks -> pass through untouched (0 cache bust).
	srv, log, _ := localTurnEffortAnthropicServer(t, nil)
	for _, model := range []string{"claude-sonnet-4-6", "claude-opus-4-6", "claude-opus-4-7", "claude-haiku-4-5-20251001"} {
		body := `{"model":"` + model + `","max_tokens":1024,"output_config":{"effort":"high"},"messages":[` + strings.Join([]string{uA, aD, uTR}, ",") + `]}`
		postLocalAnthropic(t, srv, "/v1/messages", body, map[string]string{"x-cave-session": "s-" + model})
		if sent, hdr := log.last(); string(sent) != body || hdr.Get("anthropic-beta") != "" {
			t.Fatalf("unsupported model %s must stay byte-identical (0 cache bust), got %s beta=%q", model, sent, hdr.Get("anthropic-beta"))
		}
	}

	// 2. Opus 5.5 with thinking.type="between_tools" or "enabled" stays untouched.
	betweenTools := `{"model":"claude-opus-5-5","max_tokens":4096,"thinking":{"type":"between_tools"},"output_config":{"effort":"high"},"messages":[` + strings.Join([]string{uA, aD, uTR}, ",") + `]}`
	postLocalAnthropic(t, srv, "/v1/messages", betweenTools, map[string]string{"x-cave-session": "s-bt"})
	if sent, _ := log.last(); string(sent) != betweenTools {
		t.Fatalf("between_tools must stay untouched: %s", sent)
	}

	// 3. If an upstream refuses per-message marks with 400, local turn-effort heals
	//    to the EXACT UNMARKED body (never mutating top-level effort!) and latches.
	refusal := `{"type":"error","error":{"type":"invalid_request_error","message":"output_config.effort requires a model that supports per-turn effort; this model does not"}}`
	srvRef, logRef, _ := localTurnEffortAnthropicServer(t, func(body []byte, _ http.Header) (int, string) {
		if bytes.Contains(body, []byte(mark("low"))) {
			return http.StatusBadRequest, refusal
		}
		return 0, ""
	})
	turn2 := convo("high", uA, aD, uTR)
	rec := postLocalAnthropic(t, srvRef, "/v1/messages", turn2, map[string]string{"x-cave-session": "s-refuse"})
	if rec.Code != http.StatusOK {
		t.Fatalf("refusal heal failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(logRef.bodies) != 2 || string(logRef.bodies[1]) != turn2 {
		t.Fatalf("refusal heal must retry exact unmarked body without top-level effort mutation:\n attempts=%d\n got  %s\n want %s", len(logRef.bodies), logRef.bodies[len(logRef.bodies)-1], turn2)
	}
	if strings.Contains(rec.Header().Get("x-cave-optimization"), providers.TurnEffortOptimizerID) {
		t.Fatalf("healed unmarked request must not claim turn-effort-step: %q", rec.Header().Get("x-cave-optimization"))
	}
	// Next turn on latched session goes straight through in 1 attempt, untouched!
	turn3 := convo("high", uA, aD, uTR, aG, uTR2)
	postLocalAnthropic(t, srvRef, "/v1/messages", turn3, map[string]string{"x-cave-session": "s-refuse"})
	if len(logRef.bodies) != 3 || string(logRef.bodies[2]) != turn3 {
		t.Fatalf("latched session must skip marks without extra retries: attempts=%d body=%s", len(logRef.bodies), logRef.bodies[len(logRef.bodies)-1])
	}
}

func TestLocalTurnEffortOpenAIGPT6CacheSafeAndSkipsGPT5AndChat(t *testing.T) {
	srv, log, _ := localTurnEffortOpenAIServer(t, nil)
	postOA := func(path, body, sess string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-openai-test")
		req.Header.Set("session_id", sess)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}

	// 1. GPT-6 mechanical user continuation ("continue") -> inserts configuration_update
	//    before user message while keeping top-level reasoning.effort="high" frozen!
	rCont := `{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}`
	postOA("/v1/responses", thread("high", rA, rB, rCont), "gpt6-local-1")
	sent1, _ := log.last()
	if want := thread("high", rA, rB, update("low"), rCont); string(sent1) != want {
		t.Fatalf("GPT-6 mechanical continuation:\n got %s\nwant %s", sent1, want)
	}

	// 2. Mid-tool-loop (function_call_output) -> replays configuration_update verbatim,
	//    never appends an illegal trailing update!
	postOA("/v1/responses", thread("high", rA, rB, rCont, rD, rTR), "gpt6-local-1")
	sent2, _ := log.last()
	if want := thread("high", rA, rB, update("low"), rCont, rD, rTR); string(sent2) != want {
		t.Fatalf("GPT-6 tool loop replay:\n got %s\nwant %s", sent2, want)
	}
	if !bytes.HasPrefix(sent2, sent1[:len(sent1)-2]) {
		t.Fatalf("GPT-6 tool loop broke prefix")
	}

	// 3. Substantive next human prompt -> replays first update AND restores "high" before rF!
	rSub := `{"type":"message","role":"user","content":[{"type":"input_text","text":"Now design the sharding architecture for multi-region writes."}]}`
	postOA("/v1/responses", thread("high", rA, rB, rCont, rD, rTR, rE, rSub), "gpt6-local-1")
	sent3, _ := log.last()
	if want := thread("high", rA, rB, update("low"), rCont, rD, rTR, rE, update("high"), rSub); string(sent3) != want {
		t.Fatalf("GPT-6 substantive restore:\n got %s\nwant %s", sent3, want)
	}
	if !bytes.HasPrefix(sent3, sent2[:len(sent2)-2]) {
		t.Fatalf("GPT-6 restore broke prefix")
	}

	// 4. GPT-5.4 Responses and Chat Completions NEVER mutate top-level effort (0 cache bust!).
	gpt54 := `{"model":"gpt-5.4","reasoning":{"effort":"xhigh"},"input":[` + strings.Join([]string{rA, rD, rTR}, ",") + `]}`
	postOA("/v1/responses", gpt54, "gpt54-local")
	if sent, _ := log.last(); string(sent) != gpt54 {
		t.Fatalf("GPT-5.4 Responses must NOT mutate top-level effort (cache-safety): %s", sent)
	}
	chatBody := `{"model":"gpt-6-sol","reasoning_effort":"high","messages":[{"role":"user","content":"a"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`
	postOA("/v1/chat/completions", chatBody, "chat-local")
	if sent, _ := log.last(); string(sent) != chatBody {
		t.Fatalf("Chat Completions must NOT mutate top-level reasoning_effort (cache-safety): %s", sent)
	}
	var decoded map[string]any
	_ = json.Unmarshal(sent3, &decoded)
	if decoded["reasoning"].(map[string]any)["effort"] != "high" {
		t.Fatalf("top-level reasoning.effort must stay frozen at high")
	}
}
