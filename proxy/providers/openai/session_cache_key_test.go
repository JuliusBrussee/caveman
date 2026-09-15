package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// OpenAI 5.6+ removed the fallback to the longest unmarked prefix, so
// prompt_cache_key became load-bearing routing metadata. The planner sets one per
// session — hashed, never the raw caller-supplied id — and never over an existing
// key.

const sessionKeyBody = `{"model":"gpt-5.6","instructions":"You are a coding agent.","input":"hello"}`

func planSessionKey(t *testing.T, body, session string, payg bool) ([]byte, bool) {
	t.Helper()
	adapter := New("http://upstream").(Adapter)
	return adapter.PlanCacheBreakpoints([]byte(body), providers.RequestMetadata{
		Provider:  "openai",
		SessionID: session,
	}, payg)
}

func TestSessionCacheKeySetsAStableHashedKey(t *testing.T) {
	out, ok := planSessionKey(t, sessionKeyBody, "sess-abc.123", true)
	if !ok {
		t.Fatal("planner declined a payg request with a session and no key")
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	key, isString := root["prompt_cache_key"].(string)
	if !isString || key == "" {
		t.Fatalf("prompt_cache_key missing: %s", out)
	}
	if len(key) != 16 {
		t.Fatalf("key = %q (%d chars), want a 16-char hash prefix", key, len(key))
	}
	// The raw session id is caller-supplied and may carry user content. It must
	// never reach the provider, in whole or as a substring of the key.
	if strings.Contains(string(out), "sess-abc.123") {
		t.Fatalf("the raw session id was forwarded upstream: %s", out)
	}
	if key != SessionCacheKey("sess-abc.123") {
		t.Fatalf("key %q is not the documented derivation", key)
	}

	// Stability: the same session produces the same key on every later call, which
	// is the entire point — a key that moved would scatter the session's requests
	// across cache-holding machines.
	again, ok := planSessionKey(t, sessionKeyBody, "sess-abc.123", true)
	if !ok || string(again) != string(out) {
		t.Fatalf("key is not stable across calls:\n first %s\nsecond %s", out, again)
	}
	// Distinct sessions get distinct keys.
	if SessionCacheKey("sess-abc.123") == SessionCacheKey("sess-other") {
		t.Fatal("two sessions collided on one key")
	}
}

func TestSessionCacheKeyPreconditions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		session string
		payg    bool
	}{
		{name: "no session header", body: sessionKeyBody, session: "", payg: true},
		{name: "not payg", body: sessionKeyBody, session: "sess-abc", payg: false},
		{
			name:    "caller already set a key",
			body:    `{"model":"gpt-5.6","input":"hi","prompt_cache_key":"caller-owned"}`,
			session: "sess-abc",
			payg:    true,
		},
		{name: "not json", body: `{"model":`, session: "sess-abc", payg: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out, ok := planSessionKey(t, tc.body, tc.session, tc.payg); ok {
				t.Fatalf("planner rewrote a body it should have left alone: %s", out)
			}
		})
	}
}

// TestSessionCacheKeyNeverOverwritesThePrefixOptimizer: cache_key.go's
// prefix-signature optimizer may already have set the field on the same request.
// The session planner runs after it and must leave that key alone.
func TestSessionCacheKeyNeverOverwritesThePrefixOptimizer(t *testing.T) {
	adapter := New("http://upstream").(Adapter)
	transformed, err := adapter.ApplyProviderNativeTransforms(nil, strings.NewReader(sessionKeyBody),
		providers.RequestMetadata{Provider: "openai"},
		providers.TransformPolicy{RuntimeMode: "active", Optimizers: map[string]bool{OptimizerID: true}})
	if err != nil {
		t.Fatalf("prefix optimizer error: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(transformed.Body, &root); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	existing, ok := root["prompt_cache_key"].(string)
	if !ok || existing == "" {
		t.Fatalf("fixture did not exercise the prefix optimizer: %s", transformed.Body)
	}

	if out, ok := planSessionKey(t, string(transformed.Body), "sess-abc", true); ok {
		t.Fatalf("session planner overwrote an existing prompt_cache_key: %s", out)
	}
}
