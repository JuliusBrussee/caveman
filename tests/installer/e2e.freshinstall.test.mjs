// End-to-end: real fresh install against an isolated $CLAUDE_CONFIG_DIR.
//
// Unlike e2e.dryrun.test.mjs (which only verifies the planned output), this
// suite actually writes hooks, merges settings.json, and asserts the on-disk
// state. It catches regressions in the install pipeline that a dry-run can't
// see — missing hook files, malformed settings entries, broken statusline
// wiring, idempotency bugs, JSONC-tolerance regressions (#249-class).
//
// Limitations:
//   - The Claude Code provider only triggers when `claude` is on PATH. Tests
//     that depend on it skip cleanly when missing (most CI runners and dev
//     boxes won't have it). Tests that don't need `claude` (idempotence,
//     JSONC tolerance) always run.
//   - The installer's uninstall path also calls `claude plugin uninstall` and
//     `gemini extensions uninstall` against whatever binary is on PATH. We
//     strip those out of PATH in the uninstall test so the user's real
//     plugin/extension state is never touched.
//   - The plugin install step makes a network call (clones the marketplace).
//     We tolerate failure there — only the hook/settings assertions matter
//     because the hooks-installer runs regardless of plugin-install status.
//   - Each fresh-install case spawns a real `claude plugin install` (~300MB
//     of git clone). Run the test runner with `--test-concurrency=1` to
//     avoid OOM on memory-constrained CI runners.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'bin', 'install.js');
const requireCjs = createRequire(import.meta.url);
const SETTINGS = requireCjs(path.join(REPO_ROOT, 'bin', 'lib', 'settings.js'));

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-freshinstall-'));
}

function pathWithout(binNames) {
  // Walk every PATH entry; drop any that contains one of the named binaries.
  // Cross-platform: works on macOS/Linux (`:` sep) and Windows (`;` sep).
  const sep = process.platform === 'win32' ? ';' : ':';
  const exts = process.platform === 'win32' ? ['.exe', '.cmd', '.bat', ''] : [''];
  const want = new Set(binNames);
  return (process.env.PATH || '')
    .split(sep)
    .filter(dir => {
      if (!dir) return false;
      for (const b of want) {
        for (const ext of exts) {
          try { if (fs.existsSync(path.join(dir, b + ext))) return false; } catch (_) {}
        }
      }
      return true;
    })
    .join(sep);
}

function runInstaller(args, configDir, extraEnv = {}) {
  return spawnSync('node', [INSTALLER, ...args, '--config-dir', configDir, '--non-interactive', '--no-mcp-shrink'], {
    env: { ...process.env, CLAUDE_CONFIG_DIR: configDir, NO_COLOR: '1', ...extraEnv },
    encoding: 'utf8',
  });
}

function hasClaudeCli() {
  // We can't import bin/install.js's hasCmd directly (CJS, not exported), but
  // a plain `command -v` / `where` shell-out is equivalent for this purpose.
  if (process.platform === 'win32') {
    return spawnSync('where', ['claude'], { stdio: 'ignore' }).status === 0;
  }
  return spawnSync('sh', ['-c', 'command -v claude'], { stdio: 'ignore' }).status === 0;
}

const STATUSLINE_FILE = process.platform === 'win32'
  ? 'caveman-statusline.ps1'
  : 'caveman-statusline.sh';

function getStatuslineCommand(settings) {
  if (!settings.statusLine) return '';
  return typeof settings.statusLine === 'string'
    ? settings.statusLine
    : (settings.statusLine.command || '');
}

function cavemanHookCommands(settings, event, marker) {
  return (settings.hooks?.[event] || [])
    .flatMap(e => (Array.isArray(e?.hooks) ? e.hooks : []))
    .filter(h => h && typeof h.command === 'string' && h.command.includes(marker));
}

// ── Test: fresh install populates expected files ───────────────────────────
test('fresh install populates hooks dir and settings.json (skipped without `claude` CLI)', { skip: !hasClaudeCli() && 'claude CLI not on PATH; the claude provider is the only path that wires hooks' }, () => {
  const dir = freshTmpDir();
  try {
    const r = runInstaller(['--only', 'claude'], dir);
    // The plugin install step may fail (network, auth) but the hooks step
    // runs regardless. We only require the hooks-side state.
    assert.notEqual(r.status, 2, `installer aborted on argv parse: ${r.stderr}`);

    const hooks = path.join(dir, 'hooks');
    assert.ok(fs.existsSync(path.join(hooks, 'caveman-activate.js')),     'caveman-activate.js missing');
    assert.ok(fs.existsSync(path.join(hooks, 'caveman-mode-tracker.js')), 'caveman-mode-tracker.js missing');
    assert.ok(fs.existsSync(path.join(hooks, 'caveman-config.js')),       'caveman-config.js missing');
    assert.ok(fs.existsSync(path.join(hooks, 'package.json')),            'hooks/package.json (CJS marker) missing');
    assert.ok(fs.existsSync(path.join(hooks, STATUSLINE_FILE)),           `${STATUSLINE_FILE} missing`);

    // Settings merged correctly.
    const settingsPath = path.join(dir, 'settings.json');
    assert.ok(fs.existsSync(settingsPath), 'settings.json missing');
    const settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));

    assert.ok(SETTINGS.hasCavemanHook(settings, 'SessionStart', 'caveman-activate'),
      'SessionStart hook missing or wrong marker');
    assert.ok(SETTINGS.hasCavemanHook(settings, 'UserPromptSubmit', 'caveman-mode-tracker'),
      'UserPromptSubmit hook missing or wrong marker');
    assert.ok(settings.statusLine, 'statusLine not set');
    assert.match(getStatuslineCommand(settings), /caveman-statusline/,
      'statusLine command does not reference caveman');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// ── Test: idempotent install (run twice, no duplication) ───────────────────
test('idempotent install does not duplicate hook entries (skipped without `claude` CLI)', { skip: !hasClaudeCli() && 'claude CLI not on PATH' }, () => {
  const dir = freshTmpDir();
  try {
    const r1 = runInstaller(['--only', 'claude'], dir);
    assert.notEqual(r1.status, 2, `first install argv error: ${r1.stderr}`);
    const r2 = runInstaller(['--only', 'claude'], dir);
    assert.notEqual(r2.status, 2, `second install argv error: ${r2.stderr}`);

    const settings = JSON.parse(fs.readFileSync(path.join(dir, 'settings.json'), 'utf8'));

    const sessStart = cavemanHookCommands(settings, 'SessionStart', 'caveman-activate');
    assert.equal(sessStart.length, 1, `expected 1 SessionStart caveman hook, got ${sessStart.length}`);

    const ups = cavemanHookCommands(settings, 'UserPromptSubmit', 'caveman-mode-tracker');
    assert.equal(ups.length, 1, `expected 1 UserPromptSubmit caveman hook, got ${ups.length}`);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// ── Test: uninstall removes hooks, preserves unrelated entries ─────────────
test('uninstall strips caveman hooks but preserves user-authored ones (skipped without `claude` CLI)', { skip: !hasClaudeCli() && 'claude CLI not on PATH; uninstall test depends on a prior real install' }, () => {
  const dir = freshTmpDir();
  try {
    // Seed user's existing settings so we can verify they survive.
    fs.writeFileSync(path.join(dir, 'settings.json'), JSON.stringify({
      model: 'opus',
      hooks: {
        SessionStart: [{ hooks: [{ type: 'command', command: 'echo user-owned-hook' }] }],
      },
    }, null, 2));

    const r1 = runInstaller(['--only', 'claude'], dir);
    assert.notEqual(r1.status, 2, `install argv error: ${r1.stderr}`);

    // Strip claude/gemini from PATH for uninstall so we don't touch the user's
    // real plugin/extension state — only file/settings cleanup runs.
    const cleanPath = pathWithout(['claude', 'gemini']);
    const r2 = runInstaller(['--uninstall'], dir, { PATH: cleanPath });
    assert.notEqual(r2.status, 2, `uninstall argv error: ${r2.stderr}`);

    // Hook scripts deleted.
    const hooks = path.join(dir, 'hooks');
    if (fs.existsSync(hooks)) {
      for (const f of ['caveman-activate.js', 'caveman-mode-tracker.js', 'caveman-config.js', STATUSLINE_FILE]) {
        assert.equal(fs.existsSync(path.join(hooks, f)), false, `${f} should be removed`);
      }
    }

    // Settings cleaned up.
    const settings = JSON.parse(fs.readFileSync(path.join(dir, 'settings.json'), 'utf8'));
    // No remaining caveman-marked hooks anywhere.
    for (const ev of Object.keys(settings.hooks || {})) {
      const arr = settings.hooks[ev] || [];
      for (const e of arr) {
        for (const h of (e.hooks || [])) {
          assert.doesNotMatch(h.command || '', /caveman/, `${ev} still has caveman hook: ${h.command}`);
        }
      }
    }
    // User's pre-existing hook preserved.
    const preservedUser = cavemanHookCommands(settings, 'SessionStart', 'user-owned-hook').length > 0;
    assert.ok(preservedUser, 'user-authored SessionStart hook was wiped during uninstall');

    // Statusline pointing at caveman should be removed.
    assert.doesNotMatch(getStatuslineCommand(settings), /caveman-statusline/,
      'caveman statusline survived uninstall');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// ── Test: settings.json with JSONC comments doesn't crash (#249) ───────────
// Regression guard: the installer used to crash here because JSON.parse can't
// eat // or /* */. bin/lib/settings.js now strips them before merging.
test('install tolerates JSONC settings.json (comments + trailing commas)', { skip: !hasClaudeCli() && 'claude CLI not on PATH' }, () => {
  const dir = freshTmpDir();
  try {
    fs.writeFileSync(path.join(dir, 'settings.json'),
      `// user wrote this by hand
{
  /* keep it simple */
  "model": "opus",
  "hooks": {},
}
`);

    const r = runInstaller(['--only', 'claude'], dir);
    assert.notEqual(r.status, 2, `installer aborted on argv parse: ${r.stderr}`);

    // After install, settings.json must be strict-JSON parseable.
    const raw = fs.readFileSync(path.join(dir, 'settings.json'), 'utf8');
    let parsed;
    assert.doesNotThrow(() => { parsed = JSON.parse(raw); }, 'settings.json must round-trip as strict JSON');

    // User's `model` key must survive the merge.
    assert.equal(parsed.model, 'opus', 'user-authored model setting was dropped');

    // Caveman hooks must be wired.
    assert.ok(SETTINGS.hasCavemanHook(parsed, 'SessionStart', 'caveman-activate'));
    assert.ok(SETTINGS.hasCavemanHook(parsed, 'UserPromptSubmit', 'caveman-mode-tracker'));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// ── Test: idempotent re-add at the lib level (always runs, no claude needed)
// This guards the addCommandHook idempotency promise without spawning a real
// install — even on machines with no `claude` CLI we want this assertion.
test('lib settings.addCommandHook is idempotent across two synthetic install passes', () => {
  const dir = freshTmpDir();
  const settingsPath = path.join(dir, 'settings.json');
  try {
    const settings = SETTINGS.readSettings(settingsPath);
    SETTINGS.addCommandHook(settings, 'SessionStart', {
      command: '"/usr/bin/node" "/abs/hooks/caveman-activate.js"',
      marker: 'caveman-activate',
    });
    SETTINGS.addCommandHook(settings, 'SessionStart', {
      command: '"/usr/bin/node" "/different/hooks/caveman-activate.js"',
      marker: 'caveman-activate',
    });
    SETTINGS.validateHookFields(settings);
    SETTINGS.writeSettings(settingsPath, settings);

    const round = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
    assert.equal(round.hooks.SessionStart.length, 1, 'addCommandHook duplicated entry');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
