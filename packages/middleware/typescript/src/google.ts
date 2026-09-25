import { AsyncLocalStorage } from 'node:async_hooks';
import { GoogleGenAI, Models, Chats, type GoogleGenAIOptions, type GenerateContentParameters, type GenerateContentConfig, type CallableTool, type FunctionCall, type HttpResponse, type Part, type Tool } from '@google/genai';
import { MiddlewareRuntime, sha256, type RecoveryBinding, type Scope, type Usage } from '@caveman-ai/sdk/middleware';
import { currentOwner, manifest, observe, plain, withOwner, type Attempt } from './common.js';
import { parseWire, patchWire, pathKey, type StringLeaf } from './wire.js';
import { adapterCompatible, frameworkVersion } from './compatibility.js';

export interface GoogleOptions { runtime: MiddlewareRuntime; scope: Scope }
type NativeClient = ConstructorParameters<typeof Models>[0];
type NativeRequest = Parameters<NativeClient['request']>[0];
interface Invocation { binding: RecoveryBinding | null; overhead?: string; logicalCallId: string }
const invocations = new AsyncLocalStorage<Invocation>();

function permitsRecovery(config?: GenerateContentConfig): boolean {
  const mode = config?.toolConfig?.functionCallingConfig?.mode;
  return !config?.cachedContent && !config?.responseSchema && !config?.responseJsonSchema &&
    (!config?.responseMimeType || config.responseMimeType === 'text/plain') && (!mode || mode === 'AUTO');
}

function callable(value: Tool | CallableTool): value is CallableTool {
  return 'callTool' in value && typeof value.callTool === 'function' && 'tool' in value && typeof value.tool === 'function';
}

function schema(binding: RecoveryBinding): Tool {
  return { functionDeclarations: [{ name: binding.name, description: binding.description, parametersJsonSchema: binding.inputSchema }] };
}

/** The SDK, including its AFC scheduler, invokes this real CallableTool. */
function recoveryTool(binding: RecoveryBinding): CallableTool {
  return {
    async tool() { return schema(binding); },
    async callTool(calls: FunctionCall[]): Promise<Part[]> {
      const parts: Part[] = [];
      for (const call of calls) if (call.name === binding.name) {
        const args = call.args;
        const output = args && typeof args.handle === 'string' ? await binding.execute({ ...args, handle: args.handle }) : { error: 'invalid_recovery_arguments' };
        parts.push({ functionResponse: { name: binding.name, ...(call.id ? { id: call.id } : {}), response: { output } } });
      }
      return parts;
    },
  };
}

function measured(n: unknown): number | null { return typeof n === 'number' && Number.isSafeInteger(n) && n >= 0 ? n : null; }
function usage(value: unknown): Usage | null {
  if (!plain(value) || !plain(value.usageMetadata)) return null;
  const u = value.usageMetadata, input = measured(u.promptTokenCount), output = measured(u.candidatesTokenCount);
  return { provenance: 'client_observed_sdk', complete: input !== null && output !== null, input_tokens: input, output_tokens: output,
    cache_read_tokens: measured(u.cachedContentTokenCount), cache_write_tokens: null, reasoning_tokens: measured(u.thoughtsTokenCount) };
}

function acceptsRecovery(body: Record<string, unknown>, binding: RecoveryBinding): boolean {
  if (!Array.isArray(body.tools) || body.cachedContent) return false;
  const generation = plain(body.generationConfig) ? body.generationConfig : {};
  const choice = plain(body.toolConfig) && plain(body.toolConfig.functionCallingConfig) ? body.toolConfig.functionCallingConfig : {};
  if (generation.responseSchema || generation.responseJsonSchema || (generation.responseMimeType && generation.responseMimeType !== 'text/plain') || (choice.mode && choice.mode !== 'AUTO')) return false;
  const matches = body.tools.flatMap(t => plain(t) && Array.isArray(t.functionDeclarations) ? t.functionDeclarations : []).filter(t => plain(t) && t.name === binding.name);
  return matches.length === 1 && matches[0].description === binding.description && JSON.stringify(matches[0].parametersJsonSchema) === JSON.stringify(binding.inputSchema);
}

function selected(body: Record<string, unknown>, strings: Map<string, StringLeaf>): StringLeaf[] {
  const out: StringLeaf[] = [], names = new Set<string>();
  if (!Array.isArray(body.contents)) return out;
  body.contents.forEach((content, ci) => {
    if (!plain(content) || !Array.isArray(content.parts)) return;
    for (const part of content.parts) if (plain(part) && plain(part.functionCall) && typeof part.functionCall.name === 'string') names.add(part.functionCall.name);
    content.parts.forEach((part, pi) => {
      if (!plain(part) || part.thought || part.thoughtSignature || !plain(part.functionResponse)) return;
      const result = part.functionResponse;
      if (typeof result.name !== 'string' || !names.has(result.name) || result.name === 'caveman_retrieve' || !plain(result.response) || 'error' in result.response) return;
      // Native callable tools commonly return output (TS) or result (Python).
      // Structured records and media remain records, never flattened to text.
      for (const key of ['output', 'result']) if (typeof result.response[key] === 'string') {
        const leaf = strings.get(pathKey(['contents', ci, 'parts', pi, 'functionResponse', 'response', key]));
        if (leaf) out.push(leaf);
      }
    });
  });
  return out;
}

async function prepare(request: NativeRequest, options: GoogleOptions, defaults?: GoogleGenAIOptions['httpOptions'], passiveReason?: string): Promise<[NativeRequest, Attempt | null]> {
  if (currentOwner() || request.httpMethod !== 'POST' || !/:(?:generateContent|streamGenerateContent)(?:\?|$)/.test(request.path)) return [request, null];
  const context = invocations.getStore();
  const attempt: Attempt = { runtime: options.runtime, scope: options.scope, logicalCallId: context?.logicalCallId ?? crypto.randomUUID(), attemptId: crypto.randomUUID(), optimization: null, wireSHA256: null, adapter: 'google-sdk', reason: 'opaque_payload' };
  if (options.runtime.mode === 'off' || passiveReason) {
    attempt.passive = true;
    attempt.reason = options.runtime.mode === 'off' ? 'disabled' : passiveReason ?? 'unsupported_version';
    return [request, attempt];
  }
  request.abortSignal?.throwIfAborted();
  let outgoing = request;
  const headers = new Headers({ ...defaults?.headers, ...request.httpOptions?.headers });
  // Extra bodies are merged later by the native client. Do not optimize a view
  // whose contents/tool contract could be replaced after this public seam.
  const protectedBody = defaults?.extraBody || request.httpOptions?.extraBody || ['content-encoding', 'digest', 'content-digest', 'content-md5', 'signature', 'signature-input', 'x-amz-content-sha256', 'dpop'].some(name => headers.has(name));
  if (typeof request.body === 'string' && !protectedBody) {
    const wire = parseWire(request.body);
    if (wire && plain(wire.value) && wire.value.cachedContent) attempt.reason = 'opaque_history_reference';
    if (wire && plain(wire.value) && Array.isArray(wire.value.contents) && !wire.value.cachedContent) {
      const { contents, ...envelope } = wire.value;
      const leaves = selected(wire.value, wire.strings), history = await manifest([envelope, ...contents]);
      if (history) {
        const binding = context?.binding && options.runtime.ownsBinding(context.binding, options.scope) && acceptsRecovery(wire.value, context.binding) ? context.binding : null;
        const optimization = await options.runtime.optimize({ scope: options.scope, adapter: { id: 'google-sdk', version: '0.1.0', framework_version: frameworkVersion('@google/genai') ?? 'unknown', serialization_revision: 'google-genai-native-wire-v1' },
          model: { provider: 'google', id: request.path.replace(/:(?:generateContent|streamGenerateContent).*$/, ''), protocol: 'google-genai' }, manifest: history,
          candidates: leaves.map((leaf, i) => ({ id: `leaf-${i}`, sourceId: leaf.path.join('/'), content: leaf.value })), binding,
          ...(binding ? { recoveryOverheadText: context?.overhead ?? JSON.stringify(schema(binding)) } : {}), logicalCallId: attempt.logicalCallId, attemptId: attempt.attemptId,
          ...(request.abortSignal ? { signal: request.abortSignal } : {}) });
        if (options.runtime.mode !== 'compress' && options.runtime.mode !== 'record' ||
          (binding && (!options.runtime.ownsBinding(binding, options.scope) || !acceptsRecovery(wire.value, binding)))) {
          attempt.reason = 'recovery_unavailable';
          return [request, attempt];
        }
        attempt.optimization = optimization.replacements.length ? null : optimization;
        attempt.reason = optimization.replacements.length ? 'invalid_plan' : optimization.reason;
        const patches = optimization.replacements.map(r => ({ leaf: leaves[Number(r.segment_id.slice(5))]!, replacement: r.text }));
        if (patches.length && patches.every(p => p.leaf)) {
          const body = patchWire(request.body, patches);
          if (body !== null) {
            outgoing = { ...request, body };
            attempt.optimization = optimization;
            if (headers.has('content-length')) {
              const updatedHeaders = { ...request.httpOptions?.headers };
              for (const key of Object.keys({ ...defaults?.headers, ...request.httpOptions?.headers })) if (key.toLowerCase() === 'content-length') updatedHeaders[key] = String(new TextEncoder().encode(body).length);
              outgoing.httpOptions = { ...request.httpOptions, headers: updatedHeaders };
            }
          }
        }
      }
    }
  }
  // This is the pre-auth native serialized body, not proof of a host retry's
  // final bytes: the SDK alone owns retries and any late extra-body merge.
  if (typeof outgoing.body === 'string' && !protectedBody) attempt.wireSHA256 = await sha256(outgoing.body);
  request.abortSignal?.throwIfAborted();
  return [outgoing, attempt];
}

function observedJSON(response: HttpResponse, onValue: (value: unknown) => void, onError: () => void): HttpResponse {
  return new Proxy(response, { get(target, property) {
    if (property === 'json') return async () => { try { const value = await target.json(); onValue(value); return value; } catch (error) { onError(); throw error; } };
    const value = Reflect.get(target, property, target);
    return typeof value === 'function' ? value.bind(target) : value;
  } });
}

function delegatedClient(client: NativeClient, options: GoogleOptions, defaults?: GoogleGenAIOptions['httpOptions'], passiveReason?: string): NativeClient {
  return new Proxy(client, { get(target, property) {
    if (property === 'request') return async (request: NativeRequest) => {
      const [outgoing, attempt] = await prepare(request, options, defaults, passiveReason);
      if (!attempt) return target.request(outgoing);
      observe(attempt, 'dispatch_intent');
      try {
        const response = await withOwner(attempt, () => target.request(outgoing));
        return observedJSON(response, value => observe(attempt, 'completed', usage(value)), () => observe(attempt, 'failed'));
      } catch (error) { observe(attempt, request.abortSignal?.aborted ? 'cancelled' : 'failed'); throw error; }
    };
    if (property === 'requestStream') return async (request: NativeRequest) => {
      const [outgoing, attempt] = await prepare(request, options, defaults, passiveReason);
      if (!attempt) return target.requestStream(outgoing);
      observe(attempt, 'dispatch_intent');
      const closeController = new AbortController();
      const signal = request.abortSignal ? AbortSignal.any([request.abortSignal, closeController.signal]) : closeController.signal;
      let stream: AsyncGenerator<HttpResponse>;
      try { stream = await withOwner(attempt, () => target.requestStream({ ...outgoing, abortSignal: signal })); }
      catch (error) { observe(attempt, request.abortSignal?.aborted ? 'cancelled' : 'failed'); throw error; }
      let last: Usage | null = null, ended = false, terminal = false;
      const finish = (event: 'completed' | 'failed' | 'cancelled') => { if (!ended) { ended = true; observe(attempt, event, event === 'completed' && last ? { ...last, complete: last.complete && terminal } : null); } };
      return {
        [Symbol.asyncIterator]() { return this; },
        async next(...args: [] | [unknown]) {
          try { const next = await withOwner(attempt, () => stream.next(...args));
            if (next.done) { finish('completed'); return next; }
            return { done: false as const, value: observedJSON(next.value, value => {
              last = usage(value) ?? last;
              if (plain(value) && Array.isArray(value.candidates) && value.candidates.some(c => plain(c) && c.finishReason)) terminal = true;
            }, () => finish('failed')) };
          } catch (error) { finish(request.abortSignal?.aborted ? 'cancelled' : 'failed'); throw error; }
        },
        async return(value?: unknown) { finish('cancelled'); closeController.abort(); return stream.return(value); },
        async throw(error?: unknown) { finish('failed'); closeController.abort(); return stream.throw(error); },
      };
    };
    const value = Reflect.get(target, property, target);
    return typeof value === 'function' ? value.bind(target) : value;
  } });
}

async function invocation(params: GenerateContentParameters, options: GoogleOptions): Promise<{ params: GenerateContentParameters; context: Invocation }> {
  // Native AFC appends to contents; isolate that list from the caller's history.
  const copy: GenerateContentParameters = { ...params, ...(params.config ? { config: { ...params.config } } : {}) };
  if (Array.isArray(params.contents)) copy.contents = params.contents.slice() as typeof params.contents;
  const context: Invocation = { binding: null, logicalCallId: crypto.randomUUID() };
  const tools = params.config?.tools, maximum = params.config?.automaticFunctionCalling?.maximumRemoteCalls;
  if (options.runtime.mode !== 'compress' || currentOwner() || !tools?.some(callable) || params.config?.automaticFunctionCalling?.disable || (maximum !== undefined && (!Number.isInteger(maximum) || maximum <= 0)) || !permitsRecovery(params.config)) return { params: copy, context };
  // Use each callable's actual declaration to reject name collisions. An
  // incompatible mixed native tool list retains the SDK's own error behavior.
  const declarations = await Promise.all(tools.map(t => callable(t) ? t.tool() : t));
  if (declarations.some(t => t.functionDeclarations?.some(f => f.name === 'caveman_retrieve')) || tools.some(t => !callable(t) && t.functionDeclarations?.length)) return { params: copy, context };
  const binding = options.runtime.recovery(options.scope);
  context.binding = binding; context.overhead = JSON.stringify(schema(binding));
  copy.config = { ...copy.config, tools: [...tools, recoveryTool(binding)] };
  return { params: copy, context };
}

/**
 * Native Google client with the same GoogleGenAIOptions and native Models/Chats.
 * Uses the SDK's protected ApiClient and public module constructors; it never
 * mutates a private SDK field or replaces the native automatic tool loop.
 * Already constructed Google clients have no public clone/transport injection:
 * reuse their original options here. Other modules retain the original client.
 */
export class CavemanGoogleGenAI extends GoogleGenAI {
  declare readonly models: Models;
  declare readonly chats: Chats;
  constructor(nativeOptions: GoogleGenAIOptions, options: GoogleOptions) {
    super(nativeOptions);
    const supported = options.runtime.mode !== 'off' && adapterCompatible('google');
    if (!supported && options.runtime.mode !== 'off') options.runtime.decline('unsupported_version');
    this.models = new Models(delegatedClient(this.apiClient, options, nativeOptions.httpOptions, supported ? undefined : options.runtime.mode === 'off' ? 'disabled' : 'unsupported_version'));
    this.chats = new Chats(this.models, this.apiClient);
    if (!supported) return;
    const generate = this.models.generateContent.bind(this.models), stream = this.models.generateContentStream.bind(this.models);
    this.models.generateContent = async params => { const call = await invocation(params, options); return invocations.run(call.context, () => generate(call.params)); };
    this.models.generateContentStream = async params => {
      const call = await invocation(params, options), result = await invocations.run(call.context, () => stream(call.params));
      const wrapped: Awaited<ReturnType<Models['generateContentStream']>> = { [Symbol.asyncIterator]() { return this; },
        next: (...args) => invocations.run(call.context, () => result.next(...args)),
        return: value => invocations.run(call.context, () => result.return(value)),
        throw: error => invocations.run(call.context, () => result.throw(error)),
      };
      return wrapped;
    };
  }
}
