package store

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxSectionSessions  = 64
	maxSectionTextBytes = 256 << 10
)

// sessionSource isolates provider transcript formats from learn detectors.
// Sources stream normalized events; detector logic below stays provider-blind.
type sessionSource interface {
	id() string
	discover(deadline *behaviorDeadline) ([]sessionRef, bool)
	scanSession(ref sessionRef, since time.Time, emit func(turnEvent), deadline *behaviorDeadline) bool
}

type sessionRef struct {
	path    string
	relPath string
	repo    string
	// repoProvisional marks path-derived guesses that a transcript cwd may replace.
	repoProvisional bool
	data            any
}

type turnToolCall struct {
	Name         string
	InputSummary string
	OutputText   string
	IsError      bool
}

// turnEvent is deliberately internal: it carries only detector inputs and
// locator coordinates, never persists raw transcript content.
type turnEvent struct {
	Timestamp                time.Time
	ContextTotal             int
	ContextUsagePresent      bool
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	CacheUsagePresent        bool
	// Billing buckets are DISJOINT and provider-counted, normalized per adapter
	// because sources disagree on whether input_tokens already includes the
	// cached share (claude/opencode: disjoint sum; codex/gemini: inclusive).
	// InputFreshTokens is uncached input only. A source that cannot state a
	// bucket leaves BillingUsagePresent false rather than guessing a zero.
	InputFreshTokens    int
	OutputTokens        int
	BillingUsagePresent bool
	UsageMessageID      string
	Model               string
	ProviderKey         string
	ToolCalls           []turnToolCall
	TextPayloads        []string
	TaskSpawns          int
	SkillUses           []string
	Compaction          bool
	JSONLLine           int
	RelPath             string
	Repo                string
	Side                bool
	sessionStart        bool
}

type sessionEventConsumer struct {
	sourceID       string
	ref            sessionRef
	slugs          []string
	knownSlugs     map[string]string
	behavior       *behaviorScan
	miner          *recurringMiner
	seenUsage      map[string]bool
	seenSlugs      map[string]bool
	prefixRecorded bool
	sessionPeakPct int
	sessionTasks   int
	toolCalls      []learnToolCall
	cacheHygiene   cacheHygieneTracker
	readActivity   readActivityTracker
	toolPortfolio  toolPortfolioTracker
	subagentSpend  subagentSpendTracker
	procedures     procedureMiner
	outcome        sessionOutcome
	textPayload    strings.Builder
	captureText    bool
	metric         learnSessionMetric
	repo           string
	opened         bool
}

func newSessionEventConsumer(sourceID string, ref sessionRef, slugs []string, behavior *behaviorScan, miner *recurringMiner) *sessionEventConsumer {
	if behavior.SkillUse == nil {
		behavior.SkillUse = map[string]int{}
	}
	if behavior.SessionsBySource == nil {
		behavior.SessionsBySource = map[string]int{}
	}
	return &sessionEventConsumer{
		sourceID: sourceID, ref: ref, slugs: slugs, knownSlugs: knownSkillSlugs(slugs),
		behavior: behavior, miner: miner, seenUsage: map[string]bool{}, seenSlugs: map[string]bool{},
		readActivity: newReadActivityTracker(), toolPortfolio: newToolPortfolioTracker(), repo: ref.repo,
		metric:      learnSessionMetric{Repo: ref.repo, Source: sourceID},
		captureText: true,
	}
}

func (c *sessionEventConsumer) consume(event turnEvent) {
	if event.sessionStart {
		c.opened = true
		c.behavior.recordSession(c.sourceID)
		return
	}
	if event.Repo != "" {
		c.repo = event.Repo
		c.metric.Repo = event.Repo
	}
	for _, payload := range event.TextPayloads {
		if !c.captureText {
			break
		}
		if c.textPayload.Len() >= maxSectionTextBytes {
			break
		}
		if c.textPayload.Len() > 0 {
			c.textPayload.WriteByte('\n')
		}
		remaining := maxSectionTextBytes - c.textPayload.Len()
		if len(payload) > remaining {
			payload = payload[:remaining]
		}
		c.textPayload.WriteString(payload)
	}
	c.readActivity.observe(event, c.repo)
	if !event.Timestamp.IsZero() || event.ContextUsagePresent || len(event.TextPayloads) > 0 || len(event.ToolCalls) > 0 {
		c.metric.Observed = true
	}

	if event.Compaction {
		c.outcome.Compactions++
	}
	for _, call := range event.ToolCalls {
		if call.IsError {
			c.outcome.ErrorTurns++
		}
	}
	ts := ""
	if !event.Timestamp.IsZero() {
		if c.outcome.Start.IsZero() || event.Timestamp.Before(c.outcome.Start) {
			c.outcome.Start = event.Timestamp.UTC()
		}
		if event.Timestamp.After(c.outcome.End) {
			c.outcome.End = event.Timestamp.UTC()
		}
		ts = event.Timestamp.UTC().Format(time.RFC3339)
		if c.behavior.From == "" || ts < c.behavior.From {
			c.behavior.From = ts
		}
		if c.behavior.To == "" || ts > c.behavior.To {
			c.behavior.To = ts
		}
	}
	relPath := event.RelPath
	if relPath == "" {
		relPath = c.ref.relPath
	}
	c.miner.observeTextPayloads(c.sourceID, relPath, event.JSONLLine, ts, event.TextPayloads, event.Side)

	if event.ContextUsagePresent && (event.UsageMessageID == "" || !c.seenUsage[event.UsageMessageID]) {
		if event.UsageMessageID != "" {
			c.seenUsage[event.UsageMessageID] = true
		}
		ctx := event.ContextTotal
		c.cacheHygiene.observe(cacheUsageTurn{
			ContextTotal: ctx, CacheRead: event.CacheReadInputTokens,
			CacheCreation: event.CacheCreationInputTokens, Present: event.CacheUsagePresent,
		})
		c.behavior.Turns++
		c.behavior.Contexts = append(c.behavior.Contexts, ctx)
		c.behavior.Spend.observe(event)
		c.outcome.Turns++
		c.outcome.Tokens += int64(max(0, ctx))
		c.subagentSpend.observeTurn(ctx, event.Side)
		if !c.prefixRecorded {
			c.behavior.recordPrefix(c.sourceID, ctx)
			c.prefixRecorded = true
		}
		window, exactWindow := contextWindow(event.ProviderKey, event.Model)
		if !exactWindow {
			if c.behavior.FallbackWindowSources == nil {
				c.behavior.FallbackWindowSources = map[string]bool{}
			}
			c.behavior.FallbackWindowSources[c.sourceID] = true
		}
		if pct := ctx * 100 / window; pct > c.sessionPeakPct {
			c.sessionPeakPct = pct
		}
		if ctx > int(dumbzoneFraction*float64(window)) {
			c.behavior.DumbzoneTurns++
			c.metric.Dumbzone++
			if exactWindow {
				excess := int64(ctx - int(dumbzoneFraction*float64(window)))
				if sum, ok := checkedNonNegativeSum(c.behavior.DumbzoneExcessTokens, excess); ok {
					c.behavior.DumbzoneExcessTokens = sum
				}
			}
		}
		c.metric.Turns++
		c.metric.Contexts = append(c.metric.Contexts, ctx)
		if c.metric.Prefix == 0 {
			c.metric.Prefix = ctx
		}
	}

	for _, raw := range event.SkillUses {
		if slug, ok := c.knownSlugs[normalizeSkillReference(raw)]; ok {
			c.seenSlugs[slug] = true
		}
	}
	c.sessionTasks += event.TaskSpawns
	for _, call := range event.ToolCalls {
		observed := learnToolCall{
			Name: call.Name, Input: call.InputSummary, IsError: call.IsError,
			OutputTokens: estimateTokens(call.OutputText), Position: event.JSONLLine,
		}
		c.toolCalls = append(c.toolCalls, observed)
		c.toolPortfolio.observe(observed)
		c.subagentSpend.observeSpawn(call.Name, call.InputSummary)
	}
}

func (c *sessionEventConsumer) finish() {
	if !c.opened {
		return
	}
	for slug := range c.seenSlugs {
		c.behavior.SkillUse[slug]++
	}
	if c.sessionTasks > 0 {
		c.behavior.TaskSpawns += c.sessionTasks
		c.behavior.SessionsWithTasks++
	}
	if c.sessionPeakPct > 0 {
		c.behavior.SessionPeakPct = append(c.behavior.SessionPeakPct, c.sessionPeakPct)
	}
	sessionSum := sha256.Sum256([]byte(c.ref.relPath))
	sessionID := hex.EncodeToString(sessionSum[:8])
	c.behavior.LearningLoops = append(c.behavior.LearningLoops, detectLearningLoops(c.toolCalls, sessionID)...)
	c.procedures.observeSession(sessionID, c.toolCalls)
	c.behavior.Procedures.merge(c.procedures)
	if observation, ok := c.cacheHygiene.observation(); ok {
		c.behavior.CacheHygieneSessions = append(c.behavior.CacheHygieneSessions, observation)
	}
	if observation, ok := c.readActivity.rereadObservation(); ok {
		c.behavior.RereadSessions = append(c.behavior.RereadSessions, observation)
	}
	if observation, ok := c.readActivity.compactionObservation(); ok {
		c.behavior.CompactionSessions = append(c.behavior.CompactionSessions, observation)
	}
	if c.textPayload.Len() > 0 && len(c.behavior.SessionTexts) < maxSectionSessions {
		c.behavior.SessionTexts = append(c.behavior.SessionTexts, sessionTextObservation{
			Repo: c.repo, Text: normalizeEchoText(c.textPayload.String()),
		})
	}
	if c.metric.Turns > 0 && strings.TrimSpace(c.metric.Repo) != "" {
		c.behavior.SessionMetrics = append(c.behavior.SessionMetrics, c.metric)
	}
	c.behavior.ToolPortfolio.merge(c.toolPortfolio)
	if c.subagentSpend.SideTurns > 0 || c.subagentSpend.MainTurns > 0 {
		c.subagentSpend.sessions = 1
	}
	c.behavior.SubagentSpend.merge(c.subagentSpend)
	// Outcome rows need a repository AND a real time window; a session missing
	// either cannot be joined to commit history and is dropped rather than
	// counted as commitless.
	if c.outcome.Turns > 0 && c.outcome.Tokens > 0 && !c.outcome.Start.IsZero() && strings.TrimSpace(c.repo) != "" {
		c.outcome.Repo = c.repo
		c.behavior.SessionOutcomes = append(c.behavior.SessionOutcomes, c.outcome)
	}
}

func scanSessionSourceUntil(source sessionSource, since time.Time, slugs []string, beh *behaviorScan, miner *recurringMiner, deadline *behaviorDeadline) bool {
	refs, timeBoxed := source.discover(deadline)
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].relPath != refs[j].relPath {
			return refs[i].relPath < refs[j].relPath
		}
		return refs[i].path < refs[j].path
	})
	type result struct {
		behavior  behaviorScan
		miner     *recurringMiner
		timeBoxed bool
	}
	results := make([]result, len(refs))
	parallelSessionScan(len(refs), func(i int) {
		if deadline != nil && deadline.expired() {
			results[i].timeBoxed = true
			return
		}
		localBehavior := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
		localMiner := newRecurringMiner()
		consumer := newSessionEventConsumer(source.id(), refs[i], slugs, &localBehavior, localMiner)
		consumer.captureText = i < maxSectionSessions
		truncated := source.scanSession(refs[i], since, consumer.consume, deadline)
		if !truncated {
			consumer.finish()
		}
		results[i] = result{behavior: localBehavior, miner: localMiner, timeBoxed: truncated}
	})
	for i := range results {
		mergeBehaviorScan(beh, &results[i].behavior)
		miner.merge(results[i].miner)
		timeBoxed = results[i].timeBoxed || timeBoxed
	}
	return timeBoxed
}

func scanOneSessionUntil(source sessionSource, ref sessionRef, since time.Time, slugs []string, beh *behaviorScan, miner *recurringMiner, deadline *behaviorDeadline) bool {
	consumer := newSessionEventConsumer(source.id(), ref, slugs, beh, miner)
	truncated := source.scanSession(ref, since, consumer.consume, deadline)
	if !truncated {
		consumer.finish()
	}
	return truncated
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
