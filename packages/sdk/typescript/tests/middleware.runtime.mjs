import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { createMiddlewareRuntime, sha256 } from '../dist/middleware/index.js';
import { parseCapabilities, validatePlan, validatePage, MiddlewareError } from '../dist/middleware/validate.js';

const fixture = JSON.parse(await readFile(new URL('../../parity/middleware.fixtures.json', import.meta.url), 'utf8'));
test('shared middleware wire, hashes, and atomic invalid-plan vectors', async () => {
  assert.equal(JSON.stringify(fixture.request), fixture.request_wire);
  for (const vector of fixture.digest_vectors) assert.equal(await sha256(vector.text), vector.sha256);
  const caps = parseCapabilities(fixture.capabilities);
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
  assert.deepEqual(calls.at(-1).options.headers, { 'Content-Type': 'application/json', 'Caveman-Middleware-Features': 'http_status_v2, revision_tolerant',
    'Caveman-Middleware-Client': 'caveman-sdk-typescript/1.2.0', Authorization: 'Bearer runtime-token' });
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

test('record mode prepares observations without replacements or applied reports', async () => {
  const runtime = createMiddlewareRuntime({ mode: 'record', deadlineMs: 2000, fetch: async (url, options) => {
    if (url.endsWith('/capabilities')) return Response.json(fixture.capabilities);
    const request = JSON.parse(options.body), plan = structuredClone(fixture.plan);
    assert.equal(request.mode, 'record');
    plan.input_digest = await sha256(options.body);
    plan.status = 'record'; plan.reason = 'record';
    plan.replacements = [];
    plan.skipped = request.segments.map(segment => ({ segment_id: segment.id, reason: 'record' }));
    plan.measurement.tokens_after = plan.measurement.tokens_before;
    plan.measurement.unique_tokens_reduced = 0;
    return Response.json(plan);
  }});
  try {
    const result = await runtime.optimize(input(null));
    assert.equal(result.status, 'record');
    assert.deepEqual(result.replacements, []);
    assert.equal(runtime.report(result).status, 'recorded');
  } finally { runtime.close(); }
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
  for (let i=0; i<5; i++) assert.equal((await runtime.optimize(input(null))).reason, 'runtime_unavailable');
  assert.equal((await runtime.optimize(input(null))).reason, 'circuit_open');
  assert.equal(calls, 5);
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
  await sha256('warm WebCrypto so the deadline lands on the stalled body, not on first-use initialization');
  const result = await runtime.optimize(input(runtime.recovery(fixture.request.scope)));
  assert.equal(result.reason,'deadline');
  assert.equal(result.cacheContinuity,'unavailable');
  assert.equal(cancelled,true);
});

test('conflicts and 4xx neither open the breaker nor clear capabilities; server deadlines count (B3, B5)', async () => {
  let discovery = 0, optimizeCalls = 0, status = 409, code = 'epoch_changed';
  const runtime = createMiddlewareRuntime({ deadlineMs: 1000, fetch: async url => {
    if (url.endsWith('/capabilities')) { discovery++; return Response.json(fixture.capabilities); }
    optimizeCalls++;
    return Response.json({ schema_version: 1, error: { code } }, { status });
  }});
  try {
    const binding = runtime.recovery(fixture.request.scope);
    for (let i = 0; i < 12; i++) assert.equal((await runtime.optimize(input(binding))).reason, 'epoch_changed');
    status = 400; code = 'invalid_request';
    for (let i = 0; i < 12; i++) assert.equal((await runtime.optimize(input(binding))).reason, 'invalid_request');
    assert.equal(discovery, 1, 'a 4xx must not cost a capabilities round trip per call');
    assert.equal(optimizeCalls, 24);
    status = 504; code = 'deadline';
    for (let i = 0; i < 5; i++) assert.equal((await runtime.optimize(input(binding))).reason, 'deadline');
    assert.equal((await runtime.optimize(input(binding))).reason, 'circuit_open', 'deadlines count toward the breaker');
    assert.equal(discovery, 1, 'a deadline does not invalidate known capabilities');
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

test('untested native versions decline without network traffic and strict mode stays explicit', async () => {
  const diagnostics = [];
  const runtime = createMiddlewareRuntime({ fetch: () => { throw new Error('unexpected network'); }, onDiagnostic: event => diagnostics.push(event) });
  const outcome = runtime.decline('unsupported_version');
  assert.equal(outcome.reason, 'unsupported_version'); assert.equal(outcome.plan, null); assert.deepEqual(outcome.replacements, []);
  assert.deepEqual(diagnostics, [{ code: 'unsupported_version', cacheContinuity: 'unavailable' }]);
  runtime.close();
  const strict = createMiddlewareRuntime({ strict: true, deadlineMs: 2000, fetch: async () => Response.json(fixture.capabilities) });
  assert.equal(strict.decline('unsupported_version').reason, 'unsupported_version', 'nothing raises at wrap time');
  await assert.rejects(strict.ready(), { code: 'unsupported_version' });
  assert.equal((await strict.preflight()).reason, 'unsupported_version'); strict.close();
  const off = createMiddlewareRuntime({ mode: 'off', strict: true, onDiagnostic: () => { throw new Error('off diagnostic'); } });
  assert.equal(off.decline('unsupported_version').status, 'off'); off.close();
  assert.deepEqual([strict.strict, off.strict, runtime.strict], [true, true, false], 'adapters read strict to raise adapter_error');
  // Any catalog reason, and the warn-once line names the adapter instead of `adapter=-`.
  const lines = [], warn = console.warn; console.warn = line => lines.push(line);
  const named = createMiddlewareRuntime({ strict: true, fetch: async () => Response.json(fixture.capabilities) });
  try { assert.equal(named.decline('recovery_name_conflict', 'decline-test').reason, 'recovery_name_conflict'); } finally { console.warn = warn; }
  assert.deepEqual(lines, ['Caveman middleware passed content through unchanged: adapter=decline-test reason=recovery_name_conflict']);
  await assert.rejects(named.ready(), { code: 'recovery_name_conflict' }); named.close();
});
