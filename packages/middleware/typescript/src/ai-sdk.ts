import { jsonSchema, tool, wrapLanguageModel, type GenerateTextOnStepStartCallback, type GenerateTextOnStepEndCallback,
  type OnLanguageModelCallStartCallback, type ToolSet } from 'ai';
import { isDeepStrictEqual } from 'node:util';
import type { LanguageModelV4, LanguageModelV4CallOptions, LanguageModelV4Middleware, LanguageModelV4Usage } from '@ai-sdk/provider';
import { MiddlewareRuntime, normalizeScope, recoveryInputSchema, recoveryToolDescription, type Candidate, type RecoveryBinding, type RetrieveArgs,
  type Scope, type Usage } from '@caveman-ai/sdk/middleware';
import { MIDDLEWARE_VERSION, bindRecovery, currentOwner, hintRecovery, manifest, nameConflict, observe, observeStream, passiveAttempt, plain, recoveryResult,
  resolveScope, withOwner, type Attempt, type BudgetOptions, type ScopeSource } from './common.js';
import { frameworkGate, frameworkVersion, type GateOptions, type GateReason } from './compatibility.js';
import { guard, guardSync } from './guard.js';

export interface CavemanOptions extends GateOptions, BudgetOptions {
  runtime: MiddlewareRuntime;
  scope: ScopeSource;
  logicalCallId?: string;
}
interface RecoveryRegistration {
  /** The executor registered in the native tool table; it resolves the scope when the model calls it. */
  execute: (args: RetrieveArgs, call?: { signal?: AbortSignal }) => Promise<unknown>;
  tool: ToolSet[string];
  schema: ReturnType<typeof jsonSchema<RetrieveArgs>>;
  expectedSchema: unknown;
  /** Finalized native definitions are correlated with the current executor registry. */
  calls: WeakMap<readonly unknown[], ToolSet>;
}

const adapter = { id: 'ai-sdk', version: MIDDLEWARE_VERSION, framework_version: frameworkVersion('ai') ?? 'unknown', serialization_revision: 'ai-sdk-v4.1' };
type Blocked = GateReason | 'recovery_name_conflict' | null;
const gate = (options: CavemanOptions): Blocked =>
  frameworkGate('ai-sdk', options, () => typeof wrapLanguageModel === 'function' && typeof tool === 'function' && typeof jsonSchema === 'function');
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

/** Each public callback attests one concrete provider-call array. Recheck the registry and schema at dispatch in case
 * a later native callback mutated it. */
function attested(params: LanguageModelV4CallOptions, registration?: RecoveryRegistration): boolean {
  if (!registration || !params.tools || (params.toolChoice && params.toolChoice.type !== 'auto') ||
      (params.responseFormat && params.responseFormat.type !== 'text')) return false;
  const { execute, tool: registeredTool, schema, expectedSchema } = registration;
  const registry = registration.calls.get(params.tools);
  if (!registry) return false;
  const actual = registry['caveman_retrieve'];
  const offered = params.tools.filter(entry => entry.name === 'caveman_retrieve');
  const native = offered[0];
  return !(actual !== registeredTool || !plain(actual) || !onlyKeys(actual, ['description', 'inputSchema', 'execute']) ||
      actual.execute !== execute || actual.inputSchema !== schema || actual.description !== recoveryToolDescription ||
      schema.validate !== undefined || !isDeepStrictEqual(schema.jsonSchema, expectedSchema) ||
      Object.values(registry).filter(entry => entry?.execute === execute).length !== 1 ||
      offered.length !== 1 || native?.type !== 'function' || !plain(native) ||
      !onlyKeys(native, ['type', 'name', 'description', 'inputSchema']) ||
      native.description !== recoveryToolDescription || !isDeepStrictEqual(native.inputSchema, expectedSchema) ||
      new Set(params.tools.map(entry => entry.name)).size !== params.tools.length);
}

/** Public native middleware. It cannot attest an executable recovery registry, so compress mode reports
 * `recovery_unbound`; withCaveman is the entry point that compresses. */
export function createCavemanMiddleware(options: CavemanOptions): LanguageModelV4Middleware {
  const blocked = gate(options);
  if (!blocked) hintRecovery(options.runtime, adapter.id, 'createCavemanMiddleware', 'withCaveman');
  return nativeMiddleware(options, blocked);
}

function nativeMiddleware(options: CavemanOptions, blocked: Blocked, registration?: RecoveryRegistration): LanguageModelV4Middleware {
  const { runtime } = options;
  const prepared = new WeakMap<LanguageModelV4CallOptions, Attempt>();
  // TS-7: a native retry re-sends the same prompt array in a fresh params object. It keeps the first attempt's logical
  // call id and reuses its prepared copy instead of optimizing again.
  const retries = new WeakMap<LanguageModelV4CallOptions['prompt'], { logicalCallId: string; tools?: unknown; prompt?: LanguageModelV4CallOptions['prompt']; attempt?: Attempt }>();
  return {
    specificationVersion: 'v4',
    async transformParams({ params, model }) {
      params.abortSignal?.throwIfAborted();
      if (currentOwner()) return params;
      // One logical call per request (C14); native retries of that request share it.
      const retry = retries.get(params.prompt), logicalCallId = options.logicalCallId ?? retry?.logicalCallId ?? crypto.randomUUID();
      if (!retry) retries.set(params.prompt, { logicalCallId });
      const scope = runtime.mode === 'off' || blocked ? null : resolveScope(options.scope, undefined);
      const reason = runtime.mode === 'off' ? 'disabled' : blocked ?? (!scope ? 'recovery_unbound' : model.specificationVersion !== 'v4' ? 'unsupported_shape' : null);
      const attempt: Attempt = reason ? passiveAttempt(runtime, adapter.id, reason, logicalCallId)
        : { runtime, scope: scope!, logicalCallId, attemptId: crypto.randomUUID(), optimization: null, wireSHA256: null, adapter: adapter.id, reason: 'unsupported_shape' };
      const next = { ...params };
      prepared.set(next, attempt);
      if (attempt.passive) return next;
      // A lossy copy is reused only while its recovery tool is still attested.
      if (retry?.attempt && retry.tools === params.tools && (retry.prompt === params.prompt || safeAttested(params, registration))) {
        Object.assign(attempt, { optimization: retry.attempt.optimization, reason: retry.attempt.reason, passive: retry.attempt.passive });
        next.prompt = retry.prompt!;
        return next;
      }
      await guard(runtime, adapter.id, params.abortSignal, () => project(params, next, model, attempt, options, registration),
        () => { Object.assign(attempt, { passive: true, reason: 'adapter_error', optimization: null }); next.prompt = params.prompt; return next; });
      retries.set(params.prompt, { logicalCallId, tools: params.tools, prompt: next.prompt, attempt });
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

const safeAttested = (params: LanguageModelV4CallOptions, registration?: RecoveryRegistration): boolean => {
  try { return attested(params, registration); } catch { return false; }
};

async function project(params: LanguageModelV4CallOptions, next: LanguageModelV4CallOptions, model: LanguageModelV4, attempt: Attempt,
  options: CavemanOptions, registration?: RecoveryRegistration): Promise<LanguageModelV4CallOptions> {
  const context = await manifest(params.prompt, options.manifestBytes);
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
  // An unknown registry/schema stays recovery-free.
  const binding: RecoveryBinding | null = safeAttested(params, registration) ? bindRecovery(options.runtime, attempt.scope) : null;
  const optimization = await options.runtime.optimize({ scope: attempt.scope, adapter, candidates, ...context,
    model: { provider: model.provider, id: model.modelId, protocol: 'ai-sdk-v4' },
    binding, logicalCallId: attempt.logicalCallId, attemptId: attempt.attemptId,
    ...(nativeRecovery ? { recoveryOverheadText: JSON.stringify(nativeRecovery) } : {}),
    ...(params.abortSignal ? { signal: params.abortSignal } : {}),
  });
  attempt.optimization = optimization.replacements.length === 0 ? optimization : null;
  // Native definitions can change during optimize.
  const stillRegistered = !binding || safeAttested(params, registration);
  if (optimization.replacements.length) attempt.reason = stillRegistered ? 'invalid_plan' : 'recovery_unavailable';
  // Runtime validates all leaves first; local mappings are then checked as a
  // set before publishing the copy. Caller history/checkpoints keep originals.
  if (stillRegistered && optimization.replacements.every(r => setters.has(r.segment_id))) {
    attempt.optimization = optimization;
    for (const replacement of optimization.replacements) setters.get(replacement.segment_id)!(replacement.text);
    if (optimization.replacements.length) next.prompt = prompt;
  }
  return next;
}

interface NativeHooks {
  onStepStart?: GenerateTextOnStepStartCallback;
  experimental_onStepStart?: GenerateTextOnStepStartCallback;
  onLanguageModelCallStart?: OnLanguageModelCallStartCallback;
  experimental_onLanguageModelCallStart?: OnLanguageModelCallStartCallback;
  onStepEnd?: GenerateTextOnStepEndCallback;
  onStepFinish?: GenerateTextOnStepEndCallback;
}

/** TS-4: every recovery tool withCaveman made, so wrapping a bundle again returns it unchanged. */
const recoveryTools = new WeakSet<object>();

/** Complete native model/tool/callback bundle, and the ai-sdk entry point that compresses. Pass it to the native
 * generation or ToolLoopAgent API. The bundle and its tools table stay mutable in every mode; only the recovery tool is
 * frozen. Replacing it or its tools table, or removing the callbacks, leaves model calls recovery-free;
 * application-owned source tools stay native. Wrapping a bundle twice returns it unchanged. */
export function withCaveman<T extends { model: LanguageModelV4; tools?: ToolSet }>(input: T, options: CavemanOptions): T {
  const { runtime } = options;
  const existing = input.tools?.['caveman_retrieve'];
  if (existing && recoveryTools.has(existing)) return input;
  let blocked: Blocked = runtime.mode === 'off' ? null : gate(options);
  const wrap = (registration?: RecoveryRegistration) => wrapLanguageModel({ model: input.model, middleware: nativeMiddleware(options, blocked, registration) });
  // A static scope that cannot be normalized binds no recovery tool; each call reports invalid_scope instead.
  if (runtime.mode !== 'compress' || blocked || (typeof options.scope !== 'function' && !normalizeScope(options.scope))) return { ...input, model: wrap() };
  if (input.tools?.['caveman_retrieve']) {
    blocked = nameConflict(runtime, adapter.id);
    return { ...input, model: wrap() };
  }
  return guardSync(runtime, adapter.id, () => {
    const execute: RecoveryRegistration['execute'] = (args, call) =>
      recoveryResult(adapter.id, call?.signal, () => runtime.retrieve(resolveScope(options.scope, undefined) as Scope, args, call?.signal));
    // Keep the runtime's schema separate from native framework annotations.
    const expectedSchema: RecoveryBinding['inputSchema'] = structuredClone(recoveryInputSchema);
    const nativeSchema = structuredClone(expectedSchema);
    freezeSchema(nativeSchema);
    const schema = Object.freeze(jsonSchema<RetrieveArgs>(nativeSchema));
    const recovery = Object.freeze(tool({ description: recoveryToolDescription, inputSchema: schema, execute }));
    recoveryTools.add(recovery);
    const tools: ToolSet = { ...input.tools, caveman_retrieve: recovery };
    const registration: RecoveryRegistration = { execute, tool: recovery, schema, expectedSchema, calls: new WeakMap() };
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
    return { ...input, model: wrap(registration), tools, onStepStart, onLanguageModelCallStart, onStepEnd } as T;
  }, () => input, true);
}
