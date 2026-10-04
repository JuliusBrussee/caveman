import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { createMiddlewareRuntime, MiddlewareError } from '../dist/middleware/index.js';

const protocol = JSON.parse(await readFile(new URL('../../parity/middleware.fixtures.json', import.meta.url), 'utf8'));
const fixture = JSON.parse(await readFile(new URL('../../parity/middleware-preflight.fixtures.json', import.meta.url), 'utf8'));

test('shared preflight outcomes are content-free and nonthrowing even in strict mode', async () => {
  for (const vector of fixture.cases) {
    let calls = 0;
    const runtime = createMiddlewareRuntime({ mode: vector.mode, strict: true, deadlineMs: 2000, fetch: async (url, options) => {
      calls++;
      assert.ok(url.endsWith('/capabilities'));
      assert.equal(options.method, 'GET');
      assert.equal(options.body, undefined);
      if (vector.transport_error) throw new Error('source text with api-key=must-not-log');
      if (vector.timeout_error) throw new DOMException('source text with api-key=must-not-log', 'TimeoutError');
      if (vector.error) throw new MiddlewareError(vector.error);
      return Response.json({ ...protocol.capabilities, ...vector.capabilities });
    }, onReport() { assert.fail('discovery is not an applied native call'); }, onDiagnostic() { assert.fail('preflight must not invoke callbacks'); } });
    try {
      if (vector.closed) runtime.close();
      const result = await runtime.preflight();
      assert.deepEqual(result, vector.expected, vector.id);
      assert.ok(Object.isFrozen(result));
      assert.ok(!JSON.stringify(result).includes('must-not-log'));
      assert.equal(calls, vector.mode === 'off' || vector.closed ? 0 : 1, vector.id);
      assert.equal(runtime.lastReport, null);
    } finally { runtime.close(); }
  }
});

test('ready stays throwing while preflight can recover after startup failure', async () => {
  let down = true;
  const runtime = createMiddlewareRuntime({ deadlineMs: 2000, fetch: async () => {
    if (down) throw new Error('runtime down');
    return Response.json(protocol.capabilities);
  }});
  try {
    await assert.rejects(runtime.ready());
    assert.equal((await runtime.preflight()).reason, 'runtime_unavailable');
    down = false;
    assert.equal((await runtime.preflight()).reason, 'ready');
    assert.equal(runtime.isRuntimeOrigin(runtime.endpoint), true);
    down = true;
    assert.equal((await runtime.preflight()).reason, 'runtime_unavailable');
    assert.equal(runtime.isRuntimeOrigin(runtime.endpoint), false);
  } finally { runtime.close(); }
});

test('caller cancellation propagates and close stops in-flight preflight', async () => {
  let entered;
  const started = new Promise(resolve => { entered = resolve; });
  const runtime = createMiddlewareRuntime({ deadlineMs: 2000, fetch: () => { entered(); return new Promise(() => {}); } });
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(runtime.preflight(controller.signal), { name: 'AbortError' });
  const pending = runtime.preflight();
  await started;
  runtime.close();
  assert.equal((await pending).reason, 'closed');
  const active = createMiddlewareRuntime({ deadlineMs: 2000, fetch: () => new Promise(() => {}) });
  const abort = new AbortController();
  const cancelled = active.preflight(abort.signal);
  abort.abort();
  await assert.rejects(cancelled, { name: 'AbortError' });
  active.close();
});

test('preflight deadline bounds a transport that ignores cancellation', async () => {
  const runtime = createMiddlewareRuntime({ deadlineMs: 20, fetch: () => new Promise(() => {}) });
  try { assert.equal((await runtime.preflight()).reason, 'deadline'); }
  finally { runtime.close(); }
});
