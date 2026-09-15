package store

import (
	"strings"
	"testing"
)

// TestSubagentSpendMeasuresCostWithoutClaimingWaste pins the decree boundary:
// the sink reports the measured split and the per-type spawn counts, and it
// never argues that any spawn was unnecessary.
func TestSubagentSpendMeasuresCostWithoutClaimingWaste(t *testing.T) {
	tracker := subagentSpendTracker{}
	for i := 0; i < 20; i++ {
		tracker.observeTurn(30_000, true)
		tracker.observeTurn(10_000, false)
	}
	tracker.observeSpawn("Task", `{"subagent_type":"Explore","prompt":"find the thing"}`)
	tracker.observeSpawn("Task", `{"subagent_type":"Explore"}`)
	tracker.observeSpawn("Task", `{"prompt":"no type declared"}`)
	tracker.observeSpawn("Read", `{"file_path":"/x"}`)
	tracker.sessions = 4

	sinks := subagentSpendSink(tracker, &LearnSpend{EffectiveInputUSDPerMTok: 3.0})
	if len(sinks) != 1 {
		t.Fatalf("expected one subagent spend sink, got %d", len(sinks))
	}
	sink := sinks[0]
	if sink.TokensObserved != 600_000 {
		t.Fatalf("subagent tokens = %d, want 600000", sink.TokensObserved)
	}
	if share, _ := sink.Evidence["subagent_share_pct"].(float64); share < 74 || share > 76 {
		t.Fatalf("share = %v, want ~75", share)
	}
	if spawns, _ := sink.Evidence["spawns"].(int); spawns != 3 {
		t.Fatalf("only Task calls are spawns, got %v", sink.Evidence["spawns"])
	}
	types, _ := sink.Evidence["spawns_by_type"].([]map[string]any)
	if len(types) != 2 || types[0]["type"] != "Explore" || types[0]["spawns"] != 2 {
		t.Fatalf("spawn types must rank by count: %+v", types)
	}
	if types[1]["type"] != "unnamed" {
		t.Fatalf("an undeclared type must be named unnamed, not guessed: %+v", types)
	}

	// The decree: this sink may not carry a practice id, and may not tell the
	// user to spawn fewer subagents.
	if practiceIDForSink(sink.SinkID) != "" {
		t.Fatalf("subagent findings must carry no practice id, got %q", practiceIDForSink(sink.SinkID))
	}
	// Scanned with the required disclaimer removed, so its own use of
	// "unnecessary" cannot satisfy or trip this check.
	lowered := strings.ToLower(strings.SplitN(sink.Suggestion, "It is reported for visibility only", 2)[0])
	for _, banned := range []string{"spawn fewer", "unnecessary", "avoid", "reduce the number", "too many"} {
		if strings.Contains(lowered, banned) {
			t.Fatalf("suggestion must not argue against delegation (%q): %q", banned, sink.Suggestion)
		}
	}
	if !strings.Contains(sink.Suggestion, "nothing here says any spawn was unnecessary") {
		t.Fatalf("the decree's disclaimer must be present: %q", sink.Suggestion)
	}
}

// TestSubagentSpendSilentWithoutFanOut keeps the sink off a machine that does
// not delegate.
func TestSubagentSpendSilentWithoutFanOut(t *testing.T) {
	tracker := subagentSpendTracker{sessions: 10}
	for i := 0; i < 50; i++ {
		tracker.observeTurn(50_000, false)
	}
	if got := subagentSpendSink(tracker, nil); len(got) != 0 {
		t.Fatalf("no sidechain traffic must emit nothing: %+v", got)
	}

	thin := subagentSpendTracker{sessions: 1}
	thin.observeTurn(500_000, true)
	if got := subagentSpendSink(thin, nil); len(got) != 0 {
		t.Fatalf("a single session must not carry a finding: %+v", got)
	}
}
