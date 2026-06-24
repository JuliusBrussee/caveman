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
const { readFlag, appendFlag, readHistory, safeWriteFlag } = require('./caveman-config');

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
  // Desktop audit logs tag ids with a context-window marker, e.g.
  // "claude-opus-4-8[1m]". Strip it before prefix-matching the rate card.
  const id = model.replace(/\[[^\]]*\]\s*$/, '');
  for (const [prefix, price] of MODEL_OUTPUT_PRICE_PER_M) {
    if (id.startsWith(prefix)) return price;
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

  // Session dirs are flat (UUID.jsonl files directly inside project slug dir).
  // Try CWD-scoped project first so multi-project setups don't bleed across.
  // CWD slug: /home/dave → -home-dave  (replace every / with -)
  function newestInDir(dir) {
    let names;
    try { names = fs.readdirSync(dir); } catch { return null; }
    let best = null;
    for (const name of names) {
      if (!name.endsWith('.jsonl')) continue;
      const p = path.join(dir, name);
      let st;
      try { st = fs.statSync(p); } catch { continue; }
      if (!best || st.mtimeMs > best.mtime) best = { file: p, mtime: st.mtimeMs };
    }
    return best ? best.file : null;
  }

  const cwdSlug = process.cwd().replace(/\//g, '-');
  const cwdResult = newestInDir(path.join(projectsDir, cwdSlug));
  if (cwdResult) return cwdResult;

  // Fall back: global walk (original behaviour, covers non-standard CWDs)
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

// ── Claude desktop app (Electron) session logs ───────────────────────────
// The desktop "host loop" does NOT write the CLI's
//   ~/.claude/projects/<slug>/<uuid>.jsonl
// transcript. It writes a live, per-turn audit log at
//   ~/.config/Claude/local-agent-mode-sessions/<acct>/<space>/local_<id>/audit.jsonl
// Each audit.jsonl holds one or more type=="result" entries carrying
// cumulative usage for that run; parseSession() prefers them.
function desktopSessionsRoot() {
  const base = process.env.CLAUDE_DESKTOP_DIR || path.join(os.homedir(), '.config', 'Claude');
  return path.join(base, 'local-agent-mode-sessions');
}

// Exact current-session match: the host loop runs with cwd inside the session
// dir (typically .../local_<id>/outputs), so the active audit.jsonl is the
// nearest local_<id>/audit.jsonl walking up from cwd. No mtime guessing.
function auditFromCwd() {
  let dir = process.cwd();
  for (let depth = 0; depth < 8 && dir && dir !== path.dirname(dir); depth++) {
    if (path.basename(dir).startsWith('local_')) {
      const candidate = path.join(dir, 'audit.jsonl');
      try { if (fs.statSync(candidate).isFile()) return candidate; } catch {}
    }
    dir = path.dirname(dir);
  }
  return null;
}

// Newest audit.jsonl by mtime across all desktop sessions — a recency signal
// for when cwd doesn't resolve (the session you're prompting in has the most
// recently written audit log).
function newestDesktopAudit(root) {
  let best = null;
  const stack = [root || desktopSessionsRoot()];
  while (stack.length) {
    const p = stack.pop();
    let st;
    try { st = fs.statSync(p); } catch { continue; }
    if (st.isDirectory()) {
      try { for (const c of fs.readdirSync(p)) stack.push(path.join(p, c)); } catch {}
    } else if (path.basename(p) === 'audit.jsonl' && (!best || st.mtimeMs > best.mtime)) {
      best = { file: p, mtime: st.mtimeMs };
    }
  }
  return best ? best.file : null;
}

// Return whichever of two paths has the newer mtime (nulls ignored). Lets the
// CLI vs desktop source be chosen by recency without hardcoding a winner.
function newerFile(a, b) {
  if (!a) return b;
  if (!b) return a;
  const m = f => { try { return fs.statSync(f).mtimeMs; } catch { return -1; } };
  return m(a) >= m(b) ? a : b;
}

function parseSession(filePath) {
  let raw;
  try { raw = fs.readFileSync(filePath, 'utf8'); }
  catch { return { outputTokens: 0, cacheReadTokens: 0, turns: 0, model: null, sessionId: null }; }

  const entries = [];
  for (const line of raw.split('\n')) {
    if (!line.trim()) continue;
    try { entries.push(JSON.parse(line)); } catch {}
  }

  // Desktop audit.jsonl: sum the type=="result" entries. They carry cumulative
  // per-run usage; the interleaved type=="assistant" entries are streaming
  // snapshots (output_tokens≈1) and would badly undercount if summed. A file
  // can hold 1–83 result entries (one per prompt→completion run in the
  // session) — sum them all. Error runs still carry usage, so keep them.
  const results = entries.filter(e => e && e.type === 'result' && e.usage);
  if (results.length) {
    let outputTokens = 0, cacheReadTokens = 0, turns = 0, model = null, sessionId = null;
    for (const e of results) {
      outputTokens    += e.usage.output_tokens           || 0;
      cacheReadTokens += e.usage.cache_read_input_tokens || 0;
      turns           += e.num_turns || 1;
      if (e.session_id) sessionId = e.session_id;
      // No top-level model on result entries — derive from modelUsage keys
      // (e.g. "claude-opus-4-8[1m]"); strip the context-window marker.
      if (!model && e.modelUsage && typeof e.modelUsage === 'object') {
        const k = Object.keys(e.modelUsage)[0];
        if (k) model = k.replace(/\[[^\]]*\]\s*$/, '');
      }
    }
    return { outputTokens, cacheReadTokens, turns, model, sessionId };
  }

  // CLI transcript: sum type=="assistant" message.usage.
  let outputTokens = 0, cacheReadTokens = 0, turns = 0, model = null;
  for (const entry of entries) {
    if (!entry || entry.type !== 'assistant' || !entry.message) continue;
    const usage = entry.message.usage;
    if (!usage) continue;
    outputTokens    += usage.output_tokens           || 0;
    cacheReadTokens += usage.cache_read_input_tokens || 0;
    turns++;
    if (!model && entry.message.model) model = entry.message.model;
  }
  return { outputTokens, cacheReadTokens, turns, model, sessionId: null };
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

// Compute the savings figures we want to log/share for one session snapshot.
function deriveSavings({ outputTokens, mode, model }) {
  const ratio = COMPRESSION[mode] != null ? COMPRESSION[mode] : null;
  const price = priceForModel(model);
  if (ratio === null) return { estSavedTokens: 0, estSavedUsd: 0 };
  const estNormal = Math.round(outputTokens / (1 - ratio));
  const estSavedTokens = estNormal - outputTokens;
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
  return `\nCaveman Stats — Lifetime${window}\n${sep}\n` +
    `Sessions:   ${sessions.toLocaleString()}\n${sep}\n` +
    `Output tokens:         ${outputTokens.toLocaleString()}\n` +
    `Est. tokens saved:     ${estSavedTokens.toLocaleString()}\n` +
    usdLine + sep + '\n';
}

// Single-line tweetable summary. Stays human-friendly when no ratio is known.
function formatShare({ outputTokens, turns, mode, model }) {
  if (turns === 0) {
    return '🪨 caveman armed but no turns yet — caveman.sh';
  }
  const ratio = COMPRESSION[mode] != null ? COMPRESSION[mode] : null;
  const price = priceForModel(model);

  if (ratio !== null) {
    const estSaved = Math.round(outputTokens / (1 - ratio)) - outputTokens;
    let usd = '';
    if (price !== null) {
      const amt = (estSaved / 1_000_000) * price;
      usd = ` (~${formatUsd(amt)})`;
    }
    return `🪨 Saved ${estSaved.toLocaleString()} output tokens${usd} across ${turns} turns this session — caveman.sh`;
  }
  return `🪨 ${turns} turns, ${outputTokens.toLocaleString()} output tokens this session — caveman.sh`;
}

// Pure formatter — separated from main() so tests can pass synthetic inputs.
function formatStats({ outputTokens, cacheReadTokens, turns, mode, model, sessionPath, compressed }) {
  const sep = '──────────────────────────────────';
  const shortPath = sessionPath && sessionPath.length > 45
    ? '...' + sessionPath.slice(-45)
    : (sessionPath || '');

  if (turns === 0) {
    return `\nCaveman Stats\n${sep}\nNo conversation yet — stats available after first response.\n${sep}\n`;
  }

  const ratio = COMPRESSION[mode] != null ? COMPRESSION[mode] : null;
  const price = priceForModel(model);

  let savings;
  let footer = '';
  if (ratio !== null) {
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
    savings = `Est. without caveman:  ${estNormal.toLocaleString()}\n` +
              `Est. tokens saved:     ${estSaved.toLocaleString()} (~${Math.round(ratio * 100)}%)\n` +
              usdLine.replace(/\n$/, '');
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

  // Session source priority:
  //   1. explicit --session-file
  //   2. desktop host-loop audit.jsonl derived from cwd (exact current session)
  //   3. whichever of {CLI projects transcript, newest desktop audit} is newer
  // (3) lets CLI and desktop installs both work: on the CLI the projects/*.jsonl
  // is freshest; on the desktop app the audit.jsonl is.
  const sessionFile = sessionFileArg
    || auditFromCwd()
    || newerFile(findRecentSession(claudeDir), newestDesktopAudit());

  if (!sessionFile) {
    process.stderr.write('caveman-stats: no Claude Code session found.\n');
    process.exit(1);
  }

  const parsed = parseSession(sessionFile);
  const mode = readFlag(path.join(claudeDir, '.caveman-active'));

  // Append a snapshot of this session's totals to the lifetime log. Multiple
  // /caveman-stats calls in one session emit multiple lines for the same
  // session_id; aggregateHistory keeps only the latest per session_id.
  if (parsed.turns > 0) {
    const { estSavedTokens, estSavedUsd } = deriveSavings({ ...parsed, mode });
    // Desktop audit files are all named "audit.jsonl" — basename would collapse
    // every desktop session to one history id. Use the result entry's
    // session_id when present; fall back to the CLI's <uuid>.jsonl basename.
    const sessionId = parsed.sessionId || path.basename(sessionFile, '.jsonl');
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
    process.stdout.write(formatShare({ ...parsed, mode }) + '\n');
  } else {
    const scanDirs = [claudeDir, process.cwd()].filter((d, i, a) => a.indexOf(d) === i);
    const compressed = summarizeCompressed(findCompressedPairs(scanDirs));
    process.stdout.write(formatStats({ ...parsed, mode, sessionPath: sessionFile, compressed }));
  }
}

if (require.main === module) main();

module.exports = {
  formatStats, formatShare, formatHistory, aggregateHistory, parseDuration, deriveSavings,
  parseSession, priceForModel, formatUsd, COMPRESSION, MODEL_OUTPUT_PRICE_PER_M,
  findCompressedPairs, summarizeCompressed, humanizeTokens,
  findRecentSession, auditFromCwd, newestDesktopAudit, newerFile, desktopSessionsRoot,
};
