package gateway

import (
	"crypto/sha256"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// The cache-breakpoint planner adds provider-native cache metadata to the
// UPSTREAM request only — cache_control and prompt_cache_key are never shown to
// the model — so it is byte-safe under honesty rule #2. It still ships default
// OFF behind breakpoint_plan / CAVEMAN_BREAKPOINT_PLAN, because default-on is a
// claim the escalation ladder has not earned yet.

// planPAYGHeaders is a custom agent on its own Anthropic key: payg, and it
// identifies its session so the ledger can account for it.
var planPAYGHeaders = map[string]string{
	"x-api-key":         "sk-ant-api-test",
	"anthropic-version": "2023-06-01",
	"x-cave-session":    "sess-plan-1",
}

// planRespBody carries provider cache counters so the session ledger has real
// numbers to account.
func planRespBody(cacheCreation int) string {
	return `{"id":"msg","type":"message","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],` +
		`"usage":{"input_tokens":1000,"output_tokens":10,"cache_read_input_tokens":0,` +
		`"cache_creation_input_tokens":` + strconv.Itoa(cacheCreation) + `}}`
}

func newBreakpointPlanServer(t *testing.T, adapter providers.Adapter, rt *captureTransport, cfg Config) (*Server, *captureSink) {
	t.Helper()
	sink := &captureSink{}
	if cfg.Auth == nil {
		cfg.Auth = stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "active"}}
	}
	cfg.Adapters = []providers.Adapter{adapter}
	cfg.Creds = passthroughTestCreds{}
	cfg.Sink = sink
	cfg.HTTPClient = &http.Client{Transport: rt}
	return New(cfg), sink
}

// planBody is a cold-start agent request: no cache_control anywhere.
const planBody = `{"model":"claude-sonnet-4-6","max_tokens":1024,` +
	`"tools":[{"name":"read","input_schema":{}}],` +
	`"system":"You are an agent.",` +
	`"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`

// TestBreakpointPlanIsOffByDefault is the default-off pin: an unconfigured wrap
// forwards the request byte-identically and claims nothing.
func TestBreakpointPlanIsOffByDefault(t *testing.T) {
	rt := &captureTransport{responses: []string{planRespBody(0)}}
	srv, sink := newBreakpointPlanServer(t, anthropic.New("https://upstream.test"), rt, Config{})

	rec := serveBody(t, srv, "/v1/messages", planBody, planPAYGHeaders)

	if string(rt.bodies[0]) != planBody {
		t.Fatalf("default must be byte-identical passthrough:\n got %s\nwant %s", rt.bodies[0], planBody)
	}
	if got := rec.Header().Get("x-cave-optimization"); strings.Contains(got, breakpointPlanOptimizerID) {
		t.Fatalf("default disclosed the planner: %q", got)
	}
	if row := sink.last(t); len(row.OptimizationIDs) != 0 {
		t.Fatalf("default row claimed an optimization: %v", row.OptimizationIDs)
	}
}

// TestBreakpointPlanUnknownFlagValueIsOff: the flag fails closed. Anything the
// config loader does not recognize normalizes to "off", and the decision point
// re-checks the exact enabling value rather than "not empty".
func TestBreakpointPlanUnknownFlagValueIsOff(t *testing.T) {
	for _, mode := range []string{"", "off", "on", "true", "Frontier", "aggressive"} {
		t.Run("mode="+mode, func(t *testing.T) {
			rt := &captureTransport{responses: []string{planRespBody(0)}}
			srv, _ := newBreakpointPlanServer(t, anthropic.New("https://upstream.test"), rt,
				Config{BreakpointPlan: mode})

			serveBody(t, srv, "/v1/messages", planBody, planPAYGHeaders)

			if string(rt.bodies[0]) != planBody {
				t.Fatalf("flag %q enabled the planner: %s", mode, rt.bodies[0])
			}
		})
	}
}

// TestBreakpointPlanPlacesAndDiscloses: enabled, the planner places its cold-start
// set, names itself on the response and the row — and mints nothing.
func TestBreakpointPlanPlacesAndDiscloses(t *testing.T) {
	rt := &captureTransport{responses: []string{planRespBody(0)}}
	srv, sink := newBreakpointPlanServer(t, anthropic.New("https://upstream.test"), rt,
		Config{BreakpointPlan: breakpointPlanModeFrontier})

	rec := serveBody(t, srv, "/v1/messages", planBody, planPAYGHeaders)

	upstream := string(rt.bodies[0])
	if got := strings.Count(upstream, `"cache_control"`); got != 3 {
		t.Fatalf("upstream carries %d breakpoints, want 3: %s", got, upstream)
	}
	if !strings.Contains(rec.Header().Get("x-cave-optimization"), breakpointPlanOptimizerID) {
		t.Fatalf("optimization header = %q", rec.Header().Get("x-cave-optimization"))
	}
	row := sink.last(t)
	if strings.Join(row.OptimizationIDs, ",") != breakpointPlanOptimizerID {
		t.Fatalf("row optimizations = %v", row.OptimizationIDs)
	}
	// The planner mints nothing until its id earns a place in the minting set
	// under the provider/method/optimizer discipline.
	if row.SavingsUSD != 0 {
		t.Fatalf("the planner booked a saving: %v", row.SavingsUSD)
	}
	if cacheOptimizerIDs[breakpointPlanOptimizerID] {
		t.Fatal("the planner id was added to the cache-savings minting set")
	}
}

// TestBreakpointPlanNeverRunsInRecordMode: record is an unconditional
// wire-preservation boundary, and so is the request-wide pass-through opt-out.
func TestBreakpointPlanNeverRunsInRecordMode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		headers map[string]string
	}{
		{name: "record mode", mode: "record", headers: planPAYGHeaders},
		{name: "pass-through opt-out", mode: "active", headers: map[string]string{
			"x-api-key":         "sk-ant-api-test",
			"x-cave-session":    "sess-plan-1",
			"x-cave-transforms": "caveman.pass-through.v1",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &captureTransport{responses: []string{planRespBody(0)}}
			srv, _ := newBreakpointPlanServer(t, anthropic.New("https://upstream.test"), rt, Config{
				BreakpointPlan: breakpointPlanModeFrontier,
				Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: tc.mode}},
			})

			serveBody(t, srv, "/v1/messages", planBody, tc.headers)

			if string(rt.bodies[0]) != planBody {
				t.Fatalf("%s was transformed: %s", tc.name, rt.bodies[0])
			}
		})
	}
}

// TestBreakpointPlanSetsOpenAISessionCacheKey covers the cross-provider half:
// OpenAI's routing key is set from the session, hashed, and the raw session id
// never leaves the proxy.
func TestBreakpointPlanSetsOpenAISessionCacheKey(t *testing.T) {
	body := `{"model":"gpt-5.6","instructions":"You are an agent.","input":"hello"}`
	rt := &captureTransport{}
	srv, _ := newBreakpointPlanServer(t, openai.New("https://upstream.test"), rt,
		Config{BreakpointPlan: breakpointPlanModeFrontier})

	serveBody(t, srv, "/v1/responses", body, map[string]string{
		"authorization":  "Bearer sk-openai-test",
		"x-cave-session": "sess-plan-1",
	})

	upstream := string(rt.bodies[0])
	if !strings.Contains(upstream, `"prompt_cache_key":"`+openai.SessionCacheKey("sess-plan-1")+`"`) {
		t.Fatalf("session prompt_cache_key missing: %s", upstream)
	}
	if strings.Contains(upstream, "sess-plan-1") {
		t.Fatalf("the raw session id reached the provider: %s", upstream)
	}
}

// TestBreakpointPlanNeedsASession: without x-cave-session there is no stable
// scope to key on, so OpenAI's arm declines rather than invent one.
func TestBreakpointPlanNeedsASession(t *testing.T) {
	body := `{"model":"gpt-5.6","instructions":"You are an agent.","input":"hello"}`
	rt := &captureTransport{}
	srv, _ := newBreakpointPlanServer(t, openai.New("https://upstream.test"), rt,
		Config{BreakpointPlan: breakpointPlanModeFrontier})

	serveBody(t, srv, "/v1/responses", body, map[string]string{"authorization": "Bearer sk-openai-test"})

	if string(rt.bodies[0]) != body {
		t.Fatalf("a sessionless request was keyed: %s", rt.bodies[0])
	}
}

// TestTripwireFreezesTheToolSchemaStrip is the first consumer of LeverAllowed,
// end to end: a session whose cache-creation blows up after the strip ran loses
// the strip, fails open to the untouched catalog, and is told so in a header.
func TestTripwireFreezesTheToolSchemaStrip(t *testing.T) {
	body := toolCatalogRequest("newest turn")
	// One quiet call establishes the baseline, then three spikes strike.
	responses := []string{
		planRespBody(1_000),
		planRespBody(10 * tripwireCacheCreationFloorTokens),
		planRespBody(10 * tripwireCacheCreationFloorTokens),
		planRespBody(10 * tripwireCacheCreationFloorTokens),
		planRespBody(1_000),
	}
	rt := &captureTransport{responses: responses}
	comp := &toolSchemaStripCompressor{}
	srv, _ := newToolSchemaStripServer(t, comp, rt, Config{
		RecoveryViaMCP:  true,
		ToolSchemaStrip: toolSchemaStripMode,
	})

	headers := map[string]string{"x-cave-session": "sess-harm"}
	for k, v := range subscriptionAgentHeaders {
		headers[k] = v
	}

	var last string
	for i := 0; i < len(responses); i++ {
		rec := serveBody(t, srv, "/v1/messages", body, headers)
		last = rec.Header().Get("x-caveman-tripwire")
	}

	if last != toolSchemaStripOptimizerID+"=frozen" {
		t.Fatalf("x-caveman-tripwire = %q, want the frozen strip", last)
	}
	// The last request ran with the lever frozen: the catalog goes upstream
	// exactly as the caller wrote it (fail-open to pass-through).
	final := rt.bodies[len(rt.bodies)-1]
	if sha256.Sum256(final) != sha256.Sum256([]byte(body)) {
		t.Fatalf("a frozen lever still rewrote the request: %s", final)
	}
	if comp.strips != len(responses)-1 {
		t.Fatalf("strip ran %d times, want %d (frozen on the last)", comp.strips, len(responses)-1)
	}
	// A different session is unaffected — the freeze is session-scoped.
	if !srv.ledger.LeverAllowed("sess-other", leverToolSchemaStrip) {
		t.Fatal("freezing one session took the strip from every session")
	}
}

// TestTripwireIsInertWithoutASession: a caller that does not identify its session
// keeps every lever forever. The tripwire needs a session to attribute harm to.
func TestTripwireIsInertWithoutASession(t *testing.T) {
	body := toolCatalogRequest("newest turn")
	responses := []string{}
	for i := 0; i < 6; i++ {
		responses = append(responses, planRespBody(10*tripwireCacheCreationFloorTokens))
	}
	rt := &captureTransport{responses: responses}
	comp := &toolSchemaStripCompressor{}
	srv, _ := newToolSchemaStripServer(t, comp, rt, Config{
		RecoveryViaMCP:  true,
		ToolSchemaStrip: toolSchemaStripMode,
	})

	for i := 0; i < len(responses); i++ {
		rec := serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)
		if got := rec.Header().Get("x-caveman-tripwire"); got != "" {
			t.Fatalf("sessionless traffic was frozen: %q", got)
		}
	}
	if comp.strips != len(responses) {
		t.Fatalf("strip ran %d times, want %d", comp.strips, len(responses))
	}
}
