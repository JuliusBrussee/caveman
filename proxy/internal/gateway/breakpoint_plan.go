package gateway

import (
	"encoding/json"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// breakpointPlanOptimizerID labels the cache-breakpoint planner in
// x-cave-optimization and on the telemetry row, so a request whose breakpoints we
// planned is never indistinguishable from one we left alone.
//
// It is deliberately absent from cacheOptimizerIDs. Anthropic-direct breakpoints
// are the provider_causal_cache method, so listing this id there would start
// attributing cache savings to planner-placed breakpoints. That promotion needs
// the exact provider/method/optimizer tuple discipline and is a deliberate
// later change with its own review. Today the planner claims no tokens, no
// ratio, and no dollars.
const breakpointPlanOptimizerID = "cache-breakpoint-plan"

// breakpointPlanModeFrontier is the one value that enables the planner. It names
// what is placed — a rolling frontier breakpoint plus the lookback guard — rather
// than promising a saving. "", "off", and any unrecognized value all mean off,
// normalized in the config loader AND re-checked here.
const breakpointPlanModeFrontier = "frontier"

// BreakpointPlanner is the optional adapter capability that places provider-native
// cache routing metadata on the upstream request: Anthropic cache_control
// breakpoints, OpenAI prompt_cache_key. Neither is model-visible, so both are
// byte-safe under honesty rule #2 — they are hints on the upstream request only.
//
// ok=false means "leave the body exactly as it is", which is what every parse
// anomaly, every unsupported shape, and every already-managed request returns.
type BreakpointPlanner interface {
	PlanCacheBreakpoints(body []byte, meta providers.RequestMetadata, payg bool) ([]byte, bool)
}

// breakpointPlanAllowed reports whether the planner may run for this request.
//
// Two gates, both fail-closed: the operator flag (default off — the lever does
// not ship on by default, it ships behind the escalation ladder), and the session
// tripwire, which takes the lever away for the rest of a session that measured
// harm from it.
func (s *Server) breakpointPlanAllowed(sessionID string) bool {
	if s.breakpointPlan != breakpointPlanModeFrontier {
		return false
	}
	return s.ledger.LeverAllowed(sessionID, leverBreakpointPlan)
}

// planBreakpoints runs the adapter's planner over the bytes that are actually
// going upstream. An adapter without the capability, a planner that declined, an
// empty result, or output that is not valid JSON all leave the body untouched.
func (s *Server) planBreakpoints(adapter providers.Adapter, body []byte, meta providers.RequestMetadata, payg bool) ([]byte, bool) {
	planner, ok := adapter.(BreakpointPlanner)
	if !ok {
		return nil, false
	}
	out, ok := planner.PlanCacheBreakpoints(body, meta, payg)
	if !ok || len(out) == 0 || !json.Valid(out) {
		return nil, false
	}
	return out, true
}

// observeSession records one completed call in the session ledger and logs any
// lever the harm tripwire just froze. It is content-blind: only token counters
// and lever names cross this boundary.
func (s *Server) observeSession(sessionID string, active []lever, usage providers.UsageObservation, requestID string) {
	froze := s.ledger.Observe(sessionID, active, usage)
	if len(froze) == 0 || s.logger == nil {
		return
	}
	for _, lv := range froze {
		s.logger.Warn("harm tripwire froze an optimization lever for this session",
			"lever", string(lv),
			"request_id", requestID,
			"cache_creation_input_tokens", usage.CacheCreationInputTokens,
			"strikes", tripwireStrikesToFreeze)
	}
}

// tripwireDisclosure renders the x-caveman-tripwire header value for a session,
// or "" when nothing is frozen.
//
// The freeze is decided from a call's usage, which the proxy only learns AFTER
// the response headers have gone to the client — so the request that trips the
// wire cannot carry its own disclosure. The header appears on every later request
// of that session, which is precisely the set of requests the freeze changes.
func (s *Server) tripwireDisclosure(sessionID string) string {
	frozen := s.ledger.FrozenLevers(sessionID)
	if len(frozen) == 0 {
		return ""
	}
	parts := make([]string, 0, len(frozen))
	for _, lv := range frozen {
		parts = append(parts, string(lv)+"=frozen")
	}
	return strings.Join(parts, ",")
}
