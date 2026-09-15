import { test } from "node:test";
import assert from "node:assert/strict";
import { Agent as MastraAgent } from "@mastra/core/agent";
import { ToolLoopAgent } from "ai";
import { MockLanguageModelV3 } from "ai/test";
import { Client as EveClient } from "eve/client";
import {
  createClaudeAdapter,
  createEveAdapter,
  createMastraAdapter,
  createPiAdapter,
  createVercelAISDKAdapter,
} from "../dist/adapters.js";
import { compile, defineBuild } from "../dist/build.js";
import { catalogCost } from "../dist/catalog.js";
import { agent, auto, eval as defineEval } from "../dist/index.js";

const hex = (value) => value.repeat(64);
const adapterIdentity = (overrides = {}) => ({
  adapterVersion: "0.1.0",
  upstreamVersion: "0.83.0",
  bundleSHA256: hex("a"),
  dependencyLockSHA256: hex("b"),
  ...overrides,
});

function upstreamMockModel() {
  return new MockLanguageModelV3({
    provider: "anthropic",
    modelId: "claude-haiku-4-5",
    doGenerate: {
      content: [{ type: "text", text: "ok" }],
      finishReason: { unified: "stop", raw: "stop" },
      usage: {
        inputTokens: { total: 10, noCache: 10, cacheRead: 0, cacheWrite: 0 },
        outputTokens: { total: 2, text: 2, reasoning: 0 },
      },
      response: { modelId: "claude-haiku-4-5" },
      warnings: [],
    },
  });
}

function baselinePlan() {
  return {
    schema_version: 1,
    plan_id: "baseline",
    model: "anthropic/claude-haiku-4-5",
    reasoning: "low",
    segment_routes: [],
    budgets: {
      instructions: 1_000,
      tools: 1_000,
      memory: 0,
      history: 2_000,
      results_artifacts: 1_000,
      reasoning: 1_000,
      output: 500,
      retry_cascade_reserve: 0,
    },
    recovery: { namespace: "adapter", tools: [] },
    fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
  };
}

async function buildLock({
  harnessId = "pi",
  adapterVersion = "0.1.0",
  upstreamVersion = "0.83.0",
  reasoning = "low",
} = {}) {
  const plan = baselinePlan();
  plan.reasoning = reasoning;
  const result = await compile({
    agent: agent({ id: "adapter", instructions: "test", model: auto() }),
    contextIR: { schemaVersion: 1, segments: [] },
    evals: [defineEval({
      id: "adapter",
      approved: true,
      input: "x",
      quality: [{ type: "exact_match", expected: "ok" }],
    })],
    candidates: [{ plan, estimated_cost_usd_per_run: 0.001 }],
    baselinePlan: plan,
    seeds: [1, 2, 3, 4, 5],
    config: defineBuild({ entry: "src/agent.ts", evals: "evals/*.ts" }),
    entitled: true,
    sourceSha256: hex("1"),
    catalogSha256: hex("2"),
    transformRegistrySha256: hex("3"),
    runtimeVersion: "0.1.0",
    adapterVersion,
    upstreamVersion,
    harnessId,
    runner: async () => ({
      terminal: true,
      usage_basis: "provider_reported",
      price_basis: "public_catalog",
      catalog_cost_usd: 0.001,
      quality_score: 1,
      graders: [{ type: "exact_match", passed: true }],
      latency_ms: 1,
      provider_visible_tokens: 1,
      cache_prefix_sha256: hex("c"),
      cache_boundary_known: true,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      cache_bust: false,
      error: false,
      recovery_resolved: true,
      privacy_passed: true,
      sandbox_passed: true,
      output_digest: hex("a"),
    }),
  });
  return result.lock;
}

test("Pi evidence adapter binds harness lock, actual model, and recomputed catalog cost", async () => {
  const build = await buildLock();
  const priced = catalogCost({
    provider: "anthropic",
    model: "claude-haiku-4-5",
    inputTokens: 10,
    outputTokens: 2,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    reasoningTokens: 0,
  });
  assert.equal(priced.priced, true);
  const invoke = async () => ({
    terminal: true,
    text: "ok",
    provider: "anthropic",
    model: "claude-haiku-4-5",
    inputTokens: 10,
    outputTokens: 2,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    reasoningTokens: 0,
    totalTokens: 12,
    costUsd: priced.usd,
    usageBasis: "provider_reported",
    priceBasis: "public_catalog",
    evaluatedTransformIDs: [],
    appliedTransformIDs: [],
    recoveryResolved: true,
    latencyMs: 10,
  });
  const adapter = createPiAdapter(adapterIdentity(), invoke);
  const result = await adapter.run({
    build,
    contextIR: { schemaVersion: 1, segments: [] },
    plan: build.selected_plan,
    prompt: "x",
    runID: "run",
    evaluatedTransformIDs: [],
    appliedTransformIDs: [],
    recoveryResolved: true,
  });
  assert.equal(result.planSHA256, build.plan_sha256);
  assert.equal(result.inputTokens, 10);
  assert.equal(result.outputTokens, 2);
  assert.equal(result.verifiedSavingsUsd, 0);
});

test("harness adapter rejects another harness build and adapter version before execution", async () => {
  const build = await buildLock();
  const request = {
    build,
    contextIR: { schemaVersion: 1, segments: [] },
    plan: build.selected_plan,
    prompt: "x",
    runID: "run",
    evaluatedTransformIDs: [],
    appliedTransformIDs: [],
    recoveryResolved: true,
  };
  let calls = 0;
  const invoke = async () => {
    calls++;
    throw new Error("must not execute");
  };
  await assert.rejects(
    createClaudeAdapter(adapterIdentity(), invoke).run(request),
    /cave_harness_build_mismatch/,
  );
  await assert.rejects(
    createPiAdapter(adapterIdentity({ adapterVersion: "different" }), invoke).run(request),
    /cave_harness_build_mismatch/,
  );
  await assert.rejects(
    createPiAdapter(adapterIdentity({ upstreamVersion: "0.84.0" }), invoke).run(request),
    /cave_harness_build_mismatch/,
  );
  assert.equal(calls, 0);
});

test("harness adapter binds Context IR before execution", async () => {
  const build = await buildLock();
  let calls = 0;
  await assert.rejects(
    createPiAdapter(adapterIdentity(), async () => {
      calls++;
      throw new Error("must not execute");
    }).run({
      build,
      contextIR: {
        schemaVersion: 1,
        segments: [{
          id: "changed",
          kind: "history",
          stability: "turn",
          safety: "S0",
          priority: "normal",
          recovery: "none",
          cacheRegion: "live_zone",
          privacy: "local_sensitive",
          opaque: false,
          provenanceDigest: hex("c"),
          tokenCount: 1,
          bodyHandle: `cave_local_sha256:${hex("c")}`,
        }],
      },
      plan: build.selected_plan,
      prompt: "x",
      runID: "run",
      evaluatedTransformIDs: [],
      appliedTransformIDs: [],
      recoveryResolved: true,
    }),
    /cave_harness_context_ir_mismatch/,
  );
  assert.equal(calls, 0);
});

test("adapter contract uses explicit bundle and dependency identity, never function source", () => {
  const first = createPiAdapter(adapterIdentity(), async () => {
    throw new Error("first");
  });
  const equivalent = createPiAdapter(adapterIdentity(), async () => {
    throw new Error("different source");
  });
  const changedBundle = createPiAdapter(
    adapterIdentity({ bundleSHA256: hex("c") }),
    async () => {
      throw new Error("first");
    },
  );
  const changedDependencies = createPiAdapter(
    adapterIdentity({ dependencyLockSHA256: hex("d") }),
    async () => {
      throw new Error("first");
    },
  );
  assert.equal(first.contractSHA256, equivalent.contractSHA256);
  assert.notEqual(first.contractSHA256, changedBundle.contractSHA256);
  assert.notEqual(first.contractSHA256, changedDependencies.contractSHA256);
  assert.equal(first.manifest.upstreamVersion, "0.83.0");
  assert.throws(
    () => createPiAdapter(adapterIdentity({ bundleSHA256: "not-a-digest" }), async () => {
      throw new Error("first");
    }),
    /cave_harness_artifact_digest_invalid/,
  );
});

test("evidence adapter rejects mismatched model and caller-priced cost", async () => {
  const build = await buildLock();
  const request = {
    build,
    contextIR: { schemaVersion: 1, segments: [] },
    plan: build.selected_plan,
    prompt: "x",
    runID: "run",
    evaluatedTransformIDs: [],
    appliedTransformIDs: [],
    recoveryResolved: true,
  };
  const execution = {
    terminal: true,
    text: "ok",
    provider: "openai",
    model: "gpt-5.4-mini",
    inputTokens: 10,
    outputTokens: 2,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    reasoningTokens: 0,
    totalTokens: 12,
    costUsd: 0.001,
    usageBasis: "provider_reported",
    priceBasis: "public_catalog",
    evaluatedTransformIDs: [],
    appliedTransformIDs: [],
    recoveryResolved: true,
    latencyMs: 1,
  };
  await assert.rejects(
    createPiAdapter(adapterIdentity(), async () => execution).run(request),
    /cave_harness_incomplete_evidence/,
  );
  await assert.rejects(
    createPiAdapter(adapterIdentity(), async () => ({
      ...execution,
      provider: "anthropic",
      model: "claude-haiku-4-5",
    })).run(request),
    /cave_harness_incomplete_evidence/,
  );
  const priced = catalogCost({
    provider: "anthropic",
    model: "claude-haiku-4-5",
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    reasoningTokens: 0,
  });
  await assert.rejects(
    createPiAdapter(adapterIdentity(), async () => ({
      ...execution,
      provider: "anthropic",
      model: "claude-haiku-4-5",
      inputTokens: 0,
      outputTokens: 0,
      totalTokens: 0,
      costUsd: priced.usd,
    })).run(request),
    /cave_harness_incomplete_evidence/,
  );
});

function adapterRequest(build, overrides = {}) {
  return {
    build,
    contextIR: { schemaVersion: 1, segments: [] },
    plan: build.selected_plan,
    prompt: "x",
    runID: "run",
    evaluatedTransformIDs: [],
    appliedTransformIDs: [],
    recoveryResolved: true,
    ...overrides,
  };
}

test("Vercel AI SDK adapter executes matching build and normalizes v7 usage", async () => {
  const identity = adapterIdentity({ adapterVersion: "7.0.43", upstreamVersion: "7.0.43" });
  const build = await buildLock({
    harnessId: "vercel-ai-sdk",
    adapterVersion: identity.adapterVersion,
    upstreamVersion: identity.upstreamVersion,
  });
  const calls = [];
  const adapter = createVercelAISDKAdapter(identity, {
    async generate(options) {
      calls.push(options);
      return {
        text: "ok",
        finishReason: "stop",
        response: { modelId: "claude-haiku-4-5" },
        usage: {
          inputTokens: 10,
          inputTokenDetails: { noCacheTokens: 10, cacheReadTokens: 0, cacheWriteTokens: 0 },
          outputTokens: 2,
          outputTokenDetails: { textTokens: 2, reasoningTokens: 0 },
          totalTokens: 12,
        },
      };
    },
  });
  const controller = new AbortController();
  const result = await adapter.run(adapterRequest(build, { signal: controller.signal }));
  assert.equal(calls.length, 1);
  assert.equal(calls[0].prompt, "x");
  assert.equal(calls[0].abortSignal, controller.signal);
  assert.equal(result.harness, "vercel-ai-sdk");
  assert.equal(result.totalTokens, 12);
  assert.equal(result.verifiedSavingsUsd, 0);
});

test("Vercel AI SDK adapter accepts complete usage without optional cache breakdown", async () => {
  const identity = adapterIdentity({ adapterVersion: "7.0.43", upstreamVersion: "7.0.43" });
  const build = await buildLock({
    harnessId: "vercel-ai-sdk",
    adapterVersion: identity.adapterVersion,
    upstreamVersion: identity.upstreamVersion,
  });
  const adapter = createVercelAISDKAdapter(identity, {
    async generate() {
      return {
        text: "ok",
        finishReason: "stop",
        response: { modelId: "claude-haiku-4-5" },
        usage: {
          inputTokens: 10,
          inputTokenDetails: {
            noCacheTokens: undefined,
            cacheReadTokens: undefined,
            cacheWriteTokens: undefined,
          },
          outputTokens: 2,
          outputTokenDetails: { textTokens: 2, reasoningTokens: 0 },
          totalTokens: 12,
        },
      };
    },
  });
  const result = await adapter.run(adapterRequest(build));
  assert.equal(result.inputTokens, 10);
  assert.equal(result.cacheReadTokens, 0);
  assert.equal(result.cacheWriteTokens, 0);
  assert.equal(result.totalTokens, 12);
});

test("Vercel AI SDK adapter executes an actual pinned ToolLoopAgent", async () => {
  const identity = adapterIdentity({ adapterVersion: "7.0.43", upstreamVersion: "7.0.43" });
  const build = await buildLock({
    harnessId: "vercel-ai-sdk",
    adapterVersion: identity.adapterVersion,
    upstreamVersion: identity.upstreamVersion,
  });
  const model = upstreamMockModel();
  const toolLoopAgent = new ToolLoopAgent({ model });
  const result = await createVercelAISDKAdapter(identity, toolLoopAgent).run(adapterRequest(build));
  assert.equal(model.doGenerateCalls.length, 1);
  assert.equal(result.text, "ok");
  assert.equal(result.model, "claude-haiku-4-5");
  assert.equal(result.totalTokens, 12);
});

test("Mastra adapter executes matching build with bounded retries and normalized usage", async () => {
  const identity = adapterIdentity({ adapterVersion: "1.55.0", upstreamVersion: "1.55.0" });
  const build = await buildLock({
    harnessId: "mastra",
    adapterVersion: identity.adapterVersion,
    upstreamVersion: identity.upstreamVersion,
  });
  const calls = [];
  const adapter = createMastraAdapter(identity, {
    async generate(prompt, options) {
      calls.push({ prompt, options });
      return {
        text: "ok",
        finishReason: "stop",
        error: undefined,
        response: { modelId: "claude-haiku-4-5" },
        totalUsage: {
          inputTokens: 10,
          outputTokens: 2,
          totalTokens: 12,
          reasoningTokens: 0,
          cachedInputTokens: 0,
          cacheCreationInputTokens: 0,
        },
      };
    },
  });
  const result = await adapter.run(adapterRequest(build));
  assert.deepEqual(calls, [{
    prompt: "x",
    options: { maxProcessorRetries: 0, runId: "run" },
  }]);
  assert.equal("preExecutionControls" in adapter.manifest.wireContract, false);
  assert.equal(result.harness, "mastra");
  assert.equal(result.model, "claude-haiku-4-5");
});

test("Mastra adapter forwards an opt-in framework-native maxSteps limit before execution", async () => {
  const identity = adapterIdentity({ adapterVersion: "1.55.0", upstreamVersion: "1.55.0" });
  const build = await buildLock({
    harnessId: "mastra",
    adapterVersion: identity.adapterVersion,
    upstreamVersion: identity.upstreamVersion,
  });
  const calls = [];
  const adapter = createMastraAdapter(identity, {
    async generate(prompt, options) {
      calls.push({ prompt, options });
      return {
        text: "ok",
        finishReason: "stop",
        response: { modelId: "claude-haiku-4-5" },
        totalUsage: {
          inputTokens: 10,
          outputTokens: 2,
          totalTokens: 12,
          reasoningTokens: 0,
        },
      };
    },
  }, { maxSteps: 2 });

  const result = await adapter.run(adapterRequest(build));

  assert.deepEqual(calls, [{
    prompt: "x",
    options: { maxProcessorRetries: 0, runId: "run", maxSteps: 2 },
  }]);
  assert.deepEqual(adapter.manifest.wireContract.preExecutionControls, { maxSteps: 2 });
  assert.equal(result.text, "ok");
  assert.equal(result.harness, "mastra");
});

test("Mastra adapter rejects invalid maxSteps before provider execution", () => {
  let calls = 0;
  const binding = {
    async generate() {
      calls++;
      throw new Error("must not execute");
    },
  };
  const identity = adapterIdentity({ adapterVersion: "1.55.0", upstreamVersion: "1.55.0" });

  for (const maxSteps of [0, -1, 1.5, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1]) {
    assert.throws(
      () => createMastraAdapter(identity, binding, { maxSteps }),
      /cave_mastra_max_steps_invalid/,
    );
  }
  assert.equal(calls, 0);
});

test("Mastra adapter accepts complete usage without optional cache counters", async () => {
  const identity = adapterIdentity({ adapterVersion: "1.55.0", upstreamVersion: "1.55.0" });
  const build = await buildLock({
    harnessId: "mastra",
    adapterVersion: identity.adapterVersion,
    upstreamVersion: identity.upstreamVersion,
  });
  const adapter = createMastraAdapter(identity, {
    async generate() {
      return {
        text: "ok",
        finishReason: "stop",
        error: undefined,
        response: { modelId: "claude-haiku-4-5" },
        totalUsage: {
          inputTokens: 10,
          outputTokens: 2,
          totalTokens: 12,
          reasoningTokens: 0,
        },
      };
    },
  });
  const result = await adapter.run(adapterRequest(build));
  assert.equal(result.inputTokens, 10);
  assert.equal(result.cacheReadTokens, 0);
  assert.equal(result.cacheWriteTokens, 0);
  assert.equal(result.totalTokens, 12);
});

test("Mastra adapter executes an actual pinned Agent", async () => {
  const identity = adapterIdentity({ adapterVersion: "1.55.0", upstreamVersion: "1.55.0" });
  const build = await buildLock({
    harnessId: "mastra",
    adapterVersion: identity.adapterVersion,
    upstreamVersion: identity.upstreamVersion,
  });
  const model = upstreamMockModel();
  const mastraAgent = new MastraAgent({
    id: "caveman-adapter-test",
    name: "Caveman adapter test",
    instructions: "Return ok.",
    model,
  });
  const result = await createMastraAdapter(identity, mastraAgent).run(adapterRequest(build));
  assert.equal(model.doGenerateCalls.length, 1);
  assert.equal(result.text, "ok");
  assert.equal(result.model, "claude-haiku-4-5");
  assert.equal(result.totalTokens, 12);
});

test("Eve adapter aggregates durable step usage and validates runtime model identity", async () => {
  const identity = adapterIdentity({ adapterVersion: "0.29.2", upstreamVersion: "0.29.2" });
  const build = await buildLock({
    harnessId: "eve",
    adapterVersion: identity.adapterVersion,
    upstreamVersion: identity.upstreamVersion,
    reasoning: "none",
  });
  const sends = [];
  const adapter = createEveAdapter(identity, {
    async send(input) {
      sends.push(input);
      return {
        async result() {
          return {
            message: "ok",
            status: "completed",
            events: [
              { type: "session.started", data: { runtime: { modelId: "anthropic/claude-haiku-4-5", eveVersion: "0.29.2" } } },
              { type: "step.completed", data: { usage: {
                inputTokens: 10,
                outputTokens: 2,
                cacheReadTokens: 0,
                cacheWriteTokens: 0,
              } } },
            ],
          };
        },
      };
    },
  });
  const controller = new AbortController();
  const result = await adapter.run(adapterRequest(build, { signal: controller.signal }));
  assert.equal(sends.length, 1);
  assert.equal(sends[0].message, "x");
  assert.equal(sends[0].signal, controller.signal);
  assert.equal(result.harness, "eve");
  assert.equal(result.costUsd > 0, true);
});

test("Eve adapter executes an actual pinned ClientSession transport", async () => {
  const identity = adapterIdentity({ adapterVersion: "0.29.2", upstreamVersion: "0.29.2" });
  const build = await buildLock({
    harnessId: "eve",
    adapterVersion: identity.adapterVersion,
    upstreamVersion: identity.upstreamVersion,
    reasoning: "none",
  });
  const events = [
    { type: "session.started", data: { runtime: {
      agentId: "caveman-adapter-test",
      modelId: "anthropic/claude-haiku-4-5",
      eveVersion: "0.29.2",
    } } },
    { type: "message.completed", data: {
      message: "ok", finishReason: "stop", sequence: 0, stepIndex: 0, turnId: "turn-1",
    } },
    { type: "step.completed", data: {
      finishReason: "stop", sequence: 0, stepIndex: 0, turnId: "turn-1",
      usage: { inputTokens: 10, outputTokens: 2, cacheReadTokens: 0, cacheWriteTokens: 0 },
    } },
    { type: "session.completed" },
  ];
  const calls = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (input, init = {}) => {
    const url = String(input);
    calls.push({ url, method: init.method ?? "GET", body: init.body });
    if (url === "https://eve.test/eve/v1/session" && init.method === "POST") {
      return Response.json({ sessionId: "session-real" });
    }
    if (url === "https://eve.test/eve/v1/session/session-real/stream" && init.method === undefined) {
      return new Response(`${events.map((event) => JSON.stringify(event)).join("\n")}\n`, {
        headers: { "content-type": "application/x-ndjson; charset=utf-8" },
      });
    }
    throw new Error(`unexpected Eve request: ${init.method ?? "GET"} ${url}`);
  };
  try {
    const session = new EveClient({ host: "https://eve.test" }).session();
    const result = await createEveAdapter(identity, session).run(adapterRequest(build));
    assert.deepEqual(calls.map(({ method, url }) => ({ method, url })), [
      { method: "POST", url: "https://eve.test/eve/v1/session" },
      { method: "GET", url: "https://eve.test/eve/v1/session/session-real/stream" },
    ]);
    assert.deepEqual(JSON.parse(calls[0].body), { message: "x" });
    assert.equal(result.text, "ok");
    assert.equal(result.model, "claude-haiku-4-5");
    assert.equal(result.totalTokens, 12);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("third-party adapters reject incomplete usage and model drift", async () => {
  const vercelIdentity = adapterIdentity({ adapterVersion: "7.0.43", upstreamVersion: "7.0.43" });
  const vercelBuild = await buildLock({
    harnessId: "vercel-ai-sdk",
    adapterVersion: vercelIdentity.adapterVersion,
    upstreamVersion: vercelIdentity.upstreamVersion,
  });
  await assert.rejects(
    createVercelAISDKAdapter(vercelIdentity, {
      async generate() {
        return {
          text: "ok",
          finishReason: "stop",
          response: { modelId: "wrong-model" },
          usage: {
            inputTokens: 10,
            inputTokenDetails: { noCacheTokens: 10, cacheReadTokens: 0, cacheWriteTokens: 0 },
            outputTokens: 2,
            outputTokenDetails: { textTokens: 2, reasoningTokens: 0 },
            totalTokens: 12,
          },
        };
      },
    }).run(adapterRequest(vercelBuild)),
    /cave_harness_incomplete_evidence/,
  );

  const mastraIdentity = adapterIdentity({ adapterVersion: "1.55.0", upstreamVersion: "1.55.0" });
  const mastraBuild = await buildLock({
    harnessId: "mastra",
    adapterVersion: mastraIdentity.adapterVersion,
    upstreamVersion: mastraIdentity.upstreamVersion,
  });
  await assert.rejects(
    createMastraAdapter(mastraIdentity, {
      async generate() {
        return {
          text: "ok",
          finishReason: "stop",
          response: { modelId: "claude-haiku-4-5" },
          totalUsage: { inputTokens: 10, outputTokens: undefined, totalTokens: 10 },
        };
      },
    }).run(adapterRequest(mastraBuild)),
    /cave_mastra_usage_missing/,
  );
});

test("third-party adapters pin upstream versions and abort before execution", async () => {
  // A caller claiming a version that does not match what is actually installed
  // is refused before execution: the identity is derived from the installed
  // package.json now, not from the caller's string.
  // Specifically a MISMATCH against the installed package.json (ai@7.0.43), not
  // merely "unsupported" against a constant — proves the identity is derived
  // from the installed version, not the caller's claim (old code threw
  // cave_vercel_upstream_version_unsupported here).
  assert.throws(
    () => createVercelAISDKAdapter(adapterIdentity({ upstreamVersion: "7.0.44" }), {
      async generate() { throw new Error("must not execute"); },
    }),
    /cave_vercel_upstream_version_mismatch/,
  );
  assert.throws(
    () => createEveAdapter(adapterIdentity({ upstreamVersion: "0.29.3" }), {
      async send() { throw new Error("must not execute"); },
    }),
    /cave_eve_upstream_version_(unsupported|mismatch)/,
  );
  assert.throws(
    () => createMastraAdapter(adapterIdentity({ upstreamVersion: "1.56.0" }), {
      async generate() { throw new Error("must not execute"); },
    }),
    /cave_mastra_upstream_version_(unsupported|mismatch)/,
  );

  const identity = adapterIdentity({ adapterVersion: "7.0.43", upstreamVersion: "7.0.43" });
  const build = await buildLock({
    harnessId: "vercel-ai-sdk",
    adapterVersion: identity.adapterVersion,
    upstreamVersion: identity.upstreamVersion,
  });
  let calls = 0;
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(
    createVercelAISDKAdapter(identity, {
      async generate() {
        calls++;
        throw new Error("must not execute");
      },
    }).run(adapterRequest(build, { signal: controller.signal })),
    /cave_harness_aborted/,
  );
  assert.equal(calls, 0);
});
