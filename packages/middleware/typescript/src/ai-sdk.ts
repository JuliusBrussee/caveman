import { jsonSchema, tool, wrapLanguageModel, type GenerateTextOnStepStartCallback, type GenerateTextOnStepEndCallback,
  type OnLanguageModelCallStartCallback, type ToolSet } from 'ai';
import { isDeepStrictEqual } from 'node:util';
import type { LanguageModelV4, LanguageModelV4CallOptions, LanguageModelV4Middleware, LanguageModelV4Usage } from '@ai-sdk/provider';
import { MiddlewareRuntime, type Candidate, type RecoveryBinding, type RetrieveArgs, type Scope, type Usage } from '@caveman-ai/sdk/middleware';
import { currentOwner, manifest, observe, observeStream, plain, withOwner, type Attempt } from './common.js';
import { adapterCompatible, frameworkVersion } from './compatibility.js';

export interface CavemanOptions {
  runtime: MiddlewareRuntime;
  scope: Scope;
  logicalCallId?: string;
}
interface RecoveryRegistration {
  binding: RecoveryBinding;
  tool: ToolSet[string];
  schema: ReturnType<typeof jsonSchema<RetrieveArgs>>;
  expectedSchema: unknown;
  /** Finalized native definitions are correlated with the current executor registry. */
  calls: WeakMap<readonly unknown[], ToolSet>;
}

const adapter = { id: 'ai-sdk', version: '0.1.0', framework_version: frameworkVersion('ai') ?? 'unknown', serialization_revision: 'ai-sdk-v4.1' };
const supported = (runtime: MiddlewareRuntime) => {
  const yes = adapterCompatible('ai-sdk');
  if (!yes && runtime.mode !== 'off') runtime.decline('unsupported_version');
  return yes;
};
const measured = (n: number | undefined): number | null => Number.isSafeInteger(n) && n! >= 0 ? n! : null;
function usage(value: LanguageModelV4Usage): Usage {
  const input = measured(value.inputTokens.total), output = measured(value.outputTokens.total);
  return { provenance: 'client_observed_sdk', complete: input !== null && output !== null, input_tokens: input, output_tokens: output,
    cache_read_tokens: measured(value.inputTokens.cacheRead), cache_write_tokens: measured(value.inputTokens.cacheWrite), reasoning_tokens: measured(value.outputTokens.reasoning) };
}

const onlyKeys = (value: Record<string, unknown>, keys: readonly string[]) => Object.keys(value).every(key => keys.includes(key));
function freezeSchema(value: unknown): void {
  if (value && typeof value === 'object') {
    for (const child of Object.values(value)) freezeSchema(child);
    Object.freeze(value);
  }
}

function registeredRecovery(params: LanguageModelV4CallOptions, options: CavemanOptions, registration?: RecoveryRegistration): RecoveryBinding | null {
  if (!registration || !params.tools || (params.toolChoice && params.toolChoice.type !== 'auto') ||
      (params.responseFormat && params.responseFormat.type !== 'text')) return null;
  const { binding, tool: registeredTool, schema, expectedSchema } = registration;
  if (!options.runtime.ownsBinding(binding, options.scope)) return null;
  const registry = registration.calls.get(params.tools);
  if (!registry) return null;
  const actual = registry[binding.name];
  const offered = params.tools.filter(entry => entry.name === binding.name);
  const native = offered[0];
  // Each public callback attests one concrete provider-call array. Recheck the
  // registry and schema at dispatch in case a later native callback mutated it.
  if (actual !== registeredTool || !plain(actual) || !onlyKeys(actual, ['description', 'inputSchema', 'execute']) ||
      actual.execute !== binding.execute || actual.inputSchema !== schema || actual.description !== binding.description ||
      schema.validate !== undefined || !isDeepStrictEqual(schema.jsonSchema, expectedSchema) ||
      Object.values(registry).filter(entry => entry?.execute === binding.execute).length !== 1 ||
      offered.length !== 1 || native?.type !== 'function' || !plain(native) ||
      !onlyKeys(native, ['type', 'name', 'description', 'inputSchema']) ||
      native.description !== binding.description || !isDeepStrictEqual(native.inputSchema, expectedSchema) ||
      new Set(params.tools.map(entry => entry.name)).size !== params.tools.length) return null;
  return binding;
}

/** Public native middleware. No executable registry can be attested here. */
export function createCavemanMiddleware(options: CavemanOptions): LanguageModelV4Middleware {
  return nativeMiddleware(options);
}

function nativeMiddleware(options: CavemanOptions, registration?: RecoveryRegistration): LanguageModelV4Middleware {
  const versionSupported = supported(options.runtime);
  const prepared = new WeakMap<LanguageModelV4CallOptions, Attempt>();
  const logicalCallId = options.logicalCallId ?? crypto.randomUUID();
  return {
    specificationVersion: 'v4',
    async transformParams({ params, model }) {
      if (params.abortSignal?.aborted && !currentOwner()) options.runtime.report(null, { reason: 'cancelled', adapter: adapter.id });
      params.abortSignal?.throwIfAborted();
      if (currentOwner()) return params;
      const attempt: Attempt = { runtime: options.runtime, scope: options.scope, logicalCallId, attemptId: crypto.randomUUID(), optimization: null, wireSHA256: null,
        adapter: adapter.id, reason: !versionSupported ? 'unsupported_version' : 'unsupported_shape',
        passive: !versionSupported || options.runtime.mode === 'off' || model.specificationVersion !== 'v4' };
      const next = { ...params };
      prepared.set(next, attempt);
      if (attempt.passive) return next;
      const context = await manifest(params.prompt);
      if (!context) return next;
      const candidates: Candidate[] = [];
      const setters = new Map<string, (text: string) => void>();
      const prompt = params.prompt.slice();
      const calls = new Map<string, { name: string; message: number } | null>();
      const results = new Map<string, number>();
      for (let mi = 0; mi < params.prompt.length; mi++) {
        const message = params.prompt[mi];
        if (!plain(message) || !Array.isArray(message.content)) continue;
        for (const part of message.content) {
          if (!plain(part) || typeof part.toolCallId !== 'string' || !part.toolCallId) continue;
          if (message.role === 'assistant' && part.type === 'tool-call') {
            const safe = typeof part.toolName === 'string' && !!part.toolName && 'input' in part &&
              (part.providerExecuted === undefined || part.providerExecuted === false) &&
              onlyKeys(part, ['type', 'toolCallId', 'toolName', 'input', 'providerExecuted', 'providerOptions']);
            calls.set(part.toolCallId, calls.has(part.toolCallId) || !safe ? null : { name: part.toolName as string, message: mi });
          } else if (message.role === 'tool' && part.type === 'tool-result') {
            results.set(part.toolCallId, (results.get(part.toolCallId) ?? 0) + 1);
          }
        }
      }
      for (let mi = 0; mi < params.prompt.length; mi++) {
        const message = params.prompt[mi];
        if (!plain(message) || message.role !== 'tool' || !Array.isArray(message.content) ||
            !onlyKeys(message, ['role', 'content', 'providerOptions'])) continue;
        const content = message.content.slice();
        let changed = false;
        for (let pi = 0; pi < message.content.length; pi++) {
          const part = message.content[pi];
          if (!plain(part) || part.type !== 'tool-result' || part.toolName === 'caveman_retrieve' || !plain(part.output)) continue;
          if (typeof part.toolCallId !== 'string' || !onlyKeys(part, ['type', 'toolCallId', 'toolName', 'output', 'providerOptions'])) continue;
          const call = calls.get(part.toolCallId);
          if (!call || call.message >= mi || call.name !== part.toolName || results.get(part.toolCallId) !== 1) continue;
          const output = part.output;
          // JSON string results stay JSON string results. Structured records,
          // errors, multimodal values and citations remain untouched.
          if ((output.type !== 'text' && output.type !== 'json') || typeof output.value !== 'string') continue;
          if (!onlyKeys(output, ['type', 'value', 'providerOptions'])) continue;
          const id = `message-${mi}.part-${pi}`;
          candidates.push({ id, sourceId: id, content: output.value, kind: 'tool_result' });
          setters.set(id, text => {
            content[pi] = { ...part, output: { ...output, value: text } };
            if (!changed) { prompt[mi] = { ...message, content }; changed = true; }
          });
        }
      }
      const nativeRecovery = params.tools?.find(t => t.type === 'function' && t.name === 'caveman_retrieve');
      let binding: RecoveryBinding | null = null;
      try { binding = registeredRecovery(params, options, registration); } catch { /* unknown registry/schema stays recovery-free */ }
      const optimization = await options.runtime.optimize({ scope: options.scope, adapter, candidates, manifest: context,
        model: { provider: model.provider, id: model.modelId, protocol: 'ai-sdk-v4' },
        binding, logicalCallId, attemptId: attempt.attemptId,
        ...(nativeRecovery ? { recoveryOverheadText: JSON.stringify(nativeRecovery) } : {}),
        ...(params.abortSignal ? { signal: params.abortSignal } : {}),
      });
      attempt.optimization = optimization.replacements.length === 0 ? optimization : null;
      if (optimization.replacements.length) attempt.reason = 'patch_not_applied';
      let stillRegistered = binding === null;
      try { stillRegistered = !binding || registeredRecovery(params, options, registration) === binding; } catch { /* native definitions changed during optimize */ }
      // Runtime validates all leaves first; local mappings are then checked as a
      // set before publishing the copy. Caller history/checkpoints keep originals.
      if (stillRegistered && optimization.replacements.every(r => setters.has(r.segment_id))) {
        attempt.optimization = optimization;
        for (const replacement of optimization.replacements) setters.get(replacement.segment_id)!(replacement.text);
        if (optimization.replacements.length) next.prompt = prompt;
      }
      return next;
    },
    async wrapGenerate({ params, doGenerate }) {
      const attempt = prepared.get(params);
      if (!attempt) return doGenerate();
      prepared.delete(params);
      params.abortSignal?.throwIfAborted();
      observe(attempt, 'dispatch_intent');
      try {
        const result = await withOwner(attempt, doGenerate);
        observe(attempt, 'completed', usage(result.usage));
        return result;
      } catch (error) { observe(attempt, params.abortSignal?.aborted ? 'cancelled' : 'failed'); throw error; }
    },
    async wrapStream({ params, doStream }) {
      const attempt = prepared.get(params);
      if (!attempt) return doStream();
      prepared.delete(params);
      params.abortSignal?.throwIfAborted();
      observe(attempt, 'dispatch_intent');
      try {
        const result = await withOwner(attempt, doStream);
        return { ...result, stream: observeStream(result.stream, attempt, event => event.type === 'finish' ? usage(event.usage) : null, params.abortSignal) };
      } catch (error) { observe(attempt, params.abortSignal?.aborted ? 'cancelled' : 'failed'); throw error; }
    },
  };
}

interface NativeHooks {
  onStepStart?: GenerateTextOnStepStartCallback;
  experimental_onStepStart?: GenerateTextOnStepStartCallback;
  onLanguageModelCallStart?: OnLanguageModelCallStartCallback;
  experimental_onLanguageModelCallStart?: OnLanguageModelCallStartCallback;
  onStepEnd?: GenerateTextOnStepEndCallback;
  onStepFinish?: GenerateTextOnStepEndCallback;
}

/** Complete native model/tool/callback bundle. Pass it to the native generation
 * or ToolLoopAgent API. Replacing its tools table or removing its callbacks
 * leaves model calls recovery-free; application-owned source tools stay native. */
export function withCaveman<T extends { model: LanguageModelV4; tools?: ToolSet }>(input: T, options: CavemanOptions): T {
  if (options.runtime.mode === 'off' || !supported(options.runtime)) {
    return { ...input, model: wrapLanguageModel({ model: input.model, middleware: nativeMiddleware(options) }) } as T;
  }
  if (input.tools?.['caveman_retrieve'] || options.runtime.mode === 'record') {
    return { ...input, model: wrapLanguageModel({ model: input.model, middleware: createCavemanMiddleware(options) }) };
  }
  const binding = options.runtime.recovery(options.scope);
  // Keep the runtime's schema separate from native framework annotations.
  const expectedSchema = structuredClone(binding.inputSchema);
  const nativeSchema = structuredClone(expectedSchema);
  freezeSchema(nativeSchema);
  const schema = Object.freeze(jsonSchema<RetrieveArgs>(nativeSchema));
  const recovery = Object.freeze(tool({ description: binding.description, inputSchema: schema, execute: binding.execute }));
  const tools = Object.freeze({ ...input.tools, caveman_retrieve: recovery });
  const registration: RecoveryRegistration = { binding, tool: recovery, schema, expectedSchema, calls: new WeakMap() };
  const middleware = nativeMiddleware(options, registration);
  const hooks = input as T & NativeHooks;
  const steps = new Map<string, ToolSet>();
  const onStepStart: GenerateTextOnStepStartCallback = async event => {
    steps.delete(event.callId);
    await (hooks.onStepStart ?? hooks.experimental_onStepStart)?.(event);
    if (event.tools !== tools) return;
    // Native failures can omit step-end callbacks. Bound abandoned correlation
    // entries; eviction safely disables recovery for an unusually old request.
    if (steps.size >= 128) steps.delete(steps.keys().next().value!);
    steps.set(event.callId, event.tools);
  };
  const onLanguageModelCallStart: OnLanguageModelCallStartCallback = async event => {
    if (event.tools) registration.calls.delete(event.tools);
    await (hooks.onLanguageModelCallStart ?? hooks.experimental_onLanguageModelCallStart)?.(event);
    const registry = steps.get(event.callId);
    if (event.tools && registry) registration.calls.set(event.tools, registry);
  };
  const onStepEnd: GenerateTextOnStepEndCallback = async event => {
    steps.delete(event.callId);
    await (hooks.onStepEnd ?? hooks.onStepFinish)?.(event);
  };
  return Object.freeze({ ...input, model: wrapLanguageModel({ model: input.model, middleware }), tools,
    onStepStart, onLanguageModelCallStart, onStepEnd }) as T;
}
