#!/usr/bin/env node
// Tests for the PostToolUse trim hook (src/hooks/caveman-trim-tool-result.js):
//   - pure transform: determinism, lossless noise stripping, head/tail elision,
//     JSON passthrough, whole-line cuts preserve path:line / Lnn: tokens,
//     idempotency (no growth on re-trim)
//   - end-to-end via stdin/stdout: env gate, tool filter, non-string passthrough,
//     small-result passthrough, updatedToolOutput shape
//
// Run: node tests/test_trim_tool_result.js

const fs = require('fs');
const path = require('path');
const assert = require('assert');
const { spawnSync } = require('child_process');

const HOOK = path.join(__dirname, '..', 'src', 'hooks', 'caveman-trim-tool-result.js');
const { transform, stripNoise, extractText } = require('../src/hooks/caveman-trim-tool-result');

let passed = 0;
let failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✓ ${name}`); }
  catch (e) { failed++; console.error(`  ✗ ${name}\n    ${e.message}`); }
}

function runHook(stdinObj, env) {
  const r = spawnSync(process.execPath, [HOOK], {
    input: JSON.stringify(stdinObj),
    encoding: 'utf8',
    env: { ...process.env, ...env },
  });
  return r.stdout || '';
}

function updatedOutput(stdout) {
  if (!stdout.trim()) return null;
  return JSON.parse(stdout).hookSpecificOutput.updatedToolOutput;
}

const bigLines = (n, prefix = 'line') =>
  Array.from({ length: n }, (_, i) => `${prefix} ${i}`).join('\n');

console.log('trim-tool-result: pure transform + end-to-end\n');

// ---------- pure transform ----------

test('deterministic: same input -> identical output', () => {
  const input = bigLines(500) + '\x1b[31mred\x1b[0m';
  assert.strictEqual(transform(input), transform(input));
});

test('strips ANSI color/cursor sequences', () => {
  const s = stripNoise('\x1b[31mERROR\x1b[0m: \x1b[1mboom\x1b[0m');
  assert.strictEqual(s, 'ERROR: boom');
});

test('collapses carriage-return progress redraws and blank-line runs', () => {
  // \r\n -> \n; intra-line \r is terminal overwrite (keep last segment); 3+\n -> 2
  assert.strictEqual(stripNoise('a\r\nb\rc\n\n\n\nd'), 'a\nc\n\nd');
  // a progress bar collapses to its final frame
  assert.strictEqual(stripNoise('10%\r20%\r100% done'), '100% done');
});

test('head/tail elision keeps both ends and names recovery, drops the middle', () => {
  const input = bigLines(1000);
  const out = transform(input, 100, 160, 60);
  assert.ok(out.includes('line 0'), 'keeps head');
  assert.ok(out.includes('line 999'), 'keeps tail');
  assert.ok(!out.includes('line 500'), 'drops the middle');
  assert.ok(/trimmed \d+ middle lines of 1000/.test(out), 'names how much was dropped');
  assert.ok(/re-run the tool/.test(out), 'names the recovery action');
});

test('JSON-ish result is passed through untouched (returns null)', () => {
  const json = '{\n  "a": 1,\n\n\n  "b": "x"\n}';
  assert.strictEqual(transform(json, 1), null);
  const arr = '[\n1,\n2\n]';
  assert.strictEqual(transform(arr, 1), null);
});

test('whole-line cuts preserve path:line and Lnn: tokens inside kept lines', () => {
  const lines = [];
  for (let i = 0; i < 400; i++) lines.push(`src/foo.ts:${i}: something L${i}: detail`);
  const out = transform(lines.join('\n'), 100, 160, 60);
  assert.ok(out.includes('src/foo.ts:0: something L0: detail'), 'head token intact');
  assert.ok(out.includes('src/foo.ts:399: something L399: detail'), 'tail token intact');
});

test('idempotent: re-trimming a trimmed result does not grow it', () => {
  const input = bigLines(1000);
  const once = transform(input, 100, 160, 60);
  const twice = transform(once, 100, 160, 60);
  // twice is either null (nothing left to gain) or no longer than once.
  if (twice !== null) assert.ok(twice.length <= once.length, 'must not grow');
});

test('huge single line falls back to char-level head/tail', () => {
  const input = 'x'.repeat(50000); // one giant line, few newlines
  const out = transform(input, 1000, 160, 60);
  assert.ok(out && out.length < input.length, 'shrinks the blob');
  assert.ok(/trimmed \d+ middle chars/.test(out), 'char-level marker');
});

test('returns null when there is nothing worth trimming', () => {
  assert.strictEqual(transform('short clean text', 8000), null);
});

// ---------- end-to-end (stdin/stdout) ----------

const BIG = bigLines(2000); // > default 8000-char threshold

test('disabled: no output unless CAVEMAN_TRIM_TOOL_RESULTS=1', () => {
  // Explicitly clear the var — the dev's own shell may have it set.
  const out = runHook({ tool_name: 'Bash', tool_response: BIG }, { CAVEMAN_TRIM_TOOL_RESULTS: '' });
  assert.strictEqual(out.trim(), '', 'must do nothing unless CAVEMAN_TRIM_TOOL_RESULTS=1');
});

test('enabled: trims a big Bash result via updatedToolOutput', () => {
  const out = runHook({ tool_name: 'Bash', tool_response: BIG }, { CAVEMAN_TRIM_TOOL_RESULTS: '1' });
  const updated = updatedOutput(out);
  assert.ok(updated && updated.length < BIG.length, 'should shrink');
  assert.ok(updated.includes('line 0') && updated.includes('line 1999'), 'keeps both ends');
});

test('enabled but tool not in the set: passthrough', () => {
  const out = runHook({ tool_name: 'WebFetch', tool_response: BIG }, { CAVEMAN_TRIM_TOOL_RESULTS: '1' });
  assert.strictEqual(out.trim(), '');
});

test('enabled but result below threshold: passthrough', () => {
  const out = runHook({ tool_name: 'Grep', tool_response: 'small' }, { CAVEMAN_TRIM_TOOL_RESULTS: '1' });
  assert.strictEqual(out.trim(), '');
});

test('enabled: trims a Bash object result {stdout,stderr} (the real shape)', () => {
  const out = runHook(
    { tool_name: 'Bash', tool_response: { stdout: BIG, stderr: 'oops failed', interrupted: false } },
    { CAVEMAN_TRIM_TOOL_RESULTS: '1' }
  );
  const updated = updatedOutput(out);
  assert.ok(updated && updated.length < BIG.length, 'should trim the stdout field');
  assert.ok(updated.includes('line 0') && updated.includes('line 1999'), 'keeps both ends');
  assert.ok(updated.includes('[stderr] oops failed'), 'preserves a short stderr tail');
});

test('object tool_response with no text field: passthrough', () => {
  const out = runHook({ tool_name: 'Bash', tool_response: { isImage: true, foo: 1 } }, { CAVEMAN_TRIM_TOOL_RESULTS: '1' });
  assert.strictEqual(out.trim(), '');
});

test('extractText pulls stdout from a Bash object, passes a plain string through', () => {
  assert.strictEqual(extractText('hello').text, 'hello');
  const ex = extractText({ stdout: 'big output', stderr: 'err' });
  assert.strictEqual(ex.text, 'big output');
  assert.strictEqual(ex.rebuild('TRIMMED'), 'TRIMMED\n[stderr] err');
  assert.strictEqual(extractText({ isImage: true }), null);
  assert.strictEqual(extractText(42), null);
});

test('malformed stdin: fails open (no output, no crash)', () => {
  const r = spawnSync(process.execPath, [HOOK], {
    input: 'not json',
    encoding: 'utf8',
    env: { ...process.env, CAVEMAN_TRIM_TOOL_RESULTS: '1' },
  });
  assert.strictEqual((r.stdout || '').trim(), '');
  assert.strictEqual(r.status, 0);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
