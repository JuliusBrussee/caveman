/** Version 1 compression-only contract; schemas live in packages/shared/contracts. */
export interface Scope {
  namespace: string;
  session_id: string;
  branch_id: string;
  cache_epoch: string;
}
export interface Adapter {
  id: string;
  version: string;
  framework_version: string;
  serialization_revision: string;
}
export interface Segment {
  id: string;
  kind: 'tool_result' | 'artifact';
  cache_region: 'frozen_prefix' | 'live_zone' | 'uncached';
  content: string;
  sha256: string;
  source_id: string;
  protected: boolean;
  opaque: boolean;
}
export interface Candidate {
  id: string;
  content: string;
  sourceId?: string;
  kind?: Segment['kind'];
  cacheRegion?: Segment['cache_region'];
  protected?: boolean;
  opaque?: boolean;
}
export interface ManifestItem { id: string; sha256: string }
export interface ModelIdentity { provider: string; id: string; protocol: string }
export interface Transform {
  transform_id: string;
  implementation_version: string;
  recovery: string;
  eligible_segment_kinds: string[];
  deterministic: boolean;
}
export interface Capabilities {
  schema_version: 1;
  runtime_build: string;
  policy_revision: string;
  transforms: Transform[];
  limits: { deadline_ms: number; request_bytes: number; segment_bytes: number; page_bytes: number };
  persistent: boolean;
  recovery: boolean;
  retention_seconds: number;
  trust_mode: string;
  mode: 'record' | 'compress';
}
/** Startup discovery only; readiness does not establish savings or task quality. */
export interface PreflightReport {
  readonly schema_version: 1;
  readonly status: 'ready' | 'disabled' | 'unavailable';
  readonly reason: string;
  readonly configured_mode: 'off' | 'record' | 'compress';
  readonly runtime_mode: 'record' | 'compress' | null;
  readonly runtime_build: string | null;
  readonly policy_revision: string | null;
  readonly persistent: boolean | null;
  readonly recovery: boolean | null;
  readonly action: string;
}
export interface BindingWire { id: string; kind: 'host_tool' | 'source_reader'; tool_name: 'caveman_retrieve'; overhead_text: string }
export interface OptimizeRequest {
  schema_version: 1;
  request_id: string;
  logical_call_id: string;
  attempt_id: string;
  idempotency_key: string;
  scope: Scope;
  sequence: number;
  adapter: Adapter;
  model: ModelIdentity | null;
  mode: 'record' | 'compress';
  policy: { revision: string; transforms: string[] };
  segments: Segment[];
  context_manifest: ManifestItem[];
  recovery_binding: BindingWire | null;
}
export interface Replacement {
  segment_id: string;
  source_id: string;
  original_sha256: string;
  text: string;
  sha256: string;
  transform_id: string;
  transform_version: string;
  recovery_handle: string;
  tokens_before: number;
  tokens_after: number;
  reused: boolean;
  unique_original: boolean;
}
export interface Plan {
  schema_version: 1;
  request_id: string;
  input_digest: string;
  runtime_build: string;
  policy_revision: string;
  replacement_set_id: string;
  status: 'optimized' | 'bypassed' | 'record';
  reason: string;
  replacements: Replacement[];
  skipped: { segment_id: string; reason: string }[];
  measurement: {
    basis: 'inferred'; tokenizer: string; scope: 'segment';
    tokens_before: number; tokens_after: number; unique_tokens_reduced: number;
    recovery_overhead_tokens: number; overhead_coverage: string; verified_saved_usd: 0;
  };
  stability: { native: 'persistent_choices' | 'unavailable'; provider_bytes: 'unobserved'; provider_cache_hits: 'unobserved' };
  recovery: { binding_id: string; available: boolean; persistent: boolean; expires_at: number };
}
export interface Optimization {
  status: Plan['status'] | 'off';
  reason: string;
  replacements: readonly Replacement[];
  plan: Plan | null;
  request: OptimizeRequest | null;
  cacheContinuity: 'persistent_choices' | 'unavailable' | 'off';
}
/** Final native-call decision. Contains no source text or provider credentials. */
export interface CallReport {
  readonly schema_version: 1;
  readonly status: 'applied' | 'reused' | 'skipped' | 'recorded' | 'disabled';
  readonly reason: string;
  readonly transform_ids: readonly string[];
  readonly replacement_count: number;
  readonly reused_count: number;
  readonly adapter: string | null;
  readonly logical_call_id: string | null;
  readonly attempt_id: string | null;
}
export interface RetrieveArgs { handle: string; offset?: number; limit?: number; query?: string }
export interface RecoveryPage {
  schema_version: 1;
  handle: string;
  source_id: string;
  text: string;
  original_sha256: string;
  total_bytes: number;
  complete: boolean;
  kind: 'original_page' | 'excerpt';
  offset: number;
  next_offset: number | null;
}
export interface RecoveryBinding {
  readonly id: string;
  readonly scope: Scope;
  readonly name: 'caveman_retrieve';
  readonly description: string;
  readonly inputSchema: Readonly<Record<string, unknown>>;
  readonly execute: (args: RetrieveArgs, options?: { signal?: AbortSignal }) => Promise<RecoveryPage>;
}
export interface Usage {
  provenance: 'client_observed_sdk';
  complete: boolean;
  input_tokens: number | null;
  output_tokens: number | null;
  cache_read_tokens: number | null;
  cache_write_tokens: number | null;
  reasoning_tokens: number | null;
}
export interface Receipt {
  schema_version: 1;
  scope: Scope;
  logical_call_id: string;
  attempt_id: string;
  event_kind: 'dispatch_intent' | 'completed' | 'failed' | 'cancelled';
  plan_id: string | null;
  usage: Usage | null;
  provider_request_sha256: string | null;
}
