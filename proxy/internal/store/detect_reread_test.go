package store

import (
	"strings"
	"testing"
)

func TestRereadFloorExcludesFirstReadAndNormalizesOnePath(t *testing.T) {
	tracker := newReadActivityTracker()
	events := []turnEvent{
		{Repo: "/repo", ToolCalls: []turnToolCall{{Name: "Read", InputSummary: "/repo/a.go", OutputText: strings.Repeat("x", 4_000)}}},
		{Repo: "/repo", ToolCalls: []turnToolCall{{Name: "read_file", InputSummary: "a.go", OutputText: strings.Repeat("x", 12_000)}}},
		{Repo: "/repo", ToolCalls: []turnToolCall{{Name: "Bash", InputSummary: "sed -n '1,20p' ./a.go", OutputText: strings.Repeat("x", 10_000)}}},
	}
	for _, event := range events {
		tracker.observe(event, "/repo")
	}
	observation, ok := tracker.rereadObservation()
	if !ok {
		t.Fatal("reread observation missing")
	}
	if observation.Floor != 5_500 || observation.PathFloors["/repo/a.go"] != 5_500 {
		t.Fatalf("reread floor = %+v, want later reads 3000+2500", observation)
	}
	sinks := rereadWasteSink([]rereadSession{observation})
	if len(sinks) != 1 || sinks[0].TokensObserved != 5_500 {
		t.Fatalf("reread sink = %+v", sinks)
	}
	if sinks[0].Evidence["tokens_observed_basis"] != "bytes4_estimate" || sinks[0].Evidence["overlap_note"] == "" {
		t.Fatalf("reread basis/overlap evidence = %+v", sinks[0].Evidence)
	}
	if _, ok := readLikePath("Bash", "cat a.go b.go", "/repo"); ok {
		t.Fatal("multi-path cat was accepted as exactly one path")
	}
}
