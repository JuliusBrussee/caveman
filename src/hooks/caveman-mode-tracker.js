#!/usr/bin/env node
// caveman — UserPromptSubmit hook to track which caveman mode is active
// Inspects user input for /caveman commands and writes mode to flag file

const fs = require('fs');
const path = require('path');
const os = require('os');
const { execFileSync } = require('child_process');
const { getDefaultMode, safeWriteFlag, readFlag, VALID_MODES } = require('./caveman-config');

// Modes handled by their own slash commands (/caveman-commit, etc.) — not
// selectable via /caveman <arg>.
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.caveman-active');
const turnPath = path.join(claudeDir, '.caveman-turn');

// Emit the per-turn reinforcement on turns 1, 1+N, 1+2N, ... (plus any turn that
// just (re)activated the mode). N=3 keeps the drift anchor while cutting the
// fixed INPUT tax of reminding on every single turn — over a long session that
// tax can outweigh the output caveman saves.
const REINFORCE_EVERY = 3;

// Best-effort read of the turn counter. Symlink-refusing + size-capped, symmetric
// with readFlag. Returns 0 on any anomaly. The value is only used for modulo math
// and is never injected into model context, so a plain bounded parse is safe.
function readTurnCount(p) {
  try {
    const st = fs.lstatSync(p);
    if (st.isSymbolicLink() || !st.isFile() || st.size > 16) return 0;
    const n = parseInt(fs.readFileSync(p, 'utf8').trim(), 10);
    return Number.isFinite(n) && n >= 0 && n < 1e9 ? n : 0;
  } catch (e) { return 0; }
}

// Multi-site locate verbs — tightly scoped so a trivial one-line lookup doesn't
// trip it. When one appears, the per-turn nudge suggests delegating to
// cavecrew-investigator so the verbose Grep/Read stays OUT of main context and
// only a ~60%-smaller path:line map returns. Tested against the lowercased prompt.
const INVESTIGATE_RE = /\b(where (is|are|does)|what calls|who calls|find all|list (all )?(uses|usages|references|callers|occurrences)|all (uses|usages|references|callers)|map (this |the )?(dir|directory|codebase|repo)|trace (the )?(call|flow))\b/;

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  try {
    const data = JSON.parse(input);
    const prompt = (data.prompt || '').trim().toLowerCase();

    // True when this turn's prompt (re)activated or switched the mode — forces a
    // reinforcement emit regardless of the every-Nth-turn cadence below.
    let modeSetThisTurn = false;

    // Natural language activation (e.g. "activate caveman", "turn on caveman mode",
    // "talk like caveman"). README tells users they can say these, but the hook
    // only matched /caveman commands — flag file and statusline stayed out of sync.
    // Also recognize brevity requests ("less tokens", "be brief/terse", "fewer
    // tokens", "shorter answers") — README promises these trigger caveman too.
    if (/\b(activate|enable|turn on|start|talk like)\b.*\bcaveman\b/i.test(prompt) ||
        /\bcaveman\b.*\b(mode|activate|enable|turn on|start)\b/i.test(prompt) ||
        /\b(less tokens|fewer tokens|be brief|be terse|shorter answers)\b/i.test(prompt)) {
      if (!/\b(stop|disable|turn off|deactivate)\b/i.test(prompt)) {
        const mode = getDefaultMode();
        if (mode !== 'off') {
          safeWriteFlag(flagPath, mode);
          modeSetThisTurn = true;
        }
      }
    }

    // /caveman-stats [--share] — block the prompt and inject stats output as
    // the hook's reason. The script reads the active session log, so we pass
    // transcript_path through when Claude Code provides it.
    const statsMatch = /^\/caveman(?::caveman)?-stats(?:\s+(.*))?$/.exec(prompt);
    if (statsMatch) {
      const tailArgs = (statsMatch[1] || '').trim().split(/\s+/).filter(Boolean);
      try {
        const statsPath = path.join(__dirname, 'caveman-stats.js');
        const argv = [statsPath];
        if (data.transcript_path) argv.push('--session-file', data.transcript_path);
        if (tailArgs.includes('--share')) argv.push('--share');
        if (tailArgs.includes('--all')) argv.push('--all');
        const sinceIdx = tailArgs.indexOf('--since');
        if (sinceIdx !== -1 && tailArgs[sinceIdx + 1]) {
          argv.push('--since', tailArgs[sinceIdx + 1]);
        }
        const out = execFileSync(process.execPath, argv, { encoding: 'utf8', timeout: 5000 });
        process.stdout.write(JSON.stringify({ decision: 'block', reason: out.trim() }));
      } catch (e) {
        process.stdout.write(JSON.stringify({
          decision: 'block',
          reason: 'caveman-stats: could not run stats script.\nTry manually: node hooks/caveman-stats.js'
        }));
      }
      return;
    }

    // Match /caveman commands
    if (prompt.startsWith('/caveman')) {
      const parts = prompt.split(/\s+/);
      const cmd = parts[0]; // /caveman, /caveman-commit, /caveman-review, etc.
      const arg = parts[1] || '';

      let mode = null;

      if (cmd === '/caveman-commit') {
        mode = 'commit';
      } else if (cmd === '/caveman-review') {
        mode = 'review';
      } else if (cmd === '/caveman-compress' || cmd === '/caveman:caveman-compress') {
        mode = 'compress';
      } else if (cmd === '/caveman' || cmd === '/caveman:caveman') {
        // Bare /caveman → activate at configured default
        if (!arg) {
          mode = getDefaultMode();
        } else if (arg === 'off' || arg === 'stop' || arg === 'disable') {
          mode = 'off';
        } else if (arg === 'wenyan-full') {
          // Canonical alias — config stores as 'wenyan'
          mode = 'wenyan';
        } else if (VALID_MODES.includes(arg) && !INDEPENDENT_MODES.has(arg)) {
          mode = arg;
        }
        // Unknown arg → mode stays null, flag untouched (no silent overwrite)
      }

      if (mode && mode !== 'off') {
        safeWriteFlag(flagPath, mode);
        modeSetThisTurn = true;
      } else if (mode === 'off') {
        try { fs.unlinkSync(flagPath); } catch (e) {}
      }
    }

    // Detect deactivation — natural language and slash commands
    if (/\b(stop|disable|deactivate|turn off)\b.*\bcaveman\b/i.test(prompt) ||
        /\bcaveman\b.*\b(stop|disable|deactivate|turn off)\b/i.test(prompt) ||
        /\bnormal mode\b/i.test(prompt)) {
      try { fs.unlinkSync(flagPath); } catch (e) {}
    }

    // Per-turn reinforcement: emit a structured reminder when caveman is active.
    // The SessionStart hook injects the full ruleset once, but models lose it
    // when other plugins inject competing style instructions every turn.
    // This keeps caveman visible in the model's attention on every user message.
    //
    // Skip independent modes (commit, review, compress) — they have their own
    // skill behavior and the base caveman rules would conflict.
    // readFlag enforces symlink-safe read + size cap + VALID_MODES whitelist.
    // If the flag is missing, corrupted, oversized, or a symlink pointing at
    // something like ~/.ssh/id_rsa, readFlag returns null and we emit nothing
    // — never inject untrusted bytes into model context.
    const activeMode = readFlag(flagPath);
    if (activeMode && !INDEPENDENT_MODES.has(activeMode)) {
      // Reinforcement is an attention anchor, not the source of truth — the full
      // ruleset is injected once at SessionStart. Emit on turns 1, 1+N, 1+2N, ...
      // plus any turn that just (re)activated the mode, instead of every turn.
      // The counter lives in a SEPARATE flag file, so the injected string stays
      // byte-identical and keeps hitting the prompt cache.
      const turn = readTurnCount(turnPath) + 1;
      safeWriteFlag(turnPath, String(turn));

      // Assemble the per-turn context from independent, byte-stable segments:
      //   - reinforcement: cadence-gated (turns 1, 1+N, ...) or on (re)activation
      //   - locate nudge:  only on investigation-shaped prompts
      // Each segment is a constant string (no per-turn-varying token), so the
      // tail stays cache-friendly.
      const segments = [];
      if (modeSetThisTurn || turn % REINFORCE_EVERY === 1) {
        segments.push("CAVEMAN MODE ACTIVE (" + activeMode + "). " +
          "Drop articles/filler/pleasantries/hedging. Fragments OK. " +
          "Code/commits/security: write normal.");
      }
      if (INVESTIGATE_RE.test(prompt)) {
        segments.push("Locate task — prefer spawning cavecrew-investigator over " +
          "inline Grep/Read (keeps the verbose search out of main context); skip " +
          "only for a single known-file one-liner.");
      }
      if (segments.length) {
        process.stdout.write(JSON.stringify({
          hookSpecificOutput: {
            hookEventName: "UserPromptSubmit",
            additionalContext: segments.join(" "),
          }
        }));
      }
    }
  } catch (e) {
    // Silent fail
  }
});
