package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactionChurnParsesObservedBoundaryAndCompactSummaryShape(t *testing.T) {
	var boundary, summary map[string]any
	if err := json.Unmarshal([]byte(`{"type":"system","subtype":"compact_boundary"}`), &boundary); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"summary"}}`), &summary); err != nil {
		t.Fatal(err)
	}
	if !claudeCompactionMarker(boundary) || !claudeCompactionMarker(summary) {
		t.Fatalf("observed marker shapes not recognized: boundary=%v summary=%v", boundary, summary)
	}

	tracker := newReadActivityTracker()
	tracker.observe(turnEvent{Repo: "/repo", ToolCalls: []turnToolCall{{Name: "Read", InputSummary: "a.go", OutputText: "first"}}}, "/repo")
	tracker.observe(turnEvent{Compaction: claudeCompactionMarker(boundary)}, "/repo")
	tracker.observe(turnEvent{Compaction: claudeCompactionMarker(summary)}, "/repo")
	tracker.observe(turnEvent{Repo: "/repo", ToolCalls: []turnToolCall{{Name: "Read", InputSummary: "a.go", OutputText: strings.Repeat("x", 8_400)}}}, "/repo")
	observation, ok := tracker.compactionObservation()
	if !ok || observation.Compactions != 1 || observation.Floor != 2_100 {
		t.Fatalf("compaction observation = %+v ok=%v, want one coalesced event and 2100 floor", observation, ok)
	}
	sinks := compactionChurnSink([]compactionSession{observation})
	if len(sinks) != 1 || sinks[0].TokensObserved != 2_100 {
		t.Fatalf("compaction sink = %+v", sinks)
	}
	if sinks[0].Evidence["tokens_observed_basis"] != "bytes4_estimate" || sinks[0].Evidence["overlap_note"] == "" {
		t.Fatalf("compaction basis/overlap evidence = %+v", sinks[0].Evidence)
	}
}

func TestCompactionChurnResetsPreBoundaryPathsAtEachCompaction(t *testing.T) {
	tracker := newReadActivityTracker()
	tracker.observe(turnEvent{Repo: "/repo", ToolCalls: []turnToolCall{
		{Name: "Read", InputSummary: "a.go", OutputText: "first"},
		{Name: "Read", InputSummary: "b.go", OutputText: "first"},
	}}, "/repo")
	tracker.observe(turnEvent{Compaction: true}, "/repo")
	tracker.observe(turnEvent{Repo: "/repo", ToolCalls: []turnToolCall{{Name: "Read", InputSummary: "a.go", OutputText: strings.Repeat("x", 8_400)}}}, "/repo")
	tracker.observe(turnEvent{Compaction: true}, "/repo")
	tracker.observe(turnEvent{Repo: "/repo", ToolCalls: []turnToolCall{{Name: "Read", InputSummary: "b.go", OutputText: strings.Repeat("x", 10_000)}}}, "/repo")
	observation, ok := tracker.compactionObservation()
	if !ok || observation.Compactions != 2 || observation.Floor != 2_100 || observation.PathFloors["/repo/b.go"] != 0 {
		t.Fatalf("per-boundary compaction observation = %+v ok=%v", observation, ok)
	}
}
