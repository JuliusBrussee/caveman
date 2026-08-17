package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/mem"
)

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// learn.go is the local setup profiler. It turns config files and real session
// history into a Cave Score + a ranked list of token sinks (the `caveman.learn.v1`
// contract). Class A sinks are asserted as facts; Class B sinks are softened with
// their evidence attached. Everything is `inferred`.

// Cave Score weights + caps (documented in one place; mirror the cloud "start at
// 100, subtract capped penalties" mechanic). Higher = leaner.
const (
	wConfigTax   = 50.0
	capConfigTax = 35
	wDumbzone    = 50.0
	capDumbzone  = 25
	wDeadLoad    = 40.0
	capDeadLoad  = 20
	wSubagent    = 20.0
	capSubagent  = 20

	dumbzoneFraction    = 0.5  // a turn over 50% of the window is in the dumbzone
	claudeMDLineBudget  = 150  // CLAUDE.md trim target
	claudeMDTokenAlarm  = 2000 // CLAUDE.md token tax that warrants a reducible sink
	dumbzonePctFloor    = 10.0 // only surface a dumbzone sink above this share
	maxLearnScanWorkers = 16   // bound file descriptors and per-parser buffers on large machines
)

// behaviorScan is the at-the-time picture extracted from real session transcripts.
type behaviorScan struct {
	Turns             int
	DumbzoneTurns     int
	Contexts          []int
	SessionPeakPct    []int // per session: peak context as a percent of the assumed model window
	TaskSpawns        int
	SessionsScanned   int
	SessionsBySource  map[string]int
	SessionsWithTasks int
	SkillUse          map[string]int
	LearningLoops     []learningLoop
	From, To          string
}

func (b *behaviorScan) recordSession(source string) {
	b.SessionsScanned++
	if b.SessionsBySource == nil {
		b.SessionsBySource = map[string]int{}
	}
	b.SessionsBySource[source]++
}

// contextDepth buckets each session's peak context share for the report's
// histogram. Nil when no scanned session carried usage data.
func contextDepth(beh behaviorScan) *LearnContextDepth {
	if len(beh.SessionPeakPct) == 0 {
		return nil
	}
	d := &LearnContextDepth{Sessions: len(beh.SessionPeakPct), Buckets: make([]int, 10)}
	for _, pct := range beh.SessionPeakPct {
		if pct > 30 {
			d.Over30Pct++
		}
		if pct > 50 {
			d.Over50Pct++
		}
		i := pct / 10
		if i > 9 {
			i = 9
		}
		d.Buckets[i]++
	}
	return d
}

func (b behaviorScan) medianContext() int {
	if len(b.Contexts) == 0 {
		return 0
	}
	sorted := append([]int(nil), b.Contexts...)
	sort.Ints(sorted)
	return sorted[len(sorted)/2]
}

// BuildLearnPlan is the front-door analyzer: scan config + real history, compute the
// detectors, the Cave Score, rank by tokens/day, and persist sinks. Read-only over
// user config — it never edits a config file.
func (s *Store) BuildLearnPlan(cwd string, sources []string, sinceExpr string) (LearnPlan, error) {
	return s.BuildLearnPlanWithRetro(cwd, sources, sinceExpr, RetroOptions{})
}

// BuildLearnPlanWithRetro is BuildLearnPlan plus the opt-in retrospective pass.
// With retro disabled the two are the same code path and the same output bytes.
func (s *Store) BuildLearnPlanWithRetro(cwd string, sources []string, sinceExpr string, retro RetroOptions) (LearnPlan, error) {
	sourceSet := normalizeSources(sources)
	since := parseSince(sinceExpr)

	cfg := scanConfig(cwd)
	if _, err := s.InsertConfigSnapshots(cfg.Snapshots); err != nil {
		logStoreWarning(s.logger, "config snapshot persist failed", err)
	}

	beh, rec := behaviorScan{}, recurringResult{}
	behaviorTimeBoxed := false
	if retro.Enabled {
		budgetMS := retro.BehaviorBudgetMS
		if budgetMS <= 0 {
			budgetMS = behaviorDefaultBudgetMS
		}
		beh, rec, behaviorTimeBoxed = s.scanBehaviorWithBudget(sourceSet, since, cfg, time.Duration(budgetMS)*time.Millisecond)
	} else {
		beh, rec = s.scanBehavior(sourceSet, since, cfg)
	}

	days := windowDays(sinceExpr, beh.From, beh.To)
	turnsPerDay := 0.0
	if beh.Turns > 0 && days > 0 {
		turnsPerDay = float64(beh.Turns) / days
	}

	plan := LearnPlan{
		Schema:           learnSchema,
		Basis:            learnBasis,
		Window:           LearnWindow{From: beh.From, To: beh.To, Since: sinceExpr},
		SessionsScanned:  beh.SessionsScanned,
		SessionsBySource: beh.SessionsBySource,
		ContextDepth:     contextDepth(beh),
		Sinks:            []Sink{},
		Caveats: []string{
			"Local learn results are inferred. No local number is promoted to verified; Cloud additionally requires supported provider-causal, provider-complete, catalog-priced active evidence.",
			"Config tax is reported as a forward rate from your current setup, never as tokens already wasted. Context window sizes are assumed per-provider defaults.",
		},
	}

	deadTokens, deadSkills := deadLoadSkills(cfg, beh)

	recurPerTurn := 0
	for _, e := range rec.Repaste {
		recurPerTurn += recurringPerTurn(e, beh.Turns)
	}

	plan.Sinks = append(plan.Sinks, configSinks(cfg, turnsPerDay)...)
	plan.Sinks = append(plan.Sinks, recurringSinks(rec, beh, turnsPerDay)...)
	plan.Sinks = append(plan.Sinks, learningLoopSinks(beh.LearningLoops)...)
	plan.Sinks = append(plan.Sinks, dumbzoneSink(beh)...)
	plan.Sinks = append(plan.Sinks, deadLoadSink(deadTokens, deadSkills, beh, turnsPerDay)...)
	plan.Sinks = append(plan.Sinks, subagentSink(beh)...)
	plan.Sinks = append(plan.Sinks, surfaceSink(cfg)...)
	for i := range plan.Sinks {
		plan.Sinks[i].PracticeID = practiceIDForSink(plan.Sinks[i].SinkID)
	}

	// Rank by forward tokens/day rate (reducible movers first); behavioral sinks
	// (rate 0) keep their relative order after.
	sort.SliceStable(plan.Sinks, func(i, j int) bool {
		return plan.Sinks[i].TokensPerDayRate > plan.Sinks[j].TokensPerDayRate
	})

	plan.CaveScore = caveScore(cfg, beh, deadTokens, recurPerTurn)

	if len(plan.Sinks) == 0 {
		plan.Caveats = appendUnique(plan.Caveats, "No config-tax or behavioral sink found yet. Run caveman learn after some Claude/Codex sessions exist on disk, or from a repo with a CLAUDE.md.")
	}
	if beh.SessionsScanned == 0 {
		plan.Caveats = appendUnique(plan.Caveats, "No local session transcripts were scanned, so behavioral findings (dumbzone, subagents, dead-skill use) are not measured.")
	}
	if behaviorTimeBoxed {
		plan.Caveats = appendUnique(plan.Caveats, "The base behavioral scan hit its time budget, so Cave Score and behavioral findings cover partial history. The independent retro totals still name only sessions they measured.")
	}

	// The retro pass is a second, budget-bounded walk so the base scan above keeps
	// its exact timing and its exact output when --retro is absent.
	if retro.Enabled {
		plan.Retro = s.buildLearnRetro(sourceSet, since, sinceExpr, cfg.configTaxPerTurn(), retro)
	}

	if plan.WrapMeasured = s.wrapMeasuredSince(since); plan.WrapMeasured != nil {
		plan.WrapMeasured.WindowDays = int(windowDays(sinceExpr, "", ""))
		plan.Caveats = appendUnique(plan.Caveats, fmt.Sprintf("Saved-so-far numbers are Caveman-counted tokens (basis: %s) over requests the proxy recorded in the window. Tokens only, no dollars. Kept apart from the could-have-saved replay: different requests, different method, never summed together.", plan.WrapMeasured.Basis))
	}

	if err := s.upsertSinks(plan.Sinks); err != nil {
		logStoreWarning(s.logger, "learn sink persist failed", err)
	}
	return plan, nil
}

// wrapMeasuredSince sums proxy-recorded wrap activity at or after since. Nil
// unless at least one row booked a real compression cut or an observe-mode
// estimate: a machine that never ran the proxy must not render a zero card.
// The write path already refuses negative token fields, so the clamps below
// are defense-in-depth against rows written by other tooling.
func (s *Store) wrapMeasuredSince(since time.Time) *LearnWrapMeasured {
	where := ""
	var args []any
	if !since.IsZero() {
		where = " WHERE ts >= ?"
		args = append(args, since.UTC().Format(storeTSLayout))
	}
	out := &LearnWrapMeasured{}
	var bases string
	err := s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN COALESCE(compression_tokens_before,0) > COALESCE(compression_tokens_after,0) THEN 1 ELSE 0 END),0),
		COALESCE(SUM(compression_tokens_before),0), COALESCE(SUM(compression_tokens_after),0),
		COALESCE(SUM(would_save_tokens),0),
		COALESCE(GROUP_CONCAT(DISTINCT NULLIF(compression_token_count_basis,'')),'')
		FROM requests`+where, args...).Scan(
		&out.Requests, &out.CompressedRequests,
		&out.TokensBefore, &out.TokensAfter, &out.WouldSaveTokens, &bases,
	)
	if err != nil || out.Requests == 0 {
		return nil
	}
	if out.TokensBefore < 0 {
		out.TokensBefore = 0
	}
	if out.TokensAfter < 0 {
		out.TokensAfter = 0
	}
	if out.WouldSaveTokens < 0 {
		out.WouldSaveTokens = 0
	}
	if out.TokensSaved = out.TokensBefore - out.TokensAfter; out.TokensSaved < 0 {
		out.TokensSaved = 0
	}
	if out.TokensSaved == 0 && out.WouldSaveTokens == 0 {
		return nil
	}
	switch {
	case bases == "":
		out.Basis = "unavailable"
	case !strings.Contains(bases, ","):
		out.Basis = bases
	default:
		out.Basis = "mixed"
	}
	return out
}

// LearnScan builds the plan and writes concise cavemem learnings from the reducible
// sinks (so the durable memory + trial report stay populated). Returns the plan.
func (s *Store) LearnScan(sources []string, sinceExpr string) (LearnPlan, error) {
	return s.LearnScanWithRetro(sources, sinceExpr, RetroOptions{})
}

// LearnScanWithRetro is LearnScan plus the opt-in retrospective pass.
func (s *Store) LearnScanWithRetro(sources []string, sinceExpr string, retro RetroOptions) (LearnPlan, error) {
	cwd, _ := os.Getwd()
	plan, err := s.BuildLearnPlanWithRetro(cwd, sources, sinceExpr, retro)
	if err != nil {
		return plan, err
	}
	s.writeCavememLearnings(plan)
	return plan, nil
}

func (s *Store) writeCavememLearnings(plan LearnPlan) {
	var texts []string
	for _, sink := range plan.Sinks {
		if sink.Class != classReducible {
			continue
		}
		text := sink.Title
		if sink.Suggestion != "" {
			text += " — " + sink.Suggestion
		}
		texts = append(texts, text)
	}
	if len(texts) == 0 {
		return
	}
	memStore, err := mem.Open(mem.Options{})
	if err != nil {
		logStoreWarning(s.logger, "open cavemem failed", err)
		return
	}
	defer memStore.Close()
	for _, text := range texts {
		l := Learning{Text: text, SourceKind: "caveman_learn", Confidence: "medium"}
		if memory, err := memStore.Remember(text); err == nil {
			l.ID = memory.ID
			l.StoredInCavemem = true
		} else {
			l.ID = "mem_" + hashText(text)
		}
		if err := s.insertLearning(l); err != nil {
			logStoreWarning(s.logger, "learning insert failed", err)
		}
	}
}

// --- detectors -------------------------------------------------------------

func configSinks(cfg configScan, turnsPerDay float64) []Sink {
	tax := cfg.configTaxPerTurn()
	var sinks []Sink
	if tax > 0 {
		userTokens, projectTokens := 0, 0
		if cfg.ClaudeMDUser != nil {
			userTokens = cfg.ClaudeMDUser.Tokens
		}
		if cfg.ClaudeMDProject != nil {
			projectTokens = cfg.ClaudeMDProject.Tokens
		}
		sinks = append(sinks, Sink{
			SinkID:           "config_tax:baseline",
			Title:            fmt.Sprintf("Your agent config loads ~%d tokens into every turn", tax),
			Class:            classLoadBearing,
			Basis:            observedLocal,
			TokensPerTurn:    int64(tax),
			TokensPerDayRate: rate(tax, turnsPerDay),
			Framing:          framingForward,
			Suggestion:       "Some of this is load-bearing. The reducible parts are broken out as their own sinks below.",
			Evidence: map[string]any{
				"claude_md_user_tokens":    userTokens,
				"claude_md_project_tokens": projectTokens,
				"skill_desc_tokens":        cfg.SkillDescTokens,
				"skill_count":              len(cfg.Skills),
				"hook_count":               cfg.HookCount,
				"plugin_count":             cfg.PluginCount,
			},
		})
	}
	sinks = append(sinks, claudeMDSink(cfg.ClaudeMDUser, "user", turnsPerDay)...)
	sinks = append(sinks, claudeMDSink(cfg.ClaudeMDProject, "project", turnsPerDay)...)
	if cfg.CodexAgents != nil {
		sinks = append(sinks, claudeMDSink(cfg.CodexAgents, "codex", turnsPerDay)...)
	}
	return sinks
}

func claudeMDSink(snap *ConfigSnapshot, scope string, turnsPerDay float64) []Sink {
	if snap == nil {
		return nil
	}
	if snap.Lines <= claudeMDLineBudget && snap.Tokens <= claudeMDTokenAlarm {
		return nil
	}
	label := map[string]string{"user": "User", "project": "Project", "codex": "Codex AGENTS.md"}[scope]
	kind := "CLAUDE.md"
	if snap.Kind == "agents_md" {
		kind = "AGENTS.md"
	}
	title := fmt.Sprintf("%s %s is %d lines (~%d tokens) loaded every turn", label, kind, snap.Lines, snap.Tokens)
	if scope == "codex" {
		title = fmt.Sprintf("%s is %d lines (~%d tokens) loaded every turn", label, snap.Lines, snap.Tokens)
	}
	return []Sink{{
		SinkID:           "claude_md_weight:" + scope,
		Title:            title,
		Class:            classReducible,
		Basis:            observedLocal,
		TokensPerTurn:    int64(snap.Tokens),
		TokensPerDayRate: rate(snap.Tokens, turnsPerDay),
		Framing:          framingForward,
		Suggestion:       fmt.Sprintf("Trim to the sections actually used; target < %d lines.", claudeMDLineBudget),
		Evidence:         map[string]any{"lines": snap.Lines, "tokens": snap.Tokens, "path": snap.Path},
	}}
}

func dumbzoneSink(beh behaviorScan) []Sink {
	if beh.Turns == 0 {
		return nil
	}
	pct := float64(beh.DumbzoneTurns) / float64(beh.Turns) * 100
	if pct < dumbzonePctFloor {
		return nil
	}
	return []Sink{{
		SinkID:        "context_dumbzone",
		Title:         fmt.Sprintf("%.0f%% of turns ran over %.0f%% of the model window", pct, dumbzoneFraction*100),
		Class:         classBehavioral,
		Basis:         observedLocal,
		TokensPerTurn: 0, TokensPerDayRate: 0,
		Framing:    framingHistorical,
		Suggestion: "Compact or split long sessions before the dumbzone; large context degrades quality well before the window limit.",
		Evidence: map[string]any{
			"turns_over_50pct": beh.DumbzoneTurns,
			"total_turns":      beh.Turns,
			"pct":              int(pct + 0.5),
			"median_context":   beh.medianContext(),
		},
	}}
}

// deadLoadSkills returns the total per-turn token tax of skills with no detected
// use, and the slug list (Class B: numbers asserted, "unused" softened to
// "no use detected in the scanned window").
func deadLoadSkills(cfg configScan, beh behaviorScan) (int, []string) {
	if beh.SessionsScanned == 0 {
		return 0, nil
	}
	tokens := 0
	var slugs []string
	for _, sk := range cfg.Skills {
		slug := strings.ToLower(filepath.Base(filepath.Dir(sk.Path)))
		if beh.SkillUse[slug] > 0 {
			continue
		}
		tokens += sk.DescTokens
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	return tokens, slugs
}

func deadLoadSink(deadTokens int, deadSkills []string, beh behaviorScan, turnsPerDay float64) []Sink {
	if len(deadSkills) == 0 || deadTokens == 0 {
		return nil
	}
	sample := deadSkills
	if len(sample) > 12 {
		sample = sample[:12]
	}
	return []Sink{{
		SinkID:           "dead_load:skills",
		Title:            fmt.Sprintf("%d skills load ~%d tokens/turn with no use detected in %d sessions", len(deadSkills), deadTokens, beh.SessionsScanned),
		Class:            classReducible,
		Basis:            observedLocal,
		TokensPerTurn:    int64(deadTokens),
		TokensPerDayRate: rate(deadTokens, turnsPerDay),
		Framing:          framingForward,
		Suggestion:       "Consider gating skills with no detected use; their descriptions load every turn. (No use detected is window-bounded, not proof a skill is unneeded.)",
		Evidence: map[string]any{
			"skill_count":      len(deadSkills),
			"sessions_scanned": beh.SessionsScanned,
			"skills":           sample,
		},
	}}
}

func subagentSink(beh behaviorScan) []Sink {
	if beh.TaskSpawns == 0 {
		return nil
	}
	median := 0
	if beh.SessionsWithTasks > 0 {
		median = beh.TaskSpawns / beh.SessionsWithTasks
	}
	return []Sink{{
		SinkID:        "subagent_overuse",
		Title:         fmt.Sprintf("You spawned %d subagents across %d sessions (≈%d per session that used them)", beh.TaskSpawns, beh.SessionsWithTasks, median),
		Class:         classBehavioral,
		Basis:         observedLocal,
		TokensPerTurn: 0, TokensPerDayRate: 0,
		Framing:    framingHistorical,
		Suggestion: "Subagents each carry their own context; for one-file lookups a direct read is cheaper. (Counts only — Caveman never asserts a spawn was unnecessary.)",
		Evidence: map[string]any{
			"task_spawns":         beh.TaskSpawns,
			"sessions_with_tasks": beh.SessionsWithTasks,
			"sessions_scanned":    beh.SessionsScanned,
		},
	}}
}

func surfaceSink(cfg configScan) []Sink {
	combined := cfg.HookCount + cfg.PluginCount
	if combined < 10 {
		return nil
	}
	return []Sink{{
		SinkID:        "config_surface",
		Title:         fmt.Sprintf("Your setup loads %d skills, %d hooks, and %d plugins", len(cfg.Skills), cfg.HookCount, cfg.PluginCount),
		Class:         classBehavioral,
		Basis:         observedLocal,
		TokensPerTurn: 0, TokensPerDayRate: 0,
		Framing:    framingHistorical,
		Suggestion: "SessionStart/UserPromptSubmit hooks inject their output into context each session; trimming the surface you don't use reduces per-turn overhead.",
		Evidence: map[string]any{
			"skill_count":  len(cfg.Skills),
			"hook_count":   cfg.HookCount,
			"plugin_count": cfg.PluginCount,
		},
	}}
}

// --- Cave Score ------------------------------------------------------------

func caveScore(cfg configScan, beh behaviorScan, deadTokens, recurPerTurn int) CaveScore {
	tax := cfg.configTaxPerTurn()
	median := beh.medianContext()

	components := []ScoreComponent{}
	score := 100

	// config_tax: per-turn tokens you pay to re-establish context every turn —
	// config that loads each turn plus recurring re-pasted blocks — as a share of a
	// median turn. Recurring re-paste folds in here (it's the same value family:
	// tokens cavemem could replace), so the score stays four components.
	cTax := ScoreComponent{Key: scoreKeyConfigTax}
	numerator := tax + recurPerTurn
	if numerator > 0 && median > 0 {
		cTax.Measured = true
		ratio := float64(numerator) / float64(median)
		cTax.Penalty = capped(wConfigTax*ratio, capConfigTax)
		if recurPerTurn > 0 {
			cTax.Detail = fmt.Sprintf("config %d + recurring re-paste %d tok/turn vs median context %d", tax, recurPerTurn, median)
		} else {
			cTax.Detail = fmt.Sprintf("config %d tok/turn vs median context %d", tax, median)
		}
	} else {
		cTax.Detail = "not measured (need config tax and session transcripts)"
	}
	components = append(components, cTax)
	score -= cTax.Penalty

	// dumbzone: share of turns over the window fraction.
	cDz := ScoreComponent{Key: scoreKeyDumbzone}
	if beh.Turns > 0 {
		cDz.Measured = true
		dzRate := float64(beh.DumbzoneTurns) / float64(beh.Turns)
		cDz.Penalty = capped(wDumbzone*dzRate, capDumbzone)
		cDz.Detail = fmt.Sprintf("%d/%d turns over %.0f%% window", beh.DumbzoneTurns, beh.Turns, dumbzoneFraction*100)
	} else {
		cDz.Detail = "not measured (no session transcripts)"
	}
	components = append(components, cDz)
	score -= cDz.Penalty

	// dead_load: dead-skill tokens as a share of the config tax.
	cDead := ScoreComponent{Key: scoreKeyDeadLoad}
	if beh.SessionsScanned > 0 && tax > 0 {
		cDead.Measured = true
		deadRatio := float64(deadTokens) / float64(tax)
		cDead.Penalty = capped(wDeadLoad*deadRatio, capDeadLoad)
		cDead.Detail = fmt.Sprintf("%d dead-skill tok of %d config tok", deadTokens, tax)
	} else {
		cDead.Detail = "not measured (need skills and session transcripts)"
	}
	components = append(components, cDead)
	score -= cDead.Penalty

	// subagent pressure: average task spawns per scanned session.
	cSub := ScoreComponent{Key: scoreKeySubagent}
	if beh.SessionsScanned > 0 {
		cSub.Measured = true
		pressure := float64(beh.TaskSpawns) / float64(beh.SessionsScanned) / 5.0
		if pressure > 1 {
			pressure = 1
		}
		cSub.Penalty = capped(wSubagent*pressure, capSubagent)
		cSub.Detail = fmt.Sprintf("%d spawns over %d sessions", beh.TaskSpawns, beh.SessionsScanned)
	} else {
		cSub.Detail = "not measured (no session transcripts)"
	}
	components = append(components, cSub)
	score -= cSub.Penalty

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return CaveScore{Score: score, Basis: learnBasis, Scope: "local_setup", Components: components}
}

// --- behavioral transcript scan -------------------------------------------

var behaviorClock = time.Now

type behaviorDeadline struct {
	at time.Time
}

func (d *behaviorDeadline) expired() bool {
	return d != nil && !behaviorClock().Before(d.at)
}

func (s *Store) scanBehavior(sourceSet map[string]bool, since time.Time, cfg configScan) (behaviorScan, recurringResult) {
	beh, rec, _ := s.scanBehaviorUntil(sourceSet, since, cfg, nil)
	return beh, rec
}

func (s *Store) scanBehaviorWithBudget(sourceSet map[string]bool, since time.Time, cfg configScan, budget time.Duration) (behaviorScan, recurringResult, bool) {
	deadline := &behaviorDeadline{at: behaviorClock().Add(budget)}
	return s.scanBehaviorUntil(sourceSet, since, cfg, deadline)
}

func (s *Store) scanBehaviorUntil(sourceSet map[string]bool, since time.Time, cfg configScan, deadline *behaviorDeadline) (behaviorScan, recurringResult, bool) {
	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	miner := newRecurringMiner()
	timeBoxed := false
	slugs := make([]string, 0, len(cfg.Skills))
	for _, sk := range cfg.Skills {
		slugs = append(slugs, strings.ToLower(filepath.Base(filepath.Dir(sk.Path))))
	}
	if sourceSet["claude"] {
		timeBoxed = scanClaudeSessionsUntil(claudeRoot(), since, slugs, &beh, miner, deadline) || timeBoxed
	}
	if sourceSet["codex"] && (deadline == nil || !deadline.expired()) {
		// Codex repaste mining is deferred (its payload content shape differs);
		// behavioral counts are still scanned below.
		timeBoxed = scanCodexSessionsUntil(codexRoot(), since, &beh, deadline) || timeBoxed
	} else if sourceSet["codex"] {
		timeBoxed = true
	}
	return beh, miner.result(), timeBoxed
}

func scanClaudeSessionsUntil(root string, since time.Time, slugs []string, beh *behaviorScan, miner *recurringMiner, deadline *behaviorDeadline) bool {
	if root == "" {
		return false
	}
	projects := filepath.Join(root, "projects")
	var paths []string
	timeBoxed := false
	_ = filepath.WalkDir(projects, func(path string, d os.DirEntry, err error) error {
		if deadline != nil && deadline.expired() {
			timeBoxed = true
			return fs.SkipAll
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	sort.Strings(paths)

	type result struct {
		behavior  behaviorScan
		miner     *recurringMiner
		timeBoxed bool
	}
	results := make([]result, len(paths))
	parallelSessionScan(len(paths), func(i int) {
		if deadline != nil && deadline.expired() {
			results[i].timeBoxed = true
			return
		}
		path := paths[i]
		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			relPath = filepath.Base(path)
		}
		localBehavior := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
		localMiner := newRecurringMiner()
		truncated := scanClaudeTranscriptBehaviorUntil(path, relPath, since, slugs, &localBehavior, localMiner, deadline)
		results[i] = result{behavior: localBehavior, miner: localMiner, timeBoxed: truncated}
	})
	for i := range results {
		mergeBehaviorScan(beh, &results[i].behavior)
		miner.merge(results[i].miner)
		timeBoxed = results[i].timeBoxed || timeBoxed
	}
	return timeBoxed
}

func scanClaudeTranscriptBehavior(path, relPath string, since time.Time, slugs []string, beh *behaviorScan, miner *recurringMiner) {
	_ = scanClaudeTranscriptBehaviorUntil(path, relPath, since, slugs, beh, miner, nil)
}

func scanClaudeTranscriptBehaviorUntil(path, relPath string, since time.Time, slugs []string, beh *behaviorScan, miner *recurringMiner, deadline *behaviorDeadline) bool {
	if deadline != nil && deadline.expired() {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	beh.recordSession("claude")
	sessionTasks := 0
	sessionPeakPct := 0
	seenSlugs := map[string]bool{}
	seenUsage := map[string]bool{}
	pendingTools := map[string]learnToolCall{}
	var sessionToolCalls []learnToolCall
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if deadline != nil && deadline.expired() {
			return true
		}
		line := sc.Bytes()
		var obj map[string]any
		if json.Unmarshal(line, &obj) != nil {
			continue
		}
		ts := timestampFromObject(obj)
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			continue
		}
		tsStr := ""
		if !ts.IsZero() {
			tsStr = ts.UTC().Format(time.RFC3339)
			if beh.From == "" || tsStr < beh.From {
				beh.From = tsStr
			}
			if beh.To == "" || tsStr > beh.To {
				beh.To = tsStr
			}
		}
		miner.observeTurn("claude", relPath, lineNo, tsStr, obj)
		sessionToolCalls = append(sessionToolCalls, claudeToolCalls(obj, pendingTools, lineNo)...)
		if ctx, ok := claudeTurnContext(obj); ok {
			if id := claudeUsageMessageID(obj); id == "" || !seenUsage[id] {
				if id != "" {
					seenUsage[id] = true
				}
				beh.Turns++
				beh.Contexts = append(beh.Contexts, ctx)
				window := contextWindow("anthropic", claudeModel(obj))
				if pct := ctx * 100 / window; pct > sessionPeakPct {
					sessionPeakPct = pct
				}
				if ctx > int(dumbzoneFraction*float64(window)) {
					beh.DumbzoneTurns++
				}
			}
		}
		lower := strings.ToLower(string(line))
		if strings.Contains(lower, `"name":"task"`) {
			sessionTasks += strings.Count(lower, `"name":"task"`)
		}
		for _, slug := range slugs {
			if slug != "" && !seenSlugs[slug] && strings.Contains(lower, slug) {
				seenSlugs[slug] = true
			}
		}
	}
	for slug := range seenSlugs {
		beh.SkillUse[slug]++
	}
	if sessionTasks > 0 {
		beh.TaskSpawns += sessionTasks
		beh.SessionsWithTasks++
	}
	if sessionPeakPct > 0 {
		beh.SessionPeakPct = append(beh.SessionPeakPct, sessionPeakPct)
	}
	sessionSum := sha256.Sum256([]byte(relPath))
	sessionRef := hex.EncodeToString(sessionSum[:8])
	beh.LearningLoops = append(beh.LearningLoops, detectLearningLoops(sessionToolCalls, sessionRef)...)
	return false
}

// claudeUsageMessageID identifies the API response a usage block belongs to.
// Claude Code writes one assistant turn as several JSONL lines — one per
// content block — and each line repeats the same message.usage verbatim, so
// counting per line double-counts every multi-block turn (measured ×2.03 on
// real 30-day logs). Lines carrying no id cannot be deduplicated and count
// individually.
func claudeUsageMessageID(obj map[string]any) string {
	msg := asMap(obj["message"])
	return firstString(msg["id"], obj["requestId"])
}

// claudeTurnContext returns the full context size the model saw on an assistant
// turn (input + cache read + cache creation), from the real usage block.
func claudeTurnContext(obj map[string]any) (int, bool) {
	msg := asMap(obj["message"])
	usage := asMap(msg["usage"])
	if len(usage) == 0 {
		usage = asMap(obj["usage"])
	}
	if len(usage) == 0 {
		return 0, false
	}
	total, ok := checkedNonNegativeSum(
		int64FromAny(usage["input_tokens"]),
		int64FromAny(usage["cache_read_input_tokens"]),
		int64FromAny(usage["cache_creation_input_tokens"]),
	)
	if !ok || total <= 0 || uint64(total) > uint64(^uint(0)>>1) {
		return 0, false
	}
	return int(total), true
}

func claudeModel(obj map[string]any) string {
	msg := asMap(obj["message"])
	return firstString(msg["model"], obj["model"])
}

func scanCodexSessionsUntil(root string, since time.Time, beh *behaviorScan, deadline *behaviorDeadline) bool {
	if root == "" {
		return false
	}
	paths, timeBoxed, _ := codexPathsUntil(root, func() bool { return deadline != nil && deadline.expired() })
	sort.Strings(paths)
	type result struct {
		behavior  behaviorScan
		timeBoxed bool
	}
	results := make([]result, len(paths))
	parallelSessionScan(len(paths), func(i int) {
		if deadline != nil && deadline.expired() {
			results[i].timeBoxed = true
			return
		}
		local := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
		truncated := scanCodexSessionBehaviorUntil(paths[i], since, &local, deadline)
		results[i] = result{behavior: local, timeBoxed: truncated}
	})
	for i := range results {
		mergeBehaviorScan(beh, &results[i].behavior)
		timeBoxed = results[i].timeBoxed || timeBoxed
	}
	return timeBoxed
}

func scanCodexSessionBehavior(path string, since time.Time, beh *behaviorScan) {
	_ = scanCodexSessionBehaviorUntil(path, since, beh, nil)
}

func scanCodexSessionBehaviorUntil(path string, since time.Time, beh *behaviorScan, deadline *behaviorDeadline) bool {
	if deadline != nil && deadline.expired() {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	beh.recordSession("codex")
	lineNo := 0
	sessionPeakPct := 0
	for sc.Scan() {
		lineNo++
		if deadline != nil && deadline.expired() {
			return true
		}
		var obj map[string]any
		if json.Unmarshal(sc.Bytes(), &obj) != nil {
			continue
		}
		ts := timestampFromObject(obj)
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			continue
		}
		payload := asMap(obj["payload"])
		info := asMap(payload["info"])
		usage := asMap(info["last_token_usage"])
		if len(usage) == 0 {
			usage = asMap(payload["last_token_usage"])
		}
		if len(usage) == 0 {
			continue
		}
		// Codex mirrors OpenAI usage: input_tokens is already the total
		// effective prompt size and cached_input_tokens is a subset. Adding the
		// cache detail again inflates context and false-triggers the dumbzone.
		ctx64 := int64FromAny(usage["input_tokens"])
		if uint64(ctx64) > uint64(^uint(0)>>1) {
			continue
		}
		ctx := int(ctx64)
		if ctx <= 0 {
			continue
		}
		beh.Turns++
		beh.Contexts = append(beh.Contexts, ctx)
		model := firstString(payload["model"], info["model"], obj["model"])
		window := contextWindow("openai", model)
		if pct := ctx * 100 / window; pct > sessionPeakPct {
			sessionPeakPct = pct
		}
		if ctx > int(dumbzoneFraction*float64(window)) {
			beh.DumbzoneTurns++
		}
	}
	if sessionPeakPct > 0 {
		beh.SessionPeakPct = append(beh.SessionPeakPct, sessionPeakPct)
	}
	return false
}

// parallelSessionScan bounds file-level concurrency to available Go schedulers.
// Each worker owns its parser state; results merge later in sorted path order.
func parallelSessionScan(count int, scan func(int)) {
	if count <= 0 {
		return
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > maxLearnScanWorkers {
		workers = maxLearnScanWorkers
	}
	if workers > count {
		workers = count
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := range jobs {
				scan(i)
			}
		}()
	}
	for i := range count {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

func mergeBehaviorScan(dst, src *behaviorScan) {
	if dst == nil || src == nil {
		return
	}
	dst.Turns += src.Turns
	dst.DumbzoneTurns += src.DumbzoneTurns
	dst.Contexts = append(dst.Contexts, src.Contexts...)
	dst.SessionPeakPct = append(dst.SessionPeakPct, src.SessionPeakPct...)
	dst.TaskSpawns += src.TaskSpawns
	dst.SessionsScanned += src.SessionsScanned
	dst.SessionsWithTasks += src.SessionsWithTasks
	dst.LearningLoops = append(dst.LearningLoops, src.LearningLoops...)
	if dst.SessionsBySource == nil {
		dst.SessionsBySource = map[string]int{}
	}
	for source, count := range src.SessionsBySource {
		dst.SessionsBySource[source] += count
	}
	if dst.SkillUse == nil {
		dst.SkillUse = map[string]int{}
	}
	for skill, count := range src.SkillUse {
		dst.SkillUse[skill] += count
	}
	if src.From != "" && (dst.From == "" || src.From < dst.From) {
		dst.From = src.From
	}
	if src.To > dst.To {
		dst.To = src.To
	}
}

// --- helpers ---------------------------------------------------------------

func (s *Store) upsertSinks(sinks []Sink) error {
	for _, sink := range sinks {
		evidence, _ := json.Marshal(sink.Evidence)
		if _, err := s.db.Exec(
			`INSERT INTO learn_sinks
			  (sink_id, title, class, basis, tokens_per_turn, tokens_per_day_rate, framing, evidence_json, suggestion, computed_at)
			  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			  ON CONFLICT(sink_id) DO UPDATE SET
			    title = excluded.title, class = excluded.class, basis = excluded.basis,
			    tokens_per_turn = excluded.tokens_per_turn, tokens_per_day_rate = excluded.tokens_per_day_rate,
			    framing = excluded.framing, evidence_json = excluded.evidence_json,
			    suggestion = excluded.suggestion, computed_at = excluded.computed_at`,
			sink.SinkID, sink.Title, sink.Class, sink.Basis, sink.TokensPerTurn, sink.TokensPerDayRate,
			sink.Framing, string(evidence), sink.Suggestion, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			return err
		}
	}
	return nil
}

func normalizeSources(sources []string) map[string]bool {
	set := map[string]bool{}
	for _, src := range sources {
		for _, part := range strings.Split(src, ",") {
			if part = strings.TrimSpace(part); part != "" {
				set[part] = true
			}
		}
	}
	if len(set) == 0 {
		set = map[string]bool{"codex": true, "claude": true, "caveman": true}
	}
	return set
}

func contextWindow(provider, model string) int {
	m := strings.ToLower(model)
	if strings.Contains(m, "1m") || strings.Contains(m, "[1m]") {
		return 1_000_000
	}
	switch provider {
	case "openai":
		return 400_000
	default:
		return 200_000
	}
}

func windowDays(sinceExpr, from, to string) float64 {
	sinceExpr = strings.TrimSpace(sinceExpr)
	if strings.HasSuffix(sinceExpr, "d") {
		var days int
		if _, err := fmt.Sscanf(sinceExpr, "%dd", &days); err == nil && days > 0 {
			return float64(days)
		}
	}
	if from != "" && to != "" {
		a, errA := time.Parse(time.RFC3339, from)
		b, errB := time.Parse(time.RFC3339, to)
		if errA == nil && errB == nil {
			d := b.Sub(a).Hours() / 24
			if d >= 1 {
				return d
			}
		}
	}
	return 1
}

func rate(tokensPerTurn int, turnsPerDay float64) int64 {
	if turnsPerDay <= 0 {
		return 0
	}
	return int64(float64(tokensPerTurn) * turnsPerDay)
}

func capped(v float64, max int) int {
	n := int(v + 0.5)
	if n < 0 {
		return 0
	}
	if n > max {
		return max
	}
	return n
}
