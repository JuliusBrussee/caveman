#!/usr/bin/env node
// caveman — GitHub Copilot CLI userPromptSubmitted hook
//
// Copilot CLI does NOT process userPromptSubmitted output (it cannot block the
// prompt or inject context from this event), so this hook is side-effect only:
// it updates the persisted mode flag when the user types a /caveman command or
// a natural-language activation/deactivation phrase. The next sessionStart
// (caveman-copilot-activate.js) reads the flag and injects the matching ruleset.
//
// Within the current turn, mode switches still take effect because the model
// already sees the /caveman command in the prompt alongside the always-on
// caveman rules — this hook just makes the choice persist across sessions.
//
// Reads the event payload from stdin (camelCase `prompt` field). Emits `{}`.
// Silent-fails on every error — a prompt hook must never break the CLI.

const fs = require('fs');
const path = require('path');
const os = require('os');

function emit(obj) {
  try { process.stdout.write(JSON.stringify(obj)); } catch (e) { /* ignore */ }
}

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  try {
    const { getDefaultMode, safeWriteFlag, VALID_MODES, INDEPENDENT_MODES } = require('./caveman-config');

    // Flag location: CAVEMAN_FLAG_DIR (set by the installer; repo-relative for
    // repo-scoped installs, resolved against the hook's cwd = repo root) wins;
    // otherwise the global ~/.copilot/.caveman-active.
    const copilotHome = process.env.COPILOT_HOME || path.join(os.homedir(), '.copilot');
    const flagDir = process.env.CAVEMAN_FLAG_DIR ? path.resolve(process.env.CAVEMAN_FLAG_DIR) : copilotHome;
    const flagPath = path.join(flagDir, '.caveman-active');

    let data = {};
    try { data = JSON.parse(input); } catch (e) { data = {}; }
    const prompt = (data.prompt || '').trim().toLowerCase();

    if (!prompt) { emit({}); return; }

    // Extract the mode argument the user requested. Two shapes reach this hook:
    //   1. Literal "/caveman <arg>" — when the CLI passes the raw prompt.
    //   2. The skill-invocation rewrite Copilot CLI produces because a skill
    //      named "caveman" is installed: `... invoke the "caveman" skill ...
    //      help with: <arg>`. This is the common path once skills are installed.
    let arg = null;
    const literal = /^\/caveman(?:\s+(.+))?$/.exec(prompt);
    const skillInvoke = /invoke the\s+"?caveman"?\s+skill\b.*?help with:\s*(.+)$/i.exec(prompt);
    if (literal) {
      arg = (literal[1] || '').trim();
    } else if (skillInvoke) {
      // "help with: ultra" → "ultra"; ignore trailing words ("ultra mode" → "ultra")
      arg = skillInvoke[1].trim().split(/\s+/)[0];
    }

    if (arg !== null) {
      // Map the requested arg to a persistent mode (or deactivation).
      let mode = null;
      let deactivate = false;
      if (arg === '' ) {
        mode = getDefaultMode();
      } else if (arg === 'off' || arg === 'stop' || arg === 'disable' || arg === 'normal') {
        deactivate = true;
      } else if (arg === 'wenyan-full') {
        mode = 'wenyan';
      } else if (VALID_MODES.includes(arg) && !INDEPENDENT_MODES.has(arg)) {
        mode = arg;
      }
      // Independent skills (/caveman-commit etc.) and unknown args fall through
      // untouched — no silent overwrite.

      if (deactivate) {
        try { fs.unlinkSync(flagPath); } catch (e) {}
      } else if (mode && mode !== 'off') {
        safeWriteFlag(flagPath, mode);
      }
      emit({});
      return;
    }

    // Deactivation — natural language ("stop caveman", "normal mode").
    if (/\b(stop|disable|deactivate|turn off)\b.*\bcaveman\b/i.test(prompt) ||
        /\bcaveman\b.*\b(stop|disable|deactivate|turn off)\b/i.test(prompt) ||
        /\bnormal mode\b/i.test(prompt)) {
      try { fs.unlinkSync(flagPath); } catch (e) {}
      emit({});
      return;
    }

    // Natural-language activation / brevity requests. README promises these
    // phrases turn caveman on.
    if (/\b(activate|enable|turn on|start|talk like)\b.*\bcaveman\b/i.test(prompt) ||
        /\bcaveman\b.*\b(mode|activate|enable|turn on|start)\b/i.test(prompt) ||
        /\b(less tokens|fewer tokens|be brief|be terse|shorter answers)\b/i.test(prompt)) {
      const mode = getDefaultMode();
      if (mode !== 'off') safeWriteFlag(flagPath, mode);
    }

    emit({});
  } catch (e) {
    emit({});
  }
});
