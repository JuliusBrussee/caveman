// End-to-end: --doctor reports install health read-only, across both
// install modes (Claude Code plugin vs standalone hooks).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO, 'cli', 'install.js');
const HOOKS_SRC = path.join(REPO, 'src', 'hooks');
const IS_WIN = process.platform === 'win32';

// Mirrors the installer's hook-file plan: everything the standalone install
// copies into <configDir>/hooks.
const HOOK_FILES = [
  'package.json',
  'caveman-config.js',
  'caveman-parse.js',
  'caveman-activate.js',
  'caveman-mode-tracker.js',
  'caveman-stats.js',
  'caveman-statusline.sh',
  'caveman-statusline.ps1',
  'cavecrew-model-overrides.js',
];

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'cm-doctor-'));
}

// Build a healthy STANDALONE install: all hook files copied verbatim from
// src/hooks plus a settings.json wired the way installHooks writes it.
function healthyStandaloneInstall() {
  const cfg = freshTmpDir();
  const hooksDir = path.join(cfg, 'hooks');
  fs.mkdirSync(hooksDir, { recursive: true });
  for (const f of HOOK_FILES) {
    fs.copyFileSync(path.join(HOOKS_SRC, f), path.join(hooksDir, f));
  }
  const node = process.execPath;
  const settings = {
    hooks: {
      SessionStart: [{ hooks: [{ type: 'command', command: `"${node}" "${path.join(hooksDir, 'caveman-activate.js')}"`, timeout: 5 }] }],
      UserPromptSubmit: [{ hooks: [{ type: 'command', command: `"${node}" "${path.join(hooksDir, 'caveman-mode-tracker.js')}"`, timeout: 5 }] }],
    },
    statusLine: { type: 'command', command: `bash "${path.join(hooksDir, 'caveman-statusline.sh')}"` },
  };
  fs.writeFileSync(path.join(cfg, 'settings.json'), JSON.stringify(settings, null, 2) + '\n');
  return cfg;
}

// A fake `claude` binary that answers `plugin list` deterministically,
// independent of whatever the host machine actually has installed. Returns
// the directory to prepend to PATH.
function fakeClaudeDir(pluginListStdout) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-fakebin-'));
  if (IS_WIN) {
    const bin = path.join(dir, 'claude.cmd');
    fs.writeFileSync(bin,
      `@echo off\r\nif "%1"=="plugin" if "%2"=="list" (\r\n  echo ${pluginListStdout}\r\n  exit /b 0\r\n)\r\nexit /b 1\r\n`);
  } else {
    const bin = path.join(dir, 'claude');
    fs.writeFileSync(bin,
      `#!/usr/bin/env bash\nif [ "$1" = "plugin" ] && [ "$2" = "list" ]; then\n  echo "${pluginListStdout}"\n  exit 0\nfi\nexit 1\n`);
    fs.chmodSync(bin, 0o755);
  }
  return dir;
}

// PATH deliberately excludes wherever the host might have a real `claude`
// installed (npm global bin, ~/.local/bin, etc.) so every test's outcome
// depends only on the fixtures it sets up, never on the machine running it.
function controlledEnv(cfg, extraPathDir) {
  const sysDirs = IS_WIN
    ? [process.env.SystemRoot ? path.join(process.env.SystemRoot, 'System32') : 'C:\\Windows\\System32']
    : ['/usr/bin', '/bin'];
  const PATH = [extraPathDir, path.dirname(process.execPath), ...sysDirs].filter(Boolean).join(path.delimiter);
  return { ...process.env, CLAUDE_CONFIG_DIR: cfg, PATH };
}

function doctor(cfg, extraPathDir) {
  return spawnSync(process.execPath, [INSTALLER, '--doctor', '--no-color', '--config-dir', cfg],
    { encoding: 'utf8', env: controlledEnv(cfg, extraPathDir) });
}

// Snapshot every file path + content hash under a dir, for read-only checks.
function snapshot(dir) {
  const out = new Map();
  const walk = (d) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, e.name);
      if (e.isDirectory()) walk(p);
      else out.set(p, fs.readFileSync(p, 'utf8'));
    }
  };
  walk(dir);
  return out;
}

// ── Standalone mode ─────────────────────────────────────────────────────

test('doctor exits 0 on a healthy standalone install', () => {
  const cfg = healthyStandaloneInstall();
  const r = doctor(cfg);
  assert.equal(r.status, 0, `stdout:\n${r.stdout}\nstderr:\n${r.stderr}`);
});

test('doctor exits 1 and names the file when a hook file is missing', () => {
  const cfg = healthyStandaloneInstall();
  fs.unlinkSync(path.join(cfg, 'hooks', 'caveman-parse.js'));
  const r = doctor(cfg);
  assert.equal(r.status, 1);
  assert.match(r.stdout + r.stderr, /caveman-parse\.js/);
});

test('doctor exits 1 and names the file when a hook file was modified', () => {
  const cfg = healthyStandaloneInstall();
  fs.appendFileSync(path.join(cfg, 'hooks', 'caveman-stats.js'), '\n// local edit\n');
  const r = doctor(cfg);
  assert.equal(r.status, 1);
  assert.match(r.stdout + r.stderr, /caveman-stats\.js/);
});

test('doctor exits 1 when settings.json is unparseable', () => {
  const cfg = healthyStandaloneInstall();
  fs.writeFileSync(path.join(cfg, 'settings.json'), '{ definitely not json');
  const r = doctor(cfg);
  assert.equal(r.status, 1);
  assert.match(r.stdout + r.stderr, /settings\.json/);
});

test('doctor exits 1 when the SessionStart wiring is absent (no plugin)', () => {
  const cfg = healthyStandaloneInstall();
  const p = path.join(cfg, 'settings.json');
  const settings = JSON.parse(fs.readFileSync(p, 'utf8'));
  delete settings.hooks.SessionStart;
  fs.writeFileSync(p, JSON.stringify(settings, null, 2));
  const r = doctor(cfg);
  assert.equal(r.status, 1);
  assert.match(r.stdout + r.stderr, /SessionStart/);
});

test('doctor exits 1 when the UserPromptSubmit wiring is absent (no plugin)', () => {
  const cfg = healthyStandaloneInstall();
  const p = path.join(cfg, 'settings.json');
  const settings = JSON.parse(fs.readFileSync(p, 'utf8'));
  delete settings.hooks.UserPromptSubmit;
  fs.writeFileSync(p, JSON.stringify(settings, null, 2));
  const r = doctor(cfg);
  assert.equal(r.status, 1);
  assert.match(r.stdout + r.stderr, /UserPromptSubmit/);
});

test('doctor exits 1 on a wired hook whose target script is gone (orphan)', () => {
  const cfg = healthyStandaloneInstall();
  // Migration-to-plugin scenario: hook files removed, settings left behind,
  // and (for this test) no plugin actually detected either — pure orphan.
  fs.rmSync(path.join(cfg, 'hooks'), { recursive: true });
  const r = doctor(cfg);
  assert.equal(r.status, 1);
  assert.match(r.stdout + r.stderr, /caveman-activate\.js/);
});

test('doctor does not treat a user script containing a managed name as ours', () => {
  const cfg = healthyStandaloneInstall();
  const p = path.join(cfg, 'settings.json');
  const settings = JSON.parse(fs.readFileSync(p, 'utf8'));
  settings.hooks.PostToolUse = [{ hooks: [{ type: 'command', command: `node "${path.join(cfg, 'mycaveman-activate.js')}"`, timeout: 5 }] }];
  fs.writeFileSync(p, JSON.stringify(settings, null, 2));
  const r = doctor(cfg);
  assert.equal(r.status, 0, `stdout:\n${r.stdout}`);
});

test('a bare-node command on an existing script is not a problem', () => {
  const cfg = healthyStandaloneInstall();
  const p = path.join(cfg, 'settings.json');
  const settings = JSON.parse(fs.readFileSync(p, 'utf8'));
  settings.hooks.SessionStart[0].hooks[0].command = `node "${path.join(cfg, 'hooks', 'caveman-activate.js')}"`;
  fs.writeFileSync(p, JSON.stringify(settings, null, 2));
  const r = doctor(cfg);
  assert.equal(r.status, 0, `stdout:\n${r.stdout}`);
});

test('doctor exits 1 on a completely empty config dir', () => {
  const cfg = freshTmpDir();
  const r = doctor(cfg);
  assert.equal(r.status, 1);
});

test('doctor is read-only: a broken install is left byte-for-byte untouched', () => {
  const cfg = healthyStandaloneInstall();
  fs.unlinkSync(path.join(cfg, 'hooks', 'caveman-parse.js'));
  fs.appendFileSync(path.join(cfg, 'hooks', 'caveman-stats.js'), '\n// local edit\n');
  const before = snapshot(cfg);
  const r = doctor(cfg);
  assert.equal(r.status, 1);
  const after = snapshot(cfg);
  assert.deepEqual([...after.keys()].sort(), [...before.keys()].sort());
  for (const [p, contents] of before) assert.equal(after.get(p), contents, `changed: ${p}`);
});

test('doctor respects --config-dir over CLAUDE_CONFIG_DIR', () => {
  const good = healthyStandaloneInstall();
  const bad = freshTmpDir();
  const r = spawnSync(process.execPath, [INSTALLER, '--doctor', '--no-color', '--config-dir', good],
    { encoding: 'utf8', env: controlledEnv(bad) });
  assert.equal(r.status, 0, `stdout:\n${r.stdout}\nstderr:\n${r.stderr}`);
});

// ── Plugin mode ────────────────────────────────────────────────────────

test('doctor exits 0 on a healthy plugin-only install (no standalone files at all)', () => {
  const cfg = freshTmpDir(); // nothing on disk — plugin owns its own hooks
  const claudeBin = fakeClaudeDir('caveman@caveman  1.4.0  active');
  const r = doctor(cfg, claudeBin);
  assert.equal(r.status, 0, `stdout:\n${r.stdout}\nstderr:\n${r.stderr}`);
});

test('doctor exits 1 when the plugin is installed and standalone hooks are also wired', () => {
  const cfg = healthyStandaloneInstall();
  const claudeBin = fakeClaudeDir('caveman@caveman  1.4.0  active');
  const r = doctor(cfg, claudeBin);
  assert.equal(r.status, 1);
  // Behavior only: the report must call out the plugin conflict specifically
  // (not just "something's wired wrong"), but the exact wording — whether it
  // says "twice", "duplicate", "both fire", etc. — is the solution's choice.
  assert.match(r.stdout + r.stderr, /plugin/i);
});

test('doctor exits 1 for a double-wired install even if only one of the two events is standalone-wired', () => {
  const cfg = healthyStandaloneInstall();
  const p = path.join(cfg, 'settings.json');
  const settings = JSON.parse(fs.readFileSync(p, 'utf8'));
  delete settings.hooks.UserPromptSubmit; // only SessionStart still double-wired
  fs.writeFileSync(p, JSON.stringify(settings, null, 2));
  const claudeBin = fakeClaudeDir('caveman@caveman  1.4.0  active');
  const r = doctor(cfg, claudeBin);
  assert.equal(r.status, 1);
  assert.match(r.stdout + r.stderr, /plugin/i);
});

test('doctor does not flag double-wiring when claude is installed but the caveman plugin is not', () => {
  const cfg = healthyStandaloneInstall(); // legitimate standalone install
  const claudeBin = fakeClaudeDir('some-other-plugin@acme  2.0.0  active');
  const r = doctor(cfg, claudeBin);
  assert.equal(r.status, 0, `stdout:\n${r.stdout}`);
});

test('doctor does not require standalone hook files or wiring when the plugin is installed', () => {
  const cfg = freshTmpDir();
  fs.writeFileSync(path.join(cfg, 'settings.json'), JSON.stringify({ statusLine: null }, null, 2));
  const claudeBin = fakeClaudeDir('caveman@caveman  1.4.0  active');
  const r = doctor(cfg, claudeBin);
  assert.equal(r.status, 0, `stdout:\n${r.stdout}`);
});

test('doctor still flags an orphaned hook in plugin mode (leftover from a pre-plugin install)', () => {
  const cfg = freshTmpDir();
  const node = process.execPath;
  const settings = {
    hooks: {
      // Left behind by an old standalone install whose hook files were
      // removed when the user migrated to the plugin (#471) — the plugin
      // itself never wires SessionStart/UserPromptSubmit through this path,
      // so this entry is pure debris, not a double-wiring case.
      PreToolUse: [{ hooks: [{ type: 'command', command: `"${node}" "${path.join(cfg, 'hooks', 'caveman-activate.js')}"`, timeout: 5 }] }],
    },
  };
  fs.writeFileSync(path.join(cfg, 'settings.json'), JSON.stringify(settings, null, 2));
  const claudeBin = fakeClaudeDir('caveman@caveman  1.4.0  active');
  const r = doctor(cfg, claudeBin);
  assert.equal(r.status, 1);
  assert.match(r.stdout + r.stderr, /caveman-activate\.js/);
});

test('doctor treats a broken `claude` CLI probe as no plugin installed, not a crash', () => {
  // claude exists on PATH but exits non-zero / prints nothing usable.
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-badbin-'));
  const bin = path.join(dir, IS_WIN ? 'claude.cmd' : 'claude');
  fs.writeFileSync(bin, IS_WIN ? '@echo off\r\nexit /b 1\r\n' : '#!/usr/bin/env bash\nexit 1\n');
  if (!IS_WIN) fs.chmodSync(bin, 0o755);
  const cfg = healthyStandaloneInstall();
  const r = doctor(cfg, dir);
  assert.equal(r.status, 0, `stdout:\n${r.stdout}\nstderr:\n${r.stderr}`);
});
