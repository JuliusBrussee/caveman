package gateway

import (
	"bytes"
	"crypto/sha256"
	"net/http"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/gemini"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// Subscription live-zone compression: the local wrap MAY compress
// subscription/OAuth-authenticated coding-agent traffic with NO Caveman account,
// but only through the SAME live-zone path PAYG uses, only when the
// three technical conditions hold, and only ever as a tokens-only measurement —
// a subscription row must never carry a dollar.

const subMessageRespBody = `{"id":"msg","type":"message","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1000,"output_tokens":10}}`

// subscriptionAgentHeaders is a Claude Code subscription session: a subscription
// user-agent plus an OAuth session token.
var subscriptionAgentHeaders = map[string]string{
	"user-agent":        "claude-cli/1.0.0",
	"authorization":     "Bearer sk-ant-oat-test",
	"anthropic-beta":    "oauth-2025-04-20",
	"anthropic-version": "2023-06-01",
}

func newSubscriptionCompressServer(comp Compressor, rt *captureTransport, cfg Config) (*Server, *captureSink) {
	sink := &captureSink{}
	// Production always has the durable replacement cache (the local SQLite spend
	// store); tests that pin its ABSENCE set their own.
	if cfg.PrefixCache == nil {
		cfg.PrefixCache = newTestPrefixCache()
	}
	cfg.Adapters = []providers.Adapter{anthropic.New("https://upstream.test")}
	cfg.Auth = stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}}
	cfg.Creds = passthroughTestCreds{}
	cfg.Sink = sink
	cfg.Compressor = comp
	cfg.HTTPClient = &http.Client{Transport: rt}
	return New(cfg), sink
}

func subBlock(text string) string {
	return `{"type":"text","text":"` + text + `"}`
}

func subCachedBlock(text string) string {
	return `{"type":"text","text":"` + text + `","cache_control":{"type":"ephemeral"}}`
}

// claudeCodeConversation is a multi-turn Claude Code shaped request: a stable
// system field, static tools, cache_control breakpoints planted mid-conversation,
// and a just-arrived trailing user turn. liveText is the only content the live
// zone may touch.
func claudeCodeConversation(liveText string) string {
	turn1 := strings.Repeat("turn one project context ", 30)
	turn2 := strings.Repeat("turn two file contents ", 30)
	return `{"model":"claude-sonnet-4-6","max_tokens":1024,` +
		`"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],` +
		`"tools":[{"name":"Read","description":"Read a file","input_schema":{"type":"object"}}],` +
		`"messages":[` +
		`{"role":"user","content":[` + subCachedBlock(turn1) + `]},` +
		`{"role":"assistant","content":[` + subBlock("assistant one") + `]},` +
		`{"role":"user","content":[` + subCachedBlock(turn2) + `]},` +
		`{"role":"assistant","content":[` + subBlock("assistant two") + `]},` +
		`{"role":"user","content":[` + subBlock(liveText) + `]}` +
		`]}`
}

// TestSubscriptionLiveZoneNeedsNoAccount pins the invariant: local compression is NOT
// account-gated. A bare server with no entitlement signal of any kind compresses
// the live zone exactly like a signed-in one, because the only remaining policy
// input is the operator off-switch.
func TestSubscriptionLiveZoneNeedsNoAccount(t *testing.T) {
	live := strings.Repeat("newest live turn bytes ", 40)
	body := claudeCodeConversation(live)
	for _, cfg := range []Config{
		{RecoveryViaMCP: true}, // signed out, no config at all
		{SubscriptionCompress: "live_zone", RecoveryViaMCP: true}, // explicit operator opt-in
	} {
		rt := &captureTransport{responses: []string{subMessageRespBody}}
		comp := &liveZoneCompressor{}
		srv, sink := newSubscriptionCompressServer(comp, rt, cfg)

		serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

		upstream := string(rt.bodies[0])
		if strings.Contains(upstream, live) {
			t.Fatalf("cfg %+v: an account-less wrap must still compress the live zone: %s", cfg, upstream)
		}
		if !strings.Contains(upstream, "<<ccr:") {
			t.Fatalf("cfg %+v: compression must disclose an in-block CCR marker: %s", cfg, upstream)
		}
		if comp.calls == 0 {
			t.Fatalf("cfg %+v: compressor was never called", cfg)
		}
		if row := sink.last(t); row.CompressionTokensBefore <= 0 {
			t.Fatalf("cfg %+v: row must record the token reduction: %+v", cfg, row)
		}
	}
}

// TestSubscriptionLiveZoneUnknownOffSwitchFailsClosed keeps the operator opt-out
// fail-closed: an unrecognized subscription_compress value means passthrough, not
// "probably on".
func TestSubscriptionLiveZoneUnknownOffSwitchFailsClosed(t *testing.T) {
	body := claudeCodeConversation(strings.Repeat("newest live turn bytes ", 40))
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	comp := &liveZoneCompressor{}
	srv, sink := newSubscriptionCompressServer(comp, rt, Config{SubscriptionCompress: "not_a_mode", RecoveryViaMCP: true})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("an unknown off-switch value must be byte-identical passthrough:\n got %s\nwant %s", rt.bodies[0], body)
	}
	if comp.calls != 0 || len(comp.stored) != 0 {
		t.Fatalf("an unknown off-switch value must not compress or store: compress=%d stored=%d", comp.calls, len(comp.stored))
	}
	if row := sink.last(t); row.CompressionTokensBefore != 0 || row.RecoveryHandle != "" {
		t.Fatalf("passthrough row must claim no compression: %+v", row)
	}
}

// TestSubscriptionWithoutMCPRecoveryPassesThrough pins recoverability.
// Subscription compression is marker-only — the proxy never injects its
// server-side retrieve tool into these requests — so without the agent's own
// caveman_retrieve MCP tool the elided detail would be unreachable by anyone. A
// wrap that could not install MCP recovery therefore compresses nothing.
func TestSubscriptionWithoutMCPRecoveryPassesThrough(t *testing.T) {
	body := claudeCodeConversation(strings.Repeat("newest live turn bytes ", 40))
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	comp := &liveZoneCompressor{}
	srv, sink := newSubscriptionCompressServer(comp, rt, Config{})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("no MCP recovery must be byte-identical passthrough:\n got %s\nwant %s", rt.bodies[0], body)
	}
	if strings.Contains(string(rt.bodies[0]), "<<ccr:") {
		t.Fatalf("an unrecoverable marker must never be emitted: %s", rt.bodies[0])
	}
	if comp.calls != 0 || len(comp.stored) != 0 {
		t.Fatalf("no MCP recovery must not compress or store: compress=%d stored=%d", comp.calls, len(comp.stored))
	}
	if row := sink.last(t); row.CompressionTokensBefore != 0 || row.RecoveryHandle != "" {
		t.Fatalf("row must claim no compression: %+v", row)
	}
}

// TestSubscriptionWithoutPrefixCachePassesThrough pins the maintainability
// half of the same rule: a rewrite the proxy cannot re-issue on the next turn would
// flip the upstream prefix back to the client's originals and bust the cache entry
// this turn just paid to create. With no durable replacement cache, nothing is
// compressed at all.
func TestSubscriptionWithoutPrefixCachePassesThrough(t *testing.T) {
	body := claudeCodeConversation(strings.Repeat("newest live turn bytes ", 40))
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	comp := &liveZoneCompressor{}
	sink := &captureSink{}
	srv := New(Config{
		Adapters:       []providers.Adapter{anthropic.New("https://upstream.test")},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          passthroughTestCreds{},
		Sink:           sink,
		Compressor:     comp,
		HTTPClient:     &http.Client{Transport: rt},
		RecoveryViaMCP: true,
	})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("no prefix cache must be byte-identical passthrough:\n got %s\nwant %s", rt.bodies[0], body)
	}
	if comp.calls != 0 {
		t.Fatalf("no prefix cache must not compress (calls=%d)", comp.calls)
	}
}

// TestSubscriptionCompressesLiveZoneByDefault pins the product decision:
// subscription compression is on by default — no account and no extra config
// value required.
func TestSubscriptionCompressesLiveZoneByDefault(t *testing.T) {
	live := strings.Repeat("newest live turn bytes ", 40)
	body := claudeCodeConversation(live)
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	comp := &liveZoneCompressor{}
	srv, _ := newSubscriptionCompressServer(comp, rt, Config{RecoveryViaMCP: true})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	upstream := string(rt.bodies[0])
	if strings.Contains(upstream, live) {
		t.Fatalf("subscription live zone should have been compressed: %s", upstream)
	}
	if !strings.Contains(upstream, "<<ccr:") {
		t.Fatalf("subscription compression must disclose an in-block CCR marker: %s", upstream)
	}
	// Marker-only recovery: the proxy must never inject its server-side retrieve
	// tool into a subscription request (that would add a model-visible tool).
	if strings.Contains(upstream, retrieveToolName) {
		t.Fatalf("subscription must not inject the server-side retrieve tool: %s", upstream)
	}
}

// OAuth compression is an adapter capability, not an Anthropic special case.
// OpenAI Responses and Gemini both expose schema-aware frozen/live zones, so an
// local wrap with MCP recovery can compress their latest user/tool data
// while preserving every prior block byte-for-byte.
func TestOAuthLiveZoneCompressesAcrossPrefixStableAdapters(t *testing.T) {
	cases := []struct {
		name    string
		adapter providers.Adapter
		path    string
		body    string
		frozen  string
		live    string
	}{
		{
			name:    "openai responses",
			adapter: openai.New("https://upstream.test"),
			path:    "/v1/responses",
			frozen:  strings.Repeat("oauth codex frozen prior turn ", 30),
			live:    strings.Repeat("oauth codex live tool output ", 40),
		},
		{
			name:    "gemini generate content",
			adapter: gemini.New("https://upstream.test"),
			path:    "/v1beta/models/gemini-3-pro:generateContent",
			frozen:  strings.Repeat("oauth gemini frozen prior turn ", 30),
			live:    strings.Repeat("oauth gemini live tool output ", 40),
		},
	}
	for i := range cases {
		tc := &cases[i]
		if tc.name == "openai responses" {
			tc.body = `{"model":"gpt-5.5","input":[` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + tc.frozen + `"}]},` +
				`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + tc.live + `"}]}]}`
		} else {
			tc.body = `{"contents":[` +
				`{"role":"user","parts":[{"text":"` + tc.frozen + `"}]},` +
				`{"role":"model","parts":[{"text":"done"}]},` +
				`{"role":"user","parts":[{"text":"` + tc.live + `"}]}]}`
		}
		t.Run(tc.name, func(t *testing.T) {
			rt := &captureTransport{}
			comp := &liveZoneCompressor{}
			srv := newSocketFreeCompressServerWithConfig(tc.adapter, comp, rt, passthroughTestCreds{}, Config{
				RecoveryViaMCP: true,
				PrefixCache:    newTestPrefixCache(),
			})

			headers := map[string]string{"authorization": "Bearer opaque-oauth-token"}
			if tc.name == "gemini generate content" {
				// Gemini user OAuth requires caller-owned quota attribution. Never
				// guess it in production or omit it from an OAuth conformance test.
				headers["x-goog-user-project"] = "caveman-test-project"
			}
			serveBody(t, srv, tc.path, tc.body, headers)

			if len(rt.bodies) != 1 {
				t.Fatalf("upstream calls = %d, want 1", len(rt.bodies))
			}
			upstream := string(rt.bodies[0])
			if strings.Contains(upstream, tc.live) || !strings.Contains(upstream, "<<ccr:") {
				t.Fatalf("OAuth live zone was not compressed: %s", upstream)
			}
			if !strings.Contains(upstream, tc.frozen) {
				t.Fatalf("OAuth frozen prefix changed on first sight: %s", upstream)
			}
			if got := rt.headers[0].Get("authorization"); got != "Bearer opaque-oauth-token" {
				t.Fatalf("OAuth authorization changed: %q", got)
			}
			if tc.name == "gemini generate content" && rt.headers[0].Get("x-goog-user-project") != "caveman-test-project" {
				t.Fatalf("Gemini OAuth quota project was not preserved: %q", rt.headers[0].Get("x-goog-user-project"))
			}
		})
	}
}

// TestSubscriptionOffSwitchPassesThrough pins the off-switch: the operator can
// still turn the capability off through the existing subscription_compress
// config key. It is the ONLY policy input left.
func TestSubscriptionOffSwitchPassesThrough(t *testing.T) {
	body := claudeCodeConversation(strings.Repeat("newest live turn bytes ", 40))
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	comp := &liveZoneCompressor{}
	srv, _ := newSubscriptionCompressServer(comp, rt, Config{RecoveryViaMCP: true, SubscriptionCompress: "off"})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("subscription_compress: off must be byte-identical passthrough:\n got %s", rt.bodies[0])
	}
	if comp.calls != 0 {
		t.Fatalf("subscription_compress: off must not compress (calls=%d)", comp.calls)
	}
}

// TestSubscriptionLiveZoneKeepsCacheBreakpointPrefixByteEqual is the cache-safety
// invariant: every byte at or before the last mid-conversation cache_control
// breakpoint — plus the system field and the tools array Claude Code caches
// against — is forwarded byte-identical. Only the just-arrived trailing turn,
// which no provider cache can hold yet, is reshaped.
func TestSubscriptionLiveZoneKeepsCacheBreakpointPrefixByteEqual(t *testing.T) {
	live := strings.Repeat("newest live turn bytes ", 40)
	body := claudeCodeConversation(live)
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	srv, _ := newSubscriptionCompressServer(&liveZoneCompressor{}, rt, Config{RecoveryViaMCP: true})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)
	upstream := string(rt.bodies[0])

	// Everything the client sent before the live content must be byte-equal, not
	// merely present: that span carries system, tools, both cache_control
	// breakpoints and every frozen turn.
	assertFrozenPrefixByteEqual(t, body, upstream, live)

	cut := strings.Index(body, live)
	prefix := body[:cut]
	if strings.Count(prefix, `"cache_control"`) != 3 {
		t.Fatalf("test fixture lost its breakpoints; prefix = %s", prefix)
	}
	if strings.Count(upstream, `"cache_control"`) != 3 {
		t.Fatalf("cache_control breakpoints must survive verbatim: %s", upstream)
	}
	if !strings.Contains(upstream, `"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}]`) {
		t.Fatalf("system field must not be touched: %s", upstream)
	}
	if !strings.Contains(upstream, `"tools":[{"name":"Read","description":"Read a file","input_schema":{"type":"object"}}]`) {
		t.Fatalf("tools must not be touched: %s", upstream)
	}
	if !strings.Contains(upstream, `{"role":"assistant","content":[{"type":"text","text":"assistant two"}]}`) {
		t.Fatalf("assistant turns must not be touched: %s", upstream)
	}
}

// TestSubscriptionLiveZoneClampCompressesNewestMarkedTurn documents the one case
// where the live zone reaches a message that itself carries cache_control: agents
// like Claude Code mark the just-arrived turn, and ComputeFrozenCount deliberately
// clamps the floor to len(messages)-1 so the live zone is not permanently empty.
// The clamped turn cannot be in any provider cache yet, so compressing it busts
// nothing at send time — but everything before it, including the previous
// breakpoint, still has to be byte-identical. This test pins that boundary.
func TestSubscriptionLiveZoneClampCompressesNewestMarkedTurn(t *testing.T) {
	live := strings.Repeat("newest marked turn bytes ", 40)
	prior := strings.Repeat("prior cached turn bytes ", 30)
	body := `{"model":"claude-sonnet-4-6","max_tokens":1024,` +
		`"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[` +
		`{"role":"user","content":[` + subCachedBlock(prior) + `]},` +
		`{"role":"assistant","content":[` + subBlock("assistant one") + `]},` +
		`{"role":"user","content":[` + subCachedBlock(live) + `]}` +
		`]}`
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	srv, _ := newSubscriptionCompressServer(&liveZoneCompressor{}, rt, Config{RecoveryViaMCP: true})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)
	upstream := string(rt.bodies[0])

	assertFrozenPrefixByteEqual(t, body, upstream, live)
	if !strings.Contains(upstream, prior) {
		t.Fatalf("the previous breakpoint's turn must stay original: %s", upstream)
	}
	if strings.Contains(upstream, live) || !strings.Contains(upstream, "<<ccr:") {
		t.Fatalf("the clamped trailing turn should be compressed: %s", upstream)
	}
	// The breakpoint itself is a sibling key of "text"; only the string value is
	// spliced, so the marker survives and the request still declares the same
	// number of cache breakpoints it arrived with.
	if strings.Count(upstream, `"cache_control"`) != 3 {
		t.Fatalf("cache_control markers must survive the splice: %s", upstream)
	}
}

// TestSubscriptionLiveZoneDeterministicDoubleRun pins determinism: the same client
// bytes must always produce the same upstream bytes, so a replayed prefix never
// diverges from what the agent previously sent.
func TestSubscriptionLiveZoneDeterministicDoubleRun(t *testing.T) {
	body := claudeCodeConversation(strings.Repeat("newest live turn bytes ", 40))
	rt := &captureTransport{responses: []string{subMessageRespBody, subMessageRespBody}}
	srv, _ := newSubscriptionCompressServer(&liveZoneCompressor{}, rt, Config{RecoveryViaMCP: true})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if len(rt.bodies) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(rt.bodies))
	}
	if !bytes.Equal(rt.bodies[0], rt.bodies[1]) {
		t.Fatalf("subscription compression must be deterministic:\n%s\n%s", rt.bodies[0], rt.bodies[1])
	}
}

// TestSubscriptionLiveZoneUncompressibleBodyPassesThrough pins the fail-open rule:
// a request whose live zone cannot be extracted (here: no user message to work on)
// is forwarded byte-identical, even when entitled.
func TestSubscriptionLiveZoneUncompressibleBodyPassesThrough(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"assistant","content":[` +
		subBlock(strings.Repeat("assistant only content ", 40)) + `]}]}`
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	comp := &liveZoneCompressor{}
	srv, _ := newSubscriptionCompressServer(comp, rt, Config{RecoveryViaMCP: true})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("unextractable body must pass through unchanged:\n got %s\nwant %s", rt.bodies[0], body)
	}
	if comp.calls != 0 {
		t.Fatalf("unextractable body must not reach the compressor (calls=%d)", comp.calls)
	}
}

// TestSubscriptionLiveZoneRecordsTokensNeverDollars is the no-fake-savings pin:
// a compressed subscription row carries the local o200k token estimate with its
// basis label and NOTHING in dollars — no spend, no savings, no would-have-saved
// figure. Subscription traffic has no list price, so a dollar here would be a lie.
func TestSubscriptionLiveZoneRecordsTokensNeverDollars(t *testing.T) {
	body := claudeCodeConversation(strings.Repeat("newest live turn bytes ", 40))
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	srv, sink := newSubscriptionCompressServer(&liveZoneCompressor{}, rt, Config{RecoveryViaMCP: true})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	row := sink.last(t)
	if row.AuthMode != "subscription" {
		t.Fatalf("auth_mode = %q, want subscription", row.AuthMode)
	}
	if row.CompressionTokensBefore <= 0 || row.CompressionTokensAfter >= row.CompressionTokensBefore {
		t.Fatalf("subscription row must record a token reduction: before=%d after=%d", row.CompressionTokensBefore, row.CompressionTokensAfter)
	}
	if row.CompressionTokenCountBasis != "estimated_engine_o200k" {
		t.Fatalf("compression_token_count_basis = %q, want estimated_engine_o200k", row.CompressionTokenCountBasis)
	}
	if row.RecoveryHandle == "" {
		t.Fatal("subscription row must disclose its CCR recovery handle")
	}
	if row.Basis != "inferred" {
		t.Fatalf("basis = %q, want inferred", row.Basis)
	}
	if row.TotalCostUSD != 0 {
		t.Fatalf("total_cost_usd = %v, want 0 — subscription traffic has no list price", row.TotalCostUSD)
	}
	if row.SavingsUSD != 0 {
		t.Fatalf("savings_usd = %v, want 0 — subscription savings are tokens-only", row.SavingsUSD)
	}
	if row.WouldSaveUSD != nil {
		t.Fatalf("would_save_usd = %v, want nil for subscription", *row.WouldSaveUSD)
	}
}
