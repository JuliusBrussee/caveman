#!/usr/bin/env node
// Tests for src/mcp-servers/caveman-shrink/spawn-options.js.
// Confirms that the upstream MCP child process gets shell:true on Windows
// (so npx and other .cmd shims resolve) and shell:false on POSIX.
// Run: node tests/test_mcp_shrink_spawn_options.js

const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname, '..');
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

console.log('mcp-shrink spawn-options tests\n');

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
process.exit(failed === 0 ? 0 : 1);
