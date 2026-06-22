#!/usr/bin/env node
// Tests for CodeBuddy hook support — verifies that all hook scripts
// correctly use ~/.codebuddy when CODEBUDDY_CONFIG_DIR or CODEBUDDY_PLUGIN_ROOT
// environment variables are set.
//
// Run: node tests/test_codebuddy_hooks.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');
const { execFileSync } = require('child_process');

const ROOT = path.resolve(__dirname, '..');
const CONFIG = path.join(ROOT, 'src', 'hooks', 'caveman-config.js');
const ACTIVATE = path.join(ROOT, 'src', 'hooks', 'caveman-activate.js');
const TRACKER = path.join(ROOT, 'src', 'hooks', 'caveman-mode-tracker.js');
const STATUSLINE = path.join(ROOT, 'src', 'hooks', 'caveman-statusline.sh');

let passed = 0;
let failed = 0;

function test(name, fn) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-codebuddy-test-'));
  try {
    fn(tmp);
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}\n    ${e.message}`);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

// Helper: build env with only CodeBuddy vars, strip Claude vars
function codebuddyEnv(tmp, extra = {}) {
  const env = { ...process.env, CODEBUDDY_CONFIG_DIR: tmp, ...extra };
  delete env.CLAUDE_CONFIG_DIR;
  delete env.CODEBUDDY_PLUGIN_ROOT;
  return env;
}

console.log('CodeBuddy hooks tests\n');

// ── getAgentConfigDir priority tests ─────────────────────────────────────

test('getAgentConfigDir: CODEBUDDY_CONFIG_DIR takes priority over all', (tmp) => {
  const out = execFileSync(process.execPath, ['-e', `
    process.env.CODEBUDDY_CONFIG_DIR = '/custom/cb';
    process.env.CODEBUDDY_PLUGIN_ROOT = '/other';
    process.env.CLAUDE_CONFIG_DIR = '/claude';
    const { getAgentConfigDir } = require('${CONFIG.replace(/\\/g, '\\\\')}');
    process.stdout.write(getAgentConfigDir());
  `], { encoding: 'utf8' });
  assert.strictEqual(out, '/custom/cb');
});

test('getAgentConfigDir: CODEBUDDY_PLUGIN_ROOT falls back to ~/.codebuddy', (tmp) => {
  const out = execFileSync(process.execPath, ['-e', `
    delete process.env.CODEBUDDY_CONFIG_DIR;
    process.env.CODEBUDDY_PLUGIN_ROOT = '/some/plugin/root';
    process.env.CLAUDE_CONFIG_DIR = '/claude';
    const { getAgentConfigDir } = require('${CONFIG.replace(/\\/g, '\\\\')}');
    process.stdout.write(getAgentConfigDir());
  `], { encoding: 'utf8', env: { ...process.env, CODEBUDDY_PLUGIN_ROOT: '/x' } });
  const home = os.homedir();
  assert.strictEqual(out, path.join(home, '.codebuddy'));
});

test('getAgentConfigDir: falls back to CLAUDE_CONFIG_DIR when no CodeBuddy vars', (tmp) => {
  const out = execFileSync(process.execPath, ['-e', `
    delete process.env.CODEBUDDY_CONFIG_DIR;
    delete process.env.CODEBUDDY_PLUGIN_ROOT;
    process.env.CLAUDE_CONFIG_DIR = '/my/claude';
    const { getAgentConfigDir } = require('${CONFIG.replace(/\\/g, '\\\\')}');
    process.stdout.write(getAgentConfigDir());
  `], { encoding: 'utf8', env: { ...process.env, CLAUDE_CONFIG_DIR: '/my/claude' } });
  assert.strictEqual(out, '/my/claude');
});

test('getAgentConfigDir: defaults to ~/.claude when no env vars set', (tmp) => {
  const env = { ...process.env };
  delete env.CODEBUDDY_CONFIG_DIR;
  delete env.CODEBUDDY_PLUGIN_ROOT;
  delete env.CLAUDE_CONFIG_DIR;
  const out = execFileSync(process.execPath, ['-e', `
    delete process.env.CODEBUDDY_CONFIG_DIR;
    delete process.env.CODEBUDDY_PLUGIN_ROOT;
    delete process.env.CLAUDE_CONFIG_DIR;
    const { getAgentConfigDir } = require('${CONFIG.replace(/\\/g, '\\\\')}');
    process.stdout.write(getAgentConfigDir());
  `], { encoding: 'utf8', env });
  const home = os.homedir();
  assert.strictEqual(out, path.join(home, '.claude'));
});

// ── caveman-activate.js under CodeBuddy ──────────────────────────────────

test('activate hook writes .caveman-active to CODEBUDDY_CONFIG_DIR', (tmp) => {
  const env = codebuddyEnv(tmp);
  execFileSync(process.execPath, [ACTIVATE], { encoding: 'utf8', env });
  const flagPath = path.join(tmp, '.caveman-active');
  assert.ok(fs.existsSync(flagPath), '.caveman-active should exist in codebuddy dir');
  const mode = fs.readFileSync(flagPath, 'utf8').trim();
  assert.strictEqual(mode, 'full');
});

test('activate hook does NOT write to Claude dir when CODEBUDDY_CONFIG_DIR set', (tmp) => {
  const claudeDir = path.join(tmp, 'claude-should-not-be-touched');
  fs.mkdirSync(claudeDir, { recursive: true });
  const cbDir = path.join(tmp, 'codebuddy');
  fs.mkdirSync(cbDir, { recursive: true });
  const env = { ...process.env, CODEBUDDY_CONFIG_DIR: cbDir, CLAUDE_CONFIG_DIR: claudeDir };
  execFileSync(process.execPath, [ACTIVATE], { encoding: 'utf8', env });
  assert.ok(fs.existsSync(path.join(cbDir, '.caveman-active')), 'flag in codebuddy dir');
  assert.ok(!fs.existsSync(path.join(claudeDir, '.caveman-active')), 'no flag in claude dir');
});

// ── caveman-mode-tracker.js under CodeBuddy ──────────────────────────────

test('mode-tracker writes flag to CODEBUDDY_CONFIG_DIR on /caveman ultra', (tmp) => {
  const env = codebuddyEnv(tmp);
  const input = JSON.stringify({ prompt: '/caveman ultra' });
  execFileSync(process.execPath, [TRACKER], { encoding: 'utf8', env, input });
  const flagPath = path.join(tmp, '.caveman-active');
  assert.ok(fs.existsSync(flagPath), '.caveman-active should exist');
  const mode = fs.readFileSync(flagPath, 'utf8').trim();
  assert.strictEqual(mode, 'ultra');
});

test('mode-tracker writes flag to CODEBUDDY_CONFIG_DIR on /caveman lite', (tmp) => {
  const env = codebuddyEnv(tmp);
  const input = JSON.stringify({ prompt: '/caveman lite' });
  execFileSync(process.execPath, [TRACKER], { encoding: 'utf8', env, input });
  const flagPath = path.join(tmp, '.caveman-active');
  const mode = fs.readFileSync(flagPath, 'utf8').trim();
  assert.strictEqual(mode, 'lite');
});

test('mode-tracker deletes flag on "stop caveman"', (tmp) => {
  // Pre-plant flag
  fs.writeFileSync(path.join(tmp, '.caveman-active'), 'full');
  const env = codebuddyEnv(tmp);
  const input = JSON.stringify({ prompt: 'stop caveman' });
  execFileSync(process.execPath, [TRACKER], { encoding: 'utf8', env, input });
  assert.ok(!fs.existsSync(path.join(tmp, '.caveman-active')), 'flag should be deleted');
});

// ── statusline.sh under CodeBuddy ────────────────────────────────────────

test('statusline.sh reads flag from CODEBUDDY_CONFIG_DIR', (tmp) => {
  fs.writeFileSync(path.join(tmp, '.caveman-active'), 'full');
  const env = codebuddyEnv(tmp);
  delete env.CODEBUDDY_PLUGIN_ROOT;
  const out = execFileSync('bash', [STATUSLINE], { encoding: 'utf8', env });
  assert.match(out, /CAVEMAN/);
});

test('statusline.sh shows mode suffix for non-full modes', (tmp) => {
  fs.writeFileSync(path.join(tmp, '.caveman-active'), 'ultra');
  const env = codebuddyEnv(tmp);
  delete env.CODEBUDDY_PLUGIN_ROOT;
  const out = execFileSync('bash', [STATUSLINE], { encoding: 'utf8', env });
  assert.match(out, /CAVEMAN:ULTRA/);
});

test('statusline.sh outputs nothing when flag absent', (tmp) => {
  const env = codebuddyEnv(tmp);
  delete env.CODEBUDDY_PLUGIN_ROOT;
  const out = execFileSync('bash', [STATUSLINE], { encoding: 'utf8', env });
  assert.strictEqual(out, '');
});

// ── CODEBUDDY_PLUGIN_ROOT fallback in hooks ──────────────────────────────

test('activate hook uses ~/.codebuddy when only CODEBUDDY_PLUGIN_ROOT set', (tmp) => {
  // We can't write to actual ~/.codebuddy in tests, so we verify indirectly:
  // set both CODEBUDDY_PLUGIN_ROOT and check it doesn't use CLAUDE_CONFIG_DIR
  const claudeDir = path.join(tmp, 'claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  const env = { ...process.env, CODEBUDDY_PLUGIN_ROOT: '/fake/plugin', CLAUDE_CONFIG_DIR: claudeDir };
  delete env.CODEBUDDY_CONFIG_DIR;
  // This will try to write to ~/.codebuddy which may or may not exist.
  // The key assertion: it should NOT write to claudeDir.
  try {
    execFileSync(process.execPath, [ACTIVATE], { encoding: 'utf8', env });
  } catch (e) {
    // Ignore errors (the dir might not be writable) — we only care that
    // claudeDir was NOT touched.
  }
  assert.ok(!fs.existsSync(path.join(claudeDir, '.caveman-active')),
    'should not write flag to CLAUDE_CONFIG_DIR when CODEBUDDY_PLUGIN_ROOT set');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
