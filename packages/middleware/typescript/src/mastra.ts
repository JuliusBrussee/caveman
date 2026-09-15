import { createTool } from '@mastra/core/tools';
import { standardSchemaToJSONSchema } from '@mastra/core/schema';
import { isDeepStrictEqual } from 'node:util';
import { hash } from 'node:crypto';
import type { ProcessInputStepArgs, ProcessInputStepResult, ProcessLLMRequestArgs, Processor } from '@mastra/core/processors';
import type { Agent, AgentExecutionOptions } from '@mastra/core/agent';
import type { LanguageModelV4CallOptions, LanguageModelV4Usage } from '@ai-sdk/provider';
import { MiddlewareRuntime, type Candidate, type RecoveryBinding, type RetrieveArgs, type Scope, type Usage } from '@caveman-ai/sdk/middleware';
import { currentOwner, manifest, observe, observeStream, plain, withOwner, type Attempt } from './common.js';
import { matchesFramework } from './versions.js';

export interface CavemanMastraOptions {
  runtime: MiddlewareRuntime;
  /** Resolve from the application's authenticated RequestContext, never model input. */
  scope: Scope | ((context: Pick<ProcessLLMRequestArgs, 'requestContext'>) => Scope);
  /** Model-only callers can opt out of adding a recovery executor. */
  recovery?: boolean;
  /** Supply a distinct ID when an application installs multiple processors. */
  id?: string;
}

const adapter = { id: 'mastra', version: '0.1.0', framework_version: '1.65.0', serialization_revision: 'mastra-v4.1' };
const measured = (n: number | undefined): number | null => Number.isSafeInteger(n) && n! >= 0 ? n! : null;
function usage(value: LanguageModelV4Usage): Usage {
  const input = measured(value.inputTokens.total), output = measured(value.outputTokens.total);
  return { provenance: 'client_observed_sdk', complete: input !== null && output !== null, input_tokens: input, output_tokens: output,
    cache_read_tokens: measured(value.inputTokens.cacheRead), cache_write_tokens: measured(value.inputTokens.cacheWrite), reasoning_tokens: measured(value.outputTokens.reasoning) };
}

type FinalStep = Pick<ProcessInputStepArgs, 'model' | 'tools'>;

function passiveAttempt(options: CavemanMastraOptions, reason: string): Attempt {
  return { runtime: options.runtime, scope: { namespace: 'caveman-passive', session_id: 'report-only', branch_id: 'main', cache_epoch: '0' },
    logicalCallId: crypto.randomUUID(), attemptId: crypto.randomUUID(), optimization: null, wireSHA256: null,
    passive: true, reason, adapter: 'mastra' };
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

/** Observe native calls without installing a lossy recovery grant. For native
 * executable-tool attestation use withCavemanMastra, which owns the final hook. */
export function createCavemanMastraProcessor(options: CavemanMastraOptions): Processor {
  return createProcessor(options, false).processor;
}

/** Wrap an existing Agent's public entry points. Each call keeps its native
 * processors and prepareStep callback, then attests the executable tool table
 * at Mastra's enforced final prepareStep boundary. No Agent config is mutated. */
export function withCavemanMastra<T extends Agent>(agent: T, options: CavemanMastraOptions): T {
  if (!matchesFramework('@mastra/core', '1.65', '2', '@mastra/core/agent')) {
    if (options.runtime.mode !== 'off') options.runtime.decline('unsupported_version');
    return new Proxy(agent, { get(target, key) {
      const value = Reflect.get(target, key, target);
      if ((key === 'generate' || key === 'stream') && typeof value === 'function') return (...args: unknown[]) => {
        if (currentOwner()) return Reflect.apply(value, target, args);
        const attempt = passiveAttempt(options, 'unsupported_version'); observe(attempt, 'dispatch_intent');
        return withOwner(attempt, () => Reflect.apply(value, target, args));
      };
      return typeof value === 'function' ? value.bind(target) : value;
    } });
  }
  // Mastra can normalize imported error-text into a plain DB result. Remember
  // protected text before native normalization, including subsequent memory
  // turns on this wrapper. Only hashes are retained; overflow declines loss.
  const protectedText = new Set<string>();
  let protectAll = false;
  function rememberProtected(value: unknown, depth = 0, budget = { remaining: 16384 }): void {
    if (protectAll) return;
    if (depth > 32 || --budget.remaining < 0) { protectAll = true; return; }
    if (!value || typeof value !== 'object') return;
    if (Array.isArray(value)) { for (const part of value) rememberProtected(part, depth + 1, budget); return; }
    if (!plain(value)) return;
    const failed = value.type === 'error-text' || value.type === 'error-json' || value.state === 'output-error' || value.state === 'output-denied' || value.isError === true;
    if (failed) for (const text of [value.value, value.result, value.output, value.errorText]) if (typeof text === 'string') {
      if (protectedText.size >= 1024) { protectAll = true; return; }
      protectedText.add(hash('sha256', text));
    }
    for (const entry of Object.values(value)) rememberProtected(entry, depth + 1, budget);
  }
  return new Proxy(agent, {
    get(target, key) {
      if (key === 'generate' || key === 'stream') return async (messages: Parameters<Agent['generate']>[0], call: AgentExecutionOptions = {}) => {
        call.abortSignal?.throwIfAborted();
        const defaults = await target.getDefaultOptions(call.requestContext ? { requestContext: call.requestContext } : {});
        const requestContext = call.requestContext ?? defaults.requestContext;
        const processors = call.inputProcessors ?? defaults.inputProcessors ?? await target.listConfiguredInputProcessors(requestContext);
        const applicationPrepare = call.prepareStep === undefined ? defaults.prepareStep : call.prepareStep;
        rememberProtected(messages);
        const bundle = createProcessor({ ...options, id: `${options.id ?? 'caveman'}-${crypto.randomUUID()}` }, true,
          text => protectAll || protectedText.has(hash('sha256', text)), rememberProtected);
        const prepareStep = async (args: ProcessInputStepArgs) => {
          const before: FinalStep = { model: args.model, ...(args.tools ? { tools: args.tools } : {}) };
          const result = await applicationPrepare?.(args);
          if (bundle.attest(before, result)) return result;
          const selected = result?.model ?? before.model;
          if (!selected || typeof selected !== 'object') {
            if (!currentOwner()) observe(passiveAttempt(options, 'unsupported_model'), 'dispatch_intent');
            return result;
          }
          return { ...result, model: passiveModel(selected, options, options.runtime.mode === 'off' ? 'off' : 'unattested_model') };
        };
        return Reflect.apply(target[key], target, [messages, { ...call, inputProcessors: [bundle.processor, ...processors], prepareStep }]);
      };
      const value = Reflect.get(target, key, target);
      return typeof value === 'function' ? value.bind(target) : value;
    },
  });
}

function createProcessor(options: CavemanMastraOptions, enforcedFinalStep: boolean, isProtectedText: (text: string) => boolean = () => false, rememberProtected: (value: unknown) => void = () => {}): { processor: Processor; attest: (before: FinalStep, result: ProcessInputStepResult | undefined | void) => boolean } {
  if (!matchesFramework('@mastra/core', '1.65', '2', '@mastra/core/agent')) {
    if (options.runtime.mode !== 'off') options.runtime.decline('unsupported_version');
    return { processor: { id: options.id ?? 'caveman', processInputStep(args) { return { model: passiveModel(args.model, options, 'unsupported_version') }; } }, attest() { return false; } };
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
  const calls = new WeakMap<object, string>();

  async function prepare(params: LanguageModelV4CallOptions, model: Model, step: Step): Promise<{ params: LanguageModelV4CallOptions; attempt: Attempt }> {
    params.abortSignal?.throwIfAborted();
    const attempt: Attempt = { runtime: options.runtime, scope: step.scope, logicalCallId: step.logicalCallId,
      attemptId: crypto.randomUUID(), optimization: null, wireSHA256: null, adapter: 'mastra' };
    // Protected call parameters contribute hashes, never raw authorization or
    // provider options, to the runtime's append-only manifest.
    const envelope = { tools: params.tools, toolChoice: params.toolChoice, responseFormat: params.responseFormat, providerOptions: params.providerOptions };
    const context = await manifest([envelope, ...params.prompt]);
    if (!context) { attempt.reason = 'unsupported_shape'; return { params, attempt }; }
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
    attempt.optimization = await options.runtime.optimize({ scope: step.scope, adapter, candidates, manifest: context,
      model: { provider: model.provider, id: model.modelId, protocol: 'ai-sdk-v4' }, binding: bound ? step.binding : null,
      logicalCallId: step.logicalCallId, attemptId: attempt.attemptId,
      ...(recovery ? { recoveryOverheadText: JSON.stringify(recovery) } : {}), ...(params.abortSignal ? { signal: params.abortSignal } : {}),
    });
    const replacements = attempt.optimization.replacements;
    if (replacements.length && replacements.every(replacement => setters.has(replacement.segment_id))) {
      for (const replacement of replacements) setters.get(replacement.segment_id)!(replacement.text);
      return { params: { ...params, prompt }, attempt };
    }
    if (replacements.length) { attempt.optimization = null; attempt.reason = 'invalid_replacement_plan'; }
    return { params, attempt };
  }

  const processor: Processor = {
    id: options.id ?? 'caveman',
    processInputStep(args) {
      args.abortSignal?.throwIfAborted();
      if (args.model.specificationVersion !== 'v4' || options.runtime.mode === 'off') {
        return { model: passiveModel(args.model, options, options.runtime.mode === 'off' ? 'off' : 'unsupported_model') };
      }
      rememberProtected(args.messageList.get.all.db());
      const scope = Object.freeze({ ...(typeof options.scope === 'function' ? options.scope(args) : options.scope) });
      let logicalCallId = calls.get(args.state);
      if (!logicalCallId) { logicalCallId = crypto.randomUUID(); calls.set(args.state, logicalCallId); }
      const recoveryAllowed = enforcedFinalStep && options.recovery !== false && options.runtime.mode !== 'record' && !args.structuredOutput &&
        (!args.toolChoice || args.toolChoice === 'auto') && (!args.activeTools || args.activeTools.includes('caveman_retrieve'));
      const binding = recoveryAllowed && !args.tools?.['caveman_retrieve'] ? options.runtime.recovery(scope) : null;
      const step: Step = { scope, binding, logicalCallId, outbound: false, recoveryAllowed, recoverySchema: null, finalTools: undefined, recoveryIntact: () => false };
      const nativeModel = args.model;
      const model = new Proxy(nativeModel, {
        get(target, key) {
          if (key === 'doGenerate' || key === 'doStream') return async (params: LanguageModelV4CallOptions) => {
            params.abortSignal?.throwIfAborted();
            if (currentOwner()) return target[key](params);
            if (!step.outbound) {
              const attempt = passiveAttempt(options, 'unattested_model'); observe(attempt, 'dispatch_intent');
              return withOwner(attempt, () => target[key](params));
            }
            const prepared = await prepare(params, nativeModel, step);
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
        execute: (input, context) => binding.execute(input as RetrieveArgs, context?.abortSignal ? { signal: context.abortSignal } : undefined),
      });
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
    return !!step;
  } };
}
