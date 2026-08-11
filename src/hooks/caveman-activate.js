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
const { getDefaultMode, safeWriteFlag, recordModeChange, sanitizeSessionId, flagBaseName, resolveFlag } = require('./caveman-config');

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const settingsPath = path.join(claudeDir, 'settings.json');

// Apply per-agent model overrides from env vars before emitting rules.
// Best-effort: any error is swallowed so SessionStart is never blocked.
try {
  const { applyOverrides, resolvePluginRoot } = require('./cavecrew-model-overrides');
  applyOverrides(resolvePluginRoot(__dirname));
} catch (e) {}

// SessionStart re-fires mid-conversation (resume, /clear, context compaction),
// not just at true session start.
// Sync stdin read assumes the parent (Claude Code) writes the payload and
// closes the pipe — it always does. A parent that held the pipe open forever
// would block here; no such caller exists, and a TTY (manual run) skips it.
let source = 'startup';
let sessionId = null;
try {
  if (!process.stdin.isTTY) {
    const raw = fs.readFileSync(0, 'utf8');
    if (raw) {
      const data = JSON.parse(raw);
      if (data && typeof data.source === 'string') source = data.source;
      if (data) sessionId = sanitizeSessionId(data.session_id);
    }
  }
} catch (e) { /* no/bad stdin → treat as startup */ }

// Write-target resolution (session-sync-with-opt-in-isolation, T1 v3 High
// fix): the OLD logic computed the write target from flagBaseName(sessionId)
// unconditionally, before branching on `source` — meaning EVERY event type,
// not just true startup, always wrote to the session's own scoped path. That
// silently isolated a session on its very first resume/compact/clear re-fire
// even when it had never touched anything (and would undo /caveman default's
// own unlink on the very next re-fire). Fixed: resolveFlag is called
// unconditionally, for every event type, and the write target is ALWAYS
// `resolved.path` — scoped if this session already has scoped identity
// (from an earlier /caveman <level> or /caveman default not yet run this
// turn), legacy otherwise. This also means an already-isolated session stays
// isolated across a process restart (`claude --resume`), not just a
// mid-conversation re-fire, because the branch keys on path equality rather
// than on `source`.
const resolved = resolveFlag(claudeDir, sessionId);
const legacyFlagPath = path.join(claudeDir, flagBaseName(null));

let mode;
let writeFlagPath = resolved.path;

if (resolved.rejected) {
  mode = 'off'; // fail closed -- an existing-but-rejected scoped file must
                 // NOT silently reactivate at the configured default.
                 // resolveState guarantees resolved.path is the scoped path
                 // whenever rejected is true.
} else if (source === 'startup' && resolved.path === legacyFlagPath) {
  // True startup AND no scoped identity: refresh the SHARED value from
  // config, exactly matching upstream's pre-scoping mechanism (verified via
  // `git show ec83e5b:src/hooks/caveman-activate.js`) — this is the only
  // case that ever re-derives mode from getDefaultMode() rather than
  // preserving whatever resolveFlag found.
  mode = getDefaultMode();
} else {
  // Any event on an already-isolated session, or a non-startup event on a
  // synced session: preserve exactly what resolveFlag resolved. A synced
  // session with no legacy value yet either (fresh install, first hook fire
  // happens to not be 'startup') falls back to getDefaultMode() for THIS
  // session's own ruleset emission only — writeFlagPath stays the legacy
  // path either way, so nothing gets scoped.
  mode = resolved.mode || getDefaultMode();
}

// "off" mode — skip activation entirely, don't emit rules (still records +
// writes, so the transition is logged and the flag reflects reality).
if (mode === 'off') {
  recordModeChange(claudeDir, null, sessionId); // #601: timestamped transition log
  safeWriteFlag(writeFlagPath, 'off');
  process.stdout.write('OK');
  process.exit(0);
}

// 1. Write flag file (symlink-safe). Record BEFORE write — recordModeChange
// reads the PRE-write value via its own resolveFlag call as `current`;
// writing first would make current === next always and silently suppress
// every mode-log entry this hook is meant to produce.
recordModeChange(claudeDir, mode, sessionId); // #601
safeWriteFlag(writeFlagPath, mode);

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
    'Current level: **' + modeLabel + '**. Switch: `/caveman lite|full|ultra`.\n\n' +
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
