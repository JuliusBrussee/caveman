package store

import (
	"encoding/json"
	"testing"
)

func TestCacheChurnFloorSumsOnlyPostFirstTurnSpikes(t *testing.T) {
	makeSession := func(first int, spikes ...int) cacheHygieneSession {
		tracker := cacheHygieneTracker{}
		tracker.observe(cacheUsageTurn{ContextTotal: 60_000, CacheCreation: first, Present: true})
		for _, spike := range spikes {
			tracker.observe(cacheUsageTurn{ContextTotal: 40_000, CacheCreation: spike, Present: true})
		}
		observation, ok := tracker.observation()
		if !ok {
			t.Fatal("cache session observation missing")
		}
		return observation
	}
	sessions := []cacheHygieneSession{
		makeSession(20_000, 12_000, 13_000, 14_000),
		makeSession(10_000, 15_000, 16_000, 17_000),
	}
	sinks := cacheChurnSink(sessions, nil)
	if len(sinks) != 1 {
		t.Fatalf("cache churn sinks = %d, want 1", len(sinks))
	}
	sink := sinks[0]
	// Spike sum is 87k; turn-1 writes were never included in SpikeTokens.
	if sink.TokensObserved != 87_000 || sink.Evidence["tokens_observed"] != int64(87_000) {
		t.Fatalf("cache churn floor = %d/%v, want 87000", sink.TokensObserved, sink.Evidence["tokens_observed"])
	}
	if sink.Evidence["sessions_affected"] != 2 || sink.Evidence["spike_turns_total"] != 6 || sink.Evidence["median_spike_tokens"] != 15_000 {
		t.Fatalf("cache churn evidence = %+v", sink.Evidence)
	}
	if got := cacheChurnSink(sessions[:1], nil); len(got) != 0 {
		t.Fatalf("one churned session emitted sink: %+v", got)
	}

	var claude map[string]any
	if err := json.Unmarshal([]byte(`{"message":{"usage":{"cache_read_input_tokens":300,"cache_creation_input_tokens":12000}}}`), &claude); err != nil {
		t.Fatal(err)
	}
	if read, creation, present := claudeCacheUsage(claude); read != 300 || creation != 12_000 || !present {
		t.Fatalf("claude cache split = %d/%d/%v", read, creation, present)
	}
	if read, creation, present := opencodeCacheUsage(map[string]any{"cache": map[string]any{"read": float64(400), "write": float64(13_000)}}); read != 400 || creation != 13_000 || !present {
		t.Fatalf("opencode cache split = %d/%d/%v", read, creation, present)
	}
}
