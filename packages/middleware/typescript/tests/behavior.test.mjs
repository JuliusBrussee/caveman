import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, mkdtemp, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { isDeepStrictEqual, promisify } from 'node:util';
import { createMiddlewareRuntime } from '@caveman-ai/sdk/middleware';
import { drivers } from './drivers.mjs';
import { requirePeers } from './peers.mjs';
import { finish, handle, original, runtimeFixture, scope, shortened, usage } from './runtime-fixture.mjs';

// The same five behaviors for every adapter, through its compressing entry point.
for (const [name, driver] of Object.entries(drivers)) {
  test(`${name}: compresses an outbound copy and leaves the caller's input untouched`, async t => {
    if (!requirePeers(t, name)) return;
    const f = runtimeFixture(); t.after(() => f.runtime.close());
    const result = await driver.run(f.runtime);
    assert.equal(result.seen, shortened);
    assert.ok(result.intact, 'caller input changed');
    assert.equal(f.requests.length, 1);
    assert.equal(f.requests[0].segments[0].content, original);
    assert.ok(f.reports.some(report => report.status === 'applied'));
  });

  test(`${name}: a runtime outage passes the original through`, async t => {
    if (!requirePeers(t, name)) return;
    const runtime = createMiddlewareRuntime({ fetch: async () => { throw new TypeError('fetch failed'); } }); t.after(() => runtime.close());
    const result = await driver.run(runtime);
    assert.equal(result.seen, original);
    assert.ok(result.intact);
  });

  test(`${name}: an exception in adapter code passes the original through as adapter_error`, async t => {
    if (!requirePeers(t, name)) return;
    const f = runtimeFixture(); t.after(() => f.runtime.close());
    f.runtime.optimize = async () => { throw new TypeError('adapter bug'); };
    const result = await driver.run(f.runtime);
    assert.equal(result.seen, original);
    assert.ok(result.intact);
    assert.ok(f.reports.some(report => report.reason === 'adapter_error'), JSON.stringify(f.reports));
  });

  test(`${name}: strict mode raises an exception in adapter code as adapter_error`, async t => {
    if (!requirePeers(t, name)) return;
    const f = runtimeFixture({ strict: true }); t.after(() => f.runtime.close());
    f.runtime.optimize = async () => { throw new TypeError('adapter bug'); };
    // Frameworks may wrap it (provider SDKs as a connection error); the code rides the cause chain.
    const codes = error => error ? [error.code, ...codes(error.cause)] : [];
    await assert.rejects(driver.run(f.runtime), error => codes(error).includes('adapter_error'));
  });

  test(`${name}: scopes are normalized; an unusable or missing scope runs recovery-free without throwing`, async t => {
    if (!requirePeers(t, name)) return;
    const email = runtimeFixture(); t.after(() => email.runtime.close());
    assert.equal((await driver.run(email.runtime, { scope: { namespace: 'tenant', session_id: 'jane.doe@acme.com' } })).seen, shortened);
    assert.match(email.requests[0].scope.session_id, /^h-[0-9a-f]{32}$/);
    const invalid = runtimeFixture(); t.after(() => invalid.runtime.close());
    assert.equal((await driver.run(invalid.runtime, { scope: { namespace: 'tenant', session_id: '' } })).seen, original);
    assert.equal(invalid.requests.length, 0);
    assert.ok(invalid.reports.some(report => report.reason === 'invalid_scope'), JSON.stringify(invalid.reports));
    const missing = runtimeFixture(); t.after(() => missing.runtime.close());
    assert.equal((await driver.run(missing.runtime, { scope: () => null })).seen, original);
    assert.equal(missing.requests.length, 0);
    assert.ok(missing.reports.some(report => report.reason === 'recovery_unbound'), JSON.stringify(missing.reports));
  });
}

// Every framework out of range at once, in a child process whose application node_modules says so. `ai` is a
// prerelease inside the numeric range: prereleases are deliberately unsupported (versions.ts).
const fake = { ai: '7.1.0-canary.3', langchain: '2.0.0', '@langchain/core': '2.0.0', '@google/genai': '3.0.0', '@strands-agents/sdk': '2.0.0',
  '@mastra/core': '2.0.0', '@modelcontextprotocol/sdk': '2.0.0', openai: '8.0.0', '@anthropic-ai/sdk': '0.129.0' };
let child;
async function outOfRange(adapters) {
  child ??= (async () => {
    const cwd = await mkdtemp(join(tmpdir(), 'caveman-out-of-range-'));
    for (const [name, version] of Object.entries(fake)) {
      await mkdir(join(cwd, 'node_modules', name), { recursive: true });
      await writeFile(join(cwd, 'node_modules', name, 'package.json'), JSON.stringify({ name, version }));
    }
    const { stdout } = await promisify(execFile)(process.execPath, [fileURLToPath(new URL('./out-of-range.mjs', import.meta.url))],
      { cwd, env: { ...process.env, CAVEMAN_FAKE_VERSIONS: JSON.stringify(fake), CAVEMAN_ADAPTERS: JSON.stringify(adapters) }, maxBuffer: 16 << 20 });
    return JSON.parse(stdout);
  })();
  return child;
}
const installed = Object.keys(drivers).filter(name => requirePeers({ skip() {} }, name));
for (const [name, driver] of Object.entries(drivers)) {
  test(`${name}: an out-of-range framework warns once, passes through, raises only from strict ready(), and runs when accepted`, async t => {
    if (!requirePeers(t, name)) return;
    const { results, warnings } = await outOfRange(installed), result = results[name];
    assert.ok(!result.error, result.error);
    assert.equal(result.skipped.seen, original);
    assert.ok(result.skipped.intact);
    assert.equal(result.requests, 0);
    assert.ok(result.reasons.includes('unsupported_version'), JSON.stringify(result.reasons));
    assert.equal(warnings.filter(line => line.includes(`adapter=${driver.id} reason=unsupported_version`)).length, 1, warnings.join('\n'));
    assert.equal(result.strictRun, original, 'strict mode threw on the request path or at wrap time');
    assert.equal(result.ready, 'unsupported_version');
    assert.equal(result.accepted.seen, shortened, 'acceptFrameworkVersion did not run the adapter');
  });
}

// ---------------------------------------------------------------- Stage 4 matrix: blackholed runtime, bundles

const nativeFetch = globalThis.fetch;
/** A runtime endpoint that accepts connections and never answers: up, but wedged. */
async function blackhole(t) {
  const sockets = new Set();
  const server = createServer(socket => { sockets.add(socket); socket.on('error', () => {}); }).listen(0, '127.0.0.1');
  await once(server, 'listening');
  t.after(() => { for (const socket of sockets) socket.destroy(); server.close(); });
  return `http://127.0.0.1:${server.address().port}`;
}

for (const [name, driver] of Object.entries(drivers)) {
  test(`${name}: a blackholed runtime (accepts, never answers) passes the original through at the deadline`, async t => {
    if (!requirePeers(t, name)) return;
    const reports = [];
    // An explicit fetch: the google driver swaps globalThis.fetch for its provider mock during the call.
    const runtime = createMiddlewareRuntime({ endpoint: await blackhole(t), deadlineMs: 150, fetch: (url, init) => nativeFetch(url, init),
      onReport: report => reports.push(report) }); t.after(() => runtime.close());
    const started = performance.now();
    const result = await driver.run(runtime);
    assert.equal(result.seen, original);
    assert.ok(result.intact);
    assert.ok(reports.some(report => report.reason === 'deadline'), JSON.stringify(reports));
    assert.ok(performance.now() - started < 10_000, 'the call waited far past its 150 ms deadline');
  });
}

// bundle.test.mjs bundles ai-sdk, langchain and strands; this covers every other adapter the same way (ESM).
const unbundled = Object.keys(drivers).filter(name => !['ai-sdk', 'langchain', 'strands'].includes(name));
test(`a minified esm bundle with inlined frameworks still compresses (${unbundled.join(', ')})`, async t => {
  for (const name of unbundled) if (!requirePeers(t, name)) return;
  const here = url => JSON.stringify(fileURLToPath(new URL(url, import.meta.url)));
  const directory = await mkdtemp(join(tmpdir(), 'caveman-bundle-'));
  const source = join(directory, 'entry.mjs'), outfile = join(directory, 'bundle.mjs');
  await writeFile(source, `
    import { drivers } from ${here('./drivers.mjs')};
    import { runtimeFixture, shortened } from ${here('./runtime-fixture.mjs')};
    const warnings = [], results = {};
    console.warn = (...args) => warnings.push(args.join(' '));
    for (const name of ${JSON.stringify(unbundled)}) {
      const f = runtimeFixture();
      try { results[name] = (await drivers[name].run(f.runtime)).seen === shortened; } catch (error) { results[name] = String(error?.stack ?? error); }
      finally { f.runtime.close(); }
    }
    console.log(JSON.stringify({ results, warnings }));`);
  // Optional integrations the frameworks import lazily stay external, as in any deploy bundle.
  const optional = { name: 'optional-dependencies', setup(build) {
    build.onResolve({ filter: /^[^./]/ }, async args => {
      if (args.pluginData) return undefined;
      const resolved = await build.resolve(args.path, { kind: args.kind, resolveDir: args.resolveDir, importer: args.importer, pluginData: true });
      return resolved.errors.length ? { path: args.path, external: true } : resolved;
    });
  } };
  const { build } = await import('esbuild');
  await build({ entryPoints: [source], outfile, bundle: true, minify: true, platform: 'node', format: 'esm', target: 'node22.12', logLevel: 'silent', plugins: [optional],
    banner: { js: "import { createRequire as __cr } from 'node:module'; const require = __cr(import.meta.url);" } });
  const { stdout } = await promisify(execFile)(process.execPath, [outfile], { cwd: directory, maxBuffer: 16 << 20 });
  const { results, warnings } = JSON.parse(stdout.trim().split('\n').at(-1));
  for (const name of unbundled) assert.equal(results[name], true, `${name}: ${results[name]}`);
  assert.ok(!warnings.some(line => /reason=(unsupported_version|version_unavailable)/.test(line)), warnings.join('\n'));
});

// ---------------------------------------------------------------- certified adapters: history shape, recovery, abort

// Each runner makes one native call with a caller-built history: `image` adds a PNG part to the first user turn,
// `turns` prepends that many earlier tool turns, `signal` is the caller's abort signal, and every provider request
// body is pushed onto `provider`. Returns the tool result the provider received last, whether the image reached it,
// and whether the caller's history survived.
const PNG = 'iVBORw0KGgoAAAANSUhEUg==';
const readTool = { type: 'function', function: { name: 'read', description: 'Read', parameters: { type: 'object', properties: {} } } };
const history = (first, turn, turns) => [first, ...Array.from({ length: turns }, (_, i) => turn(`old-${i}`, `ok ${i}`)).flat(), ...turn('read-1', original)];
const certified = {
  'ai-sdk': async (runtime, { image, turns = 0, signal, provider = [] } = {}) => {
    const { generateText } = await import('ai'), { MockLanguageModelV4 } = await import('ai/test'), { withCaveman } = await import('../dist/ai-sdk.js');
    const turn = (id, value) => [{ role: 'assistant', content: [{ type: 'tool-call', toolCallId: id, toolName: 'read', input: {} }] },
      { role: 'tool', content: [{ type: 'tool-result', toolCallId: id, toolName: 'read', output: { type: 'text', value } }] }];
    const messages = history({ role: 'user', content: [{ type: 'text', text: 'go' },
      ...(image ? [{ type: 'file', mediaType: 'image/png', data: new Uint8Array(Buffer.from(PNG, 'base64')) }] : [])] }, turn, turns);
    const before = structuredClone(messages);
    const model = new MockLanguageModelV4({ doGenerate: async ({ prompt }) => { provider.push(prompt); return { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] }; } });
    await generateText({ ...withCaveman({ model }, { runtime, scope }), messages, maxRetries: 0, abortSignal: signal });
    return { seen: provider[0].at(-1).content[0].output.value, image: provider[0][0].content.some(part => part.type === 'file'), intact: isDeepStrictEqual(messages, before) };
  },
  openai: async (runtime, { image, turns = 0, signal, provider = [] } = {}) => {
    const { default: OpenAI } = await import('openai'), { withCavemanOpenAITools } = await import('../dist/openai.js');
    const fetch = async (_url, init) => { provider.push(JSON.parse(init.body)); return Response.json({ id: 'c', object: 'chat.completion', created: 1, model: 'm',
      choices: [{ index: 0, message: { role: 'assistant', content: 'done' }, finish_reason: 'stop' }] }); };
    const bundle = withCavemanOpenAITools(new OpenAI({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime, scope, fetch, protocol: 'openai-chat',
      tools: [readTool], functions: { read: () => original } });
    const turn = (id, content) => [{ role: 'assistant', content: null, tool_calls: [{ id, type: 'function', function: { name: 'read', arguments: '{}' } }] },
      { role: 'tool', tool_call_id: id, content }];
    const messages = history({ role: 'user', content: [{ type: 'text', text: 'go' },
      ...(image ? [{ type: 'image_url', image_url: { url: `data:image/png;base64,${PNG}` } }] : [])] }, turn, turns);
    const before = structuredClone(messages);
    await bundle.client.chat.completions.create({ model: 'm', messages, tools: bundle.tools }, { signal });
    return { seen: provider[0].messages.at(-1).content, image: JSON.stringify(provider[0].messages[0]).includes(PNG), intact: isDeepStrictEqual(messages, before) };
  },
  anthropic: async (runtime, { image, turns = 0, signal, provider = [], reply } = {}) => {
    const { default: Anthropic } = await import('@anthropic-ai/sdk'), { withCavemanAnthropic } = await import('../dist/anthropic.js');
    const fetch = async (_url, init) => { const body = JSON.parse(init.body); provider.push(body); return Response.json({ id: 'msg_1', type: 'message', role: 'assistant', model: 'm',
      content: reply?.(body) ?? [{ type: 'text', text: 'done' }], stop_reason: reply?.(body) ? 'tool_use' : 'end_turn', stop_sequence: null, usage: { input_tokens: 1, output_tokens: 1 } }); };
    const client = withCavemanAnthropic(new Anthropic({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime, scope, fetch });
    const turn = (id, content) => [{ role: 'assistant', content: [{ type: 'tool_use', id, name: 'read', input: {} }] },
      { role: 'user', content: [{ type: 'tool_result', tool_use_id: id, content }] }];
    const messages = history({ role: 'user', content: [{ type: 'text', text: 'go' },
      ...(image ? [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: PNG } }] : [])] }, turn, turns);
    const before = structuredClone(messages);
    await client.beta.messages.toolRunner({ model: 'm', max_tokens: 10, messages,
      tools: [{ name: 'read', description: 'Read', input_schema: { type: 'object', properties: {} }, run: async () => original }] }, { signal });
    return { seen: provider[0].messages.at(-1).content[0].content, image: JSON.stringify(provider[0].messages[0]).includes(PNG), intact: isDeepStrictEqual(messages, before) };
  },
  langchain: async (runtime, { image, turns = 0, signal, provider = [], toolCalls = [] } = {}) => {
    const { createAgent, FakeToolCallingModel } = await import('langchain');
    const { AIMessage, HumanMessage, ToolMessage } = await import('@langchain/core/messages'), { tool } = await import('@langchain/core/tools');
    const { withCavemanAgent, scopeFromConfig } = await import('../dist/langchain.js');
    // bindTools returns a bound copy: the agent refuses a model that already carries tools on its second step.
    class Capture extends FakeToolCallingModel {
      bindTools(tools) { const bound = new Capture({ toolCalls: this.toolCalls, indexRef: this.indexRef }); bound.tools = tools; return bound; }
      async _generate(messages, options, run) { provider.push(messages); return super._generate(messages, options, run); } }
    const read = tool(async () => original, { name: 'read', description: 'Read', schema: { type: 'object', properties: {} } });
    const agent = createAgent(withCavemanAgent({ model: new Capture({ toolCalls }), tools: [read] }, { runtime, scope: config => scopeFromConfig(config, scope.namespace) }));
    const turn = (id, content) => [new AIMessage({ content: '', tool_calls: [{ id, name: 'read', args: {} }] }), new ToolMessage({ content, tool_call_id: id, name: 'read' })];
    const messages = history(new HumanMessage({ content: [{ type: 'text', text: 'go' },
      ...(image ? [{ type: 'image_url', image_url: { url: `data:image/png;base64,${PNG}` } }] : [])] }), turn, turns);
    await agent.invoke({ messages }, { configurable: { thread_id: 'certified' }, signal });
    return { seen: provider[0].at(-1).content, image: JSON.stringify(provider[0][0].content).includes(PNG), intact: messages.at(-1).content === original };
  },
};

for (const [name, run] of Object.entries(certified)) {
  test(`${name}: an image and a long history (2100 earlier tool turns) still compress the newest result; the image reaches the provider`, async t => {
    if (!requirePeers(t, name)) return;
    const f = runtimeFixture(); t.after(() => f.runtime.close());
    const result = await run(f.runtime, { image: true, turns: 2100 });
    assert.equal(result.seen, shortened, 'the newest tool result was not compressed');
    assert.ok(result.image, 'the image did not reach the provider');
    assert.ok(result.intact, "the caller's history changed");
    assert.equal(f.requests.length, 1);
    const [request] = f.requests;
    assert.ok(request.segments.some(segment => segment.content === original), 'the newest result was not sent');
    assert.ok(request.segments.length <= 256 && request.context_manifest.length <= 4096, 'request exceeds the default budgets');
    assert.ok(f.reports.some(report => report.status === 'applied'), JSON.stringify(f.reports));
  });

  test(`${name}: a caller abort while the runtime is slow rejects promptly and never reaches the provider`, async t => {
    if (!requirePeers(t, name)) return;
    // The runtime answers nothing until the SDK's own request is aborted.
    const stalled = (_url, init) => new Promise((_, reject) => init.signal?.addEventListener('abort', () => reject(init.signal.reason), { once: true }));
    const runtime = createMiddlewareRuntime({ fetch: stalled, deadlineMs: 20_000 }); t.after(() => runtime.close());
    const controller = new AbortController(), provider = [];
    setTimeout(() => controller.abort(new Error('caller aborted')), 50);
    const started = performance.now();
    // The caller's reason, or the provider SDK's own abort error ("Request was aborted.").
    await assert.rejects(run(runtime, { signal: controller.signal, provider }), error => /caller aborted|aborted/i.test(`${error?.message} ${error?.cause?.message}`));
    assert.ok(performance.now() - started < 5_000, `abort took ${Math.round(performance.now() - started)} ms: it waited for the runtime deadline`);
    assert.equal(provider.length, 0, 'an aborted call reached the provider');
  });
}

test('anthropic: toolRunner executes caveman_retrieve and the model receives the exact original', async t => {
  if (!requirePeers(t, 'anthropic')) return;
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const provider = [];
  const reply = body => body.messages.length === 3 ? [{ type: 'tool_use', id: 'retrieve-1', name: 'caveman_retrieve', input: { handle } }] : null;
  const result = await certified.anthropic(f.runtime, { provider, reply });
  assert.equal(result.seen, shortened);
  assert.equal(provider.length, 2);
  const recovered = provider[1].messages.at(-1).content.find(block => block.type === 'tool_result' && block.tool_use_id === 'retrieve-1');
  const text = typeof recovered.content === 'string' ? recovered.content : recovered.content.map(block => block.text).join('');
  assert.equal(JSON.parse(text).text, original);
  assert.equal(f.retrievals.length, 1);
});

test('langchain: the agent executes caveman_retrieve and the model receives the exact original', async t => {
  if (!requirePeers(t, 'langchain')) return;
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const provider = [];
  const result = await certified.langchain(f.runtime, { provider, toolCalls: [[{ name: 'caveman_retrieve', args: { handle }, id: 'retrieve-1' }], []] });
  assert.equal(result.seen, shortened);
  const recovered = provider[1].find(message => message.tool_call_id === 'retrieve-1');
  assert.ok(recovered, 'the recovery result never reached the model');
  assert.ok(String(recovered.content).includes(original) || JSON.parse(recovered.content).text === original, 'recovered text differs from the original');
  assert.equal(f.retrievals.length, 1);
});

test('openai: a streamed tool-loop call compresses and delivers every SSE chunk', async t => {
  if (!requirePeers(t, 'openai')) return;
  const { default: OpenAI } = await import('openai'), { withCavemanOpenAITools } = await import('../dist/openai.js');
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const seen = [], chunk = { id: 's', object: 'chat.completion.chunk', created: 1, model: 'm', choices: [{ index: 0, delta: { content: 'part' }, finish_reason: null }] };
  const fetch = async (_url, init) => { seen.push(JSON.parse(init.body));
    return new Response(`data: ${JSON.stringify(chunk)}\n\ndata: [DONE]\n\n`, { headers: { 'content-type': 'text/event-stream' } }); };
  const bundle = withCavemanOpenAITools(new OpenAI({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime: f.runtime, scope, fetch, protocol: 'openai-chat',
    tools: [readTool], functions: { read: () => original } });
  const messages = [{ role: 'user', content: 'go' }, { role: 'assistant', content: null, tool_calls: [{ id: 'read-1', type: 'function', function: { name: 'read', arguments: '{}' } }] },
    { role: 'tool', tool_call_id: 'read-1', content: original }];
  const before = structuredClone(messages);
  const stream = await bundle.client.chat.completions.create({ model: 'm', messages, tools: bundle.tools, stream: true });
  const chunks = [];
  for await (const part of stream) chunks.push(part);
  assert.deepEqual(chunks, [chunk]);
  assert.equal(seen[0].messages.at(-1).content, shortened);
  assert.deepEqual(messages, before);
});
