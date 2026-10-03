#!/usr/bin/env node
// caveman — session/day output-token budget intensity ladder
//
// Optional control surface. No budget configured → today's behavior is
// unchanged. An explicit `/caveman <level>` pins until `/caveman release`.
//
// Exports: readBudgetConfig, usedOutputTokens, resolveLadderMode,
// writeBudgetBadge, readHold, writeHold, clearHold — plus the transcript
// helpers extracted from caveman-stats.js so both callers share one parser.

const fs = require('fs');
const path = require('path');

let cavemanConfig;
try {
  cavemanConfig = require('./caveman-config');
} catch (e) {
  cavemanConfig = require('./caveman-config.cjs');
}
const {
  getDefaultMode, getConfigPath, findRepoConfigPath, VALID_MODES,
  safeWriteFlag, readFlag,
} = cavemanConfig;

const INDEPENDENT_MODES = ['commit', 'review', 'compress'];
const PROSE_MODES = VALID_MODES.filter((m) => m !== 'off' && !INDEPENDENT_MODES.includes(m));

const HOLD_BASENAME = '.caveman-budget-hold';
const OVERRIDE_BASENAME = '.caveman-budget-override';
const BADGE_BASENAME = '.caveman-statusline-budget';
const MAX_SESSION_BYTES = 8 * 1024 * 1024;
const MAX_DAY_FILES = 200;
const MAX_OVERRIDE_BYTES = 16;
const MAX_PARSE_MS = 50;
const BADGE_RE = /^[0-9.]+[km]? left$/;

const DEFAULT_LADDER = [
  { remainPct: 100, mode: 'lite' },
  { remainPct: 50, mode: 'full' },
  { remainPct: 20, mode: 'ultra' },
];

function isProseMode(mode) {
  return typeof mode === 'string' && PROSE_MODES.includes(mode);
}

function parsePositiveInt(raw, { trim } = {}) {
  if (typeof raw === 'number') {
    return Number.isInteger(raw) && raw > 0 ? raw : null;
  }
  if (typeof raw !== 'string') return null;
  const s = trim ? raw.trim() : raw;
  if (!/^[0-9]+$/.test(s)) return null;
  const n = Number(s);
  if (!Number.isInteger(n) || n <= 0 || !Number.isFinite(n)) return null;
  return n;
}

function synthesizeLadder(defaultMode) {
  const top = isProseMode(defaultMode) ? defaultMode : 'lite';
  if (top === 'lite') return DEFAULT_LADDER.map((r) => ({ ...r }));
  return [
    { remainPct: 100, mode: top },
    { remainPct: 50, mode: 'full' },
    { remainPct: 20, mode: 'ultra' },
  ];
}

function validateBudget(raw, defaultMode) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const outputTokens = parsePositiveInt(raw.outputTokens);
  if (outputTokens == null) return null;
  const window = raw.window == null ? 'session' : raw.window;
  if (window !== 'session' && window !== 'day') return null;
  if (!Array.isArray(raw.ladder) || raw.ladder.length === 0) return null;
  const seen = new Set();
  const ladder = [];
  for (const rung of raw.ladder) {
    if (!rung || typeof rung !== 'object') return null;
    const remainPct = typeof rung.remainPct === 'number' ? rung.remainPct : Number(rung.remainPct);
    if (!Number.isFinite(remainPct) || remainPct <= 0 || remainPct > 100) return null;
    if (seen.has(remainPct)) return null;
    seen.add(remainPct);
    const mode = typeof rung.mode === 'string' ? rung.mode.toLowerCase() : '';
    if (!isProseMode(mode)) return null;
    ladder.push({ remainPct, mode });
  }
  return { window, outputTokens, ladder };
}

function readBudgetFromFile(configPath, defaultMode) {
  try {
    const st = fs.lstatSync(configPath);
    if (st.isSymbolicLink() || !st.isFile()) return undefined;
    const config = JSON.parse(fs.readFileSync(configPath, 'utf8'));
    if (!config || typeof config !== 'object' || !Object.prototype.hasOwnProperty.call(config, 'budget')) {
      return undefined;
    }
    return validateBudget(config.budget, defaultMode);
  } catch (e) {
    return undefined;
  }
}

function envBudget(defaultMode) {
  const raw = process.env.CAVEMAN_OUTPUT_BUDGET;
  if (raw === undefined) return undefined;
  const outputTokens = parsePositiveInt(raw);
  if (outputTokens == null) return undefined; // non-integer env is ignored
  const windowRaw = process.env.CAVEMAN_BUDGET_WINDOW;
  const window = windowRaw === 'day' || windowRaw === 'session' ? windowRaw : 'session';
  return { window, outputTokens, ladder: synthesizeLadder(defaultMode) };
}

// Resolution order matches getDefaultMode(): env → repo-local file → user
// config. Stop at the first SOURCE that contains a valid budget. A file with
// a `budget` key that fails validation is fail-closed (null), not a fall-through.
function readBudgetConfig(startDir) {
  try {
    const defaultMode = getDefaultMode(startDir);
    const fromEnv = envBudget(defaultMode);
    if (fromEnv) return fromEnv;

    const repoPath = findRepoConfigPath(startDir);
    if (repoPath) {
      const fromRepo = readBudgetFromFile(repoPath, defaultMode);
      if (fromRepo !== undefined) return fromRepo; // valid or null
    }

    const fromUser = readBudgetFromFile(getConfigPath(), defaultMode);
    if (fromUser !== undefined) return fromUser;
  } catch (e) {
    return null;
  }
  return null;
}

function readOverride(claudeDir) {
  try {
    const flagPath = path.join(claudeDir, OVERRIDE_BASENAME);
    let st;
    try { st = fs.lstatSync(flagPath); } catch (e) { return null; }
    if (st.isSymbolicLink() || !st.isFile()) return null;
    if (st.size > MAX_OVERRIDE_BYTES) return null;
    const O_NOFOLLOW = typeof fs.constants.O_NOFOLLOW === 'number' ? fs.constants.O_NOFOLLOW : 0;
    const flags = fs.constants.O_RDONLY | O_NOFOLLOW;
    let fd;
    let out;
    try {
      fd = fs.openSync(flagPath, flags);
      const buf = Buffer.alloc(MAX_OVERRIDE_BYTES);
      const n = fs.readSync(fd, buf, 0, MAX_OVERRIDE_BYTES, 0);
      out = buf.slice(0, n).toString('utf8');
    } finally {
      if (fd !== undefined) fs.closeSync(fd);
    }
    const raw = out.trim();
    if (!/^[0-9]+$/.test(raw)) return null;
    return parsePositiveInt(raw);
  } catch (e) {
    return null;
  }
}

function writeOverride(claudeDir, outputTokens) {
  const n = parsePositiveInt(outputTokens);
  if (n == null) return;
  const digits = String(n);
  if (digits.length > MAX_OVERRIDE_BYTES) return;
  safeWriteFlag(path.join(claudeDir, OVERRIDE_BASENAME), digits);
}

function clearOverride(claudeDir) {
  try { fs.unlinkSync(path.join(claudeDir, OVERRIDE_BASENAME)); } catch (e) { /* best-effort */ }
}

function effectiveBudget(startDir, claudeDir) {
  const base = readBudgetConfig(startDir);
  const override = claudeDir ? readOverride(claudeDir) : null;
  if (override == null) return base;
  if (!base) {
    return {
      window: 'session',
      outputTokens: override,
      ladder: synthesizeLadder(getDefaultMode(startDir)),
    };
  }
  return { ...base, outputTokens: override };
}

function startOfLocalDay(nowMs) {
  const d = new Date(nowMs == null ? Date.now() : nowMs);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

function walkJsonl(claudeDir, visit) {
  const projectsDir = path.join(claudeDir, 'projects');
  let entries;
  try { entries = fs.readdirSync(projectsDir, { withFileTypes: true }); }
  catch { return; }
  const stack = entries.map((e) => path.join(projectsDir, e.name));
  while (stack.length) {
    const p = stack.pop();
    let st;
    try { st = fs.statSync(p); } catch { continue; }
    if (st.isDirectory()) {
      try {
        for (const child of fs.readdirSync(p)) stack.push(path.join(p, child));
      } catch { /* skip unreadable dir */ }
    } else if (p.endsWith('.jsonl')) {
      if (visit(p, st) === false) return;
    }
  }
}

function findRecentSession(claudeDir) {
  let best = null;
  walkJsonl(claudeDir, (file, st) => {
    if (!best || st.mtimeMs > best.mtime) best = { file, mtime: st.mtimeMs };
  });
  return best ? best.file : null;
}

function emptySession() {
  return { outputTokens: 0, cacheReadTokens: 0, turns: 0, model: null, messages: [] };
}

function parseSession(filePath) {
  let raw;
  try { raw = fs.readFileSync(filePath, 'utf8'); }
  catch { return emptySession(); }

  let outputTokens = 0;
  let cacheReadTokens = 0;
  let turns = 0;
  let model = null;
  const messages = [];
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

function parseSessionCapped(filePath) {
  try {
    const st = fs.lstatSync(filePath);
    if (st.isSymbolicLink() || !st.isFile()) return emptySession();
    if (st.size > MAX_SESSION_BYTES) return emptySession();
  } catch {
    return emptySession();
  }
  return parseSession(filePath);
}

function usedOutputTokens({ window, transcriptPath, claudeDir } = {}) {
  try {
    if (window === 'day') {
      if (!claudeDir) return 0;
      const midnight = startOfLocalDay();
      let used = 0;
      let seen = 0;
      walkJsonl(claudeDir, (file, st) => {
        if (seen >= MAX_DAY_FILES) return false;
        seen++;
        if (st.mtimeMs < midnight) return;
        if (st.size > MAX_SESSION_BYTES) return;
        used += parseSessionCapped(file).outputTokens;
      });
      return used;
    }
    // session window (default)
    const file = transcriptPath || (claudeDir ? findRecentSession(claudeDir) : null);
    if (!file) return 0;
    return parseSessionCapped(file).outputTokens;
  } catch (e) {
    return 0;
  }
}

function remainingPct(budget, used) {
  const ceiling = budget && budget.outputTokens;
  if (!ceiling || !Number.isFinite(ceiling) || ceiling <= 0) return null;
  const u = Number(used);
  if (!Number.isFinite(u)) return null;
  return Math.max(0, (ceiling - u) / ceiling * 100);
}

function resolveLadderMode(budget, used) {
  if (!budget || !Array.isArray(budget.ladder) || budget.ladder.length === 0) return null;
  const pct = remainingPct(budget, used);
  if (pct == null || !Number.isFinite(pct)) return null;
  const matching = budget.ladder.filter((r) => pct <= r.remainPct);
  if (matching.length === 0) return null;
  let best = matching[0];
  for (const r of matching) {
    if (r.remainPct < best.remainPct) best = r;
  }
  return isProseMode(best.mode) ? best.mode : null;
}

function nextRung(budget, used) {
  if (!budget || !Array.isArray(budget.ladder)) return null;
  const pct = remainingPct(budget, used);
  if (pct == null) return null;
  const active = resolveLadderMode(budget, used);
  const lower = budget.ladder
    .filter((r) => r.remainPct < pct)
    .sort((a, b) => b.remainPct - a.remainPct);
  if (lower.length === 0) return null;
  if (active && lower[0].mode === active && lower.length === 1) return null;
  return lower[0];
}

function formatBudgetLines({ budget, used, hold }) {
  if (!budget) return '';
  const u = Number.isFinite(used) ? used : 0;
  const pct = remainingPct(budget, u);
  const pctStr = pct == null ? '?' : (Math.round(pct * 10) / 10).toString();
  const active = resolveLadderMode(budget, u);
  const next = nextRung(budget, u);
  const holdStr = hold && isProseMode(hold) ? hold : 'off';
  const nextStr = next
    ? `next: ${next.mode} at ${next.remainPct}% remaining`
    : 'no further rung';
  return (
    `Budget ceiling:         ${budget.outputTokens.toLocaleString()} output tokens\n` +
    `Budget window:          ${budget.window}\n` +
    `Budget used:            ${u.toLocaleString()} (${pctStr}% remaining)\n` +
    `Active rung:            ${active || '—'} (${nextStr})\n` +
    `Hold:                   ${holdStr}`
  );
}

function sanitizeBudgetBadge(s) {
  const raw = String(s);
  if (/[\x00-\x1F\x7F]/.test(raw)) return '';
  if (raw.length > 16) return '';
  if (!BADGE_RE.test(raw)) return '';
  return raw;
}

function formatBudgetBadge(remainingTokens) {
  const n = Number(remainingTokens);
  if (!Number.isFinite(n)) return '';
  const remaining = Math.max(0, n);
  let s;
  if (remaining >= 1e6) s = (remaining / 1e6).toFixed(1) + 'm left';
  else if (remaining >= 1e3) s = (remaining / 1e3).toFixed(1) + 'k left';
  else s = Math.round(remaining) + ' left';
  return sanitizeBudgetBadge(s);
}

function writeBudgetBadge(claudeDir, remainingTokens) {
  try {
    const text = formatBudgetBadge(remainingTokens);
    if (!text) return;
    safeWriteFlag(path.join(claudeDir, BADGE_BASENAME), text);
  } catch (e) {
    // Silent fail — statusline is best-effort
  }
}

function holdPath(claudeDir) {
  return path.join(claudeDir, HOLD_BASENAME);
}

function readHold(claudeDir) {
  return readFlag(holdPath(claudeDir), PROSE_MODES);
}

function writeHold(claudeDir, mode) {
  if (!isProseMode(mode)) return;
  safeWriteFlag(holdPath(claudeDir), mode);
}

function clearHold(claudeDir) {
  try { fs.unlinkSync(holdPath(claudeDir)); } catch (e) { /* best-effort */ }
}

// Timed ladder resolve for the UserPromptSubmit 5s budget. Fail open (null)
// if transcript work exceeds MAX_PARSE_MS — leave the current flag alone.
function resolveTimedLadder(budget, opts) {
  if (!budget) return null;
  const t0 = Date.now();
  const used = usedOutputTokens(opts);
  if (Date.now() - t0 > MAX_PARSE_MS) return null;
  return resolveLadderMode(budget, used);
}

function remainingTokens(budget, used) {
  if (!budget || !budget.outputTokens) return 0;
  const u = Number.isFinite(used) ? used : 0;
  return Math.max(0, budget.outputTokens - u);
}

module.exports = {
  readBudgetConfig,
  usedOutputTokens,
  resolveLadderMode,
  writeBudgetBadge,
  readHold,
  writeHold,
  clearHold,
  parseSession,
  findRecentSession,
  effectiveBudget,
  readOverride,
  writeOverride,
  clearOverride,
  formatBudgetLines,
  formatBudgetBadge,
  sanitizeBudgetBadge,
  resolveTimedLadder,
  remainingPct,
  remainingTokens,
  nextRung,
  PROSE_MODES,
  DEFAULT_LADDER,
  HOLD_BASENAME,
  OVERRIDE_BASENAME,
  BADGE_BASENAME,
  MAX_SESSION_BYTES,
  MAX_DAY_FILES,
  MAX_PARSE_MS,
  MAX_OVERRIDE_BYTES,
};
