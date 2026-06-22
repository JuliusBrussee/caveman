// pi-coding-agent (earendil-works) native install — fresh install, idempotency,
// uninstall.
//
// Detection of pi is gated behind `command -v pi` (or `~/.pi/agent` dir), so
// to run on a CI box without pi installed we shim a `pi` binary onto PATH.
// The installer's per-provider dispatch only checks PATH presence for
// `command:` probes; the binary itself is never invoked by the installer.

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

const IS_WIN = process.platform === 'win32';

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-pi-'));
}

function shimPi() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-pi-shim-'));
  if (IS_WIN) {
    fs.writeFileSync(path.join(dir, 'pi.cmd'), '@echo off\r\n');
  } else {
    const f = path.join(dir, 'pi');
    fs.writeFileSync(f, '#!/bin/sh\nexit 0\n');
    fs.chmodSync(f, 0o755);
  }
  return dir;
}

function runInstallerAt(installer, args, env) {
  return spawnSync('node', [installer, ...args, '--non-interactive', '--no-mcp-shrink'], {
    env, encoding: 'utf8',
  });
}

function runInstaller(args, env) {
  return runInstallerAt(INSTALLER, args, env);
}

function copyNpxLikePackage() {
  const pkg = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-pi-pkg-'));
  for (const name of ['bin', 'src', 'agents', 'skills']) {
    fs.cpSync(path.join(REPO_ROOT, name), path.join(pkg, name), { recursive: true });
  }
  return pkg;
}

function pathWith(prependDir) {
  const sep = IS_WIN ? ';' : ':';
  return prependDir + sep + (process.env.PATH || '');
}

// ── 1. Fresh install: symlink + fenced AGENTS.md ─────────────────────────
test('pi fresh install creates skills symlink and fenced AGENTS.md block', () => {
  const fakeHome = freshTmpDir();
  const shimDir = shimPi();
  try {
    const r = runInstaller(['--only', 'pi'], {
      ...process.env,
      HOME: fakeHome,
      USERPROFILE: fakeHome,
      PATH: pathWith(shimDir),
      NO_COLOR: '1',
    });
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);

    const piAgentDir = path.join(fakeHome, '.pi', 'agent');
    const linkPath = path.join(piAgentDir, 'skills', 'caveman');
    const agentsMd = path.join(piAgentDir, 'AGENTS.md');

    // Symlink exists, points to repo skills, resolves to SKILL.md files.
    assert.ok(fs.lstatSync(linkPath).isSymbolicLink(), 'skills/caveman should be a symlink');
    const target = fs.readlinkSync(linkPath);
    assert.equal(target, path.join(REPO_ROOT, 'skills'), 'symlink target should be repo skills/');
    for (const name of ['caveman', 'caveman-commit', 'caveman-compress', 'caveman-help', 'caveman-review', 'caveman-stats', 'cavecrew']) {
      assert.ok(
        fs.existsSync(path.join(linkPath, name, 'SKILL.md')),
        `resolved skill ${name}/SKILL.md should exist via the symlink`,
      );
    }

    // AGENTS.md exists, contains the fenced caveman block.
    assert.ok(fs.existsSync(agentsMd), 'AGENTS.md should be created');
    const body = fs.readFileSync(agentsMd, 'utf8');
    assert.ok(body.includes('<!-- caveman-begin -->'), 'AGENTS.md should include BEGIN marker');
    assert.ok(body.includes('<!-- caveman-end -->'), 'AGENTS.md should include END marker');
    assert.ok(body.includes('Respond terse like smart caveman'), 'AGENTS.md should include the rule body');
  } finally {
    fs.rmSync(fakeHome, { recursive: true, force: true });
  }
});

// ── 2. Idempotency: re-run does not duplicate the fence ─────────────────
test('pi re-install is idempotent — re-run keeps a single fenced block', () => {
  const fakeHome = freshTmpDir();
  const shimDir = shimPi();
  try {
    const env = {
      ...process.env, HOME: fakeHome, USERPROFILE: fakeHome,
      PATH: pathWith(shimDir), NO_COLOR: '1',
    };
    const r1 = runInstaller(['--only', 'pi'], env);
    assert.equal(r1.status, 0, `first install failed: ${r1.stderr}`);

    const agentsMd = path.join(fakeHome, '.pi', 'agent', 'AGENTS.md');
    const before = fs.readFileSync(agentsMd, 'utf8');
    const beginCount = (before.match(/<!-- caveman-begin -->/g) || []).length;

    const r2 = runInstaller(['--only', 'pi'], env);
    assert.equal(r2.status, 0, `re-install failed: ${r2.stderr}`);

    const after = fs.readFileSync(agentsMd, 'utf8');
    const beginAfter = (after.match(/<!-- caveman-begin -->/g) || []).length;
    assert.equal(beginAfter, beginCount, 're-install must not append a second fence');
  } finally {
    fs.rmSync(fakeHome, { recursive: true, force: true });
  }
});

// ── 3. Uninstall: removes symlink and strips the fenced block ────────────
test('pi uninstall removes the skills symlink and strips the AGENTS.md block', () => {
  const fakeHome = freshTmpDir();
  const shimDir = shimPi();
  try {
    const env = {
      ...process.env, HOME: fakeHome, USERPROFILE: fakeHome,
      PATH: pathWith(shimDir), NO_COLOR: '1',
    };
    runInstaller(['--only', 'pi'], env);
    const linkPath = path.join(fakeHome, '.pi', 'agent', 'skills', 'caveman');
    const agentsMd = path.join(fakeHome, '.pi', 'agent', 'AGENTS.md');
    assert.ok(fs.lstatSync(linkPath).isSymbolicLink(), 'precondition: symlink should exist');
    assert.ok(fs.existsSync(agentsMd), 'precondition: AGENTS.md should exist');

    const r = runInstaller(['--uninstall', '--only', 'pi'], env);
    assert.equal(r.status, 0, `uninstall failed: ${r.stderr}`);

    assert.equal(fs.existsSync(linkPath), false, 'symlink should be removed');
    assert.equal(fs.existsSync(agentsMd), false, 'AGENTS.md should be removed (was 100% our block)');
  } finally {
    fs.rmSync(fakeHome, { recursive: true, force: true });
  }
});

// ── 4. Uninstall with user-authored content above the fence: preserved ──
test('pi uninstall preserves user-authored content above the fenced block', () => {
  const fakeHome = freshTmpDir();
  const shimDir = shimPi();
  try {
    const piAgentDir = path.join(fakeHome, '.pi', 'agent');
    fs.mkdirSync(piAgentDir, { recursive: true });
    const userHeader = '# My agent notes\n\ndo not delete me.\n';
    fs.writeFileSync(path.join(piAgentDir, 'AGENTS.md'), userHeader, { mode: 0o644 });

    const env = {
      ...process.env, HOME: fakeHome, USERPROFILE: fakeHome,
      PATH: pathWith(shimDir), NO_COLOR: '1',
    };
    runInstaller(['--only', 'pi'], env);

    const agentsMd = path.join(piAgentDir, 'AGENTS.md');
    const afterInstall = fs.readFileSync(agentsMd, 'utf8');
    assert.ok(afterInstall.startsWith(userHeader), 'install must preserve user header above the fence');

    runInstaller(['--uninstall', '--only', 'pi'], env);

    const afterUninstall = fs.readFileSync(agentsMd, 'utf8');
    assert.equal(afterUninstall, userHeader, 'uninstall must leave only the user-authored header');
  } finally {
    fs.rmSync(fakeHome, { recursive: true, force: true });
  }
});

// ── 5. Refuses to overwrite a real (non-symlink) directory ───────────────
test('pi install refuses to overwrite a real directory at the symlink path', () => {
  const fakeHome = freshTmpDir();
  const shimDir = shimPi();
  try {
    const realDir = path.join(fakeHome, '.pi', 'agent', 'skills', 'caveman');
    fs.mkdirSync(realDir, { recursive: true });
    fs.writeFileSync(path.join(realDir, 'user-skill.md'), '# mine');

    const r = runInstaller(['--only', 'pi'], {
      ...process.env,
      HOME: fakeHome,
      USERPROFILE: fakeHome,
      PATH: pathWith(shimDir),
      NO_COLOR: '1',
    });

    // The installer should refuse without --force and leave the real dir intact.
    assert.match(r.stdout + r.stderr, /refusing to overwrite/, 'installer should report refusal');
    assert.ok(fs.existsSync(path.join(realDir, 'user-skill.md')), 'user content must not be deleted');
    assert.equal(fs.lstatSync(realDir).isSymbolicLink(), false, 'real dir must not be replaced by symlink');
  } finally {
    fs.rmSync(fakeHome, { recursive: true, force: true });
  }
});

// ── 6. npx/package install: copy, do not symlink to temp cache ───────────
test('pi install from a package without .git copies skills instead of creating a fragile symlink', () => {
  const fakeHome = freshTmpDir();
  const shimDir = shimPi();
  const pkg = copyNpxLikePackage();
  try {
    const r = runInstallerAt(path.join(pkg, 'bin', 'install.js'), ['--only', 'pi'], {
      ...process.env,
      HOME: fakeHome,
      USERPROFILE: fakeHome,
      PATH: pathWith(shimDir),
      NO_COLOR: '1',
    });
    assert.equal(r.status, 0, `package install failed: ${r.stderr}\n${r.stdout}`);

    const packPath = path.join(fakeHome, '.pi', 'agent', 'skills', 'caveman');
    assert.equal(fs.lstatSync(packPath).isSymbolicLink(), false, 'package install should copy, not symlink');
    for (const name of ['caveman', 'caveman-commit', 'caveman-compress', 'caveman-help', 'caveman-review', 'caveman-stats', 'cavecrew']) {
      assert.ok(
        fs.existsSync(path.join(packPath, name, 'SKILL.md')),
        `copied skill ${name}/SKILL.md should exist`,
      );
    }
  } finally {
    fs.rmSync(fakeHome, { recursive: true, force: true });
    fs.rmSync(pkg, { recursive: true, force: true });
  }
});

// ── 7. Existing wrong symlink: refuse without --force ────────────────────
test('pi install refuses to accept an existing symlink that points elsewhere', () => {
  const fakeHome = freshTmpDir();
  const shimDir = shimPi();
  try {
    const linkPath = path.join(fakeHome, '.pi', 'agent', 'skills', 'caveman');
    const wrongTarget = path.join(fakeHome, 'other-skills');
    fs.mkdirSync(path.dirname(linkPath), { recursive: true });
    fs.mkdirSync(wrongTarget, { recursive: true });
    fs.symlinkSync(wrongTarget, linkPath, 'dir');

    const r = runInstaller(['--only', 'pi'], {
      ...process.env,
      HOME: fakeHome,
      USERPROFILE: fakeHome,
      PATH: pathWith(shimDir),
      NO_COLOR: '1',
    });

    assert.match(r.stdout + r.stderr, /points elsewhere|use --force/, 'installer should report wrong symlink');
    assert.equal(fs.readlinkSync(linkPath), wrongTarget, 'wrong symlink should be left untouched');
    assert.equal(fs.existsSync(path.join(fakeHome, '.pi', 'agent', 'AGENTS.md')), false, 'AGENTS.md should not be written after symlink refusal');
  } finally {
    fs.rmSync(fakeHome, { recursive: true, force: true });
  }
});

// ── 8. Uninstall removes copied package install ──────────────────────────
test('pi uninstall removes copied package skills and strips AGENTS.md', () => {
  const fakeHome = freshTmpDir();
  const shimDir = shimPi();
  const pkg = copyNpxLikePackage();
  try {
    const installer = path.join(pkg, 'bin', 'install.js');
    const env = {
      ...process.env,
      HOME: fakeHome,
      USERPROFILE: fakeHome,
      PATH: pathWith(shimDir),
      NO_COLOR: '1',
    };
    const install = runInstallerAt(installer, ['--only', 'pi'], env);
    assert.equal(install.status, 0, `package install failed: ${install.stderr}\n${install.stdout}`);

    const packPath = path.join(fakeHome, '.pi', 'agent', 'skills', 'caveman');
    const agentsMd = path.join(fakeHome, '.pi', 'agent', 'AGENTS.md');
    assert.equal(fs.lstatSync(packPath).isSymbolicLink(), false, 'precondition: copied pack should be a real dir');
    assert.ok(fs.existsSync(path.join(packPath, 'caveman', 'SKILL.md')), 'precondition: copied skill exists');
    assert.ok(fs.existsSync(agentsMd), 'precondition: AGENTS.md exists');

    const uninstall = runInstallerAt(installer, ['--uninstall', '--only', 'pi'], env);
    assert.equal(uninstall.status, 0, `uninstall failed: ${uninstall.stderr}\n${uninstall.stdout}`);

    assert.equal(fs.existsSync(packPath), false, 'copied pack should be removed');
    assert.equal(fs.existsSync(agentsMd), false, 'AGENTS.md should be removed (was 100% our block)');
  } finally {
    fs.rmSync(fakeHome, { recursive: true, force: true });
    fs.rmSync(pkg, { recursive: true, force: true });
  }
});
