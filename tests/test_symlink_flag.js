#!/usr/bin/env node
// Tests for safeWriteFlag / readFlag behavior with symlinked parent directories.
// Covers fix for issue #207: safeWriteFlag refuses flag writes when ~/.claude
// is a symlink.
//
// Run: node tests/test_symlink_flag.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');

const { safeWriteFlag, readFlag, VALID_MODES, normalizeMode } = require('../src/hooks/caveman-config');

let passed = 0;
let failed = 0;

function test(name, fn) {
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-symlink-test-'));
  try {
    fn(tmpBase);
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}`);
    console.error(`    ${e.message}`);
  } finally {
    fs.rmSync(tmpBase, { recursive: true, force: true });
  }
}

console.log('safeWriteFlag + readFlag symlink tests\n');

// ---------- safeWriteFlag ----------

test('writes flag in normal (non-symlinked) directory', (tmp) => {
  const flagDir = path.join(tmp, 'claude-config');
  fs.mkdirSync(flagDir, { recursive: true });
  const flagPath = path.join(flagDir, '.caveman-active');

  safeWriteFlag(flagPath, 'full');

  assert.strictEqual(fs.readFileSync(flagPath, 'utf8'), 'full');
});

test('writes flag when parent directory is a symlink owned by current user', (tmp) => {
  // Create real directory and symlink to it (simulating ~/.claude -> /real/path)
  const realDir = path.join(tmp, 'real-claude-config');
  fs.mkdirSync(realDir, { recursive: true });
  const symlinkDir = path.join(tmp, 'claude-symlink');
  fs.symlinkSync(realDir, symlinkDir);

  const flagPath = path.join(symlinkDir, '.caveman-active');
  safeWriteFlag(flagPath, 'ultra');

  // Flag should exist in the real directory
  const realFlagPath = path.join(realDir, '.caveman-active');
  assert.strictEqual(fs.existsSync(realFlagPath), true, 'flag file should exist in resolved dir');
  assert.strictEqual(fs.readFileSync(realFlagPath, 'utf8'), 'ultra');
});

test('readFlag works through symlinked parent directory', (tmp) => {
  const realDir = path.join(tmp, 'real-claude-config');
  fs.mkdirSync(realDir, { recursive: true });
  const symlinkDir = path.join(tmp, 'claude-symlink');
  fs.symlinkSync(realDir, symlinkDir);

  // Write directly to real path, then read through symlink path
  const realFlagPath = path.join(realDir, '.caveman-active');
  fs.writeFileSync(realFlagPath, 'lite', { mode: 0o600 });

  const result = readFlag(path.join(symlinkDir, '.caveman-active'));
  assert.strictEqual(result, 'lite');
});

test('safeWriteFlag then readFlag round-trip through symlink', (tmp) => {
  const realDir = path.join(tmp, 'real-config');
  fs.mkdirSync(realDir, { recursive: true });
  const symlinkDir = path.join(tmp, 'link-config');
  fs.symlinkSync(realDir, symlinkDir);

  const flagPath = path.join(symlinkDir, '.caveman-active');
  safeWriteFlag(flagPath, 'wenyan-ultra');

  // Read back through the same symlink path
  const result = readFlag(flagPath);
  assert.strictEqual(result, 'wenyan-ultra');
});

test('refuses flag file that is itself a symlink (even through symlinked parent)', (tmp) => {
  const realDir = path.join(tmp, 'real-config');
  fs.mkdirSync(realDir, { recursive: true });
  const symlinkDir = path.join(tmp, 'link-config');
  fs.symlinkSync(realDir, symlinkDir);

  // Create a symlink at the flag file location pointing to some other file
  const decoyFile = path.join(tmp, 'decoy.txt');
  fs.writeFileSync(decoyFile, 'ATTACK');
  const realFlagPath = path.join(realDir, '.caveman-active');
  fs.symlinkSync(decoyFile, realFlagPath);

  // safeWriteFlag should refuse (flag file is a symlink)
  safeWriteFlag(path.join(symlinkDir, '.caveman-active'), 'full');
  // The decoy should NOT have been overwritten
  assert.strictEqual(fs.readFileSync(decoyFile, 'utf8'), 'ATTACK');
});

test('readFlag refuses flag file that is a symlink', (tmp) => {
  const realDir = path.join(tmp, 'real-config');
  fs.mkdirSync(realDir, { recursive: true });

  const secretFile = path.join(tmp, 'secret.txt');
  fs.writeFileSync(secretFile, 'SSH_PRIVATE_KEY_CONTENT');
  fs.symlinkSync(secretFile, path.join(realDir, '.caveman-active'));

  const result = readFlag(path.join(realDir, '.caveman-active'));
  assert.strictEqual(result, null, 'should refuse symlinked flag file');
});

test('flag file permissions are 0600 when written through symlink', (tmp) => {
  if (process.platform === 'win32') return; // skip on Windows

  const realDir = path.join(tmp, 'real-config');
  fs.mkdirSync(realDir, { recursive: true });
  const symlinkDir = path.join(tmp, 'link-config');
  fs.symlinkSync(realDir, symlinkDir);

  safeWriteFlag(path.join(symlinkDir, '.caveman-active'), 'full');

  const realFlagPath = path.join(realDir, '.caveman-active');
  const stat = fs.statSync(realFlagPath);
  const mode = stat.mode & 0o777;
  assert.strictEqual(mode, 0o600, `expected 0600, got 0${mode.toString(8)}`);
});

test('overwrites existing flag through symlinked parent', (tmp) => {
  const realDir = path.join(tmp, 'real-config');
  fs.mkdirSync(realDir, { recursive: true });
  const symlinkDir = path.join(tmp, 'link-config');
  fs.symlinkSync(realDir, symlinkDir);

  const flagPath = path.join(symlinkDir, '.caveman-active');

  safeWriteFlag(flagPath, 'lite');
  assert.strictEqual(readFlag(flagPath), 'lite');

  safeWriteFlag(flagPath, 'ultra');
  assert.strictEqual(readFlag(flagPath), 'ultra');
});

test('creates parent directory via mkdirSync even when it does not exist yet', (tmp) => {
  const flagDir = path.join(tmp, 'nonexistent', 'nested');
  const flagPath = path.join(flagDir, '.caveman-active');

  safeWriteFlag(flagPath, 'full');

  assert.strictEqual(fs.existsSync(flagPath), true);
  assert.strictEqual(fs.readFileSync(flagPath, 'utf8'), 'full');
});

test('symlink to nonexistent target silently fails', (tmp) => {
  const symlinkDir = path.join(tmp, 'broken-link');
  try {
    fs.symlinkSync('/nonexistent/path/that/does/not/exist', symlinkDir);
  } catch (e) {
    // Can't create symlink — skip
    return;
  }

  const flagPath = path.join(symlinkDir, '.caveman-active');
  // Should not throw
  safeWriteFlag(flagPath, 'full');
  // Flag should not exist (target doesn't exist)
  assert.strictEqual(fs.existsSync(path.join(symlinkDir, '.caveman-active')), false);
});

test('all valid modes round-trip through symlinked parent', (tmp) => {
  const realDir = path.join(tmp, 'real-config');
  fs.mkdirSync(realDir, { recursive: true });
  const symlinkDir = path.join(tmp, 'link-config');
  fs.symlinkSync(realDir, symlinkDir);

  const flagPath = path.join(symlinkDir, '.caveman-active');

  for (const mode of VALID_MODES) {
    safeWriteFlag(flagPath, mode);
    const read = readFlag(flagPath);
    // readFlag normalizes aliases ('wenyan' → 'wenyan-full') so every
    // reader sees the canonical level name.
    assert.strictEqual(read, normalizeMode(mode), `mode '${mode}' did not round-trip`);
  }
});

// ---------- Source code audit ----------

test('safeWriteFlag no longer has blanket symlink parent refusal', (tmp) => {
  // Verify the old pattern "if (fs.lstatSync(flagDir).isSymbolicLink()) return;"
  // without ownership check is no longer present
  const source = fs.readFileSync(
    path.join(__dirname, '..', 'src', 'hooks', 'caveman-config.js'), 'utf8'
  );

  // The old pattern: check isSymbolicLink on flagDir and immediately return
  // New pattern: check isSymbolicLink, then realpathSync + ownership verification
  const lines = source.split('\n');
  let foundSymlinkCheck = false;
  let foundOwnershipCheck = false;
  for (const line of lines) {
    if (line.includes('isSymbolicLink()') && line.includes('flagDir')) {
      // This is the lstat check on the parent dir — should NOT be a blanket return
      foundSymlinkCheck = true;
    }
    if (line.includes('realpathSync') || line.includes('getuid') || line.includes('normalizedHome')) {
      foundOwnershipCheck = true;
    }
  }

  assert.ok(foundOwnershipCheck, 'safeWriteFlag should include ownership/home-dir verification');
});

// ---------- opencode plugin: stale caveman-config.cjs guard ----------
//
// bin/install.js copies src/hooks/caveman-config.js next to plugin.js as
// caveman-config.cjs. A user who upgrades caveman without re-running the
// installer keeps an OLD copy — one that can predate classifyPrompt. The
// plugin used to destructure it blindly and threw
// "TypeError: classifyPrompt is not a function" on every chat message.

const { pathToFileURL } = require('url');

const PLUGIN_SRC = path.join(__dirname, '..', 'src', 'plugins', 'opencode', 'plugin.js');
const CONFIG_SRC = path.join(__dirname, '..', 'src', 'hooks', 'caveman-config.js');

// Build tmp/plugins/caveman/{package.json,plugin.js,caveman-config.cjs} plus
// (optionally) the dev-fallback copy at tmp/hooks/caveman-config.js — the
// exact relative layout plugin.js probes.
function scaffoldPlugin(tmp, { staleInstalled, withDevFallback }) {
  const pluginDir = path.join(tmp, 'plugins', 'caveman');
  fs.mkdirSync(pluginDir, { recursive: true });
  fs.writeFileSync(path.join(pluginDir, 'package.json'), JSON.stringify({ type: 'module' }));
  fs.copyFileSync(PLUGIN_SRC, path.join(pluginDir, 'plugin.js'));

  const configSrc = fs.readFileSync(CONFIG_SRC, 'utf8');
  const stale = configSrc + '\ndelete module.exports.classifyPrompt;\n';
  fs.writeFileSync(
    path.join(pluginDir, 'caveman-config.cjs'),
    staleInstalled ? stale : configSrc
  );

  if (withDevFallback) {
    const hooksDir = path.join(tmp, 'hooks');
    fs.mkdirSync(hooksDir, { recursive: true });
    fs.writeFileSync(path.join(hooksDir, 'caveman-config.js'), configSrc);
    fs.writeFileSync(path.join(hooksDir, 'package.json'), JSON.stringify({ type: 'commonjs' }));
  }

  return path.join(pluginDir, 'plugin.js');
}

async function asyncTest(name, fn) {
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-plugin-test-'));
  const savedXdg = process.env.XDG_CONFIG_HOME;
  const savedDefault = process.env.CAVEMAN_DEFAULT_MODE;
  try {
    // Isolate both opencode's config dir and caveman's user config dir.
    process.env.XDG_CONFIG_HOME = tmpBase;
    delete process.env.CAVEMAN_DEFAULT_MODE;
    await fn(tmpBase);
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}`);
    console.error(`    ${e.message}`);
  } finally {
    if (savedXdg === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = savedXdg;
    if (savedDefault === undefined) delete process.env.CAVEMAN_DEFAULT_MODE;
    else process.env.CAVEMAN_DEFAULT_MODE = savedDefault;
    fs.rmSync(tmpBase, { recursive: true, force: true });
  }
}

function send(hooks, text) {
  return hooks['chat.message'](null, { parts: [{ type: 'text', text }] });
}

async function main() {
  console.log('\nopencode plugin: stale caveman-config.cjs guard\n');

  await asyncTest('stale installed config without classifyPrompt does not crash chat.message', async (tmp) => {
    const entry = scaffoldPlugin(tmp, { staleInstalled: true, withDevFallback: false });
    const mod = await import(pathToFileURL(entry).href);
    const hooks = await mod.CavemanPlugin({});
    const flagPath = path.join(tmp, 'opencode', '.caveman-active');

    // Session start still armed the flag at the default level.
    assert.strictEqual(readFlag(flagPath), 'full', 'session start should write the flag');

    // No throw, and no mode change: the NL classifier is simply inert.
    await send(hooks, 'stop caveman');
    assert.strictEqual(readFlag(flagPath), 'full', 'NL off must be inert without classifyPrompt');
    await send(hooks, 'talk like a caveman');
    assert.strictEqual(readFlag(flagPath), 'full', 'NL on must be inert without classifyPrompt');
  });

  await asyncTest('slash commands still work with a stale installed config', async (tmp) => {
    const entry = scaffoldPlugin(tmp, { staleInstalled: true, withDevFallback: false });
    const mod = await import(pathToFileURL(entry).href);
    const hooks = await mod.CavemanPlugin({});
    const flagPath = path.join(tmp, 'opencode', '.caveman-active');

    await send(hooks, '/caveman ultra');
    assert.strictEqual(readFlag(flagPath), 'ultra');
    await send(hooks, '/caveman off');
    assert.strictEqual(readFlag(flagPath), null);
  });

  await asyncTest('falls back to the dev config copy when the installed one is stale', async (tmp) => {
    const entry = scaffoldPlugin(tmp, { staleInstalled: true, withDevFallback: true });
    const mod = await import(pathToFileURL(entry).href);
    const hooks = await mod.CavemanPlugin({});
    const flagPath = path.join(tmp, 'opencode', '.caveman-active');

    // classifyPrompt recovered from the dev copy — NL toggles live again.
    await send(hooks, 'stop caveman');
    assert.strictEqual(readFlag(flagPath), null, 'NL off should work via the dev fallback');
    await send(hooks, 'talk like a caveman');
    assert.strictEqual(readFlag(flagPath), 'full', 'NL on should work via the dev fallback');
  });

  await asyncTest('classifier matrix reaches the plugin path (activate / deactivate / neither)', async (tmp) => {
    const entry = scaffoldPlugin(tmp, { staleInstalled: false, withDevFallback: false });
    const mod = await import(pathToFileURL(entry).href);
    const hooks = await mod.CavemanPlugin({});
    const flagPath = path.join(tmp, 'opencode', '.caveman-active');

    const activate = [
      'activate caveman', 'turn on caveman mode', 'talk like caveman',
      'use caveman instead of normal mode', 'switch to caveman mode, not normal mode',
      'can you talk like a caveman?', 'could you use caveman mode?'
    ];
    const deactivate = [
      'stop caveman', 'disable caveman', 'deactivate caveman', 'normal mode',
      'can you stop caveman', 'can you switch back to normal mode? caveman is hard to read',
      'caveman is annoying, please turn it off', 'disable that caveman thing'
    ];
    const neither = [
      "don't be brief, explain everything in detail", 'no need to be brief',
      "don't turn off caveman", 'do not disable caveman',
      "please don't disable caveman when I paste code",
      'caveman is better than normal mode'
    ];

    for (const p of activate) {
      try { fs.unlinkSync(flagPath); } catch (e) {}
      await send(hooks, p);
      assert.strictEqual(readFlag(flagPath), 'full', `should ACTIVATE: ${p}`);
    }
    for (const p of deactivate) {
      safeWriteFlag(flagPath, 'ultra');
      await send(hooks, p);
      assert.strictEqual(readFlag(flagPath), null, `should DEACTIVATE: ${p}`);
    }
    for (const p of neither) {
      safeWriteFlag(flagPath, 'ultra');
      await send(hooks, p);
      assert.strictEqual(readFlag(flagPath), 'ultra', `should be NEITHER (stay ultra): ${p}`);
      try { fs.unlinkSync(flagPath); } catch (e) {}
      await send(hooks, p);
      assert.strictEqual(readFlag(flagPath), null, `should be NEITHER (stay off): ${p}`);
    }
  });

  // ---------- Summary ----------

  console.log(`\n${passed} passed, ${failed} failed`);
  if (failed > 0) process.exit(1);
}

main();
