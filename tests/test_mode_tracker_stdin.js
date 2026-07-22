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

// --- Claude Code's 5s hook timeout -----------------------------------------
// A complete payload can arrive well before the caller closes stdin. Doing the
// work only in the 'end' listener meant the hook sat idle until EOF and was
// killed at the timeout — silently, injecting nothing.
//
// Driven through a child so the assertions here stay synchronous: the driver
// writes one complete payload, leaves stdin OPEN, and reports how long the hook
// took to exit on its own plus whatever it managed to write.
const HOOK_TIMEOUT_MS = 5000;

function runWithStdinHeldOpen(configDir, prompt) {
  const driver = `
    const { spawn } = require('child_process');
    const started = Date.now();
    const p = spawn(process.execPath, [${JSON.stringify(HOOK_PATH)}], {
      stdio: ['pipe', 'pipe', 'ignore'],
      env: { ...process.env, CLAUDE_CONFIG_DIR: ${JSON.stringify(configDir)} },
    });
    let out = '';
    p.stdout.on('data', c => { out += c; });
    p.stdin.write(JSON.stringify({ prompt: ${JSON.stringify(prompt)} }));   // stdin stays open
    const kill = setTimeout(() => { p.kill('SIGKILL'); }, ${HOOK_TIMEOUT_MS * 2});
    p.on('exit', (code, signal) => {
      clearTimeout(kill);
      process.stdout.write(JSON.stringify({ ms: Date.now() - started, code, signal, out }));
    });
  `;
  const res = spawnSync(process.execPath, ['-e', driver], { encoding: 'utf8' });
  return JSON.parse(res.stdout);
}

test('a complete payload is acted on before the 5s hook timeout, without EOF', () => {
  const tmpConfig = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-tracker-stdin-'));
  try {
    const r = runWithStdinHeldOpen(tmpConfig, 'hello there');
    assert.ok(
      r.signal === null && r.code === CLEAN_EXIT,
      `hook did not exit on its own with stdin held open (code=${r.code} signal=${r.signal} ` +
        `after ${r.ms}ms) — Claude Code kills it at ${HOOK_TIMEOUT_MS}ms`
    );
    assert.ok(
      r.ms < HOOK_TIMEOUT_MS,
      `hook took ${r.ms}ms with stdin held open; the hook timeout is ${HOOK_TIMEOUT_MS}ms`
    );
  } finally {
    fs.rmSync(tmpConfig, { recursive: true, force: true });
  }
});

test('the per-turn reinforcement is still emitted when stdin never closes', () => {
  const tmpConfig = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-tracker-stdin-'));
  try {
    fs.writeFileSync(path.join(tmpConfig, '.caveman-active'), 'full');
    const r = runWithStdinHeldOpen(tmpConfig, 'hello there');
    assert.ok(
      /CAVEMAN MODE ACTIVE/.test(r.out || ''),
      `expected the additionalContext injection on stdout, got ${JSON.stringify(r.out)} ` +
        `(exit code=${r.code} signal=${r.signal} after ${r.ms}ms)`
    );
  } finally {
    fs.rmSync(tmpConfig, { recursive: true, force: true });
  }
});

// The payload can also arrive split across chunks; a partial one must not be
// mistaken for a broken one, and the body must still run exactly once.
test('a payload split across chunks is handled once, and only once', () => {
  const tmpConfig = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-tracker-stdin-'));
  try {
    fs.writeFileSync(path.join(tmpConfig, '.caveman-active'), 'full');
    const driver = `
      const { spawn } = require('child_process');
      const p = spawn(process.execPath, [${JSON.stringify(HOOK_PATH)}], {
        stdio: ['pipe', 'pipe', 'ignore'],
        env: { ...process.env, CLAUDE_CONFIG_DIR: ${JSON.stringify(tmpConfig)} },
      });
      let out = '';
      p.stdout.on('data', c => { out += c; });
      const payload = JSON.stringify({ prompt: 'hello there' });
      p.stdin.write(payload.slice(0, 8));
      setTimeout(() => { p.stdin.write(payload.slice(8)); p.stdin.end(); }, 50);
      p.on('exit', (code, signal) =>
        process.stdout.write(JSON.stringify({ code, signal, out })));
    `;
    const r = JSON.parse(spawnSync(process.execPath, ['-e', driver], { encoding: 'utf8' }).stdout);
    assert.strictEqual(r.code, CLEAN_EXIT, `expected clean exit, got code=${r.code} signal=${r.signal}`);
    const injections = (r.out.match(/CAVEMAN MODE ACTIVE/g) || []).length;
    assert.strictEqual(
      injections,
      1,
      `expected exactly one injection for one payload, got ${injections}: ${JSON.stringify(r.out)}`
    );
  } finally {
    fs.rmSync(tmpConfig, { recursive: true, force: true });
  }
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
