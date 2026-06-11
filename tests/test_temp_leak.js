#!/usr/bin/env node
// Tests for temp-file cleanup in safeWriteFlag and sweepOrphanTemps.
// safeWriteFlag writes the flag via an atomic temp + rename. On Windows,
// renameSync over an existing file fails with EPERM/EBUSY when another
// process holds the target open (statusline read, concurrent hook), and the
// silent-fail catch used to leak one .caveman-active.<pid>.<timestamp> temp
// per failed rename — accumulating indefinitely in $CLAUDE_CONFIG_DIR.
//
// Run: node tests/test_temp_leak.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');

const { safeWriteFlag, sweepOrphanTemps } = require('../src/hooks/caveman-config');

let passed = 0;
let failed = 0;

function test(name, fn) {
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-temp-leak-test-'));
  try {
    fn(tmpBase);
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}`);
    console.error(`    ${e.message}`);
  } finally {
    fs.rmSync(tmpBase, { recursive: true, force: true });
  }
}

console.log('safeWriteFlag temp cleanup + sweepOrphanTemps tests\n');

// ---------- safeWriteFlag temp cleanup ----------

test('successful write leaves no temp files behind', (tmp) => {
  const flagDir = path.join(tmp, 'claude-config');
  fs.mkdirSync(flagDir, { recursive: true });
  const flagPath = path.join(flagDir, '.caveman-active');

  safeWriteFlag(flagPath, 'full');

  const leftovers = fs.readdirSync(flagDir).filter(f => f !== '.caveman-active');
  assert.deepStrictEqual(leftovers, [], `unexpected leftovers: ${leftovers}`);
  assert.strictEqual(fs.readFileSync(flagPath, 'utf8'), 'full');
});

test('failed rename does not leak the temp file', (tmp) => {
  const flagDir = path.join(tmp, 'claude-config');
  fs.mkdirSync(flagDir, { recursive: true });
  const flagPath = path.join(flagDir, '.caveman-active');
  fs.writeFileSync(flagPath, 'full');

  // Simulate the Windows EPERM/EBUSY failure mode
  const origRename = fs.renameSync;
  fs.renameSync = () => {
    const err = new Error('EPERM: operation not permitted');
    err.code = 'EPERM';
    throw err;
  };
  try {
    safeWriteFlag(flagPath, 'ultra'); // must silent-fail, not throw
  } finally {
    fs.renameSync = origRename;
  }

  const files = fs.readdirSync(flagDir);
  assert.deepStrictEqual(files, ['.caveman-active'], `temp leaked: ${files}`);
  // Original flag content untouched by the failed write
  assert.strictEqual(fs.readFileSync(flagPath, 'utf8'), 'full');
});

// ---------- sweepOrphanTemps ----------

test('sweeps temp older than 24h regardless of PID', (tmp) => {
  const old = Date.now() - 25 * 60 * 60 * 1000;
  const orphan = path.join(tmp, `.caveman-active.${process.pid}.${old}`);
  fs.writeFileSync(orphan, 'full');

  sweepOrphanTemps(tmp);

  assert.ok(!fs.existsSync(orphan), 'old orphan should be removed');
});

test('sweeps temp with dead PID after 60s grace', (tmp) => {
  // PID 999999999 is far above any real PID space on win32/linux/darwin
  const ts = Date.now() - 5 * 60 * 1000;
  const orphan = path.join(tmp, `.caveman-active.999999999.${ts}`);
  fs.writeFileSync(orphan, 'full');

  sweepOrphanTemps(tmp);

  assert.ok(!fs.existsSync(orphan), 'dead-PID orphan should be removed');
});

test('keeps fresh temp from a live PID (in-flight write)', (tmp) => {
  const fresh = path.join(tmp, `.caveman-active.${process.pid}.${Date.now()}`);
  fs.writeFileSync(fresh, 'full');

  sweepOrphanTemps(tmp);

  assert.ok(fs.existsSync(fresh), 'fresh live-PID temp must survive');
});

test('keeps recent temp from a dead PID inside 60s grace window', (tmp) => {
  const fresh = path.join(tmp, `.caveman-active.999999999.${Date.now()}`);
  fs.writeFileSync(fresh, 'full');

  sweepOrphanTemps(tmp);

  assert.ok(fs.existsSync(fresh), 'temp inside grace window must survive');
});

test('never touches the live flag file or unrelated files', (tmp) => {
  const flagPath = path.join(tmp, '.caveman-active');
  fs.writeFileSync(flagPath, 'full');
  const unrelated = path.join(tmp, '.caveman-history.jsonl');
  fs.writeFileSync(unrelated, '{}');

  sweepOrphanTemps(tmp);

  assert.ok(fs.existsSync(flagPath), 'live flag must survive');
  assert.ok(fs.existsSync(unrelated), 'unrelated file must survive');
});

test('skips symlinks matching the temp pattern', (tmp) => {
  const victim = path.join(tmp, 'victim.txt');
  fs.writeFileSync(victim, 'precious');
  const old = Date.now() - 25 * 60 * 60 * 1000;
  const link = path.join(tmp, `.caveman-active.12345.${old}`);
  try {
    fs.symlinkSync(victim, link);
  } catch (e) {
    // symlink creation can require privileges on Windows — skip silently
    return;
  }

  sweepOrphanTemps(tmp);

  assert.ok(fs.existsSync(victim), 'symlink target must survive');
});

test('silent-fails on nonexistent directory', () => {
  sweepOrphanTemps(path.join(os.tmpdir(), 'caveman-does-not-exist-' + Date.now()));
});

// ---------- Summary ----------

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
