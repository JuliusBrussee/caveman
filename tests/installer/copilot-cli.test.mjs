// GitHub Copilot CLI native provider — detection, install, idempotency, uninstall.
//
// The `copilot-cli` provider is gated behind `command:copilot` and shells out to
// the standalone `@github/copilot` CLI (`copilot plugin marketplace add` +
// `copilot plugin install`). To run on a CI box without that CLI, we prepend a
// tmpdir with a throwaway `copilot` shim to PATH. The shim answers `plugin list`
// with a controllable marker so the idempotency/uninstall probes are
// deterministic; every other subcommand just exits 0.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const INSTALLER = path.resolve(HERE, '..', '..', 'bin', 'install.js');
const IS_WIN = process.platform === 'win32';

// Fake `copilot` binary on PATH. `listsCaveman` controls what `copilot plugin
// list` prints, which drives the "already installed?" probe in both install and
// uninstall; all other invocations exit 0 so the marketplace-add/install/
// uninstall commands succeed against the shim.
function shimCopilot({ listsCaveman = false } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-copilot-shim-'));
  const marker = listsCaveman ? 'caveman@caveman' : '';
  if (IS_WIN) {
    fs.writeFileSync(path.join(dir, 'copilot.cmd'),
      `@echo off\r\nif "%1 %2"=="plugin list" echo ${marker}\r\nexit /b 0\r\n`);
  } else {
    const f = path.join(dir, 'copilot');
    fs.writeFileSync(f, `#!/bin/sh\nif [ "$1 $2" = "plugin list" ]; then echo "${marker}"; fi\nexit 0\n`);
    fs.chmodSync(f, 0o755);
  }
  return dir;
}

function pathWith(prependDir) {
  return prependDir + (IS_WIN ? ';' : ':') + (process.env.PATH || '');
}

function runInstaller(args, env) {
  return spawnSync('node', [INSTALLER, ...args, '--non-interactive', '--no-mcp-shrink'], {
    env, encoding: 'utf8',
  });
}

// Point config dirs at throwaways so an uninstall/real-install run never reads
// or touches the runner's real ~/.claude, ~/.config, etc.
function isolatedEnv(extra) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-copilot-home-'));
  return {
    env: { ...process.env, NO_COLOR: '1', CLAUDE_CONFIG_DIR: home, XDG_CONFIG_HOME: home, ...extra },
    cleanup: () => fs.rmSync(home, { recursive: true, force: true }),
  };
}

// ── Detection ──────────────────────────────────────────────────────────────

test('copilot-cli provider row renders in --list', () => {
  const r = spawnSync('node', [INSTALLER, '--list'], {
    encoding: 'utf8', env: { ...process.env, NO_COLOR: '1' },
  });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /copilot-cli\s+GitHub Copilot CLI\s+copilot plugin install/);
});

test('copilot-cli is auto-detected when `copilot` is on PATH (command:copilot)', () => {
  const shim = shimCopilot();
  try {
    const r = runInstaller(['--dry-run'], { ...process.env, PATH: pathWith(shim), NO_COLOR: '1' });
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /GitHub Copilot CLI detected/);
  } finally {
    fs.rmSync(shim, { recursive: true, force: true });
  }
});

// ── Install ────────────────────────────────────────────────────────────────

test('dry-run --only copilot-cli prints the marketplace add + plugin install plan', () => {
  // --force skips the "already installed?" probe, so this path is deterministic
  // even with no `copilot` binary on the runner.
  const r = runInstaller(['--only', 'copilot-cli', '--force', '--dry-run'],
    { ...process.env, NO_COLOR: '1' });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /GitHub Copilot CLI detected/);
  assert.match(r.stdout, /would run: copilot plugin marketplace add JuliusBrussee\/caveman/);
  assert.match(r.stdout, /would run: copilot plugin install caveman@caveman/);
});

test('copilot-cli reports success when `copilot plugin install` exits 0', () => {
  // Real (non-dry) dispatch against the shim exercises the success branch that
  // pushes copilot-cli onto results.installed — the dry-run path can't, since it
  // always fakes status 0.
  const shim = shimCopilot({ listsCaveman: false });
  const { env, cleanup } = isolatedEnv({ PATH: pathWith(shim) });
  try {
    const r = runInstaller(['--only', 'copilot-cli', '--force'], env);
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /GitHub Copilot CLI detected/);
    assert.match(r.stdout, /installed:[\s\S]*copilot-cli/);
  } finally {
    cleanup();
    fs.rmSync(shim, { recursive: true, force: true });
  }
});

test('copilot-cli install is idempotent when the plugin is already present', () => {
  // Shim reports caveman in `plugin list`; without --force the probe should skip
  // the install rather than re-run it.
  const shim = shimCopilot({ listsCaveman: true });
  try {
    const r = runInstaller(['--only', 'copilot-cli', '--dry-run'],
      { ...process.env, PATH: pathWith(shim), NO_COLOR: '1' });
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /already installed/);
    assert.doesNotMatch(r.stdout, /would run: copilot plugin install caveman@caveman/);
  } finally {
    fs.rmSync(shim, { recursive: true, force: true });
  }
});

// ── Uninstall ────────────────────────────────────────────────────────────────

test('uninstall removes the copilot-cli plugin when present', () => {
  const shim = shimCopilot({ listsCaveman: true });
  const { env, cleanup } = isolatedEnv({ PATH: pathWith(shim) });
  try {
    const r = runInstaller(['--uninstall', '--dry-run'], env);
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /would run: copilot plugin uninstall caveman/);
  } finally {
    cleanup();
    fs.rmSync(shim, { recursive: true, force: true });
  }
});

test('uninstall skips copilot-cli cleanly when the plugin is not installed', () => {
  const shim = shimCopilot({ listsCaveman: false });
  const { env, cleanup } = isolatedEnv({ PATH: pathWith(shim) });
  try {
    const r = runInstaller(['--uninstall', '--dry-run'], env);
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /copilot cli plugin not installed — skipping/);
    assert.doesNotMatch(r.stdout, /would run: copilot plugin uninstall/);
  } finally {
    cleanup();
    fs.rmSync(shim, { recursive: true, force: true });
  }
});
