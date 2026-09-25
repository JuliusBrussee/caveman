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

function assertSavingsUnknown(out) {
  assert.match(out, /savings:? unknown/i);
  assert.doesNotMatch(out, /Est\. (?:without|tokens saved|saved|output reduction|rule overhead|net)|\bSaved \d|\d[\d,.]*%|\$[\d.]+/i);
}

console.log('caveman-stats tests\n');

test('Gemini commands cannot read or mutate unrelated Claude history (#403)', (tmp) => {
  makeSession(tmp, [{ type: 'assistant', message: { usage: { output_tokens: 987654, cache_read_input_tokens: 12345 } } }]);
  const claudeDir = path.join(tmp, '.claude');
  const history = path.join(claudeDir, '.caveman-history.jsonl');
  const suffix = path.join(claudeDir, '.caveman-statusline-suffix');
  fs.writeFileSync(history, 'existing Claude history\n');
  fs.writeFileSync(suffix, 'existing Claude suffix');
  for (const args of [[], ['--all'], ['--share'], ['--host', 'gemini']]) {
    const out = execFileSync(process.execPath, [STATS, ...args], {
      encoding: 'utf8', env: { ...process.env, GEMINI_CLI: '1', CLAUDE_CONFIG_DIR: claudeDir },
    });
    assert.match(out, /\/stats model/);
    assert.match(out, /\/stats session/);
    assertSavingsUnknown(out);
    assert.doesNotMatch(out, /987[,.]?654|12[,.]?345/);
    assert.strictEqual(fs.readFileSync(history, 'utf8'), 'existing Claude history\n');
    assert.strictEqual(fs.readFileSync(suffix, 'utf8'), 'existing Claude suffix');
  }
});

test('explicit Claude hook ownership works when launched below a Gemini shell', (tmp) => {
  const sess = makeSession(tmp, [{ type: 'assistant', message: { usage: { output_tokens: 431, cache_read_input_tokens: 0 } } }]);
  const out = execFileSync(process.execPath, [STATS, '--host', 'claude', '--session-file', sess], {
    encoding: 'utf8', env: { ...process.env, GEMINI_CLI: '1', CLAUDE_CONFIG_DIR: path.join(tmp, '.claude') },
  });
  assert.match(out, /Output tokens:\s+431/);
  assertSavingsUnknown(out);
});

test('full-mode output and historical estimates never establish measured savings (#991/#789)', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 1000, cache_read_input_tokens: 2400 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  const historyPath = path.join(claudeDir, '.caveman-history.jsonl');
  const historical = JSON.stringify({ ts: 1, session_id: 'old', output_tokens: 1000,
    est_saved_tokens: 1857, est_saved_usd: 0.027855, turns: 1 }) + '\n';
  fs.writeFileSync(historyPath, historical);
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const suffixPath = path.join(claudeDir, '.caveman-statusline-suffix');
  for (const args of [['--session-file', sess], ['--session-file', sess, '--share'], ['--all']]) {
    fs.writeFileSync(suffixPath, '⛏ 1.9k');
    const out = execFileSync(process.execPath, [STATS, ...args], {
      encoding: 'utf8', env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
    });
    assertSavingsUnknown(out);
    assert.strictEqual(fs.readFileSync(suffixPath, 'utf8'), '');
  }
  const history = fs.readFileSync(historyPath, 'utf8');
  assert.ok(history.startsWith(historical), 'original history bytes must be preserved');
  for (const line of history.slice(historical.length).trim().split('\n')) {
    const row = JSON.parse(line);
    assert.strictEqual(row.output_tokens, 1000);
    assert.strictEqual(row.cache_read_input_tokens, 2400);
    assert.ok(!Object.hasOwn(row, 'est_saved_tokens'));
    assert.ok(!Object.hasOwn(row, 'est_saved_usd'));
  }
});

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

test('full mode reports observed output with savings unknown', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assertSavingsUnknown(out);
  assert.match(out, /Output tokens:\s+350/);
  assert.match(out, /Mode: full/);
});

test('non-full modes also leave savings unknown', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 100 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'ultra');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /Mode: ultra/);
  assertSavingsUnknown(out);
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

test('known models do not imply monetary savings', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'claude-sonnet-4-20250514', usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assertSavingsUnknown(out);
  assert.match(out, /Output tokens:\s+350/);
});

test('unknown models retain output counts without a savings estimate', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'some-future-model-xyz', usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  // A model identifier is not a measured comparison.
  assertSavingsUnknown(out);
  assert.doesNotMatch(out, /Est\. saved \(USD\)/);
});

test('parseSession retains the observed model without inventing pricing', (tmp) => {
  const { parseSession } = require(STATS);
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 350 } } },
  ]);
  assert.strictEqual(parseSession(sess).model, 'claude-sonnet-4-7');
});

test('formatStats handles empty session gracefully', () => {
  const { formatStats } = require(path.join(ROOT, 'src', 'hooks', 'caveman-stats.js'));
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
  assert.match(out, /^🪨 1 turn, 350 output tokens this session; savings unknown — caveman\.sh$/m);
  assertSavingsUnknown(out);
});

test('--share reports observed usage in lite mode', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 200 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'lite');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess, '--share'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /^🪨 1 turn, 200 output tokens this session; savings unknown — caveman\.sh$/m);
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
  assert.ok(!Object.hasOwn(entry, 'est_saved_tokens'));
  assert.ok(!Object.hasOwn(entry, 'est_saved_usd'));
  assert.deepStrictEqual(entry.output_tokens_by_mode, { full: 350 });
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
  assert.match(out, /Sessions:\s+2/);
  assertSavingsUnknown(out);
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
    { ts: twoDaysAgo, session_id: 'old', mode: 'full', output_tokens: 100, est_saved_tokens: 185, est_saved_usd: 0.003 },
    { ts: tenMinAgo, session_id: 'new', mode: 'full', output_tokens: 50,  est_saved_tokens: 92,  est_saved_usd: 0.001 },
  ].map(o => JSON.stringify(o)).join('\n') + '\n');
  const out = execFileSync(process.execPath, [STATS, '--since', '1d'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  // Only the recent session is counted.
  assert.match(out, /Sessions:\s+1/);
  assertSavingsUnknown(out);
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

test('reports original and current memory file bytes without provider-savings claims', (tmp) => {
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
  assert.match(out, /Memory file sizes:\s+1 pair, 800 original bytes → 200 current bytes \(600 fewer bytes\)/);
  assert.doesNotMatch(out, /tokens saved|per session start/);
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
  assert.doesNotMatch(out, /Memory file sizes/);
});

test('skips pairs where compressed is not actually smaller', (tmp) => {
  const { findCompressedPairs } = require(path.join(ROOT, 'src', 'hooks', 'caveman-stats.js'));
  fs.writeFileSync(path.join(tmp, 'foo.original.md'), 'small');
  fs.writeFileSync(path.join(tmp, 'foo.md'), 'this is actually larger somehow');
  const pairs = findCompressedPairs([tmp]);
  assert.strictEqual(pairs.length, 0);
});

test('retires the old numeric statusline suffix after a stats run', (tmp) => {
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
  const suffix = fs.readFileSync(suffixPath, 'utf8');
  assert.strictEqual(suffix, '');
});

test('compressed pair comparison counts bytes rather than assuming a tokenizer', (tmp) => {
  const { findCompressedPairs, summarizeCompressed } = require(STATS);
  fs.writeFileSync(path.join(tmp, 'MEMORY.original.md'), '文'.repeat(100));
  fs.writeFileSync(path.join(tmp, 'MEMORY.md'), '文'.repeat(20));
  assert.deepStrictEqual(summarizeCompressed(findCompressedPairs([tmp])), {
    count: 1, totalOriginal: 300, totalCompressed: 60, bytesReduced: 240,
  });
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

test('statusline ignores legacy savings before stats runs even with old opt-in', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  fs.writeFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), '⛏ 2.8k');
  const out = runStatusline({ ...process.env, CLAUDE_CONFIG_DIR: claudeDir, CAVEMAN_STATUSLINE_SAVINGS: '1' });
  assert.match(out, /\[CAVEMAN\]/);
  assert.doesNotMatch(out, /⛏|2\.8k/);
  assert.strictEqual(fs.readFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), 'utf8'), '⛏ 2.8k');
});

test('statusline ignores legacy savings before stats runs by default', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  fs.writeFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), '⛏ 2.8k');
  const env = { ...process.env, CLAUDE_CONFIG_DIR: claudeDir };
  delete env.CAVEMAN_STATUSLINE_SAVINGS;
  const out = runStatusline(env);
  assert.match(out, /\[CAVEMAN\]/);
  assert.doesNotMatch(out, /⛏|2\.8k/);
  assert.strictEqual(fs.readFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), 'utf8'), '⛏ 2.8k');
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

test('statusline never renders content from a legacy suffix', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  // Plant a malicious suffix with ANSI escape (control byte \x1b).
  fs.writeFileSync(path.join(claudeDir, '.caveman-statusline-suffix'), '\x1b[31mEVIL');
  const out = runStatusline({ ...process.env, CLAUDE_CONFIG_DIR: claudeDir, CAVEMAN_STATUSLINE_SAVINGS: '1' });
  // The retired suffix must not be read at all.
  assert.doesNotMatch(out, /\x1b\[31m|EVIL/);
});

test('PowerShell keeps session badges and ignores old savings before stats runs', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  seedSessions(claudeDir, { session: 'ultra' });
  const suffixPath = path.join(claudeDir, '.caveman-statusline-suffix');
  fs.writeFileSync(suffixPath, '⛏ 1.9k');
  const command = process.platform === 'win32' ? 'powershell' : 'pwsh';
  for (const value of [undefined, '1', '0']) {
    const env = { ...process.env, CLAUDE_CONFIG_DIR: claudeDir };
    if (value === undefined) delete env.CAVEMAN_STATUSLINE_SAVINGS;
    else env.CAVEMAN_STATUSLINE_SAVINGS = value;
    let out;
    try {
      out = execFileSync(command, ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File',
        path.join(ROOT, 'src', 'hooks', 'caveman-statusline.ps1')], {
        encoding: 'utf8', input: '{"session_id":"session"}', env,
      });
    } catch (e) {
      if (e.code === 'ENOENT' && process.platform !== 'win32') {
        console.log('    PowerShell unavailable on this host; native statusline tests still run.');
        return;
      }
      throw e;
    }
    assert.match(out, /\[CAVEMAN:ULTRA\]/);
    assert.doesNotMatch(out, /⛏|1\.9k/);
  }
  assert.strictEqual(fs.readFileSync(suffixPath, 'utf8'), '⛏ 1.9k');
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
  assert.match(parsed.hookSpecificOutput.additionalContext, /🪨 1 turn, 350 output tokens/);
  assertSavingsUnknown(parsed.hookSpecificOutput.additionalContext);
});

// ── No counterfactual from transcript usage ────────────────────────────

test('history aggregation excludes all legacy estimated-savings fields', (tmp) => {
  const { aggregateHistory } = require(STATS);
  const historyPath = path.join(tmp, 'history.jsonl');
  const original = [
    { ts: 1, session_id: 'one', output_tokens: 350, est_saved_tokens: 650, est_saved_usd: 0.01, turns: 2 },
    { ts: 2, session_id: 'two', output_tokens: 50, est_saved_tokens: 0, est_saved_usd: 0 },
  ].map(row => JSON.stringify(row)).join('\n') + '\n';
  fs.writeFileSync(historyPath, original);
  assert.deepStrictEqual(aggregateHistory(historyPath, null), { sessions: 2, outputTokens: 400, outputAvailability: 'complete' });
  assert.strictEqual(fs.readFileSync(historyPath, 'utf8'), original);
});

test('session view never infers output or budget reductions from usage', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { model: 'claude-sonnet-4-7', usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  // Observed output does not establish either kind of reduction.
  assertSavingsUnknown(out);
  assert.ok(!/budget|of your usage|of tracked usage/i.test(out),
    'must not relabel output reduction as a usage/budget share');
  // API pricing cannot establish a missing baseline.
  assertSavingsUnknown(out);
  // The missing comparison is explicit.
  assert.match(out, /no measured comparison for this session/);
  assert.ok(!/weekly limit|5-hour limit/i.test(out), 'must not fabricate Anthropic quota sizes');
});

test('--all ignores historical reduction percentages and dollars', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  const history = [
    { ts: Date.now(), session_id: 'a', output_tokens: 350, est_saved_tokens: 650, est_saved_usd: 0.01 },
    { ts: Date.now(), session_id: 'b', output_tokens: 650, est_saved_tokens: 350, est_saved_usd: 0.005 },
  ];
  fs.writeFileSync(
    path.join(claudeDir, '.caveman-history.jsonl'),
    history.map(h => JSON.stringify(h)).join('\n') + '\n',
  );
  const out = execFileSync(process.execPath, [STATS, '--all'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assertSavingsUnknown(out);
  assert.ok(!/budget|of your usage|of tracked usage/i.test(out),
    'must not relabel output reduction as a usage/budget share');
});

test('--all treats legacy zero estimates as unknown savings', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(
    path.join(claudeDir, '.caveman-history.jsonl'),
    JSON.stringify({ ts: Date.now(), session_id: 'a', output_tokens: 350, est_saved_tokens: 0, est_saved_usd: 0 }) + '\n',
  );
  const out = execFileSync(process.execPath, [STATS, '--all'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assertSavingsUnknown(out);
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
  // The old whole-session-at-current-mode math would claim 1,207 (inflated).
  assertSavingsUnknown(out);
  assert.doesNotMatch(out, /1,207/);
  assert.match(out, /Mode changed mid-session/);
  assert.match(out, /caveman off:\s+300 tokens/);
  assert.match(out, /full:\s+350 tokens/);
  assertSavingsUnknown(out);
  // Lifetime snapshots preserve the observed mode attribution.
  const hist = fs.readFileSync(path.join(claudeDir, '.caveman-history.jsonl'), 'utf8')
    .split('\n').filter(Boolean).map(l => JSON.parse(l));
  assert.ok(!Object.hasOwn(hist[hist.length - 1], 'est_saved_tokens'));
  assert.deepStrictEqual(hist[hist.length - 1].output_tokens_by_mode, { none: 300, full: 350 });
});

test('retains mode attribution after caveman is turned off mid-session (#601)', (tmp) => {
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
  // printed "Caveman not active this session." and logged zero savings.
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.doesNotMatch(out, /Caveman not active this session/);
  assert.match(out, /full:\s+350 tokens/);
  assertSavingsUnknown(out);
});

test('mixed and unattributed output stays observed usage in reports, shares and history', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', timestamp: new Date(1000).toISOString(), message: { usage: { output_tokens: 1000 } } },
    { type: 'assistant', timestamp: new Date(3000).toISOString(), message: { usage: { output_tokens: 200 } } },
    { type: 'assistant', message: { usage: { output_tokens: 50 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'ultra');
  fs.writeFileSync(path.join(claudeDir, '.caveman-mode-log.jsonl'), [
    { ts: 0, mode: 'full', prev: null },
    { ts: 2000, mode: 'ultra', prev: 'full' },
  ].map(row => JSON.stringify(row)).join('\n') + '\n');
  for (const share of [false, true]) {
    const args = [STATS, '--session-file', sess, ...(share ? ['--share'] : [])];
    const out = execFileSync(process.execPath, args, {
      encoding: 'utf8', env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
    });
    assertSavingsUnknown(out);
    if (share) {
      assert.match(out, /3 turns, 1,250 output tokens/);
    } else {
      assert.match(out, /Output tokens:\s+1,250/);
      assert.match(out, /full:\s+1,000 tokens/);
      assert.match(out, /ultra:\s+200 tokens/);
      assert.match(out, /unattributed:\s+50 tokens \(mode unknown\)/);
    }
  }
  const rows = fs.readFileSync(path.join(claudeDir, '.caveman-history.jsonl'), 'utf8').trim().split('\n').map(JSON.parse);
  for (const row of rows) {
    assert.strictEqual(row.output_tokens, 1250);
    assert.deepStrictEqual(row.output_tokens_by_mode, { full: 1000, ultra: 200 });
    assert.strictEqual(row.unattributed_output_tokens, 50);
    assert.strictEqual(row.mode_attribution, 'log');
    assert.ok(!Object.hasOwn(row, 'est_saved_tokens'));
    assert.ok(!Object.hasOwn(row, 'est_saved_usd'));
  }
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
  // the message is unknown; its token count is still observed.
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assertSavingsUnknown(out);
  assert.match(out, /unattributed:\s+350 tokens/);
  assert.match(out, /mode unknown/);
  assert.doesNotMatch(out, /Est\. without caveman/);
});

// ── No net or overhead claims without a measured comparison ────────────

test('longer output does not establish positive net savings', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 1500 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assertSavingsUnknown(out);
});

test('short output does not establish negative net savings (#145)', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 100 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assertSavingsUnknown(out);
  assert.match(out, /Output tokens:\s+100/);
  assert.doesNotMatch(out, /cost more|consider turning it off/);
});

test('legacy overhead override cannot create a comparison', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 1500 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir, CAVEMAN_RULE_OVERHEAD_TOKENS: '500' },
  });
  assertSavingsUnknown(out);
});

test('legacy overhead settings cannot manufacture a net result', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 1234 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  for (const overhead of ['500', '0', '-100', 'garbage']) {
    const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
      encoding: 'utf8', env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir, CAVEMAN_RULE_OVERHEAD_TOKENS: overhead },
    });
    assertSavingsUnknown(out);
    assert.match(out, /Output tokens:\s+1,234/);
  }
});

test('unattributed output remains counted with unknown mode and savings', (tmp) => {
  const now = Date.now();
  const sess = makeSession(tmp, [
    { type: 'assistant', timestamp: new Date(now - 60 * 60_000).toISOString(), message: { usage: { output_tokens: 350 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  // Flag written now, no transition log → mode during the message is unknown.
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'full');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /unattributed:\s+350 tokens/);
  assert.doesNotMatch(out, /Est\. net:/); // no attributed savings basis → no net claim
});

test('ultra mode also has no measured net result', (tmp) => {
  const sess = makeSession(tmp, [
    { type: 'assistant', message: { usage: { output_tokens: 100 } } },
  ]);
  const claudeDir = path.join(tmp, '.claude');
  fs.writeFileSync(path.join(claudeDir, '.caveman-active'), 'ultra');
  const out = execFileSync(process.execPath, [STATS, '--session-file', sess], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assert.match(out, /Mode: ultra/);
  assertSavingsUnknown(out);
  assert.doesNotMatch(out, /Est\. net:/);
});

test('lifetime view ignores legacy estimates even with turn counts', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  const histPath = path.join(claudeDir, '.caveman-history.jsonl');
  fs.writeFileSync(histPath, [
    { ts: 1000, session_id: 'a', mode: 'full', output_tokens: 1500, est_saved_tokens: 2786, est_saved_usd: 0, turns: 1 },
    { ts: 2000, session_id: 'b', mode: 'full', output_tokens: 100,  est_saved_tokens: 186,  est_saved_usd: 0, turns: 1 },
  ].map(o => JSON.stringify(o)).join('\n') + '\n');
  const out = execFileSync(process.execPath, [STATS, '--all'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assertSavingsUnknown(out);
  assert.match(out, /Output tokens:\s+1,600/);
});

test('lifetime view retains actual counts from legacy rows without turns', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-history.jsonl'),
    JSON.stringify({ ts: 1000, session_id: 'a', mode: 'full', output_tokens: 350, est_saved_tokens: 650, est_saved_usd: 0 }) + '\n');
  const out = execFileSync(process.execPath, [STATS, '--all'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assertSavingsUnknown(out);
  assert.match(out, /Output tokens:\s+350/);
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

test('mixed legacy history preserves output totals without net claims', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  fs.writeFileSync(path.join(claudeDir, '.caveman-history.jsonl'), [
    // Legacy row: no turns field, but its recorded output remains usable.
    { ts: 1000, session_id: 'legacy', mode: 'full', output_tokens: 350, est_saved_tokens: 650, est_saved_usd: 0 },
    { ts: 2000, session_id: 'new',    mode: 'full', output_tokens: 1500, est_saved_tokens: 2786, est_saved_usd: 0, turns: 1 },
  ].map(o => JSON.stringify(o)).join('\n') + '\n');
  const out = execFileSync(process.execPath, [STATS, '--all'], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir },
  });
  assertSavingsUnknown(out);
  assert.match(out, /Output tokens:\s+1,850/);
});

// #789 — the two caveman-stats docs describe the delivery mechanism the hook
// actually uses. This is drift, not prose: SKILL.md is loaded into the model's
// context when /caveman-stats fires, so "the hook returns decision: block, the
// model does not need to do anything" tells the model to stay silent at the
// exact moment additionalContext is asking it to print the block. The hook has
// emitted no `decision` since #618; `grep decision src/hooks/*.js` finds none.
//
// Ground truth comes from running the real hook rather than from a second
// hardcoded string, so this case cannot pass a doc that agrees with a contract
// the code has moved off.
test('caveman-stats docs describe the delivery mechanism the hook actually uses', (tmp) => {
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
  assert.strictEqual(parsed.decision, undefined,
    'ground truth: the hook does not block, so no doc may say it does');
  assert.ok(parsed.hookSpecificOutput && parsed.hookSpecificOutput.additionalContext,
    'ground truth: the hook delivers through additionalContext');

  for (const rel of ['skills/caveman-stats/SKILL.md', 'skills/caveman-stats/README.md']) {
    const doc = fs.readFileSync(path.join(ROOT, rel), 'utf8');
    assert.ok(!/decision:\s*"?block/i.test(doc) && !/blocked-decision/i.test(doc),
      `${rel} still describes the retired decision:"block" delivery`);
    assert.ok(!/does not need to do anything/i.test(doc),
      `${rel} still tells the model to do nothing, while the hook asks it to print the block`);
    assert.match(doc, /additionalContext/,
      `${rel} must name the additionalContext delivery the hook actually uses`);
  }
});

// The same #789 report also flagged the `hooks/…` paths in these docs. They are
// not wrong — the installer copies HOOK_FILES into $CLAUDE_CONFIG_DIR/hooks/,
// so that IS the installed layout — but they leave a repo reader with no path
// that exists here. Both spellings have to be reachable, so pin that the doc
// names the repo source and that the file is really there.
test('caveman-stats docs point a repo reader at a path that exists', () => {
  const doc = fs.readFileSync(path.join(ROOT, 'skills/caveman-stats/SKILL.md'), 'utf8');
  const referenced = [...doc.matchAll(/`(src\/hooks\/[A-Za-z0-9._-]+)`/g)].map(m => m[1]);
  assert.ok(referenced.includes('src/hooks/caveman-stats.js'),
    'SKILL.md must name the repo source of the stats script');
  for (const rel of referenced) {
    assert.ok(fs.existsSync(path.join(ROOT, rel)), `SKILL.md references a missing path: ${rel}`);
  }
});

// Third site of the same #789 root cause: when the stats script cannot run, the
// hook told the user to `node hooks/caveman-stats.js`. That relative path is
// only real for a standalone install rooted at $CLAUDE_CONFIG_DIR; a plugin
// user has no `hooks/` directory to run it from, and neither does anyone whose
// cwd is not the config dir. The hook already knows the resolved path.
test('stats fallback message names the script path that actually exists', (tmp) => {
  const claudeDir = path.join(tmp, '.claude');
  fs.mkdirSync(claudeDir, { recursive: true });
  // No transcript_path and an empty config dir: the stats child exits non-zero,
  // which is the branch that produces the fallback message.
  const out = execFileSync(process.execPath, [TRACKER], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: claudeDir, HOME: tmp },
    input: JSON.stringify({ prompt: '/caveman-stats' }),
    stdio: ['pipe', 'pipe', 'pipe'], // the failing child's stderr is expected noise
  });
  const ctx = JSON.parse(out).hookSpecificOutput.additionalContext;
  assert.match(ctx, /could not run stats script/);
  const suggested = /Try manually: node (.+)$/m.exec(ctx);
  assert.ok(suggested, `fallback must suggest a command: ${ctx}`);
  assert.ok(fs.existsSync(suggested[1].trim()),
    `fallback suggests a path that does not exist: ${suggested[1].trim()}`);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
