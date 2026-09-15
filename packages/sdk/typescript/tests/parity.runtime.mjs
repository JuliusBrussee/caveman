/**
 * Cross-language SDK parity conformance suite (TypeScript half).
 *
 * Runs every operation in the shared fixtures (../../parity/fixtures.json) against
 * a mocked transport and asserts the exact wire request (method, full URL, header
 * set, body) and the derived result. The Python half (tests/test_parity.py) runs
 * the SAME fixtures. If TS and Python diverge on any field, one side fails — so
 * this file plus the fixtures are a release gate.
 *
 * Run with: node --test tests/parity.runtime.mjs  (after `pnpm build`).
 */
import { test, after } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { AsyncJobsUnavailableError, Cave, RetryLoopBreaker, RetryLoopError } from "../dist/index.js";

const fixtures = JSON.parse(readFileSync(new URL("../../parity/fixtures.json", import.meta.url), "utf8"));
const config = fixtures.config;

// ─── Transport mock ───────────────────────────────────────────────────────────

let captured = [];
const originalFetch = globalThis.fetch;

function installMock(op) {
  captured = [];
  globalThis.fetch = async (url, init) => {
    const body = init?.body ? JSON.parse(init.body) : undefined;
    captured.push({ url: String(url), method: init?.method ?? "GET", headers: init?.headers ?? {}, body });
    if (op.transport === "error") throw new Error("simulated transport error");
    return { ok: true, json: async () => op.response, text: async () => JSON.stringify(op.response) };
  };
}

after(() => {
  globalThis.fetch = originalFetch;
});

function makeCave() {
  return new Cave({
    apiKey: config.api_key,
    baseURL: config.base_url,
    agent: config.agent,
    defaultWorkflow: config.default_workflow,
    retention: config.retention,
    controlURL: config.control_url,
    user: config.user
  });
}

function lowerKeys(h) {
  const out = {};
  for (const k of Object.keys(h)) out[k.toLowerCase()] = h[k];
  return out;
}

function buildCatalog(entries) {
  return entries.map((t) => ({
    name: t.name,
    description: t.description,
    inputSchema: t.input_schema,
    readOnly: t.read_only,
    idempotent: t.idempotent ?? false,
    alwaysLoad: t.always_load,
    handler: () => {}
  }));
}

// ─── Per-operation handlers (return the canonical, snake-keyed result) ─────────

const handlers = {
  context_assemble: contextAssemble,
  context_assemble_self: contextAssemble,
  context_assemble_none: contextAssemble,
  tool_search: toolSearch,
  tool_search_embeddings: toolSearch,
  tools_builder_search: async (cave, input) => {
    const catalog = buildCatalog(input.catalog);
    const handle = cave.tools({ catalog, strategy: input.strategy });
    const opts = {};
    if (input.context !== undefined) opts.context = input.context;
    if (input.max_tools !== undefined) opts.maxTools = input.max_tools;
    if (input.ranker !== undefined) opts.ranker = input.ranker;
    if (input.session_id !== undefined) opts.toolSessionId = input.session_id;
    const r = await handle.search(input.query, opts);
    const out = {
      strategy: handle.strategy,
      initial_tool_names: handle.initial.map((t) => t.name),
      sent_schema_tokens: r.sentSchemaTokens,
      full_schema_tokens: r.fullSchemaTokens,
      deferred_count: r.deferredCount,
      method: r.method,
      token_basis: r.tokenBasis,
      basis: r.basis,
      saved_tokens: r.savedTokens,
      reduction_pct: r.reductionPct,
      tool_count: r.tools.length
    };
    if (r.sessionId !== undefined) out.session_id = r.sessionId;
    return out;
  },
  artifacts_page: (cave, input) =>
    cave.trace({ traceId: input.trace_id, spanId: input.span_id }, async (t) => t.artifacts.page(input.value, input.options)),
  artifacts_get: (cave, input) =>
    cave.trace({ traceId: input.trace_id, spanId: input.span_id }, async (t) => t.artifacts.get(input.artifact_id)),
  model_create_async: (cave, input) =>
    cave.trace({ traceId: input.trace_id, spanId: input.span_id }, async (t) =>
      t.model.openai.responses.create(input.body, { cave: { latencyClass: input.latency_class } })
    ),
  model_create_traced: (cave, input) =>
    cave.trace({ traceId: input.trace_id, spanId: input.span_id }, async (t) => t.model.openai.responses.create(input.body)),
  // A provider client built off the Cave (not a trace) carries no continuity ids.
  provider_create_untraced: (cave, input) => cave.openai().responses.create(input.body),
  bedrock: (cave, input) => {
    const b = cave.bedrock({ region: input.region, endpoint: input.endpoint });
    // Normalize the idiomatic camelCase field to the canonical snake_case wire name.
    return {
      region: b.region,
      endpoint: b.endpoint,
      gateway_prefix: b.gatewayPrefix,
      instrumented: b.instrumented,
      sdk_only: b.sdkOnly,
    };
  },
  bedrock_mantle: (cave, input) => {
    const b = cave.bedrock({ region: input.region, endpoint: input.endpoint });
    return {
      region: b.region,
      endpoint: b.endpoint,
      gateway_prefix: b.gatewayPrefix,
      instrumented: b.instrumented,
      sdk_only: b.sdkOnly,
    };
  },
  compress: async (cave, input) => {
    const opts = {};
    if (input.content_type !== undefined) opts.contentType = input.content_type;
    const r = await cave.compress(input.payload, opts);
    return { output: r.output, content_type: r.contentType, tokens_before: r.tokensBefore, tokens_after: r.tokensAfter, ratio: r.ratio, basis: r.basis, token_count_basis: r.tokenCountBasis, recovery_handle: r.recoveryHandle ?? null, method: r.method ?? null, lossless_to_model: r.losslessToModel ?? null };
  },
  // cavePlan passes the plan through verbatim (snake_case wire fields), so the
  // parity result equals the canned response byte-for-byte.
  cave_plan: (cave) => cave.cavePlan(),
  compress_toon: async (cave, input) => handlers.compress(cave, input),
  compress_passthrough: async (cave, input) => handlers.compress(cave, input),
  compress_bad_report: async (cave, input) => handlers.compress(cave, input),
  compress_optimistic_ratio: async (cave, input) => handlers.compress(cave, input),
  compress_unchanged_false_claim: async (cave, input) => handlers.compress(cave, input),
  checkpoint: (cave, input) => cave.trace({ traceId: input.trace_id, spanId: input.span_id }, async (t) => t.context.checkpoint(input.messages, input.options)),
  context_pack: async (cave, input) => {
    const items = input.items.map((item) => ({
      id: item.id,
      text: item.text,
      ...(item.tokens !== undefined ? { tokens: item.tokens } : {}),
      ...(item.timestamp !== undefined ? { timestamp: item.timestamp } : {}),
      ...(item.priority !== undefined ? { priority: item.priority } : {}),
      ...(item.pin !== undefined ? { pin: item.pin } : {})
    }));
    const options = {
      maxTokens: input.options.max_tokens,
      ...(input.options.reserve_tokens !== undefined ? { reserveTokens: input.options.reserve_tokens } : {}),
      ...(input.options.now !== undefined ? { now: input.options.now } : {}),
      ...(input.options.recency_half_life_ms !== undefined ? { recencyHalfLifeMs: input.options.recency_half_life_ms } : {}),
      ...(input.options.recency_weight !== undefined ? { recencyWeight: input.options.recency_weight } : {}),
      ...(input.options.error_boost !== undefined ? { errorBoost: input.options.error_boost } : {})
    };
    const r = await cave.context.pack(input.query, items, options);
    return {
      items: r.items.map((item) => ({
        id: item.id,
        text: item.text,
        ...(item.tokens !== undefined ? { tokens: item.tokens } : {}),
        ...(item.timestamp !== undefined ? { timestamp: item.timestamp } : {}),
        ...(item.priority !== undefined ? { priority: item.priority } : {}),
        ...(item.pin !== undefined ? { pin: item.pin } : {})
      })),
      tokens_used: r.tokensUsed,
      tokens_before: r.tokensBefore,
      tokens_saved: r.tokensSaved,
      deferred_count: r.deferredCount,
      deferred_ids: r.deferredIds,
      basis: r.basis
    };
  },
  checkpoint_expand: (cave, input) => cave.trace({ traceId: input.trace_id, spanId: input.span_id }, async (t) => t.context.expand(input.source_ref)),
  event_tool_call: (cave, input) => cave.trace({ tags: input.tags, traceId: input.trace_id, spanId: input.span_id }, async (t) => t.tool(input.name, input.options, () => "tool-result")),
  otlp_export: async (cave, input) => {
    const ex = cave.exporter({ serviceName: input.service_name });
    const s = input.span;
    ex.recordSpan(s.name, {
      traceId: s.trace_id,
      spanId: s.span_id,
      startTimeNs: s.start_time_ns,
      endTimeNs: s.end_time_ns,
      operation: s.operation,
      provider: s.provider,
      model: s.model,
      inputTokens: s.input_tokens,
      outputTokens: s.output_tokens
    });
    return ex.export();
  },
  // The trace-bound exporter reuses the trace's id, so SDK spans and the
  // gateway's request rows land in one trace.
  otlp_export_traced: async (cave, input) =>
    cave.trace({ traceId: input.trace_id, spanId: input.root_span_id }, async (t) => {
      const ex = t.exporter({ serviceName: input.service_name });
      const s = input.span;
      ex.recordSpan(s.name, {
        spanId: s.span_id,
        parentSpanId: t.spanId,
        startTimeNs: s.start_time_ns,
        endTimeNs: s.end_time_ns,
        operation: s.operation,
        provider: s.provider,
        model: s.model,
        inputTokens: s.input_tokens,
        outputTokens: s.output_tokens
      });
      return ex.export();
    }),
  jobs_unavailable: async (cave, input) => {
    try {
      await cave.jobs.submit(input.body, { latencyClass: input.latency_class });
      throw new Error("jobs.submit unexpectedly succeeded");
    } catch (error) {
      assert.ok(error instanceof AsyncJobsUnavailableError);
      return { error_code: error.code };
    }
  },
  retry_loop_breaker: runBreaker,
  retry_loop_breaker_key_order: runBreaker
};

function contextAssemble(cave, input) {
  const r = cave.assemble({
    provider: input.provider,
    model: input.model,
    sessionId: input.session_id,
    slots: input.slots,
    ...(input.emit_cache_hints !== undefined ? { emitCacheHints: input.emit_cache_hints } : {})
  });
  return {
    request: r.request,
    headers: r.headers,
    prefix_hash: r.prefixHash,
    breakpoints: r.breakpoints,
    stable_tokens: r.stableTokens,
    token_basis: r.tokenBasis,
    basis: r.basis,
    volatile_below_breakpoint: r.volatileBelowBreakpoint
  };
}

async function toolSearch(cave, input) {
  const catalog = buildCatalog(input.catalog);
  const opts = {};
  if (input.context !== undefined) opts.context = input.context;
  if (input.max_tools !== undefined) opts.maxTools = input.max_tools;
  if (input.ranker !== undefined) opts.ranker = input.ranker;
  if (input.session_id !== undefined) opts.toolSessionId = input.session_id;
  const r = await cave.toolSearch(catalog, input.query, opts);
  const out = {
    sent_schema_tokens: r.sentSchemaTokens,
    full_schema_tokens: r.fullSchemaTokens,
    deferred_count: r.deferredCount,
    method: r.method,
    token_basis: r.tokenBasis,
    basis: r.basis,
    saved_tokens: r.savedTokens,
    reduction_pct: r.reductionPct,
    tool_count: r.tools.length
  };
  if (r.sessionId !== undefined) out.session_id = r.sessionId;
  return out;
}

function runBreaker(_cave, input) {
  const breaker = new RetryLoopBreaker(input.threshold);
  let firedAtCall = -1;
  let repeats = 0;
  let threshold = input.threshold;
  for (let i = 0; i < input.calls.length; i++) {
    try {
      breaker.record(input.calls[i].name, input.calls[i].args);
    } catch (e) {
      assert.ok(e instanceof RetryLoopError, "breaker must throw RetryLoopError");
      firedAtCall = i;
      repeats = e.repeats;
      threshold = e.threshold;
      break;
    }
  }
  return { fired_at_call: firedAtCall, repeats, threshold };
}

// ─── Drive every fixture ───────────────────────────────────────────────────────

for (const op of fixtures.operations) {
  test(`parity: ${op.name}`, async () => {
    const handler = handlers[op.name];
    assert.ok(handler, `no TS parity handler for operation "${op.name}" — the SDK is missing this capability`);

    installMock(op);
    const actual = await handler(makeCave(), op.input);

    if (op.expect.wire) {
      assert.equal(captured.length, 1, `${op.name}: expected exactly one wire request, got ${captured.length}`);
      const req = captured[0];
      const base = op.expect.wire.base === "control" ? config.control_url : config.base_url;
      assert.equal(req.url, base + op.expect.wire.path, `${op.name}: url`);
      assert.equal(req.method, op.expect.wire.method, `${op.name}: method`);
      const expHeaders = typeof op.expect.wire.headers === "string" ? fixtures[op.expect.wire.headers] : op.expect.wire.headers;
      assert.deepStrictEqual(lowerKeys(req.headers), expHeaders, `${op.name}: headers`);
      if (op.expect.wire.body !== undefined) {
        assert.deepStrictEqual(req.body, op.expect.wire.body, `${op.name}: body`);
      } else if (op.expect.wire.body_keys !== undefined) {
        assert.deepStrictEqual(Object.keys(req.body ?? {}).sort(), [...op.expect.wire.body_keys].sort(), `${op.name}: body_keys`);
      }
    } else {
      assert.equal(captured.length, 0, `${op.name}: expected no wire request`);
    }

    const expected = op.expect.result_from === "response" ? op.response : op.expect.result;
    assert.deepStrictEqual(actual, expected, `${op.name}: result`);
  });
}

test("parity: fixtures cover the documented operations", () => {
  // Guard against an empty/short fixture file silently passing the gate.
  assert.ok(fixtures.operations.length >= 10, "parity fixtures must cover the SDK surface");
  for (const op of fixtures.operations) {
    assert.ok(handlers[op.name], `TS parity handler missing for "${op.name}"`);
  }
});

test("bedrock descriptor rejects an unknown endpoint", () => {
  assert.throws(
    () => makeCave().bedrock({ region: "us-east-1", endpoint: "other" }),
    /runtime or mantle/,
  );
});
