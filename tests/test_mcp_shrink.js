#!/usr/bin/env node
// Tests for src/mcp-servers/caveman-shrink/compress.js — pure-Node prose compressor.
// Run: node tests/test_mcp_shrink.js

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname, '..');
const { compress, compressDescriptionsInPlace } = require(
  path.join(ROOT, 'src', 'mcp-servers', 'caveman-shrink', 'compress.js')
);
const { getSpawnOptions } = require(
  path.join(ROOT, 'src', 'mcp-servers', 'caveman-shrink', 'spawn-options.js')
);
const { makeLineBuffer } = require(
  path.join(ROOT, 'src', 'mcp-servers', 'caveman-shrink', 'line-buffer.js')
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

test('preserves enum values inside parens (nested sentinel restoration — #444)', () => {
  // Path pattern matches "STARTER/BUSINESS" first (sentinel 0).
  // Function-call pattern then matches "type ( 0 )" (sentinel 1).
  // Single-pass restore replaces " 1 " with "type ( 0 )" but leaves the
  // inner " 0 " unrestored — model sees "( 0 )" instead of the enum.
  const cases = [
    { in: 'plan type (STARTER/BUSINESS)',   needle: 'STARTER/BUSINESS' },
    { in: 'user role (ADMIN/MEMBER/GUEST)', needle: 'ADMIN/MEMBER/GUEST' },
    { in: 'user plan (Free/Pro/Business)',  needle: 'Free/Pro/Business' },
  ];
  for (const c of cases) {
    const { compressed } = compress(c.in);
    assert.ok(
      compressed.includes(c.needle),
      `nested sentinel leaked: expected "${c.needle}" in output, got "${compressed}"`
    );
    assert.doesNotMatch(
      compressed,
      / \d+ /,
      `unrestored sentinel " N " left in output: "${compressed}"`
    );
  }
});

// ── Packaging (#597) ────────────────────────────────────────────────────────
// npm publish ships only what "files" lists (plus package.json/README/LICENSE,
// which npm always includes). A local module required by a shipped entry point
// but missing from "files" makes the published package crash with
// MODULE_NOT_FOUND at startup — exactly what happened with spawn-options.js.
// This static check walks every relative require reachable from the package's
// entry points (bin + main) and fails if any resolved file is not listed.
// The package is flat, so "files" entries are exact filenames, not globs.

test('package.json "files" ships every module the entry points require (#597)', () => {
  const pkgDir = path.join(ROOT, 'src', 'mcp-servers', 'caveman-shrink');
  const pkg = JSON.parse(fs.readFileSync(path.join(pkgDir, 'package.json'), 'utf8'));
  const shipped = new Set((pkg.files || []).map(f => path.normalize(f)));

  const entries = [...Object.values(pkg.bin || {}), pkg.main]
    .filter(Boolean)
    .map(f => path.normalize(f));
  assert.ok(entries.length > 0, 'package has no entry points to check');

  const seen = new Set();
  const queue = [...entries];
  while (queue.length > 0) {
    const rel = queue.pop();
    if (seen.has(rel)) continue;
    seen.add(rel);
    assert.ok(
      shipped.has(rel),
      `"${rel}" is required by a shipped entry point but missing from package.json "files" — npm publish would ship a broken package`
    );
    const src = fs.readFileSync(path.join(pkgDir, rel), 'utf8');
    for (const m of src.matchAll(/require\(\s*['"](\.{1,2}\/[^'"]+)['"]\s*\)/g)) {
      let dep = path.normalize(path.join(path.dirname(rel), m[1]));
      if (!dep.endsWith('.js') && !dep.endsWith('.json')) dep += '.js';
      queue.push(dep);
    }
  }
});

// ── Line framing across chunk boundaries ────────────────────────────────────
// Node delivers stdout to a 'data' listener in arbitrary byte-sized chunks. A
// per-chunk Buffer#toString('utf8') mangles any multi-byte character split
// across two chunks into U+FFFD, and because the line still parses as JSON the
// corruption reaches the model silently as a mangled tool description.

test('reassembles a multi-byte character split across chunks', () => {
  const payload = JSON.stringify({
    jsonrpc: '2.0', id: 1,
    result: { tools: [{ name: 't', description: '读取文件 — reads a file 🎉' }] }
  }) + '\n';
  const full = Buffer.from(payload, 'utf8');
  // Cut one byte into the 3-byte '读' so its sequence straddles the boundary.
  const cut = full.indexOf(Buffer.from('读', 'utf8')) + 1;

  const lines = [];
  const feed = makeLineBuffer(l => lines.push(l));
  feed(full.subarray(0, cut));
  feed(full.subarray(cut));

  assert.equal(lines.length, 1, 'expected exactly one framed line');
  assert.ok(!lines[0].includes('�'),
    `replacement character in reassembled line: ${lines[0]}`);
  assert.equal(JSON.parse(lines[0]).result.tools[0].description,
    '读取文件 — reads a file 🎉');
});

test('survives a byte-at-a-time stream', () => {
  const payload = JSON.stringify({ result: { tools: [{ description: '日本語 ✅ café' }] } }) + '\n';
  const full = Buffer.from(payload, 'utf8');
  const lines = [];
  const feed = makeLineBuffer(l => lines.push(l));
  for (const byte of full) feed(Buffer.from([byte]));

  assert.equal(lines.length, 1);
  assert.equal(JSON.parse(lines[0]).result.tools[0].description, '日本語 ✅ café');
});

test('emits one line per newline-delimited message and drops blanks', () => {
  const lines = [];
  const feed = makeLineBuffer(l => lines.push(l));
  feed(Buffer.from('{"a":1}\n\n  \n{"b":2}\n', 'utf8'));
  assert.deepStrictEqual(lines, ['{"a":1}', '{"b":2}']);
});

test('withholds a trailing partial line until its newline arrives', () => {
  const lines = [];
  const feed = makeLineBuffer(l => lines.push(l));
  feed(Buffer.from('{"a":', 'utf8'));
  assert.deepStrictEqual(lines, [], 'partial line must not be emitted');
  feed(Buffer.from('1}\n', 'utf8'));
  assert.deepStrictEqual(lines, ['{"a":1}']);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
