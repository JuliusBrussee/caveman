// Tests for the hardened downloadTo() in installer/install.js.
//
// We avoid spawning child processes for most cases and instead re-implement
// the same downloadTo logic inline with a controllable fake https module,
// giving deterministic, fast coverage of every branch.
//
// The curl-flag tests run the installer via spawnSync with a fake curl binary
// on PATH that records its argv — these are POSIX-only (fake bin is a shell script).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { EventEmitter } from 'node:events';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
import { isolatedEnv } from './_isolated-env.mjs';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

// ── Load the real downloadTo and its constants from the installer ──────────
const {
  downloadTo,
  DOWNLOAD_MAX_REDIRECTS,
  DOWNLOAD_TIMEOUT_MS,
  DOWNLOAD_MAX_BYTES,
} = createRequire(import.meta.url)('../../installer/install.js');

// ── Fake-https factory ─────────────────────────────────────────────────────
function makeFakeHttps({ statusCode = 200, location = null, hangTimeout = null, bodyChunks = ['ok'] } = {}) {
  return {
    get(_url, cb) {
      const fakeRes = new EventEmitter();
      Object.assign(fakeRes, {
        statusCode,
        headers: location ? { location } : {},
        destroy() { fakeRes.emit('close'); },
        pipe(dest) {
          if (hangTimeout !== null) return; // never delivers data
          for (const chunk of bodyChunks) fakeRes.emit('data', Buffer.from(chunk));
          process.nextTick(() => dest.end());
        },
      });
      const fakeReq = new EventEmitter();
      Object.assign(fakeReq, {
        setTimeout(_ms, timeoutCb) {
          if (hangTimeout !== null) setTimeout(timeoutCb, hangTimeout);
        },
        destroy() {},
      });
      process.nextTick(() => cb(fakeRes));
      return fakeReq;
    },
  };
}

// ── Node-fallback branch tests ─────────────────────────────────────────────

test('Node fallback: http:// redirect is rejected', async () => {
  await assert.rejects(
    () => downloadTo('https://raw.githubusercontent.com/ok', '/dev/null',
      makeFakeHttps({ statusCode: 301, location: 'http://evil.example.com/x' })),
    /non-https/,
  );
});

test('Node fallback: redirect chain beyond limit is rejected', async () => {
  let count = 0;
  const loopHttps = {
    get(_url, cb) {
      count++;
      const fakeRes = new EventEmitter();
      Object.assign(fakeRes, {
        statusCode: 301,
        headers: { location: 'https://raw.githubusercontent.com/ok' },
        destroy() {},
        pipe() {},
      });
      const fakeReq = new EventEmitter();
      Object.assign(fakeReq, { setTimeout() {}, destroy() {} });
      process.nextTick(() => cb(fakeRes));
      return fakeReq;
    },
  };
  await assert.rejects(
    () => downloadTo('https://raw.githubusercontent.com/ok', '/dev/null', loopHttps),
    /too many redirects/,
  );
  assert.ok(count <= DOWNLOAD_MAX_REDIRECTS + 2, `excessive fetches: ${count}`);
});

test('Node fallback: oversized body is rejected', async () => {
  const BIG = DOWNLOAD_MAX_BYTES + 1;
  await assert.rejects(
    () => downloadTo('https://raw.githubusercontent.com/ok', '/dev/null',
      makeFakeHttps({ statusCode: 200, bodyChunks: [Buffer.alloc(BIG).fill(65).toString('binary')] })),
    /exceeded/,
  );
});

test('Node fallback: timeout fires on a hanging connection', async () => {
  await assert.rejects(
    () => downloadTo('https://raw.githubusercontent.com/ok', '/dev/null',
      makeFakeHttps({ statusCode: 200, hangTimeout: 10 })),
    /timed out/,
  );
});

test('Node fallback: successful small download resolves without error', async () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'dl-test-'));
  const dest = path.join(tmp, 'out');
  try {
    await assert.doesNotReject(() => downloadTo('https://raw.githubusercontent.com/ok', dest,
      makeFakeHttps({ statusCode: 200, bodyChunks: ['hello'] })));
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
});

// ── curl-path tests (POSIX only, requires a real shell) ────────────────────

const posixOnly = { skip: process.platform === 'win32' && 'fake curl is a POSIX shell script' };

function setupCurlTest() {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-curl-test-'));
  const fakeBin = path.join(tmp, 'fake-bin');
  fs.mkdirSync(fakeBin);
  const log = path.join(tmp, 'curl-args.txt');
  // Fake curl: write all args to log file, exit 0.
  fs.writeFileSync(path.join(fakeBin, 'curl'),
    `#!/bin/sh\necho "$@" >> ${log}\nexit 0\n`, { mode: 0o755 });
  // Also add a minimal fake claude so the installer finds it.
  fs.writeFileSync(path.join(fakeBin, 'claude'), '#!/bin/sh\nexit 0\n', { mode: 0o755 });

  // Detached copy of the installer (no src/hooks beside it → hooks are remote → curl is invoked).
  const detached = path.join(tmp, 'installer');
  fs.cpSync(path.join(REPO_ROOT, 'installer'), detached, { recursive: true });

  const configDir = path.join(tmp, 'claude');
  fs.mkdirSync(path.join(configDir, 'hooks'), { recursive: true });
  fs.writeFileSync(path.join(configDir, 'settings.json'), '{"theme":"dark"}\n');

  const home = path.join(tmp, 'home');
  fs.mkdirSync(home, { recursive: true });
  const env = { ...isolatedEnv(home, [fakeBin]), CLAUDE_CONFIG_DIR: configDir };

  const run = () => spawnSync(process.execPath, [
    path.join(detached, 'install.js'),
    '--only', 'claude', '--with-hooks',
    '--config-dir', configDir,
    '--non-interactive', '--no-mcp-shrink',
  ], { env, encoding: 'utf8' });

  const readLog = () => {
    try { return fs.readFileSync(log, 'utf8').trim(); } catch (_) { return ''; }
  };

  return { tmp, run, readLog };
}

test('curl path: --proto-redir =https is passed to curl', posixOnly, () => {
  const { tmp, run, readLog } = setupCurlTest();
  try {
    run();
    const args = readLog();
    assert.ok(args.includes('--proto-redir'), `--proto-redir missing. curl args: ${args}`);
    assert.ok(args.includes('=https'), `=https missing. curl args: ${args}`);
  } finally { fs.rmSync(tmp, { recursive: true, force: true }); }
});

test('curl path: --max-redirs is passed to curl', posixOnly, () => {
  const { tmp, run, readLog } = setupCurlTest();
  try {
    run();
    const args = readLog();
    assert.ok(args.includes('--max-redirs'), `--max-redirs missing. curl args: ${args}`);
    assert.ok(args.includes(String(DOWNLOAD_MAX_REDIRECTS)), `redirect count missing. curl args: ${args}`);
  } finally { fs.rmSync(tmp, { recursive: true, force: true }); }
});

test('curl path: --max-time is passed to curl', posixOnly, () => {
  const { tmp, run, readLog } = setupCurlTest();
  try {
    run();
    const args = readLog();
    assert.ok(args.includes('--max-time'), `--max-time missing. curl args: ${args}`);
    assert.ok(args.includes(String(DOWNLOAD_TIMEOUT_MS / 1000)), `timeout value missing. curl args: ${args}`);
  } finally { fs.rmSync(tmp, { recursive: true, force: true }); }
});

test('curl path: --max-filesize is passed to curl', posixOnly, () => {
  const { tmp, run, readLog } = setupCurlTest();
  try {
    run();
    const args = readLog();
    assert.ok(args.includes('--max-filesize'), `--max-filesize missing. curl args: ${args}`);
    assert.ok(args.includes(String(DOWNLOAD_MAX_BYTES)), `file-size value missing. curl args: ${args}`);
  } finally { fs.rmSync(tmp, { recursive: true, force: true }); }
});
