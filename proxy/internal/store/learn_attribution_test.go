package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestFingerprintProvesTheEditIsStillOurs is the core attribution mechanism:
// we hash the artifact at apply time, so a later scan can state whether the
// change we proposed is still the change on disk — rather than assuming it and
// crediting caveman for someone else's edit.
func TestFingerprintProvesTheEditIsStillOurs(t *testing.T) {
	path := writeTempFile(t, "# trimmed\n\nshort file\n")
	sink := Sink{SinkID: "claude_md_weight:project", Evidence: map[string]any{"path": path}}

	target := fingerprintFixTarget(sink)
	if target == nil || target.SHA256 == "" {
		t.Fatal("a file-editing sink must fingerprint its target")
	}

	verdict, tokens, ok := checkFixProvenance(target)
	if verdict != provenanceIntact || !ok || tokens <= 0 {
		t.Fatalf("unmodified file must read intact, got %q tokens=%d ok=%v", verdict, tokens, ok)
	}

	if err := os.WriteFile(path, []byte("# trimmed\n\nsomeone else edited this later\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if verdict, _, _ := checkFixProvenance(target); verdict != provenanceChanged {
		t.Fatalf("a later edit must downgrade provenance, got %q", verdict)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if verdict, _, _ := checkFixProvenance(target); verdict != provenanceMissing {
		t.Fatalf("a deleted target must read missing, got %q", verdict)
	}

	if verdict, _, _ := checkFixProvenance(nil); verdict != provenanceUnfingerprnt {
		t.Fatalf("a pre-fingerprint record must say so, got %q", verdict)
	}
}

// TestBehavioralSinksAreNotFingerprinted keeps the mechanism honest in the
// other direction: a sink with no file to edit must not invent a target.
func TestBehavioralSinksAreNotFingerprinted(t *testing.T) {
	if got := fingerprintFixTarget(Sink{SinkID: "context_dumbzone", Evidence: map[string]any{}}); got != nil {
		t.Fatalf("behavioral sink must have no fingerprint target: %+v", got)
	}
}

// TestAttributionRungsCarryTheirConfounders proves a weak measurement can never
// be read as a strong one: every rung names what it cannot rule out, and a
// broken provenance drops confidence regardless of method.
func TestAttributionRungsCarryTheirConfounders(t *testing.T) {
	strong := buildAttribution(attrDeterministic, provenanceIntact, "/x/CLAUDE.md")
	if strong.Confidence != "high" || strong.Rung != 4 {
		t.Fatalf("deterministic + intact must be the top rung: %+v", strong)
	}
	if len(strong.Confounders) == 0 {
		t.Fatal("even the strongest rung must state what it does not prove")
	}

	weak := buildAttribution(attrTimeSeries, provenanceIntact, "")
	if weak.Confidence != "low" || weak.Rung >= strong.Rung {
		t.Fatalf("time series must rank below deterministic: %+v", weak)
	}
	if len(weak.Confounders) < 2 {
		t.Fatalf("time series must name its missing control arm: %+v", weak.Confounders)
	}

	// A perfect method on a file someone else edited is NOT high confidence.
	tainted := buildAttribution(attrDeterministic, provenanceChanged, "/x/CLAUDE.md")
	if tainted.Confidence != "low" {
		t.Fatalf("changed provenance must drop confidence, got %q", tainted.Confidence)
	}
	if !strings.Contains(strings.Join(tainted.Confounders, " "), "changed after the fix") {
		t.Fatalf("changed provenance must be disclosed: %+v", tainted.Confounders)
	}
}

// TestSavingsNeverSumAcrossRungs is the anti-laundering guard. A deterministic
// re-measure and a before/after guess must never collapse into one headline,
// because that is exactly how a weak number gets read as a strong one.
func TestSavingsNeverSumAcrossRungs(t *testing.T) {
	after := 400.0
	plan := LearnPlan{
		Window:        LearnWindow{Since: "30d"},
		observedTurns: 300,
		Spend: &LearnSpend{
			Currency: "USD", Basis: spendBasis, EffectiveInputUSDPerMTok: 3.0,
		},
		Confirmed: []LearnConfirmed{
			{
				SinkID: "claude_md_weight:project", FixKind: "claude_md_weight",
				AppliedAt: "2026-08-01T00:00:00Z", Verdict: "improved",
				Unit: "config_tokens_per_turn", Before: 1600, After: &after,
				Attribution: buildAttribution(attrDeterministic, provenanceIntact, "/x/CLAUDE.md"),
			},
			{
				SinkID: "context_dumbzone", FixKind: "dumbzone_advice",
				AppliedAt: "2026-08-02T00:00:00Z", Verdict: "improved",
				Unit: "turns_over_half_window_pct", Before: 40, After: ptrFloat(20),
				Attribution: buildAttribution(attrTimeSeries, provenanceUnfingerprnt, ""),
			},
		},
	}
	savings := buildLearnSavings(plan)
	if len(savings.Rows) != 2 {
		t.Fatalf("both rows must appear: %+v", savings.Rows)
	}
	if savings.Rows[0].Attributed.Method != attrDeterministic {
		t.Fatalf("strongest rung must sort first, got %q", savings.Rows[0].Attributed.Method)
	}
	if len(savings.TotalSavedUSDByRung) < 1 {
		t.Fatalf("priced savings must be bucketed by rung: %+v", savings.TotalSavedUSDByRung)
	}
	if _, blended := savings.TotalSavedUSDByRung["total"]; blended {
		t.Fatal("there must be no blended cross-rung total")
	}
	joined := strings.Join(savings.Caveats, " ")
	if !strings.Contains(joined, "never summed across methods") {
		t.Fatalf("the no-blending rule must be stated to the reader: %v", savings.Caveats)
	}
}

// TestRegressionIsNeverPriced keeps a regression from rendering as money
// returned. It stays visible, in tokens, with its verdict.
func TestRegressionIsNeverPriced(t *testing.T) {
	after := 2400.0
	plan := LearnPlan{
		Window: LearnWindow{Since: "30d"}, observedTurns: 100,
		Spend: &LearnSpend{Currency: "USD", EffectiveInputUSDPerMTok: 3.0},
		Confirmed: []LearnConfirmed{{
			SinkID: "claude_md_weight:user", FixKind: "claude_md_weight",
			AppliedAt: "2026-08-01T00:00:00Z", Verdict: "regressed",
			Unit: "config_tokens_per_turn", Before: 1200, After: &after,
			Attribution: buildAttribution(attrDeterministic, provenanceIntact, "/x/CLAUDE.md"),
		}},
	}
	savings := buildLearnSavings(plan)
	if len(savings.Rows) != 1 {
		t.Fatalf("a regression must stay in the ledger: %+v", savings.Rows)
	}
	if savings.Rows[0].SavedUSD != nil {
		t.Fatalf("a regression must carry no dollar figure, got %v", *savings.Rows[0].SavedUSD)
	}
	if savings.Rows[0].Verdict != "regressed" {
		t.Fatalf("verdict must survive: %q", savings.Rows[0].Verdict)
	}
}

// TestEmptyLedgerSaysSoPlainly refuses a zero-row card that looks like a
// measurement.
func TestEmptyLedgerSaysSoPlainly(t *testing.T) {
	savings := buildLearnSavings(LearnPlan{Window: LearnWindow{Since: "30d"}})
	if len(savings.Rows) != 0 {
		t.Fatalf("no fixes means no rows: %+v", savings.Rows)
	}
	if !strings.Contains(strings.Join(savings.Caveats, " "), "No fix has been recorded yet") {
		t.Fatalf("empty ledger must explain itself: %v", savings.Caveats)
	}
}

func ptrFloat(v float64) *float64 { return &v }
