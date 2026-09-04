#!/usr/bin/env node
// caveman — Claude Code SessionStart activation hook
//
// Runs on every session start:
//   1. Resolves THIS session's mode and persists it (statusline reads it)
//   2. Emits caveman ruleset as hidden SessionStart context
//   3. Detects missing statusline config and emits setup nudge
//
// Mode state is per session, not per machine — see the "Per-session mode state"
// block in caveman-config.js. The payload's session_id scopes every read and
// write below; an absent or malformed one degrades to the old machine-wide flag.

const fs = require('fs');
const path = require('path');
const os = require('os');
// caveman-config.js is a mandatory sibling, but an incomplete install (plugin
// cache drift, a copy list that missed a file) leaves it absent. A bare
// top-level require turns that into an uncaught MODULE_NOT_FOUND on EVERY
// session start, which Claude Code surfaces only as an opaque loader stack
// trace (#848). Resolve it defensively and degrade instead.
//
// Deliberately inlined here rather than extracted into a shared helper: a
// shared loader would itself be one more sibling that can go missing, which
// is the exact failure this guards against.
function reportDegraded(name, detail) {
  process.stderr.write('caveman: ' + detail + '\n'
    + 'Run `/plugin update caveman`, or rerun install.sh for standalone hooks. '
    + 'Continuing with reduced functionality.\n');
}

function requireSibling(name, isUsable) {
  let mod;
  try {
    mod = require('./' + name);
  } catch (primary) {
    // The opencode install layout renames the sibling to `.cjs` (its plugin
    // dir is "type": "module"), same fallback caveman-parse.js already does.
    // Gate the retry on the error naming THIS module: a MODULE_NOT_FOUND
    // thrown by a require *inside* a sibling that loaded fine must not be
    // re-reported as "./<name>.cjs is missing", which blames a file that was
    // never meant to exist.
    const message = String((primary && primary.message) || primary);
    if (primary && primary.code === 'MODULE_NOT_FOUND' && message.includes("'./" + name + "'")) {
      try { return require('./' + name + '.cjs'); } catch (e) { /* report primary */ }
    }
    const absent = !fs.existsSync(path.join(__dirname, name + '.js'))
                && !fs.existsSync(path.join(__dirname, name + '.cjs'));
    // Distinguish "the sibling is absent" from "the sibling loaded but its
    // own require failed" — naming the wrong cause is worse than no message.
    // Only the first line of error.message: Node appends a multi-line
    // "Require stack:" block, which is the noise this guard exists to remove.
    reportDegraded(name, absent
      ? name + '.js is missing from ' + __dirname + ' — the install is incomplete.'
      : name + ' could not load — ' + message.split('\n')[0]);
    return null;
  }
  // A module that LOADS but exports the wrong shape is the plugin-cache-drift
  // case #848 actually describes: a stale sibling from another version. Without
  // this check the destructure below succeeds and the first use dereferences
  // undefined, producing exactly the raw top-level stack trace and exit 1 this
  // guard exists to remove. Validate the shape, not just the throw.
  if (!isUsable(mod)) {
    reportDegraded(name, name + ' loaded but is missing expected exports — the install is inconsistent.');
    return null;
  }
  return mod;
}

// Hand-copy of caveman-config.js VALID_MODES, used only when that module is
// unavailable. tests/test_hook_missing_sibling.js asserts the two stay equal.
const FALLBACK_VALID_MODES = [
  'off', 'lite', 'full', 'ultra',
  'wenyan-lite', 'wenyan', 'wenyan-full', 'wenyan-ultra',
  'commit', 'review', 'compress'
];

// Minimal stand-in for caveman-config.getDefaultMode. It must mirror the real
// resolution order rather than read only the env var: a degrade that ignores a
// checked-in `.caveman.json` or a user config saying `defaultMode: "off"` does
// not degrade toward the user's intent, it INVERTS it — a team that opted out
// would get caveman force-injected the moment one file goes missing. Reads
// only; refuses symlinked config files, symmetric with safeWriteFlag.
function fallbackReadMode(file) {
  try {
    if (!fs.lstatSync(file).isFile()) return null;
    const mode = JSON.parse(fs.readFileSync(file, 'utf8')).defaultMode;
    if (typeof mode === 'string' && FALLBACK_VALID_MODES.includes(mode.toLowerCase())) {
      return mode.toLowerCase();
    }
  } catch (e) { /* absent, unreadable, or malformed → next source */ }
  return null;
}

function fallbackUserConfigPath() {
  if (process.env.XDG_CONFIG_HOME) return path.join(process.env.XDG_CONFIG_HOME, 'caveman', 'config.json');
  if (process.platform === 'win32') {
    return path.join(process.env.APPDATA || path.join(os.homedir(), 'AppData', 'Roaming'), 'caveman', 'config.json');
  }
  return path.join(os.homedir(), '.config', 'caveman', 'config.json');
}

function fallbackGetDefaultMode(startDir) {
  // 1. Environment variable. No .trim() — the real resolver does not trim, and
  //    a degraded path that accepts " ultra" where the intact one rejects it is
  //    drift in a whitelist.
  const envMode = process.env.CAVEMAN_DEFAULT_MODE;
  if (envMode && FALLBACK_VALID_MODES.includes(envMode.toLowerCase())) return envMode.toLowerCase();
  // 2. Repo-local config, walking up. Bounded at 64 like findRepoConfigPath.
  try {
    let dir = path.resolve(startDir || process.cwd());
    for (let i = 0; i < 64; i++) {
      for (const rel of ['.caveman/config.json', '.caveman.json']) {
        const mode = fallbackReadMode(path.join(dir, rel));
        if (mode) return mode;
      }
      const parent = path.dirname(dir);
      if (parent === dir) break;
      dir = parent;
    }
  } catch (e) { /* fall through to user config */ }
  // 3. User config, then 4. the built-in default.
  return fallbackReadMode(fallbackUserConfigPath()) || 'full';
}

// Degraded stubs keep the rest of this hook working when the config module is
// unusable: the session still gets its ruleset (read from SKILL.md, which does
// not depend on the config module) and only flag persistence is lost — no flag
// write, no mode log, readFlag() reports nothing active.
const cavemanConfig = requireSibling('caveman-config', (m) =>
  m && typeof m.getDefaultMode === 'function' && typeof m.safeWriteFlag === 'function'
    && typeof m.recordModeChange === 'function' && typeof m.readFlag === 'function'
    && Array.isArray(m.VALID_MODES));

const { getDefaultMode, safeWriteFlag, recordModeChange, readFlag, VALID_MODES } = cavemanConfig || {
  getDefaultMode: fallbackGetDefaultMode,
  safeWriteFlag: () => {},
  recordModeChange: () => {},
  readFlag: () => null,
  VALID_MODES: FALLBACK_VALID_MODES,
};

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.caveman-active');
const settingsPath = path.join(claudeDir, 'settings.json');

function removeFlag(target) {
  try {
    fs.unlinkSync(target);
  } catch (error) {
    if (process.env.CAVEMAN_DEBUG === '1' && error.code !== 'ENOENT') {
      console.error(`caveman: failed to remove flag ${target}: ${error.message}`);
    }
  }
}

// The per-session helpers are resolved INDIVIDUALLY rather than folded into the
// requireSibling shape check above. A caveman-config.js from before per-session
// state loads fine and exports everything that check demands, so failing the
// whole module over the newer exports would trade "machine-wide mode, as it
// always worked" for "no flag write at all" — a strictly worse degrade on the
// exact plugin-cache-drift scenario #848 is about. Each stub below reproduces
// the pre-per-session behavior instead.
const cfg = cavemanConfig || {};
const validateSessionId = cfg.validateSessionId || (() => null);
const gcSessionStore = cfg.gcSessionStore || (() => 0);
// Literal read of THIS session's state, 'off' included. Degrades to the legacy
// flag, which is exactly what the pre-per-session hook read.
const readSessionModeRaw = cfg.readSessionModeRaw || (() => readFlag(flagPath));
const writeSessionMode = cfg.writeSessionMode || ((dir, sid, modeOrNull) => {
  if (!modeOrNull || modeOrNull === 'off') removeFlag(flagPath);
  else safeWriteFlag(flagPath, modeOrNull);
});
const legacyFlagPath = cfg.legacyFlagPath || (() => flagPath);

// Apply per-agent model overrides from env vars before emitting rules.
// Best-effort: any error is swallowed so SessionStart is never blocked.
try {
  const { applyOverrides, resolvePluginRoot } = require('./cavecrew-model-overrides');
  applyOverrides(resolvePluginRoot(__dirname));
} catch (e) {}

// SessionStart re-fires mid-conversation (resume, /clear, context compaction),
// not just at true session start. Re-firing must not clobber a mode the user
// switched to mid-session (#691): branch on the hook payload's `source` field —
// only a real `startup` (or an explicit /clear, see RESET_SOURCES) resets to the
// configured default; resume/compact/fork preserve this session's stored mode.
//
// With per-session storage the branch also has to preserve a durable `off`.
// #691 could not: it read the legacy flag, where "off" is spelled "no file", so
// a deactivated session found nothing stored and fell straight back to
// getDefaultMode() — the "stop caveman, then /compact" hole. The continuation
// branch below therefore reads the LITERAL stored value, not the collapsed one.
// Payload arrival is EVENT-DRIVEN, and activation runs on the first COMPLETE
// JSON object rather than at EOF. The host writes one object and closes, but
// under the Windows pipe implementation that close can lag arbitrarily
// (#729/#833). A synchronous `readFileSync(0)` blocks inside the read syscall
// until EOF — no deadline can interrupt it — so a lagging close spent this
// hook's entire 5s budget and the host killed it before the flag was written or
// the ruleset emitted. caveman-mode-tracker.js was fixed this way; its sibling
// was not, and SessionStart is the one that actually has work to do.
//
// A watchdog covers the case where the payload never completes at all: activate
// well inside the budget instead of forfeiting the session. It must NOT assume
// `startup` — that is the one source that resets the mode, so a slow payload on
// a `compact`/`resume` event would silently drop a user's mid-session `ultra`
// back to the default (#691 through the timeout door). An unknown source
// preserves a valid existing flag. The deadline sits well below the host's 5s
// budget but far enough above a cold Windows/AV start to be reached rarely.
const PAYLOAD_WATCHDOG_MS = 2000;

// Sources that re-derive the configured default instead of reading what this
// session already stored.
//
// `startup` is a genuinely new session. `clear` is here — unlike in #691's
// flag-only world — because /clear is an explicit user reset of the
// conversation, and per-session storage makes that distinction cheap: nothing
// else in the session survives /clear, so neither should a "stop caveman" from
// before it. Everything else (compact, resume, fork, an unrecognized source,
// and the watchdog's 'unknown') reads instead of re-deriving.
const RESET_SOURCES = new Set(['startup', 'clear']);

function activate(payload, timedOut) {
  // Unknown, not startup: we never saw the payload, so we cannot claim to know
  // what kind of session event this was — and 'unknown' must not reset, or a
  // slow payload on a compact would drop a mid-session ultra (#691 through the
  // timeout door) and re-arm a session the user turned off.
  let source = timedOut ? 'unknown' : 'startup';
  // The session's cwd, which is not necessarily this hook process's cwd. The
  // repo-local config walk must start there or a checked-in .caveman.json
  // (including `defaultMode: "off"`, a project opting out) is missed — the same
  // #634 bug already fixed in caveman-mode-tracker.js.
  let sessionCwd;
  // Scopes every mode read/write to this window. null when absent or malformed,
  // in which case the config helpers fall back to the legacy machine-wide flag.
  let sessionId = null;
  try {
    if (payload) {
      const data = JSON.parse(payload);
      if (data && typeof data.source === 'string') source = data.source;
      if (data && typeof data.cwd === 'string') sessionCwd = data.cwd;
      if (data) sessionId = validateSessionId(data.session_id);
    }
  } catch (e) { /* no/bad stdin → treat as startup */ }
  run(source, sessionCwd, sessionId);
}

if (process.stdin.isTTY) {
  // Manual run — no payload is coming.
  activate('');
} else {
  let input = '';
  let done = false;
  const finish = (timedOut) => {
    if (done) return;
    done = true;
    clearTimeout(watchdog);
    // Attaching a 'data' listener puts the stdin handle into flowing mode and
    // REFERENCES it, so pause() alone leaves the event loop alive and the
    // process never exits while the host holds the write end open — which is
    // exactly the lagging-close case this rewrite exists to survive. unref()
    // drops the handle from the loop's ref count without closing the fd, so we
    // exit as soon as stdout has flushed.
    try { process.stdin.pause(); } catch (e) {}
    try { process.stdin.unref(); } catch (e) {}
    activate(input, timedOut === true);
  };
  const watchdog = setTimeout(() => finish(true), PAYLOAD_WATCHDOG_MS);
  // StringDecoder semantics: a multi-byte character split across two chunks is
  // held until complete, rather than each half becoming a replacement char.
  process.stdin.setEncoding('utf8');
  process.stdin.on('data', (chunk) => {
    input += chunk;
    // A partial payload throws here and we simply wait for more bytes.
    try { JSON.parse(input); } catch (e) { return; }
    finish();
  });
  // Abnormal close (broken pipe, parent crash) emits 'error'; without a
  // listener Node throws it as an uncaught exception and the hook exits
  // non-zero — a spurious hook failure (#538). Hooks must always exit 0.
  process.stdin.on('error', () => finish());
  process.stdin.on('end', () => finish());
}

function run(source, sessionCwd, sessionId) {
let mode;
if (RESET_SOURCES.has(source)) {
  mode = getDefaultMode(sessionCwd);
  // Sweep stale per-session files only when a session genuinely begins, not on
  // every compaction — those are frequent in a long session and this walks a
  // directory inside a 5s hook budget.
  gcSessionStore(claudeDir);
} else {
  // Continuation: read, never re-derive. The LITERAL value, so a stored 'off'
  // is distinguishable from "nothing stored yet".
  let stored = readSessionModeRaw(claudeDir, sessionId);
  // Upgrade path: a session that began before per-session state exists only in
  // the legacy mirror. Falling through to the default there would re-derive on
  // the very compaction #691 fixed. The mirror never holds the literal 'off',
  // so this can only ever supply a real mode.
  if (stored === null) stored = readFlag(legacyFlagPath(claudeDir));
  if (stored && VALID_MODES.includes(stored)) {
    mode = stored;
  } else {
    // resume/fork can carry a session id we have never seen (a fork gets a new
    // one). With nothing stored anywhere, fall back to the configured default.
    mode = getDefaultMode(sessionCwd);
  }
}

// "off" mode — skip activation entirely, don't emit rules. The state is still
// written so the choice survives this session's later compactions: that write
// is what closes the "stop caveman → /compact re-arms caveman" hole, because
// the next SessionStart finds a durable 'off' instead of an absent file.
if (mode === 'off') {
  recordModeChange(claudeDir, null, sessionId); // #601: timestamped transition log
  writeSessionMode(claudeDir, sessionId, null);
  process.stdout.write('OK');
  process.exit(0);
}

// 1. Persist this session's mode (symlink-safe, mirrored to the legacy flag)
recordModeChange(claudeDir, mode, sessionId); // #601
writeSessionMode(claudeDir, sessionId, mode);

// 2. Emit full caveman ruleset, filtered to the active intensity level.
//    The old 2-sentence summary was too weak — models drifted back to verbose
//    mid-conversation, especially after context compression pruned it away.
//    Full rules with examples anchor behavior much more reliably.
//
//    Reads SKILL.md at runtime so edits to the source of truth propagate
//    automatically — no hardcoded duplication to go stale.

// Modes that have their own independent skill files — not caveman intensity levels.
// For these, emit a short activation line; the skill itself handles behavior.
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

if (INDEPENDENT_MODES.has(mode)) {
  process.stdout.write('CAVEMAN MODE ACTIVE — level: ' + mode + '. Behavior defined by /caveman-' + mode + ' skill.');
  process.exit(0);
}

// Resolve the canonical label for wenyan alias, and read SKILL.md — the single
// source of truth for caveman behavior, filtered to this level's intensity row.
//
// Both live in caveman-config.js so caveman-mode-tracker.js can inject the SAME
// ruleset when the user switches level mid-session (#975). Each is resolved
// individually against a local stand-in, for the reason the per-session helpers
// above are: a caveman-config.js predating these exports loads fine and passes
// the shape check, and failing the whole module over them would trade this
// hook's ruleset for no flag write at all. A missing loader degrades to the
// hardcoded fallback ruleset below, which is what a missing SKILL.md already did.
const canonicalModeLabel = cfg.canonicalModeLabel || ((m) => (m === 'wenyan' ? 'wenyan-full' : m));
const rulesetBanner = cfg.rulesetBanner || ((m) => 'CAVEMAN MODE ACTIVE — level: ' + canonicalModeLabel(m));
const loadFilteredRuleset = cfg.loadFilteredRuleset || (() => null);

const modeLabel = canonicalModeLabel(mode);
const skillContent = loadFilteredRuleset(mode, __dirname);

let output;

if (skillContent) {
  output = rulesetBanner(mode) + '\n\n' + skillContent;
} else {
  // Fallback when SKILL.md is not found (standalone hook install without skills dir).
  // This is the minimum viable ruleset — better than nothing.
  output =
    'CAVEMAN MODE ACTIVE — level: ' + modeLabel + '\n\n' +
    'Respond terse like smart caveman. All technical substance stay. Only fluff die.\n\n' +
    '## Persistence\n\n' +
    'Default style for this whole session, every response, until user say "stop caveman" or "normal mode". Keep terse on long sessions — no filler drift.\n\n' +
    'Current level: **' + modeLabel + '**. Switch: `/caveman lite|full|ultra|wenyan-lite|wenyan-full|wenyan-ultra`.\n\n' +
    '## Rules\n\n' +
    'Drop: articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries (sure/certainly/of course/happy to), hedging. ' +
    'Fragments OK. Short synonyms (big not extensive, fix not "implement a solution for"). Technical terms exact. Code blocks unchanged. Errors quoted exact.\n\n' +
    "Preserve user's dominant language. User write Portuguese → reply Portuguese caveman. Compress the style, not the language. Technical terms, code, API names, commands, error strings stay verbatim.\n\n" +
    'Answer directly in this style. Skip "caveman mode on" tags or a "Caveman:" recap — redundant with the reply itself.\n\n' +
    'Pattern: `[thing] [action] [reason]. [next step].`\n\n' +
    'Not: "Sure! I\'d be happy to help you with that. The issue you\'re experiencing is likely caused by..."\n' +
    'Yes: "Bug in auth middleware. Token expiry check use `<` not `<=`. Fix:"\n\n' +
    '## Auto-Clarity\n\n' +
    'Drop caveman for: security warnings, irreversible action confirmations, multi-step sequences where fragment order risks misread, user asks to clarify or repeats question. Resume caveman after clear part done.\n\n' +
    '## Boundaries\n\n' +
    'Code/commits/PRs: write normal. "stop caveman" or "normal mode": revert. Level persist until changed or session end.';
}

// 3. Detect missing statusline config — nudge Claude to help set it up.
// One-shot (#661): the nudge costs ~90 tokens per session, so a marker file
// gates it to the first session only. Users who declined stop paying for it.
const nudgeMarkerPath = path.join(claudeDir, '.caveman-nudge-shown');
try {
  let hasStatusline = false;
  if (fs.existsSync(settingsPath)) {
    const rawSettings = fs.readFileSync(settingsPath, 'utf8');
    try {
      hasStatusline = !!JSON.parse(rawSettings).statusLine;
    } catch (e) {
      // JSONC (comments / trailing commas) is legal in settings.json and the
      // hooks dir has no JSONC parser. Fall back to a substring probe and err
      // toward NOT nudging: a spurious "set up your statusline" for a user who
      // already has one is worse than a missing nudge.
      hasStatusline = rawSettings.includes('"statusLine"');
    }
  }

  if (!hasStatusline && !fs.existsSync(nudgeMarkerPath)) {
    safeWriteFlag(nudgeMarkerPath, '1');
    const isWindows = process.platform === 'win32';
    const scriptName = isWindows ? 'caveman-statusline.ps1' : 'caveman-statusline.sh';
    const scriptPath = path.join(__dirname, scriptName);
    const command = isWindows
      ? `powershell -ExecutionPolicy Bypass -File "${scriptPath}"`
      : `bash "${scriptPath}"`;
    const statusLineSnippet =
      '"statusLine": { "type": "command", "command": ' + JSON.stringify(command) + ' }';
    output += "\n\n" +
      "STATUSLINE SETUP NEEDED: The caveman plugin includes a statusline badge showing active mode " +
      "(e.g. [CAVEMAN], [CAVEMAN:ULTRA]). It is not configured yet. " +
      "To enable, add this to " + path.join(claudeDir, 'settings.json') + ": " +
      statusLineSnippet + " " +
      "Proactively offer to set this up for the user on first interaction.";
  }
} catch (e) {
  // Silent fail — don't block session start over statusline detection
}

process.stdout.write(output);
} // end run()
