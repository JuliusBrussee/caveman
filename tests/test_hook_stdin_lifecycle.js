#!/usr/bin/env node
// Both hooks must finish their work and EXIT while the host still holds the
// stdin write end open.
//
// Claude Code writes one JSON payload and closes, but under the Windows pipe
// implementation that close lags arbitrarily (#729/#833). Attaching a 'data'
// listener puts stdin in flowing mode and REFERENCES the handle, so a hook that
// only calls pause() keeps the event loop alive until the host closes — which
// means a hook whose real work takes ~50ms sits idle until the 5s budget
// expires and the host kills it. That is the shape of #819: UserPromptSubmit
// timing out on ~4% of Windows turns while measuring 56ms standalone.
//
// These cases are platform-independent — a held-open pipe behaves the same on
// macOS — so they guard the Windows contract from any dev machine.
//
// Run: node tests/test_hook_stdin_lifecycle.js

const path = require('path');
const os = require('os');
const fs = require('fs');
const assert = require('assert');
const { spawn } = require('child_process');

const HOOKS = path.resolve(__dirname, '..', 'src', 'hooks');
const ACTIVATE = path.join(HOOKS, 'caveman-activate.js');
const TRACKER = path.join(HOOKS, 'caveman-mode-tracker.js');

// Well inside the 5s hook budget declared in .claude-plugin/plugin.json. The
// pre-fix hooks blocked until the host killed them, so they never finished at
// all; anything near the budget is a regression even if it eventually exits.
const BUDGET_MS = 4000;

let passed = 0;
let failed = 0;

async function test(name, fn) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-stdin-'));
  try {
    await fn(dir);
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}`);
    console.error(`    ${e.message}`);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

// Spawn a hook, optionally write a payload, and NEVER close stdin. Resolves
// with how long the child took to exit on its own.
function runHoldingPipeOpen(hookPath, payload, configDir) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [hookPath], {
      env: { ...process.env, CLAUDE_CONFIG_DIR: configDir },
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    const started = Date.now();
    let stdout = '';
    child.stdout.on('data', (d) => { stdout += d; });
    const killer = setTimeout(() => {
      child.kill('SIGKILL');
      reject(new Error(`hook did not exit within ${BUDGET_MS}ms while stdin stayed open — ` +
        'it would be killed by the host on Windows (#819/#833)'));
    }, BUDGET_MS);
    child.on('error', reject);
    child.on('exit', (code) => {
      clearTimeout(killer);
      resolve({ code, stdout, elapsed: Date.now() - started });
    });
    if (payload !== null) child.stdin.write(payload);
    // Deliberately no child.stdin.end().
  });
}

console.log('hook stdin lifecycle — must not wait on a lagging pipe close\n');

(async () => {
  await test('activate: exits after a complete payload without waiting for EOF', async (dir) => {
    const payload = JSON.stringify({
      session_id: 't', cwd: process.cwd(), hook_event_name: 'SessionStart', source: 'startup',
    });
    const r = await runHoldingPipeOpen(ACTIVATE, payload, dir);
    assert.strictEqual(r.code, 0, `expected clean exit, got ${r.code}`);
    assert.match(r.stdout, /CAVEMAN MODE ACTIVE/, 'ruleset must still be emitted');
    assert.strictEqual(fs.readFileSync(path.join(dir, '.caveman-active'), 'utf8'), 'full');
  });

  await test('activate: activates on the watchdog when no payload ever arrives', async (dir) => {
    const r = await runHoldingPipeOpen(ACTIVATE, null, dir);
    assert.strictEqual(r.code, 0, `expected clean exit, got ${r.code}`);
    // Forfeiting the session because the host never delivered a payload is
    // worse than activating with startup defaults.
    assert.match(r.stdout, /CAVEMAN MODE ACTIVE/, 'must activate rather than forfeit the session');
  });

  await test('tracker: exits after a complete payload without waiting for EOF', async (dir) => {
    fs.writeFileSync(path.join(dir, '.caveman-active'), 'full');
    const payload = JSON.stringify({ prompt: 'hello there', cwd: process.cwd() });
    const r = await runHoldingPipeOpen(TRACKER, payload, dir);
    assert.strictEqual(r.code, 0, `expected clean exit, got ${r.code}`);
    assert.match(r.stdout, /CAVEMAN MODE ACTIVE \(full\)/, 'reinforcement must still be emitted');
  });

  await test('tracker: a mode change is persisted before the hook exits', async (dir) => {
    fs.writeFileSync(path.join(dir, '.caveman-active'), 'full');
    const payload = JSON.stringify({ prompt: '/caveman ultra', cwd: process.cwd() });
    const r = await runHoldingPipeOpen(TRACKER, payload, dir);
    assert.strictEqual(r.code, 0);
    assert.strictEqual(
      fs.readFileSync(path.join(dir, '.caveman-active'), 'utf8'), 'ultra',
      'the flag write must land even though stdin never closed',
    );
  });

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})();
