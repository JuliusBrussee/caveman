package store

import (
	"fmt"
	"sort"
)

const (
	cacheChurnSpikeTokens = 10_000
	cacheChurnSpikeTurns  = 3
	cacheChurnMinSessions = 2
)

type cacheUsageTurn struct {
	ContextTotal  int
	CacheRead     int
	CacheCreation int
	Present       bool
}

type cacheHygieneTracker struct {
	turns []cacheUsageTurn
}

type cacheHygieneSession struct {
	SpikeTokens []int
}

func (t *cacheHygieneTracker) observe(turn cacheUsageTurn) {
	if !turn.Present || turn.ContextTotal <= 0 || turn.CacheRead < 0 || turn.CacheCreation < 0 {
		return
	}
	t.turns = append(t.turns, turn)
}

func (t cacheHygieneTracker) observation() (cacheHygieneSession, bool) {
	if len(t.turns) < 2 {
		return cacheHygieneSession{}, false
	}
	observation := cacheHygieneSession{}
	for _, turn := range t.turns[1:] {
		if turn.CacheCreation > cacheChurnSpikeTokens && int64(turn.CacheCreation)*4 > int64(turn.ContextTotal) {
			observation.SpikeTokens = append(observation.SpikeTokens, turn.CacheCreation)
		}
	}
	return observation, len(observation.SpikeTokens) > 0
}

func cacheChurnSink(sessions []cacheHygieneSession, perTurnHooks []string) []Sink {
	var churned []cacheHygieneSession
	var spikes []int
	for _, session := range sessions {
		if len(session.SpikeTokens) < cacheChurnSpikeTurns {
			continue
		}
		churned = append(churned, session)
		spikes = append(spikes, session.SpikeTokens...)
	}
	if len(churned) < cacheChurnMinSessions {
		return nil
	}
	sort.Ints(spikes)
	var spikeTotal int64
	for _, session := range churned {
		for _, tokens := range session.SpikeTokens {
			spikeTotal += int64(max(0, tokens))
		}
	}
	// SpikeTokens already excludes turn 1, so their sum is the conservative floor.
	tokensObserved := spikeTotal
	evidence := map[string]any{
		"sessions_affected":   len(churned),
		"spike_turns_total":   len(spikes),
		"median_spike_tokens": spikes[len(spikes)/2],
		"tokens_observed":     tokensObserved,
	}
	// Per-turn hooks are the first place a human should look. They are listed
	// as CANDIDATES: this detector sees that the prefix was re-written, never
	// which configured thing wrote it.
	if len(perTurnHooks) > 0 {
		evidence["per_turn_hook_candidates"] = perTurnHooks
	}
	return []Sink{{
		SinkID: "cache_churn",
		Title:  fmt.Sprintf("%d sessions re-wrote a large prompt-cache prefix repeatedly", len(churned)),
		Class:  classBehavioral, Basis: learnBasis, Framing: framingHistorical,
		TokensObserved: tokensObserved,
		Evidence:       evidence,
		Suggestion:     "Something in the setup injects per-turn-changing content near the top of the prompt (hooks, plugins, or timestamps) and is worth finding. Churn is measured; the cause is not identified.",
	}}
}

const (
	// cacheEfficiencyMinInputTokens keeps the multiplier off a machine with a
	// handful of turns, where one cold session swings it entirely.
	cacheEfficiencyMinInputTokens = 200_000
	// cacheEfficiencyPoorMultiplier is where "your cache is not working" starts.
	// A healthy agent loop sits near 0.15: almost every input token is a cache
	// read at a tenth of list. 0.6 means most input is being paid at full rate.
	cacheEfficiencyPoorMultiplier = 0.6
)

// cacheEfficiencySink reports what a million input tokens ACTUALLY cost this
// user after their real cache mix — the single most load-bearing cost number
// available from a transcript, and the one that decides whether every other
// finding in the report is expensive or trivial.
//
// It is deliberately NOT a fix. Naming a multiplier is measurement; deciding
// what to move above or below a cache breakpoint is actuation, which this side
// does not do and cannot prove eligibility for. The suggestion points at
// candidates and stops there.
func cacheEfficiencySink(spend *LearnSpend, perTurnHooks []string) []Sink {
	if spend == nil || spend.EffectiveInputMultiplier <= 0 {
		return nil
	}
	var inputTokens int64
	for _, component := range spend.Components {
		switch component.Key {
		case "fresh_input", "cache_read", "cache_write":
			inputTokens += component.Tokens
		}
	}
	if inputTokens < cacheEfficiencyMinInputTokens {
		return nil
	}
	class := classLoadBearing
	suggestion := "Cache reuse is already doing most of the work here; the remaining input cost is close to the floor for this workload."
	if spend.EffectiveInputMultiplier >= cacheEfficiencyPoorMultiplier {
		class = classBehavioral
		suggestion = "Most input is being billed at full rate rather than as a cache read. Content that changes every turn near the top of the prompt is the usual cause; per-turn hooks are the first candidates to inspect."
	}
	evidence := map[string]any{
		"effective_input_usd_per_mtok": spend.EffectiveInputUSDPerMTok,
		"effective_input_multiplier":   spend.EffectiveInputMultiplier,
		"input_tokens_measured":        inputTokens,
		"catalog_version":              spend.CatalogVersion,
		"basis":                        spend.Basis,
	}
	if len(perTurnHooks) > 0 && spend.EffectiveInputMultiplier >= cacheEfficiencyPoorMultiplier {
		evidence["per_turn_hook_candidates"] = perTurnHooks
	}
	return []Sink{{
		SinkID:   "cache_efficiency",
		Title:    "Input cost after cache reuse: " + effectiveInputSummary(spend.EffectiveInputMultiplier),
		Class:    class,
		Basis:    "provider_counted",
		Framing:  framingHistorical,
		Evidence: evidence,
		// No TokensObserved: this sink states a RATE the other sinks are priced
		// at. Giving it a token volume of its own would double-count every one
		// of them.
		Suggestion: suggestion,
	}}
}
