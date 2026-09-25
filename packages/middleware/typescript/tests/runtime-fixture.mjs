import { readFile } from 'node:fs/promises';
import { createMiddlewareRuntime, sha256 } from '@caveman-ai/sdk/middleware';

// Protocol fixture, not a compression algorithm or provider savings benchmark.
const fixture = JSON.parse(await readFile(new URL('../../../sdk/parity/middleware.fixtures.json', import.meta.url), 'utf8'));
export const original = fixture.request.segments[0].content;
export const shortened = fixture.plan.replacements[0].text;
export const handle = fixture.plan.replacements[0].recovery_handle;
export const scope = { namespace: 'native-test', session_id: 'one', branch_id: 'main', cache_epoch: '0' };

export function runtimeFixture() {
  const requests = [], receipts = [], reports = [], retrievals = [];
  let source;
  const runtime = createMiddlewareRuntime({ deadlineMs: 2000, onReport: report => reports.push(report), fetch: async (url, options) => {
    if (url.endsWith('/capabilities')) return Response.json(fixture.capabilities);
    const request = JSON.parse(options.body);
    if (url.endsWith('/receipts')) { receipts.push(request); return Response.json({ ok: true }); }
    if (url.endsWith('/retrieve')) {
      retrievals.push(request);
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
    const segments = request.segments;
    if (request.recovery_binding && segments.length === 1) {
      source = segments[0];
      Object.assign(plan.replacements[0], { segment_id: source.id, source_id: source.source_id, original_sha256: source.sha256 });
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
