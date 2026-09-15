package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/mem"
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
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
	// maxRecurringSinkRows caps how many repaste fingerprints become sinks; the
	// remainder is disclosed in a caveat and still weighs into the Cave Score.
	maxRecurringSinkRows = 24
)

// behaviorScan is the at-the-time picture extracted from real session transcripts.
type behaviorScan struct {
	Turns                  int
	DumbzoneTurns          int
	DumbzoneExcessTokens   int64
	Contexts               []int
	PrefixContexts         map[string][]int // first deduplicated provider-counted turn per session, grouped by source
	SessionPeakPct         []int            // per session: peak context as a percent of the assumed model window
	SessionPeakPctBySource map[string][]int // usage-bearing session peaks grouped by normalized source
	TaskSpawns             int
	SessionsScanned        int
	SessionsBySource       map[string]int
	SessionsWithTasks      int
	SkillUse               map[string]int
	LearningLoops          []learningLoop
	CacheHygieneSessions   []cacheHygieneSession
	RereadSessions         []rereadSession
	CompactionSessions     []compactionSession
	SessionTexts           []sessionTextObservation
	SessionMetrics         []learnSessionMetric
	SessionOutcomes        []sessionOutcome
	SubagentSpend          subagentSpendTracker
	Procedures             procedureMiner
	Spend                  spendAccumulator
	ToolPortfolio          toolPortfolioTracker
	FallbackWindowSources  map[string]bool
	From, To               string
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

func (b *behaviorScan) recordPrefix(source string, tokens int) {
	if tokens <= 0 {
		return
	}
	if b.PrefixContexts == nil {
		b.PrefixContexts = map[string][]int{}
	}
	b.PrefixContexts[source] = append(b.PrefixContexts[source], tokens)
}

func (b behaviorScan) measuredPrefix() (tokens, sessions int) {
	var measured []int
	sources := make([]string, 0, len(b.PrefixContexts))
	for source := range b.PrefixContexts {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	for _, source := range sources {
		measured = append(measured, b.PrefixContexts[source]...)
	}
	if len(measured) == 0 {
		return 0, 0
	}
	sort.Ints(measured)
	return measured[len(measured)/2], len(measured)
}

// BuildLearnPlan is the front-door analyzer: scan config + real history, compute the
// detectors, the Cave Score, rank by daily-equivalent magnitude, and persist
// sinks. Read-only over user config — it never edits a config file.
func (s *Store) BuildLearnPlan(cwd string, sources []string, sinceExpr string) (LearnPlan, error) {
	return s.BuildLearnPlanWithRetro(cwd, sources, sinceExpr, RetroOptions{})
}

// BuildLearnPlanWithRetro is BuildLearnPlan plus the opt-in retrospective pass.
// With retro disabled the two are the same code path and the same output bytes.
func (s *Store) BuildLearnPlanWithRetro(cwd string, sources []string, sinceExpr string, retro RetroOptions) (LearnPlan, error) {
	return s.buildLearnPlan(cwd, sources, sinceExpr, retro, "")
}

// BuildLearnPlanFilteredWithRetro applies a repository substring before turn
// events reach behavioral detectors. Config scanning intentionally remains
// rooted at cwd because --repo selects transcript history, not another config.
func (s *Store) BuildLearnPlanFilteredWithRetro(cwd string, sources []string, sinceExpr string, retro RetroOptions, repoFilter string) (LearnPlan, error) {
	return s.buildLearnPlan(cwd, sources, sinceExpr, retro, repoFilter)
}

func (s *Store) buildLearnPlan(cwd string, sources []string, sinceExpr string, retro RetroOptions, repoFilter string) (LearnPlan, error) {
	sourceSet := normalizeSources(sources)
	since := parseSince(sinceExpr)

	cfg := scanConfig(cwd)
	if _, err := s.InsertConfigSnapshots(cfg.Snapshots); err != nil {
		logStoreWarning(s.logger, "config snapshot persist failed", err)
	}

	beh, rec := behaviorScan{}, recurringResult{}
	behaviorTimeBoxed := false
	var deadline *behaviorDeadline
	if retro.Enabled {
		budgetMS := retro.BehaviorBudgetMS
		if budgetMS <= 0 {
			budgetMS = behaviorDefaultBudgetMS
		}
		deadline = &behaviorDeadline{at: behaviorClock().Add(time.Duration(budgetMS) * time.Millisecond)}
		beh, rec, behaviorTimeBoxed = s.scanBehaviorUntilForRepo(sourceSet, since, cfg, deadline, repoFilter)
	} else {
		beh, rec = s.scanBehaviorForRepo(sourceSet, since, cfg, repoFilter)
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
		observedTurns:    beh.Turns,
		computedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Sinks:            []Sink{},
		Caveats: []string{
			"Local learn results are inferred. No local number is promoted to verified; Cloud additionally requires supported provider-causal, provider-complete, catalog-priced active evidence.",
			"Config tax is reported as a forward rate from your current setup, never as tokens already wasted.",
		},
	}
	if len(beh.FallbackWindowSources) > 0 {
		plan.Caveats = append(plan.Caveats, "Some context window sizes are assumed per-provider defaults because exact shared-catalog matches were unavailable. Those turns still count toward depth percentages, but contribute no excess-token floor or cross-provider depth comparison.")
	}

	deadTokens, deadSkills := deadLoadSkills(cfg, beh)

	recurPerTurn := 0
	for _, e := range rec.Repaste {
		recurPerTurn += recurringPerTurn(e, beh.Turns)
	}

	plan.Sinks = append(plan.Sinks, configSinksWithBehavior(cfg, beh, turnsPerDay)...)
	// Cap emitted repaste sinks to the heaviest fingerprints so a long history
	// cannot flood the report; the Cave Score above still folds in the full
	// recurring weight, and the cap is disclosed (no silent truncation).
	cappedRec := rec
	if len(rec.Repaste) > maxRecurringSinkRows {
		cappedRec = recurringResult{Repaste: rec.Repaste[:maxRecurringSinkRows]}
		plan.Caveats = appendUnique(plan.Caveats, fmt.Sprintf(
			"%d additional recurring-context fingerprints below the top %d by recurring weight were omitted from the sink list; the Cave Score still counts their full weight.",
			len(rec.Repaste)-maxRecurringSinkRows, maxRecurringSinkRows))
	}
	plan.Sinks = append(plan.Sinks, recurringSinks(cappedRec, beh, turnsPerDay)...)
	plan.Sinks = append(plan.Sinks, learningLoopSinks(beh.LearningLoops)...)
	plan.Sinks = append(plan.Sinks, dumbzoneSink(beh)...)
	plan.Sinks = append(plan.Sinks, deadLoadSink(deadTokens, deadSkills, beh, turnsPerDay)...)
	plan.Sinks = append(plan.Sinks, subagentSink(beh)...)
	plan.Sinks = append(plan.Sinks, surfaceSink(cfg)...)
	plan.Sinks = append(plan.Sinks, crossProviderSinks(rec, beh)...)
	plan.Sinks = append(plan.Sinks, cacheChurnSink(beh.CacheHygieneSessions, cfg.PerTurnHooks)...)
	plan.Sinks = append(plan.Sinks, rereadWasteSink(beh.RereadSessions)...)
	plan.Sinks = append(plan.Sinks, compactionChurnSink(beh.CompactionSessions)...)
	plan.Sinks = append(plan.Sinks, mcpSurfaceSink(cfg, plan.Sinks)...)
	sectionsTimeBoxed := deadline != nil && deadline.expired()
	if !sectionsTimeBoxed {
		plan.Sinks = append(plan.Sinks, claudeMDSectionSinks(cfg, beh.SessionTexts)...)
	} else {
		plan.Caveats = appendUnique(plan.Caveats, "CLAUDE.md section-echo findings were omitted because the shared behavioral deadline expired before that corpus pass.")
	}
	// Spend is computed before the practice/rank/price pass so the two sinks it
	// feeds are ranked and priced like every other sink rather than appended
	// after the fact.
	plan.Spend = buildLearnSpend(beh.Spend, int(days))
	plan.Sinks = append(plan.Sinks, cacheEfficiencySink(plan.Spend, cfg.PerTurnHooks)...)
	plan.Sinks = append(plan.Sinks, toolPortfolioSink(beh.ToolPortfolio, plan.Spend)...)
	plan.Sinks = append(plan.Sinks, outcomeSink(beh.SessionOutcomes, plan.Spend)...)
	plan.Sinks = append(plan.Sinks, subagentSpendSink(beh.SubagentSpend, plan.Spend)...)
	plan.Sinks = append(plan.Sinks, procedureSinks(beh.Procedures, plan.Spend)...)
	if trendRows, trendErr := s.configTrendRows(since); trendErr != nil {
		logStoreWarning(s.logger, "config trend read failed", trendErr)
	} else {
		plan.Sinks = append(plan.Sinks, configTrendSink(trendRows, turnsPerDay, plan.Spend)...)
	}
	plan.sessionOutcomes = beh.SessionOutcomes
	if plan.Spend != nil {
		plan.Caveats = appendUnique(plan.Caveats, "Spend is provider-counted tokens priced at the dated catalog's published rates. It is what the scanned window cost, not a projection and not an invoice; a subscription plan's marginal cost is zero.")
	}

	addSectionConfigPaths(plan.Sinks, cfg)
	for i := range plan.Sinks {
		plan.Sinks[i].PracticeID = practiceIDForSink(plan.Sinks[i].SinkID)
	}

	rankLearnSinks(plan.Sinks, days)
	priceLearnSinks(plan.Sinks, plan.Spend, days)
	if prefix, sessions := beh.measuredPrefix(); prefix > 0 && sessions > 0 && cfg.configTaxPerTurn() > 0 {
		plan.Caveats = appendUnique(plan.Caveats, "Turn-1 context includes the first user prompt, so measured prefix is an upper bound of fixed prefix; median across sessions mitigates.")
	}

	plan.CaveScore = caveScore(cfg, beh, deadTokens, recurPerTurn)
	plan.Portfolio = buildLearnPortfolio(plan.Sinks, days)
	if !behaviorTimeBoxed {
		plan.Repos = learnRepos(beh.SessionMetrics)
	} else {
		plan.Caveats = appendUnique(plan.Caveats, "Repository summaries were omitted because the shared behavioral deadline truncated their primary event scan.")
	}
	confirmed, confirmedTimeBoxed := s.confirmedFixes(cfg, sourceSet, repoFilter, deadline)
	if !confirmedTimeBoxed {
		plan.Confirmed = confirmed
	} else {
		plan.Caveats = appendUnique(plan.Caveats, "Applied-fix confirmations were omitted because their single shared-deadline after-metrics scan was truncated.")
	}
	if strings.TrimSpace(repoFilter) != "" {
		plan.Caveats = appendUnique(plan.Caveats, fmt.Sprintf("Session history was filtered by repository substring %q before behavioral detection. Config scan still reads the invoking cwd.", repoFilter))
	}

	if len(plan.Sinks) == 0 {
		plan.Caveats = appendUnique(plan.Caveats, "No config-tax or behavioral sink found yet. Run caveman learn after supported agent sessions exist on disk, or from a repo with a CLAUDE.md.")
	}
	if beh.SessionsScanned == 0 {
		plan.Caveats = appendUnique(plan.Caveats, "No local session transcripts were scanned, so behavioral findings (dumbzone, subagents, dead-skill use) are not measured.")
	}
	if behaviorTimeBoxed {
		plan.Caveats = appendUnique(plan.Caveats, "The base behavioral scan hit its time budget, so Cave Score and behavioral findings cover partial history. Deadline-truncated repository, section-echo, and applied-fix blocks are omitted rather than zero-filled. The independent retro totals still name only sessions they measured.")
	}

	// The retro pass is a second, budget-bounded walk so the base scan above keeps
	// its exact timing and its exact output when --retro is absent.
	if retro.Enabled && strings.TrimSpace(repoFilter) == "" {
		plan.Retro = s.buildLearnRetro(sourceSet, since, sinceExpr, cfg.configTaxPerTurn(), retro)
	} else if retro.Enabled {
		plan.Caveats = appendUnique(plan.Caveats, "Retrospective replay is omitted with --repo because its independent file walker cannot apply the repository filter without changing learn_retro.go.")
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
	return s.LearnScanFilteredWithRetro(sources, sinceExpr, retro, "")
}

// LearnScanFilteredWithRetro is LearnScanWithRetro plus pre-detection repo
// filtering. Durable learnings are generated only from the filtered plan.
func (s *Store) LearnScanFilteredWithRetro(sources []string, sinceExpr string, retro RetroOptions, repoFilter string) (LearnPlan, error) {
	cwd, _ := os.Getwd()
	plan, err := s.BuildLearnPlanFilteredWithRetro(cwd, sources, sinceExpr, retro, repoFilter)
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
	return configSinksWithBehavior(cfg, behaviorScan{}, turnsPerDay)
}

func configSinksWithBehavior(cfg configScan, beh behaviorScan, turnsPerDay float64) []Sink {
	tax := cfg.configTaxPerTurn()
	var sinks []Sink
	if tax > 0 {
		userTokens, projectTokens := 0, 0
		if cfg.ClaudeMDUser != nil {
			userTokens = cfg.ClaudeMDUser.Tokens
		}
		if len(cfg.ClaudeMDProjects) > 0 {
			for _, snap := range cfg.ClaudeMDProjects {
				projectTokens += snap.Tokens
			}
		} else if cfg.ClaudeMDProject != nil {
			projectTokens = cfg.ClaudeMDProject.Tokens
		}
		evidence := map[string]any{
			"claude_md_user_tokens":    userTokens,
			"claude_md_project_tokens": projectTokens,
			"skill_desc_tokens":        cfg.SkillDescTokens,
			"skill_count":              len(cfg.Skills),
			"hook_count":               cfg.HookCount,
			"plugin_count":             cfg.PluginCount,
			"token_basis":              cfg.TokenBasis,
		}
		if cfg.PluginCount > 0 {
			// A plugin count is not a zero-token measurement. Plugin caches and
			// enablement schemas vary by host/version, so keep this explicitly
			// unmeasured until the effective catalog can be resolved truthfully.
			evidence["plugin_desc_tokens"] = nil
			evidence["plugin_token_measurement"] = "unavailable"
			evidence["config_tax_coverage"] = "partial"
		} else {
			evidence["config_tax_coverage"] = "static_sources"
		}
		if prefix, sessions := beh.measuredPrefix(); prefix > 0 && sessions > 0 {
			evidence["measured_prefix_tokens"] = prefix
			evidence["measured_prefix_sessions"] = sessions
			evidence["measured_prefix_source"] = retroSourceSessionUsage
			evidence["unexplained_prefix_tokens"] = max(0, prefix-tax)
		}
		sinks = append(sinks, Sink{
			SinkID:           "config_tax:baseline",
			Title:            fmt.Sprintf("Measured agent config loads ~%d tokens into every turn", tax),
			Class:            classLoadBearing,
			Basis:            observedLocal,
			TokensPerTurn:    int64(tax),
			TokensPerDayRate: rate(tax, turnsPerDay),
			Framing:          framingForward,
			Suggestion:       "Some of this is load-bearing. The reducible parts are broken out as their own sinks below.",
			Evidence:         evidence,
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
	evidence := map[string]any{
		"turns_over_50pct": beh.DumbzoneTurns,
		"total_turns":      beh.Turns,
		"pct":              int(pct + 0.5),
		"median_context":   beh.medianContext(),
	}
	if beh.DumbzoneExcessTokens > 0 {
		evidence["excess_tokens_observed"] = beh.DumbzoneExcessTokens
		evidence["excess_tokens_basis"] = "sum of provider-counted context above 50% of each model window"
	}
	return []Sink{{
		SinkID:        "context_dumbzone",
		Title:         fmt.Sprintf("%.0f%% of turns ran over %.0f%% of the model window", pct, dumbzoneFraction*100),
		Class:         classBehavioral,
		Basis:         observedLocal,
		TokensPerTurn: 0, TokensPerDayRate: 0,
		TokensObserved: beh.DumbzoneExcessTokens,
		Framing:        framingHistorical,
		Suggestion:     "Compact or split long sessions before the dumbzone; large context degrades quality well before the window limit.",
		Evidence:       evidence,
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
			"detection":        "structured",
		},
	}}
}

func rankLearnSinks(sinks []Sink, windowDays float64) {
	sort.SliceStable(sinks, func(i, j int) bool {
		iForward := sinks[i].TokensPerDayRate > 0
		jForward := sinks[j].TokensPerDayRate > 0
		if iForward != jForward {
			return iForward
		}
		if iForward {
			return sinks[i].TokensPerDayRate > sinks[j].TokensPerDayRate
		}
		return sinks[i].TokensObserved > sinks[j].TokensObserved
	})
}

func learnSinkDailyEquivalent(sink Sink, windowDays float64) float64 {
	if windowDays <= 0 {
		windowDays = 1
	}
	observedPerDay := float64(sink.TokensObserved) / windowDays
	return max(float64(sink.TokensPerDayRate), observedPerDay)
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

func crossProviderSinks(rec recurringResult, beh behaviorScan) []Sink {
	measuredSources := 0
	for _, count := range beh.SessionsBySource {
		if count > 0 {
			measuredSources++
		}
	}
	if measuredSources < 2 {
		return nil
	}

	var sinks []Sink
	type sourceDepth struct {
		id       string
		median   int
		sessions int
	}
	var depths []sourceDepth
	for source, peaks := range beh.SessionPeakPctBySource {
		if len(peaks) == 0 || beh.SessionsBySource[source] == 0 || beh.FallbackWindowSources[source] {
			continue
		}
		depths = append(depths, sourceDepth{id: source, median: medianInts(peaks), sessions: len(peaks)})
	}
	sort.Slice(depths, func(i, j int) bool {
		if depths[i].median != depths[j].median {
			return depths[i].median < depths[j].median
		}
		return depths[i].id < depths[j].id
	})
	if len(depths) >= 2 {
		shallower, deeper := depths[0], depths[len(depths)-1]
		if deeper.median > shallower.median && shallower.median > 0 {
			ratio := float64(deeper.median) / float64(shallower.median)
			sinks = append(sinks, Sink{
				SinkID: "cross_provider:depth",
				Title:  fmt.Sprintf("On this machine, your %s sessions peaked about %.1fx deeper into the model window than %s sessions", sourceDisplayName(deeper.id), ratio, sourceDisplayName(shallower.id)),
				Class:  classBehavioral, Basis: observedLocal, Framing: framingHistorical,
				Suggestion: "This local median comparison may help identify which agent workflows reach deep context; it does not prove the agent caused the difference.",
				Evidence: map[string]any{
					"comparison": "median_session_peak_pct", "deeper_source": deeper.id,
					"deeper_median_peak_pct": deeper.median, "deeper_sessions": deeper.sessions,
					"shallower_source": shallower.id, "shallower_median_peak_pct": shallower.median,
					"shallower_sessions": shallower.sessions,
				},
			})
		}
	}

	for _, entry := range rec.Repaste {
		rootSet := map[string]bool{}
		for _, locator := range entry.Locators {
			if locator.RootKind != "" && beh.SessionsBySource[locator.RootKind] > 0 {
				rootSet[locator.RootKind] = true
			}
		}
		if len(rootSet) < 2 {
			continue
		}
		roots := make([]string, 0, len(rootSet))
		for root := range rootSet {
			roots = append(roots, root)
		}
		sort.Strings(roots)
		sinks = append(sinks, Sink{
			SinkID: "cross_provider:repaste",
			Title:  fmt.Sprintf("Recurring block %s appeared in %d agents — one cavemem offload could cover all of them", entry.Fingerprint, len(roots)),
			Class:  classBehavioral, Basis: observedLocal, Framing: framingHistorical,
			Suggestion: "Consider one shared cavemem offload after verifying a sampled locator; recurrence is window-bounded, not proof the block is unneeded.",
			Evidence: map[string]any{
				"fingerprint": entry.Fingerprint, "root_kinds": roots,
				"agent_count": len(roots), "recurrence_sessions": entry.Sessions,
			},
		})
		break
	}
	return sinks
}

func medianInts(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	return sorted[len(sorted)/2]
}

func sourceDisplayName(source string) string {
	switch source {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "gemini":
		return "Gemini"
	case "opencode":
		return "opencode"
	case "aider":
		return "aider"
	default:
		return source
	}
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
	return s.scanBehaviorForRepo(sourceSet, since, cfg, "")
}

func (s *Store) scanBehaviorForRepo(sourceSet map[string]bool, since time.Time, cfg configScan, repoFilter string) (behaviorScan, recurringResult) {
	beh, rec, _ := s.scanBehaviorUntilForRepo(sourceSet, since, cfg, nil, repoFilter)
	return beh, rec
}

func (s *Store) scanBehaviorWithBudget(sourceSet map[string]bool, since time.Time, cfg configScan, budget time.Duration) (behaviorScan, recurringResult, bool) {
	return s.scanBehaviorWithBudgetForRepo(sourceSet, since, cfg, budget, "")
}

func (s *Store) scanBehaviorWithBudgetForRepo(sourceSet map[string]bool, since time.Time, cfg configScan, budget time.Duration, repoFilter string) (behaviorScan, recurringResult, bool) {
	deadline := &behaviorDeadline{at: behaviorClock().Add(budget)}
	return s.scanBehaviorUntilForRepo(sourceSet, since, cfg, deadline, repoFilter)
}

func (s *Store) scanBehaviorUntil(sourceSet map[string]bool, since time.Time, cfg configScan, deadline *behaviorDeadline) (behaviorScan, recurringResult, bool) {
	return s.scanBehaviorUntilForRepo(sourceSet, since, cfg, deadline, "")
}

func (s *Store) scanBehaviorUntilForRepo(sourceSet map[string]bool, since time.Time, cfg configScan, deadline *behaviorDeadline, repoFilter string) (behaviorScan, recurringResult, bool) {
	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}, SessionPeakPctBySource: map[string][]int{}}
	miner := newRecurringMiner()
	timeBoxed := false
	slugs := make([]string, 0, len(cfg.Skills))
	for _, sk := range cfg.Skills {
		slugs = append(slugs, strings.ToLower(filepath.Base(filepath.Dir(sk.Path))))
	}
	for _, base := range learnSessionSources() {
		if !sourceSet[base.id()] {
			continue
		}
		source := sessionSource(base)
		if strings.TrimSpace(repoFilter) != "" {
			source = repoFilteredSource{sessionSource: base, filter: repoFilter}
		}
		if deadline != nil && deadline.expired() {
			timeBoxed = true
			continue
		}
		peakStart := len(beh.SessionPeakPct)
		timeBoxed = scanSessionSourceUntil(source, since, slugs, &beh, miner, deadline) || timeBoxed
		if peakStart < len(beh.SessionPeakPct) {
			beh.SessionPeakPctBySource[source.id()] = append(
				beh.SessionPeakPctBySource[source.id()], beh.SessionPeakPct[peakStart:]...,
			)
		}
	}
	rec := miner.result()
	canonicalizeAiderRecurringResult(&rec)
	return beh, rec, timeBoxed
}

func scanClaudeSessionsUntil(root string, since time.Time, slugs []string, beh *behaviorScan, miner *recurringMiner, deadline *behaviorDeadline) bool {
	return scanSessionSourceUntil(claudeSessionSource{root: root}, since, slugs, beh, miner, deadline)
}

func scanClaudeTranscriptBehavior(path, relPath string, since time.Time, slugs []string, beh *behaviorScan, miner *recurringMiner) {
	_ = scanClaudeTranscriptBehaviorUntil(path, relPath, since, slugs, beh, miner, nil)
}

func scanClaudeTranscriptBehaviorUntil(path, relPath string, since time.Time, slugs []string, beh *behaviorScan, miner *recurringMiner, deadline *behaviorDeadline) bool {
	ref := sessionRef{path: path, relPath: relPath, repo: claudeRepoFromRelPath(relPath)}
	return scanOneSessionUntil(claudeSessionSource{}, ref, since, slugs, beh, miner, deadline)
}

var commandNameMarker = regexp.MustCompile(`(?i)<command-name>\s*/?([^<\s]+)\s*</command-name>`)

// recordClaudeStructuredSkillUse recognizes Claude Code's explicit skill-use
// surfaces. The caller still runs the raw substring guard after this parser so
// a future transcript shape cannot create a false dead-skill finding.
func knownSkillSlugs(slugs []string) map[string]string {
	known := make(map[string]string, len(slugs))
	for _, slug := range slugs {
		if normalized := normalizeSkillReference(slug); normalized != "" {
			known[normalized] = slug
		}
	}
	return known
}

func recordClaudeStructuredSkillUse(obj map[string]any, known map[string]string, seen map[string]bool) {
	if len(known) == 0 {
		return
	}
	for _, raw := range claudeStructuredSkillReferences(obj) {
		if slug, ok := known[normalizeSkillReference(raw)]; ok {
			seen[slug] = true
		}
	}
}

func claudeMessageTexts(content any) []string {
	switch typed := content.(type) {
	case string:
		return []string{typed}
	case []any:
		var texts []string
		for _, raw := range typed {
			block := asMap(raw)
			if text := firstString(block["text"]); text != "" {
				texts = append(texts, text)
			}
		}
		return texts
	default:
		return nil
	}
}

func normalizeSkillReference(raw string) string {
	ref := strings.ToLower(strings.TrimSpace(raw))
	if fields := strings.Fields(ref); len(fields) > 0 {
		ref = fields[0]
	}
	ref = strings.TrimPrefix(ref, "/")
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		ref = ref[i+1:]
	}
	return strings.Trim(ref, `"'`)
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
	return scanSessionSourceUntil(codexSessionSource{root: root}, since, nil, beh, newRecurringMiner(), deadline)
}

func scanCodexSessionBehavior(path string, since time.Time, beh *behaviorScan) {
	_ = scanCodexSessionBehaviorUntil(path, since, beh, nil)
}

func scanCodexSessionBehaviorUntil(path string, since time.Time, beh *behaviorScan, deadline *behaviorDeadline) bool {
	ref := sessionRef{path: path, relPath: filepath.Base(path)}
	return scanOneSessionUntil(codexSessionSource{}, ref, since, nil, beh, newRecurringMiner(), deadline)
}

func mergeBehaviorScan(dst, src *behaviorScan) {
	if dst == nil || src == nil {
		return
	}
	dst.Turns += src.Turns
	dst.DumbzoneTurns += src.DumbzoneTurns
	if sum, ok := checkedNonNegativeSum(dst.DumbzoneExcessTokens, src.DumbzoneExcessTokens); ok {
		dst.DumbzoneExcessTokens = sum
	}
	dst.Contexts = append(dst.Contexts, src.Contexts...)
	if dst.PrefixContexts == nil {
		dst.PrefixContexts = map[string][]int{}
	}
	for source, contexts := range src.PrefixContexts {
		dst.PrefixContexts[source] = append(dst.PrefixContexts[source], contexts...)
	}
	dst.SessionPeakPct = append(dst.SessionPeakPct, src.SessionPeakPct...)
	if dst.SessionPeakPctBySource == nil {
		dst.SessionPeakPctBySource = map[string][]int{}
	}
	for source, peaks := range src.SessionPeakPctBySource {
		dst.SessionPeakPctBySource[source] = append(dst.SessionPeakPctBySource[source], peaks...)
	}
	dst.TaskSpawns += src.TaskSpawns
	dst.SessionsScanned += src.SessionsScanned
	dst.SessionsWithTasks += src.SessionsWithTasks
	dst.LearningLoops = append(dst.LearningLoops, src.LearningLoops...)
	dst.CacheHygieneSessions = append(dst.CacheHygieneSessions, src.CacheHygieneSessions...)
	dst.RereadSessions = append(dst.RereadSessions, src.RereadSessions...)
	dst.CompactionSessions = append(dst.CompactionSessions, src.CompactionSessions...)
	remainingTexts := maxSectionSessions - len(dst.SessionTexts)
	if remainingTexts > len(src.SessionTexts) {
		remainingTexts = len(src.SessionTexts)
	}
	if remainingTexts > 0 {
		dst.SessionTexts = append(dst.SessionTexts, src.SessionTexts[:remainingTexts]...)
	}
	dst.SessionMetrics = append(dst.SessionMetrics, src.SessionMetrics...)
	dst.SessionOutcomes = append(dst.SessionOutcomes, src.SessionOutcomes...)
	dst.SubagentSpend.merge(src.SubagentSpend)
	dst.Procedures.merge(src.Procedures)
	dst.Spend.merge(src.Spend)
	dst.ToolPortfolio.merge(src.ToolPortfolio)
	if dst.FallbackWindowSources == nil {
		dst.FallbackWindowSources = map[string]bool{}
	}
	for source := range src.FallbackWindowSources {
		dst.FallbackWindowSources[source] = true
	}
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
			  (sink_id, title, class, basis, tokens_per_turn, tokens_per_day_rate, tokens_observed, framing, evidence_json, suggestion, computed_at)
			  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			  ON CONFLICT(sink_id) DO UPDATE SET
			    title = excluded.title, class = excluded.class, basis = excluded.basis,
			    tokens_per_turn = excluded.tokens_per_turn, tokens_per_day_rate = excluded.tokens_per_day_rate,
			    tokens_observed = excluded.tokens_observed,
			    framing = excluded.framing, evidence_json = excluded.evidence_json,
			    suggestion = excluded.suggestion, computed_at = excluded.computed_at`,
			sink.SinkID, sink.Title, sink.Class, sink.Basis, sink.TokensPerTurn, sink.TokensPerDayRate,
			sink.TokensObserved, sink.Framing, string(evidence), sink.Suggestion, time.Now().UTC().Format(time.RFC3339),
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
		set = map[string]bool{"codex": true, "claude": true, "gemini": true, "opencode": true, "aider": true, "caveman": true}
	}
	return set
}

func contextWindow(provider, model string) (int, bool) {
	if window, ok := catalog.ContextWindowTokens(provider, model); ok {
		return window, true
	}
	m := strings.ToLower(model)
	if strings.Contains(m, "1m") || strings.Contains(m, "[1m]") {
		return 1_000_000, false
	}
	switch provider {
	case "openai":
		return 400_000, false
	case "gemini":
		return 1_048_576, false
	default:
		return 200_000, false
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
