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
  limits: {
    deadline_ms: number; request_bytes: number; segment_bytes: number; page_bytes: number;
    /** Protocol 1.1 (optional; absent on runtime bin-v1.1.8). */
    retrieve_deadline_ms?: number; queue_depth?: number; retrieve_queue_depth?: number;
    max_segments?: number; max_manifest_items?: number; receipt_bytes?: number; quota_requests_per_minute?: number;
  };
  persistent: boolean;
  recovery: boolean;
  retention_seconds: number;
  trust_mode: string;
  mode: 'record' | 'compress';
  /** Protocol 1.1. A missing `features` means a protocol 1.0 runtime. */
  protocol?: { min: number; max: number };
  features?: string[];
  max_retention_seconds?: number;
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
  /** Local candidate accounting for the decision event (spec §14). Set by optimize(); absent on adapter-built values. */
  counts?: Readonly<DecisionCounts>;
  /** optimize() duration in whole milliseconds. */
  latencyMs?: number;
  /** Build of the runtime whose capabilities or plan produced this outcome. */
  runtimeBuild?: string | null;
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

/* ---- Protocol 1.1 (docs/technical/middleware-protocol.md). Values are pinned by
 * packages/sdk/parity/middleware-v1_1.fixtures.json `constants` / `reason_catalog`. */
export const PROTOCOL_RANGE = Object.freeze({ min: 1, max: 1 });
export const MIDDLEWARE_FEATURES_HEADER = 'Caveman-Middleware-Features';
export const MIDDLEWARE_CLIENT_HEADER = 'Caveman-Middleware-Client';
export const MIDDLEWARE_CLIENT_PRODUCT = 'caveman-sdk-typescript';
/** Package version sent in `Caveman-Middleware-Client`; a test pins it to package.json. */
export const SDK_VERSION = '1.1.0';
export type Feature = 'http_status_v2' | 'originals_lifecycle' | 'revision_tolerant' | 'tolerant_reader';
/** Every feature this SDK understands, sorted. */
export const KNOWN_FEATURES: readonly Feature[] = Object.freeze(['http_status_v2', 'originals_lifecycle', 'revision_tolerant', 'tolerant_reader']);
/** Features the client asks the runtime to switch on; sent on every request. */
export const CLIENT_FEATURES: readonly Feature[] = Object.freeze(['http_status_v2', 'revision_tolerant']);
export const CLIENT_FEATURES_HEADER_VALUE = 'http_status_v2, revision_tolerant';
/** K5: only replacements whose transform declares one of these recovery kinds apply. */
export const CLIENT_RECOVERY_ALLOWLIST: readonly string[] = Object.freeze(['exact_ccr']);
export const RECOVERY_MARKER_PREFIX = '[caveman: shortened; exact original via caveman_retrieve handle=';
export const RECOVERY_HANDLE_PATTERN = '^cmw_[a-f0-9]{48}$';
export const REASON_PATTERN = '^[a-z][a-z0-9_]{0,63}$';
export const SCOPE_TOKEN_PATTERN = '^[A-Za-z0-9._:/-]{1,256}$';
export const SCOPE_HASH_PREFIX = 'h-';
export const SCOPE_HASH_HEX_CHARS = 32;
export const DEFAULT_BRANCH_ID = 'main';
export const DEFAULT_CACHE_EPOCH = '0';
export const MIDDLEWARE_DEFAULTS = Object.freeze({
  bootstrap_deadline_ms: 500, retrieve_deadline_ms: 5000, max_concurrency: 16, max_segments: 256,
  max_manifest_items: 4096, manifest_bytes: 2 << 20, receipt_bytes: 16384, replaced_memory_entries: 4096,
  retry_after_cap_ms: 30000, warn_once_entries: 1024,
});
export type BreakerState = 'closed' | 'open' | 'half_open';
export interface BreakerParams { consecutive_failures: number; window_size: number; window_failures: number; open_ms: number }
export const BREAKER_DEFAULTS: Readonly<BreakerParams> = Object.freeze({ consecutive_failures: 5, window_size: 20, window_failures: 10, open_ms: 30000 });

/** Client-visible reason codes (K7). `strict`: raise = optimize()/adapter call raises in strict
 * mode; ready = surfaced only by ready()/preflight(); none = never an error. */
export type ReasonCode =
  | 'unsupported_version' | 'version_unverified' | 'version_unavailable' | 'invalid_scope' | 'payload_budget' | 'opaque_part'
  | 'recovery_unbound' | 'recovery_name_conflict' | 'adapter_error' | 'provider_state_retained' | 'capacity' | 'circuit_open'
  | 'deadline' | 'runtime_unavailable' | 'invalid_plan' | 'unknown_capability' | 'no_candidate' | 'closed'
  | 'unsupported_shape' | 'redirect_refused' | 'unauthorized' | 'forbidden_origin' | 'forbidden_namespace' | 'invalid_request'
  | 'payload_limit' | 'not_found' | 'deleted' | 'expired' | 'epoch_changed' | 'identity_conflict' | 'quota_exceeded'
  | 'not_smaller' | 'cache_state_unavailable' | 'recovery_unavailable' | 'protected' | 'record' | 'eligible' | 'disabled'
  | 'invalid_endpoint' | 'remote_content_not_enabled' | 'insecure_transport_not_enabled' | 'invalid_configuration';
export interface ReasonPolicy { readonly breaker: boolean; readonly warn_once: boolean; readonly strict: 'raise' | 'ready' | 'none' }
export const REASON_CATALOG: Readonly<Record<ReasonCode, ReasonPolicy>> = Object.freeze({
  adapter_error: { breaker: false, warn_once: true, strict: 'raise' },
  cache_state_unavailable: { breaker: false, warn_once: true, strict: 'none' },
  capacity: { breaker: false, warn_once: true, strict: 'raise' },
  circuit_open: { breaker: false, warn_once: true, strict: 'raise' },
  closed: { breaker: false, warn_once: true, strict: 'none' },
  deadline: { breaker: true, warn_once: true, strict: 'raise' },
  deleted: { breaker: false, warn_once: true, strict: 'raise' },
  disabled: { breaker: false, warn_once: false, strict: 'none' },
  eligible: { breaker: false, warn_once: false, strict: 'none' },
  epoch_changed: { breaker: false, warn_once: true, strict: 'none' },
  expired: { breaker: false, warn_once: false, strict: 'none' },
  forbidden_namespace: { breaker: false, warn_once: true, strict: 'raise' },
  forbidden_origin: { breaker: false, warn_once: true, strict: 'raise' },
  identity_conflict: { breaker: false, warn_once: true, strict: 'none' },
  insecure_transport_not_enabled: { breaker: false, warn_once: true, strict: 'ready' },
  invalid_configuration: { breaker: false, warn_once: true, strict: 'ready' },
  invalid_endpoint: { breaker: false, warn_once: true, strict: 'ready' },
  invalid_plan: { breaker: true, warn_once: true, strict: 'raise' },
  invalid_request: { breaker: false, warn_once: true, strict: 'raise' },
  invalid_scope: { breaker: false, warn_once: true, strict: 'raise' },
  no_candidate: { breaker: false, warn_once: false, strict: 'none' },
  not_found: { breaker: false, warn_once: true, strict: 'raise' },
  not_smaller: { breaker: false, warn_once: false, strict: 'none' },
  opaque_part: { breaker: false, warn_once: false, strict: 'none' },
  payload_budget: { breaker: false, warn_once: true, strict: 'none' },
  payload_limit: { breaker: false, warn_once: true, strict: 'raise' },
  protected: { breaker: false, warn_once: false, strict: 'none' },
  provider_state_retained: { breaker: false, warn_once: true, strict: 'none' },
  quota_exceeded: { breaker: false, warn_once: true, strict: 'raise' },
  record: { breaker: false, warn_once: false, strict: 'none' },
  recovery_name_conflict: { breaker: false, warn_once: true, strict: 'raise' },
  recovery_unavailable: { breaker: false, warn_once: true, strict: 'raise' },
  recovery_unbound: { breaker: false, warn_once: true, strict: 'none' },
  redirect_refused: { breaker: true, warn_once: true, strict: 'raise' },
  remote_content_not_enabled: { breaker: false, warn_once: true, strict: 'ready' },
  runtime_unavailable: { breaker: true, warn_once: true, strict: 'raise' },
  unauthorized: { breaker: false, warn_once: true, strict: 'raise' },
  unknown_capability: { breaker: false, warn_once: true, strict: 'raise' },
  unsupported_shape: { breaker: false, warn_once: true, strict: 'raise' },
  unsupported_version: { breaker: false, warn_once: true, strict: 'ready' },
  version_unavailable: { breaker: false, warn_once: true, strict: 'ready' },
  version_unverified: { breaker: false, warn_once: true, strict: 'none' },
});

export interface EffectiveLimits {
  deadline_ms: number; request_bytes: number; segment_bytes: number; page_bytes: number;
  retrieve_deadline_ms: number; max_segments: number; max_manifest_items: number; receipt_bytes: number;
  queue_depth: number | null; retrieve_queue_depth: number | null; quota_requests_per_minute: number | null;
}
/** Tolerant parse of a capabilities document (K1/K2). */
export interface CapabilitiesView {
  readonly capabilities: Capabilities;
  /** True when the document has no `features` array (protocol 1.0 runtime). */
  readonly legacy: boolean;
  /** Server features this client understands, sorted. */
  readonly features: readonly Feature[];
  /** Transforms this client may request: well-formed, deterministic, allowlisted recovery, first of each id. */
  readonly transforms: readonly Transform[];
  /** Unrecognized runtime modes read as 'record'. */
  readonly mode: 'record' | 'compress';
  readonly limits: EffectiveLimits;
  readonly max_retention_seconds: number | null;
}

/** Input to client failure classification (K4/K8). `body` is the raw response text. */
export interface FailureInput { transport: 'response' | 'error' | 'timeout' | 'redirect'; status?: number; body?: string; retry_after?: string | null }
export interface FailureOutcome { reason: string; breaker: boolean; clear_capabilities: boolean; retry_after_ms: number | null }

/** K10 budget selection input; `key` = `${original_sha256}:${segment_id}`, `bytes` = wire bytes of the segment plus one. */
export interface BudgetItem { id: string; key: string; bytes: number }
export interface BudgetResult { admitted: string[]; skipped: string[] }

export interface ErrorEnvelope { schema_version: 1; error: { code: string } }
export interface DeleteCounts { scopes: number; choices: number; grants: number; originals: number }
/** Protocol 1.0 runtimes answer `originals_deleted: false` without `deleted`. */
export interface SessionDeleteResult { schema_version: 1; status: 'revoked'; originals_deleted: boolean; deleted?: DeleteCounts }
export interface ReceiptResult { schema_version: 1; status: 'recorded'; basis: 'client_observed'; verified_saved_usd: 0 }

export interface DecisionCounts {
  candidates: number; sent: number; protected: number; opaque: number; unsupported: number;
  budget_skipped: number; skipped: number; replaced: number; reused: number;
}
/** One content-free event per report() (K9). Keys are identical in every SDK. */
export interface DecisionEvent {
  readonly schema_version: 1;
  readonly status: CallReport['status'];
  readonly reason: string;
  readonly adapter: string | null;
  readonly logical_call_id: string | null;
  readonly attempt_id: string | null;
  readonly transform_ids: readonly string[];
  readonly latency_ms: number;
  readonly counts: Readonly<DecisionCounts>;
  readonly runtime_build: string | null;
  readonly cache_continuity: 'persistent_choices' | 'unavailable' | 'off';
}
/** Manifest stand-in for an image/bytes part: sha256 hex of its bytes (or UTF-8 string), else 'unhashable'. */
export interface OpaqueManifestValue { caveman_opaque: string }

/** OTel names (K9). Spans are CLIENT kind; durations are seconds. */
export const OTEL = Object.freeze({
  spans: Object.freeze({ optimize: 'caveman.middleware.optimize', retrieve: 'caveman.middleware.retrieve', receipt: 'caveman.middleware.receipt' }),
  decisions_counter: 'caveman.middleware.decisions',
  duration_histogram: 'caveman.middleware.duration',
  attribute_prefix: 'caveman.middleware.',
  usage: Object.freeze({
    input_tokens: 'gen_ai.usage.input_tokens', output_tokens: 'gen_ai.usage.output_tokens',
    cache_read_tokens: 'gen_ai.usage.cache_read.input_tokens', cache_write_tokens: 'gen_ai.usage.cache_creation.input_tokens',
  }),
});
