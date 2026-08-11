/**
 * `@caveman/mastra` — the Caveman adapter for Mastra's OpenTelemetry export.
 *
 * LICENSING (read before editing): this package is implemented against Mastra's
 * PUBLIC DOCUMENTATION only. No Mastra source code was read, vendored, or
 * transcribed. Every Mastra fact relied on below carries the doc URL it came
 * from in the comment above it. Where a detail is NOT documented — the Studio
 * per-trace URL, the span attribute that carries Mastra's span type, the keys
 * Mastra uses for token usage on spans that predate its GenAI mapping — this
 * package takes a configuration hook instead of guessing, and says so in the
 * README.
 *
 * Zero runtime dependencies. OpenTelemetry is typed STRUCTURALLY: nothing here
 * imports `@opentelemetry/*`, so the package works against whichever OTel
 * version the host application already runs.
 *
 * Honesty: nothing in this file computes, estimates, or claims a saving. It
 * moves identifiers and operation labels onto spans so Caveman's normalizer can
 * group them, and it fails closed rather than inventing a classification.
 */

import { AsyncLocalStorage } from "node:async_hooks";

// ---------------------------------------------------------------------------
// 1. Exporter configuration
// ---------------------------------------------------------------------------

/**
 * Protocols Mastra documents on its OTel exporter's custom provider config:
 * `'http/json' | 'http/protobuf' | 'grpc'`.
 * Source: https://mastra.ai/docs/observability/tracing/exporters/otel
 */
export type OtlpProtocol = "http/json" | "http/protobuf" | "grpc";

export interface CavemanExporterOptions {
  /** Caveman gateway base URL, e.g. `https://gateway.caveman.so`. */
  baseUrl: string;
  /** Caveman project key (`cave_live_…`). */
  apiKey: string;
  /** Caveman project id — becomes `caveman.project.id`. */
  projectId: string;
  /** Deployment environment — becomes `caveman.environment`. */
  environment: string;
  /** Stable agent slug — becomes `caveman.agent.id` and the `x-cave-agent` header. */
  agentId?: string;
  agentVersion?: string;
  workflowVersion?: string;
  /** Build identity (git sha) — becomes `service.version`. */
  serviceVersion?: string;
  /** Passed back out as `serviceName` for Mastra's `observability.configs.otel` block. */
  serviceName?: string;
  /** Defaults to `"http/json"`; anything else is rejected (see below). */
  protocol?: OtlpProtocol;
  /** Extra headers, merged last so a caller can override anything. */
  headers?: Record<string, string>;
  /** Extra resource attributes, merged last. */
  resourceAttributes?: Record<string, string>;
}

export interface CavemanExporterConfig {
  endpoint: string;
  protocol: OtlpProtocol;
  headers: Record<string, string>;
  resourceAttributes: Record<string, string>;
  /**
   * Ready to spread into Mastra's documented OTel exporter:
   * `new OtelExporter({ provider: { custom: { endpoint, protocol, headers } } })`.
   * Source: https://mastra.ai/docs/observability/tracing/exporters/otel
   */
  provider: { custom: { endpoint: string; protocol: OtlpProtocol; headers: Record<string, string> } };
  serviceName?: string;
}

/**
 * Builds the OTLP settings a Mastra application passes to its telemetry config.
 *
 * The endpoint is Caveman's OTLP route (`POST {baseUrl}/otlp/v1/traces`). That
 * handler decodes protobuf-JSON, not binary protobuf, so `http/json` is the only
 * protocol it can accept — a protobuf or gRPC batch is rejected rather than
 * silently dropped here.
 *
 * Auth: the gateway reads `x-cave-api-key` first and falls back to
 * `authorization` (a `Bearer ` prefix is stripped before the key is parsed), so
 * both headers are emitted and either one alone would authenticate.
 */
export function cavemanExporterConfig(options: CavemanExporterOptions): CavemanExporterConfig {
  const baseUrl = requireBaseUrl(options.baseUrl, "cavemanExporterConfig: baseUrl");
  const apiKey = requireNonEmpty(options.apiKey, "cavemanExporterConfig: apiKey");
  const projectId = requireNonEmpty(options.projectId, "cavemanExporterConfig: projectId");
  const environment = requireNonEmpty(options.environment, "cavemanExporterConfig: environment");
  const protocol = options.protocol ?? "http/json";
  if (protocol !== "http/json") {
    throw new Error(
      `cavemanExporterConfig: protocol ${protocol} is not accepted by ${baseUrl}/otlp/v1/traces; ` +
        "the Caveman OTLP endpoint decodes protobuf-JSON only. Use 'http/json'.",
    );
  }

  const endpoint = `${baseUrl}/otlp/v1/traces`;
  const headers: Record<string, string> = {
    "x-cave-api-key": apiKey,
    authorization: `Bearer ${apiKey}`,
  };
  if (options.agentId) headers["x-cave-agent"] = options.agentId;
  Object.assign(headers, options.headers ?? {});

  // §23.3 of docs/self_learning_implementation_spec.md — the required metadata block.
  const resourceAttributes: Record<string, string> = {
    "caveman.project.id": projectId,
    "caveman.environment": environment,
  };
  if (options.agentId) resourceAttributes["caveman.agent.id"] = options.agentId;
  if (options.agentVersion) resourceAttributes["caveman.agent.version"] = options.agentVersion;
  if (options.workflowVersion) resourceAttributes["caveman.workflow.version"] = options.workflowVersion;
  if (options.serviceVersion) resourceAttributes["service.version"] = options.serviceVersion;
  Object.assign(resourceAttributes, options.resourceAttributes ?? {});

  const config: CavemanExporterConfig = {
    endpoint,
    protocol,
    headers,
    resourceAttributes,
    provider: { custom: { endpoint, protocol, headers } },
  };
  if (options.serviceName) config.serviceName = options.serviceName;
  return config;
}

// ---------------------------------------------------------------------------
// 2. Task context
// ---------------------------------------------------------------------------

export interface TaskContext {
  taskId: string;
  sessionId?: string;
}

const taskStorage = new AsyncLocalStorage<TaskContext>();

/** The task context of the currently executing async scope, if any. */
export function currentTask(): TaskContext | undefined {
  return taskStorage.getStore();
}

/**
 * Runs `fn` with a task (and optional session) bound to the async scope, so the
 * span processor can stamp `caveman.task.id` / `caveman.session.id` onto every
 * Mastra span the agent or workflow produces underneath it.
 */
export function withTask<T>(taskId: string, fn: () => T): T;
export function withTask<T>(taskId: string, sessionId: string | undefined, fn: () => T): T;
export function withTask<T>(
  taskId: string,
  sessionIdOrFn: string | undefined | (() => T),
  maybeFn?: () => T,
): T {
  const fn = typeof sessionIdOrFn === "function" ? sessionIdOrFn : maybeFn;
  if (typeof fn !== "function") throw new Error("withTask: a callback is required");
  const id = requireNonEmpty(taskId, "withTask: taskId");
  const sessionId = typeof sessionIdOrFn === "function" ? undefined : sessionIdOrFn;
  const context: TaskContext = sessionId ? { taskId: id, sessionId } : { taskId: id };
  return taskStorage.run(context, fn);
}

// ---------------------------------------------------------------------------
// 3. Span processor
// ---------------------------------------------------------------------------

/** The structural slice of an OTel span this processor touches. */
export interface MastraSpanLike {
  name?: string;
  attributes?: Record<string, unknown>;
  setAttribute?(key: string, value: string | number | boolean): unknown;
  spanContext?(): { traceId?: string; spanId?: string };
}

/**
 * The Mastra operations this package classifies. `model_step`, `model_chunk`,
 * `workflow_control` and `generic` are deliberately NOT given a
 * `gen_ai.operation.name`: they are sub-spans of an operation, and labelling
 * them would inflate model-call and step counts downstream.
 */
export type CavemanSpanKind =
  | "agent_run"
  | "workflow_run"
  | "workflow_step"
  | "workflow_control"
  | "model_generation"
  | "model_step"
  | "model_chunk"
  | "tool_call"
  | "mcp_tool_call"
  | "generic";

/**
 * Mastra's documented span-type names → the kind this package maps them to.
 *
 * Sources (docs prose only; the enum itself lives in Mastra source and was not
 * read): https://mastra.ai/docs/v0/observability/ai-tracing/overview (AGENT_RUN,
 * GENERIC, LLM_GENERATION, LLM_CHUNK, MCP_TOOL_CALL, TOOL_CALL, WORKFLOW_RUN,
 * WORKFLOW_STEP) and https://mastra.ai/docs/observability/tracing/overview plus
 * https://mastra.ai/reference/observability/tracing/span-filtering
 * (MODEL_GENERATION, MODEL_STEP, MODEL_CHUNK, WORKFLOW_SLEEP and the workflow
 * control-flow types). Mastra's docs point at its source file for the complete
 * enum; anything not listed here falls through to `spanTypeMap` and then to the
 * unmapped counter, and is never guessed at.
 */
export const DOCUMENTED_SPAN_TYPES: Readonly<Record<string, CavemanSpanKind>> = Object.freeze({
  AGENT_RUN: "agent_run",
  WORKFLOW_RUN: "workflow_run",
  WORKFLOW_STEP: "workflow_step",
  WORKFLOW_CONDITIONAL: "workflow_control",
  WORKFLOW_CONDITIONAL_EVAL: "workflow_control",
  WORKFLOW_PARALLEL: "workflow_control",
  WORKFLOW_LOOP: "workflow_control",
  WORKFLOW_SLEEP: "workflow_control",
  MODEL_GENERATION: "model_generation",
  LLM_GENERATION: "model_generation",
  MODEL_STEP: "model_step",
  MODEL_CHUNK: "model_chunk",
  LLM_CHUNK: "model_chunk",
  TOOL_CALL: "tool_call",
  MCP_TOOL_CALL: "mcp_tool_call",
  GENERIC: "generic",
});

/**
 * Mastra's documented OTel span names, used as the second classifier:
 * "chat {model}", "execute_tool {tool_name}", "invoke_agent {agent_id}",
 * "invoke_workflow {workflow_id}".
 * Source: https://mastra.ai/docs/observability/tracing/exporters/otel
 */
const NAME_PREFIXES: ReadonlyArray<readonly [string, CavemanSpanKind]> = [
  ["invoke_agent ", "agent_run"],
  ["invoke_workflow ", "workflow_run"],
  ["execute_tool ", "tool_call"],
  ["chat ", "model_generation"],
];

/** The GenAI operation label emitted per kind. Kinds absent here get none. */
const DEFAULT_OPERATION_NAMES: Readonly<Partial<Record<CavemanSpanKind, string>>> = Object.freeze({
  agent_run: "invoke_agent",
  workflow_run: "invoke_workflow",
  workflow_step: "workflow_step",
  model_generation: "chat",
  tool_call: "execute_tool",
  mcp_tool_call: "execute_tool",
});

/**
 * Attribute keys checked, in order, for Mastra's span type. Mastra documents the
 * type NAMES but not the attribute key its exporter writes them under, so these
 * are candidates: a miss simply falls through to the span-name classifier.
 * Override with `spanTypeAttributes`.
 */
const DEFAULT_SPAN_TYPE_ATTRIBUTES = ["mastra.span.type", "mastra.spanType", "mastra.type"] as const;

/** Source keys copied into `gen_ai.*` when the canonical key is absent. */
export interface UsageAttributeAliases {
  model?: string;
  provider?: string;
  toolName?: string;
  inputTokens?: string;
  outputTokens?: string;
  cachedInputTokens?: string;
}

export interface CavemanSpanProcessorOptions {
  projectId?: string;
  environment?: string;
  agentId?: string;
  agentVersion?: string;
  workflowVersion?: string;
  /** Defaults to {@link currentTask}. Return `undefined` to stamp nothing. */
  resolveTask?: (span: MastraSpanLike) => TaskContext | undefined;
  /** Attribute keys that may carry Mastra's span type. */
  spanTypeAttributes?: readonly string[];
  /** Extra or overriding span-type → kind entries. */
  spanTypeMap?: Readonly<Record<string, CavemanSpanKind>>;
  /** Override the emitted `gen_ai.operation.name` per kind. */
  operationNames?: Readonly<Partial<Record<CavemanSpanKind, string>>>;
  /** Copy non-canonical usage/model keys into `gen_ai.*`. */
  usageAttributes?: UsageAttributeAliases;
  /** Attribute keys that may carry a tool name, checked before the span name. */
  toolNameAttributes?: readonly string[];
  /** How many distinct unmapped span names to retain for diagnostics (default 20). */
  unmappedSampleLimit?: number;
}

export interface CavemanProcessorDiagnostics {
  /** Spans seen at `onEnd`. */
  totalSpans: number;
  /** Spans this processor gave a `gen_ai.operation.name`. */
  mappedSpans: number;
  /** Spans that already carried `gen_ai.operation.name` before this processor. */
  alreadyInstrumentedSpans: number;
  /** Known Mastra spans deliberately left without an operation label. */
  contextOnlySpans: number;
  /** Spans this processor could not classify — passed through untouched. */
  unmappedSpans: number;
  /** A capped sample of distinct unmapped span names. */
  unmappedSpanNames: string[];
}

export interface CavemanSpanProcessor {
  onStart(span: MastraSpanLike, parentContext?: unknown): void;
  onEnd(span: MastraSpanLike): void;
  shutdown(): Promise<void>;
  forceFlush(): Promise<void>;
  diagnostics(): CavemanProcessorDiagnostics;
}

interface Classification {
  kind: CavemanSpanKind | null;
  toolName?: string;
  model?: string;
}

interface SpanState {
  alreadyInstrumented: boolean;
  kind: CavemanSpanKind | null;
  mapped: boolean;
  counted: boolean;
}

/**
 * A span processor that translates Mastra's documented span taxonomy into the
 * attributes Caveman's normalizer reads.
 *
 * Trace and span ids are never touched — the point of the adapter is that a
 * Caveman trace and the original Mastra trace are the same trace.
 *
 * A span it cannot classify is passed through completely unmodified and counted
 * in `diagnostics().unmappedSpans`; that counter is the missing-instrumentation
 * signal (§23.1), not a failure.
 */
export function cavemanSpanProcessor(options: CavemanSpanProcessorOptions = {}): CavemanSpanProcessor {
  const spanTypeAttributes = options.spanTypeAttributes ?? DEFAULT_SPAN_TYPE_ATTRIBUTES;
  // Null prototype: `declared` is attacker-influenceable span data, and a
  // prototype-chain hit ("constructor", "toString") would classify an unknown
  // span and zero the missing-instrumentation counter.
  const spanTypeMap: Record<string, CavemanSpanKind> = Object.assign(
    Object.create(null) as Record<string, CavemanSpanKind>,
    DOCUMENTED_SPAN_TYPES,
    options.spanTypeMap ?? {},
  );
  const operationNames: Partial<Record<CavemanSpanKind, string>> = {
    ...DEFAULT_OPERATION_NAMES,
    ...(options.operationNames ?? {}),
  };
  const resolveTask = options.resolveTask ?? (() => currentTask());
  const toolNameAttributes = options.toolNameAttributes ?? [];
  const sampleLimit = options.unmappedSampleLimit ?? 20;

  const state = new WeakMap<object, SpanState>();
  const diagnostics: CavemanProcessorDiagnostics = {
    totalSpans: 0,
    mappedSpans: 0,
    alreadyInstrumentedSpans: 0,
    contextOnlySpans: 0,
    unmappedSpans: 0,
    unmappedSpanNames: [],
  };

  function classify(span: MastraSpanLike, attrs: Record<string, unknown>): Classification {
    for (const key of spanTypeAttributes) {
      const declared = readString(attrs, key);
      if (!declared) continue;
      const kind = spanTypeMap[declared] ?? spanTypeMap[declared.toUpperCase()];
      if (kind) return withNameHints(kind, span, attrs);
      // A declared-but-unknown Mastra type is honestly unmapped: guessing from
      // the name after the framework told us something else would be worse.
      return { kind: null };
    }
    const name = typeof span.name === "string" ? span.name : "";
    for (const [prefix, kind] of NAME_PREFIXES) {
      if (!name.startsWith(prefix)) continue;
      const suffix = name.slice(prefix.length).trim();
      const hinted: Classification = { kind };
      if (kind === "tool_call" && suffix) hinted.toolName = suffix;
      if (kind === "model_generation" && suffix) hinted.model = suffix;
      return hinted;
    }
    return { kind: null };
  }

  function withNameHints(kind: CavemanSpanKind, span: MastraSpanLike, attrs: Record<string, unknown>): Classification {
    const out: Classification = { kind };
    if (kind === "tool_call" || kind === "mcp_tool_call") {
      const fromAttr = firstString(attrs, toolNameAttributes);
      const fromName = suffixAfter(span.name, "execute_tool ");
      const toolName = fromAttr ?? fromName;
      if (toolName) out.toolName = toolName;
    }
    if (kind === "model_generation") {
      const model = suffixAfter(span.name, "chat ");
      if (model) out.model = model;
    }
    return out;
  }

  function apply(span: MastraSpanLike, prior: SpanState | undefined): SpanState {
    const attrs = span.attributes ?? {};
    const alreadyInstrumented = prior?.alreadyInstrumented ?? Boolean(readString(attrs, "gen_ai.operation.name"));
    const classified = classify(span, attrs);

    if (!alreadyInstrumented && classified.kind === null) {
      // Unknown shape: pass through untouched. No context, no operation, no guess.
      return { alreadyInstrumented: false, kind: null, mapped: false, counted: prior?.counted ?? false };
    }

    stampContext(span, resolveTask(span), options);
    copyUsageAliases(span, attrs, options.usageAttributes);

    let mapped = prior?.mapped ?? false;
    if (!alreadyInstrumented && classified.kind) {
      const operation = operationNames[classified.kind];
      if (operation) {
        setAttribute(span, "gen_ai.operation.name", operation);
        mapped = true;
      }
      if (classified.kind === "tool_call" || classified.kind === "mcp_tool_call") {
        if (classified.toolName) setAttribute(span, "gen_ai.tool.name", classified.toolName);
        setAttribute(span, "gen_ai.tool.type", classified.kind === "mcp_tool_call" ? "mcp" : "function");
      }
      if (classified.kind === "model_generation" && classified.model) {
        setAttribute(span, "gen_ai.request.model", classified.model);
      }
    }
    return { alreadyInstrumented, kind: classified.kind, mapped, counted: prior?.counted ?? false };
  }

  function record(span: MastraSpanLike, next: SpanState): void {
    if (next.counted) return;
    next.counted = true;
    diagnostics.totalSpans += 1;
    if (next.alreadyInstrumented) {
      diagnostics.alreadyInstrumentedSpans += 1;
      return;
    }
    if (next.mapped) {
      diagnostics.mappedSpans += 1;
      return;
    }
    if (next.kind !== null) {
      diagnostics.contextOnlySpans += 1;
      return;
    }
    diagnostics.unmappedSpans += 1;
    const name = typeof span.name === "string" && span.name ? span.name : "(unnamed)";
    if (diagnostics.unmappedSpanNames.length < sampleLimit && !diagnostics.unmappedSpanNames.includes(name)) {
      diagnostics.unmappedSpanNames.push(name);
    }
  }

  return {
    onStart(span: MastraSpanLike): void {
      if (!isObject(span)) return;
      try {
        state.set(span, apply(span, state.get(span)));
      } catch {
        // A processor must never throw into the host's span pipeline.
      }
    },
    onEnd(span: MastraSpanLike): void {
      if (!isObject(span)) return;
      try {
        const next = apply(span, state.get(span));
        state.set(span, next);
        record(span, next);
      } catch {
        // Same: an adapter bug must not take down the customer's agent.
      }
    },
    shutdown(): Promise<void> {
      return Promise.resolve();
    },
    forceFlush(): Promise<void> {
      return Promise.resolve();
    },
    diagnostics(): CavemanProcessorDiagnostics {
      return { ...diagnostics, unmappedSpanNames: [...diagnostics.unmappedSpanNames] };
    },
  };
}

function stampContext(span: MastraSpanLike, task: TaskContext | undefined, options: CavemanSpanProcessorOptions): void {
  if (task?.taskId) setAttribute(span, "caveman.task.id", task.taskId);
  if (task?.sessionId) setAttribute(span, "caveman.session.id", task.sessionId);
  if (options.projectId) setAttribute(span, "caveman.project.id", options.projectId);
  if (options.environment) setAttribute(span, "caveman.environment", options.environment);
  if (options.agentId) {
    setAttribute(span, "caveman.agent.id", options.agentId);
    // The gateway's normalizer reads the agent slug from the `cave.agent` SPAN
    // attribute (resource attributes are stored prefixed and are not consulted
    // for it), so the mirror is what actually populates the agent column.
    setAttribute(span, "cave.agent", options.agentId);
  }
  if (options.agentVersion) setAttribute(span, "caveman.agent.version", options.agentVersion);
  if (options.workflowVersion) setAttribute(span, "caveman.workflow.version", options.workflowVersion);
}

function copyUsageAliases(
  span: MastraSpanLike,
  attrs: Record<string, unknown>,
  aliases: UsageAttributeAliases | undefined,
): void {
  if (!aliases) return;
  copyAlias(span, attrs, aliases.model, "gen_ai.request.model");
  copyAlias(span, attrs, aliases.provider, "gen_ai.provider.name");
  copyAlias(span, attrs, aliases.toolName, "gen_ai.tool.name");
  copyAlias(span, attrs, aliases.inputTokens, "gen_ai.usage.input_tokens");
  copyAlias(span, attrs, aliases.outputTokens, "gen_ai.usage.output_tokens");
  copyAlias(span, attrs, aliases.cachedInputTokens, "gen_ai.usage.cached_tokens");
}

function copyAlias(span: MastraSpanLike, attrs: Record<string, unknown>, from: string | undefined, to: string): void {
  if (!from) return;
  const value = attrs[from];
  if (value === undefined || value === null || value === "") return;
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
    setAttribute(span, to, value);
  }
}

/** Writes an attribute unless it already carries a value. Never throws. */
function setAttribute(span: MastraSpanLike, key: string, value: string | number | boolean): void {
  try {
    const existing = span.attributes?.[key];
    if (existing !== undefined && existing !== null && existing !== "") return;
    if (typeof span.setAttribute === "function") {
      span.setAttribute(key, value);
      return;
    }
    const attrs = span.attributes;
    if (attrs && Object.isExtensible(attrs)) attrs[key] = value;
  } catch {
    // An ended or frozen span refusing writes is the host's contract, not an error here.
  }
}

// ---------------------------------------------------------------------------
// 4. Outcome helper
// ---------------------------------------------------------------------------

export type FetchLike = (input: string, init: RequestInitLike) => Promise<ResponseLike>;

export interface RequestInitLike {
  method: string;
  headers: Record<string, string>;
  body: string;
}

export interface ResponseLike {
  status: number;
  ok?: boolean;
  text(): Promise<string>;
}

export interface CavemanOutcomeClient {
  /** Control API base URL, e.g. `https://api.caveman.so`. */
  controlApiUrl: string;
  /** Control API access token (the session JWT sent as `authorization: Bearer …`). */
  token: string;
  projectId: string;
  /** Defaults to the global `fetch`. */
  fetch?: FetchLike;
}

export interface RecordOutcomeInput {
  /** Joins the record to spans carrying `caveman.task.id` — `correlation_id`. */
  taskId: string;
  /** The Outcome Contract name, e.g. `"resolved_support_ticket"`. */
  contract: string;
  /** The observed values. Must be a plain JSON object with at least one key. */
  values: Record<string, unknown>;
  /** Pointers (ids, URLs, row keys) — 1 to 64 entries, serialized as `"k:v"`. */
  evidence: Record<string, string | number | boolean>;
  /**
   * Required. The server made confidence mandatory specifically so an omitted
   * value is rejected rather than silently ingested as confident — this client
   * must not undo that with a default of 1.
   */
  confidence: number;
  /**
   * Required. The trusted source this outcome was read from. `agent_claim`
   * and `judge` are unstorable server-side by CHECK; stating `environment`,
   * `database`, or `ci` asserts the value was read from that system, not from
   * the agent's own output.
   */
  source: "ci" | "environment" | "database" | "business_event" | "human";
  /** Defaults to now. */
  occurredAt?: Date | string;
}

const OUTCOME_SOURCES: ReadonlySet<string> = new Set(["ci", "environment", "database", "business_event", "human"]);

export interface RecordOutcomeResult {
  status: number;
  /** False when the identical record was already stored (idempotent re-delivery). */
  created: boolean;
  record: unknown;
}

/** A non-2xx answer from the Outcome Contract endpoint, surfaced verbatim. */
export class CavemanOutcomeError extends Error {
  readonly status: number;
  readonly body: string;
  constructor(status: number, body: string) {
    super(`recordOutcome: control API responded ${status}: ${body}`);
    this.name = "CavemanOutcomeError";
    this.status = status;
    this.body = body;
  }
}

/** Matches the control API's `OutcomeRecordMaxEvidenceRefs`. */
export const MAX_EVIDENCE_REFS = 64;

/**
 * Records a business outcome against a task (spec §23.4).
 *
 * `POST {controlApiUrl}/api/v1/projects/{projectId}/outcomes` with
 * `correlation_kind: "task"`, `correlation_id: taskId`, `source:
 * "business_event"`.
 *
 * `observed_outcome` derivation: `` `${contract} ${stableStringify(values)}` `` —
 * the contract name, a space, then the values as JSON with object keys sorted at
 * every level. Sorting is load-bearing: the server hashes the record for
 * idempotency, so the same values submitted in a different key order must
 * produce the same bytes.
 *
 * Fails closed BEFORE any network call on a missing task id, a missing contract,
 * empty values, an evidence map that is empty or over the server's limit, a
 * confidence outside [0,1], or values that are not plain JSON. Exactly one
 * request is made — no retries — and a non-2xx response throws a
 * {@link CavemanOutcomeError} carrying the status and body verbatim.
 */
export async function recordOutcome(
  client: CavemanOutcomeClient,
  input: RecordOutcomeInput,
): Promise<RecordOutcomeResult> {
  const controlApiUrl = requireBaseUrl(client.controlApiUrl, "recordOutcome: controlApiUrl");
  const token = requireNonEmpty(client.token, "recordOutcome: token");
  const projectId = requireNonEmpty(client.projectId, "recordOutcome: projectId");
  const taskId = requireNonEmpty(input.taskId, "recordOutcome: taskId");
  const contract = requireNonEmpty(input.contract, "recordOutcome: contract");

  if (!isPlainObject(input.values) || Object.keys(input.values).length === 0) {
    throw new Error("recordOutcome: values must be a non-empty plain object");
  }
  if (!isPlainObject(input.evidence)) {
    throw new Error("recordOutcome: evidence must be a plain object");
  }
  const evidenceRefs = Object.entries(input.evidence).map(([key, value]) => {
    if (!key.trim()) throw new Error("recordOutcome: evidence keys must be non-empty");
    if (typeof value !== "string" && typeof value !== "number" && typeof value !== "boolean") {
      throw new Error(`recordOutcome: evidence.${key} must be a string, number, or boolean`);
    }
    if (typeof value === "number" && !Number.isFinite(value)) {
      throw new Error(`recordOutcome: evidence.${key} must be a finite number`);
    }
    const ref = `${key}:${String(value)}`;
    if (!ref.slice(key.length + 1)) throw new Error(`recordOutcome: evidence.${key} must be non-empty`);
    return ref;
  });
  if (evidenceRefs.length === 0) {
    throw new Error("recordOutcome: evidence must contain at least one reference");
  }
  if (evidenceRefs.length > MAX_EVIDENCE_REFS) {
    throw new Error(`recordOutcome: evidence must contain at most ${MAX_EVIDENCE_REFS} references`);
  }

  const confidence = input.confidence;
  if (typeof confidence !== "number" || !Number.isFinite(confidence) || confidence < 0 || confidence > 1) {
    throw new Error("recordOutcome: confidence is required and must be a number within [0,1]");
  }
  if (typeof input.source !== "string" || !OUTCOME_SOURCES.has(input.source)) {
    throw new Error("recordOutcome: source is required and must be one of ci|environment|database|business_event|human");
  }

  const occurredAt = input.occurredAt === undefined ? new Date() : new Date(input.occurredAt);
  if (Number.isNaN(occurredAt.getTime())) {
    throw new Error("recordOutcome: occurredAt must be a valid date");
  }

  const body = JSON.stringify({
    correlation_kind: "task",
    correlation_id: taskId,
    source: input.source,
    observed_outcome: `${contract} ${stableStringify(input.values, "values")}`,
    confidence,
    evidence_refs: evidenceRefs,
    occurred_at: occurredAt.toISOString(),
  });

  const doFetch: FetchLike = client.fetch ?? ((url, init) => globalFetch(url, init));
  const response = await doFetch(
    `${controlApiUrl}/api/v1/projects/${encodeURIComponent(projectId)}/outcomes`,
    {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${token}` },
      body,
    },
  );

  const text = await response.text();
  const ok = response.ok ?? (response.status >= 200 && response.status < 300);
  if (!ok) throw new CavemanOutcomeError(response.status, text);

  let parsed: { data?: unknown; created?: unknown };
  try {
    parsed = JSON.parse(text) as { data?: unknown; created?: unknown };
  } catch {
    throw new CavemanOutcomeError(response.status, text);
  }
  return { status: response.status, created: parsed.created === true, record: parsed.data ?? null };
}

function globalFetch(url: string, init: RequestInitLike): Promise<ResponseLike> {
  const fetchImpl = (globalThis as { fetch?: (u: string, i: unknown) => Promise<ResponseLike> }).fetch;
  if (typeof fetchImpl !== "function") {
    throw new Error("recordOutcome: no global fetch; pass client.fetch");
  }
  return fetchImpl(url, init);
}

/**
 * JSON with object keys sorted at every level. Throws on anything that is not a
 * plain object, array, string, finite number, boolean, or null — a Date, a Map,
 * or a class instance would serialize into something the caller did not mean,
 * and the record's digest is its identity.
 */
export function stableStringify(value: unknown, path = "value"): string {
  if (value === null) return "null";
  switch (typeof value) {
    case "string":
      return JSON.stringify(value);
    case "boolean":
      return value ? "true" : "false";
    case "number":
      if (!Number.isFinite(value)) throw new Error(`${path} must be a finite number`);
      return JSON.stringify(value);
    case "object": {
      if (Array.isArray(value)) {
        return `[${value.map((item, i) => stableStringify(item, `${path}[${i}]`)).join(",")}]`;
      }
      if (!isPlainObject(value)) throw new Error(`${path} must be a plain object, array, or primitive`);
      const entries = Object.entries(value).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
      return `{${entries
        .map(([key, item]) => `${JSON.stringify(key)}:${stableStringify(item, `${path}.${key}`)}`)
        .join(",")}}`;
    }
    default:
      throw new Error(`${path} must be JSON data (got ${typeof value})`);
  }
}

// ---------------------------------------------------------------------------
// 5. Deep links
// ---------------------------------------------------------------------------

/**
 * Mastra Studio's documented default address. The dev server listens on 4111
 * (or `PORT`): https://mastra.ai/docs/studio/overview
 */
export const MASTRA_STUDIO_DEFAULT_BASE_URL = "http://localhost:4111";

export interface MastraStudioLinkOptions {
  /**
   * REQUIRED. Mastra documents a Studio Observability tab but not a per-trace
   * URL, so this package will not invent one. Supply the shape your Studio
   * build uses, with `{traceId}` (and optionally `{baseUrl}`) placeholders —
   * e.g. `"{baseUrl}/observability?traceId={traceId}"`.
   */
  template: string;
  /** Defaults to {@link MASTRA_STUDIO_DEFAULT_BASE_URL}. */
  baseUrl?: string;
}

/** Builds a Mastra Studio link from the caller-supplied template. */
export function mastraStudioLink(traceId: string, options: MastraStudioLinkOptions): string {
  const id = requireNonEmpty(traceId, "mastraStudioLink: traceId");
  const template = requireNonEmpty(options.template, "mastraStudioLink: template");
  if (!template.includes("{traceId}")) {
    throw new Error("mastraStudioLink: template must contain the {traceId} placeholder");
  }
  const baseUrl = trimTrailingSlashes(options.baseUrl ?? MASTRA_STUDIO_DEFAULT_BASE_URL);
  return template.replaceAll("{baseUrl}", baseUrl).replaceAll("{traceId}", encodeURIComponent(id));
}

/**
 * The Caveman dashboard trace page: `{baseUrl}/traces/{traceId}?project_id=…`.
 *
 * The dashboard currently resolves the project from the workspace switcher and
 * ignores the query parameter; it is carried so the link is unambiguous about
 * which project's trace it means.
 */
export function cavemanTraceLink(baseUrl: string, projectId: string, traceId: string): string {
  const base = requireBaseUrl(baseUrl, "cavemanTraceLink: baseUrl");
  const project = requireNonEmpty(projectId, "cavemanTraceLink: projectId");
  const trace = requireNonEmpty(traceId, "cavemanTraceLink: traceId");
  return `${base}/traces/${encodeURIComponent(trace)}?project_id=${encodeURIComponent(project)}`;
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

function requireNonEmpty(value: string | undefined, label: string): string {
  if (typeof value !== "string" || value.trim() === "") throw new Error(`${label} is required`);
  return value.trim();
}

function requireBaseUrl(value: string | undefined, label: string): string {
  const raw = requireNonEmpty(value, label);
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    throw new Error(`${label} must be an absolute http(s) URL`);
  }
  // Credentials ride every request to this host. Plain http is allowed only
  // for loopback development targets — anywhere else the key or session token
  // would transit unencrypted to a caller-chosen host.
  const loopback = parsed.hostname === "localhost" || parsed.hostname === "127.0.0.1" || parsed.hostname === "::1" || parsed.hostname === "[::1]";
  if (parsed.protocol === "http:" && !loopback) {
    throw new Error(`${label} must use https (plain http is allowed only for localhost)`);
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    throw new Error(`${label} must be an absolute http(s) URL`);
  }
  return trimTrailingSlashes(raw);
}

function trimTrailingSlashes(value: string): string {
  return value.replace(/\/+$/, "");
}

function isObject(value: unknown): value is object {
  return typeof value === "object" && value !== null;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (!isObject(value) || Array.isArray(value)) return false;
  const proto: unknown = Object.getPrototypeOf(value);
  return proto === Object.prototype || proto === null;
}

function readString(attrs: Record<string, unknown>, key: string): string | undefined {
  const value = attrs[key];
  return typeof value === "string" && value !== "" ? value : undefined;
}

function firstString(attrs: Record<string, unknown>, keys: readonly string[]): string | undefined {
  for (const key of keys) {
    const value = readString(attrs, key);
    if (value) return value;
  }
  return undefined;
}

function suffixAfter(name: string | undefined, prefix: string): string | undefined {
  if (typeof name !== "string" || !name.startsWith(prefix)) return undefined;
  const suffix = name.slice(prefix.length).trim();
  return suffix === "" ? undefined : suffix;
}
