// GitHub Copilot CLI always-on: after `npx skills add -a github-copilot`, the
// installer owns $COPILOT_HOME/caveman/ (the shared sessionStart hook payload)
// and $COPILOT_HOME/hooks/caveman.json, which Copilot CLI loads as a user hook.
// Stubbed npx/copilot on PATH; HOME and COPILOT_HOME are temp dirs.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { nodeStub, stubEnv } from '../../packages/cli/tests/harness/stub-bin.mjs';
import { isolatedEnv } from './_isolated-env.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const INSTALLER = path.join(ROOT, 'installer/install.js');

function fixture(t, { copilotBin = true, copilotHome = true } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman copilot '));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const home = path.join(dir, 'home');
  const root = path.join(dir, 'copilot home');
  fs.mkdirSync(home);
  const bin = path.join(dir, 'bin');
  const npxLog = path.join(dir, 'npx.json');
  // Like the real skills CLI, whose github-copilot globalSkillsDir is
  // ~/.copilot/skills: a bare ~/.copilot says nothing about the Copilot CLI.
  nodeStub(bin, 'npx', `import fs from 'node:fs'; import os from 'node:os'; import path from 'node:path';
    fs.writeFileSync(${JSON.stringify(npxLog)}, JSON.stringify(ARGV));
    fs.mkdirSync(path.join(os.homedir(), '.copilot', 'skills', 'caveman'), { recursive: true });`);
  if (copilotBin) nodeStub(bin, 'copilot', 'process.exit(0);');
  // Only the stubs (and node, which Windows shims need by name) on PATH.
  const base = Object.fromEntries(Object.entries(isolatedEnv(home)).filter(([key]) =>
    !['caveman_default_mode', 'copilot_home'].includes(key.toLowerCase())));
  const env = stubEnv(base, bin);
  if (copilotHome) env.COPILOT_HOME = root;
  const run = (...args) => spawnSync(process.execPath, [INSTALLER, ...args, '--non-interactive'], {
    encoding: 'utf8', cwd: dir, env,
  });
  return { dir, home, root, env, run, npxLog };
}

const hookPath = (root) => path.join(root, 'hooks', 'caveman.json');

test('installs an owned sessionStart hook that injects the ruleset; rerun and uninstall keep foreign hooks', (t) => {
  const { dir, root, env, run } = fixture(t);
  const foreign = path.join(root, 'hooks', 'mine.json');
  fs.mkdirSync(path.dirname(foreign), { recursive: true });
  fs.writeFileSync(foreign, '{"version":1,"hooks":{}}\n');

  let r = run('--only', 'copilot');
  assert.equal(r.status, 0, r.stdout + r.stderr);
  const config = JSON.parse(fs.readFileSync(hookPath(root), 'utf8'));
  assert.equal(config.version, 1);
  const [entry] = config.hooks.sessionStart;
  assert.equal(entry.type, 'command');
  assert.equal(entry.timeoutSec, 10);
  assert.match(entry.bash, /caveman-host-session-start\.js'? copilot$/);
  assert.match(entry.powershell, /^node ".+caveman-host-session-start\.js" copilot$/);
  assert.deepEqual(Object.keys(config.hooks), ['sessionStart'], 'userPromptSubmitted output is dropped by Copilot');

  const command = process.platform === 'win32' ? entry.powershell : entry.bash;
  const sh = spawnSync(command, { shell: true, cwd: dir, env, input: JSON.stringify({ cwd: dir, source: 'startup' }), encoding: 'utf8' });
  assert.match(JSON.parse(sh.stdout).additionalContext, /CAVEMAN MODE ACTIVE — mode: caveman/);

  const before = fs.readFileSync(hookPath(root), 'utf8');
  r = run('--only', 'copilot');
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.equal(fs.readFileSync(hookPath(root), 'utf8'), before);
  assert.equal(fs.existsSync(path.join(root, '.caveman-copilot-cli-backups')), false);

  r = run('--uninstall');
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.equal(fs.existsSync(hookPath(root)), false);
  assert.equal(fs.existsSync(path.join(root, 'caveman')), false);
  assert.equal(fs.readFileSync(foreign, 'utf8'), '{"version":1,"hooks":{}}\n');
});

test('a copilot binary on PATH is detected without --only', (t) => {
  const { dir, env, npxLog } = fixture(t);
  // Keep macOS system apps (Cursor.app etc.) out of detection.
  const preload = path.join(dir, 'hide-system-apps.cjs');
  fs.writeFileSync(preload, `const fs = require('fs'); const exists = fs.existsSync; fs.existsSync = p => String(p).startsWith('/Applications/') ? false : exists(p);`);
  const r = spawnSync(process.execPath, ['--require', preload, INSTALLER, '--non-interactive'], { encoding: 'utf8', cwd: dir, env });
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.ok(JSON.parse(fs.readFileSync(npxLog, 'utf8')).includes('github-copilot'));
});

test('no Copilot CLI and no COPILOT_HOME: skills only, no hook', (t) => {
  const { home, run } = fixture(t, { copilotBin: false, copilotHome: false });
  const r = run('--only', 'copilot');
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.equal(fs.existsSync(path.join(home, '.copilot', 'skills', 'caveman')), true, 'stub wrote the skills');
  assert.deepEqual(fs.readdirSync(path.join(home, '.copilot')), ['skills']);
});

test('--no-hooks skips the hook', (t) => {
  const { root, run } = fixture(t);
  const r = run('--only', 'copilot', '--no-hooks');
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.equal(fs.existsSync(hookPath(root)), false);
});

test('a user file at hooks/caveman.json is never overwritten', (t) => {
  const { root, run } = fixture(t);
  fs.mkdirSync(path.dirname(hookPath(root)), { recursive: true });
  fs.writeFileSync(hookPath(root), 'mine\n');
  const r = run('--only', 'copilot');
  assert.match(r.stderr, /ownership conflict/);
  assert.equal(fs.readFileSync(hookPath(root), 'utf8'), 'mine\n');
});
