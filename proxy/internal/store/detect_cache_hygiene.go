package store

import (
	"fmt"
	"sort"
	"time"
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
	Timestamp     time.Time
	CacheTTL      time.Duration
	Model         string
	MissReason    string
	SessionPath   string
	Source        string
	JSONLLine     int
}

type cacheHygieneTracker struct {
	turns []cacheUsageTurn
}

type cacheSpike struct {
	Classification string    `json:"classification"`
	SessionPath    string    `json:"session_path"`
	Source         string    `json:"source"`
	JSONLLine      int       `json:"jsonl_line"`
	PriorLine      int       `json:"prior_line"`
	Timestamp      time.Time `json:"timestamp"`
	GapSeconds     *float64  `json:"gap_seconds"`
	TTLSeconds     int64     `json:"ttl_seconds"`
	Model          string    `json:"model"`
	PriorModel     string    `json:"prior_model"`
	MissReason     string    `json:"miss_reason"`
	CacheCreation  int       `json:"cache_creation_input_tokens"`
	CacheRead      int       `json:"cache_read_input_tokens"`
}

type cacheHygieneSession struct {
	Spikes []cacheSpike
}

func (t *cacheHygieneTracker) observe(turn cacheUsageTurn) {
	if !turn.Present || turn.ContextTotal <= 0 || turn.CacheRead < 0 || turn.CacheCreation < 0 {
		return
	}
	// Cache hits refresh the lifetime but may write zero tokens, so their
	// breakdown cannot restate the known TTL. Carry it across read-only hits
	// on the same model; never infer a lifetime for unreported new writes.
	if turn.CacheTTL == 0 && turn.CacheCreation == 0 && turn.CacheRead > 0 && len(t.turns) > 0 {
		prior := t.turns[len(t.turns)-1]
		if prior.Model == turn.Model {
			turn.CacheTTL = prior.CacheTTL
		}
	}
	t.turns = append(t.turns, turn)
}

func (t cacheHygieneTracker) observation() (cacheHygieneSession, bool) {
	observation := cacheHygieneSession{}
	for i := 1; i < len(t.turns); i++ {
		turn, prior := t.turns[i], t.turns[i-1]
		if turn.CacheCreation <= cacheChurnSpikeTokens || int64(turn.CacheCreation)*4 <= int64(turn.ContextTotal) {
			continue
		}
		spike := cacheSpike{
			Classification: "unknown", SessionPath: turn.SessionPath, Source: turn.Source,
			JSONLLine: turn.JSONLLine, PriorLine: prior.JSONLLine, Timestamp: turn.Timestamp,
			TTLSeconds: int64(prior.CacheTTL / time.Second), Model: turn.Model, PriorModel: prior.Model,
			MissReason: turn.MissReason, CacheCreation: turn.CacheCreation, CacheRead: turn.CacheRead,
		}
		if !turn.Timestamp.IsZero() && !prior.Timestamp.IsZero() && !turn.Timestamp.Before(prior.Timestamp) {
			gap := turn.Timestamp.Sub(prior.Timestamp).Seconds()
			spike.GapSeconds = &gap
		}
		// A provider tool-change diagnostic confirms invalidation even after idle.
		// Other reasons are unknown unless the prior cache's known lifetime elapsed.
		switch turn.MissReason {
		case "tools_changed":
			spike.Classification = "invalidation"
		case "previous_message_not_found":
			if prior.CacheTTL > 0 && spike.GapSeconds != nil && *spike.GapSeconds >= prior.CacheTTL.Seconds() &&
				(prior.Model == "" || turn.Model == "" || prior.Model == turn.Model) {
				spike.Classification = "cold_resume"
			}
		}
		observation.Spikes = append(observation.Spikes, spike)
	}
	return observation, len(observation.Spikes) > 0
}

type cacheSpikeBucket struct {
	Turns          int   `json:"turns"`
	TokensObserved int64 `json:"tokens_observed"`
}

func cacheChurnSink(sessions []cacheHygieneSession, perTurnHooks []string) []Sink {
	var affected, observed int
	var spikes []int
	var locators []cacheSpike
	buckets := map[string]cacheSpikeBucket{
		"cold_resume": {}, "invalidation": {}, "unknown": {},
	}
	for _, session := range sessions {
		// Preserve the original recurrence threshold and measured cohort. Cold
		// resumes contribute to its volume, but cannot make a session churn alone.
		if len(session.Spikes) < cacheChurnSpikeTurns {
			continue
		}
		observed++
		churn := false
		for _, spike := range session.Spikes {
			churn = churn || spike.Classification != "cold_resume"
			bucket := buckets[spike.Classification]
			bucket.Turns++
			bucket.TokensObserved += int64(spike.CacheCreation)
			buckets[spike.Classification] = bucket
			spikes = append(spikes, spike.CacheCreation)
			locators = append(locators, spike)
		}
		if churn {
			affected++
		}
	}
	if affected < cacheChurnMinSessions {
		return nil
	}
	sort.Ints(spikes)
	tokensObserved := buckets["cold_resume"].TokensObserved + buckets["invalidation"].TokensObserved + buckets["unknown"].TokensObserved
	evidence := map[string]any{
		"sessions_affected":    affected,
		"sessions_with_spikes": observed,
		"spike_turns_total":    len(spikes),
		"median_spike_tokens":  spikes[len(spikes)/2],
		"tokens_observed":      tokensObserved,
		"attribution":          buckets,
		"locators":             locators,
	}
	// Hooks remain candidates only for unresolved transitions, not a diagnosis.
	if len(perTurnHooks) > 0 && buckets["unknown"].Turns > 0 {
		evidence["per_turn_hook_candidates"] = perTurnHooks
	}
	suggestion := fmt.Sprintf("%s; %s; %s after the known cache TTL. Creation totals include all three buckets and are not proven avoidable tokens.", plural(buckets["invalidation"].Turns, "tool-change invalidation"), plural(buckets["unknown"].Turns, "unresolved spike"), plural(buckets["cold_resume"].Turns, "likely cold resume"))
	if buckets["invalidation"].Turns > 0 {
		suggestion += " Inspect tool definitions around the located tools_changed transitions."
	}
	if buckets["unknown"].Turns > 0 {
		suggestion += " Compare the located unresolved transitions before attributing them to hooks, plugins, model changes, or prompt content."
	}
	if buckets["cold_resume"].Turns > 0 {
		suggestion += " Idle expiry is consistent with the cold resumes; eviction or a cache namespace change remains possible."
	}
	return []Sink{{
		SinkID: "cache_churn",
		Title:  fmt.Sprintf("%s had cache invalidations or unresolved rebuilds", plural(affected, "session")),
		Class:  classBehavioral, Basis: learnBasis, Framing: framingHistorical,
		TokensObserved: tokensObserved, Evidence: evidence, Suggestion: suggestion,
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
	suggestion := "Caching already does most of the work here. What input still costs is close to the lowest it can be for this kind of work."
	if spend.EffectiveInputMultiplier >= cacheEfficiencyPoorMultiplier {
		class = classBehavioral
		suggestion = "Most input is billed at full price instead of the cheaper cached price. Idle cache expiry, invalidations, and uncached requests can all contribute. Check cache-miss diagnostics and time gaps before attributing this to prompt content or hooks. Configured hooks remain candidates, not established causes."
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
		Title:    "What input really costs after caching: " + effectiveInputSummary(spend.EffectiveInputMultiplier),
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
