#!/usr/bin/env node
// Tests for /caveman-stats — direct script invocation and via mode tracker.
// Run: node tests/test_caveman_stats.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');
const { execFileSync } = require('child_process');

const ROOT = path.resolve(__dirname, '..');
const STATS = path.join(ROOT, 'src', 'hooks', 'caveman-stats.js');
const TRACKER = path.join(ROOT, 'src', 'hooks', 'caveman-mode-tracker.js');

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

test('counts a multi-block API response once, not once per JSONL line', (tmp) => {
  // Claude Code writes one assistant line per content block (text + each
  // tool_use) of the same API response — same message.id + requestId, same
  // usage repeated. Only one line per response may count.
  const usage = { output_tokens: 487, cache_read_input_tokens: 1000 };
  const sess = makeSession(tmp, [
    { type: 'assistant', requestId: 'req_1', message: { id: 'msg_a', usage } },
    { type: 'assistant', requestId: 'req_1', message: { id: 'msg_a', usage } },
    { type: 'assistant', requestId: 'req_1', message: { id: 'msg_a', usage } },
    { type: 'user', message: { content: 'tool result' } },
    { type: 'assistant', requestId: 'req_2', message: { id: 'msg_b', usage: { output_tokens: 13, cache_read_input_tokens: 500 } } },
  ]);
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: path.join(tmp, '.claude') },
  });
  assert.match(out, /Turns:\s+2/);
  assert.match(out, /Output tokens:\s+500\b/);
  assert.match(out, /Cache-read tokens:\s+1,?\.?500\b/);
});

test('same message.id under different requestIds counts per response (retry path)', (tmp) => {
  // A retried request re-sends the same message.id under a new requestId —
  // those are distinct billed responses and must both count.
  const sess = makeSession(tmp, [
    { type: 'assistant', requestId: 'req_1', message: { id: 'msg_a', usage: { output_tokens: 100 } } },
    { type: 'assistant', requestId: 'req_2', message: { id: 'msg_a', usage: { output_tokens: 100 } } },
  ]);
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: path.join(tmp, '.claude') },
  });
  assert.match(out, /Turns:\s+2/);
  assert.match(out, /Output tokens:\s+200\b/);
});

test('entries without message.id keep per-line counting (no dedupe key)', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 100 } } },
    { type: 'assistant', message: { usage: { output_tokens: 100 } } },
  ]);
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: path.join(tmp, '.claude') },
  });
  assert.match(out, /Turns:\s+2/);
  assert.match(out, /Output tokens:\s+200\b/);
});

test('shows rule-overhead accounting and the HONEST-NUMBERS pointer when flag is full', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /Est\. rule overhead:\s+1,250 \(input, ~1,250\/turn over 1 turn\)/);
  assert.match(out, /docs\/HONEST-NUMBERS\.md/);
});

test('shows the same rule-overhead accounting for non-full modes (overhead is not benchmark-gated)', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 100 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'ultra');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /Est\. rule overhead:\s+1,250 \(input, ~1,250\/turn over 1 turn\)/);
  assert.match(out, /docs\/HONEST-NUMBERS\.md/);
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

test('mode tracker delivers /caveman-stats via additionalContext', (tmp) => {
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
  assert.strictEqual(parsed.hookSpecificOutput.hookEventName, 'UserPromptSubmit');
  assert.match(parsed.hookSpecificOutput.additionalContext, /Caveman Stats/);
  assert.match(parsed.hookSpecificOutput.additionalContext, /Output tokens:\s+100/);
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

test('priceForModel matches by prefix across point releases', () => {
  const { priceForModel } = require(path.join(ROOT, 'src', 'hooks', 'caveman-stats.js'));
  assert.strictEqual(priceForModel('claude-opus-4-7'), 25.00);
  assert.strictEqual(priceForModel('claude-opus-4-8'), 25.00);
  assert.strictEqual(priceForModel('claude-opus-4-20250101'), 75.00);
  assert.strictEqual(priceForModel('claude-opus-4-1-20250805'), 75.00);
  assert.strictEqual(priceForModel('claude-sonnet-4-7-20260315'), 15.00);
  assert.strictEqual(priceForModel('claude-haiku-4-5'), 5.00);
  assert.strictEqual(priceForModel('claude-3-5-sonnet-20241022'), 15.00);
  assert.strictEqual(priceForModel('claude-opus-5'), 25.00);
  assert.strictEqual(priceForModel('claude-sonnet-5'), 10.00);
  assert.strictEqual(priceForModel('claude-fable-5'), 50.00);
  assert.strictEqual(priceForModel('claude-mythos-5'), 50.00);
  assert.strictEqual(priceForModel('claude-opus-5[1m]'), 25.00);
  assert.strictEqual(priceForModel('claude-sonnet-5[1m]'), 10.00);
  assert.strictEqual(priceForModel('claude-haiku-5'), null);
  assert.strictEqual(priceForModel(null), null);
  assert.strictEqual(priceForModel('gpt-4'), null);
});

test('formatStats handles empty session gracefully', () => {
  const { formatStats } = require(path.join(ROOT, 'src', 'hooks', 'caveman-stats.js'));
  const out = formatStats({ outputTokens: 0, cacheReadTokens: 0, turns: 0, mode: 'full' });
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
  assert.match(out, /1 turns, 350 output tokens this session/);
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
  assert.strictEqual(entry.turns, 1);
  assert.strictEqual(entry.mode, 'full');
  assert.strictEqual(entry.model, 'claude-sonnet-4-7');
});

test('--all aggregates latest entry per session', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  const histPath = path.join(claudeDir, '.caveman-history.jsonl');
  // Two sessions, second one has two snapshots — only latest counts.
  fs.writeFileSync(histPath, [
    { ts: 1000, session_id: 'a', mode: 'full', output_tokens: 100, turns: 1 },
    { ts: 2000, session_id: 'b', mode: 'full', output_tokens: 50,  turns: 1 },
    { ts: 3000, session_id: 'b', mode: 'full', output_tokens: 200, turns: 1 },
  ].map(o => JSON.stringify(o)).join('\n') + '\n');
  const out = execFileSync(process.execPath, [STATS, '--all'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  // a: 100 + b-latest: 200 = 300
  assert.match(out, /Sessions:\s+2/);
  assert.match(out, /Output tokens:\s+300/);
});

test('--since filters by time window', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  const histPath = path.join(claudeDir, '.caveman-history.jsonl');
  const now = Date.now();
  const twoDaysAgo = now - 2 * 86_400_000;
  const tenMinAgo = now - 10 * 60_000;
  fs.writeFileSync(histPath, [
    { ts: twoDaysAgo, session_id: 'old', mode: 'full', output_tokens: 100, turns: 1 },
    { ts: tenMinAgo, session_id: 'new', mode: 'full', output_tokens: 50,  turns: 1 },
  ].map(o => JSON.stringify(o)).join('\n') + '\n');
  const out = execFileSync(process.execPath, [STATS, '--since', '1d'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  // Only the recent session is counted.
  assert.match(out, /Sessions:\s+1/);
  assert.match(out, /Output tokens:\s+50/);
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
  const { findCompressedPairs } = require(path.join(ROOT, 'src', 'hooks', 'caveman-stats.js'));
  fs.writeFileSync(path.join(tmp, 'foo.original.md'), 'small');
  fs.writeFileSync(path.join(tmp, 'foo.md'), 'this is actually larger somehow');
  const pairs = findCompressedPairs([tmp]);
  assert.strictEqual(pairs.length, 0);
});

test('statusline suffix is always empty (no counterfactual figure to render)', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 1500 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  // Plant a stale suffix from before this fix, the way an existing install
  // would have one on disk. The run must overwrite it with empty, not leave
  // the old fictional figure in place.
  fs.writeFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), 'stale-savings-figure');
  execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  const suffixPath = path.join(claudeDir, '.caveman-statusline-suffix');
  assert.ok(fs.existsSync(suffixPath));
  assert.strictEqual(fs.readFileSync(suffixPath, 'utf8'), '');
});

test('humanizeTokens formats small/medium/large correctly', () => {
  const { humanizeTokens } = require(path.join(ROOT, 'src', 'hooks', 'caveman-stats.js'));
  assert.strictEqual(humanizeTokens(0), '0');
  assert.strictEqual(humanizeTokens(42), '42');
  assert.strictEqual(humanizeTokens(2786), '2.8k');
  assert.strictEqual(humanizeTokens(1_250_000), '1.3M');
});

// The statusline ships as two scripts with one contract: caveman-statusline.sh
// for POSIX hosts and caveman-statusline.ps1 for Windows. These tests used to
// bail out on win32, which left the .ps1 — the script Windows users actually
// run — with no coverage at all, including its control-byte stripping. Run
// whichever script the host would run instead.
const STATUSLINE = process.platform === 'win32'
  ? { command: 'powershell', args: ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File',
      path.join(ROOT, 'src', 'hooks', 'caveman-statusline.ps1')] }
  : { command: 'bash', args: [path.join(ROOT, 'src', 'hooks', 'caveman-statusline.sh')] };

function runStatusline(env) {
  return execFileSync(STATUSLINE.command, STATUSLINE.args, { encoding: 'utf8', env });
}

test('statusline appends savings when CAVEMAN_STATUSLINE_SAVINGS=1', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  fs.writeFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), '⛏ 2.8k');
  const out = runStatusline({ ...process.env, CLAUDE_CONFIG_DIR: claudeDir, CAVEMAN_STATUSLINE_SAVINGS: '1' });
  assert.match(out, /\[CAVEMAN\]/);
  assert.match(out, /⛏ 2\.8k/);
});

test('statusline renders savings by default when env var is unset', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  fs.writeFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), '⛏ 2.8k');
  const env = { ...process.env, CLAUDE_CONFIG_DIR: claudeDir };
  delete env.CAVEMAN_STATUSLINE_SAVINGS;
  const out = runStatusline(env);
  assert.match(out, /\[CAVEMAN\]/);
  assert.match(out, /⛏ 2\.8k/);
});

test('statusline omits savings when CAVEMAN_STATUSLINE_SAVINGS=0', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  fs.writeFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), '⛏ 2.8k');
  const out = runStatusline({ ...process.env, CLAUDE_CONFIG_DIR: claudeDir, CAVEMAN_STATUSLINE_SAVINGS: '0' });
  assert.match(out, /\[CAVEMAN\]/);
  assert.doesNotMatch(out, /⛏/);
});

test('statusline omits savings when suffix file is missing (fresh install)', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  // No suffix file written — simulates the moment after install but before
  // /caveman-stats has run. Default-on must NOT fabricate a number.
  const env = { ...process.env, CLAUDE_CONFIG_DIR: claudeDir };
  delete env.CAVEMAN_STATUSLINE_SAVINGS;
  const out = runStatusline(env);
  assert.match(out, /\[CAVEMAN\]/);
  assert.doesNotMatch(out, /⛏/);
});

test('statusline strips control bytes from suffix', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  // Plant a malicious suffix with ANSI escape (control byte \x1b).
  fs.writeFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), '\x1b[31mEVIL');
  const out = runStatusline({ ...process.env, CLAUDE_CONFIG_DIR: claudeDir, CAVEMAN_STATUSLINE_SAVINGS: '1' });
  // Escape byte stripped; "[31mEVIL" remains, but the leading \x1b is gone so
  // the user's terminal won't be hijacked.
  assert.doesNotMatch(out, /\x1b\[31m/);
});

// ── statusline: per-session badge ──────────────────────────────────────────
//
// Claude Code pipes session JSON (including session_id) to the statusline
// command. These drive the script the same way, so the badge is proven to
// reflect the window it belongs to rather than a machine-wide flag.

function statusline(claudeDir, stdin) {
  return execFileSync('bash', [path.join(ROOT, 'src', 'hooks', 'caveman-statusline.sh')], {
    encoding: 'utf8',
    input: stdin === undefined ? '' : stdin,
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir, CAVEMAN_STATUSLINE_SAVINGS: '0' },
  });
}

function seedSessions(claudeDir, modes) {
  const dir = path.join(claudeDir, '.caveman-sessions');
  fs.mkdirSync(dir, { recursive: true });
  for (const [sid, mode] of Object.entries(modes)) {
    fs.writeFileSync(path.join(dir, `${sid}.mode`), mode);
  }
}

test('statusline.sh renders the session mode, not the shared flag', (tmp) => {
  if (process.platform === 'win32') return;
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  seedSessions(claudeDir, { sessA: 'ultra', sessB: 'lite' });

  assert.match(statusline(claudeDir, '{"session_id":"sessA"}'), /\[CAVEMAN:ULTRA\]/);
  assert.match(statusline(claudeDir, '{"session_id":"sessB"}'), /\[CAVEMAN:LITE\]/);
});

test('statusline.sh renders nothing for a durable off session', (tmp) => {
  if (process.platform === 'win32') return;
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  // Another window is still on 'full' — this one must stay silent anyway.
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  seedSessions(claudeDir, { sessOff: 'off' });

  const out = statusline(claudeDir, '{"session_id":"sessOff"}');
  assert.strictEqual(out, '', `expected empty badge, got ${JSON.stringify(out)}`);
  assert.doesNotMatch(out, /OFF/, 'must never render [CAVEMAN:OFF]');
});

test('statusline.sh falls back to the legacy flag without a usable session id', (tmp) => {
  if (process.platform === 'win32') return;
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'wenyan');
  seedSessions(claudeDir, { sessA: 'ultra' });

  for (const stdin of [
    '{"session_id":"unknown-session"}',   // valid id, no state file yet
    '{"model":{"id":"x"}}',               // payload without session_id
    '{"session_id":"../../etc/passwd"}',  // traversal attempt
    'not json at all',
    '',                                   // no payload
  ]) {
    assert.match(statusline(claudeDir, stdin), /\[CAVEMAN:WENYAN\]/, `stdin: ${stdin}`);
  }
});

test('statusline.sh never reads a session file outside the sessions dir', (tmp) => {
  if (process.platform === 'win32') return;
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  // Plant a file the traversal would reach if the id were interpolated raw.
  fs.mkdirSync(path.join(claudeDir, '.caveman-sessions'), { recursive: true });
  fs.writeFileSync(path.join(claudeDir, 'escaped.mode'), 'ultra');

  const out = statusline(claudeDir, '{"session_id":"../escaped"}');
  assert.match(out, /\[CAVEMAN\]/, 'must fall back to the legacy full badge');
  assert.doesNotMatch(out, /ULTRA/, 'traversal must not reach the planted file');
});

test('statusline.sh tolerates multiline and whitespace-padded JSON', (tmp) => {
  if (process.platform === 'win32') return;
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  seedSessions(claudeDir, { sessA: 'ultra' });

  const pretty = '{\n  "model": { "id": "x" },\n  "session_id" : "sessA"\n}\n';
  assert.match(statusline(claudeDir, pretty), /\[CAVEMAN:ULTRA\]/);
});

test('appendFlag is symlink-safe (refuses symlinked target)', (tmp) => {
  // Creating a symlink on Windows needs Developer Mode or admin (#115), so the
  // fixture cannot be built here. The Windows guard is the ReparsePoint check in
  // caveman-statusline.ps1 and the O_NOFOLLOW path in caveman-config.js; both are
  // exercised by tests/test_symlink_flag.js on POSIX. Accepted platform gap.
  if (process.platform === 'win32') return;
  const { appendFlag } = require(path.join(ROOT, 'src', 'hooks', 'caveman-config.js'));
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
  assert.strictEqual(parsed.hookSpecificOutput.hookEventName, 'UserPromptSubmit');
  assert.match(parsed.hookSpecificOutput.additionalContext, /1 turns, 350 output tokens this session/);
});

// ── No counterfactual output-savings estimate (caveman#991) ────────────────
// docs/HONEST-NUMBERS.md commits to no such estimate until a reviewed
// benchmark result is committed; check both the session and lifetime views.

test('session view never claims an output-savings figure, only rule overhead', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.ok(!/est\. \d|% of output|Est\. saved \(USD\)|Est\. without caveman|output reduction/i.test(out),
    'must not print any counterfactual output-savings figure');
  assert.ok(!/budget|of your usage|of tracked usage/i.test(out),
    'must not claim a share of usage/budget either');
  assert.match(out, /Est\. rule overhead:\s+1,250/);
  assert.match(out, /docs\/HONEST-NUMBERS\.md/);
});

test('--all lifetime view never claims an output-savings figure, only rule overhead', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  const history = [
    { ts: Date.now(), session_id: 'a', output_tokens: 350, turns: 1 },
    { ts: Date.now(), session_id: 'b', output_tokens: 650, turns: 1 },
  ];
  fs.writeFileSync(
    path.join(claudeDir, '.caveman-history.jsonl'),
    history.map(h => JSON.stringify(h)).join('\n') + '\n',
  );
  const out = execFileSync(process.execPath, [STATS, '--all'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.ok(!/est\. \d|% of output|Est\. saved \(USD\)|output reduction|budget/i.test(out),
    'must not print any counterfactual output-savings figure');
  assert.match(out, /Output tokens:\s+1,000/);
  assert.match(out, /Est\. rule overhead:\s+2,500 \(input, ~1,250\/turn over 2 turns\)/);
  assert.match(out, /docs\/HONEST-NUMBERS\.md/);
});

// ── Mid-session mode-change attribution (#601) ─────────────────────────────
// Tokens must be attributed to the mode active WHEN each message happened,
// via the .caveman-mode-log.jsonl transition log — never to whatever mode the
// flag holds at stats time (which inflated savings after a late activation,
// and zeroed them after a late deactivation).

test('attributes tokens to the mode active when each message happened (#601)', (tmp) => {
  const now = Date.now();
  const iso = (minAgo) => new Date(now - minAgo * 60_000).toISOString();
  // 300 verbose tokens BEFORE caveman was activated, 350 after.
  const sess = makeSession(tmp, [
    { type: 'assistant', timestamp: iso(60), message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 300 } } },
    { type: 'assistant', timestamp: iso(10), message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-mode-log.jsonl'),
    JSON.stringify({ ts: now - 30 * 60_000, mode: 'full', prev: null }) + '\n');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /Mode changed mid-session/);
  assert.match(out, /caveman off:\s+300 tokens/);
  assert.match(out, /full:\s+350 tokens/);
  // No estimate is attached to either span (caveman#991).
  assert.doesNotMatch(out, /est\. \d+ saved/i);
  // The lifetime history row records the real total, not a per-mode estimate.
  const hist = fs.readFileSync(path.join(claudeDir, '.caveman-history.jsonl'), 'utf8')
    .split('\n').filter(Boolean).map(l => JSON.parse(l));
  assert.strictEqual(hist[hist.length - 1].output_tokens, 650);
});

test('credits caveman spans even after mode is turned off mid-session (#601)', (tmp) => {
  const now = Date.now();
  const iso = (minAgo) => new Date(now - minAgo * 60_000).toISOString();
  const sess = makeSession(tmp, [
    { type: 'assistant', timestamp: iso(60), message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 350 } } },
    { type: 'assistant', timestamp: iso(10), message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 200 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-mode-log.jsonl'),
    JSON.stringify({ ts: now - 90 * 60_000, mode: 'full', prev: null }) + '\n' +
    JSON.stringify({ ts: now - 30 * 60_000, mode: null, prev: 'full' }) + '\n');
  // No .caveman-active flag — caveman is off at stats time. The old behavior
  // printed "Caveman not active this session." even though caveman ran for
  // part of the session.
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.doesNotMatch(out, /Caveman not active this session/);
  assert.match(out, /full:\s+350 tokens/);
  assert.match(out, /caveman off:\s+200 tokens/);
});

test('mode tracker logs timestamped transitions, deduping unchanged modes (#601)', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const run = (prompt) => execFileSync(process.execPath, [TRACKER], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir, HOME: tmp },
    input: JSON.stringify({ prompt }),
  });
  const logPath = path.join(claudeDir, '.caveman-mode-log.jsonl');
  const rows = () => fs.readFileSync(logPath, 'utf8').split('\n').filter(Boolean).map(l => JSON.parse(l));

  run('/caveman ultra');
  run('/caveman ultra'); // unchanged — must not append a duplicate row
  assert.strictEqual(rows().length, 1);
  assert.strictEqual(rows()[0].mode, 'ultra');
  assert.strictEqual(rows()[0].prev, 'full');
  assert.ok(Number.isFinite(rows()[0].ts));

  run('/caveman off'); // deactivation is a transition too
  assert.strictEqual(rows().length, 2);
  assert.strictEqual(rows()[1].mode, null);
  assert.strictEqual(rows()[1].prev, 'ultra');
});

test('excludes tokens that predate a mid-session flag write with no log (#601)', (tmp) => {
  const now = Date.now();
  const sess = makeSession(tmp, [
    { type: 'assistant', timestamp: new Date(now - 60 * 60_000).toISOString(), message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  // Flag written NOW (after the message), no transition log: the mode during
  // the message is unknown. The honest number is zero — say so, don't guess.
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /Mode was set mid-session/);
  assert.match(out, /unattributed:\s+350 tokens/);
  assert.doesNotMatch(out, /Est\. rule overhead/);
});

// ── Rule-overhead accounting (#145/#677) ───────────────────────────────────
// docs/HONEST-NUMBERS.md admits caveman's rules cost ~1-1.5k input tokens
// every turn; this is a sourced, published estimate (unlike output savings)
// so it stays independent of any per-mode benchmark.

test('overhead scales with turn count, not output tokens', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 100 } } },
    { type: 'assistant', message: { usage: { output_tokens: 200 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /Est\. rule overhead:\s+2,500 \(input, ~1,250\/turn over 2 turns\)/);
});

test('CAVEMAN_RULE_OVERHEAD_TOKENS overrides the per-turn overhead estimate', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 1500 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir, CAVEMAN_RULE_OVERHEAD_TOKENS: '500' },
  });
  assert.match(out, /Est\. rule overhead:\s+500 \(input, ~500\/turn over 1 turn\)/);
});

test('overheadLine and ruleOverheadPerTurn validate a positive integer, falling back otherwise', () => {
  const { overheadLine, ruleOverheadPerTurn } = require(STATS);
  const saved = process.env.CAVEMAN_RULE_OVERHEAD_TOKENS;
  try {
    delete process.env.CAVEMAN_RULE_OVERHEAD_TOKENS;
    assert.strictEqual(ruleOverheadPerTurn(), 1250);
    assert.strictEqual(overheadLine(1), 'Est. rule overhead:    1,250 (input, ~1,250/turn over 1 turn)');
    assert.strictEqual(overheadLine(2), 'Est. rule overhead:    2,500 (input, ~1,250/turn over 2 turns)');

    process.env.CAVEMAN_RULE_OVERHEAD_TOKENS = '500';
    assert.strictEqual(ruleOverheadPerTurn(), 500);

    // Invalid overrides (non-numeric, zero, negative, non-integer) all fall
    // back to the default rather than produce a nonsensical overhead.
    process.env.CAVEMAN_RULE_OVERHEAD_TOKENS = 'garbage';
    assert.strictEqual(ruleOverheadPerTurn(), 1250);
    process.env.CAVEMAN_RULE_OVERHEAD_TOKENS = '0';
    assert.strictEqual(ruleOverheadPerTurn(), 1250);
    process.env.CAVEMAN_RULE_OVERHEAD_TOKENS = '-100';
    assert.strictEqual(ruleOverheadPerTurn(), 1250);
    process.env.CAVEMAN_RULE_OVERHEAD_TOKENS = '12.5';
    assert.strictEqual(ruleOverheadPerTurn(), 1250);
  } finally {
    if (saved === undefined) delete process.env.CAVEMAN_RULE_OVERHEAD_TOKENS;
    else process.env.CAVEMAN_RULE_OVERHEAD_TOKENS = saved;
  }
});

test('lifetime view sums rule overhead across sessions logged turns', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  const histPath = path.join(claudeDir, '.caveman-history.jsonl');
  fs.writeFileSync(histPath, [
    { ts: 1000, session_id: 'a', mode: 'full', output_tokens: 1500, turns: 1 },
    { ts: 2000, session_id: 'b', mode: 'full', output_tokens: 100,  turns: 1 },
  ].map(o => JSON.stringify(o)).join('\n') + '\n');
  const out = execFileSync(process.execPath, [STATS, '--all'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /Output tokens:\s+1,600/);
  assert.match(out, /Est\. rule overhead:\s+2,500 \(input, ~1,250\/turn over 2 turns\)/);
});

test('lifetime view omits rule overhead for legacy history rows that never logged turns', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-history.jsonl'),
    JSON.stringify({ ts: 1000, session_id: 'a', mode: 'full', output_tokens: 350 }) + '\n');
  const out = execFileSync(process.execPath, [STATS, '--all'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /Output tokens:\s+350/);
  assert.doesNotMatch(out, /Est\. rule overhead:/);
});

test('number formatting is pinned to en-US even under a dot-grouping locale', (tmp) => {
  // Regression for the locale pin: toLocaleString() alone inherits the host OS
  // locale (de-DE renders 1234 as "1.234"), which would break machine-readable
  // output and any test oracle. fmt() forces 'en-US' grouping everywhere.
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 1234, cache_read_input_tokens: 5678 } } },
  ]);
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: {
      ...process.env,
      CLAUDE_CONFIG_DIR: path.join(tmp, '.claude'),
      LANG: 'de-DE.UTF-8',
      LC_ALL: 'de-DE.UTF-8',
    },
  });
  // en-US grouping: commas, never the de-DE dot separator.
  assert.match(out, /Output tokens:\s+1,234/);
  assert.match(out, /Cache-read tokens:\s+5,678/);
  assert.doesNotMatch(out, /Output tokens:\s+1\.234/);
});

test('lifetime view excludes legacy rows from rule-overhead accounting even when mixed with rows that logged turns', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-history.jsonl'), [
    // Legacy row: no turns field, must not contribute to overhead accounting.
    { ts: 1000, session_id: 'legacy', mode: 'full', output_tokens: 350 },
    { ts: 2000, session_id: 'new',    mode: 'full', output_tokens: 1500, turns: 1 },
  ].map(o => JSON.stringify(o)).join('\n') + '\n');
  const out = execFileSync(process.execPath, [STATS, '--all'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  // Gross total includes both rows: 350 + 1500 = 1850.
  assert.match(out, /Output tokens:\s+1,850/);
  // Overhead only counts the 'new' row's 1 logged turn, not the legacy row.
  assert.match(out, /Est\. rule overhead:\s+1,250 \(input, ~1,250\/turn over 1 turn\)/);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
