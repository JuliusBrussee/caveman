#!/usr/bin/env node
// caveman — shared configuration resolver
//
// KEEP THIS FILE LOADABLE WITHOUT THE MODULE LOADER. `src/plugins/opencode/
// plugin.js` cannot `require()` a file from disk (compiled Bun binary), so it
// reads this file's source and evaluates it through `new Function(...)` with a
// `createRequire` rooted at this file. Node built-ins (fs/path/os) resolve
// there; anything heavier does not. Consequence for mode-state work: keep it
// HERE. Parsing already lives in caveman-parse.js (#602) and is loaded the same
// way, so a second parser copy is no longer a risk — but a THIRD home for the
// state primitives would be, because opencode would keep its own drifting
// version of them (the drift #602 was created to end).
//
// Resolution order for default mode:
//   1. CAVEMAN_DEFAULT_MODE environment variable
//   2. Repo-local config (checked-in, per-project default):
//      - <cwd>/.caveman/config.json
//      - <cwd>/.caveman.json
//      Walks up from process.cwd() to the nearest ancestor containing one of
//      these (stops at filesystem root). Lets a team pin a project's default
//      mode without polluting every contributor's user-level config or env.
//   3. User config file defaultMode field:
//      - $XDG_CONFIG_HOME/caveman/config.json (any platform, if set)
//      - ~/.config/caveman/config.json (macOS / Linux fallback)
//      - %APPDATA%\caveman\config.json (Windows fallback)
//   4. 'full'

const fs = require('fs');
const path = require('path');
const os = require('os');

const VALID_MODES = [
  'off', 'lite', 'full', 'ultra',
  'wenyan-lite', 'wenyan', 'wenyan-full', 'wenyan-ultra',
  'commit', 'review', 'compress'
];

// Legacy machine-wide flag. Kept as a last-write-wins MIRROR of whichever
// session wrote most recently, because INSTALL.md tells users to `cat` it and
// src/hooks/README.md ships a third-party statusline snippet that reads it.
// It never receives the literal string 'off' — see writeSessionMode.
const FLAG_BASENAME = '.caveman-active';
const PREV_BASENAME = '.caveman-active.prev';

// Per-session state lives here, one small file per session id. Dot-prefixed to
// match the rest of the caveman namespace in $CLAUDE_CONFIG_DIR and to avoid
// colliding with Claude Code's own directories (projects/, hooks/, ...).
const SESSIONS_DIRNAME = '.caveman-sessions';

function getConfigDir() {
  if (process.env.XDG_CONFIG_HOME) {
    return path.join(process.env.XDG_CONFIG_HOME, 'caveman');
  }
  if (process.platform === 'win32') {
    return path.join(
      process.env.APPDATA || path.join(os.homedir(), 'AppData', 'Roaming'),
      'caveman'
    );
  }
  return path.join(os.homedir(), '.config', 'caveman');
}

function getConfigPath() {
  return path.join(getConfigDir(), 'config.json');
}

// Walk up from `start` looking for a repo-local caveman config. Returns the
// absolute path of the first match, or null. Stops at the filesystem root.
// Candidates per dir (first wins): .caveman/config.json, .caveman.json.
//
// Bounded to 64 levels to defend against symlink cycles on pathological mounts.
function findRepoConfigPath(start) {
  try {
    let dir = path.resolve(start || process.cwd());
    const candidates = ['.caveman/config.json', '.caveman.json'];
    for (let i = 0; i < 64; i++) {
      for (const rel of candidates) {
        const p = path.join(dir, rel);
        try {
          const st = fs.lstatSync(p);
          // Refuse symlinks — symmetric with safeWriteFlag/readFlag policy.
          if (st.isSymbolicLink() || !st.isFile()) continue;
          return p;
        } catch (e) {
          // not present, try next candidate
        }
      }
      const parent = path.dirname(dir);
      if (parent === dir) return null;
      dir = parent;
    }
  } catch (e) {
    // Defensive: any cwd / fs failure → no repo config
  }
  return null;
}

function readModeFromConfigFile(configPath) {
  try {
    const raw = fs.readFileSync(configPath, 'utf8');
    const config = JSON.parse(raw);
    if (config && config.defaultMode &&
        VALID_MODES.includes(String(config.defaultMode).toLowerCase())) {
      return String(config.defaultMode).toLowerCase();
    }
  } catch (e) {
    // Missing / unreadable / invalid JSON → caller falls through
  }
  return null;
}

// startDir overrides the cwd the repo-config walk starts from (default
// process.cwd(), same as findRepoConfigPath's own fallback) — lets a caller
// resolve the mode for a directory other than its own process cwd (#634:
// the UserPromptSubmit hook's stdin carries the session's cwd, which can
// differ from the hook process's cwd). Every other resolution step is
// cwd-independent, so only the repo-config walk takes it.
function getDefaultMode(startDir) {
  // 1. Environment variable (highest priority)
  const envMode = process.env.CAVEMAN_DEFAULT_MODE;
  if (envMode && VALID_MODES.includes(envMode.toLowerCase())) {
    return envMode.toLowerCase();
  }

  // 2. Repo-local config (checked-in, per-project default)
  const repoConfigPath = findRepoConfigPath(startDir);
  if (repoConfigPath) {
    const repoMode = readModeFromConfigFile(repoConfigPath);
    if (repoMode) return repoMode;
  }

  // 3. User config file
  const userMode = readModeFromConfigFile(getConfigPath());
  if (userMode) return userMode;

  // 4. Default
  return 'full';
}

// Symlink-safe flag file write.
// Uses O_NOFOLLOW where available, writes atomically via temp + rename with
// 0600 permissions. Protects against local attackers replacing the predictable
// flag path (~/.claude/.caveman-active) with a symlink to clobber other files.
//
// When the parent directory is itself a symlink (legitimate pattern: ~/.claude
// symlinked to another drive or shared config dir), resolves through to the
// real path and verifies ownership on Unix (uid match). This allows e.g.
//   ln -s /opt/shared-claude-config ~/.claude
// while still refusing attacker-planted symlinks pointing to dirs owned by
// another user.
//
// On Windows, uid checks are unavailable — falls back to verifying the resolved
// path lives under the user's home directory.
//
// The flag file itself must never be a symlink (that's the actual clobber vector).
//
// Set CAVEMAN_DEBUG=1 to emit stderr diagnostics when flag writes are refused.
//
// Silent-fails on any filesystem error — the flag is best-effort.
// Blocking sleep with no child process and no busy-wait. Used only to space
// out rename retries under Windows lock contention.
function sleepMs(ms) {
  try {
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
  } catch (e) { /* SharedArrayBuffer unavailable — skip the backoff */ }
}

function safeWriteFlag(flagPath, content) {
  const debug = process.env.CAVEMAN_DEBUG === '1';
  try {
    const flagDir = path.dirname(flagPath);
    fs.mkdirSync(flagDir, { recursive: true });

    // When the parent directory is a symlink, resolve it and verify ownership.
    // This allows legitimate symlinked ~/.claude dirs while still refusing
    // attacker-planted symlinks pointing at dirs owned by another user.
    let realFlagDir;
    try {
      const lstat = fs.lstatSync(flagDir);
      if (lstat.isSymbolicLink()) {
        realFlagDir = fs.realpathSync(flagDir);
        const realStat = fs.statSync(realFlagDir);
        if (!realStat.isDirectory()) {
          if (debug) process.stderr.write(`[caveman] safeWriteFlag: symlink target ${realFlagDir} is not a directory\n`);
          return;
        }
        if (typeof process.getuid === 'function') {
          if (realStat.uid !== process.getuid()) {
            if (debug) process.stderr.write(`[caveman] safeWriteFlag: symlink target ${realFlagDir} owned by uid ${realStat.uid}, not current user ${process.getuid()}\n`);
            return;
          }
        } else {
          const home = os.homedir();
          const normalizedReal = path.resolve(realFlagDir);
          const normalizedHome = path.resolve(home);
          if (!normalizedReal.toLowerCase().startsWith(normalizedHome.toLowerCase() + path.sep) &&
              normalizedReal.toLowerCase() !== normalizedHome.toLowerCase()) {
            if (debug) process.stderr.write(`[caveman] safeWriteFlag: symlink target ${normalizedReal} is outside home directory ${normalizedHome}\n`);
            return;
          }
        }
      } else {
        realFlagDir = flagDir;
      }
    } catch (e) {
      return;
    }

    // The flag file itself must never be a symlink (that's the actual clobber vector).
    const realFlagPath = path.join(realFlagDir, path.basename(flagPath));
    try {
      if (fs.lstatSync(realFlagPath).isSymbolicLink()) return;
    } catch (e) {
      if (e.code !== 'ENOENT') return;
    }

    // tempPath is hoisted above the try so the finally below can always find
    // it. On Windows, renameSync onto an existing target throws EPERM/EBUSY/
    // EACCES/EEXIST when another process (statusline read, a concurrent
    // session's hook) holds the file open without FILE_SHARE_DELETE —
    // without the retry + guaranteed cleanup here, every such miss left an
    // orphaned .caveman-active.<pid>.<ts> file behind (#511/#578/#657).
    let tempPath;
    try {
      tempPath = path.join(realFlagDir, `.caveman-active.${process.pid}.${Date.now()}`);
      const O_NOFOLLOW = typeof fs.constants.O_NOFOLLOW === 'number' ? fs.constants.O_NOFOLLOW : 0;
      const flags = fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | O_NOFOLLOW;
      let fd;
      try {
        fd = fs.openSync(tempPath, flags, 0o600);
        fs.writeSync(fd, String(content));
        try { fs.fchmodSync(fd, 0o600); } catch (e) { /* best-effort on Windows */ }
      } finally {
        if (fd !== undefined) fs.closeSync(fd);
      }

      // Retry brief lock contention a few times, backing off between attempts.
      // Three synchronous attempts with no delay all complete inside a few
      // microseconds, so whichever handle blocked the first (a statusline read,
      // a concurrent session's hook, an AV scan) is still held for the other
      // two — effectively one attempt with two extra syscalls. Sleep for real:
      // Atomics.wait blocks this thread without pulling in a child process,
      // which is what a hook budget can least afford.
      let renamed = false;
      for (let attempt = 0; attempt < 3; attempt++) {
        try {
          fs.renameSync(tempPath, realFlagPath);
          renamed = true;
          break;
        } catch (e) {
          const transient = e.code === 'EPERM' || e.code === 'EBUSY' ||
                             e.code === 'EACCES' || e.code === 'EEXIST';
          if (!transient) throw e;
          if (attempt < 2) sleepMs(10 * (attempt + 1));
        }
      }
      if (!renamed && debug) {
        process.stderr.write('[caveman] safeWriteFlag: rename contended after 3 attempts; flag not updated this write\n');
      }
    } finally {
      if (tempPath) {
        try {
          fs.unlinkSync(tempPath);
        } catch (error) {
          if (debug && error.code !== 'ENOENT') {
            process.stderr.write(`[caveman] safeWriteFlag: failed to remove temp flag ${tempPath}: ${error.message}\n`);
          }
        }
      }
    }
  } catch (e) {
    // Silent fail — flag is best-effort
  }
}

// Symlink-safe, size-capped, whitelist-validated flag file read.
// Symmetric with safeWriteFlag: refuses symlinks at the target, caps the read,
// and rejects anything that isn't a known mode. Returns null on any anomaly.
//
// Without this, a local attacker with write access to ~/.claude/ could replace
// the flag with a symlink to ~/.ssh/id_rsa (or any user-readable secret). Every
// reader — statusline, per-turn reinforcement — would slurp that content and
// either echo it to the terminal or inject it into model context.
//
// MAX_FLAG_BYTES is a hard cap. The longest legitimate value is "wenyan-ultra"
// (12 bytes); 64 leaves slack without enabling exfil.
const MAX_FLAG_BYTES = 64;

function readFlag(flagPath) {
  try {
    let st;
    try {
      st = fs.lstatSync(flagPath);
    } catch (e) {
      return null;
    }
    if (st.isSymbolicLink() || !st.isFile()) return null;
    if (st.size > MAX_FLAG_BYTES) return null;

    const O_NOFOLLOW = typeof fs.constants.O_NOFOLLOW === 'number' ? fs.constants.O_NOFOLLOW : 0;
    const flags = fs.constants.O_RDONLY | O_NOFOLLOW;
    let fd;
    let out;
    try {
      fd = fs.openSync(flagPath, flags);
      const buf = Buffer.alloc(MAX_FLAG_BYTES);
      const n = fs.readSync(fd, buf, 0, MAX_FLAG_BYTES, 0);
      out = buf.slice(0, n).toString('utf8');
    } finally {
      if (fd !== undefined) fs.closeSync(fd);
    }

    const raw = out.trim().toLowerCase();
    if (!VALID_MODES.includes(raw)) return null;
    return raw;
  } catch (e) {
    return null;
  }
}

// Symlink-safe append. Same parent-dir + symlink-target rules as safeWriteFlag,
// but opens with O_APPEND so concurrent writers from different sessions don't
// clobber each other. Used for the lifetime stats log
// ($CLAUDE_CONFIG_DIR/.caveman-history.jsonl).
//
// Silent-fails on any filesystem error.
function appendFlag(filePath, line) {
  const debug = process.env.CAVEMAN_DEBUG === '1';
  try {
    const dir = path.dirname(filePath);
    fs.mkdirSync(dir, { recursive: true });

    let realDir;
    try {
      const lstat = fs.lstatSync(dir);
      if (lstat.isSymbolicLink()) {
        realDir = fs.realpathSync(dir);
        const realStat = fs.statSync(realDir);
        if (!realStat.isDirectory()) return;
        if (typeof process.getuid === 'function') {
          if (realStat.uid !== process.getuid()) {
            if (debug) process.stderr.write(`[caveman] appendFlag: symlink target ${realDir} owned by uid ${realStat.uid}\n`);
            return;
          }
        } else {
          const home = os.homedir();
          const normalized = path.resolve(realDir).toLowerCase();
          const normalizedHome = path.resolve(home).toLowerCase();
          if (!normalized.startsWith(normalizedHome + path.sep) && normalized !== normalizedHome) return;
        }
      } else {
        realDir = dir;
      }
    } catch (e) {
      return;
    }

    const realPath = path.join(realDir, path.basename(filePath));
    try {
      if (fs.lstatSync(realPath).isSymbolicLink()) return;
    } catch (e) {
      if (e.code !== 'ENOENT') return;
    }

    const O_NOFOLLOW = typeof fs.constants.O_NOFOLLOW === 'number' ? fs.constants.O_NOFOLLOW : 0;
    const flags = fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_APPEND | O_NOFOLLOW;
    let fd;
    try {
      fd = fs.openSync(realPath, flags, 0o600);
      fs.writeSync(fd, String(line).replace(/\n$/, '') + '\n');
      try { fs.fchmodSync(fd, 0o600); } catch (e) { /* best-effort on Windows */ }
    } finally {
      if (fd !== undefined) fs.closeSync(fd);
    }
  } catch (e) {
    // Silent fail — history is best-effort
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-session mode state
//
// The mode is applied per session but used to be stored in ONE file per
// machine. That single fact caused four bugs: parallel sessions shared a mode,
// SessionStart re-derived the default and clobbered an explicit "stop caveman"
// after every auto-compaction, the statusline badge showed the same mode in
// every window, and "off" — expressed as file ABSENCE — could never survive a
// SessionStart.
//
// #691 fixed the compaction case for a mid-session LEVEL change by branching on
// the hook payload's `source`. It cannot fix the other three, because all of
// them are consequences of the storage shape rather than of when the hook
// re-derives: one file cannot hold two windows' modes, and an absent file
// cannot express "deliberately off" distinguishably from "never set".
//
// Storage: $CLAUDE_CONFIG_DIR/.caveman-sessions/<session_id>.mode (+ .prev),
// with the legacy flag kept as a compat mirror.
//
// Every function below accepts a sessionId that may be null or malformed and
// degrades to the previous machine-wide behavior in that case. That is the
// whole backward-compatibility story: the old code path IS the fallback
// branch, so a caller with no session id behaves exactly as it did before.
// ─────────────────────────────────────────────────────────────────────────────

// A session id becomes part of a filesystem path, so it is a path-traversal
// vector. Whitelist the alphabet rather than blacklisting separators — same
// philosophy as VALID_MODES. Claude Code sends a UUID, but the regex stays
// deliberately broad so a future id format degrades to the legacy path instead
// of throwing. Length-capped so a hostile id cannot blow past filename limits.
const SESSION_ID_RE = /^[A-Za-z0-9_-]{1,128}$/;

function validateSessionId(id) {
  if (typeof id !== 'string' || !SESSION_ID_RE.test(id)) return null;
  return id;
}

function legacyFlagPath(claudeDir) {
  return path.join(claudeDir, FLAG_BASENAME);
}

function sessionsDir(claudeDir) {
  return path.join(claudeDir, SESSIONS_DIRNAME);
}

// Returns null for any id the whitelist rejects. The resolved-path containment
// check is redundant while SESSION_ID_RE holds, but it is cheap and it defends
// against a future edit loosening the regex — the same defense-in-depth style
// as the symlink checks in safeWriteFlag.
function sessionStatePath(claudeDir, sessionId, ext) {
  const sid = validateSessionId(sessionId);
  if (!sid) return null;
  const dir = sessionsDir(claudeDir);
  const candidate = path.join(dir, sid + ext);
  if (path.dirname(path.resolve(candidate)) !== path.resolve(dir)) return null;
  return candidate;
}

function sessionActivePath(claudeDir, sessionId) {
  return sessionStatePath(claudeDir, sessionId, '.mode');
}

function sessionPrevPath(claudeDir, sessionId) {
  return sessionStatePath(claudeDir, sessionId, '.prev');
}

// Both "no file" and the literal 'off' mean caveman is off. Collapsing them to
// a single null is what implements the dual-read compat requirement: a missing
// file is the old spelling, a literal 'off' the new durable one.
function offToNull(mode) {
  return (!mode || mode === 'off') ? null : mode;
}

// "What mode is in effect right now" — for every reader (per-turn
// reinforcement gate, ruleset re-emission, statusline, stats).
function resolveActiveMode(claudeDir, sessionId) {
  const sessionPath = sessionActivePath(claudeDir, sessionId);
  if (sessionPath) {
    const stored = readFlag(sessionPath);
    if (stored !== null) return offToNull(stored);
  }
  return offToNull(readFlag(legacyFlagPath(claudeDir)));
}

// "What is literally written for THIS session" — no legacy fallback, and 'off'
// comes back verbatim. Two callers need exactly this: recordModeChange (whose
// comparison against the shared mirror would diff against whatever some OTHER
// session wrote last and spam the log with phantom transitions) and the
// SessionStart continuation branch, which has to tell "this session chose off"
// apart from "this session has no state yet".
function readSessionModeRaw(claudeDir, sessionId) {
  const sessionPath = sessionActivePath(claudeDir, sessionId);
  if (!sessionPath) return readFlag(legacyFlagPath(claudeDir));
  return readFlag(sessionPath);
}

// Single writer for the active mode. Pass null (or 'off') to deactivate.
//
// The session file stores 'off' literally — that is what makes deactivation
// durable across SessionStart. The legacy mirror instead gets UNLINKED, and
// never holds the literal 'off', on purpose: 'off' is already in VALID_MODES,
// so an older caveman-mode-tracker.js reading it from the legacy path would
// clear its !INDEPENDENT_MODES check and inject "CAVEMAN MODE ACTIVE (off)",
// and an older caveman-statusline.sh would render [CAVEMAN:OFF] instead of
// staying silent. Mixed-version installs are real — plugin hooks and
// standalone hooks can both be registered, and settings.json holds a
// statusline path baked in at install time.
function writeSessionMode(claudeDir, sessionId, modeOrNull) {
  const canonical = (!modeOrNull || modeOrNull === 'off') ? 'off' : modeOrNull;
  if (!VALID_MODES.includes(canonical)) return;

  const sessionPath = sessionActivePath(claudeDir, sessionId);
  if (sessionPath) safeWriteFlag(sessionPath, canonical);

  const legacy = legacyFlagPath(claudeDir);
  if (canonical === 'off') {
    try { fs.unlinkSync(legacy); } catch (e) { /* already absent */ }
  } else {
    safeWriteFlag(legacy, canonical);
  }
}

// Displaced-prose-mode memory for one-shot independent modes (#599), scoped to
// the session. Without the scoping, two windows each running /caveman-commit
// would overwrite each other's "where to return to".
//
// Unlike the active mode, prev is stored in EXACTLY ONE place — the session
// file when there is a valid session id, the legacy file otherwise. It is not
// mirrored, and the read/clear helpers must not cross that line either.
//
// Falling back to the legacy prev whenever a session simply has none of its own
// is a real bug, not a convenience: a session where caveman was never on saves
// no prev (there is nothing to displace), so after /caveman-commit it would
// restore from whatever stale machine-wide prev another context left behind and
// switch itself on at that mode. Symmetrically, clearing must not reach across
// and delete machine-wide state on behalf of one session.
function writeSessionPrev(claudeDir, sessionId, mode) {
  if (!mode || !VALID_MODES.includes(mode)) return;
  const p = sessionPrevPath(claudeDir, sessionId) || path.join(claudeDir, PREV_BASENAME);
  safeWriteFlag(p, mode);
}

function readSessionPrev(claudeDir, sessionId) {
  const p = sessionPrevPath(claudeDir, sessionId);
  if (p) return readFlag(p);
  return readFlag(path.join(claudeDir, PREV_BASENAME));
}

function clearSessionPrev(claudeDir, sessionId) {
  const p = sessionPrevPath(claudeDir, sessionId);
  if (p) {
    try { fs.unlinkSync(p); } catch (e) {}
    return;
  }
  try { fs.unlinkSync(path.join(claudeDir, PREV_BASENAME)); } catch (e) {}
}

// Sweep stale per-session files. Called from SessionStart on a genuinely new
// session only — not on every compaction, which is frequent in a long session
// and would spend the 5s hook budget on directory walks.
//
// maxDeletes caps the first sweep on a machine that already accumulated many
// sessions. TTL is overridable via env so tests need not fake two weeks.
const SESSION_TTL_MS = 14 * 24 * 60 * 60 * 1000;

function gcSessionStore(claudeDir, opts) {
  const options = opts || {};
  const envTtl = Number(process.env.CAVEMAN_SESSION_TTL_MS);
  const maxAgeMs = options.maxAgeMs
    || (Number.isFinite(envTtl) && envTtl > 0 ? envTtl : SESSION_TTL_MS);
  const maxDeletes = options.maxDeletes || 500;

  try {
    const dir = sessionsDir(claudeDir);
    const entries = fs.readdirSync(dir);
    const cutoff = Date.now() - maxAgeMs;
    let deleted = 0;
    for (const name of entries) {
      if (deleted >= maxDeletes) break;
      const p = path.join(dir, name);
      try {
        const st = fs.lstatSync(p);
        // Refuse symlinks rather than following them into someone else's file.
        if (st.isSymbolicLink() || !st.isFile()) continue;
        if (st.mtimeMs < cutoff) { fs.unlinkSync(p); deleted++; }
      } catch (e) { /* vanished or unreadable — skip */ }
    }
    return deleted;
  } catch (e) {
    // Missing directory (fresh install) or any fs failure — nothing to do.
    return 0;
  }
}

// Mode-transition log (#601). Whenever the active mode actually changes,
// append {ts, mode, prev, session_id} to
// $CLAUDE_CONFIG_DIR/.caveman-mode-log.jsonl so caveman-stats can attribute
// output tokens to the mode that was active when each message was generated,
// instead of whatever mode the flag holds at stats time. mode/prev are a
// VALID_MODES string or null (null = caveman off). prev lets stats attribute
// messages that predate the first logged transition of a session. No-op when
// the mode is unchanged; best-effort like all flag IO.
//
// session_id is omitted (not null) when unknown, so JSON.stringify drops the
// key and readers written before this field existed see the old shape.
const MODE_LOG_BASENAME = '.caveman-mode-log.jsonl';

// Normalizing through offToNull before comparing matters here: without it a
// durable-off write looks like a transition from 'off' to null and lands a
// phantom entry in the log — and attributing output tokens to a mode named
// 'off' would be meaningless besides.
function recordModeChange(claudeDir, newMode, sessionId) {
  try {
    const current = offToNull(readSessionModeRaw(claudeDir, sessionId));
    const next = offToNull(newMode);
    if (current === next) return;
    const entry = { ts: Date.now(), mode: next, prev: current };
    const sid = validateSessionId(sessionId);
    if (sid) entry.session_id = sid;
    appendFlag(path.join(claudeDir, MODE_LOG_BASENAME), JSON.stringify(entry));
  } catch (e) {
    // Silent fail — the log is best-effort
  }
}

// Symlink-safe history read. Returns lines (untrimmed) or empty array on any
// anomaly. Caller is responsible for parsing JSON. Does NOT enforce a size cap
// the way readFlag does — history is expected to grow with use.
function readHistory(filePath) {
  try {
    const st = fs.lstatSync(filePath);
    if (st.isSymbolicLink() || !st.isFile()) return [];
    const O_NOFOLLOW = typeof fs.constants.O_NOFOLLOW === 'number' ? fs.constants.O_NOFOLLOW : 0;
    const flags = fs.constants.O_RDONLY | O_NOFOLLOW;
    let fd;
    let raw;
    try {
      fd = fs.openSync(filePath, flags);
      raw = fs.readFileSync(fd, 'utf8');
    } finally {
      if (fd !== undefined) fs.closeSync(fd);
    }
    return raw.split('\n').filter(line => line.trim());
  } catch (e) {
    return [];
  }
}

// ---------------------------------------------------------------------------
// Caveman ruleset — SKILL.md read + intensity filter
//
// SKILL.md is the single source of truth for caveman behavior, and BOTH loaders
// need it filtered to one level. caveman-activate.js injects it at SessionStart;
// caveman-mode-tracker.js re-injects it when the user switches level mid-session
// (#975). Before that, a switch moved the banner and the stored mode while the
// model kept whatever level SessionStart had given it — `/caveman ultra` was a
// no-op on behavior.
//
// It lives here rather than in a new `caveman-ruleset.js` sibling for the reason
// the "Keep mode-state logic in caveman-config.js" rule gives: the hook file set
// is pinned in three places (checksums.sha256, verify_repo.py, the install
// lists) and src/plugins/opencode/plugin.js can only evaluate files it knows by
// name, so a second shared sibling is a standing drift risk. This module is
// already the one file every loader resolves.

// The wenyan storage alias: config stores wenyan-full as 'wenyan', while
// SKILL.md spells the row 'wenyan-full'. Filtering on the raw stored value
// would match no row and emit a ruleset with no intensity line at all.
function canonicalModeLabel(mode) {
  return mode === 'wenyan' ? 'wenyan-full' : mode;
}

// Candidate locations, tried in order (#587/#589 — the old single '..' path
// resolved to <plugin_root>/src/skills/, which does not exist, so plugin
// installs silently used the stale fallback ruleset):
//   1. $CLAUDE_PLUGIN_ROOT/skills/caveman/SKILL.md — Claude Code sets
//      CLAUDE_PLUGIN_ROOT when invoking plugin hooks; authoritative when present.
//   2. ../../skills/caveman/SKILL.md — hook at <plugin_root>/src/hooks/
//      (plugin.json layout) or a repo checkout.
//   3. ../skills/caveman/SKILL.md — standalone install with hooks at
//      $CLAUDE_CONFIG_DIR/hooks/ and the skill at
//      $CLAUDE_CONFIG_DIR/skills/caveman/.
function skillPathCandidates(hookDir) {
  const dir = hookDir || __dirname;
  const candidates = [];
  if (process.env.CLAUDE_PLUGIN_ROOT) {
    candidates.push(path.join(process.env.CLAUDE_PLUGIN_ROOT, 'skills', 'caveman', 'SKILL.md'));
  }
  candidates.push(
    path.join(dir, '..', '..', 'skills', 'caveman', 'SKILL.md'),
    path.join(dir, '..', 'skills', 'caveman', 'SKILL.md')
  );
  return candidates;
}

// Reads SKILL.md and keeps only `mode`'s row of the intensity table and only
// its example lines, so the model is never handed a second level's rules to
// choose between. Returns null when SKILL.md cannot be read from any candidate
// — callers decide what to do with that (activate.js has a hardcoded fallback
// ruleset; the tracker degrades to its one-line reinforcement).
function loadFilteredRuleset(mode, hookDir) {
  const modeLabel = canonicalModeLabel(mode);
  let skillContent = '';
  for (const candidate of skillPathCandidates(hookDir)) {
    try {
      skillContent = fs.readFileSync(candidate, 'utf8');
      break;
    } catch (e) { /* try next candidate */ }
  }
  if (!skillContent) return null;

  // Strip YAML frontmatter
  const body = skillContent.replace(/^---[\s\S]*?---\s*/, '');

  const filtered = body.split('\n').reduce((acc, line) => {
    // Intensity table rows start with | **level** |
    const tableRowMatch = line.match(/^\|\s*\*\*(\S+?)\*\*\s*\|/);
    if (tableRowMatch) {
      // Keep only the active level's row (and always keep header/separator)
      if (tableRowMatch[1] === modeLabel) {
        acc.push(line);
      }
      return acc;
    }

    // Example lines start with "- level:" — keep only lines matching active level
    const exampleMatch = line.match(/^- (\S+?):\s/);
    if (exampleMatch) {
      if (exampleMatch[1] === modeLabel) {
        acc.push(line);
      }
      return acc;
    }

    acc.push(line);
    return acc;
  }, []);

  return filtered.join('\n');
}

// The banner both loaders put above the ruleset, so the label the model reads
// cannot drift between SessionStart and a mid-session switch.
function rulesetBanner(mode) {
  return 'CAVEMAN MODE ACTIVE — level: ' + canonicalModeLabel(mode);
}

module.exports = {
  getDefaultMode, getConfigDir, getConfigPath, findRepoConfigPath, VALID_MODES,
  safeWriteFlag, readFlag, appendFlag, readHistory,
  recordModeChange, MODE_LOG_BASENAME,
  // Per-session state
  SESSIONS_DIRNAME, FLAG_BASENAME, PREV_BASENAME,
  validateSessionId, legacyFlagPath, sessionsDir,
  sessionActivePath, sessionPrevPath,
  resolveActiveMode, readSessionModeRaw, writeSessionMode,
  writeSessionPrev, readSessionPrev, clearSessionPrev,
  gcSessionStore,
  // Ruleset injection
  canonicalModeLabel, loadFilteredRuleset, rulesetBanner,
};
