#!/usr/bin/env node
// Tests for the release pipeline scripts (scripts/release-bump.js,
// scripts/upstream-check.js). Run: node tests/test_release_scripts.js

const path = require('path');
const assert = require('assert');
const { execFileSync } = require('child_process');

const ROOT = path.resolve(__dirname, '..');
const BUMP = path.join(ROOT, 'scripts', 'release-bump.js');
const CHECK = path.join(ROOT, 'scripts', 'upstream-check.js');

let passed = 0;
let failed = 0;

function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}\n    ${e.message}`);
  }
}

function run(script, ...args) {
  return execFileSync(process.execPath, [script, ...args], { encoding: 'utf8' }).trim();
}

console.log('release script tests\n');

test('--check reports a single consistent version', () => {
  const v = run(BUMP, '--check');
  assert.match(v, /^\d+\.\d+\.\d+$/);
});

test('--bump then --set restores the original version everywhere', () => {
  const original = run(BUMP, '--check');
  const bumped = run(BUMP, '--bump', 'patch');
  const [maj, min, pat] = original.split('.').map(Number);
  assert.strictEqual(bumped, `${maj}.${min}.${pat + 1}`);
  assert.strictEqual(run(BUMP, '--check'), bumped); // all sync points moved
  assert.strictEqual(run(BUMP, '--set', original), original);
  assert.strictEqual(run(BUMP, '--check'), original); // fully restored
});

test('--set rejects malformed versions', () => {
  let err = null;
  try { run(BUMP, '--set', 'not-a-version'); } catch (e) { err = e; }
  assert.ok(err, 'should exit non-zero');
});

test('upstream-check decide() enforces policies', () => {
  const { decide } = require(CHECK);
  // major policy: only major bumps trigger
  assert.strictEqual(decide('v1.9.0', 'v2.0.0', 'major').trigger, true);
  assert.strictEqual(decide('v1.9.0', 'v1.10.0', 'major').trigger, false);
  assert.strictEqual(decide('v1.9.0', 'v1.9.1', 'major').trigger, false);
  // minor policy: minor or major triggers, patch does not
  assert.strictEqual(decide('v0.43.0', 'v0.44.0', 'minor').trigger, true);
  assert.strictEqual(decide('v0.43.0', 'v1.0.0', 'minor').trigger, true);
  assert.strictEqual(decide('v0.43.0', 'v0.43.9', 'minor').trigger, false);
  // patch policy: any increase triggers
  assert.strictEqual(decide('v0.43.0', 'v0.43.1', 'patch').trigger, true);
  // never trigger on same or older
  assert.strictEqual(decide('v2.0.0', 'v2.0.0', 'patch').trigger, false);
  assert.strictEqual(decide('v2.0.0', 'v1.9.9', 'patch').trigger, false);
  // garbage tags never trigger
  assert.strictEqual(decide('v1.0.0', 'nightly-build', 'patch').trigger, false);
});

test('upstream-check CLI reads the state file and prints a decision', () => {
  const out = run(CHECK, 'rtk', 'v0.43.0');
  assert.match(out, /^skip /); // tracked version — nothing to do
  const out2 = run(CHECK, 'rtk', 'v0.99.0');
  assert.match(out2, /^trigger v0\.99\.0$/);
  const out3 = run(CHECK, 'upstream-caveman', 'v1.10.0');
  assert.match(out3, /^skip .*not a major bump/);
});

test('upstream-check rejects unknown upstream names', () => {
  let err = null;
  try { run(CHECK, 'nonexistent', 'v1.0.0'); } catch (e) { err = e; }
  assert.ok(err, 'should exit non-zero');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
