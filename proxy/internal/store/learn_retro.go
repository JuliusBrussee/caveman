package store

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// learn_retro.go is the opt-in retrospective pass behind `learn scan --retro`:
// over the same window the profiler already scans, it reports what the sessions
// actually sent and what the engine measures it would have cut. It is a second,
// budget-bounded walk so the base scan's timing and output stay byte-identical
// when `--retro` is absent.
//
// HONESTY: everything here is `inferred` and
// sums only over sessions actually read. Provider usage is counted once per API
// response (message id), never once per transcript line. WouldCutTokens counts
// each unique tool-output once plus a conservative lower bound for observed
// re-pastes (sum of occurrence sizes minus the largest normalized variant);
// WouldCutStreamTokens re-weights tool-output and
// timestamp-ordered repeated-block cuts by later provider-counted turns of the
// same transcript — and because the
// transcript does not observe mid-session eviction, believed residency is
// CAPPED by each turn's own provider-counted size (oldest blocks evicted
// first) and cleared at compaction or end of session. Cross-file duplicates
// weight deterministically in the newest file that carries them. Never
// projected; token volume only — the price of a re-sent token depends on
// provider caching, so no dollar is ever derived here. Under-claim, never blend.

const (
	// behaviorDefaultBudgetMS caps the base behavioral pass when retro is enabled.
	// Together with retroDefaultBudgetMS it stays below the first-run child's
	// derived timeout, so a cold base scan cannot erase a partial retro result.
	behaviorDefaultBudgetMS = 20000
	// retroDefaultBudgetMS matches the CLI's first-run budget so the bare
	// `learn scan --retro` and the welcome reveal report the same coverage on
	// the same history.
	retroDefaultBudgetMS = 60000
	// retroSegmentFloorBytes: below this a tool result is not worth an engine
	// pass, and the engine's not-smaller check would pass it through anyway.
	retroSegmentFloorBytes = 512
)

// RetroOptions turns on the retrospective pass. Off by default: an existing
// `learn scan` caller must see zero behavior change.
type RetroOptions struct {
	Enabled bool
	// BudgetMS bounds the retro pass, including discovery and transcript parsing,
	// but never the base scan. <= 0 uses the default. Exceeding it truncates
	// coverage and sets TimeBoxed.
	BudgetMS int
	// BehaviorBudgetMS bounds the base behavioral pass that precedes retro.
	// <= 0 uses behaviorDefaultBudgetMS. Ignored when Enabled is false, preserving
	// existing `learn scan` timing and bytes.
	BehaviorBudgetMS int
}

// retroClock and retroEngineFactory are the pass's only injected seams. The
// deadline is what makes the pass bounded, and "the engine could not be built"
// is a fail-closed path that must stay reachable in tests.
var (
	retroClock          = time.Now
	retroDiscoveryClock = time.Now
	retroEngineFactory  = openRetroEngine
)

// openRetroEngine builds a counting-only engine. Simulate stores nothing, so the
// in-memory CCR store is never written; it exists so an S4 reduction is reported
// as realizable — which is what the local wrap path, where CCR is wired, would
// actually achieve. Without it the engine would report those reductions as
// unrecoverable and the retro block would silently under-count.
func openRetroEngine() (*engine.Engine, func(), error) {
	recovery, err := ccr.OpenMemory()
	if err != nil {
		return nil, nil, err
	}
	eng := engine.New(recovery, nil)
	// Warm the shared token counter on this goroutine before any worker touches
	// it: its regexp splitter lazily allocates a runner pool on first use and
	// that allocation is not itself synchronized. Warming here happens-before
	// the scan workers start, after which the pool is safe for concurrent use.
	eng.Simulate([]byte("warm"), engine.Options{Mode: engine.ModeCompress})
	return eng, func() { _ = recovery.Close() }, nil
}

// retroSessionPath is one discovered transcript, ordered newest-first so a
// truncated pass covers the freshest sessions rather than an arbitrary slice.
type retroSessionPath struct {
	path    string
	relPath string
	source  string
	modTime time.Time
}

// retroTurn is one deduplicated provider-counted API response: where it sits
// in the file, how many tokens the provider counted for it, and whether it
// belongs to an interleaved legacy sidechain.
type retroTurn struct {
	line int
	ctx  int64
	side bool
}

// retroCandidate is one tool-output segment (>= floor) a file carries. Whether
// it earned a cut lives in the collector, keyed by sha; size is the scanner's
// byte-based estimate of the WHOLE segment, used only to cap believed
// residency against each turn's provider-counted size.
type retroCandidate struct {
	sha  string
	line int
	size int64
	side bool
}

// retroFileResult is one transcript's contribution. A file counts toward
// SessionsScanned only when it carried provider usage — mixing counted and
// estimated sessions in one total is exactly the blend the accounting forbids.
// Stream weighting happens after the parallel scan, in deterministic
// newest-first file order, from turns/boundaries/candidates.
type retroFileResult struct {
	source      string
	relPath     string
	usageTokens int64
	usageTurns  int
	turns       []retroTurn
	boundaries  []int
	candidates  []retroCandidate
	parsed      bool // read before the deadline (as opposed to skipped entirely)
	usable      bool // carried provider-counted usage
	truncated   bool
	miner       *recurringMiner
}

// retroCollector measures unique tool-output segments exactly once per scan.
// savedBySha keeps each booked cut so the deterministic stream pass can weight
// a segment from whichever file wins the newest-first attribution, regardless
// of which goroutine ran the engine.
type retroCollector struct {
	mu         sync.Mutex
	seen       map[string]bool
	savedBySha map[string]int64
	cutTokens  int64
	segments   int
	eng        *engine.Engine
	deadline   time.Time
}

// claim reports whether this scan has not measured the segment yet. The claim is
// what makes the count first-send-only across the whole window.
func (c *retroCollector) claim(sha string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[sha] {
		return false
	}
	c.seen[sha] = true
	return true
}

// measure runs one segment through the engine exactly once per unique sha and
// books only a real reduction. A pass-through, a non-positive delta, or an
// unrecoverable result books zero. The result is deterministic per content, so
// whichever goroutine wins the claim records the same figure in savedBySha.
func (c *retroCollector) measure(sha, text string) {
	if c.eng == nil {
		return
	}
	if !c.claim(sha) {
		return
	}
	sim := c.eng.Simulate([]byte(text), engine.Options{Mode: engine.ModeCompress})
	if !sim.Recoverable || sim.TokensSaved <= 0 {
		return
	}
	c.mu.Lock()
	c.cutTokens += int64(sim.TokensSaved)
	c.segments++
	c.savedBySha[sha] = int64(sim.TokensSaved)
	c.mu.Unlock()
}

func (c *retroCollector) expired() bool { return !retroClock().Before(c.deadline) }

// buildLearnRetro runs the bounded retro pass. It returns nil rather than a
// block of zeros when there is nothing honest to report.
func (s *Store) buildLearnRetro(sourceSet map[string]bool, since time.Time, sinceExpr string, configPrefixPerTurn int, opts RetroOptions) *LearnRetro {
	budget := time.Duration(opts.BudgetMS) * time.Millisecond
	if opts.BudgetMS <= 0 {
		budget = retroDefaultBudgetMS * time.Millisecond
	}
	// Overall deadline starts before transcript discovery. Discovery gets at most
	// one quarter of the pass (capped at 10s), reserving time to parse and return
	// honest partial coverage before the CLI child deadline.
	col := &retroCollector{seen: map[string]bool{}, savedBySha: map[string]int64{}, deadline: retroClock().Add(budget)}
	discoveryBudget := budget / 4
	if discoveryBudget > 10*time.Second {
		discoveryBudget = 10 * time.Second
	}
	discoveryDeadline := retroDiscoveryClock().Add(discoveryBudget)
	paths, discoveryTimeBoxed := retroSessionPathsUntil(sourceSet, since, func() bool {
		return !retroDiscoveryClock().Before(discoveryDeadline)
	})
	if len(paths) == 0 {
		return nil
	}

	// Fail closed on the engine: without it the tool-output family is omitted
	// entirely rather than estimated, and engine_used records why.
	engineUsed := true
	engineErr := ""
	if eng, closeEngine, err := retroEngineFactory(); err == nil {
		col.eng = eng
		defer closeEngine()
	} else {
		engineUsed = false
		engineErr = err.Error()
	}

	results := make([]retroFileResult, len(paths))
	skipped := discoveryTimeBoxed
	var skippedMu sync.Mutex
	parallelSessionScan(len(paths), func(i int) {
		if col.expired() {
			skippedMu.Lock()
			skipped = true
			skippedMu.Unlock()
			return
		}
		switch paths[i].source {
		case "claude":
			results[i] = retroScanClaudeFile(paths[i], since, col)
		case "codex":
			results[i] = retroScanCodexFile(paths[i], since, col)
		}
	})

	miner := newRecurringStreamMiner()
	retro := LearnRetro{
		Basis:                     learnBasis,
		WindowDays:                int(windowDays(sinceExpr, "", "")),
		SessionsTotal:             len(paths),
		TokensObservedSource:      retroSourceSessionUsage,
		ConfigPrefixTokensPerTurn: configPrefixPerTurn,
		EngineUsed:                engineUsed,
		TimeBoxed:                 skipped,
		Families:                  []LearnRetroFamily{},
	}
	// Merge in newest-first path order so recurrence aggregation stays
	// deterministic even though parsing ran concurrently.
	unusable := 0
	for i := range results {
		if results[i].truncated {
			retro.TimeBoxed = true
		}
		if !results[i].usable {
			// Only a completed scan can establish that a session lacked usage.
			// A deadline-truncated file may carry usage below the stopping point.
			if results[i].parsed && !results[i].truncated {
				unusable++
			}
			continue
		}
		// Every family must be mined from exactly the session set that backs
		// TokensObserved. Folding a usage-free session's blocks in here would
		// book a cut against tokens no total ever counted — a blend, and one
		// that can push would_cut past observed.
		if results[i].miner != nil {
			miner.merge(results[i].miner)
		}
		retro.SessionsScanned++
		retro.TurnsObserved += results[i].usageTurns
		retro.TokensObserved += results[i].usageTokens
	}
	if retro.SessionsScanned == 0 || retro.TokensObserved <= 0 {
		return nil // nothing provider-counted was read; emit no block at all
	}

	if engineUsed && col.cutTokens > 0 {
		retro.Families = append(retro.Families, LearnRetroFamily{
			ID: retroFamilyToolOutputs, Label: "tool outputs (wrap compression)", Tokens: col.cutTokens,
		})
	}
	recurring := miner.result()
	if repeated := retroRepeatedBlockTokens(recurring); repeated > 0 {
		retro.Families = append(retro.Families, LearnRetroFamily{
			ID: retroFamilyRepeatedBlocks, Label: "re-pasted context (cavemem offload)", Tokens: repeated,
		})
	}
	repeatedByFile, undatedRepeated := retroRepeatedStreamCandidates(recurring)
	streamClaimed := map[string]bool{}
	for i := range results {
		if !results[i].usable {
			continue
		}
		key := retroStreamFileKey(results[i].source, results[i].relPath)
		retro.WouldCutStreamTokens += retroStreamCutTokens(
			&results[i], col.savedBySha, streamClaimed, repeatedByFile[key],
		)
	}
	for _, family := range retro.Families {
		retro.WouldCutTokens += family.Tokens
	}

	retro.Caveats = retroCaveats(retro, sourceSet, unusable, engineErr, undatedRepeated)
	return &retro
}

type retroStreamCandidate struct {
	line      int
	cut, size int64
	side      bool
}

func retroStreamFileKey(source, relPath string) string { return source + "\x00" + relPath }

// retroStreamCutTokens weights one file's tool-output and repeated-block cuts
// together by later provider-counted turns of the same transcript. Combining
// both families before the residency cap prevents each from independently
// claiming the same provider-counted context. Believed residency is
// CAPPED by each turn's own provider-counted size: the transcript cannot
// observe mid-session eviction, so when the byte-estimated sizes of the
// believed-resident segments exceed what the provider counted for a turn, the
// oldest segments are evicted first and never credit again. A compact boundary
// clears residency outright. Cross-file duplicates weight only in the first
// file the deterministic caller order reaches (streamClaimed). In a legacy
// MIXED file (interleaved sidechain lines) only main-thread cuts weigh,
// against main-thread turns — current Claude Code writes each subagent thread
// to its own file, where every turn is honestly the same thread.
func retroStreamCutTokens(r *retroFileResult, savedBySha map[string]int64, streamClaimed map[string]bool, repeated []retroStreamCandidate) int64 {
	if (len(r.candidates) == 0 && len(repeated) == 0) || len(r.turns) == 0 {
		return 0
	}
	sawMain, sawSide := false, false
	for _, t := range r.turns {
		if t.side {
			sawSide = true
		} else {
			sawMain = true
		}
	}
	mixed := sawMain && sawSide
	var segs []retroStreamCandidate
	for _, cand := range r.candidates {
		saved := savedBySha[cand.sha]
		if saved <= 0 || streamClaimed[cand.sha] {
			continue
		}
		streamClaimed[cand.sha] = true
		if mixed && cand.side {
			continue
		}
		segs = append(segs, retroStreamCandidate{line: cand.line, cut: saved, size: cand.size})
	}
	for _, cand := range repeated {
		if mixed && cand.side {
			continue
		}
		segs = append(segs, cand)
	}
	// Candidates from both families can share a transcript line. Evict the
	// largest cut first on a tie: transcript order is unavailable at that grain,
	// so the deterministic tie-break under-claims rather than favoring savings.
	sort.SliceStable(segs, func(i, j int) bool {
		if segs[i].line != segs[j].line {
			return segs[i].line < segs[j].line
		}
		return segs[i].cut > segs[j].cut
	})
	var total int64
	var active []retroStreamCandidate
	var activeSize int64
	si, bi := 0, 0
	for _, t := range r.turns {
		if mixed && t.side {
			continue
		}
		// Replay segment arrivals and compaction clears in line order up to
		// this turn.
		for {
			nextSeg, nextBound := t.line, t.line
			if si < len(segs) {
				nextSeg = segs[si].line
			}
			if bi < len(r.boundaries) {
				nextBound = r.boundaries[bi]
			}
			if nextSeg >= t.line && nextBound >= t.line {
				break
			}
			if nextSeg < nextBound {
				active = append(active, segs[si])
				activeSize += segs[si].size
				si++
			} else {
				active = active[:0]
				activeSize = 0
				bi++
			}
		}
		for len(active) > 0 && activeSize > t.ctx {
			activeSize -= active[0].size
			active = active[1:]
		}
		for _, s := range active {
			total += s.cut
		}
	}
	return total
}

// retroRepeatedBlockTokens counts historical duplication actually present in
// the transcripts. Normalization can group differently sized digit/whitespace
// variants, and chronology may be unavailable, so subtracting the largest
// observed occurrence is the conservative lower bound for "all but the first."
func retroRepeatedBlockTokens(rec recurringResult) int64 {
	var total int64
	for _, e := range rec.Repaste {
		if e.Occurrences <= 1 || len(e.Positions) != e.Occurrences {
			continue
		}
		var sum, largest int64
		complete := true
		for _, position := range e.Positions {
			if position.Tokens <= 0 {
				complete = false
				break
			}
			size := int64(position.Tokens)
			sum += size
			if size > largest {
				largest = size
			}
		}
		if complete {
			total += sum - largest
		}
	}
	return total
}

// retroRepeatedStreamCandidates turns every timestamp-ordered re-paste after
// the earliest occurrence group into a resident cut. Exact earliest ties stay
// necessary because separate files have no causal ordering. If any occurrence
// lacks a timestamp the whole fingerprint stays out of the stream figure:
// choosing a first paste from file/path order could inflate residency, so the
// base occurrence count remains the honest fallback.
func retroRepeatedStreamCandidates(rec recurringResult) (map[string][]retroStreamCandidate, bool) {
	byFile := map[string][]retroStreamCandidate{}
	undated := false
	for _, entry := range rec.Repaste {
		if entry.Occurrences <= 1 || entry.BlockTokens <= 0 {
			continue
		}
		if len(entry.Positions) != entry.Occurrences {
			undated = true
			continue
		}
		type timedOccurrence struct {
			recurringOccurrence
			at time.Time
		}
		positions := make([]timedOccurrence, 0, len(entry.Positions))
		complete := true
		for _, position := range entry.Positions {
			if position.Timestamp == "" || position.Tokens <= 0 {
				complete = false
				break
			}
			at, err := time.Parse(time.RFC3339Nano, position.Timestamp)
			if err != nil {
				complete = false
				break
			}
			positions = append(positions, timedOccurrence{recurringOccurrence: position, at: at})
		}
		if !complete {
			undated = true
			continue
		}
		sort.SliceStable(positions, func(i, j int) bool {
			if !positions[i].at.Equal(positions[j].at) {
				return positions[i].at.Before(positions[j].at)
			}
			if positions[i].RootKind != positions[j].RootKind {
				return positions[i].RootKind < positions[j].RootKind
			}
			if positions[i].RelPath != positions[j].RelPath {
				return positions[i].RelPath < positions[j].RelPath
			}
			return positions[i].JSONLLine < positions[j].JSONLLine
		})
		// Separate transcript files have no causal ordering at an identical
		// timestamp. Treat the whole earliest tied group as necessary context;
		// path order must never decide which one earns removable-repeat credit.
		firstRepeat := 1
		for firstRepeat < len(positions) && positions[firstRepeat].at.Equal(positions[0].at) {
			firstRepeat++
		}
		for _, position := range positions[firstRepeat:] {
			key := retroStreamFileKey(position.RootKind, position.RelPath)
			byFile[key] = append(byFile[key], retroStreamCandidate{
				line: position.JSONLLine, cut: int64(position.Tokens),
				size: int64(position.Tokens), side: position.Side,
			})
		}
	}
	return byFile, undated
}

func retroCaveats(retro LearnRetro, sourceSet map[string]bool, unusable int, engineErr string, undatedRepeated bool) []string {
	caveats := []string{
		"Provider usage is counted once per API response (message id), never once per transcript line.",
		"would_cut_tokens counts each unique tool-output cut once plus a conservative repeated-block lower bound: occurrence sizes summed minus the largest normalized variant; it carries no ratio against tokens_observed.",
		"would_cut_stream_tokens re-weights tool-output and timestamp-ordered repeated-block cuts by later provider-counted turns of the same session, with exact earliest timestamp ties kept as necessary context; both families share one residency cap per turn (oldest evicted first) and clear at compaction or end of session — same basis as tokens_observed, token volume only; the price of a re-sent token depends on provider caching.",
		"Totals are sums over the sessions actually read; nothing is extrapolated to unscanned sessions, to a month, or to a dollar.",
		"Config prefix is reported as a per-turn rate and is deliberately excluded from would_cut_tokens; trimming config is a different fix from wrap compression.",
	}
	if retro.EngineUsed {
		caveats = append(caveats, "Tool-output savings are engine-measured in o200k tokens; repeated-block savings use the scanner's byte-based estimate. The two are not the same estimator.")
	} else {
		msg := "The compression engine could not be started, so the tool-output family is omitted entirely rather than estimated."
		if engineErr != "" {
			msg += " (" + engineErr + ")"
		}
		caveats = append(caveats, msg)
	}
	if sourceSet["codex"] {
		caveats = append(caveats, "Codex sessions contribute observed tokens only; tool outputs and repeated blocks are scanned from Claude Code transcripts, whose payload shape exposes them.")
	}
	if undatedRepeated {
		caveats = append(caveats, "Repeated blocks without complete transcript timestamps remain in would_cut_tokens but are omitted from would_cut_stream_tokens rather than guessing which occurrence came first.")
	}
	if retro.TimeBoxed {
		caveats = append(caveats, "The retro pass hit its discovery or scan time budget, so coverage is partial and the figures under-count. Complete paths discovered before expiry were read newest-first.")
	}
	if unusable > 0 {
		caveats = appendUnique(caveats, "Sessions without provider-counted usage were excluded from every total rather than estimated.")
	}
	return caveats
}

// retroSessionPaths discovers the transcripts in the window and orders them
// newest-first, so a budget-truncated pass covers the freshest sessions.
func retroSessionPaths(sourceSet map[string]bool, since time.Time) []retroSessionPath {
	paths, _ := retroSessionPathsUntil(sourceSet, since, nil)
	return paths
}

// retroSessionPathsUntil makes discovery part of the pass budget. A truncated
// discovery returns every complete path found before expiry and marks the result
// partial; callers sort that known subset newest-first and never extrapolate it.
func retroSessionPathsUntil(sourceSet map[string]bool, since time.Time, expired func() bool) ([]retroSessionPath, bool) {
	var out []retroSessionPath
	timeBoxed := false
	stopped := func() bool {
		if expired == nil || !expired() {
			return false
		}
		timeBoxed = true
		return true
	}
	add := func(root, source, path string, info os.FileInfo) bool {
		if stopped() {
			return false
		}
		if info == nil {
			var err error
			info, err = os.Stat(path)
			if err != nil {
				return true
			}
		}
		if info.IsDir() || (!since.IsZero() && info.ModTime().Before(since)) {
			return true
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = filepath.Base(path)
		}
		out = append(out, retroSessionPath{path: path, relPath: rel, source: source, modTime: info.ModTime()})
		return true
	}
	if sourceSet["claude"] {
		if root := claudeRoot(); root != "" {
			_ = filepath.WalkDir(filepath.Join(root, "projects"), func(path string, d os.DirEntry, err error) error {
				if stopped() {
					return fs.SkipAll
				}
				if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
					return nil
				}
				info, infoErr := d.Info()
				if infoErr == nil && !add(root, "claude", path, info) {
					return fs.SkipAll
				}
				return nil
			})
		}
	}
	if sourceSet["codex"] && !stopped() {
		if root := codexRoot(); root != "" {
			paths, pathsTimeBoxed, _ := codexPathsUntil(root, stopped)
			timeBoxed = pathsTimeBoxed || timeBoxed
			for _, path := range paths {
				if !add(root, "codex", path, nil) {
					break
				}
			}
		}
	} else if sourceSet["codex"] {
		timeBoxed = true
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].modTime.Equal(out[j].modTime) {
			return out[i].modTime.After(out[j].modTime)
		}
		return out[i].path < out[j].path
	})
	return out, timeBoxed
}

// retroScanClaudeFile reads one Claude Code transcript: provider-counted usage,
// recurring blocks, and tool-result payloads for the engine.
func retroScanClaudeFile(p retroSessionPath, since time.Time, col *retroCollector) retroFileResult {
	res := retroFileResult{source: p.source, relPath: p.relPath, miner: newRecurringStreamMiner()}
	f, err := os.Open(p.path)
	if err != nil {
		return res
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	// Record positions for the deterministic stream pass (retroStreamCutTokens):
	// deduplicated provider-counted turns with their counted sizes, compaction
	// clears, and every >= floor tool-output segment. All ascending by
	// construction. Sidechain flags matter only for legacy transcripts that
	// interleaved subagent lines into the session file — current Claude Code
	// writes each subagent thread to its own file.
	seenUsage := map[string]bool{}
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if col.expired() {
			res.truncated = true
			break
		}
		res.parsed = true
		var obj map[string]any
		if json.Unmarshal(sc.Bytes(), &obj) != nil {
			continue
		}
		ts := timestampFromObject(obj)
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			continue
		}
		tsStr := ""
		if !ts.IsZero() {
			tsStr = ts.UTC().Format(time.RFC3339Nano)
		}
		if obj["type"] == "system" && obj["subtype"] == "compact_boundary" {
			res.boundaries = append(res.boundaries, lineNo)
		}
		sidechain := obj["isSidechain"] == true
		if ctx, ok := claudeTurnContext(obj); ok {
			res.usable = true
			// One API response is written as several transcript lines (one per
			// content block), each repeating the same usage — count it once.
			if id := claudeUsageMessageID(obj); id == "" || !seenUsage[id] {
				if id != "" {
					seenUsage[id] = true
				}
				res.usageTurns++
				res.usageTokens += int64(ctx)
				res.turns = append(res.turns, retroTurn{line: lineNo, ctx: int64(ctx), side: sidechain})
			}
		}
		res.miner.observeTurn("claude", p.relPath, lineNo, tsStr, obj)
		// Measure only once this session is known to carry provider usage, so
		// the cut total and the observed total cover exactly the same sessions.
		// In a Claude Code transcript the first tool_result always follows an
		// assistant turn, which is where usage lives, so nothing real is lost.
		if !res.usable {
			continue
		}
		for _, text := range claudeToolResultTexts(obj) {
			sha := contentSHA256(text)
			col.measure(sha, text)
			res.candidates = append(res.candidates, retroCandidate{
				sha: sha, line: lineNo, size: int64(estimateTokens(text)), side: sidechain,
			})
		}
	}
	return res
}

// claudeToolResultTexts pulls tool-result payload text out of one transcript
// turn. These are the bodies the wrap path compresses.
func claudeToolResultTexts(obj map[string]any) []string {
	msg := asMap(obj["message"])
	content, _ := msg["content"].([]any)
	var out []string
	for _, raw := range content {
		block := asMap(raw)
		if block == nil || block["type"] != "tool_result" {
			continue
		}
		if text := toolResultText(block["content"]); len(text) >= retroSegmentFloorBytes {
			out = append(out, text)
		}
	}
	return out
}

// retroScanCodexFile reads a Codex rollout for its provider-counted usage only.
// Codex's payload shape does not expose tool-result bodies the way the Claude
// transcript does, so it contributes to the observed total and to nothing else.
func retroScanCodexFile(p retroSessionPath, since time.Time, col *retroCollector) retroFileResult {
	res := retroFileResult{source: p.source, relPath: p.relPath}
	f, err := os.Open(p.path)
	if err != nil {
		return res
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if col.expired() {
			res.truncated = true
			break
		}
		res.parsed = true
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
		// Codex mirrors OpenAI usage: input_tokens is already the total effective
		// prompt size and cached_input_tokens is a subset of it. Adding the cache
		// detail again would double-count context that was sent once.
		ctx := int64FromAny(usage["input_tokens"])
		if ctx <= 0 {
			continue
		}
		res.usable = true
		res.usageTurns++
		res.usageTokens += ctx
	}
	return res
}
