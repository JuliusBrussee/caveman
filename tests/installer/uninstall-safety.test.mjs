// Uninstall must never leave settings.json pointing at hook scripts it deleted,
// and neither install nor uninstall may touch a hooks/package.json another
// plugin owns.
//
// Both are the same class of bug: bin/install.js treating shared, user-owned
// state in $CLAUDE_CONFIG_DIR/hooks as if caveman owned it outright.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'bin', 'install.js');

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-uninstall-safety-'));
}

// Drop every PATH entry holding a `claude`/`gemini` binary so the installer
// never reaches the user's real plugin or extension state.
function pathWithout(binNames) {
  const sep = process.platform === 'win32' ? ';' : ':';
  const exts = process.platform === 'win32' ? ['.exe', '.cmd', '.bat', ''] : [''];
  return (process.env.PATH || '')
    .split(sep)
    .filter(dir => {
      if (!dir) return false;
      for (const b of binNames) {
        for (const ext of exts) {
          try { if (fs.existsSync(path.join(dir, b + ext))) return false; } catch (_) {}
        }
      }
      return true;
    })
    .join(sep);
}

function fakeClaudeDir(root) {
  const dir = path.join(root, 'fake-bin');
  fs.mkdirSync(dir, { recursive: true });
  if (process.platform === 'win32') {
    fs.writeFileSync(path.join(dir, 'claude.cmd'), '@echo off\r\nexit /b 0\r\n');
  } else {
    const file = path.join(dir, 'claude');
    fs.writeFileSync(file, '#!/bin/sh\nexit 0\n');
    fs.chmodSync(file, 0o755);
  }
  return dir;
}

function isolatedEnv(root) {
  const home = path.join(root, 'home');
  const sep = process.platform === 'win32' ? ';' : ':';
  return {
    HOME: home,
    USERPROFILE: home,
    XDG_CONFIG_HOME: path.join(home, '.config'),
    HERMES_HOME: path.join(home, '.hermes'),
    OPENCLAW_WORKSPACE: path.join(home, '.openclaw', 'workspace'),
    PATH: `${fakeClaudeDir(root)}${sep}${pathWithout(['claude', 'gemini'])}`,
  };
}

function runInstaller(args, configDir, extraEnv) {
  return spawnSync(process.execPath, [INSTALLER, ...args, '--config-dir', configDir, '--non-interactive', '--no-mcp-shrink'], {
    env: { ...process.env, CLAUDE_CONFIG_DIR: configDir, NO_COLOR: '1', ...extraEnv },
    encoding: 'utf8',
  });
}

// A settings.json the JSONC-tolerant reader still cannot parse, so readSettings
// returns null and the hook-removal block is skipped entirely.
const UNPARSEABLE = '{ "hooks": { "SessionStart": [ , ] }';

test('uninstall keeps the hook files when settings.json cannot be updated', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const env = isolatedEnv(dir);
  try {
    const installed = runInstaller(['--only', 'claude', '--with-hooks'], configDir, env);
    assert.equal(installed.status, 0, installed.stderr || installed.stdout);
    const activate = path.join(configDir, 'hooks', 'caveman-activate.js');
    assert.ok(fs.existsSync(activate), 'setup: the hook was never installed');

    fs.writeFileSync(path.join(configDir, 'settings.json'), UNPARSEABLE);
    const removed = runInstaller(['--uninstall'], configDir, env);

    // Deleting the scripts here strands the entries settings.json still holds:
    // Claude Code then dies with `Cannot find module …caveman-activate.js` on
    // every session start (#471).
    assert.ok(fs.existsSync(activate), 'uninstall deleted a hook settings.json may still reference');
    assert.notEqual(removed.status, 0, 'a cleanup that could not finish must not exit 0');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("install and uninstall leave another plugin's hooks/package.json alone", () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const env = isolatedEnv(dir);
  const foreign = '{\n  "type": "module",\n  "name": "some-other-plugin"\n}\n';
  try {
    const hooks = path.join(configDir, 'hooks');
    fs.mkdirSync(hooks, { recursive: true });
    const manifest = path.join(hooks, 'package.json');
    fs.writeFileSync(manifest, foreign);

    const installed = runInstaller(['--only', 'claude', '--with-hooks'], configDir, env);
    assert.equal(installed.status, 0, installed.stderr || installed.stdout);
    assert.equal(fs.readFileSync(manifest, 'utf8'), foreign, 'install overwrote a foreign hooks/package.json');

    runInstaller(['--uninstall'], configDir, env);
    assert.equal(fs.readFileSync(manifest, 'utf8'), foreign, 'uninstall deleted a foreign hooks/package.json');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
