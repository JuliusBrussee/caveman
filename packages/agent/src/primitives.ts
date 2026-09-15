import { Type, type Static, type TSchema } from "@earendil-works/pi-ai";
import type {
  StandardJSONSchemaV1,
  StandardSchemaV1,
} from "@standard-schema/spec";

export const AUTO = Symbol.for("@caveman-ai/agent:auto");
export type Auto = { readonly kind: "auto"; readonly [AUTO]: true };

export function auto(): Auto {
  return Object.freeze({ kind: "auto", [AUTO]: true as const });
}

export interface FileSource {
  readonly kind: "file";
  readonly path: string;
}

export function file(path: string): FileSource {
  if (typeof path !== "string" || path.trim() === "") {
    throw new Error("caveman agent: file path is required");
  }
  return Object.freeze({ kind: "file", path });
}

export const schema = {
  any: () => Type.Any(),
  array: <T extends TSchema>(items: T) => Type.Array(items),
  boolean: () => Type.Boolean(),
  integer: () => Type.Integer(),
  literal: <T extends string | number | boolean>(value: T) => Type.Literal(value),
  number: () => Type.Number(),
  object: <T extends Record<string, TSchema>>(properties: T) => Type.Object(properties),
  optional: <T extends TSchema>(value: T) => Type.Optional(value),
  string: () => Type.String(),
  union: <T extends TSchema[]>(values: [...T]) => Type.Union(values),
};

export type ToolEffect = "read" | "write" | "idempotent" | "external";
export type ToolResultPolicy = "auto" | "inline" | "page" | "compress" | "exact_ccr";

export interface SubagentRuntimeDefinition {
  readonly kind: "subagent";
  readonly definition: unknown;
  readonly maxInputChars: number;
  readonly maxCalls: number;
  readonly maxCostUsd: number;
  /**
   * The token-denominated sibling of `maxCostUsd`. A run metered in tokens
   * carves this child's wallet from it; without it, such a run cannot fund
   * this subagent at all.
   */
  readonly maxTokens?: number;
  readonly maxContextTokens: number;
}

export type StandardToolSchema<Input = unknown, Output = Input> =
  StandardSchemaV1<Input, Output> & StandardJSONSchemaV1<Input, Output>;

export interface ToolDefinition<TInput = unknown, TResult = unknown> {
  readonly kind: "tool";
  readonly name: string;
  readonly description: string;
  readonly input: TSchema;
  readonly effect: ToolEffect;
  readonly result: ToolResultPolicy;
  readonly artifact?: ArtifactDefinition;
  /**
   * Opt this tool out of the repeated-call loop breaker. Set it on tools whose
   * job is to be called again with the same arguments — polling a queue,
   * waiting on a build, re-reading a file that changes underneath.
   */
  readonly allowRepeat?: boolean;
  readonly timeoutMs: number;
  readonly runtime?: SubagentRuntimeDefinition;
  readonly execute: {
    bivarianceHack(input: TInput, signal?: AbortSignal): TResult | Promise<TResult>;
  }["bivarianceHack"];
}

export interface ToolOptions<TInput extends TSchema, TResult> {
  name: string;
  description: string;
  input: TInput;
  effect: ToolEffect;
  result?: ToolResultPolicy | ArtifactDefinition;
  /** See {@link ToolDefinition.allowRepeat}. */
  allowRepeat?: boolean;
  timeoutMs?: number;
  runtime?: SubagentRuntimeDefinition;
  execute: (input: Static<TInput>, signal?: AbortSignal) => TResult | Promise<TResult>;
}

export interface StandardToolOptions<Input, Output, TResult> {
  name: string;
  description: string;
  input: StandardSchemaV1<Input, Output>;
  inputJSONSchema: TSchema;
  effect: ToolEffect;
  result?: ToolResultPolicy | ArtifactDefinition;
  /** See {@link ToolDefinition.allowRepeat}. */
  allowRepeat?: boolean;
  timeoutMs?: number;
  runtime?: SubagentRuntimeDefinition;
  execute: (input: Output, signal?: AbortSignal) => TResult | Promise<TResult>;
}

export interface StandardJSONToolOptions<Input, Output, TResult>
  extends Omit<StandardToolOptions<Input, Output, TResult>, "input" | "inputJSONSchema"> {
  input: StandardToolSchema<Input, Output>;
  inputJSONSchema?: never;
}

export function tool<TInput extends TSchema, TResult>(
  options: ToolOptions<TInput, TResult>,
): ToolDefinition<Static<TInput>, TResult>;
export function tool<Input, Output, TResult>(
  options: StandardToolOptions<Input, Output, TResult>,
): ToolDefinition<Output, TResult>;
export function tool<Input, Output, TResult>(
  options: StandardJSONToolOptions<Input, Output, TResult>,
): ToolDefinition<Output, TResult>;
export function tool(
  options: ToolOptions<TSchema, unknown> |
    StandardToolOptions<unknown, unknown, unknown> |
    StandardJSONToolOptions<unknown, unknown, unknown>,
): ToolDefinition<unknown, unknown> {
  if (!/^[a-zA-Z][a-zA-Z0-9_-]{0,127}$/.test(options.name)) {
    throw new Error(`caveman agent: invalid tool name ${JSON.stringify(options.name)}`);
  }
  if (!["read", "write", "idempotent", "external"].includes(options.effect)) {
    throw new Error(`caveman agent: unknown tool effect ${JSON.stringify(options.effect)}`);
  }
  const result = typeof options.result === "object"
    ? artifactResultPolicy(options.result)
    : options.result ?? "auto";
  if (!["auto", "inline", "page", "compress", "exact_ccr"].includes(result)) {
    throw new Error(`caveman agent: unknown tool result policy ${JSON.stringify(result)}`);
  }
  const timeoutMs = options.timeoutMs ?? 30_000;
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0) {
    throw new Error("caveman agent: tool timeoutMs must be a positive integer");
  }
  const standard = standardToolSchema(options.input);
  let input: TSchema;
  if (standard === undefined) {
    input = options.input as TSchema;
  } else {
    let converted = "inputJSONSchema" in options
      ? options.inputJSONSchema
      : undefined;
    if (converted === undefined && standard.jsonSchema !== undefined) {
      try {
        converted = standard.jsonSchema.input({ target: "draft-07" });
      } catch (error) {
        throw new Error("caveman agent: Standard Schema cannot emit draft-07 input JSON Schema", {
          cause: error,
        });
      }
    }
    if (converted === undefined) {
      throw new Error(
        "caveman agent: Standard Schema needs inputJSONSchema or Standard JSON Schema conversion",
      );
    }
    if (!isRecord(converted)) {
      throw new Error("caveman agent: Standard Schema emitted invalid input JSON Schema");
    }
    input = converted as TSchema;
  }
  const definition = {
    kind: "tool",
    name: options.name,
    description: options.description,
    input,
    effect: options.effect,
    result,
    ...(typeof options.result === "object" ? { artifact: options.result } : {}),
    ...(options.allowRepeat === undefined ? {} : { allowRepeat: options.allowRepeat }),
    timeoutMs,
    ...(options.runtime === undefined ? {} : { runtime: options.runtime }),
    async execute(value: unknown, signal?: AbortSignal) {
      if (standard === undefined) {
        return options.execute(value as never, signal);
      }
      const validated = await standard.validate(value);
      if (validated.issues) {
        throw new Error(`cave_tool_input_schema_mismatch:${options.name}`);
      }
      return options.execute(validated.value, signal);
    },
  } as const;
  Object.defineProperty(
    definition,
    Symbol.for("@caveman-ai/agent:tool-implementation-source"),
    {
      value: Function.prototype.toString.call(options.execute),
      enumerable: false,
      configurable: false,
      writable: false,
    },
  );
  return Object.freeze(definition);
}

function standardToolSchema(
  value: unknown,
): StandardSchemaV1.Props<unknown, unknown> & {
  jsonSchema?: StandardJSONSchemaV1.Converter;
} | undefined {
  if (!isRecord(value) || !isRecord(value["~standard"])) return undefined;
  const standard = value["~standard"];
  if (standard.version !== 1 || typeof standard.vendor !== "string" ||
      typeof standard.validate !== "function") {
    throw new Error(
      "caveman agent: tool Standard Schema must implement version 1 validation",
    );
  }
  if (standard.jsonSchema !== undefined &&
      (!isRecord(standard.jsonSchema) || typeof standard.jsonSchema.input !== "function")) {
    throw new Error("caveman agent: tool Standard JSON Schema converter is invalid");
  }
  return standard as unknown as StandardSchemaV1.Props<unknown, unknown> & {
    jsonSchema?: StandardJSONSchemaV1.Converter;
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function artifactResultPolicy(definition: ArtifactDefinition): ToolResultPolicy {
  if (definition.strategy === "verbatim") {
    return definition.recovery === "exact_ccr" ? "exact_ccr" : "inline";
  }
  if (definition.strategy === "json-index") return "compress";
  return "page";
}

export interface MemoryDefinition {
  readonly kind: "memory";
  readonly namespace: string;
  readonly provenance: "local" | "project" | "external";
  readonly ttl: string;
  readonly recallBudget: number;
  readonly consent: "local_only" | "project_shared";
}

export function memoryTTLMilliseconds(value: string): number {
  const match = /^([1-9][0-9]*)(m|h|d)$/.exec(value);
  if (!match) throw new Error("cave_memory_ttl_invalid");
  const amount = Number(match[1]);
  const scale = match[2] === "m" ? 60_000 : match[2] === "h" ? 3_600_000 : 86_400_000;
  const milliseconds = amount * scale;
  if (!Number.isSafeInteger(amount) || !Number.isSafeInteger(milliseconds) || milliseconds <= 0) {
    throw new Error("cave_memory_ttl_invalid");
  }
  return milliseconds;
}

export function memory(options: {
  namespace: string;
  provenance?: MemoryDefinition["provenance"];
  ttl: string;
  recallBudget: number;
  consent?: MemoryDefinition["consent"];
}): MemoryDefinition {
  if (!/^[a-z0-9][a-z0-9_-]{0,95}$/.test(options.namespace)) {
    throw new Error(`caveman agent: invalid memory namespace ${JSON.stringify(options.namespace)}`);
  }
  if (!Number.isSafeInteger(options.recallBudget) || options.recallBudget < 0) {
    throw new Error("caveman agent: memory recallBudget must be a non-negative integer");
  }
  try {
    memoryTTLMilliseconds(options.ttl);
  } catch {
    throw new Error("caveman agent: memory ttl must use positive m, h, or d duration");
  }
  // Fail closed at CONSTRUCTION, not at tool-call time: the durable
  // store implements only local, single-tenant memory. A `project`/`external`
  // provenance or a `project_shared` consent is a shared-backend contract this
  // package does not provide, so it is refused here rather than burning a model
  // turn to discover a config the framework already knew was unsupported.
  const provenance = options.provenance ?? "local";
  if (provenance !== "local") {
    throw new Error("cave_memory_provenance_unsupported: only \"local\" memory is supported");
  }
  const consent = options.consent ?? "local_only";
  if (consent !== "local_only") {
    throw new Error("cave_memory_consent_unsupported: only \"local_only\" consent is supported");
  }
  return Object.freeze({
    kind: "memory",
    namespace: options.namespace,
    provenance,
    ttl: options.ttl,
    recallBudget: options.recallBudget,
    consent,
  });
}

export type ContextKind =
  | "instruction"
  | "user_intent"
  | "tool_schema"
  | "skill"
  | "memory"
  | "history"
  | "tool_result"
  | "artifact"
  | "error"
  | "output_contract";
export type ContextStability = "build" | "session" | "turn";
export type SafetyClass = "S0" | "S1" | "S2" | "S3" | "S4";
export type ContextPriority = "required" | "high" | "normal" | "low";
export type RecoveryKind = "none" | "exact_ccr" | "source_ref" | "recompute";
export type CacheRegion = "frozen_prefix" | "live_zone" | "uncached";
export type PrivacyClass = "content_blind" | "local_sensitive" | "connected_allowed";

export interface ContextDefinition {
  readonly kind: "context";
  readonly id: string;
  readonly segmentKind: ContextKind;
  readonly source: string | FileSource;
  readonly stability: ContextStability;
  readonly safety: SafetyClass;
  readonly priority: ContextPriority;
  readonly recovery: RecoveryKind;
  readonly cacheRegion: CacheRegion;
  readonly privacy: PrivacyClass;
  readonly opaque: boolean;
  readonly ttlTurns?: number;
}

export function context(options: {
  id: string;
  kind: ContextKind;
  source: string | FileSource;
  stability: ContextStability;
  safety?: SafetyClass;
  priority?: ContextPriority;
  recovery?: RecoveryKind;
  cacheRegion?: CacheRegion;
  privacy?: PrivacyClass;
  opaque?: boolean;
  ttlTurns?: number;
}): ContextDefinition {
  if (options.id.trim() === "") throw new Error("caveman agent: context id is required");
  if (options.ttlTurns !== undefined && (!Number.isSafeInteger(options.ttlTurns) || options.ttlTurns <= 0)) {
    throw new Error("caveman agent: context ttlTurns must be a positive integer");
  }
  const definition: ContextDefinition = {
    kind: "context",
    id: options.id,
    segmentKind: options.kind,
    source: options.source,
    stability: options.stability,
    safety: options.safety ?? "S0",
    priority: options.priority ?? "required",
    recovery: options.recovery ?? "none",
    cacheRegion: options.cacheRegion ?? (options.stability === "build" ? "frozen_prefix" : "live_zone"),
    privacy: options.privacy ?? "local_sensitive",
    opaque: options.opaque ?? false,
    ...(options.ttlTurns === undefined ? {} : { ttlTurns: options.ttlTurns }),
  };
  return Object.freeze(definition);
}

export interface ArtifactDefinition {
  readonly kind: "artifact";
  readonly strategy: "verbatim" | "json-index" | "page";
  readonly maxInlineTokens: number;
  readonly recovery: "exact_ccr" | "source_ref";
}

export function artifact(options: {
  strategy?: ArtifactDefinition["strategy"];
  maxInlineTokens?: number;
  recovery?: ArtifactDefinition["recovery"];
} = {}): ArtifactDefinition {
  const maxInlineTokens = options.maxInlineTokens ?? 1_200;
  if (!Number.isSafeInteger(maxInlineTokens) || maxInlineTokens < 0) {
    throw new Error("caveman agent: artifact maxInlineTokens must be a non-negative integer");
  }
  return Object.freeze({
    kind: "artifact",
    strategy: options.strategy ?? "page",
    maxInlineTokens,
    recovery: options.recovery ?? "exact_ccr",
  });
}

export interface OutputDefinition<TSchemaValue extends TSchema | undefined = TSchema | undefined> {
  readonly kind: "output";
  readonly maxTokens: number;
  readonly schema?: TSchemaValue;
}

export function output<T extends TSchema | undefined = undefined>(options: {
  maxTokens: number;
  schema?: T;
}): OutputDefinition<T> {
  if (!Number.isSafeInteger(options.maxTokens) || options.maxTokens <= 0) {
    throw new Error("caveman agent: output maxTokens must be a positive integer");
  }
  return Object.freeze({
    kind: "output",
    maxTokens: options.maxTokens,
    ...(options.schema === undefined ? {} : { schema: options.schema }),
  }) as OutputDefinition<T>;
}

export type QualityGrader =
  | { type: "contains"; fragments: string[] }
  | { type: "tool_called"; tools: string[] }
  | { type: "exact_match"; expected: string }
  | { type: "json_schema"; schema: TSchema };

export type EvalGuardrail =
  | { type: "latency_threshold"; p95_ms: number }
  | { type: "error_rate"; max: number };

export interface EvalDefinition {
  readonly kind: "eval";
  readonly id: string;
  readonly approved: boolean;
  readonly required: boolean;
  readonly input: unknown;
  readonly tools: { mode: "fixture" | "live"; sandbox?: string };
  readonly quality: readonly QualityGrader[];
  readonly guardrails: readonly EvalGuardrail[];
}

export function evalFixture(options: {
  id: string;
  approved?: boolean;
  required?: boolean;
  input: unknown;
  tools?: { mode: "fixture" | "live"; sandbox?: string };
  quality: QualityGrader[];
  guardrails?: EvalGuardrail[];
}): EvalDefinition {
  if (options.id.trim() === "") throw new Error("caveman agent: eval id is required");
  if (options.quality.length === 0) throw new Error("caveman agent: eval needs at least one quality grader");
  const known = new Set(["contains", "tool_called", "exact_match", "json_schema"]);
  for (const grader of options.quality) {
    if (!known.has(grader.type)) {
      throw new Error(`caveman agent: unknown grader ${(grader as { type: string }).type}`);
    }
  }
  for (const guardrail of options.guardrails ?? []) {
    if (guardrail.type === "latency_threshold" &&
        (!Number.isSafeInteger(guardrail.p95_ms) || guardrail.p95_ms <= 0)) {
      throw new Error("caveman agent: latency guardrail requires positive integer p95_ms");
    }
    if (guardrail.type === "error_rate" &&
        (!Number.isFinite(guardrail.max) || guardrail.max < 0 || guardrail.max > 1)) {
      throw new Error("caveman agent: error-rate guardrail max must be in [0,1]");
    }
  }
  const tools = options.tools ?? { mode: "fixture" as const };
  if (tools.mode === "live" && !tools.sandbox) {
    throw new Error("caveman agent: live eval tools require an explicit sandbox");
  }
  return Object.freeze({
    kind: "eval",
    id: options.id,
    approved: options.approved ?? false,
    required: options.required ?? true,
    input: options.input,
    tools,
    quality: Object.freeze([...options.quality]),
    guardrails: Object.freeze([...(options.guardrails ?? [])]),
  });
}

export { evalFixture as eval };

// The real `subagent` builder + its SubagentDefinition live in index.ts and
// shadow this module's `export *`. A stale duplicate here (different shape:
// contextBudget/modelCallBudget) was dead and, worse, still re-exported a
// misleading type — deleted.
