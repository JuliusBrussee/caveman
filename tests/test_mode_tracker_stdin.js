#!/usr/bin/env node
// Tests for the stdin 'error' handler in caveman-mode-tracker.js.
// Covers issue #538: an abnormal stdin close (broken pipe, parent crash) emits
// an 'error' event on process.stdin; without a listener Node throws it as an
// uncaught exception and the hook exits non-zero — a spurious hook failure.
//
// Run: node tests/test_mode_tracker_stdin.js

const path = require('path');
const os = require('os');
const fs = require('fs');
const assert = require('assert');
const { spawnSync } = require('child_process');

const HOOK_PATH = path.resolve(__dirname, '..', 'src', 'hooks', 'caveman-mode-tracker.js');
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

console.log('caveman-mode-tracker stdin error handling\n');

// Load the REAL hook in a child, then emit an 'error' on process.stdin to
// simulate an abnormal close. stdin is left open (never closed) so the only
// event that fires is the injected 'error' — isolating the handler under test.
function runWithStdinError() {
  const harness =
    `require(${JSON.stringify(HOOK_PATH)});` +
    `setImmediate(() => process.stdin.emit('error', new Error('EPIPE (simulated)')));`;
  return spawnSync(process.execPath, ['-e', harness], {
    stdio: ['pipe', 'ignore', 'pipe'],
    encoding: 'utf8',
  });
}

test('stdin "error" event does not crash the hook (exit 0)', () => {
  const res = runWithStdinError();
  assert.strictEqual(
    res.status,
    CLEAN_EXIT,
    `expected clean exit on stdin error, got status=${res.status} signal=${res.signal}\n` +
      `stderr: ${(res.stderr || '').trim()}`
  );
  assert.ok(
    !/Unhandled 'error' event/.test(res.stderr || ''),
    `hook leaked an uncaught stdin error:\n${(res.stderr || '').trim()}`
  );
});

// Regression guard: the new listener must not disturb the normal path — a valid
// prompt piped on stdin, then a clean EOF, still exits 0.
test('normal stdin (valid JSON + clean EOF) still exits 0', () => {
  const tmpConfig = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-tracker-stdin-'));
  try {
    const res = spawnSync(process.execPath, [HOOK_PATH], {
      input: JSON.stringify({ prompt: 'hello there' }),
      env: { ...process.env, CLAUDE_CONFIG_DIR: tmpConfig },
      stdio: ['pipe', 'ignore', 'pipe'],
      encoding: 'utf8',
    });
    assert.strictEqual(
      res.status,
      CLEAN_EXIT,
      `expected clean exit on normal input, got status=${res.status}\n` +
        `stderr: ${(res.stderr || '').trim()}`
    );
  } finally {
    fs.rmSync(tmpConfig, { recursive: true, force: true });
  }
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
