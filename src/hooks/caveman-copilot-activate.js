#!/usr/bin/env node
// caveman — GitHub Copilot CLI sessionStart hook
//
// Copilot CLI's hook system mirrors Claude Code's but with a different output
// contract: a sessionStart hook injects context by writing a single-line JSON
// object { "additionalContext": "..." } to stdout (exit 0). See
// https://docs.github.com/en/copilot/reference/hooks-reference
//
// Responsibilities:
//   1. Resolve the active mode (persisted flag overrides the configured default)
//   2. Write the flag file under the Copilot config dir (symlink-safe)
//   3. Emit the caveman ruleset as additionalContext so caveman is on from the
//      first turn — no manual /caveman needed each session.
//
// Unlike Claude Code, Copilot CLI does NOT process userPromptSubmitted output,
// so per-turn reinforcement is not available. The sessionStart injection plus
// the always-on rule files (AGENTS.md / .github/copilot-instructions.md) carry
// the behavior instead.
//
// Silent-fails on every error: a sessionStart hook must never block the CLI.
// On any failure we emit `{}` (no-op) so Copilot proceeds normally.

const fs = require('fs');
const path = require('path');
const os = require('os');

function emit(obj) {
  try { process.stdout.write(JSON.stringify(obj)); } catch (e) { /* ignore */ }
}

try {
  const { getDefaultMode, safeWriteFlag, readFlag, buildRuleset } = require('./caveman-config');

  const copilotHome = process.env.COPILOT_HOME || path.join(os.homedir(), '.copilot');
  // Flag location: CAVEMAN_FLAG_DIR (set by the installer; repo-relative for
  // repo-scoped installs, resolved against the hook's cwd = repo root) wins;
  // otherwise the global ~/.copilot/.caveman-active. Must match the path the
  // userPromptSubmitted hook (caveman-copilot-mode-tracker.js) writes to.
  const flagDir = process.env.CAVEMAN_FLAG_DIR ? path.resolve(process.env.CAVEMAN_FLAG_DIR) : copilotHome;
  const flagPath = path.join(flagDir, '.caveman-active');

  // Resolve the active mode. An explicit "off" from env/config always wins and
  // disables caveman. Otherwise a persisted flag (set by a prior /caveman <mode>
  // in an earlier session) overrides the configured default — this is how mode
  // selection survives across Copilot CLI sessions, since userPromptSubmitted
  // output is not processed and cannot re-inject mid-session.
  const defaultMode = getDefaultMode();
  let mode;
  if (defaultMode === 'off') {
    mode = 'off';
  } else {
    mode = readFlag(flagPath) || defaultMode;
  }

  // "off" — skip activation entirely. Clear any stale flag, inject nothing.
  if (mode === 'off') {
    try { fs.unlinkSync(flagPath); } catch (e) {}
    emit({});
    process.exit(0);
  }

  // Keep the flag in sync so the resolved mode persists to the next session.
  safeWriteFlag(flagPath, mode);

  emit({ additionalContext: buildRuleset(mode) });
} catch (e) {
  emit({});
}
