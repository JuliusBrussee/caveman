// opencode native install — fresh install, idempotency, uninstall, plugin smoke.
//
// Detection of opencode is gated behind `command -v opencode`, so to run on a
// CI box without opencode installed we prepend a tmpdir with a no-op `opencode`
// shim to PATH. The installer's per-provider dispatch only checks PATH; it
// never invokes the binary itself.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createRequire } from 'node:module';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'bin', 'install.js');
const requireCjs = createRequire(import.meta.url);
const SETTINGS = requireCjs(path.join(REPO_ROOT, 'bin', 'lib', 'settings.js'));

const IS_WIN = process.platform === 'win32';

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-opencode-'));
}

// Make a throwaway `opencode` binary on PATH so detectMatch('command:opencode')
// returns true. The shim never executes — installer only checks PATH presence.
function shimOpencode() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-shim-'));
  if (IS_WIN) {
    fs.writeFileSync(path.join(dir, 'opencode.cmd'), '@echo off\r\n');
  } else {
    const f = path.join(dir, 'opencode');
    fs.writeFileSync(f, '#!/bin/sh\nexit 0\n');
    fs.chmodSync(f, 0o755);
  }
  return dir;
}

function runInstaller(args, env) {
  return spawnSync('node', [INSTALLER, ...args, '--non-interactive', '--no-mcp-shrink'], {
    env, encoding: 'utf8',
  });
}

function pathWith(prependDir) {
  const sep = IS_WIN ? ';' : ':';
  return prependDir + sep + (process.env.PATH || '');
}

// ── 1. Fresh install populates expected files ────────────────────────────
test('opencode fresh install drops plugin, commands, agents, skills, AGENTS.md, opencode.json', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const r = runInstaller(['--only', 'opencode'], {
      ...process.env,
      XDG_CONFIG_HOME: xdg,
      PATH: pathWith(shimDir),
      NO_COLOR: '1',
    });
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);

    const ocDir = path.join(xdg, 'opencode');
    assert.ok(fs.existsSync(path.join(ocDir, 'plugins', 'caveman', 'plugin.js')), 'plugin.js missing');
    assert.ok(fs.existsSync(path.join(ocDir, 'plugins', 'caveman', 'package.json')), 'plugin package.json missing');
    assert.ok(fs.existsSync(path.join(ocDir, 'plugins', 'caveman', 'caveman-config.cjs')), 'caveman-config.cjs sibling missing');

    for (const f of ['caveman.md', 'caveman-commit.md', 'caveman-review.md', 'caveman-compress.md', 'caveman-stats.md', 'caveman-help.md']) {
      assert.ok(fs.existsSync(path.join(ocDir, 'commands', f)), `command ${f} missing`);
    }
    for (const f of ['cavecrew-investigator.md', 'cavecrew-builder.md', 'cavecrew-reviewer.md']) {
      assert.ok(fs.existsSync(path.join(ocDir, 'agents', f)), `agent ${f} missing`);
    }
    for (const name of ['caveman', 'caveman-commit', 'caveman-review', 'caveman-help', 'caveman-stats', 'caveman-compress', 'cavecrew']) {
      assert.ok(fs.existsSync(path.join(ocDir, 'skills', name, 'SKILL.md')), `skill ${name}/SKILL.md missing`);
    }
    assert.ok(fs.existsSync(path.join(ocDir, 'AGENTS.md')), 'AGENTS.md missing');
    assert.match(fs.readFileSync(path.join(ocDir, 'AGENTS.md'), 'utf8'), /Respond terse like smart caveman/);

    const cfgPath = path.join(ocDir, 'opencode.json');
    assert.ok(fs.existsSync(cfgPath), 'opencode.json missing');
    const cfg = JSON.parse(fs.readFileSync(cfgPath, 'utf8'));
    assert.ok(Array.isArray(cfg.plugin), 'opencode.json missing plugin array');
    assert.ok(cfg.plugin.includes('./plugins/caveman/plugin.js'), 'plugin entry missing');
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 2. Idempotency: install twice, plugin array stays length 1 ───────────
test('opencode idempotent install does not duplicate plugin entries', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
    const r1 = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r1.status, 2);
    const r2 = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r2.status, 2);

    const cfg = JSON.parse(fs.readFileSync(path.join(xdg, 'opencode', 'opencode.json'), 'utf8'));
    const matches = cfg.plugin.filter(p => p === './plugins/caveman/plugin.js');
    assert.equal(matches.length, 1, `expected 1 plugin entry, got ${matches.length}`);

    // AGENTS.md should not have the ruleset duplicated either.
    const agentsMd = fs.readFileSync(path.join(xdg, 'opencode', 'AGENTS.md'), 'utf8');
    const sentinelCount = (agentsMd.match(/Respond terse like smart caveman/g) || []).length;
    assert.equal(sentinelCount, 1, `expected 1 sentinel, got ${sentinelCount}`);
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 3. Tolerates JSONC opencode.json (#249-class regression guard) ───────
test('opencode install tolerates JSONC opencode.json (comments + trailing commas)', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const ocDir = path.join(xdg, 'opencode');
    fs.mkdirSync(ocDir, { recursive: true });
    fs.writeFileSync(path.join(ocDir, 'opencode.json'),
      `// hand-written
{
  /* user prefs */
  "model": "anthropic/claude-sonnet-4-5",
  "theme": "dark",
}
`);

    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
    const r = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r.status, 2);

    const cfg = JSON.parse(fs.readFileSync(path.join(ocDir, 'opencode.json'), 'utf8'));
    assert.equal(cfg.model, 'anthropic/claude-sonnet-4-5', 'user model setting wiped');
    assert.equal(cfg.theme, 'dark', 'user theme setting wiped');
    assert.ok(cfg.plugin.includes('./plugins/caveman/plugin.js'), 'plugin entry missing');
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 4. Uninstall removes opencode artifacts and prunes config ────────────
test('opencode uninstall removes plugin dir, command/agent/skill files, prunes opencode.json', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
    const r1 = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r1.status, 2);

    const r2 = runInstaller(['--uninstall'], env);
    assert.notEqual(r2.status, 2);

    const ocDir = path.join(xdg, 'opencode');
    assert.equal(fs.existsSync(path.join(ocDir, 'plugins', 'caveman')), false, 'plugin dir survived');
    assert.equal(fs.existsSync(path.join(ocDir, 'commands', 'caveman.md')), false, 'caveman.md command survived');
    assert.equal(fs.existsSync(path.join(ocDir, 'agents', 'cavecrew-builder.md')), false, 'cavecrew agent survived');
    assert.equal(fs.existsSync(path.join(ocDir, 'skills', 'caveman')), false, 'caveman skill dir survived');
    assert.equal(fs.existsSync(path.join(ocDir, 'AGENTS.md')), false, 'AGENTS.md (we wrote it) survived');

    if (fs.existsSync(path.join(ocDir, 'opencode.json'))) {
      const cfg = JSON.parse(fs.readFileSync(path.join(ocDir, 'opencode.json'), 'utf8'));
      const stillHasPlugin = Array.isArray(cfg.plugin) && cfg.plugin.includes('./plugins/caveman/plugin.js');
      assert.equal(stillHasPlugin, false, 'plugin entry survived in opencode.json');
    }
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 5. Plugin smoke: load installed plugin.js, fire fake hooks ────────────
test('opencode plugin handles /caveman ultra and stop caveman via tui.prompt.append', async () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
    const r = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r.status, 2);

    const pluginPath = path.join(xdg, 'opencode', 'plugins', 'caveman', 'plugin.js');
    const flagPath = path.join(xdg, 'opencode', '.caveman-active');

    // Set XDG_CONFIG_HOME for the plugin so flagPath resolves to our temp dir.
    process.env.XDG_CONFIG_HOME = xdg;

    const mod = await import(pathToFileURL(pluginPath).href);
    const factory = mod.default || mod.CavemanPlugin;
    const handlers = await factory({});

    // Slash command activates ultra
    const out1 = await handlers['tui.prompt.append']({ prompt: '/caveman ultra' });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'ultra');
    assert.ok(out1 && typeof out1.append === 'string', 'expected reinforcement append');
    assert.match(out1.append, /CAVEMAN MODE ACTIVE \(ultra\)/);

    // Natural-language deactivation removes flag
    const out2 = await handlers['tui.prompt.append']({ prompt: 'stop caveman please' });
    assert.equal(fs.existsSync(flagPath), false, 'flag should be deleted after deactivation');
    assert.equal(out2, undefined, 'no reinforcement when flag absent');

    // session.created writes default mode
    await handlers['session.created']();
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'full');
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});
