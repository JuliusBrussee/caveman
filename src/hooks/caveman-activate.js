#!/usr/bin/env node
// caveman — Claude Code SessionStart activation hook
//
// Runs on every session start (startup, resume, compact, clear):
//   1. Reads stdin JSON for session_id and source
//   2. Writes per-session flag file (conditional on source)
//   3. Writes global flag file (best-effort for external tools)
//   4. Emits caveman ruleset as hidden SessionStart context
//   5. Detects missing statusline config and emits setup nudge
//   6. Cleans up stale per-session flag files on fresh startup

const fs = require('fs');
const path = require('path');
const os = require('os');
const { getDefaultMode, safeWriteFlag, readFlag, getFlagPath, getGlobalFlagPath, safeUnlinkFlag, cleanupStaleFlagFiles } = require('./caveman-config');

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const settingsPath = path.join(claudeDir, 'settings.json');

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  let sessionId = null;
  let source = 'startup';
  try {
    const data = JSON.parse(input);
    sessionId = data.session_id || null;
    source = data.source || 'startup';
  } catch (e) {
    // No stdin or invalid JSON — proceed with defaults
  }

  const mode = getDefaultMode();
  const perSessionFlagPath = getFlagPath(claudeDir, sessionId);
  const globalFlagPath = getGlobalFlagPath(claudeDir);

  // "off" mode — skip activation entirely
  if (mode === 'off') {
    safeUnlinkFlag(perSessionFlagPath);
    safeUnlinkFlag(globalFlagPath);
    process.stdout.write('OK');
    return;
  }

  // Per-session flag: conditional on source.
  // On compact/resume, preserve existing per-session mode (the core fix for #184/#334).
  // On startup/clear, write (or overwrite) with configured default.
  if (source === 'startup' || source === 'clear') {
    safeWriteFlag(perSessionFlagPath, mode);
  } else {
    // compact or resume — only write if per-session flag doesn't exist yet
    const existing = readFlag(perSessionFlagPath);
    if (!existing) {
      safeWriteFlag(perSessionFlagPath, mode);
    }
  }

  // Global flag: always written (best-effort for external tools, last-writer-wins)
  safeWriteFlag(globalFlagPath, mode);

  // Stale file cleanup — only on fresh startup
  if (source === 'startup') {
    cleanupStaleFlagFiles(claudeDir, 24 * 60 * 60 * 1000);
  }

  // Modes that have their own independent skill files — not caveman intensity levels.
  // For these, emit a short activation line; the skill itself handles behavior.
  const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

  if (INDEPENDENT_MODES.has(mode)) {
    process.stdout.write('CAVEMAN MODE ACTIVE — level: ' + mode + '. Behavior defined by /caveman-' + mode + ' skill.');
    return;
  }

  // Resolve the canonical label for wenyan alias
  const modeLabel = mode === 'wenyan' ? 'wenyan-full' : mode;

  // Read SKILL.md — the single source of truth for caveman behavior.
  // Plugin installs: __dirname = <plugin_root>/hooks/, SKILL.md at <plugin_root>/skills/caveman/SKILL.md
  // Standalone installs: __dirname = $CLAUDE_CONFIG_DIR/hooks/, SKILL.md won't exist — falls back to hardcoded rules.
  let skillContent = '';
  try {
    skillContent = fs.readFileSync(
      path.join(__dirname, '..', 'skills', 'caveman', 'SKILL.md'), 'utf8'
    );
  } catch (e) { /* standalone install — will use fallback below */ }

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
      'Pattern: `[thing] [action] [reason]. [next step].`\n\n' +
      'Not: "Sure! I\'d be happy to help you with that. The issue you\'re experiencing is likely caused by..."\n' +
      'Yes: "Bug in auth middleware. Token expiry check use `<` not `<=`. Fix:"\n\n' +
      '## Auto-Clarity\n\n' +
      'Drop caveman for: security warnings, irreversible action confirmations, multi-step sequences where fragment order risks misread, user asks to clarify or repeats question. Resume caveman after clear part done.\n\n' +
      '## Boundaries\n\n' +
      'Code/commits/PRs: write normal. "stop caveman" or "normal mode": revert. Level persist until changed or session end.';
  }

  // Detect missing statusline config — nudge Claude to help set it up
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
});
