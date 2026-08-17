#!/usr/bin/env node
// Tests for the sibling-require guard in the hook entrypoints.
// Covers issue #848: caveman-activate.js, caveman-mode-tracker.js and
// caveman-stats.js required './caveman-config' at module top level with no
// guard, so an install missing that one file produced an uncaught
// MODULE_NOT_FOUND — a raw Node stack trace and exit 1 on EVERY session start
// and EVERY prompt, which Claude Code surfaces only as:
//
//   SessionStart:startup hook error
//   Failed with non-blocking status code: node:internal/modules/cjs/loader:1408
//
// Run: node tests/test_hook_missing_sibling.js

const path = require('path');
const os = require('os');
const fs = require('fs');
const assert = require('assert');
const { spawnSync } = require('child_process');

const HOOKS_DIR = path.resolve(__dirname, '..', 'src', 'hooks');
const SKILL_SRC = path.resolve(__dirname, '..', 'skills');

let passed = 0;
let failed = 0;

function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}`);
    console.error(`    ${e.stack || e.message}`);
  }
}

// Build a hook directory that is a faithful copy of src/hooks minus the files
// named in `omit`, laid out so the relative SKILL.md lookup still resolves
// (<root>/src/hooks/ alongside <root>/skills/).
function makeInstall(omit = []) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-sibling-'));
  const hooks = path.join(root, 'src', 'hooks');
  fs.mkdirSync(hooks, { recursive: true });
  for (const entry of fs.readdirSync(HOOKS_DIR)) {
    if (omit.includes(entry)) continue;
    const src = path.join(HOOKS_DIR, entry);
    if (!fs.statSync(src).isFile()) continue;
    fs.copyFileSync(src, path.join(hooks, entry));
  }
  fs.cpSync(SKILL_SRC, path.join(root, 'skills'), { recursive: true });
  return { root, hooks };
}

function runHook(hooks, name, { stdin = '', env = {} } = {}) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-home-'));
  try {
    return spawnSync(process.execPath, [path.join(hooks, name)], {
      input: stdin,
      encoding: 'utf8',
      env: { ...process.env, CLAUDE_CONFIG_DIR: home, ...env },
    });
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
}

function withInstall(omit, fn) {
  const install = makeInstall(omit);
  try {
    return fn(install);
  } finally {
    fs.rmSync(install.root, { recursive: true, force: true });
  }
}

const SESSION_START = JSON.stringify({
  session_id: 't', cwd: '/tmp', hook_event_name: 'SessionStart', source: 'startup',
});

console.log('\ncaveman-activate.js — missing caveman-config.js');

test('exits 0 instead of crashing with MODULE_NOT_FOUND', () => {
  withInstall(['caveman-config.js'], ({ hooks }) => {
    const r = runHook(hooks, 'caveman-activate.js', { stdin: SESSION_START });
    assert.strictEqual(r.status, 0, `expected exit 0, got ${r.status}\n${r.stderr}`);
    assert.doesNotMatch(r.stderr, /MODULE_NOT_FOUND/, 'raw loader error must not surface');
    assert.doesNotMatch(r.stderr, /Require stack:/, 'Node require-stack noise must not surface');
  });
});

test('still emits a usable ruleset so a degraded session keeps caveman', () => {
  withInstall(['caveman-config.js'], ({ hooks }) => {
    const r = runHook(hooks, 'caveman-activate.js', { stdin: SESSION_START });
    assert.match(r.stdout, /CAVEMAN MODE ACTIVE/, 'ruleset must still be injected');
  });
});

test('names the missing file and the remedy on stderr', () => {
  withInstall(['caveman-config.js'], ({ hooks }) => {
    const r = runHook(hooks, 'caveman-activate.js', { stdin: SESSION_START });
    assert.match(r.stderr, /caveman-config\.js is missing/, 'must name the absent file');
    assert.match(r.stderr, /install is incomplete/, 'must name the cause');
    assert.match(r.stderr, /plugin update caveman|install\.sh/, 'must name the remedy');
  });
});

test('CAVEMAN_DEFAULT_MODE=off still opts out without the config module', () => {
  withInstall(['caveman-config.js'], ({ hooks }) => {
    const r = runHook(hooks, 'caveman-activate.js', {
      stdin: SESSION_START,
      env: { CAVEMAN_DEFAULT_MODE: 'off' },
    });
    assert.strictEqual(r.status, 0);
    assert.doesNotMatch(r.stdout, /CAVEMAN MODE ACTIVE/, 'off must suppress the ruleset');
  });
});

console.log('\ncaveman-mode-tracker.js — missing siblings');

test('missing caveman-config.js: exits 0, emits nothing', () => {
  withInstall(['caveman-config.js'], ({ hooks }) => {
    const r = runHook(hooks, 'caveman-mode-tracker.js', {
      stdin: JSON.stringify({ prompt: '/caveman ultra' }),
    });
    assert.strictEqual(r.status, 0, `expected exit 0, got ${r.status}\n${r.stderr}`);
    assert.strictEqual(r.stdout.trim(), '', 'must not inject anything while degraded');
    assert.doesNotMatch(r.stderr, /MODULE_NOT_FOUND/);
  });
});

test('missing caveman-parse.js: exits 0, emits nothing', () => {
  withInstall(['caveman-parse.js'], ({ hooks }) => {
    const r = runHook(hooks, 'caveman-mode-tracker.js', {
      stdin: JSON.stringify({ prompt: '/caveman ultra' }),
    });
    assert.strictEqual(r.status, 0, `expected exit 0, got ${r.status}\n${r.stderr}`);
    assert.match(r.stderr, /caveman-parse\.js is missing/, 'must name the absent file');
    assert.doesNotMatch(r.stderr, /MODULE_NOT_FOUND/);
  });
});

console.log('\ncaveman-stats.js — missing caveman-config.js');

test('prints one actionable line instead of a stack trace', () => {
  withInstall(['caveman-config.js'], ({ hooks }) => {
    const r = runHook(hooks, 'caveman-stats.js');
    assert.notStrictEqual(r.status, 0, 'stats has no useful degraded output — must fail loudly');
    assert.match(r.stderr, /caveman-config\.js is missing/);
    assert.doesNotMatch(r.stderr, /MODULE_NOT_FOUND/);
    assert.doesNotMatch(r.stderr, /Require stack:/);
    assert.ok(r.stderr.trim().split('\n').length <= 3, `expected a short message, got:\n${r.stderr}`);
  });
});

console.log('\nmissing vs. broken is distinguished');

test('a sibling that loads but throws is not reported as an incomplete install', () => {
  withInstall([], ({ hooks }) => {
    // caveman-config.js is present, but requires something that is not.
    fs.writeFileSync(
      path.join(hooks, 'caveman-config.js'),
      "require('./definitely-not-here');\n",
    );
    const r = runHook(hooks, 'caveman-activate.js', { stdin: SESSION_START });
    assert.strictEqual(r.status, 0, `expected exit 0, got ${r.status}\n${r.stderr}`);
    assert.doesNotMatch(
      r.stderr, /is missing from/,
      'the file IS present — pointing at the wrong cause is worse than no message',
    );
    assert.match(r.stderr, /could not load/, 'must report a load failure instead');
    assert.match(r.stderr, /definitely-not-here/, 'must name what actually failed to resolve');
  });
});

test('the echoed error is one line, not the whole require stack', () => {
  withInstall([], ({ hooks }) => {
    fs.writeFileSync(
      path.join(hooks, 'caveman-config.js'),
      "require('./definitely-not-here');\n",
    );
    const r = runHook(hooks, 'caveman-activate.js', { stdin: SESSION_START });
    assert.doesNotMatch(r.stderr, /Require stack:/, 'Node appends a multi-line block — it must be trimmed');
  });
});

console.log('\nintact install is unaffected');

test('all three entrypoints behave normally when nothing is missing', () => {
  withInstall([], ({ hooks }) => {
    const activate = runHook(hooks, 'caveman-activate.js', { stdin: SESSION_START });
    assert.strictEqual(activate.status, 0);
    assert.match(activate.stdout, /CAVEMAN MODE ACTIVE/);
    assert.doesNotMatch(activate.stderr, /install is incomplete|could not load/);

    const tracker = runHook(hooks, 'caveman-mode-tracker.js', {
      stdin: JSON.stringify({ prompt: 'hello' }),
    });
    assert.strictEqual(tracker.status, 0);
    assert.doesNotMatch(tracker.stderr, /install is incomplete|could not load/);
  });
});

console.log('\nstale module shape (the plugin-cache-drift case #848 describes)');

// A sibling that LOADS but exports the wrong shape is not caught by a
// try/catch. Before the shape check, the destructure succeeded and the first
// use dereferenced undefined — a raw stack trace and exit 1 on SessionStart,
// i.e. the exact bug this guard is named after, and a violation of the
// "never let a hook crash block session start" rule in CLAUDE.md.
function withStaleConfig(body, fn) {
  return withInstall([], (install) => {
    fs.writeFileSync(path.join(install.hooks, 'caveman-config.js'), body);
    return fn(install);
  });
}

test('activate: stale config module degrades instead of throwing TypeError', () => {
  withStaleConfig('module.exports = { getDefaultMode: () => "full" };\n', ({ hooks }) => {
    const r = runHook(hooks, 'caveman-activate.js', { stdin: SESSION_START });
    assert.strictEqual(r.status, 0, `expected exit 0, got ${r.status}\n${r.stderr}`);
    assert.doesNotMatch(r.stderr, /TypeError/, 'must not surface a raw TypeError');
    assert.match(r.stderr, /missing expected exports/, 'must name the real cause');
    assert.match(r.stdout, /CAVEMAN MODE ACTIVE/, 'degraded session still gets its rules');
  });
});

test('stats: stale config module exits with one line, not a TypeError', () => {
  withStaleConfig('module.exports = { readFlag: () => null };\n', ({ hooks }) => {
    const r = runHook(hooks, 'caveman-stats.js');
    assert.notStrictEqual(r.status, 0);
    assert.doesNotMatch(r.stderr, /TypeError/);
    assert.match(r.stderr, /missing expected exports/);
  });
});

test('tracker: stale config module exits 0 and emits nothing', () => {
  withStaleConfig('module.exports = { readFlag: () => null };\n', ({ hooks }) => {
    const r = runHook(hooks, 'caveman-mode-tracker.js', { stdin: JSON.stringify({ prompt: '/caveman ultra' }) });
    assert.strictEqual(r.status, 0, r.stderr);
    assert.strictEqual(r.stdout.trim(), '');
  });
});

console.log('\ndegrading must not INVERT a persisted opt-out');

// The fallback resolver must mirror caveman-config's resolution order. Reading
// only CAVEMAN_DEFAULT_MODE would force caveman ON for a team that checked in
// an opt-out, the moment one file goes missing — that is not degrading toward
// the user's intent, it is reversing it.
function runActivateIn(hooks, cwd, env = {}) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-home-'));
  try {
    return spawnSync(process.execPath, [path.join(hooks, 'caveman-activate.js')], {
      input: SESSION_START,
      encoding: 'utf8',
      cwd,
      env: { ...process.env, CLAUDE_CONFIG_DIR: home, HOME: home, USERPROFILE: home, ...env },
    });
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
}

test('repo-local .caveman.json defaultMode:off is honored while degraded', () => {
  withInstall(['caveman-config.js'], ({ hooks, root }) => {
    const repo = path.join(root, 'repo');
    fs.mkdirSync(repo, { recursive: true });
    fs.writeFileSync(path.join(repo, '.caveman.json'), JSON.stringify({ defaultMode: 'off' }));
    const r = runActivateIn(hooks, repo);
    assert.strictEqual(r.status, 0);
    assert.doesNotMatch(r.stdout, /CAVEMAN MODE ACTIVE/, 'a checked-in opt-out must survive a degraded install');
  });
});

test('repo-local opt-out is found from a subdirectory', () => {
  withInstall(['caveman-config.js'], ({ hooks, root }) => {
    const repo = path.join(root, 'repo2');
    const deep = path.join(repo, 'src', 'nested');
    fs.mkdirSync(deep, { recursive: true });
    fs.mkdirSync(path.join(repo, '.caveman'), { recursive: true });
    fs.writeFileSync(path.join(repo, '.caveman', 'config.json'), JSON.stringify({ defaultMode: 'off' }));
    assert.doesNotMatch(runActivateIn(hooks, deep).stdout, /CAVEMAN MODE ACTIVE/);
  });
});

test('user config defaultMode:off is honored while degraded', () => {
  withInstall(['caveman-config.js'], ({ hooks, root }) => {
    const xdg = path.join(root, 'xdg');
    fs.mkdirSync(path.join(xdg, 'caveman'), { recursive: true });
    fs.writeFileSync(path.join(xdg, 'caveman', 'config.json'), JSON.stringify({ defaultMode: 'off' }));
    const cwd = path.join(root, 'plain');
    fs.mkdirSync(cwd, { recursive: true });
    assert.doesNotMatch(
      runActivateIn(hooks, cwd, { XDG_CONFIG_HOME: xdg }).stdout,
      /CAVEMAN MODE ACTIVE/,
    );
  });
});

test('degraded resolution still activates when nothing opts out', () => {
  withInstall(['caveman-config.js'], ({ hooks, root }) => {
    const cwd = path.join(root, 'plain2');
    fs.mkdirSync(cwd, { recursive: true });
    assert.match(runActivateIn(hooks, cwd).stdout, /CAVEMAN MODE ACTIVE/);
  });
});

test('degraded fallback mode list matches caveman-config exactly', () => {
  const config = fs.readFileSync(path.join(HOOKS_DIR, 'caveman-config.js'), 'utf8');
  const activate = fs.readFileSync(path.join(HOOKS_DIR, 'caveman-activate.js'), 'utf8');
  const listOf = (src, name) => {
    const m = new RegExp(name + '\\s*=\\s*\\[([^\\]]*)\\]').exec(src);
    assert.ok(m, `could not find ${name}`);
    return m[1].split(',').map(v => v.trim().replace(/^['"]|['"]$/g, '')).filter(Boolean).sort();
  };
  assert.deepStrictEqual(
    listOf(activate, 'FALLBACK_VALID_MODES'),
    listOf(config, 'VALID_MODES'),
    'the hand-copied fallback list has drifted from caveman-config.js',
  );
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
