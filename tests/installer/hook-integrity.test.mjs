// Detached installs (curl|bash, no clone) download the hooks from the pinned
// ref and must verify every file against src/hooks/checksums.sha256 there.
// A missing manifest or a mismatched file aborts before hooksDir or
// settings.json changes. A fake `curl` on PATH serves a fixture directory.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const HOOKS = path.join(REPO_ROOT, 'src', 'hooks');
const HOOK_FILES = fs.readFileSync(path.join(HOOKS, 'checksums.sha256'), 'utf8')
  .split('\n').filter(Boolean).map((line) => line.split(/\s+/)[1]);

function setup({ manifest = true, tamper = null } = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-hook-integrity-'));
  // Detached copy of the installer: no src/hooks beside it, so every hook is remote.
  fs.cpSync(path.join(REPO_ROOT, 'bin'), path.join(root, 'bin'), { recursive: true });
  const served = path.join(root, 'served');
  fs.mkdirSync(served);
  for (const f of HOOK_FILES) fs.copyFileSync(path.join(HOOKS, f), path.join(served, f));
  if (manifest) {
    fs.writeFileSync(path.join(served, 'checksums.sha256'), HOOK_FILES.map((f) =>
      `${createHash('sha256').update(fs.readFileSync(path.join(served, f))).digest('hex')}  ${f}\n`).join(''));
  }
  if (tamper) fs.appendFileSync(path.join(served, tamper), '\n// tampered\n');
  const fakeBin = path.join(root, 'fake-bin');
  fs.mkdirSync(fakeBin);
  fs.writeFileSync(path.join(fakeBin, 'claude'), '#!/bin/sh\nexit 0\n', { mode: 0o755 });
  fs.writeFileSync(path.join(fakeBin, 'curl'), `#!/bin/sh
out=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in -o) out="$2"; shift 2 ;; -*) shift ;; *) url="$1"; shift ;; esac
done
case "$url" in */src/hooks/*) f="${served}/\${url##*/}"; [ -f "$f" ] && cp "$f" "$out" && exit 0 ;; esac
exit 22
`, { mode: 0o755 });
  const configDir = path.join(root, 'claude');
  fs.mkdirSync(path.join(configDir, 'hooks'), { recursive: true });
  fs.writeFileSync(path.join(configDir, 'settings.json'), '{"theme":"dark"}\n');
  fs.writeFileSync(path.join(configDir, 'hooks', 'caveman-config.js'), '// previous install\n');
  const run = () => spawnSync(process.execPath, [
    path.join(root, 'bin', 'install.js'),
    '--only', 'claude', '--with-hooks', '--config-dir', configDir, '--non-interactive', '--no-mcp-shrink',
  ], {
    env: { ...process.env, PATH: `${fakeBin}${path.delimiter}${process.env.PATH}`, CLAUDE_CONFIG_DIR: configDir, NO_COLOR: '1' },
    encoding: 'utf8',
  });
  return { root, configDir, run };
}

function assertUntouched(configDir) {
  assert.equal(fs.readFileSync(path.join(configDir, 'settings.json'), 'utf8'), '{"theme":"dark"}\n');
  assert.equal(fs.readFileSync(path.join(configDir, 'hooks', 'caveman-config.js'), 'utf8'), '// previous install\n');
  assert.equal(fs.existsSync(path.join(configDir, 'hooks', 'caveman-activate.js')), false);
}

const posixOnly = { skip: process.platform === 'win32' && 'fake curl is a POSIX shell script' };

test('missing hook integrity manifest aborts the detached hook install', posixOnly, () => {
  const { root, configDir, run } = setup({ manifest: false });
  try {
    const r = run();
    assert.match(r.stderr, /claude-hooks — no hook integrity manifest at .* refusing to install unverified hooks/);
    assertUntouched(configDir);
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});

test('one mismatched hook aborts before any hook or settings.json changes', posixOnly, () => {
  const { root, configDir, run } = setup({ tamper: 'caveman-activate.js' });
  try {
    const r = run();
    assert.match(r.stderr, /claude-hooks — integrity check failed for caveman-activate\.js/);
    assertUntouched(configDir);
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});

test('verified detached hook install lands every file and wires settings.json', posixOnly, () => {
  const { root, configDir, run } = setup();
  try {
    const r = run();
    assert.equal(r.status, 0, r.stdout + r.stderr);
    for (const f of HOOK_FILES) {
      assert.deepEqual(fs.readFileSync(path.join(configDir, 'hooks', f)), fs.readFileSync(path.join(HOOKS, f)), f);
    }
    assert.match(fs.readFileSync(path.join(configDir, 'settings.json'), 'utf8'), /caveman-activate/);
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});

test('hook install replaces a symlinked hook instead of writing through it', posixOnly, () => {
  const { root, configDir, run } = setup();
  try {
    const outside = path.join(root, 'outside.js');
    fs.writeFileSync(outside, '// not caveman\n');
    fs.rmSync(path.join(configDir, 'hooks', 'caveman-config.js'));
    fs.symlinkSync(outside, path.join(configDir, 'hooks', 'caveman-config.js'));
    const r = run();
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.equal(fs.readFileSync(outside, 'utf8'), '// not caveman\n');
    const dest = path.join(configDir, 'hooks', 'caveman-config.js');
    assert.equal(fs.lstatSync(dest).isSymbolicLink(), false);
    assert.deepEqual(fs.readFileSync(dest), fs.readFileSync(path.join(HOOKS, 'caveman-config.js')));
    assert.deepEqual(fs.readdirSync(path.join(configDir, 'hooks')).filter((f) => f.includes('.tmp-')), []);
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});
