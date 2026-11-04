package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// recurring.go mines heavy context blocks that recur across sessions — the
// "re-established context" a developer pastes or restates session after session.
// It is the detector behind the recurring_context sink class, whose fix is
// cavemem_offload: move the block into cavemem so it's recalled compactly instead
// of re-loaded every turn.
//
// HONESTY: the miner is deterministic and read-only. It persists only
// fingerprints, counts, token weights, and content *locators* in sink evidence —
// never the raw block body. The body stays on disk; the consent-gated editing
// skill re-derives it locally from the locator (file + line + block index) and
// verifies it against content_sha256 before offloading.
const (
	// recurSegmenterVersion / recurNormalizationVer are a shared contract with the
	// editing skill: the skill re-segments a turn the same way to locate a block by
	// (jsonl_line, block_index) and verifies it by content_sha256. Bump on change.
	recurSegmenterVersion = "v1"
	recurNormalizationVer = "v1"

	minBlockTokens        = 180 // a block must weigh at least this to be a candidate
	minRecurrenceSessions = 3   // must appear in at least this many distinct sessions
	maxLocatorSamples     = 5   // cap locator samples per fingerprint (refs, never bodies)
)

var (
	reBlankLine  = regexp.MustCompile(`\n[ \t]*\n`)
	reWhitespace = regexp.MustCompile(`\s+`)
	reDigits     = regexp.MustCompile(`\d+`)

	// reHookResultPreamble matches the host's own hook-result preamble, which
	// opens a payload the host injected: "SessionStart:startup hook success:",
	// "PreToolUse:Bash hook failed:", and the same shape from other events and
	// hosts. Anchored at the payload start, because a human may well QUOTE one
	// mid-sentence and that block is still theirs (#1459).
	//
	// The trailing colon is load-bearing, and so is the capitalised event name
	// and the lower-case verb: without the colon this also matched an ordinary
	// English sentence whose first word happens to precede the words "hook
	// success" ("The hook success path is the one I care about"), which would
	// have suppressed a real finding to hide a false one.
	reHookResultPreamble = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*(?::[A-Za-z0-9_.\-]+)?[ \t]+hook[ \t]+(?:success|succeeded|completed|failed|error)[ \t]*:`)
)

// injectedContextOpeners are literal payload openings that only the host or
// caveman's own hook writes: Claude Code's system-reminder envelope (which
// carries the CLAUDE.md / auto-memory injection), the bare CLAUDE.md preamble,
// and the caveman ruleset's banner and SENTINEL first line.
var injectedContextOpeners = []string{
	"<system-reminder>",
	"Codebase and user instructions are shown below",
	"CAVEMAN MODE ACTIVE",
	"Respond terse like smart caveman",
}

// injectedContextPayload reports whether one text payload was injected by the
// host or by caveman's own hook rather than written by the user.
//
// The repaste miner's whole premise is that a recurring block is context a
// developer re-establishes by hand, and its fix (cavemem_offload) only saves
// anything if NOT pasting it is an option. For these payloads it is not: the
// host re-injects CLAUDE.md and its memory blocks every session whatever
// cavemem holds, and the SessionStart hook re-emits the ruleset for the same
// reason it runs on every source — compaction prunes it out of context. Acting
// on the recommendation the other way round (dropping the source once it is in
// cavemem) silently stops the agent receiving its instructions. Those bytes are
// already accounted as always-loaded setup (claude_md_weight), so mining them
// double-counts them and crowds genuine pastes out of a capped report (#1459).
//
// Every rule is anchored at the payload start: a human block that mentions a
// reminder tag or quotes the hook banner is still the human's own text, and
// silencing it would cost a real finding to suppress a false one.
func injectedContextPayload(payload string) bool {
	head := strings.TrimLeft(payload, " \t\r\n")
	if head == "" {
		return false
	}
	for _, opener := range injectedContextOpeners {
		if strings.HasPrefix(head, opener) {
			return true
		}
	}
	return reHookResultPreamble.MatchString(head)
}

// blockCandidate is one segmented block plus whether the payload it came out of
// was host- or hook-injected. The flag rides alongside the block instead of
// removing it, because a locator addresses a block by its flattened index
// within the turn (segmenter v1) and the editing skill re-segments the line to
// re-derive the body: dropping a payload would renumber every block after it.
type blockCandidate struct {
	raw      string
	injected bool
}

// minableBlocks segments a turn's text payloads in document order, each payload
// independently under segmenter v1, keeping the flattened block index that
// locators and the editing skill share. It is the one place the injected-context
// rule is applied, so every source and every fingerprint consumer agrees.
func minableBlocks(payloads []string) []blockCandidate {
	var out []blockCandidate
	for _, payload := range payloads {
		injected := injectedContextPayload(payload)
		for _, block := range segmentBlocks(payload) {
			out = append(out, blockCandidate{raw: block, injected: injected})
		}
	}
	return out
}

// blockLocator points at one occurrence of a block without carrying its body.
// For transcripts the address is (jsonl_line, block_index); the skill re-reads
// the line, re-segments it (segmenter v1), picks block_index, and verifies the
// raw block against ContentSHA before acting.
type blockLocator struct {
	RootKind   string `json:"root_kind"`
	RelPath    string `json:"rel_path"`
	JSONLLine  int    `json:"jsonl_line"`
	BlockIndex int    `json:"block_index"`
	ContentSHA string `json:"content_sha256"`
}

// recurringOccurrence is uncapped scan-local position data used only to weight
// repeated context against later provider-counted turns. Unlike Locators it is
// never emitted in sink evidence and carries no block body.
type recurringOccurrence struct {
	RootKind  string
	RelPath   string
	JSONLLine int
	Timestamp string
	Tokens    int
	Side      bool
}

type fpAgg struct {
	sessions    map[string]bool
	occurrences int
	blockTokens int
	totalTokens int
	blockLines  int
	locators    []blockLocator
	positions   []recurringOccurrence
	firstSeen   string
	lastSeen    string
}

type recurringEntry struct {
	Fingerprint string
	Sessions    int
	Occurrences int
	BlockTokens int
	TotalTokens int
	BlockLines  int
	Locators    []blockLocator
	Positions   []recurringOccurrence
	First, Last string
}

type recurringResult struct {
	Repaste []recurringEntry
}

type recurringMiner struct {
	byFP           map[string]*fpAgg
	trackPositions bool
}

func newRecurringMiner() *recurringMiner {
	return &recurringMiner{byFP: map[string]*fpAgg{}}
}

// newRecurringStreamMiner retains occurrence coordinates for retro stream
// weighting. The ordinary learn scan deliberately uses newRecurringMiner so
// its memory stays bounded by locator samples rather than transcript length.
func newRecurringStreamMiner() *recurringMiner {
	return &recurringMiner{byFP: map[string]*fpAgg{}, trackPositions: true}
}

// merge folds one session-local miner into the scan-wide miner. Callers merge
// sessions in path order so capped locator samples stay deterministic even when
// transcript parsing runs concurrently.
func (m *recurringMiner) merge(other *recurringMiner) {
	if m == nil || other == nil {
		return
	}
	for fp, src := range other.byFP {
		dst := m.byFP[fp]
		if dst == nil {
			dst = &fpAgg{sessions: map[string]bool{}}
			m.byFP[fp] = dst
		}
		for session := range src.sessions {
			dst.sessions[session] = true
		}
		dst.occurrences += src.occurrences
		dst.totalTokens += src.totalTokens
		if m.trackPositions {
			dst.positions = append(dst.positions, src.positions...)
		}
		if src.blockTokens > dst.blockTokens {
			dst.blockTokens = src.blockTokens
			dst.blockLines = src.blockLines
		}
		remaining := maxLocatorSamples - len(dst.locators)
		if remaining > len(src.locators) {
			remaining = len(src.locators)
		}
		if remaining > 0 {
			dst.locators = append(dst.locators, src.locators[:remaining]...)
		}
		if src.firstSeen != "" && (dst.firstSeen == "" || src.firstSeen < dst.firstSeen) {
			dst.firstSeen = src.firstSeen
		}
		if src.lastSeen > dst.lastSeen {
			dst.lastSeen = src.lastSeen
		}
	}
}

// segmentBlocks splits one text payload into candidate blocks on blank-line
// boundaries, preserving each block's internal bytes verbatim. The editing skill
// reproduces this exactly to locate a block by index.
func segmentBlocks(text string) []string {
	parts := reBlankLine.Split(text, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(p, "\n")
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// extractTurnBlocks pulls the human-readable text payloads out of one transcript
// turn (message.content as a string, or "text" blocks in a list) and segments
// them in document order. Tool inputs and tool results are excluded — the target
// is re-established narrative/context, not tool noise — and so is context the
// host or caveman's own hook injected (#1459).
func extractTurnBlocks(obj map[string]any) []blockCandidate {
	msg := asMap(obj["message"])
	content := msg["content"]
	if content == nil {
		content = obj["content"]
	}
	var payloads []string
	switch c := content.(type) {
	case string:
		payloads = append(payloads, c)
	case []any:
		for _, item := range c {
			block := asMap(item)
			if block == nil || block["type"] != "text" {
				continue
			}
			if t := firstString(block["text"]); t != "" {
				payloads = append(payloads, t)
			}
		}
	}
	return minableBlocks(payloads)
}

// normalizeBlock is the internal grouping key (lowercase, digit-run masked,
// whitespace collapsed) so the same block with trivial numeric/whitespace drift
// still collides. It is the proxy's concern only — the skill never needs it.
func normalizeBlock(block string) string {
	s := strings.ToLower(block)
	s = reDigits.ReplaceAllString(s, "#")
	s = reWhitespace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func contentSHA256(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// observeTurn segments one transcript turn and records each qualifying block's
// fingerprint, distinct session, and a capped locator sample. It never stores the
// block text.
func (m *recurringMiner) observeTurn(rootKind, relPath string, jsonlLine int, ts string, obj map[string]any) {
	m.observeBlocks(rootKind, relPath, jsonlLine, ts, extractTurnBlocks(obj), obj["isSidechain"] == true)
}

// observeTextPayloads is the normalized-source entrypoint. Payload boundaries
// stay in document order and each payload uses segmenter v1 independently,
// producing the same flattened block indexes as extractTurnBlocks.
func (m *recurringMiner) observeTextPayloads(rootKind, relPath string, jsonlLine int, ts string, payloads []string, side bool) {
	m.observeBlocks(rootKind, relPath, jsonlLine, ts, minableBlocks(payloads), side)
}

func (m *recurringMiner) observeBlocks(rootKind, relPath string, jsonlLine int, ts string, blocks []blockCandidate, side bool) {
	if m == nil {
		return
	}
	for idx, candidate := range blocks {
		// idx still counts injected blocks: it is the locator address the
		// editing skill re-derives a body with.
		if candidate.injected {
			continue
		}
		raw := candidate.raw
		tokens := estimateTokens(raw)
		if tokens < minBlockTokens {
			continue
		}
		fp := hashText(normalizeBlock(raw))
		agg := m.byFP[fp]
		if agg == nil {
			agg = &fpAgg{sessions: map[string]bool{}}
			m.byFP[fp] = agg
		}
		agg.sessions[rootKind+"\x00"+relPath] = true
		agg.occurrences++
		agg.totalTokens += tokens
		if m.trackPositions {
			agg.positions = append(agg.positions, recurringOccurrence{
				RootKind: rootKind, RelPath: relPath, JSONLLine: jsonlLine,
				Timestamp: ts, Tokens: tokens, Side: side,
			})
		}
		if tokens > agg.blockTokens {
			agg.blockTokens = tokens
			agg.blockLines = strings.Count(raw, "\n") + 1
		}
		if len(agg.locators) < maxLocatorSamples {
			agg.locators = append(agg.locators, blockLocator{
				RootKind: rootKind, RelPath: relPath, JSONLLine: jsonlLine,
				BlockIndex: idx, ContentSHA: contentSHA256(raw),
			})
		}
		if ts != "" {
			if agg.firstSeen == "" || ts < agg.firstSeen {
				agg.firstSeen = ts
			}
			if ts > agg.lastSeen {
				agg.lastSeen = ts
			}
		}
	}
}

// result applies the recurrence + weight thresholds and returns entries ordered
// deterministically (by total recurring weight, then fingerprint).
func (m *recurringMiner) result() recurringResult {
	var res recurringResult
	if m == nil {
		return res
	}
	for fp, agg := range m.byFP {
		if len(agg.sessions) < minRecurrenceSessions {
			continue
		}
		res.Repaste = append(res.Repaste, recurringEntry{
			Fingerprint: fp,
			Sessions:    len(agg.sessions),
			Occurrences: agg.occurrences,
			BlockTokens: agg.blockTokens,
			TotalTokens: agg.totalTokens,
			BlockLines:  agg.blockLines,
			Locators:    agg.locators,
			Positions:   append([]recurringOccurrence(nil), agg.positions...),
			First:       agg.firstSeen,
			Last:        agg.lastSeen,
		})
	}
	sort.SliceStable(res.Repaste, func(i, j int) bool {
		wi := res.Repaste[i].TotalTokens
		wj := res.Repaste[j].TotalTokens
		if wi != wj {
			return wi > wj
		}
		return res.Repaste[i].Fingerprint < res.Repaste[j].Fingerprint
	})
	return res
}

// recurringPerTurn amortizes actual observed occurrence sizes over scanned
// turns. Normalized variants can differ sharply in raw size, so multiplying the
// largest occurrence by count would overstate inferred headroom.
func recurringPerTurn(e recurringEntry, turns int) int {
	if turns <= 0 {
		return e.BlockTokens
	}
	return (e.TotalTokens + turns - 1) / turns
}

// recurringSinks turns mined cross-session re-paste into recurring_context sinks
// whose fix is cavemem_offload. Soft, currency-free, evidence carries no body.
func recurringSinks(rec recurringResult, beh behaviorScan, turnsPerDay float64) []Sink {
	var sinks []Sink
	for _, e := range rec.Repaste {
		perTurn := recurringPerTurn(e, beh.Turns)
		sinks = append(sinks, Sink{
			SinkID:           "recurring_context:repaste:" + e.Fingerprint,
			Title:            fmt.Sprintf("The same text (up to ~%s tokens) was pasted again in %s", commaInt(int64(e.BlockTokens)), plural(e.Sessions, "session")),
			Class:            classRecurringContext,
			Basis:            observedLocal,
			TokensPerTurn:    int64(perTurn),
			TokensPerDayRate: rate(perTurn, turnsPerDay),
			Framing:          framingForward,
			Suggestion:       "Consider moving this to Caveman memory (cavemem). The agent then recalls a short version when it needs it, instead of pasting it into each session. Repeating in these sessions doesn't prove the text is unneeded.",
			Evidence: map[string]any{
				"fix_kind":                "cavemem_offload",
				"fingerprint":             e.Fingerprint,
				"recurrence_sessions":     e.Sessions,
				"occurrences_total":       e.Occurrences,
				"block_tokens":            e.BlockTokens,
				"occurrence_tokens_total": e.TotalTokens,
				"block_lines":             e.BlockLines,
				"first_seen":              e.First,
				"last_seen":               e.Last,
				"segmenter":               recurSegmenterVersion,
				"normalization":           recurNormalizationVer,
				"topic_label":             "recurring context block " + e.Fingerprint,
				"locators":                e.Locators,
			},
		})
	}
	return sinks
}
