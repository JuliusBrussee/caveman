// Hermes Agent native install — fresh install lands skills, uninstall removes them.
//
// Hermes loads skills from <HERMES_HOME>/skills/<category>/<skill>/SKILL.md
// (verified against a live `hermes skills list`). The installer copies the 7
// caveman skill dirs into the `productivity/` category. `--only hermes` makes
// the provider explicit, so no `hermes` binary needs to be on PATH for the
// dispatch to run — we drive it purely through a throwaway HERMES_HOME.
//
// The uninstall test is the important one: PR #524 shipped installHermes with
// NO matching uninstall block, so `--uninstall` silently orphaned all 7 skill
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
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-hermes-'));
}

function runInstaller(args, hermesHome) {
  return spawnSync('node', [INSTALLER, ...args, '--non-interactive', '--no-mcp-shrink'], {
    env: { ...process.env, HERMES_HOME: hermesHome, NO_COLOR: '1' },
    encoding: 'utf8',
  });
}

function productivityDir(hermesHome) {
  return path.join(hermesHome, 'skills', 'productivity');
}

// ── 1. Fresh install drops all 7 skills with SKILL.md in the productivity category ──
test('hermes fresh install lands 7 skill dirs with SKILL.md under skills/productivity/', () => {
  const home = freshHome();
  try {
    const r = runInstaller(['--only', 'hermes'], home);
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);

    const prod = productivityDir(home);
    for (const name of SKILLS) {
      assert.ok(fs.existsSync(path.join(prod, name, 'SKILL.md')), `skill ${name}/SKILL.md missing`);
    }
    // caveman-compress ships executable scripts — ensure the recursive copy kept them.
    assert.ok(fs.existsSync(path.join(prod, 'caveman-compress', 'scripts')), 'caveman-compress/scripts/ not copied');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

// ── 2. Uninstall removes every skill we installed (regression guard for #524) ──
test('hermes uninstall removes all installed caveman skills (no orphans)', () => {
  const home = freshHome();
  try {
    const r1 = runInstaller(['--only', 'hermes'], home);
    assert.notEqual(r1.status, 2);
    const prod = productivityDir(home);
    for (const name of SKILLS) {
      assert.ok(fs.existsSync(path.join(prod, name)), `precondition: ${name} should be installed`);
    }

    const r2 = runInstaller(['--uninstall'], home);
    assert.notEqual(r2.status, 2);

    for (const name of SKILLS) {
      assert.equal(fs.existsSync(path.join(prod, name)), false, `${name} survived uninstall (orphaned skill)`);
    }
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

// ── 3. Dry-run uninstall must NOT delete anything ──
test('hermes dry-run uninstall leaves skills in place', () => {
  const home = freshHome();
  try {
    runInstaller(['--only', 'hermes'], home);
    const r = runInstaller(['--uninstall', '--dry-run'], home);
    assert.notEqual(r.status, 2);

    const prod = productivityDir(home);
    for (const name of SKILLS) {
      assert.ok(fs.existsSync(path.join(prod, name)), `${name} was deleted by a dry-run uninstall`);
    }
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});
