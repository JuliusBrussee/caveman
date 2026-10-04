package store

import "fmt"

const compactionChurnMinTokenFloor = 2_000

func compactionChurnSink(sessions []compactionSession) []Sink {
	pathFloors := map[string]int{}
	sessionsAffected := 0
	compactionsObserved := 0
	tokensObserved := 0
	for _, session := range sessions {
		if session.Floor <= 0 || len(session.PathFloors) == 0 {
			continue
		}
		sessionsAffected++
		compactionsObserved += max(0, session.Compactions)
		tokensObserved += max(0, session.Floor)
		for path, floor := range session.PathFloors {
			pathFloors[path] += max(0, floor)
		}
	}
	if sessionsAffected < 1 || tokensObserved < compactionChurnMinTokenFloor {
		return nil
	}
	return []Sink{{
		SinkID: "compaction_churn",
		Title:  fmt.Sprintf("%s read files again after the conversation was compacted", plural(sessionsAffected, "session")),
		Class:  classBehavioral, Basis: learnBasis, Framing: framingHistorical,
		TokensObserved: int64(tokensObserved),
		Evidence: map[string]any{
			"sessions_affected":     sessionsAffected,
			"compactions_observed":  compactionsObserved,
			"refetched_paths":       rankedPaths(pathFloors, 5),
			"tokens_observed":       tokensObserved,
			"tokens_observed_basis": "bytes4_estimate",
			"overlap_note":          "May overlap reread_waste when a repeated read follows compaction; totals must not be summed.",
		},
		Suggestion: "This is the measured cost of filling the context window. When a conversation is compacted, the agent forgets file contents and reads them again. Splitting work into shorter sessions, or saving progress notes before compaction, avoids it.",
	}}
}
