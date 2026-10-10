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
import { isolatedEnv } from './_isolated-env.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'installer', 'install.js');

// Derived, not pinned, so this suite tracks the real skills/ set (as
// provider-skills-integration.test.mjs does) instead of drifting stale
// whenever a skill is added or removed.
const SKILLS = fs.readdirSync(path.join(REPO_ROOT, 'skills'))
  .filter((name) => fs.existsSync(path.join(REPO_ROOT, 'skills', name, 'SKILL.md')))
  .sort();

function freshHome() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-grok-'));
}

function runInstaller(args, grokHome, cwd = grokHome) {
  return spawnSync(process.execPath, [INSTALLER, ...args, '--config-dir', path.join(grokHome, '.claude-test'), '--non-interactive', '--no-mcp-shrink'], {
    cwd,
    env: { ...isolatedEnv(path.join(grokHome, 'home')), GROK_HOME: grokHome },
    encoding: 'utf8',
  });
}

function skillsDir(grokHome) {
  return path.join(grokHome, 'skills');
}

// ── 1. Fresh install drops every owned skill with SKILL.md directly under skills/ ──
test('grok fresh install lands every skill dir with SKILL.md under skills/', () => {
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

// ── 5. Always-on: one marker-fenced ruleset block in $GROK_HOME/AGENTS.md (#754) ──
// Grok Build loads global rules from ~/.grok/AGENTS.md. The block goes through
// the same append/strip helpers as OpenClaw's SOUL.md.
const BEGIN = '<!-- caveman-begin -->';
const END = '<!-- caveman-end -->';
const count = (s, sub) => s.split(sub).length - 1;

test('grok install writes exactly one fenced ruleset block to AGENTS.md, idempotently', () => {
  const home = freshHome();
  try {
    const agentsMd = path.join(home, 'AGENTS.md');
    fs.writeFileSync(agentsMd, '# my grok rules\n\nUse tabs.\n');
    assert.notEqual(runInstaller(['--only', 'grok'], home).status, 2);
    assert.notEqual(runInstaller(['--only', 'grok'], home).status, 2);
    const body = fs.readFileSync(agentsMd, 'utf8');
    assert.equal(count(body, BEGIN), 1, body);
    assert.equal(count(body, END), 1, body);
    assert.ok(body.indexOf(BEGIN) < body.indexOf(END));
    const block = body.slice(body.indexOf(BEGIN), body.indexOf(END));
    assert.match(block, /Respond terse like smart caveman/);
    assert.ok(body.startsWith('# my grok rules\n\nUse tabs.\n'), 'user text above the block preserved');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('grok uninstall strips the AGENTS.md block, keeps user text, removes a caveman-only file', () => {
  const home = freshHome();
  const bare = freshHome();
  try {
    const agentsMd = path.join(home, 'AGENTS.md');
    fs.writeFileSync(agentsMd, '# my grok rules\n');
    runInstaller(['--only', 'grok'], home);
    runInstaller(['--uninstall'], home);
    assert.equal(fs.readFileSync(agentsMd, 'utf8'), '# my grok rules\n');

    runInstaller(['--only', 'grok'], bare);
    assert.ok(fs.existsSync(path.join(bare, 'AGENTS.md')), 'precondition: block written');
    runInstaller(['--uninstall'], bare);
    assert.equal(fs.existsSync(path.join(bare, 'AGENTS.md')), false, 'caveman-only AGENTS.md must be removed');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
    fs.rmSync(bare, { recursive: true, force: true });
  }
});

test('grok dry-run leaves AGENTS.md alone and reports the planned block', () => {
  const home = freshHome();
  try {
    const r = runInstaller(['--only', 'grok', '--dry-run'], home);
    assert.match(r.stdout, /would add the caveman ruleset block to .*AGENTS\.md/);
    assert.equal(fs.existsSync(path.join(home, 'AGENTS.md')), false);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('grok install leaves a symlinked AGENTS.md alone but still installs skills', { skip: process.platform === 'win32' && 'symlinks need privileges on Windows' }, () => {
  const home = freshHome();
  try {
    const target = path.join(home, 'dotfiles-AGENTS.md');
    fs.writeFileSync(target, '# dotfiles\n');
    fs.symlinkSync(target, path.join(home, 'AGENTS.md'));
    const r = runInstaller(['--only', 'grok'], home);
    assert.equal(fs.readFileSync(target, 'utf8'), '# dotfiles\n');
    assert.ok(fs.existsSync(path.join(skillsDir(home), 'caveman', 'SKILL.md')));
    assert.match(r.stdout, /• grok\n/, 'skills install still reported');
    assert.match(r.stderr, /grok-always-on/);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});
