// `absoluteNodePath()` bakes a node path into settings.json at install time and
// every hook then runs under it, so the path has to outlive the install shell.
//
// Preferring the PATH entry over process.execPath is deliberate (#805): Homebrew
// runs the installer as the versioned Cellar binary, and /opt/homebrew/bin/node
// is the stable symlink that follows the next upgrade. fnm breaks that premise.
// Its PATH entry is a per-SHELL link — /run/user/1000/fnm_multishells/<id>/bin —
// which fnm deletes when the shell exits, and which is gone after a reboot. It
// runs fine at install time, so the exercise-the-candidate guard passes it, and
// every hook then fails with ENOENT in the next session.
//
// The CLI already refuses these for `node` (stableNodePath) and, since #1541,
// for the caveman script (durableCavemen). The standalone installer is the
// sibling path that serves Claude Code users who never install the CLI, and it
// must refuse them too — falling back to process.execPath, which is the node
// version's own install dir and lasts as long as that version is installed.

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
const CLI_SRC = path.join(REPO_ROOT, 'packages', 'cli', 'src', 'index.ts');

// The node path the installer persisted into a Claude Code hook command.
function storedHookNode(configDir) {
  const settings = JSON.parse(fs.readFileSync(path.join(configDir, 'settings.json'), 'utf8'));
  const command = (settings.hooks?.SessionStart || [])
    .flatMap((e) => (Array.isArray(e?.hooks) ? e.hooks : []))
    .map((h) => h?.command || '')
    .find((c) => c.includes('caveman-activate')) || '';
  const stored = /^"([^"]+)"/.exec(command)?.[1];
  assert.ok(stored, `no node path in hook command: ${command}`);
  return stored;
}

test('an fnm multishell node on PATH is not persisted into a hook command', { skip: process.platform === 'win32' && 'POSIX shim behavior' }, () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-fnm-node-'));
  const configDir = path.join(dir, 'claude-config');
  // The real layout: a per-shell dir whose `node` is a working link to the
  // version's install dir. It reports a current version, so the floor check
  // and the does-it-actually-run check both pass — the path is unusable only
  // LATER, which is what makes it the wrong thing to persist.
  const multishell = path.join(dir, 'fnm_multishells', '4242_1760000000000', 'bin');
  fs.mkdirSync(multishell, { recursive: true });
  const fnmNode = path.join(multishell, 'node');
  fs.symlinkSync(process.execPath, fnmNode);

  try {
    const r = spawnSync(process.execPath, [
      INSTALLER, '--only', 'claude', '--with-hooks', '--skip-skills',
      '--config-dir', configDir, '--non-interactive', '--no-mcp-shrink',
    ], {
      env: { ...isolatedEnv(path.join(dir, 'home'), [multishell]), CLAUDE_CONFIG_DIR: configDir },
      encoding: 'utf8',
    });
    assert.notEqual(r.status, 2, `installer aborted on argv parse: ${r.stderr}`);

    const stored = storedHookNode(configDir);
    assert.ok(
      !/[\\/]fnm_multishells[\\/]/.test(stored),
      `installer persisted an fnm multishell node that vanishes with the shell: ${stored}`
    );
    // Not merely "something else": it has to be a node that runs.
    const probe = spawnSync(stored, ['--version'], { encoding: 'utf8' });
    assert.equal(probe.status, 0, `stored hook executable must run: ${probe.stderr}`);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// installer/install.js also runs detached from a checkout (the curl fallback), so
// it cannot import the CLI's copy — the pattern is hand-copied, like
// FALLBACK_VALID_MODES in .codex/codex-sessionstart.js. This is the guard that
// keeps the two from drifting: a host that learns to refuse one of these paths
// and leaves its sibling behind is the bug class #1541 came from.
test('the installer and the CLI refuse the same fnm multishell pattern', () => {
  const read = (file, label) => {
    const m = /fnm_multishells/.exec(fs.readFileSync(file, 'utf8'));
    assert.ok(m, `${label} no longer refuses fnm multishell paths at all`);
    const src = fs.readFileSync(file, 'utf8');
    const literal = /\/\[\\\\\/\]fnm_multishells\[\\\\\/\]\/i/.exec(src);
    assert.ok(literal, `${label} does not carry the /[\\\\/]fnm_multishells[\\\\/]/i literal`);
    return literal[0];
  };
  assert.equal(read(INSTALLER, 'installer/install.js'), read(CLI_SRC, 'packages/cli/src/index.ts'));
});
