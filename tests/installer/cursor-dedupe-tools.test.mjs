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

function estimatedTokens(text) {
  return Math.ceil(Buffer.byteLength(String(text), 'utf8') / 4);
}

function readFileRangeText(filePath, offset, limit) {
  const lines = fs.readFileSync(filePath, 'utf8').split('\n');
  const start = Math.max(1, Number(offset) || 1);
  const lim = Number(limit) || lines.length;
  return lines.slice(start - 1, start - 1 + lim).join('\n');
}

function grepPayload(rootDir, pattern) {
  const out = [];
  const visit = (p) => {
    let st;
    try {
      st = fs.statSync(p);
    } catch {
      return;
    }
    if (st.isDirectory()) {
      for (const ent of fs.readdirSync(p, { withFileTypes: true })) {
        if (ent.name === 'node_modules' || ent.name === '.git') continue;
        visit(path.join(p, ent.name));
      }
      return;
    }
    let body;
    try {
      body = fs.readFileSync(p, 'utf8');
    } catch {
      return;
    }
    for (const line of body.split('\n')) {
      if (line.includes(pattern)) out.push(line);
    }
  };
  visit(rootDir);
  return out.join('\n');
}

function globTxtPayload(rootDir) {
  const files = [];
  for (const name of fs.readdirSync(rootDir)) {
    const full = path.join(rootDir, name);
    try {
      if (fs.statSync(full).isFile() && name.endsWith('.txt')) files.push(name);
    } catch {
      /* skip */
    }
  }
  return files.join('\n');
}

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

function charMeasure(text) {
  const chars = String(text).length;
  return { chars, tokens: Math.ceil(chars / 4) };
}

function fmtNum(n) {
  return n.toLocaleString('en-US');
}

function globTestMjsPayload(testsDir, repoRoot, maxFiles = 500) {
  const files = [];
  let count = 0;
  const visit = (p) => {
    if (count >= maxFiles) return;
    let st;
    try {
      st = fs.statSync(p);
    } catch {
      return;
    }
    if (st.isDirectory()) {
      for (const ent of fs.readdirSync(p, { withFileTypes: true })) {
        if (count >= maxFiles) return;
        if (ent.name === 'node_modules' || ent.name === '.git') continue;
        visit(path.join(p, ent.name));
      }
      return;
    }
    count++;
    if (p.endsWith('.test.mjs')) {
      files.push(path.relative(repoRoot, p).split(path.sep).join('/'));
    }
  };
  visit(testsDir);
  return files.sort().join('\n');
}

function grepToolPayload(rootDir, pattern, maxFiles = 500) {
  const out = [];
  let count = 0;
  const visit = (p) => {
    if (count >= maxFiles) return;
    let st;
    try {
      st = fs.statSync(p);
    } catch {
      return;
    }
    if (st.isDirectory()) {
      for (const ent of fs.readdirSync(p, { withFileTypes: true })) {
        if (count >= maxFiles) return;
        if (ent.name === 'node_modules' || ent.name === '.git') continue;
        visit(path.join(p, ent.name));
      }
      return;
    }
    count++;
    let body;
    try {
      body = fs.readFileSync(p, 'utf8');
    } catch {
      return;
    }
    const rel = path.relative(rootDir, p).split(path.sep).join('/');
    for (const line of body.split('\n')) {
      if (line.includes(pattern)) out.push(`${rel}:${line}`);
    }
  };
  visit(rootDir);
  return out.join('\n');
}

function rowFromPayload(label, payloadText, denyMessage) {
  const payload = charMeasure(payloadText);
  const deny = charMeasure(denyMessage);
  const savedTokens = payload.tokens - deny.tokens;
  assert.ok(savedTokens > 0, label);
  return { label, payload, deny, savedTokens };
}

test('first Read allowed, second unchanged Read denied, third allowed in same conversation', () => {
  const { dir, file } = tempFile();
  const state = HOOK.loadState(path.join(dir, 'state'));
  const input = { conversation_id: 'c1', tool_input: { path: file } };
  const stateDir = path.join(dir, 'state');

  const first = HOOK.decideRead(input, { state, stateDir });
  assert.equal(first.permission, 'allow');

  const second = HOOK.decideRead(input, { state: first.state, stateDir });
  assert.equal(second.permission, 'deny');
  assert.match(second.agent_message, /Already read/);
  assert.doesNotMatch(second.agent_message, /offset|limit/i);
  const readKey = process.platform === 'win32' ? file.toLowerCase() : file;
  assert.equal(second.state.conversations.c1.reads[readKey].duplicateDenied, true);

  const third = HOOK.decideRead(input, { state: second.state, stateDir });
  assert.equal(third.permission, 'allow');

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
  assert.doesNotMatch(convBFirst.agent_message, /offset|limit/i);

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
  const gitEnv = {
    ...process.env,
    GIT_AUTHOR_NAME: 'caveman-test',
    GIT_AUTHOR_EMAIL: 'caveman-test@example.com',
    GIT_COMMITTER_NAME: 'caveman-test',
    GIT_COMMITTER_EMAIL: 'caveman-test@example.com',
  };
  const init = spawnSync('git', ['init'], { cwd: dir, env: gitEnv });
  assert.equal(init.status, 0, init.stderr && init.stderr.toString());
  fs.writeFileSync(path.join(dir, 'README'), 'hi\n');
  const add = spawnSync('git', ['add', 'README'], { cwd: dir, env: gitEnv });
  assert.equal(add.status, 0, add.stderr && add.stderr.toString());
  const commit = spawnSync('git', ['commit', '-m', 'init'], { cwd: dir, env: gitEnv });
  assert.equal(commit.status, 0, commit.stderr && commit.stderr.toString());
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

test('mixed session scores dedupe effectiveness and fresh-call correctness', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-mixed-'));
  const file = path.join(dir, 'sample.txt');
  fs.writeFileSync(file, 'line1\nline2\nline3\nline4\nline5\n');
  const stateDir = path.join(dir, 'state');
  let state = HOOK.loadState(stateDir);
  const conv = 'c-mixed';
  const cwd = dir;

  const gitEnv = {
    ...process.env,
    GIT_AUTHOR_NAME: 'caveman-test',
    GIT_AUTHOR_EMAIL: 'caveman-test@example.com',
    GIT_COMMITTER_NAME: 'caveman-test',
    GIT_COMMITTER_EMAIL: 'caveman-test@example.com',
  };
  assert.equal(spawnSync('git', ['init'], { cwd: dir, env: gitEnv }).status, 0);
  fs.writeFileSync(path.join(dir, 'README'), 'hi\n');
  assert.equal(spawnSync('git', ['add', 'README'], { cwd: dir, env: gitEnv }).status, 0);
  assert.equal(spawnSync('git', ['commit', '-m', 'init'], { cwd: dir, env: gitEnv }).status, 0);

  const score = {
    dupCalls: 0,
    dupDenied: 0,
    freshCalls: 0,
    intentionalDupAllow: 0,
    tokensWithoutDedupe: 0,
    tokensWithDedupe: 0,
  };

  function runStep({ label, dup, intentionalAllow, decide, input, denyPattern, toolResultPayload }) {
    const result = decide(input, { state, stateDir, cwd });
    state = result.state;
    let stepTokens = 0;
    if (toolResultPayload !== undefined) {
      const text = typeof toolResultPayload === 'function' ? toolResultPayload() : toolResultPayload;
      stepTokens = estimatedTokens(text);
      score.tokensWithoutDedupe += stepTokens;
      if (result.permission === 'allow') score.tokensWithDedupe += stepTokens;
    }
    if (dup) {
      score.dupCalls++;
      if (intentionalAllow) {
        score.intentionalDupAllow++;
        assert.equal(result.permission, 'allow', label);
      } else {
        assert.equal(result.permission, 'deny', label);
        score.dupDenied++;
        if (denyPattern) assert.match(result.agent_message, denyPattern);
      }
    } else {
      score.freshCalls++;
      assert.equal(result.permission, 'allow', label);
    }
    return result;
  }

  runStep({
    label: 'first full Read',
    dup: false,
    decide: HOOK.decideRead,
    input: { conversation_id: conv, tool_input: { path: file } },
    toolResultPayload: () => fs.readFileSync(file, 'utf8'),
  });
  runStep({
    label: 'repeat full Read',
    dup: true,
    decide: HOOK.decideRead,
    input: { conversation_id: conv, tool_input: { path: file } },
    denyPattern: /Already read this unchanged file/,
    toolResultPayload: () => fs.readFileSync(file, 'utf8'),
  });
  runStep({
    label: 'third full Read second chance',
    dup: true,
    intentionalAllow: true,
    decide: HOOK.decideRead,
    input: { conversation_id: conv, tool_input: { path: file } },
    toolResultPayload: () => fs.readFileSync(file, 'utf8'),
  });

  fs.appendFileSync(file, 'line6\n');

  const narrow = { conversation_id: conv, tool_input: { path: file, offset: 2, limit: 2 } };
  runStep({
    label: 'ranged Read after edit',
    dup: false,
    decide: HOOK.decideRead,
    input: narrow,
    toolResultPayload: () => readFileRangeText(file, 2, 2),
  });
  runStep({
    label: 'repeat narrow range',
    dup: true,
    decide: HOOK.decideRead,
    input: narrow,
    denyPattern: /line range/,
    toolResultPayload: () => readFileRangeText(file, 2, 2),
  });
  runStep({
    label: 'wider range Read',
    dup: false,
    decide: HOOK.decideRead,
    input: { conversation_id: conv, tool_input: { path: file, offset: 2, limit: 4 } },
    toolResultPayload: () => readFileRangeText(file, 2, 4),
  });

  runStep({
    label: 'full Read for coverage',
    dup: false,
    decide: HOOK.decideRead,
    input: { conversation_id: conv, tool_input: { path: file } },
    toolResultPayload: () => fs.readFileSync(file, 'utf8'),
  });
  runStep({
    label: 'subset range after full Read',
    dup: true,
    decide: HOOK.decideRead,
    input: { conversation_id: conv, tool_input: { path: file, offset: 2, limit: 1 } },
    denyPattern: /line range/,
    toolResultPayload: () => readFileRangeText(file, 2, 1),
  });

  const grepBase = { conversation_id: conv, tool_input: { pattern: 'line2', path: dir } };
  runStep({
    label: 'first Grep',
    dup: false,
    decide: HOOK.decideGrep,
    input: grepBase,
    toolResultPayload: () => grepPayload(dir, 'line2'),
  });
  runStep({
    label: 'repeat Grep',
    dup: true,
    decide: HOOK.decideGrep,
    input: grepBase,
    denyPattern: /grep/i,
    toolResultPayload: () => grepPayload(dir, 'line2'),
  });
  runStep({
    label: 'Grep new pattern',
    dup: false,
    decide: HOOK.decideGrep,
    input: { conversation_id: conv, tool_input: { pattern: 'line6', path: dir } },
    toolResultPayload: () => grepPayload(dir, 'line6'),
  });
  fs.appendFileSync(file, 'line7\n');
  runStep({
    label: 'Grep after tree change',
    dup: false,
    decide: HOOK.decideGrep,
    input: grepBase,
    toolResultPayload: () => grepPayload(dir, 'line2'),
  });

  const globBase = { conversation_id: conv, tool_input: { glob_pattern: '*.txt', path: dir } };
  runStep({
    label: 'first Glob',
    dup: false,
    decide: HOOK.decideGlob,
    input: globBase,
    toolResultPayload: () => globTxtPayload(dir),
  });
  runStep({
    label: 'repeat Glob',
    dup: true,
    decide: HOOK.decideGlob,
    input: globBase,
    denyPattern: /glob/i,
    toolResultPayload: () => globTxtPayload(dir),
  });
  runStep({
    label: 'Glob other conversation',
    dup: false,
    decide: HOOK.decideGlob,
    input: { conversation_id: 'c-other', tool_input: { glob_pattern: '*.txt', path: dir } },
    toolResultPayload: () => globTxtPayload(dir),
  });

  runStep({
    label: 'npm test first',
    dup: false,
    decide: HOOK.decideShell,
    input: { conversation_id: conv, command: 'npm test' },
    toolResultPayload: 'npm test\n',
  });
  runStep({
    label: 'npm test repeat',
    dup: true,
    decide: HOOK.decideShell,
    input: { conversation_id: conv, command: 'npm test' },
    denyPattern: /npm test/,
    toolResultPayload: 'npm test\n',
  });

  const gitStatus = { conversation_id: conv, command: 'git status' };
  runStep({
    label: 'git status first',
    dup: false,
    decide: HOOK.decideShell,
    input: gitStatus,
    toolResultPayload: () => spawnSync('git', ['status', '--porcelain'], { cwd: dir, encoding: 'utf8' }).stdout,
  });
  runStep({
    label: 'git status repeat',
    dup: true,
    decide: HOOK.decideShell,
    input: gitStatus,
    denyPattern: /git status/,
    toolResultPayload: () => spawnSync('git', ['status', '--porcelain'], { cwd: dir, encoding: 'utf8' }).stdout,
  });
  fs.appendFileSync(path.join(dir, 'README'), 'more\n');
  runStep({
    label: 'git status after worktree change',
    dup: false,
    decide: HOOK.decideShell,
    input: gitStatus,
    toolResultPayload: () => spawnSync('git', ['status', '--porcelain'], { cwd: dir, encoding: 'utf8' }).stdout,
  });
  runStep({
    label: 'git status -sb not keyed',
    dup: false,
    decide: HOOK.decideShell,
    input: { conversation_id: conv, command: 'git status -sb' },
    toolResultPayload: () => spawnSync('git', ['status', '-sb'], { cwd: dir, encoding: 'utf8' }).stdout,
  });
  runStep({
    label: 'npm install not keyed',
    dup: false,
    decide: HOOK.decideShell,
    input: { conversation_id: conv, command: 'npm install' },
    toolResultPayload: 'npm install\n',
  });

  const targetDupDenied = score.dupCalls - score.intentionalDupAllow;
  assert.equal(score.dupDenied, targetDupDenied);
  assert.equal(score.freshCalls > 0, true);
  const savedTokens = score.tokensWithoutDedupe - score.tokensWithDedupe;
  assert.ok(savedTokens > 0);
  assert.ok(score.tokensWithoutDedupe > score.tokensWithDedupe);
  const savedPercent = Math.round((savedTokens / score.tokensWithoutDedupe) * 100);

  score.reportLine = `dedupe effectiveness ${score.dupDenied}/${targetDupDenied} duplicate calls blocked; correctness ${score.freshCalls}/${score.freshCalls} fresh calls allowed (${score.intentionalDupAllow} intentional duplicate allow: third full Read); estimated tool-result tokens without dedupe: ${score.tokensWithoutDedupe}; with dedupe: ${score.tokensWithDedupe}; saved: ${savedTokens} (${savedPercent}%, ceil(bytes/4))`;
  console.log(score.reportLine);

  fs.rmSync(dir, { recursive: true, force: true });
});

test('real repo dedupe token table for PR comment', () => {
  const repoRoot = REPO_ROOT;
  const cwd = repoRoot;
  const stateDir = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-table-')), 'state');
  let state = HOOK.loadState(stateDir);
  const conv = 'c-dedupe-measure';
  const rows = [];

  function denyRepeatRead(label, fileRel) {
    const filePath = path.join(repoRoot, fileRel);
    const input = { conversation_id: conv, tool_input: { path: filePath } };
    const first = HOOK.decideRead(input, { state, stateDir, cwd });
    state = first.state;
    assert.equal(first.permission, 'allow', label);
    const payloadText = fs.readFileSync(filePath, 'utf8');
    const second = HOOK.decideRead(input, { state, stateDir, cwd });
    state = second.state;
    assert.equal(second.permission, 'deny', label);
    rows.push(rowFromPayload(label, payloadText, second.agent_message));
  }

  function denyRepeatGrep(label, pattern, searchPathRel) {
    const searchPath = path.join(repoRoot, searchPathRel);
    const input = { conversation_id: conv, tool_input: { pattern, path: searchPath } };
    const first = HOOK.decideGrep(input, { state, stateDir, cwd });
    state = first.state;
    assert.equal(first.permission, 'allow', label);
    const payloadText = grepToolPayload(searchPath, pattern);
    const second = HOOK.decideGrep(input, { state, stateDir, cwd });
    state = second.state;
    assert.equal(second.permission, 'deny', label);
    rows.push(rowFromPayload(label, payloadText, second.agent_message));
  }

  function denyRepeatGlob(label, globPattern, testsRel) {
    const searchPath = path.join(repoRoot, testsRel);
    const input = { conversation_id: conv, tool_input: { glob_pattern: globPattern, path: searchPath } };
    const first = HOOK.decideGlob(input, { state, stateDir, cwd });
    state = first.state;
    assert.equal(first.permission, 'allow', label);
    const payloadText = globTestMjsPayload(searchPath, repoRoot);
    const second = HOOK.decideGlob(input, { state, stateDir, cwd });
    state = second.state;
    assert.equal(second.permission, 'deny', label);
    rows.push(rowFromPayload(label, payloadText, second.agent_message));
  }

  function denyRepeatGitStatus(label) {
    const input = { conversation_id: conv, command: 'git status' };
    const first = HOOK.decideShell(input, { state, stateDir, cwd });
    state = first.state;
    assert.equal(first.permission, 'allow', label);
    const payloadText = spawnSync('git', ['status'], { cwd: repoRoot, encoding: 'utf8' }).stdout;
    const second = HOOK.decideShell(input, { state, stateDir, cwd });
    state = second.state;
    assert.equal(second.permission, 'deny', label);
    rows.push(rowFromPayload(label, payloadText, second.agent_message));
  }

  const indexTs = 'packages/cli/src/index.ts';
  denyRepeatRead('`src/hooks/cursor-dedupe-tools.js`', 'src/hooks/cursor-dedupe-tools.js');
  denyRepeatRead('`installer/lib/cursor-dedupe-hooks.js`', 'installer/lib/cursor-dedupe-hooks.js');
  denyRepeatRead('`CLAUDE.md`', 'CLAUDE.md');
  denyRepeatRead(`\`${indexTs}\` (full file)`, indexTs);
  denyRepeatRead('`tests/installer/cursor-dedupe-tools.test.mjs`', 'tests/installer/cursor-dedupe-tools.test.mjs');
  {
    const label = '`index.ts` lines 1–80 (already covered by the full read)';
    const filePath = path.join(repoRoot, indexTs);
    const input = { conversation_id: conv, tool_input: { path: filePath, offset: 1, limit: 80 } };
    const payloadText = readFileRangeText(filePath, 1, 80);
    const denied = HOOK.decideRead(input, { state, stateDir, cwd });
    state = denied.state;
    assert.equal(denied.permission, 'deny', label);
    rows.push(rowFromPayload(label, payloadText, denied.agent_message));
  }
  denyRepeatGrep('Grep `decideRead` in `src/hooks`', 'decideRead', 'src/hooks');
  denyRepeatGrep('Grep `cursor-dedupe` in the repo', 'cursor-dedupe', '.');
  denyRepeatGlob('Glob `tests/**/*.test.mjs`', '**/*.test.mjs', 'tests');
  denyRepeatGitStatus('`git status`');

  assert.equal(rows.length, 10);

  let totalPayloadChars = 0;
  let totalPayloadTokens = 0;
  let totalDenyTokens = 0;
  let totalSavedTokens = 0;
  for (const row of rows) {
    totalPayloadChars += row.payload.chars;
    totalPayloadTokens += row.payload.tokens;
    totalDenyTokens += row.deny.tokens;
    totalSavedTokens += row.savedTokens;
  }
  const savedPercent = Math.round((totalSavedTokens / totalPayloadTokens) * 100);

  const lines = [
    '## Cursor dedupe-hook token measurement (generated)',
    '',
    'Denied repeat replaces tool payload with short `agent_message`. Savings = payload that would re-enter context minus that message. Token estimate is `ceil(chars / 4)`.',
    '',
    '| Repeat call | Payload | Deny message | Saved tokens |',
    '|---|---:|---:|---:|',
  ];
  for (const row of rows) {
    lines.push(
      `| ${row.label} | ${fmtNum(row.payload.chars)} chars / ${fmtNum(row.payload.tokens)} tok | ${fmtNum(row.deny.chars)} / ${fmtNum(row.deny.tokens)} | **${fmtNum(row.savedTokens)}** |`,
    );
  }
  lines.push('');
  lines.push(
    `**${rows.length} denied repeats: ${fmtNum(totalPayloadChars)} characters, about ${fmtNum(totalPayloadTokens)} tokens without dedupe; about ${fmtNum(totalDenyTokens)} tokens with dedupe (deny messages only); saved ${fmtNum(totalSavedTokens)} tokens (${savedPercent}%).**`,
  );

  const indexRow = rows.find((r) => r.label.includes('full file'));
  if (indexRow) {
    const withoutIndex = rows.filter((r) => r !== indexRow);
    let chars = 0;
    let saved = 0;
    for (const row of withoutIndex) {
      chars += row.payload.chars;
      saved += row.savedTokens;
    }
    lines.push(
      `Without the full \`${indexTs}\` read, the other ${withoutIndex.length} repeats still save **${fmtNum(chars)} characters, about ${fmtNum(saved)} tokens**.`,
    );
  }

  console.log(lines.join('\n'));

  fs.rmSync(path.dirname(stateDir), { recursive: true, force: true });
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
test('hook finishes on first complete JSON while stdin write end stays open', async () => {
  const { spawn } = await import('node:child_process');
  const payload = JSON.stringify({ conversation_id: 'pipe', tool_input: { path: HOOK_SCRIPT } });
  const child = spawn(process.execPath, [HOOK_SCRIPT, 'read'], {
    env: { ...process.env, CAVEMAN_CURSOR_HOOK_NO_SAVE: '1' },
    stdio: ['pipe', 'pipe', 'inherit'],
  });
  child.stdin.write(payload);
  let out = '';
  child.stdout.on('data', (chunk) => { out += chunk; });
  const start = Date.now();
  const done = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('hook hung with open stdin')), 8000);
    child.on('close', (code) => {
      clearTimeout(timer);
      resolve({ code, ms: Date.now() - start });
    });
  });
  return done.then(({ code, ms }) => {
    assert.equal(code, 0);
    assert.ok(ms < 5000, `expected fast exit, got ${ms}ms`);
    assert.equal(JSON.parse(out).permission, 'allow');
    child.stdin.end();
  });
});
