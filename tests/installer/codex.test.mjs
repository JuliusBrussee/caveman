// Codex plugin installer state-machine tests. A fake Codex CLI makes first
// install, rerun, disabled-plugin recovery, and --force deterministic.

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

function makeFakeCodex(initial = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-codex-'));
  const statePath = path.join(dir, 'state.json');
  const logPath = path.join(dir, 'calls.log');
  fs.writeFileSync(statePath, JSON.stringify({
    marketplace: false,
    sourceType: 'git',
    installed: false,
    enabled: false,
    ...initial,
  }));

  const scriptPath = path.join(dir, 'codex.js');
  fs.writeFileSync(scriptPath, `#!/usr/bin/env node
const fs = require('fs');
const statePath = process.env.FAKE_CODEX_STATE;
const logPath = process.env.FAKE_CODEX_LOG;
const args = process.argv.slice(2);
const state = JSON.parse(fs.readFileSync(statePath, 'utf8'));
fs.appendFileSync(logPath, args.join(' ') + '\\n');
const save = () => fs.writeFileSync(statePath, JSON.stringify(state));
const out = value => process.stdout.write(JSON.stringify(value));
if (args.join(' ') === 'plugin list --json') {
  out({ installed: state.installed ? [{
    pluginId: 'caveman@caveman', enabled: state.enabled, installed: true
  }] : [], available: [] });
} else if (args.join(' ') === 'plugin marketplace list --json') {
  out({ marketplaces: state.marketplace ? [{
    name: 'caveman', marketplaceSource: { sourceType: state.sourceType }
  }] : [] });
} else if (args[0] === 'plugin' && args[1] === 'marketplace' && args[2] === 'add') {
  if (state.marketplace) process.exit(1);
  state.marketplace = true;
  save();
} else if (args.join(' ') === 'plugin marketplace upgrade caveman') {
  if (!state.marketplace || state.sourceType !== 'git') process.exit(1);
} else if (args.join(' ') === 'plugin remove caveman@caveman') {
  if (!state.installed) process.exit(1);
  state.installed = false;
  state.enabled = false;
  save();
} else if (args.join(' ') === 'plugin add caveman@caveman') {
  if (!state.marketplace || state.installed) process.exit(1);
  state.installed = true;
  state.enabled = true;
  save();
} else {
  process.exit(2);
}
`);

  if (IS_WIN) {
    fs.writeFileSync(path.join(dir, 'codex.cmd'), '@echo off\r\nnode "%~dp0\\codex.js" %*\r\n');
  } else {
    const launcher = path.join(dir, 'codex');
    fs.writeFileSync(launcher, `#!/bin/sh\nexec "${process.execPath}" "${scriptPath}" "$@"\n`);
    fs.chmodSync(launcher, 0o755);
  }

  return { dir, statePath, logPath };
}

function runInstaller(fake, extraArgs = []) {
  const separator = IS_WIN ? ';' : ':';
  return spawnSync(process.execPath, [
    INSTALLER, '--only', 'codex', '--non-interactive', '--no-mcp-shrink', ...extraArgs,
  ], {
    encoding: 'utf8',
    env: {
      ...process.env,
      PATH: fake.dir + separator + (process.env.PATH || ''),
      FAKE_CODEX_STATE: fake.statePath,
      FAKE_CODEX_LOG: fake.logPath,
      NO_COLOR: '1',
    },
  });
}

function state(fake) {
  return JSON.parse(fs.readFileSync(fake.statePath, 'utf8'));
}

function calls(fake) {
  return fs.readFileSync(fake.logPath, 'utf8').trim().split('\n').filter(Boolean);
}

test('Codex install is idempotent when marketplace and plugin already exist', () => {
  const fake = makeFakeCodex();
  try {
    const first = runInstaller(fake);
    assert.equal(first.status, 0, first.stderr);
    assert.deepEqual(state(fake), {
      marketplace: true, sourceType: 'git', installed: true, enabled: true,
    });

    const before = calls(fake);
    const second = runInstaller(fake);
    assert.equal(second.status, 0, second.stderr);
    assert.match(second.stdout, /already installed and enabled/);
    assert.deepEqual(calls(fake).slice(before.length), ['plugin list --json']);
  } finally {
    fs.rmSync(fake.dir, { recursive: true, force: true });
  }
});

test('Codex install replaces a disabled plugin without re-adding marketplace', () => {
  const fake = makeFakeCodex({ marketplace: true, installed: true, enabled: false });
  try {
    const result = runInstaller(fake);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(state(fake).enabled, true);
    assert.ok(calls(fake).includes('plugin remove caveman@caveman'));
    assert.ok(calls(fake).includes('plugin add caveman@caveman'));
    assert.ok(!calls(fake).some(call => call.startsWith('plugin marketplace add')));
  } finally {
    fs.rmSync(fake.dir, { recursive: true, force: true });
  }
});

test('Codex --force refreshes Git marketplace and reinstalls plugin', () => {
  const fake = makeFakeCodex({ marketplace: true, installed: true, enabled: true });
  try {
    const result = runInstaller(fake, ['--force']);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(state(fake).enabled, true);
    const log = calls(fake);
    assert.ok(log.includes('plugin remove caveman@caveman'));
    assert.ok(log.includes('plugin marketplace upgrade caveman'));
    assert.ok(log.includes('plugin add caveman@caveman'));
    assert.ok(log.indexOf('plugin marketplace upgrade caveman') <
              log.indexOf('plugin remove caveman@caveman'));
    assert.ok(!log.some(call => call.startsWith('plugin marketplace add')));
  } finally {
    fs.rmSync(fake.dir, { recursive: true, force: true });
  }
});
