/**
 * Runtime tests for SDK → gateway trace continuity.
 * Run with: node --test tests/trace-continuity.runtime.mjs (after `pnpm build`).
 * Mirrors the Python tests/test_trace_continuity.py.
 */
import { test, after } from "node:test";
import assert from "node:assert/strict";

import { Cave } from "../dist/index.js";

let captured = [];
const originalFetch = globalThis.fetch;

function mockFetch(responseData = { ok: true }) {
  captured = [];
  globalThis.fetch = async (url, init) => {
    captured.push({ url: String(url), method: init?.method, headers: init?.headers ?? {}, body: init?.body ? JSON.parse(init.body) : undefined });
    return { ok: true, json: async () => responseData, text: async () => JSON.stringify(responseData) };
  };
}

after(() => { globalThis.fetch = originalFetch; });

const cave = new Cave({ apiKey: "cave_live_test", baseURL: "http://localhost:8787", agent: "a" });

const TRACE_ID = /^[0-9a-f]{32}$/;
const SPAN_ID = /^[0-9a-f]{16}$/;

test("trace mints a 32-hex trace id and a 16-hex root span id", async () => {
  await cave.trace({}, async (t) => {
    assert.match(t.traceId, TRACE_ID);
    assert.match(t.spanId, SPAN_ID);
  });
});

test("two traces get distinct ids", async () => {
  const first = await cave.trace({}, async (t) => ({ traceId: t.traceId, spanId: t.spanId }));
  const second = await cave.trace({}, async (t) => ({ traceId: t.traceId, spanId: t.spanId }));
  assert.notEqual(first.traceId, second.traceId);
  assert.notEqual(first.spanId, second.spanId);
});

test("provider calls inside a trace carry the trace and parent-span headers", async () => {
  mockFetch({ id: "resp_1" });
  const ids = await cave.trace({}, async (t) => {
    await t.model.openai.responses.create({ model: "gpt-4o", input: "hi" });
    await t.model.openai.chat.completions.create({ model: "gpt-4o", messages: [] });
    return { traceId: t.traceId, spanId: t.spanId };
  });
  assert.equal(captured.length, 2);
  for (const req of captured) {
    assert.equal(req.headers["x-cave-trace-id"], ids.traceId);
    assert.equal(req.headers["x-cave-parent-span-id"], ids.spanId);
  }
});

test("an injected trace id and root span id are used verbatim", async () => {
  mockFetch({ id: "resp_1" });
  await cave.trace({ traceId: "0123456789abcdef0123456789abcdef", spanId: "fedcba9876543210" }, async (t) => {
    assert.equal(t.traceId, "0123456789abcdef0123456789abcdef");
    assert.equal(t.spanId, "fedcba9876543210");
    await t.model.openai.responses.create({ model: "gpt-4o", input: "hi" });
  });
  assert.equal(captured[0].headers["x-cave-trace-id"], "0123456789abcdef0123456789abcdef");
  assert.equal(captured[0].headers["x-cave-parent-span-id"], "fedcba9876543210");
});

test("injected ids are canonicalized or replaced, never malformed on the wire", async () => {
  mockFetch({ id: "resp_1" });
  await cave.trace({ traceId: "0123456789ABCDEF0123456789ABCDEF", spanId: "FEDCBA9876543210" }, async (t) => {
    assert.equal(t.traceId, "0123456789abcdef0123456789abcdef");
    assert.equal(t.spanId, "fedcba9876543210");
  });
  for (const traceId of ["0123", "zz23456789abcdef0123456789abcdef", "0".repeat(32)]) {
    await cave.trace({ traceId }, async (t) => {
      assert.match(t.traceId, TRACE_ID);
      assert.notEqual(t.traceId, traceId);
      await t.model.openai.responses.create({ model: "gpt-4o", input: "hi" });
    });
  }
  for (const spanId of ["01", "zedcba9876543210", "0".repeat(16)]) {
    await cave.trace({ spanId }, async (t) => {
      assert.match(t.spanId, SPAN_ID);
      assert.notEqual(t.spanId, spanId);
      await t.model.openai.responses.create({ model: "gpt-4o", input: "hi" });
    });
  }
  for (const req of captured) {
    assert.match(req.headers["x-cave-trace-id"], TRACE_ID);
    assert.match(req.headers["x-cave-parent-span-id"], SPAN_ID);
  }
});

test("trace-scoped SDK calls carry continuity headers", async () => {
  mockFetch({ ok: true, artifact_id: "art_1", stored: true, source_ref: "tenant/ref", messages: [] });
  const ids = await cave.trace({}, async (t) => {
    await t.tool("do_thing", { readOnly: true }, () => "ok");
    await t.artifacts.page({ big: "payload" }, { source: "tool:fetch", strategy: "json-index" });
    await t.context.checkpoint([{ role: "user", content: "hi" }], { keep_last: 1 });
    await t.context.expand("tenant/ref");
    return { traceId: t.traceId, spanId: t.spanId };
  });
  assert.equal(captured.length, 4);
  for (const req of captured) {
    assert.equal(req.headers["x-cave-trace-id"], ids.traceId);
    assert.equal(req.headers["x-cave-parent-span-id"], ids.spanId);
  }
});

test("provider clients built off the Cave carry no continuity headers", async () => {
  mockFetch({ id: "resp_1" });
  await cave.openai().responses.create({ model: "gpt-4o", input: "hi" });
  assert.equal(captured[0].headers["x-cave-trace-id"], undefined);
  assert.equal(captured[0].headers["x-cave-parent-span-id"], undefined);
});

test("the trace-bound exporter reuses the trace id", async () => {
  mockFetch({ ok: true, spans_accepted: 1, spans_total: 1 });
  const traceId = await cave.trace({}, async (t) => {
    const exporter = t.exporter();
    assert.equal(t.exporter(), exporter, "trace exporter is memoized by service name");
    assert.equal(exporter.defaultTraceId, t.traceId);
    const span = exporter.recordSpan("plan", { parentSpanId: t.spanId });
    assert.equal(span.traceId, t.traceId);
    assert.equal(span.parentSpanId, t.spanId);
    await exporter.export();
    return t.traceId;
  });
  assert.equal(captured[0].body.resourceSpans[0].scopeSpans[0].spans[0].traceId, traceId);
});

test("trace exporters with different service names keep separate buffers", async () => {
  await cave.trace({}, async (t) => {
    const defaultExporter = t.exporter();
    const customExporter = t.exporter({ serviceName: "custom" });
    assert.notEqual(defaultExporter, customExporter);
    assert.equal(t.exporter({ serviceName: "custom" }), customExporter);
  });
});

test("an explicit span traceId still wins over the trace binding", async () => {
  await cave.trace({}, async (t) => {
    const span = t.exporter().recordSpan("plan", { traceId: "11112222333344445555666677778888" });
    assert.equal(span.traceId, "11112222333344445555666677778888");
  });
});

test("a Cave-level exporter has no default trace id", () => {
  const exporter = cave.exporter();
  assert.equal(exporter.defaultTraceId, undefined);
  assert.match(exporter.recordSpan("plan").traceId, TRACE_ID);
});
