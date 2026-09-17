// Grok Build native install — fresh install lands skills, uninstall removes them.
//
// Grok Build (xAI `grok` CLI) has no confirmed upstream `skills` CLI profile,
// so the installer copies owned skill dirs natively into `$GROK_HOME/skills`
// (default `~/.grok/skills`). `--only grok` makes the provider explicit, so no
// `grok` binary needs to be on PATH for the dispatch to run — we drive it
// purely through a throwaway GROK_HOME.
//
// The uninstall test is the important one: like the Hermes PR #524 precedent,
// an install path without a matching uninstall would silently orphan skill
// folders forever. This pins the symmetry so it cannot regress.

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

const SKILLS = ['caveman', 'caveman-commit', 'caveman-review', 'caveman-help', 'caveman-stats', 'caveman-compress', 'cavecrew'];

function freshHome() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-grok-'));
}

function runInstaller(args, grokHome, cwd = grokHome) {
  return spawnSync(process.execPath, [INSTALLER, ...args, '--config-dir', path.join(grokHome, '.claude-test'), '--non-interactive', '--no-mcp-shrink'], {
    cwd,
    env: { ...process.env, GROK_HOME: grokHome, NO_COLOR: '1' },
    encoding: 'utf8',
  });
}

function skillsDir(grokHome) {
  return path.join(grokHome, 'skills');
}

// ── 1. Fresh install drops all 7 skills with SKILL.md directly under skills/ ──
test('grok fresh install lands 7 skill dirs with SKILL.md under skills/', () => {
  const home = freshHome();
  try {
    const r = runInstaller(['--only', 'grok'], home);
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);

    const root = skillsDir(home);
    for (const name of SKILLS) {
      assert.ok(fs.existsSync(path.join(root, name, 'SKILL.md')), `skill ${name}/SKILL.md missing`);
    }
    // caveman-compress ships executable scripts — ensure the recursive copy kept them.
    assert.ok(fs.existsSync(path.join(root, 'caveman-compress', 'scripts')), 'caveman-compress/scripts/ not copied');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

// ── 2. Uninstall removes every skill we installed (regression guard) ──
test('grok uninstall removes all installed caveman skills (no orphans)', () => {
  const home = freshHome();
  try {
    const r1 = runInstaller(['--only', 'grok'], home);
    assert.notEqual(r1.status, 2);
    const root = skillsDir(home);
    for (const name of SKILLS) {
      assert.ok(fs.existsSync(path.join(root, name)), `precondition: ${name} should be installed`);
    }

    const r2 = runInstaller(['--uninstall'], home);
    assert.notEqual(r2.status, 2);

    for (const name of SKILLS) {
      assert.equal(fs.existsSync(path.join(root, name)), false, `${name} survived uninstall (orphaned skill)`);
    }
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

// ── 3. Dry-run writes nothing but reports the would-copy target ──
test('grok dry-run writes nothing but reports the would-copy target', () => {
  const home = freshHome();
  try {
    const r = runInstaller(['--only', 'grok', '--dry-run'], home);
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);
    assert.match(r.stdout, /would copy Caveman skills into .*skills/);
    assert.equal(fs.existsSync(skillsDir(home)), false, 'dry-run created skill dirs');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

// ── 4. Relative GROK_HOME resolves cwd-relative ──
test('grok relative GROK_HOME resolves cwd-relative', () => {
  const home = freshHome();
  try {
    const project = path.join(home, 'project');
    fs.mkdirSync(project);
    const r = spawnSync(process.execPath, [INSTALLER, '--only', 'grok', '--config-dir', path.join(home, '.claude-test'), '--non-interactive', '--no-mcp-shrink'], {
      cwd: project,
      env: { ...process.env, GROK_HOME: 'custom grok', NO_COLOR: '1' },
      encoding: 'utf8',
    });
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);
    for (const name of SKILLS) {
      assert.ok(fs.existsSync(path.join(project, 'custom grok', 'skills', name, 'SKILL.md')), `skill ${name}/SKILL.md missing under cwd-relative GROK_HOME`);
    }
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});
