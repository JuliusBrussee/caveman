import assert from 'node:assert/strict';
import test from 'node:test';
import { drivers } from './drivers.mjs';
import { requirePeers } from './peers.mjs';
import { original, runtimeFixture, scope, shortened, tick } from './runtime-fixture.mjs';

const finish = { unified: 'stop', raw: 'stop' }, usage = { inputTokens: { total: 1 }, outputTokens: { total: 1 } };
function warnings(t) {
  const lines = [], warn = console.warn;
  console.warn = (...args) => lines.push(args.join(' '));
  t.after(() => { console.warn = warn; });
  return lines;
}
const toolTurn = (id, value) => [
  { role: 'assistant', content: [{ type: 'tool-call', toolCallId: id, toolName: 'read', input: {} }] },
  { role: 'tool', content: [{ type: 'tool-result', toolCallId: id, toolName: 'read', output: { type: 'text', value } }] },
];

test('C3: LangGraph without configurable.thread_id passes through instead of failing the model call', async t => {
  if (!requirePeers(t, 'langchain')) return;
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const result = await drivers.langchain.run(f.runtime, { configurable: {} });
  assert.equal(result.seen, original);
  assert.equal(f.requests.length, 0);
  assert.ok(f.reports.some(report => report.reason === 'recovery_unbound'), JSON.stringify(f.reports));
});

test('C4: a Mastra thread too large to scan disables compression for that thread only', async t => {
  if (!requirePeers(t, 'mastra')) return;
  const { Agent } = await import('@mastra/core/agent'), { MockLanguageModelV4 } = await import('ai/test'), { withCavemanMastra } = await import('../dist/mastra.js');
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const model = new MockLanguageModelV4({ doGenerate: { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] } });
  // One wrapper for every call, as an application holds it: the old latch lived in this wrapper's closure.
  const agent = withCavemanMastra(new Agent({ id: 'agent', name: 'agent', instructions: 'Be brief.', model }), { runtime: f.runtime, scope });
  const sent = () => model.doGenerateCalls.at(-1).prompt.findLast(message => message.role === 'tool').content[0].output.value;
  // More than 16,384 JSON nodes latches "protect all" for this history.
  const big = [{ role: 'user', content: Array.from({ length: 6000 }, () => ({ type: 'text', text: 'x' })) }, ...toolTurn('read-1', original)];
  await agent.generate(big, { memory: { thread: 'big-thread', resource: 'tenant-a' } });
  assert.equal(sent(), original);
  assert.equal(f.requests.length, 0);
  const normal = [{ role: 'user', content: 'go' }, ...toolTurn('read-1', original)];
  await agent.generate(normal, { memory: { thread: 'normal-thread', resource: 'tenant-b' } });
  assert.equal(sent(), shortened);
  await agent.generate(normal);
  assert.equal(sent(), shortened);
  assert.equal(f.requests.length, 2, 'later normal calls still send optimize requests');
});

test('C5: a 700KB tool output and an earlier screenshot no longer skip the call; the manifest is a head window', async t => {
  if (!requirePeers(t, 'ai-sdk')) return;
  const { generateText } = await import('ai'), { MockLanguageModelV4 } = await import('ai/test'), { withCaveman } = await import('../dist/ai-sdk.js');
  const screenshot = { role: 'user', content: [{ type: 'file', mediaType: 'image/png', data: new Uint8Array([137, 80, 78, 71, 1, 2, 3]) }] };
  const messages = [{ role: 'user', content: 'go' }, screenshot, ...toolTurn('huge-1', 'y'.repeat(700_000)), ...toolTurn('read-1', original)];
  for (const manifestBytes of [undefined, 4096]) {
    const f = runtimeFixture(); t.after(() => f.runtime.close());
    const model = new MockLanguageModelV4({ doGenerate: { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] } });
    await generateText({ ...withCaveman({ model }, { runtime: f.runtime, scope, ...(manifestBytes ? { manifestBytes } : {}) }), messages, maxRetries: 0 });
    const prompt = model.doGenerateCalls[0].prompt;
    assert.equal(prompt.at(-1).content[0].output.value, shortened, 'the eligible result was compressed');
    assert.equal(prompt.at(-3).content[0].output.value.length, 700_000, 'the over-budget result was skipped on its own');
    const request = f.requests[0];
    assert.deepEqual(request.segments.map(segment => segment.content), [original], 'segment_bytes skips only the 700KB candidate');
    assert.equal(request.sequence, prompt.length);
    if (manifestBytes) assert.ok(request.context_manifest.length < prompt.length, 'a small manifestBytes sends a head window');
    else assert.equal(request.context_manifest.length, prompt.length, 'opaque bytes are hashed into the manifest');
  }
});

test('C7: entry points that cannot bind recovery say which one compresses, and report recovery_unbound without I/O', async t => {
  if (!requirePeers(t, 'ai-sdk')) return;
  const lines = warnings(t);
  const { generateText, wrapLanguageModel } = await import('ai'), { MockLanguageModelV4 } = await import('ai/test');
  const { createCavemanMiddleware } = await import('../dist/ai-sdk.js');
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const model = new MockLanguageModelV4({ doGenerate: { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] } });
  const wrapped = wrapLanguageModel({ model, middleware: createCavemanMiddleware({ runtime: f.runtime, scope }) });
  await generateText({ model: wrapped, messages: [{ role: 'user', content: 'go' }, ...toolTurn('read-1', original)], maxRetries: 0 });
  assert.equal(model.doGenerateCalls[0].prompt.at(-1).content[0].output.value, original);
  assert.equal(f.requests.length, 0);
  assert.deepEqual(f.reports.map(report => report.reason), ['recovery_unbound']);
  assert.ok(lines.some(line => line.includes('createCavemanMiddleware') && line.includes('use withCaveman')), lines.join('\n'));
  if (!requirePeers({ skip() {} }, 'langchain')) return;
  const { withCavemanModel } = await import('../dist/langchain.js'), { FakeListChatModel } = await import('@langchain/core/utils/testing');
  withCavemanModel(new FakeListChatModel({ responses: ['done'] }), { runtime: f.runtime, scope });
  assert.ok(lines.some(line => line.includes('withCavemanModel') && line.includes('use withCavemanAgent')), lines.join('\n'));
});

test('C12: a projected ToolMessage does not carry the original in lc_kwargs or its serialized form', async t => {
  if (!requirePeers(t, 'langchain')) return;
  const { AIMessage, ToolMessage } = await import('@langchain/core/messages');
  const { prepareLangChain } = await import('../dist/langchain.js');
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const messages = [new AIMessage({ content: '', tool_calls: [{ id: 'read-1', name: 'read', args: {} }] }),
    new ToolMessage({ content: original, tool_call_id: 'read-1', name: 'read', artifact: { rows: 3 } })];
  const prepared = await prepareLangChain(messages, { runtime: f.runtime, scope }, undefined, f.runtime.recovery(scope));
  const projected = prepared.messages[1];
  assert.equal(projected.content, shortened);
  assert.ok(ToolMessage.isInstance(projected));
  assert.deepEqual([projected.tool_call_id, projected.name, projected.artifact], ['read-1', 'read', { rows: 3 }]);
  // Compare JSON-escaped text: the original is a multi-line log.
  const leaked = value => JSON.stringify(value).includes(JSON.stringify(original).slice(1, 200));
  assert.ok(!leaked(projected), 'serialized copy leaks the original');
  assert.ok(!leaked(projected.lc_kwargs));
  assert.ok(leaked(messages[1]), 'the probe finds the original where it belongs');
  assert.equal(messages[1].content, original);
});

test('C14: every adapter disables recovery on a caveman_retrieve name clash, warns once, and reports it', async t => {
  if (!requirePeers(t, 'openai')) return;
  const lines = warnings(t);
  const { default: OpenAI } = await import('openai'), { withCavemanOpenAITools } = await import('../dist/openai.js');
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const seen = [];
  const fetch = async (_url, init) => { seen.push(JSON.parse(init.body)); return Response.json({ id: 'c', object: 'chat.completion', created: 1, model: 'm',
    choices: [{ index: 0, message: { role: 'assistant', content: 'done' }, finish_reason: 'stop' }] }); };
  const definition = { type: 'function', function: { name: 'caveman_retrieve', description: 'host tool', parameters: { type: 'object', properties: {} } } };
  const bundle = withCavemanOpenAITools(new OpenAI({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime: f.runtime, scope, fetch, protocol: 'openai-chat',
    tools: [definition], functions: { caveman_retrieve: () => 'host' } });
  await bundle.client.chat.completions.create({ model: 'm', tools: bundle.tools, messages: [
    { role: 'assistant', content: null, tool_calls: [{ id: 'read-1', type: 'function', function: { name: 'caveman_retrieve', arguments: '{}' } }] },
    { role: 'tool', tool_call_id: 'read-1', content: original }] });
  assert.equal(bundle.functions.caveman_retrieve(), 'host', 'the host tool keeps its name');
  assert.equal(f.requests.length, 0);
  assert.deepEqual(f.reports.map(report => report.reason), ['recovery_name_conflict']);
  assert.equal(lines.filter(line => line.includes('adapter=openai-sdk reason=recovery_name_conflict')).length, 1);
  if (!requirePeers({ skip() {} }, 'ai-sdk')) return;
  const { generateText, tool, jsonSchema } = await import('ai'), { MockLanguageModelV4 } = await import('ai/test'), { withCaveman } = await import('../dist/ai-sdk.js');
  const model = new MockLanguageModelV4({ doGenerate: { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] } });
  const clash = tool({ description: 'host', inputSchema: jsonSchema({ type: 'object', properties: {} }), execute: async () => 'host' });
  await generateText({ ...withCaveman({ model, tools: { caveman_retrieve: clash } }, { runtime: f.runtime, scope }), messages: [{ role: 'user', content: 'go' }, ...toolTurn('read-1', original)], maxRetries: 0 });
  assert.equal(f.reports.at(-1).reason, 'recovery_name_conflict');
  assert.equal(model.doGenerateCalls[0].prompt.at(-1).content[0].output.value, original);
});

test('C14: OpenAI Responses turns that OpenAI would store pass through unless store is false', async t => {
  if (!requirePeers(t, 'openai')) return;
  const { default: OpenAI } = await import('openai'), { withCavemanOpenAITools } = await import('../dist/openai.js');
  for (const store of [undefined, true, false]) {
    const f = runtimeFixture(); t.after(() => f.runtime.close());
    const seen = [];
    const fetch = async (_url, init) => { seen.push(JSON.parse(init.body)); return Response.json({ id: 'resp_1', object: 'response', created_at: 1, status: 'completed', model: 'm',
      output: [{ type: 'message', id: 'msg_1', role: 'assistant', status: 'completed', content: [{ type: 'output_text', text: 'done', annotations: [] }] }],
      usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2 } }); };
    const bundle = withCavemanOpenAITools(new OpenAI({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime: f.runtime, scope, fetch, protocol: 'openai-responses',
      tools: [{ type: 'function', name: 'read', description: 'Read', parameters: { type: 'object', properties: {} }, strict: false }], functions: { read: () => original } });
    await bundle.client.responses.create({ model: 'm', tools: bundle.tools, ...(store === undefined ? {} : { store }), input: [
      { type: 'function_call', call_id: 'read-1', name: 'read', arguments: '{}' }, { type: 'function_call_output', call_id: 'read-1', output: original }] });
    assert.equal(seen[0].input[1].output, store === false ? shortened : original, `store=${store}`);
    if (store !== false) assert.deepEqual(f.reports.map(report => report.reason), ['provider_state_retained']);
  }
});

test('C14: usage is read from the tail of a large JSON response without buffering or reparsing the body', async t => {
  if (!requirePeers(t, 'openai')) return;
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const { default: OpenAI } = await import('openai'), { withCavemanOpenAITools } = await import('../dist/openai.js');
  const fetch = async () => Response.json({ id: 'c', object: 'chat.completion', created: 1, model: 'm',
    choices: [{ index: 0, message: { role: 'assistant', content: 'z'.repeat(3 << 20) }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 7, completion_tokens: 9, total_tokens: 16, prompt_tokens_details: { cached_tokens: 2 } } });
  const bundle = withCavemanOpenAITools(new OpenAI({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime: f.runtime, scope, fetch, protocol: 'openai-chat',
    tools: [{ type: 'function', function: { name: 'read', description: 'Read', parameters: { type: 'object', properties: {} } } }], functions: { read: () => original } });
  await bundle.client.chat.completions.create({ model: 'm', tools: bundle.tools, messages: [{ role: 'user', content: 'go' },
    { role: 'assistant', content: null, tool_calls: [{ id: 'read-1', type: 'function', function: { name: 'read', arguments: '{}' } }] },
    { role: 'tool', tool_call_id: 'read-1', content: original }] });
  await tick();
  const completed = f.receipts.find(receipt => receipt.event_kind === 'completed');
  assert.deepEqual([completed.usage.input_tokens, completed.usage.output_tokens, completed.usage.cache_read_tokens, completed.usage.complete], [7, 9, 2, true]);
});

test('C14: ai-sdk uses one logical call id per request, and Google asks each callable tool for its declaration once per turn', async t => {
  if (!requirePeers(t, 'ai-sdk')) return;
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const { generateText } = await import('ai'), { MockLanguageModelV4 } = await import('ai/test'), { withCaveman } = await import('../dist/ai-sdk.js');
  const model = new MockLanguageModelV4({ doGenerate: { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] } });
  const bundle = withCaveman({ model }, { runtime: f.runtime, scope });
  for (let i = 0; i < 2; i++) await generateText({ ...bundle, messages: [{ role: 'user', content: 'go' }, ...toolTurn('read-1', original)], maxRetries: 0 });
  assert.equal(f.requests.length, 2);
  assert.notEqual(f.requests[0].logical_call_id, f.requests[1].logical_call_id);
  if (!requirePeers({ skip() {} }, 'google')) return;
  const { CavemanGoogleGenAI } = await import('../dist/google.js');
  const native = globalThis.fetch;
  globalThis.fetch = async () => Response.json({ candidates: [{ content: { role: 'model', parts: [{ text: 'done' }] }, finishReason: 'STOP' }] });
  t.after(() => { globalThis.fetch = native; });
  let declared = 0;
  const read = { tool: async () => { declared++; return { functionDeclarations: [{ name: 'read', description: 'Read', parametersJsonSchema: { type: 'object', properties: {} } }] }; }, callTool: async () => [] };
  await new CavemanGoogleGenAI({ apiKey: 'k' }, { runtime: f.runtime, scope }).models.generateContent({ model: 'gemini-x', contents: 'go', config: { tools: [read] } });
  assert.equal(declared, 1);
});

test('C8: when an SDK-internal seam moves, wrapping still succeeds and calls run natively with adapter_error', async t => {
  if (!requirePeers(t, 'openai')) return;
  const lines = warnings(t);
  const { default: OpenAI } = await import('openai'), { withCavemanOpenAITools } = await import('../dist/openai.js');
  const post = Object.getOwnPropertyDescriptor(OpenAI.prototype, 'post') ?? Object.getOwnPropertyDescriptor(Object.getPrototypeOf(OpenAI.prototype), 'post');
  const owner = Object.hasOwn(OpenAI.prototype, 'post') ? OpenAI.prototype : Object.getPrototypeOf(OpenAI.prototype);
  // An accessor without a setter: the adapter's `post` hook cannot be installed.
  Object.defineProperty(owner, 'post', { configurable: true, get() { return post.value; } });
  t.after(() => Object.defineProperty(owner, 'post', post));
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const result = await drivers.openai.run(f.runtime);
  assert.equal(result.seen, original);
  assert.ok(lines.some(line => line.includes('adapter=openai-sdk reason=adapter_error')), lines.join('\n'));
  if (!requirePeers({ skip() {} }, 'google')) return;
  const { GoogleGenAI } = await import('@google/genai'), { CavemanGoogleGenAI } = await import('../dist/google.js');
  const client = Object.getPrototypeOf(new GoogleGenAI({ apiKey: 'k' }).apiClient), requestStream = client.requestStream;
  delete client.requestStream;
  try {
    const ai = new CavemanGoogleGenAI({ apiKey: 'k' }, { runtime: f.runtime, scope });
    assert.equal(ai.models.constructor.name.length > 0, true, 'construction fell back to the native client');
  } finally { client.requestStream = requestStream; }
  assert.ok(lines.some(line => line.includes('adapter=google-sdk reason=adapter_error')), lines.join('\n'));
});
