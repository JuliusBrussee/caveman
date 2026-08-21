#!/usr/bin/env node
// Tests for the stdout/stderr 'error' handlers in caveman-mode-tracker.js.
// Covers issue #397: an abnormal stdout/stderr close (broken pipe, the
// harness tearing down its read end) emits an 'error' event on the stream;
// without a listener Node throws it as an uncaught exception and the hook
// exits non-zero — the same failure shape #538 fixed on the stdin side.
//
// Run: node tests/test_mode_tracker_stdout.js

const path = require('path');
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

console.log('caveman-mode-tracker stdout/stderr error handling\n');

// Load the REAL hook in a child, then emit an 'error' on the given stream to
// simulate a broken pipe. stdin is left open so the injected 'error' is the
// only event that fires, isolating the handler under test.
function runWithStreamError(streamName) {
  const harness =
    `require(${JSON.stringify(HOOK_PATH)});` +
    `setImmediate(() => process.${streamName}.emit('error', new Error('EPIPE (simulated)')));`;
  return spawnSync(process.execPath, ['-e', harness], {
    stdio: ['pipe', 'ignore', 'pipe'],
    encoding: 'utf8',
  });
}

for (const streamName of ['stdout', 'stderr']) {
  test(`${streamName} "error" event does not crash the hook (exit 0)`, () => {
    const res = runWithStreamError(streamName);
    assert.strictEqual(
      res.status,
      CLEAN_EXIT,
      `expected clean exit on ${streamName} error, got status=${res.status} signal=${res.signal}\n` +
        `stderr: ${(res.stderr || '').trim()}`
    );
    assert.ok(
      !/Unhandled 'error' event/.test(res.stderr || ''),
      `hook leaked an uncaught ${streamName} error:\n${(res.stderr || '').trim()}`
    );
  });
}

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
