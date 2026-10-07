import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const HOOK = require('../../src/hooks/cursor-dedupe-tools.js');
const HOOK_SCRIPT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../src/hooks/cursor-dedupe-tools.js');

function tempFile(body = 'hello\n') {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-hook-'));
  const file = path.join(dir, 'sample.txt');
  fs.writeFileSync(file, body);
  return { dir, file };
}

function runHook(mode, payload, env = {}) {
  const res = spawnSync(process.execPath, [HOOK_SCRIPT, mode], {
    input: payload,
    encoding: 'utf8',
    env: { ...process.env, CAVEMAN_CURSOR_HOOK_NO_SAVE: '1', ...env },
  });
  return JSON.parse(res.stdout);
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

test('covered ranged Read denied; wider range allowed', () => {
  const { dir, file } = tempFile('line1\nline2\nline3\nline4\nline5\n');
  const stateDir = path.join(dir, 'state');
  let state = HOOK.loadState(stateDir);
  const narrow = { conversation_id: 'c1', tool_input: { path: file, offset: 2, limit: 2 } };
  const first = HOOK.decideRead(narrow, { state, stateDir, cwd: dir });
  assert.equal(first.permission, 'allow');

  const repeat = HOOK.decideRead(narrow, { state: first.state, stateDir, cwd: dir });
  assert.equal(repeat.permission, 'deny');

  const wider = { conversation_id: 'c1', tool_input: { path: file, offset: 2, limit: 4 } };
  const third = HOOK.decideRead(wider, { state: repeat.state, stateDir, cwd: dir });
  assert.equal(third.permission, 'allow');

  fs.rmSync(dir, { recursive: true, force: true });
});

test('same range allowed after file hash changes', () => {
  const { dir, file } = tempFile('a\nb\nc\n');
  const stateDir = path.join(dir, 'state');
  let state = HOOK.loadState(stateDir);
  const ranged = { conversation_id: 'c1', tool_input: { path: file, offset: 1, limit: 1 } };
  const first = HOOK.decideRead(ranged, { state, stateDir, cwd: dir });
  fs.appendFileSync(file, 'd\n');
  const second = HOOK.decideRead(ranged, { state: first.state, stateDir, cwd: dir });
  assert.equal(second.permission, 'allow');
  fs.rmSync(dir, { recursive: true, force: true });
});

test('full Read records coverage for later subset deny', () => {
  const { dir, file } = tempFile('one\ntwo\nthree\n');
  const stateDir = path.join(dir, 'state');
  let state = HOOK.loadState(stateDir);
  const full = { conversation_id: 'c1', tool_input: { path: file } };
  const first = HOOK.decideRead(full, { state, stateDir, cwd: dir });
  const subset = { conversation_id: 'c1', tool_input: { path: file, offset: 2, limit: 1 } };
  const second = HOOK.decideRead(subset, { state: first.state, stateDir, cwd: dir });
  assert.equal(second.permission, 'deny');
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

test('identical Grep denied; changed pattern allowed', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-grep-'));
  const file = path.join(dir, 'a.txt');
  fs.writeFileSync(file, 'needle\n');
  const stateDir = path.join(dir, 'state');
  let state = HOOK.loadState(stateDir);
  const base = { conversation_id: 'c1', tool_input: { pattern: 'needle', path: dir } };
  const first = HOOK.decideGrep(base, { state, stateDir, cwd: dir });
  assert.equal(first.permission, 'allow');
  const second = HOOK.decideGrep(base, { state: first.state, stateDir, cwd: dir });
  assert.equal(second.permission, 'deny');
  const changed = HOOK.decideGrep(
    { conversation_id: 'c1', tool_input: { pattern: 'other', path: dir } },
    { state: second.state, stateDir, cwd: dir },
  );
  assert.equal(changed.permission, 'allow');
  fs.rmSync(dir, { recursive: true, force: true });
});

test('Grep allowed after file under search root changes', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-grep-'));
  const file = path.join(dir, 'a.txt');
  fs.writeFileSync(file, 'x\n');
  const stateDir = path.join(dir, 'state');
  let state = HOOK.loadState(stateDir);
  const query = { conversation_id: 'c1', tool_input: { pattern: 'x', path: dir } };
  const first = HOOK.decideGrep(query, { state, stateDir, cwd: dir });
  fs.appendFileSync(file, 'y\n');
  const second = HOOK.decideGrep(query, { state: first.state, stateDir, cwd: dir });
  assert.equal(second.permission, 'allow');
  fs.rmSync(dir, { recursive: true, force: true });
});

test('Glob dedupe is scoped per conversation', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-glob-'));
  fs.writeFileSync(path.join(dir, 'one.js'), '//');
  const stateDir = path.join(dir, 'state');
  let state = HOOK.loadState(stateDir);
  const query = { conversation_id: 'c1', tool_input: { glob_pattern: '*.js', path: dir } };
  const first = HOOK.decideGlob(query, { state, stateDir, cwd: dir });
  const second = HOOK.decideGlob(query, { state: first.state, stateDir, cwd: dir });
  assert.equal(second.permission, 'deny');
  const otherChat = HOOK.decideGlob(
    { conversation_id: 'c2', tool_input: { glob_pattern: '*.js', path: dir } },
    { state: second.state, stateDir, cwd: dir },
  );
  assert.equal(otherChat.permission, 'allow');
  fs.rmSync(dir, { recursive: true, force: true });
});

test('exact git status denied only while fingerprint matches', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-git-'));
  spawnSync('git', ['init'], { cwd: dir });
  fs.writeFileSync(path.join(dir, 'README'), 'hi\n');
  spawnSync('git', ['add', 'README'], { cwd: dir });
  spawnSync('git', ['commit', '-m', 'init'], { cwd: dir });
  const stateDir = path.join(dir, 'state');
  let state = HOOK.loadState(stateDir);
  const input = { conversation_id: 'c1', command: 'git status' };
  const first = HOOK.decideShell(input, { state, stateDir, cwd: dir });
  assert.equal(first.permission, 'allow');
  const second = HOOK.decideShell(input, { state: first.state, stateDir, cwd: dir });
  assert.equal(second.permission, 'deny');
  fs.appendFileSync(path.join(dir, 'README'), 'more\n');
  const third = HOOK.decideShell(input, { state: second.state, stateDir, cwd: dir });
  assert.equal(third.permission, 'allow');
  fs.rmSync(dir, { recursive: true, force: true });
});

test('git commands with flags and npm install stay allowed', () => {
  const state = { version: 1, conversations: { c1: { reads: {}, shell: { 'git status': 'abc:1' }, crossWarned: {}, searches: {} } }, fingerprints: {} };
  assert.equal(HOOK.shellKey('git status -sb'), null);
  assert.equal(HOOK.decideShell({ conversation_id: 'c1', command: 'git status -sb' }, { state, stateDir: '/tmp' }).permission, 'allow');
  assert.equal(HOOK.decideShell({ conversation_id: 'c1', command: 'npm install' }, { state, stateDir: '/tmp' }).permission, 'allow');
});

test('trimConversations drops oldest and keeps recent denies', () => {
  const state = { version: 1, conversations: {}, fingerprints: {} };
  for (let i = 0; i < HOOK.MAX_CONVERSATIONS + 2; i++) {
    state.conversations[`old-${i}`] = { reads: {}, shell: {}, crossWarned: {}, searches: {} };
  }
  state.conversations.keep = {
    reads: {},
    shell: { 'npm test': true },
    crossWarned: {},
    searches: {},
  };
  const trimmed = HOOK.trimConversations(state);
  assert.equal(Object.keys(trimmed.conversations).length, HOOK.MAX_CONVERSATIONS);
  assert.ok(trimmed.conversations.keep);
  const denied = HOOK.decideShell(
    { conversation_id: 'keep', command: 'npm test' },
    { state: trimmed, stateDir: '/tmp' },
  );
  assert.equal(denied.permission, 'deny');
});

test('hook CLI fails open on invalid JSON and missing path', () => {
  assert.equal(runHook('read', '').permission, 'allow');
  assert.equal(runHook('read', '{not json').permission, 'allow');
  const res = runHook('read', JSON.stringify({ conversation_id: 'c1', tool_input: { path: '/no/such/file' } }));
  assert.equal(res.permission, 'allow');
});

test('saveState enforces conversation cap', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-cap-'));
  const stateDir = path.join(dir, 'state');
  const state = { version: 1, conversations: {}, fingerprints: {} };
  for (let i = 0; i < HOOK.MAX_CONVERSATIONS + 5; i++) {
    state.conversations[`c-${i}`] = { reads: {}, shell: {}, crossWarned: {}, searches: {} };
  }
  HOOK.saveState(stateDir, state);
  const loaded = HOOK.loadState(stateDir);
  assert.equal(Object.keys(loaded.conversations).length, HOOK.MAX_CONVERSATIONS);
  fs.rmSync(dir, { recursive: true, force: true });
});
