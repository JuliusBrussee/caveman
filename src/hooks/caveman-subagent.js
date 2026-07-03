#!/usr/bin/env node
// caveman — Claude Code SubagentStart hook
//
// SessionStart context is parent-thread only and never reaches subagents, so
// without this every Task-spawned agent runs without caveman rules and emits
// verbose output. When caveman mode is active, re-inject the same ruleset into
// each subagent.
//
// Pattern ported from ponytail (MIT, DietrichGebert/ponytail).

const fs = require('fs');
const path = require('path');
const os = require('os');
const { readFlag } = require('./caveman-config');

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.caveman-active');

// Modes handled by their own skill files — not caveman intensity levels.
// For these the base caveman rules would conflict with the skill's own behavior.
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

// readFlag is symlink-safe, size-capped, and VALID_MODES-whitelisted. Returns
// null when the flag is absent, a symlink, oversized, or an unrecognised value.
const mode = readFlag(flagPath);

// Absent flag or off → caveman isn't active; inject nothing.
if (!mode || mode === 'off') {
  process.exit(0);
}

// Independent modes have their own skill behavior; skip re-injection.
if (INDEPENDENT_MODES.has(mode)) {
  process.exit(0);
}

try {
  // Resolve the canonical label for the wenyan alias.
  const modeLabel = mode === 'wenyan' ? 'wenyan-full' : mode;

  // Read SKILL.md — the single source of truth for caveman behavior.
  // Plugin installs: __dirname = <plugin_root>/src/hooks/,
  //   SKILL.md at <plugin_root>/skills/caveman/SKILL.md
  // Standalone installs: __dirname = $CLAUDE_CONFIG_DIR/hooks/,
  //   SKILL.md won't exist — fall back to compact inline rules.
  let skillContent = '';
  try {
    skillContent = fs.readFileSync(
      path.join(__dirname, '..', 'skills', 'caveman', 'SKILL.md'), 'utf8'
    );
  } catch (e) { /* standalone install — will use fallback below */ }

  let instructions;

  if (skillContent) {
    // Strip YAML frontmatter.
    const body = skillContent.replace(/^---[\s\S]*?---\s*/, '');

    // Filter intensity table: keep header rows + only the active level's row.
    const filtered = body.split('\n').reduce((acc, line) => {
      // Intensity table rows start with | **level** |
      const tableRowMatch = line.match(/^\|\s*\*\*(\S+?)\*\*\s*\|/);
      if (tableRowMatch) {
        if (tableRowMatch[1] === modeLabel) {
          acc.push(line);
        }
        return acc;
      }

      // Example lines start with "- level:" — keep only lines matching active level.
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

    instructions = 'CAVEMAN MODE ACTIVE — level: ' + modeLabel + '\n\n' + filtered.join('\n');
  } else {
    // Compact fallback for standalone hook installs without a skills directory.
    instructions =
      'CAVEMAN MODE ACTIVE — level: ' + modeLabel + '\n\n' +
      'Respond terse like smart caveman. All technical substance stay. Only fluff die.\n\n' +
      'Drop: articles (a/an/the), filler (just/really/basically/actually/simply), ' +
      'pleasantries (sure/certainly/of course/happy to), hedging. ' +
      'Fragments OK. Short synonyms. Technical terms exact. ' +
      'Code blocks unchanged. Errors quoted exact.\n\n' +
      'ACTIVE EVERY RESPONSE. Off only: "stop caveman" / "normal mode". ' +
      'Level persist until changed or session end.';
  }

  // SubagentStart requires the hookSpecificOutput JSON form — raw stdout is
  // dropped by the Claude Code runtime for this event type.
  process.stdout.write(JSON.stringify({
    hookSpecificOutput: {
      hookEventName: 'SubagentStart',
      additionalContext: instructions,
    },
  }));
} catch (e) {
  // Silent fail — a hook crash must never block a subagent spawn.
}
