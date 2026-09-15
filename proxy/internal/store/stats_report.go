package store

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
)

func DefaultStatsReportPath(home string) string {
	return filepath.Join(home, "reports", "caveman-stats.html")
}

// WriteStatsHTML writes a private, self-contained snapshot. The page only rolls
// up recorded metric groups; it never derives a price or upgrades evidence.
func (s *Store) WriteStatsHTML(report StatsReport, outPath string) error {
	if outPath == "" {
		return fmt.Errorf("report path is required")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode stats report: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(outPath), ".caveman-stats-*.html")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	// encoding/json escapes <, >, &, and JavaScript line separators. Mark only
	// its output as JavaScript: an untrusted model name cannot close the script.
	err = statsReportTemplate.Execute(f, struct{ Data template.JS }{Data: template.JS(raw)})
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), outPath)
}

var statsReportTemplate = template.Must(template.New("stats").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light">
<link rel="icon" href="data:,">
<title>Caveman Stats</title>
<style>
:root{color-scheme:light;--ink:#37352f;--muted:#787774;--faint:#9b9a97;--line:#ededec;--paper:#f7f7f5;--green:#448361;--green-soft:#dbeddb;--purple:#9065b0;--purple-soft:#e8deee;--red:#d44c47;--orange:#cb912f}
[hidden]{display:none!important}*{box-sizing:border-box}body{margin:0;background:#fff;color:var(--ink);font:15px/1.6 ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;-webkit-font-smoothing:antialiased}
main{max-width:1120px;margin:0 auto;padding:58px 32px 96px}a{color:inherit;text-underline-offset:3px}button,select{font:inherit;color:inherit}button,select,summary{cursor:pointer}button:focus-visible,select:focus-visible,summary:focus-visible,a:focus-visible{outline:2px solid #2383e2;outline-offset:3px}button{border:0}button:disabled{cursor:default;opacity:.45}h1,h2,h3,p{margin:0}h1{font-size:42px;font-weight:700;letter-spacing:-.035em;line-height:1.2}h2{font-size:22px;font-weight:600;letter-spacing:-.02em}h3{font-size:15px;font-weight:600}.breadcrumb{display:flex;align-items:center;gap:8px;font-size:13px;color:var(--muted);margin-bottom:34px}.breadcrumb .slash{color:#ccc}.breadcrumb a{text-decoration:none}.breadcrumb a:hover{text-decoration:underline}.local{margin-left:auto;display:flex;align-items:center;gap:6px;font-size:12px}.dot{height:6px;width:6px;background:var(--green);border-radius:50%;display:inline-block}.heading{display:flex;gap:18px;align-items:center}.icon{font-size:50px;line-height:1}.subtitle{color:var(--muted);font-size:14px;margin:9px 0 0}.head-actions{margin-left:auto;align-self:center}.button{padding:6px 12px;border:1px solid #e3e3e1;border-radius:5px;background:#fff;font-size:13px;white-space:nowrap}.button:hover{background:var(--paper)}.pill{display:inline-block;border-radius:4px;padding:1px 7px;font-size:11px;font-weight:500;white-space:nowrap;background:#f1f1ef;color:#57564f}.pill.green{background:var(--green-soft);color:#1c3829}.pill.purple{background:var(--purple-soft);color:#412454}.pill.orange{background:#fadec9;color:#49290e}.pill.red{background:#ffe2dd;color:#5d1715}.filters{display:flex;gap:8px;align-items:end;flex-wrap:wrap;padding:25px 0 22px;margin-top:14px;border-bottom:1px solid var(--line)}.filter{display:flex;flex-direction:column;gap:4px}.filter label{font-size:11px;color:var(--muted)}select{background:#fff;border:1px solid #e3e3e1;border-radius:5px;padding:5px 25px 5px 9px;max-width:210px;font-size:12px;min-height:31px}.filter:first-child{margin-right:6px}.filter-reset{font-size:12px;color:var(--muted);background:none;padding:6px 8px}.scope{font-size:12px;color:var(--muted);margin:14px 0 0;min-height:20px}.hero{display:grid;grid-template-columns:1.15fr 1fr;gap:50px;padding:24px 0 28px;border-bottom:1px solid var(--line)}.eyebrow{font-size:11px;letter-spacing:.07em;text-transform:uppercase;color:var(--muted);font-weight:600}.hero-num{font-size:64px;line-height:1.1;font-weight:650;letter-spacing:-.06em;margin-top:9px;font-variant-numeric:tabular-nums}.hero-unit{font-size:15px;color:var(--muted);letter-spacing:0;font-weight:400;margin-left:10px}.hero-note{font-size:13px;color:var(--muted);margin-top:10px;max-width:450px}.positive{color:var(--green)}.negative{color:var(--red)}.equivalent{color:var(--purple)}.flow{padding-top:8px;display:flex;flex-direction:column;justify-content:center;gap:12px}.flow-row{display:grid;grid-template-columns:135px 1fr 85px;align-items:center;gap:12px;font-size:12px}.flow-label{color:var(--muted)}.track{height:9px;background:var(--paper);border-radius:2px;overflow:hidden}.track span{display:block;height:100%;background:#c9c7c2;border-radius:2px}.track .delivered{background:var(--green)}.track .recovery{background:#d6bc9e}.flow-val{text-align:right;font-variant-numeric:tabular-nums}.flow-foot{font-size:11px;color:var(--faint);border-top:1px solid var(--line);padding-top:8px;margin-top:1px}.metrics{display:grid;grid-template-columns:repeat(3,1fr);gap:26px;margin:26px 0 30px}.metric{padding-right:24px;border-right:1px solid var(--line)}.metric:last-child{border:0;padding-right:0}.metric-label{font-size:12px;color:var(--muted);display:flex;gap:7px;align-items:center;flex-wrap:wrap}.metric-num{font-size:31px;line-height:1.3;font-weight:600;letter-spacing:-.035em;font-variant-numeric:tabular-nums;margin:8px 0 5px}.metric-note{font-size:12px;line-height:1.5;color:var(--muted);max-width:285px}.callout{display:flex;gap:10px;background:var(--paper);border-radius:5px;padding:12px 15px;font-size:12px;color:var(--muted)}.callout strong{color:var(--ink);font-weight:500}.callout-mark{font-size:16px;color:var(--faint);flex:none}.section{margin-top:36px}.section-head{display:flex;justify-content:space-between;align-items:center;gap:12px;margin-bottom:4px}.note{font-size:13px;color:var(--muted);margin:4px 0 16px}.tabs{display:inline-flex;gap:2px;background:#f1f1ef;border-radius:5px;padding:3px}.tab{padding:3px 11px;font-size:12px;background:none;border-radius:3px;color:var(--muted)}.tab[aria-pressed=true]{color:var(--ink);background:#fff;box-shadow:0 1px 2px rgba(0,0,0,.07)}.chart-card{border:1px solid var(--line);border-radius:8px;padding:20px 23px 14px}.chart-heading{display:flex;align-items:baseline;gap:9px}.chart-value{font-size:27px;font-weight:600;letter-spacing:-.03em;font-variant-numeric:tabular-nums}.chart-caption{font-size:12px;color:var(--muted)}.chart{margin-top:10px;position:relative;min-height:232px}.chart svg{display:block;width:100%;height:auto;overflow:visible}.chart text{font-family:inherit;font-size:11px;fill:var(--faint)}.chart .bar:hover{opacity:.76}.chart-empty{min-height:216px;display:flex;justify-content:center;align-items:center;color:var(--muted);font-size:13px}.chart-legend{display:flex;align-items:center;gap:15px;color:var(--muted);font-size:11px;margin-top:5px}.legend-dot{height:7px;width:7px;display:inline-block;border-radius:2px;margin-right:4px}.legend-caption{margin-left:auto}.table-wrap{overflow-x:auto}table{width:100%;border-collapse:collapse;font-size:12px;white-space:nowrap}th{text-align:left;font-weight:400;color:var(--muted);padding:9px 10px;border-bottom:1px solid var(--line)}td{padding:11px 10px;border-bottom:1px solid var(--line);font-variant-numeric:tabular-nums}th:first-child,td:first-child{padding-left:0}th:last-child,td:last-child{padding-right:0}.num{text-align:right}.table-name{font-weight:500;color:var(--ink);max-width:285px;overflow:hidden;text-overflow:ellipsis}.row-sub{display:block;font-size:10px;color:var(--faint);font-weight:400}.empty-row{white-space:normal;text-align:center;color:var(--muted);padding:24px}.meter-cell{display:flex;align-items:center;gap:9px}.mini-track{width:50px;height:4px;background:var(--paper);border-radius:2px;flex:none}.mini-track span{display:block;height:100%;background:var(--green);border-radius:2px}.detail-grid{display:grid;grid-template-columns:1fr 1fr;gap:30px}.usage{display:grid;grid-template-columns:1fr auto;gap:0;font-size:13px}.usage dt,.usage dd{border-bottom:1px solid var(--line);margin:0;padding:9px 0}.usage dt{color:var(--muted)}.usage dd{text-align:right;font-variant-numeric:tabular-nums}.receipts{border-top:1px solid var(--line)}details{border-bottom:1px solid var(--line)}summary{display:flex;align-items:center;gap:9px;list-style:none;padding:11px 4px;border-radius:4px;font-size:13px}summary::-webkit-details-marker{display:none}summary:hover{background:rgba(55,53,47,.04)}.caret{font-size:10px;color:var(--faint);transition:transform .15s ease}details[open] .caret{transform:rotate(90deg)}.receipt-title{min-width:0;flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.receipt-time{font-size:11px;color:var(--faint);white-space:nowrap}.receipt-total{font-variant-numeric:tabular-nums;font-size:12px;white-space:nowrap}.receipt-body{padding:5px 4px 18px 25px}.receipt-meta{font-size:11px;color:var(--muted);overflow-wrap:anywhere}.formula{display:flex;flex-wrap:wrap;align-items:center;gap:13px;padding:16px 0}.formula-part strong{font-size:19px;font-weight:500;font-variant-numeric:tabular-nums;display:block;line-height:1.3}.formula-part span{font-size:10px;color:var(--muted)}.operator{font-size:17px;color:var(--faint)}.receipt-money{font-size:12px;color:var(--muted);margin:0 0 8px}.method{font-size:12px;color:var(--muted)}.method li{margin:7px 0}.method ol{padding-left:18px}.method .mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:11px}.method-details{margin-top:36px;background:var(--paper);border:0;border-radius:5px;padding:2px 10px}.method-details summary{font-size:12px}.method-body{padding:4px 13px 14px 23px}.footer{display:flex;gap:15px;justify-content:space-between;border-top:1px solid var(--line);padding-top:18px;margin-top:32px;color:var(--faint);font-size:11px}.noscript{padding:20px;background:#fadec9}.count-badge{color:var(--faint);font-size:12px;font-weight:400;margin-left:6px}.subhead{display:flex;align-items:center;justify-content:space-between;margin-bottom:10px}
@media(max-width:800px){main{padding:32px 22px 70px}.hero{gap:25px}.flow-row{grid-template-columns:110px 1fr 66px;gap:8px}.hero-num{font-size:53px}.metrics{gap:18px}.metric{padding-right:17px}.metric-num{font-size:27px}.section-head{align-items:start;flex-wrap:wrap}.detail-grid{gap:24px}.legend-caption{display:none}}
@media(max-width:560px){main{padding:22px 16px 55px}.breadcrumb{margin-bottom:26px}.local{font-size:10px}.heading{gap:11px;flex-wrap:wrap}.icon{font-size:40px}h1{font-size:33px}.head-actions{margin-left:0;width:100%;margin-top:8px}.subtitle{font-size:12px}.filters{gap:9px;padding:18px 0}.filter{flex:1;min-width:130px}.filter:first-child{margin:0}select{max-width:none;width:100%}.hero{grid-template-columns:1fr;gap:20px;padding-top:20px}.hero-num{font-size:58px}.flow-row{grid-template-columns:125px 1fr 85px}.metrics{grid-template-columns:1fr;gap:18px;margin:22px 0}.metric{padding:0 0 18px;border-right:0;border-bottom:1px solid var(--line)}.metric:last-child{padding-bottom:0}.metric-num{font-size:30px}.metric-note{max-width:none}.detail-grid{grid-template-columns:1fr}.chart-card{padding:16px 12px 12px}.chart{min-height:180px}.chart-legend{flex-wrap:wrap;gap:6px 12px}.tabs{flex-wrap:wrap}.tab{padding:3px 8px}.receipt-time{display:none}.receipt-body{padding-left:15px}.footer{flex-direction:column;gap:5px}}
@media(prefers-reduced-motion:reduce){*{transition:none!important}}
@media print{main{max-width:none;padding:20px}.filters,.head-actions,.tabs,.filter-reset{display:none}.hero,.metrics,.chart-card{break-inside:avoid}.receipt-body{display:block}.method-details{break-inside:avoid}}
</style>
</head>
<body>
<main>
<nav class="breadcrumb" aria-label="Breadcrumb"><span>🪨</span><span>Caveman</span><span class="slash">/</span><span>Stats</span><span class="local"><span class="dot"></span>Local snapshot · private</span></nav>
<header class="heading"><span class="icon" aria-hidden="true">🗿</span><div><h1>Caveman Stats</h1><p class="subtitle">Every token has a story. Here is what changed.</p></div><div class="head-actions"><button type="button" class="button" id="download">↓ Export snapshot</button></div></header>
<div class="filters" aria-label="Filter statistics">
<div class="filter"><label for="period">Period</label><select id="period"><option value="all">Whole report</option><option value="30">Last 30 days</option><option value="7">Last 7 days</option><option value="1">Today (UTC)</option></select></div>
<div class="filter"><label for="provider">Provider</label><select id="provider"><option value="">All providers</option></select></div>
<div class="filter"><label for="model">Model</label><select id="model"><option value="">All models</option></select></div>
<div class="filter"><label for="agent">Agent</label><select id="agent"><option value="">All agents</option></select></div>
<div class="filter"><label for="auth">Billing</label><select id="auth"><option value="">All billing types</option></select></div>
<button type="button" class="filter-reset" id="reset">Reset</button>
</div>
<p id="scope" class="scope" aria-live="polite"></p>
<section class="hero" aria-label="Token reduction">
<div><p class="eyebrow">Request token reduction <span class="pill green">Locally estimated</span></p><p class="hero-num" id="net">—</p><p class="hero-note" id="net-note"></p></div>
<div class="flow"><div class="flow-row"><span class="flow-label">Original request</span><span class="track"><span id="before-bar"></span></span><span class="flow-val" id="before"></span></div><div class="flow-row"><span class="flow-label">Delivered</span><span class="track"><span class="delivered" id="after-bar"></span></span><span class="flow-val" id="after"></span></div><div class="flow-row"><span class="flow-label">Request reduction</span><span class="track"><span class="recovery" id="recovery-bar"></span></span><span class="flow-val" id="recovery"></span></div><p class="flow-foot">Original − delivered = recorded request delta. Full-session recovery and earlier native/MCP transforms are outside this counterfactual.</p></div>
</section>
<aside class="callout" id="legacy-summary" hidden><span class="callout-mark" aria-hidden="true">↳</span><p><strong id="legacy-heading"></strong> <span id="legacy-note"></span></p></aside>
<section class="metrics" aria-label="Separate money estimates">
<div class="metric"><p class="metric-label">API cost reduction <span class="pill green">Estimate</span></p><p class="metric-num positive" id="api-value">—</p><p class="metric-note" id="api-note"></p></div>
<div class="metric"><p class="metric-label">Subscription / OAuth <span class="pill purple">API equivalent</span></p><p class="metric-num equivalent" id="equivalent-value">—</p><p class="metric-note" id="equivalent-note"></p></div>
<div class="metric"><p class="metric-label">API spend estimate <span class="pill">List price</span></p><p class="metric-num" id="spend-value">—</p><p class="metric-note" id="spend-note"></p></div>
</section>
<aside class="callout"><span class="callout-mark" aria-hidden="true">ⓘ</span><p><strong>Different numbers, different evidence.</strong> Token deltas compare locally counted request content. API estimates depend on recorded model and cache pricing. Subscription equivalents show hypothetical API value, never money saved on a seat. No invoice savings are verified here.</p></aside>
<section class="section" aria-labelledby="timeline-heading"><div class="section-head"><h2 id="timeline-heading">Over time</h2><div class="tabs" aria-label="Chart metric"><button type="button" class="tab" data-metric="tokens" aria-pressed="true">Tokens</button><button type="button" class="tab" data-metric="api" aria-pressed="false">API estimate</button><button type="button" class="tab" data-metric="equivalent" aria-pressed="false">API equivalent</button><button type="button" class="tab" data-metric="legacy" id="legacy-tab" aria-pressed="false" hidden>Earlier estimates</button></div></div><p class="note" id="timeline-note">Daily deltas in UTC. Red bars keep regressions visible.</p><div class="chart-card"><div class="chart-heading"><span class="chart-value" id="chart-value"></span><span class="chart-caption" id="chart-caption"></span></div><div class="chart" id="chart"></div><div class="chart-legend"><span><i class="legend-dot" id="chart-color" style="background:#448361"></i><span id="chart-legend-label">Token reduction</span></span><span><i class="legend-dot" style="background:#d44c47"></i>Regression</span><span class="legend-caption">Hover a bar for exact values</span></div><details><summary><span class="caret">▶</span>Daily values</summary><div class="table-wrap"><table><thead><tr><th>Date (UTC)</th><th class="num">Requests</th><th class="num">Request delta</th><th class="num">Earlier estimates</th><th class="num">API estimate</th><th class="num">API equivalent</th></tr></thead><tbody id="daily-body"></tbody></table></div></details></div></section>
<section class="section" aria-labelledby="breakdown-heading"><div class="section-head"><h2 id="breakdown-heading">Where it adds up</h2><div class="tabs" aria-label="Breakdown grouping"><button type="button" class="tab" data-group="provider" aria-pressed="true">Providers</button><button type="button" class="tab" data-group="model" aria-pressed="false">Models</button><button type="button" class="tab" data-group="agent" aria-pressed="false">Agents</button></div></div><p class="note">Same selected requests. API estimates and subscription equivalents stay separate.</p><div class="table-wrap"><table><thead><tr><th id="breakdown-label">Provider</th><th class="num">Requests</th><th class="num">Original</th><th class="num">Net reduction</th><th class="num">API estimate</th><th class="num">API equivalent</th></tr></thead><tbody id="breakdown-body"></tbody></table></div></section>
<section class="section detail-grid"><div><h2>Usage &amp; coverage</h2><p class="note">Provider usage and local compression are separate counts.</p><dl class="usage" id="usage"></dl></div><div><h2>Accounting health</h2><p class="note">Missing evidence stays visible.</p><dl class="usage" id="health"></dl></div></section>
<section class="section" aria-labelledby="receipts-heading"><div class="section-head"><h2 id="receipts-heading">Calculation receipts <span class="count-badge" id="receipt-count"></span></h2></div><p class="note" id="receipt-note">Open a request to inspect its original and delivered counts, exact model, and pricing evidence.</p><div class="receipts" id="receipts"></div></section>
<details class="method-details"><summary><span class="caret">▶</span>How these numbers are calculated</summary><div class="method-body method"><ol><li>Compare the original request content with the content Caveman delivered using the same local token counter. Repeated transmissions count again only when recorded on another request.</li><li>Keep negative deltas when a transform adds tokens. The comparison covers the proxy request boundary; it does not prove a whole-session saving or subtract extra recovery calls.</li><li>Use the request's recorded provider, model, billing mode, cache usage, and dated catalog evidence for eligible price estimates. Unknown pricing stays unknown.</li><li>Keep API estimates separate from subscription / OAuth API equivalents. No subscription bill reduction is inferred.</li></ol><ul id="caveats"></ul><p class="mono" id="method-meta"></p></div></details>
<footer class="footer"><span>Caveman · keep your agent, see the difference.</span><span id="generated"></span></footer>
<noscript><p class="noscript">Enable JavaScript to explore this local report. Machine-readable data remains embedded below; use caveman stats --json for terminal output.</p></noscript>
</main>
<script id="stats-data" type="application/json">{{.Data}}</script>
<script>
'use strict';
// Stats presentation code is intentionally dependency-free and makes no network requests.
const report = JSON.parse(document.getElementById('stats-data').textContent);
const groups = Array.isArray(report.groups) ? report.groups : [];
const receipts = Array.isArray(report.receipts) ? report.receipts : [];
const evidence = report.evidence || {};
const moneyFields = ['api_spend_usd','api_savings_usd','api_equivalent_spend_usd','api_equivalent_savings_usd'];
const countFields = ['requests','successful_requests','failed_requests','complete_usage_requests','partial_usage_requests','input_tokens','output_tokens','cache_read_tokens','cache_write_tokens','cache_write_1h_tokens','reasoning_tokens','measured_requests','unmeasured_requests','before_tokens','after_tokens','saved_tokens','expanded_requests','legacy_requests','legacy_saved_tokens','observe_would_save_tokens','priced_requests','unpriced_requests','api_spend_requests','api_savings_requests','equivalent_spend_requests','equivalent_savings_requests','unknown_auth_requests'];
const state = {metric:'tokens', group:'provider'};
const $ = id => document.getElementById(id);
const finite = value => typeof value === 'number' && Number.isFinite(value);
const count = value => finite(value) ? value : 0;
const exact = value => finite(value) ? new Intl.NumberFormat('en-US',{maximumFractionDigits:0}).format(value).replace('-', '−') : '—';
const compact = value => finite(value) ? new Intl.NumberFormat('en-US',{notation:'compact',maximumFractionDigits:2}).format(value).replace('-', '−') : '—';
const usd = value => finite(value) ? (value < 0 ? '−' : '') + '$' + new Intl.NumberFormat('en-US',{minimumFractionDigits:2,maximumFractionDigits:Math.abs(value) > 0 && Math.abs(value) < .01 ? 4 : 2}).format(Math.abs(value)) : 'Unknown';
const usdExact = value => finite(value) ? (value < 0 ? '−' : '') + '$' + new Intl.NumberFormat('en-US',{minimumFractionDigits:2,maximumFractionDigits:10}).format(Math.abs(value)) : 'Unknown';
const identity = value => value || 'unknown';
const human = value => identity(value).replaceAll('_', ' ');
const authLabel = value => ({api_key:'API key',subscription:'Subscription',oauth:'OAuth',unknown:'Unknown billing'})[identity(value)] || human(value);
const text = (id, value) => { $(id).textContent = value; };
function node(tag, content, className) { const el = document.createElement(tag); if(content !== undefined) el.textContent = content; if(className) el.className = className; return el; }
function sum(rows) {
  const total = Object.fromEntries(countFields.map(key => [key,0]));
  for(const key of moneyFields) total[key] = null;
  for(const row of rows) {
    for(const key of countFields) total[key] += count(row[key]);
    for(const key of moneyFields) if(finite(row[key])) total[key] = (total[key] ?? 0) + row[key];
  }
  return total;
}
function partition(rows, keyFn) {
  const buckets = new Map();
  for(const row of rows) { const key = keyFn(row); if(!buckets.has(key)) buckets.set(key,[]); buckets.get(key).push(row); }
  return Array.from(buckets, ([key, rows]) => ({key, rows, total:sum(rows)}));
}
function cutoff() {
  const days = Number($('period').value);
  if(!Number.isFinite(days) || days <= 0) return '';
  const end = new Date(report.generated_at || report.window?.to);
  if(!Number.isFinite(end.getTime())) return '';
  end.setUTCHours(0,0,0,0); end.setUTCDate(end.getUTCDate() - days + 1);
  return end.toISOString().slice(0,10);
}
function matches(row, receipt) {
  const from = cutoff();
  const day = receipt ? String(row.timestamp || '').slice(0,10) : row.day;
  if(from && (!day || day < from)) return false;
  return ['provider','model','agent','auth'].every(field => !$(field).value || identity(row[field === 'auth' ? 'auth_mode' : field]) === $(field).value);
}
function populateFilters() {
  for(const field of ['provider','model','agent','auth']) {
    const values = Array.from(new Set(groups.map(row => identity(row[field === 'auth' ? 'auth_mode' : field])))).sort();
    for(const value of values) { const option = node('option', field === 'auth' ? authLabel(value) : human(value)); option.value = value; $(field).append(option); }
    $(field).addEventListener('change', render);
  }
  $('period').addEventListener('change', render);
}
function metricValue(total) {
  if(state.metric==='legacy') return total.legacy_requests?total.legacy_saved_tokens:null;
  return state.metric === 'tokens' ? (total.measured_requests ? total.saved_tokens : null) : total[state.metric === 'api' ? 'api_savings_usd' : 'api_equivalent_savings_usd'];
}
function metricFormat(value, short) { return ['tokens','legacy'].includes(state.metric) ? (short ? compact(value) : exact(value)) : usd(value); }
function moneyCoverage(value, number, label) {
  return finite(value) ? exact(number) + ' eligible ' + label + (number === 1 ? ' request' : ' requests') + ' priced. Unknown rows excluded.' : 'Unavailable: no eligible priced ' + label + ' observations in this selection.';
}
function renderHero(total) {
  const saved = total.measured_requests ? total.saved_tokens : null;
  text('net', compact(saved)); $('net').className = 'hero-num' + (saved < 0 ? ' negative' : '');
  if(saved !== null) $('net').append(node('span',' tokens','hero-unit'));
  const pct = total.before_tokens > 0 ? new Intl.NumberFormat('en-US',{maximumFractionDigits:1}).format(Math.abs(total.saved_tokens) / total.before_tokens * 100) + '% ' + (total.saved_tokens < 0 ? 'larger' : 'smaller') + '. ' : '';
  text('net-note', saved !== null ? pct + exact(total.measured_requests) + ' measured requests; repeated request content counts on each transmission. Session-level savings remain unknown.' : 'No current request measurements in this selection. Run your agent through Caveman to record a comparable original and delivered request.');
  const max = Math.max(total.before_tokens,total.after_tokens,Math.abs(total.saved_tokens),1);
  for(const [id,value] of [['before',total.before_tokens],['after',total.after_tokens],['recovery',total.saved_tokens]]) {
    text(id, saved !== null ? compact(value) : '—'); $(id).title = saved !== null ? exact(value) + ' locally estimated tokens' : 'No comparable measurement';
    $(id+'-bar').style.width = (saved !== null ? Math.abs(value) / max * 100 : 0) + '%';
  }
  $('recovery-bar').style.background = total.saved_tokens < 0 ? '#d44c47' : '#448361';
  text('api-value',usd(total.api_savings_usd)); $('api-value').className = 'metric-num ' + (total.api_savings_usd < 0 ? 'negative' : 'positive');
  text('api-note',moneyCoverage(total.api_savings_usd,total.api_savings_requests,'API') + ' Request counterfactual; not invoice savings.');
  text('equivalent-value',usd(total.api_equivalent_savings_usd)); $('equivalent-value').className = 'metric-num ' + (total.api_equivalent_savings_usd < 0 ? 'negative' : 'equivalent');
  text('equivalent-note',moneyCoverage(total.api_equivalent_savings_usd,total.equivalent_savings_requests,'subscription / OAuth') + ' Hypothetical API value; no seat bill reduction.');
  text('spend-value',usd(total.api_spend_usd));
  text('spend-note',moneyCoverage(total.api_spend_usd,total.api_spend_requests,'API usage') + ' Provider usage × recorded catalog rates.');
  $('legacy-summary').hidden=!total.legacy_requests;
  text('legacy-heading','Earlier segment estimates: '+compact(total.legacy_saved_tokens)+' tokens.');
  $('legacy-heading').title=exact(total.legacy_saved_tokens)+' tokens';
  text('legacy-note',exact(total.legacy_requests)+' earlier requests recorded segment estimates that exclude complete request overhead. These remain separate from request reduction above. View their history under “Earlier estimates.”');
}
function cell(row, content, className) { const el=node('td',content,className); row.append(el); return el; }
function moneyCell(row,value,covered) { const el=cell(row,usd(value),'num' + (value < 0 ? ' negative' : '')); el.title=finite(value) ? exact(covered)+' eligible priced requests; unknown rows excluded' : 'No eligible priced observation'; return el; }
function emptyRow(body,columns,message) { const row=node('tr'); const el=cell(row,message,'empty-row'); el.colSpan=columns; body.append(row); }
function renderDaily(days) {
  const body=$('daily-body'); body.replaceChildren();
  for(const day of days) {
    const row=node('tr'); cell(row,day.key); cell(row,exact(day.total.requests),'num');
    cell(row,day.total.measured_requests ? exact(day.total.saved_tokens) : '—','num' + (day.total.saved_tokens < 0 ? ' negative' : ''));
    cell(row,day.total.legacy_requests ? exact(day.total.legacy_saved_tokens) : '—','num');
    moneyCell(row,day.total.api_savings_usd,day.total.api_savings_requests);
    moneyCell(row,day.total.api_equivalent_savings_usd,day.total.equivalent_savings_requests); body.append(row);
  }
  if(!days.length) emptyRow(body,6,'No recorded requests in this selection.');
}
function svgNode(tag,attrs,content) { const el=document.createElementNS('http://www.w3.org/2000/svg',tag); for(const [key,value] of Object.entries(attrs)) el.setAttribute(key,String(value)); if(content !== undefined) el.textContent=content; return el; }
function renderChart(days,total) {
  const host=$('chart'); host.replaceChildren();
  const value=metricValue(total), isLegacy=state.metric==='legacy', isTokens=state.metric==='tokens'||isLegacy;
  const color=isLegacy?'#cb912f':state.metric==='equivalent' ? '#9065b0' : '#448361';
  text('chart-value',metricFormat(value,true)); $('chart-value').className='chart-value'+(value<0?' negative':'');
  text('chart-caption',isLegacy?'earlier segment estimates · tokens':isTokens?'request tokens reduced':'USD · '+(state.metric==='api'?'estimated API reduction':'hypothetical API equivalent'));
  text('chart-legend-label',isLegacy?'Earlier segment estimates':isTokens?'Request reduction':state.metric==='api'?'API estimate':'API equivalent'); $('chart-color').style.background=color;
  text('timeline-note',isLegacy?'Earlier segment estimates by day in UTC. Complete request overhead is excluded; these are never added to current request reduction.':'Daily deltas in UTC. Red bars keep regressions visible.');
  const known=days.filter(day=>finite(metricValue(day.total)));
  if(!known.length) { host.append(node('div',isLegacy?'No earlier segment estimates in this selection.':isTokens?'No comparable token measurements in this selection.':'No eligible priced measurements in this selection.','chart-empty')); return; }
  const width=940,height=230,left=55,right=10,top=17,bottom=30,plotHeight=height-top-bottom;
  const values=known.map(day=>metricValue(day.total));
  const low=Math.min(...values,0), maximum=Math.max(...values,0), high=maximum===0 && low===0 ? 1 : maximum, span=high-low;
  const y=value=>top+(high-value)/span*plotHeight;
  const chart=svgNode('svg',{viewBox:'0 0 '+width+' '+height,role:'img','aria-label':'Daily '+(isLegacy?'earlier segment token estimates':isTokens?'request token reduction':state.metric==='api'?'estimated API cost reduction':'hypothetical API equivalent')+'. Exact values are available in the daily values table.'});
  for(let i=0;i<=4;i++) { const value=high-span*i/4,yy=top+plotHeight*i/4; chart.append(svgNode('line',{x1:left,x2:width-right,y1:yy,y2:yy,stroke:'#ededec','stroke-width':1})); chart.append(svgNode('text',{x:left-9,y:yy+4,'text-anchor':'end'},isTokens?compact(value):usd(value))); }
  const reportFrom=String(report.window?.from||days[0].key).slice(0,10),requestedFrom=cutoff();
  const firstDate=Date.parse((requestedFrom>reportFrom?requestedFrom:reportFrom)+'T00:00:00Z');
  const lastDate=Date.parse(String(report.window?.to||report.generated_at||days[days.length-1].key).slice(0,10)+'T00:00:00Z');
  const calendar=Number.isFinite(firstDate)&&Number.isFinite(lastDate)&&lastDate>=firstDate;
  const slots=calendar?Math.max(1,Math.round((lastDate-firstDate)/86400000)+1):days.length;
  const baseline=y(0),step=(width-left-right)/slots;
  const barWidth=Math.max(.5,Math.min(30,step*.66));
  for(let i=0;i<days.length;i++) {
    const day=days[i],value=metricValue(day.total),slot=calendar?Math.round((Date.parse(day.key+'T00:00:00Z')-firstDate)/86400000):i,center=left+step*(slot+.5);
    if(finite(value)) {
      const bar=svgNode('rect',{x:center-barWidth/2,y:Math.min(y(value),baseline),width:barWidth,height:Math.max(value===0?1:2,Math.abs(y(value)-baseline)),rx:2,fill:value<0?'#d44c47':color,class:'bar',tabindex:0});
      bar.append(svgNode('title',{},day.key+' · '+metricFormat(value,false)+(isTokens?' tokens':'')+' · '+exact(day.total.requests)+' requests')); chart.append(bar);
    }
    const stride=Math.max(1,Math.ceil(days.length/7));
    if(i%stride===0 || i===days.length-1 && days.length>1 && (i-1)%stride!==0) chart.append(svgNode('text',{x:center,y:height-8,'text-anchor':'middle'},day.key.slice(5)));
  }
  chart.append(svgNode('line',{x1:left,x2:width-right,y1:baseline,y2:baseline,stroke:'#d8d7d4','stroke-width':1})); host.append(chart);
}
function renderBreakdown(rows) {
  const body=$('breakdown-body'); body.replaceChildren();
  text('breakdown-label',state.group.charAt(0).toUpperCase()+state.group.slice(1));
  const buckets=partition(rows,row=>state.group==='model'?identity(row.provider)+'\u0000'+identity(row.model):identity(row[state.group])).sort((a,b)=>Math.abs(b.total.saved_tokens)-Math.abs(a.total.saved_tokens)||b.total.requests-a.total.requests||a.key.localeCompare(b.key));
  for(const bucket of buckets) {
    const row=node('tr'), first=bucket.rows[0],name=human(first[state.group]);
    const label=cell(row,name,'table-name'); label.title=name;
    const modelCount=new Set(bucket.rows.map(item=>identity(item.model))).size;
    const details=state.group==='model'?human(first.provider):modelCount+' model'+(modelCount===1?'':'s');
    label.append(node('span',details,'row-sub'));
    cell(row,exact(bucket.total.requests),'num'); cell(row,bucket.total.measured_requests?compact(bucket.total.before_tokens):'—','num');
    const delta=cell(row,bucket.total.measured_requests?compact(bucket.total.saved_tokens):'—','num'+(bucket.total.saved_tokens<0?' negative':' positive')); delta.title=bucket.total.measured_requests?exact(bucket.total.saved_tokens)+' locally estimated tokens; '+exact(bucket.total.measured_requests)+' measured requests':'No comparable request measurement';
    moneyCell(row,bucket.total.api_savings_usd,bucket.total.api_savings_requests);
    moneyCell(row,bucket.total.api_equivalent_savings_usd,bucket.total.equivalent_savings_requests); body.append(row);
  }
  if(!buckets.length) emptyRow(body,6,'No recorded requests match these filters.');
}
function properties(id,items) { const host=$(id); host.replaceChildren(); for(const [label,value] of items) host.append(node('dt',label),node('dd',value)); }
function renderCoverage(total) {
  const usageValue=value=>total.complete_usage_requests||total.partial_usage_requests&&value!==0?exact(value):'Unknown';
  properties('usage',[
    ['Requests',exact(total.requests)],['Successful / failed',exact(total.successful_requests)+' / '+exact(total.failed_requests)],
    ['Input tokens',usageValue(total.input_tokens)],['Output tokens',usageValue(total.output_tokens)],['Cache read tokens',usageValue(total.cache_read_tokens)],
    ['Cache write tokens',usageValue(total.cache_write_tokens)],['Cache write · 1 hour',usageValue(total.cache_write_1h_tokens)],['Reasoning tokens',usageValue(total.reasoning_tokens)],
    ['Subscription API-equivalent usage',usd(total.api_equivalent_spend_usd)]
  ]);
  properties('health',[
    ['Comparable request measurements',exact(total.measured_requests)+' / '+exact(total.requests)],
    ['Missing request measurements',exact(total.unmeasured_requests)],['Requests that grew',exact(total.expanded_requests)],
    ['Complete / partial usage',exact(total.complete_usage_requests)+' / '+exact(total.partial_usage_requests)],
    ['Known / unknown model pricing',exact(total.priced_requests)+' / '+exact(total.unpriced_requests)],
    ['Unknown billing mode',exact(total.unknown_auth_requests)],['Legacy token estimates (separate)',exact(total.legacy_saved_tokens)],
    ['Watch-only potential (separate)',exact(total.observe_would_save_tokens)],['Full-session recovery cost','Not measured']
  ]);
}
function formulaPart(value,label) { const item=node('div',undefined,'formula-part'); item.append(node('strong',exact(value)),node('span',label)); return item; }
function renderReceipts() {
  const selected=receipts.filter(row=>matches(row,true)); const host=$('receipts'); host.replaceChildren();
  text('receipt-count',exact(selected.length));
  text('receipt-note',evidence.receipts_truncated ? 'Showing matching receipts from the latest '+exact(evidence.receipts_limit)+' requests. Charts and totals include the whole selected window.' : 'Open a request to inspect its original and delivered counts, exact model, and pricing evidence.');
  for(const receipt of selected) {
    const detail=node('details'),summary=node('summary'),delta=receipt.saved_tokens;
    summary.append(node('span','▶','caret'),node('span',human(receipt.provider)+' / '+human(receipt.model),'receipt-title'),node('span',String(receipt.timestamp||'').replace('T',' ').slice(0,16)+' UTC','receipt-time'));
    summary.append(node('span',finite(delta)?compact(delta)+' tokens':receipt.legacy_requests?'Earlier: '+compact(receipt.legacy_saved_tokens)+' tokens':'Not measured','receipt-total'+(delta<0?' negative':finite(delta)?' positive':'')));
    const body=node('div',undefined,'receipt-body'); body.append(node('p',human(receipt.agent)+' · '+authLabel(receipt.auth_mode)+' · HTTP '+receipt.status_code+' · '+receipt.id,'receipt-meta'));
    if(finite(delta)) {
      const formula=node('div',undefined,'formula'); formula.append(formulaPart(receipt.tokens_before,'original request'),node('span','−','operator'),formulaPart(receipt.tokens_after,'delivered request'),node('span','=','operator'),formulaPart(delta,'token reduction')); body.append(formula);
    } else { body.append(node('p',receipt.legacy_requests?'Earlier segment estimate: '+exact(receipt.legacy_saved_tokens)+' tokens. Complete request overhead is excluded; current request delta is unavailable.':'Comparable token delta unavailable: '+human(receipt.measurement_status)+'.','receipt-money')); }
    const metrics=receipt.metrics || receipt;
    const amounts=[['API estimate',metrics.api_savings_usd],['API equivalent',metrics.api_equivalent_savings_usd],['API spend estimate',metrics.api_spend_usd]].filter(([,value])=>finite(value)).map(([label,value])=>label+': '+usd(value));
    body.append(node('p',amounts.length?amounts.join(' · '):'Dollar estimate unknown: no eligible priced measurement.','receipt-money'));
    if(finite(delta) && finite(receipt.estimated_input_delta_usd) && finite(receipt.effective_input_rate_per_million)) body.append(node('p',exact(delta)+' tokens × '+usdExact(receipt.effective_input_rate_per_million)+' effective input rate / 1,000,000 = '+usdExact(receipt.estimated_input_delta_usd)+' '+(metrics.equivalent_savings_requests?'API equivalent':'API estimate')+'. The recorded fresh/cache-read/cache-write mix determines this rate.','receipt-money'));
    const price=receipt.price;
    if(price) {
      body.append(node('p','Catalog '+price.catalog_version+' · '+price.provider+'/'+price.model+' · applied rates per 1M tokens','receipt-meta'));
      body.append(node('p','Input '+usd(price.input_per_million)+' · cache read '+usd(price.cache_read_per_million)+' · cache write 5m '+usd(price.cache_write_per_million)+' · cache write 1h '+usd(price.cache_write_1h_per_million)+' · output '+usd(price.output_per_million)+' · reasoning '+usd(price.reasoning_per_million),'receipt-meta'));
    }
    body.append(node('p','Measurement: '+human(receipt.measurement_status)+' · '+(receipt.measurement_basis||'unknown')+' · Savings: '+(receipt.savings_basis||'unknown')+' · Usage: '+(receipt.token_usage_basis||'unknown'),'receipt-meta'));
    if(receipt.raw_request_sha256 || receipt.transformed_request_sha256) body.append(node('p','Original SHA-256: '+(receipt.raw_request_sha256||'unknown')+' · Delivered SHA-256: '+(receipt.transformed_request_sha256||'unknown'),'receipt-meta'));
    detail.append(summary,body); host.append(detail);
  }
  if(!selected.length) host.append(node('p',evidence.receipts_truncated?'No matching receipts in the bounded recent sample. Aggregate totals may include older matching requests.':'No request receipts in this selection.','note'));
}
function render() {
  const selected=groups.filter(row=>matches(row,false)),total=sum(selected);
  const days=partition(selected,row=>row.day || 'Unknown date').sort((a,b)=>a.key.localeCompare(b.key));
  const window=report.window||{},reportFrom=String(window.from||'').slice(0,10),requestedFrom=cutoff(),from=requestedFrom>reportFrom?requestedFrom:reportFrom,to=String(window.to||report.generated_at||'').slice(0,10);
  const providerCount=new Set(selected.map(row=>identity(row.provider))).size,modelCount=new Set(selected.map(row=>identity(row.provider)+'/'+identity(row.model))).size;
  text('scope',exact(total.requests)+' request'+(total.requests===1?'':'s')+' · '+(from?from+' — '+to:'All recorded history through '+to)+' UTC · '+exact(providerCount)+' provider'+(providerCount===1?'':'s')+' · '+exact(modelCount)+' model'+(modelCount===1?'':'s'));
  renderHero(total); renderDaily(days); renderChart(days,total); renderBreakdown(selected); renderCoverage(total); renderReceipts();
}
populateFilters();
$('legacy-tab').hidden=!groups.some(row=>row.legacy_requests>0);
for(const el of document.querySelectorAll('[data-metric]')) el.addEventListener('click',()=>{state.metric=el.dataset.metric; for(const tab of document.querySelectorAll('[data-metric]')) tab.setAttribute('aria-pressed',String(tab===el)); render();});
for(const el of document.querySelectorAll('[data-group]')) el.addEventListener('click',()=>{state.group=el.dataset.group; for(const tab of document.querySelectorAll('[data-group]')) tab.setAttribute('aria-pressed',String(tab===el)); render();});
$('reset').addEventListener('click',()=>{for(const id of ['provider','model','agent','auth']) $(id).value=''; $('period').value='all'; render();});
$('download').addEventListener('click',()=>{const url=URL.createObjectURL(new Blob([JSON.stringify(report,null,2)+'\n'],{type:'application/json'})); const link=node('a');link.href=url;link.download='caveman-stats-'+String(report.generated_at||'snapshot').slice(0,10)+'.json';document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1000);});
for(const caveat of evidence.caveats || []) $('caveats').append(node('li',caveat));
text('method-meta',(report.schema||'')+' · tokens: '+(evidence.token_basis||'unknown')+' · spend: '+(evidence.spend_basis||'unknown')+' · catalogs: '+((evidence.catalog_versions||[]).join(', ')||'none'));
text('generated','Generated '+String(report.generated_at||'').replace('T',' ').replace('Z',' UTC'));
render();
</script>
</body>
</html>`))
