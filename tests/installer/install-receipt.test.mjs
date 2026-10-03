// Installer transaction receipt: last-run verify / revert.
//
// Isolated via $XDG_CONFIG_HOME so we never touch the developer's real
// ~/.config/caveman/last-install.json.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import child_process from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'bin', 'install.js');
const RECEIPT = require(path.join(REPO_ROOT, 'bin', 'lib', 'install-receipt.js'));
const SETTINGS = require(path.join(REPO_ROOT, 'bin', 'lib', 'settings.js'));

function withXdg(fn) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-receipt-'));
  const prev = process.env.XDG_CONFIG_HOME;
  process.env.XDG_CONFIG_HOME = dir;
  try {
    return fn(dir);
  } finally {
    if (prev === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = prev;
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

function runInstaller(args, xdg) {
  return spawnSync(process.execPath, [INSTALLER, ...args], {
    encoding: 'utf8',
    env: { ...process.env, XDG_CONFIG_HOME: xdg, NO_COLOR: '1' },
  });
}

function writeThree(dir) {
  const files = [];
  for (const name of ['a.txt', 'b.txt', 'c.txt']) {
    const p = path.join(dir, name);
    fs.writeFileSync(p, name + '\n');
    files.push(p);
  }
  return files;
}

test('beginReceipt + three writes + writeReceipt produces version 1 JSON with mode 0600', () => {
  withXdg((xdg) => {
    const work = path.join(xdg, 'work');
    fs.mkdirSync(work);
    const files = writeThree(work);
    const receipt = RECEIPT.beginReceipt({
      argv: ['--only', 'claude'],
      pinnedRef: 'v2.2.0',
      configDir: work,
    });
    for (const p of files) RECEIPT.recordWrite(receipt, { path: p, created: true });
    const dest = RECEIPT.writeReceipt(receipt);
    const raw = fs.readFileSync(dest, 'utf8');
    const json = JSON.parse(raw);
    assert.equal(json.version, 1);
    assert.match(json.id, /^\d{8}T\d{6}Z-[0-9a-f]{8}$/);
    assert.deepEqual(json.argv, ['--only', 'claude']);
    assert.equal(json.pinnedRef, 'v2.2.0');
    assert.equal(json.entries.length, 3);
    for (const entry of json.entries) {
      assert.equal(entry.op, 'write');
      assert.equal(entry.created, true);
      assert.match(entry.sha256, /^[0-9a-f]{64}$/);
      assert.equal(path.resolve(entry.path), entry.path);
      assert.ok(!entry.path.includes('\0'));
    }
    const st = fs.statSync(dest);
    if (process.platform !== 'win32') {
      assert.equal(st.mode & 0o777, 0o600);
    }
  });
});

test('symlink receipt path is refused', { skip: process.platform === 'win32' }, () => {
  withXdg((xdg) => {
    const cavemanDir = path.join(xdg, 'caveman');
    fs.mkdirSync(cavemanDir, { recursive: true });
    const other = path.join(xdg, 'other.json');
    fs.writeFileSync(other, '{}\n');
    fs.symlinkSync(other, path.join(cavemanDir, 'last-install.json'));
    const receipt = RECEIPT.beginReceipt({ argv: [], pinnedRef: 't', configDir: xdg });
    assert.throws(() => RECEIPT.writeReceipt(receipt), /symlink/);
  });
});

test('symlink receipt parent is refused', { skip: process.platform === 'win32' }, () => {
  withXdg((xdg) => {
    const real = path.join(xdg, 'real-caveman');
    fs.mkdirSync(real);
    fs.symlinkSync(real, path.join(xdg, 'caveman'));
    const receipt = RECEIPT.beginReceipt({ argv: [], pinnedRef: 't', configDir: xdg });
    assert.throws(() => RECEIPT.writeReceipt(receipt), /symlink/);
  });
});

test('verifyReceipt ok / missing / hash-mismatch', () => {
  withXdg((xdg) => {
    const work = path.join(xdg, 'work');
    fs.mkdirSync(work);
    const okPath = path.join(work, 'ok.txt');
    const missingPath = path.join(work, 'missing.txt');
    const mismatchPath = path.join(work, 'mismatch.txt');
    fs.writeFileSync(okPath, 'ok\n');
    fs.writeFileSync(missingPath, 'gone\n');
    fs.writeFileSync(mismatchPath, 'before\n');
    const receipt = RECEIPT.beginReceipt({ argv: [], pinnedRef: 't', configDir: work });
    RECEIPT.recordWrite(receipt, { path: okPath, created: true });
    RECEIPT.recordWrite(receipt, { path: missingPath, created: true });
    RECEIPT.recordWrite(receipt, { path: mismatchPath, created: true });
    fs.writeFileSync(mismatchPath, 'after\n');
    fs.unlinkSync(missingPath);
    const silent = { write() {}, warn() {} };
    const result = RECEIPT.verifyReceipt(receipt, { log: silent, settings: SETTINGS });
    assert.equal(result.ok, false);
    assert.deepEqual(result.results.map(r => r.status), ['ok', 'missing', 'hash-mismatch']);
  });
});

test('verifyReceipt reports symlink', { skip: process.platform === 'win32' }, () => {
  withXdg((xdg) => {
    const work = path.join(xdg, 'work');
    fs.mkdirSync(work);
    const okPath = path.join(work, 'ok.txt');
    const linkPath = path.join(work, 'link.txt');
    fs.writeFileSync(okPath, 'ok\n');
    fs.symlinkSync(okPath, linkPath);
    const receipt = {
      version: 1,
      entries: [{
        op: 'write',
        path: linkPath,
        sha256: RECEIPT.sha256File(okPath),
        created: true,
      }],
    };
    const silent = { write() {}, warn() {} };
    const result = RECEIPT.verifyReceipt(receipt, { log: silent, settings: SETTINGS });
    assert.equal(result.ok, false);
    assert.deepEqual(result.results.map(r => r.status), ['symlink']);
  });
});

test('revert deletes a created: true write whose hash matches', () => {
  withXdg((xdg) => {
    const p = path.join(xdg, 'hook.js');
    fs.writeFileSync(p, 'exports.x = 1;\n');
    const receipt = RECEIPT.beginReceipt({ argv: [], pinnedRef: 't', configDir: xdg });
    RECEIPT.recordWrite(receipt, { path: p, created: true });
    const silent = { write() {}, warn() {} };
    const r = RECEIPT.revertReceipt(receipt, { log: silent });
    assert.equal(r.ok, true);
    assert.equal(fs.existsSync(p), false);
  });
});

test('revert refuses to delete a created: true write whose bytes changed', () => {
  withXdg((xdg) => {
    const p = path.join(xdg, 'hook.js');
    fs.writeFileSync(p, 'original\n');
    const receipt = RECEIPT.beginReceipt({ argv: [], pinnedRef: 't', configDir: xdg });
    RECEIPT.recordWrite(receipt, { path: p, created: true });
    fs.writeFileSync(p, 'user edited\n');
    const warnings = [];
    const r = RECEIPT.revertReceipt(receipt, {
      log: { write() {}, warn: (s) => warnings.push(s) },
    });
    assert.equal(r.ok, true);
    assert.equal(fs.readFileSync(p, 'utf8'), 'user edited\n');
    assert.ok(warnings.some(w => /hash mismatch/i.test(w)));
  });
});

test('revert restores previousSha256 when current hash still matches', () => {
  withXdg((xdg) => {
    const p = path.join(xdg, 'file.txt');
    fs.writeFileSync(p, 'old bytes\n');
    const previousBytes = fs.readFileSync(p);
    const previousSha256 = RECEIPT.sha256Bytes(previousBytes);
    fs.writeFileSync(p, 'new bytes\n');
    const receipt = RECEIPT.beginReceipt({ argv: [], pinnedRef: 't', configDir: xdg });
    RECEIPT.recordWrite(receipt, {
      path: p,
      created: false,
      previousSha256,
      previousBytes,
    });
    const silent = { write() {}, warn() {} };
    const r = RECEIPT.revertReceipt(receipt, { log: silent });
    assert.equal(r.ok, true);
    assert.equal(fs.readFileSync(p, 'utf8'), 'old bytes\n');
  });
});

test('revert of merge uses removeCavemanHooks and does not wipe unrelated hooks', () => {
  withXdg((xdg) => {
    const settingsPath = path.join(xdg, 'settings.json');
    const settings = { hooks: {} };
    SETTINGS.addCommandHook(settings, 'SessionStart', {
      command: 'node /tmp/caveman-activate.js',
      marker: 'caveman-activate',
    });
    settings.hooks.SessionStart.push({
      hooks: [{ type: 'command', command: 'node /tmp/foreign-hook.js' }],
    });
    SETTINGS.writeSettings(settingsPath, settings);
    const receipt = RECEIPT.beginReceipt({ argv: [], pinnedRef: 't', configDir: xdg });
    RECEIPT.recordMerge(receipt, {
      path: settingsPath,
      marker: 'caveman',
      keys: ['hooks.SessionStart'],
    });
    const silent = { write() {}, warn() {} };
    const r = RECEIPT.revertReceipt(receipt, { log: silent, settings: SETTINGS });
    assert.equal(r.ok, true);
    const next = SETTINGS.readSettings(settingsPath);
    assert.equal(SETTINGS.hasCavemanHook(next, 'SessionStart', 'caveman-activate'), false);
    const remaining = JSON.stringify(next);
    assert.match(remaining, /foreign-hook\.js/);
  });
});

test('revert of fence strips only the marked block', () => {
  withXdg((xdg) => {
    const p = path.join(xdg, 'SOUL.md');
    const begin = RECEIPT.DEFAULT_BEGIN;
    const end = RECEIPT.DEFAULT_END;
    fs.writeFileSync(p, [
      '# User voice',
      '',
      'Keep this.',
      '',
      begin,
      'Respond terse like smart caveman',
      end,
      '',
      'Also keep this.',
      '',
    ].join('\n'));
    const receipt = RECEIPT.beginReceipt({ argv: [], pinnedRef: 't', configDir: xdg });
    RECEIPT.recordFence(receipt, { path: p, begin, end });
    const silent = { write() {}, warn() {} };
    const r = RECEIPT.revertReceipt(receipt, { log: silent });
    assert.equal(r.ok, true);
    const next = fs.readFileSync(p, 'utf8');
    assert.match(next, /Keep this/);
    assert.match(next, /Also keep this/);
    assert.doesNotMatch(next, /caveman-begin/);
    assert.doesNotMatch(next, /smart caveman/);
  });
});

test('command entries never spawn', () => {
  let spawned = 0;
  const orig = {
    spawnSync: child_process.spawnSync,
    spawn: child_process.spawn,
    execSync: child_process.execSync,
    execFileSync: child_process.execFileSync,
    execFile: child_process.execFile,
    exec: child_process.exec,
  };
  const bump = (...args) => {
    spawned++;
    throw new Error('spawned during revert: ' + JSON.stringify(args[0]));
  };
  child_process.spawnSync = bump;
  child_process.spawn = bump;
  child_process.execSync = bump;
  child_process.execFileSync = bump;
  child_process.execFile = bump;
  child_process.exec = bump;
  try {
    const receipt = RECEIPT.beginReceipt({ argv: [], pinnedRef: 't', configDir: '/tmp' });
    RECEIPT.recordCommand(receipt, {
      argv: ['npx', '-y', 'skills', 'remove', 'caveman'],
      revert: 'manual',
    });
    const lines = [];
    const r = RECEIPT.revertReceipt(receipt, {
      log: { write: (s) => lines.push(s), warn() {} },
    });
    assert.equal(r.ok, true);
    assert.equal(spawned, 0);
    assert.ok(lines.some(l => /manual/.test(l) && /npx/.test(l)));
    assert.ok(!lines.some(l => /skills remove/.test(l) && /would run/.test(l)));
  } finally {
    Object.assign(child_process, orig);
  }
});

test('--verify-last with no file exits 2', () => {
  withXdg((xdg) => {
    const r = runInstaller(['--verify-last'], xdg);
    assert.equal(r.status, 2, r.stderr || r.stdout);
    assert.match(r.stderr, /no last-install receipt/);
  });
});

test('--revert-last then a second --revert-last exits 2 (receipt renamed)', () => {
  withXdg((xdg) => {
    const p = path.join(xdg, 'hook.js');
    fs.writeFileSync(p, 'exports.x = 1;\n');
    const receipt = RECEIPT.beginReceipt({ argv: [], pinnedRef: 't', configDir: xdg });
    RECEIPT.recordWrite(receipt, { path: p, created: true });
    RECEIPT.writeReceipt(receipt);
    const first = runInstaller(['--revert-last'], xdg);
    assert.equal(first.status, 0, first.stderr || first.stdout);
    assert.equal(fs.existsSync(p), false);
    assert.equal(fs.existsSync(RECEIPT.receiptPath()), false);
    assert.ok(fs.existsSync(RECEIPT.revertedReceiptPath()));
    const second = runInstaller(['--revert-last'], xdg);
    assert.equal(second.status, 2, second.stderr || second.stdout);
  });
});
