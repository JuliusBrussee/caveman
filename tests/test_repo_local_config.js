#!/usr/bin/env node
// Tests for repo-local config resolution in getDefaultMode().
// Covers the resolution-order contract:
//   env CAVEMAN_DEFAULT_MODE → repo-local (.caveman/config.json or .caveman.json,
//   walking up to filesystem root) → user config → 'full'.
//
// Run: node tests/test_repo_local_config.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');

// Isolate from the host's real user config: point XDG_CONFIG_HOME at a tmp dir
// before requiring the module so getConfigPath() never reads the developer's
// own ~/.config/caveman/config.json.
const tmpHome = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-userhome-'));
process.env.XDG_CONFIG_HOME = tmpHome;
delete process.env.CAVEMAN_DEFAULT_MODE;
const APPDATA_SEGMENT = /AppData/;
const CONFIG_FILE_NAME = 'config.json';
const WINDOWS_PLATFORM = 'win32';

const { getDefaultMode, findRepoConfigPath, getConfigPath } = require('../src/hooks/caveman-config');

let passed = 0;
let failed = 0;

function test(name, fn) {
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-repocfg-'));
  const origCwd = process.cwd();
  const origEnv = process.env.CAVEMAN_DEFAULT_MODE;
  try {
    fn(tmpBase);
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}`);
    console.error(`    ${e.message}`);
  } finally {
    process.chdir(origCwd);
    if (origEnv === undefined) delete process.env.CAVEMAN_DEFAULT_MODE;
    else process.env.CAVEMAN_DEFAULT_MODE = origEnv;
    fs.rmSync(tmpBase, { recursive: true, force: true });
  }
}

console.log('repo-local config resolution tests\n');

test('returns "full" when no env, no repo config, no user config', (tmp) => {
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'full');
});

test('reads .caveman/config.json in cwd', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'),
    JSON.stringify({ defaultMode: 'lite' }));
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'lite');
});

test('reads .caveman.json in cwd', (tmp) => {
  fs.writeFileSync(path.join(tmp, '.caveman.json'),
    JSON.stringify({ defaultMode: 'ultra' }));
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'ultra');
});

test('.caveman/config.json wins over .caveman.json at same level', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'),
    JSON.stringify({ defaultMode: 'lite' }));
  fs.writeFileSync(path.join(tmp, '.caveman.json'),
    JSON.stringify({ defaultMode: 'ultra' }));
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'lite');
});

test('walks up from nested cwd to find repo config', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'),
    JSON.stringify({ defaultMode: 'wenyan-lite' }));
  const nested = path.join(tmp, 'a', 'b', 'c');
  fs.mkdirSync(nested, { recursive: true });
  process.chdir(nested);
  assert.strictEqual(getDefaultMode(), 'wenyan-lite');
});

test('env var beats repo-local config', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'),
    JSON.stringify({ defaultMode: 'lite' }));
  process.chdir(tmp);
  process.env.CAVEMAN_DEFAULT_MODE = 'ultra';
  assert.strictEqual(getDefaultMode(), 'ultra');
});

test('repo-local config beats user config', (tmp) => {
  // user config at XDG_CONFIG_HOME points to 'commit'
  fs.mkdirSync(path.join(tmpHome, 'caveman'), { recursive: true });
  fs.writeFileSync(path.join(tmpHome, 'caveman', 'config.json'),
    JSON.stringify({ defaultMode: 'commit' }));
  // repo-local points to 'lite'
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'),
    JSON.stringify({ defaultMode: 'lite' }));
  process.chdir(tmp);
  try {
    assert.strictEqual(getDefaultMode(), 'lite');
  } finally {
    fs.rmSync(path.join(tmpHome, 'caveman'), { recursive: true, force: true });
  }
});

test('falls through to user config when repo config absent', (tmp) => {
  fs.mkdirSync(path.join(tmpHome, 'caveman'), { recursive: true });
  fs.writeFileSync(path.join(tmpHome, 'caveman', 'config.json'),
    JSON.stringify({ defaultMode: 'review' }));
  process.chdir(tmp);
  try {
    assert.strictEqual(getDefaultMode(), 'review');
  } finally {
    fs.rmSync(path.join(tmpHome, 'caveman'), { recursive: true, force: true });
  }
});

test('win32 without APPDATA falls back to ~/.config/caveman/config.json', (tmp) => {
  const origXdg = process.env.XDG_CONFIG_HOME;
  const origAppData = process.env.APPDATA;
  const origPlatform = Object.getOwnPropertyDescriptor(process, 'platform');
  const origHomedir = os.homedir;
  const posixConfigDir = path.join(tmp, '.config', 'caveman');
  try {
    delete process.env.XDG_CONFIG_HOME;
    delete process.env.APPDATA;
    Object.defineProperty(process, 'platform', { value: WINDOWS_PLATFORM });
    os.homedir = () => tmp;
    fs.mkdirSync(posixConfigDir, { recursive: true });
    fs.writeFileSync(path.join(posixConfigDir, CONFIG_FILE_NAME),
      JSON.stringify({ defaultMode: 'lite' }));

    assert.match(getConfigPath(), APPDATA_SEGMENT);
    assert.strictEqual(getDefaultMode(), 'lite');
  } finally {
    if (origXdg === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = origXdg;
    if (origAppData === undefined) delete process.env.APPDATA;
    else process.env.APPDATA = origAppData;
    Object.defineProperty(process, 'platform', origPlatform);
    os.homedir = origHomedir;
  }
});

test('invalid mode in repo config falls through to default', (tmp) => {
  fs.writeFileSync(path.join(tmp, '.caveman.json'),
    JSON.stringify({ defaultMode: 'definitely-not-a-mode' }));
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'full');
});

test('malformed JSON in repo config falls through to default', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'), '{ not json');
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'full');
});

test('refuses symlinked .caveman.json (symmetric with readFlag policy)', (tmp) => {
  const real = path.join(tmp, 'real-config.json');
  fs.writeFileSync(real, JSON.stringify({ defaultMode: 'ultra' }));
  try {
    fs.symlinkSync(real, path.join(tmp, '.caveman.json'));
  } catch (e) {
    // Skip on platforms without symlink perms
    console.log('    (skipped: symlink not permitted)');
    return;
  }
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'full');
});

test('findRepoConfigPath returns null outside any repo', (tmp) => {
  process.chdir(tmp);
  assert.strictEqual(findRepoConfigPath(tmp), null);
});

console.log(`\n${passed} passed, ${failed} failed`);
fs.rmSync(tmpHome, { recursive: true, force: true });
process.exit(failed === 0 ? 0 : 1);
