#!/usr/bin/env node
// caveman — Claude Code SessionStart activation hook
//
// Runs on every session start:
//   1. Writes flag file at $CLAUDE_CONFIG_DIR/.caveman-active (statusline reads this)
//   2. Emits caveman ruleset as hidden SessionStart context
//   3. Detects missing statusline config and emits setup nudge

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

function removeFlag(path) {
  try {
    fs.unlinkSync(path);
  } catch (error) {
    if (process.env.CAVEMAN_DEBUG === '1' && error.code !== 'ENOENT') {
      console.error(`caveman: failed to remove flag ${path}: ${error.message}`);
    }
  }
}

// Apply per-agent model overrides from env vars before emitting rules.
// Best-effort: any error is swallowed so SessionStart is never blocked.
try {
  const { applyOverrides, resolvePluginRoot } = require('./cavecrew-model-overrides');
  applyOverrides(resolvePluginRoot(__dirname));
} catch (e) {}

// SessionStart re-fires mid-conversation (resume, /clear, context compaction),
// not just at true session start. Re-firing must not clobber a mode the user
// switched to mid-session (#691): branch on the hook payload's `source` field —
// only a real `startup` resets to the configured default; resume/clear/compact
// preserve a valid existing flag.
// Sync stdin read assumes the parent (Claude Code) writes the payload and
// closes the pipe — it always does. A parent that held the pipe open forever
// would block here; no such caller exists, and a TTY (manual run) skips it.
let source = 'startup';
try {
  if (!process.stdin.isTTY) {
    const raw = fs.readFileSync(0, 'utf8');
    if (raw) {
      const data = JSON.parse(raw);
      if (data && typeof data.source === 'string') source = data.source;
    }
  }
} catch (e) { /* no/bad stdin → treat as startup */ }

let mode = getDefaultMode();
if (source !== 'startup') {
  const existing = readFlag(flagPath);
  if (existing && VALID_MODES.includes(existing)) mode = existing;
}

// "off" mode — skip activation entirely, don't write flag or emit rules
if (mode === 'off') {
  recordModeChange(claudeDir, null); // #601: timestamped transition log
  removeFlag(flagPath);
  process.stdout.write('OK');
  process.exit(0);
}

// 1. Write flag file (symlink-safe)
recordModeChange(claudeDir, mode); // #601
safeWriteFlag(flagPath, mode);

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

// Resolve the canonical label for wenyan alias
const modeLabel = mode === 'wenyan' ? 'wenyan-full' : mode;

// Read SKILL.md — the single source of truth for caveman behavior.
// Candidate locations, tried in order (#587/#589 — the old single '..' path
// resolved to <plugin_root>/src/skills/, which doesn't exist, so plugin
// installs silently used the stale fallback ruleset):
//   1. $CLAUDE_PLUGIN_ROOT/skills/caveman/SKILL.md — Claude Code sets
//      CLAUDE_PLUGIN_ROOT when invoking plugin hooks; authoritative when present.
//   2. ../../skills/caveman/SKILL.md — hook at <plugin_root>/src/hooks/
//      (plugin.json layout) or a repo checkout.
//   3. ../skills/caveman/SKILL.md — standalone install with hooks at
//      $CLAUDE_CONFIG_DIR/hooks/ and the skill at $CLAUDE_CONFIG_DIR/skills/caveman/.
// All misses fall through to the hardcoded fallback ruleset below.
const skillCandidates = [];
if (process.env.CLAUDE_PLUGIN_ROOT) {
  skillCandidates.push(path.join(process.env.CLAUDE_PLUGIN_ROOT, 'skills', 'caveman', 'SKILL.md'));
}
skillCandidates.push(
  path.join(__dirname, '..', '..', 'skills', 'caveman', 'SKILL.md'),
  path.join(__dirname, '..', 'skills', 'caveman', 'SKILL.md')
);

let skillContent = '';
for (const candidate of skillCandidates) {
  try {
    skillContent = fs.readFileSync(candidate, 'utf8');
    break;
  } catch (e) { /* try next candidate */ }
}

let output;

if (skillContent) {
  // Strip YAML frontmatter
  const body = skillContent.replace(/^---[\s\S]*?---\s*/, '');

  // Filter intensity table: keep header rows + only the active level's row
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

  output = 'CAVEMAN MODE ACTIVE — level: ' + modeLabel + '\n\n' + filtered.join('\n');
} else {
  // Fallback when SKILL.md is not found (standalone hook install without skills dir).
  // This is the minimum viable ruleset — better than nothing.
  output =
    'CAVEMAN MODE ACTIVE — level: ' + modeLabel + '\n\n' +
    'Respond terse like smart caveman. All technical substance stay. Only fluff die.\n\n' +
    '## Persistence\n\n' +
    'ACTIVE EVERY RESPONSE. No revert after many turns. No filler drift. Still active if unsure. Off only: "stop caveman" / "normal mode".\n\n' +
    'Current level: **' + modeLabel + '**. Switch: `/caveman lite|full|ultra|wenyan-lite|wenyan-full|wenyan-ultra`.\n\n' +
    '## Rules\n\n' +
    'Drop: articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries (sure/certainly/of course/happy to), hedging. ' +
    'Fragments OK. Short synonyms (big not extensive, fix not "implement a solution for"). Technical terms exact. Code blocks unchanged. Errors quoted exact.\n\n' +
    "Preserve user's dominant language. User write Portuguese → reply Portuguese caveman. Compress the style, not the language. Technical terms, code, API names, commands, error strings stay verbatim.\n\n" +
    'No self-reference. Never name or announce the style. No "caveman mode on" tags. Output caveman-only.\n\n' +
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
    const settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
    if (settings.statusLine) {
      hasStatusline = true;
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
