import type { Capabilities, OptimizeRequest, Plan, RecoveryPage, RetrieveArgs, Scope } from './types.js';

const encoder = new TextEncoder();
export const byteLength = (text: string): number => encoder.encode(text).length;
export async function sha256(text: string): Promise<string> {
  return Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', encoder.encode(text))), b => b.toString(16).padStart(2, '0')).join('');
}
export const isHash = (s: unknown): s is string => typeof s === 'string' && /^[a-f0-9]{64}$/.test(s);
export const isToken = (s: unknown): s is string => typeof s === 'string' && /^[a-zA-Z0-9._:/-]{1,256}$/.test(s);
const integer = (n: unknown): n is number => Number.isSafeInteger(n) && (n as number) >= 0;
export function scopeKey(scope: Scope): string {
  if (![scope.namespace, scope.session_id, scope.branch_id, scope.cache_epoch].every(isToken)) throw new MiddlewareError('invalid_scope');
  return JSON.stringify([scope.namespace, scope.session_id, scope.branch_id, scope.cache_epoch]);
}
export class MiddlewareError extends Error {
  constructor(readonly code: string) { super(`Caveman middleware: ${code}`); this.name = 'MiddlewareError'; }
}
export function validateCapabilities(value: unknown): Capabilities {
  const c = value as Capabilities;
  if (!c || c.schema_version !== 1 || !isToken(c.policy_revision) || typeof c.runtime_build !== 'string' ||
    !['record', 'compress'].includes(c.mode) || !Array.isArray(c.transforms) || c.transforms.length > 128 ||
    !c.limits || !Object.values(c.limits).every(n => integer(n) && n > 0) ||
    !integer(c.retention_seconds) || typeof c.recovery !== 'boolean' || typeof c.persistent !== 'boolean') throw new MiddlewareError('unsupported_version');
  const ids = new Set<string>();
  for (const t of c.transforms) {
    if (!isToken(t.transform_id) || ids.has(t.transform_id) || !isToken(t.implementation_version) || !Array.isArray(t.eligible_segment_kinds) ||
      !['exact_ccr', 'none'].includes(t.recovery) || t.deterministic !== true) throw new MiddlewareError('unknown_capability');
    ids.add(t.transform_id);
  }
  return c;
}

/** Validate every leaf before an adapter is allowed to apply any of them. */
export async function validatePlan(value: unknown, request: OptimizeRequest, inputDigest: string, caps: Capabilities): Promise<Plan> {
  const p = value as Plan;
  const invalid = () => { throw new MiddlewareError('invalid_plan'); };
  if (!p || p.schema_version !== 1 || p.request_id !== request.request_id || p.input_digest !== inputDigest ||
    p.policy_revision !== request.policy.revision || !isHash(p.replacement_set_id) || !['optimized','bypassed','record'].includes(p.status) ||
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
    if (!s || !t || seen.has(r.segment_id) || r.original_sha256 !== s.sha256 || r.source_id !== s.source_id ||
      !request.policy.transforms.includes(r.transform_id) || r.transform_version !== t.implementation_version || !t.eligible_segment_kinds.includes(s.kind) ||
      s.protected || s.opaque || request.mode === 'record' || caps.mode === 'record' ||
      typeof r.text !== 'string' || byteLength(r.text) > caps.limits.segment_bytes || !isHash(r.sha256) ||
      r.sha256 !== await sha256(r.text) || !integer(r.tokens_before) || !integer(r.tokens_after) || r.tokens_after >= r.tokens_before || typeof r.reused !== 'boolean' ||
      typeof r.unique_original !== 'boolean' || (r.unique_original && (r.reused || credited.has(r.original_sha256)))) invalid();
    if (t?.recovery === 'exact_ccr' && (!request.recovery_binding || p.recovery.binding_id !== request.recovery_binding.id ||
      !p.recovery.available || !p.recovery.persistent || !/^cmw_[a-f0-9]{48}$/.test(r.recovery_handle) ||
      !r.text.startsWith(`[caveman: shortened; exact original via caveman_retrieve handle=${r.recovery_handle}]\n`))) invalid();
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
