import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const require = createRequire(import.meta.url);
const OWNED = require('../../bin/lib/owned-install.js');

function freshRoot() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-owned-'));
}

function fileOperation(relativePath, body, beforeWrite = () => {}) {
  return {
    relativePath,
    write(stage) {
      beforeWrite(stage);
      fs.writeFileSync(stage, body, { flag: 'wx' });
    },
  };
}

test('owned payload upgrades when the installed digest still matches', () => {
  const root = freshRoot();
  try {
    OWNED.installOwned({
      root,
      integration: 'test',
      operations: [fileOperation('payload.txt', 'v1\n')],
    });
    OWNED.installOwned({
      root,
      integration: 'test',
      operations: [fileOperation('payload.txt', 'v2\n')],
    });
    assert.equal(fs.readFileSync(path.join(root, 'payload.txt'), 'utf8'), 'v2\n');
    assert.equal(fs.existsSync(path.join(root, '.caveman-test-backups')), false, 'owned upgrades need no user backup');
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test('journal commit failure restores displaced user bytes', () => {
  const root = freshRoot();
  try {
    const target = path.join(root, 'payload.txt');
    fs.writeFileSync(target, 'user bytes\n');
    const { journalPath } = OWNED.journalPaths(root, 'test');

    assert.throws(() => OWNED.installOwned({
      root,
      integration: 'test',
      force: true,
      operations: [fileOperation('payload.txt', 'managed bytes\n', () => fs.mkdirSync(journalPath))],
    }));

    assert.equal(fs.readFileSync(target, 'utf8'), 'user bytes\n');
    const backupRoot = path.join(root, '.caveman-test-backups');
    assert.deepEqual(fs.readdirSync(backupRoot), [], 'failed transaction must not leave an orphan backup');
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test('broken symbolic-link targets are conflicts even with force', { skip: process.platform === 'win32' }, () => {
  const root = freshRoot();
  try {
    const target = path.join(root, 'payload.txt');
    fs.symlinkSync(path.join(root, 'missing-target'), target);
    assert.throws(() => OWNED.installOwned({
      root,
      integration: 'test',
      force: true,
      operations: [fileOperation('payload.txt', 'managed bytes\n')],
    }), /symbolic links are never overwritten/);
    assert.equal(fs.lstatSync(target).isSymbolicLink(), true);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test('host registration failure retains committed payload and ownership for recovery', () => {
  const root = freshRoot();
  try {
    const options = { root, integration: 'test' };
    OWNED.installOwned({ ...options, operations: [fileOperation('payload.txt', 'v1')] });
    const journalPath = OWNED.journalPaths(root, 'test').journalPath;
    const before = fs.readFileSync(journalPath, 'utf8');
    const upgrade = fileOperation('payload.txt', 'v2');
    upgrade.register = (target) => {
      assert.equal(fs.readFileSync(target, 'utf8'), 'v2');
      throw new Error('host registration failed');
    };
    assert.throws(() => OWNED.installOwned({ ...options, operations: [upgrade] }), /host registration failed/);
    assert.equal(fs.readFileSync(path.join(root, 'payload.txt'), 'utf8'), 'v2');
    assert.notEqual(fs.readFileSync(journalPath, 'utf8'), before);
    assert.equal(JSON.parse(fs.readFileSync(journalPath, 'utf8')).entries['payload.txt'].installedDigest, OWNED.digestPath(path.join(root, 'payload.txt')));
    assert.deepEqual(fs.readdirSync(root).sort(), ['.caveman-test-ownership.json', 'payload.txt']);
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});

test('failed host deregistration retains owned bytes and journal', () => {
  const root = freshRoot();
  try {
    const options = { root, integration: 'test' };
    OWNED.installOwned({ ...options, operations: [fileOperation('payload.txt', 'v1')] });
    const journalPath = OWNED.journalPaths(root, 'test').journalPath;
    const before = fs.readFileSync(journalPath, 'utf8');
    assert.throws(() => OWNED.uninstallOwned({ ...options, unregister() { throw new Error('host unavailable'); } }), /host unavailable/);
    assert.equal(fs.readFileSync(path.join(root, 'payload.txt'), 'utf8'), 'v1');
    assert.equal(fs.readFileSync(journalPath, 'utf8'), before);
    let calls = 0;
    OWNED.uninstallOwned({ ...options, dryRun: true, unregister() { calls++; } });
    assert.equal(calls, 0, 'dry run must never mutate host registration');
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});

test('journal failure never reaches host registration', () => {
  const root = freshRoot();
  try {
    let calls = 0;
    const { journalPath } = OWNED.journalPaths(root, 'test');
    const operation = fileOperation('payload.txt', 'managed', () => fs.mkdirSync(journalPath));
    operation.register = () => { calls++; };
    assert.throws(() => OWNED.installOwned({ root, integration: 'test', operations: [operation] }));
    assert.equal(calls, 0);
    assert.equal(fs.existsSync(path.join(root, 'payload.txt')), false);
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});

test('uninstall refuses a symlinked backup root before unregistering', { skip: process.platform === 'win32' }, () => {
  const root = freshRoot();
  const elsewhere = freshRoot();
  try {
    const target = path.join(root, 'payload.txt');
    fs.writeFileSync(target, 'user bytes');
    OWNED.installOwned({ root, integration: 'test', force: true, operations: [fileOperation('payload.txt', 'managed')] });
    const { journalPath, backupRoot } = OWNED.journalPaths(root, 'test');
    const journal = fs.readFileSync(journalPath, 'utf8');
    const backupName = JSON.parse(journal).entries['payload.txt'].restoreBackup;
    fs.renameSync(path.join(backupRoot, backupName), path.join(elsewhere, backupName));
    fs.rmdirSync(backupRoot);
    fs.symlinkSync(elsewhere, backupRoot);
    let calls = 0;
    assert.throws(() => OWNED.uninstallOwned({ root, integration: 'test', unregister() { calls++; } }), /invalid backup root/);
    assert.equal(calls, 0);
    assert.equal(fs.readFileSync(target, 'utf8'), 'managed');
    assert.equal(fs.readFileSync(path.join(elsewhere, backupName), 'utf8'), 'user bytes');
    assert.equal(fs.readFileSync(journalPath, 'utf8'), journal);
  } finally { fs.rmSync(root, { recursive: true, force: true }); fs.rmSync(elsewhere, { recursive: true, force: true }); }
});
