// End-to-end: dry-run installer prints expected file plan without touching disk.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const INSTALLER = path.resolve(HERE, '..', '..', 'bin', 'install.js');

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'cm-dryrun-'));
}

test('dry-run --only claude prints plan and writes nothing', () => {
  const cfg = freshTmpDir();
  const r = spawnSync('node', [INSTALLER,
    // --with-hooks: since #392/#393 the default only wires standalone hooks
    // when the plugin install fails. Force the hook-planning path so the
    // "would install / would merge" assertions below are exercised.
    '--dry-run', '--only', 'claude', '--with-hooks', '--no-mcp-shrink', '--non-interactive',
    '--config-dir', cfg,
  ], { encoding: 'utf8', env: { ...process.env, CLAUDE_CONFIG_DIR: cfg } });
  assert.equal(r.status, 0);
  // Only fires if `claude` is on PATH on the test runner. If not, this assertion
  // is a no-op (the installer just prints "nothing detected" and exits 0).
  if (/Claude Code detected/.test(r.stdout)) {
    assert.match(r.stdout, /would run: claude plugin marketplace add/);
    assert.match(r.stdout, /would run: claude plugin install caveman@caveman/);
    assert.match(r.stdout, /would mkdir -p .*\/hooks/);
    assert.match(r.stdout, /would install .*caveman-activate\.js/);
    assert.match(r.stdout, /would merge SessionStart \+ UserPromptSubmit \+ statusline/);
  }
  // Nothing should have been written.
  assert.equal(fs.existsSync(path.join(cfg, 'settings.json')), false);
  assert.equal(fs.existsSync(path.join(cfg, 'hooks')), false);
});

test('dry-run --uninstall does not delete files', () => {
  const cfg = freshTmpDir();
  // Seed a fake installation
  fs.mkdirSync(path.join(cfg, 'hooks'), { recursive: true });
  const fake = path.join(cfg, 'hooks', 'caveman-activate.js');
  fs.writeFileSync(fake, '// fake');
  fs.writeFileSync(path.join(cfg, 'settings.json'),
    JSON.stringify({ hooks: { SessionStart: [{ hooks: [{ type: 'command', command: 'node ' + fake }] }] } }, null, 2));
  const before = fs.readFileSync(path.join(cfg, 'settings.json'), 'utf8');

  const r = spawnSync('node', [INSTALLER, '--uninstall', '--dry-run', '--non-interactive', '--config-dir', cfg],
    { encoding: 'utf8', env: { ...process.env, CLAUDE_CONFIG_DIR: cfg } });
  assert.equal(r.status, 0);

  // File still present, settings unchanged.
  assert.equal(fs.existsSync(fake), true);
  assert.equal(fs.readFileSync(path.join(cfg, 'settings.json'), 'utf8'), before);
  assert.match(r.stdout, /would remove 1 caveman hook entry from settings\.json/);
  assert.doesNotMatch(r.stdout, /removed .*settings\.json/);
});

const CLAUDE_EPHEMERAL_STATE_FILES = [
  '.caveman-active',
  '.caveman-active.prev',
  '.caveman-mode-log.jsonl',
  '.caveman-statusline-suffix',
];
const CLAUDE_HISTORY_FILE = '.caveman-history.jsonl';

test('dry-run --uninstall reports state cleanup without deleting state files', () => {
  const cfg = freshTmpDir();
  for (const file of [...CLAUDE_EPHEMERAL_STATE_FILES, CLAUDE_HISTORY_FILE]) {
    fs.writeFileSync(path.join(cfg, file), 'seed\n');
  }

  const r = spawnSync(process.execPath, [INSTALLER, '--uninstall', '--dry-run', '--non-interactive', '--config-dir', cfg],
    { encoding: 'utf8', env: { ...process.env, CLAUDE_CONFIG_DIR: cfg, PATH: '' } });
  assert.equal(r.status, 0, r.stderr);

  for (const file of CLAUDE_EPHEMERAL_STATE_FILES) {
    assert.equal(fs.existsSync(path.join(cfg, file)), true, `${file} should survive dry-run`);
    assert.match(r.stdout, new RegExp(`would remove .*${file.replaceAll('.', '\\.')}`));
  }
  assert.equal(fs.existsSync(path.join(cfg, CLAUDE_HISTORY_FILE)), true, 'history should survive dry-run');
  assert.match(r.stdout, /kept .*\.caveman-history\.jsonl/);
});

test('uninstall removes ephemeral Claude state but keeps lifetime history', () => {
  const cfg = freshTmpDir();

  for (const file of [...CLAUDE_EPHEMERAL_STATE_FILES, CLAUDE_HISTORY_FILE]) {
    fs.writeFileSync(path.join(cfg, file), 'seed\n');
  }

  const r = spawnSync(process.execPath, [INSTALLER, '--uninstall', '--non-interactive', '--config-dir', cfg],
    { encoding: 'utf8', env: { ...process.env, CLAUDE_CONFIG_DIR: cfg, PATH: '' } });
  assert.equal(r.status, 0, r.stderr);

  for (const file of CLAUDE_EPHEMERAL_STATE_FILES) {
    assert.equal(fs.existsSync(path.join(cfg, file)), false, `${file} should be removed`);
    assert.match(r.stdout, new RegExp(`removed .*${file.replaceAll('.', '\\.')}`));
  }
  assert.equal(fs.existsSync(path.join(cfg, CLAUDE_HISTORY_FILE)), true, 'lifetime history should be preserved');
  assert.match(r.stdout, /kept .*\.caveman-history\.jsonl/);
});

test('uninstall warns when an ephemeral state file cannot be removed', () => {
  const cfg = freshTmpDir();
  const blockedState = '.caveman-active';
  const blockedPath = path.join(cfg, blockedState);
  fs.mkdirSync(blockedPath);
  fs.writeFileSync(path.join(cfg, CLAUDE_HISTORY_FILE), 'seed\n');

  const r = spawnSync(process.execPath, [INSTALLER, '--uninstall', '--non-interactive', '--config-dir', cfg],
    { encoding: 'utf8', env: { ...process.env, CLAUDE_CONFIG_DIR: cfg, PATH: '' } });
  assert.equal(r.status, 0, r.stderr);

  const combinedOutput = r.stdout + r.stderr;
  assert.match(combinedOutput, /failed to remove .*\.caveman-active/);
  assert.doesNotMatch(combinedOutput, /removed .*\.caveman-active/);
  assert.equal(fs.existsSync(blockedPath), true, `${blockedState} directory should remain after unlink failure`);
  assert.equal(fs.existsSync(path.join(cfg, CLAUDE_HISTORY_FILE)), true, 'history should survive failed cleanup');
});

test('uninstall warns when a managed hook file cannot be removed', () => {
  const cfg = freshTmpDir();
  const blockedHook = path.join(cfg, 'hooks', 'caveman-activate.js');
  fs.mkdirSync(blockedHook, { recursive: true });

  const r = spawnSync(process.execPath, [INSTALLER, '--uninstall', '--non-interactive', '--config-dir', cfg],
    { encoding: 'utf8', env: { ...process.env, CLAUDE_CONFIG_DIR: cfg, PATH: '' } });
  assert.equal(r.status, 0, r.stderr);

  const combinedOutput = r.stdout + r.stderr;
  assert.match(combinedOutput, /failed to remove .*caveman-activate\.js/);
  assert.doesNotMatch(combinedOutput, /removed .*caveman-activate\.js/);
  assert.equal(fs.existsSync(blockedHook), true, 'hook directory should remain after unlink failure');
});
