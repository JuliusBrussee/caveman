import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const proxy = path.join(root, 'src', 'mcp-servers', 'caveman-shrink', 'index.js');
const fixture = path.join(root, 'tests', 'hermes', 'fixtures', 'mcp-stdio-server.mjs');

test('real stdio proxy shrinks tools/list descriptions but preserves tools/call payloads', async () => {
  const child = spawn(process.execPath, [proxy, process.execPath, fixture], {
    cwd: root,
    env: { ...process.env, CAVEMAN_SHRINK_DEBUG: '0' },
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  const pending = new Map();
  let buffer = '';
  let stderr = '';
  child.stderr.on('data', chunk => { stderr += chunk; });
  child.stdout.on('data', chunk => {
    buffer += chunk.toString('utf8');
    let newline;
    while ((newline = buffer.indexOf('\n')) !== -1) {
      const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
      if (!line.trim()) continue;
      const message = JSON.parse(line);
      const resolve = pending.get(message.id);
      if (resolve) { pending.delete(message.id); resolve(message); }
    }
  });

  const request = (id, method, params = {}) => new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`timeout waiting for ${method}; stderr=${stderr}`)), 5_000);
    pending.set(id, message => { clearTimeout(timer); resolve(message); });
    child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n');
  });

  try {
    await request(1, 'initialize');
    const listed = await request(2, 'tools/list');
    const description = listed.result.tools[0].description;
    assert.match(description, /weather/i);
    assert.match(description, /city/i);
    assert.doesNotMatch(description, /\bthe\b/i);
    assert.ok(description.length < 'The tool will carefully return the current weather for the requested city.'.length);

    const called = await request(3, 'tools/call', { name: 'fixture_tool', arguments: {} });
    assert.equal(called.result.content[0].text, 'The tool response must stay exactly the same.');
  } finally {
    child.stdin.end();
    child.kill('SIGTERM');
  }
});
