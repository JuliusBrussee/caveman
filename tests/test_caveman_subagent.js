#!/usr/bin/env node
// Tests for the SubagentStart hook (caveman-subagent.js).
//
// The hook re-injects the active caveman ruleset into Task-spawned subagents
// (#672) — SessionStart context never reaches them. These tests pin the three
// contracts that matter:
//   1. active mode → hookSpecificOutput JSON carrying the real, level-filtered
//      SKILL.md ruleset (not the weak inline fallback),
//   2. inactive/invalid/independent mode → completely silent, exit 0,
//   3. a broken install (missing caveman-config.js) degrades to silence, never
//      to a MODULE_NOT_FOUND crash on every subagent spawn (#848).
//
// Run: node tests/test_caveman_subagent.js

const path = require('path');
const os = require('os');
const fs = require('fs');
const assert = require('assert');
const { spawnSync } = require('child_process');

const REPO_ROOT = path.resolve(__dirname, '..');
const HOOK = path.join(REPO_ROOT, 'src', 'hooks', 'caveman-subagent.js');

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

// Run the hook with an isolated $CLAUDE_CONFIG_DIR holding the given flag
// value (null = no flag file). Returns { status, stdout, stderr }.
function runHook(flagValue, { hookPath = HOOK, pluginRoot = REPO_ROOT } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-subagent-test-'));
  try {
    if (flagValue !== null) {
      fs.writeFileSync(path.join(dir, '.caveman-active'), flagValue);
    }
    const env = { ...process.env, CLAUDE_CONFIG_DIR: dir };
    delete env.CLAUDE_PLUGIN_ROOT;
    if (pluginRoot) env.CLAUDE_PLUGIN_ROOT = pluginRoot;
    return spawnSync(process.execPath, [hookPath], {
      env,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
      timeout: 10000,
    });
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

test('active mode emits hookSpecificOutput with the level-filtered SKILL.md ruleset', () => {
  const r = runHook('full');
  assert.strictEqual(r.status, 0, `exit ${r.status}, stderr: ${r.stderr}`);
  const out = JSON.parse(r.stdout);
  assert.strictEqual(out.hookSpecificOutput.hookEventName, 'SubagentStart');
  const ctx = out.hookSpecificOutput.additionalContext;
  assert.ok(ctx.includes('CAVEMAN MODE ACTIVE — level: full'), 'missing activation header');
  // Real SKILL.md content, not the inline fallback — the fallback has no '## Intensity'.
  assert.ok(ctx.includes('## Intensity'), 'served fallback instead of SKILL.md');
  // Intensity table filtered to the active level only.
  assert.ok(ctx.includes('| **full** |'), 'active level row missing');
  assert.ok(!ctx.includes('| **lite** |'), 'other level rows must be filtered out');
});

test('wenyan alias resolves to wenyan-full', () => {
  const r = runHook('wenyan');
  const ctx = JSON.parse(r.stdout).hookSpecificOutput.additionalContext;
  assert.ok(ctx.includes('level: wenyan-full'), 'alias not resolved');
});

test('mode off is silent', () => {
  const r = runHook('off');
  assert.strictEqual(r.status, 0);
  assert.strictEqual(r.stdout, '');
});

test('absent flag is silent', () => {
  const r = runHook(null);
  assert.strictEqual(r.status, 0);
  assert.strictEqual(r.stdout, '');
});

test('unrecognised flag value is silent (readFlag whitelist)', () => {
  const r = runHook('DROP TABLE;[31m');
  assert.strictEqual(r.status, 0);
  assert.strictEqual(r.stdout, '');
});

test('independent modes (own skill files) are silent', () => {
  for (const mode of ['commit', 'review', 'compress']) {
    const r = runHook(mode);
    assert.strictEqual(r.status, 0, `${mode}: exit ${r.status}`);
    assert.strictEqual(r.stdout, '', `${mode}: expected no output`);
  }
});

test('missing caveman-config.js degrades to silence, not MODULE_NOT_FOUND (#848)', () => {
  // Faithful copy of the hook without its config sibling.
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-subagent-broken-'));
  try {
    const hookCopy = path.join(dir, 'caveman-subagent.js');
    fs.copyFileSync(HOOK, hookCopy);
    const r = runHook('full', { hookPath: hookCopy, pluginRoot: null });
    assert.strictEqual(r.status, 0, `exit ${r.status}, stderr: ${r.stderr}`);
    assert.strictEqual(r.stdout, '');
    assert.ok(!/MODULE_NOT_FOUND/.test(r.stderr), 'crashed with MODULE_NOT_FOUND');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('unreachable SKILL.md falls back to the inline minimum ruleset', () => {
  // Copy hook + config siblings to an isolated dir with no skills/ anywhere
  // above it and no CLAUDE_PLUGIN_ROOT.
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-subagent-noskill-'));
  try {
    const hooksDir = path.join(root, 'hooks');
    fs.mkdirSync(hooksDir);
    for (const f of ['caveman-subagent.js', 'caveman-config.js']) {
      fs.copyFileSync(path.join(REPO_ROOT, 'src', 'hooks', f), path.join(hooksDir, f));
    }
    const r = runHook('full', { hookPath: path.join(hooksDir, 'caveman-subagent.js'), pluginRoot: null });
    assert.strictEqual(r.status, 0, `exit ${r.status}, stderr: ${r.stderr}`);
    const ctx = JSON.parse(r.stdout).hookSpecificOutput.additionalContext;
    assert.ok(ctx.includes('CAVEMAN MODE ACTIVE — level: full'), 'missing activation header');
    assert.ok(!ctx.includes('## Intensity'), 'expected inline fallback, got SKILL.md');
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
