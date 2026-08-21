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
fs.appendFileSync(path.join(process.env.HOME || process.env.USERPROFILE, '${OMP_ARGS_LOG}'), args.join(' ') + '\\n');
if (process.env.OMP_FAIL_INSTALL === '1' && args[0] === 'plugin' && args[1] === 'install') process.exit(1);
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

function runInstaller(args, home, extraEnv = {}) {
  const bin = shimOmp(home);
  return spawnSync(process.execPath, [INSTALLER, ...args, '--non-interactive', '--no-mcp-shrink'], {
    env: {
      ...process.env,
      ...extraEnv,
      HOME: home,
      USERPROFILE: home,
      PATH: bin + PATH_SEPARATOR + (process.env.PATH || ''),
      NO_COLOR: '1',
    },
    encoding: 'utf8',
  });
}

test('omp fresh install prepares plugin package and invokes OMP plugin install', () => {
  const home = freshHome();
  try {
    const r = runInstaller(['--only', 'omp'], home);
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);

    const pluginDir = path.join(home, OMP_PLUGIN_DIR);
    const pkgPath = path.join(pluginDir, 'package.json');
    assert.ok(fs.existsSync(pkgPath), 'OMP plugin package.json missing');
    const pkg = JSON.parse(fs.readFileSync(pkgPath, 'utf8'));
    assert.equal(pkg.name, 'caveman');
    assert.deepEqual(pkg.omp.extensions, ['./index.js']);

    assert.ok(fs.existsSync(path.join(pluginDir, 'index.js')), 'OMP extension entry missing');
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

test('omp install restores previous prepared package when plugin install fails', () => {
  const home = freshHome();
  try {
    const pluginDir = path.join(home, OMP_PLUGIN_DIR);
    fs.mkdirSync(pluginDir, { recursive: true });
    fs.writeFileSync(path.join(pluginDir, 'package.json'), EXISTING_MARKER);

    const r = runInstaller(['--only', 'omp'], home, { OMP_FAIL_INSTALL: '1' });
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);
    assert.match(r.stdout + r.stderr, /omp plugin install failed/, 'install failure was not reported');
    assert.equal(fs.readFileSync(path.join(pluginDir, 'package.json'), 'utf8'), EXISTING_MARKER);
    assert.equal(fs.existsSync(pluginDir + '.previous'), false, 'backup directory leaked after failed install');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
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
