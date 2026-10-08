package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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
	TurnsBySource          map[string]int // every counted turn by source, repo or not
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
	TrendSessions          []trendSession
	SubagentSpend          subagentSpendTracker
	Procedures             procedureMiner
	Spend                  spendAccumulator
	ToolPortfolio          toolPortfolioTracker
	FallbackWindowSources  map[string]bool
	InferredWindowSources  map[string]bool // sessions whose context outgrew the assumed window
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
			"Every number here is an estimate from your own history on this computer. None of it counts as verified. Caveman Cloud needs more before it calls a number verified: complete usage from the provider, a known price, and proof that a change caused the result.",
			"Instruction costs show what your current setup adds from now on. They are not tokens already wasted.",
		},
	}
	if len(beh.FallbackWindowSources) > 0 {
		plan.Caveats = append(plan.Caveats, "For some models Caveman did not know the exact context window size, so it used a default size for that provider. Those messages still count in the how-full numbers. They are left out of the count of tokens past the halfway mark and out of agent-to-agent comparisons.")
	}
	if len(beh.InferredWindowSources) > 0 {
		plan.Caveats = append(plan.Caveats, "Some sessions grew bigger than the window size Caveman assumed (transcripts often leave out that a model has a 1M window). For those sessions Caveman assumed the next bigger window and measured every message against it.")
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
			"Only the %[2]d biggest pieces of repeated text are listed. %[1]d smaller ones are left off the list, but the Setup Score still counts all of them.",
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
	plan.Sinks = append(plan.Sinks, memoryHealthSinks(cwd, turnsPerDay)...)
	sectionsTimeBoxed := deadline != nil && deadline.expired()
	if !sectionsTimeBoxed {
		plan.Sinks = append(plan.Sinks, claudeMDSectionSinks(cfg, beh.SessionTexts)...)
	} else {
		plan.Caveats = appendUnique(plan.Caveats, "The check for unused CLAUDE.md sections was skipped because the scan ran out of time.")
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
		plan.Sinks = append(plan.Sinks, configTrendSink(trendRows, beh, turnsPerDay, plan.Spend)...)
	}
	plan.sessionOutcomes = beh.SessionOutcomes
	if plan.Spend != nil {
		plan.Caveats = appendUnique(plan.Caveats, "Cost is the tokens your model provider counted, priced at published list prices. It covers only the period scanned. It is not a forecast and not a bill. On a subscription plan you pay nothing extra per token.")
	}

	addSectionConfigPaths(plan.Sinks, cfg)
	for i := range plan.Sinks {
		plan.Sinks[i].PracticeID = practiceIDForSink(plan.Sinks[i].SinkID)
	}

	rankLearnSinks(plan.Sinks, days)
	priceLearnSinks(plan.Sinks, plan.Spend, days)
	if prefix, sessions := beh.measuredPrefix(); prefix > 0 && sessions > 0 && cfg.configTaxPerTurn() > 0 {
		plan.Caveats = appendUnique(plan.Caveats, "The first-message size includes your first prompt, so it can overstate how big your fixed setup is. Using the middle value across sessions keeps one long prompt from skewing it.")
	}

	plan.CaveScore = caveScore(cfg, beh, deadTokens, recurPerTurn)
	plan.Portfolio = buildLearnPortfolio(plan.Sinks, days)
	if !behaviorTimeBoxed {
		plan.Repos = learnRepos(beh.SessionMetrics)
	} else {
		plan.Caveats = appendUnique(plan.Caveats, "Per-repository summaries were skipped because the scan ran out of time.")
	}
	if !behaviorTimeBoxed {
		plan.Trends = buildLearnTrends(beh.TrendSessions, since, sinceClock())
	}
	confirmed, confirmedTimeBoxed := s.confirmedFixes(cfg, sourceSet, repoFilter, deadline)
	if !confirmedTimeBoxed {
		plan.Confirmed = confirmed
	} else {
		plan.Caveats = appendUnique(plan.Caveats, "Results for fixes you already applied were skipped because the scan ran out of time.")
	}
	if strings.TrimSpace(repoFilter) != "" {
		plan.Caveats = appendUnique(plan.Caveats, fmt.Sprintf("Only sessions whose repository matches %q were read. The setup check still reads the folder you ran this from.", repoFilter))
	}

	if len(plan.Sinks) == 0 {
		plan.Caveats = appendUnique(plan.Caveats, "Nothing found yet. Run caveman learn again after your agents have saved some sessions, or run it in a repo that has a CLAUDE.md.")
	}
	if beh.SessionsScanned == 0 {
		plan.Caveats = appendUnique(plan.Caveats, "No session history was found on this computer, so habits (overloaded messages, subagents, unused skills) were not measured.")
	}
	if behaviorTimeBoxed {
		plan.Caveats = appendUnique(plan.Caveats, "The scan ran out of time, so the Setup Score and habits cover only part of your history. Parts cut short (per-repository summaries, unused CLAUDE.md sections, applied fixes) are left out, not shown as zero. The past-session replay still counts only sessions it fully read.")
	}

	// The retro pass is a second, budget-bounded walk so the base scan above keeps
	// its exact timing and its exact output when --retro is absent.
	if retro.Enabled && strings.TrimSpace(repoFilter) == "" {
		plan.Retro = s.buildLearnRetro(sourceSet, since, sinceExpr, cfg.configTaxPerTurn(), retro)
	} else if retro.Enabled {
		plan.Caveats = appendUnique(plan.Caveats, "The past-session replay was skipped because it cannot filter by repository (--repo).")
	}

	if plan.WrapMeasured = s.wrapMeasuredSince(since); plan.WrapMeasured != nil {
		plan.WrapMeasured.WindowDays = int(windowDays(sinceExpr, "", ""))
		plan.Caveats = appendUnique(plan.Caveats, fmt.Sprintf("Saved so far counts tokens with Caveman's own counter (%s), over requests Caveman handled in this period. Tokens only, no dollars. It is kept apart from could have saved: different requests, a different method, never added together.", plan.WrapMeasured.Basis))
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
	err := s.db.QueryRow(`SELECT COALESCE(SUM(CASE WHEN `+cacheWarmRow+` THEN 0 ELSE 1 END),0),
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
			Title:            fmt.Sprintf("Your always-loaded setup (CLAUDE.md, skills, hooks) adds ~%s tokens to every message", commaInt(int64(tax))),
			Class:            classLoadBearing,
			Basis:            observedLocal,
			TokensPerTurn:    int64(tax),
			TokensPerDayRate: rate(tax, turnsPerDay),
			Framing:          framingForward,
			Suggestion:       "Much of this is needed. The parts you can trim are listed as separate findings.",
			Evidence:         evidence,
		})
	}
	sinks = append(sinks, scopedClaudeMDSink(cfg.ClaudeMDUser, "user", beh, turnsPerDay)...)
	sinks = append(sinks, scopedClaudeMDSink(cfg.ClaudeMDProject, "project", beh, turnsPerDay)...)
	sinks = append(sinks, scopedClaudeMDSink(cfg.CodexAgents, "codex", beh, turnsPerDay)...)
	return sinks
}

// configTurnsPerDay is the turn rate of the sessions that actually load one
// config file: Claude Code reads CLAUDE.md and the Claude root's skills, Codex
// reads AGENTS.md, and a project-scoped file loads only in sessions whose cwd is
// at or under its directory. Multiplying by every scanned session's turns
// charged a file for traffic that never loaded it. Without per-session metrics
// it falls back to the all-session rate and says so; a project file no scanned
// session ran under gets basis "no_matching_sessions" rather than a silent 0.
func configTurnsPerDay(scope, kind, path string, beh behaviorScan, turnsPerDay float64) (float64, string) {
	var source string
	switch kind {
	case "claude_md", "skill_desc", "hooks", "plugins":
		source = "claude"
	case "agents_md":
		source = "codex"
	}
	if source == "" || beh.Turns == 0 {
		return turnsPerDay, "all_scanned_sessions"
	}
	if scope != "project" {
		// TurnsBySource counts every turn, including sessions with no repo,
		// which SessionMetrics drops; both sides of the ratio share one basis.
		if beh.TurnsBySource == nil {
			return turnsPerDay, "all_scanned_sessions"
		}
		return turnsPerDay * float64(beh.TurnsBySource[source]) / float64(beh.Turns), source + "_sessions"
	}
	if len(beh.SessionMetrics) == 0 {
		return turnsPerDay, "all_scanned_sessions"
	}
	dir := canonicalDir(filepath.Dir(path))
	seen := map[string]string{}
	turns := 0
	for _, m := range beh.SessionMetrics {
		if m.Source != source {
			continue
		}
		repo, ok := seen[m.Repo]
		if !ok {
			repo = canonicalDir(m.Repo)
			seen[m.Repo] = repo
		}
		rel, err := filepath.Rel(dir, repo)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		turns += m.Turns
	}
	if turns == 0 {
		return 0, "no_matching_sessions"
	}
	return turnsPerDay * float64(turns) / float64(beh.Turns), source + "_sessions_under_project"
}

// canonicalDir resolves symlinks so a config path reached through a link
// ($PWD form, /tmp vs /private/tmp) matches the resolved cwd a transcript
// records, and case-folds on macOS, whose default filesystem ignores case.
func canonicalDir(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "darwin" {
		path = strings.ToLower(path)
	}
	return path
}

// scopedClaudeMDSink is claudeMDSink at the turn rate of the sessions that load
// the file, with that basis recorded in evidence.
func scopedClaudeMDSink(snap *ConfigSnapshot, scope string, beh behaviorScan, turnsPerDay float64) []Sink {
	if snap == nil {
		return nil
	}
	rate, basis := configTurnsPerDay(snap.Scope, snap.Kind, snap.Path, beh, turnsPerDay)
	sinks := claudeMDSink(snap, scope, rate)
	for i := range sinks {
		sinks[i].Evidence["turns_per_day_basis"] = basis
		if basis == "no_matching_sessions" {
			sinks[i].Suggestion += " No scanned session was matched to this project, so its tokens a day could not be measured."
		}
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
	title := fmt.Sprintf("%s %s is %s (~%s tokens), loaded with every message", label, kind, plural(snap.Lines, "line"), commaInt(int64(snap.Tokens)))
	if scope == "codex" {
		title = fmt.Sprintf("%s is %s (~%s tokens), loaded with every message", label, plural(snap.Lines, "line"), commaInt(int64(snap.Tokens)))
	}
	return []Sink{{
		SinkID:           "claude_md_weight:" + scope,
		Title:            title,
		Class:            classReducible,
		Basis:            observedLocal,
		TokensPerTurn:    int64(snap.Tokens),
		TokensPerDayRate: rate(snap.Tokens, turnsPerDay),
		Framing:          framingForward,
		Suggestion:       fmt.Sprintf("Cut it down to the sections your agent actually uses. Aim for under %d lines.", claudeMDLineBudget),
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
		Title:         fmt.Sprintf("%.0f%% of messages went past %.0f%% of the model's context window", pct, dumbzoneFraction*100),
		Class:         classBehavioral,
		Basis:         observedLocal,
		TokensPerTurn: 0, TokensPerDayRate: 0,
		TokensObserved: beh.DumbzoneExcessTokens,
		Framing:        framingHistorical,
		Suggestion:     "Start a fresh session, or compact, before you pass half the window. Answers tend to get worse well before the window is full.",
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
		Title:            fmt.Sprintf("%s add ~%s tokens to every message, with no use seen in %s", plural(len(deadSkills), "skill"), commaInt(int64(deadTokens)), plural(beh.SessionsScanned, "session")),
		Class:            classReducible,
		Basis:            observedLocal,
		TokensPerTurn:    int64(deadTokens),
		TokensPerDayRate: rate(deadTokens, turnsPerDay),
		Framing:          framingForward,
		Suggestion:       "Consider turning off skills you don't use. Each skill's description loads with every message. Not seeing a skill used in these sessions doesn't prove you never need it.",
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
		Title:         fmt.Sprintf("You started %s across %s (about %d per session that used them)", plural(beh.TaskSpawns, "subagent"), plural(beh.SessionsWithTasks, "session"), median),
		Class:         classBehavioral,
		Basis:         observedLocal,
		TokensPerTurn: 0, TokensPerDayRate: 0,
		Framing:    framingHistorical,
		Suggestion: "Each subagent carries its own context. For a one-file lookup, reading the file directly is cheaper. This only counts subagents. Caveman never says one was unnecessary.",
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
		Suggestion: "Hooks that run at session start or on each prompt add their output to the context. Removing the ones you don't use makes every message smaller.",
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
		if len(peaks) == 0 || beh.SessionsBySource[source] == 0 || beh.FallbackWindowSources[source] || beh.InferredWindowSources[source] {
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
				Title:  fmt.Sprintf("On this computer, your %s sessions filled about %.1fx more of the context window at their peak than your %s sessions", sourceDisplayName(deeper.id), ratio, sourceDisplayName(shallower.id)),
				Class:  classBehavioral, Basis: observedLocal, Framing: framingHistorical,
				Suggestion: "This compares typical sessions on this computer. It can hint at which agent workflows run long. It does not prove the agent caused the difference.",
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
			Title:  fmt.Sprintf("The same text (block %s) showed up in %d agents — one move to Caveman memory could cover all of them", entry.Fingerprint, len(roots)),
			Class:  classBehavioral, Basis: observedLocal, Framing: framingHistorical,
			Suggestion: "Consider moving it to Caveman memory once, after checking one of the places it appears. Repeating in these sessions doesn't prove the text is unneeded.",
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
			cTax.Detail = fmt.Sprintf("%s tokens of setup + %s of repeated text in every message; a typical message is %s tokens", commaInt(int64(tax)), commaInt(int64(recurPerTurn)), commaInt(int64(median)))
		} else {
			cTax.Detail = fmt.Sprintf("%s tokens of setup in every message; a typical message is %s tokens", commaInt(int64(tax)), commaInt(int64(median)))
		}
	} else {
		cTax.Detail = "not measured (needs setup files and session history)"
	}
	components = append(components, cTax)
	score -= cTax.Penalty

	// dumbzone: share of turns over the window fraction.
	cDz := ScoreComponent{Key: scoreKeyDumbzone}
	if beh.Turns > 0 {
		cDz.Measured = true
		dzRate := float64(beh.DumbzoneTurns) / float64(beh.Turns)
		cDz.Penalty = capped(wDumbzone*dzRate, capDumbzone)
		cDz.Detail = fmt.Sprintf("%s of %s messages (%.0f%%) went past half the context window", commaInt(int64(beh.DumbzoneTurns)), commaInt(int64(beh.Turns)), dzRate*100)
	} else {
		cDz.Detail = "not measured (no session history)"
	}
	components = append(components, cDz)
	score -= cDz.Penalty

	// dead_load: dead-skill tokens as a share of the config tax.
	cDead := ScoreComponent{Key: scoreKeyDeadLoad}
	if beh.SessionsScanned > 0 && tax > 0 {
		cDead.Measured = true
		deadRatio := float64(deadTokens) / float64(tax)
		cDead.Penalty = capped(wDeadLoad*deadRatio, capDeadLoad)
		cDead.Detail = fmt.Sprintf("%s of the %s setup tokens are skills with no use seen", commaInt(int64(deadTokens)), commaInt(int64(tax)))
	} else {
		cDead.Detail = "not measured (needs skills and session history)"
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
		cSub.Detail = fmt.Sprintf("%s started in %s", plural(beh.TaskSpawns, "subagent"), plural(beh.SessionsScanned, "session"))
	} else {
		cSub.Detail = "not measured (no session history)"
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
	if len(src.TurnsBySource) > 0 && dst.TurnsBySource == nil {
		dst.TurnsBySource = map[string]int{}
	}
	for source, n := range src.TurnsBySource {
		dst.TurnsBySource[source] += n
	}
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
	dst.TrendSessions = append(dst.TrendSessions, src.TrendSessions...)
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
	for source := range src.InferredWindowSources {
		if dst.InferredWindowSources == nil {
			dst.InferredWindowSources = map[string]bool{}
		}
		dst.InferredWindowSources[source] = true
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

// windowTurn is one usage-bearing turn measured against its assumed window.
type windowTurn struct {
	ctx, window int
	exact       bool
}

// sessionWindows buffers a session's per-turn context sizes so dumbzone and
// peak depth are judged after the whole session is seen. Claude Code records
// "claude-opus-5-5" with no [1m] suffix even on a 1M window, so a session whose
// context outgrows its assumed window proves the window is larger; every turn
// of that session is then measured against the inferred window, not just the
// turns past the old limit.
type sessionWindows struct {
	turns []windowTurn
	peak  map[int]int // assumed window -> largest context seen against it
}

func (s *sessionWindows) add(provider, model string, ctx int) windowTurn {
	window, exact := contextWindow(provider, model)
	t := windowTurn{ctx: ctx, window: window, exact: exact}
	s.turns = append(s.turns, t)
	if s.peak == nil {
		s.peak = map[int]int{}
	}
	s.peak[window] = max(s.peak[window], ctx)
	return t
}

// resolve returns the window a turn is measured against. inferred is true when
// the session's own context exceeded the assumed window; exact still reports
// whether that assumed window came from the catalog, so an inferred catalog
// session is not mistaken for a missing catalog match.
func (s *sessionWindows) resolve(t windowTurn) (window int, exact, inferred bool) {
	observed := s.peak[t.window]
	if observed <= t.window {
		return t.window, t.exact, false
	}
	for _, next := range []int{1_000_000, 2_000_000} {
		if next > t.window && next >= observed {
			return next, t.exact, true
		}
	}
	return observed, t.exact, true
}

// dumbzone counts turns past the dumbzone line of their resolved window.
func (s *sessionWindows) dumbzone(turns []windowTurn) int {
	n := 0
	for _, t := range turns {
		if window, _, _ := s.resolve(t); t.ctx > int(dumbzoneFraction*float64(window)) {
			n++
		}
	}
	return n
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
