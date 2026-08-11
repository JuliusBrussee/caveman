package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// --- fixtures ---------------------------------------------------------------

// retroLogPayload builds a repetitive tool-result body the engine can actually
// reduce; `tag` keeps each payload a distinct segment for dedupe assertions.
func retroLogPayload(tag string) string {
	var b strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "2026-07-28T12:00:%02dZ INFO %s worker=%d processed batch ok\n", i%60, tag, i)
	}
	return b.String()
}

// retroRepeatedBlock is one paragraph heavy enough to clear minBlockTokens.
func retroRepeatedBlock() string {
	return strings.TrimSpace(strings.Repeat("The deploy runbook requires draining the queue before the migration runs. ", 20))
}

type retroFixture struct {
	claudeRoot string
	codexRoot  string
	cwd        string
}

// writeRetroClaudeSessions lays down `sessions` transcripts, each with
// `usageTurns` provider-counted assistant turns, one unique tool result, and
// `repeats` occurrences of the shared recurring block.
func writeRetroClaudeSessions(t *testing.T, root string, sessions, usageTurns, repeats int, sharedPayload string) {
	t.Helper()
	for s := 0; s < sessions; s++ {
		var lines []string
		for turn := 0; turn < usageTurns; turn++ {
			lines = append(lines, fmt.Sprintf(
				`{"type":"assistant","timestamp":"2026-07-28T12:0%d:0%dZ","message":{"model":"claude","usage":{"input_tokens":1000,"cache_read_input_tokens":500,"cache_creation_input_tokens":100}}}`,
				s, turn))
		}
		payload := sharedPayload
		if payload == "" {
			payload = retroLogPayload(fmt.Sprintf("session-%d", s))
		}
		result, err := json.Marshal(map[string]any{
			"type": "user",
			"message": map[string]any{"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": payload},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(result))
		for r := 0; r < repeats; r++ {
			block, err := json.Marshal(map[string]any{
				"type":    "user",
				"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": retroRepeatedBlock()}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, string(block))
		}
		path := filepath.Join(root, "projects", "repo", fmt.Sprintf("session-%d.jsonl", s))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// writeRetroUsageFreeSessions lays down transcripts that carry the recurring
// block but no provider usage at all — the shape that must contribute to no
// total whatsoever.
func writeRetroUsageFreeSessions(t *testing.T, root string, sessions, repeats int) {
	t.Helper()
	for s := 0; s < sessions; s++ {
		var lines []string
		for r := 0; r < repeats; r++ {
			block, err := json.Marshal(map[string]any{
				"type":    "user",
				"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": retroRepeatedBlock()}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, string(block))
		}
		path := filepath.Join(root, "projects", "repo", fmt.Sprintf("usage-free-%d.jsonl", s))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func newRetroFixture(t *testing.T) retroFixture {
	t.Helper()
	fx := retroFixture{claudeRoot: t.TempDir(), codexRoot: t.TempDir(), cwd: t.TempDir()}
	t.Setenv("CAVEMAN_HOME", t.TempDir())
	t.Setenv("CAVEMAN_CLAUDE_ROOT", fx.claudeRoot)
	t.Setenv("CAVEMAN_CODEX_ROOT", fx.codexRoot)
	return fx
}

func openRetroTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// retroExpectedCut measures the same segments the retro pass would, through the
// same engine call, so the assertion is an oracle rather than a magic number.
func retroExpectedCut(t *testing.T, texts ...string) int64 {
	t.Helper()
	recovery, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Close()
	eng := engine.New(recovery, nil)
	var total int64
	for _, text := range texts {
		sim := eng.Simulate([]byte(text), engine.Options{Mode: engine.ModeCompress})
		if sim.Recoverable && sim.TokensSaved > 0 {
			total += int64(sim.TokensSaved)
		}
	}
	return total
}

func familyTokens(retro *LearnRetro, id string) (int64, bool) {
	for _, f := range retro.Families {
		if f.ID == id {
			return f.Tokens, true
		}
	}
	return 0, false
}

// --- tests ------------------------------------------------------------------

func TestLearnRetroEmitsMeasuredBlock(t *testing.T) {
	fx := newRetroFixture(t)
	writeRetroClaudeSessions(t, fx.claudeRoot, 3, 2, 2, "")
	s := openRetroTestStore(t)

	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude", "codex"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	retro := plan.Retro
	if retro == nil {
		t.Fatal("retro block must be emitted when provider-counted sessions were scanned")
	}
	if retro.Basis != learnBasis {
		t.Fatalf("basis = %q, want %q", retro.Basis, learnBasis)
	}
	if retro.WindowDays != 30 {
		t.Fatalf("window_days = %d, want 30", retro.WindowDays)
	}
	if retro.SessionsTotal != 3 || retro.SessionsScanned != 3 {
		t.Fatalf("sessions total/scanned = %d/%d, want 3/3", retro.SessionsTotal, retro.SessionsScanned)
	}
	if retro.TurnsObserved != 6 {
		t.Fatalf("turns_observed = %d, want 6", retro.TurnsObserved)
	}
	// 6 turns x (1000 input + 500 cache read + 100 cache creation).
	if retro.TokensObserved != 9600 {
		t.Fatalf("tokens_observed = %d, want 9600", retro.TokensObserved)
	}
	if retro.TokensObservedSource != retroSourceSessionUsage {
		t.Fatalf("tokens_observed_source = %q", retro.TokensObservedSource)
	}
	if !retro.EngineUsed || retro.TimeBoxed {
		t.Fatalf("engine_used=%v time_boxed=%v, want true/false", retro.EngineUsed, retro.TimeBoxed)
	}

	wantTools := retroExpectedCut(t,
		retroLogPayload("session-0"), retroLogPayload("session-1"), retroLogPayload("session-2"))
	gotTools, ok := familyTokens(retro, retroFamilyToolOutputs)
	if !ok || gotTools != wantTools {
		t.Fatalf("tool_outputs = %d (present=%v), want %d", gotTools, ok, wantTools)
	}
	// 3 sessions x 2 occurrences = 6; the first occurrence is never claimed.
	wantRepeat := int64(estimateTokens(retroRepeatedBlock())) * 5
	gotRepeat, ok := familyTokens(retro, retroFamilyRepeatedBlocks)
	if !ok || gotRepeat != wantRepeat {
		t.Fatalf("repeated_blocks = %d (present=%v), want %d", gotRepeat, ok, wantRepeat)
	}
	if retro.WouldCutTokens != wantTools+wantRepeat {
		t.Fatalf("would_cut_tokens = %d, want %d", retro.WouldCutTokens, wantTools+wantRepeat)
	}
	if retro.WouldCutTokens >= retro.TokensObserved {
		t.Fatalf("would_cut %d must stay below observed %d", retro.WouldCutTokens, retro.TokensObserved)
	}
	// Config prefix is a rate and must never be folded into the cut.
	if retro.ConfigPrefixTokensPerTurn != 0 {
		t.Fatalf("config_prefix_tokens_per_turn = %d, want 0 for a bare fixture", retro.ConfigPrefixTokensPerTurn)
	}
	if len(retro.Caveats) == 0 {
		t.Fatal("retro must carry caveats")
	}
}

func TestLearnRetroCountsEachSegmentOnce(t *testing.T) {
	fx := newRetroFixture(t)
	shared := retroLogPayload("shared")
	// The same tool output in all 3 sessions: re-sends are provider-cache
	// dependent, so the scan claims exactly one measurement.
	writeRetroClaudeSessions(t, fx.claudeRoot, 3, 1, 0, shared)
	s := openRetroTestStore(t)

	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Retro == nil {
		t.Fatal("retro block missing")
	}
	want := retroExpectedCut(t, shared)
	if want <= 0 {
		t.Fatal("fixture must be compressible for this assertion to mean anything")
	}
	got, ok := familyTokens(plan.Retro, retroFamilyToolOutputs)
	if !ok || got != want {
		t.Fatalf("tool_outputs = %d (present=%v), want a single measurement of %d", got, ok, want)
	}
}

func TestLearnRetroExcludesUsageFreeSessionsFromEveryFamily(t *testing.T) {
	fx := newRetroFixture(t)
	// Three usage-free sessions share the recurring block — enough to clear the
	// miner's recurrence threshold on their own. Their tokens are in no total,
	// so their blocks must be in no cut either.
	writeRetroUsageFreeSessions(t, fx.claudeRoot, 3, 4)
	// One usable session: provider-counted turns, and a tool result under the
	// segment floor so no family can be credited from it.
	writeRetroClaudeSessions(t, fx.claudeRoot, 1, 3, 0, "tiny")
	s := openRetroTestStore(t)

	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	retro := plan.Retro
	if retro == nil {
		t.Fatal("the one usable session still reports")
	}
	if retro.SessionsTotal != 4 || retro.SessionsScanned != 1 {
		t.Fatalf("sessions total/scanned = %d/%d, want 4/1", retro.SessionsTotal, retro.SessionsScanned)
	}
	if retro.TokensObserved != 4800 {
		t.Fatalf("tokens_observed = %d, want only the usable session's 3 turns (4800)", retro.TokensObserved)
	}
	if tokens, ok := familyTokens(retro, retroFamilyRepeatedBlocks); ok {
		t.Fatalf("repeated_blocks = %d must not be mined from sessions excluded from tokens_observed", tokens)
	}
	if tokens, ok := familyTokens(retro, retroFamilyToolOutputs); ok {
		t.Fatalf("tool_outputs = %d, want no family from a sub-floor payload", tokens)
	}
	if retro.WouldCutTokens != 0 {
		t.Fatalf("would_cut_tokens = %d, want 0", retro.WouldCutTokens)
	}
	if retro.WouldCutTokens > retro.TokensObserved {
		t.Fatalf("would_cut %d must never exceed observed %d", retro.WouldCutTokens, retro.TokensObserved)
	}
	found := false
	for _, c := range retro.Caveats {
		if strings.Contains(c, "without provider-counted usage") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the excluded sessions must be named in the caveats: %v", retro.Caveats)
	}
}

func TestLearnRetroFamilyLabelsNameTheirFix(t *testing.T) {
	fx := newRetroFixture(t)
	writeRetroClaudeSessions(t, fx.claudeRoot, 3, 2, 2, "")
	s := openRetroTestStore(t)

	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Retro == nil {
		t.Fatal("retro block missing")
	}
	want := map[string]string{
		retroFamilyToolOutputs:    "tool outputs (wrap compression)",
		retroFamilyRepeatedBlocks: "re-pasted context (cavemem offload)",
	}
	seen := map[string]bool{}
	for _, family := range plan.Retro.Families {
		if family.Label != want[family.ID] {
			t.Fatalf("family %q label = %q, want %q", family.ID, family.Label, want[family.ID])
		}
		seen[family.ID] = true
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("family %q missing from the fixture's retro block", id)
		}
	}
}

// writeRetroTranscript writes one transcript from raw JSONL lines.
func writeRetroTranscript(t *testing.T, root, name string, lines []string) {
	t.Helper()
	path := filepath.Join(root, "projects", "repo", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func retroUsageLine(messageID string) string {
	id := ""
	if messageID != "" {
		id = fmt.Sprintf(`"id":%q,`, messageID)
	}
	return fmt.Sprintf(`{"type":"assistant","message":{%s"model":"claude","usage":{"input_tokens":1000,"cache_read_input_tokens":500,"cache_creation_input_tokens":100}}}`, id)
}

// retroUsageLineSized is retroUsageLine with an explicit provider-counted size,
// for exercising the residency cap.
func retroUsageLineSized(messageID string, inputTokens int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"model":"claude","usage":{"input_tokens":%d}}}`, messageID, inputTokens)
}

func retroToolResultLine(t *testing.T, payload string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": payload},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

func retroTextLine(t *testing.T, timestamp, text string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "user", "timestamp": timestamp,
		"message": map[string]any{"content": []any{
			map[string]any{"type": "text", "text": text},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

// Claude Code writes one API response as several JSONL lines (one per content
// block), each repeating the same message.usage. The scan must count the
// response once, not once per line.
func TestLearnRetroDedupsUsageByMessageID(t *testing.T) {
	fx := newRetroFixture(t)
	writeRetroTranscript(t, fx.claudeRoot, "session-0.jsonl", []string{
		retroUsageLine("msg_a"),
		retroUsageLine("msg_a"),
		retroUsageLine("msg_a"),
		retroUsageLine("msg_b"),
	})
	s := openRetroTestStore(t)

	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	retro := plan.Retro
	if retro == nil {
		t.Fatal("retro block missing")
	}
	if retro.TurnsObserved != 2 {
		t.Fatalf("turns_observed = %d, want 2 deduplicated API responses", retro.TurnsObserved)
	}
	if retro.TokensObserved != 3200 {
		t.Fatalf("tokens_observed = %d, want 2 x 1600, not once per line", retro.TokensObserved)
	}
}

// The base behavior scan (dumbzone, turn counts) must apply the same
// once-per-API-response rule as the retro pass.
func TestBehaviorScanDedupsUsageByMessageID(t *testing.T) {
	fx := newRetroFixture(t)
	writeRetroTranscript(t, fx.claudeRoot, "session-0.jsonl", []string{
		retroUsageLine("msg_a"),
		retroUsageLine("msg_a"),
		retroUsageLine("msg_b"),
	})
	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	path := filepath.Join(fx.claudeRoot, "projects", "repo", "session-0.jsonl")
	scanClaudeTranscriptBehavior(path, "repo/session-0.jsonl", time.Time{}, nil, &beh, newRecurringMiner())
	if beh.Turns != 2 {
		t.Fatalf("behavior turns = %d, want 2 deduplicated API responses", beh.Turns)
	}
	if len(beh.Contexts) != 2 {
		t.Fatalf("contexts recorded = %d, want 2", len(beh.Contexts))
	}
}

// The stream figure weights each cut by the provider-counted turns that
// contained the block, and a compact boundary stops the weighting because
// compaction drops the block from context.
func TestLearnRetroStreamWeightsCutsByObservedTurns(t *testing.T) {
	fx := newRetroFixture(t)
	payload := retroLogPayload("stream")
	writeRetroTranscript(t, fx.claudeRoot, "session-0.jsonl", []string{
		retroUsageLine("msg_0"), // flips usable before the tool result
		retroToolResultLine(t, payload),
		retroUsageLine("msg_1"),
		retroUsageLine("msg_1"), // duplicate line of the same response: not a turn
		retroUsageLine("msg_2"),
		retroUsageLine("msg_3"),
		`{"type":"system","subtype":"compact_boundary","content":"compacted"}`,
		retroUsageLine("msg_4"), // after compaction: block gone, never weighted
	})
	s := openRetroTestStore(t)

	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	retro := plan.Retro
	if retro == nil {
		t.Fatal("retro block missing")
	}
	cut := retroExpectedCut(t, payload)
	if cut <= 0 {
		t.Fatal("fixture must be compressible for this assertion to mean anything")
	}
	if got, _ := familyTokens(retro, retroFamilyToolOutputs); got != cut {
		t.Fatalf("tool_outputs = %d, want the unique cut %d", got, cut)
	}
	// msg_1, msg_2, msg_3 contained the block; msg_0 predates it and msg_4 is
	// past the compaction.
	if want := cut * 3; retro.WouldCutStreamTokens != want {
		t.Fatalf("would_cut_stream_tokens = %d, want cut x 3 observed turns (%d)", retro.WouldCutStreamTokens, want)
	}
}

func TestLearnRetroStreamWeightsTimestampedRepeatedBlocks(t *testing.T) {
	fx := newRetroFixture(t)
	block := retroRepeatedBlock()
	blockTokens := int64(estimateTokens(block))
	writeRetroTranscript(t, fx.claudeRoot, "session-0.jsonl", []string{
		retroUsageLine("msg_0a"),
		retroTextLine(t, "2026-07-28T12:00:00Z", block), // first occurrence: necessary, excluded
		retroUsageLine("msg_0b"),
		retroUsageLine("msg_0c"),
	})
	writeRetroTranscript(t, fx.claudeRoot, "session-1.jsonl", []string{
		retroUsageLine("msg_1a"),
		retroTextLine(t, "2026-07-28T12:10:00Z", block),
		retroUsageLine("msg_1b"), // one observed turn carries this re-paste
		`{"type":"system","subtype":"compact_boundary","content":"compacted"}`,
		retroUsageLine("msg_1c"), // compacted: no weight
	})
	writeRetroTranscript(t, fx.claudeRoot, "session-2.jsonl", []string{
		retroUsageLine("msg_2a"),
		retroTextLine(t, "2026-07-28T12:20:00Z", block),
		retroUsageLine("msg_2b"),
		retroUsageLine("msg_2c"), // two observed turns carry this re-paste
	})
	s := openRetroTestStore(t)

	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	retro := plan.Retro
	if retro == nil {
		t.Fatal("retro block missing")
	}
	if got, ok := familyTokens(retro, retroFamilyRepeatedBlocks); !ok || got != 2*blockTokens {
		t.Fatalf("repeated unique cut = %d present=%v, want two re-pastes (%d)", got, ok, 2*blockTokens)
	}
	if want := 3 * blockTokens; retro.WouldCutStreamTokens != want {
		t.Fatalf("repeated stream cut = %d, want 1+2 observed later turns (%d)", retro.WouldCutStreamTokens, want)
	}
}

func TestLearnRetroStreamPreservesFractionalOccurrenceOrderAcrossFiles(t *testing.T) {
	fx := newRetroFixture(t)
	block := retroRepeatedBlock()
	blockTokens := int64(estimateTokens(block))
	// Alphabetical path order puts session-a before session-z. Timestamp order
	// says the opposite; collapsing both fractions to one second would credit
	// the actual first occurrence in session-z for all three later turns.
	writeRetroTranscript(t, fx.claudeRoot, "session-a.jsonl", []string{
		retroUsageLine("msg_a0"),
		retroTextLine(t, "2026-07-28T12:00:00.900Z", block),
		retroUsageLine("msg_a1"),
	})
	writeRetroTranscript(t, fx.claudeRoot, "session-z.jsonl", []string{
		retroUsageLine("msg_z0"),
		retroTextLine(t, "2026-07-28T12:00:00.100Z", block),
		retroUsageLine("msg_z1"),
		retroUsageLine("msg_z2"),
		retroUsageLine("msg_z3"),
	})
	writeRetroTranscript(t, fx.claudeRoot, "session-m.jsonl", []string{
		retroUsageLine("msg_m0"),
		retroTextLine(t, "2026-07-28T12:00:01Z", block),
		retroUsageLine("msg_m1"),
		retroUsageLine("msg_m2"),
	})

	s := openRetroTestStore(t)
	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Retro == nil {
		t.Fatal("retro block missing")
	}
	if want := 3 * blockTokens; plan.Retro.WouldCutStreamTokens != want {
		t.Fatalf("fractional stream cut = %d, want later .900 turn + later-file turns (%d)", plan.Retro.WouldCutStreamTokens, want)
	}
}

func TestLearnRetroStreamExcludesEarliestExactTimestampTie(t *testing.T) {
	fx := newRetroFixture(t)
	block := retroRepeatedBlock()
	blockTokens := int64(estimateTokens(block))
	for _, session := range []struct {
		name  string
		stamp string
		turns int
	}{
		{name: "session-a.jsonl", stamp: "2026-07-28T12:00:00.123456789Z", turns: 3},
		{name: "session-z.jsonl", stamp: "2026-07-28T12:00:00.123456789Z", turns: 2},
		{name: "session-m.jsonl", stamp: "2026-07-28T12:00:01Z", turns: 1},
	} {
		lines := []string{retroUsageLine("before-" + session.name), retroTextLine(t, session.stamp, block)}
		for i := 0; i < session.turns; i++ {
			lines = append(lines, retroUsageLine(fmt.Sprintf("after-%s-%d", session.name, i)))
		}
		writeRetroTranscript(t, fx.claudeRoot, session.name, lines)
	}

	s := openRetroTestStore(t)
	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Retro == nil {
		t.Fatal("retro block missing")
	}
	if plan.Retro.WouldCutStreamTokens != blockTokens {
		t.Fatalf("exact-tie stream cut = %d, want only unambiguously later occurrence %d", plan.Retro.WouldCutStreamTokens, blockTokens)
	}
}

func TestLearnRetroRepeatedSizeSkewUsesOccurrenceSizes(t *testing.T) {
	fx := newRetroFixture(t)
	short := strings.TrimSpace(strings.Repeat("policy context remains stable ", 30))
	huge := strings.ReplaceAll(short, " ", strings.Repeat(" ", 400))
	if normalizeBlock(short) != normalizeBlock(huge) {
		t.Fatal("fixture variants must share one normalized fingerprint")
	}
	shortTokens := int64(estimateTokens(short))
	if shortTokens < minBlockTokens {
		t.Fatalf("short fixture = %d tokens, want >= %d", shortTokens, minBlockTokens)
	}
	hugeTokens := int64(estimateTokens(huge))
	if hugeTokens <= 10*shortTokens {
		t.Fatalf("huge fixture = %d tokens, want > 10x short %d", hugeTokens, shortTokens)
	}

	writeRetroTranscript(t, fx.claudeRoot, "session-0.jsonl", []string{
		retroUsageLineSized("msg_0a", 20000),
		retroTextLine(t, "2026-07-28T12:00:00Z", huge),
		retroUsageLineSized("msg_0b", 20000),
	})
	for i, stamp := range []string{"2026-07-28T12:01:00Z", "2026-07-28T12:02:00Z"} {
		writeRetroTranscript(t, fx.claudeRoot, fmt.Sprintf("session-%d.jsonl", i+1), []string{
			retroUsageLineSized(fmt.Sprintf("msg_%da", i+1), 20000),
			retroTextLine(t, stamp, short),
			retroUsageLineSized(fmt.Sprintf("msg_%db", i+1), 20000),
		})
	}

	s := openRetroTestStore(t)
	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Retro == nil {
		t.Fatal("retro block missing")
	}
	want := 2 * shortTokens
	if got, ok := familyTokens(plan.Retro, retroFamilyRepeatedBlocks); !ok || got != want {
		t.Fatalf("size-skew base cut = %d present=%v, want sum minus largest %d", got, ok, want)
	}
	if plan.Retro.WouldCutStreamTokens != want {
		t.Fatalf("size-skew stream cut = %d, want actual later occurrence sizes %d", plan.Retro.WouldCutStreamTokens, want)
	}
	sink, ok := sinkByPrefix(plan, "recurring_context:repaste:")
	if !ok {
		t.Fatal("recurring sink missing")
	}
	wantPerTurn := (hugeTokens + 2*shortTokens + int64(plan.Retro.TurnsObserved) - 1) / int64(plan.Retro.TurnsObserved)
	if sink.TokensPerTurn != wantPerTurn {
		t.Fatalf("size-skew recurring rate = %d, want actual occurrence total / turns %d", sink.TokensPerTurn, wantPerTurn)
	}
}

func TestRecurringPositionsAreRetroOnly(t *testing.T) {
	block := strings.Repeat("bounded recurring context ", minBlockTokens)
	turn := map[string]any{"message": map[string]any{"content": block}}

	base := newRecurringMiner()
	stream := newRecurringStreamMiner()
	for i := 0; i < minRecurrenceSessions; i++ {
		rel := fmt.Sprintf("session-%d.jsonl", i)
		base.observeTurn("claude", rel, 1, "2026-08-01T00:00:00Z", turn)
		stream.observeTurn("claude", rel, 1, "2026-08-01T00:00:00Z", turn)
	}

	baseResult := base.result()
	streamResult := stream.result()
	if len(baseResult.Repaste) != 1 || len(baseResult.Repaste[0].Positions) != 0 {
		t.Fatalf("base positions = %+v, want none", baseResult.Repaste)
	}
	if len(streamResult.Repaste) != 1 || len(streamResult.Repaste[0].Positions) != minRecurrenceSessions {
		t.Fatalf("retro positions = %+v, want %d", streamResult.Repaste, minRecurrenceSessions)
	}
}

func TestLearnRetroStreamOmitsUndatedRepeatedBlocks(t *testing.T) {
	fx := newRetroFixture(t)
	block := retroRepeatedBlock()
	for i := 0; i < 3; i++ {
		writeRetroTranscript(t, fx.claudeRoot, fmt.Sprintf("session-%d.jsonl", i), []string{
			retroUsageLine(fmt.Sprintf("msg_%da", i)),
			retroTextLine(t, "", block),
			retroUsageLine(fmt.Sprintf("msg_%db", i)),
		})
	}
	s := openRetroTestStore(t)
	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	retro := plan.Retro
	if retro == nil {
		t.Fatal("retro block missing")
	}
	if cut, ok := familyTokens(retro, retroFamilyRepeatedBlocks); !ok || cut <= 0 {
		t.Fatal("undated repeats must remain in the unique-once family")
	}
	if retro.WouldCutStreamTokens != 0 {
		t.Fatalf("undated repeated stream cut = %d, want honest zero", retro.WouldCutStreamTokens)
	}
	found := false
	for _, caveat := range retro.Caveats {
		if strings.Contains(caveat, "without complete transcript timestamps") {
			found = true
		}
	}
	if !found {
		t.Fatalf("undated under-count caveat missing: %v", retro.Caveats)
	}
}

func TestRetroStreamFamiliesShareOneResidencyCap(t *testing.T) {
	r := &retroFileResult{
		turns:      []retroTurn{{line: 3, ctx: 100}},
		candidates: []retroCandidate{{sha: "tool", line: 1, size: 80}},
	}
	repeated := []retroStreamCandidate{{line: 2, cut: 80, size: 80}}
	got := retroStreamCutTokens(r, map[string]int64{"tool": 50}, map[string]bool{}, repeated)
	if got != 80 {
		t.Fatalf("combined stream cut = %d, want oldest tool evicted and repeated cut 80", got)
	}
}

// Sidechain (subagent) lines share the transcript file but are a separate API
// stream: their turns must not weight a main-thread cut, and a sidechain cut
// itself carries zero stream weight (while still counting once in the unique
// figure and in tokens_observed).
func TestLearnRetroStreamExcludesSidechains(t *testing.T) {
	fx := newRetroFixture(t)
	mainPayload := retroLogPayload("mainline")
	sidePayload := retroLogPayload("sideline")
	sidechainUsage := `{"type":"assistant","isSidechain":true,"message":{"id":"msg_side","model":"claude","usage":{"input_tokens":1000,"cache_read_input_tokens":500,"cache_creation_input_tokens":100}}}`
	sideResult := `{"type":"user","isSidechain":true,"message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":` + string(mustJSON(t, sidePayload)) + `}]}}`
	writeRetroTranscript(t, fx.claudeRoot, "session-0.jsonl", []string{
		retroUsageLine("msg_0"),
		retroToolResultLine(t, mainPayload),
		retroUsageLine("msg_1"), // 1 main-thread turn contains the block
		sideResult,              // sidechain cut: zero stream weight
		sidechainUsage,          // sidechain turn: weights nothing
		sidechainUsage,          // duplicate line of the same sidechain response
	})
	s := openRetroTestStore(t)

	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	retro := plan.Retro
	if retro == nil {
		t.Fatal("retro block missing")
	}
	// msg_0 + msg_1 + msg_side: sidechain usage is real sent tokens.
	if retro.TurnsObserved != 3 || retro.TokensObserved != 4800 {
		t.Fatalf("turns/tokens = %d/%d, want 3/4800 with the sidechain response counted once", retro.TurnsObserved, retro.TokensObserved)
	}
	mainCut := retroExpectedCut(t, mainPayload)
	bothCut := retroExpectedCut(t, mainPayload, sidePayload)
	if got, _ := familyTokens(retro, retroFamilyToolOutputs); got != bothCut {
		t.Fatalf("tool_outputs = %d, want both unique cuts (%d)", got, bothCut)
	}
	if retro.WouldCutStreamTokens != mainCut {
		t.Fatalf("would_cut_stream_tokens = %d, want the main cut x 1 main-thread turn (%d)", retro.WouldCutStreamTokens, mainCut)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Believed residency is capped by each turn's provider-counted size: a turn
// the provider counted smaller than the believed-resident tool output evicts
// the oldest blocks, and an evicted block never credits again — even at a
// later, larger turn.
func TestLearnRetroStreamCapsResidencyByCountedContext(t *testing.T) {
	fx := newRetroFixture(t)
	payload := retroLogPayload("evict") // ~4.5KB, ~1.1k estimated tokens
	writeRetroTranscript(t, fx.claudeRoot, "session-0.jsonl", []string{
		retroUsageLine("msg_0"),
		retroToolResultLine(t, payload),
		retroUsageLineSized("msg_1", 50),   // counted 50 tokens: block cannot be resident
		retroUsageLineSized("msg_2", 9000), // once evicted it never comes back
	})
	s := openRetroTestStore(t)

	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	retro := plan.Retro
	if retro == nil {
		t.Fatal("retro block missing")
	}
	if cut, _ := familyTokens(retro, retroFamilyToolOutputs); cut <= 0 {
		t.Fatal("the unique cut must still be booked once")
	}
	if retro.WouldCutStreamTokens != 0 {
		t.Fatalf("would_cut_stream_tokens = %d, want 0 — the provider counted less than the believed-resident block", retro.WouldCutStreamTokens)
	}
}

// A segment appearing in several files weights deterministically in the
// newest file that carries it, regardless of scan-goroutine scheduling.
func TestLearnRetroStreamCrossFileAttributionIsNewestFirst(t *testing.T) {
	fx := newRetroFixture(t)
	shared := retroLogPayload("xfile")
	writeRetroTranscript(t, fx.claudeRoot, "newest.jsonl", []string{
		retroUsageLine("msg_n0"),
		retroToolResultLine(t, shared),
		retroUsageLine("msg_n1"), // 1 later turn here
	})
	writeRetroTranscript(t, fx.claudeRoot, "older.jsonl", []string{
		retroUsageLine("msg_o0"),
		retroToolResultLine(t, shared),
		retroUsageLine("msg_o1"),
		retroUsageLine("msg_o2"),
		retroUsageLine("msg_o3"), // 3 later turns here
	})
	newest := filepath.Join(fx.claudeRoot, "projects", "repo", "newest.jsonl")
	older := filepath.Join(fx.claudeRoot, "projects", "repo", "older.jsonl")
	now := time.Now()
	if err := os.Chtimes(older, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newest, now, now); err != nil {
		t.Fatal(err)
	}
	s := openRetroTestStore(t)

	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	retro := plan.Retro
	if retro == nil {
		t.Fatal("retro block missing")
	}
	cut := retroExpectedCut(t, shared)
	if got, _ := familyTokens(retro, retroFamilyToolOutputs); got != cut {
		t.Fatalf("tool_outputs = %d, want the shared segment measured once (%d)", got, cut)
	}
	// The newest file owns the stream weight: 1 later turn, not the older
	// file's 3.
	if retro.WouldCutStreamTokens != cut {
		t.Fatalf("would_cut_stream_tokens = %d, want cut x 1 turn in the newest file (%d)", retro.WouldCutStreamTokens, cut)
	}
}

// A block whose session shows no provider-counted turn after it was never sent,
// so it must weigh zero in the stream figure (while still counting once in the
// unique figure).
func TestLearnRetroStreamZeroWithoutLaterTurns(t *testing.T) {
	fx := newRetroFixture(t)
	payload := retroLogPayload("tail")
	writeRetroTranscript(t, fx.claudeRoot, "session-0.jsonl", []string{
		retroUsageLine("msg_0"),
		retroToolResultLine(t, payload),
	})
	s := openRetroTestStore(t)

	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	retro := plan.Retro
	if retro == nil {
		t.Fatal("retro block missing")
	}
	if cut, _ := familyTokens(retro, retroFamilyToolOutputs); cut <= 0 {
		t.Fatal("the unique cut must still be booked once")
	}
	if retro.WouldCutStreamTokens != 0 {
		t.Fatalf("would_cut_stream_tokens = %d, want 0 for a block never observed in a later turn", retro.WouldCutStreamTokens)
	}
}

func TestLearnRetroTimeBoxTruncatesNewestFirst(t *testing.T) {
	fx := newRetroFixture(t)
	writeRetroClaudeSessions(t, fx.claudeRoot, 8, 1, 0, "")
	oldProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(oldProcs) })

	// A clock that stops being fresh after the deadline is computed and two jobs
	// have started: the pass must then stop, report partial coverage, and say so.
	base := time.Now()
	var mu sync.Mutex
	calls := 0
	original := retroClock
	retroClock = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= 3 {
			return base
		}
		return base.Add(time.Hour)
	}
	t.Cleanup(func() { retroClock = original })

	s := openRetroTestStore(t)
	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true, BudgetMS: 8000})
	if err != nil {
		t.Fatal(err)
	}
	retro := plan.Retro
	if retro == nil {
		t.Fatal("a partially scanned window still reports what it read")
	}
	if !retro.TimeBoxed {
		t.Fatal("time_boxed must be set when the budget truncated coverage")
	}
	if retro.SessionsTotal != 8 {
		t.Fatalf("sessions_total = %d, want all 8 discovered", retro.SessionsTotal)
	}
	if retro.SessionsScanned < 1 || retro.SessionsScanned >= retro.SessionsTotal {
		t.Fatalf("sessions_scanned = %d, want partial coverage of %d", retro.SessionsScanned, retro.SessionsTotal)
	}
	if retro.TokensObserved != int64(retro.SessionsScanned)*1600 {
		t.Fatalf("tokens_observed = %d must sum only the scanned sessions", retro.TokensObserved)
	}
	found := false
	for _, c := range retro.Caveats {
		if strings.Contains(c, "time budget") {
			found = true
		}
	}
	if !found {
		t.Fatalf("truncation must be named in the caveats: %v", retro.Caveats)
	}
}

func TestBehaviorBudgetCannotErasePartialRetroResult(t *testing.T) {
	fx := newRetroFixture(t)
	writeRetroClaudeSessions(t, fx.claudeRoot, 8, 1, 0, "")
	oldProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(oldProcs) })

	base := time.Now()
	var behaviorMu sync.Mutex
	behaviorCalls := 0
	originalBehaviorClock := behaviorClock
	behaviorClock = func() time.Time {
		behaviorMu.Lock()
		defer behaviorMu.Unlock()
		behaviorCalls++
		if behaviorCalls == 1 {
			return base // establish the base-scan deadline
		}
		return base.Add(time.Hour) // expire before its first transcript walk
	}

	var retroMu sync.Mutex
	retroCalls := 0
	originalRetroClock := retroClock
	retroClock = func() time.Time {
		retroMu.Lock()
		defer retroMu.Unlock()
		retroCalls++
		if retroCalls <= 3 {
			return base // deadline plus enough work for a measurable partial result
		}
		return base.Add(time.Hour)
	}
	t.Cleanup(func() {
		behaviorClock = originalBehaviorClock
		retroClock = originalRetroClock
	})

	s := openRetroTestStore(t)
	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{
		Enabled:          true,
		BehaviorBudgetMS: 100,
		BudgetMS:         8000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Retro == nil {
		t.Fatal("bounded base scan erased the partial retro result")
	}
	if !plan.Retro.TimeBoxed || plan.Retro.SessionsScanned < 1 || plan.Retro.SessionsScanned >= plan.Retro.SessionsTotal {
		t.Fatalf("retro coverage = %d of %d time_boxed=%v, want measurable partial result",
			plan.Retro.SessionsScanned, plan.Retro.SessionsTotal, plan.Retro.TimeBoxed)
	}
	if plan.SessionsScanned != 0 {
		t.Fatalf("base sessions_scanned = %d, want deadline before first transcript", plan.SessionsScanned)
	}
	foundBehaviorCaveat := false
	for _, caveat := range plan.Caveats {
		if strings.Contains(caveat, "base behavioral scan hit its time budget") {
			foundBehaviorCaveat = true
		}
	}
	if !foundBehaviorCaveat {
		t.Fatalf("base truncation caveat missing: %v", plan.Caveats)
	}
}

func TestRetroDiscoveryDeadlineReturnsMeasuredPartialResult(t *testing.T) {
	fx := newRetroFixture(t)
	writeRetroClaudeSessions(t, fx.claudeRoot, 8, 1, 0, "")

	base := time.Now()
	originalRetroClock := retroClock
	originalDiscoveryClock := retroDiscoveryClock
	retroClock = func() time.Time { return base }
	var mu sync.Mutex
	calls := 0
	retroDiscoveryClock = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= 5 {
			return base // deadline plus one complete Claude path
		}
		return base.Add(time.Hour)
	}
	t.Cleanup(func() {
		retroClock = originalRetroClock
		retroDiscoveryClock = originalDiscoveryClock
	})

	s := openRetroTestStore(t)
	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{
		Enabled:          true,
		BehaviorBudgetMS: 8000,
		BudgetMS:         8000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Retro == nil {
		t.Fatal("deadline-aware discovery discarded its complete partial path")
	}
	if !plan.Retro.TimeBoxed || plan.Retro.SessionsTotal != 1 || plan.Retro.SessionsScanned != 1 {
		t.Fatalf("retro discovery coverage = %d/%d time_boxed=%v, want 1/1 partial discovery",
			plan.Retro.SessionsScanned, plan.Retro.SessionsTotal, plan.Retro.TimeBoxed)
	}
	if plan.Retro.TokensObserved <= 0 {
		t.Fatal("partial discovery returned no measured provider usage")
	}
	found := false
	for _, caveat := range plan.Retro.Caveats {
		if strings.Contains(caveat, "discovery or scan time budget") {
			found = true
		}
	}
	if !found {
		t.Fatalf("discovery truncation caveat missing: %v", plan.Retro.Caveats)
	}
}

func TestScanDeadlinesPollBeforeEveryLineParse(t *testing.T) {
	fx := newRetroFixture(t)
	writeRetroClaudeSessions(t, fx.claudeRoot, 1, 1, 0, "")
	path := filepath.Join(fx.claudeRoot, "projects", "repo", "session-0.jsonl")
	base := time.Now()

	originalBehaviorClock := behaviorClock
	t.Cleanup(func() { behaviorClock = originalBehaviorClock })
	behaviorCalls := 0
	behaviorClock = func() time.Time {
		behaviorCalls++
		if behaviorCalls == 1 {
			return base
		}
		return base.Add(time.Hour)
	}
	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	if !scanClaudeTranscriptBehaviorUntil(path, "repo/session-0.jsonl", time.Time{}, nil, &beh, newRecurringMiner(), &behaviorDeadline{at: base.Add(time.Minute)}) {
		t.Fatal("base transcript did not stop at the first line deadline check")
	}
	behaviorClock = originalBehaviorClock
	if beh.Turns != 0 {
		t.Fatalf("base parsed %d turns after deadline, want zero", beh.Turns)
	}

	originalRetroClock := retroClock
	t.Cleanup(func() { retroClock = originalRetroClock })
	retroClock = func() time.Time { return base.Add(time.Hour) }
	result := retroScanClaudeFile(retroSessionPath{path: path, relPath: "repo/session-0.jsonl", source: "claude"}, time.Time{}, &retroCollector{
		seen: map[string]bool{}, savedBySha: map[string]int64{}, deadline: base.Add(time.Minute),
	})
	retroClock = originalRetroClock
	if !result.truncated || result.parsed || result.usageTurns != 0 {
		t.Fatalf("retro truncated=%v parsed=%v usage_turns=%d, want stop before first parse", result.truncated, result.parsed, result.usageTurns)
	}
}

func TestTruncatedBeforeFirstParseDoesNotClaimUsageFreeSession(t *testing.T) {
	fx := newRetroFixture(t)
	writeRetroClaudeSessions(t, fx.claudeRoot, 8, 1, 0, "")
	oldProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(oldProcs) })

	base := time.Now()
	originalRetroClock := retroClock
	var mu sync.Mutex
	calls := 0
	retroClock = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= 5 {
			return base // finish one file, then start the next
		}
		return base.Add(time.Hour) // expire before second file's first parse
	}
	t.Cleanup(func() { retroClock = originalRetroClock })

	s := openRetroTestStore(t)
	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{
		Enabled: true, BudgetMS: 8000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Retro == nil || !plan.Retro.TimeBoxed || plan.Retro.SessionsScanned != 1 {
		t.Fatalf("retro = %+v, want one measured session plus deadline truncation", plan.Retro)
	}
	for _, caveat := range plan.Retro.Caveats {
		if strings.Contains(caveat, "Sessions without provider-counted usage") {
			t.Fatalf("truncated unknown session was mislabeled usage-free: %v", plan.Retro.Caveats)
		}
	}
}

func TestRetroSessionPathsAreNewestFirst(t *testing.T) {
	fx := newRetroFixture(t)
	writeRetroClaudeSessions(t, fx.claudeRoot, 3, 1, 0, "")
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		path := filepath.Join(fx.claudeRoot, "projects", "repo", fmt.Sprintf("session-%d.jsonl", i))
		when := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	paths := retroSessionPaths(map[string]bool{"claude": true}, time.Time{})
	if len(paths) != 3 {
		t.Fatalf("discovered %d transcripts, want 3", len(paths))
	}
	for i := 1; i < len(paths); i++ {
		if paths[i-1].modTime.Before(paths[i].modTime) {
			t.Fatalf("paths must be newest-first: %v", paths)
		}
	}
	if !strings.HasSuffix(paths[0].path, "session-2.jsonl") {
		t.Fatalf("newest transcript first, got %s", paths[0].path)
	}
}

func TestLearnRetroFailsClosedWithoutEngine(t *testing.T) {
	fx := newRetroFixture(t)
	writeRetroClaudeSessions(t, fx.claudeRoot, 3, 1, 2, "")

	original := retroEngineFactory
	retroEngineFactory = func() (*engine.Engine, func(), error) {
		return nil, nil, fmt.Errorf("recovery store unavailable")
	}
	t.Cleanup(func() { retroEngineFactory = original })

	s := openRetroTestStore(t)
	plan, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude"}, "30d", RetroOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	retro := plan.Retro
	if retro == nil {
		t.Fatal("observed tokens are still measurable without the engine")
	}
	if retro.EngineUsed {
		t.Fatal("engine_used must be false when the engine could not be started")
	}
	if _, ok := familyTokens(retro, retroFamilyToolOutputs); ok {
		t.Fatal("the tool-output family must be omitted, never estimated, without the engine")
	}
	wantRepeat := int64(estimateTokens(retroRepeatedBlock())) * 5
	if retro.WouldCutTokens != wantRepeat {
		t.Fatalf("would_cut_tokens = %d, want only the repeated-block family (%d)", retro.WouldCutTokens, wantRepeat)
	}
	found := false
	for _, c := range retro.Caveats {
		if strings.Contains(c, "recovery store unavailable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the failure reason must be named: %v", retro.Caveats)
	}
}

func TestLearnRetroOmittedWithoutFlag(t *testing.T) {
	fx := newRetroFixture(t)
	writeRetroClaudeSessions(t, fx.claudeRoot, 3, 2, 2, "")
	s := openRetroTestStore(t)

	baseline, err := s.BuildLearnPlan(fx.cwd, []string{"claude", "codex"}, "30d")
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := s.BuildLearnPlanWithRetro(fx.cwd, []string{"claude", "codex"}, "30d", RetroOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Retro != nil || disabled.Retro != nil {
		t.Fatal("retro must be absent unless --retro asked for it")
	}
	wantJSON, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(disabled)
	if err != nil {
		t.Fatal(err)
	}
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("plan output changed:\n%s\n%s", wantJSON, gotJSON)
	}
	if strings.Contains(string(gotJSON), `"retro"`) {
		t.Fatal("the retro key must not appear in a plan built without --retro")
	}
}
