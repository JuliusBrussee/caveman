#!/usr/bin/env node
// caveman-stats — read the active Claude Code session log, print real token
// usage and mode attribution. A transcript alone cannot establish savings.
//
// Run directly:    node src/hooks/caveman-stats.js  (installed: next to the
//                  other hooks, wherever the installer or plugin put them)
// Inside Claude:   /caveman-stats triggers this via the UserPromptSubmit hook.
// Hook integration passes --session-file <transcript_path> so we always read
// the active session, not whichever JSONL was modified most recently.

const fs = require('fs');
const path = require('path');
const os = require('os');
// caveman-config.js is a mandatory sibling, but an incomplete install leaves
// it absent. A bare top-level require turns that into an uncaught
// MODULE_NOT_FOUND stack trace, which the calling mode-tracker hook can only
// report as an unexplained failure (#848). Print one actionable line instead.
//
// Deliberately inlined rather than extracted into a shared helper: a shared
// loader would itself be one more sibling that can go missing, which is the
// exact failure this guards against.
let cavemanConfig;
let configFailure = null;
try {
  cavemanConfig = require('./caveman-config');
} catch (primary) {
  // The opencode install layout renames the sibling to `.cjs` (its plugin dir
  // is "type": "module"), same fallback caveman-parse.js already does. Gate the
  // retry on the error naming THIS module: a MODULE_NOT_FOUND thrown by a
  // require *inside* a sibling that loaded fine must not be re-reported as
  // "./caveman-config.cjs is missing", blaming a file never meant to exist.
  const message = String((primary && primary.message) || primary);
  if (primary && primary.code === 'MODULE_NOT_FOUND' && message.includes("'./caveman-config'")) {
    try { cavemanConfig = require('./caveman-config.cjs'); } catch (e) { /* report primary */ }
  }
  if (!cavemanConfig) {
    const absent = !fs.existsSync(path.join(__dirname, 'caveman-config.js'))
                && !fs.existsSync(path.join(__dirname, 'caveman-config.cjs'));
    // Distinguish "the sibling is absent" from "the sibling loaded but its own
    // require failed" — naming the wrong cause is worse than no message. Only
    // the first line of error.message: Node appends a multi-line "Require
    // stack:" block, the very noise this guard exists to remove.
    configFailure = absent
      ? 'caveman-config.js is missing from ' + __dirname + ' — the install is incomplete.'
      : 'caveman-config could not load — ' + message.split('\n')[0];
  }
}
// A module that LOADS but exports the wrong shape is the plugin-cache-drift
// case #848 describes; without this check the first use dereferences undefined.
if (cavemanConfig && !(typeof cavemanConfig.readFlag === 'function'
    && typeof cavemanConfig.appendFlag === 'function'
    && typeof cavemanConfig.readHistory === 'function'
    && typeof cavemanConfig.safeWriteFlag === 'function'
    && Array.isArray(cavemanConfig.VALID_MODES))) {
  configFailure = 'caveman-config loaded but is missing expected exports — the install is inconsistent.';
}
if (configFailure) {
  process.stderr.write('caveman-stats: ' + configFailure + '\n'
    + 'Run `/plugin update caveman`, or rerun install.sh for standalone hooks.\n');
  // Unlike the two style hooks, stats has no useful degraded output — every
  // figure it prints comes from the flag/history the config module owns.
  // Exiting non-zero lets the mode-tracker's existing catch substitute its
  // "could not run stats script" message rather than injecting a half-report.
  process.exit(1);
}
const { readFlag, appendFlag, readHistory, safeWriteFlag, VALID_MODES, MODE_LOG_BASENAME } = cavemanConfig;

// Per-session helpers, resolved individually and NOT added to the shape check
// above: a config module from before per-session state still produces correct
// (machine-wide) figures, and hard-failing stats over the newer exports would
// turn a working report into an error. Each stub is the pre-per-session read.
const resolveActiveMode = cavemanConfig.resolveActiveMode
  || ((dir) => { const m = readFlag(path.join(dir, '.caveman-active')); return (!m || m === 'off') ? null : m; });
const validateSessionId = cavemanConfig.validateSessionId || (() => null);
const sessionActivePath = cavemanConfig.sessionActivePath || (() => null);
const legacyFlagPath = cavemanConfig.legacyFlagPath || ((dir) => path.join(dir, '.caveman-active'));

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

const isTokenCount = (value) => Number.isSafeInteger(value) && value >= 0;

// A total contains only reported, valid counts. Availability travels with it
// so a known subtotal cannot become a complete total when history is read.
function totalCounts(counts) {
  let total = 0;
  let known = 0;
  let complete = true;
  for (const { value, availability } of counts) {
    const state = availability === undefined ? 'complete' : availability;
    if (!isTokenCount(value) || !['complete', 'partial'].includes(state)) {
      complete = false;
      continue;
    }
    total += value;
    if (!Number.isSafeInteger(total)) return { value: null, availability: 'unknown' };
    known++;
    if (state !== 'complete') complete = false;
  }
  return known === 0
    ? { value: null, availability: 'unknown' }
    : { value: total, availability: complete ? 'complete' : 'partial' };
}

function parseSession(filePath) {
  // The caller reports read failure separately from an empty conversation.
  const raw = fs.readFileSync(filePath, 'utf8');
  let model = null;
  const messages = []; // one response, including responses whose usage is absent
  // Claude Code writes one JSONL line PER CONTENT BLOCK of an API response
  // (text block, then each tool_use block), all sharing the same message.id +
  // requestId and repeating the same usage object. Summing every line counts
  // the same response's tokens once per block — 1.5-2.1x inflation measured
  // on real tool-heavy sessions. Count each (requestId, message.id) once.
  // Entries without a message.id (synthetic/legacy logs) keep per-line
  // counting — there is no key to dedupe on.
  const seenResponses = new Map();
  for (const line of raw.split('\n')) {
    if (!line.trim()) continue;
    let entry;
    try { entry = JSON.parse(line); } catch { continue; }
    if (!entry || entry.type !== 'assistant' || !entry.message || typeof entry.message !== 'object') continue;
    if (!model && entry.message.model) model = entry.message.model;
    const key = entry.message.id ? (entry.requestId || '') + ':' + entry.message.id : null;
    let response = key ? seenResponses.get(key) : null;
    if (!response) {
      const ts = entry.timestamp ? Date.parse(entry.timestamp) : NaN;
      response = { ts: Number.isFinite(ts) ? ts : null, outputTokens: null, cacheReadTokens: null };
      messages.push(response);
      if (key) seenResponses.set(key, response);
    }
    // Some content blocks omit usage that a later block supplies. Fill each
    // missing counter once, preserving the existing response deduplication.
    const usage = entry.message.usage || {};
    if (response.outputTokens === null && isTokenCount(usage.output_tokens)) {
      response.outputTokens = usage.output_tokens;
    }
    if (response.cacheReadTokens === null && isTokenCount(usage.cache_read_input_tokens)) {
      response.cacheReadTokens = usage.cache_read_input_tokens;
    }
  }
  const output = totalCounts(messages.map(m => ({ value: m.outputTokens })));
  const cacheRead = totalCounts(messages.map(m => ({ value: m.cacheReadTokens })));
  return {
    outputTokens: output.value, outputAvailability: output.availability,
    cacheReadTokens: cacheRead.value, cacheReadAvailability: cacheRead.availability,
    turns: messages.length, model, messages,
  };
}

// Detect *.original.md / *.md pairs left behind by caveman-compress. The
// presence of a *.original.md backup means the *.md sibling is a compressed
// memory file. Compare file bytes only: this does not establish whether an
// agent loaded either file, its token count, or a change in provider usage.
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
  return { count: pairs.length, totalOriginal, totalCompressed, bytesReduced: totalOriginal - totalCompressed };
}

// ── Per-mode attribution (#601) ─────────────────────────────────────────────
// The whole session's tokens must never be credited to whatever mode the flag
// happens to hold at stats time — a mid-session mode change would inflate the
// estimate (verbose tokens counted as compressed) or zero it (caveman tokens
// counted as uncompressed). The mode tracker + SessionStart hook append
// {ts, mode, prev} rows to .caveman-mode-log.jsonl on every actual transition;
// stats joins those timestamps against the session JSONL message timestamps.

// Read + validate the transition log. Returns rows sorted by ts.
//
// When sessionId is given, rows belonging to a DIFFERENT session are dropped.
// Without this the log is a machine-wide interleaving: a mode switch in window
// B lands between two of window A's messages and gets joined onto A's timeline,
// skewing A's savings estimate. Rows with no session_id predate the tagging and
// are kept — for a single-session user they are still the right answer, and
// discarding them would silently downgrade attribution to 'whole-session'.
function readModeLog(logPath, sessionId) {
  const wanted = validateSessionId(sessionId);
  const rows = [];
  for (const line of readHistory(logPath)) {
    let e;
    try { e = JSON.parse(line); } catch { continue; }
    if (!e || typeof e !== 'object' || !Number.isFinite(e.ts)) continue;
    if (wanted && e.session_id != null && e.session_id !== wanted) continue;
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
  if (!isTokenCount(outputTokens)) return { byMode: {}, unknownTokens: 0, basis: 'unavailable' };
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
    if (!isTokenCount(m.outputTokens)) continue;
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
// (kept for formatStats callers without a transcript).
function wholeSessionAttribution(mode, outputTokens) {
  if (!isTokenCount(outputTokens)) return { byMode: {}, unknownTokens: 0, basis: 'unavailable' };
  return { byMode: { [mode || 'none']: outputTokens || 0 }, unknownTokens: 0, basis: 'whole-session' };
}

// Pin grouping to en-US so token counts do not depend on the host locale.
const fmt = (n) => n.toLocaleString('en-US');
const SAVINGS_UNKNOWN = 'Savings: unknown — no measured comparison for this session.';

function formatCount(value, availability) {
  const count = totalCounts([{ value, availability }]);
  if (count.availability === 'unknown') return 'unknown (usage unavailable)';
  return fmt(count.value) + (count.availability === 'partial' ? ' known (partial; total unknown)' : '');
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
// time window. Preserve incomplete snapshots instead of claiming full totals.
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
  // Legacy est_saved_* fields came from an unsupported fixed ratio. Keep
  // the source history intact, but never treat those fields as measurements.
  const output = totalCounts([...latestPerSession.values()].map(e => ({
    value: e.output_tokens, availability: e.output_tokens_availability,
  })));
  return { sessions: latestPerSession.size, outputTokens: output.value, outputAvailability: output.availability };
}

function formatHistory({ sessions, outputTokens, outputAvailability, since }) {
  const sep = '──────────────────────────────────';
  const window = since ? ` (last ${since})` : '';
  if (sessions === 0) {
    return `\nCaveman Stats — Lifetime${window}\n${sep}\nNo sessions logged yet — run /caveman-stats inside any session to start tracking.\n${sep}\n`;
  }
  return `\nCaveman Stats — Lifetime${window}\n${sep}\n` +
    `Sessions:   ${fmt(sessions)}\n${sep}\n` +
    `Output tokens:         ${formatCount(outputTokens, outputAvailability)}\n` +
    'Savings: unknown — historical estimates are not verified measurements.\n' + sep + '\n';
}

// Share only observed transcript usage, with the missing comparison explicit.
function formatShare({ outputTokens, outputAvailability, turns }) {
  if (turns === 0) {
    return '🪨 No turns yet; savings unknown — caveman.sh';
  }
  const count = totalCounts([{ value: outputTokens, availability: outputAvailability }]);
  const usage = count.availability === 'unknown' ? 'output tokens unknown (usage unavailable)'
    : `${fmt(count.value)}${count.availability === 'partial' ? ' known' : ''} output tokens` +
      (count.availability === 'partial' ? ' (partial; total unknown)' : '');
  return `🪨 ${turns} turn${turns === 1 ? '' : 's'}, ${usage} this session; savings unknown — caveman.sh`;
}

// Pure formatter — separated from main() so tests can pass synthetic inputs.
// `attribution` (from attributeByMode, #601) splits output tokens per mode;
// when omitted, the current mode is assumed for the whole session.
function formatStats({ outputTokens, outputAvailability, cacheReadTokens, cacheReadAvailability, turns, mode, sessionPath, compressed, attribution }) {
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

  let modeDetails;
  if (!uniform) {
    const lines = [attr.basis === 'flag-mtime'
      ? 'Mode was set mid-session — only output after the change is attributed:'
      : 'Mode changed mid-session — output attributed per mode:'];
    for (const key of activeKeys) {
      const label = key === 'none' ? 'caveman off' : key;
      lines.push(`  ${label}: ${fmt(attr.byMode[key])} tokens`);
    }
    if (attr.unknownTokens > 0) {
      lines.push(`  unattributed: ${fmt(attr.unknownTokens)} tokens (mode unknown)`);
    }
    modeDetails = lines.join('\n');
  } else {
    modeDetails = `Mode: ${mode && mode !== 'off' ? mode : 'caveman off'}`;
    if (attr.basis === 'whole-session') {
      modeDetails += ' (current mode; no transition log)';
    }
  }
  if (outputAvailability === 'partial') modeDetails += '\nMode attribution covers known output tokens only.';

  let memoryLine = '';
  if (compressed && compressed.count > 0) {
    memoryLine = `${sep}\nMemory file sizes:     ${compressed.count} pair${compressed.count === 1 ? '' : 's'}, ` +
      `${fmt(compressed.totalOriginal)} original bytes → ${fmt(compressed.totalCompressed)} current bytes ` +
      `(${fmt(compressed.bytesReduced)} fewer bytes)\n` +
      'File sizes do not measure provider token or billing savings.\n';
  }

  return `\nCaveman Stats\n${sep}\n` +
    (shortPath ? `Session:  ${shortPath}\n` : '') +
    `Turns:    ${turns}\n${sep}\n` +
    `Output tokens:         ${formatCount(outputTokens, outputAvailability)}\n` +
    `Cache-read tokens:     ${formatCount(cacheReadTokens, cacheReadAvailability)}\n${sep}\n` +
    `${modeDetails}\n${SAVINGS_UNKNOWN}\n` +
    memoryLine;
}

function main() {
  const args = process.argv.slice(2);
  const hostIdx = args.indexOf('--host');
  const host = hostIdx !== -1 ? args[hostIdx + 1]
    : process.env.GEMINI_CLI === '1' ? 'gemini' : 'claude';
  if (host === 'gemini') {
    // Gemini CLI's ShellExecutionService identifies child commands with
    // GEMINI_CLI=1. Its statistics live in the host session, not Claude JSONL.
    // Stop before reading or updating another host's history/flags (#403).
    process.stdout.write('Gemini CLI: use /stats model for current session token usage, or /stats session for session statistics.\nCaveman savings: unknown. Claude Code transcripts are not Gemini usage.\n');
    return;
  }
  if (host !== 'claude') {
    process.stderr.write('caveman-stats: --host must be claude or gemini.\n');
    process.exitCode = 2;
    return;
  }
  const i = args.indexOf('--session-file');
  const sessionFileArg = i !== -1 ? args[i + 1] : null;
  const sessionIdIdx = args.indexOf('--session-id');
  const sessionIdArg = sessionIdIdx !== -1 ? args[sessionIdIdx + 1] : null;
  const share = args.includes('--share');
  const all = args.includes('--all');
  const sinceIdx = args.indexOf('--since');
  const sinceArg = sinceIdx !== -1 ? args[sinceIdx + 1] : null;

  const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
  const historyPath = path.join(claudeDir, '.caveman-history.jsonl');

  // Retire the old numeric badge even for lifetime, empty, or failed reads.
  // safeWriteFlag refuses symlinks; original history rows remain untouched.
  safeWriteFlag(path.join(claudeDir, '.caveman-statusline-suffix'), '');

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

  let parsed;
  try { parsed = parseSession(sessionFile); }
  catch (error) {
    process.stderr.write(`caveman-stats: could not read Claude Code session ${sessionFile} (${error.code || 'read failed'}). Usage unavailable.\n`);
    process.exitCode = 1;
    return;
  }

  // Session id: the hook forwards --session-id from the UserPromptSubmit
  // payload. Falling back to the transcript filename is not a guess — Claude
  // Code names transcripts by session id, which is why the lifetime history has
  // always keyed on it.
  const sessionId = validateSessionId(sessionIdArg)
    || validateSessionId(path.basename(sessionFile, '.jsonl'));

  // Read whichever layer holds this session's state, and take the mtime from
  // that same file so the 'flag-mtime' attribution fallback measures the right
  // thing. resolveActiveMode collapses a durable 'off' to null, matching the
  // pre-existing "no flag file means no mode" contract the formatters expect.
  const sessionPath = sessionActivePath(claudeDir, sessionId);
  const flagPath = (sessionPath && fs.existsSync(sessionPath))
    ? sessionPath
    : legacyFlagPath(claudeDir);
  const mode = resolveActiveMode(claudeDir, sessionId);

  // #601: attribute tokens to the mode active when each message happened,
  // via the transition log the hooks maintain (fallbacks documented on
  // attributeByMode). Never credit the whole session to the current flag.
  let flagMtimeMs = null;
  try { flagMtimeMs = fs.statSync(flagPath).mtimeMs; } catch (e) {}
  const modeLog = readModeLog(path.join(claudeDir, MODE_LOG_BASENAME), sessionId);
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
    appendFlag(historyPath, JSON.stringify({
      ts: Date.now(),
      session_id: sessionId || path.basename(sessionFile, '.jsonl'),
      mode: mode || null,
      model: parsed.model || null,
      output_tokens: parsed.outputTokens,
      output_tokens_availability: parsed.outputAvailability,
      turns: parsed.turns,
      cache_read_input_tokens: parsed.cacheReadTokens,
      cache_read_input_tokens_availability: parsed.cacheReadAvailability,
      output_tokens_by_mode: attribution.byMode,
      unattributed_output_tokens: attribution.unknownTokens,
      mode_attribution: attribution.basis,
    }));
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
  formatStats, formatShare, formatHistory, aggregateHistory, parseDuration,
  parseSession, findCompressedPairs, summarizeCompressed, readModeLog, attributeByMode,
};
