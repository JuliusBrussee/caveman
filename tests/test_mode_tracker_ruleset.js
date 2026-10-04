#!/usr/bin/env node
// Tests for mid-session mode switching in caveman-mode-tracker.js (#975).
//
// SessionStart injects the active mode's skill body, once. Before this fix the
// UserPromptSubmit hook answered a mid-session switch with the per-turn
// reinforcement line alone, so the model kept the rules it was given at
// SessionStart while every banner asserted the new mode — the switch moved the
// label and nothing else.
//
// The switch must therefore carry the new mode's skill body, and ONLY a
// switch: a session that never changes mode must keep paying one line per turn.
//
// Run: node tests/test_mode_tracker_ruleset.js

const fs = require('fs');
const os = require('os');
const path = require('path');
const assert = require('assert');
const { spawnSync } = require('child_process');

const HOOK_PATH = path.resolve(__dirname, '..', 'src', 'hooks', 'caveman-mode-tracker.js');
const REPO_ROOT = path.resolve(__dirname, '..');
const { writeSessionMode, loadRuleset, thesisLine } = require(path.join(REPO_ROOT, 'src', 'hooks', 'caveman-config.js'));

// Distinctive substrings from each skill body. If a SKILL.md is reworded these
// must follow — they are the assertion that the body actually travelled, not
// a paraphrase of it.
const ULTRA_ROW = 'Ultracave is caveman with the grammar stripped.';
const FULL_ROW = 'Caveman is a voice, not broken grammar.';
const MEGA_ROW = 'Megacave is caveman in Classical Chinese.';

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

console.log('caveman-mode-tracker mid-session mode switch (#975)\n');

test('switching caveman -> ultracave injects the ultracave skill body', () => {
  const ctx = runTracker(slash('ultracave', ''), 'caveman');
  assert.ok(ctx.includes(ULTRA_ROW), `ultracave rules missing from injected context:\n${ctx}`);
});

test('switching caveman -> ultracave does not carry the stale caveman body', () => {
  const ctx = runTracker(slash('ultracave', ''), 'caveman');
  assert.ok(!ctx.includes(FULL_ROW), `caveman body leaked into an ultracave switch:\n${ctx}`);
});

test('switch banner names the new mode', () => {
  const ctx = runTracker(slash('caveman', 'ultra'), 'caveman');
  assert.ok(/CAVEMAN MODE ACTIVE — mode: ultracave/.test(ctx), `no mode banner:\n${ctx}`);
});

test('/megacave (namespaced) injects the megacave skill body', () => {
  const ctx = runTracker(slash('caveman:megacave', ''), 'caveman');
  assert.ok(ctx.includes(MEGA_ROW), `megacave rules missing:\n${ctx}`);
});

test('the switch injects the WHOLE skill body, unfiltered', () => {
  const body = loadRuleset('ultracave', path.join(REPO_ROOT, 'src', 'hooks'));
  const file = fs.readFileSync(path.join(REPO_ROOT, 'skills', 'ultracave', 'SKILL.md'), 'utf8');
  assert.strictEqual(body, file.replace(/^---[\s\S]*?---\s*/, ''), 'loadRuleset must not filter the body');
  assert.ok(!body.startsWith('---'), 'frontmatter must be stripped');
  const ctx = runTracker(slash('ultracave', ''), 'caveman');
  assert.ok(ctx.includes(body.trimEnd()), `injected body differs from SKILL.md:\n${ctx}`);
});

test('activating from off injects the ruleset (model has none yet)', () => {
  const ctx = runTracker(slash('caveman', 'ultra'), null);
  assert.ok(ctx.includes(ULTRA_ROW), `activation from off sent no ruleset:\n${ctx}`);
});

test('re-issuing the SAME mode does not re-inject the ruleset', () => {
  const ctx = runTracker(slash('ultracave', ''), 'ultracave');
  assert.ok(!ctx.includes(ULTRA_ROW), `unchanged mode paid for a full re-injection:\n${ctx}`);
  assert.ok(/CAVEMAN MODE ACTIVE \(ultracave\)/.test(ctx), `lost the per-turn reinforcement:\n${ctx}`);
});

test('a legacy alias of the stored mode is not a switch', () => {
  // A pre-three-skill session file holding `wenyan` reads back as megacave,
  // and `/caveman wenyan-full` parses to megacave: same mode, no re-injection.
  const ctx = runTracker(slash('caveman', 'wenyan-full'), 'wenyan');
  assert.ok(!/CAVEMAN MODE ACTIVE — mode:/.test(ctx), `alias no-op re-injected:\n${ctx}`);
  assert.ok(/CAVEMAN MODE ACTIVE \(megacave\)/.test(ctx), `lost the per-turn reinforcement:\n${ctx}`);
});

test('an ordinary prompt still costs one reinforcement line', () => {
  const ctx = runTracker('why is this test failing', 'ultracave');
  assert.ok(!ctx.includes(ULTRA_ROW), `ordinary turn injected the ruleset:\n${ctx}`);
  assert.ok(/CAVEMAN MODE ACTIVE \(ultracave\)/.test(ctx), `lost the per-turn reinforcement:\n${ctx}`);
});

test('reinforcement carries the thesis line read from the mode\'s SKILL.md', () => {
  for (const mode of ['caveman', 'ultracave', 'megacave']) {
    const file = fs.readFileSync(path.join(REPO_ROOT, 'skills', mode, 'SKILL.md'), 'utf8');
    const lines = file.split('\n');
    const thesis = lines.slice(lines.indexOf('# ' + mode) + 1).find((l) => l.trim()).trim();
    assert.strictEqual(thesisLine(mode, path.join(REPO_ROOT, 'src', 'hooks')), thesis);
    const ctx = runTracker('why is this test failing', mode);
    assert.ok(ctx.startsWith('CAVEMAN MODE ACTIVE (' + mode + '). ' + thesis + ' Answer only what was asked:'),
      `reinforcement not derived from ${mode} SKILL.md:\n${ctx}`);
  }
});

test('independent modes get no prose ruleset', () => {
  // /caveman-commit has its own skill; the base rules would conflict with it.
  const ctx = runTracker(slash('caveman-commit', ''), 'caveman');
  assert.ok(!ctx.includes(FULL_ROW) && !ctx.includes(ULTRA_ROW),
    `independent mode received a prose ruleset:\n${ctx}`);
});

// A caveman-config.js from before the loaders loads fine and passes the tracker's
// shape check — it exports everything that check demands. Plugin-cache drift
// leaves exactly that on disk, so the switch path has to degrade to the
// pre-#975 reminder rather than dereference an absent loader. Both halves are
// asserted: a stripped config must degrade, and the SAME harness with the
// exports intact must inject, or the first assertion would pass for the wrong
// reason (a SKILL.md the copy simply cannot find).
function runTrackerAgainstConfig({ stripNewExports, pluginRoot = REPO_ROOT }) {
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
    const stripped = body.replace(/^\s*skillPathCandidates, loadRuleset, thesisLine, rulesetBanner,\n/m, '');
    assert.notStrictEqual(stripped, body, 'export line to strip not found — test is stale');
    fs.writeFileSync(configPath, stripped);
  }
  const configDir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-skew-home-'));
  writeSessionMode(configDir, 'skew-probe', 'caveman');
  const res = spawnSync(process.execPath, [path.join(hooks, 'caveman-mode-tracker.js')], {
    input: JSON.stringify({ session_id: 'skew-probe', cwd: REPO_ROOT, prompt: slash('ultracave', '') }),
    encoding: 'utf8',
    // The copied hooks dir has no skills/ above it; point the loader at the
    // checkout the way Claude Code points it at the plugin root.
    env: { ...process.env, CLAUDE_CONFIG_DIR: configDir, CLAUDE_PLUGIN_ROOT: pluginRoot },
  });
  assert.strictEqual(res.status, 0, `hook exited ${res.status}: ${res.stderr}`);
  return JSON.parse(res.stdout.trim()).hookSpecificOutput.additionalContext;
}

test('control: the copied hooks dir does inject when the exports are present', () => {
  const ctx = runTrackerAgainstConfig({ stripNewExports: false });
  assert.ok(ctx.includes(ULTRA_ROW), `harness cannot reach SKILL.md at all:\n${ctx}`);
});

test('a caveman-config.js without the loaders degrades to the reminder, not a crash', () => {
  const ctx = runTrackerAgainstConfig({ stripNewExports: true });
  assert.ok(!ctx.includes(ULTRA_ROW), `stale config still injected:\n${ctx}`);
  assert.ok(/CAVEMAN MODE ACTIVE \(ultracave\)/.test(ctx), `lost the reminder too:\n${ctx}`);
});

test('no reachable SKILL.md: reinforcement uses the built-in thesis fallback', () => {
  const nowhere = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-no-skills-'));
  const ctx = runTrackerAgainstConfig({ stripNewExports: false, pluginRoot: nowhere });
  assert.ok(!ctx.includes(ULTRA_ROW), `no skill file, yet a body was injected:\n${ctx}`);
  assert.ok(ctx.includes('CAVEMAN MODE ACTIVE (ultracave). Respond terse like smart caveman. All technical'
    + ' substance stay. Only fluff die. Then cut again. Answer only what was asked:'),
    `fallback thesis missing:\n${ctx}`);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
