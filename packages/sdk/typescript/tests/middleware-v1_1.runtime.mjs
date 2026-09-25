// Protocol 1.1 parity vectors (TypeScript half). Iterates every client-side section of
// ../../parity/middleware-v1_1.fixtures.json; caveman_cloud's tests run the same file.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import * as mw from '../dist/middleware/index.js';

const load = name => readFile(new URL(`../../parity/${name}`, import.meta.url), 'utf8').then(JSON.parse);
const v11 = await load('middleware-v1_1.fixtures.json');
const base = await load(v11.base);

/** Apply {op: set|delete|append, path, value} patches to a deep copy of `doc`. */
function patch(doc, patches) {
  const out = structuredClone(doc);
  for (const { op, path, value } of patches) {
    let target = out;
    for (const key of path.slice(0, -1)) target = target[key];
    const last = path.at(-1);
    if (op === 'set') target[last] = structuredClone(value);
    else if (op === 'delete') delete target[last];
    else if (op === 'append') target[last].push(structuredClone(value));
    else throw new Error(`unknown patch op ${op}`);
  }
  return out;
}

test('constants and reason catalog match the contract', () => {
  const c = v11.constants;
  assert.deepEqual({ ...mw.PROTOCOL_RANGE }, c.protocol);
  assert.deepEqual([...mw.KNOWN_FEATURES], c.features.known);
  assert.deepEqual([...mw.CLIENT_FEATURES], c.features.client);
  assert.equal(mw.MIDDLEWARE_FEATURES_HEADER, c.headers.features);
  assert.equal(mw.MIDDLEWARE_CLIENT_HEADER, c.headers.client);
  assert.equal(mw.CLIENT_FEATURES_HEADER_VALUE, c.client_features_header_value);
  assert.equal(mw.MIDDLEWARE_CLIENT_PRODUCT, c.client_header_products.typescript);
  assert.deepEqual([...mw.CLIENT_RECOVERY_ALLOWLIST], c.client_recovery_allowlist);
  assert.equal(mw.RECOVERY_MARKER_PREFIX, c.marker_prefix);
  assert.equal(mw.RECOVERY_HANDLE_PATTERN, c.handle_pattern);
  assert.equal(mw.REASON_PATTERN, c.reason_pattern);
  assert.equal(mw.SCOPE_TOKEN_PATTERN, c.scope.token_pattern);
  assert.equal(mw.SCOPE_HASH_PREFIX, c.scope.hash_prefix);
  assert.equal(mw.SCOPE_HASH_HEX_CHARS, c.scope.hash_hex_chars);
  assert.equal(mw.DEFAULT_BRANCH_ID, c.scope.default_branch_id);
  assert.equal(mw.DEFAULT_CACHE_EPOCH, c.scope.default_cache_epoch);
  assert.deepEqual({ ...mw.BREAKER_DEFAULTS }, c.breaker);
  assert.deepEqual({ ...mw.MIDDLEWARE_DEFAULTS }, c.defaults);
  assert.deepEqual(JSON.parse(JSON.stringify(mw.REASON_CATALOG)), v11.reason_catalog);
});

test('scope_tokens and scopes', () => {
  for (const v of v11.scope_tokens) {
    const input = v.input_repeat ? v.input_repeat.text.repeat(v.input_repeat.count) : v.input;
    assert.equal(mw.normalizeScopeToken(input), v.output, v.id);
  }
  for (const v of v11.scopes) assert.deepEqual(mw.normalizeScope(v.input), v.output, v.id);
});

test('capabilities_parsing', () => {
  for (const v of v11.capabilities_parsing) {
    const doc = patch(base.capabilities, v.patches);
    if (!v.expect.ok) { assert.throws(() => mw.parseCapabilities(doc), { code: v.expect.reason }, v.id); continue; }
    const view = mw.parseCapabilities(doc);
    assert.deepEqual({ legacy: view.legacy, features: [...view.features], transforms: view.transforms.map(t => t.transform_id), mode: view.mode,
      limits: { ...view.limits }, max_retention_seconds: view.max_retention_seconds },
    { legacy: v.expect.legacy, features: v.expect.features, transforms: v.expect.transforms, mode: v.expect.mode, limits: v.expect.limits,
      max_retention_seconds: v.expect.max_retention_seconds }, v.id);
  }
});

test('plan_constraints (validator; parsed view and raw document)', async () => {
  for (const v of v11.plan_constraints) {
    const docs = patch({ capabilities: base.capabilities, request: base.request, plan: base.plan }, v.patches);
    for (const caps of [mw.parseCapabilities(docs.capabilities), docs.capabilities]) {
      const run = mw.validatePlan(docs.plan, docs.request, base.plan.input_digest, caps);
      if (v.expect.valid) assert.deepEqual(await run, docs.plan, v.id);
      else await assert.rejects(run, { code: v.expect.reason }, v.id);
    }
  }
});

test('plan_constraints refresh_capabilities: the next optimize issues exactly that many capabilities GETs', async () => {
  const r = base.request;
  for (const v of v11.plan_constraints.filter(v => v.expect.valid)) {
    const docs = patch({ capabilities: base.capabilities, request: base.request, plan: base.plan }, v.patches);
    let gets = 0;
    const runtime = mw.createMiddlewareRuntime({ deadlineMs: 5000, fetch: async (url, init) => {
      if (url.endsWith('/capabilities')) { gets++; return Response.json(docs.capabilities); }
      const plan = structuredClone(docs.plan);
      plan.input_digest = await mw.sha256(init.body);
      plan.recovery.binding_id = JSON.parse(init.body).recovery_binding.id;
      return Response.json(plan);
    } });
    try {
      const binding = runtime.recovery(r.scope);
      const call = () => runtime.optimize({ scope: r.scope, adapter: r.adapter, binding, manifest: r.context_manifest, requestId: r.request_id,
        candidates: r.segments.map(s => ({ id: s.id, sourceId: s.source_id, content: s.content })) });
      assert.equal((await call()).status, 'optimized', v.id);
      const before = gets;
      assert.equal((await call()).status, 'optimized', v.id);
      await new Promise(resolve => setImmediate(resolve));
      assert.equal(gets - before, v.expect.refresh_capabilities ? 1 : 0, v.id);
    } finally { runtime.close(); }
  }
});

test('failure_classification, server_error_codes and legacy_conditions', () => {
  for (const v of v11.failure_classification) assert.deepEqual(mw.classifyFailure(v.input), v.expect, v.id);
  for (const e of v11.server_error_codes) {
    for (const status of [e.status, e.legacy_status].filter(Boolean)) {
      const out = mw.classifyFailure({ transport: 'response', status, body: JSON.stringify({ schema_version: 1, error: { code: e.code } }) });
      assert.equal(out.reason, e.code === 'request_timeout' ? 'deadline' : e.code, e.code);
    }
  }
  for (const e of v11.examples.error_envelopes) assert.match(e.error.code, new RegExp(mw.REASON_PATTERN));
  for (const c of v11.legacy_conditions) {
    assert.equal(mw.classifyFailure({ transport: 'response', status: c.legacy_status, body: JSON.stringify({ schema_version: 1, error: { code: c.legacy_code } }) }).reason, c.legacy_code, c.condition);
  }
});

test('breaker_sequences', () => {
  for (const v of v11.breaker_sequences) {
    const breaker = new mw.CircuitBreaker();
    for (const [i, step] of v.steps.entries()) {
      let sent = false;
      if (step.result !== 'local') { sent = breaker.allow(step.at_ms); if (sent) breaker.record(step.result, step.at_ms); }
      assert.deepEqual({ sent, state: breaker.state }, step.expect, `${v.id} step ${i}`);
    }
  }
});

test('deadlines', () => {
  for (const v of v11.deadlines) {
    const out = mw.resolveDeadlines({ deadlineMs: v.input.deadline_ms, retrieveDeadlineMs: v.input.retrieve_deadline_ms }, v.input.limits);
    assert.deepEqual({ optimize_ms: out.optimizeMs, retrieve_ms: out.retrieveMs }, v.expect, v.id);
  }
});

test('budgets, manifest_windows and opaque_manifest_values', async () => {
  for (const v of v11.budgets) {
    const out = mw.planBudget(v.input.items, { maxSegments: v.input.max_segments, maxBytes: v.input.max_bytes }, v.input.replaced);
    const bypass = v.input.items.length && !out.admitted.length ? 'payload_budget' : null;
    assert.deepEqual({ ...out, bypass }, v.expect, v.id);
  }
  for (const v of v11.manifest_windows) assert.equal(mw.manifestWindow(v.input.sizes, v.input.max_items, v.input.max_bytes), v.expect, v.id);
  for (const v of v11.opaque_manifest_values) {
    const input = 'bytes_base64' in v.input ? Buffer.from(v.input.bytes_base64, 'base64') : 'string' in v.input ? v.input.string : v.input;
    assert.deepEqual(await mw.opaqueManifestValue(input), v.expect, v.id);
  }
});

test('endpoints and proxies', () => {
  for (const v of v11.endpoints) {
    const options = { allowRemoteContent: v.input.allow_remote_content, allowInsecureTransport: v.input.allow_insecure_transport };
    if (v.expect.error) assert.throws(() => mw.resolveEndpoint(v.input.endpoint, options), { code: v.expect.error }, v.id);
    else assert.equal(`${mw.resolveEndpoint(v.input.endpoint, options)}caveman/v1/middleware/capabilities`, v.expect.capabilities_url, v.id);
  }
  for (const v of v11.proxies) assert.equal(mw.resolveProxy(v.input.url, v.input.env), v.expect, v.id);
});

test('examples: session delete responses parse; decision events carry exactly the contract keys', async () => {
  for (const response of v11.examples.session_delete_responses) {
    const runtime = mw.createMiddlewareRuntime({ fetch: async (url, init) => {
      assert.ok(url.endsWith('/sessions/delete'));
      assert.deepEqual(JSON.parse(init.body), v11.examples.session_delete_request);
      return Response.json(response);
    } });
    try { assert.deepEqual(await runtime.deleteSession(v11.examples.session_delete_request.scope), response); } finally { runtime.close(); }
  }
  for (const v of v11.examples.session_delete_malformed) {
    const runtime = mw.createMiddlewareRuntime({ fetch: async () => Response.json(v.json) });
    try {
      const deleting = runtime.deleteSession(v11.examples.session_delete_request.scope);
      if (v.expect.error) await assert.rejects(deleting, error => error instanceof mw.MiddlewareError && error.code === v.expect.error, v.id);
      else assert.deepEqual(await deleting, v.expect.result, v.id);
    } finally { runtime.close(); }
  }
  const [applied, skipped] = v11.examples.decision_events;
  const events = [], runtime = mw.createMiddlewareRuntime({ onDecision: event => events.push(event), fetch: () => { throw new Error('no I/O'); } });
  try {
    runtime.report({ status: 'bypassed', reason: 'no_candidate', replacements: [], plan: null, request: null, cacheContinuity: 'unavailable' }, { adapter: 'langchain' });
    assert.deepEqual(events[0], skipped, 'no_candidate emits an event too');
    runtime.report({ status: 'optimized', reason: applied.reason, cacheContinuity: applied.cache_continuity, latencyMs: applied.latency_ms + 0.9,
      counts: { ...applied.counts, replaced: 0, reused: 0 }, runtimeBuild: 'ignored-when-plan-present',
      replacements: [{ transform_id: applied.transform_ids[0], reused: false }], plan: { runtime_build: applied.runtime_build },
      request: { adapter: { id: applied.adapter }, logical_call_id: applied.logical_call_id, attempt_id: applied.attempt_id } });
    assert.deepEqual(events[1], applied);
    assert.ok(events.every(event => Object.isFrozen(event) && Object.isFrozen(event.counts)));
  } finally { runtime.close(); }
});

test('runtime_scenarios (the Python SDK runs the same scenarios)', async t => {
  const r = base.request, warn = console.warn;
  for (const scenario of v11.runtime_scenarios) await t.test(scenario.id, async () => {
    const caps = patch(base.capabilities, scenario.capabilities ?? []);
    let queue = {}, latency = {}, requests = [];
    const lines = [], events = [];
    const fetch = async (url, init) => {
      const route = url.split('/caveman/v1/middleware/')[1], spec = queue[route]?.shift();
      requests.push(route);
      if (latency[route]) await new Promise(resolve => setTimeout(resolve, latency[route]));
      let body;
      if (spec?.json !== undefined) body = JSON.stringify(spec.json);
      else if (spec?.body_text !== undefined) body = spec.body_text;
      else if (spec?.body_base64 !== undefined) body = Buffer.from(spec.body_base64, 'base64');
      else if (route === 'capabilities') body = JSON.stringify(patch(caps, spec?.caps ?? []));
      else {
        const sent = JSON.parse(init.body), plan = structuredClone(base.plan);
        Object.assign(plan, { request_id: sent.request_id, input_digest: await mw.sha256(init.body) });
        plan.recovery.binding_id = sent.recovery_binding?.id ?? null;
        body = JSON.stringify(patch(plan, spec?.plan ?? []));
      }
      return new Response(body, { status: spec?.status ?? 200, headers: spec?.headers ?? {} });
    };
    const runtime = mw.createMiddlewareRuntime({ deadlineMs: 5000, ...scenario.options, fetch, onDecision: event => events.push(event) });
    let last = null;
    const optimize = step => {
      const scope = step.scope ?? r.scope;
      return runtime.optimize({ scope, adapter: step.adapter === null ? undefined : { ...r.adapter, id: scenario.adapter_id },
        binding: step.scope ? null : runtime.recovery(scope), manifest: step.manifest ?? r.context_manifest, requestId: r.request_id,
        candidates: (step.candidates ?? r.segments).map(c => ({ id: c.id, content: c.content, ...(c.source_id ? { sourceId: c.source_id } : {}) })) });
    };
    console.warn = line => lines.push(line);
    try {
      for (const [i, step] of scenario.steps.entries()) {
        const label = `${scenario.id} step ${i}`, e = step.expect;
        queue = {};
        for (const response of step.responses ?? []) (queue[response.route] ??= []).push(response);
        latency = step.latency_ms ?? {}; requests = []; lines.length = 0;
        let result = null;
        if (step.op === 'optimize') result = last = await optimize(step);
        else if (step.op === 'report') runtime.report(step.optimization ? { replacements: [], plan: null, request: null, cacheContinuity: 'unavailable', ...step.optimization } : last,
          step.adapter ? { adapter: step.adapter } : {});
        else if (step.op === 'decline') runtime.decline(step.reason, step.adapter);
        else if (step.op === 'preflight') result = await runtime.preflight();
        else if (step.op === 'concurrent_optimize') await Promise.all(Array.from({ length: step.count }, () => optimize(step)));
        else throw new Error(`unknown scenario op ${step.op}`);
        if ('reason' in e) assert.equal(result.reason, e.reason, label);
        if ('status' in e) assert.equal(result.status, e.status, label);
        if ('requests' in e) assert.deepEqual(requests, e.requests, label);
        if ('capabilities_gets' in e) assert.equal(requests.filter(route => route === 'capabilities').length, e.capabilities_gets, label);
        if ('warnings' in e) assert.deepEqual(lines, e.warnings, label);
        if ('preflight' in e) assert.deepEqual({ status: result.status, reason: result.reason }, e.preflight, label);
        for (const [key, value] of Object.entries(e.event ?? {})) assert.deepEqual(events.at(-1)[key], value, `${label} event.${key}`);
      }
    } finally { console.warn = warn; runtime.close(); }
  });
});
