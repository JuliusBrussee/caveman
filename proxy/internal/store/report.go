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
	}{Plan: plan, Generated: time.Now().UTC().Format(time.RFC3339)})
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

var learnTemplate = template.Must(template.New("learn").Funcs(template.FuncMap{
	"reducible": func(sinks []Sink) []Sink {
		out := []Sink{}
		for _, s := range sinks {
			if s.Class == classReducible {
				out = append(out, s)
			}
		}
		return out
	},
	"behavioral": func(sinks []Sink) []Sink {
		out := []Sink{}
		for _, s := range sinks {
			if s.Class == classBehavioral {
				out = append(out, s)
			}
		}
		return out
	},
	"recurring": func(sinks []Sink) []Sink {
		out := []Sink{}
		for _, s := range sinks {
			if s.Class == classRecurringContext {
				out = append(out, s)
			}
		}
		return out
	},
	"classClass": func(class string) string {
		switch class {
		case classReducible:
			return "tag reducible"
		case classBehavioral:
			return "tag behavioral"
		case classRecurringContext:
			return "tag recurring"
		default:
			return "tag loadbearing"
		}
	},
	"basisClass": func(basis string) string {
		if strings.EqualFold(basis, "verified") {
			return "bad"
		}
		return "basis"
	},
	"scoreBasis": func(basis string) string {
		if strings.EqualFold(basis, "verified") {
			return "inferred"
		}
		return basis
	},
	"comma": func(v int64) string {
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
<title>Caveman Learn Report</title>
<style>
:root{color-scheme:light;--ink:#111;--muted:#666;--line:#ddd;--bg:#fafafa;--accent:#0f766e;--warn:#92400e}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.45 ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
main{max-width:1040px;margin:0 auto;padding:32px 20px 56px}header{border-bottom:2px solid var(--ink);padding-bottom:18px;margin-bottom:24px}
h1{font-size:32px;line-height:1.1;margin:0 0 8px}h2{font-size:20px;margin:28px 0 10px}
p{margin:0 0 10px}.muted{color:var(--muted)}.basis,.bad,.tag{display:inline-block;border:1px solid var(--line);border-radius:999px;padding:2px 8px;font-size:12px;background:white}.bad{border-color:#f59e0b;color:var(--warn)}
.tag.reducible{border-color:#0f766e;color:#0f766e}.tag.behavioral{border-color:#6366f1;color:#4338ca}.tag.recurring{border-color:#b45309;color:#b45309}.tag.loadbearing{color:var(--muted)}
.score{display:flex;align-items:center;gap:18px;background:white;border:1px solid var(--line);border-radius:10px;padding:18px}
.score .num{font-size:46px;font-weight:800;line-height:1;color:var(--accent)}.score .of{font-size:16px;color:var(--muted)}
.components{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px;margin:12px 0}.comp{background:white;border:1px solid var(--line);border-radius:8px;padding:10px}.comp .k{font-size:12px;color:var(--muted)}.comp .p{font-size:18px;font-weight:700;margin-top:3px}.comp .d{font-size:11px;color:var(--muted);margin-top:4px}
table{border-collapse:collapse;width:100%;background:white;border:1px solid var(--line);border-radius:8px;overflow:hidden}th,td{text-align:left;padding:9px 10px;border-bottom:1px solid var(--line);vertical-align:top}th{font-size:12px;color:var(--muted);font-weight:600;background:#f3f4f6}tr:last-child td{border-bottom:0}
.section-note{color:var(--muted);margin-bottom:10px}.ev{font-size:12px;color:var(--muted);margin-top:4px}.caveats li{margin:6px 0}.empty{padding:12px;background:white;border:1px solid var(--line);border-radius:8px;color:var(--muted)}
@media(max-width:780px){.components{grid-template-columns:repeat(2,minmax(0,1fr))}table{font-size:12px}main{padding:22px 12px}}
</style>
</head>
<body>
<main>
<header>
  <h1>Caveman Learn Report</h1>
  <p class="muted">generated {{.Generated}} · <span class="{{basisClass .Plan.Basis}}">{{.Plan.Basis}}</span> · window {{.Plan.Window.Since}}</p>
</header>

<section>
  <h2>Cave Score</h2>
  <p class="section-note">Setup leanness, starting at 100 and subtracting capped penalties. Inferred, local-only, and distinct from the Cloud Cave Score. Not a savings or dollar figure.</p>
  <div class="score"><div><span class="num">{{.Plan.CaveScore.Score}}</span><span class="of"> / 100</span></div><div class="muted">basis: {{scoreBasis .Plan.CaveScore.Basis}}</div></div>
  <div class="components">
    {{range .Plan.CaveScore.Components}}
    <div class="comp"><div class="k">{{.Key}}</div><div class="p">{{if .Measured}}-{{.Penalty}}{{else}}n/a{{end}}</div><div class="d">{{if .Measured}}{{.Detail}}{{else}}not measured{{end}}</div></div>
    {{end}}
  </div>
</section>

<section>
  <h2>Biggest Token Sinks</h2>
  <p class="section-note">Ranked by forward tokens/day rate. Class A facts are asserted; Class B observations carry softened suggestions with their evidence.</p>
  {{if .Plan.Sinks}}
  <table>
    <thead><tr><th>Sink</th><th>Class</th><th>Tokens/turn</th><th>Tokens/day</th><th>Basis</th></tr></thead>
    <tbody>
      {{range .Plan.Sinks}}
      <tr><td>{{.Title}}<div class="muted">{{.SinkID}}</div>{{if .Suggestion}}<div class="ev">{{.Suggestion}}</div>{{end}}{{if .Evidence}}<div class="ev">{{evidence .Evidence}}</div>{{end}}</td><td><span class="{{classClass .Class}}">{{.Class}}</span></td><td>{{comma .TokensPerTurn}}</td><td>{{comma .TokensPerDayRate}}</td><td><span class="{{basisClass .Basis}}">{{.Basis}}</span></td></tr>
      {{end}}
    </tbody>
  </table>
  {{else}}<div class="empty">No token sinks found yet.</div>{{end}}
</section>

<section>
  <h2>What's Safely Reducible</h2>
  <p class="section-note">Config-bloat sinks with a mechanical, net-token-negative fix. The editing skill proposes a diff per item; you approve each one.</p>
  {{$red := reducible .Plan.Sinks}}
  {{if $red}}
  <table>
    <thead><tr><th>Move</th><th>Tokens/turn</th><th>Suggested fix</th></tr></thead>
    <tbody>
      {{range $red}}
      <tr><td>{{.Title}}</td><td>{{comma .TokensPerTurn}}</td><td>{{.Suggestion}}</td></tr>
      {{end}}
    </tbody>
  </table>
  {{else}}<div class="empty">No safely-reducible config sinks detected.</div>{{end}}
</section>

<section>
  <h2>Recurring Context (offload to cavemem)</h2>
  <p class="section-note">Heavy context re-established across sessions. The editing skill can offload each block to cavemem — recalled on demand instead of re-pasted every turn — only when that measurably reduces tokens/turn. Counts and locators only; no block content is stored.</p>
  {{$rec := recurring .Plan.Sinks}}
  {{if $rec}}
  <table>
    <thead><tr><th>Block</th><th>Tokens/turn</th><th>Suggested fix</th></tr></thead>
    <tbody>
      {{range $rec}}
      <tr><td>{{.Title}}<div class="ev">{{evidence .Evidence}}</div></td><td>{{comma .TokensPerTurn}}</td><td>{{.Suggestion}}</td></tr>
      {{end}}
    </tbody>
  </table>
  {{else}}<div class="empty">No recurring context blocks detected across the scanned sessions.</div>{{end}}
</section>

<section>
  <h2>Behavioral Observations</h2>
  <p class="section-note">Measured habits stated as history. Caveman never asserts a subagent or skill was unnecessary; the numbers are facts, the suggestions are soft.</p>
  {{$beh := behavioral .Plan.Sinks}}
  {{if $beh}}
  <table>
    <thead><tr><th>Observation</th><th>Suggested practice</th></tr></thead>
    <tbody>
      {{range $beh}}
      <tr><td>{{.Title}}<div class="ev">{{evidence .Evidence}}</div></td><td>{{.Suggestion}}</td></tr>
      {{end}}
    </tbody>
  </table>
  {{else}}<div class="empty">No behavioral observations from scanned sessions.</div>{{end}}
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
