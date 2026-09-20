// OMP native install. Running from a local checkout (the common case: this
// installer already requires src/hooks, agents/, skills/ siblings for every
// other native provider) installs exactly what's in that checkout — build
// packages/pi-extension once, then `omp plugin install <path>` symlinks it
// in, so the checkout stays the source of truth. Without a local checkout,
// it falls back to `omp plugin install @caveman-ai/pi` from the npm
// registry and verifies the installed version actually declares a loadable
// OMP extension before reporting success — bin/install.js and
// packages/pi-extension release on separate schedules, so the registry
// version can predate this repo's OMP support.

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
//
// `install` mimics a real OMP plugin-manager install: it writes a package.json
// (and, unless OMP_NO_EXTENSION is set, a loadable dist/omp.mjs) under a fake
// plugins root, then `doctor --json` reports that root exactly like a real
// `omp plugin doctor` would — the same shape ompRegistryExtensionInstalled reads.
const OMP_SHIM_SCRIPT = `const fs = require('fs');
const path = require('path');
const args = process.argv.slice(2);
const home = process.env.HOME || process.env.USERPROFILE;
fs.appendFileSync(path.join(home, '${OMP_ARGS_LOG}'), args.join(' ') + '\\n');
if (process.env.OMP_FAIL_INSTALL === '1' && args[1] === 'install') process.exit(1);
if (process.env.OMP_FAIL_UNINSTALL === '1' && args[1] === 'uninstall') process.exit(1);
const pluginsRoot = path.join(home, 'omp-plugins');
if (args[1] === 'doctor') {
  const exists = fs.existsSync(pluginsRoot);
  console.log(JSON.stringify([{ name: 'plugins_directory', status: exists ? 'ok' : 'warning', message: exists ? 'Found at ' + pluginsRoot : 'Not created yet' }]));
  process.exit(0);
}
if (args[1] === 'list') {
  console.log(JSON.stringify({ npm: process.env.OMP_NOT_REGISTERED === '1' ? [] : [{ name: '${OMP_PACKAGE_NAME}' }] }));
  process.exit(0);
}
if (args[1] === 'install' && !path.isAbsolute(args[2])) {
  // Registry install (a bare package name, not a local path).
  const pkgDir = path.join(pluginsRoot, 'node_modules', '@caveman-ai', 'pi');
  fs.mkdirSync(pkgDir, { recursive: true });
  const pkg = { name: '${OMP_PACKAGE_NAME}', version: '0.1.1' };
  if (process.env.OMP_NO_EXTENSION !== '1') {
    pkg.omp = { extensions: ['./dist/omp.mjs'] };
    fs.mkdirSync(path.join(pkgDir, 'dist'), { recursive: true });
    fs.writeFileSync(path.join(pkgDir, 'dist', 'omp.mjs'), 'export default function(){};\\n');
  }
  fs.writeFileSync(path.join(pkgDir, 'package.json'), JSON.stringify(pkg));
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
// detectRepoRoot() finds nothing, so installOmp falls back to the npm
// registry path exactly like a detached curl-fallback install would.
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
test('omp fresh install from a local checkout builds/links the real package.json, not @caveman-ai/pi from npm', () => {
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

// ── Without a local clone: falls back to the npm registry ──────────────────

test('omp fresh install without a local clone falls back to `omp plugin install @caveman-ai/pi` and verifies the extension', () => {
  const installer = installerWithoutRepoRoot();
  const home = freshHome();
  try {
    const r = runInstaller(installer, ['--only', 'omp'], home);
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.deepEqual(argLog(home), [`plugin install ${OMP_PACKAGE_NAME}`, 'plugin doctor --json']);
    assert.match(r.stdout, /installed:\s*\n\s*• omp/);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
    fs.rmSync(path.dirname(path.dirname(installer)), { recursive: true, force: true });
  }
});

test('omp install without a local clone of a pre-OMP-release package fails honestly instead of claiming success', () => {
  const installer = installerWithoutRepoRoot();
  const home = freshHome();
  try {
    const r = runInstaller(installer, ['--only', 'omp'], home, { OMP_NO_EXTENSION: '1' });
    assert.equal(r.status, 1, r.stdout + r.stderr);
    assert.match(r.stdout + r.stderr, /predates OMP support/);
    // The npm install itself still ran and reported success upstream —
    // only caveman's own verification catches the gap.
    assert.deepEqual(argLog(home), [`plugin install ${OMP_PACKAGE_NAME}`, 'plugin doctor --json']);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
    fs.rmSync(path.dirname(path.dirname(installer)), { recursive: true, force: true });
  }
});

test('omp dry run without a local clone never invokes the omp binary', () => {
  const installer = installerWithoutRepoRoot();
  const home = freshHome();
  try {
    const r = runInstaller(installer, ['--only', 'omp', '--dry-run'], home);
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.equal(fs.existsSync(path.join(home, OMP_ARGS_LOG)), false);
    assert.match(r.stdout, new RegExp(`would run: omp plugin install ${OMP_PACKAGE_NAME.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`));
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
    fs.rmSync(path.dirname(path.dirname(installer)), { recursive: true, force: true });
  }
});
