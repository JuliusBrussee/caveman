import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const INSTALLER = path.resolve(HERE, '..', '..', 'bin', 'install.js');
const IS_WIN = process.platform === 'win32';

function shimCopilot({
  listsCaveman = false,
  disabled = false,
  listStatus = 0,
  pluginHelpStatus = 0,
  uninstallStatus = 0,
} = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-copilot-shim-'));
  const marker = listsCaveman ? `  • caveman@_direct (v1.0.0)${disabled ? ' [disabled]' : ''}` : '';
  if (IS_WIN) {
    fs.writeFileSync(
      path.join(dir, 'copilot.js'),
      [
        "const args = process.argv.slice(2).join(' ');",
        `if (args === 'plugin --help') { console.log('Manage plugins'); process.exit(${pluginHelpStatus}); }`,
        `if (args === 'plugin list') { if (${JSON.stringify(marker)}) console.log(${JSON.stringify(marker)}); process.exit(${listStatus}); }`,
        `if (args === 'plugin uninstall caveman@_direct') process.exit(${uninstallStatus});`,
      ].join('\n'),
    );
    fs.writeFileSync(path.join(dir, 'copilot.cmd'),
      '@echo off\r\nnode "%~dp0\\copilot.js" %*\r\n');
  } else {
    const f = path.join(dir, 'copilot');
    fs.writeFileSync(
      f,
      `#!/bin/sh\nif [ "$1 $2" = "plugin --help" ]; then echo "Manage plugins"; exit ${pluginHelpStatus}; fi\nif [ "$1 $2" = "plugin list" ]; then [ -n "${marker}" ] && echo "${marker}"; exit ${listStatus}; fi\nif [ "$1 $2 $3" = "plugin uninstall caveman@_direct" ]; then exit ${uninstallStatus}; fi\nexit 0\n`,
    );
    fs.chmodSync(f, 0o755);
  }
  return dir;
}

function pathWith(prependDir) {
  return prependDir + (IS_WIN ? ';' : ':') + (process.env.PATH || '');
}

function isolatedPath(prependDir) {
  return IS_WIN ? prependDir : `${prependDir}:/usr/bin:/bin`;
}

function runInstaller(args, env) {
  return spawnSync(process.execPath, [INSTALLER, ...args, '--non-interactive', '--no-mcp-shrink'], {
    env, encoding: 'utf8',
  });
}

// Point config dirs at throwaways so an uninstall/real-install run never reads
// or touches the runner's real ~/.claude, ~/.config, etc.
function isolatedEnv(extra) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-copilot-home-'));
  return {
    env: {
      ...process.env,
      NO_COLOR: '1',
      HOME: home,
      USERPROFILE: home,
      CLAUDE_CONFIG_DIR: home,
      XDG_CONFIG_HOME: home,
      OPENCLAW_WORKSPACE: path.join(home, 'no-openclaw'),
      ...extra,
    },
    cleanup: () => fs.rmSync(home, { recursive: true, force: true }),
  };
}

test('copilot-cli provider row renders in --list', () => {
  const r = spawnSync(process.execPath, [INSTALLER, '--list'], {
    encoding: 'utf8', env: { ...process.env, NO_COLOR: '1' },
  });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /copilot-cli\s+GitHub Copilot CLI\s+copilot plugin install/);
});

test('copilot-cli is auto-detected when the plugin command is available', () => {
  const shim = shimCopilot();
  try {
    const r = runInstaller(['--dry-run'], { ...process.env, PATH: pathWith(shim), NO_COLOR: '1' });
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /GitHub Copilot CLI detected/);
  } finally {
    fs.rmSync(shim, { recursive: true, force: true });
  }
});

test('copilot-cli ignores an unrelated copilot executable', () => {
  const shim = shimCopilot({ pluginHelpStatus: 1 });
  try {
    const r = runInstaller(
      ['--dry-run', '--skip-skills'],
      { ...process.env, PATH: pathWith(shim), NO_COLOR: '1' },
    );
    assert.equal(r.status, 0, r.stderr);
    assert.doesNotMatch(r.stdout, /GitHub Copilot CLI detected/);
  } finally {
    fs.rmSync(shim, { recursive: true, force: true });
  }
});

test('dry-run --only copilot-cli prints the direct plugin install plan', () => {
  // --force skips the "already installed?" probe, so this path is deterministic
  // even with no `copilot` binary on the runner.
  const r = runInstaller(['--only', 'copilot-cli', '--force', '--dry-run'],
    { ...process.env, NO_COLOR: '1' });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /GitHub Copilot CLI detected/);
  assert.match(r.stdout, /would run: copilot plugin install JuliusBrussee\/caveman/);
  assert.doesNotMatch(r.stdout, /plugin marketplace add/);
});

test('copilot-cli reports success when `copilot plugin install` exits 0', () => {
  const shim = shimCopilot({ listsCaveman: false });
  const { env, cleanup } = isolatedEnv({ PATH: pathWith(shim) });
  try {
    const r = runInstaller(['--only', 'copilot-cli', '--force'], env);
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /GitHub Copilot CLI detected/);
    assert.match(r.stdout, /installed:[\s\S]*copilot-cli/);
  } finally {
    cleanup();
    fs.rmSync(shim, { recursive: true, force: true });
  }
});

test('copilot-cli install is idempotent when the plugin is already present', () => {
  const shim = shimCopilot({ listsCaveman: true });
  try {
    const r = runInstaller(['--only', 'copilot-cli', '--dry-run'],
      { ...process.env, PATH: pathWith(shim), NO_COLOR: '1' });
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /already installed/);
    assert.doesNotMatch(r.stdout, /would run: copilot plugin install/);
  } finally {
    fs.rmSync(shim, { recursive: true, force: true });
  }
});

test('copilot-cli enables an installed but disabled plugin', () => {
  const shim = shimCopilot({ listsCaveman: true, disabled: true });
  try {
    const r = runInstaller(['--only', 'copilot-cli', '--dry-run'],
      { ...process.env, PATH: pathWith(shim), NO_COLOR: '1' });
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /would run: copilot plugins enable caveman@_direct --plugin/);
    assert.doesNotMatch(r.stdout, /would run: copilot plugin install/);
  } finally {
    fs.rmSync(shim, { recursive: true, force: true });
  }
});

test('--with-init writes .github/copilot/settings.json to enable the plugin', () => {
  const shim = shimCopilot();
  const { env, cleanup } = isolatedEnv({ PATH: pathWith(shim) });
  const cwd = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-copilot-repo-'));
  try {
    const r = spawnSync('node',
      [INSTALLER, '--only', 'copilot-cli', '--with-init', '--force', '--non-interactive', '--no-mcp-shrink'],
      { cwd, env, encoding: 'utf8' });
    assert.equal(r.status, 0, r.stderr);
    const p = path.join(cwd, '.github', 'copilot', 'settings.json');
    assert.ok(fs.existsSync(p), `expected ${p} to be written`);
    const cfg = JSON.parse(fs.readFileSync(p, 'utf8'));
    assert.deepEqual(cfg.extraKnownMarketplaces.caveman.source,
      { source: 'github', repo: 'JuliusBrussee/caveman' });
    assert.equal(cfg.enabledPlugins['caveman@caveman'], true);
  } finally {
    cleanup();
    fs.rmSync(shim, { recursive: true, force: true });
    fs.rmSync(cwd, { recursive: true, force: true });
  }
});

test('uninstall removes the copilot-cli plugin when present', () => {
  const shim = shimCopilot({ listsCaveman: true });
  const { env, cleanup } = isolatedEnv({ PATH: pathWith(shim) });
  try {
    const r = runInstaller(['--uninstall', '--dry-run'], env);
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /would run: copilot plugin uninstall caveman@_direct/);
  } finally {
    cleanup();
    fs.rmSync(shim, { recursive: true, force: true });
  }
});

test('uninstall skips copilot-cli cleanly when the plugin is not installed', () => {
  const shim = shimCopilot({ listsCaveman: false });
  const { env, cleanup } = isolatedEnv({ PATH: pathWith(shim) });
  try {
    const r = runInstaller(['--uninstall', '--dry-run'], env);
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /copilot cli plugin not installed — skipping/);
    assert.doesNotMatch(r.stdout, /would run: copilot plugin uninstall/);
  } finally {
    cleanup();
    fs.rmSync(shim, { recursive: true, force: true });
  }
});

test('uninstall reports a Copilot plugin removal failure', () => {
  const shim = shimCopilot({ listsCaveman: true, uninstallStatus: 1 });
  const { env, cleanup } = isolatedEnv({ PATH: isolatedPath(shim) });
  try {
    const r = runInstaller(['--uninstall'], env);
    assert.equal(r.status, 1);
    assert.match(r.stderr, /could not remove copilot cli plugin/);
    assert.match(r.stderr, /uninstall incomplete/);
  } finally {
    cleanup();
    fs.rmSync(shim, { recursive: true, force: true });
  }
});

test('uninstall ignores an unrelated copilot executable', () => {
  const shim = shimCopilot({ pluginHelpStatus: 1 });
  const { env, cleanup } = isolatedEnv({ PATH: isolatedPath(shim) });
  try {
    const r = runInstaller(['--uninstall'], env);
    assert.equal(r.status, 0, r.stderr);
    assert.doesNotMatch(r.stderr, /could not inspect Copilot CLI plugins/);
  } finally {
    cleanup();
    fs.rmSync(shim, { recursive: true, force: true });
  }
});
