import { MiddlewareError, warnOnce, type MiddlewareRuntime } from '@caveman-ai/sdk/middleware';

/** Decision 4, the one fail-open boundary for adapter code (including hooks on framework internals). An exception
 * becomes `fallback()`, the caller's own input, with a one-time `adapter_error` warning; the native call still runs.
 * Caller cancellation and SDK errors propagate; strict mode raises any other exception as `adapter_error` (spec §8). */
export async function guard<T>(runtime: MiddlewareRuntime, adapter: string, signal: AbortSignal | null | undefined,
  work: () => T | Promise<T>, fallback: () => T): Promise<T> {
  try { return await work(); }
  catch (error) {
    if (signal?.aborted || error instanceof MiddlewareError) throw error;
    if (runtime.strict) throw Object.assign(new MiddlewareError('adapter_error'), { cause: error });
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
