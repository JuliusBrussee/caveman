import { createTool } from '@mastra/core/tools';
import { standardSchemaToJSONSchema } from '@mastra/core/schema';
import { isDeepStrictEqual } from 'node:util';
import { hash } from 'node:crypto';
import type { ProcessInputStepArgs, ProcessInputStepResult, ProcessLLMRequestArgs, Processor } from '@mastra/core/processors';
import type { Agent, AgentExecutionOptions } from '@mastra/core/agent';
import type { LanguageModelV4CallOptions, LanguageModelV4Usage } from '@ai-sdk/provider';
import { MiddlewareRuntime, type Candidate, type RecoveryBinding, type Scope, type Usage } from '@caveman-ai/sdk/middleware';
import { MIDDLEWARE_VERSION, bindRecovery, currentOwner, hintRecovery, manifest, nameConflict, observe, observeStream, passiveAttempt as passive, plain,
  recoveryResult, resolveScope, withOwner, type Attempt, type BudgetOptions, type ScopeSource } from './common.js';
import { frameworkGate, frameworkVersion, type GateOptions, type GateReason } from './compatibility.js';
import { guard, guardSync } from './guard.js';

export interface CavemanMastraOptions extends GateOptions, BudgetOptions {
  runtime: MiddlewareRuntime;
  /** Resolve from the application's authenticated RequestContext, never model input. */
  scope: ScopeSource<Pick<ProcessLLMRequestArgs, 'requestContext'>>;
  /** Model-only callers can opt out of adding a recovery executor. */
  recovery?: boolean;
  /** Supply a distinct ID when an application installs multiple processors. */
  id?: string;
}

const adapter = { id: 'mastra', version: MIDDLEWARE_VERSION, framework_version: frameworkVersion('@mastra/core') ?? 'unknown', serialization_revision: 'mastra-v4.1' };
const measured = (n: number | undefined): number | null => Number.isSafeInteger(n) && n! >= 0 ? n! : null;
function usage(value: LanguageModelV4Usage): Usage {
  const input = measured(value.inputTokens.total), output = measured(value.outputTokens.total);
  return { provenance: 'client_observed_sdk', complete: input !== null && output !== null, input_tokens: input, output_tokens: output,
    cache_read_tokens: measured(value.inputTokens.cacheRead), cache_write_tokens: measured(value.inputTokens.cacheWrite), reasoning_tokens: measured(value.outputTokens.reasoning) };
}

type FinalStep = Pick<ProcessInputStepArgs, 'model' | 'tools'>;

const passiveAttempt = (options: CavemanMastraOptions, reason: string): Attempt => passive(options.runtime, 'mastra', reason);
const gate = (options: CavemanMastraOptions): GateReason | null =>
  frameworkGate('mastra', options, () => typeof createTool === 'function' && typeof standardSchemaToJSONSchema === 'function');

/** Hashes of error texts the application saw before Mastra normalized them, kept per thread (or per call without one),
 * so they never spread to other tenants (C4). `all` latches when a history is too large or deep to scan; it lasts one
 * turn (TS-8), so a thread that once overflowed compresses again when its next turn fits the budget. */
interface Protection { hashes: Set<string>; all: boolean }
function remember(protection: Protection, value: unknown, depth = 0, budget = { remaining: 16384 }): void {
  if (protection.all) return;
  if (depth > 32 || --budget.remaining < 0) { protection.all = true; return; }
  if (!value || typeof value !== 'object') return;
  if (Array.isArray(value)) { for (const part of value) remember(protection, part, depth + 1, budget); return; }
  if (!plain(value)) return;
  const failed = value.type === 'error-text' || value.type === 'error-json' || value.state === 'output-error' || value.state === 'output-denied' || value.isError === true;
  if (failed) for (const text of [value.value, value.result, value.output, value.errorText]) if (typeof text === 'string') {
    if (protection.hashes.size >= 1024) { protection.all = true; return; }
    protection.hashes.add(hash('sha256', text));
  }
  for (const entry of Object.values(value)) remember(protection, entry, depth + 1, budget);
}

/** Observe only the native method boundary; no untested model shape is read. */
function passiveModel<T extends object>(model: T, options: CavemanMastraOptions, reason: string): T {
  return new Proxy(model, { get(target, key) {
    const value = Reflect.get(target, key, target);
    if ((key === 'doGenerate' || key === 'doStream') && typeof value === 'function') return (...args: unknown[]) => {
      if (currentOwner()) return Reflect.apply(value, target, args);
      const attempt = passiveAttempt(options, reason); observe(attempt, 'dispatch_intent');
      return withOwner(attempt, () => Reflect.apply(value, target, args));
    };
    return typeof value === 'function' ? value.bind(target) : value;
  } });
}

/** Observe native calls without installing a lossy recovery grant, so compress mode reports `recovery_unbound`. For
 * native executable-tool attestation use withCavemanMastra, which owns the final hook and compresses. */
export function createCavemanMastraProcessor(options: CavemanMastraOptions): Processor {
  const blocked = gate(options);
  if (!blocked) hintRecovery(options.runtime, 'mastra', 'createCavemanMastraProcessor', 'withCavemanMastra');
  return createProcessor(options, false, blocked).processor;
}

/** Wrap an existing Agent's public entry points. Each call keeps its native
 * processors and prepareStep callback, then attests the executable tool table
 * at Mastra's enforced final prepareStep boundary. No Agent config is mutated. */
export function withCavemanMastra<T extends Agent>(agent: T, options: CavemanMastraOptions): T {
  if (mastraAgents.has(agent)) return agent;
  const blocked = gate(options);
  if (blocked) {
    return own(new Proxy(agent, { get(target, key) {
      const value = Reflect.get(target, key, target);
      if ((key === 'generate' || key === 'stream') && typeof value === 'function') return (...args: unknown[]) => {
        if (currentOwner()) return Reflect.apply(value, target, args);
        const attempt = passiveAttempt(options, blocked); observe(attempt, 'dispatch_intent');
        return withOwner(attempt, () => Reflect.apply(value, target, args));
      };
      return typeof value === 'function' ? value.bind(target) : value;
    } }));
  }
  // Mastra can normalize imported error-text into a plain DB result. Remember protected text before native
  // normalization, including later memory turns of the same thread. Only hashes are kept; overflow declines loss for
  // that thread's current turn alone. An LRU bounds the threads remembered per wrapper.
  const threads = new Map<string, Set<string>>();
  const protection = (call: AgentExecutionOptions): Protection => {
    const thread = call.memory?.thread, id = typeof thread === 'string' ? thread : thread?.id;
    if (typeof id !== 'string') return { hashes: new Set(), all: false };
    const key = JSON.stringify([call.memory?.resource ?? null, id]), hashes = threads.get(key) ?? new Set();
    threads.delete(key); threads.set(key, hashes);
    if (threads.size > 256) threads.delete(threads.keys().next().value!);
    return { hashes, all: false };
  };
  return own(new Proxy(agent, {
    get(target, key) {
      if (key === 'generate' || key === 'stream') return async (messages: Parameters<Agent['generate']>[0], call: AgentExecutionOptions = {}) => {
        call.abortSignal?.throwIfAborted();
        const defaults = await target.getDefaultOptions(call.requestContext ? { requestContext: call.requestContext } : {});
        const requestContext = call.requestContext ?? defaults.requestContext;
        const processors = call.inputProcessors ?? defaults.inputProcessors ?? await target.listConfiguredInputProcessors(requestContext);
        const applicationPrepare = call.prepareStep === undefined ? defaults.prepareStep : call.prepareStep;
        const protected_ = protection(call);
        remember(protected_, messages);
        const bundle = createProcessor({ ...options, id: `${options.id ?? 'caveman'}-${crypto.randomUUID()}` }, true, null,
          text => protected_.all || protected_.hashes.has(hash('sha256', text)), value => remember(protected_, value));
        const prepareStep = async (args: ProcessInputStepArgs) => {
          const before: FinalStep = { model: args.model, ...(args.tools ? { tools: args.tools } : {}) };
          const result = await applicationPrepare?.(args);
          if (bundle.attest(before, result)) return result;
          const selected = result?.model ?? before.model;
          if (!selected || typeof selected !== 'object') {
            if (!currentOwner()) observe(passiveAttempt(options, 'unsupported_provider'), 'dispatch_intent');
            return result;
          }
          return { ...result, model: passiveModel(selected, options, options.runtime.mode === 'off' ? 'disabled' : 'unsupported_request') };
        };
        return Reflect.apply(target[key], target, [messages, { ...call, inputProcessors: [bundle.processor, ...processors], prepareStep }]);
      };
      const value = Reflect.get(target, key, target);
      return typeof value === 'function' ? value.bind(target) : value;
    },
  }));
}

/** Every agent withCavemanMastra returned. Wrapping one again returns it unchanged. */
const mastraAgents = new WeakSet<object>();
function own<T extends object>(agent: T): T { mastraAgents.add(agent); return agent; }

function createProcessor(options: CavemanMastraOptions, enforcedFinalStep: boolean, blocked: GateReason | null, isProtectedText: (text: string) => boolean = () => false, rememberProtected: (value: unknown) => void = () => {}): { processor: Processor; attest: (before: FinalStep, result: ProcessInputStepResult | undefined | void) => boolean } {
  if (blocked) {
    return { processor: { id: options.id ?? 'caveman', processInputStep(args) { return { model: passiveModel(args.model, options, blocked) }; } }, attest() { return false; } };
  }
  type Model = Extract<ProcessInputStepArgs['model'], { specificationVersion: 'v4' }>;
  interface Step {
    scope: Scope;
    binding: RecoveryBinding | null;
    logicalCallId: string;
    outbound: boolean;
    recoveryAllowed: boolean;
    recoverySchema: unknown;
    finalTools: Record<string, unknown> | undefined;
    recoveryIntact: () => boolean;
  }
  const steps = new WeakMap<object, Step>();
  const ours = new WeakSet<object>();
  // A step this processor already made report-only keeps its own reason at the final prepareStep boundary.
  const passives = new WeakSet<object>();
  const passiveStep = (model: ProcessInputStepArgs['model'], reason: string) => { const wrapped = passiveModel(model, options, reason); passives.add(wrapped); return { model: wrapped }; };
  const calls = new WeakMap<object, string>();

  async function prepare(params: LanguageModelV4CallOptions, model: Model, step: Step): Promise<{ params: LanguageModelV4CallOptions; attempt: Attempt }> {
    params.abortSignal?.throwIfAborted();
    const attempt: Attempt = { runtime: options.runtime, scope: step.scope, logicalCallId: step.logicalCallId,
      attemptId: crypto.randomUUID(), optimization: null, wireSHA256: null, adapter: 'mastra' };
    // Protected call parameters contribute hashes, never raw authorization or
    // provider options, to the runtime's append-only manifest.
    const envelope = { tools: params.tools, toolChoice: params.toolChoice, responseFormat: params.responseFormat, providerOptions: params.providerOptions };
    const context = await manifest([envelope, ...params.prompt], options.manifestBytes);
    const candidates: Candidate[] = [];
    const setters = new Map<string, (text: string) => void>();
    const prompt = params.prompt.slice();
    const toolCalls = new Map<string, string | null>();
    for (let mi = 0; mi < params.prompt.length; mi++) {
      const message = params.prompt[mi];
      if (plain(message) && message.role === 'assistant' && Array.isArray(message.content)) {
        for (const part of message.content) if (plain(part) && part.type === 'tool-call' && typeof part.toolCallId === 'string' && typeof part.toolName === 'string') {
          toolCalls.set(part.toolCallId, toolCalls.has(part.toolCallId) ? null : part.toolName);
        }
      }
      if (!plain(message) || message.role !== 'tool' || !Array.isArray(message.content)) continue;
      const content = message.content.slice();
      for (let pi = 0; pi < content.length; pi++) {
        const part = content[pi];
        if (!plain(part) || part.type !== 'tool-result' || part.toolName === 'caveman_retrieve' || !plain(part.output)) continue;
        if (toolCalls.get(part.toolCallId) !== part.toolName) continue;
        const output = part.output;
        if ((output.type !== 'text' && output.type !== 'json') || typeof output.value !== 'string') continue;
        if (isProtectedText(output.value)) continue;
        if ('citations' in part || 'citations' in output || 'isError' in part) continue;
        const id = `message-${mi + 1}.part-${pi}`;
        candidates.push({ id, sourceId: id, content: output.value, kind: 'tool_result' });
        setters.set(id, text => { content[pi] = { ...part, output: { ...output, value: text } }; prompt[mi] = { ...message, content }; });
      }
    }
    const recoveryTools = params.tools?.filter(tool => tool.type === 'function' && tool.name === 'caveman_retrieve') ?? [];
    const recovery = recoveryTools.length === 1 ? recoveryTools[0] : undefined;
    const allowed = step.recoveryAllowed && (!params.toolChoice || params.toolChoice.type === 'auto') && (!params.responseFormat || params.responseFormat.type === 'text');
    const bound = allowed && step.recoveryIntact() && options.runtime.ownsBinding(step.binding, step.scope) && recovery?.type === 'function' &&
      recovery.description === step.binding.description && isDeepStrictEqual(recovery.inputSchema, step.recoverySchema);
    attempt.optimization = await options.runtime.optimize({ scope: step.scope, adapter, candidates, ...context,
      model: { provider: model.provider, id: model.modelId, protocol: 'ai-sdk-v4' }, binding: bound ? step.binding : null,
      logicalCallId: step.logicalCallId, attemptId: attempt.attemptId,
      ...(recovery ? { recoveryOverheadText: JSON.stringify(recovery) } : {}), ...(params.abortSignal ? { signal: params.abortSignal } : {}),
    });
    const replacements = attempt.optimization.replacements;
    if (replacements.length && replacements.every(replacement => setters.has(replacement.segment_id))) {
      for (const replacement of replacements) setters.get(replacement.segment_id)!(replacement.text);
      return { params: { ...params, prompt }, attempt };
    }
    if (replacements.length) { attempt.optimization = null; attempt.reason = 'invalid_plan'; }
    return { params, attempt };
  }

  const processor: Processor = {
    id: options.id ?? 'caveman',
    processInputStep(args) {
      args.abortSignal?.throwIfAborted();
      if (args.model.specificationVersion !== 'v4' || options.runtime.mode === 'off') {
        return passiveStep(args.model, options.runtime.mode === 'off' ? 'disabled' : 'unsupported_provider');
      }
      // messageList is a Mastra internal: if it moves, this step passes through (adapter_error) instead of failing.
      if (!guardSync(options.runtime, 'mastra', () => { rememberProtected(args.messageList.get.all.db()); return true; }, () => false)) {
        return passiveStep(args.model, 'adapter_error');
      }
      const scope = resolveScope(options.scope, args);
      if (!scope) return passiveStep(args.model, 'recovery_unbound');
      let logicalCallId = calls.get(args.state);
      if (!logicalCallId) { logicalCallId = crypto.randomUUID(); calls.set(args.state, logicalCallId); }
      const recoveryAllowed = enforcedFinalStep && options.recovery !== false && options.runtime.mode !== 'record' && !args.structuredOutput &&
        (!args.toolChoice || args.toolChoice === 'auto') && (!args.activeTools || args.activeTools.includes('caveman_retrieve'));
      const existing = args.tools?.['caveman_retrieve'];
      // C14: a host tool named caveman_retrieve keeps recovery off and the step reports why.
      if (recoveryAllowed && existing && !ours.has(existing)) return passiveStep(args.model, nameConflict(options.runtime, 'mastra'));
      const binding = recoveryAllowed && !existing ? bindRecovery(options.runtime, scope) : null;
      const step: Step = { scope, binding, logicalCallId, outbound: false, recoveryAllowed, recoverySchema: null, finalTools: undefined, recoveryIntact: () => false };
      const nativeModel = args.model;
      const model = new Proxy(nativeModel, {
        get(target, key) {
          if (key === 'doGenerate' || key === 'doStream') return async (params: LanguageModelV4CallOptions) => {
            params.abortSignal?.throwIfAborted();
            if (currentOwner()) return target[key](params);
            if (!step.outbound) {
              const attempt = passiveAttempt(options, 'unsupported_request'); observe(attempt, 'dispatch_intent');
              return withOwner(attempt, () => target[key](params));
            }
            const prepared = await guard(options.runtime, 'mastra', params.abortSignal, () => prepare(params, nativeModel, step),
              () => ({ params, attempt: passive(options.runtime, 'mastra', 'adapter_error', step.logicalCallId) }));
            params.abortSignal?.throwIfAborted();
            observe(prepared.attempt, 'dispatch_intent');
            try {
              const result = await withOwner(prepared.attempt, () => target[key](prepared.params));
              // Mastra normalizes both native methods to a stream result.
              return { ...result, stream: observeStream(result.stream, prepared.attempt, event => event.type === 'finish' ? usage(event.usage) : null, params.abortSignal) };
            } catch (error) { observe(prepared.attempt, params.abortSignal?.aborted ? 'cancelled' : 'failed'); throw error; }
          };
          const value = Reflect.get(target, key, target);
          return typeof value === 'function' ? value.bind(target) : value;
        },
      });
      steps.set(model, step);
      if (!binding) return { model };
      const recovery = createTool({ id: binding.name, description: binding.description, inputSchema: structuredClone(binding.inputSchema),
        execute: (input, context) => recoveryResult('mastra', context?.abortSignal, input, args => binding.execute(args, context?.abortSignal ? { signal: context.abortSignal } : undefined)),
      });
      ours.add(recovery);
      if (recovery.inputSchema) step.recoverySchema = structuredClone(standardSchemaToJSONSchema(recovery.inputSchema, { io: 'input' }));
      const tools = { ...args.tools, [binding.name]: recovery };
      // A schema-only descriptor is insufficient: native processors can alter
      // the real Tool after this hook. Attest the executor and output contract
      // again at the model delegate, alongside finalized provider parameters.
      const contract = Object.entries(recovery), inputSchema = recovery.inputSchema;
      const validate = inputSchema?.['~standard'].validate;
      step.recoveryIntact = () => {
        try {
          return step.finalTools?.[binding.name] === recovery && recovery.id === binding.name && recovery.description === binding.description &&
            Object.keys(recovery).length === contract.length && contract.every(([key, value]) => Reflect.get(recovery, key) === value) &&
            !!inputSchema && inputSchema['~standard'].validate === validate &&
            isDeepStrictEqual(standardSchemaToJSONSchema(inputSchema, { io: 'input' }), step.recoverySchema);
        } catch { return false; }
      };
      return { model, tools };
    },
    processLLMRequest(args) {
      args.abortSignal?.throwIfAborted();
      const step = steps.get(args.model);
      if (step) step.outbound = true;
      // Finalized model options may disable recovery or inject a structured
      // contract after this hook. The delegate validates them before optimizing.
    },
  };
  return { processor, attest(before, result) {
    const model = result?.model ?? before.model;
    if (!model || typeof model !== 'object') return false;
    const step = steps.get(model);
    if (step) step.finalTools = result && Object.hasOwn(result, 'tools') ? result.tools : before.tools;
    return !!step || passives.has(model);
  } };
}
