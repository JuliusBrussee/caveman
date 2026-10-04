// OMP native plugin install — prepares a real local OMP plugin package, then
// routes through `omp plugin install <path>` so OMP owns lifecycle state.

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
const OMP_PLUGIN_DIR = path.join('.omp', 'caveman-plugin');
const OMP_ARGS_LOG = 'omp-args.log';
const OMP_SHIM_NAME = process.platform === 'win32' ? 'omp.cmd' : 'omp';
const OMP_SHIM_SCRIPT_NAME = 'omp-shim.js';
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
const hostRoot = path.join(home, 'omp-host-plugins');
const link = path.join(hostRoot, 'node_modules', 'caveman');
const registration = path.join(home, 'omp-registration.json');
if (args[1] === 'doctor') {
  console.log(JSON.stringify([{ name: 'plugins_directory', status: fs.existsSync(hostRoot) ? 'ok' : 'warning', message: fs.existsSync(hostRoot) ? 'Found at ' + hostRoot : 'Not created yet' }]));
  process.exit(0);
}
if (args[1] === 'list') {
  if (process.env.OMP_INVALID_LIST === '1') { console.log('{}'); process.exit(0); }
  console.log(JSON.stringify({ npm: fs.existsSync(registration) ? [{ name: 'caveman', path: link }] : [], marketplace: process.env.OMP_MARKETPLACE_CONFLICT === '1' ? [{ id: 'caveman@example' }] : [] }));
  process.exit(0);
}
fs.appendFileSync(path.join(home, '${OMP_ARGS_LOG}'), args.join(' ') + '\\n');
if (process.env.OMP_FAIL_INSTALL === '1' && args[1] === 'install') process.exit(1);
if (process.env.OMP_FAIL_UNINSTALL === '1' && args[1] === 'uninstall') process.exit(1);
if (args[1] === 'install') {
  fs.mkdirSync(path.dirname(link), { recursive: true });
  fs.rmSync(link, { recursive: true, force: true });
  fs.symlinkSync(args[2], link, process.platform === 'win32' ? 'junction' : 'dir');
  fs.writeFileSync(registration, JSON.stringify({ source: args[2] }));
  if (process.env.OMP_PARTIAL_INSTALL === '1') process.exit(1);
}
if (args[1] === 'uninstall') {
  fs.rmSync(link, { recursive: true, force: true });
  fs.rmSync(registration, { force: true });
}
`;
const OMP_SHIM_BODY = process.platform === 'win32'
  ? `@echo off\r\nendLocal & "%_prog%" "%dp0%\\${OMP_SHIM_SCRIPT_NAME}" %*\r\n`
  : `#!/bin/sh\nexec ${JSON.stringify(process.execPath)} "$(dirname "$0")/${OMP_SHIM_SCRIPT_NAME}" "$@"\n`;
const PATH_SEPARATOR = process.platform === 'win32' ? ';' : ':';
const OMP_SKILLS = ['caveman', 'caveman-commit', 'caveman-review', 'caveman-help', 'caveman-stats', 'caveman-compress', 'cavecrew'];
const OMP_COMMANDS = ['caveman.md', 'caveman-commit.md', 'caveman-review.md', 'caveman-compress.md', 'caveman-stats.md', 'caveman-help.md'];
const OMP_AGENTS = ['cavecrew-investigator.md', 'cavecrew-builder.md', 'cavecrew-reviewer.md'];
const EXISTING_MARKER = 'existing package must survive failed install';

function freshHome() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-omp-'));
}

function shimOmp(home) {
  const bin = path.join(home, 'bin');
  fs.mkdirSync(bin, { recursive: true });
  fs.writeFileSync(path.join(bin, OMP_SHIM_SCRIPT_NAME), OMP_SHIM_SCRIPT);
  const shim = path.join(bin, OMP_SHIM_NAME);
  fs.writeFileSync(shim, OMP_SHIM_BODY);
  if (process.platform !== 'win32') fs.chmodSync(shim, 0o755);
  return bin;
}

function runInstaller(args, home, extraEnv = {}, withOmp = true) {
  const bin = withOmp ? shimOmp(home) : path.dirname(process.execPath);
  return spawnSync(process.execPath, [INSTALLER, ...args, '--non-interactive', '--no-mcp-shrink'], {
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

test('omp fresh install prepares plugin package and invokes OMP plugin install', () => {
  const home = freshHome();
  try {
    const r = runInstaller(['--only', 'omp'], home);
    assert.equal(r.status, 0, r.stdout + r.stderr);

    const pluginDir = path.join(home, OMP_PLUGIN_DIR);
    const pkgPath = path.join(pluginDir, 'package.json');
    assert.ok(fs.existsSync(pkgPath), 'OMP plugin package.json missing');
    const pkg = JSON.parse(fs.readFileSync(pkgPath, 'utf8'));
    assert.equal(pkg.name, 'caveman');
    assert.deepEqual(pkg.omp.extensions, ['./index.cjs']);

    assert.ok(fs.existsSync(path.join(pluginDir, 'index.cjs')), 'OMP extension entry missing');
    for (const name of OMP_SKILLS) {
      assert.ok(fs.existsSync(path.join(pluginDir, 'skills', name, 'SKILL.md')), `skill ${name}/SKILL.md missing`);
    }
    for (const name of OMP_COMMANDS) {
      assert.ok(fs.existsSync(path.join(pluginDir, 'commands', name)), `command ${name} missing`);
    }
    for (const name of OMP_AGENTS) {
      const agentPath = path.join(pluginDir, 'agents', name);
      assert.ok(fs.existsSync(agentPath), `agent ${name} missing`);
      assert.doesNotMatch(fs.readFileSync(agentPath, 'utf8'), /^tools[ \t]*:/m, `agent ${name} kept incompatible tools field`);
    }
    const rule = fs.readFileSync(path.join(pluginDir, 'rules', 'caveman.md'), 'utf8');
    assert.match(rule, /Respond terse like smart caveman/, 'activation rule missing sentinel');

    const shimCalls = fs.readFileSync(path.join(home, OMP_ARGS_LOG), 'utf8');
    assert.match(shimCalls, new RegExp(`plugin install ${pluginDir.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`));
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('OMP failed registration retains owned payload and backs up replaced user files', () => {
  const home = freshHome();
  try {
    const pluginDir = path.join(home, OMP_PLUGIN_DIR);
    fs.mkdirSync(pluginDir, { recursive: true });
    fs.writeFileSync(path.join(pluginDir, 'package.json'), EXISTING_MARKER);

    const r = runInstaller(['--only', 'omp', '--force'], home, { OMP_FAIL_INSTALL: '1' });
    assert.equal(r.status, 1, r.stdout + r.stderr);
    assert.match(r.stdout + r.stderr, /omp plugin install failed/, 'install failure was not reported');
    assert.equal(JSON.parse(fs.readFileSync(path.join(pluginDir, 'package.json'), 'utf8')).name, 'caveman');
    const journal = JSON.parse(fs.readFileSync(path.join(home, '.omp', '.caveman-omp-ownership.json'), 'utf8'));
    const backup = path.join(home, '.omp', '.caveman-omp-backups', journal.entries['caveman-plugin'].restoreBackup);
    assert.equal(fs.readFileSync(path.join(backup, 'package.json'), 'utf8'), EXISTING_MARKER);
    assert.equal(fs.existsSync(pluginDir + '.previous'), false, 'backup directory leaked after failed install');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('OMP failed deregistration preserves its package, journal and registration', () => {
  const home = freshHome();
  try {
    assert.equal(runInstaller(['--only', 'omp'], home).status, 0);
    const plugin = path.join(home, OMP_PLUGIN_DIR);
    const journal = path.join(home, '.omp', '.caveman-omp-ownership.json');
    const before = fs.readFileSync(path.join(plugin, 'index.cjs'), 'utf8');
    const journalBefore = fs.readFileSync(journal, 'utf8');
    const result = runInstaller(['--uninstall'], home, { OMP_FAIL_UNINSTALL: '1' });
    assert.equal(result.status, 1);
    assert.match(result.stdout + result.stderr, /omp plugin uninstall failed/);
    assert.equal(fs.readFileSync(path.join(plugin, 'index.cjs'), 'utf8'), before);
    assert.equal(fs.readFileSync(journal, 'utf8'), journalBefore);
    assert.ok(fs.existsSync(path.join(home, 'omp-registration.json')));
    assert.doesNotMatch(result.stdout, /uninstall done\./);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('OMP missing host during uninstall preserves its owned package and journal', () => {
  const home = freshHome();
  try {
    assert.equal(runInstaller(['--only', 'omp'], home).status, 0);
    const plugin = path.join(home, OMP_PLUGIN_DIR);
    const journal = path.join(home, '.omp', '.caveman-omp-ownership.json');
    const before = fs.readFileSync(journal, 'utf8');
    const result = runInstaller(['--uninstall'], home, {}, false);
    assert.equal(result.status, 1);
    assert.match(result.stdout + result.stderr, /omp.*unavailable/);
    assert.ok(fs.existsSync(path.join(plugin, 'index.cjs')));
    assert.equal(fs.readFileSync(journal, 'utf8'), before);
    assert.ok(fs.existsSync(path.join(home, 'omp-registration.json')));
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('OMP dry runs leave payload, registration and journal untouched', () => {
  const home = freshHome();
  try {
    const plugin = path.join(home, OMP_PLUGIN_DIR);
    assert.equal(runInstaller(['--only', 'omp', '--dry-run'], home).status, 0);
    assert.equal(fs.existsSync(plugin), false);
    assert.equal(fs.existsSync(path.join(home, OMP_ARGS_LOG)), false);
    assert.equal(runInstaller(['--only', 'omp'], home).status, 0);
    const journal = path.join(home, '.omp', '.caveman-omp-ownership.json');
    const before = fs.readFileSync(journal, 'utf8');
    const calls = fs.readFileSync(path.join(home, OMP_ARGS_LOG), 'utf8');
    assert.equal(runInstaller(['--uninstall', '--dry-run'], home).status, 0);
    assert.ok(fs.existsSync(path.join(plugin, 'index.cjs')));
    assert.equal(fs.readFileSync(journal, 'utf8'), before);
    assert.equal(fs.readFileSync(path.join(home, OMP_ARGS_LOG), 'utf8'), calls);
    assert.ok(fs.existsSync(path.join(home, 'omp-registration.json')));
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('OMP failed fresh registration retains journaled payload for recovery', () => {
  const home = freshHome();
  try {
    const result = runInstaller(['--only', 'omp'], home, { OMP_FAIL_INSTALL: '1' });
    assert.equal(result.status, 1);
    assert.ok(fs.existsSync(path.join(home, OMP_PLUGIN_DIR, 'index.cjs')));
    assert.ok(fs.existsSync(path.join(home, '.omp', '.caveman-omp-ownership.json')));
    assert.deepEqual(fs.readdirSync(path.join(home, '.omp')).sort(), ['.caveman-omp-ownership.json', 'caveman-plugin']);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('OMP refuses untracked physical host collisions during install and uninstall', () => {
  for (const installed of [false, true]) {
    const home = freshHome();
    try {
      if (installed) assert.equal(runInstaller(['--only', 'omp'], home).status, 0);
      const target = path.join(home, 'omp-host-plugins', 'node_modules', 'caveman');
      fs.rmSync(target, { recursive: true, force: true });
      fs.mkdirSync(target, { recursive: true });
      fs.writeFileSync(path.join(target, 'user-notes.txt'), 'foreign content');
      for (const args of [['--only', 'omp', '--force'], ...(installed ? [['--uninstall']] : [])]) {
        const result = runInstaller(args, home);
        assert.equal(result.status, 1);
        assert.match(result.stdout + result.stderr, /OMP plugin name conflict/);
        assert.equal(fs.readFileSync(path.join(target, 'user-notes.txt'), 'utf8'), 'foreign content');
      }
      if (installed) assert.ok(fs.existsSync(path.join(home, OMP_PLUGIN_DIR, 'index.cjs')));
      else assert.equal(fs.existsSync(path.join(home, OMP_PLUGIN_DIR)), false);
    } finally { fs.rmSync(home, { recursive: true, force: true }); }
  }
});

test('OMP refuses marketplace collisions and ambiguous host state', () => {
  for (const extraEnv of [{ OMP_MARKETPLACE_CONFLICT: '1' }, { OMP_INVALID_LIST: '1' }]) {
    const home = freshHome();
    try {
      const result = runInstaller(['--only', 'omp', '--force'], home, extraEnv);
      assert.equal(result.status, 1);
      assert.equal(fs.existsSync(path.join(home, OMP_PLUGIN_DIR)), false);
      assert.equal(fs.existsSync(path.join(home, OMP_ARGS_LOG)), false);
    } finally { fs.rmSync(home, { recursive: true, force: true }); }
  }
});

test('OMP partial host registration keeps a valid owned link and can be retried then uninstalled', () => {
  const home = freshHome();
  try {
    const failed = runInstaller(['--only', 'omp'], home, { OMP_PARTIAL_INSTALL: '1' });
    assert.equal(failed.status, 1);
    const target = path.join(home, 'omp-host-plugins', 'node_modules', 'caveman');
    assert.equal(fs.realpathSync(target), fs.realpathSync(path.join(home, OMP_PLUGIN_DIR)));
    assert.ok(fs.existsSync(path.join(home, '.omp', '.caveman-omp-ownership.json')));
    assert.equal(runInstaller(['--only', 'omp'], home).status, 0);
    assert.equal(runInstaller(['--uninstall'], home).status, 0);
    assert.equal(fs.existsSync(target), false);
    assert.equal(fs.existsSync(path.join(home, OMP_PLUGIN_DIR)), false);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('OMP preserves an unowned package and never unregisters it', () => {
  const home = freshHome();
  try {
    const plugin = path.join(home, OMP_PLUGIN_DIR);
    fs.mkdirSync(plugin, { recursive: true });
    const foreign = path.join(plugin, 'my-notes.txt');
    fs.writeFileSync(foreign, 'foreign user data');
    const install = runInstaller(['--only', 'omp'], home);
    assert.equal(install.status, 1);
    assert.match(install.stdout + install.stderr, /ownership conflict/);
    assert.equal(fs.readFileSync(foreign, 'utf8'), 'foreign user data');
    const uninstall = runInstaller(['--uninstall'], home);
    assert.equal(uninstall.status, 0);
    assert.equal(fs.readFileSync(foreign, 'utf8'), 'foreign user data');
    const log = path.join(home, OMP_ARGS_LOG);
    assert.equal(fs.existsSync(log), false, 'unowned package must not reach OMP lifecycle commands');
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('OMP refuses upgrade and uninstall of edited owned content', () => {
  const home = freshHome();
  try {
    assert.equal(runInstaller(['--only', 'omp'], home).status, 0);
    const plugin = path.join(home, OMP_PLUGIN_DIR);
    const extension = path.join(plugin, 'index.cjs');
    fs.appendFileSync(extension, '\n// user customization\n');
    fs.writeFileSync(path.join(plugin, 'my-notes.txt'), 'user addition');
    const before = fs.readFileSync(extension, 'utf8');
    for (const args of [['--only', 'omp'], ['--uninstall']]) {
      const result = runInstaller(args, home);
      assert.equal(result.status, 1);
      assert.equal(fs.readFileSync(extension, 'utf8'), before);
      assert.equal(fs.readFileSync(path.join(plugin, 'my-notes.txt'), 'utf8'), 'user addition');
    }
    assert.ok(fs.existsSync(path.join(home, 'omp-registration.json')));
    assert.equal(fs.readFileSync(path.join(home, OMP_ARGS_LOG), 'utf8').trim().split('\n').length, 1);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('OMP forced replacement restores foreign bytes on uninstall and preserves unrelated backup path', () => {
  const home = freshHome();
  try {
    const plugin = path.join(home, OMP_PLUGIN_DIR);
    fs.mkdirSync(plugin, { recursive: true });
    fs.writeFileSync(path.join(plugin, 'notes.txt'), 'original foreign bytes');
    fs.mkdirSync(plugin + '.previous');
    fs.writeFileSync(path.join(plugin + '.previous', 'notes.txt'), 'unrelated backup');
    assert.equal(runInstaller(['--only', 'omp', '--force'], home).status, 0);
    assert.ok(fs.existsSync(path.join(plugin, 'index.cjs')));
    assert.equal(runInstaller(['--uninstall'], home).status, 0);
    assert.equal(fs.readFileSync(path.join(plugin, 'notes.txt'), 'utf8'), 'original foreign bytes');
    assert.equal(fs.readFileSync(path.join(plugin + '.previous', 'notes.txt'), 'utf8'), 'unrelated backup');
    assert.equal(fs.existsSync(path.join(home, '.omp', '.caveman-omp-ownership.json')), false);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('OMP registration is repaired on an unchanged reinstall', () => {
  const home = freshHome();
  try {
    assert.equal(runInstaller(['--only', 'omp'], home).status, 0);
    fs.unlinkSync(path.join(home, 'omp-registration.json'));
    assert.equal(runInstaller(['--only', 'omp'], home).status, 0);
    assert.ok(fs.existsSync(path.join(home, 'omp-registration.json')));
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('OMP refuses a symlinked integration root', { skip: process.platform === 'win32' }, () => {
  const home = freshHome();
  try {
    const elsewhere = path.join(home, 'elsewhere');
    fs.mkdirSync(elsewhere);
    fs.symlinkSync(elsewhere, path.join(home, '.omp'));
    const result = runInstaller(['--only', 'omp'], home);
    assert.equal(result.status, 1);
    assert.deepEqual(fs.readdirSync(elsewhere), []);
  } finally { fs.rmSync(home, { recursive: true, force: true }); }
});

test('omp uninstall uses plugin lifecycle and removes prepared package', () => {
  const home = freshHome();
  try {
    const r1 = runInstaller(['--only', 'omp'], home);
    assert.notEqual(r1.status, 2);
    const pluginDir = path.join(home, OMP_PLUGIN_DIR);
    assert.ok(fs.existsSync(pluginDir), 'precondition: OMP plugin package missing');

    const r2 = runInstaller(['--uninstall'], home);
    assert.notEqual(r2.status, 2, `uninstall argv error: ${r2.stderr}`);
    assert.equal(fs.existsSync(pluginDir), false, 'prepared OMP plugin package survived uninstall');
    const shimCalls = fs.readFileSync(path.join(home, OMP_ARGS_LOG), 'utf8');
    assert.match(shimCalls, /plugin uninstall caveman/, 'OMP plugin uninstall was not invoked');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});
test('omp extension keeps caveman active beyond the first prompt', async () => {
  const home = freshHome();
  try {
    const r = runInstaller(['--only', 'omp'], home);
    assert.equal(r.status, 0, r.stdout + r.stderr);
    const pluginDir = path.join(home, OMP_PLUGIN_DIR);
    const indexPath = path.join(pluginDir, 'index.cjs');
    const src = fs.readFileSync(indexPath, 'utf8');
    assert.match(src, /before_agent_start/, 'extension never re-injects after first prompt');
    assert.match(src, /Respond terse like smart caveman/, 'ruleset not embedded in extension');
    const { createRequire } = await import('node:module');
    const factory = createRequire(import.meta.url)(indexPath);
    const handlers = {};
    factory({ on: (event, fn) => { handlers[event] = fn; } });
    assert.ok(handlers.session_start, 'session_start handler missing');
    assert.ok(handlers.before_agent_start, 'before_agent_start handler missing');
    let status;
    await handlers.session_start({}, { ui: { setStatus: (k, v) => { status = [k, v]; } } });
    assert.deepEqual(status, ['caveman', 'CAVEMAN']);
    const first = await handlers.before_agent_start({ systemPrompt: ['base'] });
    assert.match(first.systemPrompt.join('\n'), /Respond terse like smart caveman/, 'first prompt lost ruleset');
    const second = await handlers.before_agent_start({ systemPrompt: ['base'] });
    assert.match(second.systemPrompt.join('\n'), /Respond terse like smart caveman/, 'second prompt lost ruleset');
    const retry = await handlers.before_agent_start({ systemPrompt: first.systemPrompt });
    assert.equal(retry, undefined, 'retry re-appended duplicate ruleset');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});
