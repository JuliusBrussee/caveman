package cloudlink

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

func TestCacheTTLIsReadFromTheBody(t *testing.T) {
	for _, c := range []struct{ endpoint, body, want string }{
		{"messages", `{"messages":[]}`, ""},
		{"messages", `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],"messages":[]}`, "5m"},
		// Claude Code writes 1h entries: the longest wins.
		{"messages", `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]}]}`, "1h"},
		{"messages", `{"cache_control":{"type":"ephemeral","ttl":"1h"},"messages":[]}`, "1h"},
		{"messages", `{"cache_control":{"type":"ephemeral","ttl":"2h"},"messages":[]}`, ""}, // a TTL the contract does not know
		{"messages", `{"tools":[{"name":"t","input_schema":{"properties":{"cache_control":{"type":"object"}}}}],"messages":[]}`, ""},
		{"messages", `{"messages":[{"role":"user","content":"\"cache_control\":{\"ttl\":\"1h\"}"}]}`, ""},
		{"responses", `{"prompt_cache_options":{"ttl":"30m"},"input":[]}`, "30m"},
		{"responses", `{"prompt_cache_retention":"24h","input":[]}`, "24h"},
		{"chat", `{"prompt_cache_retention":"in_memory","messages":[]}`, "5m"},
		{"responses", `{"input":[]}`, ""},
	} {
		body := []byte(c.body)
		root, _ := jsonsplice.Root(body)
		if got := cacheTTL(c.endpoint, body, root); got != c.want {
			t.Errorf("%s %s: cache_ttl %q, want %q", c.endpoint, c.body, got, c.want)
		}
	}
	// It rides on the ask's request.
	fake := &poolCloud{answer: func(map[string]any) (int, string) { return 200, `{"model":"claude-opus-5-5"}` }}
	cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer cloud.Close()
	ask := messagesAsk("claude-opus-5-5")
	ask.Body = []byte(`{"model":"claude-opus-5-5","system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"` + promptText + `"}]}`)
	newLink(signedIn(t, cloud.URL)).Ask(t.Context(), ask)()
	if request, _ := fake.bodies[0]["request"].(map[string]any); request["cache_ttl"] != "1h" {
		t.Fatalf("request = %v", fake.bodies[0]["request"])
	}
}

func ttlAsk() gateway.RouteAsk {
	ask := messagesAsk("claude-opus-5-5")
	ask.Body = []byte(`{"model":"claude-opus-5-5","system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"` + promptText + `"}]}`)
	return ask
}

func carries(body map[string]any) (ttl, pool bool) {
	request, _ := body["request"].(map[string]any)
	_, ttl = request["cache_ttl"]
	_, pool = body["pool"]
	return ttl, pool
}

// Cloud's real refusal of an unknown field names none ("Router request
// invalid."): the newest field goes first, then the next; the one whose
// removal got the 200 stays out for the login, so a Cloud that knows neither
// converges on the next ask.
func TestOlderCloudsGenericRefusalDropsTheNewestFieldsFirst(t *testing.T) {
	const refusal = `{"error":{"type":"cave_gateway_error","code":"cave_router_request_invalid","message":"Router request invalid.","request_id":"r"}}`
	for _, c := range []struct {
		name      string
		knowsPool bool
		wantAsks  int  // posts for the first ask
		wantNext  int  // posts for the second
		wantPool  bool // asks after that carry pool
	}{
		{"knows pool, not cache_ttl", true, 2, 1, true},
		{"knows neither", false, 3, 2, false},
	} {
		fake := &poolCloud{answer: func(body map[string]any) (int, string) {
			ttl, pool := carries(body)
			if ttl || pool && !c.knowsPool {
				return 400, refusal
			}
			return 200, `{"model":"claude-sonnet-5-5"}`
		}}
		cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
		home := signedIn(t, cloud.URL)
		addLogin(t, home, "openai", "sk-openai")
		link := newLink(home)
		if answer := link.Ask(t.Context(), ttlAsk())(); answer.Model != "claude-sonnet-5-5" {
			t.Fatalf("%s: answer = %+v", c.name, answer)
		}
		if len(fake.bodies) != c.wantAsks {
			t.Fatalf("%s: asked %d times, want %d", c.name, len(fake.bodies), c.wantAsks)
		}
		for i, want := range []int{c.wantNext, 1} {
			before := len(fake.bodies)
			next := ttlAsk()
			next.SessionID = fmt.Sprintf("s%d", i+2)
			link.Ask(t.Context(), next)()
			ttl, pool := carries(fake.bodies[len(fake.bodies)-1])
			if len(fake.bodies)-before != want || ttl || pool != c.wantPool {
				t.Errorf("%s: ask %d took %d posts, carries cache_ttl %v, pool %v", c.name, i+2, len(fake.bodies)-before, ttl, pool)
			}
		}
		cloud.Close()
	}
}

// A refusal that dropping the fields does not cure is a bad ask: nothing is
// latched off, and the next ask carries them again.
func TestGenericRefusalOfABadAskLatchesNothing(t *testing.T) {
	fake := &poolCloud{answer: func(map[string]any) (int, string) {
		return 400, `{"error":{"code":"cave_router_request_invalid","message":"Router request invalid."}}`
	}}
	cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer cloud.Close()
	home := signedIn(t, cloud.URL)
	addLogin(t, home, "openai", "sk-openai")
	link := newLink(home)
	if answer := link.Ask(t.Context(), ttlAsk())(); answer.Reason != "cloud_400" {
		t.Fatalf("answer = %+v", answer)
	}
	if len(fake.bodies) != 3 {
		t.Fatalf("asked %d times, want once per field and once without", len(fake.bodies))
	}
	link.mu.Lock()
	link.pauseUntil = link.now() // the 400 paused new asks; look past it
	link.mu.Unlock()
	next := ttlAsk()
	next.SessionID = "s2"
	link.Ask(t.Context(), next)()
	if ttl, pool := carries(fake.bodies[3]); !ttl || !pool {
		t.Errorf("next ask carries cache_ttl %v, pool %v", ttl, pool)
	}
}

// A field Cloud has answered 200 for is never blamed for a later generic
// 400: a bad ask is not sent again without it.
func TestAcceptedFieldsAreNeverDropped(t *testing.T) {
	var bad atomic.Bool
	fake := &poolCloud{answer: func(map[string]any) (int, string) {
		if bad.Load() {
			return 400, `{"error":{"code":"cave_router_request_invalid","message":"Router request invalid."}}`
		}
		return 200, `{"model":"claude-sonnet-5-5"}`
	}}
	cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer cloud.Close()
	home := signedIn(t, cloud.URL)
	addLogin(t, home, "openai", "sk-openai")
	link := newLink(home)
	link.Ask(t.Context(), ttlAsk())()
	bad.Store(true)
	next := ttlAsk()
	next.SessionID = "s2"
	link.Ask(t.Context(), next)()
	if len(fake.bodies) != 2 {
		t.Fatalf("asked %d times, want once each", len(fake.bodies))
	}
}
