// CodeBuddy Code installer — dry-run, fresh install, uninstall, --list.
//
// Detection of codebuddy is gated behind `command -v codebuddy`, so we prepend
// a tmpdir with a no-op `codebuddy` shim to PATH. The shim also handles
// `plugin list` (returns empty) and `plugin marketplace add` / `plugin install`
// (returns 0) to satisfy the installer's plugin flow.
//
// Run: node --test tests/installer/codebuddy.test.mjs

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'bin', 'install.js');
const requireCjs = createRequire(import.meta.url);
const SETTINGS = requireCjs(path.join(REPO_ROOT, 'bin', 'lib', 'settings.js'));

const IS_WIN = process.platform === 'win32';

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-codebuddy-'));
}

// Create a `codebuddy` shim that accepts any subcommand and returns 0.
// For `plugin list` it outputs nothing (simulates no plugins installed).
function shimCodebuddy() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-cb-shim-'));
  if (IS_WIN) {
    fs.writeFileSync(path.join(dir, 'codebuddy.cmd'), '@echo off\r\nexit /b 0\r\n');
  } else {
    const f = path.join(dir, 'codebuddy');
    fs.writeFileSync(f, '#!/bin/sh\nexit 0\n');
    fs.chmodSync(f, 0o755);
  }
  return dir;
}

function pathWith(prependDir) {
  const sep = IS_WIN ? ';' : ':';
  return prependDir + sep + (process.env.PATH || '');
}

function runInstaller(args, env) {
  return spawnSync('node', [INSTALLER, ...args, '--non-interactive'], {
    env, encoding: 'utf8',
  });
}

// ── 1. --list includes codebuddy ─────────────────────────────────────────
test('--list shows codebuddy in provider matrix', () => {
  const r = spawnSync('node', [INSTALLER, '--list'], {
    env: { ...process.env, NO_COLOR: '1' },
    encoding: 'utf8',
  });
  assert.equal(r.status, 0);
  assert.match(r.stdout, /codebuddy/);
  assert.match(r.stdout, /CodeBuddy Code/);
  assert.match(r.stdout, /codebuddy plugin install/);
});

// ── 2. Dry-run outputs correct planned actions ───────────────────────────
test('dry-run --only codebuddy shows codebuddy-specific actions', () => {
  const shimDir = shimCodebuddy();
  try {
    const r = runInstaller(['--only', 'codebuddy', '--dry-run', '--no-mcp-shrink'], {
      ...process.env,
      PATH: pathWith(shimDir),
      NO_COLOR: '1',
    });
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);
    assert.match(r.stdout, /CodeBuddy Code detected/);
    assert.match(r.stdout, /would run: codebuddy plugin marketplace add/);
    assert.match(r.stdout, /would run: codebuddy plugin install caveman@caveman/);
    assert.match(r.stdout, /\.codebuddy\/hooks/);
    assert.match(r.stdout, /\.codebuddy\/settings\.json/);
  } finally {
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 3. Fresh install populates hooks and settings.json ───────────────────
test('fresh install writes hooks + settings.json to CODEBUDDY_CONFIG_DIR', () => {
  const cbDir = freshTmpDir();
  const shimDir = shimCodebuddy();
  try {
    const r = runInstaller(['--only', 'codebuddy', '--with-hooks', '--no-mcp-shrink'], {
      ...process.env,
      CODEBUDDY_CONFIG_DIR: cbDir,
      PATH: pathWith(shimDir),
      NO_COLOR: '1',
    });
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);

    // Hook files should be installed
    const hooksDir = path.join(cbDir, 'hooks');
    assert.ok(fs.existsSync(hooksDir), 'hooks dir should exist');
    assert.ok(fs.existsSync(path.join(hooksDir, 'caveman-activate.js')), 'activate.js missing');
    assert.ok(fs.existsSync(path.join(hooksDir, 'caveman-mode-tracker.js')), 'mode-tracker.js missing');
    assert.ok(fs.existsSync(path.join(hooksDir, 'caveman-config.js')), 'config.js missing');
    assert.ok(fs.existsSync(path.join(hooksDir, 'caveman-statusline.sh')), 'statusline.sh missing');

    // settings.json should contain hook entries
    const settingsPath = path.join(cbDir, 'settings.json');
    assert.ok(fs.existsSync(settingsPath), 'settings.json should be created');
    const settings = SETTINGS.readSettings(settingsPath);
    assert.ok(settings, 'settings.json should be parseable');
    assert.ok(SETTINGS.hasCavemanHook(settings, 'SessionStart', 'caveman-activate'),
      'SessionStart hook missing');
    assert.ok(SETTINGS.hasCavemanHook(settings, 'UserPromptSubmit', 'caveman-mode-tracker'),
      'UserPromptSubmit hook missing');
  } finally {
    fs.rmSync(cbDir, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 4. Idempotency: install twice, hooks still correct ───────────────────
test('re-running install is idempotent (hooks not duplicated)', () => {
  const cbDir = freshTmpDir();
  const shimDir = shimCodebuddy();
  try {
    const env = {
      ...process.env,
      CODEBUDDY_CONFIG_DIR: cbDir,
      PATH: pathWith(shimDir),
      NO_COLOR: '1',
    };
    // First install
    runInstaller(['--only', 'codebuddy', '--with-hooks', '--no-mcp-shrink'], env);
    // Second install
    runInstaller(['--only', 'codebuddy', '--with-hooks', '--no-mcp-shrink', '--force'], env);

    const settingsPath = path.join(cbDir, 'settings.json');
    const settings = SETTINGS.readSettings(settingsPath);
    // Count SessionStart hooks — should be exactly 1 caveman entry
    const ssHooks = (settings.hooks?.SessionStart || [])
      .flatMap(g => g.hooks || [])
      .filter(h => h.command && h.command.includes('caveman-activate'));
    assert.equal(ssHooks.length, 1, `expected 1 activate hook, got ${ssHooks.length}`);
  } finally {
    fs.rmSync(cbDir, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 5. Uninstall cleans up CodeBuddy hooks ───────────────────────────────
test('uninstall removes codebuddy hooks and settings entries', () => {
  const cbDir = freshTmpDir();
  const shimDir = shimCodebuddy();
  try {
    const env = {
      ...process.env,
      CODEBUDDY_CONFIG_DIR: cbDir,
      // Also set CLAUDE_CONFIG_DIR to a non-existent dir so uninstall
      // doesn't try to touch the real ~/.claude
      CLAUDE_CONFIG_DIR: path.join(cbDir, 'fake-claude'),
      PATH: pathWith(shimDir),
      NO_COLOR: '1',
    };
    // Install first
    runInstaller(['--only', 'codebuddy', '--with-hooks', '--no-mcp-shrink'], env);
    assert.ok(fs.existsSync(path.join(cbDir, 'settings.json')), 'pre-condition: settings exists');

    // Now uninstall
    runInstaller(['--uninstall'], env);

    // settings.json should have hooks removed
    const settingsPath = path.join(cbDir, 'settings.json');
    if (fs.existsSync(settingsPath)) {
      const settings = SETTINGS.readSettings(settingsPath);
      if (settings) {
        const hasActivate = SETTINGS.hasCavemanHook(settings, 'SessionStart', 'caveman-activate');
        assert.ok(!hasActivate, 'activate hook should be removed after uninstall');
      }
    }

    // Hook files should be deleted
    const hooksDir = path.join(cbDir, 'hooks');
    assert.ok(!fs.existsSync(path.join(hooksDir, 'caveman-activate.js')),
      'activate.js should be removed');
    assert.ok(!fs.existsSync(path.join(hooksDir, 'caveman-mode-tracker.js')),
      'mode-tracker.js should be removed');
  } finally {
    fs.rmSync(cbDir, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 6. Install does NOT touch Claude config dir ──────────────────────────
test('codebuddy install does not write to CLAUDE_CONFIG_DIR', () => {
  const cbDir = freshTmpDir();
  const claudeDir = freshTmpDir();
  const shimDir = shimCodebuddy();
  try {
    const r = runInstaller(['--only', 'codebuddy', '--with-hooks', '--no-mcp-shrink'], {
      ...process.env,
      CODEBUDDY_CONFIG_DIR: cbDir,
      CLAUDE_CONFIG_DIR: claudeDir,
      PATH: pathWith(shimDir),
      NO_COLOR: '1',
    });
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);

    // Claude dir should remain empty
    const claudeContents = fs.readdirSync(claudeDir);
    assert.equal(claudeContents.length, 0,
      `Claude dir should be untouched, but contains: ${claudeContents.join(', ')}`);
  } finally {
    fs.rmSync(cbDir, { recursive: true, force: true });
    fs.rmSync(claudeDir, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});
