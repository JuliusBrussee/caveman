// Antigravity CLI (`agy`): the installer stages a plugin (plugin.json, skills,
// rules/AGENTS.md) and hands it to `agy plugin install`, which copies it into
// agy's own plugin root. A stub `agy` records argv and snapshots the staged
// directory, since the installer deletes its staging copy afterwards.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { nodeStub, stubEnv } from '../../packages/cli/tests/harness/stub-bin.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const INSTALLER = path.join(ROOT, 'installer/install.js');
const SKILLS = ['caveman', 'ultracave', 'megacave', 'caveman-commit', 'caveman-review', 'caveman-help', 'caveman-stats', 'caveman-compress', 'cavecrew'];

function fixture(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman agy '));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const home = path.join(dir, 'home');
  fs.mkdirSync(home);
  const log = path.join(dir, 'agy.log');
  const installed = path.join(dir, 'installed');
  nodeStub(path.join(dir, 'bin'), 'agy', `
import fs from 'node:fs';
fs.appendFileSync(${JSON.stringify(log)}, JSON.stringify(ARGV) + '\\n');
if (ARGV[1] === 'install') {
  if (process.env.AGY_FAIL === '1') process.exit(1);
  fs.rmSync(${JSON.stringify(installed)}, { recursive: true, force: true });
  fs.cpSync(ARGV[2], ${JSON.stringify(installed)}, { recursive: true });
}
if (ARGV[1] === 'list') console.log(JSON.stringify({ imports: fs.existsSync(${JSON.stringify(installed)}) ? [{ name: 'caveman', source: 'antigravity' }] : null }, null, 2));
if (ARGV[1] === 'uninstall') fs.rmSync(${JSON.stringify(installed)}, { recursive: true, force: true });
`);
  // Only the stub (and node, which Windows shims need by name) on PATH.
  const base = Object.fromEntries(Object.entries(process.env).filter(([key]) => key.toLowerCase() !== 'path'));
  base.PATH = process.platform === 'win32' ? path.dirname(process.execPath) : '/usr/bin:/bin';
  const env = stubEnv({ ...base, HOME: home, USERPROFILE: home, NO_COLOR: '1' }, path.join(dir, 'bin'));
  const run = (args, extra = {}) => spawnSync(process.execPath, [INSTALLER, ...args, '--non-interactive'], {
    encoding: 'utf8', cwd: dir, env: { ...env, ...extra },
  });
  const calls = () => (fs.existsSync(log) ? fs.readFileSync(log, 'utf8').trim().split('\n').map((line) => JSON.parse(line)) : []);
  return { dir, env, run, calls, installed };
}

test('stages the plugin with skills and the always-on rule, then hands it to agy', (t) => {
  const { run, calls, installed } = fixture(t);
  const r = run(['--only', 'antigravity-cli']);
  assert.equal(r.status, 0, r.stdout + r.stderr);
  const [install] = calls().filter((argv) => argv[1] === 'install');
  assert.deepEqual(install.slice(0, 2), ['plugin', 'install']);
  assert.equal(path.basename(install[2]), 'caveman');
  assert.equal(fs.existsSync(install[2]), false, 'staging copy is cleaned up');
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(installed, 'plugin.json'), 'utf8')).name, 'caveman');
  for (const id of SKILLS) assert.ok(fs.existsSync(path.join(installed, 'skills', id, 'SKILL.md')), id);
  assert.equal(
    fs.readFileSync(path.join(installed, 'rules', 'AGENTS.md'), 'utf8'),
    fs.readFileSync(path.join(ROOT, 'src/rules/caveman-activate.md'), 'utf8'),
  );
});

test('`agy` on PATH is detected without --only', (t) => {
  const { dir, env, calls } = fixture(t);
  // Keep macOS system apps (Cursor.app etc.) out of detection.
  const preload = path.join(dir, 'hide-system-apps.cjs');
  fs.writeFileSync(preload, `const fs = require('fs'); const exists = fs.existsSync; fs.existsSync = p => String(p).startsWith('/Applications/') ? false : exists(p);`);
  const r = spawnSync(process.execPath, ['--require', preload, INSTALLER, '--non-interactive'], { encoding: 'utf8', cwd: dir, env });
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.ok(calls().some((argv) => argv[1] === 'install'));
});

test('a failed agy install is reported', (t) => {
  const { run } = fixture(t);
  const r = run(['--only', 'antigravity-cli'], { AGY_FAIL: '1' });
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /antigravity-cli/);
});

test('uninstall removes the plugin only when agy lists it', (t) => {
  const { run, calls, installed } = fixture(t);
  assert.equal(run(['--uninstall']).status, 0);
  assert.equal(calls().some((argv) => argv[1] === 'uninstall'), false);
  assert.equal(run(['--only', 'antigravity-cli']).status, 0);
  assert.equal(run(['--uninstall']).status, 0);
  assert.deepEqual(calls().filter((argv) => argv[1] === 'uninstall'), [['plugin', 'uninstall', 'caveman']]);
  assert.equal(fs.existsSync(installed), false);
});
