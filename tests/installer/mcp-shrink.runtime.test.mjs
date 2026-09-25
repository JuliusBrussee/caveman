import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const proxy = path.join(root, 'src', 'mcp-servers', 'caveman-shrink', 'index.js');

function isAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

async function waitUntil(predicate, { timeoutMs = 5000, intervalMs = 20 } = {}) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await sleep(intervalMs);
  }
  throw new Error('waitUntil: condition never became true');
}

test('caveman-shrink drains a large final response without requiring a trailing newline', () => {
  const size = 1_500_000;
  const upstream = `process.stdout.write(JSON.stringify({jsonrpc:'2.0',id:1,result:{payload:'x'.repeat(${size})}}))`;
  const result = spawnSync(process.execPath, [proxy, process.execPath, '-e', upstream], {
    encoding: 'utf8',
    maxBuffer: 4 * 1024 * 1024,
  });
  assert.equal(result.status, 0, result.stderr);
  const message = JSON.parse(result.stdout);
  assert.equal(message.result.payload.length, size);
});

test('caveman-shrink preserves UTF-8 code points split across upstream chunks', () => {
  const upstream = [
    "const bytes=Buffer.from(JSON.stringify({jsonrpc:'2.0',id:1,result:{payload:'🙂'}}))",
    "const split=bytes.indexOf(Buffer.from('🙂'))+1",
    "process.stdout.write(bytes.subarray(0,split))",
    "setImmediate(()=>process.stdout.write(bytes.subarray(split)))",
  ].join(';');
  const result = spawnSync(process.execPath, [proxy, process.execPath, '-e', upstream], {
    encoding: 'utf8',
    maxBuffer: 1024 * 1024,
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).result.payload, '🙂');
});

for (const signal of ['SIGTERM', 'SIGINT', 'SIGHUP']) {
  test(`caveman-shrink forwards ${signal} to its spawned upstream process`, async () => {
    const pidFile = path.join(os.tmpdir(), `caveman-shrink-${signal}-${process.pid}-${Date.now()}.pid`);
    const upstream = [
      `require('fs').writeFileSync(${JSON.stringify(pidFile)}, String(process.pid))`,
      'process.stdin.resume()',
      'setInterval(() => {}, 1000000)',
    ].join(';');

    const wrapper = spawn(process.execPath, [proxy, process.execPath, '-e', upstream], { stdio: 'ignore' });

    try {
      await waitUntil(() => fs.existsSync(pidFile));
      const upstreamPid = Number(fs.readFileSync(pidFile, 'utf8'));
      assert.ok(isAlive(upstreamPid), 'upstream process should be running before the signal');

      wrapper.kill(signal);
      await waitUntil(() => wrapper.exitCode !== null || wrapper.signalCode !== null);
      await waitUntil(() => !isAlive(upstreamPid));
    } finally {
      fs.rmSync(pidFile, { force: true });
      if (isAlive(wrapper.pid)) wrapper.kill('SIGKILL');
    }
  });
}
