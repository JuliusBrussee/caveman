package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// detect_procedures.go mines the user's own trajectories for procedures they
// repeat, so a repeated sequence can be distilled into a skill instead of being
// re-derived from scratch every session.
//
// This is the trajectory-distillation idea with the failure mode taken
// seriously. A distilled skill is not free: it loads into the prefix on EVERY
// session, and only pays back on the sessions that hit its pattern. That is the
// exact shape of the dead_load sink this same report punishes. So:
//
//   - mining only ever produces a CANDIDATE, never an assertion of savings;
//   - the candidate carries the measured cost of re-deriving the procedure and
//     the number of sessions it recurred in, so payback is arguable in advance;
//   - the fix kind routes to the holdout harness (learn_experiment.go), NOT to
//     the net-token-negative gate, because a re-count of a file cannot grade a
//     change whose cost and benefit land in different places.
//
// Sequences are of normalized tool SIGNATURES (Read(*.go), Bash(pytest)), which
// already discard paths, arguments and queries. No prompt text, file content,
// or command line is mined, stored, or reported.

const (
	procedureMinLength   = 3
	procedureMaxLength   = 6
	procedureMinSessions = 3
	procedureMinTokens   = 15_000
	procedureMaxRows     = 5
	// procedureMaxCallsPerSession bounds n-gram extraction on very long
	// sessions; the cap is disclosed on the sink rather than silently applied.
	procedureMaxCallsPerSession = 400
)

type procedureAgg struct {
	Steps    []string
	Sessions map[string]bool
	Tokens   int64
	Count    int
}

type procedureMiner struct {
	byKey     map[string]*procedureAgg
	truncated bool
}

func newProcedureMiner() procedureMiner {
	return procedureMiner{byKey: map[string]*procedureAgg{}}
}

// observeSession extracts every n-gram of tool signatures in the session's call
// order and books the output tokens the span consumed.
func (m *procedureMiner) observeSession(sessionID string, calls []learnToolCall) {
	if m.byKey == nil {
		m.byKey = map[string]*procedureAgg{}
	}
	signatures := make([]string, 0, len(calls))
	tokens := make([]int64, 0, len(calls))
	for _, call := range calls {
		signature := toolSignature(call.Name, call.Input)
		if signature == "" {
			continue
		}
		signatures = append(signatures, signature)
		tokens = append(tokens, int64(max(0, call.OutputTokens)))
	}
	if len(signatures) > procedureMaxCallsPerSession {
		signatures = signatures[:procedureMaxCallsPerSession]
		tokens = tokens[:procedureMaxCallsPerSession]
		m.truncated = true
	}
	seenInSession := map[string]bool{}
	for length := procedureMinLength; length <= procedureMaxLength; length++ {
		for start := 0; start+length <= len(signatures); start++ {
			steps := signatures[start : start+length]
			if !meaningfulProcedure(steps) {
				continue
			}
			var span int64
			for _, value := range tokens[start : start+length] {
				span += value
			}
			key := procedureKey(steps)
			agg := m.byKey[key]
			if agg == nil {
				agg = &procedureAgg{Steps: append([]string(nil), steps...), Sessions: map[string]bool{}}
				m.byKey[key] = agg
			}
			agg.Count++
			agg.Sessions[sessionID] = true
			// One session contributes its span cost once per distinct
			// procedure, so a tight loop cannot inflate a candidate's value.
			if !seenInSession[key] {
				agg.Tokens += span
				seenInSession[key] = true
			}
		}
	}
}

func (m *procedureMiner) merge(other procedureMiner) {
	if len(other.byKey) == 0 {
		m.truncated = m.truncated || other.truncated
		return
	}
	if m.byKey == nil {
		m.byKey = map[string]*procedureAgg{}
	}
	for key, src := range other.byKey {
		dst := m.byKey[key]
		if dst == nil {
			dst = &procedureAgg{Steps: src.Steps, Sessions: map[string]bool{}}
			m.byKey[key] = dst
		}
		dst.Count += src.Count
		dst.Tokens += src.Tokens
		for session := range src.Sessions {
			dst.Sessions[session] = true
		}
	}
	m.truncated = m.truncated || other.truncated
}

// meaningfulProcedure rejects sequences that are not a procedure: a single call
// repeated. A skill distilled from "Read, Read, Read" teaches nothing.
func meaningfulProcedure(steps []string) bool {
	distinct := map[string]bool{}
	for _, step := range steps {
		distinct[step] = true
	}
	return len(distinct) >= 2
}

func procedureKey(steps []string) string {
	sum := sha256.Sum256([]byte(strings.Join(steps, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// procedureSinks emits at most a handful of distillation candidates, ranked by
// how much re-derivation they represent.
func procedureSinks(miner procedureMiner, spend *LearnSpend) []Sink {
	type row struct {
		key string
		agg *procedureAgg
	}
	var rows []row
	for key, agg := range miner.byKey {
		if len(agg.Sessions) < procedureMinSessions || agg.Tokens < procedureMinTokens {
			continue
		}
		rows = append(rows, row{key: key, agg: agg})
	}
	if len(rows) == 0 {
		return nil
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].agg.Tokens != rows[j].agg.Tokens {
			return rows[i].agg.Tokens > rows[j].agg.Tokens
		}
		return rows[i].key < rows[j].key
	})
	// Keep the longest distinct procedures: a 3-step candidate contained in a
	// kept 5-step one teaches the same thing twice.
	var kept []row
	for _, candidate := range rows {
		if len(kept) >= procedureMaxRows {
			break
		}
		overlapping := false
		for _, existing := range kept {
			if sharesProcedureBody(existing.agg.Steps, candidate.agg.Steps) {
				overlapping = true
				break
			}
		}
		if !overlapping {
			kept = append(kept, candidate)
		}
	}

	sinks := make([]Sink, 0, len(kept))
	for _, entry := range kept {
		evidence := map[string]any{
			"steps":                 entry.agg.Steps,
			"sessions":              len(entry.agg.Sessions),
			"occurrences":           entry.agg.Count,
			"tokens_observed":       entry.agg.Tokens,
			"fix_kind":              "skill_distillation",
			"grading":               "holdout only - a distilled skill costs prefix tokens every session and pays back only on sessions that hit the pattern, so a file re-count cannot grade it",
			"content_mined":         "normalized tool signatures only; no prompt text, file content, or command arguments",
			"signature_token_basis": "estimated_local",
		}
		if miner.truncated {
			evidence["truncated"] = fmt.Sprintf("sessions longer than %d tool calls were truncated before mining", procedureMaxCallsPerSession)
		}
		if spend != nil {
			if usd := spend.priceInputTokens(entry.agg.Tokens); usd > 0 {
				evidence["spend_usd_observed"] = usd
			}
		}
		sinks = append(sinks, Sink{
			SinkID: "procedure_repeat:" + entry.key,
			Title: fmt.Sprintf("A %d-step procedure recurred in %d sessions (%s tokens)",
				len(entry.agg.Steps), len(entry.agg.Sessions), humanTokens(entry.agg.Tokens)),
			Class: classBehavioral, Basis: learnBasis, Framing: framingHistorical,
			TokensObserved: entry.agg.Tokens,
			Evidence:       evidence,
			Suggestion:     "This sequence is re-derived from scratch each time it comes up. Writing it down as a skill may cut that, but a skill also loads every session - so it is worth proving with `caveman learn experiment` rather than assuming.",
		})
	}
	return sinks
}

// sharesProcedureBody reports whether one step sequence contains the other, in
// order. Used to keep near-duplicate candidates out of the report.
func sharesProcedureBody(a, b []string) bool {
	long, short := a, b
	if len(b) > len(a) {
		long, short = b, a
	}
	return strings.Contains(strings.Join(long, "\x00"), strings.Join(short, "\x00"))
}
