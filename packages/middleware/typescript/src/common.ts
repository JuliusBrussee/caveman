import { AsyncLocalStorage } from 'node:async_hooks';
import { hash } from 'node:crypto';
import { MiddlewareRuntime, type Optimization, type Scope, type Usage } from '@caveman-ai/sdk/middleware';

export interface Attempt {
  runtime: MiddlewareRuntime;
  scope: Scope;
  logicalCallId: string;
  attemptId: string;
  optimization: Optimization | null;
  wireSHA256: string | null;
  passive?: boolean;
  reason?: string;
  adapter?: string;
  reportedAttemptId?: string;
}
const owners = new AsyncLocalStorage<Attempt>();
export const currentOwner = (): Attempt | undefined => owners.getStore();
export function withOwner<T>(attempt: Attempt, fn: () => T): T { return owners.run(attempt, fn); }

export function observe(attempt: Attempt, event: 'dispatch_intent' | 'completed' | 'failed' | 'cancelled', usage: Usage | null = null): void {
  if (event === 'dispatch_intent' && attempt.reportedAttemptId !== attempt.attemptId) {
    attempt.reportedAttemptId = attempt.attemptId;
    attempt.runtime.report(attempt.optimization, {
      logicalCallId: attempt.logicalCallId, attemptId: attempt.attemptId,
      ...(attempt.reason ? { reason: attempt.reason } : {}), ...(attempt.adapter ? { adapter: attempt.adapter } : {}),
    });
  }
  if (attempt.passive || attempt.runtime.mode === 'off') return;
  void attempt.runtime.observe({ schema_version: 1, scope: attempt.scope, logical_call_id: attempt.logicalCallId, attempt_id: attempt.attemptId,
    event_kind: event, plan_id: attempt.optimization?.plan?.replacement_set_id ?? null,
    usage, provider_request_sha256: attempt.wireSHA256 });
}

export function plain(value: unknown): value is Record<string, unknown> {
  if (!value || typeof value !== 'object') return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

/** Content-blind manifests include protected components without uploading them.
 * Opaque subclasses, cycles and oversized values decline the whole view. */
export async function manifest(items: readonly unknown[]): Promise<{ id: string; sha256: string }[] | null> {
  let budget = 2 << 20;
  const seen = new Set<object>();
  function visit(value: unknown, depth: number): void {
    if (depth > 64 || budget < 0) throw new Error('bounded');
    if (typeof value === 'string') { budget -= value.length * 3; return; }
    if (value === null || value === undefined || typeof value === 'boolean' || typeof value === 'number') { budget -= 32; return; }
    if (!Array.isArray(value) && !plain(value)) throw new Error('opaque');
    if (seen.has(value as object)) throw new Error('cyclic');
    seen.add(value as object);
    for (const [key, entry] of Object.entries(value as object)) { budget -= key.length * 3; visit(entry, depth+1); }
    seen.delete(value as object);
  }
  try {
    const out = [];
    for (let i = 0; i < items.length; i++) {
      visit(items[i], 0);
      if (budget < 0) return null;
      out.push({ id: `message-${i}`, sha256: hash('sha256', JSON.stringify(items[i])) });
    }
    return out;
  } catch { return null; }
}

/** Read exactly when the native consumer pulls. Never eagerly drain a stream. */
export function observeStream<T>(stream: ReadableStream<T>, attempt: Attempt, getUsage: (event: T) => Usage | null, signal?: AbortSignal): ReadableStream<T> {
  const reader = stream.getReader();
  let usage: Usage | null = null, ended = false;
  const finish = (event: 'completed' | 'failed' | 'cancelled') => {
    if (ended) return;
    ended = true;
    observe(attempt, event, event === 'completed' ? usage : null);
    reader.releaseLock();
  };
  return new ReadableStream<T>({
    async pull(controller) {
      try {
        const result = await withOwner(attempt, () => reader.read());
        if (result.done) { finish(signal?.aborted ? 'cancelled' : usage ? 'completed' : 'failed'); controller.close(); return; }
        try { usage = getUsage(result.value) ?? usage; } catch { /* unknown native event remains untouched */ }
        controller.enqueue(result.value);
      } catch (error) { finish(signal?.aborted ? 'cancelled' : 'failed'); controller.error(error); }
    },
    async cancel(reason) {
      try { await reader.cancel(reason); } finally { finish('cancelled'); }
    },
  }, { highWaterMark: 0 });
}
