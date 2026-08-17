import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const proxy = path.join(root, 'src', 'mcp-servers', 'caveman-shrink', 'index.js');

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
