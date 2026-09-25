import type Anthropic from '@anthropic-ai/sdk';
import { MiddlewareRuntime } from '@caveman-ai/sdk/middleware';
import { bindRecovery, hintRecovery, nameConflict, plain, recoveryResult, resolveScope, type BudgetOptions, type ScopeSource } from './common.js';
import { frameworkGate, type GateOptions } from './compatibility.js';
import { guardSync } from './guard.js';
import { clientVersion } from './versions.js';
import { clientFetch, createCavemanFetch, withNativeRecovery, type FetchOptions, type RecoveryContext, type UnboundContext } from './transport.js';

export interface AnthropicOptions extends GateOptions, BudgetOptions, Pick<FetchOptions, 'wireBytes'> {
  runtime: MiddlewareRuntime;
  /** A scope, or a function called per request so one shared client can serve many users. */
  scope: ScopeSource;
  /** The same fetch implementation selected for the existing client. Default: the client's own. */
  fetch?: typeof globalThis.fetch;
  cavemanProxy?: boolean;
}
const ID = 'anthropic-sdk';

/** Keep lazy native iterators in their invocation's recovery context. */
function scopedIterator<T>(iterator: AsyncIterator<T>, context: RecoveryContext | UnboundContext): AsyncIterator<T> {
  return {
    next: (...args) => withNativeRecovery(context, () => iterator.next(...args)),
    ...(iterator.return ? { return: value => withNativeRecovery(context, () => iterator.return!(value)) } : {}),
    ...(iterator.throw ? { throw: error => withNativeRecovery(context, () => iterator.throw!(error)) } : {}),
  };
}

/** Messages and stream helpers remain native. `beta.messages.toolRunner` owns execution and is the entry point that
 * compresses; `messages.create`/`stream` cannot bind the recovery tool and report `recovery_unbound` (hinted once, on use). */
export function withCavemanAnthropic<T extends Anthropic>(client: T, options: AnthropicOptions): T {
  if (wrapped.has(client)) return client;
  // TS-2: gate on the SDK copy that built this client, not whichever `@anthropic-ai/sdk` resolves from this package.
  return wrapAnthropic(client, options, frameworkGate('anthropic', options, undefined, { '@anthropic-ai/sdk': clientVersion(client) }));
}

/** Every client wrapAnthropic returned. Wrapping one again returns it unchanged. */
const wrapped = new WeakSet<Anthropic>();

function wrapAnthropic<T extends Anthropic>(client: T, options: AnthropicOptions, blocked: string | null): T {
  if (!options.fetch) options = { ...options, fetch: clientFetch(client) };
  const fetch = createCavemanFetch({ ...options, provider: 'anthropic', providerBaseURL: client.baseURL, frameworkVersion: clientVersion(client) ?? 'unknown',
    ...(blocked ? { passiveReason: blocked } : {}),
    onUnbound: () => hintRecovery(options.runtime, ID, 'withCavemanAnthropic messages.create()/stream()', 'beta.messages.toolRunner') });
  const native = client.withOptions({ fetch });
  wrapped.add(native);
  if (blocked || options.runtime.mode === 'off') return native;
  guardSync(options.runtime, ID, () => {
    const run = native.beta.messages.toolRunner.bind(native.beta.messages);
    native.beta.messages.toolRunner = ((body: unknown, requestOptions?: unknown) => {
      if (!plain(body) || !Array.isArray(body.tools) || options.runtime.mode !== 'compress') return run(body as never, requestOptions as never);
      const bodyTools: unknown[] = body.tools;
      // The runner and its iterator are SDK internals: if they change shape, run natively and recovery-free.
      return guardSync(options.runtime, ID, () => {
        const names = bodyTools.map(tool => plain(tool) ? tool.name : null);
        const logicalCallId = crypto.randomUUID();
        let context: RecoveryContext | UnboundContext;
        let runner: ReturnType<typeof run>;
        const binding = names.includes('caveman_retrieve') || names.some(name => typeof name !== 'string' || !name) || new Set(names).size !== names.length
          ? null : bindRecovery(options.runtime, resolveScope(options.scope, undefined));
        if (names.includes('caveman_retrieve')) {
          context = { runtime: options.runtime, reason: nameConflict(options.runtime, ID), logicalCallId, owner: fetch };
          runner = run(body as never, requestOptions as never);
        } else if (!binding) return run(body as never, requestOptions as never);
        else {
          const schema = { name: binding.name, description: binding.description, input_schema: binding.inputSchema };
          const execute = async (input: unknown, context?: { signal?: AbortSignal | null }) =>
            JSON.stringify(await recoveryResult(ID, context?.signal, input, args => binding.execute(args, context?.signal ? { signal: context.signal } : undefined)));
          const recovery = Object.freeze({ ...schema, parse: (input: unknown) => input, run: execute });
          const created = runner = run({ ...body, tools: [...bodyTools, recovery] } as never, requestOptions as never);
          const isRegistered = () => {
            const tools = created.params.tools;
            const currentNames = tools.map(tool => plain(tool) ? tool.name : null);
            return options.runtime.ownsBinding(binding, binding.scope) && new Set(currentNames).size === currentNames.length &&
              currentNames.every(name => typeof name === 'string' && name.length > 0) &&
              tools.filter(tool => plain(tool) && tool.name === binding.name).length === 1 && tools.find(tool => plain(tool) && tool.name === binding.name) === recovery && recovery.run === execute;
          };
          context = { runtime: options.runtime, scope: binding.scope, binding, overhead: JSON.stringify(schema), logicalCallId, isRegistered, owner: fetch };
        }
        // The SDK runner is lazy. Wrapping only its constructor would lose the
        // binding before the first next(), and await runner uses this iterator too.
        const iterate = runner[Symbol.asyncIterator].bind(runner);
        runner[Symbol.asyncIterator] = () => scopedIterator(iterate(), context);
        return runner;
      }, () => run(body as never, requestOptions as never));
    }) as typeof native.beta.messages.toolRunner;
  }, () => undefined, true);
  const clone = native.withOptions.bind(native);
  native.withOptions = next => {
    const nextFetch = (next.fetch ?? options.fetch) as typeof globalThis.fetch;
    return wrapAnthropic(clone({ ...next, fetch: nextFetch }), { ...options, fetch: nextFetch }, blocked);
  };
  return native;
}
