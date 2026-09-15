package store

import (
	"strings"
	"testing"
)

// TestToolSignatureGroupsWithoutLeakingPaths pins both halves of the
// normalization contract: calls collapse into an actionable shape, and no
// user path, argument, or query survives into a printable report row.
func TestToolSignatureGroupsWithoutLeakingPaths(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"Read", "/Users/someone/secret-project/pnpm-lock.yaml", "Read(lockfile)"},
		{"Read", "/Users/someone/secret-project/src/main.go", "Read(*.go)"},
		{"Read", "/Users/someone/.zshrc", "Read(dotfile)"},
		{"Bash", "git diff --stat HEAD~3 -- /Users/someone/private", "Bash(git diff)"},
		{"Bash", "pytest -k test_secret_thing", "Bash(pytest)"},
		{"Bash", "/usr/local/bin/rg --hidden pattern", "Bash(rg)"},
		{"mcp__linear__list_issues", "anything", "mcp__linear__list_issues"},
		{"Grep", "some secret pattern", "Grep"},
	}
	for _, tc := range cases {
		got := toolSignature(tc.name, tc.input)
		if got != tc.want {
			t.Fatalf("toolSignature(%q, %q) = %q, want %q", tc.name, tc.input, got, tc.want)
		}
		if strings.Contains(got, "secret") || strings.Contains(got, "/Users/") {
			t.Fatalf("signature leaked user data: %q", got)
		}
	}
	if toolSignature("", "x") != "" {
		t.Fatal("an unnamed tool must not produce a signature")
	}
}

// TestToolPortfolioRanksAndDisclosesItsBasis proves the sink names the heaviest
// call shape, prices it at the measured rate, and labels its token basis as the
// local estimate rather than letting it read as provider-counted.
func TestToolPortfolioRanksAndDisclosesItsBasis(t *testing.T) {
	tracker := newToolPortfolioTracker()
	for i := 0; i < 12; i++ {
		tracker.observe(learnToolCall{Name: "Read", Input: "/repo/pnpm-lock.yaml", OutputTokens: 30_000})
		tracker.observe(learnToolCall{Name: "Bash", Input: "git diff", OutputTokens: 9_000})
		tracker.observe(learnToolCall{Name: "Grep", Input: "x", OutputTokens: 200})
	}
	spend := &LearnSpend{EffectiveInputUSDPerMTok: 3.0, Currency: "USD"}
	sinks := toolPortfolioSink(tracker, spend)
	if len(sinks) != 1 {
		t.Fatalf("expected one portfolio sink, got %d", len(sinks))
	}
	sink := sinks[0]
	if !strings.Contains(sink.Title, "Read(lockfile)") {
		t.Fatalf("title must name the heaviest shape: %q", sink.Title)
	}
	rows, ok := sink.Evidence["top_signatures"].([]map[string]any)
	if !ok || len(rows) == 0 {
		t.Fatalf("evidence must carry ranked rows: %+v", sink.Evidence)
	}
	if rows[0]["signature"] != "Read(lockfile)" {
		t.Fatalf("rows must be ranked by tokens: %+v", rows)
	}
	if _, priced := rows[0]["spend_usd_next_turn"]; !priced {
		t.Fatalf("a priced window must price the row: %+v", rows[0])
	}
	if sink.Evidence["signature_token_basis"] != "estimated_local" {
		t.Fatalf("the estimate must be labeled, not passed off as provider-counted: %+v", sink.Evidence)
	}
	// Grep is below every floor and must not appear as a row.
	for _, row := range rows {
		if row["signature"] == "Grep" {
			t.Fatalf("below-threshold shapes must not be listed: %+v", rows)
		}
	}
}

// TestToolPortfolioSilentBelowThreshold keeps the sink off a machine with
// nothing worth reporting.
func TestToolPortfolioSilentBelowThreshold(t *testing.T) {
	tracker := newToolPortfolioTracker()
	tracker.observe(learnToolCall{Name: "Read", Input: "/repo/a.go", OutputTokens: 100})
	if got := toolPortfolioSink(tracker, nil); len(got) != 0 {
		t.Fatalf("trivial volume must emit nothing: %+v", got)
	}
	if got := toolPortfolioSink(newToolPortfolioTracker(), nil); len(got) != 0 {
		t.Fatalf("an empty tracker must emit nothing: %+v", got)
	}
}

// TestCacheEfficiencySinkNamesCandidatesNotCauses guards the honesty boundary
// on the biggest money number: it reports a measured multiplier and lists
// per-turn hooks as candidates, and it never claims to have found the cause.
func TestCacheEfficiencySinkNamesCandidatesNotCauses(t *testing.T) {
	cold := &LearnSpend{
		EffectiveInputMultiplier: 0.95, EffectiveInputUSDPerMTok: 14.2,
		CatalogVersion: "2026-08-10", Basis: spendBasis,
		Components: []LearnSpendComponent{{Key: "fresh_input", Tokens: 900_000}, {Key: "cache_read", Tokens: 100_000}},
	}
	sinks := cacheEfficiencySink(cold, []string{"UserPromptSubmit: node caveman-mode-tracker.js"})
	if len(sinks) != 1 {
		t.Fatalf("poor reuse over the floor must emit a sink, got %d", len(sinks))
	}
	if sinks[0].TokensObserved != 0 || sinks[0].TokensPerDayRate != 0 {
		t.Fatal("a rate sink must carry no token volume of its own; it would double-count every other sink")
	}
	candidates, ok := sinks[0].Evidence["per_turn_hook_candidates"].([]string)
	if !ok || len(candidates) != 1 {
		t.Fatalf("per-turn hooks must be listed as candidates: %+v", sinks[0].Evidence)
	}
	if !strings.Contains(sinks[0].Suggestion, "candidates") {
		t.Fatalf("the suggestion must stay a candidate list, not an accusation: %q", sinks[0].Suggestion)
	}

	// Caveman's own hook must be listable. If this detector can never name it,
	// it is not measuring honestly.
	if !strings.Contains(candidates[0], "caveman") {
		t.Fatalf("caveman's own per-turn hook must be nameable: %v", candidates)
	}

	warm := &LearnSpend{
		EffectiveInputMultiplier: 0.12, EffectiveInputUSDPerMTok: 1.9,
		Components: []LearnSpendComponent{{Key: "cache_read", Tokens: 5_000_000}},
	}
	warmSinks := cacheEfficiencySink(warm, nil)
	if len(warmSinks) != 1 || warmSinks[0].Class != classLoadBearing {
		t.Fatalf("healthy reuse must report as load-bearing, not a problem: %+v", warmSinks)
	}
	if _, leaked := warmSinks[0].Evidence["per_turn_hook_candidates"]; leaked {
		t.Fatal("a healthy cache must not accuse hooks of anything")
	}

	if got := cacheEfficiencySink(&LearnSpend{EffectiveInputMultiplier: 0.5,
		Components: []LearnSpendComponent{{Key: "fresh_input", Tokens: 1_000}}}, nil); len(got) != 0 {
		t.Fatalf("too little traffic must emit nothing: %+v", got)
	}
	if got := cacheEfficiencySink(nil, nil); len(got) != 0 {
		t.Fatal("an unpriced window must emit no efficiency sink")
	}
}
