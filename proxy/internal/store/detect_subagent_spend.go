package store

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// detect_subagent_spend.go measures what fan-out costs. Fan-out is the fastest
// growing line item in an agent bill and the one users have least visibility
// into: a subagent's tokens are invisible in the main conversation.
//
// It is bounded by a standing decree (see proxy/CLAUDE.md): spawn count cannot
// prove any spawn was unnecessary, and this sink must never reactivate the
// retired context-exploration-offload opportunity. Measuring COST is not
// claiming WASTE — so this reports the split and the per-type medians, carries
// no practice id, and offers no "spawn fewer" suggestion. What the user does
// with the number is their call.

const (
	subagentSpendMinTokens   = 100_000
	subagentSpendMinSessions = 3
	subagentSpendMaxTypes    = 6
)

// subagentTypePattern pulls the declared subagent type out of a Task call's
// serialized input. A spawn with no declared type is counted under "unnamed"
// rather than guessed at.
var subagentTypePattern = regexp.MustCompile(`"subagent_type"\s*:\s*"([^"]{1,64})"`)

type subagentSpendTracker struct {
	SideTokens   int64
	MainTokens   int64
	SideTurns    int
	MainTurns    int
	Spawns       int
	SpawnsByType map[string]int
	sessions     int
}

func (t *subagentSpendTracker) observeTurn(tokens int, side bool) {
	if tokens <= 0 {
		return
	}
	if side {
		t.SideTokens += int64(tokens)
		t.SideTurns++
		return
	}
	t.MainTokens += int64(tokens)
	t.MainTurns++
}

func (t *subagentSpendTracker) observeSpawn(toolName, input string) {
	if !strings.EqualFold(strings.TrimSpace(toolName), "task") {
		return
	}
	t.Spawns++
	if t.SpawnsByType == nil {
		t.SpawnsByType = map[string]int{}
	}
	t.SpawnsByType[subagentTypeOf(input)]++
}

func subagentTypeOf(input string) string {
	if match := subagentTypePattern.FindStringSubmatch(input); len(match) == 2 {
		if trimmed := strings.TrimSpace(match[1]); trimmed != "" {
			return trimmed
		}
	}
	return "unnamed"
}

func (t *subagentSpendTracker) merge(other subagentSpendTracker) {
	t.SideTokens += other.SideTokens
	t.MainTokens += other.MainTokens
	t.SideTurns += other.SideTurns
	t.MainTurns += other.MainTurns
	t.Spawns += other.Spawns
	t.sessions += other.sessions
	if len(other.SpawnsByType) == 0 {
		return
	}
	if t.SpawnsByType == nil {
		t.SpawnsByType = map[string]int{}
	}
	for name, count := range other.SpawnsByType {
		t.SpawnsByType[name] += count
	}
}

// subagentSpendSink reports the share of provider-counted context that ran in
// sidechains. It states no opinion about whether any of it should have.
func subagentSpendSink(tracker subagentSpendTracker, spend *LearnSpend) []Sink {
	total := tracker.SideTokens + tracker.MainTokens
	if tracker.SideTokens < subagentSpendMinTokens || total <= 0 || tracker.sessions < subagentSpendMinSessions {
		return nil
	}
	share := float64(tracker.SideTokens) * 100 / float64(total)
	evidence := map[string]any{
		"subagent_tokens":     tracker.SideTokens,
		"main_tokens":         tracker.MainTokens,
		"subagent_share_pct":  roundPct(share),
		"subagent_turns":      tracker.SideTurns,
		"sessions_with_usage": tracker.sessions,
		"measurement":         "provider-counted context on transcript turns marked as sidechains",
	}
	if tracker.Spawns > 0 {
		evidence["spawns"] = tracker.Spawns
		evidence["median_tokens_per_spawn"] = tracker.SideTokens / int64(tracker.Spawns)
		if types := topSubagentTypes(tracker.SpawnsByType); len(types) > 0 {
			evidence["spawns_by_type"] = types
		}
	}
	if spend != nil {
		if usd := spend.priceInputTokens(tracker.SideTokens); usd > 0 {
			evidence["subagent_spend_usd"] = usd
		}
	}
	return []Sink{{
		SinkID: "subagent_spend",
		Title:  fmt.Sprintf("%.0f%% of measured context ran inside subagents", share),
		Class:  classBehavioral, Basis: "provider_counted", Framing: framingHistorical,
		TokensObserved: tracker.SideTokens,
		Evidence:       evidence,
		// Deliberately not a recommendation. Delegation is often the cheaper
		// choice; this sink exists so the cost is visible, not to argue against it.
		Suggestion: "Subagent context is invisible in the main conversation, so this share is usually a surprise. It is reported for visibility only — nothing here says any spawn was unnecessary.",
	}}
}

func topSubagentTypes(counts map[string]int) []map[string]any {
	if len(counts) == 0 {
		return nil
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > subagentSpendMaxTypes {
		names = names[:subagentSpendMaxTypes]
	}
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{"type": name, "spawns": counts[name]})
	}
	return out
}
