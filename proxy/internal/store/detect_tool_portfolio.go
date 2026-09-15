package store

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// detect_tool_portfolio.go answers "what are my tokens actually made of?".
//
// The retro pass already measures one blended would-cut figure for tool
// outputs. A blended number is not actionable: nobody can decide anything from
// "the engine would cut 1.4M tokens". This groups the SAME measured output
// volume by normalized tool signature, so the report can name the five call
// shapes that dominate — `Read(*.lock)`, `Bash(git diff)`, `mcp__x__query` —
// and the user can act on the specific one.
//
// Token counts here are the local estimator over transcript tool results, not
// provider-counted usage: a tool result's tokens are billed on the NEXT
// request, blended into that turn's input. The estimate is stated as such and
// never blended into the provider-counted spend headline.

const (
	toolPortfolioMinTokens     = 20_000 // per signature, over the window
	toolPortfolioMinCalls      = 3
	toolPortfolioMaxRows       = 8
	toolPortfolioMinTotalShare = 5.0 // percent of measured tool output
)

type toolSignatureTotals struct {
	Signature    string
	Calls        int
	Tokens       int64
	ErrorCalls   int
	LargestCall  int64
	SampleInputs []string
}

type toolPortfolioTracker struct {
	bySignature map[string]*toolSignatureTotals
}

func newToolPortfolioTracker() toolPortfolioTracker {
	return toolPortfolioTracker{bySignature: map[string]*toolSignatureTotals{}}
}

func (t *toolPortfolioTracker) observe(call learnToolCall) {
	if t.bySignature == nil {
		t.bySignature = map[string]*toolSignatureTotals{}
	}
	if call.OutputTokens <= 0 {
		return
	}
	signature := toolSignature(call.Name, call.Input)
	if signature == "" {
		return
	}
	entry := t.bySignature[signature]
	if entry == nil {
		entry = &toolSignatureTotals{Signature: signature}
		t.bySignature[signature] = entry
	}
	entry.Calls++
	entry.Tokens += int64(call.OutputTokens)
	if call.IsError {
		entry.ErrorCalls++
	}
	if int64(call.OutputTokens) > entry.LargestCall {
		entry.LargestCall = int64(call.OutputTokens)
	}
}

func (t *toolPortfolioTracker) merge(other toolPortfolioTracker) {
	if len(other.bySignature) == 0 {
		return
	}
	if t.bySignature == nil {
		t.bySignature = map[string]*toolSignatureTotals{}
	}
	for signature, src := range other.bySignature {
		dst := t.bySignature[signature]
		if dst == nil {
			dst = &toolSignatureTotals{Signature: signature}
			t.bySignature[signature] = dst
		}
		dst.Calls += src.Calls
		dst.Tokens += src.Tokens
		dst.ErrorCalls += src.ErrorCalls
		if src.LargestCall > dst.LargestCall {
			dst.LargestCall = src.LargestCall
		}
	}
}

// bashVerb captures the leading command words of a shell invocation, which is
// what makes `Bash(git diff)` distinguishable from `Bash(pytest)`. Flags and
// arguments are dropped: they carry paths and secrets and add nothing to the
// grouping.
var bashVerb = regexp.MustCompile(`^[a-zA-Z0-9_.\-/]+`)

// toolSignature normalizes a call into a groupable shape. It deliberately
// discards everything specific: no full paths, no arguments, no queries. The
// signature has to be safe to print in a report and stable enough that a
// hundred calls collapse into one row.
func toolSignature(name, input string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	input = strings.TrimSpace(input)
	// MCP tools are already namespaced by server and tool; the name IS the shape.
	if strings.HasPrefix(name, "mcp__") {
		return name
	}
	switch name {
	case "Read", "Write", "Edit", "NotebookEdit":
		return name + "(" + pathShape(input) + ")"
	case "Bash", "BashOutput":
		return name + "(" + bashShape(input) + ")"
	default:
		return name
	}
}

// pathShape reduces a path to its extension class. A lockfile read and a source
// read are different cost shapes; which lockfile is nobody's business.
func pathShape(input string) string {
	if input == "" {
		return "?"
	}
	base := filepath.Base(input)
	switch {
	case strings.HasSuffix(base, ".lock") || base == "package-lock.json" || base == "yarn.lock" || base == "pnpm-lock.yaml" || base == "Cargo.lock" || base == "go.sum" || base == "poetry.lock":
		return "lockfile"
	case strings.HasPrefix(base, "."):
		return "dotfile"
	}
	ext := strings.TrimPrefix(filepath.Ext(base), ".")
	if ext == "" {
		return "no-ext"
	}
	if len(ext) > 12 {
		return "no-ext"
	}
	return "*." + strings.ToLower(ext)
}

// bashShape keeps the command verb and, for a few multiplexing tools, its
// subcommand — `git diff` costs very differently from `git status`.
func bashShape(input string) string {
	if input == "" {
		return "?"
	}
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return "?"
	}
	verb := bashVerb.FindString(filepath.Base(fields[0]))
	if verb == "" {
		return "?"
	}
	multiplexers := map[string]bool{"git": true, "npm": true, "pnpm": true, "yarn": true, "cargo": true, "go": true, "docker": true, "kubectl": true, "uv": true, "make": true}
	if multiplexers[verb] {
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "-") {
				continue
			}
			sub := bashVerb.FindString(field)
			if sub != "" {
				return verb + " " + sub
			}
			break
		}
	}
	return verb
}

// toolPortfolioSink ranks measured tool-output volume by signature. Class is
// behavioral: the fix is either a habit change (narrower reads, quieter
// commands) or the wrap compressing those outputs — neither is a config edit
// this side can make, so the suggestion stays soft.
func toolPortfolioSink(tracker toolPortfolioTracker, spend *LearnSpend) []Sink {
	if len(tracker.bySignature) == 0 {
		return nil
	}
	rows := make([]*toolSignatureTotals, 0, len(tracker.bySignature))
	var total int64
	for _, entry := range tracker.bySignature {
		rows = append(rows, entry)
		total += entry.Tokens
	}
	if total <= 0 {
		return nil
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Tokens != rows[j].Tokens {
			return rows[i].Tokens > rows[j].Tokens
		}
		return rows[i].Signature < rows[j].Signature
	})

	var kept []map[string]any
	var keptTokens int64
	for _, entry := range rows {
		if len(kept) >= toolPortfolioMaxRows {
			break
		}
		share := float64(entry.Tokens) * 100 / float64(total)
		if entry.Tokens < toolPortfolioMinTokens || entry.Calls < toolPortfolioMinCalls || share < toolPortfolioMinTotalShare {
			continue
		}
		row := map[string]any{
			"signature":         entry.Signature,
			"calls":             entry.Calls,
			"tokens":            entry.Tokens,
			"share_pct":         roundPct(share),
			"median_call_share": roundPct(float64(entry.Tokens) / float64(entry.Calls)),
			"largest_call":      entry.LargestCall,
		}
		if entry.ErrorCalls > 0 {
			row["error_calls"] = entry.ErrorCalls
		}
		if spend != nil {
			if usd := spend.priceInputTokens(entry.Tokens); usd > 0 {
				row["spend_usd_next_turn"] = usd
			}
		}
		kept = append(kept, row)
		keptTokens += entry.Tokens
	}
	if len(kept) == 0 {
		return nil
	}
	top, _ := kept[0]["signature"].(string)
	evidence := map[string]any{
		"top_signatures":         kept,
		"tool_output_tokens":     total,
		"distinct_signatures":    len(rows),
		"signature_token_basis":  "estimated_local",
		"ranked_share_pct":       roundPct(float64(keptTokens) * 100 / float64(total)),
		"tokens_are_billed_when": "a tool result is billed as input on the following request, not on the turn that produced it",
	}
	return []Sink{{
		SinkID: "tool_output_portfolio",
		Title:  fmt.Sprintf("Tool output is %s tokens; %s is the heaviest call shape", humanTokens(total), top),
		Class:  classBehavioral, Basis: learnBasis, Framing: framingHistorical,
		TokensObserved: total,
		Evidence:       evidence,
		Suggestion:     "These call shapes dominate what re-enters context. Narrower reads and quieter commands cut them at the source; the wrap compresses them in place. Both are worth measuring against this list before changing anything.",
	}}
}
