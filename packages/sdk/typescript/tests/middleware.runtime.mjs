import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { createMiddlewareRuntime, sha256 } from '../dist/middleware/index.js';
import { validateCapabilities, validatePlan, validatePage, MiddlewareError } from '../dist/middleware/validate.js';

const fixture = JSON.parse(await readFile(new URL('../../parity/middleware.fixtures.json', import.meta.url), 'utf8'));
test('shared middleware wire, hashes, and atomic invalid-plan vectors', async () => {
  assert.equal(JSON.stringify(fixture.request), fixture.request_wire);
  for (const vector of fixture.digest_vectors) assert.equal(await sha256(vector.text), vector.sha256);
  const caps = validateCapabilities(fixture.capabilities);
  assert.deepEqual(await validatePlan(fixture.plan, fixture.request, await sha256(fixture.request_wire), caps), fixture.plan);
  for (const vector of fixture.invalid_plans) {
    const bad = structuredClone(fixture.plan);
    let target = bad;
    for (const key of vector.path.slice(0,-1)) target = target[key];
    target[vector.path.at(-1)] = vector.value;
    await assert.rejects(validatePlan(bad, fixture.request, fixture.plan.input_digest, caps), { code: 'invalid_plan' }, vector.id);
  }
  assert.deepEqual(await validatePage(fixture.page, { handle: fixture.page.handle }, 262144), fixture.page);
  await assert.rejects(validatePage({ ...fixture.page, text: 'corrupted' }, { handle: fixture.page.handle }, 262144), { code: 'invalid_recovery' });
});

function input(binding) {
  const r = fixture.request;
  return { scope: r.scope, adapter: r.adapter, candidates: r.segments.map(s => ({ id: s.id, sourceId: s.source_id, kind: s.kind, cacheRegion: s.cache_region, content: s.content })),
    manifest: r.context_manifest, sequence: r.sequence, binding, recoveryOverheadText: 'registered executor', requestId: r.request_id,
    logicalCallId: r.logical_call_id, attemptId: r.attempt_id, idempotencyKey: r.idempotency_key };
}

test('runtime delegates candidates, verifies binding, and preserves entire plan on malformed response', async () => {
  const calls = [];
  let corrupt = false;
  const runtime = createMiddlewareRuntime({ token: 'runtime-token', deadlineMs: 2000, fetch: async (url, options) => {
    calls.push({ url, options });
    if (url.endsWith('/capabilities')) return Response.json(fixture.capabilities);
    const request = JSON.parse(options.body);
    const plan = structuredClone(fixture.plan);
    plan.input_digest = await sha256(options.body);
    plan.recovery.binding_id = request.recovery_binding?.id ?? '';
    if (corrupt) plan.replacements[0].original_sha256 = '0'.repeat(64);
    return Response.json(plan);
  }});
  await runtime.ready();
  const binding = runtime.recovery(fixture.request.scope);
  const result = await runtime.optimize(input(binding));
  assert.equal(result.status, 'optimized');
  assert.equal(result.replacements.length, 1);
  const sent = JSON.parse(calls.at(-1).options.body);
  assert.deepEqual(sent, { ...fixture.request, recovery_binding: { ...fixture.request.recovery_binding, id: binding.id } });
  assert.deepEqual(calls.at(-1).options.headers, { 'Content-Type': 'application/json', Authorization: 'Bearer runtime-token' });
  assert.equal(calls.at(-1).options.redirect, 'error');
  corrupt = true;
  const bad = await runtime.optimize(input(binding));
  assert.equal(bad.reason, 'invalid_plan');
  assert.equal(bad.cacheContinuity, 'unavailable');
  assert.deepEqual(bad.replacements, []);
  assert.equal(runtime.ownsBinding({ ...binding }, fixture.request.scope), false, 'schema/look-alike cannot attest executor');
  runtime.close();
});

test('off and pre-dispatch cancellation send no content', async () => {
  let calls = 0;
  const runtime = createMiddlewareRuntime({ mode: 'off', fetch: async () => { calls++; throw new Error('unexpected'); } });
  assert.equal((await runtime.optimize(input(null))).status, 'off');
  const controller = new AbortController(); controller.abort();
  await assert.rejects(runtime.optimize({ ...input(null), signal: controller.signal }), { name: 'AbortError' });
  assert.equal(calls, 0);
});

test('recovery schema and executor stay immutable across registrations', () => {
  const runtime = createMiddlewareRuntime();
  try {
    const binding = runtime.recovery(fixture.request.scope);
    assert.throws(() => { binding.inputSchema.properties.handle.type = 'integer'; }, TypeError);
    assert.throws(() => { binding.inputSchema.required.push('extra'); }, TypeError);
    assert.throws(() => { binding.execute = () => 'wrong executor'; }, TypeError);
    assert.throws(() => { binding.scope.session_id = 'other'; }, TypeError);
    assert.equal(runtime.ownsBinding(binding, fixture.request.scope), true);
    const next = runtime.recovery(fixture.request.scope);
    assert.equal(next.inputSchema.properties.handle.type, 'string');
    assert.deepEqual(next.inputSchema.required, ['handle']);
    assert.equal(runtime.ownsBinding({ ...binding }, fixture.request.scope), false);
  } finally { runtime.close(); }
});

test('runtime outage and circuit breaker never become inference retries', async () => {
  let calls = 0;
  const runtime = createMiddlewareRuntime({ fetch: async () => { calls++; throw new Error('contains sensitive provider body'); } });
  for (let i=0; i<3; i++) assert.equal((await runtime.optimize(input(null))).reason, 'runtime_unavailable');
  assert.equal((await runtime.optimize(input(null))).reason, 'circuit_open');
  assert.equal(calls, 3);
  assert.throws(() => createMiddlewareRuntime({ endpoint: 'https://remote.example' }), { code: 'remote_content_not_enabled' });
  assert.throws(() => createMiddlewareRuntime({ endpoint: 'https://user:secret@remote.example', allowRemoteContent: true }), { code: 'invalid_endpoint' });
});

test('valid fallback plans never claim continuity for unavailable frozen choices or recovery', async () => {
  for (const reason of ['cache_state_unavailable', 'recovery_unavailable', 'protected']) {
    const diagnostics = [];
    const runtime = createMiddlewareRuntime({ deadlineMs: 2000, onDiagnostic: event => diagnostics.push(event), fetch: async (url, options) => {
      if (url.endsWith('/capabilities')) return Response.json(fixture.capabilities);
      const plan = structuredClone(fixture.plan);
      plan.input_digest = await sha256(options.body);
      plan.status = 'bypassed'; plan.reason = reason;
      plan.skipped = [{ segment_id: plan.replacements[0].segment_id, reason }]; plan.replacements = [];
      plan.measurement.tokens_after = plan.measurement.tokens_before; plan.measurement.unique_tokens_reduced = 0;
      return Response.json(plan);
    }});
    try {
      const outcome = await runtime.optimize(input(runtime.recovery(fixture.request.scope)));
      assert.equal(outcome.reason, reason);
      assert.deepEqual(outcome.replacements, []);
      const expected = reason === 'protected' ? 'persistent_choices' : 'unavailable';
      assert.equal(outcome.cacheContinuity, expected);
      assert.deepEqual(diagnostics, [{ code: reason, cacheContinuity: expected }]);
    } finally { runtime.close(); }
  }
});

test('deadline cancels stalled custom response body without waiting for its transport', async () => {
  let cancelled = false;
  const runtime = createMiddlewareRuntime({ deadlineMs:30, fetch:async url => {
    if (url.endsWith('/capabilities')) return Response.json(fixture.capabilities);
    return new Response(new ReadableStream({ pull:()=>new Promise(()=>{}), cancel:()=>{cancelled=true;} }));
  }});
  await runtime.ready();
  const result = await runtime.optimize(input(runtime.recovery(fixture.request.scope)));
  assert.equal(result.reason,'deadline');
  assert.equal(result.cacheContinuity,'unavailable');
  assert.equal(cancelled,true);
});

test('deadline and stale-scope failures do not open the runtime outage circuit', async () => {
  let discovery = 0, mode = 'deadline';
  const runtime = createMiddlewareRuntime({ deadlineMs: 1000, fetch: async url => {
    if (url.endsWith('/capabilities')) { discovery++; return Response.json(fixture.capabilities); }
    throw new MiddlewareError(mode);
  }});
  try {
    for (let i = 0; i < 5; i++) assert.equal((await runtime.optimize(input(null))).reason, 'deadline');
    assert.equal(discovery, 1, 'a scheduling deadline does not invalidate known capabilities');
    mode = 'epoch_changed';
    for (let i = 0; i < 5; i++) assert.equal((await runtime.optimize(input(null))).reason, 'epoch_changed');
    mode = 'runtime_unavailable';
    for (let i = 0; i < 3; i++) assert.equal((await runtime.optimize(input(null))).reason, 'runtime_unavailable');
    assert.equal((await runtime.optimize(input(null))).reason, 'circuit_open');
  } finally { runtime.close(); }
});

test('bounded background receipts cannot consume optimizer transport slots', async () => {
  let receiptCalls = 0, optimizeCalls = 0, releaseReceipt, lateCancelled = false;
  const runtime = createMiddlewareRuntime({ deadlineMs: 5000, fetch: async (url, options) => {
    if (url.endsWith('/capabilities')) return Response.json(fixture.capabilities);
    if (url.endsWith('/receipts')) {
      receiptCalls++;
      // The custom receipt transport deliberately ignores AbortSignal.
      return new Promise(resolve => { releaseReceipt = resolve; });
    }
    optimizeCalls++;
    const request = JSON.parse(options.body), plan = structuredClone(fixture.plan);
    plan.input_digest = await sha256(options.body);
    plan.recovery.binding_id = request.recovery_binding.id;
    return Response.json(plan);
  } });
  try {
    await runtime.ready();
    const receipts = Array.from({ length: 32 }, () => runtime.observe({ schema_version: 1, scope: fixture.request.scope,
      logical_call_id: 'background', attempt_id: 'one', event_kind: 'dispatch_intent', plan_id: null, usage: null, provider_request_sha256: null }));
    const binding = runtime.recovery(fixture.request.scope);
    const outcomes = await Promise.all(Array.from({ length: 16 }, () => runtime.optimize(input(binding))));
    assert.equal(optimizeCalls, 16);
    assert.ok(outcomes.every(outcome => outcome.status === 'optimized'));
    assert.equal(receiptCalls, 1, 'one bounded metadata transport is active');
    runtime.close();
    await Promise.all(receipts);
    assert.equal(receiptCalls, 1, 'close drops queued receipt delivery');
    releaseReceipt(new Response(new ReadableStream({ cancel() { lateCancelled = true; } })));
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(lateCancelled, true, 'late ignored-abort transport body is disposed');
  } finally { runtime.close(); }
});

test('untested native versions decline without network traffic and strict mode stays explicit', () => {
  const diagnostics = [];
  const runtime = createMiddlewareRuntime({ fetch: () => { throw new Error('unexpected network'); }, onDiagnostic: event => diagnostics.push(event) });
  const outcome = runtime.decline('unsupported_version');
  assert.equal(outcome.reason, 'unsupported_version'); assert.equal(outcome.plan, null); assert.deepEqual(outcome.replacements, []);
  assert.deepEqual(diagnostics, [{ code: 'unsupported_version', cacheContinuity: 'unavailable' }]);
  runtime.close();
  const strict = createMiddlewareRuntime({ strict: true });
  assert.throws(() => strict.decline('unsupported_version'), error => error.code === 'unsupported_version'); strict.close();
  const off = createMiddlewareRuntime({ mode: 'off', strict: true, onDiagnostic: () => { throw new Error('off diagnostic'); } });
  assert.equal(off.decline('unsupported_version').status, 'off'); off.close();
});
