// OMP native install. `installOmp` requires repoRoot (same contract as
// opencode/hermes) and branches on whether that repoRoot is a durable git
// working tree:
//   - A real clone or worktree (`.git` present, e.g. a local clone or `bash
//     install.sh` run from inside one): build packages/pi-extension once
//     (skipped once already built) and hand OMP a direct path — `omp plugin
//     install <path>` symlinks it in, so the checkout stays the source of
//     truth.
//   - No `.git` (the documented curl|bash path, which always resolves to
//     `npx -y github:JuliusBrussee/caveman#<ref>` — npm unpacks that into its
//     `_npx` cache with no `.git`): install the published `@caveman-ai/pi`
//     extension by name instead. A symlink into `_npx` would eventually
//     dangle when npm reuses or prunes that cache dir.

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
  if (process.env.OMP_LIST_INVALID_SHAPE === '1') { console.log(JSON.stringify({ plugins: [] })); process.exit(0); }
  const entry = { name: '${OMP_PACKAGE_NAME}' };
  if (process.env.OMP_LIST_PATH) entry.path = process.env.OMP_LIST_PATH;
  console.log(JSON.stringify({ npm: process.env.OMP_NOT_REGISTERED === '1' ? [] : [entry] }));
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

// A repoRoot with the marker dirs detectRepoRoot() requires but no `.git` —
// simulates the `npx -y github:...` extraction the curl|bash path always
// hits (npm packs a git dependency with no `.git` directory). Not backed by
// a real packages/pi-extension; the non-clone branch never reads it.
function installerWithoutGitCheckout() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-nogit-'));
  const binDir = path.join(dir, 'bin');
  fs.cpSync(path.join(REPO_ROOT, 'bin'), binDir, { recursive: true });
  fs.mkdirSync(path.join(dir, 'src', 'hooks'), { recursive: true });
  fs.mkdirSync(path.join(dir, 'agents'), { recursive: true });
  fs.mkdirSync(path.join(dir, 'skills'), { recursive: true });
  return path.join(binDir, 'install.js');
}

// A fake installed-package directory for the registry-install branch's
// manifest check. `valid: true` declares a real `omp.extensions` entry that
// exists on disk (mirrors a future OMP-capable @caveman-ai/pi release);
// `valid: false` declares only `pi.extensions` (mirrors the real published
// 0.1.1, which OMP still "installs" successfully but never loads as an OMP
// extension).
function fixtureOmpPackage(home, { valid }) {
  const dir = path.join(home, 'fake-registry-pkg');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'index.js'), 'module.exports = {};\n');
  const manifest = valid
    ? { name: OMP_PACKAGE_NAME, omp: { extensions: ['./index.js'] } }
    : { name: OMP_PACKAGE_NAME, pi: { extensions: ['./index.js'] } };
  fs.writeFileSync(path.join(dir, 'package.json'), JSON.stringify(manifest));
  return dir;
}

// A controlled local-clone fixture with its own tiny build script (counts
// builds to a file instead of running the real tsc/esbuild pipeline) — lets
// the skip-when-built and --force-rebuilds contracts be asserted directly,
// instead of depending on whether this checkout's own packages/pi-extension
// already happens to be built.
function fixtureLocalRepo() {
  const repoRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-localbuild-'));
  const binDir = path.join(repoRoot, 'bin');
  fs.cpSync(path.join(REPO_ROOT, 'bin'), binDir, { recursive: true });
  fs.mkdirSync(path.join(repoRoot, 'src', 'hooks'), { recursive: true });
  fs.mkdirSync(path.join(repoRoot, 'agents'), { recursive: true });
  fs.mkdirSync(path.join(repoRoot, 'skills'), { recursive: true });
  fs.mkdirSync(path.join(repoRoot, '.git'), { recursive: true });
  const pkgDir = path.join(repoRoot, 'packages', 'pi-extension');
  fs.mkdirSync(pkgDir, { recursive: true });
  const buildScript = "const fs=require('fs');fs.mkdirSync('dist',{recursive:true});"
    + "fs.writeFileSync('dist/omp.mjs','');"
    + "const n=fs.existsSync('build-count.txt')?Number(fs.readFileSync('build-count.txt','utf8'))+1:1;"
    + "fs.writeFileSync('build-count.txt',String(n));";
  fs.writeFileSync(path.join(pkgDir, 'package.json'), JSON.stringify({
    name: 'fixture-pi-extension',
    version: '0.0.0',
    omp: { extensions: ['./dist/omp.mjs'] },
    scripts: { build: `node -e ${JSON.stringify(buildScript)}` },
  }));
  return path.join(binDir, 'install.js');
}

function fixtureBuildCount(installer) {
  const f = path.join(path.dirname(path.dirname(installer)), 'packages', 'pi-extension', 'build-count.txt');
  return fs.existsSync(f) ? Number(fs.readFileSync(f, 'utf8')) : 0;
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

test('omp install builds packages/pi-extension once, then skips the build on a second install', () => {
  const installer = fixtureLocalRepo();
  const home1 = freshHome();
  try {
    const r1 = runInstaller(installer, ['--only', 'omp'], home1);
    assert.equal(r1.status, 0, r1.stdout + r1.stderr);
    assert.equal(fixtureBuildCount(installer), 1);

    const home2 = freshHome();
    try {
      const r2 = runInstaller(installer, ['--only', 'omp'], home2);
      assert.equal(r2.status, 0, r2.stdout + r2.stderr);
      assert.equal(fixtureBuildCount(installer), 1, 'second install must not rebuild an already-built extension');
    } finally { fs.rmSync(home2, { recursive: true, force: true }); }
  } finally {
    fs.rmSync(home1, { recursive: true, force: true });
    fs.rmSync(path.dirname(path.dirname(installer)), { recursive: true, force: true });
  }
});

test('omp install --force rebuilds packages/pi-extension even when already built', () => {
  const installer = fixtureLocalRepo();
  const home1 = freshHome();
  try {
    const r1 = runInstaller(installer, ['--only', 'omp'], home1);
    assert.equal(r1.status, 0, r1.stdout + r1.stderr);
    assert.equal(fixtureBuildCount(installer), 1);

    const home2 = freshHome();
    try {
      const r2 = runInstaller(installer, ['--only', 'omp', '--force'], home2);
      assert.equal(r2.status, 0, r2.stdout + r2.stderr);
      assert.equal(fixtureBuildCount(installer), 2, '--force must rebuild even when dist/ already carries a loadable entry');
    } finally { fs.rmSync(home2, { recursive: true, force: true }); }
  } finally {
    fs.rmSync(home1, { recursive: true, force: true });
    fs.rmSync(path.dirname(path.dirname(installer)), { recursive: true, force: true });
  }
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

test('omp install without a git checkout installs the published extension by name instead of symlinking the extraction dir', () => {
  const installer = installerWithoutGitCheckout();
  const home = freshHome();
  try {
    const pkgDir = fixtureOmpPackage(home, { valid: true });
    const r = runInstaller(installer, ['--only', 'omp'], home, { OMP_LIST_PATH: pkgDir });
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.deepEqual(argLog(home), [`plugin install ${OMP_PACKAGE_NAME}`, 'plugin list --json']);
    assert.match(r.stdout, /installed:\s*\n\s*• omp/);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
    fs.rmSync(path.dirname(path.dirname(installer)), { recursive: true, force: true });
  }
});

test('omp install without a git checkout fails honestly when the published package has no loadable OMP extension yet', () => {
  const installer = installerWithoutGitCheckout();
  const home = freshHome();
  try {
    const pkgDir = fixtureOmpPackage(home, { valid: false });
    const r = runInstaller(installer, ['--only', 'omp'], home, { OMP_LIST_PATH: pkgDir });
    assert.equal(r.status, 1, r.stdout + r.stderr);
    assert.match(r.stdout + r.stderr, /does not declare a loadable OMP extension yet/);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
    fs.rmSync(path.dirname(path.dirname(installer)), { recursive: true, force: true });
  }
});

test('omp dry run without a git checkout announces the registry install, not a local build', () => {
  const installer = installerWithoutGitCheckout();
  const home = freshHome();
  try {
    const r = runInstaller(installer, ['--only', 'omp', '--dry-run'], home);
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.equal(fs.existsSync(path.join(home, OMP_ARGS_LOG)), false);
    assert.match(r.stdout, new RegExp(`would run: omp plugin install ${OMP_PACKAGE_NAME.replace('/', '\\/')}`));
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

test('omp uninstall proceeds (does not silently skip) when `omp plugin list --json` returns an unrecognized shape', () => {
  const home = freshHome();
  try {
    const r = runInstaller(INSTALLER, ['--uninstall'], home, { OMP_LIST_INVALID_SHAPE: '1' });
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
