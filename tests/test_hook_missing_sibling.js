#!/usr/bin/env node
// Tests for degraded sibling loading in the hook entrypoints.
// Covers issue #848: caveman-activate.js, caveman-mode-tracker.js and
// caveman-stats.js required './caveman-config' at module top level with no
// guard, so an install that is missing that one file produced an uncaught
// MODULE_NOT_FOUND — a raw Node stack trace and exit 1 on EVERY session start
// and EVERY prompt, reported by Claude Code as an opaque
// "SessionStart:startup hook error ... node:internal/modules/cjs/loader".
// Same failure class as #801 (installer copy list omitting caveman-parse.js).
//
// Run: node tests/test_hook_missing_sibling.js

const path = require('path');
const os = require('os');
const fs = require('fs');
const assert = require('assert');
const { spawnSync } = require('child_process');

const HOOKS_DIR = path.resolve(__dirname, '..', 'src', 'hooks');
const CLEAN_EXIT = 0;

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
    console.error(`    ${e.message}`);
  }
}

console.log('caveman hooks: missing/broken sibling degradation\n');

// A hook dir holding real copies of every hook file, minus `omit`, plus any
// `overrides` written verbatim. Copying rather than symlinking keeps require
// resolution inside the temp dir, so the real src/hooks siblings can never
// satisfy a require the test is trying to break.
function makeHookDir(omit, overrides) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-sibling-'));
  for (const name of fs.readdirSync(HOOKS_DIR)) {
    if (name === omit) continue;
    const src = path.join(HOOKS_DIR, name);
    if (fs.statSync(src).isFile()) fs.copyFileSync(src, path.join(dir, name));
  }
  for (const [name, body] of Object.entries(overrides || {})) {
    fs.writeFileSync(path.join(dir, name), body);
  }
  return dir;
}

function run(hookDir, hookName, payload) {
  const configDir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-sibling-cfg-'));
  const res = spawnSync(process.execPath, [path.join(hookDir, hookName)], {
    input: payload === undefined ? '' : JSON.stringify(payload),
    // A stale CAVEMAN_DEFAULT_MODE in the developer's shell would otherwise
    // change which mode the degraded path reports.
    env: { ...process.env, CLAUDE_CONFIG_DIR: configDir, CAVEMAN_DEFAULT_MODE: '' },
    stdio: ['pipe', 'pipe', 'pipe'],
    encoding: 'utf8',
  });
  res.configDir = configDir;
  return res;
}

function cleanup(...dirs) {
  for (const d of dirs) fs.rmSync(d, { recursive: true, force: true });
}

// The exact shape that reached users: an unhandled require failure prints the
// loader frame plus a "Require stack:" block. Neither may ever appear again.
function assertNoStackTrace(res) {
  const err = res.stderr || '';
  assert.ok(!/Require stack:/.test(err), `hook leaked a require stack:\n${err.trim()}`);
  assert.ok(
    !/node:internal\/modules\/cjs\/loader/.test(err),
    `hook leaked a module-loader stack trace:\n${err.trim()}`
  );
}

// ---------- SessionStart (caveman-activate.js) ----------

test('activate: missing caveman-config.js still emits the ruleset and exits 0', () => {
  const dir = makeHookDir('caveman-config.js');
  const res = run(dir, 'caveman-activate.js', { source: 'startup' });
  try {
    assert.strictEqual(
      res.status,
      CLEAN_EXIT,
      `expected clean exit, got status=${res.status}\nstderr: ${(res.stderr || '').trim()}`
    );
    assertNoStackTrace(res);
    assert.match(
      res.stdout || '',
      /CAVEMAN MODE ACTIVE/,
      'degraded SessionStart must still inject the fallback ruleset'
    );
    assert.match(
      res.stderr || '',
      /caveman-config\.js is missing from .*— the install is incomplete/,
      'stderr must name the missing file and the remedy'
    );
  } finally {
    cleanup(dir, res.configDir);
  }
});

test('activate: a sibling that throws at load is reported as a load error, not a missing file', () => {
  const dir = makeHookDir(null, {
    'caveman-config.js': 'throw new Error("boom from inside caveman-config");\n',
  });
  const res = run(dir, 'caveman-activate.js', { source: 'startup' });
  try {
    assert.strictEqual(res.status, CLEAN_EXIT, `expected clean exit, got status=${res.status}`);
    assertNoStackTrace(res);
    assert.match(res.stdout || '', /CAVEMAN MODE ACTIVE/);
    assert.match(
      res.stderr || '',
      /could not load caveman-config\.js — boom from inside caveman-config/,
      'a broken sibling must not be misreported as an incomplete install'
    );
  } finally {
    cleanup(dir, res.configDir);
  }
});

test('activate: a MODULE_NOT_FOUND raised *inside* the sibling is not misreported as missing', () => {
  const dir = makeHookDir(null, {
    'caveman-config.js': "require('./definitely-not-a-real-module');\n",
  });
  const res = run(dir, 'caveman-activate.js', { source: 'startup' });
  try {
    assert.strictEqual(res.status, CLEAN_EXIT, `expected clean exit, got status=${res.status}`);
    assertNoStackTrace(res);
    assert.match(
      res.stderr || '',
      /could not load caveman-config\.js — Cannot find module '\.\/definitely-not-a-real-module'/,
      'nested resolution failures must surface the inner module, not "install is incomplete"'
    );
  } finally {
    cleanup(dir, res.configDir);
  }
});

test('activate: control — complete hook dir writes the flag and emits no warning', () => {
  const dir = makeHookDir(null);
  const res = run(dir, 'caveman-activate.js', { source: 'startup' });
  try {
    assert.strictEqual(res.status, CLEAN_EXIT);
    assert.match(res.stdout || '', /CAVEMAN MODE ACTIVE/);
    assert.strictEqual(
      fs.readFileSync(path.join(res.configDir, '.caveman-active'), 'utf8'),
      'full',
      'the non-degraded path must still persist the mode flag'
    );
    assert.ok(
      !/install is incomplete|could not load/.test(res.stderr || ''),
      `healthy install must not warn:\n${(res.stderr || '').trim()}`
    );
  } finally {
    cleanup(dir, res.configDir);
  }
});

// ---------- UserPromptSubmit (caveman-mode-tracker.js) ----------

for (const omitted of ['caveman-config.js', 'caveman-parse.js']) {
  test(`mode-tracker: missing ${omitted} exits 0 and emits nothing`, () => {
    const dir = makeHookDir(omitted);
    const res = run(dir, 'caveman-mode-tracker.js', { prompt: 'fix the auth bug' });
    try {
      assert.strictEqual(
        res.status,
        CLEAN_EXIT,
        `expected clean exit, got status=${res.status}\nstderr: ${(res.stderr || '').trim()}`
      );
      assertNoStackTrace(res);
      assert.strictEqual(
        (res.stdout || '').trim(),
        '',
        'a degraded tracker must inject nothing into the prompt'
      );
      assert.match(res.stderr || '', /install is incomplete|could not load/);
    } finally {
      cleanup(dir, res.configDir);
    }
  });
}

test('mode-tracker: control — complete hook dir still reinforces an active mode', () => {
  const dir = makeHookDir(null);
  const configDir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-sibling-cfg-'));
  try {
    fs.writeFileSync(path.join(configDir, '.caveman-active'), 'full');
    const res = spawnSync(process.execPath, [path.join(dir, 'caveman-mode-tracker.js')], {
      input: JSON.stringify({ prompt: 'fix the auth bug' }),
      env: { ...process.env, CLAUDE_CONFIG_DIR: configDir },
      stdio: ['pipe', 'pipe', 'pipe'],
      encoding: 'utf8',
    });
    assert.strictEqual(res.status, CLEAN_EXIT);
    assert.match(res.stdout || '', /CAVEMAN MODE ACTIVE \(full\)/);
  } finally {
    cleanup(dir, configDir);
  }
});

// ---------- /caveman-stats subprocess (caveman-stats.js) ----------

test('stats: missing caveman-config.js prints one actionable line, no stack trace', () => {
  const dir = makeHookDir('caveman-config.js');
  const res = run(dir, 'caveman-stats.js');
  try {
    assertNoStackTrace(res);
    assert.notStrictEqual(res.status, CLEAN_EXIT, 'stats must report that it did not run');
    assert.match(
      res.stderr || '',
      /caveman-stats: caveman-config\.js is missing from .*— the install is incomplete/
    );
  } finally {
    cleanup(dir, res.configDir);
  }
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
