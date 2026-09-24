import { MiddlewareError, warnOnce, type MiddlewareRuntime } from '@caveman-ai/sdk/middleware';

// ponytail: the SDK keeps `strict` private; a public `strict` flag, once the SDK exposes one, makes the guard raise.
const strict = (runtime: MiddlewareRuntime) => (runtime as { strict?: unknown }).strict === true;
const raises = (runtime: MiddlewareRuntime, error: unknown, signal?: AbortSignal | null) =>
  !!signal?.aborted || error instanceof MiddlewareError || strict(runtime);

/** Decision 4, the one fail-open boundary for adapter code (including hooks on framework internals). An exception
 * becomes `fallback()`, the caller's own input, with a one-time `adapter_error` warning; the native call still runs.
 * Caller cancellation and strict-mode SDK errors propagate. */
export async function guard<T>(runtime: MiddlewareRuntime, adapter: string, signal: AbortSignal | null | undefined,
  work: () => T | Promise<T>, fallback: () => T): Promise<T> {
  try { return await work(); }
  catch (error) {
    if (raises(runtime, error, signal)) throw error;
    warnOnce(adapter, 'adapter_error');
    return fallback();
  }
}

/** Synchronous twin of guard() for wrap-time hooks and native method replacement. Nothing raises at wrap time
 * (spec §8), so only an SDK error, which strict mode raises on the request path, propagates. */
export function guardSync<T>(runtime: MiddlewareRuntime, adapter: string, work: () => T, fallback: () => T): T {
  try { return work(); }
  catch (error) {
    if (error instanceof MiddlewareError) throw error;
    warnOnce(adapter, 'adapter_error');
    return fallback();
  }
}
