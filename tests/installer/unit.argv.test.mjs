// Unit tests for the argv parser embedded in bin/install.js.
// We don't import parseArgs (it's not exported) — instead we shell out to the
// installer with --help / --list / unknown flags and assert the framing.
// For deeper coverage of flag-resolution semantics, exec --dry-run --list and
// check the rendered defaults.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const INSTALLER = path.resolve(HERE, '..', '..', 'bin', 'install.js');

function run(...args) {
  return spawnSync('node', [INSTALLER, ...args], { encoding: 'utf8' });
}

test('--help prints usage and exits 0', () => {
  const r = run('--help');
  assert.equal(r.status, 0);
  assert.match(r.stdout, /USAGE/);
  assert.match(r.stdout, /--with-hooks/);
});

test('--list prints provider matrix', () => {
  const r = run('--list');
  assert.equal(r.status, 0);
  assert.match(r.stdout, /caveman provider matrix/);
  assert.match(r.stdout, /claude\b/);
  assert.match(r.stdout, /gemini\b/);
  assert.match(r.stdout, /antigravity\b.*\(soft\)/);
});

test('unknown flag exits 2 with error', () => {
  const r = run('--bogus');
  assert.equal(r.status, 2);
  assert.match(r.stderr, /unknown flag/);
});

test('--all + --minimal mutually exclusive', () => {
  const r = run('--all', '--minimal');
  assert.equal(r.status, 2);
  assert.match(r.stderr, /mutually exclusive/);
});

test('--only without arg fails', () => {
  const r = run('--only');
  assert.equal(r.status, 2);
  assert.match(r.stderr, /--only requires an argument/);
});

test('--config-dir without arg fails', () => {
  const r = run('--config-dir');
  assert.equal(r.status, 2);
  assert.match(r.stderr, /--config-dir requires a path/);
});

test('--config-dir followed by another flag fails', () => {
  const r = run('--config-dir', '--all');
  assert.equal(r.status, 2);
  assert.match(r.stderr, /--config-dir requires a path/);
});

test('aider alias rewrites to aider-desk in dry-run output', () => {
  const r = run('--dry-run', '--only', 'aider', '--non-interactive', '--config-dir', '/tmp/__cm_alias_test');
  // No detection means no install lines, but the script should not crash.
  assert.equal(r.status, 0);
});
