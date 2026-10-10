#!/usr/bin/env node
// Tests for repo-local config resolution in getDefaultMode().
// Covers the resolution-order contract:
//   env CAVEMAN_DEFAULT_MODE → repo-local (.caveman/config.json or .caveman.json,
//   walking up to filesystem root) → user config → 'caveman'.
// Legacy level names (lite/full/ultra/wenyan*) in any source resolve to the
// skill they became.
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

const { getDefaultMode, findRepoConfigPath } = require('../src/hooks/caveman-config');

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

test('returns "caveman" when no env, no repo config, no user config', (tmp) => {
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'caveman');
});

test('reads .caveman/config.json in cwd', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'),
    JSON.stringify({ defaultMode: 'lite' }));
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'caveman');
});

test('reads .caveman.json in cwd', (tmp) => {
  fs.writeFileSync(path.join(tmp, '.caveman.json'),
    JSON.stringify({ defaultMode: 'ultra' }));
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'ultracave');
});

test('.caveman/config.json wins over .caveman.json at same level', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'),
    JSON.stringify({ defaultMode: 'lite' }));
  fs.writeFileSync(path.join(tmp, '.caveman.json'),
    JSON.stringify({ defaultMode: 'ultra' }));
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'caveman');
});

test('walks up from nested cwd to find repo config', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'),
    JSON.stringify({ defaultMode: 'wenyan-lite' }));
  const nested = path.join(tmp, 'a', 'b', 'c');
  fs.mkdirSync(nested, { recursive: true });
  process.chdir(nested);
  assert.strictEqual(getDefaultMode(), 'megacave');
});

test('env var beats repo-local config', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'),
    JSON.stringify({ defaultMode: 'lite' }));
  process.chdir(tmp);
  process.env.CAVEMAN_DEFAULT_MODE = 'ultra';
  assert.strictEqual(getDefaultMode(), 'ultracave');
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
    assert.strictEqual(getDefaultMode(), 'caveman');
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

test('invalid mode in repo config falls through to default', (tmp) => {
  fs.writeFileSync(path.join(tmp, '.caveman.json'),
    JSON.stringify({ defaultMode: 'definitely-not-a-mode' }));
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'caveman');
});

test('malformed JSON in repo config falls through to default', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'), '{ not json');
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'caveman');
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
  assert.strictEqual(getDefaultMode(), 'caveman');
});

test('findRepoConfigPath returns null outside any repo', (tmp) => {
  process.chdir(tmp);
  assert.strictEqual(findRepoConfigPath(tmp), null);
});

// ── #634: optional startDir param (backward compatible) ────────────────────

test('getDefaultMode(startDir) resolves repo config for a directory other than process.cwd()', (tmp) => {
  fs.writeFileSync(path.join(tmp, '.caveman.json'), JSON.stringify({ defaultMode: 'off' }));
  const elsewhere = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-elsewhere-'));
  try {
    process.chdir(elsewhere); // process cwd has no repo config
    assert.strictEqual(getDefaultMode(), 'caveman', 'process cwd alone should not see the other dir\'s config');
    assert.strictEqual(getDefaultMode(tmp), 'off', 'startDir should resolve that directory\'s repo config');
  } finally {
    // Leave the directory before removing it — Windows EBUSYs on rmdir of cwd.
    process.chdir(tmp);
    fs.rmSync(elsewhere, { recursive: true, force: true });
  }
});

test('getDefaultMode() with no args is unchanged (defaults to process.cwd())', (tmp) => {
  fs.writeFileSync(path.join(tmp, '.caveman.json'), JSON.stringify({ defaultMode: 'lite' }));
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'caveman');
  assert.strictEqual(getDefaultMode(undefined), 'caveman');
});

test('new mode ids are accepted from every source', (tmp) => {
  fs.writeFileSync(path.join(tmp, '.caveman.json'), JSON.stringify({ defaultMode: 'megacave' }));
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'megacave');
  process.env.CAVEMAN_DEFAULT_MODE = 'ultracave';
  assert.strictEqual(getDefaultMode(), 'ultracave');
  process.env.CAVEMAN_DEFAULT_MODE = 'manual';
  assert.strictEqual(getDefaultMode(), 'manual', 'manual stays a default-policy value');
});

// C37: hand-edited config files. PowerShell 5.1's `>` writes UTF-16LE with a
// BOM, `Set-Content -Encoding UTF8` a UTF-8 BOM; comments and trailing commas
// are what installer/lib/settings.js already tolerates in settings.json.
test('a BOM, UTF-16LE, a comment or a trailing comma does not drop defaultMode', (tmp) => {
  const json = '{"defaultMode":"off"}';
  const variants = {
    'UTF-8 BOM': Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from(json)]),
    'UTF-16LE BOM': Buffer.concat([Buffer.from([0xff, 0xfe]), Buffer.from(json, 'utf16le')]),
    'line comment': '// mine\n{"defaultMode":"off"} // trailing',
    'block comment': '/* mine */ {"defaultMode": /* why */ "off"}',
    'trailing comma': '{\n  "defaultMode": "off",\n}',
    'comment-like string': '{"note": "http://x // y,}", "defaultMode": "off",}',
  };
  fs.mkdirSync(path.join(tmpHome, 'caveman'), { recursive: true });
  process.chdir(tmp);
  try {
    for (const [name, content] of Object.entries(variants)) {
      fs.writeFileSync(path.join(tmpHome, 'caveman', 'config.json'), content);
      assert.strictEqual(getDefaultMode(), 'off', `user config with ${name}`);
      fs.writeFileSync(path.join(tmp, '.caveman.json'), content);
      assert.strictEqual(getDefaultMode(), 'off', `.caveman.json with ${name}`);
      fs.rmSync(path.join(tmp, '.caveman.json'));
    }
  } finally {
    fs.rmSync(path.join(tmpHome, 'caveman'), { recursive: true, force: true });
  }
});

// C38: the docs give ~/.config/caveman/config.json for every platform. Windows
// reads %APPDATA%\caveman\config.json first, then that path.
test('Windows reads %APPDATA% then ~/.config for the user config', (tmp) => {
  const saved = { XDG_CONFIG_HOME: process.env.XDG_CONFIG_HOME, APPDATA: process.env.APPDATA,
    HOME: process.env.HOME, USERPROFILE: process.env.USERPROFILE };
  const platform = Object.getOwnPropertyDescriptor(process, 'platform');
  delete process.env.XDG_CONFIG_HOME;
  process.env.APPDATA = path.join(tmp, 'appdata');
  process.env.HOME = process.env.USERPROFILE = path.join(tmp, 'home');
  Object.defineProperty(process, 'platform', { value: 'win32' });
  try {
    process.chdir(tmp);
    fs.mkdirSync(path.join(tmp, 'home', '.config', 'caveman'), { recursive: true });
    fs.writeFileSync(path.join(tmp, 'home', '.config', 'caveman', 'config.json'), '{"defaultMode":"off"}');
    assert.strictEqual(getDefaultMode(), 'off', '~/.config is read when %APPDATA% has none');
    fs.mkdirSync(path.join(tmp, 'appdata', 'caveman'), { recursive: true });
    fs.writeFileSync(path.join(tmp, 'appdata', 'caveman', 'config.json'), '{"defaultMode":"ultra"}');
    assert.strictEqual(getDefaultMode(), 'ultracave', '%APPDATA% wins when both name a mode');
  } finally {
    Object.defineProperty(process, 'platform', platform);
    for (const [k, v] of Object.entries(saved)) {
      if (v === undefined) delete process.env[k]; else process.env[k] = v;
    }
  }
});

// C39: .caveman/config.json is also the CLI's project overlay, so it often
// holds no defaultMode. It must not hide a sibling .caveman.json or a parent's.
test('a .caveman/config.json without defaultMode does not hide .caveman.json', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.caveman'));
  fs.writeFileSync(path.join(tmp, '.caveman', 'config.json'), JSON.stringify({ execute: { mcp: false } }));
  fs.writeFileSync(path.join(tmp, '.caveman.json'), JSON.stringify({ defaultMode: 'off' }));
  const nested = path.join(tmp, 'sub');
  fs.mkdirSync(path.join(nested, '.caveman'), { recursive: true });
  fs.writeFileSync(path.join(nested, '.caveman', 'config.json'), '{}');
  process.chdir(tmp);
  assert.strictEqual(getDefaultMode(), 'off', 'sibling .caveman.json');
  assert.strictEqual(getDefaultMode(nested), 'off', 'parent directory');
});

console.log(`\n${passed} passed, ${failed} failed`);
fs.rmSync(tmpHome, { recursive: true, force: true });
process.exit(failed === 0 ? 0 : 1);
