package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// responsesServer is an OpenAI proxy whose stub provider answers with respond.
func responsesServer(t *testing.T, cloud CloudLink, respond func(body []byte) (int, string)) (*Server, *upstreamLog) {
	t.Helper()
	log := &upstreamLog{}
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		log.mu.Lock()
		log.bodies, log.headers = append(log.bodies, raw), append(log.headers, r.Header.Clone())
		log.mu.Unlock()
		mu.Unlock()
		status, answer := http.StatusOK, `{"id":"r","object":"response","model":"gpt-6-sol","output":[],"usage":{"input_tokens":1000,"output_tokens":5,"total_tokens":1005,"input_tokens_details":{"cached_tokens":800}}}`
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
	return New(Config{
		Adapters: []providers.Adapter{openai.New("https://api.openai.com")}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds: stubCreds{key: "sk-byok"}, Sink: &captureSink{}, HTTPClient: &http.Client{Transport: toStub(upstream.URL)}, Cloud: cloud,
	}), log
}

func postResponses(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("authorization", "Bearer sk-proj-api-key")
	req.Header.Set("x-cave-agent", "codex")
	req.Header.Set("session_id", "codex-1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// thread builds a Codex Responses body: top-level effort, then the input items.
func thread(effort string, items ...string) string {
	return `{"model":"gpt-6-sol","prompt_cache_key":"thread-1","reasoning":{"effort":"` + effort + `","summary":"auto"},"input":[` + strings.Join(items, ",") + `]}`
}

const (
	rA  = `{"type":"message","role":"user","content":[{"type":"input_text","text":"plan the migration"}]}`
	rB  = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Three steps."}]}`
	rC  = `{"type":"message","role":"user","content":[{"type":"input_text","text":"now do step one"}]}`
	rD  = `{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"}`
	rTR = `{"type":"function_call_output","call_id":"c1","output":"ok"}`
	rE  = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}`
	rF  = `{"type":"message","role":"user","content":[{"type":"input_text","text":"and step two"}]}`
)

func update(effort string) string {
	return `{"type":"configuration_update","reasoning":{"effort":"` + effort + `"}}`
}

// GPT-6 per-message effort: a configuration_update before the newest user
// input, the top-level effort left alone, marks replayed byte for byte, a later
// "top" answer one more mark, never two updates side by side, never at the end.
func TestResponsesConfigurationUpdateKeepsThePrefix(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := responsesServer(t, cloud, nil)

	postResponses(t, srv, thread("high", rA, rB, rC))
	sent1, header1 := log.last()
	if want := thread("high", rA, rB, update("low"), rC); string(sent1) != want {
		t.Fatalf("first update:\n got %s\nwant %s", sent1, want)
	}
	if header1.Get("anthropic-beta") != "" {
		t.Errorf("anthropic-beta on OpenAI: %q", header1.Get("anthropic-beta"))
	}
	// The tool loop resends history without the update: it comes back where it
	// was, and every earlier byte is unchanged.
	postResponses(t, srv, thread("high", rA, rB, rC, rD, rTR))
	sent2, _ := log.last()
	if want := thread("high", rA, rB, update("low"), rC, rD, rTR); string(sent2) != want {
		t.Fatalf("replay:\n got %s\nwant %s", sent2, want)
	}
	if !bytes.HasPrefix(sent2, sent1[:len(sent1)-2]) {
		t.Errorf("the prefix changed between two consecutive requests")
	}
	// Another effort mid-loop has no place: before the user input it would sit
	// next to this turn's update, and an update never ends the input.
	cloud.answer = RouteAnswer{Outcome: "kept", Effort: "medium", EffortMode: "message"}
	postResponses(t, srv, thread("high", rA, rB, rC, rD, rTR))
	if sent, _ := log.last(); string(sent) != string(sent2) {
		t.Fatalf("mid-loop:\n got %s\nwant %s", sent, sent2)
	}
	// The next turn: the update replays, and a "top" answer on a session with
	// marks becomes one more update before the new user input.
	cloud.answer = RouteAnswer{Outcome: "kept", Effort: "medium", EffortMode: "top"}
	postResponses(t, srv, thread("high", rA, rB, rC, rD, rTR, rE, rF))
	sent4, _ := log.last()
	if want := thread("high", rA, rB, update("low"), rC, rD, rTR, rE, update("medium"), rF); string(sent4) != want {
		t.Fatalf("top as a mark:\n got %s\nwant %s", sent4, want)
	}
	if got := effortInForce("/v1/responses", sent4); got != "medium" {
		t.Errorf("effort in force = %q", got)
	}
	postResponses(t, srv, thread("high", rA, rB, rC, rD, rTR, rE, rF, rD, rTR))
	if ask := cloud.asks[len(cloud.asks)-1]; ask.Last == nil || ask.Last.Effort != "medium" {
		t.Errorf("last = %+v, want the newest update's effort", ask.Last)
	}
}

// A fresh thread takes the routed effort top-level when the request sets one
// (nothing to cache yet); one that sets none gets an update before its first
// user input. An input string has no item to go before: top-level.
func TestResponsesFreshThread(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := responsesServer(t, cloud, nil)
	postResponses(t, srv, thread("high", rA))
	if sent, _ := log.last(); string(sent) != thread("low", rA) {
		t.Fatalf("fresh with an effort: %s", sent)
	}
	postResponses(t, srv, thread("high", rA, rB, rC))
	if sent, _ := log.last(); string(sent) != thread("low", rA, rB, rC) {
		t.Fatalf("the fixed top-level effort holds: %s", sent)
	}

	srv, log = responsesServer(t, &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}, nil)
	none := `{"model":"gpt-6-sol","input":[` + rA + `]}`
	postResponses(t, srv, none)
	// No prompt_cache_key of the agent's: the session's (hashed) goes in.
	if sent, _ := log.last(); string(sent) != `{"model":"gpt-6-sol","input":[`+update("low")+`,`+rA+`],"prompt_cache_key":"`+openai.SessionCacheKey("codex-1")+`"}` {
		t.Fatalf("fresh without an effort: %s", sent)
	}

	srv, log = responsesServer(t, &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}, nil)
	postResponses(t, srv, `{"model":"gpt-6-sol","input":"go"}`)
	if sent, _ := log.last(); string(sent) != `{"model":"gpt-6-sol","input":"go","prompt_cache_key":"`+openai.SessionCacheKey("codex-1")+`","reasoning":{"effort":"low"}}` {
		t.Fatalf("input string: %s", sent)
	}
}

// A refused update (a pro model): one retry at top-level effort only, then the
// session is latched (per_message_off) and the model takes Cloud's effort
// top-level from then on.
func TestResponsesRefusedUpdateHealsAndLatches(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	refusal := `{"error":{"message":"The 'configuration_update' item type is not supported with pro or tournament models.","type":"invalid_request_error","param":"input","code":null}}`
	srv, log := responsesServer(t, cloud, func(body []byte) (int, string) {
		if bytes.Contains(body, []byte(`"configuration_update"`)) {
			return http.StatusBadRequest, refusal
		}
		return 0, ""
	})
	if rec := postResponses(t, srv, thread("high", rA, rB, rC)); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(log.bodies) != 2 || string(log.bodies[1]) != thread("low", rA, rB, rC) {
		t.Fatalf("attempts %d, heal sent %s", len(log.bodies), log.bodies[len(log.bodies)-1])
	}
	postResponses(t, srv, thread("high", rA, rB, rC, rE, rF))
	if ask := cloud.asks[len(cloud.asks)-1]; !ask.PerMessageOff {
		t.Errorf("the next ask did not report the latch")
	}
	if sent, _ := log.last(); string(sent) != thread("low", rA, rB, rC, rE, rF) || len(log.bodies) != 3 {
		t.Errorf("a latched session got an update: %s", sent)
	}
}

// Without an effort from Cloud a session with updates keeps them (its history
// does not change) and goes back to the request's own effort with one more,
// where there is a place for it.
func TestResponsesUpdatesSurviveACloudFailure(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := responsesServer(t, cloud, nil)
	postResponses(t, srv, thread("high", rA, rB, rC))
	cloud.answer = RouteAnswer{Outcome: "degraded", Reason: "timeout"}
	postResponses(t, srv, thread("high", rA, rB, rC, rD, rTR, rE, rF))
	if sent, _ := log.last(); string(sent) != thread("high", rA, rB, update("low"), rC, rD, rTR, rE, update("high"), rF) {
		t.Fatalf("after a failure: %s", sent)
	}
}

// An effort that first differs mid tool loop never goes back before the user
// message the model already answered: that history is cached as sent.
func TestResponsesUpdateNeverRewritesAnsweredHistory(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "high", EffortMode: "message"}}
	srv, log := responsesServer(t, cloud, nil)
	postResponses(t, srv, thread("high", rA, rB, rC)) // same as in force: nothing goes in
	first, _ := log.last()
	cloud.answer = RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}
	postResponses(t, srv, thread("high", rA, rB, rC, rD, rTR))
	if sent, _ := log.last(); !bytes.HasPrefix(sent, first[:len(first)-2]) || bytes.Contains(sent, []byte("configuration_update")) {
		t.Fatalf("mid-loop update rewrote history: %s", sent)
	}
	// A request ending right where a mark goes never ends with it.
	postResponses(t, srv, thread("high", rA, rB, rC, rD, rTR, rE, rF))
	postResponses(t, srv, thread("high", rA, rB, rC, rD, rTR, rE))
	if sent, _ := log.last(); bytes.HasSuffix(sent, []byte(update("low")+`]}`)) {
		t.Fatalf("an update ended the input: %s", sent)
	}
}

// One session id on both wires: Anthropic's fixed effort and marks never
// reach OpenAI.
func TestEffortStateStaysOnItsWire(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "max", EffortMode: "message"}}
	s := &Server{}
	run := newRouteRun(http.Header{"X-Cave-Session": {"same"}}, "same", "/v1/messages", nil)
	s.applyEffort(run, "anthropic", "/v1/messages", "claude-opus-5-5", []byte(convo("high", uA)), cloud.answer)
	run = newRouteRun(http.Header{}, "same", "/v1/responses", nil)
	got := s.applyEffort(run, "openai", "/v1/responses", "gpt-6-sol", []byte(thread("high", rA, rB, rC)), RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"})
	if string(got) != thread("high", rA, rB, update("low"), rC) {
		t.Fatalf("the Anthropic state leaked into Responses: %s", got)
	}
}

// The heal of a refused update keeps the session's prompt_cache_key, and a
// 429 on a request the route stage only keyed is not sent again.
func TestCacheKeySurvivesTheHealAndA429IsNotDoubled(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := responsesServer(t, cloud, func(body []byte) (int, string) {
		if bytes.Contains(body, []byte(`"configuration_update"`)) {
			return http.StatusBadRequest, `{"error":{"message":"The 'configuration_update' item type is not supported with pro or tournament models."}}`
		}
		return 0, ""
	})
	postResponses(t, srv, `{"model":"gpt-6-sol","reasoning":{"effort":"high"},"input":[`+rA+`,`+rB+`,`+rC+`]}`)
	if len(log.bodies) != 2 || !bytes.Contains(log.bodies[1], []byte(`"prompt_cache_key":"`+openai.SessionCacheKey("codex-1")+`"`)) {
		t.Fatalf("heal: %d attempts, %s", len(log.bodies), log.bodies[len(log.bodies)-1])
	}

	srv, log = responsesServer(t, &fakeCloud{answer: RouteAnswer{Outcome: "kept"}}, func([]byte) (int, string) {
		return http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`
	})
	if rec := postResponses(t, srv, `{"model":"gpt-6-sol","input":"go"}`); rec.Code != http.StatusTooManyRequests || len(log.bodies) != 1 {
		t.Fatalf("429: status %d after %d attempts", rec.Code, len(log.bodies))
	}
}
