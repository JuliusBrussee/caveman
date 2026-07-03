// End-to-end: `caveman config` subcommand — CLI sugar over the config files
// src/hooks/caveman-config.js already reads. The killer assertion: a mode set
// via the CLI is honored by the real SessionStart hook.

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
const ACTIVATE = path.join(REPO_ROOT, 'src', 'hooks', 'caveman-activate.js');

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'cm-config-'));
}

function runConfig(args, { cwd, env = {} } = {}) {
  const base = { ...process.env, NO_COLOR: '1', ...env };
  delete base.CAVEMAN_DEFAULT_MODE; // hermetic: env short-circuits resolution
  return spawnSync(process.execPath, [INSTALLER, 'config', ...args], {
    cwd: cwd || REPO_ROOT, env: base, encoding: 'utf8',
  });
}

test('config set writes user config and the SessionStart hook honors it', () => {
  const dir = freshTmpDir();
  const xdg = path.join(dir, 'xdg');
  const home = path.join(dir, 'home');
  const claude = path.join(dir, 'claude');
  fs.mkdirSync(home, { recursive: true });
  const cwd = path.join(dir, 'work');
  fs.mkdirSync(cwd);
  const env = { XDG_CONFIG_HOME: xdg, APPDATA: xdg, HOME: home, USERPROFILE: home, CLAUDE_CONFIG_DIR: claude };
  try {
    const r = runConfig(['set', 'intensity', 'ultra'], { cwd, env });
    assert.equal(r.status, 0, r.stderr);
    const cfgPath = path.join(xdg, 'caveman', 'config.json');
    assert.equal(JSON.parse(fs.readFileSync(cfgPath, 'utf8')).defaultMode, 'ultra');

    const g = runConfig(['get', 'mode'], { cwd, env });
    assert.equal(g.stdout.trim(), 'ultra');

    // The real hook must pick it up: flag file content == configured mode.
    const hook = spawnSync(process.execPath, [ACTIVATE], {
      cwd, env: { ...process.env, ...env, CAVEMAN_DEFAULT_MODE: '' }, encoding: 'utf8',
    });
    assert.equal(hook.status, 0, hook.stderr);
    assert.equal(fs.readFileSync(path.join(claude, '.caveman-active'), 'utf8'), 'ultra');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('config set --project writes repo config; subdir updates the same file', () => {
  const dir = freshTmpDir();
  const xdg = path.join(dir, 'xdg');
  const env = { XDG_CONFIG_HOME: xdg, APPDATA: xdg };
  try {
    const r1 = runConfig(['set', 'mode', 'lite', '--project'], { cwd: dir, env });
    assert.equal(r1.status, 0, r1.stderr);
    const cfgPath = path.join(dir, '.caveman', 'config.json');
    assert.equal(JSON.parse(fs.readFileSync(cfgPath, 'utf8')).defaultMode, 'lite');

    // From a nested dir, --project must find and update the SAME file
    const sub = path.join(dir, 'a', 'b');
    fs.mkdirSync(sub, { recursive: true });
    const r2 = runConfig(['set', 'mode', 'wenyan-full', '--project'], { cwd: sub, env });
    assert.equal(r2.status, 0, r2.stderr);
    assert.equal(JSON.parse(fs.readFileSync(cfgPath, 'utf8')).defaultMode, 'wenyan-full');
    assert.equal(fs.existsSync(path.join(sub, '.caveman')), false, 'must not create a second config');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('config set rejects invalid mode and unknown key without writing', () => {
  const dir = freshTmpDir();
  const xdg = path.join(dir, 'xdg');
  const env = { XDG_CONFIG_HOME: xdg, APPDATA: xdg };
  try {
    const bad = runConfig(['set', 'mode', 'shouty'], { cwd: dir, env });
    assert.equal(bad.status, 2);
    assert.match(bad.stderr, /invalid mode/);
    const unk = runConfig(['set', 'volume', 'high'], { cwd: dir, env });
    assert.equal(unk.status, 2);
    assert.match(unk.stderr, /unknown key/);
    assert.equal(fs.existsSync(path.join(xdg, 'caveman', 'config.json')), false);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('config unset removes the key; show reports resolution', () => {
  const dir = freshTmpDir();
  const xdg = path.join(dir, 'xdg');
  const env = { XDG_CONFIG_HOME: xdg, APPDATA: xdg };
  try {
    runConfig(['set', 'mode', 'ultra'], { cwd: dir, env });
    const u = runConfig(['unset', 'mode'], { cwd: dir, env });
    assert.equal(u.status, 0, u.stderr);
    const cfgPath = path.join(xdg, 'caveman', 'config.json');
    assert.equal('defaultMode' in JSON.parse(fs.readFileSync(cfgPath, 'utf8')), false);

    const s = runConfig(['show'], { cwd: dir, env });
    assert.equal(s.status, 0, s.stderr);
    assert.match(s.stdout, /Effective mode: full/);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
