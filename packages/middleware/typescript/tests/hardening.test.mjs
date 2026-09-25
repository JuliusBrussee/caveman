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

test('C4/TS-8: a Mastra thread too large to scan skips compression for that thread and turn only', async t => {
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
  // TS-8: the latch lasts one turn. The same thread's next turn fits the budget and compresses again.
  await agent.generate([{ role: 'user', content: 'go' }, ...toolTurn('read-1', original)], { memory: { thread: 'big-thread', resource: 'tenant-a' } });
  assert.equal(sent(), shortened, 'the thread that once overflowed compresses on its next turn');
  const normal = [{ role: 'user', content: 'go' }, ...toolTurn('read-1', original)];
  await agent.generate(normal, { memory: { thread: 'normal-thread', resource: 'tenant-b' } });
  assert.equal(sent(), shortened);
  await agent.generate(normal);
  assert.equal(sent(), shortened);
  assert.equal(f.requests.length, 3, 'later normal calls still send optimize requests');
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

// ---------------------------------------------------------------- Stage 5 review (TS-3 .. TS-8, nits)

const readSchema = { type: 'object', properties: {} };
const chat = (message, finish_reason = 'stop') => ({ id: 'c', object: 'chat.completion', created: 1, model: 'm', choices: [{ index: 0, message, finish_reason }],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } });

test('TS-3: a failed LangChain tool is marked status:error, never sent for compression, and the agent still resolves', async t => {
  if (!requirePeers(t, 'langchain')) return;
  const { createAgent, FakeToolCallingModel, toolRetryMiddleware } = await import('langchain'), { HumanMessage } = await import('@langchain/core/messages');
  const { tool } = await import('@langchain/core/tools'), { withCavemanAgent } = await import('../dist/langchain.js');
  let failures = 0;
  const read = tool(async () => original, { name: 'read', description: 'Read', schema: readSchema });
  const fail = tool(async () => { failures++; throw new Error(original); }, { name: 'fail', description: 'Fail', schema: readSchema });
  const turns = () => new FakeToolCallingModel({ toolCalls: [[{ id: 'a', name: 'read', args: {} }, { id: 'b', name: 'fail', args: {} }], []] });
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const result = await createAgent(withCavemanAgent({ model: turns(), tools: [read, fail] }, { runtime: f.runtime, scope })).invoke({ messages: [new HumanMessage('go')] });
  const failed = result.messages.find(message => message.getType() === 'tool' && message.name === 'fail');
  assert.equal(failed.status, 'error');
  assert.equal(failed.content, `Error: ${original}\n Please fix your mistakes.`, "ToolNode's own error text");
  assert.deepEqual(f.requests.at(-1).segments.map(segment => segment.content), [original], 'only the parallel read was sent, not the error text');
  // An application's own wrapToolCall (retry) still sees the error: ours steps aside.
  const retried = runtimeFixture(); t.after(() => retried.runtime.close());
  failures = 0;
  await createAgent(withCavemanAgent({ model: turns(), tools: [read, fail], middleware: [toolRetryMiddleware({ maxRetries: 1, initialDelayMs: 0, jitter: false })] },
    { runtime: retried.runtime, scope })).invoke({ messages: [new HumanMessage('go')] });
  assert.equal(failures, 2, 'toolRetryMiddleware retried the failing tool');
  assert.deepEqual(retried.requests.at(-1).segments.map(segment => segment.content), [original]);
});

test('TS-4/TS-5: wrapping an ai-sdk bundle twice compresses once; the bundle stays mutable in every mode', async t => {
  if (!requirePeers(t, 'ai-sdk')) return;
  const { generateText, jsonSchema, tool } = await import('ai'), { MockLanguageModelV4 } = await import('ai/test'), { withCaveman } = await import('../dist/ai-sdk.js');
  const read = tool({ description: 'Read', inputSchema: jsonSchema(readSchema), execute: async () => original });
  const messages = [{ role: 'user', content: 'go' }, ...toolTurn('read-1', original)];
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const model = new MockLanguageModelV4({ doGenerate: { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] } });
  const once = withCaveman({ model, tools: { read } }, { runtime: f.runtime, scope }), twice = withCaveman(once, { runtime: f.runtime, scope });
  assert.equal(twice, once);
  await generateText({ ...twice, messages, maxRetries: 0 });
  assert.equal(model.doGenerateCalls[0].prompt.at(-1).content[0].output.value, shortened);
  assert.equal(f.requests.length, 1);
  assert.deepEqual(f.reports.map(report => `${report.status}:${report.reason}`), ['applied:eligible']);
  for (const mode of ['off', 'record', 'compress']) {
    const m = runtimeFixture({ mode }); t.after(() => m.runtime.close());
    const native = new MockLanguageModelV4({ doGenerate: { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] } });
    const bundle = withCaveman({ model: native, tools: { read } }, { runtime: m.runtime, scope });
    bundle.maxRetries = 0; bundle.tools.extra = read;
    assert.equal(Object.isFrozen(bundle.tools.caveman_retrieve ?? {}), mode === 'compress', `${mode}: only the recovery tool is frozen`);
    await generateText({ ...bundle, messages });
    assert.equal(native.doGenerateCalls[0].prompt.at(-1).content[0].output.value, mode === 'compress' ? shortened : original, `${mode}: an added tool keeps recovery attested`);
  }
});

test('TS-6: CavemanChatModel exposes the inner model profile', async t => {
  if (!requirePeers(t, 'langchain')) return;
  const { withCavemanModel } = await import('../dist/langchain-model.js'), { FakeListChatModel } = await import('@langchain/core/utils/testing');
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const inner = new FakeListChatModel({ responses: ['done'] });
  Object.defineProperty(inner, 'profile', { value: { structuredOutput: true, maxInputTokens: 128000 } });
  assert.deepEqual(withCavemanModel(inner, { runtime: f.runtime, scope }).profile, { structuredOutput: true, maxInputTokens: 128000 });
});

test('TS-7: an ai-sdk retry (503, then success) keeps one logical call id and one optimize', async t => {
  if (!requirePeers(t, 'ai-sdk')) return;
  const { APICallError, generateText } = await import('ai'), { MockLanguageModelV4 } = await import('ai/test'), { withCaveman } = await import('../dist/ai-sdk.js');
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  let n = 0;
  const model = new MockLanguageModelV4({ doGenerate: async () => {
    if (++n === 1) throw new APICallError({ message: 'overloaded', url: 'x', requestBodyValues: {}, statusCode: 503, isRetryable: true, responseHeaders: { 'retry-after-ms': '1' } });
    return { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] };
  } });
  await generateText({ ...withCaveman({ model }, { runtime: f.runtime, scope }), messages: [{ role: 'user', content: 'go' }, ...toolTurn('read-1', original)], maxRetries: 2 });
  await tick();
  assert.equal(n, 2);
  assert.equal(f.requests.length, 1, 'the retry reused the first optimization');
  assert.ok(model.doGenerateCalls.every(call => call.prompt.at(-1).content[0].output.value === shortened), 'both attempts sent the compressed copy');
  assert.equal(new Set([...f.reports, ...f.receipts].map(item => item.logical_call_id)).size, 1, JSON.stringify(f.reports));
  assert.equal(new Set(f.receipts.map(receipt => receipt.attempt_id)).size, 2, 'each attempt keeps its own attempt id');
});

test('nit: withCavemanOpenAI/Anthropic hint recovery_unbound only once a plain call runs, not for runTools/toolRunner', async t => {
  if (!requirePeers(t, 'openai') || !requirePeers(t, 'anthropic')) return;
  const lines = warnings(t), hinted = entry => lines.filter(line => line.includes(`${entry} cannot bind`)).length;
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const { default: OpenAI } = await import('openai'), { withCavemanOpenAI } = await import('../dist/openai.js');
  const fetch = async () => Response.json(chat({ role: 'assistant', content: 'done' }));
  const openai = withCavemanOpenAI(new OpenAI({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime: f.runtime, scope, fetch });
  await openai.chat.completions.runTools({ model: 'm', messages: [{ role: 'user', content: 'go' }],
    tools: [{ type: 'function', function: { name: 'read', parameters: readSchema, function: async () => original } }] }).finalContent();
  assert.equal(hinted('withCavemanOpenAI create()'), 0, lines.join('\n'));
  await openai.chat.completions.create({ model: 'm', messages: [{ role: 'user', content: 'go' }] });
  assert.equal(hinted('withCavemanOpenAI create()'), 1, lines.join('\n'));
  const { default: Anthropic } = await import('@anthropic-ai/sdk'), { withCavemanAnthropic } = await import('../dist/anthropic.js');
  const reply = async () => Response.json({ id: 'msg_1', type: 'message', role: 'assistant', model: 'm', content: [{ type: 'text', text: 'done' }],
    stop_reason: 'end_turn', stop_sequence: null, usage: { input_tokens: 1, output_tokens: 1 } });
  const anthropic = withCavemanAnthropic(new Anthropic({ apiKey: 'k', fetch: reply, maxRetries: 0 }), { runtime: f.runtime, scope, fetch: reply });
  await anthropic.beta.messages.toolRunner({ model: 'm', max_tokens: 10, messages: [{ role: 'user', content: 'go' }],
    tools: [{ name: 'read', description: 'Read', input_schema: readSchema, run: async () => original }] });
  assert.equal(hinted('withCavemanAnthropic messages.create()/stream()'), 0, lines.join('\n'));
  await anthropic.messages.create({ model: 'm', max_tokens: 10, messages: [{ role: 'user', content: 'go' }] });
  assert.equal(hinted('withCavemanAnthropic messages.create()/stream()'), 1, lines.join('\n'));
});

test('nit: a call nested in a runTools tool on another client keeps its own scope and logical call id', async t => {
  if (!requirePeers(t, 'openai')) return;
  const { default: OpenAI } = await import('openai'), { withCavemanOpenAI } = await import('../dist/openai.js');
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  let n = 0;
  const fetchA = async () => Response.json(++n === 1
    ? chat({ role: 'assistant', content: null, tool_calls: [{ id: 'c1', type: 'function', function: { name: 'sub', arguments: '{}' } }] }, 'tool_calls')
    : chat({ role: 'assistant', content: 'done' }));
  const fetchB = async () => Response.json(chat({ role: 'assistant', content: 'inner' }));
  const a = withCavemanOpenAI(new OpenAI({ apiKey: 'k', fetch: fetchA, maxRetries: 0 }), { runtime: f.runtime, scope: { ...scope, session_id: 'tenant-a' }, fetch: fetchA });
  const b = withCavemanOpenAI(new OpenAI({ apiKey: 'k', fetch: fetchB, maxRetries: 0 }), { runtime: f.runtime, scope: { ...scope, session_id: 'tenant-b' }, fetch: fetchB });
  const sub = async () => { await b.chat.completions.create({ model: 'm', messages: [{ role: 'user', content: 'x' },
    { role: 'assistant', content: null, tool_calls: [{ id: 'r', type: 'function', function: { name: 'read', arguments: '{}' } }] }, { role: 'tool', tool_call_id: 'r', content: original }] }); return 'ok'; };
  await a.chat.completions.runTools({ model: 'm', messages: [{ role: 'user', content: 'go' }],
    tools: [{ type: 'function', function: { name: 'sub', parameters: readSchema, function: sub } }] }).finalContent();
  await tick();
  const sessions = f.receipts.filter(receipt => receipt.event_kind === 'dispatch_intent').map(receipt => receipt.scope.session_id);
  assert.deepEqual(sessions, ['tenant-a', 'tenant-b', 'tenant-a'], 'the nested call dispatched under its own client scope');
  const ids = f.receipts.filter(receipt => receipt.event_kind === 'dispatch_intent').map(receipt => receipt.logical_call_id);
  assert.notEqual(ids[1], ids[0], 'the nested call is its own logical call');
});

test('nit: guardSync raises adapter_error on the strict request path; at wrap time it declines for strict ready()', async t => {
  if (!requirePeers(t, 'openai')) return;
  const { default: OpenAI } = await import('openai'), { withCavemanOpenAI, withCavemanOpenAITools } = await import('../dist/openai.js');
  const fetch = async () => Response.json(chat({ role: 'assistant', content: 'done' }));
  const strict = runtimeFixture({ strict: true }); t.after(() => strict.runtime.close());
  const client = withCavemanOpenAI(new OpenAI({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime: strict.runtime, scope, fetch });
  // A tool table the adapter cannot read: its own code throws while binding recovery.
  const broken = { type: 'function', function: { get name() { throw new TypeError('adapter bug'); }, parameters: readSchema, function: async () => 'x' } };
  assert.throws(() => client.chat.completions.runTools({ model: 'm', messages: [{ role: 'user', content: 'go' }], tools: [broken] }), { code: 'adapter_error' });
  const owner = Object.hasOwn(OpenAI.prototype, 'post') ? OpenAI.prototype : Object.getPrototypeOf(OpenAI.prototype), post = Object.getOwnPropertyDescriptor(owner, 'post');
  Object.defineProperty(owner, 'post', { configurable: true, get() { return post.value; } });
  t.after(() => Object.defineProperty(owner, 'post', post));
  const declined = runtimeFixture({ strict: true }); t.after(() => declined.runtime.close());
  withCavemanOpenAITools(new OpenAI({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime: declined.runtime, scope, fetch, protocol: 'openai-chat',
    tools: [{ type: 'function', function: { name: 'read', description: 'Read', parameters: readSchema } }], functions: { read: () => original } });
  await assert.rejects(declined.runtime.ready(), { code: 'adapter_error' });
});

test('nit: langchain-model reports @langchain/core as its framework version', async t => {
  if (!requirePeers(t, 'langchain')) return;
  const { inspectFrameworkCompatibility } = await import('../dist/compatibility.js');
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  await drivers.langchain.run(f.runtime);
  assert.equal(f.requests[0].adapter.framework_version, inspectFrameworkCompatibility('langchain-core').frameworks[0].installed_version);
});
