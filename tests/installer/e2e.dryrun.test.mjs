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
});

// ── Test: --init-only skips every agent install (issue #603) ───────────────
// The /caveman-init slash command shipped to Codex/Gemini users runs
// `npx ... -- --init-only`. It must write per-repo rule files and NOTHING
// else — no plugin installs, no npx-skills fallback, no interactive prompt.
test('--init-only dry-run plans rule files only, no agent installs', () => {
  const cfg = freshTmpDir();
  const cwd = freshTmpDir();
  const r = spawnSync('node', [INSTALLER,
    '--init-only', '--dry-run', '--no-mcp-shrink',
    '--config-dir', cfg,
  ], { encoding: 'utf8', cwd, env: { ...process.env, CLAUDE_CONFIG_DIR: cfg } });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /writing per-repo IDE rule files/);
  // No provider may run — not even ones detected on this machine.
  assert.doesNotMatch(r.stdout, /Claude Code detected/);
  assert.doesNotMatch(r.stdout, /Gemini CLI detected/);
  assert.doesNotMatch(r.stdout, /would run: claude plugin/);
  assert.doesNotMatch(r.stdout, /skills add/);
  // Dry run — nothing written anywhere.
  assert.equal(fs.existsSync(path.join(cfg, 'settings.json')), false);
  assert.equal(fs.readdirSync(cwd).length, 0);
});
