#!/usr/bin/env node
// Tests for caveman-expand (experimental). No network: the backend paths are
// exercised by pointing OLLAMA_HOST at a dead port and asserting passthrough.
// Run: node tests/test_caveman_expand.js

const path = require('path');
const assert = require('assert');
const { execFileSync } = require('child_process');

const ROOT = path.resolve(__dirname, '..');
const EXPAND = path.join(ROOT, 'src', 'tools', 'caveman-expand.js');
const DEAD_HOST = 'http://127.0.0.1:9'; // discard port — refuses instantly

let passed = 0;
let failed = 0;

function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}\n    ${e.message}`);
  }
}

console.log('caveman-expand tests\n');

test('segmentMarkdown preserves code fences verbatim and in order', () => {
  const { segmentMarkdown } = require(EXPAND);
  const input = 'Intro prose.\n\n```js\nconst x = 1;\n```\n\nMore prose.\n\n~~~\nliteral\n~~~\nTail.';
  const segs = segmentMarkdown(input);
  assert.deepStrictEqual(segs.map(s => s.type), ['prose', 'code', 'prose', 'code', 'prose']);
  assert.strictEqual(segs[1].text, '```js\nconst x = 1;\n```');
  assert.strictEqual(segs[3].text, '~~~\nliteral\n~~~');
  // Reassembly of raw segments is byte-identical to the input.
  assert.strictEqual(segs.map(s => s.text).join(''), input);
});

test('segmentMarkdown handles text with no fences', () => {
  const { segmentMarkdown } = require(EXPAND);
  const segs = segmentMarkdown('just prose, no code');
  assert.strictEqual(segs.length, 1);
  assert.strictEqual(segs[0].type, 'prose');
});

test('segmentMarkdown does not treat an unclosed fence as code', () => {
  const { segmentMarkdown } = require(EXPAND);
  const input = 'prose\n```js\nnever closed';
  const segs = segmentMarkdown(input);
  assert.ok(segs.every(s => s.type === 'prose'));
  assert.strictEqual(segs.map(s => s.text).join(''), input);
});

test('buildPrompt embeds the prose and the no-new-facts rule', () => {
  const { buildPrompt } = require(EXPAND);
  const p = buildPrompt('fix bug. push.');
  assert.match(p, /do NOT add information/);
  assert.match(p, /fix bug\. push\./);
});

test('passes stdin through unchanged when no backend is reachable', () => {
  const input = 'Rock good. `npm i` fix. Done.\n';
  const out = execFileSync(process.execPath, [EXPAND], {
    encoding: 'utf8',
    input,
    env: { ...process.env, OLLAMA_HOST: DEAD_HOST },
  });
  assert.strictEqual(out, input);
});

test('--check exits non-zero and says unavailable when no backend', () => {
  let err = null;
  let out = '';
  try {
    out = execFileSync(process.execPath, [EXPAND, '--check'], {
      encoding: 'utf8',
      env: { ...process.env, OLLAMA_HOST: DEAD_HOST },
    });
  } catch (e) { err = e; out = e.stdout || ''; }
  assert.ok(err, 'should exit non-zero');
  assert.match(out, /unavailable/);
});

test('--help prints usage and exits zero', () => {
  const out = execFileSync(process.execPath, [EXPAND, '--help'], { encoding: 'utf8' });
  assert.match(out, /experimental/);
  assert.match(out, /--model/);
});

test('empty stdin produces empty stdout', () => {
  const out = execFileSync(process.execPath, [EXPAND], {
    encoding: 'utf8',
    input: '',
    env: { ...process.env, OLLAMA_HOST: DEAD_HOST },
  });
  assert.strictEqual(out, '');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
