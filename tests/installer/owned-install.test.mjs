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
