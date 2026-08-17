package store

import (
	"reflect"
	"testing"
)

func TestPracticeIDForSink(t *testing.T) {
	tests := []struct {
		sinkID string
		want   string
	}{
		{sinkID: "config_tax:baseline", want: "context-compression"},
		{sinkID: "claude_md_weight:user", want: "context-compression"},
		{sinkID: "claude_md_weight:project", want: "context-compression"},
		{sinkID: "claude_md_weight:codex", want: "context-compression"},
		{sinkID: "claude_md_sections:user", want: "context-compression"},
		{sinkID: "context_dumbzone", want: "context-compression"},
		{sinkID: "dead_load:skills", want: "tool-schema-deferral"},
		{sinkID: "subagent_overuse", want: ""},
		{sinkID: "recurring_context:repaste:abc123", want: "prompt-prefix-stability"},
		{sinkID: "config_surface", want: ""},
		{sinkID: "cache_churn", want: ""},
		{sinkID: "reread_waste", want: ""},
		{sinkID: "compaction_churn", want: ""},
		{sinkID: "cross_provider:depth", want: ""},
		{sinkID: "mcp_surface", want: ""},
		{sinkID: "unknown", want: ""},
	}

	for _, test := range tests {
		t.Run(test.sinkID, func(t *testing.T) {
			if got := practiceIDForSink(test.sinkID); got != test.want {
				t.Fatalf("practiceIDForSink(%q) = %q, want %q", test.sinkID, got, test.want)
			}
		})
	}
}

func TestSinkPatternsForPracticeID(t *testing.T) {
	if got, want := sinkPatternsForPracticeID("context-compression"), []string{
		"claude_md_sections:*",
		"claude_md_weight:*",
		"config_tax:*",
		"context_dumbzone",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sinkPatternsForPracticeID(context-compression) = %#v, want %#v", got, want)
	}
	if got := sinkPatternsForPracticeID("unknown"); len(got) != 0 {
		t.Fatalf("sinkPatternsForPracticeID(unknown) = %#v, want empty", got)
	}
}

func TestGeneratedLearnSinksCarryPracticeIDOrHonestEmpty(t *testing.T) {
	cfg := configScan{
		ClaudeMDUser: &ConfigSnapshot{
			Scope:  "user",
			Kind:   "claude_md",
			Lines:  claudeMDLineBudget + 1,
			Tokens: claudeMDTokenAlarm + 1,
		},
		HookCount:   10,
		PluginCount: 1,
	}
	beh := behaviorScan{
		Turns:             10,
		DumbzoneTurns:     2,
		TaskSpawns:        3,
		SessionsScanned:   3,
		SessionsWithTasks: 1,
	}

	sinks := append([]Sink{}, configSinks(cfg, 1)...)
	sinks = append(sinks, dumbzoneSink(beh)...)
	sinks = append(sinks, subagentSink(beh)...)
	sinks = append(sinks, surfaceSink(cfg)...)
	for i := range sinks {
		sinks[i].PracticeID = practiceIDForSink(sinks[i].SinkID)
	}

	for _, sink := range sinks {
		switch sink.SinkID {
		case "config_surface", "subagent_overuse":
			if sink.PracticeID != "" {
				t.Fatalf("sink %q practice_id = %q, want honest empty", sink.SinkID, sink.PracticeID)
			}
		default:
			if sink.PracticeID == "" {
				t.Fatalf("sink %q missing expected practice_id", sink.SinkID)
			}
		}
	}
}
