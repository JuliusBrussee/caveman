package store

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

func TestTrialPayloadCaptureOnlyTrialLabel(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	s.RecordPayload("local", "req-local", "trace", []byte(`{"prompt":"ignore"}`))
	s.RecordPayload("trial:trial_1", "req-trial", "trace", []byte(`{"prompt":"capture"}`))

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM trial_payloads`).Scan(&count); err != nil {
		t.Fatalf("count payloads: %v", err)
	}
	if count != 1 {
		t.Fatalf("payload rows = %d, want 1", count)
	}
}

func TestUsageImportsCodexClaudeAndReportRedactsRawContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CAVEMAN_HOME", filepath.Join(home, ".caveman"))
	s, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	codexRoot := filepath.Join(home, ".codex")
	sessionsDir := filepath.Join(codexRoot, "sessions", "2026", "06")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Fixture timestamps must stay inside the "30d" import window on every run —
	// hardcoded dates rot into a time-bomb failure once the window passes them.
	recent := time.Now().UTC().AddDate(0, 0, -5).Format(time.RFC3339)
	codexLine := `{"timestamp":"` + recent + `","payload":{"model":"gpt-4o","model_provider":"openai","info":{"last_token_usage":{"input_tokens":1000,"output_tokens":120,"cached_input_tokens":10,"reasoning_output_tokens":5}},"rate_limits":{"plan_type":"pro","five_hour":{"used_percentage":82,"resets_at":"` + recent + `"}}}}`
	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-test.jsonl"), []byte(codexLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Codex moves completed sessions into archived_sessions. The same immutable
	// event appearing at both paths during that transition must count once.
	archivedDir := filepath.Join(codexRoot, "archived_sessions")
	if err := os.MkdirAll(archivedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archivedDir, "rollout-test.jsonl"), []byte(codexLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	claudeRoot := filepath.Join(home, ".claude")
	if err := os.MkdirAll(filepath.Join(claudeRoot, "transcripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	rawPrompt := "secret prompt should never appear"
	claudeLine := `{"timestamp":"` + recent + `","type":"user","content":"` + rawPrompt + `"}`
	if err := os.WriteFile(filepath.Join(claudeRoot, "transcripts", "one.jsonl"), []byte(claudeLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	codex, err := s.ImportCodex(codexRoot, "30d")
	if err != nil {
		t.Fatalf("import codex: %v", err)
	}
	if codex.EventsImported != 1 || codex.QuotaImported == 0 {
		t.Fatalf("codex summary = %+v, want one event and quota", codex)
	}
	claude, err := s.ImportClaude(claudeRoot, "30d")
	if err != nil {
		t.Fatalf("import claude: %v", err)
	}
	if claude.EventsImported != 1 || claude.Basis != "estimated" {
		t.Fatalf("claude summary = %+v, want estimated event", claude)
	}

	plan, err := s.BuildTrialPlan("")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	out := filepath.Join(home, "report.html")
	if err := s.WriteTrialHTML(plan, out); err != nil {
		t.Fatalf("html: %v", err)
	}
	html, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(html)
	for _, want := range []string{"Executive Summary", "Usage Origins", "Plan/Quota Windows", "Optimizer Trial Results", "Modeled dollar delta", "Learned Past Patterns", "Caveats"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing section %q", want)
		}
	}
	if strings.Contains(text, rawPrompt) {
		t.Fatalf("report leaked raw prompt content")
	}
	if strings.Contains(text, "sessionKey") || strings.Contains(text, "Authorization") {
		t.Fatalf("report leaked secret-like header/cookie text")
	}
	if strings.Contains(text, "safe_now moves can be promoted locally") {
		t.Fatalf("report advertises automatic local promotion without sufficient evidence")
	}
}

func TestAnalyzeTrialWithoutPayloadIsInsufficientData(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.StartTrial("trial_empty", "codex", "codex"); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AnalyzeTrial("trial_empty", filepath.Join(dir, "ccr.db"))
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	found := false
	for _, move := range plan.Moves {
		if move.OptimizerID == "context-compression" {
			found = true
			if move.Status != statusNoData {
				t.Fatalf("context-compression status = %q, want %q", move.Status, statusNoData)
			}
		}
	}
	if !found {
		t.Fatalf("context-compression move not found")
	}
	if slices.Contains(plan.Caveats, compressionReplayCaveat) {
		t.Fatalf("no-data trial falsely claims a measured replay delta: %+v", plan.Caveats)
	}
}

func TestAnalyzeTrialBoundsAggregatePayloadReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Setenv("CAVE_TRIAL_REPLAY_MAX_BYTES", "128")
	const trialID = "trial_bounded"
	if err := s.StartTrial(trialID, "codex", "codex"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s.RecordPayload("trial:"+trialID, "req-"+strconv.Itoa(i), "trace", []byte(strings.Repeat("x", 80)))
	}
	if _, err := s.AnalyzeTrial(trialID, filepath.Join(dir, "ccr.db")); err == nil || !strings.Contains(err.Error(), "payload budget") {
		t.Fatalf("AnalyzeTrial error = %v, want bounded replay failure", err)
	}
}

func TestAnalyzeTrialCompressionReplayReportsTokenShapeWithZeroDollars(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const trialID = "trial_shape"
	if err := s.StartTrial(trialID, "codex", "codex"); err != nil {
		t.Fatal(err)
	}
	record := gatewayRecord("trial:"+trialID, "openai", "gpt-4o", 1000, 10, 1)
	s.Record(record)
	row := `{"status":"healthy","region":"eu-west-1","tier":"standard"}`
	content := `{"rows":[` + strings.TrimSuffix(strings.Repeat(row+",", 40), ",") + `]}`
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"messages": []any{map[string]any{
			"role": "tool", "tool_call_id": "call-1", "content": content,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.RecordPayload("trial:"+trialID, record.RequestID, "trace-shape", body)
	plan, err := s.AnalyzeTrial(trialID, filepath.Join(dir, "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Headline.EstimatedSavingsUSD != 0 {
		t.Fatalf("local replay entered dollar headline: %+v", plan.Headline)
	}
	for _, move := range plan.Moves {
		if move.OptimizerID != "context-compression" {
			continue
		}
		if move.Status != statusNeedsEval || move.SavingsUSDBase != 0 || move.Confidence != "low" {
			t.Fatalf("compression replay posture = %+v", move)
		}
		if !strings.Contains(move.Title, "o200k tokens") || !strings.Contains(move.Title, "task outcome not evaluated") {
			t.Fatalf("compression replay title omits basis/outcome limit: %q", move.Title)
		}
		if !slices.Contains(plan.Caveats, compressionReplayCaveat) {
			t.Fatalf("compression replay caveat missing: %+v", plan.Caveats)
		}
		return
	}
	t.Fatalf("context-compression move missing: %+v", plan.Moves)
}

func TestTrialReportKeepsImportedHistoryContextual(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	s.Record(gatewayRecord("trial:trial_scoped", "openai", "gpt-4o", 100, 10, 0.01))
	if _, err := s.InsertUsageEvents([]UsageEvent{{
		SourceKind: "codex_session", EventID: "codex-1", Timestamp: "2026-06-01T00:00:00Z",
		AgentSlug: "codex", Provider: "openai", Model: "gpt-4o",
		Requests: 100, InputTokens: 999999, OutputTokens: 1, TotalCostUSD: 99, Basis: observedLocal,
	}}); err != nil {
		t.Fatal(err)
	}
	plan, err := s.BuildTrialPlan("trial_scoped")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Headline.Requests != 1 || plan.Headline.InputTokens != 100 || plan.Headline.TotalCostUSD != 0.01 {
		t.Fatalf("headline = %+v, want only proxied trial traffic", plan.Headline)
	}
	if len(plan.Origins) != 2 {
		t.Fatalf("origins = %d, want trial + imported context", len(plan.Origins))
	}
}

func TestExportTrialWritesSpansWithoutPayloads(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	rec := gatewayRecord("trial:trial_export", "openai", "gpt-4o", 100, 10, 0.01)
	rec.CachedInputTokens = 40
	rec.CacheCreationInputTokens = 20
	s.Record(rec)
	if _, err := s.InsertUsageEvents([]UsageEvent{{
		SourceKind: "codex_session", EventID: "codex-export", Timestamp: "2026-06-01T00:00:00Z",
		AgentSlug: "codex", Provider: "openai", Model: "gpt-4o",
		Requests: 1, InputTokens: 123, OutputTokens: 4, CachedInputTokens: 80, CacheCreationInputTokens: 30,
		TotalCostUSD: 0.02, Basis: observedLocal,
		MetadataJSON: `{"prompt":"do not export raw metadata"}`,
	}}); err != nil {
		t.Fatal(err)
	}
	plan, err := s.BuildTrialPlan("trial_export")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := s.ExportTrial(plan, filepath.Join(dir, "export"))
	if err != nil {
		t.Fatal(err)
	}
	spans, err := os.ReadFile(paths["spans_jsonl"])
	if err != nil {
		t.Fatal(err)
	}
	text := string(spans)
	for _, want := range []string{
		`"span_kind":"request"`, `"request_id":"req-trial:trial_export"`,
		`"cached_input_tokens":40`, `"cache_creation_input_tokens":20`,
		`"span_kind":"usage_event"`, `"event_id":"codex-export"`,
		`"cached_input_tokens":80`, `"cache_creation_input_tokens":30`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("spans missing %s in %s", want, text)
		}
	}
	if strings.Contains(text, "do not export raw metadata") {
		t.Fatalf("spans exported raw metadata")
	}
}

func TestHeuristicProviderAndCostAloneEmitNoCacheMove(t *testing.T) {
	plan := TrialPlan{Headline: TrialHeadline{Requests: 2, TotalCostUSD: 2}, Origins: []UsageOrigin{
		{Provider: "anthropic", TotalCostUSD: 1},
		{Provider: "openai", TotalCostUSD: 1},
	}}
	moves := heuristicMoves("", plan)
	for _, move := range moves {
		if move.OptimizerID == "anthropic-cache-breakpoints" || move.OptimizerID == "openai-prompt-cache-key" || move.Status == statusSafeNow {
			t.Fatalf("provider and positive cost do not prove a stable prefix or actionable cache move: %+v", move)
		}
	}
}

func TestHeuristicModelNameDoesNotReactivateRetiredRoutingMove(t *testing.T) {
	moves := heuristicMoves("", TrialPlan{Origins: []UsageOrigin{{Provider: "openai", Model: "gpt-5.5", TotalCostUSD: 10}}})
	for _, move := range moves {
		if move.OptimizerID == "model-right-sizing" {
			t.Fatalf("model name alone reactivated retired routing identity: %+v", move)
		}
	}
}

func TestLegacyUnsafeTrialRowsFailClosed(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, row := range []struct {
		id, class, status string
		dollars           float64
	}{
		{id: "model-right-sizing", class: classS3, status: statusNeedsEval, dollars: 9},
		{id: "anthropic-cache-breakpoints", class: classS1, status: statusSafeNow, dollars: 8},
		{id: "context-compression", class: classS2, status: statusNeedsEval, dollars: 7},
		{id: "unknown-future-optimizer", class: classS1, status: statusSafeNow, dollars: 6},
	} {
		if _, err := s.db.Exec(`INSERT INTO trial_results
			(trial_id, optimizer_id, title, safety_class, status, savings_usd_base, confidence, evidence_json)
			VALUES ('legacy', ?, 'legacy unsafe row', ?, ?, ?, 'low', '{}')`, row.id, row.class, row.status, row.dollars); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := s.BuildTrialPlan("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Headline.EstimatedSavingsUSD != 0 {
		t.Fatalf("legacy rows entered savings headline: %+v", plan.Headline)
	}
	for _, move := range plan.Moves {
		if move.OptimizerID == "model-right-sizing" || move.OptimizerID == "anthropic-cache-breakpoints" || move.OptimizerID == "unknown-future-optimizer" {
			t.Fatalf("legacy retired/actionable row remained visible: %+v", move)
		}
		if move.OptimizerID == "context-compression" && (move.SavingsUSDBase != 0 || move.Status != statusNoData || !strings.Contains(move.Title, "re-run trial analysis")) {
			t.Fatalf("legacy compression row was not demoted to unavailable: %+v", move)
		}
	}
	if slices.Contains(plan.Caveats, compressionReplayCaveat) {
		t.Fatalf("legacy compression row falsely claims a measured token delta: %+v", plan.Caveats)
	}
}

func TestScopedTrialMovesIgnoreImportedContext(t *testing.T) {
	plan := TrialPlan{
		Headline: TrialHeadline{Requests: 1, InputTokens: 100, TotalCostUSD: 0.01},
		Origins: []UsageOrigin{
			{SourceKind: sourceCaveProxy, Provider: "openai", Model: "gpt-4o", TotalCostUSD: 0.01},
			{SourceKind: "codex_session", Provider: "openai", Model: "gpt-5.5", TotalCostUSD: 100},
		},
	}
	moves := heuristicMoves("trial_scoped", plan)
	for _, move := range moves {
		if move.OptimizerID == "model-right-sizing" {
			t.Fatalf("scoped trial moves must ignore imported expensive context: %+v", moves)
		}
	}
}

func writeClaudeProject(t *testing.T, root, slug, file string, lines []string) {
	t.Helper()
	dir := filepath.Join(root, "projects", slug)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestClaudeImporterReadsProjectsRealUsage proves the importer reads
// ~/.claude/projects transcripts and the real usage block as observed_local (R3).
func TestClaudeImporterReadsProjectsRealUsage(t *testing.T) {
	root := t.TempDir()
	writeClaudeProject(t, root, "myrepo", "s.jsonl", []string{
		`{"type":"assistant","timestamp":"2026-06-20T12:00:00Z","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":2000,"cache_creation_input_tokens":8000,"cache_read_input_tokens":150000,"output_tokens":500}}}`,
		`{"type":"user","timestamp":"2026-06-20T12:00:30Z","content":"no usage here"}`,
	})
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	out, err := s.ImportClaude(root, "")
	if err != nil {
		t.Fatalf("import claude: %v", err)
	}
	if out.EventsImported != 1 || out.Basis != observedLocal {
		t.Fatalf("import summary = %+v, want 1 observed_local event (user line skipped)", out)
	}
	var input, cached, cacheCreation int64
	var totalCost float64
	var basis string
	if err := s.db.QueryRow(`SELECT input_tokens, cached_input_tokens, cache_creation_input_tokens, total_cost_usd, basis FROM usage_events WHERE source_kind='claude_transcript'`).Scan(&input, &cached, &cacheCreation, &totalCost, &basis); err != nil {
		t.Fatal(err)
	}
	if input != 160000 || cached != 150000 || cacheCreation != 8000 || basis != observedLocal {
		t.Fatalf("event = input %d cached %d cache_creation %d basis %s, want total input 160000 with cache subsets 150000/8000 and observed_local", input, cached, cacheCreation, basis)
	}
	if totalCost != 0 {
		t.Fatalf("cost = %v, want honest zero because local history does not prove PAYG auth/tier", totalCost)
	}
}

func TestInsertedUsageCannotSelfPromoteOrPoisonTotals(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.InsertUsageEvents([]UsageEvent{{
		SourceKind: "manual", EventID: "bad", Requests: -1, InputTokens: -2,
		OutputTokens: -3, CachedInputTokens: -4, CacheCreationInputTokens: -5, ReasoningTokens: -6,
		TotalCostUSD: math.Inf(1), Basis: "verified",
	}})
	if err != nil {
		t.Fatal(err)
	}
	var requests, input, output, cached, cacheCreation, reasoning int64
	var total float64
	var basis string
	if err := s.db.QueryRow(`SELECT requests,input_tokens,output_tokens,cached_input_tokens,cache_creation_input_tokens,reasoning_tokens,total_cost_usd,basis FROM usage_events WHERE event_id='bad'`).Scan(&requests, &input, &output, &cached, &cacheCreation, &reasoning, &total, &basis); err != nil {
		t.Fatal(err)
	}
	if requests != 0 || input != 0 || output != 0 || cached != 0 || cacheCreation != 0 || reasoning != 0 || total != 0 || basis != trialBasis {
		t.Fatalf("sanitized event = %d/%d/%d/%d/%d/%d cost=%v basis=%q", requests, input, output, cached, cacheCreation, reasoning, total, basis)
	}
}

func TestImportedOpenAIUsagePricesOverlappingDetailsOnce(t *testing.T) {
	price := cost.Price{
		InputPerMillion:     5,
		OutputPerMillion:    30,
		CacheReadPerMillion: 0.5,
		// Deliberately non-zero: OpenAI reasoning tokens are a subset of
		// output_tokens and must not be charged a second time.
		ReasoningPerMillion: 30,
	}
	got := importedUsageCost("openai", price, 1000, 120, 700, 0, 20)
	if got != 0.00545 {
		t.Fatalf("cost = %v, want 0.00545 with cached/reasoning subsets priced once", got)
	}
}

func TestImportedUsageHonorsDistinctReasoningRate(t *testing.T) {
	price := cost.Price{InputPerMillion: 5, OutputPerMillion: 10, ReasoningPerMillion: 25}
	got := importedUsageCost("openai", price, 1000, 100, 0, 0, 40)
	want := float64(1000*5+60*10+40*25) / 1_000_000
	if got != want {
		t.Fatalf("cost = %.10f, want %.10f", got, want)
	}
}

func TestImportedAnthropicUsagePricesDisjointCacheBucketsWhenBillingEvidenceExists(t *testing.T) {
	price := cost.Price{InputPerMillion: 3, OutputPerMillion: 15, CacheReadPerMillion: .3, CacheWritePerMillion: 3.75}
	got := importedUsageCost("anthropic", price, 2000, 500, 150000, 8000, 0)
	if got != 0.0885 {
		t.Fatalf("cost = %v, want 0.0885 with input/cache-write/cache-read priced once", got)
	}
}

func TestCodexCumulativeTotalWithoutBucketsIsNeverMispriced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	raw := strings.Join([]string{
		`{"timestamp":"2026-06-20T12:00:00Z","payload":{"info":{"total_token_usage":100}}}`,
		`{"timestamp":"2026-06-20T12:01:00Z","payload":{"info":{"total_token_usage":250}}}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	events, _, err := parseCodexFile(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want one cumulative delta", len(events))
	}
	if events[0].InputTokens != 150 || events[0].TotalCostUSD != 0 || events[0].Basis != "estimated" {
		t.Fatalf("event = %+v, want unpriced estimated 150-token delta", events[0])
	}
	if !strings.Contains(events[0].MetadataJSON, "unclassified_cumulative_delta") {
		t.Fatalf("metadata = %s, want unclassified token-bucket disclosure", events[0].MetadataJSON)
	}
}

func TestImportSummaryUsesWeakestTokenBasis(t *testing.T) {
	if got := aggregateImportBasis([]UsageEvent{{Basis: observedLocal}, {Basis: "estimated"}}); got != "estimated" {
		t.Fatalf("mixed import basis = %q, want estimated", got)
	}
	if got := aggregateImportBasis([]UsageEvent{{Basis: observedLocal}, {Basis: "observed_provider"}}); got != observedLocal {
		t.Fatalf("fully observed import basis = %q, want observed_local", got)
	}
}

func TestCodexBehaviorDoesNotDoubleCountCachedInputSubset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	raw := `{"timestamp":"2026-06-20T12:00:00Z","payload":{"model":"gpt-5.5","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":80}}}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	beh := behaviorScan{}
	scanCodexSessionBehavior(path, time.Time{}, &beh)
	if len(beh.Contexts) != 1 || beh.Contexts[0] != 100 {
		t.Fatalf("contexts = %v, want provider total 100 (cached 80 is a subset)", beh.Contexts)
	}
}

// TestLearnConfigSinksAndCavemem proves config-tax + CLAUDE.md-weight detection and
// that reducible sinks become stored cavemem learnings.
func TestLearnConfigSinksAndCavemem(t *testing.T) {
	home := t.TempDir()
	claudeDir := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeDir)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	bigMD := strings.Repeat("- a guideline line that adds tokens to every single turn\n", 220)
	if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte(bigMD), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	plan, err := s.LearnScan([]string{"claude"}, "30d")
	if err != nil {
		t.Fatalf("learn scan: %v", err)
	}
	if !hasSink(plan, "config_tax:baseline") || !hasSink(plan, "claude_md_weight:user") {
		t.Fatalf("sinks = %s, want config_tax + claude_md_weight", sinkIDs(plan))
	}
	for _, sink := range plan.Sinks {
		if sink.SinkID == "claude_md_weight:user" {
			if sink.Class != classReducible || sink.Framing != framingForward {
				t.Fatalf("claude_md sink = %+v, want reducible/forward", sink)
			}
		}
	}
	var stored int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM learnings WHERE stored_in_cavemem=1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == 0 {
		t.Fatalf("stored cavemem learnings = 0, want at least one from a reducible sink")
	}
}

// TestLearnBehaviorDumbzoneAndSubagent proves real-transcript behavioral detection.
func TestLearnBehaviorDumbzoneAndSubagent(t *testing.T) {
	home := t.TempDir()
	claudeDir := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeDir)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	writeClaudeProject(t, claudeDir, "repo", "s.jsonl", []string{
		`{"type":"assistant","timestamp":"2026-06-20T12:00:00Z","message":{"model":"claude-opus-4","usage":{"input_tokens":2000,"cache_creation_input_tokens":8000,"cache_read_input_tokens":150000,"output_tokens":500}}}`,
		`{"type":"assistant","timestamp":"2026-06-20T12:00:30Z","message":{"model":"claude-opus-4","usage":{"input_tokens":1000,"cache_read_input_tokens":2000,"output_tokens":100}}}`,
		`{"type":"assistant","timestamp":"2026-06-20T12:01:00Z","message":{"model":"claude-opus-4","content":[{"type":"tool_use","name":"Task","input":{}}],"usage":{"input_tokens":100,"output_tokens":50}}}`,
	})
	s, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	plan, err := s.BuildLearnPlan(t.TempDir(), []string{"claude"}, "")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if !hasSink(plan, "context_dumbzone") || !hasSink(plan, "subagent_overuse") {
		t.Fatalf("sinks = %s, want dumbzone + subagent", sinkIDs(plan))
	}
	if plan.SessionsScanned != 1 || plan.SessionsBySource["claude"] != 1 {
		t.Fatalf("session scope = %d %+v, want 1 claude", plan.SessionsScanned, plan.SessionsBySource)
	}
	if plan.CaveScore.Scope != "local_setup" {
		t.Fatalf("score scope = %q, want local_setup", plan.CaveScore.Scope)
	}
	dz := componentByKey(plan.CaveScore, scoreKeyDumbzone)
	if !dz.Measured {
		t.Fatalf("dumbzone component not measured: %+v", dz)
	}
}

// TestLearnHonestyAndScoreTransparency proves Class B softening, score transparency,
// inferred basis, and no currency on the local path.
func TestLearnHonestyAndScoreTransparency(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	s, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	plan, err := s.BuildLearnPlan(t.TempDir(), []string{"claude", "codex"}, "30d")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if plan.CaveScore.Basis != learnBasis || plan.Basis != learnBasis {
		t.Fatalf("basis = %s/%s, want inferred", plan.Basis, plan.CaveScore.Basis)
	}
	if len(plan.CaveScore.Components) != 4 {
		t.Fatalf("components = %d, want 4 always-present", len(plan.CaveScore.Components))
	}
	// With no transcripts and no config, every component is unmeasured and the score is full.
	for _, c := range plan.CaveScore.Components {
		if c.Measured || c.Penalty != 0 {
			t.Fatalf("component %s measured/penalized with no data: %+v", c.Key, c)
		}
	}
	if plan.CaveScore.Score != 100 {
		t.Fatalf("score = %d with no penalties, want 100", plan.CaveScore.Score)
	}
	raw, _ := json.Marshal(plan)
	text := string(raw)
	for _, banned := range []string{"you don't need", "you over-use", "you overuse", "$"} {
		if strings.Contains(text, banned) {
			t.Fatalf("learn output contains banned token %q (honesty/imperative violation)", banned)
		}
	}
}

// TestLearnIsReadOnly proves the analyzer never edits user config.
func TestLearnIsReadOnly(t *testing.T) {
	home := t.TempDir()
	claudeDir := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeDir)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	mdPath := filepath.Join(claudeDir, "CLAUDE.md")
	original := strings.Repeat("- guideline\n", 200)
	if err := os.WriteFile(mdPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	before := dirFingerprint(t, claudeDir)
	s, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if _, err := s.BuildLearnPlan(t.TempDir(), []string{"claude"}, "30d"); err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if got := dirFingerprint(t, claudeDir); got != before {
		t.Fatalf("learn modified the user config dir (read-only violation)")
	}
	if data, _ := os.ReadFile(mdPath); string(data) != original {
		t.Fatalf("learn modified CLAUDE.md")
	}
}

// TestLearnHTMLReport proves the static offline report shape.
func TestLearnHTMLReport(t *testing.T) {
	home := t.TempDir()
	claudeDir := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeDir)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte(strings.Repeat("- line\n", 220)), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	plan, err := s.BuildLearnPlan(t.TempDir(), []string{"claude"}, "30d")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(home, "learn.html")
	if err := s.WriteLearnHTML(plan, out); err != nil {
		t.Fatalf("write html: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"TLDR", "Cave score", "Cave Score", "Token Sinks", "Caveats"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing section %q", want)
		}
	}
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		t.Fatalf("report references remote assets")
	}
}

// claudeUserTurn builds a Claude transcript user turn carrying one text block.
func claudeUserTurn(t *testing.T, ts, text string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type":      "user",
		"timestamp": ts,
		"message":   map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

const claudeAssistantUsageTurn = `{"type":"assistant","timestamp":"2026-06-20T12:00:00Z","message":{"model":"claude-opus-4","usage":{"input_tokens":2000,"cache_creation_input_tokens":8000,"cache_read_input_tokens":150000,"output_tokens":500}}}`

func recurringBlock(extra string) string {
	return "PROJECT CONTEXT (restated each session): " + extra + "\n" +
		strings.Repeat("- the billing service owns gainshare math and must stay byte-exact\n", 16)
}

func sinkByPrefix(plan LearnPlan, prefix string) (Sink, bool) {
	for _, s := range plan.Sinks {
		if strings.HasPrefix(s.SinkID, prefix) {
			return s, true
		}
	}
	return Sink{}, false
}

// TestLearnRecurringContextSink proves cross-session re-paste mining emits a
// recurring_context sink whose fix is cavemem_offload, and that the recurring
// share folds into the config_tax score component (still 4 components).
func TestLearnRecurringContextSink(t *testing.T) {
	home := t.TempDir()
	claudeDir := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeDir)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	block := recurringBlock("auth tokens rotate hourly")
	for _, f := range []string{"s1.jsonl", "s2.jsonl", "s3.jsonl"} {
		writeClaudeProject(t, claudeDir, "repo", f, []string{
			claudeAssistantUsageTurn,
			claudeUserTurn(t, "2026-06-20T12:00:30Z", block),
		})
	}
	s, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	plan, err := s.BuildLearnPlan(t.TempDir(), []string{"claude"}, "")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	sink, ok := sinkByPrefix(plan, "recurring_context:repaste:")
	if !ok {
		t.Fatalf("no recurring_context sink, sinks = %s", sinkIDs(plan))
	}
	if sink.Class != classRecurringContext || sink.Framing != framingForward || sink.TokensPerTurn <= 0 {
		t.Fatalf("recurring sink = %+v, want recurring_context/forward/positive tokens", sink)
	}
	if got, _ := sink.Evidence["recurrence_sessions"].(int); got != 3 {
		t.Fatalf("recurrence_sessions = %v, want 3", sink.Evidence["recurrence_sessions"])
	}
	if got, _ := sink.Evidence["fix_kind"].(string); got != "cavemem_offload" {
		t.Fatalf("fix_kind = %v, want cavemem_offload", sink.Evidence["fix_kind"])
	}
	if len(plan.CaveScore.Components) != 4 {
		t.Fatalf("components = %d, want 4 (recurring folds into config_tax)", len(plan.CaveScore.Components))
	}
	cTax := componentByKey(plan.CaveScore, scoreKeyConfigTax)
	if !cTax.Measured || !strings.Contains(cTax.Detail, "recurring re-paste") {
		t.Fatalf("config_tax component must disclose recurring re-paste: %+v", cTax)
	}
}

// TestLearnRecurringEvidenceNoRawBody proves the analyzer stores fingerprints and
// locators, never the raw block body (extends the no-raw-prompts rule).
func TestLearnRecurringEvidenceNoRawBody(t *testing.T) {
	home := t.TempDir()
	claudeDir := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeDir)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	const sentinel = "SENTINELPASTE9999"
	block := recurringBlock(sentinel)
	for _, f := range []string{"s1.jsonl", "s2.jsonl", "s3.jsonl"} {
		writeClaudeProject(t, claudeDir, "repo", f, []string{claudeUserTurn(t, "2026-06-20T12:00:30Z", block)})
	}
	s, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	plan, err := s.BuildLearnPlan(t.TempDir(), []string{"claude"}, "")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	raw, _ := json.Marshal(plan)
	if strings.Contains(string(raw), sentinel) {
		t.Fatalf("plan JSON leaked the raw block body")
	}
	var evidence string
	if err := s.db.QueryRow(`SELECT evidence_json FROM learn_sinks WHERE sink_id LIKE 'recurring_context:repaste:%'`).Scan(&evidence); err != nil {
		t.Fatalf("query sink evidence: %v", err)
	}
	if strings.Contains(evidence, sentinel) {
		t.Fatalf("learn_sinks evidence leaked the raw block body")
	}
	for _, want := range []string{"fingerprint", "content_sha256", "segmenter"} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("evidence missing %q: %s", want, evidence)
		}
	}
}

// TestLearnRecurringBelowThreshold proves fail-closed: a heavy block in one
// session and a tiny block in three sessions produce no recurring sink.
func TestLearnRecurringBelowThreshold(t *testing.T) {
	home := t.TempDir()
	claudeDir := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeDir)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	writeClaudeProject(t, claudeDir, "repo", "heavy.jsonl", []string{
		claudeUserTurn(t, "2026-06-20T12:00:30Z", recurringBlock("only here once")),
	})
	for _, f := range []string{"t1.jsonl", "t2.jsonl", "t3.jsonl"} {
		writeClaudeProject(t, claudeDir, "repo", f, []string{
			claudeUserTurn(t, "2026-06-20T12:00:30Z", "quick note: fix the typo in the header"),
		})
	}
	s, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	plan, err := s.BuildLearnPlan(t.TempDir(), []string{"claude"}, "")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if _, ok := sinkByPrefix(plan, "recurring_context:repaste:"); ok {
		t.Fatalf("recurring sink emitted below threshold, sinks = %s", sinkIDs(plan))
	}
}

func hasSink(plan LearnPlan, id string) bool {
	for _, s := range plan.Sinks {
		if s.SinkID == id {
			return true
		}
	}
	return false
}

func sinkIDs(plan LearnPlan) string {
	ids := make([]string, 0, len(plan.Sinks))
	for _, s := range plan.Sinks {
		ids = append(ids, s.SinkID)
	}
	return strings.Join(ids, ",")
}

func componentByKey(score CaveScore, key string) ScoreComponent {
	for _, c := range score.Components {
		if c.Key == key {
			return c
		}
	}
	return ScoreComponent{}
}

func dirFingerprint(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b.WriteString(path)
		b.WriteString(":")
		b.WriteString(info.ModTime().String())
		b.WriteString(":")
		b.WriteString(fmtInt(info.Size()))
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func fmtInt(v int64) string { return strconv.FormatInt(v, 10) }

func gatewayRecord(label, provider, model string, input, output int, cost float64) gateway.RequestRecord {
	return gateway.RequestRecord{
		Timestamp: "2026-06-16 00:00:00.000", RequestID: "req-" + label, Label: label,
		Provider: provider, Model: model, Endpoint: "/v1/chat/completions",
		StatusCode: 200, InputTokens: input, OutputTokens: output,
		TotalCostUSD: cost, Basis: "inferred", RuntimeMode: "record",
		TokenUsageBasis: "provider_complete", AuthMode: "payg",
	}
}
