#!/usr/bin/env node
// Black-box conformance kit for the Caveman middleware protocol
// (docs/technical/middleware-protocol.md). Runs against any runtime URL and
// prints TAP; exit 0 = conformant, 1 = a check failed, 2 = usage, 3 = ajv
// missing (run pnpm install for @caveman-ai/contracts).
//
//   node run.mjs <baseURL> [--token T] [--namespace N] [--foreign-token T2] [--legacy-only]
//
// The kit version tracks the protocol: 1.1.x checks protocol 1.1 (and the 1.0
// view every 1.1 runtime must still serve). See README.md.
import assert from 'node:assert/strict';
import { createHash, randomBytes } from 'node:crypto';
import http from 'node:http';
import https from 'node:https';
import { readdir, readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

export const KIT_VERSION = '1.1.0';
const here = path.dirname(fileURLToPath(import.meta.url));
const contracts = path.resolve(here, '..');
const fixture = JSON.parse(await readFile(path.resolve(contracts, '../../sdk/parity/middleware-v1_1.fixtures.json'), 'utf8'));
const FEATURES = fixture.constants.client_features_header_value;
const MARKER = fixture.constants.marker_prefix;
const KNOWN_FEATURES = fixture.constants.features.known;

let args;
try {
  args = parseArgs({ allowPositionals: true, options: {
    token: { type: 'string' }, namespace: { type: 'string', default: 'conformance' },
    'foreign-token': { type: 'string' }, 'legacy-only': { type: 'boolean', default: false } } });
  if (args.positionals.length !== 1) throw new Error('expected exactly one <baseURL>');
} catch (error) {
  console.error(`${error.message}\nusage: node run.mjs <baseURL> [--token T] [--namespace N] [--foreign-token T2] [--legacy-only]`);
  process.exit(2);
}
const base = `${args.positionals[0].replace(/\/+$/, '')}/caveman/v1/middleware/`;
const { token, namespace, 'foreign-token': foreignToken, 'legacy-only': legacyOnly } = args.values;

let Ajv2020;
try {
  const mod = createRequire(path.join(contracts, 'package.json'))('ajv/dist/2020.js');
  Ajv2020 = mod.default ?? mod;
} catch {
  console.error('ajv not installed for packages/shared/contracts (run: pnpm install --filter @caveman-ai/contracts)');
  process.exit(3);
}
const ajv = new Ajv2020({ allErrors: true, strict: true });
for (const file of (await readdir(path.join(contracts, 'schemas'))).filter(name => name.endsWith('.schema.json'))) {
  ajv.addSchema(JSON.parse(await readFile(path.join(contracts, 'schemas', file), 'utf8')));
}
const SCHEMA_BASE = 'https://raw.githubusercontent.com/JuliusBrussee/caveman/main/packages/shared/contracts/schemas/';
function assertSchema(name, value, label = name) {
  const validate = ajv.getSchema(`${SCHEMA_BASE}middleware-${name}.schema.json`);
  if (!validate(value)) throw new assert.AssertionError({ message: `${label} violates middleware-${name}: ${ajv.errorsText(validate.errors)}` });
}

// ---------------------------------------------------------------- TAP

let count = 0, failed = 0, skipped = 0;
const trail = [];
async function test(name, fn) {
  count++;
  trail.length = 0;
  try {
    const skip = await fn();
    if (typeof skip === 'string') { skipped++; console.log(`ok ${count} - ${name} # SKIP ${skip}`); }
    else console.log(`ok ${count} - ${name}`);
  } catch (error) {
    failed++;
    console.log(`not ok ${count} - ${name}`);
    console.log(`  ---\n  message: ${JSON.stringify(String(error?.message ?? error))}\n  requests: ${JSON.stringify(trail.slice(-3))}\n  ...`);
  }
}

// ---------------------------------------------------------------- HTTP

const ROUTE_SCHEMA = { capabilities: 'capabilities', optimize: 'plan', retrieve: 'page', receipts: 'receipt-response', 'sessions/delete': 'session-delete-response' };
let responses = 0;

/** Every response: JSON body, its schema (route schema on 200, error envelope otherwise), no-store, nosniff, and
 * Retry-After on each 429/503. A violation fails the check that made the call. */
function inspect(route, status, header, text) {
  responses++;
  trail.push(`${route} -> ${status} ${text.slice(0, 160)}`);
  let json;
  try { json = JSON.parse(text); } catch { assert.fail(`${route}: ${status} body is not JSON`); }
  assertSchema(status === 200 && ROUTE_SCHEMA[route] ? ROUTE_SCHEMA[route] : 'error', json, `${route} ${status} body`);
  assert.match(header('cache-control') ?? '', /\bno-store\b/, `${route}: Cache-Control: no-store missing`);
  assert.equal(header('x-content-type-options'), 'nosniff', `${route}: X-Content-Type-Options: nosniff missing`);
  // A 1.1 obligation in both views; protocol 1.0 runtimes (--legacy-only) never sent it.
  if (!legacyOnly && (status === 429 || status === 503)) assert.match(header('retry-after') ?? '', /^[1-9][0-9]*$/, `${route}: ${status} without Retry-After >= 1`);
  return { status, json, header };
}

function headersFor({ v11, auth = token, extra = {} }) {
  const headers = { ...extra };
  if (auth) headers.authorization = `Bearer ${auth}`;
  if (v11) headers['caveman-middleware-features'] = FEATURES;
  return headers;
}

async function call(route, { v11, body, method = body === undefined ? 'GET' : 'POST', auth, headers: extra, contentType = 'application/json' } = {}) {
  const headers = headersFor({ v11, auth, extra });
  if (body !== undefined) headers['content-type'] = contentType;
  const response = await fetch(base + route, { method, headers, redirect: 'manual',
    body: body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body) });
  return inspect(route, response.status, name => response.headers.get(name), await response.text());
}

/** A request whose body never finishes arriving (§6 slow_request_body). */
function stalledBody(route, v11) {
  const url = new URL(base + route);
  return new Promise((resolve, reject) => {
    const request = (url.protocol === 'https:' ? https : http).request(url, { method: 'POST',
      headers: { ...headersFor({ v11 }), 'content-type': 'application/json', 'content-length': 4096 } }, response => {
      let text = '';
      response.setEncoding('utf8');
      response.on('data', chunk => { text += chunk; });
      response.on('end', () => {
        request.destroy();
        try { resolve(inspect(route, response.statusCode, name => response.headers[name], text)); } catch (error) { reject(error); }
      });
    });
    request.on('error', reject);
    request.setTimeout(30_000, () => request.destroy(new Error('stalled body got no answer within 30s')));
    request.write('{"schema_version":1,');
  });
}

// ---------------------------------------------------------------- protocol helpers

const sha256 = value => createHash('sha256').update(value).digest('hex');
const bytes = value => Buffer.byteLength(value, 'utf8');
const id = prefix => `${prefix}-${randomBytes(6).toString('hex')}`;
const run = randomBytes(4).toString('hex');
const scope = label => ({ namespace, session_id: `kit-${run}-${label}`, branch_id: 'main', cache_epoch: '0' });
const unknownHandle = () => `cmw_${randomBytes(24).toString('hex')}`;

function noisy(lines = 150, tag = '') {
  let text = '';
  for (let i = 0; i < lines; i++) text += `[INFO] reading row ${i}: café 🌍 exact-value-${String(i).padStart(3, '0')}${tag} with verbose repeated details\r\n`;
  return `${text}[ERROR] preserve this diagnostic exactly\r\n`;
}

function optimizeRequest(caps, { scope: s, content = noisy(), sequence = 1, manifest, binding } = {}) {
  const key = id('req');
  const segment = { id: 'tool-1', kind: 'tool_result', cache_region: 'live_zone', content, sha256: sha256(content), source_id: 'document-1', protected: false, opaque: false };
  const request = { schema_version: 1, request_id: key, logical_call_id: id('call'), attempt_id: id('attempt'), idempotency_key: key, scope: s, sequence,
    adapter: { id: 'conformance-kit', version: KIT_VERSION, framework_version: '1', serialization_revision: 'kit-v1' }, model: null, mode: 'compress',
    policy: { revision: caps.policy_revision, transforms: caps.transforms.map(t => t.transform_id) }, segments: [segment],
    context_manifest: manifest ?? [{ id: 'msg-0', sha256: sha256('user turn') }, { id: 'msg-1', sha256: segment.sha256 }],
    recovery_binding: binding === undefined ? { id: id('binding'), kind: 'host_tool', tool_name: 'caveman_retrieve', overhead_text: 'A registered native tool.' } : binding };
  assertSchema('optimize', request, 'kit optimize request');
  return request;
}
const retrieveRequest = (s, handle, { offset = 0, limit = 0, query = '' } = {}) => {
  const request = { schema_version: 1, scope: s, handle, offset, limit, query };
  assertSchema('retrieve', request, 'kit retrieve request');
  return request;
};
const receipt = (s, extra = {}) => {
  const request = { schema_version: 1, scope: s, logical_call_id: id('call'), attempt_id: id('attempt'), event_kind: 'dispatch_intent',
    plan_id: null, usage: null, provider_request_sha256: null, ...extra };
  assertSchema('receipt', request, 'kit receipt');
  return request;
};

const errors = new Map(fixture.server_error_codes.map(entry => [entry.code, entry]));
const conditions = new Map(fixture.legacy_conditions.map(entry => [entry.condition, entry]));
const exercised = new Set();
function expectError(response, code, v11) {
  const entry = errors.get(code);
  assert.ok(entry, `kit bug: ${code} is not in server_error_codes`);
  exercised.add(code);
  const status = v11 ? entry.status : entry.legacy_status ?? entry.status;
  assert.equal(response.status, status, `expected ${status} ${code}, got ${response.status} ${JSON.stringify(response.json)}`);
  assert.equal(response.json.error.code, code);
}
function expectCondition(response, condition, v11) {
  const entry = conditions.get(condition);
  exercised.add(`condition:${condition}`);
  const [status, code] = v11 ? [entry.status, entry.code] : [entry.legacy_status, entry.legacy_code];
  assert.equal(response.status, status, `${condition}: expected ${status} ${code}, got ${response.status} ${JSON.stringify(response.json)}`);
  if (status !== 200) return assert.equal(response.json.error.code, code);
  // Decisions are 200 under http_status_v2: a bypassed plan carrying the reason (§6).
  assert.equal(response.json.status, 'bypassed');
  assert.deepEqual(response.json.replacements, []);
  assert.ok(response.json.skipped.some(skip => skip.reason === code), `no segment skipped with ${code}`);
}

// ---------------------------------------------------------------- the checks

console.log('TAP version 13');
console.log(`# caveman middleware conformance kit ${KIT_VERSION} (protocol ${fixture.protocol ?? '1.1'}) against ${base}`);

const caps = {};
await test('capabilities: legacy view (no features header) is one SDK 1.1.0 accepts', async () => {
  const response = await call('capabilities');
  assert.equal(response.status, 200);
  const doc = response.json;
  for (const transform of doc.transforms) {
    assert.ok(['exact_ccr', 'none'].includes(transform.recovery), `legacy view lists ${transform.transform_id} with recovery ${transform.recovery}`);
    assert.equal(transform.deterministic, true, `legacy view lists non-deterministic ${transform.transform_id}`);
  }
  for (const [key, value] of Object.entries(doc.limits)) assert.ok(Number.isSafeInteger(value) && value > 0, `limits.${key} = ${value}`);
  caps.legacy = doc;
});
if (!legacyOnly) {
  await test('capabilities: 1.1 view (features header) advertises protocol 1.1 and every 1.1 feature', async () => {
    const response = await call('capabilities', { v11: true });
    assert.equal(response.status, 200);
    const doc = response.json;
    assert.ok(Array.isArray(doc.features), 'no features array: a protocol 1.0 runtime (rerun with --legacy-only)');
    assert.ok(doc.protocol && doc.protocol.min <= 1 && doc.protocol.max >= 1, `protocol ${JSON.stringify(doc.protocol)} excludes 1`);
    for (const feature of KNOWN_FEATURES) assert.ok(doc.features.includes(feature), `feature ${feature} not advertised`);
    assert.ok(doc.max_retention_seconds >= doc.retention_seconds, 'max_retention_seconds < retention_seconds');
    caps.v11 = doc;
  });
  if (!caps.v11) {
    console.log('Bail out! no usable protocol 1.1 capabilities; rerun with --legacy-only against a protocol 1.0 runtime');
    process.exit(1);
  }
}
if (!caps.legacy) {
  console.log('Bail out! capabilities unavailable');
  process.exit(1);
}
const lifecycle = caps.v11?.features.includes('originals_lifecycle') ?? false;
const compressing = doc => doc.mode === 'compress' && doc.recovery && doc.persistent;

const views = legacyOnly ? [false] : [false, true];
for (const v11 of views) {
  const view = v11 ? '1.1' : '1.0';
  const doc = v11 ? caps.v11 : caps.legacy;
  const live = { scope: scope(`live-${view}`) };
  const needsCompress = () => compressing(doc) ? null : `runtime mode ${doc.mode}, recovery ${doc.recovery}, persistent ${doc.persistent}`;

  await test(`[${view}] optimize compresses a tool result into a valid, marked, smaller replacement`, async () => {
    if (needsCompress()) return needsCompress();
    const request = optimizeRequest(doc, { scope: live.scope });
    const body = JSON.stringify(request);
    const response = await call('optimize', { v11, body });
    assert.equal(response.status, 200);
    const plan = response.json, segment = request.segments[0];
    assert.equal(plan.status, 'optimized', `plan ${plan.status} ${plan.reason}`);
    assert.equal(plan.request_id, request.request_id);
    assert.equal(plan.input_digest, sha256(body), 'input_digest is not sha256 of the request body');
    assert.equal(plan.policy_revision, doc.policy_revision, 'plan revision is not the view revision');
    assert.equal(plan.recovery.binding_id, request.recovery_binding.id);
    assert.equal(plan.replacements.length, 1);
    const replacement = plan.replacements[0];
    assert.ok(request.policy.transforms.includes(replacement.transform_id));
    assert.equal(replacement.original_sha256, segment.sha256);
    assert.equal(replacement.sha256, sha256(replacement.text));
    assert.ok(replacement.text.startsWith(`${MARKER}${replacement.recovery_handle}]\n`), 'replacement lacks the recovery marker');
    assert.ok(bytes(replacement.text) < bytes(segment.content), 'replacement is not smaller in UTF-8 bytes');
    Object.assign(live, { request, plan, handle: replacement.recovery_handle, original: segment.content });
  });

  const needsLive = () => needsCompress() ?? (live.handle ? null : 'no live handle (optimize check failed)');
  await test(`[${view}] retrieve returns the exact original (sha256)`, async () => {
    if (needsLive()) return needsLive();
    const response = await call('retrieve', { v11, body: retrieveRequest(live.scope, live.handle) });
    assert.equal(response.status, 200);
    const page = response.json;
    assert.equal(page.complete, true);
    assert.equal(page.kind, 'original_page');
    assert.equal(page.next_offset, null);
    assert.equal(page.original_sha256, sha256(live.original));
    assert.equal(sha256(page.text), sha256(live.original), 'recovered text differs from the original');
    assert.equal(page.total_bytes, bytes(live.original));
  });

  await test(`[${view}] retrieve pages reassemble the exact original; an offset past the end is invalid_range`, async () => {
    if (needsLive()) return needsLive();
    const limit = 1000, pages = [];
    for (let offset = 0; offset !== null && pages.length < 100;) {
      const response = await call('retrieve', { v11, body: retrieveRequest(live.scope, live.handle, { offset, limit }) });
      assert.equal(response.status, 200);
      assert.equal(response.json.offset, offset);
      assert.ok(bytes(response.json.text) <= limit, 'page exceeds limit');
      pages.push(response.json.text);
      offset = response.json.next_offset;
    }
    assert.ok(pages.length > 1, 'original fit in one page; paging not exercised');
    assert.equal(sha256(pages.join('')), sha256(live.original));
    const past = retrieveRequest(live.scope, live.handle, { offset: bytes(live.original) + 1, limit });
    expectError(await call('retrieve', { v11, body: past }), 'invalid_range', v11);
  });

  await test(`[${view}] retrieve with a query answers an excerpt page`, async () => {
    if (needsLive()) return needsLive();
    const response = await call('retrieve', { v11, body: retrieveRequest(live.scope, live.handle, { query: 'ERROR diagnostic' }) });
    assert.equal(response.status, 200);
    assert.equal(response.json.kind, 'excerpt');
    assert.equal(response.json.complete, false);
  });

  await test(`[${view}] receipts are recorded with the fixed response shape`, async () => {
    const plan = live.plan?.replacement_set_id ?? null;
    for (const body of [receipt(live.scope, { plan_id: plan }), receipt(live.scope, { event_kind: 'completed', plan_id: plan,
      usage: { provenance: 'client_observed_sdk', complete: true, input_tokens: 10, output_tokens: 2, cache_read_tokens: null, cache_write_tokens: null, reasoning_tokens: null } })]) {
      const response = await call('receipts', { v11, body });
      assert.equal(response.status, 200);
      assert.deepEqual(response.json, { schema_version: 1, status: 'recorded', basis: 'client_observed', verified_saved_usd: 0 });
    }
  });

  // ---- server_error_codes and legacy_conditions reachable from outside

  await test(`[${view}] invalid_request: malformed JSON, missing field, wrong content type`, async () => {
    expectError(await call('optimize', { v11, body: '{"schema_version":1,' }), 'invalid_request', v11);
    const missing = optimizeRequest(doc, { scope: scope('missing') });
    delete missing.sequence;
    expectError(await call('optimize', { v11, body: missing }), 'invalid_request', v11);
    expectError(await call('optimize', { v11, body: optimizeRequest(doc, { scope: scope('type') }), contentType: 'text/plain' }), 'invalid_request', v11);
  });

  await test(`[${view}] unknown request fields are ${v11 ? 'tolerated with the features header' : 'rejected without the features header'}`, async () => {
    const request = optimizeRequest(doc, { scope: scope(`tolerant-${view}`) });
    request.future_field = { nested: true };
    request.segments[0].future_segment_field = 1;
    const response = await call('optimize', { v11, body: request });
    if (v11) assert.equal(response.status, 200, `tolerant reader rejected unknown fields: ${JSON.stringify(response.json)}`);
    else expectError(response, 'invalid_request', v11);
  });

  await test(`[${view}] unsupported_version: schema_version 2`, async () => {
    const request = optimizeRequest(doc, { scope: scope('version') });
    request.schema_version = 2;
    expectError(await call('optimize', { v11, body: request }), 'unsupported_version', v11);
  });

  await test(`[${view}] unknown_capability: unadvertised transform${v11 ? '' : ' and stale revision'}`, async () => {
    const request = optimizeRequest(doc, { scope: scope('capability') });
    request.policy.transforms = ['caveman.conformance.not-a-transform.v1'];
    expectError(await call('optimize', { v11, body: request }), 'unknown_capability', v11);
    const stale = optimizeRequest(doc, { scope: scope(`stale-${view}`) });
    stale.policy.revision = 'middleware-v1:cached-before-an-upgrade';
    const response = await call('optimize', { v11, body: stale });
    if (!v11) return expectError(response, 'unknown_capability', v11);
    assert.equal(response.status, 200, 'revision_tolerant request rejected for a stale revision');
    assert.equal(response.json.policy_revision, doc.policy_revision);
  });

  await test(`[${view}] not_found: unknown handle, unknown route, wrong method`, async () => {
    expectError(await call('retrieve', { v11, body: retrieveRequest(scope('unknown'), unknownHandle()) }), 'not_found', v11);
    expectError(await call('conformance-no-such-route', { v11, body: {} }), 'not_found', v11);
    expectError(await call('optimize', { v11, method: 'GET' }), 'not_found', v11);
  });

  await test(`[${view}] invalid_range / legacy_conditions retrieve_limit_out_of_range`, async () => {
    const response = await call('retrieve', { v11, body: retrieveRequest(scope('range'), unknownHandle(), { limit: 1 }) });
    expectCondition(response, 'retrieve_limit_out_of_range', v11);
    if (v11) expectError(response, 'invalid_range', v11);
  });

  await test(`[${view}] payload_limit: optimize body over limits.request_bytes`, async () => {
    const body = JSON.stringify({ schema_version: 1, pad: 'x'.repeat(doc.limits.request_bytes) });
    expectError(await call('optimize', { v11, body }), 'payload_limit', v11);
  });

  if (v11) {
    await test('[1.1] payload_limit: receipt over limits.receipt_bytes', async () => {
      const limit = doc.limits.receipt_bytes ?? fixture.constants.defaults.receipt_bytes;
      const body = receipt(scope('receipt-limit'));
      body.pad = 'x'.repeat(limit);
      expectError(await call('receipts', { v11, body }), 'payload_limit', v11);
    });
  }

  await test(`[${view}] forbidden_origin: a browser Origin is refused before authentication`, async () => {
    expectError(await call('capabilities', { v11, auth: '', headers: { origin: 'https://example.com' } }), 'forbidden_origin', v11);
    expectError(await call('optimize', { v11, headers: { origin: 'https://example.com' }, body: optimizeRequest(doc, { scope: scope('origin') }) }), 'forbidden_origin', v11);
  });

  await test(`[${view}] unauthorized: missing or unknown credential, capabilities included`, async () => {
    if (!token) return 'no --token: runtime assumed open';
    expectError(await call('capabilities', { v11, auth: '' }), 'unauthorized', v11);
    expectError(await call('optimize', { v11, auth: `not-${token}`, body: optimizeRequest(doc, { scope: scope('auth') }) }), 'unauthorized', v11);
  });

  await test(`[${view}] legacy_conditions malformed_recovery_binding`, async () => {
    const request = optimizeRequest(doc, { scope: scope('binding') });
    request.recovery_binding.kind = 'not_a_binding_kind';
    const response = await call('optimize', { v11, body: request });
    expectCondition(response, 'malformed_recovery_binding', v11);
    expectError(response, v11 ? 'invalid_request' : 'recovery_unavailable', v11);
  });

  await test(`[${view}] legacy_conditions optimize_not_smaller (recovery overhead above the saving)`, async () => {
    if (needsCompress()) return needsCompress();
    const overhead = Array.from({ length: 4000 }, (_, i) => `w${i}`).join(' ').slice(0, 32768);
    const request = optimizeRequest(doc, { scope: scope(`small-${view}`), content: noisy(12),
      binding: { id: id('binding'), kind: 'host_tool', tool_name: 'caveman_retrieve', overhead_text: overhead } });
    expectCondition(await call('optimize', { v11, body: request }), 'optimize_not_smaller', v11);
  });

  await test(`[${view}] epoch_changed: a manifest that is not an extension, and a sequence that went backwards`, async () => {
    if (needsCompress()) return needsCompress();
    const s = scope(`epoch-${view}`), first = optimizeRequest(doc, { scope: s, sequence: 5 });
    assert.equal((await call('optimize', { v11, body: first })).status, 200);
    const rewritten = optimizeRequest(doc, { scope: s, sequence: 6, manifest: [{ id: 'msg-0', sha256: sha256('a different history') }] });
    expectError(await call('optimize', { v11, body: rewritten }), 'epoch_changed', v11);
    const backwards = optimizeRequest(doc, { scope: s, sequence: 4, manifest: [...first.context_manifest, { id: 'msg-2', sha256: sha256('next') }] });
    expectError(await call('optimize', { v11, body: backwards }), 'epoch_changed', v11);
  });

  await test(`[${view}] legacy_conditions slow_request_body${v11 ? ' (request_timeout)' : ''}`, async () => {
    // 1.0 computed its 413 but then drained the stalled body with no deadline, so the answer never arrived.
    if (legacyOnly) return 'a protocol 1.0 runtime withholds its answer until the client closes the stalled body';
    const response = await stalledBody('optimize', v11);
    expectCondition(response, 'slow_request_body', v11);
    if (v11) expectError(response, 'request_timeout', v11);
  });

  if (foreignToken) {
    await test(`[${view}] forbidden_namespace: another principal cannot use this namespace`, async () => {
      if (needsLive()) return needsLive();
      assert.equal((await call('capabilities', { v11, auth: foreignToken })).status, 200, 'foreign token does not authenticate');
      expectError(await call('optimize', { v11, auth: foreignToken, body: optimizeRequest(doc, { scope: live.scope }) }), 'forbidden_namespace', v11);
      expectError(await call('retrieve', { v11, auth: foreignToken, body: retrieveRequest(live.scope, live.handle) }), 'forbidden_namespace', v11);
      expectError(await call('receipts', { v11, auth: foreignToken, body: receipt(live.scope) }), 'forbidden_namespace', v11);
      expectError(await call('sessions/delete', { v11, auth: foreignToken, body: { schema_version: 1, scope: live.scope } }), 'forbidden_namespace', v11);
      const still = await call('retrieve', { v11, body: retrieveRequest(live.scope, live.handle) });
      assert.equal(still.status, 200, 'a refused foreign delete removed the owner\'s original');
      assert.equal(sha256(still.json.text), sha256(live.original));
    });
  }

  await test(`[${view}] sessions/delete revokes the scope${lifecycle ? ', deletes its originals' : ''}, and later use is 410 deleted`, async () => {
    if (needsLive()) return needsLive();
    const response = await call('sessions/delete', { v11, body: { schema_version: 1, scope: live.scope } });
    assert.equal(response.status, 200);
    assert.equal(response.json.status, 'revoked');
    if (lifecycle) {
      assert.equal(response.json.originals_deleted, true);
      for (const key of ['scopes', 'originals', 'grants', 'choices']) assert.ok(response.json.deleted[key] >= 1, `deleted.${key} = ${response.json.deleted[key]}`);
    }
    expectError(await call('retrieve', { v11, body: retrieveRequest(live.scope, live.handle) }), 'deleted', v11);
    expectError(await call('optimize', { v11, body: optimizeRequest(doc, { scope: live.scope, sequence: 2 }) }), 'deleted', v11);
    const unknown = await call('sessions/delete', { v11, body: { schema_version: 1, scope: scope(`never-used-${view}`) } });
    assert.equal(unknown.status, 200, 'delete of an unknown scope is not idempotent');
    if (lifecycle) assert.deepEqual(unknown.json.deleted, { scopes: 0, choices: 0, grants: 0, originals: 0 });
  });
}

// Quota last: it spends this principal's requests for the rest of the minute.
await test('quota_exceeded: a burst past quota_requests_per_minute is 429 with Retry-After (1.0: 503 capacity)', async () => {
  const quota = caps.v11?.limits.quota_requests_per_minute;
  if (!quota) return legacyOnly ? 'legacy view advertises no quota' : 'runtime advertises no quota_requests_per_minute';
  if (quota > 2000) return `quota ${quota}/min is too large to burst`;
  let response;
  for (let sent = 0; sent <= quota * 2 + 10; sent += 10) {
    const burst = await Promise.all(Array.from({ length: 10 }, () => call('retrieve', { v11: true, body: retrieveRequest(scope('quota'), unknownHandle()) })));
    response = burst.find(item => item.status !== 404);
    if (response) break;
  }
  assert.ok(response, 'no quota response within twice the advertised quota');
  expectError(response, 'quota_exceeded', true);
  const legacy = await call('retrieve', { body: retrieveRequest(scope('quota'), unknownHandle()) });
  assert.equal(legacy.status, 503, `legacy client over quota: ${legacy.status}`);
  assert.equal(legacy.json.error.code, 'capacity');
});

// Coverage: every catalog entry either ran above or says why a black box cannot reach it.
const queue = 'queue saturation cannot be forced from outside: a stalled body frees its slot at the deadline a waiter would hit';
const fault = 'needs a storage fault inside the runtime';
const unreachable = { capacity: queue, queue_wait_exceeded: queue, deadline: 'server work past deadline_ms depends on host speed',
  expired: 'needs retention_seconds to elapse', identity_conflict: 'needs a concurrent-writer race', runtime_unavailable: fault, storage_error: fault,
  optimize_cache_state_unavailable: fault, optimize_recovery_unavailable: fault, optimize_store_capacity: 'needs the store capacity filled',
  forbidden_namespace: 'pass --foreign-token (a principal without this namespace)', quota_exceeded: 'runtime advertises no quota_requests_per_minute',
  ...legacyOnly && { request_timeout: 'protocol 1.1 status: the 1.0 view answers slow_request_body as 413 payload_limit',
    slow_request_body: 'a protocol 1.0 runtime withholds its answer until the client closes the stalled body' } };
for (const { code } of fixture.server_error_codes) {
  if (!exercised.has(code)) await test(`server_error_codes: ${code}`, async () => unreachable[code] ?? assert.fail(`no check reaches ${code}`));
}
for (const { condition } of fixture.legacy_conditions) {
  if (!exercised.has(`condition:${condition}`)) await test(`legacy_conditions: ${condition}`, async () => unreachable[condition] ?? assert.fail(`no check reaches ${condition}`));
}

console.log(`1..${count}`);
console.log(`# ${responses} responses validated against the contract schemas`);
console.log(`# pass ${count - failed - skipped}, fail ${failed}, skip ${skipped}`);
process.exit(failed ? 1 : 0);
