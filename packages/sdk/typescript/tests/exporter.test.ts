/**
 * Type-level tests for the TS SDK OTel exporter surface.
 *
 * Compiled by `tsc --project tsconfig.test.json` (the package's "test" script).
 * Runtime assertions live in exporter.runtime.mjs (node:test).
 */
import type { Cave, OTelExporter, OTelSpan, SpanOptions } from "../src/index.js";

declare const cave: Cave;

// cave.exporter() returns an OTelExporter.
const exporter: OTelExporter = cave.exporter();
const _withName: OTelExporter = cave.exporter({ serviceName: "svc" });

// recordSpan() takes a name + optional SpanOptions → OTelSpan.
const span: OTelSpan = exporter.recordSpan("chat");
const _opts: SpanOptions = { provider: "openai", model: "gpt-5.5", inputTokens: 10, status: "ok" };
const _spanWithOpts: OTelSpan = exporter.recordSpan("chat", _opts);

// OTelSpan fields exist with the right types.
const _traceId: string = span.traceId;
const _spanId: string = span.spanId;
const _parent: string = span.parentSpanId;
const _status: number = span.statusCode;
const _attrs: Record<string, string | number | boolean> = span.attributes;

// pending is a number; export()/flush() return a Promise.
const _pending: number = exporter.pending;
const _exportReturn: Promise<Record<string, unknown>> = exporter.export();
const _flushReturn: Promise<Record<string, unknown>> = exporter.flush();
const _payload: Record<string, unknown> = exporter.buildPayload();
const _trace: string = exporter.newTraceId();
const _sid: string = exporter.newSpanId();
