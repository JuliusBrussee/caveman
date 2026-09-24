#!/usr/bin/env node
// Middleware perf gate. Two measurements, compared with perf-thresholds.json (how the limits were derived is in that
// file); any value over its limit fails the run.
//   - BenchmarkMiddlewarePreparation: engine and prepare ns/op on a 100 KiB log, median of 5 runs of 20 iterations.
//   - HTTP probe: optimize round-trip p50/p99 against a local HEAD runtime, 200 small (~1.6 KiB) and 100 large
//     (~100 KiB) tool results after 10 warmups each, one fresh scope per call.
//
//   node tests/middleware-e2e/perf.mjs [--measure]      (--measure prints the values and skips the gate)
import { randomBytes } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { TOKEN, buildProxy, finish, sh, sha256, startRuntime, step, tempDir } from './harness.mjs';

const measureOnly = process.argv.includes('--measure');
const { limits } = JSON.parse(await readFile(new URL('./perf-thresholds.json', import.meta.url), 'utf8'));
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
  const result = {};
  for (const [size, lines, samples] of [['small', 20, 200], ['large', 1250, 100]]) {
    const times = [];
    for (let i = -10; i < samples; i++) {
      const content = log(lines, `${size}-${i}`), key = randomBytes(8).toString('hex');
      const body = JSON.stringify({ schema_version: 1, request_id: key, logical_call_id: key, attempt_id: key, idempotency_key: key,
        scope: { namespace: 'perf', session_id: key, branch_id: 'main', cache_epoch: '0' }, sequence: 1,
        adapter: { id: 'perf-probe', version: '1', framework_version: '1', serialization_revision: 'perf-v1' }, model: null, mode: 'compress',
        policy: { revision: caps.policy_revision, transforms: caps.transforms.map(t => t.transform_id) },
        segments: [{ id: 'tool-1', kind: 'tool_result', cache_region: 'live_zone', content, sha256: sha256(content), source_id: 'doc-1', protected: false, opaque: false }],
        context_manifest: [{ id: 'msg-1', sha256: sha256(content) }],
        recovery_binding: { id: 'binding-1', kind: 'host_tool', tool_name: 'caveman_retrieve', overhead_text: 'A registered native tool.' } });
      const start = performance.now();
      const response = await fetch(`${url}optimize`, { method: 'POST', headers, body });
      const plan = await response.json();
      const elapsed = performance.now() - start;
      if (response.status !== 200 || plan.status !== 'optimized') throw new Error(`${size} optimize: ${response.status} ${plan.status ?? plan.error?.code} ${plan.reason ?? ''}`);
      if (i >= 0) times.push(elapsed);
    }
    times.sort((x, y) => x - y);
    result[`http_optimize_${size}_p50_ms`] = round(percentile(times, 50));
    result[`http_optimize_${size}_p99_ms`] = round(percentile(times, 99));
  }
  return result;
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
  for (const [key, limit] of Object.entries(limits)) {
    await step(`${key} <= ${limit}`, async () => {
      if (!(key in measured)) throw new Error(`${key} was not measured`);
      if (measured[key] > limit) throw new Error(`${key} = ${measured[key]} breaches the limit ${limit}`);
      return `measured ${measured[key]}`;
    });
  }
}
await finish();
