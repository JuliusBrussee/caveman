/**
 * Runtime tests for the TS SDK OTel exporter surface.
 * Run with: node --test packages/sdk-ts/tests/exporter.runtime.mjs
 * (Node 22, uses built-in node:test runner with global fetch mocking.)
 */
import { test } from "node:test";
import assert from "node:assert/strict";

// Import compiled dist (must run `pnpm --filter @caveman-ai/sdk build` first)
import { Cave, OTelExporter } from "../dist/index.js";

// ─── Fetch mock helpers ──────────────────────────────────────────────────────

let capturedRequests = [];
let nextResponse = {};

const originalFetch = globalThis.fetch;

function mockFetch(responseData) {
  nextResponse = responseData;
  capturedRequests = [];
  globalThis.fetch = async (url, init) => {
    capturedRequests.push({ url: String(url), method: init?.method, headers: init?.headers ?? {}, body: init?.body ? JSON.parse(init.body) : undefined });
    return { ok: true, json: async () => nextResponse, text: async () => JSON.stringify(nextResponse) };
  };
}

function restoreFetch() {
  globalThis.fetch = originalFetch;
}

// Flatten OTLP attribute list back into {key: scalar} for assertions.
function attrMap(otlpAttrs) {
  const out = {};
  for (const kv of otlpAttrs) {
    const v = kv.value;
    if ("stringValue" in v) out[kv.key] = v.stringValue;
    else if ("intValue" in v) out[kv.key] = v.intValue;
    else if ("doubleValue" in v) out[kv.key] = v.doubleValue;
    else if ("boolValue" in v) out[kv.key] = v.boolValue;
  }
  return out;
}

// ─── Tests ───────────────────────────────────────────────────────────────────

test("cave.exporter() returns an OTelExporter bound to the agent", () => {
  const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
  const exp = cave.exporter();
  assert.ok(exp instanceof OTelExporter);
  assert.equal(exp.serviceName, "a");
  assert.equal(exp.pending, 0);
});

test("recordSpan() maps GenAI fields to gen_ai.* attributes and generates ids", () => {
  const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "billing-agent" });
  const exp = cave.exporter();
  const span = exp.recordSpan("chat gpt-5.5", {
    provider: "openai",
    model: "gpt-5.5",
    operation: "chat",
    inputTokens: 1200,
    outputTokens: 350,
    cachedTokens: 800,
    costUsd: 0.0145,
    workflow: "invoice-flow"
  });

  assert.equal(span.traceId.length, 32);
  assert.equal(span.spanId.length, 16);
  assert.equal(exp.pending, 1);

  const a = span.attributes;
  assert.equal(a["gen_ai.provider.name"], "openai");
  assert.equal(a["gen_ai.request.model"], "gpt-5.5");
  assert.equal(a["gen_ai.response.model"], "gpt-5.5");
  assert.equal(a["gen_ai.operation.name"], "chat");
  assert.equal(a["gen_ai.usage.input_tokens"], 1200);
  assert.equal(a["gen_ai.usage.output_tokens"], 350);
  assert.equal(a["gen_ai.usage.cache_read.input_tokens"], 800);
  assert.equal(a["gen_ai.usage.cost_usd"], 0.0145);
  assert.equal(a["cave.agent"], "billing-agent");
  assert.equal(a["cave.workflow"], "invoice-flow");
});

test("recordSpan() canonicalizes valid IDs and replaces or drops malformed IDs", () => {
  const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "agent" });
  const exp = cave.exporter();
  const canonical = exp.recordSpan("canonical", {
    traceId: "ABCDEF0123456789ABCDEF0123456789",
    spanId: "ABCDEF0123456789",
    parentSpanId: "FEDCBA9876543210",
  });
  assert.equal(canonical.traceId, "abcdef0123456789abcdef0123456789");
  assert.equal(canonical.spanId, "abcdef0123456789");
  assert.equal(canonical.parentSpanId, "fedcba9876543210");

  const malformed = exp.recordSpan("malformed", {
    traceId: "not-a-trace",
    spanId: "0".repeat(16),
    parentSpanId: "bad-parent",
  });
  assert.match(malformed.traceId, /^[0-9a-f]{32}$/);
  assert.match(malformed.spanId, /^[0-9a-f]{16}$/);
  assert.notEqual(malformed.spanId, "0".repeat(16));
  assert.equal(malformed.parentSpanId, "");
});

test("recordSpan() omits malformed counters and prevents reserved-attribute overrides", () => {
  const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "real-agent" });
  const span = cave.exporter().recordSpan("bad telemetry", {
    inputTokens: 10,
    outputTokens: -1,
    cachedTokens: 20,
    costUsd: Number.NaN,
    attributes: {
      "gen_ai.usage.input_tokens": 999999,
      "gen_ai.usage.cost_usd": 999999,
      "cave.agent": "spoofed-agent",
    },
  });

  assert.equal(span.attributes["gen_ai.usage.input_tokens"], 10);
  assert.ok(!("gen_ai.usage.output_tokens" in span.attributes));
  assert.ok(!("gen_ai.usage.cache_read.input_tokens" in span.attributes));
  assert.ok(!("gen_ai.usage.cost_usd" in span.attributes));
  assert.equal(span.attributes["cave.agent"], "real-agent");
});

test("export() POSTs OTLP payload to standard /v1/traces with Caveman headers", async () => {
  mockFetch({ ok: true, spans_accepted: 2, spans_total: 2, otel_schema_version: "genai-2026-06" });
  try {
    const cave = new Cave({ apiKey: "cave_live_test_key", baseURL: "http://localhost:8787", agent: "billing-agent", defaultWorkflow: "invoice-flow" });
    const exp = cave.exporter();

    const trace = exp.newTraceId();
    const chat = exp.recordSpan("chat gpt-5.5", { traceId: trace, provider: "openai", model: "gpt-5.5", operation: "chat", inputTokens: 1200, outputTokens: 350, cachedTokens: 800 });
    exp.recordSpan("tool_call fetch_invoice", { traceId: trace, parentSpanId: chat.spanId, operation: "tool_call", toolName: "fetch_invoice" });

    const result = await exp.export();

    assert.equal(capturedRequests.length, 1);
    const req = capturedRequests[0];

    // Endpoint + method.
    assert.equal(req.url, "http://localhost:8787/v1/traces");
    assert.equal(req.method, "POST");

    // Caveman headers.
    assert.equal(req.headers["x-cave-api-key"], "cave_live_test_key");
    assert.equal(req.headers["x-cave-agent"], "billing-agent");
    assert.equal(req.headers["x-cave-workflow"], "invoice-flow");
    assert.equal(req.headers["content-type"], "application/json");

    // OTLP/JSON shape: resourceSpans -> scopeSpans -> spans.
    const rs = req.body.resourceSpans;
    assert.equal(rs.length, 1);
    const resAttrs = attrMap(rs[0].resource.attributes);
    assert.equal(resAttrs["service.name"], "billing-agent");
    const spans = rs[0].scopeSpans[0].spans;
    assert.equal(spans.length, 2);

    const chatSpan = spans[0];
    assert.equal(chatSpan.traceId, trace);
    assert.equal(chatSpan.parentSpanId, "");
    assert.equal(chatSpan.status.code, 1);
    // Nanosecond timestamps are encoded as strings (proto3 int64 → JSON string).
    assert.equal(typeof chatSpan.startTimeUnixNano, "string");
    const chatAttrs = attrMap(chatSpan.attributes);
    assert.equal(chatAttrs["gen_ai.provider.name"], "openai");
    assert.equal(chatAttrs["gen_ai.response.model"], "gpt-5.5");
    // Token counts encoded as intValue strings.
    assert.equal(chatAttrs["gen_ai.usage.input_tokens"], "1200");
    assert.equal(chatAttrs["gen_ai.usage.output_tokens"], "350");
    assert.equal(chatAttrs["gen_ai.usage.cache_read.input_tokens"], "800");

    const toolSpan = spans[1];
    assert.equal(toolSpan.parentSpanId, chat.spanId);
    const toolAttrs = attrMap(toolSpan.attributes);
    assert.equal(toolAttrs["gen_ai.tool.name"], "fetch_invoice");
    assert.equal(toolAttrs["gen_ai.operation.name"], "tool_call");

    // Parsed gateway response returned; buffer cleared.
    assert.equal(result.ok, true);
    assert.equal(result.spans_accepted, 2);
    assert.equal(exp.pending, 0);
  } finally {
    restoreFetch();
  }
});

test("error status maps to OTLP code 2", () => {
  const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
  const exp = cave.exporter();
  exp.recordSpan("failed-call", { status: "error" });
  const payload = exp.buildPayload();
  const span = payload.resourceSpans[0].scopeSpans[0].spans[0];
  assert.equal(span.status.code, 2);
});

test("unknown runtime status fails closed to OTLP unset", () => {
  const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
  const exp = cave.exporter();
  exp.recordSpan("future-status", { status: "future-status" });
  const payload = exp.buildPayload();
  const span = payload.resourceSpans[0].scopeSpans[0].spans[0];
  assert.equal(span.status.code, 0);
});

test("export() with no spans is a no-op (no fetch)", async () => {
  mockFetch({});
  try {
    const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
    const exp = cave.exporter();
    const result = await exp.export();
    assert.equal(capturedRequests.length, 0, "no fetch when empty");
    assert.equal(result.ok, true);
    assert.equal(result.spans_accepted, 0);
  } finally {
    restoreFetch();
  }
});

test("overlapping exports serialize batches without duplicate or dropped spans", async () => {
  capturedRequests = [];
  let releaseFirst;
  let markFirstStarted;
  const firstStarted = new Promise((resolve) => { markFirstStarted = resolve; });
  const firstRelease = new Promise((resolve) => { releaseFirst = resolve; });
  globalThis.fetch = async (url, init) => {
    capturedRequests.push({ url: String(url), body: JSON.parse(init.body) });
    if (capturedRequests.length === 1) {
      markFirstStarted();
      await firstRelease;
    }
    return { ok: true, json: async () => ({}), text: async () => "" };
  };

  try {
    const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
    const exp = cave.exporter();
    exp.recordSpan("before-export");
    const first = exp.export();
    await firstStarted;

    exp.recordSpan("during-export");
    const second = exp.export();
    assert.equal(exp.pending, 2);
    assert.equal(capturedRequests.length, 1, "second export waits for first batch");

    releaseFirst();
    await Promise.all([first, second]);

    const names = capturedRequests.map((request) =>
      request.body.resourceSpans[0].scopeSpans[0].spans.map((span) => span.name)
    );
    assert.deepEqual(names, [["before-export"], ["during-export"]]);
    assert.equal(exp.pending, 0);
  } finally {
    restoreFetch();
  }
});

test("custom serviceName overrides the agent on the resource", () => {
  const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
  const exp = cave.exporter({ serviceName: "custom-svc" });
  exp.recordSpan("op");
  const payload = exp.buildPayload();
  const resAttrs = attrMap(payload.resourceSpans[0].resource.attributes);
  assert.equal(resAttrs["service.name"], "custom-svc");
});
