package store

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func DefaultReportPath(home, trialID string) string {
	if trialID == "" {
		trialID = "local"
	}
	return filepath.Join(home, "reports", "caveman-trial-"+trialID+".html")
}

func DefaultLearnReportPath(home string) string {
	return filepath.Join(home, "reports", "caveman-learn.html")
}

func DefaultLearnJSONPath(home string) string {
	return filepath.Join(home, "reports", "caveman-learn.json")
}

type LearnSnapshot struct {
	LearnPlan
	Moves       int    `json:"moves"`
	GeneratedAt string `json:"generated_at"`
	Generation  string `json:"generation,omitempty"`
}

// WriteLearnSidecars persists the current machine-readable plan plus a bounded
// dated history used by local learn diffs. Both states with and without moves
// are written; HTML existence is never used as plan-state evidence.
func (s *Store) WriteLearnSidecars(home string, plan LearnPlan, now time.Time, generation string) (string, error) {
	dir := filepath.Join(home, "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	snapshot := LearnSnapshot{
		LearnPlan:   plan,
		Moves:       len(plan.Sinks),
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Generation:  generation,
	}
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	current := DefaultLearnJSONPath(home)
	if err := os.WriteFile(current, raw, 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(current, 0o600); err != nil {
		return "", err
	}
	history := filepath.Join(dir, "caveman-learn."+now.UTC().Format("2006-01-02")+".json")
	if err := os.WriteFile(history, raw, 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(history, 0o600); err != nil {
		return "", err
	}
	entries, _ := filepath.Glob(filepath.Join(dir, "caveman-learn.????-??-??.json"))
	sort.Strings(entries)
	for len(entries) > 8 {
		_ = os.Remove(entries[0])
		entries = entries[1:]
	}
	return current, nil
}

func (s *Store) WriteLearnHTML(plan LearnPlan, outPath string) error {
	if outPath == "" {
		return fmt.Errorf("report path is required")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return learnTemplate.Execute(f, struct {
		Plan      LearnPlan
		Generated string
	}{Plan: plan, Generated: time.Now().UTC().Format("2006-01-02 15:04 UTC")})
}

func (s *Store) WriteTrialHTML(plan TrialPlan, outPath string) error {
	if outPath == "" {
		return fmt.Errorf("report path is required")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return trialTemplate.Execute(f, struct {
		Plan      TrialPlan
		Generated string
	}{Plan: plan, Generated: time.Now().UTC().Format(time.RFC3339)})
}

func (s *Store) ExportTrial(plan TrialPlan, outDir string) (map[string]string, error) {
	if outDir == "" {
		return nil, fmt.Errorf("export directory is required")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, err
	}
	trialPath := filepath.Join(outDir, "trial.json")
	raw, _ := json.MarshalIndent(plan, "", "  ")
	if err := os.WriteFile(trialPath, append(raw, '\n'), 0o600); err != nil {
		return nil, err
	}
	spansPath := filepath.Join(outDir, "spans.jsonl")
	f, err := os.OpenFile(spansPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := s.writeExportSpans(f, plan); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return map[string]string{"trial_json": trialPath, "spans_jsonl": spansPath}, nil
}

func (s *Store) writeExportSpans(f *os.File, plan TrialPlan) error {
	enc := json.NewEncoder(f)
	label := ""
	if plan.TrialID != "" {
		label = "trial:" + plan.TrialID
	}
	rows, err := s.db.Query(
		`SELECT request_id, COALESCE(trace_id, ''), ts, COALESCE(label, ''),
		        COALESCE(agent_slug, ''), COALESCE(provider, ''), COALESCE(model, ''),
		        COALESCE(endpoint, ''), COALESCE(runtime_mode, ''), COALESCE(optimization_ids, ''),
		        COALESCE(input_tokens, 0), COALESCE(output_tokens, 0),
		        COALESCE(cached_input_tokens, 0), COALESCE(cache_creation_input_tokens, 0),
		        COALESCE(reasoning_tokens, 0),
		        COALESCE(total_cost_usd, 0), basis
		   FROM requests
		  WHERE (? = '' OR label = ?)
		  ORDER BY ts, id`,
		label, label,
	)
	if err != nil {
		return err
	}
	for rows.Next() {
		var requestID, traceID, ts, rowLabel, agent, provider, model, endpoint, mode, optimizers, basis string
		var input, output, cached, cacheCreation, reasoning int64
		var totalCost float64
		if err := rows.Scan(&requestID, &traceID, &ts, &rowLabel, &agent, &provider, &model, &endpoint, &mode, &optimizers, &input, &output, &cached, &cacheCreation, &reasoning, &totalCost, &basis); err != nil {
			_ = rows.Close()
			return err
		}
		if err := enc.Encode(map[string]any{
			"span_kind":                   "request",
			"source_kind":                 sourceCaveProxy,
			"trial_id":                    plan.TrialID,
			"request_id":                  requestID,
			"trace_id":                    traceID,
			"ts":                          ts,
			"label":                       rowLabel,
			"agent_slug":                  agent,
			"provider":                    provider,
			"model":                       model,
			"endpoint":                    endpoint,
			"runtime_mode":                mode,
			"optimization_ids":            optimizers,
			"input_tokens":                input,
			"output_tokens":               output,
			"cached_input_tokens":         cached,
			"cache_creation_input_tokens": cacheCreation,
			"reasoning_tokens":            reasoning,
			"total_cost_usd":              roundUSD(totalCost),
			"basis":                       basis,
		}); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	imported, err := s.db.Query(
		`SELECT source_kind, COALESCE(source_path, ''), event_id, ts,
		        COALESCE(agent_slug, ''), COALESCE(provider, ''), COALESCE(model, ''),
		        COALESCE(requests, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0),
		        COALESCE(cached_input_tokens, 0), COALESCE(cache_creation_input_tokens, 0),
		        COALESCE(reasoning_tokens, 0),
		        COALESCE(total_cost_usd, 0), basis
		   FROM usage_events
		  ORDER BY ts, id`,
	)
	if err != nil {
		return err
	}
	for imported.Next() {
		var sourceKind, sourcePath, eventID, ts, agent, provider, model, basis string
		var requests, input, output, cached, cacheCreation, reasoning int64
		var totalCost float64
		if err := imported.Scan(&sourceKind, &sourcePath, &eventID, &ts, &agent, &provider, &model, &requests, &input, &output, &cached, &cacheCreation, &reasoning, &totalCost, &basis); err != nil {
			_ = imported.Close()
			return err
		}
		if err := enc.Encode(map[string]any{
			"span_kind":                   "usage_event",
			"source_kind":                 sourceKind,
			"source_path":                 sourcePath,
			"event_id":                    eventID,
			"ts":                          ts,
			"agent_slug":                  agent,
			"provider":                    provider,
			"model":                       model,
			"requests":                    requests,
			"input_tokens":                input,
			"output_tokens":               output,
			"cached_input_tokens":         cached,
			"cache_creation_input_tokens": cacheCreation,
			"reasoning_tokens":            reasoning,
			"total_cost_usd":              roundUSD(totalCost),
			"basis":                       basis,
		}); err != nil {
			_ = imported.Close()
			return err
		}
	}
	if err := imported.Err(); err != nil {
		_ = imported.Close()
		return err
	}
	return imported.Close()
}

// commaInt renders 1234567 as "1,234,567"; shared by the template and the TLDR.
func commaInt(v int64) string {
	s := fmt.Sprintf("%d", v)
	n := len(s)
	if n <= 3 {
		return s
	}
	var b strings.Builder
	pre := n % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if n > pre {
			b.WriteString(",")
		}
	}
	for i := pre; i < n; i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < n {
			b.WriteString(",")
		}
	}
	return b.String()
}

// Input-token list prices in $/MTok, checked 2026-08 (Claude from Anthropic
// docs; GPT-5.6 after OpenAI's 2026-07-30 Terra/Luna cuts). Used only for the
// clearly-labeled cost chart in the HTML report: list-price arithmetic over
// the forward rate at an assumed cache mix, never a bill, a savings claim, or
// a verified figure.
var costModels = []struct {
	family string
	label  string
	rate   float64
}{
	{"claude", "Fable 5", 10.0},
	{"claude", "Opus 5", 5.0},
	{"claude", "Sonnet 5", 3.0},
	{"gpt56", "Sol", 5.0},
	{"gpt56", "Terra", 2.0},
	{"gpt56", "Luna", 0.20},
}

func fmtUSD(usd float64) string {
	if usd > 0 && usd < 0.01 {
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", usd)
}

func fmtRate(rate float64) string {
	switch {
	case rate == float64(int64(rate)):
		return fmt.Sprintf("$%.0f/MTok in", rate)
	case rate < 0.1:
		return fmt.Sprintf("$%.3f/MTok in", rate)
	}
	return fmt.Sprintf("$%.2f/MTok in", rate)
}

// The cost chart assumes a 90% cache hit rate. Cache reads bill at 10% of the
// input list price on both Anthropic and OpenAI, so the effective rate is
// 0.1 + 0.9*0.1 = 0.19x list. An assumption, not this user's measured mix.
const (
	chartCacheHitRate      = 0.90
	chartCacheReadDiscount = 0.10
)

func cachedRate(list float64) float64 {
	return list * ((1 - chartCacheHitRate) + chartCacheHitRate*chartCacheReadDiscount)
}

// costRow is one bar in the 30-day cost chart.
type costRow struct {
	Label string
	Rate  string
	USD   string
	Pct   int
}

// costFamily is one tab of the chart's family switch.
type costFamily struct {
	ID   string
	Name string
	Rows []costRow
}

// costFamilies prices the total forward tokens/day rate over 30 days at each
// model's input list price under the chart's 90%-cache assumption, grouped by
// provider family for the chart's tab switch. Bar widths share one scale
// across families (relative to the most expensive model anywhere) so
// switching tabs stays comparable.
func costFamilies(sinks []Sink) []costFamily {
	total := sumPerDay(sinks)
	if total <= 0 {
		return nil
	}
	var max float64
	for _, m := range costModels {
		if m.rate > max {
			max = m.rate
		}
	}
	claude := costFamily{ID: "claude", Name: "Claude"}
	gpt := costFamily{ID: "gpt56", Name: "GPT-5.6"}
	for _, m := range costModels {
		rate := cachedRate(m.rate)
		row := costRow{
			Label: m.label,
			Rate:  fmtRate(rate),
			USD:   fmtUSD(float64(total) * 30 / 1e6 * rate),
			Pct:   int(m.rate / max * 100),
		}
		if m.family == "claude" {
			claude.Rows = append(claude.Rows, row)
		} else {
			gpt.Rows = append(gpt.Rows, row)
		}
	}
	return []costFamily{claude, gpt}
}

// depthBar is one column of the session context-depth histogram. Columns at
// or past 30% of the window shade muted amber, past 50% muted red, matching
// the dumbzone guidance; counts are always printed and a legend names each
// tone, so color never carries the number alone.
type depthBar struct {
	Label string
	Count int
	Pct   int
	Color string
}

func depthBars(d *LearnContextDepth) []depthBar {
	if d == nil {
		return nil
	}
	max := 0
	for _, c := range d.Buckets {
		if c > max {
			max = c
		}
	}
	if max == 0 {
		return nil
	}
	out := make([]depthBar, 0, len(d.Buckets))
	for i, c := range d.Buckets {
		color := "#cfcdc7"
		switch {
		case i >= 5:
			color = "#e0837c"
		case i >= 3:
			color = "#e0b357"
		}
		pct := c * 100 / max
		if c > 0 && pct < 4 {
			pct = 4
		}
		out = append(out, depthBar{
			Label: fmt.Sprintf("%d-%d", i*10, (i+1)*10),
			Count: c,
			Pct:   pct,
			Color: color,
		})
	}
	return out
}

// pctOf is part as a whole-percent of whole, clamped to 0..100 for bar widths.
// Template callers must pass int64 fields; an int argument fails at Execute time.
func pctOf(part, whole int64) int {
	if whole <= 0 || part <= 0 {
		return 0
	}
	pct := int(part * 100 / whole)
	if pct > 100 {
		pct = 100
	}
	return pct
}

// famLabel is the plain-language display name for a retro family. The JSON
// contract keeps the fix-naming Label untouched; only the report translates.
func famLabel(f LearnRetroFamily) string {
	switch f.ID {
	case retroFamilyToolOutputs:
		return "big tool results, compressed"
	case retroFamilyRepeatedBlocks:
		return "re-pasted context, remembered once in cavemem"
	}
	return f.Label
}

// famSeg is one segment of the single stacked composition bar; widths are
// shares of the family total, floored so a small family stays visible.
type famSeg struct {
	Label  string
	Tokens int64
	Pct    int
	Color  string
}

func famSegs(families []LearnRetroFamily) []famSeg {
	var total int64
	for _, f := range families {
		total += f.Tokens
	}
	if total <= 0 {
		return nil
	}
	colors := map[string]string{
		retroFamilyToolOutputs:    "#448361",
		retroFamilyRepeatedBlocks: "#e0b357",
	}
	out := make([]famSeg, 0, len(families))
	for _, f := range families {
		if f.Tokens <= 0 {
			continue
		}
		pct := pctOf(f.Tokens, total)
		if pct < 3 {
			pct = 3
		}
		color := colors[f.ID]
		if color == "" {
			color = "#cfcdc7"
		}
		out = append(out, famSeg{Label: famLabel(f), Tokens: f.Tokens, Pct: pct, Color: color})
	}
	return out
}

// humanTokens compacts a token count for the big stat numbers: 21.7M, 855k.
// Exact values stay in tooltips and the JSON sidecar.
func humanTokens(v int64) string {
	switch {
	case v >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(v)/1e6), ".0") + "M"
	case v >= 10_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(v)/1e3), ".0") + "k"
	}
	return commaInt(v)
}

// topSinks caps the HTML report's sink list. Sinks arrive ranked by daily
// equivalent, so this is the head of that ranking; the JSON plan keeps all.
func topSinks(sinks []Sink) []Sink {
	const max = 20
	if len(sinks) > max {
		return sinks[:max]
	}
	return sinks
}

func sumPerDay(sinks []Sink) int64 {
	var total int64
	for _, s := range sinks {
		total += s.TokensPerDayRate
	}
	return total
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	if strings.HasSuffix(noun, "x") {
		return fmt.Sprintf("%d %ses", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// learnTLDR builds the caveman-voice summary at the top of the HTML report.
// Every number comes straight from the plan; framing stays forward-rate and
// token-only: never "wasted", never a dollar.
func learnTLDR(plan LearnPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cave score %d of 100.", plan.CaveScore.Score)
	if len(plan.Sinks) == 0 {
		b.WriteString(" No token sinks found. Setup lean.")
		savingsTLDR(&b, plan)
		b.WriteString(" Keep going.")
		return b.String()
	}
	var red, rec, beh int
	var redPerTurn int64
	for _, s := range plan.Sinks {
		switch s.Class {
		case classReducible:
			red++
			redPerTurn += s.TokensPerTurn
		case classRecurringContext:
			rec++
		case classBehavioral:
			beh++
		}
	}
	fmt.Fprintf(&b, " Found %s across %s.", plural(len(plan.Sinks), "token sink"), plural(plan.SessionsScanned, "scanned session"))
	top := plan.Sinks[0]
	if top.TokensObserved > 0 {
		fmt.Fprintf(&b, " Top sink by daily equivalent: %s, with %s tokens observed in the window (basis: %s).", top.Title, commaInt(top.TokensObserved), observedTokenBasis(top))
	} else {
		fmt.Fprintf(&b, " Top sink by daily equivalent: %s, at %s tokens/day.", top.Title, commaInt(top.TokensPerDayRate))
	}
	if red > 0 {
		fmt.Fprintf(&b, " %s safe to make now, worth ~%s tokens per turn.", plural(red, "fix"), commaInt(redPerTurn))
	}
	if rec > 0 {
		fmt.Fprintf(&b, " %s better live in cavemem.", plural(rec, "recurring block"))
	}
	if beh > 0 {
		fmt.Fprintf(&b, " %s observed. Changing habits is optional.", plural(beh, "habit"))
	}
	if d := plan.ContextDepth; d != nil && d.Over50Pct > 0 {
		fmt.Fprintf(&b, " %s ran past half the context window.", plural(d.Over50Pct, "session"))
	}
	savingsTLDR(&b, plan)
	b.WriteString(" Cost chart below. Approve fixes one by one. Cost go down.")
	return b.String()
}

func observedTokenBasis(sink Sink) string {
	if basis, _ := sink.Evidence["tokens_observed_basis"].(string); basis != "" {
		return basis
	}
	return sink.Basis
}

// savingsTLDR appends the wrap-measured and retro-replay sentences. Token-only,
// claiming only what was measured, and the retro figure is attributed to the
// fixes collectively — most of it can need a cavemem offload the wrap alone
// cannot deliver.
func savingsTLDR(b *strings.Builder, plan LearnPlan) {
	if w := plan.WrapMeasured; w != nil && w.TokensSaved > 0 {
		fmt.Fprintf(b, " Caveman already saved %s tokens.", humanTokens(w.TokensSaved))
	}
	if r := plan.Retro; r != nil && r.WouldCutStreamTokens > 0 {
		fmt.Fprintf(b, " Replay of %s: the fixes here could have saved %s of the %s tokens sent.",
			plural(r.SessionsScanned, "past session"), humanTokens(r.WouldCutStreamTokens), humanTokens(r.TokensObserved))
	}
}

var learnTemplate = template.Must(template.New("learn").Funcs(template.FuncMap{
	"tldr": learnTLDR,
	// comma accepts both int64 plan fields and int rates so no raw integer
	// renders unformatted next to formatted ones.
	"comma": func(v any) string {
		switch n := v.(type) {
		case int:
			return commaInt(int64(n))
		case int64:
			return commaInt(n)
		}
		return fmt.Sprintf("%v", v)
	},
	"plural":        plural,
	"costFamilies":  costFamilies,
	"sumPerDay":     sumPerDay,
	"depthBars":     depthBars,
	"pctOf":         pctOf,
	"famSegs":       famSegs,
	"human":         humanTokens,
	"observedBasis": observedTokenBasis,
	"topSinks":      topSinks,
	"classClass": func(class string) string {
		switch class {
		case classReducible:
			return "green"
		case classBehavioral:
			return "purple"
		case classRecurringContext:
			return "orange"
		default:
			return "gray"
		}
	},
	"classLabel": func(class string) string {
		switch class {
		case classReducible:
			return "safe fix"
		case classBehavioral:
			return "habit"
		case classRecurringContext:
			return "offload"
		default:
			return "load-bearing"
		}
	},
	"basisClass": func(basis string) string {
		if strings.EqualFold(basis, "verified") {
			return "red"
		}
		return "gray"
	},
	"scoreBasis": func(basis string) string {
		if strings.EqualFold(basis, "verified") {
			return "inferred"
		}
		return basis
	},
	"scoreColor": func(score int) string {
		switch {
		case score >= 80:
			return "#448361"
		case score >= 50:
			return "#cb912f"
		default:
			return "#d44c47"
		}
	},
	"evidence": func(m map[string]any) string {
		if len(m) == 0 {
			return ""
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
		}
		return strings.Join(parts, " · ")
	},
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Caveman Learn</title>
<style>
:root{color-scheme:light}
*{box-sizing:border-box}
body{margin:0;background:#fff;color:#37352f;font:16px/1.6 ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;-webkit-font-smoothing:antialiased}
main{max-width:708px;margin:0 auto;padding:72px 24px 120px}
.icon{font-size:60px;line-height:1}
h1{font-size:40px;font-weight:700;letter-spacing:-.01em;margin:10px 0 6px}
.meta{color:#787774;font-size:14px;margin:0 0 28px}
h2{font-size:24px;font-weight:600;letter-spacing:-.01em;margin:48px 0 6px}
.note{color:#787774;font-size:14px;margin:0 0 16px}
.callout{display:flex;gap:12px;background:#f7f7f5;border-radius:6px;padding:16px;font-size:15px}
.callout .emoji{font-size:20px;line-height:1.5;flex:none}
.callout .label{font-weight:600;font-size:12px;letter-spacing:.06em;text-transform:uppercase;color:#787774;margin-bottom:3px}
.pill{display:inline-block;border-radius:4px;padding:1px 7px;font-size:12px;font-weight:500;white-space:nowrap}
.pill.gray{background:#f1f1ef;color:#57564f}
.pill.green{background:#dbeddb;color:#1c3829}
.pill.orange{background:#fadec9;color:#49290e}
.pill.purple{background:#e8deee;color:#412454}
.pill.red{background:#ffe2dd;color:#5d1715}
.score-row{display:flex;align-items:baseline;gap:10px;margin:4px 0 12px}
.score-num{font-size:40px;font-weight:700;letter-spacing:-.02em}
.score-of{color:#787774;font-size:14px}
.bar{height:4px;border-radius:2px;background:#ededec;overflow:hidden;margin:0 0 20px}
.bar span{display:block;height:100%;border-radius:2px}
.props{border-top:1px solid #ededec}
.prop{display:grid;grid-template-columns:minmax(120px,160px) 1fr auto;gap:12px;padding:8px 0;border-bottom:1px solid #ededec;font-size:14px;align-items:baseline}
.prop .k{color:#787774}
.prop .v{font-variant-numeric:tabular-nums;color:#787774}
details{border-bottom:1px solid #ededec}
summary{display:flex;align-items:center;gap:10px;padding:10px 6px;cursor:pointer;list-style:none;border-radius:4px}
summary::-webkit-details-marker{display:none}
summary:hover{background:rgba(55,53,47,.05)}
.caret{color:#9b9a97;font-size:10px;transition:transform .15s ease;flex:none}
details[open] .caret{transform:rotate(90deg)}
summary .title{flex:1;font-weight:500;min-width:0}
summary .num{font-variant-numeric:tabular-nums;color:#787774;font-size:14px;white-space:nowrap}
.body{padding:2px 6px 14px 28px;font-size:14px}
.body p{margin:0 0 8px}
.kv{color:#787774;font-size:13px;overflow-wrap:anywhere}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px;color:#9b9a97;overflow-wrap:anywhere}
.empty{color:#787774;font-size:14px;padding:12px 0}
.chart{margin:4px 0}
.chart input{position:absolute;opacity:0;pointer-events:none}
.tabs{display:inline-flex;gap:2px;background:#f1f1ef;border-radius:6px;padding:2px;margin:0 0 10px}
.tabs label{font-size:13px;padding:3px 12px;border-radius:4px;color:#787774;cursor:pointer}
#fam-claude:checked ~ .tabs label[for=fam-claude],#fam-gpt56:checked ~ .tabs label[for=fam-gpt56]{background:#fff;color:#37352f;font-weight:500;box-shadow:0 1px 2px rgba(0,0,0,.08)}
#fam-claude:checked ~ #g-gpt56{display:none}
#fam-gpt56:checked ~ #g-claude{display:none}
.chart input:focus-visible ~ .tabs{outline:2px solid #2383e2;outline-offset:2px}
.crow{display:grid;grid-template-columns:96px 1fr 72px;gap:14px;align-items:center;padding:7px 0}
.clabel{font-size:14px;font-weight:500}
.crate{display:block;font-size:12px;font-weight:400;color:#787774}
.ctrack{display:block;height:16px;border-radius:0 4px 4px 0;background:#f1f1ef;overflow:hidden}
.cbar{display:block;height:100%;min-width:3px;border-radius:0 4px 4px 0;background:#57564f}
.crow:hover .cbar{background:#37352f}
.cval{font-variant-numeric:tabular-nums;font-size:14px;text-align:right}
.dcard{border:1px solid #ededec;border-radius:10px;padding:20px 24px 16px;margin:4px 0;background:#fff}
.kicker{font-size:11px;font-weight:600;letter-spacing:.08em;text-transform:uppercase;color:#9b9a97;margin:0 0 16px}
.dstats{display:flex;gap:40px;margin:0 0 20px}
.dstat .dn{display:block;font-size:26px;font-weight:700;letter-spacing:-.02em;line-height:1.15;font-variant-numeric:tabular-nums}
.dstat .dk{display:flex;align-items:center;gap:6px;font-size:12px;color:#787774;margin-top:3px}
.dot{display:inline-block;width:8px;height:8px;border-radius:50%;flex:none}
.hist{display:grid;grid-template-columns:repeat(10,1fr);gap:10px;align-items:end;height:140px;border-bottom:1px solid #e5e4e0;background-image:linear-gradient(to top,#f4f4f2 1px,transparent 1px);background-size:100% 35px}
.hcol{display:flex;flex-direction:column;justify-content:flex-end;align-items:center;height:100%}
.hcount{font-size:11px;color:#57564f;margin-bottom:4px;font-variant-numeric:tabular-nums}
.hbar{display:block;width:64%;border-radius:3px 3px 0 0}
.hcol:hover .hbar{filter:brightness(.92)}
.hlabels{display:grid;grid-template-columns:repeat(10,1fr);gap:10px;font-size:10px;color:#9b9a97;text-align:center;margin:6px 0 0}
.hlegend{display:flex;align-items:center;gap:16px;margin-top:14px;font-size:12px;color:#787774}
.hlegend>span{display:flex;align-items:center;gap:6px}
.hcap{margin-left:auto;color:#9b9a97}
.split{display:grid;grid-template-columns:1fr 1fr}
.panel{min-width:0;padding-right:28px}
.panel+.panel{border-left:1px solid #ededec;padding-left:28px;padding-right:0}
.pnum{font-size:34px;font-weight:700;letter-spacing:-.02em;font-variant-numeric:tabular-nums;line-height:1.15;margin:2px 0 10px}
.punit{font-size:14px;font-weight:400;color:#787774;margin-left:6px;letter-spacing:0}
.pcap{font-size:13px;color:#787774;margin:8px 0 0;line-height:1.5}
.pcap.dim{color:#9b9a97}
.meter{height:10px;border-radius:5px;background:#f1f1ef;overflow:hidden}
.meter span{display:block;height:100%;border-radius:5px;background:#448361;min-width:3px}
.stack{display:flex;height:12px;border-radius:6px;overflow:hidden;background:#f1f1ef;margin:22px 0 10px}
.stack span{display:block;height:100%}
.fine{font-size:12px;color:#9b9a97;margin:10px 0 0}
.dcard details{border-bottom:none;margin-top:12px}
.dcard summary{padding:6px 0;color:#787774;font-size:13px}
@media(max-width:560px){.dstats{flex-wrap:wrap;gap:20px}.split{grid-template-columns:1fr}.panel{padding-right:0}.panel+.panel{border-left:none;border-top:1px solid #ededec;padding:20px 0 0;margin-top:20px}}
ul.caveats{margin:0;padding-left:20px;color:#787774;font-size:14px}
ul.caveats li{margin:6px 0}
@media(max-width:560px){main{padding:48px 16px 96px}h1{font-size:32px}.icon{font-size:48px}}
</style>
</head>
<body>
<main>
<div class="icon">🪨</div>
<h1>Caveman Learn</h1>
<p class="meta">{{.Generated}} · window {{.Plan.Window.Since}} · {{plural .Plan.SessionsScanned "session"}} scanned · <span class="pill {{basisClass .Plan.Basis}}">{{.Plan.Basis}}</span></p>

<div class="callout">
  <div class="emoji">🗿</div>
  <div><div class="label">TLDR</div>{{tldr .Plan}}</div>
</div>

{{if or .Plan.WrapMeasured .Plan.Retro}}
<h2>Savings</h2>
<p class="note">Counted in tokens, not dollars — what a token costs depends on caching. Each number is measured on its own set of requests; none are added together.</p>
<div class="dcard">
  <div class="split">
    {{with .Plan.WrapMeasured}}
    <div class="panel">
      <div class="kicker">Saved so far</div>
      <div class="pnum" title="{{comma .TokensSaved}} tokens">{{human .TokensSaved}}<span class="punit">tokens</span></div>
      {{if .TokensSaved}}
      <div class="meter" title="cut {{comma .TokensSaved}} of {{comma .TokensBefore}} tokens"><span style="width:{{pctOf .TokensSaved .TokensBefore}}%"></span></div>
      <p class="pcap">Caveman compressed {{comma .CompressedRequests}} of the {{comma .Requests}} requests it handled, making them {{pctOf .TokensSaved .TokensBefore}}% smaller.</p>
      {{else}}
      <p class="pcap">Nothing cut yet — Caveman watched {{comma .Requests}} requests without changing them.</p>
      {{end}}
      {{if .WouldSaveTokens}}<p class="pcap dim">Plus {{human .WouldSaveTokens}} tokens spotted in watch-only mode, not cut.</p>{{end}}
    </div>
    {{end}}
    {{with .Plan.Retro}}
    <div class="panel">
      <div class="kicker">Could have saved{{if .TimeBoxed}} · partial scan{{end}}</div>
      <div class="pnum" title="{{comma .WouldCutStreamTokens}} tokens">{{human .WouldCutStreamTokens}}<span class="punit">tokens</span></div>
      {{if .WouldCutStreamTokens}}
      <div class="meter" title="{{comma .WouldCutStreamTokens}} of {{comma .TokensObserved}} tokens"><span style="width:{{pctOf .WouldCutStreamTokens .TokensObserved}}%"></span></div>
      <p class="pcap">{{pctOf .WouldCutStreamTokens .TokensObserved}}% of the {{human .TokensObserved}} tokens your last {{plural .SessionsScanned "session"}} sent, across the two fixes below.</p>
      {{else}}
      <p class="pcap">The replay found savings but too few timestamps to state an honest total.</p>
      {{end}}
    </div>
    {{end}}
  </div>
  {{with .Plan.Retro}}
  {{$segs := famSegs .Families}}
  {{if $segs}}
  <div class="kicker" style="margin:22px 0 8px">Where it comes from · each cut counted once · {{human .WouldCutTokens}} tokens</div>
  <div class="stack" style="margin-top:0">
    {{range $segs}}<span style="width:{{.Pct}}%;background:{{.Color}}" title="{{.Label}} · {{comma .Tokens}} tokens"></span>{{end}}
  </div>
  <div class="hlegend">
    {{range $segs}}<span><span class="dot" style="background:{{.Color}}"></span>{{.Label}} · {{human .Tokens}}</span>{{end}}
    <span class="hcap">a different count from the headline, which weighs every re-send</span>
  </div>
  {{end}}
  {{if .ConfigPrefixTokensPerTurn}}<p class="fine">Your always-on config adds {{comma .ConfigPrefixTokensPerTurn}} tokens every turn — a separate fix, not included above.</p>{{end}}
  {{if not .EngineUsed}}<p class="fine">The compression engine could not start, so tool-result savings are missing from these numbers.</p>{{end}}
  {{end}}
  {{with .Plan.WrapMeasured}}<p class="fine">Saved-so-far counts use Caveman's own token counter ({{.Basis}}), not provider numbers.</p>{{end}}
  {{with .Plan.Retro}}{{if .Caveats}}
  <details>
    <summary><span class="caret">▶</span>How this was measured</summary>
    <div class="body"><ul class="caveats">{{range .Caveats}}<li>{{.}}</li>{{end}}</ul></div>
  </details>
  {{end}}{{end}}
</div>
{{end}}

<h2>Cave Score</h2>
<p class="note">Setup leanness. Starts at 100, subtracts capped penalties. Inferred and local-only, never a savings or dollar figure.</p>
<div class="score-row"><span class="score-num" style="color:{{scoreColor .Plan.CaveScore.Score}}">{{.Plan.CaveScore.Score}}</span><span class="score-of">/ 100 · {{scoreBasis .Plan.CaveScore.Basis}}</span></div>
<div class="bar"><span style="width:{{.Plan.CaveScore.Score}}%;background:{{scoreColor .Plan.CaveScore.Score}}"></span></div>
<div class="props">
  {{range .Plan.CaveScore.Components}}
  <div class="prop"><span class="k">{{.Key}}</span><span>{{if .Measured}}{{.Detail}}{{else}}not measured{{end}}</span><span class="v">{{if .Measured}}−{{.Penalty}}{{else}}n/a{{end}}</span></div>
  {{end}}
</div>

<h2>Token Sinks</h2>
<p class="note">Ranked by daily-equivalent magnitude. Behavioral token totals remain historical observations, never rates. Open a row for evidence and suggested fix. Load-bearing rows are listed for honesty and never touched.</p>
{{if .Plan.Sinks}}
<details>
  <summary><span class="caret">▶</span><span class="title">Show {{plural (len (topSinks .Plan.Sinks)) "sink"}}</span>{{if gt (len .Plan.Sinks) 20}}<span class="num">top 20 of {{comma (len .Plan.Sinks)}}</span>{{end}}</summary>
  <div class="body">
{{range topSinks .Plan.Sinks}}
<details>
  <summary><span class="caret">▶</span><span class="pill {{classClass .Class}}">{{classLabel .Class}}</span><span class="title">{{.Title}}</span><span class="num">{{if .TokensObserved}}{{comma .TokensObserved}} observed · {{observedBasis .}}{{else}}{{comma .TokensPerTurn}} / turn{{end}}</span></summary>
  <div class="body">
    {{if .Suggestion}}<p>{{.Suggestion}}</p>{{end}}
    <p class="kv">{{if .TokensObserved}}{{comma .TokensObserved}} tokens observed in window · basis {{observedBasis .}}{{else}}{{comma .TokensPerDayRate}} tokens/day · basis {{.Basis}}{{end}}{{if .Evidence}} · {{evidence .Evidence}}{{end}}</p>
    <p class="mono">{{.SinkID}}</p>
  </div>
</details>
{{end}}
    {{if gt (len .Plan.Sinks) 20}}<p class="fine">{{comma (len .Plan.Sinks)}} sinks found; the {{comma (len (topSinks .Plan.Sinks))}} largest by daily equivalent are shown. caveman learn report --json carries all of them.</p>{{end}}
  </div>
</details>
{{else}}<div class="empty">No token sinks found yet. Scan more sessions, then come back.</div>{{end}}

{{with .Plan.ContextDepth}}
<h2>Session Context Depth</h2>
<p class="note">Each session's peak context as a share of its model's window, from provider-counted usage in {{plural .Sessions "session"}}. Window sizes are assumed per-provider defaults. This measures how deep sessions run, a quality and habit signal, not a dollar figure. Deep sessions re-send the whole history every turn, and quality degrades well before the window limit: compact or split before half the window, or offload recurring context to cavemem.</p>
<div class="dcard">
  <div class="kicker">Session health · not a cost figure</div>
  <div class="dstats">
    <div class="dstat"><span class="dn">{{.Over30Pct}}</span><span class="dk"><span class="dot" style="background:#e0b357"></span>past 30% of window</span></div>
    <div class="dstat"><span class="dn">{{.Over50Pct}}</span><span class="dk"><span class="dot" style="background:#e0837c"></span>past 50% of window</span></div>
    <div class="dstat"><span class="dn">{{.Sessions}}</span><span class="dk"><span class="dot" style="background:#cfcdc7"></span>sessions measured</span></div>
  </div>
  <div class="hist">
    {{range depthBars .}}
    <div class="hcol" title="peak {{.Label}}% of window · {{plural .Count "session"}}">
      {{if .Count}}<span class="hcount">{{.Count}}</span>{{end}}
      <span class="hbar" style="height:{{.Pct}}%;background:{{.Color}}"></span>
    </div>
    {{end}}
  </div>
  <div class="hlabels">
    {{range depthBars .}}<span>{{.Label}}</span>{{end}}
  </div>
  <div class="hlegend">
    <span><span class="dot" style="background:#cfcdc7"></span>under 30%</span>
    <span><span class="dot" style="background:#e0b357"></span>30 to 50%</span>
    <span><span class="dot" style="background:#e0837c"></span>past 50%</span>
    <span class="hcap">peak context, share of window</span>
  </div>
</div>
{{end}}

{{$fams := costFamilies .Plan.Sinks}}
{{if $fams}}
<h2>Cost per 30 Days</h2>
<p class="note">Total sink flow of {{comma (sumPerDay .Plan.Sinks)}} tokens/day, priced at each model's input list price assuming 90% of it is a cache read (0.19x list). Bars share one scale across tabs. Illustration, not a bill; your real cache mix will differ.</p>
<div class="dcard chart">
  <input type="radio" name="costfam" id="fam-claude" checked>
  <input type="radio" name="costfam" id="fam-gpt56">
  <div class="kicker">Cost illustration · 90% cached list price</div>
  <div class="tabs">
    {{range $fams}}<label for="fam-{{.ID}}">{{.Name}}</label>{{end}}
  </div>
  {{range $fams}}
  <div class="cgroup" id="g-{{.ID}}">
    {{range .Rows}}
    <div class="crow" title="{{.Label}} · {{.Rate}} · {{.USD}} per 30 days">
      <span class="clabel">{{.Label}}<span class="crate">{{.Rate}}</span></span>
      <span class="ctrack"><span class="cbar" style="width:{{.Pct}}%"></span></span>
      <span class="cval">{{.USD}}</span>
    </div>
    {{end}}
  </div>
  {{end}}
</div>
{{end}}

<h2>Caveats</h2>
<ul class="caveats">
  {{range .Plan.Caveats}}<li>{{.}}</li>{{end}}
</ul>
</main>
</body>
</html>`))

var trialTemplate = template.Must(template.New("trial").Funcs(template.FuncMap{
	"usd": func(v float64) string {
		return fmt.Sprintf("$%.4f", v)
	},
	"pct": func(v float64) string {
		return fmt.Sprintf("%.0f%%", v)
	},
	"bar": func(v, max float64) template.HTML {
		width := 0.0
		if max > 0 {
			width = v / max * 100
		}
		if width < 0 {
			width = 0
		}
		if width > 100 {
			width = 100
		}
		return template.HTML(fmt.Sprintf(`<span class="bar"><span style="width:%.2f%%"></span></span>`, width))
	},
	"maxCost": func(origins []UsageOrigin) float64 {
		max := 0.0
		for _, origin := range origins {
			if origin.TotalCostUSD > max {
				max = origin.TotalCostUSD
			}
		}
		if max == 0 {
			return 1
		}
		return max
	},
	"maxQuota": func() float64 { return 100 },
	"basisClass": func(basis string) string {
		if strings.EqualFold(basis, "verified") {
			return "bad"
		}
		return "basis"
	},
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Caveman Trial Report</title>
<style>
:root{color-scheme:light;--ink:#111;--muted:#666;--line:#ddd;--bg:#fafafa;--accent:#0f766e;--warn:#92400e}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.45 ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
main{max-width:1040px;margin:0 auto;padding:32px 20px 56px}header{border-bottom:2px solid var(--ink);padding-bottom:18px;margin-bottom:24px}
h1{font-size:32px;line-height:1.1;margin:0 0 8px}h2{font-size:20px;margin:28px 0 10px}h3{font-size:15px;margin:18px 0 8px}
p{margin:0 0 10px}.muted{color:var(--muted)}.basis,.bad{display:inline-block;border:1px solid var(--line);border-radius:999px;padding:2px 8px;font-size:12px;background:white}.bad{border-color:#f59e0b;color:var(--warn)}
.cards{display:grid;grid-template-columns:repeat(5,minmax(0,1fr));gap:10px;margin:14px 0 4px}.card{background:white;border:1px solid var(--line);border-radius:8px;padding:12px;min-width:0}.k{font-size:12px;color:var(--muted)}.v{font-size:22px;font-weight:700;margin-top:3px}
table{border-collapse:collapse;width:100%;background:white;border:1px solid var(--line);border-radius:8px;overflow:hidden}th,td{text-align:left;padding:9px 10px;border-bottom:1px solid var(--line);vertical-align:top}th{font-size:12px;color:var(--muted);font-weight:600;background:#f3f4f6}tr:last-child td{border-bottom:0}
.bar{display:inline-block;width:120px;height:8px;background:#e5e7eb;border-radius:999px;overflow:hidden;vertical-align:middle}.bar span{display:block;height:100%;background:var(--accent)}
.section-note{color:var(--muted);margin-bottom:10px}.moves td:first-child,.learn td:first-child{font-weight:600}.caveats li{margin:6px 0}.empty{padding:12px;background:white;border:1px solid var(--line);border-radius:8px;color:var(--muted)}
@media(max-width:780px){.cards{grid-template-columns:repeat(2,minmax(0,1fr))}table{font-size:12px}.bar{width:72px}main{padding:22px 12px}}
</style>
</head>
<body>
<main>
<header>
  <h1>Caveman Trial Report</h1>
  <p class="muted">Trial {{.Plan.TrialID}} · generated {{.Generated}} · <span class="{{basisClass .Plan.Basis}}">{{.Plan.Basis}}</span></p>
</header>

<section>
  <h2>Executive Summary</h2>
  <p>Caveman observed local usage across proxied requests and imported agent history. The report shows origin mix, quota windows, report-only replay candidates, and learnings stored into cavemem. No local number is promoted to verified.</p>
  <div class="cards">
    <div class="card"><div class="k">Requests</div><div class="v">{{.Plan.Headline.Requests}}</div></div>
    <div class="card"><div class="k">Input tokens</div><div class="v">{{.Plan.Headline.InputTokens}}</div></div>
    <div class="card"><div class="k">Output tokens</div><div class="v">{{.Plan.Headline.OutputTokens}}</div></div>
    <div class="card"><div class="k">Total cost</div><div class="v">{{usd .Plan.Headline.TotalCostUSD}}</div></div>
    <div class="card"><div class="k">Modeled dollar delta</div><div class="v">{{usd .Plan.Headline.EstimatedSavingsUSD}}</div></div>
  </div>
</section>

<section>
  <h2>Usage Origins</h2>
  <p class="section-note">Origin rows are normalized; raw prompts, transcript contents, cookies, and auth headers are excluded.</p>
  {{if .Plan.Origins}}
  {{$max := maxCost .Plan.Origins}}
  <table>
    <thead><tr><th>Source</th><th>Agent</th><th>Provider</th><th>Model</th><th>Requests</th><th>Input tokens</th><th>Cost</th><th>Basis</th></tr></thead>
    <tbody>
      {{range .Plan.Origins}}
      <tr><td>{{.SourceKind}}</td><td>{{.AgentSlug}}</td><td>{{.Provider}}</td><td>{{.Model}}</td><td>{{.Requests}}</td><td>{{.InputTokens}}</td><td>{{usd .TotalCostUSD}} {{bar .TotalCostUSD $max}}</td><td><span class="{{basisClass .Basis}}">{{.Basis}}</span></td></tr>
      {{end}}
    </tbody>
  </table>
  {{else}}<div class="empty">No usage origins imported or recorded yet.</div>{{end}}
</section>

<section>
  <h2>Plan/Quota Windows</h2>
  <p class="section-note">Quota snapshots come from Codex local rate-limit state or linked usage refreshes. They are usage-window signals, not billing receipts.</p>
  {{if .Plan.QuotaSnapshots}}
  <table>
    <thead><tr><th>Provider</th><th>Plan</th><th>Window</th><th>Used</th><th>Resets</th><th>Basis</th></tr></thead>
    <tbody>
      {{range .Plan.QuotaSnapshots}}
      <tr><td>{{.Provider}}</td><td>{{.PlanType}}</td><td>{{.Window}}</td><td>{{pct .UsedPct}} {{bar .UsedPct (maxQuota)}}</td><td>{{.ResetsAt}}</td><td><span class="{{basisClass .Basis}}">{{.Basis}}</span></td></tr>
      {{end}}
    </tbody>
  </table>
  {{else}}<div class="empty">No quota snapshots linked or imported yet.</div>{{end}}
</section>

<section>
  <h2>Optimizer Trial Results</h2>
  <p class="section-note">Current local heuristics are report-only or insufficient-data candidates. Provider/model names and positive cost do not prove a stable cache prefix, safe route, or promotable transform. Compression replay reports one-run local estimated-token shape only and needs task-outcome evaluation.</p>
  {{if .Plan.Moves}}
  <table class="moves">
    <thead><tr><th>Move</th><th>Safety</th><th>Status</th><th>Modeled dollar delta</th><th>Confidence</th></tr></thead>
    <tbody>
      {{range .Plan.Moves}}
      <tr><td>{{.Title}}<br><span class="muted">{{.OptimizerID}}</span></td><td>{{.SafetyClass}}</td><td>{{.Status}}</td><td>{{usd .SavingsUSDBase}}</td><td>{{.Confidence}}</td></tr>
      {{end}}
    </tbody>
  </table>
  {{else}}<div class="empty">No optimizer moves available yet.</div>{{end}}
</section>

<section>
  <h2>Learned Past Patterns</h2>
  <p class="section-note">Learnings are concise cavemem entries derived from observed history and quota pressure.</p>
  {{if .Plan.Learnings}}
  <table class="learn">
    <thead><tr><th>Learning</th><th>Source</th><th>Confidence</th><th>Stored</th></tr></thead>
    <tbody>
      {{range .Plan.Learnings}}
      <tr><td>{{.Text}}</td><td>{{.SourceKind}}</td><td>{{.Confidence}}</td><td>{{.StoredInCavemem}}</td></tr>
      {{end}}
    </tbody>
  </table>
  {{else}}<div class="empty">No cavemem learnings generated yet. Run caveman learn scan after importing usage.</div>{{end}}
</section>

<section>
  <h2>Caveats</h2>
  <ul class="caveats">
    {{range .Plan.Caveats}}<li>{{.}}</li>{{end}}
  </ul>
</section>
</main>
</body>
</html>`))
