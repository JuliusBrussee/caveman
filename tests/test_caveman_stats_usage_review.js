#!/usr/bin/env node
// Independent stats evidence checks: execute the installed-style script with
// isolated transcripts. No provider requests or user state are involved.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const test = require('node:test');

const STATS = path.resolve(__dirname, '../src/hooks/caveman-stats.js');

function fixture(t, rows = []) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-stats-usage-review-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const claude = path.join(dir, '.claude');
  fs.mkdirSync(claude);
  fs.writeFileSync(path.join(claude, '.caveman-active'), 'full');
  const transcript = path.join(dir, 'session.jsonl');
  fs.writeFileSync(transcript, rows.map(usage => JSON.stringify({ type: 'assistant', message: { usage } })).join('\n'));
  const run = (args = ['--session-file', transcript], env = {}, preload = []) => spawnSync(process.execPath,
    [...preload, STATS, ...args], { encoding: 'utf8', cwd: dir, env: { ...process.env, HOME: dir, CLAUDE_CONFIG_DIR: claude, GEMINI_CLI: '0', ...env } });
  return { dir, claude, transcript, run };
}

function usageOutput(result) {
  assert.equal(result.status, 0, result.stderr);
  // The unrelated savings-unknown sentence cannot satisfy a missing-usage
  // disclosure. These are different kinds of evidence.
  return result.stdout.split('\n').filter(line => !/savings/i.test(line)).join('\n');
}

test('missing output counter is unknown while a reported cache count remains usable', t => {
  const f = fixture(t, [{ input_tokens: 12, cache_read_input_tokens: 7 }]);
  const out = usageOutput(f.run());
  assert.doesNotMatch(out, /Output tokens:\s+0\b/);
  assert.match(out, /unknown|unavailable|partial/i);
  assert.match(out, /Cache-read tokens:\s+7\b/);
});

test('missing cache counter is not reported as a measured zero', t => {
  const f = fixture(t, [{ output_tokens: 17 }]);
  const out = usageOutput(f.run());
  assert.match(out, /Output tokens:\s+17\b/);
  assert.doesNotMatch(out, /Cache-read tokens:\s+0\b/);
  assert.match(out, /unknown|unavailable|partial/i);
});

test('explicit zero counters remain measured zeros', t => {
  const f = fixture(t, [{ output_tokens: 0, cache_read_input_tokens: 0 }]);
  const out = usageOutput(f.run());
  assert.match(out, /Output tokens:\s+0\b/);
  assert.match(out, /Cache-read tokens:\s+0\b/);
});

test('a string counter cannot concatenate into public totals or numeric history', t => {
  const f = fixture(t, [{ output_tokens: '100', cache_read_input_tokens: 0 }, { output_tokens: 50, cache_read_input_tokens: 0 }]);
  const out = usageOutput(f.run());
  assert.doesNotMatch(out, /010050|10,050/);
  assert.match(out, /Output tokens:\s+50 known \(partial; total unknown\)/);
  assert.match(out, /Cache-read tokens:\s+0\n/);
  const rows = fs.readFileSync(path.join(f.claude, '.caveman-history.jsonl'), 'utf8').trim().split('\n').map(JSON.parse);
  assert.equal(rows.at(-1).output_tokens, 50);
  assert.equal(rows.at(-1).output_tokens_availability, 'partial');
  assert.equal(rows.at(-1).cache_read_input_tokens_availability, 'complete');
});

test('a response with no usage does not look like an empty conversation', t => {
  const f = fixture(t);
  fs.writeFileSync(f.transcript, JSON.stringify({ type: 'assistant', message: { id: 'msg_fixture', content: [{ type: 'text', text: 'An actual response' }] } }) + '\n');
  const result = f.run();
  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stdout, /No conversation yet|No turns yet/);
  assert.match(result.stdout, /Turns:\s+1/);
  assert.match(usageOutput(result), /unknown|unavailable|partial/i);
  const row = JSON.parse(fs.readFileSync(path.join(f.claude, '.caveman-history.jsonl'), 'utf8').trim());
  assert.equal(row.output_tokens, null);
  assert.equal(row.cache_read_input_tokens, null);
  assert.equal(row.output_tokens_availability, 'unknown');
  assert.equal(row.cache_read_input_tokens_availability, 'unknown');
  assert.match(usageOutput(f.run(['--all'])), /Output tokens:\s+unknown/);
});

test('an unreadable explicit transcript reports unavailable usage instead of an empty conversation', t => {
  const f = fixture(t);
  for (const file of [path.join(f.dir, 'missing.jsonl'), f.dir]) {
    const result = f.run(['--session-file', file]);
    assert.equal(result.status, 1);
    assert.equal(result.stdout, '');
    assert.match(result.stderr, /could not read.*Usage unavailable/);
    assert.ok(result.stderr.includes(file));
  }
  assert.equal(fs.existsSync(path.join(f.claude, '.caveman-history.jsonl')), false);
});

test('each counter accepts only nonnegative safe integers and keeps the other reported field', t => {
  const invalid = ['100', null, false, true, -1, 0.5, Number.MAX_SAFE_INTEGER + 1, [], {}];
  for (const counter of ['output_tokens', 'cache_read_input_tokens']) {
    const other = counter === 'output_tokens' ? 'cache_read_input_tokens' : 'output_tokens';
    const label = counter === 'output_tokens' ? 'Output tokens' : 'Cache-read tokens';
    const otherLabel = counter === 'output_tokens' ? 'Cache-read tokens' : 'Output tokens';
    for (const value of invalid) {
      const f = fixture(t, [{ [counter]: value, [other]: 7 }, { [counter]: 5, [other]: 0 }]);
      const out = usageOutput(f.run());
      assert.match(out, new RegExp(`${label}:\\s+5 known \\(partial; total unknown\\)`));
      assert.match(out, new RegExp(`${otherLabel}:\\s+7\\n`));
      const history = JSON.parse(fs.readFileSync(path.join(f.claude, '.caveman-history.jsonl'), 'utf8').trim());
      assert.equal(history[counter], 5);
      assert.equal(history[counter + '_availability'], 'partial');
    }
  }
});

test('overflow cannot produce an unsafe session or lifetime token total', t => {
  const f = fixture(t, [
    { output_tokens: Number.MAX_SAFE_INTEGER, cache_read_input_tokens: 3 },
    { output_tokens: 1, cache_read_input_tokens: 2 },
  ]);
  const out = usageOutput(f.run());
  assert.match(out, /Output tokens:\s+unknown/);
  assert.match(out, /Cache-read tokens:\s+5\n/);
  const history = path.join(f.claude, '.caveman-history.jsonl');
  const row = JSON.parse(fs.readFileSync(history, 'utf8').trim());
  assert.equal(row.output_tokens, null);
  assert.equal(row.output_tokens_availability, 'unknown');
  assert.deepEqual(row.output_tokens_by_mode, {});
  fs.writeFileSync(history, [
    { session_id: 'a', ts: 1, output_tokens: Number.MAX_SAFE_INTEGER },
    { session_id: 'b', ts: 2, output_tokens: 1 },
  ].map(JSON.stringify).join('\n') + '\n');
  assert.match(usageOutput(f.run(['--all'])), /Output tokens:\s+unknown/);
});

test('later content blocks can supply missing usage without adding another response', t => {
  const f = fixture(t);
  const entry = usage => ({ type: 'assistant', requestId: 'req_fixture', timestamp: '2026-01-01T12:00:00.000Z',
    message: { id: 'msg_fixture', content: [{ type: 'text', text: 'A response block' }], usage } });
  fs.writeFileSync(f.transcript, [
    entry(undefined), entry({ cache_read_input_tokens: 3 }),
    entry({ output_tokens: 12, cache_read_input_tokens: 3 }),
    entry({ output_tokens: 12, cache_read_input_tokens: 3 }),
  ].map(JSON.stringify).join('\n'));
  const out = usageOutput(f.run());
  assert.match(out, /Turns:\s+1/);
  assert.match(out, /Output tokens:\s+12\n/);
  assert.match(out, /Cache-read tokens:\s+3\n/);
  assert.doesNotMatch(out, /partial|unavailable/);
});

test('share keeps missing and partial output distinct from observed zero', t => {
  const f = fixture(t, [{ output_tokens: 50 }, {}]);
  const partial = f.run(['--session-file', f.transcript, '--share']);
  assert.equal(partial.status, 0, partial.stderr);
  assert.match(partial.stdout, /2 turns, 50 known output tokens \(partial; total unknown\)/);
  fs.writeFileSync(f.transcript, JSON.stringify({ type: 'assistant', message: { content: [] } }));
  const unknown = f.run(['--session-file', f.transcript, '--share']);
  assert.equal(unknown.status, 0, unknown.stderr);
  assert.match(unknown.stdout, /1 turn, output tokens unknown \(usage unavailable\)/);
  assert.doesNotMatch(unknown.stdout, /No turns yet|0 output tokens/);
});

test('lifetime retains availability across latest snapshots and valid legacy history', t => {
  const f = fixture(t, [{ output_tokens: 50, cache_read_input_tokens: 0 }, {}]);
  const history = path.join(f.claude, '.caveman-history.jsonl');
  const old = [
    { session_id: 'old-complete', ts: 1, output_tokens: 75, est_saved_tokens: 1000 },
    { session_id: 'session', ts: 2, output_tokens: 40 },
  ].map(JSON.stringify).join('\n') + '\n';
  fs.writeFileSync(history, old);
  assert.equal(f.run().status, 0);
  const afterAppend = fs.readFileSync(history, 'utf8');
  assert.ok(afterAppend.startsWith(old), 'old snapshots must remain intact');
  assert.match(usageOutput(f.run(['--all'])), /Output tokens:\s+125 known \(partial; total unknown\)/);
  assert.match(usageOutput(f.run(['--since', '1h'])), /Output tokens:\s+50 known \(partial; total unknown\)/);
  assert.equal(fs.readFileSync(history, 'utf8'), afterAppend, 'reading history must not mutate it');
  // A latest snapshot with no counts must not revive an older complete value.
  fs.appendFileSync(history, JSON.stringify({ session_id: 'session', ts: Date.now() + 1,
    output_tokens: null, output_tokens_availability: 'unknown' }) + '\n');
  assert.match(usageOutput(f.run(['--all'])), /Output tokens:\s+75 known \(partial; total unknown\)/);
});

test('invalid historical counters or availability never become complete numeric totals', t => {
  const f = fixture(t);
  const history = path.join(f.claude, '.caveman-history.jsonl');
  for (const row of [
    { output_tokens: '100' }, { output_tokens: -3 }, { output_tokens: 0.5 }, {},
    { output_tokens: 100, output_tokens_availability: 'unknown' },
    { output_tokens: 100, output_tokens_availability: 'unexpected' },
  ]) {
    fs.writeFileSync(history, [
      { session_id: 'valid', ts: 1, output_tokens: 7 },
      { session_id: 'incomplete', ts: 2, ...row },
    ].map(JSON.stringify).join('\n') + '\n');
    assert.match(usageOutput(f.run(['--all'])), /Output tokens:\s+7 known \(partial; total unknown\)/);
  }
});

test('Gemini exits before any Claude state file operation', t => {
  const f = fixture(t, [{ output_tokens: 987654, cache_read_input_tokens: 12345 }]);
  const accessLog = path.join(f.dir, 'access.jsonl');
  const preload = path.join(f.dir, 'fs-access.cjs');
  fs.writeFileSync(preload, `
const fs = require('node:fs');
const path = require('node:path');
const append = fs.appendFileSync;
for (const name of ['readFileSync', 'writeFileSync', 'appendFileSync', 'readdirSync', 'statSync', 'lstatSync', 'openSync', 'mkdirSync', 'unlinkSync', 'renameSync']) {
  const original = fs[name];
  fs[name] = function(first, ...rest) {
    if (typeof first === 'string' && (path.resolve(first) === process.env.CLAUDE_CONFIG_DIR || path.resolve(first).startsWith(process.env.CLAUDE_CONFIG_DIR + path.sep))) {
      append(process.env.CAVE_REVIEW_ACCESS_LOG, JSON.stringify({ method: name }) + '\\n');
    }
    return original.call(this, first, ...rest);
  };
}
`);
  for (const args of [[], ['--all'], ['--share'], ['--session-file', f.transcript], ['--host', 'gemini', '--since', '7d']]) {
    fs.writeFileSync(accessLog, '');
    const result = f.run(args, { GEMINI_CLI: '1', CAVE_REVIEW_ACCESS_LOG: accessLog }, ['--require', preload]);
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /\/stats model/);
    assert.doesNotMatch(result.stdout, /987[,.]?654|12[,.]?345/);
    assert.equal(fs.readFileSync(accessLog, 'utf8'), '', 'Gemini touched Claude state');
  }
  // Positive control: an explicitly owned Claude invocation exercises the
  // instrumentation even when launched from a Gemini-marked parent process.
  f.run(['--host', 'claude', '--session-file', f.transcript], { GEMINI_CLI: '1', CAVE_REVIEW_ACCESS_LOG: accessLog }, ['--require', preload]);
  assert.notEqual(fs.readFileSync(accessLog, 'utf8'), '');
});
