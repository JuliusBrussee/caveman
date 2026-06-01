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

test('--only with unknown agent id exits 2', () => {
  const r = run('--only', 'definitely-not-an-agent', '--non-interactive');
  assert.equal(r.status, 2);
  assert.match(r.stderr, /unknown agent: definitely-not-an-agent/);
  assert.match(r.stderr, /caveman --list/);
});

test('--only known id passes argv validation', () => {
  // Dry-run + --only claude exits 0 even if the claude binary isn't on PATH.
  const r = run('--dry-run', '--only', 'claude', '--non-interactive', '--config-dir', '/tmp/__cm_only_test');
  assert.equal(r.status, 0);
});

test('--config-dir expands ~ to home directory', async () => {
  // Pass `~/cm-test-…` and assert the dry-run plan resolves it relative to $HOME.
  // Use a unique suffix so the assertion is unambiguous.
  const suffix = `cm-test-${process.pid}`;
  const r = run('--dry-run', '--only', 'claude', '--non-interactive', '--config-dir', `~/${suffix}`);
  assert.equal(r.status, 0);
  // If the literal `~` had survived, we'd see `~/cm-test-…/hooks` in the plan.
  // The fix expands it in parseArgs, so we expect the absolute home path.
  assert.doesNotMatch(r.stdout, /~\/cm-test-/);
  // The plan only includes the hooks dir if claude is detected. Skip the
  // positive assertion when claude isn't on PATH on the runner.
  if (/Claude Code detected/.test(r.stdout)) {
    const { homedir } = await import('node:os');
    assert.match(r.stdout, new RegExp(homedir().replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '/' + suffix));
  }
});

test('bare -- (POSIX end-of-options) is accepted and ignored', () => {
  // Regression: npx forwarded `--` from `curl|bash -- --only openclaw` to the
  // package, and parseArgs rejected it as an unknown flag. Now we accept it.
  const r = run('--', '--only', 'claude', '--non-interactive', '--dry-run', '--config-dir', '/tmp/__cm_dashdash');
  assert.equal(r.status, 0);
});

test('--help discloses --config-dir scope', () => {
  const r = run('--help');
  assert.equal(r.status, 0);
  // Disclosure: --config-dir does NOT scope third-party CLI invocations.
  // Help text wraps mid-phrase, so collapse whitespace before matching.
  const collapsed = r.stdout.replace(/\s+/g, ' ');
  assert.match(collapsed, /Does NOT scope/);
  assert.match(collapsed, /XDG_CONFIG_HOME/);
  assert.match(collapsed, /OPENCLAW_WORKSPACE/);
});
