import type Anthropic from '@anthropic-ai/sdk';
import { VERSION } from '@anthropic-ai/sdk/version';
import { MiddlewareRuntime, type Scope } from '@caveman-ai/sdk/middleware';
import { plain } from './common.js';
import { inRange } from './versions.js';
import { createCavemanFetch, withNativeRecovery, type RecoveryContext } from './transport.js';

export interface AnthropicOptions {
  runtime: MiddlewareRuntime;
  scope: Scope;
  /** The same fetch implementation selected for the existing client. */
  fetch: typeof globalThis.fetch;
  cavemanProxy?: boolean;
}

/** Keep lazy native iterators in their invocation's recovery context. */
function scopedIterator<T>(iterator: AsyncIterator<T>, context: RecoveryContext): AsyncIterator<T> {
  return {
    next: (...args) => withNativeRecovery(context, () => iterator.next(...args)),
    ...(iterator.return ? { return: value => withNativeRecovery(context, () => iterator.return!(value)) } : {}),
    ...(iterator.throw ? { throw: error => withNativeRecovery(context, () => iterator.throw!(error)) } : {}),
  };
}

/** Messages and stream helpers remain native; beta.toolRunner owns execution. */
export function withCavemanAnthropic<T extends Anthropic>(client: T, options: AnthropicOptions): T {
  const versionSupported = inRange(VERSION, '0.124', '1');
  if (!versionSupported && options.runtime.mode !== 'off') options.runtime.decline('unsupported_version');
  const native = client.withOptions({ fetch: createCavemanFetch({ ...options, provider: 'anthropic', providerBaseURL: client.baseURL, frameworkVersion: VERSION,
    ...(!versionSupported ? { passiveReason: 'unsupported_version' as const } : {}) }) });
  if (!versionSupported || options.runtime.mode === 'off') return native;
  const run = native.beta.messages.toolRunner.bind(native.beta.messages);
  native.beta.messages.toolRunner = ((body: unknown, requestOptions?: unknown) => {
    if (!plain(body) || !Array.isArray(body.tools) || options.runtime.mode !== 'compress') return run(body as never, requestOptions as never);
    const names = body.tools.map(tool => plain(tool) ? tool.name : null);
    if (names.some(name => typeof name !== 'string' || !name) || new Set(names).size !== names.length || names.includes('caveman_retrieve')) return run(body as never, requestOptions as never);
    const binding = options.runtime.recovery(options.scope);
    const schema = { name: binding.name, description: binding.description, input_schema: binding.inputSchema };
    const execute = async (input: unknown, context?: { signal?: AbortSignal | null }) => JSON.stringify(await binding.execute(input as never, context?.signal ? { signal: context.signal } : undefined));
    const recovery = Object.freeze({ ...schema, parse: (input: unknown) => input, run: execute });
    const runner = run({ ...body, tools: [...body.tools, recovery] } as never, requestOptions as never);
    const isRegistered = () => {
      const tools = runner.params.tools;
      const currentNames = tools.map(tool => plain(tool) ? tool.name : null);
      return options.runtime.ownsBinding(binding, options.scope) && new Set(currentNames).size === currentNames.length &&
        currentNames.every(name => typeof name === 'string' && name.length > 0) &&
        tools.filter(tool => plain(tool) && tool.name === binding.name).length === 1 && tools.find(tool => plain(tool) && tool.name === binding.name) === recovery && recovery.run === execute;
    };
    const context = { runtime: options.runtime, scope: options.scope, binding, overhead: JSON.stringify(schema), logicalCallId: crypto.randomUUID(), isRegistered };
    // The SDK runner is lazy. Wrapping only its constructor would lose the
    // binding before the first next(), and await runner uses this iterator too.
    const iterate = runner[Symbol.asyncIterator].bind(runner);
    runner[Symbol.asyncIterator] = () => scopedIterator(iterate(), context);
    return runner;
  }) as typeof native.beta.messages.toolRunner;
  const clone = native.withOptions.bind(native);
  native.withOptions = next => {
    const nextFetch = (next.fetch ?? options.fetch) as typeof globalThis.fetch;
    return withCavemanAnthropic(clone({ ...next, fetch: nextFetch }), { ...options, fetch: nextFetch });
  };
  return native;
}
