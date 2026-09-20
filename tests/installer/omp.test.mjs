// OMP native install. Every real distribution path (local clone, curl|bash,
// `npx -y github:JuliusBrussee/caveman`, the Windows pwsh shim) runs
// bin/install.js from inside a full git checkout — caveman-installer is not
// published to the npm registry standalone, so there is no scenario where
// this installer runs without a local repo tree. installOmp therefore
// requires repoRoot (same contract as opencode/hermes) and always builds
// packages/pi-extension once (skipped once already built) and hands OMP a
// direct path — `omp plugin install <path>` symlinks it in, so the checkout
// stays the source of truth.

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
const LOCAL_PKG_DIR = path.join(REPO_ROOT, 'packages', 'pi-extension');
const OMP_ARGS_LOG = 'omp-args.log';
const OMP_SHIM_NAME = process.platform === 'win32' ? 'omp.cmd' : 'omp';
const OMP_SHIM_SCRIPT_NAME = 'omp-shim.js';
const OMP_PACKAGE_NAME = '@caveman-ai/pi';
const PATH_SEPARATOR = process.platform === 'win32' ? ';' : ':';

function freshHome() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-omp-'));
}

// The fake `omp` binary lives in a Node script both platforms share; only the
// launcher differs. On Windows the installer never executes a `.cmd` — it reads
// it, extracts the Node entrypoint (bin/lib/portable-process.js) and spawns node
// directly — so the shim must be shaped like the npm-generated cmd-shim a real
// `npm i -g oh-my-pi` produces. A plain batch script is rejected as a
// "non-Node Windows command shim" and never runs.
const OMP_SHIM_SCRIPT = `const fs = require('fs');
const path = require('path');
const args = process.argv.slice(2);
const home = process.env.HOME || process.env.USERPROFILE;
fs.appendFileSync(path.join(home, '${OMP_ARGS_LOG}'), args.join(' ') + '\\n');
if (process.env.OMP_FAIL_INSTALL === '1' && args[1] === 'install') process.exit(1);
if (process.env.OMP_FAIL_UNINSTALL === '1' && args[1] === 'uninstall') process.exit(1);
if (args[1] === 'list') {
  console.log(JSON.stringify({ npm: process.env.OMP_NOT_REGISTERED === '1' ? [] : [{ name: '${OMP_PACKAGE_NAME}' }] }));
  process.exit(0);
}
`;
const OMP_SHIM_BODY = process.platform === 'win32'
  ? `@echo off\r\nendLocal & "%_prog%" "%dp0%\\${OMP_SHIM_SCRIPT_NAME}" %*\r\n`
  : `#!/bin/sh\nexec ${JSON.stringify(process.execPath)} "$(dirname "$0")/${OMP_SHIM_SCRIPT_NAME}" "$@"\n`;

function shimOmp(home) {
  const bin = path.join(home, 'bin');
  fs.mkdirSync(bin, { recursive: true });
  fs.writeFileSync(path.join(bin, OMP_SHIM_SCRIPT_NAME), OMP_SHIM_SCRIPT);
  const shim = path.join(bin, OMP_SHIM_NAME);
  fs.writeFileSync(shim, OMP_SHIM_BODY);
  if (process.platform !== 'win32') fs.chmodSync(shim, 0o755);
  return bin;
}

function runInstaller(installer, args, home, extraEnv = {}, withOmp = true) {
  const bin = withOmp ? shimOmp(home) : path.dirname(process.execPath);
  return spawnSync(process.execPath, [installer, ...args, '--non-interactive', '--no-mcp-shrink'], {
    env: {
      ...process.env,
      ...extraEnv,
      HOME: home,
      USERPROFILE: home,
      XDG_CONFIG_HOME: path.join(home, '.config'),
      XDG_DATA_HOME: path.join(home, '.local', 'share'),
      APPDATA: path.join(home, 'AppData', 'Roaming'),
      CLAUDE_CONFIG_DIR: path.join(home, '.claude'),
      CAVEMAN_HOME: path.join(home, '.caveman'),
      PATH: withOmp ? bin + PATH_SEPARATOR + (process.env.PATH || '') : bin,
      NO_COLOR: '1',
    },
    encoding: 'utf8',
  });
}

function argLog(home) {
  return fs.readFileSync(path.join(home, OMP_ARGS_LOG), 'utf8').trim().split('\n').filter(Boolean);
}

// A standalone copy of bin/ with no src/hooks, agents/, or skills/ siblings —
// detectRepoRoot() finds nothing. Not a real distribution path (see file
// header) but exercises the same "requires a local clone" guard opencode and
// hermes already rely on.
function installerWithoutRepoRoot() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-noclone-'));
  const binDir = path.join(dir, 'bin');
  fs.cpSync(path.join(REPO_ROOT, 'bin'), binDir, { recursive: true });
  return path.join(binDir, 'install.js');
}

// Exercises the real buildLocalOmpExtension path against this checkout: if
// packages/pi-extension is not already built (a fresh clone/CI checkout),
// this triggers a real `npm install && npm run build` there — the same thing
// a real user's first `caveman install --only omp` from a clone would do.
test('omp fresh install from a local checkout builds and links the real package, not @caveman-ai/pi from npm', () => {
  const home = freshHome();
  try {
    const r = runInstaller(INSTALLER, ['--only', 'omp'], home);
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.deepEqual(argLog(home), [`plugin install ${LOCAL_PKG_DIR}`]);
    assert.match(r.stdout, /installed:\s*\n\s*• omp/);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('omp dry run from a local checkout never invokes the omp binary', () => {
  const home = freshHome();
  try {
    const r = runInstaller(INSTALLER, ['--only', 'omp', '--dry-run'], home);
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.equal(fs.existsSync(path.join(home, OMP_ARGS_LOG)), false);
    assert.match(r.stdout, /would build packages\/pi-extension and run: omp plugin install/);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('omp install failure (omp plugin install itself fails) is reported and fails the run', () => {
  const home = freshHome();
  try {
    const r = runInstaller(INSTALLER, ['--only', 'omp'], home, { OMP_FAIL_INSTALL: '1' });
    assert.equal(r.status, 1, r.stdout + r.stderr);
    assert.match(r.stdout + r.stderr, /omp plugin install failed/);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('omp install without a local clone fails cleanly instead of reaching the npm registry', () => {
  const installer = installerWithoutRepoRoot();
  const home = freshHome();
  try {
    const r = runInstaller(installer, ['--only', 'omp'], home);
    assert.equal(r.status, 1, r.stdout + r.stderr);
    assert.match(r.stdout + r.stderr, /requires a local clone/);
    assert.equal(fs.existsSync(path.join(home, OMP_ARGS_LOG)), false, 'must never invoke omp without a checkout to build from');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
    fs.rmSync(path.dirname(path.dirname(installer)), { recursive: true, force: true });
  }
});

test('omp dry run without a local clone fails cleanly', () => {
  const installer = installerWithoutRepoRoot();
  const home = freshHome();
  try {
    const r = runInstaller(installer, ['--only', 'omp', '--dry-run'], home);
    assert.equal(r.status, 1, r.stdout + r.stderr);
    assert.match(r.stdout + r.stderr, /requires a local clone/);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
    fs.rmSync(path.dirname(path.dirname(installer)), { recursive: true, force: true });
  }
});

test('omp uninstall checks registration then delegates to `omp plugin uninstall @caveman-ai/pi` by name', () => {
  const home = freshHome();
  try {
    const r = runInstaller(INSTALLER, ['--uninstall'], home);
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.deepEqual(argLog(home), ['plugin list --json', `plugin uninstall ${OMP_PACKAGE_NAME}`]);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('omp uninstall is a no-op (not a failure) when @caveman-ai/pi was never installed via OMP', () => {
  const home = freshHome();
  try {
    const r = runInstaller(INSTALLER, ['--uninstall'], home, { OMP_NOT_REGISTERED: '1' });
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.deepEqual(argLog(home), ['plugin list --json']);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('omp uninstall failure is reported and fails cleanup', () => {
  const home = freshHome();
  try {
    const r = runInstaller(INSTALLER, ['--uninstall'], home, { OMP_FAIL_UNINSTALL: '1' });
    assert.equal(r.status, 1, r.stdout + r.stderr);
    assert.match(r.stdout + r.stderr, /omp plugin uninstall failed/);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('omp uninstall is skipped (not failed) when the omp binary is unavailable', () => {
  const home = freshHome();
  try {
    const r = runInstaller(INSTALLER, ['--uninstall'], home, {}, false);
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.equal(fs.existsSync(path.join(home, OMP_ARGS_LOG)), false);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('omp dry-run uninstall never invokes the omp binary', () => {
  const home = freshHome();
  try {
    const r = runInstaller(INSTALLER, ['--uninstall', '--dry-run'], home);
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.equal(fs.existsSync(path.join(home, OMP_ARGS_LOG)), false);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});
