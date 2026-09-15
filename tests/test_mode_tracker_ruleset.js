#!/usr/bin/env node
// Tests for mid-session level switching in caveman-mode-tracker.js (#975).
//
// SessionStart injects the ruleset filtered to the active level, once. Before
// this fix the UserPromptSubmit hook answered a mid-session `/caveman ultra`
// with the per-turn reinforcement line alone, so the model kept the level's
// rules it was given at SessionStart while every banner asserted the new one —
// `/caveman ultra` moved the label and nothing else.
//
// The switch must therefore carry the new level's ruleset, and ONLY a switch:
// a session that never changes level must keep paying one line per turn.
//
// Run: node tests/test_mode_tracker_ruleset.js

const fs = require('fs');
const os = require('os');
const path = require('path');
const assert = require('assert');
const { spawnSync } = require('child_process');

const HOOK_PATH = path.resolve(__dirname, '..', 'src', 'hooks', 'caveman-mode-tracker.js');
const REPO_ROOT = path.resolve(__dirname, '..');
const { writeSessionMode } = require(path.join(REPO_ROOT, 'src', 'hooks', 'caveman-config.js'));

// Distinctive substrings from the SKILL.md intensity rows. If SKILL.md is
// reworded these must follow — they are the assertion that the row actually
// travelled, not a paraphrase of it.
const ULTRA_ROW = 'Strip conjunctions';
const FULL_ROW = 'Classic caveman';

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

// One temp CLAUDE_CONFIG_DIR per case: mode state is per session, and a leaked
// flag from a previous case would decide the next one's switch detection.
function runTracker(prompt, seedMode) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-ruleset-'));
  const sessionId = 'ruleset-probe';
  if (seedMode !== undefined) writeSessionMode(dir, sessionId, seedMode);
  const payload = JSON.stringify({ session_id: sessionId, cwd: REPO_ROOT, prompt });
  const res = spawnSync(process.execPath, [HOOK_PATH], {
    input: payload,
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: dir },
  });
  assert.strictEqual(res.status, 0, `hook exited ${res.status}: ${res.stderr}`);
  const out = (res.stdout || '').trim();
  if (!out) return '';
  return JSON.parse(out).hookSpecificOutput.additionalContext;
}

// Claude Code delivers slash commands as an envelope, not the literal command.
function slash(name, args) {
  return `<command-name>/${name}</command-name><command-args>${args || ''}</command-args>`;
}

console.log('caveman-mode-tracker mid-session level switch (#975)\n');

test('switching full -> ultra injects the ultra ruleset', () => {
  const ctx = runTracker(slash('caveman', 'ultra'), 'full');
  assert.ok(ctx.includes(ULTRA_ROW), `ultra rules missing from injected context:\n${ctx}`);
});

test('switching full -> ultra does not carry the stale full row', () => {
  const ctx = runTracker(slash('caveman', 'ultra'), 'full');
  assert.ok(!ctx.includes(FULL_ROW), `full-level row leaked into an ultra switch:\n${ctx}`);
});

test('switch banner names the new level', () => {
  const ctx = runTracker(slash('caveman', 'ultra'), 'full');
  assert.ok(/CAVEMAN MODE ACTIVE — level: ultra/.test(ctx), `no level banner:\n${ctx}`);
});

test('activating from off injects the ruleset (model has none yet)', () => {
  const ctx = runTracker(slash('caveman', 'ultra'), null);
  assert.ok(ctx.includes(ULTRA_ROW), `activation from off sent no ruleset:\n${ctx}`);
});

test('re-issuing the SAME level does not re-inject the ruleset', () => {
  const ctx = runTracker(slash('caveman', 'ultra'), 'ultra');
  assert.ok(!ctx.includes(ULTRA_ROW), `unchanged level paid for a full re-injection:\n${ctx}`);
  assert.ok(/CAVEMAN MODE ACTIVE \(ultra\)/.test(ctx), `lost the per-turn reinforcement:\n${ctx}`);
});

test('wenyan-full is not a switch when wenyan is already stored (alias)', () => {
  // parseModeChange canonicalizes `wenyan-full` to the stored spelling
  // `wenyan`; a naive string compare would call that a level change.
  const ctx = runTracker(slash('caveman', 'wenyan-full'), 'wenyan');
  assert.ok(!/CAVEMAN MODE ACTIVE — level:/.test(ctx), `alias no-op re-injected:\n${ctx}`);
});

test('an ordinary prompt still costs one reinforcement line', () => {
  const ctx = runTracker('why is this test failing', 'ultra');
  assert.ok(!ctx.includes(ULTRA_ROW), `ordinary turn injected the ruleset:\n${ctx}`);
  assert.ok(/CAVEMAN MODE ACTIVE \(ultra\)/.test(ctx), `lost the per-turn reinforcement:\n${ctx}`);
});

test('independent modes get no prose ruleset', () => {
  // /caveman-commit has its own skill; the base rules would conflict with it.
  const ctx = runTracker(slash('caveman-commit', ''), 'full');
  assert.ok(!ctx.includes(FULL_ROW) && !ctx.includes(ULTRA_ROW),
    `independent mode received a prose ruleset:\n${ctx}`);
});

// A caveman-config.js from before #975 loads fine and passes the tracker's
// shape check — it exports everything that check demands. Plugin-cache drift
// leaves exactly that on disk, so the switch path has to degrade to the
// pre-#975 reminder rather than dereference an absent loader. Both halves are
// asserted: a stripped config must degrade, and the SAME harness with the
// exports intact must inject, or the first assertion would pass for the wrong
// reason (a SKILL.md the copy simply cannot find).
function runTrackerAgainstConfig({ stripNewExports }) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-skew-'));
  const hooks = path.join(root, 'hooks');
  fs.mkdirSync(hooks);
  const srcHooks = path.join(REPO_ROOT, 'src', 'hooks');
  for (const entry of fs.readdirSync(srcHooks)) {
    const src = path.join(srcHooks, entry);
    if (fs.statSync(src).isFile()) fs.copyFileSync(src, path.join(hooks, entry));
  }
  if (stripNewExports) {
    const configPath = path.join(hooks, 'caveman-config.js');
    const body = fs.readFileSync(configPath, 'utf8');
    const stripped = body.replace(/^\s*canonicalModeLabel, loadFilteredRuleset, rulesetBanner,\n/m, '');
    assert.notStrictEqual(stripped, body, 'export line to strip not found — test is stale');
    fs.writeFileSync(configPath, stripped);
  }
  const configDir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-skew-home-'));
  writeSessionMode(configDir, 'skew-probe', 'full');
  const res = spawnSync(process.execPath, [path.join(hooks, 'caveman-mode-tracker.js')], {
    input: JSON.stringify({ session_id: 'skew-probe', cwd: REPO_ROOT, prompt: slash('caveman', 'ultra') }),
    encoding: 'utf8',
    // The copied hooks dir has no skills/ above it; point the loader at the
    // checkout the way Claude Code points it at the plugin root.
    env: { ...process.env, CLAUDE_CONFIG_DIR: configDir, CLAUDE_PLUGIN_ROOT: REPO_ROOT },
  });
  assert.strictEqual(res.status, 0, `hook exited ${res.status}: ${res.stderr}`);
  return JSON.parse(res.stdout.trim()).hookSpecificOutput.additionalContext;
}

test('control: the copied hooks dir does inject when the exports are present', () => {
  const ctx = runTrackerAgainstConfig({ stripNewExports: false });
  assert.ok(ctx.includes(ULTRA_ROW), `harness cannot reach SKILL.md at all:\n${ctx}`);
});

test('a pre-#975 caveman-config.js degrades to the reminder, not a crash', () => {
  const ctx = runTrackerAgainstConfig({ stripNewExports: true });
  assert.ok(!ctx.includes(ULTRA_ROW), `stale config still injected:\n${ctx}`);
  assert.ok(/CAVEMAN MODE ACTIVE \(ultra\)/.test(ctx), `lost the pre-#975 reminder too:\n${ctx}`);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
