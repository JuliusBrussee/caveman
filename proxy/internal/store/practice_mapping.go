package store

import (
	"slices"
	"strings"
)

// sinkPracticeMappings is the single local-learn dialect join. A trailing "*"
// means prefix match. Order is deliberate and exact enough that one sink can
// never match two practices.
var sinkPracticeMappings = []struct {
	SinkPattern string
	PracticeID  string
}{
	{SinkPattern: "config_tax:*", PracticeID: "context-compression"},
	{SinkPattern: "claude_md_weight:*", PracticeID: "context-compression"},
	{SinkPattern: "claude_md_sections:*", PracticeID: "context-compression"},
	{SinkPattern: "context_dumbzone", PracticeID: "context-compression"},
	{SinkPattern: "dead_load:skills", PracticeID: "tool-schema-deferral"},
	{SinkPattern: "recurring_context:repaste:*", PracticeID: "prompt-prefix-stability"},
}

// cache_churn, reread_waste, compaction_churn, cross_provider, and mcp_surface
// remain unmapped: they are observations, not actionable practice families.

func practiceIDForSink(sinkID string) string {
	match := ""
	for _, mapping := range sinkPracticeMappings {
		if !matchesSinkPattern(sinkID, mapping.SinkPattern) {
			continue
		}
		if match != "" {
			// Fail closed if a future edit makes patterns overlap.
			return ""
		}
		match = mapping.PracticeID
	}
	return match
}

func sinkPatternsForPracticeID(practiceID string) []string {
	patterns := []string{}
	for _, mapping := range sinkPracticeMappings {
		if mapping.PracticeID == practiceID {
			patterns = append(patterns, mapping.SinkPattern)
		}
	}
	slices.Sort(patterns)
	return patterns
}

func matchesSinkPattern(sinkID, pattern string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(sinkID, strings.TrimSuffix(pattern, "*"))
	}
	return sinkID == pattern
}
