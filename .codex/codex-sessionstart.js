#!/usr/bin/env node
// Codex SessionStart hook (#185).
//
// The repo-local `.codex/hooks.json` used to hardcode a single static echo of
// the `full`-level rules, so `CAVEMAN_DEFAULT_MODE`, a repo-local
// `.caveman/config.json` / `.caveman.json`, or a user config `defaultMode`
// had no effect on Codex sessions — and an `off` default still injected
// rules. This script replaces the hardcoded echo and resolves the mode the
// same way the Claude SessionStart hook does, through
// `src/hooks/caveman-config.js`.
//
// The command in `.codex/hooks.json` resolves this file from the git root
// (`$(git rev-parse --show-toplevel)/.codex/...`) because Codex runs command
// hooks with the SESSION cwd as their working directory and can be launched
// from a subdirectory (Codex hooks docs: "prefer resolving from the git root
// instead of using a relative path"). Discovery of `.codex/hooks.json` is
// git-root based, so cwd may legitimately differ from the manifest's home;
// `__dirname` below is what keeps script-relative resolution stable either
// way.
//
// Keep this fail-open and cheap: any error prints nothing and exits 0, so a
// broken install can never block a Codex session. Never call process.exit()
// after the ruleset write — pipe stdout is asynchronous and process.exit()
// can drop pending output, leaving caveman silently unactivated. Exit by
// falling off the end of the script instead (exit code 0 throughout).
//
// Resolution order for default mode (identical to caveman-config.js):
//   1. CAVEMAN_DEFAULT_MODE environment variable
//   2. Repo-local config: <cwd>/.caveman/config.json or <cwd>/.caveman.json,
//      walking up to the filesystem root
//   3. User config: $XDG_CONFIG_HOME/caveman/config.json, then
//      ~/.config/caveman/config.json (macOS/Linux) or
//      %APPDATA%\caveman\config.json then ~/.config/caveman/config.json
//      (Windows)
//   4. 'caveman'
//
// Deliberate non-goal (#185 scope): this hook resolves the CONFIGURED default
// only. It does not read the legacy `~/.claude/.caveman-active` mirror or the
// per-session store, so a mode switched mid-session is not resumed here —
// Codex has no tracker hook wired in this repo-local setup, so there is
// nothing session-scoped to read without guessing at another host's state.

const fs = require('fs');
const path = require('path');
const os = require('os');

// Hand-copy of caveman-config.js VALID_MODES, used only when that module is
// unavailable. tests/test_hook_missing_sibling.js asserts the Claude hook's
// copy stays equal to the real one; tests/hooks/codex-sessionstart.test.mjs
// asserts this copy does too.
const FALLBACK_VALID_MODES = [
  'off', 'caveman', 'ultracave', 'megacave',
  'commit', 'review', 'compress'
];
// Hand-copy of caveman-config.js LEGACY_MODES: a config still naming a
// pre-three-skill level resolves the same way when degraded.
const FALLBACK_LEGACY_MODES = {
  lite: 'caveman', full: 'caveman', ultra: 'ultracave',
  wenyan: 'megacave', 'wenyan-lite': 'megacave',
  'wenyan-full': 'megacave', 'wenyan-ultra': 'megacave',
};
// Default-mode sources also accept the 'manual' startup policy, like the real
// resolver's VALID_DEFAULT_MODES.
function fallbackCanonicalMode(raw) {
  if (typeof raw !== 'string') return null;
  const m = raw.toLowerCase();
  if (FALLBACK_VALID_MODES.includes(m) || m === 'manual') return m;
  return Object.prototype.hasOwnProperty.call(FALLBACK_LEGACY_MODES, m) ? FALLBACK_LEGACY_MODES[m] : null;
}

// Modes that inject nothing: the user opted out (`off`) or starts inactive
// (`manual`), and rules must not be force-injected (caveman-activate.js treats
// both the same way on the Claude side).
const SILENT_MODES = new Set(['off', 'manual']);

// Modes with their own independent skill files — not caveman prose modes.
// The Claude hook emits a one-line pointer for them; so does this.
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

// Hand-copy of caveman-config.js readModeFromConfigFile, used only when that
// module is unavailable. Refuses symlinked config files, symmetric with the
// real resolver's findRepoConfigPath/readModeFromConfigFile policy.
function fallbackReadMode(file) {
  try {
    if (!fs.lstatSync(file).isFile()) return null;
    // A BOM or UTF-16LE (PowerShell 5.1) must not drop an opt-out here either.
    const buf = fs.readFileSync(file);
    const text = buf[0] === 0xff && buf[1] === 0xfe ? buf.toString('utf16le', 2) : buf.toString('utf8').replace(/^\uFEFF/, '');
    return fallbackCanonicalMode(JSON.parse(text).defaultMode);
  } catch (e) { /* absent, unreadable, or malformed → next source */ }
  return null;
}

function fallbackUserConfigMode() {
  let userConfigPath;
  if (process.env.XDG_CONFIG_HOME) {
    userConfigPath = path.join(process.env.XDG_CONFIG_HOME, 'caveman', 'config.json');
  } else if (process.platform === 'win32') {
    userConfigPath = path.join(process.env.APPDATA || path.join(os.homedir(), 'AppData', 'Roaming'), 'caveman', 'config.json');
    // Then ~/.config, the path the docs give for every platform.
    return fallbackReadMode(userConfigPath)
      || fallbackReadMode(path.join(os.homedir(), '.config', 'caveman', 'config.json'));
  } else {
    userConfigPath = path.join(os.homedir(), '.config', 'caveman', 'config.json');
  }
  return fallbackReadMode(userConfigPath);
}

// Minimal stand-in for caveman-config.getDefaultMode. Mirrors the real
// resolution order rather than reading only the env var — a degrade that
// ignores a user config saying `defaultMode: "off"` would INVERT the user's
// intent by force-injecting rules, the same reasoning caveman-activate.js
// documents for its own fallback.
function fallbackGetDefaultMode(startDir) {
  // 1. Environment variable. No .trim() — the real resolver does not trim,
  //    and a degraded path accepting " ultra" where the intact one rejects it
  //    is drift in a whitelist.
  const envMode = fallbackCanonicalMode(process.env.CAVEMAN_DEFAULT_MODE);
  if (envMode) return envMode;
  // 2. Repo-local config, walking up. Bounded at 64 like findRepoConfigPath.
  try {
    let dir = path.resolve(startDir || process.cwd());
    for (let i = 0; i < 64; i++) {
      for (const rel of ['.caveman/config.json', '.caveman.json']) {
        const mode = fallbackReadMode(path.join(dir, rel));
        if (mode) return mode;
      }
      const parent = path.dirname(dir);
      if (parent === dir) break;
      dir = parent;
    }
  } catch (e) { /* fall through to user config */ }
  // 3. User config, then 4. the built-in default.
  return fallbackUserConfigMode() || 'caveman';
}

// The hook dir is `<repo>/.codex`, so the shared resolver lives one level up.
// Resolve it once; anything missing just degrades to the hand-copied logic.
const hookDir = __dirname;
let shared = null;
try {
  // eslint-disable-next-line global-require
  shared = require(path.join(hookDir, '..', 'src', 'hooks', 'caveman-config.js'));
} catch (e) {
  try {
    // Standalone install layout: hooks live beside the lib instead.
    // eslint-disable-next-line global-require
    shared = require(path.join(hookDir, 'caveman-config.js'));
  } catch (e2) {
    shared = null;
  }
}

function main() {
  const getDefaultMode = (shared && typeof shared.getDefaultMode === 'function')
    ? shared.getDefaultMode
    : fallbackGetDefaultMode;
  const VALID_MODES = (shared && Array.isArray(shared.VALID_MODES)) ? shared.VALID_MODES : FALLBACK_VALID_MODES;

  const mode = getDefaultMode();
  if (!VALID_MODES.includes(mode) || SILENT_MODES.has(mode)) {
    return;
  }

  if (INDEPENDENT_MODES.has(mode)) {
    process.stdout.write('CAVEMAN MODE ACTIVE — mode: ' + mode + '. Behavior defined by /caveman-' + mode + ' skill.');
    return;
  }

  const loadRuleset = (shared && typeof shared.loadRuleset === 'function')
    ? shared.loadRuleset
    : null;
  const rulesetBanner = (shared && typeof shared.rulesetBanner === 'function')
    ? shared.rulesetBanner
    : ((m) => 'CAVEMAN MODE ACTIVE — mode: ' + m);

  // SKILL.md lives at <repo>/skills/<mode>/SKILL.md. skillPathCandidates()
  // resolves the plugin layouts; a repo checkout is the first candidate it
  // tries. Pass the shared resolver's own directory so the candidates resolve
  // relative to src/hooks/, not this file.
  const skillContent = loadRuleset
    ? loadRuleset(mode, path.join(hookDir, '..', 'src', 'hooks'))
    : null;

  let output;
  if (skillContent) {
    output = rulesetBanner(mode) + '\n\n' + skillContent;
  } else {
    // Degraded one-liner: name the mode and where its rules come from, so a
    // missing SKILL.md degrades the same way on Codex as the Claude hook's
    // fallback does. The old static echo's full text would go stale, which is
    // the defect class this hook exists to end.
    output = rulesetBanner(mode)
      + '. Rules: skills/' + mode + '/SKILL.md. Switch: /caveman, /ultracave, /megacave.'
      + ' Respond terse like smart caveman. All technical substance stay. Only fluff die.'
      + ' Code/commits/security: write normal.';
  }

  process.stdout.write(output);
}

try {
  main();
} catch (e) {
  if (process.env.CAVEMAN_DEBUG === '1') {
    process.stderr.write('[caveman] codex-sessionstart: ' + (e && e.message) + '\n');
  }
  // Swallow: fail-open, exit 0 by falling off the end. Never process.exit()
  // here — pending stdout above must still flush.
}
