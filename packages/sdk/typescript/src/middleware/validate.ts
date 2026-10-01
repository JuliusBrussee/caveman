import { CLIENT_RECOVERY_ALLOWLIST, KNOWN_FEATURES, MIDDLEWARE_DEFAULTS, RECOVERY_MARKER_PREFIX } from './types.js';
import type { Capabilities, CapabilitiesView, EffectiveLimits, Feature, OptimizeRequest, Plan, RecoveryPage, RetrieveArgs, Scope, Transform } from './types.js';

const encoder = new TextEncoder();
export const byteLength = (text: string): number => encoder.encode(text).length;
export async function sha256(value: string | Uint8Array): Promise<string> {
  const bytes = typeof value === 'string' ? encoder.encode(value) : value;
  return Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes as Uint8Array<ArrayBuffer>)), b => b.toString(16).padStart(2, '0')).join('');
}
const K = Uint32Array.from('428a2f98 71374491 b5c0fbcf e9b5dba5 3956c25b 59f111f1 923f82a4 ab1c5ed5 d807aa98 12835b01 243185be 550c7dc3 72be5d74 80deb1fe 9bdc06a7 c19bf174 e49b69c1 efbe4786 0fc19dc6 240ca1cc 2de92c6f 4a7484aa 5cb0a9dc 76f988da 983e5152 a831c66d b00327c8 bf597fc7 c6e00bf3 d5a79147 06ca6351 14292967 27b70a85 2e1b2138 4d2c6dfc 53380d13 650a7354 766a0abb 81c2c92e 92722c85 a2bfe8a1 a81a664b c24b8b70 c76c51a3 d192e819 d6990624 f40e3585 106aa070 19a4c116 1e376c08 2748774c 34b0bcb5 391c0cb3 4ed8aa4a 5b9cca4f 682e6ff3 748f82ee 78a5636f 84c87814 8cc70208 90befffa a4506ceb bef9a3f7 c67178f2'.split(' '), h => parseInt(h, 16));
const rotr = (x: number, n: number) => (x >>> n) | (x << (32 - n));
/** Synchronous SHA-256 hex of UTF-8 text. Scope normalization must stay synchronous (recovery() is), and
 * WebCrypto is async-only while `node:crypto` is unavailable on edge runtimes. Inputs here are short. */
export function sha256Sync(text: string): string {
  const data = encoder.encode(text), m = new Uint8Array(((data.length + 72) >> 6) << 6), v = new DataView(m.buffer), w = new Uint32Array(64);
  m.set(data); m[data.length] = 0x80;
  v.setUint32(m.length - 8, Math.floor(data.length / 0x20000000)); v.setUint32(m.length - 4, data.length * 8 >>> 0);
  const H = Uint32Array.of(0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19);
  for (let o = 0; o < m.length; o += 64) {
    for (let i = 0; i < 64; i++) {
      const a = w[i - 15] ?? 0, b = w[i - 2] ?? 0;
      w[i] = i < 16 ? v.getUint32(o + i * 4) : (rotr(a, 7) ^ rotr(a, 18) ^ (a >>> 3)) + (w[i - 7] ?? 0) + (rotr(b, 17) ^ rotr(b, 19) ^ (b >>> 10)) + (w[i - 16] ?? 0);
    }
    let [a, b, c, d, e, f, g, h] = Array.from(H) as [number, number, number, number, number, number, number, number];
    for (let i = 0; i < 64; i++) {
      const t1 = h + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + ((e & f) ^ (~e & g)) + (K[i] ?? 0) + (w[i] ?? 0) | 0;
      const t2 = (rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b) ^ (a & c) ^ (b & c)) | 0;
      h = g; g = f; f = e; e = d + t1 | 0; d = c; c = b; b = a; a = t1 + t2 | 0;
    }
    [a, b, c, d, e, f, g, h].forEach((x, i) => { H[i] = (H[i] ?? 0) + x; });
  }
  return Array.from(H, x => x.toString(16).padStart(8, '0')).join('');
}
export const isHash = (s: unknown): s is string => typeof s === 'string' && /^[a-f0-9]{64}$/.test(s);
export const isToken = (s: unknown): s is string => typeof s === 'string' && /^[a-zA-Z0-9._:/-]{1,256}$/.test(s);
const integer = (n: unknown): n is number => Number.isSafeInteger(n) && (n as number) >= 0;
export const positive = (n: unknown): n is number => integer(n) && n > 0;
export function scopeKey(scope: Scope): string {
  if (![scope.namespace, scope.session_id, scope.branch_id, scope.cache_epoch].every(isToken)) throw new MiddlewareError('invalid_scope');
  return JSON.stringify([scope.namespace, scope.session_id, scope.branch_id, scope.cache_epoch]);
}
export class MiddlewareError extends Error {
  constructor(readonly code: string, detail?: string | null) { super(`Caveman middleware: ${code}${detail ? `: ${detail}` : ''}`); this.name = 'MiddlewareError'; }
}
const record = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);

/** Tolerant capabilities reader (spec §4). Throws `unsupported_version` only for a document this client cannot use at
 * all; unknown fields, limits, features and transforms are ignored instead of rejecting the document. */
export function parseCapabilities(value: unknown): CapabilitiesView {
  const c = value as Capabilities & Record<string, unknown>;
  const protocol = record(c?.protocol) && integer(c.protocol.min) && integer(c.protocol.max) ? c.protocol : null;
  const limits = record(c?.limits) ? c.limits as Record<string, unknown> : null;
  if (!record(c) || c.schema_version !== 1 || (protocol && !(protocol.min <= 1 && 1 <= protocol.max)) || !isToken(c.policy_revision) ||
    typeof c.runtime_build !== 'string' || !Array.isArray(c.transforms) || !limits ||
    !['deadline_ms', 'request_bytes', 'segment_bytes', 'page_bytes'].every(k => positive(limits[k])) ||
    typeof c.persistent !== 'boolean' || typeof c.recovery !== 'boolean' || !integer(c.retention_seconds)) throw new MiddlewareError('unsupported_version');
  const ids = new Set<string>();
  const transforms = (c.transforms as unknown[]).filter((t): t is Transform => {
    const ok = record(t) && isToken(t.transform_id) && isToken(t.implementation_version) && Array.isArray(t.eligible_segment_kinds) &&
      t.deterministic === true && CLIENT_RECOVERY_ALLOWLIST.includes(t.recovery as string) && !ids.has(t.transform_id);
    if (ok) ids.add(t.transform_id as string);
    return ok;
  });
  const limit = (key: string, fallback: number | null) => positive(limits[key]) ? limits[key] as number : fallback;
  const d = MIDDLEWARE_DEFAULTS;
  const effective: EffectiveLimits = { deadline_ms: limits['deadline_ms'] as number, request_bytes: limits['request_bytes'] as number,
    segment_bytes: limits['segment_bytes'] as number, page_bytes: limits['page_bytes'] as number,
    retrieve_deadline_ms: limit('retrieve_deadline_ms', d.retrieve_deadline_ms)!, max_segments: limit('max_segments', d.max_segments)!,
    max_manifest_items: limit('max_manifest_items', d.max_manifest_items)!, receipt_bytes: limit('receipt_bytes', d.receipt_bytes)!,
    queue_depth: limit('queue_depth', null), retrieve_queue_depth: limit('retrieve_queue_depth', null), quota_requests_per_minute: limit('quota_requests_per_minute', null) };
  const features = Array.isArray(c.features) ? KNOWN_FEATURES.filter(f => (c.features as unknown[]).includes(f)) : [];
  return Object.freeze({ capabilities: c, legacy: !Array.isArray(c.features), features: Object.freeze(features as Feature[]), transforms: Object.freeze(transforms),
    mode: c.mode === 'compress' ? 'compress' : 'record', limits: Object.freeze(effective),
    max_retention_seconds: positive(c.max_retention_seconds) ? c.max_retention_seconds : null });
}

/** The capabilities fields plan validation reads; a parsed CapabilitiesView or a raw document both qualify. */
export type PlanCapabilities = { readonly transforms: readonly Transform[]; readonly mode: string; readonly limits: { readonly segment_bytes: number } };

/** Validate every leaf before an adapter is allowed to apply any of them (spec §7). Total: any malformed value,
 * including one that would throw mid-check (a null replacement), is `invalid_plan`, never an adapter error. */
export async function validatePlan(value: unknown, request: OptimizeRequest, inputDigest: string, caps: PlanCapabilities): Promise<Plan> {
  try { return await checkPlan(value, request, inputDigest, caps); } catch { throw new MiddlewareError('invalid_plan'); }
}

async function checkPlan(value: unknown, request: OptimizeRequest, inputDigest: string, caps: PlanCapabilities): Promise<Plan> {
  const p = value as Plan;
  const invalid = () => { throw new MiddlewareError('invalid_plan'); };
  if (!p || p.schema_version !== 1 || p.request_id !== request.request_id || p.input_digest !== inputDigest ||
    !isToken(p.policy_revision) || !isHash(p.replacement_set_id) || !['optimized','bypassed','record'].includes(p.status) ||
    !isToken(p.reason) || !Array.isArray(p.replacements) || !Array.isArray(p.skipped) || !p.measurement || !p.stability || !p.recovery) invalid();
  const m = p.measurement;
  if (m.basis !== 'inferred' || m.scope !== 'segment' || m.verified_saved_usd !== 0 || typeof m.tokenizer !== 'string' ||
    ![m.tokens_before,m.tokens_after,m.unique_tokens_reduced,m.recovery_overhead_tokens].every(integer) || m.tokens_after > m.tokens_before ||
    p.stability.provider_bytes !== 'unobserved' || p.stability.provider_cache_hits !== 'unobserved' || !['persistent_choices','unavailable'].includes(p.stability.native) ||
    typeof p.recovery.available !== 'boolean' || typeof p.recovery.persistent !== 'boolean' || !integer(p.recovery.expires_at)) invalid();
  const seen = new Set<string>();
  const credited = new Set<string>();
  const segments = new Map(request.segments.map(s => [s.id, s]));
  let reduction = 0, unique = 0;
  for (const r of p.replacements) {
    const s = segments.get(r.segment_id);
    const t = caps.transforms.find(t => t.transform_id === r.transform_id);
    // K5: allowlisted recovery only, marker + handle on every replacement, strictly fewer UTF-8 bytes than the original.
    if (!s || !t || !CLIENT_RECOVERY_ALLOWLIST.includes(t.recovery) || seen.has(r.segment_id) || r.original_sha256 !== s.sha256 || r.source_id !== s.source_id ||
      !request.policy.transforms.includes(r.transform_id) || !isToken(r.transform_version) || !t.eligible_segment_kinds.includes(s.kind) ||
      s.protected || s.opaque || request.mode === 'record' || caps.mode === 'record' ||
      typeof r.text !== 'string' || !r.text.isWellFormed() || byteLength(r.text) > caps.limits.segment_bytes || byteLength(r.text) >= byteLength(s.content) || !isHash(r.sha256) ||
      r.sha256 !== await sha256(r.text) || !integer(r.tokens_before) || !integer(r.tokens_after) || r.tokens_after >= r.tokens_before || typeof r.reused !== 'boolean' ||
      typeof r.unique_original !== 'boolean' || (r.unique_original && (r.reused || credited.has(r.original_sha256))) ||
      !request.recovery_binding || p.recovery.binding_id !== request.recovery_binding.id || !p.recovery.available || !p.recovery.persistent ||
      typeof r.recovery_handle !== 'string' || !/^cmw_[a-f0-9]{48}$/.test(r.recovery_handle) || !r.text.startsWith(`${RECOVERY_MARKER_PREFIX}${r.recovery_handle}]\n`)) invalid();
    seen.add(r.segment_id);
    reduction += r.tokens_before - r.tokens_after;
    if (r.unique_original) { unique += r.tokens_before - r.tokens_after; credited.add(r.original_sha256); }
  }
  for (const skipped of p.skipped) {
    if (!segments.has(skipped.segment_id) || seen.has(skipped.segment_id) || !isToken(skipped.reason)) invalid();
    seen.add(skipped.segment_id);
  }
  if (seen.size !== segments.size || m.tokens_before - m.tokens_after !== reduction || m.unique_tokens_reduced !== unique ||
    (p.replacements.length > 0 && (p.status !== 'optimized' || reduction <= m.recovery_overhead_tokens)) ||
    (p.status === 'optimized' && p.replacements.length === 0)) invalid();
  return p;
}

export async function validatePage(value: unknown, args: RetrieveArgs, maxBytes: number): Promise<RecoveryPage> {
  const p = value as RecoveryPage;
  if (!p || p.schema_version !== 1 || p.handle !== args.handle || !isToken(p.source_id) || !isHash(p.original_sha256) ||
    typeof p.text !== 'string' || byteLength(p.text) > maxBytes || !integer(p.total_bytes) || !integer(p.offset) || p.offset !== (args.offset ?? 0) ||
    !['original_page','excerpt'].includes(p.kind) || typeof p.complete !== 'boolean' ||
    !(p.next_offset === null || integer(p.next_offset))) throw new MiddlewareError('invalid_recovery');
  const length = byteLength(p.text);
  if (p.kind === 'original_page' && (p.offset + length > p.total_bytes ||
    p.next_offset !== (p.offset + length < p.total_bytes ? p.offset + length : null) ||
    p.complete !== (p.offset === 0 && length === p.total_bytes))) throw new MiddlewareError('invalid_recovery');
  if (p.kind === 'excerpt' && (p.complete || !args.query || p.next_offset !== 0)) throw new MiddlewareError('invalid_recovery');
  if (p.complete && await sha256(p.text) !== p.original_sha256) throw new MiddlewareError('invalid_recovery');
  return p;
}
