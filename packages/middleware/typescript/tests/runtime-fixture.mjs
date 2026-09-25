import { createMiddlewareRuntime, sha256 } from '@caveman-ai/sdk/middleware';
// A static JSON import, not a top-level await, so a CommonJS bundle of these tests can carry it.
import fixture from '../../../sdk/parity/middleware.fixtures.json' with { type: 'json' };

// Protocol fixture, not a compression algorithm or provider savings benchmark.
export const original = fixture.request.segments[0].content;
export const shortened = fixture.plan.replacements[0].text;
export const handle = fixture.plan.replacements[0].recovery_handle;
export const scope = { namespace: 'native-test', session_id: 'one', branch_id: 'main', cache_epoch: '0' };

// The deadline is far past any in-process answer: these tests assert behavior, not latency, and a loaded CI host
// (load average 54 observed) must not turn the first capabilities fetch into a `deadline` bypass. `retrieveError`
// ([status, code]) makes every recovery fail the way the runtime answers an unknown handle or an outage.
export function runtimeFixture({ retrieveError, ...options } = {}) {
  const requests = [], receipts = [], reports = [], retrievals = [];
  let source;
  const runtime = createMiddlewareRuntime({ deadlineMs: 60_000, ...options, onReport: report => reports.push(report), fetch: async (url, options) => {
    if (url.endsWith('/capabilities')) return Response.json(fixture.capabilities);
    const request = JSON.parse(options.body);
    if (url.endsWith('/receipts')) { receipts.push(request); return Response.json({ ok: true }); }
    if (url.endsWith('/retrieve')) {
      retrievals.push(request);
      if (retrieveError) return Response.json({ schema_version: 1, error: { code: retrieveError[1] } }, { status: retrieveError[0] });
      if (!source) throw new Error('Recovery before storing a source');
      return Response.json({ schema_version: 1, handle, source_id: source.source_id, text: source.content,
        original_sha256: source.sha256, total_bytes: Buffer.byteLength(source.content), complete: true,
        kind: 'original_page', offset: 0, next_offset: null });
    }
    if (!url.endsWith('/optimize')) throw new Error(`Unexpected runtime endpoint: ${url}`);
    requests.push(request);
    const plan = structuredClone(fixture.plan);
    plan.request_id = request.request_id;
    plan.input_digest = await sha256(options.body);
    plan.recovery.binding_id = request.recovery_binding?.id ?? '';
    // Only the fixture original has a fixture replacement; every other segment is skipped.
    const segments = request.segments, match = segments.find(segment => segment.content === original);
    if (request.recovery_binding && match) {
      source = match;
      Object.assign(plan.replacements[0], { segment_id: source.id, source_id: source.source_id, original_sha256: source.sha256 });
      plan.skipped = segments.filter(segment => segment !== match).map(segment => ({ segment_id: segment.id, reason: 'not_smaller' }));
    } else {
      plan.status = 'bypassed'; plan.reason = 'recovery_unavailable'; plan.replacements = [];
      plan.skipped = segments.map(segment => ({ segment_id: segment.id, reason: 'recovery_unavailable' }));
      plan.measurement.tokens_after = plan.measurement.tokens_before; plan.measurement.unique_tokens_reduced = 0;
    }
    return Response.json(plan);
  } });
  return { runtime, requests, receipts, reports, retrievals };
}

export function history() {
  return [
    { role: 'user', content: 'Summarize this log and recover the original when needed.' },
    { role: 'assistant', content: [{ type: 'tool-call', toolCallId: 'read-1', toolName: 'read', input: {} }] },
    { role: 'tool', content: [{ type: 'tool-result', toolCallId: 'read-1', toolName: 'read', output: { type: 'text', value: original } }] },
  ];
}

export const usage = { inputTokens: { total: 25 }, outputTokens: { total: 2 } };
export const finish = { unified: 'stop', raw: 'stop' };
export const tick = () => new Promise(resolve => setImmediate(resolve));
