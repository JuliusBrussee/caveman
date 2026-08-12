import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { isAbsolute, relative, resolve } from "node:path";
import type { TSchema } from "@earendil-works/pi-ai";
import type {
  CacheRegion,
  ContextDefinition,
  ContextKind,
  ContextPriority,
  ContextStability,
  FileSource,
  PrivacyClass,
  RecoveryKind,
  SafetyClass,
  ToolDefinition,
} from "./primitives.js";

export interface ContextSegment {
  id: string;
  kind: ContextKind;
  stability: ContextStability;
  safety: SafetyClass;
  priority: ContextPriority;
  recovery: RecoveryKind;
  cacheRegion: CacheRegion;
  privacy: PrivacyClass;
  opaque: boolean;
  ttlTurns?: number;
  provenanceDigest: string;
  tokenCount: number;
  bodyHandle: string;
}

export interface ContextIR {
  schemaVersion: 1;
  segments: ContextSegment[];
}

export interface ContextSegmentWire {
  id: string;
  kind: ContextKind;
  stability: ContextStability;
  safety: SafetyClass;
  priority: ContextPriority;
  recovery: RecoveryKind;
  cache_region: CacheRegion;
  privacy: PrivacyClass;
  opaque: boolean;
  ttl_turns?: number;
  provenance_digest: string;
  token_count: number;
  body_handle: string;
}

export interface ContextIRWire {
  schema_version: 1;
  segments: ContextSegmentWire[];
}

export function contextIRToWire(ir: ContextIR): ContextIRWire {
  if (ir.schemaVersion !== 1) throw new Error("cave_context_ir_unknown_schema");
  return {
    schema_version: 1,
    segments: ir.segments.map((segment) => ({
      id: segment.id,
      kind: segment.kind,
      stability: segment.stability,
      safety: segment.safety,
      priority: segment.priority,
      recovery: segment.recovery,
      cache_region: segment.cacheRegion,
      privacy: segment.privacy,
      opaque: segment.opaque,
      ...(segment.ttlTurns === undefined ? {} : { ttl_turns: segment.ttlTurns }),
      provenance_digest: segment.provenanceDigest,
      token_count: segment.tokenCount,
      body_handle: segment.bodyHandle,
    })),
  };
}

export function contextIRFromWire(value: unknown): ContextIR {
  if (!isRecord(value) || !exactKeys(value, ["schema_version", "segments"]) ||
      value.schema_version !== 1 || !Array.isArray(value.segments)) {
    throw new Error("cave_context_ir_invalid");
  }
  const segments = value.segments.map((item): ContextSegment => {
    const required = [
      "id", "kind", "stability", "safety", "priority", "recovery", "cache_region",
      "privacy", "opaque", "provenance_digest", "token_count", "body_handle",
    ];
    if (!isRecord(item) ||
        !(exactKeys(item, required) || exactKeys(item, [...required, "ttl_turns"])) ||
        typeof item.id !== "string" || item.id.length === 0 ||
        !known(item.kind, ["instruction", "user_intent", "tool_schema", "skill", "memory", "history", "tool_result", "artifact", "error", "output_contract"]) ||
        !known(item.stability, ["build", "session", "turn"]) ||
        !known(item.safety, ["S0", "S1", "S2", "S3", "S4"]) ||
        !known(item.priority, ["required", "high", "normal", "low"]) ||
        !known(item.recovery, ["none", "exact_ccr", "source_ref", "recompute"]) ||
        !known(item.cache_region, ["frozen_prefix", "live_zone", "uncached"]) ||
        !known(item.privacy, ["content_blind", "local_sensitive", "connected_allowed"]) ||
        typeof item.opaque !== "boolean" ||
        !/^[0-9a-f]{64}$/.test(String(item.provenance_digest)) ||
        !Number.isSafeInteger(item.token_count) || Number(item.token_count) < 0 ||
        typeof item.body_handle !== "string" || item.body_handle.length === 0 ||
        (item.ttl_turns !== undefined &&
          (!Number.isSafeInteger(item.ttl_turns) || Number(item.ttl_turns) < 1))) {
      throw new Error("cave_context_ir_invalid");
    }
    return {
      id: item.id,
      kind: item.kind as ContextKind,
      stability: item.stability as ContextStability,
      safety: item.safety as SafetyClass,
      priority: item.priority as ContextPriority,
      recovery: item.recovery as RecoveryKind,
      cacheRegion: item.cache_region as CacheRegion,
      privacy: item.privacy as PrivacyClass,
      opaque: item.opaque,
      ...(item.ttl_turns === undefined ? {} : { ttlTurns: Number(item.ttl_turns) }),
      provenanceDigest: String(item.provenance_digest),
      tokenCount: Number(item.token_count),
      bodyHandle: item.body_handle,
    };
  });
  return { schemaVersion: 1, segments };
}

export interface LoweredContext {
  ir: ContextIR;
  bodies: ReadonlyMap<string, Uint8Array>;
}

export interface RuntimeContextSegment {
  id: string;
  kind: "history" | "tool_result";
  body: Uint8Array;
}

export async function lowerContext(options: {
  rootDir?: string;
  instructions: string | FileSource;
  tools: readonly ToolDefinition[];
  contexts?: readonly ContextDefinition[];
  memory?: { namespace: string; recallBudget: number };
  output?: { maxTokens: number; schema?: TSchema };
  runtimeSegments?: readonly RuntimeContextSegment[];
  input?: unknown;
}): Promise<LoweredContext> {
  const rootDir = resolve(options.rootDir ?? process.cwd());
  const segments: ContextSegment[] = [];
  const bodies = new Map<string, Uint8Array>();

  const add = (
    spec: Omit<ContextSegment, "provenanceDigest" | "tokenCount" | "bodyHandle" | "opaque"> & { opaque?: boolean },
    body: Uint8Array,
  ) => {
    const digest = sha256(body);
    const handle = `cave_local_sha256:${digest}`;
    bodies.set(handle, body.slice());
    segments.push({
      ...spec,
      opaque: spec.opaque === true || opaquePayload(body),
      provenanceDigest: digest,
      tokenCount: estimateTokens(body),
      bodyHandle: handle,
    });
  };

  add({
    id: "agent.instructions",
    kind: "instruction",
    stability: "build",
    safety: "S0",
    priority: "required",
    recovery: "none",
    cacheRegion: "frozen_prefix",
    privacy: "local_sensitive",
  }, await sourceBytes(options.instructions, rootDir));

  for (const declared of options.contexts ?? []) {
    add({
      id: declared.id,
      kind: declared.segmentKind,
      stability: declared.stability,
      safety: declared.safety,
      priority: declared.priority,
      recovery: declared.recovery,
      cacheRegion: declared.cacheRegion,
      privacy: declared.privacy,
      opaque: declared.opaque,
      ...(declared.ttlTurns === undefined ? {} : { ttlTurns: declared.ttlTurns }),
    }, await sourceBytes(declared.source, rootDir));
  }

  for (const definition of options.tools) {
    add({
      id: `tool.${definition.name}`,
      kind: "tool_schema",
      stability: "build",
      safety: "S4",
      priority: "required",
      recovery: "exact_ccr",
      cacheRegion: "frozen_prefix",
      privacy: "content_blind",
    }, encodeCanonical({
      name: definition.name,
      description: definition.description,
      input: definition.input,
      effect: definition.effect,
      result: definition.result,
      timeout_ms: definition.timeoutMs,
    }));
  }

  if (options.memory) {
    add({
      id: `memory.${options.memory.namespace}`,
      kind: "memory",
      stability: "session",
      safety: "S2",
      priority: "normal",
      recovery: "source_ref",
      cacheRegion: "live_zone",
      privacy: "local_sensitive",
    }, encodeCanonical(options.memory));
  }

  if (options.output) {
    add({
      id: "agent.output",
      kind: "output_contract",
      stability: "build",
      safety: "S1",
      priority: "required",
      recovery: "none",
      cacheRegion: "frozen_prefix",
      privacy: "content_blind",
    }, encodeCanonical(options.output));
  }

  for (const segment of options.runtimeSegments ?? []) {
    add({
      id: segment.id,
      kind: segment.kind,
      stability: "turn",
      safety: "S4",
      priority: "normal",
      recovery: "exact_ccr",
      cacheRegion: "live_zone",
      privacy: "local_sensitive",
    }, segment.body);
  }

  if (options.input !== undefined) {
    add({
      id: "turn.input",
      kind: "user_intent",
      stability: "turn",
      safety: "S0",
      priority: "required",
      recovery: "none",
      cacheRegion: "live_zone",
      privacy: "local_sensitive",
    }, typeof options.input === "string"
      ? new TextEncoder().encode(options.input)
      : encodeCanonical(options.input));
  }

  return { ir: { schemaVersion: 1, segments }, bodies };
}

export function appendRuntimeContextSegment(
  lowered: LoweredContext,
  segment: RuntimeContextSegment,
): ContextSegment {
  const digest = sha256(segment.body);
  const existing = lowered.ir.segments.find((item) => item.id === segment.id);
  if (existing) {
    // Segment identity is content-derived, so the same id must
    // carry the same bytes. If it does not, the id has been reused for
    // different content — the exact confusion that once let compaction
    // substitute an OLD message's compressed body into a NEW message. Fail
    // closed rather than returning the stale segment and poisoning the CCR
    // restore map.
    if (existing.provenanceDigest !== digest) {
      throw new Error("cave_runtime_segment_id_collision");
    }
    return existing;
  }
  const bodyHandle = `cave_local_sha256:${digest}`;
  (lowered.bodies as Map<string, Uint8Array>).set(bodyHandle, segment.body.slice());
  const appended: ContextSegment = {
    id: segment.id,
    kind: segment.kind,
    stability: "turn",
    safety: "S4",
    priority: "normal",
    recovery: "exact_ccr",
    cacheRegion: "live_zone",
    privacy: "local_sensitive",
    opaque: opaquePayload(segment.body),
    provenanceDigest: digest,
    tokenCount: estimateTokens(segment.body),
    bodyHandle,
  };
  lowered.ir.segments.push(appended);
  return appended;
}

export function contextBill(ir: ContextIR): Record<string, number> {
  const bill: Record<string, number> = {};
  for (const segment of ir.segments) {
    bill[segment.kind] = (bill[segment.kind] ?? 0) + segment.tokenCount;
  }
  return bill;
}

async function sourceBytes(source: string | FileSource, rootDir: string): Promise<Uint8Array> {
  if (typeof source === "string") return new TextEncoder().encode(source);
  const fullPath = resolve(rootDir, source.path);
  const relativePath = relative(rootDir, fullPath);
  if (relativePath === ".." || relativePath.startsWith("../") ||
      relativePath.startsWith("..\\") || isAbsolute(relativePath)) {
    throw new Error("caveman agent: file source escapes project root");
  }
  return new Uint8Array(await readFile(fullPath));
}

function encodeCanonical(value: unknown): Uint8Array {
  return new TextEncoder().encode(stableStringify(value));
}

export function stableStringify(value: unknown): string {
  if (value === null || typeof value !== "object") {
    const encoded = JSON.stringify(value);
    if (encoded === undefined) throw new Error("caveman agent: value is not canonically serializable");
    return encoded;
  }
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(",")}]`;
  const object = value as Record<string, unknown>;
  return `{${Object.keys(object).sort().map((key) => `${JSON.stringify(key)}:${stableStringify(object[key])}`).join(",")}}`;
}

export function sha256(value: Uint8Array | string): string {
  return createHash("sha256").update(value).digest("hex");
}

export function opaquePayload(input: Uint8Array): boolean {
  const value = new TextDecoder().decode(input).trim();
  if (/^[-]{5}BEGIN (?:PGP SIGNED MESSAGE|[A-Z ]+ SIGNATURE)[-]{5}/.test(value) ||
      /^[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}$/.test(value) ||
      /\b(?:x-amz-signature|signature)=([0-9a-f]{32,}|[A-Za-z0-9_-]{32,})\b/i.test(value)) {
    return true;
  }
  try {
    return signedJSONValue(JSON.parse(value));
  } catch {
    return false;
  }
}

function signedJSONValue(value: unknown): boolean {
  if (Array.isArray(value)) return value.some(signedJSONValue);
  if (value === null || typeof value !== "object") return false;
  return Object.entries(value as Record<string, unknown>).some(([key, item]) =>
    /^(?:signature|sig|mac|hmac|ciphertext|encrypted|auth_tag|tag)$/i.test(key) ||
    signedJSONValue(item));
}

function estimateTokens(body: Uint8Array): number {
  return Math.ceil(body.byteLength / 4);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function exactKeys(value: Record<string, unknown>, expected: string[]): boolean {
  const keys = Object.keys(value).sort();
  const wanted = [...expected].sort();
  return keys.length === wanted.length && keys.every((key, index) => key === wanted[index]);
}

function known<T extends string>(value: unknown, allowed: readonly T[]): value is T {
  return typeof value === "string" && allowed.includes(value as T);
}
