#!/usr/bin/env node
// Tests for /caveman-stats — direct script invocation and via mode tracker.
// Run: node tests/test_caveman_stats.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');
const { execFileSync } = require('child_process');

const ROOT = path.resolve(__dirname, '..');
const STATS = path.join(ROOT, 'hooks', 'caveman-stats.js');
const TRACKER = path.join(ROOT, 'hooks', 'caveman-mode-tracker.js');

let passed = 0;
let failed = 0;

function test(name, fn) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-stats-test-'));
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

function makeSession(dir, lines) {
  const projDir = path.join(dir, '.claude', 'projects', 'p');
  fs.mkdirSync(projDir, { recursive: true });
  const sessFile = path.join(projDir, 's.jsonl');
  fs.writeFileSync(sessFile, lines.map(l => JSON.stringify(l)).join('\n'));
  return sessFile;
}

console.log('caveman-stats tests\n');

test('reads --session-file directly and sums output tokens', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 100, cache_read_input_tokens: 200 } } },
    { type: 'user', message: { content: 'hi' } },
    { type: 'assistant', message: { usage: { output_tokens: 50, cache_read_input_tokens: 50 } } },
  ]);
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: path.join(tmp, '.claude') },
  });
  assert.match(out, /Turns:\s+2/);
  assert.match(out, /Output tokens:\s+150/);
  assert.match(out, /Cache-read tokens:\s+250/);
});

test('shows full-mode savings estimate when flag is full', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  // 350 / 0.35 = 1000, saved = 650, ~65%
  assert.match(out, /Est\. without caveman:\s+1,000/);
  assert.match(out, /Est\. tokens saved:\s+650 \(~65%\)/);
});

test('skips estimate for non-full modes', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 100 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'ultra');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /No savings estimate for 'ultra' mode/);
});

test('reports no-session when no .jsonl exists', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.claude', 'projects'), { recursive: true });
  let err = null;
  try {
    execFileSync(process.execPath, [STATS], {
      encoding: 'utf8',
      env: { ...process.env, CLAUDE_CONFIG_DIR: path.join(tmp, '.claude') },
    });
  } catch (e) { err = e; }
  assert.ok(err, 'should exit non-zero');
  assert.match(err.stderr, /no Claude Code session found/);
});

test('mode tracker handles /caveman-stats with decision block', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 100 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [TRACKER], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir, HOME: tmp },
    input: JSON.stringify({ prompt: '/caveman-stats', transcript_path: sess }),
  });
  const parsed = JSON.parse(out);
  assert.strictEqual(parsed.decision, 'block');
  assert.match(parsed.reason, /Caveman Stats/);
  assert.match(parsed.reason, /Output tokens:\s+100/);
});

test('mode tracker preserves caveman flag when /caveman-stats fires', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 50 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  execFileSync(process.execPath, [TRACKER], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir, HOME: tmp },
    input: JSON.stringify({ prompt: '/caveman-stats', transcript_path: sess }),
  });
  // The flag must still say 'full' — the stats command must not change mode.
  assert.strictEqual(fs.readFileSync(path.join(claudeDir, '.caveman-active'), 'utf8'), 'full');
});

test('shows USD savings when model is a known sonnet variant', (tmp) => {
  // 350 / 0.35 = 1000, saved = 650 tokens. At $15/M output → $0.00975.
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'claude-sonnet-4-20250514', usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  // 650/1M * $15 = $0.00975 — JS toFixed(4) rounds the float repr to 0.0097.
  assert.match(out, /Est\. saved \(USD\):\s+~\$0\.009[78]/);
  assert.match(out, /Pricing for claude-sonnet-4-20250514/);
});

test('omits USD line when model is unknown', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'some-future-model-xyz', usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  // Token estimate still appears, USD line does not.
  assert.match(out, /Est\. tokens saved:\s+650 \(~65%\)/);
  assert.doesNotMatch(out, /Est\. saved \(USD\)/);
});

test('priceForModel matches by prefix across point releases', () => {
  const { priceForModel } = require(path.join(ROOT, 'hooks', 'caveman-stats.js'));
  assert.strictEqual(priceForModel('claude-opus-4-7'), 75.00);
  assert.strictEqual(priceForModel('claude-opus-4-20250101'), 75.00);
  assert.strictEqual(priceForModel('claude-sonnet-4-7-20260315'), 15.00);
  assert.strictEqual(priceForModel('claude-haiku-4-5'), 4.00);
  assert.strictEqual(priceForModel('claude-3-5-sonnet-20241022'), 15.00);
  assert.strictEqual(priceForModel(null), null);
  assert.strictEqual(priceForModel('gpt-4'), null);
});

test('formatStats handles empty session gracefully', () => {
  const { formatStats } = require(path.join(ROOT, 'hooks', 'caveman-stats.js'));
  const out = formatStats({ outputTokens: 0, cacheReadTokens: 0, turns: 0, mode: 'full', model: null });
  assert.match(out, /No conversation yet/);
});

test('--share prints single-line tweetable summary', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess, '--share'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.strictEqual(out.split('\n').filter(Boolean).length, 1);
  assert.match(out, /^🪨 Saved 650 output tokens \(~\$0\.009[78]\) across 1 turns this session — caveman\.sh$/m);
});

test('--share works with no benchmark ratio (lite mode)', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 200 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'lite');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess, '--share'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /^🪨 1 turns, 200 output tokens this session — caveman\.sh$/m);
});

test('appends to lifetime history on each run', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  const histPath = path.join(claudeDir, '.caveman-history.jsonl');
  assert.ok(fs.existsSync(histPath), 'history file should be created');
  const lines = fs.readFileSync(histPath, 'utf8').split('\n').filter(Boolean);
  assert.strictEqual(lines.length, 1);
  const entry = JSON.parse(lines[0]);
  assert.strictEqual(entry.session_id, 's');
  assert.strictEqual(entry.output_tokens, 350);
  assert.strictEqual(entry.est_saved_tokens, 650);
  assert.strictEqual(entry.mode, 'full');
  assert.strictEqual(entry.model, 'claude-sonnet-4-7');
});

test('--all aggregates latest entry per session', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  const histPath = path.join(claudeDir, '.caveman-history.jsonl');
  // Two sessions, second one has two snapshots — only latest counts.
  fs.writeFileSync(histPath, [
    { ts: 1000, session_id: 'a', mode: 'full', output_tokens: 100, est_saved_tokens: 185, est_saved_usd: 0.0028 },
    { ts: 2000, session_id: 'b', mode: 'full', output_tokens: 50,  est_saved_tokens: 92,  est_saved_usd: 0.0014 },
    { ts: 3000, session_id: 'b', mode: 'full', output_tokens: 200, est_saved_tokens: 371, est_saved_usd: 0.0056 },
  ].map(o => JSON.stringify(o)).join('\n') + '\n');
  const out = execFileSync(process.execPath, [STATS, '--all'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  // a: 185 + b-latest: 371 = 556
  assert.match(out, /Sessions:\s+2/);
  assert.match(out, /Est\. tokens saved:\s+556/);
  // 0.0028 + 0.0056 = 0.0084 → formatted as $0.0084
  assert.match(out, /\$0\.0084/);
});

test('--since filters by time window', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  const histPath = path.join(claudeDir, '.caveman-history.jsonl');
  const now = Date.now();
  const twoDaysAgo = now - 2 * 86_400_000;
  const tenMinAgo = now - 10 * 60_000;
  fs.writeFileSync(histPath, [
    { ts: twoDaysAgo, session_id: 'old', mode: 'full', output_tokens: 100, est_saved_tokens: 185, est_saved_usd: 0.003 },
    { ts: tenMinAgo, session_id: 'new', mode: 'full', output_tokens: 50,  est_saved_tokens: 92,  est_saved_usd: 0.001 },
  ].map(o => JSON.stringify(o)).join('\n') + '\n');
  const out = execFileSync(process.execPath, [STATS, '--since', '1d'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  // Only the recent session is counted.
  assert.match(out, /Sessions:\s+1/);
  assert.match(out, /Est\. tokens saved:\s+92/);
  assert.match(out, /\(last 1d\)/);
});

test('--since rejects malformed durations', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  let err = null;
  try {
    execFileSync(process.execPath, [STATS, '--since', 'sometime'], {
      encoding: 'utf8',
      env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
    });
  } catch (e) { err = e; }
  assert.ok(err, 'should exit non-zero');
  assert.match(err.stderr, /--since takes Nh or Nd/);
});

test('--all reports empty when no history', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  const out = execFileSync(process.execPath, [STATS, '--all'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /No sessions logged yet/);
});

test('detects compressed memory pairs and reports approx token savings', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  // Make a fake compressed/original pair: original is 800 bytes, compressed 200 bytes.
  fs.writeFileSync(path.join(claudeDir, 'CLAUDE.original.md'), 'x'.repeat(800));
  fs.writeFileSync(path.join(claudeDir, 'CLAUDE.md'), 'y'.repeat(200));
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 100 } } },
  ]);
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  // 600 bytes / 4 chars-per-token ≈ 150 tokens (approx).
  assert.match(out, /Memory compressed:\s+1 file, ~150 tokens saved per session start/);
});

test('omits memory line when no compressed pairs exist', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 100 } } },
  ]);
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.doesNotMatch(out, /Memory compressed/);
});

test('skips pairs where compressed is not actually smaller', (tmp) => {
  const { findCompressedPairs } = require(path.join(ROOT, 'hooks', 'caveman-stats.js'));
  fs.writeFileSync(path.join(tmp, 'foo.original.md'), 'small');
  fs.writeFileSync(path.join(tmp, 'foo.md'), 'this is actually larger somehow');
  const pairs = findCompressedPairs([tmp]);
  assert.strictEqual(pairs.length, 0);
});

test('writes statusline suffix file after a stats run', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 1500 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  const suffixPath = path.join(claudeDir, '.caveman-statusline-suffix');
  assert.ok(fs.existsSync(suffixPath));
  // 1500 / 0.35 = 4286, saved = 2786 → "⛏ 2.8k"
  const suffix = fs.readFileSync(suffixPath, 'utf8');
  assert.match(suffix, /^⛏ 2\.8k$/);
});

test('humanizeTokens formats small/medium/large correctly', () => {
  const { humanizeTokens } = require(path.join(ROOT, 'hooks', 'caveman-stats.js'));
  assert.strictEqual(humanizeTokens(0), '0');
  assert.strictEqual(humanizeTokens(42), '42');
  assert.strictEqual(humanizeTokens(2786), '2.8k');
  assert.strictEqual(humanizeTokens(1_250_000), '1.3M');
});

test('statusline.sh appends savings when CAVEMAN_STATUSLINE_SAVINGS=1', (tmp) => {
  if (process.platform === 'win32') return; // bash test
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  fs.writeFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), '⛏ 2.8k');
  const out = execFileSync('bash', [path.join(ROOT, 'hooks', 'caveman-statusline.sh')], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir, CAVEMAN_STATUSLINE_SAVINGS: '1' },
  });
  assert.match(out, /\[CAVEMAN\]/);
  assert.match(out, /⛏ 2\.8k/);
});

test('statusline.sh renders savings by default when env var is unset', (tmp) => {
  if (process.platform === 'win32') return;
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  fs.writeFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), '⛏ 2.8k');
  const env = { ...process.env, CLAUDE_CONFIG_DIR: claudeDir };
  delete env.CAVEMAN_STATUSLINE_SAVINGS;
  const out = execFileSync('bash', [path.join(ROOT, 'hooks', 'caveman-statusline.sh')], {
    encoding: 'utf8', env,
  });
  assert.match(out, /\[CAVEMAN\]/);
  assert.match(out, /⛏ 2\.8k/);
});

test('statusline.sh omits savings when CAVEMAN_STATUSLINE_SAVINGS=0', (tmp) => {
  if (process.platform === 'win32') return;
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  fs.writeFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), '⛏ 2.8k');
  const out = execFileSync('bash', [path.join(ROOT, 'hooks', 'caveman-statusline.sh')], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir, CAVEMAN_STATUSLINE_SAVINGS: '0' },
  });
  assert.match(out, /\[CAVEMAN\]/);
  assert.doesNotMatch(out, /⛏/);
});

test('statusline.sh omits savings when suffix file is missing (fresh install)', (tmp) => {
  if (process.platform === 'win32') return;
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  // No suffix file written — simulates the moment after install but before
  // /caveman-stats has run. Default-on must NOT fabricate a number.
  const env = { ...process.env, CLAUDE_CONFIG_DIR: claudeDir };
  delete env.CAVEMAN_STATUSLINE_SAVINGS;
  const out = execFileSync('bash', [path.join(ROOT, 'hooks', 'caveman-statusline.sh')], {
    encoding: 'utf8', env,
  });
  assert.match(out, /\[CAVEMAN\]/);
  assert.doesNotMatch(out, /⛏/);
});

test('statusline.sh strips control bytes from suffix', (tmp) => {
  if (process.platform === 'win32') return;
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  // Plant a malicious suffix with ANSI escape (control byte \x1b).
  fs.writeFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), '\x1b[31mEVIL');
  const out = execFileSync('bash', [path.join(ROOT, 'hooks', 'caveman-statusline.sh')], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir, CAVEMAN_STATUSLINE_SAVINGS: '1' },
  });
  // Escape byte stripped; "[31mEVIL" remains, but the leading \x1b is gone so
  // the user's terminal won't be hijacked.
  assert.doesNotMatch(out, /\x1b\[31m/);
});

test('appendFlag is symlink-safe (refuses symlinked target)', (tmp) => {
  if (process.platform === 'win32') return; // symlink semantics differ
  const { appendFlag } = require(path.join(ROOT, 'hooks', 'caveman-config.js'));
  const target = path.join(tmp, 'real-target');
  fs.writeFileSync(target, 'do-not-clobber\n');
  const linkPath = path.join(tmp, 'history.jsonl');
  fs.symlinkSync(target, linkPath);
  appendFlag(linkPath, JSON.stringify({ ts: 1, session_id: 'x' }));
  // Original target must be untouched.
  assert.strictEqual(fs.readFileSync(target, 'utf8'), 'do-not-clobber\n');
});

test('mode tracker forwards --share to stats script', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [TRACKER], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir, HOME: tmp },
    input: JSON.stringify({ prompt: '/caveman-stats --share', transcript_path: sess }),
  });
  const parsed = JSON.parse(out);
  assert.strictEqual(parsed.decision, 'block');
  assert.match(parsed.reason, /^🪨 Saved 650 output tokens/);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
