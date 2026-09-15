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

// Drop every PATH entry holding a `claude`/`gemini`/`caveman` binary so the
// installer never reaches the user's real plugin, extension or native-agent
// state.
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

function isolatedEnv(root, extraBinDirs = []) {
  const home = path.join(root, 'home');
  const sep = process.platform === 'win32' ? ';' : ':';
  const bins = [fakeClaudeDir(root), ...extraBinDirs].join(sep);
  return {
    HOME: home,
    USERPROFILE: home,
    XDG_CONFIG_HOME: path.join(home, '.config'),
    HERMES_HOME: path.join(home, '.hermes'),
    OPENCLAW_WORKSPACE: path.join(home, '.openclaw', 'workspace'),
    PATH: `${bins}${sep}${pathWithout(['claude', 'gemini', 'caveman'])}`,
  };
}

// A fake `caveman` CLI that records each invocation (one argument per line,
// invocations separated by a blank line) into `record` and exits 0, the same
// shape `disableNativeAgent`'s real command uses for `caveman disable --all`.
function fakeCavemanDir(root, record) {
  const dir = path.join(root, 'fake-caveman-bin');
  fs.mkdirSync(dir, { recursive: true });
  if (process.platform === 'win32') {
    // install.js's own portableInvocation() refuses to launch a `.cmd` shim
    // via cmd.exe unless it recognizes it as an npm/pnpm-style Node shim
    // (a line calling node on a sibling .js/.cjs/.mjs file); it launches that
    // script directly with the running node binary instead. Match the same
    // shape the gemini fixture in gemini-install.test.mjs already uses.
    fs.writeFileSync(path.join(dir, 'caveman.js'),
      "const fs = require('node:fs');\n"
      + `fs.appendFileSync(${JSON.stringify(record)}, process.argv.slice(2).join('\\n') + '\\n\\n');\n`);
    fs.writeFileSync(path.join(dir, 'caveman.cmd'),
      '@echo off\r\n'
      + '"%~dp0\\node.exe" "%~dp0\\caveman.js" %*\r\n');
  } else {
    const file = path.join(dir, 'caveman');
    fs.writeFileSync(file,
      '#!/bin/sh\n'
      + `{ for a in "$@"; do echo "$a"; done; echo; } >> "${record}"\n`
      + 'exit 0\n');
    fs.chmodSync(file, 0o755);
  }
  return dir;
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

test('uninstall hands native agent integrations to `caveman disable --all` when the CLI is present', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const record = path.join(dir, 'caveman-record.txt');
  const env = isolatedEnv(dir, [fakeCavemanDir(dir, record)]);
  try {
    const installed = runInstaller(['--only', 'claude', '--with-hooks'], configDir, env);
    assert.equal(installed.status, 0, installed.stderr || installed.stdout);
    assert.ok(!fs.existsSync(record), 'install must not touch native agent integrations');

    const removed = runInstaller(['--uninstall'], configDir, env);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);

    const calls = fs.readFileSync(record, 'utf8').trim().split(/\n\s*\n/).filter(Boolean);
    assert.equal(calls.length, 1, `expected exactly one \`caveman\` invocation, got:\n${calls.join('\n---\n')}`);
    assert.deepEqual(calls[0].split('\n'), ['disable', '--all']);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('uninstall does not invoke `caveman` when it is not on PATH', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const env = isolatedEnv(dir);
  try {
    const installed = runInstaller(['--only', 'claude', '--with-hooks'], configDir, env);
    assert.equal(installed.status, 0, installed.stderr || installed.stdout);
    const removed = runInstaller(['--uninstall'], configDir, env);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// `~/.caveman/integrations/<agent>.json` is what `caveman enable <agent>` writes
// and what `caveman disable` removes, so its presence AFTER uninstall is exact
// evidence that a native route (ANTHROPIC_BASE_URL and friends) is still in the
// host's settings — never a false positive on a user's own base-URL export.
function seedIntegrationJournal(root, agent, route) {
  const dir = path.join(root, 'home', '.caveman', 'integrations');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, `${agent}.json`), JSON.stringify({
    agent, operations: [{ kind: `${agent}-settings`, owned: { route, assume_first_party: '1' }, previous_route: null }],
  }, null, 2));
  return dir;
}

test('uninstall says so when a native route survives it', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  // No `caveman` on PATH — the documented order is `--uninstall` first, but a
  // user who ran `npm uninstall -g @caveman-ai/cli` first lands exactly here,
  // and so does anyone whose `disable --all` failed. Without a word from the
  // installer they keep a dead ANTHROPIC_BASE_URL and the Remote Control
  // breakage of #947, with nothing pointing at the cause (#1040).
  const env = isolatedEnv(dir);
  try {
    assert.equal(runInstaller(['--only', 'claude', '--with-hooks'], configDir, env).status, 0);
    seedIntegrationJournal(dir, 'claude', 'http://127.0.0.1:8787/w/claude');

    const removed = runInstaller(['--uninstall'], configDir, env);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
    const output = `${removed.stdout}${removed.stderr}`;
    assert.match(output, /claude/);
    assert.match(output, /caveman disable --all/, 'name the command that withdraws the route');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('uninstall stays quiet when no native integration is journaled', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const env = isolatedEnv(dir);
  try {
    assert.equal(runInstaller(['--only', 'claude', '--with-hooks'], configDir, env).status, 0);
    const removed = runInstaller(['--uninstall'], configDir, env);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
    assert.doesNotMatch(`${removed.stdout}${removed.stderr}`, /caveman disable --all/);
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
