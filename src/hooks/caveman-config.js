#!/usr/bin/env node
// caveman — shared configuration resolver
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

// Mode aliases → canonical names. Canonical set matches SKILL.md's six levels
// (lite/full/ultra, wenyan-lite/wenyan-full/wenyan-ultra); 'wenyan' is a
// user-facing shorthand for wenyan-full. Normalization lives here so every
// entry path (slash arg, env var, config file, flag read) agrees — previously
// the alias was applied only in the two arg parsers, so CAVEMAN_DEFAULT_MODE
// or a config file could store 'wenyan-full' un-aliased while
// '/caveman wenyan-full' stored 'wenyan', and the statusline badge and
// per-turn reinforcement diverged depending on which path set the mode.
// 'wenyan' stays in VALID_MODES so legacy flag files still validate; readFlag
// normalizes them on the way out.
const MODE_ALIASES = { 'wenyan': 'wenyan-full' };

function normalizeMode(mode) {
  if (!mode) return mode;
  const raw = String(mode).trim().toLowerCase();
  return MODE_ALIASES[raw] || raw;
}

// Polite request wrappers. "can you stop caveman" and "could you use caveman
// mode?" are commands wearing a question mark, not questions about caveman.
// Stripping the wrapper before classification lets one set of imperative
// patterns serve both the bare form ("stop caveman") and the polite form,
// and keeps the question guard for genuine questions ("what is caveman
// mode?", "can you explain caveman mode?" → "explain caveman mode").
const POLITE_PREFIX =
  /^(?:(?:hey|hi|ok|okay|yo)[,!]?\s+)?(?:please[,]?\s+)?(?:(?:can|could|would|will)\s+(?:you|we|u)\s+(?:please\s+)?|let'?s\s+|i(?:'d|\s+would)\s+like\s+(?:you\s+)?to\s+|i\s+want\s+(?:you\s+)?to\s+|please\s+)/;

function stripPolitePrefix(s) {
  let out = s;
  for (let i = 0; i < 3; i++) {
    const next = out.replace(POLITE_PREFIX, '');
    if (next === out) break;
    out = next;
  }
  return out.trim();
}

// Natural-language on/off classifier — single source shared by the Claude
// Code UserPromptSubmit hook (caveman-mode-tracker.js) and the opencode
// plugin (src/plugins/opencode/plugin.js). Both previously carried their own
// regex copies, which drifted and shared the same bugs: negated prompts
// ("don't use caveman") activated, questions mentioning caveman and "normal
// mode" deactivated, and "stop using caveman" was a no-op because the off
// verbs required the word 'caveman' to be adjacent.
//
// Returns { wantsOn, wantsOff, isQuestion }. wantsOn and wantsOff are
// mutually exclusive; both false means the prompt doesn't change state
// (including negated prompts — a scoped "don't use caveman for this"
// should neither activate nor kill an active session-wide mode).
function classifyPrompt(promptRaw) {
  // Collapse whitespace so phrase triggers still match multiline prompts —
  // every regex below sees a single-line prompt (#598).
  const prompt = String(promptRaw == null ? '' : promptRaw)
    .trim().toLowerCase().replace(/\s+/g, ' ');

  // Every pattern below runs against the de-politened text so "can you
  // switch back to normal mode?" reaches the prompt-initial "normal mode"
  // command pattern and "can you talk like a caveman?" reaches activation.
  const core = stripPolitePrefix(prompt);

  const mentionsCaveman = /\bcaveman\b/.test(core);

  const isQuestion =
    /^(what|whats|what's|how|why|when|where|who|does|do|did|is|are|can|could|would|should|tell me|explain)\b/.test(core);

  // Negated deactivation: "don't turn off caveman", "do not disable
  // caveman", "please don't disable caveman when I paste code". These read
  // as *keep it on* — they must never trip the off path.
  const negatedOff =
    /\b(?:don'?t|do\s+not|dont|never|avoid|rather\s+not|no\s+need\s+to)\s+(?:[a-z'-]+\s+){0,3}?(?:stop|disable|deactivate|quit|kill|turn\s+off|switch\s+back|go\s+back|revert)\b/.test(core);

  // Strong off: an explicit deactivation verb aimed at caveman. Honored even
  // in question form — "can you stop caveman" is a polite command, not a
  // question about caveman. Off verbs tolerate 'using'/'talking like' and a
  // determiner between verb and 'caveman' ("stop using caveman", "stop
  // talking like a caveman", "disable that caveman thing").
  const strongOff =
    /\b(stop|disable|deactivate|quit|exit|kill)\s+(?:using\s+|talking\s+like\s+)?(?:the\s+|a\s+|that\s+|this\s+|your\s+)?caveman\b/.test(core) ||
    /\bcaveman(\s+mode)?\s+(off|stop|disabled?)\b/.test(core) ||
    /\bturn\s+off\s+(?:the\s+|that\s+|this\s+|your\s+)?caveman\b/.test(core) ||
    // Pronoun reference back to a caveman mention in the same prompt:
    // "caveman is annoying, please turn it off".
    (mentionsCaveman && /\b(turn|shut|switch)\s+(?:it|that|this)\s+off\b/.test(core)) ||
    (mentionsCaveman && /\b(stop|disable|deactivate|kill)\s+(?:it|that|this)\b/.test(core)) ||
    // "normal mode" only as a command: prompt-initial (after the polite
    // wrapper is stripped), optionally led by a switch-back verb — never
    // mid-sentence for e.g. vim's normal mode ("how do I exit vim normal
    // mode").
    /^(please\s+)?(go\s+|back\s+to\s+|switch\s+(back\s+)?to\s+|return\s+to\s+|revert\s+to\s+)?normal\s+mode\b/.test(core);

  // Contrastive "normal mode": the phrase names the thing being rejected,
  // not requested — "use caveman instead of normal mode", "switch to caveman
  // mode, not normal mode", "caveman is better than normal mode". None of
  // these are deactivation.
  const contrastiveNormal =
    /\b(?:instead\s+of|rather\s+than|than|as\s+opposed\s+to|not|over|versus|vs\.?)\s+(?:the\s+|a\s+|plain\s+|regular\s+|usual\s+)*normal\s+mode\b/.test(core);

  // Weak off: "normal mode" co-occurring with caveman anywhere. Question-
  // guarded so "what is the difference between caveman mode and normal
  // mode?" doesn't silently deactivate — questions about modes are not
  // commands.
  const weakOff = /\bnormal\s+mode\b/.test(core) && mentionsCaveman && !contrastiveNormal;

  const wantsOff = (strongOff || (weakOff && !isQuestion)) && !negatedOff;

  // Negation guard: "don't use caveman", "never talk like caveman", "please
  // do not use caveman mode" must not activate. A negator within a few words
  // before the activation verb suppresses activation entirely.
  const negatedOn =
    /\b(don'?t|do\s+not|dont|not|never|avoid|without|no\s+need\s+to)\s+(?:[a-z'-]+\s+){0,4}?(activate|enable|start|turn\s+on|use|using|switch\s+to|talk(?:ing)?\s+like)\b[^.]{0,40}\bcaveman\b/.test(core);

  // Same guard for the brevity triggers, which carry no 'caveman' anchor:
  // "don't be brief, explain everything in detail", "no need to be brief".
  const negatedBrevity =
    /\b(don'?t|do\s+not|dont|not|never|avoid|no\s+need\s+to|no\s+need)\s+(?:[a-z'-]+\s+){0,3}?(be\s+brief|be\s+terse|less\s+tokens|fewer\s+tokens|shorter\s+answers)\b/.test(core);

  // Natural-language activation (e.g. "activate caveman", "turn on caveman
  // mode", "talk like caveman"). README tells users they can say these.
  // Also brevity requests ("less tokens", "be brief/terse", "fewer tokens",
  // "shorter answers") — but not when scoped to a single section
  // ("be brief in the summary"), which is a one-off instruction, not a
  // session-wide mode switch. Questions about caveman are not activation
  // commands ("what is caveman mode?").
  const wantsOn = !wantsOff && !negatedOn && !isQuestion && (
    /\b(activate|enable|start|turn on|use|switch to|want|give me)\b[^.]{0,40}\bcaveman\b/.test(core) ||
    /\btalk like\b[^.]{0,40}\bcaveman\b/.test(core) ||
    /\bcaveman\s+mode\s+(on|please|now)\b/.test(core) ||
    /^caveman(\s+mode)?\s*[.!]*$/.test(core) ||
    (!negatedBrevity &&
      /\b(less tokens|fewer tokens|be brief|be terse|shorter answers)\b(?!\s+(in|for|on|about|when|during|with)\b)/.test(core))
  );

  return { wantsOn, wantsOff, isQuestion };
}

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
      return normalizeMode(config.defaultMode);
    }
  } catch (e) {
    // Missing / unreadable / invalid JSON → caller falls through
  }
  return null;
}

function getDefaultMode() {
  // 1. Environment variable (highest priority)
  const envMode = process.env.CAVEMAN_DEFAULT_MODE;
  if (envMode && VALID_MODES.includes(envMode.toLowerCase())) {
    return normalizeMode(envMode);
  }

  // 2. Repo-local config (checked-in, per-project default)
  const repoConfigPath = findRepoConfigPath(process.cwd());
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

    const tempPath = path.join(realFlagDir, `.caveman-active.${process.pid}.${Date.now()}`);
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
    fs.renameSync(tempPath, realFlagPath);
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
    // Legacy flag files may still hold the 'wenyan' alias — hand every
    // reader the canonical name so badges and reinforcement agree.
    return normalizeMode(raw);
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

// Mode-transition log (#601). Whenever the active-mode flag actually changes,
// append {ts, mode, prev} to $CLAUDE_CONFIG_DIR/.caveman-mode-log.jsonl so
// caveman-stats can attribute output tokens to the mode that was active when
// each message was generated, instead of whatever mode the flag holds at
// stats time. mode/prev are a VALID_MODES string or null (null = caveman off).
// prev lets stats attribute messages that predate the first logged transition
// of a session. No-op when the mode is unchanged; best-effort like all flag IO.
const MODE_LOG_BASENAME = '.caveman-mode-log.jsonl';

function recordModeChange(claudeDir, newMode) {
  try {
    const current = readFlag(path.join(claudeDir, '.caveman-active'));
    const next = newMode || null;
    if ((current || null) === next) return;
    appendFlag(
      path.join(claudeDir, MODE_LOG_BASENAME),
      JSON.stringify({ ts: Date.now(), mode: next, prev: current || null })
    );
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

module.exports = { getDefaultMode, getConfigDir, getConfigPath, findRepoConfigPath, VALID_MODES, MODE_ALIASES, normalizeMode, classifyPrompt, safeWriteFlag, readFlag, appendFlag, readHistory, recordModeChange, MODE_LOG_BASENAME };
