#!/usr/bin/env node
// Tests for the PreToolUse input-bounding hook (src/hooks/caveman-bound-tool-input.js):
//   - pure boundInput: Read cap, Grep cap, respect explicit values, strict-object
//     spread, non-target tools/inputs left alone
//   - end-to-end via stdin/stdout: env gate, updatedInput shape, fail-open
//
// Run: node tests/test_bound_tool_input.js

const path = require('path');
const assert = require('assert');
const { spawnSync } = require('child_process');

const HOOK = path.join(__dirname, '..', 'src', 'hooks', 'caveman-bound-tool-input.js');
const { boundInput, DEFAULT_READ_LIMIT } = require('../src/hooks/caveman-bound-tool-input');

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

function updatedInput(stdout) {
  if (!stdout.trim()) return null;
  return JSON.parse(stdout).hookSpecificOutput.updatedInput;
}

console.log('bound-tool-input: PreToolUse input capping\n');

// ---------- pure boundInput ----------

test('Read with no limit -> injects the cap, keeps file_path', () => {
  const out = boundInput('Read', { file_path: '/a/big.ts' });
  assert.deepStrictEqual(out, { file_path: '/a/big.ts', limit: DEFAULT_READ_LIMIT });
});

test('Read with an explicit limit -> untouched (respect the model)', () => {
  assert.strictEqual(boundInput('Read', { file_path: '/a/x.ts', limit: 50 }), null);
});

test('Read spreads existing offset/pages (strict-object safe)', () => {
  const out = boundInput('Read', { file_path: '/a/x.ts', offset: 100 }, { readLimit: 1500 });
  assert.deepStrictEqual(out, { file_path: '/a/x.ts', offset: 100, limit: 1500 });
});

test('Read with no file_path -> null', () => {
  assert.strictEqual(boundInput('Read', { limit: null }), null);
});

test('Grep with an oversized head_limit -> capped', () => {
  const out = boundInput('Grep', { pattern: 'x', output_mode: 'content', head_limit: 100000 }, { grepHead: 1000 });
  assert.strictEqual(out.head_limit, 1000);
  assert.strictEqual(out.pattern, 'x');
  assert.strictEqual(out.output_mode, 'content');
});

test('Grep with a small head_limit -> untouched', () => {
  assert.strictEqual(boundInput('Grep', { pattern: 'x', head_limit: 50 }), null);
});

test('Grep with no head_limit -> untouched (tool applies its own default)', () => {
  assert.strictEqual(boundInput('Grep', { pattern: 'x', output_mode: 'content' }), null);
});

test('Bash and other tools -> never bounded', () => {
  assert.strictEqual(boundInput('Bash', { command: 'ls' }), null);
  assert.strictEqual(boundInput('Glob', { pattern: '**/*' }), null);
});

test('non-object input -> null', () => {
  assert.strictEqual(boundInput('Read', null), null);
  assert.strictEqual(boundInput('Read', 'x'), null);
});

// ---------- end-to-end ----------

test('disabled unless CAVEMAN_BOUND_TOOL_INPUT=1', () => {
  const out = runHook({ tool_name: 'Read', tool_input: { file_path: '/a/x.ts' } }, { CAVEMAN_BOUND_TOOL_INPUT: '' });
  assert.strictEqual(out.trim(), '', 'must do nothing unless enabled');
});

test('enabled: emits updatedInput with the injected limit for an unbounded Read', () => {
  const out = runHook(
    { tool_name: 'Read', tool_input: { file_path: '/a/big.ts' } },
    { CAVEMAN_BOUND_TOOL_INPUT: '1', CAVEMAN_BOUND_READ_LIMIT: '1234' }
  );
  const ui = updatedInput(out);
  assert.deepStrictEqual(ui, { file_path: '/a/big.ts', limit: 1234 });
});

test('enabled but Read already bounded: passthrough (no output)', () => {
  const out = runHook(
    { tool_name: 'Read', tool_input: { file_path: '/a/x.ts', limit: 30 } },
    { CAVEMAN_BOUND_TOOL_INPUT: '1' }
  );
  assert.strictEqual(out.trim(), '');
});

test('malformed stdin: fails open (no output, exit 0)', () => {
  const r = spawnSync(process.execPath, [HOOK], {
    input: 'not json', encoding: 'utf8',
    env: { ...process.env, CAVEMAN_BOUND_TOOL_INPUT: '1' },
  });
  assert.strictEqual((r.stdout || '').trim(), '');
  assert.strictEqual(r.status, 0);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
