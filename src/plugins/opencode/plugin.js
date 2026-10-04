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
//   └── caveman-parse.cjs      ← copied sibling of src/hooks/caveman-parse.js
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
// bin/install.js, renamed to .cjs because this directory's package.json
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

// Resolved defensively, NOT destructured with the three above. loadConfig()
// reads whatever caveman-config.cjs sits in the installed plugin directory,
// which can predate this file (#848). recordModeChange is the newest of these
// exports, and handleSessionCreated() runs at factory time below, outside any
// try — so destructuring an absent one would throw during plugin construction
// and take caveman on opencode from "mode works, history missing" to "plugin
// does not load at all". The history log is best-effort by design (its own
// body silent-fails), so the no-op stub is the honest fallback.
const recordModeChange = config.recordModeChange || function () {};

// Displaced-prose-mode memory for the one-shot independent modes (#599),
// resolved defensively for the same reason recordModeChange is: the installed
// caveman-config.cjs is a COPY and can predate these exports (#848).
//
// All three or none. No-op stubs are NOT a safe fallback here, unlike
// recordModeChange's: a stubbed read returns null, the restore path reads that
// as "caveman was off when the one-shot started" and DELETES the flag, so a
// single /caveman-commit would deactivate caveman for the rest of the session
// on an older copy — worse than both the old behavior and the new one. When
// any helper is missing, one-shot bookkeeping is skipped entirely and the
// plugin behaves exactly as it did before this feature: the one-shot sticks
// until the next session.created re-derives the default.
//
// opencode has no per-session id to scope by (its flag is one machine-wide
// file already), so every call passes `null` and the shared helpers fall
// through to <opencodeDir>/.caveman-active.prev. That is exactly the legacy
// branch caveman-config documents for a caller with no session id.
const oneShotMemory = typeof config.writeSessionPrev === 'function'
  && typeof config.readSessionPrev === 'function'
  && typeof config.clearSessionPrev === 'function'
  ? {
    write: (mode) => config.writeSessionPrev(opencodeDir, null, mode),
    read: () => config.readSessionPrev(opencodeDir, null),
    clear: () => config.clearSessionPrev(opencodeDir, null),
  }
  : null;

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

const opencodeDir = opencodeConfigDir();
const flagPath = path.join(opencodeDir, '.caveman-active');

function removeFlag() {
  try {
    unlinkSync(flagPath);
  } catch (error) {
    if (process.env.CAVEMAN_DEBUG === '1' && error.code !== 'ENOENT') {
      console.error(`caveman: failed to remove flag ${flagPath}: ${error.message}`);
    }
  }
}

function reinforcementBanner(mode) {
  return 'CAVEMAN MODE ACTIVE (' + mode + ') — session ruleset applies.';
}

function escapeRegExp(str) {
  return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

// Derived from reinforcementBanner() itself (split on a sentinel) rather than
// re-spelling the banner text as a second regex literal: one source of truth,
// and it stays in sync if the wording above ever changes.
const [bannerPrefix, bannerSuffix] = reinforcementBanner('\0').split('\0');
const staleBlock = new RegExp(
  escapeRegExp(bannerPrefix) + '[a-z-]+' + escapeRegExp(bannerSuffix) + '[\\s\\S]*$'
);

// skills/<mode>/SKILL.md is the single source of truth for each mode, loaded
// whole the same way caveman-activate.js and caveman-mode-tracker.js do. The
// loader itself is NOT re-implemented here: it lives in caveman-config.js,
// which loadConfig() already evaluates, so all three loaders share one copy.
// A local copy here is the exact drift risk CLAUDE.md's "keep it in
// caveman-config.js" rule exists to prevent.
//
// Resolved off `config` rather than destructured at module scope because the
// installed caveman-config.cjs is a COPY: a user whose opencode plugin dir
// still holds an older copy gets a config without these exports, and the
// stand-ins below degrade to the banner alone rather than throwing inside a
// system-prompt hook.
function loadRuleset(mode) {
  if (typeof config.loadRuleset !== 'function') return null;
  // The shared loader probes <base>/../../skills and <base>/../skills. opencode
  // has no CLAUDE_PLUGIN_ROOT equivalent and two layouts to cover, so it is
  // called once per base — `here` resolves the installed tree
  // (~/.config/opencode/plugins/caveman → ~/.config/opencode/skills) and the
  // parent resolves the dev tree (src/plugins/opencode → repo-root skills).
  for (const base of [here, join(here, '..')]) {
    const ruleset = config.loadRuleset(mode, base);
    if (ruleset) return ruleset.trimEnd();
  }
  return null;
}

function reinforcementLine(mode) {
  const banner = reinforcementBanner(mode);
  const ruleset = loadRuleset(mode);
  if (ruleset) return banner + '\n\n' + ruleset;
  // No SKILL.md reachable from the plugin install: the mode's thesis line
  // (caveman-config's built-in fallback map), else the banner alone.
  const thesis = typeof config.thesisLine === 'function' ? config.thesisLine(mode) : null;
  return thesis ? banner + '\n\n' + thesis : banner;
}

// Returns true when this change set a one-shot independent mode, so the caller
// knows not to immediately restore it on the very turn it was requested.
function applyModeChange(change) {
  if (!change) return false;
  if (change.action === 'clear') {
    recordModeChange(opencodeDir, null);
    removeFlag();
    if (oneShotMemory) oneShotMemory.clear();
    return false;
  }
  if (change.action === 'set' && change.mode) {
    if (INDEPENDENT_MODES.has(change.mode)) {
      // Remember the prose mode being displaced so the next ordinary prompt
      // can bring it back. Mirrors caveman-mode-tracker.js exactly:
      //   - a prose mode is saved,
      //   - an already-saved target survives a SECOND one-shot chained onto
      //     the first (/caveman-commit then /caveman-review restores the
      //     original, not `commit`),
      //   - entering a one-shot from off saves the literal 'off', so a stale
      //     return target from an earlier one-shot cannot switch caveman on.
      // The flag file never holds 'off' on opencode (clear unlinks it), so a
      // null read is exactly "caveman was off".
      if (oneShotMemory) {
        const before = readFlag(flagPath);
        if (before && !INDEPENDENT_MODES.has(before)) oneShotMemory.write(before);
        else if (!before) oneShotMemory.write('off');
      }
      recordModeChange(opencodeDir, change.mode);
      safeWriteFlag(flagPath, change.mode);
      return true;
    }
    recordModeChange(opencodeDir, change.mode);
    safeWriteFlag(flagPath, change.mode);
  }
  return false;
}

// One-shot restore (#599). An independent mode set on a PREVIOUS message has
// served its turn: bring back the prose mode it displaced, or deactivate if
// caveman was off then. Without this the plugin left the flag on `commit` and
// experimental.chat.system.transform — which skips INDEPENDENT_MODES — stopped
// reinforcing for the REST of the session, self-healing only at the next
// session.created. Claude Code has restored on the next prompt since #599.
function restoreAfterOneShot(setIndependentThisTurn) {
  if (setIndependentThisTurn) return;
  // No prev helpers means nothing was ever recorded, so there is no return
  // target to read — restoring from that absence would read as "caveman was
  // off" and deactivate. Leave the one-shot in place instead.
  if (!oneShotMemory) return;
  const active = readFlag(flagPath);
  if (!active || !INDEPENDENT_MODES.has(active)) return;
  const prev = oneShotMemory.read();
  oneShotMemory.clear();
  // `prev !== 'off'` is not redundant: 'off' is stored literally, and restoring
  // it as a mode would reinforce "CAVEMAN MODE ACTIVE (off)" for a session that
  // had deliberately turned caveman off.
  if (prev && !INDEPENDENT_MODES.has(prev) && prev !== 'off') {
    recordModeChange(opencodeDir, prev);
    safeWriteFlag(flagPath, prev);
  } else {
    recordModeChange(opencodeDir, null);
    removeFlag();
  }
}

// Session-start logic — extracted so the `event` dispatcher (opencode >= 1.15)
// drives one shared implementation. Re-fires on every `session.created` event,
// so a new session in a long-lived plugin process re-asserts the flag.
function handleSessionCreated() {
  // Manual startup is currently a Claude Code policy. OpenCode's installer
  // also ships static AGENTS.md activation, so a cleared flag alone cannot
  // promise normal prose here. Preserve its existing caveman-mode default.
  const configured = getDefaultMode();
  const mode = configured === 'manual' ? 'caveman' : configured;
  if (mode === 'off') {
    recordModeChange(opencodeDir, null);
    removeFlag();
    return;
  }
  recordModeChange(opencodeDir, mode);
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
    // One message is one turn, so the one-shot bookkeeping spans the whole
    // parts loop: a /caveman-commit in any part must not be restored away by
    // the same message that requested it.
    let setIndependentThisTurn = false;
    // Status is observational — on Claude Code it returns before any state
    // mutation, one-shot restore included. Keep that here: asking what mode is
    // active must not be the thing that consumes a pending restore.
    let statusThisTurn = false;
    for (const part of output.parts) {
      if (part && part.type === 'text' && part.text) {
        const change = parseModeChange(part.text, { getDefaultMode, expandedTpl: true, unwrapQuotes: true });
        if (change && change.action === 'status') {
          // readFlag maps a legacy level name to its current mode id.
          const active = readFlag(flagPath);
          // Replace the expanded activation template for this message only.
          // No shared pending response: concurrent sessions cannot steal it.
          part.text = 'Report this status verbatim without changing mode: Caveman mode: ' + (active || 'off');
          statusThisTurn = true;
          continue;
        }
        if (change && applyModeChange(change)) setIndependentThisTurn = true;
      }
    }
    if (!statusThisTurn) restoreAfterOneShot(setIndependentThisTurn);
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
      // staleBlock matches to end of string: `line` now carries the ruleset
      // appended after the banner, and that content is always the last thing
      // this hook writes into an entry, so replacing from the banner on is safe.
      let found = false;
      for (let i = 0; i < output.system.length; i++) {
        if (typeof output.system[i] === 'string' && staleBlock.test(output.system[i])) {
          output.system[i] = output.system[i].replace(staleBlock, line);
          found = true;
        }
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
