import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const require = createRequire(import.meta.url);
const HOOK = require('../../src/hooks/cursor-dedupe-tools.js');

function tempFile(body = 'hello\n') {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-hook-'));
  const file = path.join(dir, 'sample.txt');
  fs.writeFileSync(file, body);
  return { dir, file };
}

test('first Read allowed, second unchanged Read denied in same conversation', () => {
  const { dir, file } = tempFile();
  const state = HOOK.loadState(path.join(dir, 'state'));
  const input = { conversation_id: 'c1', tool_input: { path: file } };

  const first = HOOK.decideRead(input, { state, stateDir: path.join(dir, 'state') });
  assert.equal(first.permission, 'allow');

  const second = HOOK.decideRead(input, { state: first.state, stateDir: path.join(dir, 'state') });
  assert.equal(second.permission, 'deny');
  assert.match(second.agent_message, /Already read/);

  fs.rmSync(dir, { recursive: true, force: true });
});

test('edited file allows another full Read in same conversation', () => {
  const { dir, file } = tempFile('v1\n');
  let state = { version: 1, conversations: {}, fingerprints: {} };
  const input = { conversation_id: 'c1', tool_input: { path: file } };

  const first = HOOK.decideRead(input, { state, stateDir: path.join(dir, 'state') });
  fs.appendFileSync(file, 'v2\n');
  const second = HOOK.decideRead(input, { state: first.state, stateDir: path.join(dir, 'state') });
  assert.equal(second.permission, 'allow');

  fs.rmSync(dir, { recursive: true, force: true });
});

test('ranged Read always allowed', () => {
  const { dir, file } = tempFile();
  const norm = process.platform === 'win32' ? file.toLowerCase() : file;
  const state = {
    version: 1,
    conversations: { c1: { reads: { [norm]: { mtimeMs: 1, hash: 'x' } }, shell: {}, crossWarned: {} } },
    fingerprints: {},
  };
  const input = { conversation_id: 'c1', tool_input: { path: file, offset: 1, limit: 5 } };
  const result = HOOK.decideRead(input, { state, stateDir: path.join(dir, 'state') });
  assert.equal(result.permission, 'allow');
  fs.rmSync(dir, { recursive: true, force: true });
});

test('new chat denies one full Read when fingerprint matches, then allows', () => {
  const { dir, file } = tempFile('stable\n');
  const stateDir = path.join(dir, 'state');
  let state = { version: 1, conversations: {}, fingerprints: {} };

  const convA = HOOK.decideRead(
    { conversation_id: 'chat-a', tool_input: { path: file } },
    { state, stateDir },
  );
  assert.equal(convA.permission, 'allow');

  const convBFirst = HOOK.decideRead(
    { conversation_id: 'chat-b', tool_input: { path: file } },
    { state: convA.state, stateDir },
  );
  assert.equal(convBFirst.permission, 'deny');
  assert.match(convBFirst.agent_message, /previous chat/);

  const convBSecond = HOOK.decideRead(
    { conversation_id: 'chat-b', tool_input: { path: file } },
    { state: convBFirst.state, stateDir },
  );
  assert.equal(convBSecond.permission, 'allow');

  fs.rmSync(dir, { recursive: true, force: true });
});

test('repeat full test suite denied in same conversation', () => {
  const state = { version: 1, conversations: {}, fingerprints: {} };
  const input = { conversation_id: 'c1', command: 'npm test' };
  const first = HOOK.decideShell(input, { state, stateDir: '/tmp/unused' });
  assert.equal(first.permission, 'allow');
  const second = HOOK.decideShell(input, { state: first.state, stateDir: '/tmp/unused' });
  assert.equal(second.permission, 'deny');
});
