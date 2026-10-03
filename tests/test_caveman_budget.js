#!/usr/bin/env node
// Tests for src/hooks/caveman-budget.js — session token-budget intensity ladder.
// Run: node tests/test_caveman_budget.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');

const tmpHome = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-budget-home-'));
process.env.XDG_CONFIG_HOME = tmpHome;
delete process.env.CAVEMAN_OUTPUT_BUDGET;
delete process.env.CAVEMAN_BUDGET_WINDOW;
delete process.env.CAVEMAN_DEFAULT_MODE;

const budget = require('../src/hooks/caveman-budget');

let passed = 0;
let failed = 0;

function test(name, fn) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-budget-'));
  const origCwd = process.cwd();
  const origEnv = {
    CAVEMAN_OUTPUT_BUDGET: process.env.CAVEMAN_OUTPUT_BUDGET,
    CAVEMAN_BUDGET_WINDOW: process.env.CAVEMAN_BUDGET_WINDOW,
    CAVEMAN_DEFAULT_MODE: process.env.CAVEMAN_DEFAULT_MODE,
  };
  try {
    fn(tmp);
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}`);
    console.error(`    ${e.stack || e.message}`);
  } finally {
    process.chdir(origCwd);
    for (const [k, v] of Object.entries(origEnv)) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

function writeTranscript(file, outputTokens) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, JSON.stringify({
    type: 'assistant',
    message: { usage: { output_tokens: outputTokens } },
  }) + '\n');
}

const defaultLadder = {
  window: 'session',
  outputTokens: 20000,
  ladder: [
    { remainPct: 100, mode: 'lite' },
    { remainPct: 50, mode: 'full' },
    { remainPct: 20, mode: 'ultra' },
  ],
};

console.log('caveman-budget tests\n');

test('default ladder thresholds at 80 / 40 / 10 / 0 percent remaining', () => {
  assert.strictEqual(budget.resolveLadderMode(defaultLadder, 4000), 'lite');  // 80% remaining
  assert.strictEqual(budget.resolveLadderMode(defaultLadder, 12000), 'full'); // 40%
  assert.strictEqual(budget.resolveLadderMode(defaultLadder, 18000), 'ultra'); // 10%
  assert.strictEqual(budget.resolveLadderMode(defaultLadder, 20000), 'ultra'); // 0%
});

test('invalid budget object → null (no throw)', (tmp) => {
  process.chdir(tmp);
  fs.writeFileSync(path.join(tmp, '.caveman.json'), JSON.stringify({
    budget: { window: 'week', outputTokens: 20, ladder: [] },
  }));
  assert.doesNotThrow(() => budget.readBudgetConfig(tmp));
  assert.strictEqual(budget.readBudgetConfig(tmp), null);
  assert.strictEqual(budget.validateBudget ? budget.validateBudget({ nope: true }) : budget.readBudgetConfig(tmp), null);
  assert.strictEqual(budget.resolveLadderMode(null, 0), null);
  assert.strictEqual(budget.resolveLadderMode({ outputTokens: -1, window: 'session', ladder: defaultLadder.ladder }, 0), null);
});

test('env integer wins over repo config; non-integer env is ignored', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'), JSON.stringify({
    budget: defaultLadder,
  }));
  process.chdir(tmp);
  process.env.CAVEMAN_OUTPUT_BUDGET = '9999';
  const won = budget.readBudgetConfig(tmp);
  assert.strictEqual(won.outputTokens, 9999);
  assert.strictEqual(won.window, 'session');
  assert.ok(won.ladder.length >= 1);

  process.env.CAVEMAN_OUTPUT_BUDGET = 'not-an-int';
  const ignored = budget.readBudgetConfig(tmp);
  assert.strictEqual(ignored.outputTokens, 20000, 'non-integer env must fall through to repo');
});

test('session uses the provided transcript fixture; missing file → used 0', (tmp) => {
  const sess = path.join(tmp, 's.jsonl');
  writeTranscript(sess, 1234);
  assert.strictEqual(budget.usedOutputTokens({
    window: 'session',
    transcriptPath: sess,
    claudeDir: tmp,
  }), 1234);
  assert.strictEqual(budget.usedOutputTokens({
    window: 'session',
    transcriptPath: path.join(tmp, 'missing.jsonl'),
    claudeDir: tmp,
  }), 0);
});

test('day walk ignores files older than local midnight and caps at 200 files', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  const proj = path.join(claudeDir, 'projects', 'p');
  fs.mkdirSync(proj, { recursive: true });

  const yesterday = Date.now() - 36 * 3600 * 1000;
  const oldFile = path.join(proj, 'old.jsonl');
  writeTranscript(oldFile, 99999);
  fs.utimesSync(oldFile, yesterday / 1000, yesterday / 1000);

  for (let i = 0; i < 201; i++) {
    writeTranscript(path.join(proj, `t${i}.jsonl`), 1);
  }

  const used = budget.usedOutputTokens({ window: 'day', claudeDir });
  assert.ok(used <= 200, `cap is 200 files, got used=${used}`);
  assert.ok(used >= 1, 'today\'s files must count');
  assert.ok(used < 99999, 'yesterday\'s 99999-token file must not count');
});

test('hold file symlink is refused', (tmp) => {
  const secret = path.join(tmp, 'secret.txt');
  fs.writeFileSync(secret, 'SSH_PRIVATE_KEY_CONTENT');
  const hold = path.join(tmp, budget.HOLD_BASENAME);
  try {
    fs.symlinkSync(secret, hold);
  } catch (e) {
    console.log('    (skipped: symlink not permitted)');
    return;
  }
  assert.strictEqual(budget.readHold(tmp), null, 'readHold must refuse a symlink');
  budget.writeHold(tmp, 'lite');
  assert.strictEqual(fs.readFileSync(secret, 'utf8'), 'SSH_PRIVATE_KEY_CONTENT');
});

test('badge writer rejects leftover control characters', (tmp) => {
  assert.strictEqual(budget.sanitizeBudgetBadge('12.4k left\x07'), '');
  assert.strictEqual(budget.sanitizeBudgetBadge('12\x01.4k left'), '');
  assert.strictEqual(budget.sanitizeBudgetBadge('not a badge'), '');
  assert.strictEqual(budget.sanitizeBudgetBadge('12.4k left'), '12.4k left');
  budget.writeBudgetBadge(tmp, 1234);
  const badge = fs.readFileSync(path.join(tmp, budget.BADGE_BASENAME), 'utf8');
  assert.doesNotMatch(badge, /[\x00-\x1F\x7F]/);
  assert.match(badge, /^[0-9.]+[km]? left$/);
});

console.log(`\n${passed} passed, ${failed} failed`);
fs.rmSync(tmpHome, { recursive: true, force: true });
process.exit(failed === 0 ? 0 : 1);
