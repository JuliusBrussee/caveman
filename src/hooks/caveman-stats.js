#!/usr/bin/env node
// caveman-stats — read the active Claude Code session log, print real token
// usage plus an estimated savings figure from the benchmark in benchmarks/.
//
// Run directly:    node hooks/caveman-stats.js
// Inside Claude:   /caveman-stats triggers this via the UserPromptSubmit hook.
// Hook integration passes --session-file <transcript_path> so we always read
// the active session, not whichever JSONL was modified most recently.

const fs = require('fs');
const path = require('path');
const os = require('os');
const { readFlag, appendFlag, readHistory, safeWriteFlag, VALID_MODES, MODE_LOG_BASENAME } = require('./caveman-config');

// Mean per-task savings from benchmarks/results/*.json (avg_savings: 65 across
// 10 tasks, sonnet-4-20250514). Only 'full' has measured data; lite / ultra /
// wenyan modes show no estimate until benchmarked. Add an entry here when a new
// run is committed.
const COMPRESSION = { 'full': 0.65 };

// Approximate Anthropic public output-token pricing, USD per million.
// Match by model id prefix so this stays correct across point releases
// (e.g. claude-sonnet-4-20250514, claude-sonnet-4-7). Update from
// https://www.anthropic.com/pricing if a release changes the tier.
// Most-specific prefixes MUST come first — priceForModel returns the first match.
const MODEL_OUTPUT_PRICE_PER_M = [
  // Legacy Opus 4.0 / 4.1 (pre-4.5) billed at the old $75/M output tier,
  // including the dated ids (e.g. claude-opus-4-20250514).
  ['claude-opus-4-0',    75.00],
  ['claude-opus-4-1',    75.00],
  ['claude-opus-4-2025', 75.00],
  // Opus 4.5–4.8 dropped to $25/M output (rate card held since 4.5).
  ['claude-opus-4',      25.00],
  ['claude-sonnet-4',    15.00],
  ['claude-haiku-4',      5.00],   // Haiku 4.5 = $5/M output
  ['claude-3-5-sonnet',  15.00],
  ['claude-3-5-haiku',    4.00],
  ['claude-3-opus',      75.00],
];

function priceForModel(model) {
  if (!model) return null;
  for (const [prefix, price] of MODEL_OUTPUT_PRICE_PER_M) {
    if (model.startsWith(prefix)) return price;
  }
  return null;
}

function formatUsd(amount) {
  if (amount >= 1) return `$${amount.toFixed(2)}`;
  if (amount >= 0.01) return `$${amount.toFixed(3)}`;
  return `$${amount.toFixed(4)}`;
}

function findRecentSession(claudeDir) {
  const projectsDir = path.join(claudeDir, 'projects');
  let entries;
  try { entries = fs.readdirSync(projectsDir, { withFileTypes: true }); }
  catch { return null; }

  let best = null;
  const stack = entries.map(e => path.join(projectsDir, e.name));
  while (stack.length) {
    const p = stack.pop();
    let st;
    try { st = fs.statSync(p); } catch { continue; }
    if (st.isDirectory()) {
      try {
        for (const child of fs.readdirSync(p)) stack.push(path.join(p, child));
      } catch {}
    } else if (p.endsWith('.jsonl') && (!best || st.mtimeMs > best.mtime)) {
      best = { file: p, mtime: st.mtimeMs };
    }
  }
  return best ? best.file : null;
}

function parseSession(filePath) {
  let raw;
  try { raw = fs.readFileSync(filePath, 'utf8'); }
  catch { return { outputTokens: 0, cacheReadTokens: 0, turns: 0, model: null, messages: [] }; }

  let outputTokens = 0;
  let cacheReadTokens = 0;
  let turns = 0;
  let model = null;
  const messages = []; // per-message {ts, outputTokens} for mode attribution (#601)
  for (const line of raw.split('\n')) {
    if (!line.trim()) continue;
    let entry;
    try { entry = JSON.parse(line); } catch { continue; }
    if (entry.type !== 'assistant' || !entry.message) continue;
    const usage = entry.message.usage;
    if (!usage) continue;
    outputTokens    += usage.output_tokens           || 0;
    cacheReadTokens += usage.cache_read_input_tokens || 0;
    turns++;
    if (!model && entry.message.model) model = entry.message.model;
    const ts = entry.timestamp ? Date.parse(entry.timestamp) : NaN;
    messages.push({
      ts: Number.isFinite(ts) ? ts : null,
      outputTokens: usage.output_tokens || 0,
    });
  }
  return { outputTokens, cacheReadTokens, turns, model, messages };
}

// Detect *.original.md / *.md pairs left behind by caveman-compress. The
// presence of a *.original.md backup means the *.md sibling is a compressed
// memory file — every session start reads the compressed version, so the
// delta is per-session input-token savings (passive). Returns a summary or
// null if nothing was found in the given dirs.
function findCompressedPairs(dirs) {
  const pairs = [];
  for (const dir of dirs) {
    let entries;
    try { entries = fs.readdirSync(dir, { withFileTypes: true }); }
    catch { continue; }
    for (const entry of entries) {
      if (!entry.isFile() || !entry.name.endsWith('.original.md')) continue;
      const base = entry.name.slice(0, -'.original.md'.length);
      const originalPath = path.join(dir, entry.name);
      const compressedPath = path.join(dir, `${base}.md`);
      let oSize, cSize;
      try {
        oSize = fs.statSync(originalPath).size;
        cSize = fs.statSync(compressedPath).size;
      } catch { continue; }
      if (oSize <= cSize) continue;
      pairs.push({ name: base, dir, originalSize: oSize, compressedSize: cSize });
    }
  }
  return pairs;
}

function summarizeCompressed(pairs) {
  if (!pairs || pairs.length === 0) return null;
  const totalOriginal = pairs.reduce((s, p) => s + p.originalSize, 0);
  const totalCompressed = pairs.reduce((s, p) => s + p.compressedSize, 0);
  const bytesSaved = totalOriginal - totalCompressed;
  // English prose runs ~4 chars per token. Label result as approximate so we
  // don't make claims tighter than the method warrants.
  const tokensSaved = Math.round(bytesSaved / 4);
  return { count: pairs.length, bytesSaved, tokensSaved };
}

// ── Per-mode attribution (#601) ─────────────────────────────────────────────
// The whole session's tokens must never be credited to whatever mode the flag
// happens to hold at stats time — a mid-session mode change would inflate the
// estimate (verbose tokens counted as compressed) or zero it (caveman tokens
// counted as uncompressed). The mode tracker + SessionStart hook append
// {ts, mode, prev} rows to .caveman-mode-log.jsonl on every actual transition;
// stats joins those timestamps against the session JSONL message timestamps.

// Read + validate the transition log. Returns rows sorted by ts.
function readModeLog(logPath) {
  const rows = [];
  for (const line of readHistory(logPath)) {
    let e;
    try { e = JSON.parse(line); } catch { continue; }
    if (!e || typeof e !== 'object' || !Number.isFinite(e.ts)) continue;
    const norm = (v) => (v == null ? null : (VALID_MODES.includes(String(v)) ? String(v) : undefined));
    const mode = norm(e.mode);
    const prev = norm(e.prev);
    if (mode === undefined || prev === undefined) continue; // reject non-whitelisted values
    rows.push({ ts: e.ts, mode, prev });
  }
  rows.sort((a, b) => a.ts - b.ts);
  return rows;
}

// Attribute each message's output tokens to the mode active when it was
// generated. Sources, most to least exact:
//   'log'           — the transition log covers the message (rows at/before its
//                     ts, or the first row's `prev` for the pre-inception span).
//   'flag-mtime'    — no log rows, but the flag was written mid-session: tokens
//                     from the write onward belong to the current mode; earlier
//                     tokens have UNKNOWN mode and are excluded, never guessed
//                     (no-fake-savings). Messages without timestamps are also
//                     unknown in this case.
//   'whole-session' — no log and no evidence of a mid-session change: the
//                     current mode covers the whole session (correct when the
//                     mode never changed; pre-#601 behavior).
// Returns { byMode: {modeKey: tokens}, unknownTokens, basis } where modeKey is
// a mode string or 'none' (caveman inactive).
function attributeByMode({ messages, modeLog, mode, flagMtimeMs, outputTokens }) {
  const currentKey = mode || 'none';
  const msgs = messages || [];
  let firstTs = null;
  for (const m of msgs) {
    if (m.ts != null && (firstTs === null || m.ts < firstTs)) firstTs = m.ts;
  }

  let events = modeLog || [];
  let basis = 'log';
  let prefixMode; // mode for messages before the first event (undefined = unknown)
  if (events.length === 0) {
    if (flagMtimeMs != null && firstTs != null && flagMtimeMs > firstTs) {
      // Flag written mid-session with no transition log: only the span from
      // the write onward is attributable. The write may have been a
      // reaffirmation of the same mode, but assuming so would guess savings
      // into existence — exclude the prefix instead.
      events = [{ ts: flagMtimeMs, mode: mode || null }];
      basis = 'flag-mtime';
      prefixMode = undefined;
    } else {
      return { byMode: { [currentKey]: outputTokens || 0 }, unknownTokens: 0, basis: 'whole-session' };
    }
  } else {
    // Every transition since log inception is recorded, so the span before
    // the first row ran under that row's `prev` mode.
    prefixMode = events[0].prev;
  }

  const byMode = {};
  let unknownTokens = 0;
  const add = (key, tokens) => { byMode[key] = (byMode[key] || 0) + tokens; };
  for (const m of msgs) {
    if (m.ts == null) { unknownTokens += m.outputTokens; continue; }
    let active;
    for (const ev of events) {
      if (ev.ts <= m.ts) active = ev;
      else break;
    }
    if (active !== undefined) add(active.mode || 'none', m.outputTokens);
    else if (prefixMode !== undefined) add(prefixMode || 'none', m.outputTokens);
    else unknownTokens += m.outputTokens;
  }
  return { byMode, unknownTokens, basis };
}

// Attribution shape for callers without a session log to join against
// (kept for formatStats/formatShare backward compatibility in tests).
function wholeSessionAttribution(mode, outputTokens) {
  return { byMode: { [mode || 'none']: outputTokens || 0 }, unknownTokens: 0, basis: 'whole-session' };
}

// Compute the savings figures we want to log/share for one session snapshot.
// Sums per-mode: only spans whose mode has benchmark data earn an estimate;
// unknown spans earn nothing.
function deriveSavings({ byMode, model }) {
  let estSavedTokens = 0;
  for (const [key, tokens] of Object.entries(byMode || {})) {
    const ratio = COMPRESSION[key];
    if (ratio == null || tokens <= 0) continue;
    estSavedTokens += Math.round(tokens / (1 - ratio)) - tokens;
  }
  const price = priceForModel(model);
  const estSavedUsd = price !== null ? (estSavedTokens / 1_000_000) * price : 0;
  return { estSavedTokens, estSavedUsd };
}

// Parse "7d", "12h" etc. to milliseconds. Returns null on invalid input.
function parseDuration(spec) {
  if (!spec) return null;
  const m = /^(\d+)([dh])$/.exec(spec.trim());
  if (!m) return null;
  const n = parseInt(m[1], 10);
  return m[2] === 'd' ? n * 86_400_000 : n * 3_600_000;
}

// Aggregate history into latest-per-session totals, optionally filtered to a
// time window. Returns { sessions, outputTokens, estSavedTokens, estSavedUsd }.
function aggregateHistory(historyPath, sinceMs) {
  const lines = readHistory(historyPath);
  const cutoff = sinceMs ? Date.now() - sinceMs : null;
  const latestPerSession = new Map();
  for (const line of lines) {
    let entry;
    try { entry = JSON.parse(line); } catch { continue; }
    if (!entry || typeof entry !== 'object') continue;
    if (cutoff !== null && (entry.ts || 0) < cutoff) continue;
    const id = entry.session_id || '_';
    const prev = latestPerSession.get(id);
    if (!prev || (entry.ts || 0) >= (prev.ts || 0)) latestPerSession.set(id, entry);
  }
  let outputTokens = 0, estSavedTokens = 0, estSavedUsd = 0;
  for (const e of latestPerSession.values()) {
    outputTokens   += e.output_tokens     || 0;
    estSavedTokens += e.est_saved_tokens  || 0;
    estSavedUsd    += e.est_saved_usd     || 0;
  }
  return { sessions: latestPerSession.size, outputTokens, estSavedTokens, estSavedUsd };
}

// Output-reduction share: saved / (saved + used) = the fraction of the
// would-be OUTPUT tokens that caveman avoided. That is the only ratio we can
// honestly compute from output counts alone. It is NOT a share of session or
// limit usage — input + cache tokens dominate agentic sessions, count against
// Pro/Max limits, and are not reduced by caveman, so real limit relief is far
// smaller (docs/HONEST-NUMBERS.md: session-level totals land ~14–21%, below
// zero on terse workloads). Never label this "usage" or "budget". Returns a
// rounded percent, or null when there is nothing measured to divide.
function outputReductionPct(savedTokens, usedTokens) {
  if (!Number.isFinite(savedTokens) || !Number.isFinite(usedTokens)) return null;
  if (savedTokens <= 0 || usedTokens < 0) return null;
  const total = savedTokens + usedTokens;
  if (total <= 0) return null;
  return Math.round((savedTokens / total) * 100);
}

function humanizeTokens(n) {
  if (!Number.isFinite(n) || n <= 0) return '0';
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k';
  return String(Math.round(n));
}

function formatHistory({ sessions, outputTokens, estSavedTokens, estSavedUsd, since }) {
  const sep = '──────────────────────────────────';
  const window = since ? ` (last ${since})` : '';
  if (sessions === 0) {
    return `\nCaveman Stats — Lifetime${window}\n${sep}\nNo sessions logged yet — run /caveman-stats inside any session to start tracking.\n${sep}\n`;
  }
  const usdLine = estSavedUsd > 0 ? `Est. saved (USD):      ~${formatUsd(estSavedUsd)}\n` : '';
  const pct = outputReductionPct(estSavedTokens, outputTokens);
  const budgetLine = pct !== null
    ? `Est. output reduction: ~${pct}% (output tokens only, est.)\n`
    : '';
  return `\nCaveman Stats — Lifetime${window}\n${sep}\n` +
    `Sessions:   ${sessions.toLocaleString()}\n${sep}\n` +
    `Output tokens:         ${outputTokens.toLocaleString()}\n` +
    `Est. tokens saved:     ${estSavedTokens.toLocaleString()}\n` +
    budgetLine + usdLine + sep + '\n';
}

// Single-line tweetable summary. Stays human-friendly when no ratio is known.
// Savings come from per-mode attribution (#601) so a mid-session mode change
// never inflates the shared number.
function formatShare({ outputTokens, turns, mode, model, attribution }) {
  if (turns === 0) {
    return '🪨 caveman armed but no turns yet — caveman.sh';
  }
  const attr = attribution || wholeSessionAttribution(mode, outputTokens);
  const { estSavedTokens, estSavedUsd } = deriveSavings({ byMode: attr.byMode, model });

  if (estSavedTokens > 0) {
    const usd = estSavedUsd > 0 ? ` (~${formatUsd(estSavedUsd)})` : '';
    return `🪨 Saved ${estSavedTokens.toLocaleString()} output tokens${usd} across ${turns} turns this session — caveman.sh`;
  }
  return `🪨 ${turns} turns, ${outputTokens.toLocaleString()} output tokens this session — caveman.sh`;
}

// Pure formatter — separated from main() so tests can pass synthetic inputs.
// `attribution` (from attributeByMode, #601) splits output tokens per mode;
// when omitted, the current mode is assumed for the whole session.
function formatStats({ outputTokens, cacheReadTokens, turns, mode, model, sessionPath, compressed, attribution }) {
  const sep = '──────────────────────────────────';
  const shortPath = sessionPath && sessionPath.length > 45
    ? '...' + sessionPath.slice(-45)
    : (sessionPath || '');

  if (turns === 0) {
    return `\nCaveman Stats\n${sep}\nNo conversation yet — stats available after first response.\n${sep}\n`;
  }

  const attr = attribution || wholeSessionAttribution(mode, outputTokens);
  const activeKeys = Object.keys(attr.byMode).filter(k => attr.byMode[k] > 0);
  // Uniform = every token ran under the CURRENT mode. Anything else — a
  // second mode, tokens under a mode the flag no longer shows, or spans we
  // could not attribute — gets the per-mode breakdown below.
  const uniform = attr.unknownTokens === 0 &&
    (activeKeys.length === 0 || (activeKeys.length === 1 && activeKeys[0] === (mode || 'none')));

  const ratio = COMPRESSION[mode] != null ? COMPRESSION[mode] : null;
  const price = priceForModel(model);

  let savings;
  let footer = '';
  if (!uniform) {
    const { estSavedTokens, estSavedUsd } = deriveSavings({ byMode: attr.byMode, model });
    const lines = [attr.basis === 'flag-mtime'
      ? 'Mode was set mid-session — only output after the change is attributed:'
      : 'Mode changed mid-session — output attributed per mode:'];
    for (const key of activeKeys) {
      const tokens = attr.byMode[key];
      const r = COMPRESSION[key];
      const label = key === 'none' ? 'caveman off' : key;
      const note = r != null
        ? `est. ${(Math.round(tokens / (1 - r)) - tokens).toLocaleString()} saved`
        : 'no benchmark estimate';
      lines.push(`  ${label}: ${tokens.toLocaleString()} tokens (${note})`);
    }
    if (attr.unknownTokens > 0) {
      lines.push(`  unattributed: ${attr.unknownTokens.toLocaleString()} tokens (mode unknown — excluded from estimate)`);
    }
    lines.push(`Est. tokens saved:     ${estSavedTokens.toLocaleString()}`);
    if (estSavedUsd > 0) lines.push(`Est. saved (USD):      ~${formatUsd(estSavedUsd)}`);
    savings = lines.join('\n');

    footer = 'Savings est. from benchmarks/ (mean per-task), applied only to spans whose mode is known.';
    if (estSavedUsd > 0) footer += ` Pricing for ${model}.`;
    if (attr.basis === 'flag-mtime') {
      footer += ' Tokens before the mode change could not be attributed and are excluded rather than guessed.';
    } else if (attr.unknownTokens > 0) {
      footer += ' Unattributed tokens are excluded rather than guessed.';
    }
    footer += ' Reduction is of output tokens only; input/cache usage is unchanged.';
  } else if (ratio !== null) {
    const estNormal = Math.round(outputTokens / (1 - ratio));
    const estSaved = estNormal - outputTokens;
    let usdLine = '';
    if (price !== null) {
      const usd = (estSaved / 1_000_000) * price;
      usdLine = `Est. saved (USD):      ~${formatUsd(usd)}\n`;
      footer = `Savings est. from benchmarks/ (mean per-task). Pricing for ${model}. Actual varies by task.`;
    } else {
      footer = 'Savings est. from benchmarks/ (mean per-task). Actual varies by task.';
    }
    // No "% of your usage/budget" line here on purpose: from output tokens
    // alone the only computable ratio is the output reduction already shown
    // on the line above, and input + cache tokens (which dominate agentic
    // sessions and count against Pro/Max limits) are untouched by caveman —
    // any session-usage % would overstate real limit relief. See
    // docs/HONEST-NUMBERS.md.
    footer += ' Reduction is of output tokens only; input/cache usage is unchanged.';
    savings = (`Est. without caveman:  ${estNormal.toLocaleString()}\n` +
              `Est. tokens saved:     ${estSaved.toLocaleString()} (~${Math.round(ratio * 100)}% of output)\n` +
              usdLine).replace(/\n$/, '');
  } else if (mode && mode !== 'off') {
    savings = `No savings estimate for '${mode}' mode — only 'full' has benchmark data.`;
  } else {
    savings = 'Caveman not active this session.';
  }

  let memoryLine = '';
  if (compressed && compressed.count > 0) {
    const tokensApprox = compressed.tokensSaved.toLocaleString();
    memoryLine = `${sep}\nMemory compressed:     ${compressed.count} file${compressed.count === 1 ? '' : 's'}, ` +
      `~${tokensApprox} tokens saved per session start (approx)\n`;
  }

  return `\nCaveman Stats\n${sep}\n` +
    (shortPath ? `Session:  ${shortPath}\n` : '') +
    `Turns:    ${turns}\n${sep}\n` +
    `Output tokens:         ${outputTokens.toLocaleString()}\n` +
    `Cache-read tokens:     ${cacheReadTokens.toLocaleString()}\n${sep}\n` +
    `${savings}\n` +
    memoryLine +
    (footer ? footer + '\n' : '');
}

function main() {
  const args = process.argv.slice(2);
  const i = args.indexOf('--session-file');
  const sessionFileArg = i !== -1 ? args[i + 1] : null;
  const share = args.includes('--share');
  const all = args.includes('--all');
  const sinceIdx = args.indexOf('--since');
  const sinceArg = sinceIdx !== -1 ? args[sinceIdx + 1] : null;

  const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
  const historyPath = path.join(claudeDir, '.caveman-history.jsonl');

  // Lifetime aggregation paths short-circuit before we need a live session.
  if (all || sinceArg) {
    const sinceMs = parseDuration(sinceArg);
    if (sinceArg && sinceMs === null) {
      process.stderr.write(`caveman-stats: --since takes Nh or Nd (e.g. 7d, 24h), got: ${sinceArg}\n`);
      process.exit(2);
    }
    const agg = aggregateHistory(historyPath, sinceMs);
    process.stdout.write(formatHistory({ ...agg, since: sinceArg || null }));
    return;
  }

  const sessionFile = sessionFileArg || findRecentSession(claudeDir);

  if (!sessionFile) {
    process.stderr.write('caveman-stats: no Claude Code session found.\n');
    process.exit(1);
  }

  const parsed = parseSession(sessionFile);
  const flagPath = path.join(claudeDir, '.caveman-active');
  const mode = readFlag(flagPath);

  // #601: attribute tokens to the mode active when each message happened,
  // via the transition log the hooks maintain (fallbacks documented on
  // attributeByMode). Never credit the whole session to the current flag.
  let flagMtimeMs = null;
  try { flagMtimeMs = fs.statSync(flagPath).mtimeMs; } catch (e) {}
  const modeLog = readModeLog(path.join(claudeDir, MODE_LOG_BASENAME));
  const attribution = attributeByMode({
    messages: parsed.messages,
    modeLog,
    mode,
    flagMtimeMs,
    outputTokens: parsed.outputTokens,
  });

  // Append a snapshot of this session's totals to the lifetime log. Multiple
  // /caveman-stats calls in one session emit multiple lines for the same
  // session_id; aggregateHistory keeps only the latest per session_id.
  if (parsed.turns > 0) {
    const { estSavedTokens, estSavedUsd } = deriveSavings({ byMode: attribution.byMode, model: parsed.model });
    const sessionId = path.basename(sessionFile, '.jsonl');
    appendFlag(historyPath, JSON.stringify({
      ts: Date.now(),
      session_id: sessionId,
      mode: mode || null,
      model: parsed.model || null,
      output_tokens: parsed.outputTokens,
      est_saved_tokens: estSavedTokens,
      est_saved_usd: estSavedUsd,
    }));

    // Statusline suffix: tiny pre-rendered string the shell statusline can
    // cat without parsing JSONL. Updated on every /caveman-stats run.
    // Routed through safeWriteFlag — the suffix path is predictable and
    // user-owned, same symlink-clobber surface as the .caveman-active flag.
    const agg = aggregateHistory(historyPath, null);
    const suffix = agg.estSavedTokens > 0 ? `⛏  ${humanizeTokens(agg.estSavedTokens)}` : '';
    safeWriteFlag(path.join(claudeDir, '.caveman-statusline-suffix'), suffix);
  }

  if (share) {
    process.stdout.write(formatShare({ ...parsed, mode, attribution }) + '\n');
  } else {
    const scanDirs = [claudeDir, process.cwd()].filter((d, i, a) => a.indexOf(d) === i);
    const compressed = summarizeCompressed(findCompressedPairs(scanDirs));
    process.stdout.write(formatStats({ ...parsed, mode, sessionPath: sessionFile, compressed, attribution }));
  }
}

if (require.main === module) main();

module.exports = {
  formatStats, formatShare, formatHistory, aggregateHistory, parseDuration, deriveSavings,
  parseSession, priceForModel, formatUsd, COMPRESSION, MODEL_OUTPUT_PRICE_PER_M,
  findCompressedPairs, summarizeCompressed, humanizeTokens, outputReductionPct,
  readModeLog, attributeByMode,
};
