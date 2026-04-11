#!/usr/bin/env node
// caveman — optional Claude Code SessionStart activation hook
//
// When wired into ~/.claude/settings.json as a SessionStart hook:
//   - Writes a flag file at ~/.claude/.caveman-active so a statusline
//     script can prove caveman mode is loaded (see README for the badge
//     snippet — SessionStart stdout is otherwise invisible to users)
//   - Emits a short ruleset reminder as SessionStart context
//
// This is a pure addition — if you don't wire it up, nothing changes.
// Install instructions: see the "Optional: SessionStart Hook" section
// in README.md.

const fs = require('fs');
const path = require('path');
const os = require('os');
const { getDefaultMode } = require('./caveman-config');

const flagPath = path.join(os.homedir(), '.claude', '.caveman-active');

try {
  fs.mkdirSync(path.dirname(flagPath), { recursive: true });
  fs.writeFileSync(flagPath, getDefaultMode());
} catch (e) {
  // Silent fail -- flag is best-effort, don't block the hook
}

process.stdout.write(
  "CAVEMAN MODE ACTIVE. Rules: Drop articles/filler/pleasantries/hedging. " +
  "Fragments OK. Short synonyms. Pattern: [thing] [action] [reason]. [next step]. " +
  "Not: 'Sure! I'd be happy to help you with that.' " +
  "Yes: 'Bug in auth middleware. Fix:' " +
  "Code/commits/security: write normal. " +
  "User says 'normal' or 'stop caveman' to deactivate."
);
