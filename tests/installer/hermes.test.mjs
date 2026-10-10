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
const INSTALLER = path.join(REPO_ROOT, 'installer', 'install.js');

const SKILLS = ['caveman', 'ultracave', 'megacave', 'caveman-commit', 'caveman-review', 'caveman-help', 'caveman-stats', 'caveman-compress', 'cavecrew'];

function freshHome() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-hermes-'));
}

function runInstaller(args, hermesHome) {
  return spawnSync(process.execPath, [INSTALLER, ...args, '--config-dir', path.join(hermesHome, '.claude-test'), '--non-interactive', '--no-mcp-shrink'], {
    env: { ...process.env, HERMES_HOME: hermesHome, NO_COLOR: '1' },
    encoding: 'utf8',
  });
}

function productivityDir(hermesHome) {
  return path.join(hermesHome, 'skills', 'productivity');
}

// ── 1. Fresh install drops all 9 skills with SKILL.md in the productivity category ──
test('hermes fresh install lands 9 skill dirs with SKILL.md under skills/productivity/', () => {
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

test('hermes refuses unowned same-named skills without writing a partial install', () => {
  const home = freshHome();
  try {
    const prod = productivityDir(home);
    const userSkill = path.join(prod, 'caveman');
    fs.mkdirSync(userSkill, { recursive: true });
    fs.writeFileSync(path.join(userSkill, 'SKILL.md'), '# user-owned\n');

    const result = runInstaller(['--only', 'hermes'], home);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /ownership conflict/);
    assert.equal(fs.readFileSync(path.join(userSkill, 'SKILL.md'), 'utf8'), '# user-owned\n');
    assert.equal(fs.existsSync(path.join(prod, 'caveman-review')), false, 'conflict must fail before partial copy');
    assert.equal(fs.existsSync(path.join(prod, '.caveman-hermes-ownership.json')), false);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('hermes uninstall never deletes unjournaled same-named user content', () => {
  const home = freshHome();
  try {
    const userSkill = path.join(productivityDir(home), 'caveman');
    fs.mkdirSync(userSkill, { recursive: true });
    fs.writeFileSync(path.join(userSkill, 'SKILL.md'), '# user-owned\n');
    const removed = runInstaller(['--uninstall'], home);
    assert.equal(removed.status, 0, removed.stderr);
    assert.equal(fs.readFileSync(path.join(userSkill, 'SKILL.md'), 'utf8'), '# user-owned\n');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('hermes --force backs up conflicts and uninstall restores original bytes', () => {
  const home = freshHome();
  try {
    const prod = productivityDir(home);
    const userSkill = path.join(prod, 'caveman');
    fs.mkdirSync(userSkill, { recursive: true });
    fs.writeFileSync(path.join(userSkill, 'SKILL.md'), '# user-owned\n');

    const installed = runInstaller(['--only', 'hermes', '--force'], home);
    assert.equal(installed.status, 0, installed.stderr);
    assert.notEqual(fs.readFileSync(path.join(userSkill, 'SKILL.md'), 'utf8'), '# user-owned\n');
    assert.ok(fs.existsSync(path.join(prod, '.caveman-hermes-ownership.json')));

    const removed = runInstaller(['--uninstall'], home);
    assert.equal(removed.status, 0, removed.stderr);
    assert.equal(fs.readFileSync(path.join(userSkill, 'SKILL.md'), 'utf8'), '# user-owned\n');
    assert.equal(fs.existsSync(path.join(prod, 'caveman-review')), false);
    assert.equal(fs.existsSync(path.join(prod, '.caveman-hermes-ownership.json')), false);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('hermes uninstall leaves modified installed content and retains ownership record', () => {
  const home = freshHome();
  try {
    const installed = runInstaller(['--only', 'hermes'], home);
    assert.equal(installed.status, 0, installed.stderr);
    const prod = productivityDir(home);
    const changed = path.join(prod, 'caveman', 'SKILL.md');
    fs.appendFileSync(changed, '\n# local edit\n');

    const removed = runInstaller(['--uninstall'], home);
    assert.equal(removed.status, 1, removed.stderr);
    assert.match(removed.stderr, /left modified/);
    assert.match(removed.stderr, /uninstall incomplete/);
    assert.match(fs.readFileSync(changed, 'utf8'), /# local edit/);
    assert.ok(fs.existsSync(path.join(prod, '.caveman-hermes-ownership.json')));
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

// Hermes 0.21.5 reads HERMES_HOME as Path(expanduser(expandvars(value.strip()))).
// A value from a dotenv file or service unit reaches us unexpanded; taken raw, the
// skills landed in ./~/h/skills, a directory Hermes never reads.
for (const [name, override] of [
  ['tilde', '  ~/hermes-home  '],
  ['env var', '$CAVE_TEST_HERMES_ROOT/hermes-home'],
  ['braced env var', '${CAVE_TEST_HERMES_ROOT}/hermes-home'],
]) {
  test(`hermes install expands HERMES_HOME like Hermes does: ${name}`, () => {
    const home = freshHome();
    const cwd = freshHome();
    try {
      const r = spawnSync(process.execPath, [INSTALLER, '--only', 'hermes', '--config-dir', path.join(home, '.claude-test'), '--non-interactive', '--no-mcp-shrink'], {
        cwd,
        env: { ...process.env, HOME: home, USERPROFILE: home, CAVE_TEST_HERMES_ROOT: home, HERMES_HOME: override, NO_COLOR: '1' },
        encoding: 'utf8',
      });
      assert.equal(r.status, 0, r.stderr);
      const prod = productivityDir(path.join(home, 'hermes-home'));
      for (const skill of SKILLS) {
        assert.ok(fs.existsSync(path.join(prod, skill, 'SKILL.md')), `skill ${skill}/SKILL.md missing under expanded HERMES_HOME`);
      }
      assert.deepEqual(fs.readdirSync(cwd), [], 'unexpanded HERMES_HOME wrote under the cwd');
    } finally {
      fs.rmSync(home, { recursive: true, force: true });
      fs.rmSync(cwd, { recursive: true, force: true });
    }
  });
}
