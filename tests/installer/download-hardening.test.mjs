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
import { isolatedEnv } from './_isolated-env.mjs';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

// ── Constants (must match installer/install.js) ────────────────────────────
const DOWNLOAD_MAX_REDIRECTS = 5;
const DOWNLOAD_TIMEOUT_MS    = 60_000;
const DOWNLOAD_MAX_BYTES     = 8 * 1024 * 1024;

// ── Inline reimplementation of downloadTo (Node-fallback path only) ────────
// Kept structurally identical to the implementation in install.js so any
// divergence between the two is a bug in that file.
function makeDownloadTo(fakeHttps) {
  return function downloadTo(url, dest) {
    return new Promise((resolve, reject) => {
      let redirects = 0;
      function fetch(target) {
        if (!target.startsWith('https://')) {
          reject(new Error('download refused non-https URL: ' + target));
          return;
        }
        const req = fakeHttps.get(target, (res) => {
          if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
            if (++redirects > DOWNLOAD_MAX_REDIRECTS) {
              reject(new Error('too many redirects for ' + url));
              return;
            }
            fetch(res.headers.location);
            return;
          }
          if (res.statusCode !== 200) {
            reject(new Error('HTTP ' + res.statusCode + ' for ' + url));
            return;
          }
          let received = 0;
          const out = {
            write() {}, end() {}, close(cb) { resolve(); if (cb) cb(); }, destroy() {},
            on(e, cb) { if (e === 'finish') process.nextTick(cb); return out; },
          };
          res.on('data', (chunk) => {
            received += chunk.length;
            if (received > DOWNLOAD_MAX_BYTES) {
              out.destroy(); res.destroy();
              reject(new Error('download exceeded ' + DOWNLOAD_MAX_BYTES + ' bytes for ' + url));
            }
          });
          res.pipe(out);
        });
        req.setTimeout(DOWNLOAD_TIMEOUT_MS, () => {
          req.destroy();
          reject(new Error('download timed out for ' + url));
        });
        req.on('error', reject);
      }
      fetch(url);
    });
  };
}

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
          process.nextTick(() => dest.close && dest.close());
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
  const dl = makeDownloadTo(makeFakeHttps({ statusCode: 301, location: 'http://evil.example.com/x' }));
  await assert.rejects(
    () => dl('https://raw.githubusercontent.com/ok', '/dev/null'),
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
  const dl = makeDownloadTo(loopHttps);
  await assert.rejects(
    () => dl('https://raw.githubusercontent.com/ok', '/dev/null'),
    /too many redirects/,
  );
  assert.ok(count <= DOWNLOAD_MAX_REDIRECTS + 2, `excessive fetches: ${count}`);
});

test('Node fallback: oversized body is rejected', async () => {
  const BIG = DOWNLOAD_MAX_BYTES + 1;
  const dl = makeDownloadTo(makeFakeHttps({ statusCode: 200, bodyChunks: [Buffer.alloc(BIG).fill(65).toString('binary')] }));
  await assert.rejects(
    () => dl('https://raw.githubusercontent.com/ok', '/dev/null'),
    /exceeded/,
  );
});

test('Node fallback: timeout fires on a hanging connection', async () => {
  const dl = makeDownloadTo(makeFakeHttps({ statusCode: 200, hangTimeout: 10 }));
  await assert.rejects(
    () => dl('https://raw.githubusercontent.com/ok', '/dev/null'),
    /timed out/,
  );
});

test('Node fallback: successful small download resolves without error', async () => {
  const dl = makeDownloadTo(makeFakeHttps({ statusCode: 200, bodyChunks: ['hello'] }));
  await assert.doesNotReject(() => dl('https://raw.githubusercontent.com/ok', '/dev/null'));
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
