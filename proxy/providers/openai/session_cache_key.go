package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// SessionCacheKeyID labels the session-scoped prompt_cache_key on
// x-cave-optimization and on the telemetry row.
//
// It mints nothing. OpenAI prompt caching is automatic; prompt_cache_key is
// routing affinity, so the field alone cannot prove an incremental cache hit —
// which is exactly why the gateway's cacheOptimizerIDs set has never contained an
// OpenAI key, and why this id is not added to it either.
const SessionCacheKeyID = "openai-session-prompt-cache-key"

// sessionCacheKeyDomain separates this hash from any other sha256 of the same
// input, so the key can never be confused with (or reversed into) an identifier
// used elsewhere.
const sessionCacheKeyDomain = "caveman.session-prompt-cache-key.v1:"

// PlanCacheBreakpoints sets a per-session prompt_cache_key.
//
// OpenAI 5.6+ dropped the fallback to the longest unmarked prefix, so cache
// routing is load-bearing: requests that share a prefix AND a key are routed to
// the machine that already holds that prefix. A session is the right key scope —
// one agent run is one conversation growing by append — and it is stable for the
// life of that run.
//
// The key is sha256(domain + session id), hex, truncated to 16 characters. The
// raw session id never reaches the provider: x-cave-session is caller-supplied
// and may carry user content, so it is hashed rather than forwarded.
//
// Precedence, stated explicitly because two optimizers write the same field: the
// PREFIX-SIGNATURE optimizer in cache_key.go WINS. It runs earlier, inside
// ApplyProviderNativeTransforms, so when an operator has both enabled this
// function finds a key already present and declines. That is the right way round
// — a key derived from the actual cacheable prefix is at least as good a routing
// scope as the session, and silently replacing an optimizer's output from a later
// pipeline stage would make the upstream bytes depend on stage ordering rather
// than on config. The same rule covers a key the CALLER set: never overwritten.
// Any parse problem leaves the body unchanged.
func (a Adapter) PlanCacheBreakpoints(body []byte, meta providers.RequestMetadata, payg bool) ([]byte, bool) {
	if !payg || meta.SessionID == "" {
		return nil, false
	}
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return nil, false
	}
	if _, exists := root["prompt_cache_key"]; exists {
		return nil, false
	}
	object, ok := jsonsplice.Root(body)
	if !ok {
		return nil, false
	}
	key, err := json.Marshal(SessionCacheKey(meta.SessionID))
	if err != nil {
		return nil, false
	}
	out, err := jsonsplice.AppendObjectFields(body, object,
		jsonsplice.FieldInsertion{Name: "prompt_cache_key", Value: key},
	)
	if err != nil || !json.Valid(out) {
		return nil, false
	}
	return out, true
}

// SessionCacheKey is the stable, content-blind key derived from a session id.
func SessionCacheKey(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionCacheKeyDomain + sessionID))
	return hex.EncodeToString(sum[:])[:16]
}
