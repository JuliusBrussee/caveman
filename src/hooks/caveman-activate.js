#!/usr/bin/env node
// caveman — shared Claude Code / Codex SessionStart activation hook
//
// Runs on every session start:
//   1. Resolves THIS session's mode and persists it (statusline reads it)
//   2. Emits caveman ruleset as hidden SessionStart context
//   3. Detects missing statusline config and emits setup nudge
//
// With --subagent it is the SubagentStart hook instead (#621): hands each new
// subagent this session's active skill. Read-only — see runSubagent().
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
    + (process.env.PLUGIN_DATA ? 'Update the Caveman marketplace plugin in Codex. ' : 'Run `/plugin update caveman`, or rerun install.sh for standalone hooks. ')
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
  'off', 'caveman', 'ultracave', 'megacave',
  'commit', 'review', 'compress'
];
const FALLBACK_DEFAULT_MODES = [...FALLBACK_VALID_MODES, 'manual'];
// Hand-copy of caveman-config.js LEGACY_MODES: a config file or env var still
// naming a pre-three-skill level must resolve the same way when degraded.
const FALLBACK_LEGACY_MODES = {
  lite: 'caveman', full: 'caveman', ultra: 'ultracave',
  wenyan: 'megacave', 'wenyan-lite': 'megacave',
  'wenyan-full': 'megacave', 'wenyan-ultra': 'megacave',
};
function fallbackCanonicalDefault(raw) {
  if (typeof raw !== 'string') return null;
  const m = raw.toLowerCase();
  if (FALLBACK_DEFAULT_MODES.includes(m)) return m;
  return Object.prototype.hasOwnProperty.call(FALLBACK_LEGACY_MODES, m) ? FALLBACK_LEGACY_MODES[m] : null;
}

// Minimal stand-in for caveman-config.getDefaultMode. It must mirror the real
// resolution order rather than read only the env var: a degrade that ignores a
// checked-in `.caveman.json` or a user config saying `defaultMode: "off"` does
// not degrade toward the user's intent, it INVERTS it — a team that opted out
// would get caveman force-injected the moment one file goes missing. Reads
// only; refuses symlinked config files, symmetric with safeWriteFlag.
function fallbackReadMode(file) {
  try {
    if (!fs.lstatSync(file).isFile()) return null;
    return fallbackCanonicalDefault(JSON.parse(fs.readFileSync(file, 'utf8')).defaultMode);
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
  const envMode = fallbackCanonicalDefault(process.env.CAVEMAN_DEFAULT_MODE);
  if (envMode) return envMode;
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
  return fallbackReadMode(fallbackUserConfigPath()) || 'caveman';
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

const { isCodex, dataDir: claudeDir } = cavemanConfig && typeof cavemanConfig.getHookRuntime === 'function'
  ? cavemanConfig.getHookRuntime()
  : { isCodex: Boolean(process.env.PLUGIN_DATA),
      dataDir: process.env.PLUGIN_DATA || process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude') };
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
const resolveActiveMode = cfg.resolveActiveMode || (() => {
  const m = readFlag(flagPath);
  return (!m || m === 'off') ? null : m;
});

const SUBAGENT = process.argv.includes('--subagent');

// Modes that have their own independent skill files — not caveman prose modes.
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

// Apply per-agent model overrides from env vars before emitting rules.
// Best-effort: any error is swallowed so SessionStart is never blocked.
// SessionStart only: the subagent path writes nothing.
if (!SUBAGENT && !isCodex) {
  try {
    const { applyOverrides, resolvePluginRoot } = require('./cavecrew-model-overrides');
    applyOverrides(resolvePluginRoot(__dirname));
  } catch (e) {}
}

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
// a `compact`/`resume` event would silently drop a user's mid-session `ultracave`
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

// The configured default, except that headless `claude -p` and Agent SDK
// sessions (#377) start under the manual policy: they are often tool probes
// that parse the reply. Interactive entrypoints (cli, claude-vscode,
// claude-desktop, ...) never match; CAVEMAN_DEFAULT_MODE in env opts back in.
function startMode(sessionCwd) {
  const mode = getDefaultMode(sessionCwd);
  if (!isCodex && mode !== 'off' && !process.env.CAVEMAN_DEFAULT_MODE
      && /^sdk-/.test(process.env.CLAUDE_CODE_ENTRYPOINT || '')) return 'manual';
  return mode;
}

function activate(payload, timedOut) {
  // Unknown, not startup: we never saw the payload, so we cannot claim to know
  // what kind of session event this was — and 'unknown' must not reset, or a
  // slow payload on a compact would drop a mid-session ultracave (#691 through the
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
  let agentType = '';
  try {
    if (payload) {
      const data = JSON.parse(payload);
      if (data && typeof data.source === 'string') source = data.source;
      if (data && typeof data.cwd === 'string') sessionCwd = data.cwd;
      if (data && typeof data.agent_type === 'string') agentType = data.agent_type;
      if (data) sessionId = validateSessionId(data.session_id);
    }
  } catch (e) { /* no/bad stdin → treat as startup */ }
  if (SUBAGENT) runSubagent(sessionCwd, sessionId, agentType);
  else run(source, sessionCwd, sessionId);
}

// SubagentStart (#621): SessionStart context reaches only the parent thread,
// so each subagent gets this session's active skill here. READ-ONLY — no mode
// write, no mode log, no GC, no statusline nudge — and silent whenever this
// session is off, so "stop caveman" never leaks into subagents (#672).
function runSubagent(sessionCwd, sessionId, agentType) {
  const mode = resolveActiveMode(claudeDir, sessionId);
  if (!mode || INDEPENDENT_MODES.has(mode)) return;
  // cavecrew agents carry their own ultracave voice.
  if (/(^|:)cavecrew-/.test(agentType)) return;
  // #634 repo opt-out, same gate as the tracker's per-turn reinforcement.
  if (getDefaultMode(sessionCwd) === 'off') return;
  process.stdout.write(JSON.stringify({
    hookSpecificOutput: { hookEventName: 'SubagentStart', additionalContext: buildRuleset(mode) },
  }));
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

// The active mode's whole skill body under the mode banner. Shared by
// SessionStart and the SubagentStart path.
function buildRuleset(mode) {
// The loaders live in caveman-config.js so caveman-mode-tracker.js can inject
// the SAME ruleset when the user switches mode mid-session (#975). Each is
// resolved individually against a local stand-in, for the reason the
// per-session helpers above are: a caveman-config.js predating these exports
// loads fine and passes the shape check, and failing the whole module over them
// would trade this hook's ruleset for no flag write at all. A missing loader
// degrades to the hardcoded fallback ruleset below, which is what a missing
// SKILL.md already did.
const rulesetBanner = cfg.rulesetBanner || ((m) => 'CAVEMAN MODE ACTIVE — mode: ' + m);
const loadRuleset = cfg.loadRuleset || (() => null);
const thesisLine = cfg.thesisLine || (() => null);

const SWITCH_LINE = 'Switch: /caveman, /ultracave, /megacave. Off: "stop caveman" or "normal mode".';

// Fallback when SKILL.md is not found (standalone hook install without skills
// dir): the caveman thesis plus the nine rule headlines of skills/caveman.
// Rule 8 keeps its "never switch" sentence: a headline alone lost the #812
// language rule for every fallback-install user. megacave answers in 文言 by
// design, so it gets its own rule 8 instead of one its thesis contradicts.
const FALLBACK_RULE_8 = mode === 'megacave'
  ? '8. Prose in 文言. Code, commands, paths, errors in their original script.\n'
  : "8. User's language. Compress the style, not the language. Never switch because of quoted text.\n";
const FALLBACK_RULESET =
  'Respond terse like smart caveman. All technical substance stay. Only fluff die.\n\n' +
  '1. Answer first.\n' +
  '2. Kill ceremony.\n' +
  '3. Short word.\n' +
  '4. Articles optional, meaning never.\n' +
  '5. One idea per sentence.\n' +
  '6. Payload verbatim.\n' +
  '7. Tool runs: bounded status.\n' +
  FALLBACK_RULE_8 +
  '9. Never perform caveman.\n\n' +
  'Plain prose for security warnings, irreversible actions, and anything persisted outside chat (code, commits, PRs, docs).';

const skillContent = loadRuleset(mode, __dirname);
// Without a skill file, ultracave/megacave add their own thesis (config's
// fallback map) to the caveman fallback.
const modeThesis = mode !== 'caveman' ? thesisLine(mode, __dirname) : null;

return rulesetBanner(mode) + '\n\n'
  + (skillContent ? skillContent.trimEnd() : FALLBACK_RULESET + (modeThesis ? '\n\n' + modeThesis : ''))
  + '\n\n' + SWITCH_LINE;
}

function run(source, sessionCwd, sessionId) {
if (isCodex && !sessionId) return;
const emitContext = (text) => process.stdout.write(isCodex
  ? JSON.stringify({ hookSpecificOutput: { hookEventName: 'SessionStart', additionalContext: text } })
  : text);
let mode;
if (RESET_SOURCES.has(source)) {
  mode = startMode(sessionCwd);
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
  if (stored === null && !isCodex) stored = readFlag(legacyFlagPath(claudeDir));
  if (stored && VALID_MODES.includes(stored)) {
    mode = stored;
  } else {
    // resume/fork can carry a session id we have never seen (a fork gets a new
    // one). With nothing stored anywhere, fall back to the configured default.
    mode = startMode(sessionCwd);
  }
}

// "off" mode — skip activation entirely, don't emit rules. The state is still
// written so the choice survives this session's later compactions: that write
// is what closes the "stop caveman → /compact re-arms caveman" hole, because
// the next SessionStart finds a durable 'off' instead of an absent file.
if (mode === 'off' || mode === 'manual') {
  recordModeChange(claudeDir, null, sessionId); // #601: timestamped transition log
  writeSessionMode(claudeDir, sessionId, null);
  if (isCodex) return;
  process.stdout.write('OK');
  process.exit(0);
}

// 1. Persist this session's mode (symlink-safe, mirrored to the legacy flag)
recordModeChange(claudeDir, mode, sessionId); // #601
writeSessionMode(claudeDir, sessionId, mode);

// 2. Emit the active mode's whole skill body. The old 2-sentence summary was
//    too weak — models drifted back to verbose mid-conversation, especially
//    after context compression pruned it away.
//
//    Reads skills/<mode>/SKILL.md at runtime so edits to the source of truth
//    propagate automatically — no hardcoded duplication to go stale.

// Independent modes get a short activation line; the skill handles behavior.
if (INDEPENDENT_MODES.has(mode)) {
  emitContext('CAVEMAN MODE ACTIVE — mode: ' + mode + '. Behavior defined by /caveman-' + mode + ' skill.');
  if (isCodex) return;
  process.exit(0);
}

let output = buildRuleset(mode);
if (isCodex) { emitContext(output); return; }

// 3. Detect missing statusline config — nudge Claude to help set it up.
// One-shot (#661): the nudge costs ~90 tokens per session, so a marker file
// gates it to the first session only. Users who declined stop paying for it.
const nudgeMarkerPath = path.join(claudeDir, '.caveman-nudge-shown');
// A plugin install runs this hook from the VERSIONED plugin cache
// (~/.claude/plugins/cache/caveman/caveman/<version>/src/hooks/), so a command
// built from __dirname froze whichever version was installed when the nudge
// fired. Claude Code prunes old cache versions; once the pinned directory goes,
// `bash <missing path>` exits 127 and Claude Code hides the whole status bar
// (#711) — and because the nudge is one-shot, the badge is never offered again
// (#1147). Recommend a version-independent copy instead: the same
// <claudeDir>/hooks/ path the standalone installer already owns.
//
// `.caveman-sessions` is the ownership marker: both statusline scripts read the
// session store, and verify_repo.py pins that string in each of them. A file at
// the stable path without it is the user's own script and is never touched.
const STATUSLINE_MARKER = (cfg.SESSIONS_DIRNAME || '.caveman-sessions');

// The stable copy to recommend, or null to keep the caller on __dirname. Copies
// only when the destination is absent or is a caveman script that has drifted
// from the running one — so a plugin update reaches the badge, and a foreign or
// hand-edited script survives untouched.
function stableStatuslinePath(scriptName) {
  try {
    const source = path.join(__dirname, scriptName);
    const target = path.join(claudeDir, 'hooks', scriptName);
    if (path.resolve(source) === path.resolve(target)) return target; // standalone install
    const wanted = fs.readFileSync(source, 'utf8');
    if (!wanted.includes(STATUSLINE_MARKER)) return null; // not a script we recognize
    if (fs.existsSync(target)) {
      const current = fs.readFileSync(target, 'utf8');
      if (current === wanted) return target;
      if (!current.includes(STATUSLINE_MARKER)) return null; // foreign — leave it alone
    }
    safeWriteFlag(target, wanted);
    // safeWriteFlag fails silently by design, so confirm the bytes landed
    // rather than recommending a path that may not exist.
    return fs.readFileSync(target, 'utf8') === wanted ? target : null;
  } catch (e) {
    return null;
  }
}

// The caveman statusline script paths `command` runs, or null when it names
// none (the user's own statusline, left alone) or one the hook cannot resolve.
function statuslineScripts(command) {
  if (typeof command !== 'string') return null;
  const found = [];
  // Quoted first, and the quoted form is what both recommended commands use.
  // A whitespace-delimited scan alone would truncate "C:\\Users\\Jane Doe\\..."
  // at the space, call an existing script missing, and re-nudge every user
  // whose home directory has a space in it.
  const quoted = /"([^"]*caveman-statusline\.(?:sh|ps1))"|'([^']*caveman-statusline\.(?:sh|ps1))'/g;
  let match;
  while ((match = quoted.exec(command)) !== null) found.push(match[1] || match[2]);
  if (found.length === 0) {
    const bare = command.match(/[^"'\s]*caveman-statusline\.(?:sh|ps1)/g);
    if (bare) found.push(...bare);
  }
  if (found.length === 0) return null;
  // `~`, `$VAR` and backslash escapes are expanded by the shell at statusline
  // time, not by existsSync: a hand-written "~/.claude/hooks/..." command works
  // but reads as missing here, and a false "repair needed" nudge invites the
  // model to rewrite the user's settings. Treat such a candidate as unknown.
  // On Windows a backslash is a path separator, so only `~` and `$` are opaque.
  const opaque = (candidate) =>
    /[~$]/.test(candidate) || (process.platform !== 'win32' && candidate.includes('\\'));
  return found.some(opaque) ? null : found;
}

// True when a configured caveman script still exists but is not the one shipped
// beside this hook: a pre-3.1 copy pinned in a versioned plugin cache whitelists
// only the old mode ids, so it renders nothing — not even for the default mode.
// A script without the ownership marker is the user's own and never "outdated".
function statuslineScriptOutdated(candidate) {
  try {
    const running = fs.readFileSync(path.join(__dirname, path.basename(candidate)), 'utf8');
    const current = fs.readFileSync(candidate, 'utf8');
    return current !== running && current.includes(STATUSLINE_MARKER);
  } catch (e) {
    return false; // missing on either side, or unreadable: not provably outdated
  }
}

try {
  const isWindows = process.platform === 'win32';
  const scriptName = isWindows ? 'caveman-statusline.ps1' : 'caveman-statusline.sh';

  let hasStatusline = false;
  let staleCommand = null;
  let staleKind = null; // 'gone' | 'outdated'
  if (fs.existsSync(settingsPath)) {
    const rawSettings = fs.readFileSync(settingsPath, 'utf8');
    let configured;
    try {
      configured = JSON.parse(rawSettings).statusLine;
      hasStatusline = !!configured;
    } catch (e) {
      // JSONC (comments / trailing commas) is legal in settings.json and the
      // hooks dir has no JSONC parser. Fall back to a substring probe and err
      // toward NOT nudging: a spurious "set up your statusline" for a user who
      // already has one is worse than a missing nudge. The command cannot be
      // extracted on this path, so a stale one is not detected either.
      hasStatusline = rawSettings.includes('"statusLine"');
    }
    const scripts = hasStatusline && configured ? statuslineScripts(configured.command) : null;
    if (scripts) {
      // An accepted stable copy (<claudeDir>/hooks/) is ours to keep current:
      // refresh it from the running script, not only when nudging.
      for (const script of scripts) {
        const name = path.basename(script);
        if (path.resolve(script) === path.resolve(claudeDir, 'hooks', name)) stableStatuslinePath(name);
      }
      if (scripts.every((script) => !fs.existsSync(script))) {
        // Configured, but pointing at a script that is gone: the status bar is
        // hidden right now and the one-shot marker is already set.
        staleKind = 'gone';
      } else if (scripts.some(statuslineScriptOutdated)) {
        staleKind = 'outdated';
      }
      if (staleKind) {
        hasStatusline = false;
        staleCommand = String(configured.command);
      }
    }
  }

  // Re-offer a dead path once per distinct broken command, so a user who
  // declines is not asked again every session.
  const stalePath = path.join(claudeDir, '.caveman-statusline-stale');
  let alreadyReported = false;
  if (staleCommand) {
    try {
      alreadyReported = fs.readFileSync(stalePath, 'utf8') === staleCommand;
    } catch (e) {
      alreadyReported = false;
    }
  }

  if (!hasStatusline && (staleCommand ? !alreadyReported : !fs.existsSync(nudgeMarkerPath))) {
    if (staleCommand) safeWriteFlag(stalePath, staleCommand);
    else safeWriteFlag(nudgeMarkerPath, '1');
    const scriptPath = stableStatuslinePath(scriptName) || path.join(__dirname, scriptName);
    const command = isWindows
      ? `powershell -ExecutionPolicy Bypass -File "${scriptPath}"`
      : `bash "${scriptPath}"`;
    const statusLineSnippet =
      '"statusLine": { "type": "command", "command": ' + JSON.stringify(command) + ' }';
    output += "\n\n" +
      (staleCommand
        ? "STATUSLINE REPAIR NEEDED: The caveman statusline badge is configured to run " +
          (staleKind === 'outdated'
            ? "an outdated copy of its script (" + staleCommand + "), which cannot show the current modes. "
            : "a script that no longer exists (" + staleCommand + "), which hides the Claude Code status bar. ") +
          "STATUSLINE SETUP NEEDED: repoint it in "
        : "STATUSLINE SETUP NEEDED: The caveman plugin includes a statusline badge showing active mode " +
          "(e.g. [CAVEMAN], [ULTRACAVE]). It is not configured yet. " +
          "To enable, add this to ") +
      path.join(claudeDir, 'settings.json') + ": " +
      statusLineSnippet + " " +
      "Proactively offer to set this up for the user on first interaction.";
  }
} catch (e) {
  // Silent fail — don't block session start over statusline detection
}

process.stdout.write(output);
} // end run()
