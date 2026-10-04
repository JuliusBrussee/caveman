import assert from 'node:assert/strict';
import test from 'node:test';
import { requirePeers } from './peers.mjs';
import { runtimeFixture, original, shortened, handle, scope } from './runtime-fixture.mjs';

test('native OpenAI client projects tool results and application executor recovers exact source', async t => {
  if (!requirePeers(t, 'openai')) return;
  const { default: OpenAI } = await import('openai');
  const { withCavemanOpenAITools } = await import('../dist/openai.js');
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const seen = [];
  const providerFetch = async (_url, options) => {
    seen.push(JSON.parse(options.body));
    return Response.json({ id: 'chatcmpl-fixture', object: 'chat.completion', created: 1, model: 'fixture-model',
      choices: [{ index: 0, message: { role: 'assistant', content: 'done' }, finish_reason: 'stop' }],
      usage: { prompt_tokens: 25, completion_tokens: 1, total_tokens: 26 } });
  };
  const client = new OpenAI({ apiKey: 'deterministic-test-key', fetch: providerFetch, maxRetries: 0 });
  const bundle = withCavemanOpenAITools(client, { runtime: f.runtime, scope, fetch: providerFetch, protocol: 'openai-chat',
    tools: [{ type: 'function', function: { name: 'read', description: 'Read source', parameters: { type: 'object', properties: {} } } }],
    functions: { read: () => original },
  });
  const messages = [
    { role: 'user', content: 'Summarize log' },
    { role: 'assistant', content: null, tool_calls: [{ id: 'read-1', type: 'function', function: { name: 'read', arguments: '{}' } }] },
    { role: 'tool', tool_call_id: 'read-1', content: original },
  ];
  const before = structuredClone(messages);
  const result = await bundle.client.chat.completions.create({ model: 'fixture-model', messages, tools: bundle.tools });
  assert.equal(result.choices[0].message.content, 'done');
  assert.equal(seen[0].messages.at(-1).content, shortened);
  assert.deepEqual(messages, before);
  assert.equal((await bundle.functions.caveman_retrieve({ handle })).text, original);
  assert.equal(f.retrievals.length, 1);
  assert.ok(f.reports.some(report => report.status === 'applied'));
});

test('native OpenAI stream preserves SSE chunks and runtime outage preserves source', async t => {
  if (!requirePeers(t, 'openai')) return;
  const { default: OpenAI } = await import('openai');
  const { withCavemanOpenAI } = await import('../dist/openai.js');
  const { createMiddlewareRuntime } = await import('@caveman-ai/sdk/middleware');
  const runtime = createMiddlewareRuntime({ fetch: async () => { throw new TypeError('fetch failed'); } });
  t.after(() => runtime.close());
  let seen;
  const chunk = { id: 'fixture-stream', object: 'chat.completion.chunk', created: 1, model: 'fixture-model',
    choices: [{ index: 0, delta: { content: 'part' }, finish_reason: null }] };
  const providerFetch = async (_url, options) => {
    seen = JSON.parse(options.body);
    return new Response(`data: ${JSON.stringify(chunk)}\n\ndata: [DONE]\n\n`, { headers: { 'content-type': 'text/event-stream' } });
  };
  const client = withCavemanOpenAI(new OpenAI({ apiKey: 'deterministic-test-key', fetch: providerFetch, maxRetries: 0 }), { runtime, scope, fetch: providerFetch });
  const messages = [
    { role: 'assistant', content: null, tool_calls: [{ id: 'read-1', type: 'function', function: { name: 'read', arguments: '{}' } }] },
    { role: 'tool', tool_call_id: 'read-1', content: original },
  ];
  const stream = await client.chat.completions.create({ model: 'fixture-model', messages, stream: true });
  const chunks = []; for await (const part of stream) chunks.push(part);
  assert.deepEqual(chunks, [chunk]);
  assert.equal(seen.messages.at(-1).content, original);
});
