// Grok CLI native install — always-on rules + skills + agents + AGENTS.md fence.
//
// Grok Build TUI loads $GROK_HOME/rules/*.md and AGENTS.md every session, and
// discovers skills under $GROK_HOME/skills/<name>/SKILL.md. SessionStart hook
// stdout is ignored, so always-on is rules/ + AGENTS.md (not Claude hooks).
//
// `--only grok` dispatches even without a `grok` binary on PATH. Isolation is
// via GROK_HOME (when set, installer never spills into the real ~/.grok).

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
const AGENTS = ['cavecrew-investigator.md', 'cavecrew-builder.md', 'cavecrew-reviewer.md'];
const SENTINEL = 'Respond terse like smart caveman';
const MARK_BEGIN = '<!-- caveman-begin -->';
const MARK_END = '<!-- caveman-end -->';

function freshHome() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-grok-'));
}

function runInstaller(args, grokHome) {
  return spawnSync('node', [INSTALLER, ...args, '--non-interactive', '--no-mcp-shrink'], {
    env: { ...process.env, GROK_HOME: grokHome, NO_COLOR: '1' },
    encoding: 'utf8',
  });
}

// ── 1. Fresh install lands rules, skills, agents, AGENTS.md ───────────────
test('grok fresh install lands rules/caveman.md, 7 skills, 3 agents, fenced AGENTS.md', () => {
  const home = freshHome();
  try {
    const r = runInstaller(['--only', 'grok'], home);
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);
    assert.match(r.stdout, /Grok CLI detected/);
    assert.match(r.stdout, /installed:\s+grok|• grok/);

    const rule = path.join(home, 'rules', 'caveman.md');
    assert.ok(fs.existsSync(rule), 'rules/caveman.md missing');
    const ruleBody = fs.readFileSync(rule, 'utf8');
    assert.match(ruleBody, new RegExp(SENTINEL));

    for (const name of SKILLS) {
      assert.ok(fs.existsSync(path.join(home, 'skills', name, 'SKILL.md')), `skill ${name}/SKILL.md missing`);
    }
    assert.ok(fs.existsSync(path.join(home, 'skills', 'caveman-compress', 'scripts')), 'caveman-compress/scripts/ not copied');

    for (const f of AGENTS) {
      assert.ok(fs.existsSync(path.join(home, 'agents', f)), `agent ${f} missing`);
    }

    const agentsMd = fs.readFileSync(path.join(home, 'AGENTS.md'), 'utf8');
    assert.ok(agentsMd.includes(MARK_BEGIN) && agentsMd.includes(MARK_END), 'AGENTS.md missing marker fence');
    assert.ok(agentsMd.includes(SENTINEL), 'AGENTS.md missing ruleset sentinel');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

// ── 2. Idempotent re-install does not duplicate the AGENTS.md fence ───────
test('grok re-install is idempotent (no duplicate AGENTS.md fence)', () => {
  const home = freshHome();
  try {
    const r1 = runInstaller(['--only', 'grok'], home);
    assert.notEqual(r1.status, 2);
    const r2 = runInstaller(['--only', 'grok'], home);
    assert.notEqual(r2.status, 2);

    const agentsMd = fs.readFileSync(path.join(home, 'AGENTS.md'), 'utf8');
    const begins = agentsMd.split(MARK_BEGIN).length - 1;
    const ends = agentsMd.split(MARK_END).length - 1;
    assert.equal(begins, 1, `expected 1 begin marker, got ${begins}`);
    assert.equal(ends, 1, `expected 1 end marker, got ${ends}`);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

// ── 3. Preserves user content around the AGENTS.md fence ──────────────────
test('grok install appends fence without wiping existing AGENTS.md content', () => {
  const home = freshHome();
  try {
    fs.mkdirSync(home, { recursive: true });
    fs.writeFileSync(path.join(home, 'AGENTS.md'), '# User rules\n\nAlways use TypeScript.\n', { mode: 0o644 });

    const r = runInstaller(['--only', 'grok'], home);
    assert.notEqual(r.status, 2);

    const body = fs.readFileSync(path.join(home, 'AGENTS.md'), 'utf8');
    assert.match(body, /Always use TypeScript/);
    assert.ok(body.includes(MARK_BEGIN) && body.includes(MARK_END));
    assert.ok(body.indexOf('Always use TypeScript') < body.indexOf(MARK_BEGIN), 'user content should stay above the fence');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

// ── 4. Uninstall strips fence + removes owned files, keeps user prefix ────
test('grok uninstall removes rules/skills/agents and strips AGENTS.md fence', () => {
  const home = freshHome();
  try {
    fs.mkdirSync(home, { recursive: true });
    fs.writeFileSync(path.join(home, 'AGENTS.md'), '# Keep me\n\n', { mode: 0o644 });

    const r1 = runInstaller(['--only', 'grok'], home);
    assert.notEqual(r1.status, 2);
    for (const name of SKILLS) {
      assert.ok(fs.existsSync(path.join(home, 'skills', name)), `precondition: ${name}`);
    }

    const r2 = runInstaller(['--uninstall'], home);
    assert.notEqual(r2.status, 2);

    assert.equal(fs.existsSync(path.join(home, 'rules', 'caveman.md')), false, 'rules/caveman.md survived uninstall');
    for (const name of SKILLS) {
      assert.equal(fs.existsSync(path.join(home, 'skills', name)), false, `${name} survived uninstall`);
    }
    for (const f of AGENTS) {
      assert.equal(fs.existsSync(path.join(home, 'agents', f)), false, `${f} survived uninstall`);
    }

    const agentsMd = fs.readFileSync(path.join(home, 'AGENTS.md'), 'utf8');
    assert.match(agentsMd, /Keep me/);
    assert.equal(agentsMd.includes(MARK_BEGIN), false, 'begin marker left behind');
    assert.equal(agentsMd.includes(MARK_END), false, 'end marker left behind');
    assert.equal(agentsMd.includes(SENTINEL), false, 'sentinel left behind');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

// ── 5. Dry-run install writes nothing ─────────────────────────────────────
test('grok dry-run install does not write files', () => {
  const home = freshHome();
  try {
    // Pre-create so dry-run discovery can list the path without mkdir.
    fs.mkdirSync(home, { recursive: true });
    const r = runInstaller(['--only', 'grok', '--dry-run'], home);
    assert.notEqual(r.status, 2);
    assert.match(r.stdout, /would install into/);

    assert.equal(fs.existsSync(path.join(home, 'rules', 'caveman.md')), false);
    assert.equal(fs.existsSync(path.join(home, 'skills', 'caveman')), false);
    assert.equal(fs.existsSync(path.join(home, 'AGENTS.md')), false);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

// ── 6. --list advertises the grok provider ────────────────────────────────
test('grok appears in --list provider matrix', () => {
  const r = spawnSync('node', [INSTALLER, '--list'], {
    env: { ...process.env, NO_COLOR: '1' },
    encoding: 'utf8',
  });
  assert.equal(r.status, 0);
  assert.match(r.stdout, /\bgrok\b/);
  assert.match(r.stdout, /Grok CLI/);
});
