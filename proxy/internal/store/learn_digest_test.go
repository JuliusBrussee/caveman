package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDigestCarriesIdentityNotContent is the privacy contract, tested the only
// way that survives future edits: serialize the whole digest and prove none of
// the user's data appears anywhere in the bytes.
func TestDigestCarriesIdentityNotContent(t *testing.T) {
	after := 400.0
	plan := LearnPlan{
		Window:          LearnWindow{Since: "30d", From: "2026-07-22T00:00:00Z", To: "2026-08-21T00:00:00Z"},
		CaveScore:       CaveScore{Score: 71},
		SessionsScanned: 42,
		SessionsBySource: map[string]int{"claude": 30, "codex": 12},
		Sinks: []Sink{
			{
				SinkID: "claude_md_weight:project", PracticeID: "context-compression",
				Class: classReducible, Basis: observedLocal, Framing: framingForward,
				TokensPerTurn: 1600, TokensPerDayRate: 16_000, SpendUSDPerDay: 0.048,
				Evidence: map[string]any{
					"path":  "/Users/someone/secret-project/CLAUDE.md",
					"lines": 400,
				},
				Suggestion: "trim it",
			},
			{
				SinkID: "procedure_repeat:deadbeefcafe", Class: classBehavioral,
				Basis: learnBasis, Framing: framingHistorical, TokensObserved: 90_000,
				Evidence: map[string]any{"steps": []string{"Read(*.py)", "Bash(pytest)"}},
			},
			{
				SinkID: "cache_efficiency", Class: classBehavioral, Basis: "provider_counted",
				Evidence: map[string]any{"per_turn_hook_candidates": []string{"UserPromptSubmit: node secret-hook.js"}},
			},
		},
		Confirmed: []LearnConfirmed{{
			SinkID: "claude_md_weight:project", FixKind: "claude_md_weight",
			AppliedAt: "2026-08-01T00:00:00Z", Verdict: "improved",
			Unit: "config_tokens_per_turn", Before: 1600, After: &after,
			Attribution: buildAttribution(attrDeterministic, provenanceIntact, "/Users/someone/secret-project/CLAUDE.md"),
		}},
		Spend: &LearnSpend{
			USD: 34.10, Currency: "USD", CatalogVersion: "2026-08-10",
			EffectiveInputUSDPerMTok: 1.42, EffectiveInputMultiplier: 0.19,
			Components: []LearnSpendComponent{{Key: "cache_read", Tokens: 7_900_000, USD: 11.85}},
			Models:     []LearnSpendModel{{Provider: "anthropic", Model: "claude-opus-5", USD: 30}},
		},
	}
	digest := buildLearnDigest(plan)
	raw, err := json.Marshal(digest)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	encoded := string(raw)

	for _, secret := range []string{
		"/Users/someone", "secret-project", "CLAUDE.md",
		"Bash(pytest)", "Read(*.py)", "secret-hook.js", "UserPromptSubmit",
		"deadbeefcafe", "trim it", "lines",
	} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("digest leaked %q:\n%s", secret, encoded)
		}
	}

	// What it MUST carry, or it cannot seed a plan.
	for _, required := range []string{
		"claude_md_weight:project", "context-compression", "procedure_repeat:*",
		"anthropic", "claude-opus-5", "2026-08-10", "deterministic_remeasure",
	} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("digest dropped %q, which the cloud needs:\n%s", required, encoded)
		}
	}
}

// TestDigestRanksByMeasurementRung proves a provider-counted finding travels
// with a higher rung than a static estimate, so the cloud does not treat every
// local row as equally solid.
func TestDigestRanksByMeasurementRung(t *testing.T) {
	digest := buildLearnDigest(LearnPlan{
		Sinks: []Sink{
			{SinkID: "a_provider", Basis: "provider_counted"},
			{SinkID: "b_observed", Basis: observedLocal},
			{SinkID: "c_inferred", Basis: learnBasis},
			{SinkID: "d_unknown", Basis: "something-new"},
		},
	})
	want := map[string]int{"a_provider": 3, "b_observed": 2, "c_inferred": 1, "d_unknown": 0}
	for _, sink := range digest.Sinks {
		if got := sink.Rung; got != want[sink.SinkID] {
			t.Fatalf("%s rung = %d, want %d", sink.SinkID, got, want[sink.SinkID])
		}
	}
}

// TestDigestStatesWhatItIsAndIsNot keeps the inspect-before-you-send framing on
// the artifact itself rather than only in the docs.
func TestDigestStatesWhatItIsAndIsNot(t *testing.T) {
	digest := buildLearnDigest(LearnPlan{})
	if len(digest.Contains) == 0 || len(digest.Excludes) == 0 {
		t.Fatal("the digest must declare its own contents and exclusions")
	}
	joined := strings.Join(digest.Caveats, " ")
	if !strings.Contains(joined, "is sent anywhere by running this command") {
		t.Fatalf("the digest must say building it sends nothing: %v", digest.Caveats)
	}
	if !strings.Contains(joined, "does not make anything verified") {
		t.Fatalf("uploading must not read as a promotion to verified: %v", digest.Caveats)
	}
}
