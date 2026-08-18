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

func cacheChurnSink(sessions []cacheHygieneSession) []Sink {
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
	return []Sink{{
		SinkID: "cache_churn",
		Title:  fmt.Sprintf("%d sessions re-wrote a large prompt-cache prefix repeatedly", len(churned)),
		Class:  classBehavioral, Basis: learnBasis, Framing: framingHistorical,
		TokensObserved: tokensObserved,
		Evidence: map[string]any{
			"sessions_affected":   len(churned),
			"spike_turns_total":   len(spikes),
			"median_spike_tokens": spikes[len(spikes)/2],
			"tokens_observed":     tokensObserved,
		},
		Suggestion: "Something in the setup injects per-turn-changing content near the top of the prompt (hooks, plugins, or timestamps) and is worth finding. Churn is measured; the cause is not identified.",
	}}
}
