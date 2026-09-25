import assert from 'node:assert/strict';
import test from 'node:test';
import { requirePeers } from './peers.mjs';
import { runtimeFixture, history, original, shortened, handle, scope, usage, finish, tick } from './runtime-fixture.mjs';

async function native(t) {
  if (!requirePeers(t, 'ai-sdk')) return null;
  return { ...await import('ai'), ...await import('ai/test'), ...await import('../dist/ai-sdk.js') };
}

test('native generateText compresses an outbound copy and executes recovery in its own tool loop', async t => {
  const api = await native(t); if (!api) return;
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const messages = history(), before = structuredClone(messages), seen = [];
  let starts = 0, ends = 0;
  const model = new api.MockLanguageModelV4({ doGenerate: async params => {
    seen.push(structuredClone(params.prompt));
    return seen.length === 1
      ? { content: [{ type: 'tool-call', toolCallId: 'retrieve-1', toolName: 'caveman_retrieve', input: JSON.stringify({ handle }) }],
        finishReason: { unified: 'tool-calls', raw: 'tool_calls' }, usage, warnings: [] }
      : { content: [{ type: 'text', text: 'Recovered exact original.' }], finishReason: finish, usage, warnings: [] };
  } });
  const bundle = api.withCaveman({ model, onStepStart: () => { starts++; }, onStepEnd: () => { ends++; } }, { runtime: f.runtime, scope });
  const result = await api.generateText({ ...bundle, messages, stopWhen: api.stepCountIs(2), maxRetries: 0 });
  assert.equal(result.text, 'Recovered exact original.');
  assert.equal(seen[0].at(-1).content[0].output.value, shortened);
  assert.equal(f.requests[0].segments[0].content, original);
  assert.ok(f.requests[0].recovery_binding, 'real native registry was attested');
  assert.equal(f.retrievals.length, 1);
  const recoveryResult = result.steps[0].toolResults.find(part => part.toolName === 'caveman_retrieve');
  assert.equal(recoveryResult.output.text, original);
  assert.deepEqual(messages, before, 'caller history retains originals');
  assert.equal(starts, 2); assert.equal(ends, 2);
  assert.ok(f.reports.some(report => report.status === 'applied'));
  await tick();
  assert.equal(f.receipts.filter(receipt => receipt.event_kind === 'completed').length, 2);
});

test('native streamText preserves chunks and applies compression without rewriting history', async t => {
  const api = await native(t); if (!api) return;
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const messages = history(), before = structuredClone(messages);
  const model = new api.MockLanguageModelV4({ doStream: async () => ({ stream: api.simulateReadableStream({
    initialDelayInMs: null, chunkDelayInMs: null, chunks: [
      { type: 'stream-start', warnings: [] }, { type: 'text-start', id: 'text-1' },
      { type: 'text-delta', id: 'text-1', delta: 'hello ' }, { type: 'text-delta', id: 'text-1', delta: 'world' },
      { type: 'text-end', id: 'text-1' }, { type: 'finish', finishReason: finish, usage },
    ],
  }) }) });
  const bundle = api.withCaveman({ model }, { runtime: f.runtime, scope });
  const result = api.streamText({ ...bundle, messages, maxRetries: 0 });
  const chunks = [];
  for await (const chunk of result.textStream) chunks.push(chunk);
  assert.deepEqual(chunks, ['hello ', 'world']);
  assert.equal(await result.text, 'hello world');
  assert.equal(model.doStreamCalls[0].prompt.at(-1).content[0].output.value, shortened);
  assert.deepEqual(messages, before);
  await tick();
  assert.equal(f.receipts.filter(receipt => receipt.event_kind === 'completed').length, 1);
});

test('native generateText pre-abort does not reach the provider or optimizer', async t => {
  const api = await native(t); if (!api) return;
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const model = new api.MockLanguageModelV4();
  const bundle = api.withCaveman({ model }, { runtime: f.runtime, scope });
  const controller = new AbortController(); controller.abort();
  await assert.rejects(api.generateText({ ...bundle, messages: history(), abortSignal: controller.signal, maxRetries: 0 }), { name: 'AbortError' });
  assert.equal(model.doGenerateCalls.length, 0); assert.equal(f.requests.length, 0);
});

test('native model stream cancellation reaches source and emits one cancelled receipt', async t => {
  const api = await native(t); if (!api) return;
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  let cancelled;
  const model = new api.MockLanguageModelV4({ doStream: async () => ({ stream: new ReadableStream({
    pull(controller) { controller.enqueue({ type: 'text-delta', id: 't', delta: 'part' }); },
    cancel(reason) { cancelled = reason; },
  }, { highWaterMark: 0 }) }) });
  const bundle = api.withCaveman({ model }, { runtime: f.runtime, scope });
  const result = await bundle.model.doStream({ prompt: [{ role: 'user', content: [{ type: 'text', text: 'hello' }] }] });
  const reader = result.stream.getReader();
  assert.equal((await reader.read()).value.delta, 'part');
  await reader.cancel('caller-cancel'); await tick();
  assert.equal(cancelled, 'caller-cancel');
  assert.equal(f.receipts.filter(receipt => receipt.event_kind === 'cancelled').length, 1);
  assert.equal(f.receipts.filter(receipt => receipt.event_kind === 'completed').length, 0);
});

test('native streamText propagates abort and records cancellation without completion', async t => {
  const api = await native(t); if (!api) return;
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const controller = new AbortController();
  let providerAborted = false;
  const model = new api.MockLanguageModelV4({ doStream: async params => ({ stream: new ReadableStream({
    start(stream) {
      stream.enqueue({ type: 'stream-start', warnings: [] });
      stream.enqueue({ type: 'text-start', id: 't' });
      stream.enqueue({ type: 'text-delta', id: 't', delta: 'part' });
      params.abortSignal.addEventListener('abort', () => {
        providerAborted = true;
        stream.error(params.abortSignal.reason);
      }, { once: true });
    },
  }) }) });
  const bundle = api.withCaveman({ model }, { runtime: f.runtime, scope });
  const result = api.streamText({ ...bundle, messages: history(), abortSignal: controller.signal, maxRetries: 0, onError() {} });
  const events = [];
  for await (const event of result.fullStream) {
    events.push(event.type);
    if (event.type === 'text-delta') controller.abort();
  }
  await tick();
  assert.equal(providerAborted, true);
  assert.ok(events.includes('abort'));
  assert.equal(f.receipts.filter(receipt => receipt.event_kind === 'cancelled').length, 1);
  assert.equal(f.receipts.filter(receipt => receipt.event_kind === 'completed').length, 0);
});

test('removing native recovery callbacks prevents lossy compression', async t => {
  const api = await native(t); if (!api) return;
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const model = new api.MockLanguageModelV4({ doGenerate: { content: [{ type: 'text', text: 'unchanged' }], finishReason: finish, usage, warnings: [] } });
  const bundle = api.withCaveman({ model }, { runtime: f.runtime, scope });
  await api.generateText({ model: bundle.model, tools: bundle.tools, messages: history(), maxRetries: 0 });
  assert.equal(model.doGenerateCalls[0].prompt.at(-1).content[0].output.value, original);
  assert.equal(f.requests[0].recovery_binding, null);
  assert.equal(f.reports.some(report => report.status === 'applied'), false);
});
