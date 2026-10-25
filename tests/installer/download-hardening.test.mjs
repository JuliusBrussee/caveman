// Exercise downloadTo directly from the shipped installer source. Only the
// transport is redirected to a local HTTP server; no downloader is copied here.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import vm from 'node:vm';
import { spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const source = fs.readFileSync(path.join(root, 'installer/install.js'), 'utf8');
const start = source.indexOf('// ── HTTPS download via stdlib');
const end = source.indexOf('// ── Integrity verification for downloaded hooks', start);
assert.ok(start > 0 && end > start, 'load the actual installer download section');

function loadDownload({ get, curl, clock = setTimeout } = {}) {
  return vm.runInNewContext(`${source.slice(start, end)}\ndownloadTo`, {
    fs, URL, Buffer, setTimeout: clock, clearTimeout,
    hasCmd: () => Boolean(curl),
    child_process: { spawnSync: curl },
    require: (name) => name === 'https' ? { get } : require(name),
  });
}

async function fixture(t, handler) {
  const folder = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-download-'));
  const server = http.createServer(handler);
  const sockets = new Set();
  const requests = [];
  const urls = [];
  server.on('connection', (socket) => {
    sockets.add(socket);
    socket.on('close', () => sockets.delete(socket));
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  t.after(async () => {
    for (const request of requests) request.destroy();
    for (const socket of sockets) socket.destroy();
    await new Promise((resolve) => server.close(resolve));
    fs.rmSync(folder, { recursive: true, force: true });
  });
  const get = (url, callback) => {
    urls.push(String(url));
    const target = new URL(url);
    if (target.protocol !== 'https:') throw new Error('https transport refuses other protocols');
    const request = http.get({ hostname: '127.0.0.1', port: server.address().port,
      path: target.pathname + target.search }, callback);
    requests.push(request);
    return request;
  };
  return { folder, dest: path.join(folder, 'hook.js'), urls, get };
}

test('Node downloader keeps the exact complete bytes', async (t) => {
  const bytes = Buffer.from([0, 1, 127, 128, 255]);
  const f = await fixture(t, (_req, res) => res.end(bytes));
  await loadDownload(f)('https://fixture.test/hook', f.dest);
  assert.deepEqual(fs.readFileSync(f.dest), bytes);
});

test('Node downloader rejects unknown-length oversized bodies before touching destination', async (t) => {
  const f = await fixture(t, (_req, res) => {
    res.writeHead(200);
    res.write(Buffer.alloc(4 * 1024 * 1024));
    res.end(Buffer.alloc(4 * 1024 * 1024 + 1));
  });
  fs.writeFileSync(f.dest, 'previous file');
  await assert.rejects(loadDownload(f)('https://fixture.test/large', f.dest), /exceeds|too large/i);
  assert.equal(fs.readFileSync(f.dest, 'utf8'), 'previous file');
});

test('Node downloader resolves relative HTTPS redirects', async (t) => {
  const f = await fixture(t, (req, res) => {
    if (req.url === '/start') { res.writeHead(302, { location: '/hook' }); res.end(); }
    else res.end('complete');
  });
  await loadDownload(f)('https://fixture.test/start', f.dest);
  assert.equal(fs.readFileSync(f.dest, 'utf8'), 'complete');
  assert.deepEqual(f.urls, ['https://fixture.test/start', 'https://fixture.test/hook']);
});

test('Node downloader refuses a redirect off HTTPS', async (t) => {
  const f = await fixture(t, (req, res) => {
    if (req.url === '/start') { res.writeHead(302, { location: 'http://fixture.test/hook' }); res.end(); }
    else res.end('must not arrive');
  });
  await assert.rejects(loadDownload(f)('https://fixture.test/start', f.dest), /https/i);
  assert.equal(f.urls.length, 1);
  assert.equal(fs.existsSync(f.dest), false);
});

test('Node downloader bounds the entire redirect chain', async (t) => {
  let count = 0;
  const f = await fixture(t, (_req, res) => {
    if (++count < 8) { res.writeHead(302, { location: 'https://fixture.test/again' }); res.end(); }
    else res.end('loop escaped only by the fixture');
  });
  await assert.rejects(loadDownload(f)('https://fixture.test/start', f.dest), /redirect/i);
  assert.equal(count, 6, 'initial request plus at most five redirects');
  assert.equal(fs.existsSync(f.dest), false);
});

test('Node downloader destroys a stalled request at its total deadline', async (t) => {
  const f = await fixture(t, () => {});
  const download = loadDownload({ ...f, clock: (fn, ms) => setTimeout(fn, Math.min(ms, 80)) });
  let watchdog;
  const bounded = Promise.race([
    download('https://fixture.test/hang', f.dest),
    new Promise((_, reject) => { watchdog = setTimeout(() => reject(new Error('fixture watchdog: no download deadline')), 800); }),
  ]);
  try { await assert.rejects(bounded, /download timed out/i); }
  finally { clearTimeout(watchdog); }
  assert.equal(fs.existsSync(f.dest), false);
});

test('Node downloader deadline also bounds a continuously arriving body', async (t) => {
  const f = await fixture(t, (_req, res) => {
    res.write('first');
    const drip = setInterval(() => res.write('more'), 10);
    res.on('close', () => clearInterval(drip));
  });
  const download = loadDownload({ ...f, clock: (fn, ms) => setTimeout(fn, Math.min(ms, 80)) });
  await assert.rejects(download('https://fixture.test/drip', f.dest), /download timed out/i);
  assert.equal(fs.existsSync(f.dest), false);
});

test('Node downloader rejects an interrupted response without leaving partial bytes', async (t) => {
  const f = await fixture(t, (_req, res) => {
    res.writeHead(200, { 'content-length': '100' });
    res.write('partial');
    setImmediate(() => res.destroy());
  });
  fs.writeFileSync(f.dest, 'previous file');
  await assert.rejects(loadDownload(f)('https://fixture.test/truncated', f.dest), /aborted|reset/i);
  assert.equal(fs.readFileSync(f.dest, 'utf8'), 'previous file');
});

test('Node downloader closes provider error responses without touching destination', async (t) => {
  const f = await fixture(t, (_req, res) => { res.writeHead(503); res.write('unavailable'); });
  await assert.rejects(loadDownload(f)('https://fixture.test/error', f.dest), /HTTP 503/);
  assert.equal(fs.existsSync(f.dest), false);
});

test('curl download enforces transport bounds and writes only successful complete bytes', () => {
  const folder = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-curl-download-'));
  const dest = path.join(folder, 'hook.js');
  try {
    const download = loadDownload({ curl: (cmd, args, options) => {
      assert.equal(cmd, 'curl');
      for (const [flag, value] of [['--proto', '=https'], ['--proto-redir', '=https'],
        ['--max-redirs', '5'], ['--max-time', '60'], ['--max-filesize', '8388608']]) {
        assert.equal(args[args.indexOf(flag) + 1], value, flag);
      }
      assert.equal(options.maxBuffer, 8 * 1024 * 1024);
      assert.equal(options.timeout, 60_000);
      assert.equal(options.killSignal, 'SIGKILL');
      return { status: 0, stdout: Buffer.from('complete') };
    } });
    download('https://fixture.test/hook', dest);
    assert.equal(fs.readFileSync(dest, 'utf8'), 'complete');
    const failed = loadDownload({ curl: () => ({ status: null, error: new Error('maxBuffer'), stdout: Buffer.from('partial') }) });
    assert.throws(() => failed('https://fixture.test/hook', dest), /curl failed/);
    assert.equal(fs.readFileSync(dest, 'utf8'), 'complete');
  } finally { fs.rmSync(folder, { recursive: true, force: true }); }
});

test('curl stdout cap terminates a real oversized subprocess and preserves destination', () => {
  const folder = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-curl-cap-'));
  const dest = path.join(folder, 'hook.js');
  fs.writeFileSync(dest, 'previous file');
  try {
    const download = loadDownload({ curl: (_cmd, _args, options) => spawnSync(process.execPath,
      ['-e', 'process.stdout.write(Buffer.alloc(9 * 1024 * 1024))'], options) });
    assert.throws(() => download('https://fixture.test/large', dest), /curl failed/);
    assert.equal(fs.readFileSync(dest, 'utf8'), 'previous file');
  } finally { fs.rmSync(folder, { recursive: true, force: true }); }
});
