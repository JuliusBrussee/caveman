import assert from 'node:assert/strict';
import test from 'node:test';
import { requirePeers } from './peers.mjs';
import { finish, original, runtimeFixture, scope, usage } from './runtime-fixture.mjs';

// TS-1: the model calls caveman_retrieve with a well-formed handle the runtime does not know (404), has expired (410),
// or cannot serve (503). Every adapter's native tool loop still resolves, and the model reads `{error: code}`.
const handle = `cmw_${'f'.repeat(48)}`;
const failures = [[404, 'not_found'], [410, 'expired'], [503, 'runtime_unavailable']];
const readSchema = { type: 'object', properties: {} };
const lines = [];
console.warn = (...args) => lines.push(args.join(' '));
const completion = (message, finish_reason) => ({ id: 'c', object: 'chat.completion', created: 1, model: 'm', choices: [{ index: 0, message, finish_reason }],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } });

// Each loop returns the recovery output the model received on its next turn.
const loops = {
  'ai-sdk': async runtime => {
    const { generateText, jsonSchema, stepCountIs, tool } = await import('ai'), { MockLanguageModelV4 } = await import('ai/test'), { withCaveman } = await import('../dist/ai-sdk.js');
    let n = 0;
    const model = new MockLanguageModelV4({ doGenerate: async () => ++n === 1
      ? { content: [{ type: 'tool-call', toolCallId: 'r1', toolName: 'caveman_retrieve', input: JSON.stringify({ handle }) }], finishReason: { unified: 'tool-calls', raw: 'tool_calls' }, usage, warnings: [] }
      : { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] } });
    const read = tool({ description: 'Read', inputSchema: jsonSchema(readSchema), execute: async () => original });
    const result = await generateText({ ...withCaveman({ model, tools: { read } }, { runtime, scope }), prompt: 'go', stopWhen: stepCountIs(3), maxRetries: 0 });
    assert.equal(result.text, 'done');
    return model.doGenerateCalls[1].prompt.at(-1).content[0].output.value;
  },
  openai: async runtime => {
    const { default: OpenAI } = await import('openai'), { withCavemanOpenAI } = await import('../dist/openai.js');
    let n = 0;
    const fetch = async () => Response.json(++n === 1
      ? completion({ role: 'assistant', content: null, tool_calls: [{ id: 'r1', type: 'function', function: { name: 'caveman_retrieve', arguments: JSON.stringify({ handle }) } }] }, 'tool_calls')
      : completion({ role: 'assistant', content: 'done' }, 'stop'));
    const runner = withCavemanOpenAI(new OpenAI({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime, scope, fetch }).chat.completions.runTools({ model: 'm',
      messages: [{ role: 'user', content: 'go' }], tools: [{ type: 'function', function: { name: 'read', parameters: readSchema, function: async () => original } }] });
    assert.equal(await runner.finalContent(), 'done');
    return JSON.parse(runner.messages.find(message => message.role === 'tool').content);
  },
  'openai functions': async runtime => {
    const { default: OpenAI } = await import('openai'), { withCavemanOpenAITools } = await import('../dist/openai.js');
    const fetch = async () => Response.json(completion({ role: 'assistant', content: 'done' }, 'stop'));
    const bundle = withCavemanOpenAITools(new OpenAI({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime, scope, fetch, protocol: 'openai-chat',
      tools: [{ type: 'function', function: { name: 'read', description: 'Read', parameters: readSchema } }], functions: { read: () => original } });
    return bundle.functions.caveman_retrieve({ handle });
  },
  anthropic: async runtime => {
    const { default: Anthropic } = await import('@anthropic-ai/sdk'), { withCavemanAnthropic } = await import('../dist/anthropic.js');
    const seen = [];
    const fetch = async (_url, init) => { const first = seen.push(JSON.parse(init.body)) === 1;
      return Response.json({ id: `msg_${seen.length}`, type: 'message', role: 'assistant', model: 'm', stop_sequence: null, usage: { input_tokens: 1, output_tokens: 1 },
        content: first ? [{ type: 'tool_use', id: 'r1', name: 'caveman_retrieve', input: { handle } }] : [{ type: 'text', text: 'done' }], stop_reason: first ? 'tool_use' : 'end_turn' }); };
    const final = await withCavemanAnthropic(new Anthropic({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime, scope, fetch }).beta.messages.toolRunner({ model: 'm', max_tokens: 10,
      messages: [{ role: 'user', content: 'go' }], tools: [{ name: 'read', description: 'Read', input_schema: readSchema, run: async () => original }] });
    assert.equal(final.content[0].text, 'done');
    const result = seen[1].messages.at(-1).content.find(block => block.type === 'tool_result');
    return JSON.parse(typeof result.content === 'string' ? result.content : result.content[0].text);
  },
  google: async runtime => {
    const { CavemanGoogleGenAI } = await import('../dist/google.js');
    const seen = [], native = globalThis.fetch;
    globalThis.fetch = async (_url, init) => Response.json({ candidates: [{ finishReason: 'STOP', content: { role: 'model',
      parts: [seen.push(JSON.parse(init.body)) === 1 ? { functionCall: { name: 'caveman_retrieve', args: { handle } } } : { text: 'done' }] } }] });
    try {
      const read = { tool: async () => ({ functionDeclarations: [{ name: 'read', description: 'Read', parametersJsonSchema: readSchema }] }), callTool: async () => [] };
      const response = await new CavemanGoogleGenAI({ apiKey: 'k' }, { runtime, scope }).models.generateContent({ model: 'gemini-x', contents: 'go', config: { tools: [read] } });
      assert.equal(response.text, 'done');
      return seen[1].contents.at(-1).parts[0].functionResponse.response.output;
    } finally { globalThis.fetch = native; }
  },
  langchain: async runtime => {
    const { createAgent, FakeToolCallingModel } = await import('langchain'), { HumanMessage } = await import('@langchain/core/messages'), { tool } = await import('@langchain/core/tools');
    const { withCavemanAgent } = await import('../dist/langchain.js');
    const read = tool(async () => original, { name: 'read', description: 'Read', schema: readSchema });
    const model = new FakeToolCallingModel({ toolCalls: [[{ id: 'r1', name: 'caveman_retrieve', args: { handle } }], []] });
    const result = await createAgent(withCavemanAgent({ model, tools: [read] }, { runtime, scope })).invoke({ messages: [new HumanMessage('go')] });
    const recovered = result.messages.find(message => message.getType() === 'tool');
    assert.notEqual(recovered.status, 'error', 'the recovery answered; it did not fail');
    return JSON.parse(recovered.content);
  },
  strands: async runtime => {
    const { Agent, Model, FunctionTool } = await import('@strands-agents/sdk'), { withCavemanStrands } = await import('../dist/strands.js');
    const seen = [];
    class Fake extends Model {
      config = {};
      updateConfig(config) { this.config = config; }
      getConfig() { return this.config; }
      async *stream(messages) {
        yield { type: 'modelMessageStartEvent', role: 'assistant' };
        if (seen.push(messages) === 1) {
          yield { type: 'modelContentBlockStartEvent', start: { type: 'toolUseStart', name: 'caveman_retrieve', toolUseId: 'r1' } };
          yield { type: 'modelContentBlockDeltaEvent', delta: { type: 'toolUseInputDelta', input: JSON.stringify({ handle }) } };
          yield { type: 'modelContentBlockStopEvent' }; yield { type: 'modelMessageStopEvent', stopReason: 'toolUse' };
        } else {
          yield { type: 'modelContentBlockStartEvent' }; yield { type: 'modelContentBlockDeltaEvent', delta: { type: 'textDelta', text: 'done' } };
          yield { type: 'modelContentBlockStopEvent' }; yield { type: 'modelMessageStopEvent', stopReason: 'endTurn' };
        }
      }
    }
    const read = new FunctionTool({ name: 'read', description: 'Read', inputSchema: readSchema, callback: async () => original });
    await new Agent(withCavemanStrands({ model: new Fake(), tools: [read], printer: false }, { runtime, scope })).invoke('go');
    const block = seen[1].flatMap(message => message.content).find(block => block.type === 'toolResultBlock');
    assert.equal(block.status, 'success');
    return JSON.parse(block.content[0].text);
  },
  mastra: async runtime => {
    const { Agent } = await import('@mastra/core/agent'), { MockLanguageModelV4 } = await import('ai/test'), { withCavemanMastra } = await import('../dist/mastra.js');
    let n = 0;
    const model = new MockLanguageModelV4({ doGenerate: async () => ++n === 1
      ? { content: [{ type: 'tool-call', toolCallId: 'r1', toolName: 'caveman_retrieve', input: JSON.stringify({ handle }) }], finishReason: { unified: 'tool-calls', raw: 'tool_calls' }, usage, warnings: [] }
      : { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] } });
    const agent = withCavemanMastra(new Agent({ id: 'agent', name: 'agent', instructions: 'Be brief.', model }), { runtime, scope });
    const result = await agent.generate('go', { maxSteps: 3 });
    assert.equal(result.text, 'done');
    return model.doGenerateCalls[1].prompt.findLast(message => message.role === 'tool').content[0].output.value;
  },
  mcp: async runtime => {
    const { CavemanMCPHost } = await import('../dist/mcp.js');
    const result = await new CavemanMCPHost({ runtime, scope, serverId: 'server', protocolVersion: '2025-11-25' }).recovery.execute({ handle });
    assert.equal(result.isError, true, 'an MCP tool error result');
    return JSON.parse(result.content[0].text);
  },
};

for (const [name, loop] of Object.entries(loops)) {
  test(`${name}: a caveman_retrieve the runtime refuses (404, 410, 503) answers the model {error} and the loop resolves`, async t => {
    if (!requirePeers(t, name.split(' ')[0])) return;
    for (const [status, code] of failures) {
      const f = runtimeFixture({ retrieveError: [status, code] }); t.after(() => f.runtime.close());
      let output = await loop(f.runtime);
      if (typeof output === 'string') output = JSON.parse(output);
      assert.deepEqual(output, { error: code }, `${status}`);
      assert.equal(f.retrievals.length, 1, `${status}: the handle reached the runtime`);
      assert.equal(f.retrievals[0].handle, handle);
    }
  });
}

test('each adapter warns once per refused-recovery code', t => {
  const ids = { 'ai-sdk': 'ai-sdk', openai: 'openai-sdk', anthropic: 'anthropic-sdk', google: 'google-sdk', langchain: 'langchain', strands: 'strands', mastra: 'mastra', mcp: 'mcp' };
  for (const [name, id] of Object.entries(ids)) if (requirePeers(t, name)) {
    for (const [, code] of failures) assert.equal(lines.filter(line => line.includes(`adapter=${id} reason=${code}`)).length, code === 'expired' ? 0 : 1, `${id} ${code}\n${lines.join('\n')}`);
  }
});

test('a caller abort during recovery still propagates instead of becoming {error}', async t => {
  const { recoveryResult } = await import('../dist/common.js'), { MiddlewareError } = await import('@caveman-ai/sdk/middleware');
  const controller = new AbortController(); controller.abort(new Error('caller aborted'));
  await assert.rejects(recoveryResult('test', controller.signal, { handle: 'h' }, async () => { throw new MiddlewareError('deadline'); }), { code: 'deadline' });
  await assert.rejects(recoveryResult('test', undefined, { handle: 'h' }, async () => { throw new TypeError('bug'); }), TypeError);
  assert.deepEqual(await recoveryResult('test', undefined, { handle: 'h' }, async () => { throw new MiddlewareError('not_found'); }), { error: 'not_found' });
});
