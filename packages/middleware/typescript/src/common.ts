import { AsyncLocalStorage } from 'node:async_hooks';
import { hash } from 'node:crypto';
import { MIDDLEWARE_DEFAULTS, MiddlewareError, MiddlewareRuntime, manifestWindow, normalizeScope, opaqueManifestValue, warnOnce,
  type ManifestItem, type Optimization, type RecoveryBinding, type Scope, type Usage } from '@caveman-ai/sdk/middleware';

/** This package's version, sent as `adapter.version`; a test pins it to package.json. */
export const MIDDLEWARE_VERSION = '1.0.0';

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

export interface BudgetOptions {
  /** Bound on history hashed into the context manifest (spec §11; default 2 MiB). The head window is sent; an
   * oversized history never skips the call. */
  manifestBytes?: number;
}

/** Spec §11 manifest: history items hashed in order up to `manifestBytes` and 4096 items, a head window that stays
 * prefix-stable as history grows. Parts that are not plain JSON (bytes, URLs, class instances, cycles) enter as
 * `{caveman_opaque: h}`; nothing here skips the call. `sequence` is the untruncated history length. */
export async function manifest(items: readonly unknown[], manifestBytes = MIDDLEWARE_DEFAULTS.manifest_bytes): Promise<{ manifest: ManifestItem[]; sequence: number }> {
  const maxItems = MIDDLEWARE_DEFAULTS.max_manifest_items, texts: string[] = [], sizes: number[] = [];
  let total = 0;
  for (const item of items) {
    if (texts.length >= maxItems || total > manifestBytes) break;
    const opaque: { caveman_opaque: unknown }[] = [];
    const json = jsonable(item, opaque, 0, new Set());
    for (const part of opaque) part.caveman_opaque = (await opaqueManifestValue(part.caveman_opaque)).caveman_opaque;
    const text = JSON.stringify(json) ?? 'null';
    texts.push(text); sizes.push(Buffer.byteLength(text)); total += sizes.at(-1)!;
  }
  const k = manifestWindow(sizes, maxItems, manifestBytes);
  return { manifest: texts.slice(0, k).map((text, i) => ({ id: `message-${i}`, sha256: hash('sha256', text) })), sequence: items.length };
}

/** A JSON copy whose non-plain parts are placeholders for their opaque hash. Plain data serializes as before. */
function jsonable(value: unknown, opaque: { caveman_opaque: unknown }[], depth: number, path: Set<object>): unknown {
  if (value === null || typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') return value;
  if (value === undefined || typeof value === 'function' || typeof value === 'symbol') return undefined;
  if ((Array.isArray(value) || plain(value)) && depth < 64 && !path.has(value)) {
    path.add(value);
    const copy = Array.isArray(value) ? value.map(entry => jsonable(entry, opaque, depth + 1, path) ?? null)
      : Object.fromEntries(Object.entries(value).map(([key, entry]) => [key, jsonable(entry, opaque, depth + 1, path)]));
    path.delete(value);
    return copy;
  }
  const marker = { caveman_opaque: value as unknown };
  opaque.push(marker);
  return marker;
}

/** A static scope, or a function called per request (inside the request's async context) so one shared client or
 * model can serve many users, and `deleteSession` can target one of them. */
export type ScopeSource<C = void> = Scope | ((context: C) => Scope | null | undefined);

/** Spec §9 for framework-provided IDs, never throwing. A valid value comes back normalized (an email or
 * `user 42 / chat #7` becomes an `h-` hash). null means no scope was supplied (missing, or the resolver threw): the
 * call runs recovery-free as `recovery_unbound`. An unnormalizable value is returned as-is so optimize() reports
 * `invalid_scope` (raising only in strict mode). */
export function resolveScope<C>(source: ScopeSource<C>, context: C): Scope | null {
  let value: unknown;
  try { value = typeof source === 'function' ? source(context) : source; } catch { return null; }
  return value == null ? null : normalizeScope(value) ?? value as Scope;
}

/** A recovery binding only for a normalizable scope, so the strict-mode `invalid_scope` throw of recovery() never
 * happens at wrap time; optimize() reports it per call instead. */
export function bindRecovery(runtime: MiddlewareRuntime, scope: Scope | null): RecoveryBinding | null {
  return scope && normalizeScope(scope) ? runtime.recovery(scope) : null;
}

const passiveScope: Scope = Object.freeze({ namespace: 'caveman-passive', session_id: 'report-only', branch_id: 'main', cache_epoch: '0' });
/** A report-only attempt: the native call gets its original input and the report carries `reason`. */
export function passiveAttempt(runtime: MiddlewareRuntime, adapter: string, reason: string, logicalCallId: string = crypto.randomUUID()): Attempt {
  return { runtime, scope: passiveScope, logicalCallId, attemptId: crypto.randomUUID(), optimization: null, wireSHA256: null, passive: true, reason, adapter };
}

/** C14: the host already has a tool named caveman_retrieve. Recovery stays off, compress-mode calls pass through
 * reporting `recovery_name_conflict`, and strict mode raises it from ready(). */
export function nameConflict(runtime: MiddlewareRuntime, adapter: string): 'recovery_name_conflict' {
  warnOnce(adapter, 'recovery_name_conflict');
  runtime.decline('recovery_name_conflict', adapter);
  return 'recovery_name_conflict';
}

/** TS-1: a recovery the runtime refuses (unknown or expired handle, runtime down) answers the model `{error: code}`
 * with a warn-once instead of failing the host's native tool loop. Caller aborts and non-SDK errors propagate. */
export async function recoveryResult<T>(adapter: string, signal: AbortSignal | null | undefined, retrieve: () => Promise<T>): Promise<T | { error: string }> {
  try { return await retrieve(); }
  catch (error) {
    if (signal?.aborted || !(error instanceof MiddlewareError)) throw error;
    warnOnce(adapter, error.code);
    return { error: error.code };
  }
}

const hinted = new Set<string>();
/** C7: an entry point that cannot bind the recovery tool leaves compress mode `recovery_unbound` on every call. Say so
 * once, naming the entry point that does compress. */
export function hintRecovery(runtime: MiddlewareRuntime, adapter: string, entry: string, use: string): void {
  if (runtime.mode !== 'compress' || hinted.has(entry)) return;
  hinted.add(entry);
  console.warn(`Caveman middleware: adapter=${adapter} reason=recovery_unbound ${entry} cannot bind the recovery tool, so compress mode leaves content unchanged; use ${use} to compress.`);
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
