#!/usr/bin/env node
// Middleware perf gate. Two measurements, compared with perf-thresholds.json (how the limits were derived is in that
// file); any value over its limit fails the run.
//   - BenchmarkMiddlewarePreparation: engine and prepare ns/op on a 100 KiB log, median of 5 runs of 20 iterations.
//   - HTTP probe against a local HEAD runtime, 200 rounds after 10 warmups. Each round sends, back to back, two
//     calibration requests, a capabilities GET (HTTP and auth, served from memory) and a receipt (the same path plus a
//     queue slot and one SQLite commit), then a small (~1.6 KiB) optimize and, every other round, a large (~100 KiB)
//     one, each in a fresh scope. Optimize p50 is gated as a ratio to a calibration request of the same run, so the
//     gate tracks the runtime's own work rather than the host's speed: the small optimize (commit-bound) against the
//     receipt, the large optimize (CPU-bound) against capabilities. p99 and the receipt p50 get wide absolute ceilings.
//
//   node tests/middleware-e2e/perf.mjs [--measure]      (--measure prints the values and skips the gate)
import { randomBytes } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { TOKEN, buildProxy, finish, sh, sha256, startRuntime, step, tempDir } from './harness.mjs';

const measureOnly = process.argv.includes('--measure');
const { limits, ratio_limits: ratioLimits } = JSON.parse(await readFile(new URL('./perf-thresholds.json', import.meta.url), 'utf8'));
const median = values => [...values].sort((x, y) => x - y)[Math.floor(values.length / 2)];
const percentile = (sorted, p) => sorted[Math.ceil((p / 100) * sorted.length) - 1];
const round = value => Math.round(value * 100) / 100;

async function goBenchmark() {
  const { stdout } = await sh('go', ['test', '-run', '^$', '-bench', '^BenchmarkMiddlewarePreparation$', '-benchtime', '20x', '-count', '5', './proxy/internal/middleware']);
  const samples = {};
  for (const [, name, ns] of stdout.matchAll(/^BenchmarkMiddlewarePreparation\/(\w+)(?:-\d+)?\s+\d+\s+([\d.]+) ns\/op/gm)) (samples[name] ??= []).push(Number(ns));
  return Object.fromEntries(Object.entries(samples).map(([name, values]) => [`go_${name}_ns_per_op`, median(values)]));
}

function log(lines, tag) {
  let text = `[INFO] probe ${tag}\r\n`;
  for (let i = 0; i < lines; i++) text += `[INFO] reading row ${i}: café 🌍 exact-value-${String(i).padStart(4, '0')} with verbose repeated details\r\n`;
  return text;
}

async function httpProbe(base) {
  const url = `${base}/caveman/v1/middleware/`;
  const headers = { authorization: `Bearer ${TOKEN}`, 'content-type': 'application/json', 'caveman-middleware-features': 'http_status_v2, revision_tolerant' };
  const caps = await (await fetch(`${url}capabilities`, { headers })).json();
  const scope = key => ({ namespace: 'perf', session_id: key, branch_id: 'main', cache_epoch: '0' });
  const optimize = (lines, tag) => () => {
    const content = log(lines, tag), key = randomBytes(8).toString('hex');
    return ['optimize', JSON.stringify({ schema_version: 1, request_id: key, logical_call_id: key, attempt_id: key, idempotency_key: key,
      scope: scope(key), sequence: 1,
      adapter: { id: 'perf-probe', version: '1', framework_version: '1', serialization_revision: 'perf-v1' }, model: null, mode: 'compress',
      policy: { revision: caps.policy_revision, transforms: caps.transforms.map(t => t.transform_id) },
      segments: [{ id: 'tool-1', kind: 'tool_result', cache_region: 'live_zone', content, sha256: sha256(content), source_id: 'doc-1', protected: false, opaque: false }],
      context_manifest: [{ id: 'msg-1', sha256: sha256(content) }],
      recovery_binding: { id: 'binding-1', kind: 'host_tool', tool_name: 'caveman_retrieve', overhead_text: 'A registered native tool.' } })];
  };
  const probes = {
    capabilities: () => ['capabilities'],
    receipt: () => {
      const key = randomBytes(8).toString('hex');
      return ['receipts', JSON.stringify({ schema_version: 1, scope: scope(key), logical_call_id: key, attempt_id: key, event_kind: 'dispatch_intent',
        plan_id: null, usage: null, provider_request_sha256: null })];
    },
  };
  const times = { capabilities: [], receipt: [], optimize_small: [], optimize_large: [] };
  for (let i = -10; i < 200; i++) {
    const round = { ...probes, optimize_small: optimize(20, `small-${i}`), ...(i % 2 === 0 && { optimize_large: optimize(1250, `large-${i}`) }) };
    for (const [name, make] of Object.entries(round)) {
      const [route, body] = make();
      const start = performance.now();
      const response = await fetch(`${url}${route}`, body === undefined ? { headers } : { method: 'POST', headers, body });
      const json = await response.json();
      const elapsed = performance.now() - start;
      const ok = response.status === 200 && (route !== 'optimize' || json.status === 'optimized');
      if (!ok) throw new Error(`${name}: ${response.status} ${json.status ?? json.error?.code} ${json.reason ?? ''}`);
      if (i >= 0) times[name].push(elapsed);
    }
  }
  const raw = {};
  for (const [name, values] of Object.entries(times)) {
    values.sort((x, y) => x - y);
    for (const p of [50, 99]) raw[`http_${name}_p${p}_ms`] = percentile(values, p);
  }
  raw.http_optimize_small_p50_x_receipt = raw.http_optimize_small_p50_ms / raw.http_receipt_p50_ms;
  raw.http_optimize_large_p50_x_capabilities = raw.http_optimize_large_p50_ms / raw.http_capabilities_p50_ms;
  return Object.fromEntries(Object.entries(raw).map(([key, value]) => [key, round(value)]));
}

const measured = {};
const work = await tempDir('perf');
await step('BenchmarkMiddlewarePreparation', async () => {
  const bench = await goBenchmark();
  Object.assign(measured, bench);
  return JSON.stringify(bench);
});
let runtime;
await step('HTTP optimize latency probe against a HEAD runtime', async () => {
  runtime = await startRuntime(await buildProxy(work), { CAVEMAN_MIDDLEWARE_MODE: 'compress' });
  try {
    const probe = await httpProbe(runtime.base);
    Object.assign(measured, probe);
    return JSON.stringify(probe);
  } finally {
    await runtime.stop();
  }
});
console.log(`# measured ${JSON.stringify(measured)}`);
if (!measureOnly) {
  for (const [key, limit] of Object.entries({ ...limits, ...ratioLimits })) {
    await step(`${key} <= ${limit}`, async () => {
      if (!(key in measured)) throw new Error(`${key} was not measured`);
      if (measured[key] > limit) throw new Error(`${key} = ${measured[key]} breaches the limit ${limit}`);
      return `measured ${measured[key]}`;
    });
  }
}
await finish();
