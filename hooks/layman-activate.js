#!/usr/bin/env node
// layman — Claude Code SessionStart activation hook
//
// Runs on every session start:
//   1. Writes flag file at $CLAUDE_CONFIG_DIR/.layman-active (statusline reads this)
//   2. Emits Layman ruleset as hidden SessionStart context
//   3. Detects missing statusline config and emits setup nudge

const fs = require('fs');
const path = require('path');
const os = require('os');
const { getDefaultMode, safeWriteFlag } = require('./layman-config');

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.layman-active');
const settingsPath = path.join(claudeDir, 'settings.json');

const mode = getDefaultMode();

// "off" mode — skip activation entirely, don't write flag or emit rules
if (mode === 'off') {
  try { fs.unlinkSync(flagPath); } catch (e) {}
  process.stdout.write('OK');
  process.exit(0);
}

// 1. Write flag file (symlink-safe)
safeWriteFlag(flagPath, mode);

// 2. Emit full Layman ruleset. The rules are intentionally explicit because
//    final-response style is easy for models to lose after other tools inject
//    competing instructions.
//
//    Reads SKILL.md at runtime so edits to the source of truth propagate
//    automatically — no hardcoded duplication to go stale.

// Modes that have their own independent skill files — not Layman response modes.
// For these, emit a short activation line; the skill itself handles behavior.
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);
const BRIEF_MODES = new Set([
  'brief', 'lite', 'full', 'ultra',
  'wenyan-lite', 'wenyan', 'wenyan-full', 'wenyan-ultra'
]);

if (INDEPENDENT_MODES.has(mode)) {
  process.stdout.write('LAYMAN MODE ACTIVE — mode: ' + mode + '. Behavior defined by matching Layman skill.');
  process.exit(0);
}

const isBriefMode = BRIEF_MODES.has(mode);
const modeLabel = isBriefMode
  ? (mode === 'wenyan' ? 'wenyan-full' : mode)
  : (mode === 'explain' ? 'explain' : 'summary');

// Read SKILL.md — the single source of truth for active behavior.
// Plugin installs: __dirname = <plugin_root>/hooks/, SKILL.md at <plugin_root>/skills/<name>/SKILL.md
// Standalone installs: __dirname = $CLAUDE_CONFIG_DIR/hooks/, SKILL.md won't exist — falls back to hardcoded rules.
let skillContent = '';
try {
  skillContent = fs.readFileSync(
    path.join(__dirname, '..', 'skills', 'layman', 'SKILL.md'), 'utf8'
  );
} catch (e) { /* standalone install — will use fallback below */ }

let output;

if (skillContent) {
  // Strip YAML frontmatter
  const body = skillContent.replace(/^---[\s\S]*?---\s*/, '');
  output = 'LAYMAN MODE ACTIVE — mode: ' + modeLabel + '\n\n' + body;
} else {
  // Fallback when SKILL.md is not found (standalone hook install without skills dir).
  // This is the minimum viable ruleset — better than nothing.
  if (isBriefMode) {
    output =
      'LAYMAN MODE ACTIVE — mode: ' + modeLabel + '\n\n' +
      'Respond with token-saving brevity. Cut filler, pleasantries, hedging, and repeated setup. ' +
      'Keep technical terms exact. Code unchanged. Write normal clear prose for security warnings and irreversible actions.';
  } else {
    output =
      'LAYMAN MODE ACTIVE — mode: ' + modeLabel + '\n\n' +
      'When coding work is complete, respond with a plain-English handoff.\n\n' +
      '## Rules\n\n' +
      'Use Layman Summary with Done, Why it matters, What changed, Check this, and Warning only if needed. ' +
      'Simple, calm, human, clear. Accurate, not babyish. Keep important technical terms when needed. ' +
      'Do not hide risks or claim tests passed unless they were run.';
  }
}

// 3. Detect missing statusline config — nudge Claude to help set it up
try {
  let hasStatusline = false;
  if (fs.existsSync(settingsPath)) {
    const settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
    if (settings.statusLine) {
      hasStatusline = true;
    }
  }

  if (!hasStatusline) {
    const isWindows = process.platform === 'win32';
    const scriptName = isWindows ? 'layman-statusline.ps1' : 'layman-statusline.sh';
    const scriptPath = path.join(__dirname, scriptName);
    const command = isWindows
      ? `powershell -ExecutionPolicy Bypass -File "${scriptPath}"`
      : `bash "${scriptPath}"`;
    const statusLineSnippet =
      '"statusLine": { "type": "command", "command": ' + JSON.stringify(command) + ' }';
    output += "\n\n" +
      "STATUSLINE SETUP NEEDED: The Layman plugin includes a statusline badge showing active mode " +
      "(e.g. [LAYMAN], [LAYMAN:EXPLAIN], [LAYMAN:ULTRA]). It is not configured yet. " +
      "To enable, add this to " + path.join(claudeDir, 'settings.json') + ": " +
      statusLineSnippet + " " +
      "Proactively offer to set this up for the user on first interaction.";
  }
} catch (e) {
  // Silent fail — don't block session start over statusline detection
}

process.stdout.write(output);
