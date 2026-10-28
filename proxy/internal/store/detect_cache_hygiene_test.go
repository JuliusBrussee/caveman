package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
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
	// Spike sum is 87k; turn-1 writes were never included in the spikes.
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

func TestCacheSpikeAttribution(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, reason, model, want string
		gap, ttl                  time.Duration
		missingTime               bool
	}{
		{name: "one hour boundary", reason: "previous_message_not_found", gap: time.Hour, ttl: time.Hour, want: "cold_resume"},
		{name: "five minute boundary", reason: "previous_message_not_found", gap: 5 * time.Minute, ttl: 5 * time.Minute, want: "cold_resume"},
		{name: "hot tools", reason: "tools_changed", gap: 2 * time.Second, ttl: time.Hour, want: "invalidation"},
		{name: "idle tools", reason: "tools_changed", gap: 2 * time.Hour, ttl: time.Hour, want: "invalidation"},
		{name: "unknown diagnostic", reason: "unavailable", gap: 2 * time.Hour, ttl: time.Hour, want: "unknown"},
		{name: "future diagnostic", reason: "new_reason", gap: 2 * time.Hour, ttl: time.Hour, want: "unknown"},
		{name: "absent diagnostic", gap: 2 * time.Hour, ttl: time.Hour, want: "unknown"},
		{name: "unknown TTL", reason: "previous_message_not_found", gap: 2 * time.Hour, want: "unknown"},
		{name: "hot miss", reason: "previous_message_not_found", gap: time.Hour - time.Millisecond, ttl: time.Hour, want: "unknown"},
		{name: "backwards timestamp", reason: "previous_message_not_found", gap: -time.Hour, ttl: time.Hour, want: "unknown"},
		{name: "missing timestamp", reason: "previous_message_not_found", gap: time.Hour, ttl: time.Hour, missingTime: true, want: "unknown"},
		{name: "model switch", reason: "previous_message_not_found", model: "other", gap: time.Hour, ttl: time.Hour, want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			priorTime := now
			if tc.missingTime {
				priorTime = time.Time{}
			}
			tracker := cacheHygieneTracker{}
			tracker.observe(cacheUsageTurn{ContextTotal: 40000, CacheCreation: 20000, Present: true, Timestamp: priorTime, CacheTTL: tc.ttl, JSONLLine: 2, Model: "model"})
			model := "model"
			if tc.model != "" {
				model = tc.model
			}
			tracker.observe(cacheUsageTurn{ContextTotal: 40000, CacheCreation: 20000, CacheRead: 1000, Present: true, Timestamp: now.Add(tc.gap), CacheTTL: 5 * time.Minute, JSONLLine: 9, Model: model, MissReason: tc.reason, SessionPath: "/sessions/a.jsonl", Source: "claude"})
			observation, ok := tracker.observation()
			if !ok || len(observation.Spikes) != 1 {
				t.Fatalf("observation = %+v, %v", observation, ok)
			}
			spike := observation.Spikes[0]
			if spike.Classification != tc.want || spike.PriorLine != 2 || spike.JSONLLine != 9 || spike.SessionPath != "/sessions/a.jsonl" || spike.TTLSeconds != int64(tc.ttl/time.Second) || spike.CacheCreation != 20000 || spike.CacheRead != 1000 || spike.MissReason != tc.reason {
				t.Fatalf("spike = %+v, want %s", spike, tc.want)
			}
			if tc.missingTime || tc.gap < 0 {
				if spike.GapSeconds != nil {
					t.Fatal("unknown gap became a value")
				}
			} else if spike.GapSeconds == nil || *spike.GapSeconds != tc.gap.Seconds() {
				t.Fatalf("gap = %v", spike.GapSeconds)
			}
		})
	}
}

func TestCacheChurnSourceMixedSessions(t *testing.T) {
	root := t.TempDir()
	writeSession := func(name string, reasons ...string) {
		var lines []string
		for i, reason := range append([]string{"previous_message_not_found"}, reasons...) {
			lines = append(lines, fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-01T%02d:00:00Z","message":{"id":"%s-%d","model":"claude-sonnet-4-6","usage":{"input_tokens":10,"cache_read_input_tokens":1000,"cache_creation_input_tokens":20000,"cache_creation":{"ephemeral_1h_input_tokens":20000,"ephemeral_5m_input_tokens":0}},"diagnostics":{"cache_miss_reason":{"type":"%s"}}}}`, i*2, name, i, reason))
			// A repeated usage record must not become the prior turn or another spike.
			lines = append(lines, lines[len(lines)-1])
		}
		writeClaudeProject(t, root, "repo", name+".jsonl", lines)
	}
	cold := []string{"previous_message_not_found", "previous_message_not_found", "previous_message_not_found"}
	writeSession("cold-a", cold...)
	writeSession("cold-b", cold...)
	scan := func() []Sink {
		behavior := behaviorScan{}
		if scanSessionSourceUntil(claudeSessionSource{root: root}, time.Time{}, nil, &behavior, newRecurringMiner(), nil) {
			t.Fatal("truncated scan")
		}
		return cacheChurnSink(behavior.CacheHygieneSessions, []string{"candidate hook"})
	}
	if sinks := scan(); len(sinks) != 0 {
		t.Fatalf("cold-only sessions flagged as churn: %+v", sinks)
	}
	writeSession("mixed-a", "previous_message_not_found", "tools_changed", "unavailable")
	if sinks := scan(); len(sinks) != 0 {
		t.Fatalf("cold-only sessions satisfied churn minimum: %+v", sinks)
	}
	writeSession("mixed-b", "previous_message_not_found", "tools_changed", "unavailable")
	sinks := scan()
	if len(sinks) != 1 {
		t.Fatalf("sinks = %+v", sinks)
	}
	sink := sinks[0]
	buckets := sink.Evidence["attribution"].(map[string]cacheSpikeBucket)
	if sink.TokensObserved != 240000 || sink.Evidence["sessions_affected"] != 2 || sink.Evidence["spike_turns_total"] != 12 || buckets["cold_resume"] != (cacheSpikeBucket{Turns: 8, TokensObserved: 160000}) || buckets["invalidation"] != (cacheSpikeBucket{Turns: 2, TokensObserved: 40000}) || buckets["unknown"] != (cacheSpikeBucket{Turns: 2, TokensObserved: 40000}) {
		t.Fatalf("mixed attribution = %+v", sink)
	}
	locators := sink.Evidence["locators"].([]cacheSpike)
	for _, spike := range locators {
		if !strings.HasPrefix(spike.SessionPath, root) || spike.Source != "claude" || spike.PriorLine != spike.JSONLLine-2 || spike.GapSeconds == nil || *spike.GapSeconds != 7200 || spike.TTLSeconds != 3600 || spike.Model != "claude-sonnet-4-6" {
			t.Fatalf("source metadata lost: %+v", spike)
		}
	}
	raw, err := json.Marshal(sink)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	evidence := decoded["evidence"].(map[string]any)
	if evidence["tokens_observed"] != float64(240000) || len(evidence["locators"].([]any)) != 12 {
		t.Fatalf("JSON evidence = %s", raw)
	}
	if strings.Contains(sink.Suggestion, "content that changes every message") || !strings.Contains(sink.Suggestion, "2 tool-change invalidations; 2 unresolved spikes; 8 likely cold resumes") {
		t.Fatalf("suggestion = %s", sink.Suggestion)
	}
}

func TestClaudeCacheTTL(t *testing.T) {
	for _, tc := range []struct {
		usage string
		want  time.Duration
	}{
		{`{"cache_creation_input_tokens":100}`, 0},
		{`{"cache_creation":{"ephemeral_5m_input_tokens":100}}`, 5 * time.Minute},
		{`{"cache_creation":{"ephemeral_1h_input_tokens":100}}`, time.Hour},
		{`{"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":100}}`, time.Hour},
		{`{"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}`, 0},
	} {
		for _, raw := range []string{`{"message":{"usage":` + tc.usage + `}}`, `{"usage":` + tc.usage + `}`} {
			var obj map[string]any
			if err := json.Unmarshal([]byte(raw), &obj); err != nil {
				t.Fatal(err)
			}
			if got := claudeCacheTTL(obj); got != tc.want {
				t.Fatalf("TTL(%s) = %s, want %s", raw, got, tc.want)
			}
		}
	}
}

func TestCacheTTLRefreshAcrossReadOnlyHits(t *testing.T) {
	for _, model := range []string{"model", "other"} {
		tracker := cacheHygieneTracker{}
		now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		tracker.observe(cacheUsageTurn{ContextTotal: 40000, CacheCreation: 20000, Present: true, Timestamp: now, CacheTTL: time.Hour, Model: "model"})
		tracker.observe(cacheUsageTurn{ContextTotal: 40000, CacheRead: 40000, Present: true, Timestamp: now.Add(time.Minute), Model: model, JSONLLine: 2})
		tracker.observe(cacheUsageTurn{ContextTotal: 40000, CacheRead: 40000, Present: true, Timestamp: now.Add(2 * time.Minute), Model: model, JSONLLine: 3})
		tracker.observe(cacheUsageTurn{ContextTotal: 40000, CacheCreation: 20000, Present: true, Timestamp: now.Add(time.Hour + 2*time.Minute), Model: model, MissReason: "previous_message_not_found"})
		observation, _ := tracker.observation()
		want := "cold_resume"
		if model != "model" {
			want = "unknown"
		}
		if len(observation.Spikes) != 1 || observation.Spikes[0].Classification != want || observation.Spikes[0].PriorLine != 3 {
			t.Fatalf("read refresh (%s) = %+v", model, observation)
		}
	}
}
