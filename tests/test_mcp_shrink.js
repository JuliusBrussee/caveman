#!/usr/bin/env node
// Tests for src/mcp-servers/caveman-shrink/compress.js — pure-Node prose compressor.
// Run: node tests/test_mcp_shrink.js

const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname, '..');
const { compress, compressDescriptionsInPlace, stripResultText } = require(
  path.join(ROOT, 'src', 'mcp-servers', 'caveman-shrink', 'compress.js')
);
const { getSpawnOptions } = require(
  path.join(ROOT, 'src', 'mcp-servers', 'caveman-shrink', 'spawn-options.js')
);

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

console.log('mcp-shrink compress tests\n');

test('drops articles', () => {
  const { compressed } = compress('The user is the owner of an account');
  assert.match(compressed, /User is owner of account/i);
  // No leftover lone "the" / "an" / "a"
  assert.doesNotMatch(compressed, /\bthe\b/i);
  assert.doesNotMatch(compressed, /\ban\b/i);
});

test('drops filler and pleasantries', () => {
  const { compressed } = compress('Sure, this just basically returns the value');
  assert.doesNotMatch(compressed, /sure/i);
  assert.doesNotMatch(compressed, /just/i);
  assert.doesNotMatch(compressed, /basically/i);
});

test('drops hedging and "I will" leaders', () => {
  const { compressed } = compress('I will perhaps connect to the database');
  assert.doesNotMatch(compressed, /perhaps/i);
  assert.doesNotMatch(compressed, /^I will/i);
  assert.match(compressed, /database/i);
});

test('preserves fenced code blocks verbatim', () => {
  const input = 'Run the example: ```\nthe just sure return 1;\n``` and also more text';
  const { compressed } = compress(input);
  // Inside the fence, "the just sure" must survive untouched.
  assert.match(compressed, /```\nthe just sure return 1;\n```/);
});

test('preserves inline code verbatim', () => {
  const input = 'Use `the just basically API` for fetching';
  const { compressed } = compress(input);
  assert.match(compressed, /`the just basically API`/);
});

test('preserves URLs verbatim', () => {
  const input = 'See the docs at https://example.com/the/just/api';
  const { compressed } = compress(input);
  assert.match(compressed, /https:\/\/example\.com\/the\/just\/api/);
});

test('preserves filesystem paths verbatim', () => {
  const input = 'Read just the file at /tmp/the/just/file.txt';
  const { compressed } = compress(input);
  assert.match(compressed, /\/tmp\/the\/just\/file\.txt/);
});

test('preserves identifiers in CONST_CASE / dotted form', () => {
  const input = 'Set the API_KEY_VALUE on the just config.api.endpoint()';
  const { compressed } = compress(input);
  assert.match(compressed, /API_KEY_VALUE/);
  assert.match(compressed, /config\.api\.endpoint\(\)/);
});

test('compresses real MCP-style description', () => {
  const input = 'Get the current weather for a given location. ' +
    'Returns the temperature in Fahrenheit. ' +
    'Please make sure to provide the location as a city name.';
  const { compressed, before, after } = compress(input);
  assert.ok(after < before, `expected size reduction, got ${before}→${after}`);
  // ~30% reduction is the floor; descriptions like this should compress well.
  assert.ok((before - after) / before > 0.15, `wanted >15% savings, got ${(before - after) / before}`);
  // Substance preserved
  assert.match(compressed, /weather/i);
  assert.match(compressed, /Fahrenheit/i);
  assert.match(compressed, /city name/i);
});

test('handles empty / null input gracefully', () => {
  assert.deepStrictEqual(compress(''), { compressed: '', before: 0, after: 0 });
  const r = compress(null);
  assert.strictEqual(r.compressed, null);
});

test('compressDescriptionsInPlace walks nested tools/list response', () => {
  const payload = {
    result: {
      tools: [
        { name: 'get_weather', description: 'The function returns the current weather for a city.' },
        { name: 'send_email', description: 'Sends an email to a given recipient.' },
      ]
    }
  };
  compressDescriptionsInPlace(payload.result, ['description']);
  assert.ok(!payload.result.tools[0].description.match(/\bthe\b/i),
    `expected 'the' stripped, got: ${payload.result.tools[0].description}`);
  assert.match(payload.result.tools[0].description, /weather/i);
  assert.match(payload.result.tools[1].description, /email/i);
});

test('compressDescriptionsInPlace skips non-string description fields', () => {
  const obj = { description: { not: 'a string' }, name: 'x' };
  // Should not throw.
  compressDescriptionsInPlace(obj, ['description']);
  assert.deepStrictEqual(obj.description, { not: 'a string' });
});

// stripResultText: LOSSLESS strip for opt-in tools/call result compression.
// Must remove terminal noise but NEVER prose-compress (result data the model
// acts on) and NEVER touch structured (JSON-ish) text.

test('stripResultText removes ANSI + blank-line runs losslessly', () => {
  assert.strictEqual(stripResultText('\x1b[31mERR\x1b[0m: boom\n\n\n\ndone'), 'ERR: boom\n\ndone');
});

test('stripResultText collapses carriage-return progress redraws to the last frame', () => {
  assert.strictEqual(stripResultText('10%\r50%\r100% done'), '100% done');
});

test('stripResultText does NOT prose-compress result data (keeps articles/fillers)', () => {
  // A tool result is data — caveman-speak would corrupt it. Words must survive.
  const t = 'The user just basically owns the account';
  assert.strictEqual(stripResultText(t), t);
});

test('stripResultText leaves JSON-ish result untouched', () => {
  const j = '{\n  "a": 1,\n\n\n  "b": "x"\n}';
  assert.strictEqual(stripResultText(j), j);
  assert.strictEqual(stripResultText('[1,\n2,\n3]'), '[1,\n2,\n3]');
});

test('stripResultText is deterministic and passes through non-strings', () => {
  const t = 'line1   \nline2\x1b[0m\n\n\n\nline3';
  assert.strictEqual(stripResultText(t), stripResultText(t));
  assert.strictEqual(stripResultText(42), 42);
});

// spawn-options: upstream MCP child process spawn flags.
// Confirms shell:true on Windows (so npx and other .cmd shims resolve) and
// shell:false on POSIX.

test('win32 enables shell so npx and .cmd shims resolve', () => {
  const opts = getSpawnOptions('win32');
  assert.equal(opts.shell, true);
  assert.equal(opts.windowsHide, true);
  assert.deepEqual(opts.stdio, ['pipe', 'pipe', 'inherit']);
});

test('linux keeps shell off to avoid argv quoting surprises', () => {
  const opts = getSpawnOptions('linux');
  assert.equal(opts.shell, false);
  assert.deepEqual(opts.stdio, ['pipe', 'pipe', 'inherit']);
});

test('darwin keeps shell off', () => {
  const opts = getSpawnOptions('darwin');
  assert.equal(opts.shell, false);
});

test('defaults to current platform when no arg passed', () => {
  const opts = getSpawnOptions();
  assert.equal(opts.shell, process.platform === 'win32');
  assert.equal(opts.windowsHide, true);
  assert.deepEqual(opts.stdio, ['pipe', 'pipe', 'inherit']);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
