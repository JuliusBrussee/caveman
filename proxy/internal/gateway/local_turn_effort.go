package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// supportsPerMessageEffortModel reports whether (provider, endpoint, model)
// natively supports cache-safe mid-conversation effort marks without mutating
// the top-level effort parameter:
//   - Anthropic direct Messages API (`/messages`, never `count_tokens`):
//     Claude 5 / 5.5 family (`claude-opus-5-5`, `claude-sonnet-5-5`,
//     `claude-haiku-5-5`, `claude-opus-5`, `claude-fable-5*`, `claude-mythos-5*`,
//     and future 6+ series) via `{"role":"system","content":[],"output_config":{"effort":...}}`
//     + `anthropic-beta: mid-conversation-output-config-2026-07-01`.
//     Claude 4.x models do NOT support per-message effort marks and are left untouched.
//   - OpenAI Responses API (`/responses`, including ChatGPT `/chatgpt/responses`):
//     GPT-6+ (`gpt-6*`, excluding `-pro`, `tournament`, `chat-latest`, `deep-research`)
//     via `{"type":"configuration_update","reasoning":{"effort":...}}`.
//     GPT-5.x, o-series, and Chat Completions lack per-message updates and are left untouched.
func supportsPerMessageEffortModel(provider, endpoint, model string) (defaultEffort string, ok bool) {
	ep := strings.ToLower(strings.TrimSpace(endpoint))
	if strings.Contains(ep, "count_tokens") || strings.Contains(ep, "counttokens") {
		return "", false
	}
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return "", false
	}
	switch provider {
	case "anthropic":
		if !strings.HasSuffix(ep, "/messages") {
			return "", false
		}
		if idx := strings.Index(m, "claude-"); idx >= 0 {
			m = m[idx:]
		}
		if !strings.HasPrefix(m, "claude-") {
			return "", false
		}
		m = strings.ReplaceAll(m, ".", "-")
		// Claude 5.5 / 5.1 models default to "medium" effort per Anthropic docs.
		for _, med := range []string{"opus-5-5", "sonnet-5-5", "haiku-5-5", "fable-5-1", "mythos-5-1"} {
			if strings.Contains(m, med) {
				return "medium", true
			}
		}
		// Earlier Claude 5.0 / Fable 5 / Mythos 5 / future 6.x series.
		for _, hi := range []string{"opus-5", "sonnet-5", "haiku-5", "fable-5", "mythos-5", "-6-", "opus-6", "sonnet-6", "haiku-6"} {
			if strings.Contains(m, hi) {
				return "high", true
			}
		}
		return "", false

	case "openai":
		if ep != "/responses" && !strings.HasSuffix(ep, "/responses") {
			return "", false
		}
		if slash := strings.LastIndexByte(m, '/'); slash >= 0 {
			m = m[slash+1:]
		}
		if strings.Contains(m, "-pro") || strings.Contains(m, "tournament") ||
			strings.Contains(m, "chat-latest") || strings.Contains(m, "deep-research") {
			return "", false
		}
		if strings.HasPrefix(m, "gpt-6") || strings.HasPrefix(m, "gpt-7") {
			return "medium", true
		}
		return "", false

	default:
		return "", false
	}
}

// localTurnEffortBodyEligible enforces provider-specific body invariants so we
// never emit a mark on an incompatible thinking/sampling configuration.
func localTurnEffortBodyEligible(provider string, body []byte) bool {
	root, ok := objectRoot(body)
	if !ok {
		return false
	}
	switch provider {
	case "anthropic":
		if th, found := jsonsplice.Field(body, root, "thinking"); found {
			kind, _ := jsonsplice.StringField(body, th, "type")
			// Fixed-budget thinking ("enabled"), explicitly disabled thinking
			// ("disabled"), and Sonnet 5.5 "between_tools" disallow mid-conversation
			// effort changes (or error on Opus 5.5).
			if kind == "enabled" || kind == "disabled" || kind == "between_tools" {
				return false
			}
		}
		return true

	case "openai":
		if responsesWire.noUpdates(body) {
			return false
		}
		for _, forbidden := range []string{"temperature", "top_p", "logprobs"} {
			if _, found := jsonsplice.Field(body, root, forbidden); found {
				return false
			}
		}
		return true

	default:
		return false
	}
}

// localTurnEffortSessionKey ensures every conversation using cache-safe local
// per-message marks has a stable session key for byte-identical mark replay
// across turns. Prefers explicit session headers (Claude Code, Codex, OpenCode,
// x-cave-session); falls back to a deterministic conversation fingerprint built
// from the stable cached prefix (provider + model + tools + system + first user
// turn) so harnesses without session headers (Hermes, raw SDKs) also stay 100%
// cache-safe without colliding across separate conversations.
func localTurnEffortSessionKey(run *routeRun, adapter providers.Adapter, meta providers.RequestMetadata, body []byte) string {
	if run != nil && run.key != "" {
		return run.key
	}
	if adapter == nil {
		return ""
	}
	cp, ok := adapter.(interface {
		CachedPrefixComponents([]byte, providers.RequestMetadata) ([][]byte, int, bool)
	})
	if !ok {
		return ""
	}
	components, _, ok := cp.CachedPrefixComponents(body, meta)
	if !ok || len(components) < 3 || len(components[2]) == 0 {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(meta.Provider))
	h.Write([]byte{0})
	h.Write([]byte(meta.Model))
	for i := 0; i < 3; i++ {
		h.Write([]byte{0})
		h.Write(components[i])
	}
	return "local-conv:" + hex.EncodeToString(h.Sum(nil)[:16])
}

// planLocalTurnEffort produces a RouteAnswer for cache-safe local `--turn-effort`.
//   - Returns (EffortMode:"message", active=true) when this turn should step
//     effort down via an append-only per-message mark.
//   - Returns (Outcome:"off", DefaultEffort:defaultEffort, active=false) on
//     human prompts, first turns, and error/failure tool outputs so any session
//     already carrying a lowered mark automatically restores full baseline
//     effort cache-safely via `perMessage`'s restore path, while untouched
//     sessions remain byte-identical with zero beta headers or marks.
func planLocalTurnEffort(provider, endpoint, model string, body []byte) (RouteAnswer, bool) {
	defaultEffort, ok := supportsPerMessageEffortModel(provider, endpoint, model)
	if !ok || !localTurnEffortBodyEligible(provider, body) {
		return RouteAnswer{Outcome: "off"}, false
	}
	root, ok := objectRoot(body)
	if !ok {
		return RouteAnswer{Outcome: "off"}, false
	}

	switch provider {
	case "anthropic":
		if !providers.ShouldStepDownTurnEffort(body, provider, endpoint) {
			return RouteAnswer{Outcome: "off", DefaultEffort: defaultEffort}, false
		}
		own := messagesWire.topEffort(body, root)
		stepped := providers.StepDownEffort(own)
		if stepped == "" {
			return RouteAnswer{Outcome: "off", DefaultEffort: defaultEffort}, false
		}
		return RouteAnswer{
			Outcome:       "kept",
			Effort:        stepped,
			EffortMode:    "message",
			DefaultEffort: defaultEffort,
		}, true

	case "openai":
		if !providers.IsMechanicalUserContinuation(body) {
			return RouteAnswer{Outcome: "off", DefaultEffort: defaultEffort}, false
		}
		own := responsesWire.topEffort(body, root)
		stepped := providers.StepDownEffort(own)
		if stepped == "" {
			return RouteAnswer{Outcome: "off", DefaultEffort: defaultEffort}, false
		}
		return RouteAnswer{
			Outcome:       "kept",
			Effort:        stepped,
			EffortMode:    "message",
			DefaultEffort: defaultEffort,
		}, true

	default:
		return RouteAnswer{Outcome: "off"}, false
	}
}

func removeOptimizerID(ids []string, target string) []string {
	if len(ids) == 0 {
		return ids
	}
	out := ids[:0]
	for _, id := range ids {
		if id != target {
			out = append(out, id)
		}
	}
	return out
}
