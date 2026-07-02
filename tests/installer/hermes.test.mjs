// End-to-end coverage for Hermes Agent native skill installation.

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
const SKILL_NAMES = ['caveman', 'caveman-commit', 'caveman-review', 'caveman-help', 'caveman-stats', 'caveman-compress', 'cavecrew'];

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-hermes-'));
}

function runInstaller(args, hermesHome) {
  return spawnSync('node', [INSTALLER, ...args, '--non-interactive', '--no-color'], {
    cwd: REPO_ROOT,
    env: { ...process.env, HERMES_HOME: hermesHome },
    encoding: 'utf8',
  });
}

function skillPath(hermesHome, name) {
  return path.join(hermesHome, 'skills', 'productivity', name);
}

test('hermes install copies all shipped skill directories under HERMES_HOME', () => {
  const dir = freshTmpDir();
  try {
    const hermesHome = path.join(dir, 'hermes-home');
    const r = runInstaller(['--only', 'hermes'], hermesHome);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    for (const name of SKILL_NAMES) {
      assert.ok(fs.existsSync(path.join(skillPath(hermesHome, name), 'SKILL.md')), `${name} SKILL.md missing`);
    }
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('hermes reinstall preserves existing local skill edits unless --force is passed', () => {
  const dir = freshTmpDir();
  try {
    const hermesHome = path.join(dir, 'hermes-home');
    const first = runInstaller(['--only', 'hermes'], hermesHome);
    assert.equal(first.status, 0, first.stderr || first.stdout);

    const editedSkill = path.join(skillPath(hermesHome, 'caveman'), 'SKILL.md');
    fs.appendFileSync(editedSkill, '\nLOCAL USER EDIT\n');

    const second = runInstaller(['--only', 'hermes'], hermesHome);
    assert.equal(second.status, 0, second.stderr || second.stdout);
    assert.match(fs.readFileSync(editedSkill, 'utf8'), /LOCAL USER EDIT/, 'reinstall without --force wiped local edit');

    const forced = runInstaller(['--only', 'hermes', '--force'], hermesHome);
    assert.equal(forced.status, 0, forced.stderr || forced.stdout);
    assert.doesNotMatch(fs.readFileSync(editedSkill, 'utf8'), /LOCAL USER EDIT/, '--force should refresh the skill from repo contents');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('hermes uninstall removes native skill directories and dry-run preserves them', () => {
  const dir = freshTmpDir();
  try {
    const hermesHome = path.join(dir, 'hermes-home');
    const installed = runInstaller(['--only', 'hermes'], hermesHome);
    assert.equal(installed.status, 0, installed.stderr || installed.stdout);

    const dry = runInstaller(['--uninstall', '--dry-run'], hermesHome);
    assert.equal(dry.status, 0, dry.stderr || dry.stdout);
    for (const name of SKILL_NAMES) {
      assert.ok(fs.existsSync(skillPath(hermesHome, name)), `${name} removed during dry-run`);
    }

    const removed = runInstaller(['--uninstall'], hermesHome);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
    for (const name of SKILL_NAMES) {
      assert.equal(fs.existsSync(skillPath(hermesHome, name)), false, `${name} survived uninstall`);
    }
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('provider matrix labels Hermes as a native install', () => {
  const r = spawnSync('node', [INSTALLER, '--list', '--no-color'], { encoding: 'utf8' });
  assert.equal(r.status, 0);
  assert.match(r.stdout, /hermes\s+Hermes Agent\s+native Hermes skills copy/);
});
