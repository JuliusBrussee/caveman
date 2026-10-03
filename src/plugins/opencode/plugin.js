// caveman — opencode plugin
//
// Provides dynamic caveman mode tracking for opencode:
// - Writes the mode flag on each session start (via the `event` dispatcher)
// - Parses user messages for /caveman commands and natural-language toggles
// - Injects per-turn reinforcement into the system prompt
//
// Bun ESM module; loads the existing security-hardened helpers from
// caveman-config.js via createRequire so the symlink-safe flag-write code
// lives in one place. Same trick loads caveman-parse.js (#602) so the mode-
// change parsing is a single shared source with caveman-mode-tracker.js.
//
// Layout once installed:
//   ~/.config/opencode/plugins/caveman/
//   ├── package.json
//   ├── plugin.js              ← this file
//   ├── caveman-config.cjs     ← copied sibling of src/hooks/caveman-config.js
//   ├── caveman-parse.cjs      ← copied sibling of src/hooks/caveman-parse.js
//   └── caveman-budget.cjs     ← copied sibling of src/hooks/caveman-budget.js
//
// The always-on caveman ruleset is provided separately via
// ~/.config/opencode/AGENTS.md (Tier-3 base). This plugin handles dynamic
// state only: flag writes, slash-command parsing, natural-language
// activation, and per-turn reinforcement.
//
// Hook mapping (opencode >= 1.15.x):
//   - event (event.type === 'session.created'): session-init flag write,
//     re-fires per session rather than once per plugin-process load
//   - chat.message: intercept user prompts for mode changes
//   - experimental.chat.system.transform: inject reinforcement per-turn
//
// Note: opencode does NOT support 'session.created' or 'tui.prompt.append'
// as named plugin-hook keys. 'session.created' is an event *type* dispatched
// through the single `event` handler; the old direct-key handlers were
// silently ignored. See:
// https://github.com/JuliusBrussee/caveman/issues/418
// https://github.com/JuliusBrussee/caveman/issues/421

import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { dirname, join } from 'node:path';
import { existsSync, unlinkSync, readFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));

// When installed: caveman-config.cjs sits next to plugin.js (copied by
// cli/install.js, renamed to .cjs because this directory's package.json
// declares "type": "module" — bare .js would be loaded as ESM). When loaded
// from the source tree (tests, dev): fall back to the canonical
// src/hooks/caveman-config.js, which lives in a directory whose own
// package.json pins "type": "commonjs". One source of truth either way.
//
// Loaded by evaluating the file as CommonJS by hand, NOT via the module
// loader: opencode runs plugins inside a compiled Bun binary where
// require() of on-disk files is rejected ("require() async module is
// unsupported") and await import() of a CJS file yields an empty namespace —
// both silently break the plugin (#418 follow-up). createRequire() still
// resolves node BUILT-INS fine in the compiled binary, which is all
// caveman-config needs (fs/path/os).
function loadConfig() {
  const installed = join(here, 'caveman-config.cjs');
  const dev = join(here, '..', '..', 'hooks', 'caveman-config.js');
  const target = existsSync(installed) ? installed : dev;
  const code = readFileSync(target, 'utf8').replace(/^#![^\n]*\n/, '');
  const mod = { exports: {} };
  // Base require on the loaded file, not plugin.js — caveman-parse.js does a
  // relative require('./caveman-config') that must resolve against src/hooks/
  // in the dev layout and against pluginDir when installed.
  new Function('module', 'exports', 'require', '__dirname', '__filename', code)(
    mod, mod.exports, createRequire(pathToFileURL(target).href), dirname(target), target
  );
  return mod.exports;
}
const config = loadConfig();

const { getDefaultMode, safeWriteFlag, readFlag } = config;

// Load the shared mode-change parser (#602) the same way loadConfig() loads
// caveman-config.js — see the doc comment above loadConfig() for why this
// can't go through require()/import() in a compiled Bun binary.
function loadParse() {
  const installed = join(here, 'caveman-parse.cjs');
  const dev = join(here, '..', '..', 'hooks', 'caveman-parse.js');
  const target = existsSync(installed) ? installed : dev;
  const code = readFileSync(target, 'utf8').replace(/^#![^\n]*\n/, '');
  const mod = { exports: {} };
  new Function('module', 'exports', 'require', '__dirname', '__filename', code)(
    mod, mod.exports, createRequire(pathToFileURL(target).href), dirname(target), target
  );
  return mod.exports;
}
const { parseModeChange, INDEPENDENT_MODES } = loadParse();

function loadBudget() {
  const installed = join(here, 'caveman-budget.cjs');
  const dev = join(here, '..', '..', 'hooks', 'caveman-budget.js');
  const target = existsSync(installed) ? installed : dev;
  if (!existsSync(target)) return null;
  try {
    const code = readFileSync(target, 'utf8').replace(/^#![^\n]*\n/, '');
    const mod = { exports: {} };
    new Function('module', 'exports', 'require', '__dirname', '__filename', code)(
      mod, mod.exports, createRequire(pathToFileURL(target).href), dirname(target), target
    );
    const m = mod.exports;
    if (!m || typeof m.readBudgetConfig !== 'function' || typeof m.resolveLadderMode !== 'function') return null;
    return m;
  } catch (e) {
    return null;
  }
}
const budgetMod = loadBudget();

// opencode resolves its config dir from $XDG_CONFIG_HOME, else ~/.config/opencode
// on every platform — including Windows, where it uses %USERPROFILE%\.config\opencode
// (NOT %APPDATA%). os.homedir() is %USERPROFILE% on win32, so the default branch
// is already correct cross-platform.
function opencodeConfigDir() {
  if (process.env.XDG_CONFIG_HOME) {
    return path.join(process.env.XDG_CONFIG_HOME, 'opencode');
  }
  return path.join(os.homedir(), '.config', 'opencode');
}

const flagPath = path.join(opencodeConfigDir(), '.caveman-active');
const holdDir = opencodeConfigDir();
const usedCounterPath = path.join(opencodeConfigDir(), '.caveman-budget-used');

function removeFlag() {
  try {
    unlinkSync(flagPath);
  } catch (error) {
    if (process.env.CAVEMAN_DEBUG === '1' && error.code !== 'ENOENT') {
      console.error(`caveman: failed to remove flag ${flagPath}: ${error.message}`);
    }
  }
}

function readOpencodeUsed(budget) {
  // opencode has no Claude JSONL. Do not invent usage numbers.
  // session window: only the local counter, incremented when the host later
  // exposes usage. Missing counter → unknown → ladder no-ops (no flag write).
  // day window: only if Claude Code project transcripts exist for today.
  if (!budgetMod || !budget) return null;
  if (budget.window === 'session') {
    try {
      if (!existsSync(usedCounterPath)) return null;
      const raw = readFileSync(usedCounterPath, 'utf8').trim();
      if (!/^[0-9]+$/.test(raw)) return null;
      const used = Number(raw);
      return Number.isInteger(used) && used >= 0 ? used : null;
    } catch (e) {
      return null;
    }
  }
  if (budget.window === 'day') {
    const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
    if (!existsSync(path.join(claudeDir, 'projects'))) return null;
    try {
      if (!budgetMod.findRecentSession(claudeDir)) return null;
    } catch (e) {
      return null;
    }
    return budgetMod.usedOutputTokens({ window: 'day', claudeDir });
  }
  return null;
}

function applyLadderIfReady() {
  if (!budgetMod) return false;
  const hold = budgetMod.readHold(holdDir);
  if (hold) {
    safeWriteFlag(flagPath, hold);
    return true;
  }
  const budget = budgetMod.effectiveBudget
    ? budgetMod.effectiveBudget(undefined, holdDir)
    : budgetMod.readBudgetConfig();
  if (!budget) return false;
  const used = readOpencodeUsed(budget);
  if (used == null) return false; // no-op, no flag write from the ladder
  const mode = budgetMod.resolveLadderMode(budget, used);
  if (!mode) return false;
  safeWriteFlag(flagPath, mode);
  return true;
}

function reinforcementLine(mode) {
  return 'CAVEMAN MODE ACTIVE (' + mode + ') — session ruleset applies.';
}

function applyModeChange(change) {
  if (!change) return { pinned: false, cleared: false, independent: false };
  if (change.action === 'clear') {
    removeFlag();
    if (budgetMod) budgetMod.clearHold(holdDir);
    return { pinned: false, cleared: true, independent: false };
  }
  if (change.action === 'hold' && budgetMod) {
    const current = readFlag(flagPath);
    if (current && !INDEPENDENT_MODES.has(current)) {
      budgetMod.writeHold(holdDir, current);
      return { pinned: true, cleared: false, independent: false };
    }
    return { pinned: false, cleared: false, independent: false };
  }
  if (change.action === 'release' && budgetMod) {
    budgetMod.clearHold(holdDir);
    return { pinned: false, cleared: false, independent: false, released: true };
  }
  if (change.action === 'budget-override' && budgetMod) {
    budgetMod.writeOverride(holdDir, change.outputTokens);
    return { pinned: false, cleared: false, independent: false };
  }
  if (change.action === 'set' && change.mode) {
    const independent = INDEPENDENT_MODES.has(change.mode);
    if (change.pin && budgetMod && !independent) {
      budgetMod.writeHold(holdDir, change.mode);
    }
    safeWriteFlag(flagPath, change.mode);
    return { pinned: !!(change.pin && !independent), cleared: false, independent };
  }
  return { pinned: false, cleared: false, independent: false };
}

// Session-start logic — extracted so the `event` dispatcher (opencode >= 1.15)
// drives one shared implementation. Re-fires on every `session.created` event,
// so a new session in a long-lived plugin process re-asserts the flag.
//
// opencode has no Claude transcript. The session-window ladder uses
// ~/.config/opencode/.caveman-budget-used, incremented only when the host
// later exposes usage. Until then the ladder applies only for `day` if
// Claude Code project history exists; otherwise it no-ops with no flag write.
function handleSessionCreated() {
  const mode = getDefaultMode();
  if (mode === 'off') {
    removeFlag();
    return;
  }
  if (budgetMod) {
    try { budgetMod.clearHold(holdDir); } catch (e) {}
    if (applyLadderIfReady()) return;
  }
  safeWriteFlag(flagPath, mode);
}

export const CavemanPlugin = async (_ctx) => {
  // Assert the flag at plugin load as well: in one-shot `opencode run` the
  // first session.created publishes before plugin event dispatch is wired,
  // so the event handler alone misses it. The factory-time write covers that
  // race; the event handler re-asserts on every later session in long-lived
  // TUI processes.
  handleSessionCreated();

  return {
  // opencode dispatches session/lifecycle events through a single `event`
  // handler keyed on event.type; the older direct top-level
  // 'session.created' key is silently ignored. Routing session-init through
  // here means the flag is rewritten on every new session, not just once when
  // the plugin module loads. See https://opencode.ai/docs/plugins#events.
  event: async ({ event } = {}) => {
    if (event && event.type === 'session.created') handleSessionCreated();
  },

  // Intercept user messages to detect /caveman commands and natural-language
  // mode toggles. opencode fires chat.message with (input, output) where
  // output.parts is the array of message parts; text parts carry .text.
  // Return value is ignored — state changes happen via the flag file.
  // expandedTpl: opencode replaces a typed slash command with its command
  // file's prose before this hook sees it. unwrapQuotes: the non-interactive
  // `run` path delivers the message wrapped in literal quote characters.
  'chat.message': async (_input, output) => {
    if (!output || !output.parts) return;
    for (const part of output.parts) {
      if (part && part.type === 'text' && part.text) {
        const change = parseModeChange(part.text, { getDefaultMode, expandedTpl: true, unwrapQuotes: true });
        const result = applyModeChange(change);
        if (result.cleared || result.pinned || result.independent) continue;
        if (!result.pinned) applyLadderIfReady();
      }
    }
  },

  // Inject the reinforcement line into the system prompt when caveman is
  // active. opencode calls this before every LLM request and expects the hook
  // to mutate output.system (a string[]); the return value is discarded.
  'experimental.chat.system.transform': async (_input, output) => {
    if (!output || !Array.isArray(output.system)) return;
    const active = readFlag(flagPath);
    if (active && !INDEPENDENT_MODES.has(active)) {
      const line = reinforcementLine(active);
      // Idempotent: opencode is expected to rebuild `output.system` per
      // request, but if it ever reuses the array across turns an unguarded
      // append grows the system prompt without bound — silently eating the
      // context window. Rewrite any line we already left instead of stacking
      // another, so a mode switch updates in place rather than accumulating.
      const stale = /CAVEMAN MODE ACTIVE \([a-z-]+\) — session ruleset applies\./g;
      let found = false;
      for (let i = 0; i < output.system.length; i++) {
        if (typeof output.system[i] === 'string' && stale.test(output.system[i])) {
          stale.lastIndex = 0;
          output.system[i] = output.system[i].replace(stale, line);
          found = true;
        }
        stale.lastIndex = 0;
      }
      if (found) return;
      if (output.system.length > 0) {
        output.system[output.system.length - 1] += '\n\n' + line;
      } else {
        output.system.push(line);
      }
    }
  },
  };
};

export default CavemanPlugin;
