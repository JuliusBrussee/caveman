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
const { killUpstream, installShutdownHandlers, FORWARDED_SIGNALS } = require(
  path.join(ROOT, 'src', 'mcp-servers', 'caveman-shrink', 'shutdown.js')
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

// spawn-options: upstream MCP child process spawn flags. Windows .cmd shims
// are resolved separately; all platforms keep shell mode disabled.

test('win32 keeps shell off so upstream args are never interpolated', () => {
  const opts = getSpawnOptions('win32');
  assert.equal(opts.shell, undefined);
  assert.equal(opts.windowsHide, true);
  assert.deepEqual(opts.stdio, ['pipe', 'pipe', 'inherit']);
});

test('linux keeps shell off to avoid argv quoting surprises', () => {
  const opts = getSpawnOptions('linux');
  assert.equal(opts.shell, undefined);
  assert.deepEqual(opts.stdio, ['pipe', 'pipe', 'inherit']);
});

test('darwin keeps shell off', () => {
  const opts = getSpawnOptions('darwin');
  assert.equal(opts.shell, undefined);
});

test('defaults to current platform when no arg passed', () => {
  const opts = getSpawnOptions();
  assert.equal(opts.shell, undefined);
  assert.equal(opts.windowsHide, true);
  assert.deepEqual(opts.stdio, ['pipe', 'pipe', 'inherit']);
});

// shutdown: the proxy owns the upstream it spawned, so a host stopping us has
// to take the upstream with it rather than orphaning it (#742).

// Minimal stand-ins so these tests never spawn a real process or wait out a
// real grace period.
function fakeChild() {
  const calls = [];
  return {
    pid: 4321,
    exitCode: null,
    signalCode: null,
    kill(signal) { calls.push(signal); return true; },
    killed: calls,
  };
}

function fakeProcess() {
  const handlers = new Map();
  const exits = [];
  return {
    on(sig, fn) { handlers.set(sig, fn); },
    exit(code) { exits.push(code); },
    emit(sig) { const fn = handlers.get(sig); if (fn) fn(); },
    handlers,
    exits,
  };
}

test('forwards a termination signal to the upstream (#742)', () => {
  const child = fakeChild();
  const proc = fakeProcess();
  installShutdownHandlers(child, { process: proc, setTimeout: () => null });

  proc.emit('SIGTERM');
  assert.deepStrictEqual(child.killed, ['SIGTERM']);
});

test('forwards every signal a host might use to stop us', () => {
  const proc = fakeProcess();
  installShutdownHandlers(fakeChild(), { process: proc, setTimeout: () => null });

  assert.deepStrictEqual([...proc.handlers.keys()].sort(), [...FORWARDED_SIGNALS].sort());
  // SIGKILL cannot be trapped; registering for it would be a silent no-op.
  assert.ok(!proc.handlers.has('SIGKILL'));
});

test('shutdown is idempotent so a second signal does not re-kill', () => {
  const child = fakeChild();
  const proc = fakeProcess();
  const { isShuttingDown } = installShutdownHandlers(child, {
    process: proc,
    setTimeout: () => null,
  });

  proc.emit('SIGINT');
  proc.emit('SIGTERM');
  assert.deepStrictEqual(child.killed, ['SIGINT']);
  assert.equal(isShuttingDown(), true);
});

test('does not signal an upstream that already exited', () => {
  const child = fakeChild();
  child.exitCode = 0;
  assert.equal(killUpstream(child, 'SIGTERM'), false);
  assert.deepStrictEqual(child.killed, []);
});

test('a kill that throws is reported rather than crashing teardown', () => {
  const child = fakeChild();
  child.kill = () => { throw new Error('ESRCH'); };
  assert.equal(killUpstream(child, 'SIGTERM'), false);
});

test('exits with the conventional code when the upstream ignores the signal', () => {
  for (const [signal, code] of [['SIGTERM', 143], ['SIGINT', 130]]) {
    const proc = fakeProcess();
    let onGrace;
    installShutdownHandlers(fakeChild(), {
      process: proc,
      graceMs: 0,
      setTimeout: fn => { onGrace = fn; return null; },
    });

    proc.emit(signal);
    onGrace();
    assert.deepStrictEqual(proc.exits, [code], `${signal} should exit ${code}`);
  }
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

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
