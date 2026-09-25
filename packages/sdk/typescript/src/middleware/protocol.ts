/** Protocol 1.1 client rules, pure and I/O-free (docs/technical/middleware-protocol.md). Every helper is pinned by
 * packages/sdk/parity/middleware-v1_1.fixtures.json and mirrored by caveman_cloud.middleware.protocol.
 * @experimental */
import { BREAKER_DEFAULTS, DEFAULT_BRANCH_ID, DEFAULT_CACHE_EPOCH, MIDDLEWARE_DEFAULTS, REASON_CATALOG, SCOPE_HASH_HEX_CHARS, SCOPE_HASH_PREFIX } from './types.js';
import type { BreakerParams, BreakerState, BudgetItem, BudgetResult, FailureInput, FailureOutcome, OpaqueManifestValue, ReasonPolicy, Scope } from './types.js';
import { isToken, MiddlewareError, positive, sha256, sha256Sync } from './validate.js';

const REASON = /^[a-z][a-z0-9_]{0,63}$/;
const LOOPBACK = new Set(['localhost', '127.0.0.1', '[::1]']);
const CLEARS = new Set(['runtime_unavailable', 'unknown_capability', 'unsupported_version']);
export const reasonPolicy = (code: string): ReasonPolicy | undefined => (REASON_CATALOG as Record<string, ReasonPolicy | undefined>)[code];

/** Spec §9: a valid scope token passes unchanged; any other well-formed string becomes `h-` + 32 hex of its SHA-256; else null. */
export function normalizeScopeToken(value: unknown): string | null {
  if (typeof value !== 'string' || value === '' || !value.isWellFormed()) return null;
  return isToken(value) ? value : SCOPE_HASH_PREFIX + sha256Sync(value).slice(0, SCOPE_HASH_HEX_CHARS);
}

/** Spec §9: namespace and session_id are required; branch_id / cache_epoch default when absent or null. */
export function normalizeScope(input: unknown): Scope | null {
  if (typeof input !== 'object' || input === null) return null;
  const s = input as Record<string, unknown>;
  const pick = (key: keyof Scope, fallback?: string) => s[key] == null && fallback !== undefined ? fallback : normalizeScopeToken(s[key]);
  const scope = { namespace: pick('namespace'), session_id: pick('session_id'), branch_id: pick('branch_id', DEFAULT_BRANCH_ID), cache_epoch: pick('cache_epoch', DEFAULT_CACHE_EPOCH) };
  return Object.values(scope).every(v => v !== null) ? scope as Scope : null;
}

/** Spec §6 step 6 for an error code already extracted from a response (status 0: raised by a transport). */
export function codeOutcome(code: string, status = 0, retryAfterMs: number | null = null): FailureOutcome {
  const policy = reasonPolicy(code);
  return { reason: code, breaker: policy ? policy.breaker : status >= 500, clear_capabilities: CLEARS.has(code) || (!policy && status >= 500), retry_after_ms: retryAfterMs };
}

/** Spec §6 client classification. Outcomes derive from `error.code`, not the status, so mixed 1.0/1.1 fleets agree. */
export function classifyFailure(input: FailureInput): FailureOutcome {
  if (input.transport === 'error') return { reason: 'runtime_unavailable', breaker: true, clear_capabilities: true, retry_after_ms: null };
  if (input.transport === 'timeout') return { reason: 'deadline', breaker: true, clear_capabilities: false, retry_after_ms: null };
  if (input.transport === 'redirect') return { reason: 'redirect_refused', breaker: true, clear_capabilities: true, retry_after_ms: null };
  let code: unknown;
  try { code = (JSON.parse(input.body ?? '') as { error?: { code?: unknown } } | null)?.error?.code; } catch { /* not JSON */ }
  if (typeof code !== 'string' || !REASON.test(code)) return classifyFailure({ transport: 'error' });
  if (code === 'request_timeout') return classifyFailure({ transport: 'timeout' });
  const status = input.status ?? 0, retry = typeof input.retry_after === 'string' ? input.retry_after.trim() : '';
  return codeOutcome(code, status, (status === 429 || status === 503) && /^\d+$/.test(retry)
    ? Math.min(Number(retry) * 1000, MIDDLEWARE_DEFAULTS.retry_after_cap_ms) : null);
}

/** Spec §10 breaker: opens on 5 consecutive failures or 10 of the last 20 outcomes, stays open 30 s, then admits one probe. */
export class CircuitBreaker {
  #state: BreakerState = 'closed';
  #openedAt = 0;
  #probing = false;
  #consecutive = 0;
  #window: boolean[] = [];
  constructor(readonly params: Readonly<BreakerParams> = BREAKER_DEFAULTS) {}
  get state(): BreakerState { return this.#state; }
  /** Call immediately before a call's first network request. False means bypass with `circuit_open`. */
  allow(nowMs: number): boolean {
    if (this.#state === 'open') {
      if (nowMs - this.#openedAt < this.params.open_ms) return false;
      this.#state = 'half_open'; this.#probing = false;
    }
    if (this.#state === 'half_open') { if (this.#probing) return false; this.#probing = true; }
    return true;
  }
  /** Exactly one outcome per admitted call: `failure` iff its reason counts toward the breaker. */
  record(result: 'success' | 'failure', nowMs: number): void {
    const failed = result === 'failure';
    if (this.#state === 'half_open') {
      this.#probing = false;
      if (failed) { this.#state = 'open'; this.#openedAt = nowMs; } else { this.#state = 'closed'; this.#consecutive = 0; this.#window = []; }
      return;
    }
    if (this.#state === 'open') return;
    this.#consecutive = failed ? this.#consecutive + 1 : 0;
    this.#window.push(failed);
    if (this.#window.length > this.params.window_size) this.#window.shift();
    if (failed && (this.#consecutive >= this.params.consecutive_failures ||
      (this.#window.length === this.params.window_size && this.#window.filter(Boolean).length >= this.params.window_failures))) {
      this.#state = 'open'; this.#openedAt = nowMs;
    }
  }
}

/** Spec §10: optimize = override, else capabilities `deadline_ms` (capped at 5000), else 500; retrieve/delete =
 * override, else `retrieve_deadline_ms` (capped at 30000), else 5000. Every result is capped at 2**31 - 1 ms, the
 * largest delay setTimeout honors. */
export function resolveDeadlines(options: { readonly deadlineMs?: number | null | undefined; readonly retrieveDeadlineMs?: number | null | undefined },
  limits?: { readonly deadline_ms?: unknown; readonly retrieve_deadline_ms?: unknown } | null): { optimizeMs: number; retrieveMs: number } {
  const d = MIDDLEWARE_DEFAULTS;
  const pick = (override: unknown, limit: unknown, cap: number, fallback: number) =>
    Math.min(positive(override) ? override : positive(limit) ? Math.min(limit, cap) : fallback, d.timer_cap_ms);
  return { optimizeMs: pick(options.deadlineMs, limits?.deadline_ms, d.deadline_cap_ms, d.bootstrap_deadline_ms),
    retrieveMs: pick(options.retrieveDeadlineMs, limits?.retrieve_deadline_ms, d.retrieve_deadline_cap_ms, d.retrieve_deadline_ms) };
}

/** Spec §11: previously replaced keys first (input order), then newest first; first-fit; results in input order. */
export function planBudget(items: readonly BudgetItem[], limits: { readonly maxSegments: number; readonly maxBytes: number }, replaced: Iterable<string> = []): BudgetResult {
  const prior = new Set(replaced), admitted = new Set<string>();
  let used = 0;
  for (const item of [...items.filter(i => prior.has(i.key)), ...items.filter(i => !prior.has(i.key)).reverse()]) {
    if (admitted.size < limits.maxSegments && used + item.bytes <= limits.maxBytes) { admitted.add(item.id); used += item.bytes; }
  }
  return { admitted: items.filter(i => admitted.has(i.id)).map(i => i.id), skipped: items.filter(i => !admitted.has(i.id)).map(i => i.id) };
}

/** Spec §11: the largest head length k with k <= maxItems and sum(sizes[0..k)) <= maxBytes. */
export function manifestWindow(sizes: readonly number[], maxItems: number, maxBytes: number): number {
  let k = 0, total = 0;
  while (k < sizes.length && k < maxItems && (total += sizes[k]!) <= maxBytes) k++;
  return k;
}

/** Spec §11: manifest stand-in for an image/bytes part. Never a reason to bypass. */
export async function opaqueManifestValue(value: unknown): Promise<OpaqueManifestValue> {
  const bytes = value instanceof ArrayBuffer ? new Uint8Array(value)
    : ArrayBuffer.isView(value) ? new Uint8Array(value.buffer, value.byteOffset, value.byteLength) : null;
  return { caveman_opaque: bytes ? await sha256(bytes) : typeof value === 'string' && value.isWellFormed() ? await sha256(value) : 'unhashable' };
}

const ENDPOINT = /^(https?):\/\/(\[[0-9a-f:.]+\]|[a-z0-9_.-]+)(?::([1-9][0-9]{0,4}))?((?:\/[A-Za-z0-9._~-]+)*)\/?$/i;
const OCTET = /^(0|[1-9][0-9]{0,2})$/;
/** Spec §15 host grammar: a bracketed IPv6 literal, a canonical dotted quad, or a name that does not end in a number
 * (127.1, 0x7f.1 and 2130706433 are IPv4 to URL parsers; they are refused, never reinterpreted). */
function endpointHost(host: string): boolean {
  const labels = (host.endsWith('.') ? host.slice(0, -1) : host).split('.'), last = labels.at(-1) ?? '';
  if (host.startsWith('[') || !labels.every(Boolean)) return host.startsWith('[');
  if (!/^\d+$/.test(last) && !last.startsWith('0x')) return true;
  return !host.endsWith('.') && labels.length === 4 && labels.every(x => OCTET.test(x) && Number(x) <= 255);
}

/** Spec §15: the base URL (prefix preserved, trailing `/`) that routes are appended to. Throws MiddlewareError with
 * `invalid_endpoint`, `remote_content_not_enabled` or `insecure_transport_not_enabled`. Strict: whitespace or control
 * characters anywhere, percent-encoding, userinfo, query, fragment, an empty or leading-zero port and numeric host
 * shorthands are all `invalid_endpoint`. Scheme and host are lowercased; nothing else is rewritten. */
export function resolveEndpoint(endpoint: unknown, options: { readonly allowRemoteContent?: boolean | undefined; readonly allowInsecureTransport?: boolean | undefined } = {}): string {
  // Validate the raw text: URL parsing would trim whitespace, drop tabs, resolve `..` and reinterpret numeric hosts.
  const m = typeof endpoint === 'string' ? ENDPOINT.exec(endpoint) : null;
  const [scheme = '', host = '', port = '', path = ''] = m ? [m[1]!.toLowerCase(), m[2]!.toLowerCase(), m[3] ?? '', m[4] ?? ''] : [];
  let url: URL;
  try { url = new URL(`${scheme}://${host}`); } catch { throw new MiddlewareError('invalid_endpoint'); }
  if (!m || Number(port) > 65535 || !endpointHost(host) || path.split('/').some(s => s === '.' || s === '..')) throw new MiddlewareError('invalid_endpoint');
  if (!LOOPBACK.has(url.hostname)) {
    if (!options.allowRemoteContent) throw new MiddlewareError('remote_content_not_enabled');
    if (scheme === 'http' && !options.allowInsecureTransport) throw new MiddlewareError('insecure_transport_not_enabled');
  }
  return `${scheme}://${host}${port ? `:${port}` : ''}${path}/`;
}

const isIp = (host: string) => /^\d{1,3}(\.\d{1,3}){3}$/.test(host) || host.includes(':');
/** Spec §15: the proxy for `url` from http(s)_proxy / no_proxy (lowercase wins, empty = unset), else null. */
export function resolveProxy(url: string, env: Readonly<Record<string, string | undefined>>): string | null {
  let target: URL;
  try { target = new URL(url); } catch { return null; }
  if (LOOPBACK.has(target.hostname)) return null;
  const name = target.protocol === 'https:' ? 'https_proxy' : 'http_proxy';
  const proxy = env[name] || env[name.toUpperCase()] || null;
  if (!proxy) return null;
  const host = target.hostname.replace(/^\[|\]$/g, ''), port = target.port || (target.protocol === 'https:' ? '443' : '80');
  for (const raw of (env['no_proxy'] || env['NO_PROXY'] || '').split(',')) {
    const entry = raw.trim().toLowerCase();
    if (entry === '*') return null;
    const m = /^\[([^\]]+)\](?::(\d+))?$/.exec(entry) ?? (entry.split(':').length === 2 ? /^([^:]+):(\d+)$/.exec(entry) : null);
    const suffix = (m ? m[1]! : entry).replace(/^\./, '');
    if (!suffix || (m?.[2] !== undefined && m[2] !== port)) continue;
    if (host === suffix || (!isIp(host) && !isIp(suffix) && host.endsWith(`.${suffix}`))) return null;
  }
  return proxy;
}

const warned = new Set<string>();
/** One `console.warn` per (adapter, reason) per process, bounded at 1024 pairs; returns whether it logged. The line
 * carries only `adapter=<id or ->` and `reason=<code>`: never content, scope values, handles or credentials. */
export function warnOnce(adapter: unknown, reason: unknown): boolean {
  if (typeof reason === 'string' && reasonPolicy(reason)?.warn_once === false) return false;
  const a = isToken(adapter) ? adapter : '-', r = typeof reason === 'string' && REASON.test(reason) ? reason : 'unknown_reason';
  // ponytail: a full set stops logging new pairs instead of evicting; 1024 is far past real adapter x reason counts.
  if (warned.has(`${a} ${r}`) || warned.size >= MIDDLEWARE_DEFAULTS.warn_once_entries) return false;
  warned.add(`${a} ${r}`);
  console.warn(`Caveman middleware passed content through unchanged: adapter=${a} reason=${r}`);
  return true;
}
