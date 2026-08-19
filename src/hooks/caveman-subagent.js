#!/usr/bin/env node
// caveman — Claude Code SubagentStart hook
//
// SessionStart context is parent-thread only and never reaches Task-spawned
// subagents, so without this every subagent (Explore, custom agents, …) runs
// caveman-unaware and emits full-verbosity output — forfeiting the savings
// exactly where output volume is highest (#672). When caveman mode is active,
// re-inject the level-filtered ruleset into each spawned subagent.
//
// Pattern ported from ponytail (MIT, DietrichGebert/ponytail), which closed
// the same parent-only-context gap for its own mode.
//
// No stdin read: the SubagentStart payload isn't needed (every subagent gets
// the same currently-active mode), and waiting on stdin risks the Windows
// stall class where a shell wrapper swallows the pipe and 'end' never fires.

const fs = require('fs');
const path = require('path');
const os = require('os');

// Guarded sibling require (#848): an install missing caveman-config.js must
// degrade to "inject nothing", never to an uncaught MODULE_NOT_FOUND on every
// subagent spawn. The opencode layout renames siblings to .cjs — same retry
// caveman-parse.js does.
let readFlag;
try {
  ({ readFlag } = require('./caveman-config'));
} catch (e) {
  try { ({ readFlag } = require('./caveman-config.cjs')); } catch (e2) { /* degrade below */ }
}
if (typeof readFlag !== 'function') process.exit(0);

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');

// readFlag is symlink-safe, size-capped, and VALID_MODES-whitelisted. Returns
// null when the flag is absent, a symlink, oversized, or an unrecognised value.
const mode = readFlag(path.join(claudeDir, '.caveman-active'));

// Absent flag or off → caveman isn't active; inject nothing.
if (!mode || mode === 'off') process.exit(0);

// Modes owned by their own skill files (/caveman-commit etc.). The skill only
// exists in the parent session — a subagent never loaded it, so an activation
// line pointing at it would be an instruction the subagent can't follow.
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);
if (INDEPENDENT_MODES.has(mode)) process.exit(0);

function emit(text) {
  try {
    // Native Claude Code drops raw stdout on SubagentStart — the context must
    // arrive as hookSpecificOutput JSON or it is silently discarded.
    process.stdout.write(JSON.stringify({
      hookSpecificOutput: { hookEventName: 'SubagentStart', additionalContext: text },
    }));
  } catch (e) { /* a stdout error at hook exit must not fail the spawn */ }
}

// Resolve the canonical label for the wenyan alias.
const modeLabel = mode === 'wenyan' ? 'wenyan-full' : mode;

// Read SKILL.md — the single source of truth for caveman behavior. Same
// candidate order as caveman-activate.js (#587/#589 — a lone '..' lookup
// resolves to <plugin_root>/src/skills/, which doesn't exist, so plugin
// installs would silently serve the weak fallback):
//   1. $CLAUDE_PLUGIN_ROOT/skills/caveman/SKILL.md — authoritative when set.
//   2. ../../skills/caveman/SKILL.md — plugin layout / repo checkout.
//   3. ../skills/caveman/SKILL.md — standalone install under $CLAUDE_CONFIG_DIR.
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

if (!skillContent) {
  // Minimum viable ruleset when no SKILL.md is reachable.
  emit('CAVEMAN MODE ACTIVE — level: ' + modeLabel + '. Respond terse: drop filler, articles, pleasantries; keep all technical substance; code, errors, and technical terms exact; preserve the user\'s language.');
  process.exit(0);
}

// Strip YAML frontmatter, then filter the intensity table and examples down
// to the active level — same reduction caveman-activate.js applies.
const body = skillContent.replace(/^---[\s\S]*?---\s*/, '');
const filtered = body.split('\n').reduce((acc, line) => {
  const tableRowMatch = line.match(/^\|\s*\*\*(\S+?)\*\*\s*\|/);
  if (tableRowMatch) {
    if (tableRowMatch[1] === modeLabel) acc.push(line);
    return acc;
  }
  const exampleMatch = line.match(/^- (\S+?):\s/);
  if (exampleMatch) {
    if (exampleMatch[1] === modeLabel) acc.push(line);
    return acc;
  }
  acc.push(line);
  return acc;
}, []);

emit('CAVEMAN MODE ACTIVE — level: ' + modeLabel + '\n\n' + filtered.join('\n'));
