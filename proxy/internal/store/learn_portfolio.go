package store

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LearnPortfolio is presentation-layer grouping over sink ranking keys.
type LearnPortfolio struct {
	Groups       []LearnPortfolioGroup `json:"groups"`
	BestNextMove *LearnPortfolioGroup  `json:"best_next_move,omitempty"`
}

type LearnPortfolioGroup struct {
	FixLabel                 string   `json:"fix_label"`
	SinkIDs                  []string `json:"sink_ids"`
	CombinedRatePerDay       int64    `json:"combined_rate_per_day"`
	CombinedObservedInWindow int64    `json:"combined_observed_in_window"`
	TopSinkID                string   `json:"top_sink_id"`
	TopSinkTitle             string   `json:"top_sink_title"`
	Confidence               string   `json:"confidence"`
	NetNote                  string   `json:"net_note,omitempty"`
	groupKey                 string
	combinedKey              float64
	actionable               bool
}

// LearnRepo is one repository's measured transcript shape. Prefix is optional:
// absence means no provider-counted first turn was available, not zero tokens.
type LearnRepo struct {
	Repo                 string `json:"repo"`
	Sessions             int    `json:"sessions"`
	Turns                int    `json:"turns"`
	MedianContext        int    `json:"median_context"`
	DumbzonePct          int    `json:"dumbzone_pct"`
	MeasuredPrefixTokens *int   `json:"measured_prefix_tokens,omitempty"`
}

func learnSessionSources() []sessionSource {
	return []sessionSource{
		claudeSessionSource{root: claudeRoot()},
		codexSessionSource{root: codexRoot()},
		geminiSessionSource{root: geminiRoot()},
		opencodeSessionSource{root: opencodeRoot()},
		aiderSessionSource{root: aiderRoot()},
	}
}

// repoFilteredSource buffers only until a repository is known, then replays the
// pending session-start event and all later events into detectors. A non-match
// never reaches detector state, satisfying the "filter before detection" rule.
type repoFilteredSource struct {
	sessionSource
	filter string
}

const maxRepoFilterPendingEvents = 64

func (s repoFilteredSource) discover(deadline *behaviorDeadline) ([]sessionRef, bool) {
	refs, timeBoxed := s.sessionSource.discover(deadline)
	filtered := refs[:0]
	for _, ref := range refs {
		// Gemini transcripts expose only an opaque project hash and never emit cwd.
		// With --repo they cannot match honestly, so reject before any buffering.
		if ref.repo == "" && s.sessionSource.id() == "gemini" {
			continue
		}
		if ref.repoProvisional || ref.repo == "" || repoMatches(ref.repo, s.filter) {
			filtered = append(filtered, ref)
		}
	}
	return filtered, timeBoxed
}

func (s repoFilteredSource) scanSession(ref sessionRef, since time.Time, emit func(turnEvent), deadline *behaviorDeadline) bool {
	if s.filter == "" {
		return s.sessionSource.scanSession(ref, since, emit, deadline)
	}
	decided := ref.repo != "" && !ref.repoProvisional
	matched := decided && repoMatches(ref.repo, s.filter)
	pending := make([]turnEvent, 0, 2)
	wrapped := func(event turnEvent) {
		if decided {
			if matched {
				emit(event)
			}
			return
		}
		pending = append(pending, event)
		if len(pending) > maxRepoFilterPendingEvents {
			decided = true
			matched = false
			pending = nil
			return
		}
		if event.Repo == "" || (event.sessionStart && ref.repoProvisional) {
			return
		}
		decided = true
		matched = repoMatches(event.Repo, s.filter)
		if matched {
			for _, buffered := range pending {
				emit(buffered)
			}
		}
		pending = nil
	}
	return s.sessionSource.scanSession(ref, since, wrapped, deadline)
}

func repoMatches(repo, filter string) bool {
	filter = strings.TrimSpace(filter)
	return filter == "" || strings.Contains(strings.ToLower(filepath.Clean(repo)), strings.ToLower(filter))
}

type learnSessionMetric struct {
	Repo         string
	Source       string
	Observed     bool
	Turns        int
	Contexts     []int
	Dumbzone     int
	Prefix       int
	Fingerprints map[string]int
}

// scanLearnSessionMetrics is an independent small aggregation over normalized
// turn events. strictAfter excludes undated/equal timestamps and is used by the
// outcome ledger, where "after" must mean after applied_at exactly.
func scanLearnSessionMetrics(sourceSet map[string]bool, since time.Time, repoFilter string, strictAfter bool) []learnSessionMetric {
	metrics, _ := scanLearnSessionMetricsUntil(sourceSet, since, repoFilter, strictAfter, nil)
	return metrics
}

func scanLearnSessionMetricsUntil(sourceSet map[string]bool, since time.Time, repoFilter string, strictAfter bool, deadline *behaviorDeadline) ([]learnSessionMetric, bool) {
	var all []learnSessionMetric
	timeBoxed := false
	for _, base := range learnSessionSources() {
		if !sourceSet[base.id()] {
			continue
		}
		source := sessionSource(base)
		if repoFilter != "" {
			source = repoFilteredSource{sessionSource: base, filter: repoFilter}
		}
		refs, truncated := source.discover(deadline)
		timeBoxed = timeBoxed || truncated
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].relPath != refs[j].relPath {
				return refs[i].relPath < refs[j].relPath
			}
			return refs[i].path < refs[j].path
		})
		metrics := make([]learnSessionMetric, len(refs))
		truncatedSessions := make([]bool, len(refs))
		parallelSessionScan(len(refs), func(i int) {
			if deadline != nil && deadline.expired() {
				truncatedSessions[i] = true
				return
			}
			metric := learnSessionMetric{Repo: refs[i].repo, Source: source.id(), Fingerprints: map[string]int{}}
			seenUsage := map[string]bool{}
			truncatedSessions[i] = source.scanSession(refs[i], since, func(event turnEvent) {
				if event.sessionStart {
					return
				}
				if event.Repo != "" {
					metric.Repo = event.Repo
				}
				if strictAfter && (event.Timestamp.IsZero() || !event.Timestamp.After(since)) {
					return
				}
				if !event.Timestamp.IsZero() || event.ContextUsagePresent || len(event.TextPayloads) > 0 || len(event.ToolCalls) > 0 {
					metric.Observed = true
				}
				if event.ContextUsagePresent && (event.UsageMessageID == "" || !seenUsage[event.UsageMessageID]) {
					if event.UsageMessageID != "" {
						seenUsage[event.UsageMessageID] = true
					}
					metric.Turns++
					metric.Contexts = append(metric.Contexts, event.ContextTotal)
					if metric.Prefix == 0 {
						metric.Prefix = event.ContextTotal
					}
					window, _ := contextWindow(event.ProviderKey, event.Model)
					if event.ContextTotal > int(dumbzoneFraction*float64(window)) {
						metric.Dumbzone++
					}
				}
				for _, payload := range event.TextPayloads {
					for _, block := range segmentBlocks(payload) {
						if estimateTokens(block) < minBlockTokens {
							continue
						}
						metric.Fingerprints[hashText(normalizeBlock(block))]++
					}
				}
			}, deadline)
			metrics[i] = metric
		})
		for i, metric := range metrics {
			timeBoxed = timeBoxed || truncatedSessions[i]
			if metric.Observed && strings.TrimSpace(metric.Repo) != "" {
				all = append(all, metric)
			}
		}
	}
	return all, timeBoxed
}

func learnRepos(metrics []learnSessionMetric) []LearnRepo {
	type aggregate struct {
		sessions, turns, dumbzone int
		contexts, prefixes        []int
	}
	byRepo := map[string]*aggregate{}
	for _, metric := range metrics {
		repo := filepath.Clean(metric.Repo)
		if repo == "." || repo == "" {
			continue
		}
		agg := byRepo[repo]
		if agg == nil {
			agg = &aggregate{}
			byRepo[repo] = agg
		}
		agg.sessions++
		agg.turns += metric.Turns
		agg.dumbzone += metric.Dumbzone
		agg.contexts = append(agg.contexts, metric.Contexts...)
		if metric.Prefix > 0 {
			agg.prefixes = append(agg.prefixes, metric.Prefix)
		}
	}
	var repos []LearnRepo
	for repo, agg := range byRepo {
		if agg.sessions < 2 || agg.turns == 0 || len(agg.contexts) == 0 {
			continue
		}
		row := LearnRepo{Repo: repo, Sessions: agg.sessions, Turns: agg.turns, MedianContext: medianInts(agg.contexts)}
		if agg.turns > 0 {
			row.DumbzonePct = int(math.Floor(float64(agg.dumbzone)/float64(agg.turns)*100 + 0.5))
		}
		if len(agg.prefixes) > 0 {
			prefix := medianInts(agg.prefixes)
			row.MeasuredPrefixTokens = &prefix
		}
		repos = append(repos, row)
	}
	if len(repos) < 2 {
		return nil
	}
	sort.Slice(repos, func(i, j int) bool {
		if repos[i].Sessions != repos[j].Sessions {
			return repos[i].Sessions > repos[j].Sessions
		}
		return repos[i].Repo < repos[j].Repo
	})
	if len(repos) > 12 {
		repos = repos[:12]
	}
	return repos
}

func buildLearnPortfolio(sinks []Sink, windowDays float64) *LearnPortfolio {
	type groupState struct {
		group LearnPortfolioGroup
		rung  int
	}
	groups := map[string]*groupState{}
	ranked := append([]Sink(nil), sinks...)
	rankLearnSinks(ranked, windowDays)
	for _, sink := range ranked {
		family, label := learnFixFamily(sink)
		if sink.PracticeID == "" || family == "" {
			continue
		}
		key := sink.PracticeID + "\x00" + family
		state := groups[key]
		if state == nil {
			state = &groupState{group: LearnPortfolioGroup{
				FixLabel: label, groupKey: key, TopSinkID: sink.SinkID, TopSinkTitle: sink.Title,
			}}
			groups[key] = state
		}
		state.group.SinkIDs = append(state.group.SinkIDs, sink.SinkID)
		state.group.CombinedRatePerDay += sink.TokensPerDayRate
		state.group.CombinedObservedInWindow += sink.TokensObserved
		state.group.combinedKey += learnSinkDailyEquivalent(sink, windowDays)
		state.group.actionable = state.group.actionable || sink.Class == classReducible || sink.Class == classRecurringContext
		if family == "cavemem_offload" {
			state.group.NetNote = "Net effect must subtract pointer and real cavemem recall cost; apply only when measured net-token-negative."
		}
		if rung := sinkConfidenceRung(sink); rung > state.rung {
			state.rung = rung
		}
	}
	if len(groups) == 0 {
		return nil
	}
	portfolio := &LearnPortfolio{}
	states := make([]*groupState, 0, len(groups))
	for _, state := range groups {
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].group.combinedKey != states[j].group.combinedKey {
			return states[i].group.combinedKey > states[j].group.combinedKey
		}
		return states[i].group.groupKey < states[j].group.groupKey
	})
	for _, state := range states {
		sort.Strings(state.group.SinkIDs)
		state.group.Confidence = []string{"static_estimate", "transcript_inferred", "measured_usage"}[state.rung]
		portfolio.Groups = append(portfolio.Groups, state.group)
		candidate := state.group
		if !candidate.actionable {
			continue
		}
		if portfolio.BestNextMove == nil || candidate.combinedKey > portfolio.BestNextMove.combinedKey ||
			(candidate.combinedKey == portfolio.BestNextMove.combinedKey && candidate.groupKey < portfolio.BestNextMove.groupKey) {
			portfolio.BestNextMove = &candidate
		}
	}
	for i := range portfolio.Groups {
		portfolio.Groups[i].groupKey = ""
		portfolio.Groups[i].combinedKey = 0
		portfolio.Groups[i].actionable = false
	}
	if portfolio.BestNextMove != nil {
		portfolio.BestNextMove.groupKey = ""
		portfolio.BestNextMove.combinedKey = 0
		portfolio.BestNextMove.actionable = false
	}
	return portfolio
}

func learnFixFamily(sink Sink) (family, label string) {
	if fix, _ := sink.Evidence["fix_kind"].(string); fix != "" {
		if fix == "cavemem_offload" {
			return fix, "Offload recurring context to cavemem"
		}
		return fix, strings.ReplaceAll(fix, "_", " ")
	}
	switch {
	case strings.HasPrefix(sink.SinkID, "claude_md_weight:"), strings.HasPrefix(sink.SinkID, "claude_md_sections:"):
		return "config_trim", "Trim loaded config"
	case sink.SinkID == "config_tax:baseline":
		return "config_baseline", "Loaded config baseline"
	case sink.SinkID == "dead_load:skills":
		return "skill_gating", "Gate unused skill descriptions"
	case sink.SinkID == "context_dumbzone":
		return "dumbzone_advice", "Reduce long-session dumbzone exposure"
	default:
		return "", ""
	}
}

func sinkConfidenceRung(sink Sink) int {
	if sink.TokensObserved > 0 || fmt.Sprint(sink.Evidence["measured_prefix_source"]) == retroSourceSessionUsage {
		return 2
	}
	for _, key := range []string{"sessions_scanned", "sessions_checked", "recurrence_sessions", "total_turns", "task_spawns"} {
		if value, ok := sink.Evidence[key]; ok && fmt.Sprint(value) != "0" {
			return 1
		}
	}
	return 0
}
