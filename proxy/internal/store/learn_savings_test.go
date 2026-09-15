package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

func recordCompressedRow(t *testing.T, s *Store, ts, id, basis string, before, after int) {
	t.Helper()
	s.Record(gateway.RequestRecord{
		Timestamp: ts, RequestID: id,
		Provider: "anthropic", Model: "claude-fable-5", Endpoint: "/v1/messages",
		StatusCode: 200, Basis: "inferred", RuntimeMode: "compress",
		TokenUsageBasis: "provider_complete", AuthMode: "payg",
		CompressionTokensBefore: before, CompressionTokensAfter: after,
		CompressionTokenCountBasis: basis, OptimizationIDs: []string{},
	})
}

// TestWrapMeasuredSince proves the proxy-measured savings block: real
// compression cuts and observe-mode estimates sum per window, an empty store
// yields nil rather than a zero card, activity without any saving yields nil,
// mixed bases are named mixed, and the since filter excludes older rows.
func TestWrapMeasuredSince(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if got := s.wrapMeasuredSince(time.Time{}); got != nil {
		t.Fatalf("empty store must yield nil, got %+v", got)
	}

	// Recorded traffic with zero cut and zero estimate: still nil. This guard
	// keeps a "saved 0 tokens" card off a machine that proxied but never saved.
	recordCompressedRow(t, s, "2026-08-10 09:59:00.000", "flat-1", "estimated_engine_o200k", 100, 100)
	if got := s.wrapMeasuredSince(time.Time{}); got != nil {
		t.Fatalf("no cut and no estimate must yield nil, got %+v", got)
	}

	recordCompressedRow(t, s, "2026-08-10 10:00:00.000", "cut-1", "estimated_engine_o200k", 900, 300)
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-08-10 10:01:00.000", RequestID: "obs-1",
		Provider: "anthropic", Model: "claude-fable-5", Endpoint: "/v1/messages",
		StatusCode: 200, Basis: "inferred", RuntimeMode: "record",
		TokenUsageBasis: "provider_complete", AuthMode: "payg",
		WouldSaveTokens: 250, OptimizationIDs: []string{},
	})
	recordCompressedRow(t, s, "2026-01-01 10:00:00.000", "cut-old", "estimated_engine_o200k", 100, 50)

	all := s.wrapMeasuredSince(time.Time{})
	if all == nil {
		t.Fatal("expected a wrap-measured block")
	}
	if all.Requests != 4 || all.CompressedRequests != 2 {
		t.Errorf("requests = %d compressed = %d, want 4/2", all.Requests, all.CompressedRequests)
	}
	if all.TokensSaved != 650 || all.WouldSaveTokens != 250 {
		t.Errorf("saved = %d wouldSave = %d, want 650/250", all.TokensSaved, all.WouldSaveTokens)
	}
	if all.Basis != "estimated_engine_o200k" {
		t.Errorf("basis = %q, want estimated_engine_o200k", all.Basis)
	}

	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	windowed := s.wrapMeasuredSince(since)
	if windowed == nil || windowed.Requests != 3 || windowed.TokensSaved != 600 {
		t.Fatalf("windowed = %+v, want 3 requests / 600 saved", windowed)
	}

	recordCompressedRow(t, s, "2026-08-10 10:02:00.000", "cut-legacy", "legacy_unspecified", 40, 10)
	if got := s.wrapMeasuredSince(time.Time{}); got == nil || got.Basis != "mixed" {
		t.Fatalf("two distinct bases must report mixed, got %+v", got)
	}
}

// TestLearnPlanCarriesWrapMeasured proves the plan build wires the block in
// and names its basis in a caveat — the HTML test below hand-constructs
// structs, so without this a dropped assignment would pass the suite.
func TestLearnPlanCarriesWrapMeasured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	s, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ts := time.Now().UTC().Add(-time.Hour).Format(storeTSLayout)
	recordCompressedRow(t, s, ts, "cut-now", "estimated_engine_o200k", 500, 200)

	plan, err := s.BuildLearnPlan(t.TempDir(), []string{"claude"}, "30d")
	if err != nil {
		t.Fatal(err)
	}
	if plan.WrapMeasured == nil {
		t.Fatal("plan must carry the wrap-measured block when the window has a cut")
	}
	if plan.WrapMeasured.TokensSaved != 300 || plan.WrapMeasured.WindowDays != 30 {
		t.Fatalf("wrap measured = %+v, want 300 saved over 30 days", plan.WrapMeasured)
	}
	found := false
	for _, c := range plan.Caveats {
		if strings.Contains(c, "basis: estimated_engine_o200k") {
			found = true
		}
	}
	if !found {
		t.Fatalf("caveats must name the wrap basis: %v", plan.Caveats)
	}
}

// TestLearnHTMLSavingsSection proves the report renders the single savings
// card — saved-so-far and could-have-saved panels — token-only, with neutral
// attribution and the retro caveats inline.
func TestLearnHTMLSavingsSection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	s, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	plan, err := s.BuildLearnPlan(t.TempDir(), []string{"claude"}, "30d")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mustRenderLearnHTML(t, s, plan, home, "plain.html"), "<h2>Savings</h2>") {
		t.Fatal("savings section must be absent without wrap or retro data")
	}

	plan.WrapMeasured = &LearnWrapMeasured{
		Basis: "estimated_engine_o200k", WindowDays: 30, Requests: 12, CompressedRequests: 9,
		TokensBefore: 40000, TokensAfter: 15000, TokensSaved: 25000, WouldSaveTokens: 3000,
	}
	plan.Retro = &LearnRetro{
		Basis: learnBasis, WindowDays: 30, SessionsTotal: 6, SessionsScanned: 5,
		TurnsObserved: 400, TokensObserved: 9000000, TokensObservedSource: retroSourceSessionUsage,
		WouldCutTokens: 700000, WouldCutStreamTokens: 2100000,
		ConfigPrefixTokensPerTurn: 3900, EngineUsed: true,
		Families: []LearnRetroFamily{
			{ID: retroFamilyToolOutputs, Label: "tool outputs (wrap compression)", Tokens: 500000},
			{ID: retroFamilyRepeatedBlocks, Label: "re-pasted context (cavemem offload)", Tokens: 200000},
		},
		Caveats: []string{"Totals are sums over the sessions actually read; nothing is extrapolated to unscanned sessions, to a month, or to a dollar."},
	}
	text := mustRenderLearnHTML(t, s, plan, home, "savings.html")
	for _, want := range []string{
		"<h2>Savings</h2>", "Saved so far", "Could have saved",
		"25k", "2.1M", "9M", "23%", "700k",
		"estimated_engine_o200k", "3,900",
		"big tool results, compressed", "remembered once in cavemem",
		"each cut counted once", "How this was measured",
		"Caveman already saved 25k tokens",
		"the fixes here could have saved 2.1M of the 9M tokens sent",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("savings report missing %q", want)
		}
	}
	// The savings story stays token-only, and the could-have-saved figure is
	// never attributed to the wrap alone — part of it needs a cavemem offload.
	section := text[strings.Index(text, "<h2>Savings</h2>"):]
	section = section[:strings.Index(section, "<h2>Cave Score</h2>")]
	if strings.Contains(section, "$") {
		t.Error("savings section carries a dollar figure")
	}
	for _, banned := range []string{"wrap would have", "wrap could have"} {
		if strings.Contains(text, banned) {
			t.Errorf("report attributes the combined replay figure to the wrap alone: %q", banned)
		}
	}
}

func mustRenderLearnHTML(t *testing.T, s *Store, plan LearnPlan, home, name string) string {
	t.Helper()
	out := filepath.Join(home, name)
	if err := s.WriteLearnHTML(plan, out); err != nil {
		t.Fatalf("write html: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
