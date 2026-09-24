// One real framework call per adapter, through the entry point that compresses. `run(runtime, extra)` merges `extra`
// into the adapter options and returns the tool-result text the provider received plus whether the caller's own
// input survived unchanged. Imports are lazy so a missing optional framework only skips its adapter.
import { isDeepStrictEqual } from 'node:util';
import { original, scope } from './runtime-fixture.mjs';

const clone = value => structuredClone(value);
const finish = { unified: 'stop', raw: 'stop' }, usage = { inputTokens: { total: 1 }, outputTokens: { total: 1 } };

export const drivers = {
  'ai-sdk': { id: 'ai-sdk', async run(runtime, extra = {}) {
    const { generateText } = await import('ai'), { MockLanguageModelV4 } = await import('ai/test');
    const { withCaveman } = await import('../dist/ai-sdk.js');
    const messages = [{ role: 'user', content: 'go' },
      { role: 'assistant', content: [{ type: 'tool-call', toolCallId: 'read-1', toolName: 'read', input: {} }] },
      { role: 'tool', content: [{ type: 'tool-result', toolCallId: 'read-1', toolName: 'read', output: { type: 'text', value: original } }] }];
    const before = clone(messages);
    const model = new MockLanguageModelV4({ doGenerate: { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] } });
    await generateText({ ...withCaveman({ model }, { runtime, scope, ...extra }), messages, maxRetries: 0 });
    return { seen: model.doGenerateCalls[0].prompt.at(-1).content[0].output.value, intact: isDeepStrictEqual(messages, before) };
  } },
  openai: { id: 'openai-sdk', async run(runtime, extra = {}) {
    const { default: OpenAI } = await import('openai'), { withCavemanOpenAITools } = await import('../dist/openai.js');
    const seen = [];
    const fetch = async (_url, init) => { seen.push(JSON.parse(init.body)); return Response.json({ id: 'c', object: 'chat.completion', created: 1, model: 'm',
      choices: [{ index: 0, message: { role: 'assistant', content: 'done' }, finish_reason: 'stop' }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } }); };
    const bundle = withCavemanOpenAITools(new OpenAI({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime, scope, fetch, protocol: 'openai-chat',
      tools: [{ type: 'function', function: { name: 'read', description: 'Read', parameters: { type: 'object', properties: {} } } }], functions: { read: () => original }, ...extra });
    const messages = [{ role: 'user', content: 'go' },
      { role: 'assistant', content: null, tool_calls: [{ id: 'read-1', type: 'function', function: { name: 'read', arguments: '{}' } }] },
      { role: 'tool', tool_call_id: 'read-1', content: original }];
    const before = clone(messages);
    await bundle.client.chat.completions.create({ model: 'm', messages, tools: bundle.tools });
    return { seen: seen[0].messages.at(-1).content, intact: isDeepStrictEqual(messages, before) };
  } },
  anthropic: { id: 'anthropic-sdk', async run(runtime, extra = {}) {
    const { default: Anthropic } = await import('@anthropic-ai/sdk'), { withCavemanAnthropic } = await import('../dist/anthropic.js');
    const seen = [];
    const fetch = async (_url, init) => { seen.push(JSON.parse(init.body)); return Response.json({ id: 'msg_1', type: 'message', role: 'assistant', model: 'm',
      content: [{ type: 'text', text: 'done' }], stop_reason: 'end_turn', stop_sequence: null, usage: { input_tokens: 1, output_tokens: 1 } }); };
    const client = withCavemanAnthropic(new Anthropic({ apiKey: 'k', fetch, maxRetries: 0 }), { runtime, scope, fetch, ...extra });
    const messages = [{ role: 'user', content: 'go' }, { role: 'assistant', content: [{ type: 'tool_use', id: 'read-1', name: 'read', input: {} }] },
      { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'read-1', content: original }] }];
    const before = clone(messages);
    await client.beta.messages.toolRunner({ model: 'm', max_tokens: 10, messages,
      tools: [{ name: 'read', description: 'Read', input_schema: { type: 'object', properties: {} }, run: async () => original }] });
    return { seen: seen[0].messages.at(-1).content[0].content, intact: isDeepStrictEqual(messages, before) };
  } },
  google: { id: 'google-sdk', async run(runtime, extra = {}) {
    const { CavemanGoogleGenAI } = await import('../dist/google.js');
    const seen = [], native = globalThis.fetch;
    // The Google SDK has no fetch option; its ApiClient calls the global.
    globalThis.fetch = async (_url, init) => { seen.push(JSON.parse(init.body)); return Response.json({ candidates: [{ content: { role: 'model', parts: [{ text: 'done' }] },
      finishReason: 'STOP' }], usageMetadata: { promptTokenCount: 1, candidatesTokenCount: 1 } }); };
    try {
      const ai = new CavemanGoogleGenAI({ apiKey: 'k' }, { runtime, scope, ...extra });
      const contents = [{ role: 'user', parts: [{ text: 'go' }] }, { role: 'model', parts: [{ functionCall: { name: 'read', args: {} } }] },
        { role: 'user', parts: [{ functionResponse: { name: 'read', response: { output: original } } }] }];
      const before = clone(contents);
      const read = { tool: async () => ({ functionDeclarations: [{ name: 'read', description: 'Read', parametersJsonSchema: { type: 'object', properties: {} } }] }), callTool: async () => [] };
      await ai.models.generateContent({ model: 'gemini-x', contents, config: { tools: [read] } });
      return { seen: seen[0].contents.at(-1).parts[0].functionResponse.response.output, intact: isDeepStrictEqual(contents, before) };
    } finally { globalThis.fetch = native; }
  } },
  langchain: { id: 'langchain', async run(runtime, { configurable = { thread_id: 'thread-1' }, ...extra } = {}) {
    const { createAgent, FakeToolCallingModel } = await import('langchain');
    const { AIMessage, HumanMessage, ToolMessage } = await import('@langchain/core/messages'), { tool } = await import('@langchain/core/tools');
    const { withCavemanAgent, scopeFromConfig } = await import('../dist/langchain.js');
    const seen = [];
    class Capture extends FakeToolCallingModel { bindTools(tools) { this.tools = tools; return this; }
      async _generate(messages, options, run) { seen.push(messages); return super._generate(messages, options, run); } }
    const read = tool(async () => original, { name: 'read', description: 'Read', schema: { type: 'object', properties: {} } });
    const agent = createAgent(withCavemanAgent({ model: new Capture({ toolCalls: [] }), tools: [read] },
      { runtime, scope: config => scopeFromConfig(config, scope.namespace), ...extra }));
    const messages = [new HumanMessage('go'), new AIMessage({ content: '', tool_calls: [{ id: 'read-1', name: 'read', args: {} }] }),
      new ToolMessage({ content: original, tool_call_id: 'read-1', name: 'read' })];
    await agent.invoke({ messages }, { configurable });
    return { seen: seen[0].at(-1).content, intact: messages[2].content === original };
  } },
  strands: { id: 'strands', async run(runtime, extra = {}) {
    const { Agent, Model, Message, ToolUseBlock, ToolResultBlock, TextBlock, FunctionTool } = await import('@strands-agents/sdk');
    const { withCavemanStrands } = await import('../dist/strands.js');
    const seen = [];
    class Fake extends Model {
      config = {};
      updateConfig(config) { this.config = config; }
      getConfig() { return this.config; }
      async *stream(messages) {
        seen.push(messages);
        yield { type: 'modelMessageStartEvent', role: 'assistant' }; yield { type: 'modelContentBlockStartEvent' };
        yield { type: 'modelContentBlockDeltaEvent', delta: { type: 'textDelta', text: 'done' } }; yield { type: 'modelContentBlockStopEvent' };
        yield { type: 'modelMessageStopEvent', stopReason: 'endTurn' }; yield { type: 'modelMetadataEvent', usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } };
      }
    }
    const read = new FunctionTool({ name: 'read', description: 'Read', inputSchema: { type: 'object', properties: {} }, callback: async () => original });
    const result = new ToolResultBlock({ toolUseId: 'read-1', status: 'success', content: [new TextBlock(original)] });
    const messages = [new Message({ role: 'user', content: [new TextBlock('go')] }),
      new Message({ role: 'assistant', content: [new ToolUseBlock({ name: 'read', toolUseId: 'read-1', input: {} })] }), new Message({ role: 'user', content: [result] })];
    await new Agent(withCavemanStrands({ model: new Fake(), tools: [read], messages, printer: false }, { runtime, scope, ...extra })).invoke('continue');
    const sent = seen[0].find(message => message.content.some(block => block.type === 'toolResultBlock'));
    return { seen: sent.content[0].content[0].text, intact: result.content[0].text === original };
  } },
  mastra: { id: 'mastra', async run(runtime, extra = {}) {
    const { Agent } = await import('@mastra/core/agent'), { MockLanguageModelV4 } = await import('ai/test');
    const { withCavemanMastra } = await import('../dist/mastra.js');
    const model = new MockLanguageModelV4({ doGenerate: { content: [{ type: 'text', text: 'done' }], finishReason: finish, usage, warnings: [] } });
    const agent = withCavemanMastra(new Agent({ id: 'agent', name: 'agent', instructions: 'Be brief.', model }), { runtime, scope, ...extra });
    const messages = [{ role: 'user', content: 'go' },
      { role: 'assistant', content: [{ type: 'tool-call', toolCallId: 'read-1', toolName: 'read', input: {} }] },
      { role: 'tool', content: [{ type: 'tool-result', toolCallId: 'read-1', toolName: 'read', output: { type: 'text', value: original } }] }];
    const before = clone(messages);
    await agent.generate(messages);
    const sent = model.doGenerateCalls[0].prompt.findLast(message => message.role === 'tool');
    return { seen: sent?.content[0].output.value ?? null, intact: isDeepStrictEqual(messages, before) };
  } },
  mcp: { id: 'mcp', async run(runtime, extra = {}) {
    const { CavemanMCPHost } = await import('../dist/mcp.js');
    const host = new CavemanMCPHost({ runtime, scope, serverId: 'server', protocolVersion: '2025-11-25', ...extra });
    const tool = { name: 'read', description: 'Read', inputSchema: { type: 'object', properties: {} } };
    const native = { tool, execute: async () => ({ content: [{ type: 'text', text: original }] }) };
    const result = await native.execute({}), before = clone(result);
    const view = await host.projectResult(result, { tool, callId: 'read-1', contextManifest: [], registeredTools: host.register([native]) });
    return { seen: view.content[0].text, intact: isDeepStrictEqual(result, before) };
  } },
};
