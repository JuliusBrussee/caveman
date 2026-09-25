import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import Ajv2020 from "ajv/dist/2020.js";
import { resolve } from "node:path";
import {
  agentDefinitionSHA256,
  checkLock,
  compile,
  compileAndWrite,
  contextIRSHA256,
  defineBuild,
  generateCandidatePlans,
  knownGrader,
  parseCaveBuildLock,
} from "../dist/build.js";
import { catalogCost, catalogSearchCeiling } from "../dist/catalog.js";
import {
  agent,
  auto,
  contextIRFromWire,
  contextIRToWire,
  eval as defineEval,
  lowerContext,
  subagent,
} from "../dist/index.js";

const hex = (character) => character.repeat(64);

function plan(id, model = "anthropic/claude-haiku-4-5") {
  return {
    schema_version: 1,
    plan_id: id,
    model,
    reasoning: "low",
    segment_routes: [],
    budgets: {
      instructions: 2_000,
      tools: 2_000,
      memory: 1_000,
      history: 4_000,
      results_artifacts: 2_000,
      reasoning: 1_000,
      output: 500,
      retry_cascade_reserve: 500,
    },
    recovery: { namespace: "test", tools: [] },
    fallbacks: {
      unknown: "original",
      transform_error: "original",
      not_smaller: "original",
    },
  };
}

function transformedPlan(id) {
  return {
    ...plan(id),
    segment_routes: [{
      segment_kind: "history",
      transform_id: "caveman.engine.text.v1",
      fallback: "original",
    }],
  };
}

function fixture(id = "quality") {
  return defineEval({
    id,
    approved: true,
    input: "test",
    quality: [{ type: "contains", fragments: ["ok"] }],
  });
}

function baseInput(overrides = {}) {
  const baseline = plan("baseline");
  return {
    agent: agent({
      id: "compiler-test",
      instructions: "Be exact.",
      model: auto(),
    }),
    contextIR: { schemaVersion: 1, segments: [] },
    evals: [fixture("one"), fixture("two")],
    candidates: [
      { plan: baseline, estimated_cost_usd_per_run: 0.02 },
      { plan: plan("cheap-fail"), estimated_cost_usd_per_run: 0.005 },
      { plan: plan("pass-a"), estimated_cost_usd_per_run: 0.015 },
      { plan: plan("pass-b"), estimated_cost_usd_per_run: 0.012 },
      { plan: plan("unpriced", "unknown/no-price"), estimated_cost_usd_per_run: 0, static_rejection: "unpriced_model" },
    ],
    baselinePlan: baseline,
    seeds: [1, 2, 3, 4, 5],
    config: defineBuild({
      entry: "./src/agent.ts",
      evals: "./evals/*.eval.ts",
      maxSearchCostUsd: 2,
    }),
    entitled: true,
    sourceSha256: hex("1"),
    catalogSha256: hex("2"),
    transformRegistrySha256: hex("3"),
    runtimeVersion: "1.0.0",
    adapterVersion: "1.0.0",
    upstreamVersion: "0.83.0",
    async runner({ plan: candidate }) {
      const failing = candidate.plan_id === "cheap-fail";
      const costs = { baseline: 0.02, "cheap-fail": 0.005, "pass-a": 0.015, "pass-b": 0.012 };
      return evidence({
        cost: costs[candidate.plan_id],
        quality: failing ? 0.5 : candidate.plan_id === "pass-b" ? 0.99 : 1,
        passed: !failing,
        latency: candidate.plan_id === "pass-b" ? 120 : 100,
        tokens: candidate.plan_id === "pass-b" ? 700 : 900,
      });
    },
    ...overrides,
  };
}

function evidence({
  cost = 0.01,
  quality = 1,
  passed = true,
  latency = 100,
  tokens = 1_000,
  cachePrefix = hex("c"),
  cacheRead = 0,
  cacheWrite = 0,
} = {}) {
  return {
    terminal: true,
    usage_basis: "provider_reported",
    price_basis: "public_catalog",
    catalog_cost_usd: cost,
    quality_score: quality,
    graders: [{ type: "contains", passed }],
    latency_ms: latency,
    provider_visible_tokens: tokens,
    cache_prefix_sha256: cachePrefix,
    cache_boundary_known: true,
    cache_read_tokens: cacheRead,
    cache_write_tokens: cacheWrite,
    cache_bust: false,
    error: false,
    recovery_resolved: true,
    privacy_passed: true,
    sandbox_passed: true,
    output_digest: hex("a"),
  };
}

test("compiler locks lowest-cost complete passing build after exhaustive search", async () => {
  const result = await compile(baseInput());
  assert.equal(result.status, "locked");
  assert.equal(result.completed_runs, 40);
  assert.equal(result.static_rejections, 1);
  assert.equal(result.lock.selected_plan_id, "pass-b");
  assert.equal(result.baseline_catalog_cost_usd_per_task, 0.02);
  assert.equal(result.selected_catalog_cost_usd_per_task, 0.012);
  assert.equal(result.lock.evidence.basis, "inferred");
  assert.match(result.lock.build_sha256, /^[0-9a-f]{64}$/);
  assert.equal("timestamp" in result.lock, false);
  const schemaRoot = new URL("../../shared/contracts/schemas/", import.meta.url);
  const [buildSchema, planSchema] = await Promise.all([
    readFile(new URL("cave-build.schema.json", schemaRoot), "utf8").then(JSON.parse),
    readFile(new URL("cave-plan.schema.json", schemaRoot), "utf8").then(JSON.parse),
  ]);
  const ajv = new Ajv2020({ strict: true, allErrors: true });
  ajv.addSchema(planSchema);
  const validate = ajv.compile(buildSchema);
  assert.equal(validate(result.lock), true, JSON.stringify(validate.errors));
});

test("compileAndWrite removes its temporary lock after an atomic rename failure", async () => {
  const root = await mkdtemp(resolve(tmpdir(), "caveman-build-lock-"));
  const target = resolve(root, "occupied");
  await mkdir(target);
  try {
    await assert.rejects(compileAndWrite(baseInput(), target));
    assert.deepEqual(await readdir(root), ["occupied"]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("Context IR wire codec matches public schema and rejects unknown fields", async () => {
  const lowered = await lowerContext({
    instructions: "Be exact.",
    tools: [],
    input: "hello",
  });
  const wire = contextIRToWire(lowered.ir);
  assert.equal("schemaVersion" in wire, false);
  assert.equal("cacheRegion" in wire.segments[0], false);
  const schema = JSON.parse(await readFile(
    new URL("../../shared/contracts/schemas/context-ir.schema.json", import.meta.url),
    "utf8",
  ));
  const validate = new Ajv2020({ strict: true }).compile(schema);
  assert.equal(validate(wire), true, JSON.stringify(validate.errors));
  assert.deepEqual(contextIRFromWire(wire), lowered.ir);
  assert.throws(
    () => contextIRFromWire({ ...wire, unknown: true }),
    /cave_context_ir_invalid/,
  );
});

test("host sandbox agent is lock-ineligible and never reaches a search run", async () => {
  let runs = 0;
  const input = baseInput({
    agent: agent({
      id: "host-compiler-test",
      instructions: "Be exact.",
      model: auto(),
      sandbox: "host",
    }),
    async runner() {
      runs += 1;
      return evidence();
    },
  });
  await assert.rejects(compile(input), /cave_host_sandbox_lock_ineligible/);
  assert.equal(runs, 0);
  const contained = await compile({ ...input, agent: baseInput().agent });
  assert.equal(contained.status, "locked");
});

test("a host subagent under a contained root is lock-ineligible too", async () => {
  let runs = 0;
  // The root is contained; the subagent is not. Host closures run in this
  // process wherever they sit in the graph, so the evidence proves no
  // containment either way and the lock the CLI would save is not earned.
  const input = baseInput({
    agent: agent({
      id: "host-subagent-compiler-test",
      instructions: "Be exact.",
      model: auto(),
      sandbox: "fixture",
      tools: [subagent({
        name: "delegate",
        description: "Delegate to a host-mode agent.",
        agent: agent({
          id: "host-child",
          instructions: "Runs on the host.",
          model: auto(),
          sandbox: "host",
        }),
      })],
    }),
    async runner() {
      runs += 1;
      return evidence();
    },
  });
  await assert.rejects(compile(input), /cave_host_sandbox_lock_ineligible/);
  assert.equal(runs, 0);
});

test("missing usage makes whole search incomplete and writes no lock", async () => {
  const input = baseInput({
    candidates: [
      { plan: plan("baseline"), estimated_cost_usd_per_run: 0.01 },
      { plan: plan("missing"), estimated_cost_usd_per_run: 0.01 },
    ],
  });
  input.runner = async ({ plan: candidate }) => ({
    ...evidence(),
    usage_basis: candidate.plan_id === "missing" ? "missing" : "provider_reported",
  });
  const result = await compile(input);
  assert.equal(result.status, "incomplete_evidence");
  assert.equal(result.lock, undefined);
});

test("runner rejection and timeout return incomplete evidence without lock", async () => {
  let aborted = false;
  let completedAfterAbort = false;
  for (const runner of [
    async () => { throw new Error("adapter_timeout"); },
    async ({ signal }) => new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        completedAfterAbort = true;
        resolve(evidence());
      }, 100);
      signal.addEventListener("abort", () => {
        aborted = true;
        clearTimeout(timer);
        reject(signal.reason);
      }, { once: true });
    }),
  ]) {
    const result = await compile(baseInput({
      runner,
      runnerTimeoutMs: 5,
    }));
    assert.equal(result.status, "incomplete_evidence");
    assert.equal(result.lock, undefined);
    assert.equal(result.completed_runs, 0);
    assert.equal(result.actual_cost_usd, null);
  }
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(aborted, true);
  assert.equal(completedAfterAbort, false);
});

test("runner deadline returns even when adapter ignores AbortSignal", async () => {
  const started = Date.now();
  const result = await compile(baseInput({
    runner: async () => new Promise(() => {}),
    runnerTimeoutMs: 10,
  }));
  assert.equal(result.status, "incomplete_evidence");
  assert.equal(result.lock, undefined);
  assert.equal(result.completed_runs, 0);
  assert.equal(result.actual_cost_usd, null);
  assert.ok(Date.now() - started < 500, "uncooperative runner outlived hard compiler deadline");
});

test("estimated ceiling stops before paid runner starts", async () => {
  let calls = 0;
  const input = baseInput({
    config: defineBuild({
      entry: "./src/agent.ts",
      evals: "./evals/*.eval.ts",
      maxSearchCostUsd: 0.001,
    }),
  });
  input.runner = async () => {
    calls++;
    return evidence();
  };
  const result = await compile(input);
  assert.equal(result.status, "search_budget_exceeded");
  assert.equal(calls, 0);
});

test("unimplemented residency policy fails closed before search", () => {
  assert.throws(
    () => defineBuild({
      entry: "src/agent.ts",
      evals: "evals/*.eval.ts",
      dataResidency: "eu",
    }),
    /dataResidency is not enforced yet; refusing to ignore residency policy/,
  );
});

test("no approved eval returns needs_eval without runner", async () => {
  let calls = 0;
  const result = await compile(baseInput({
    evals: [defineEval({
      id: "draft",
      input: "x",
      quality: [{ type: "contains", fragments: ["x"] }],
    })],
    async runner() {
      calls++;
      return evidence();
    },
  }));
  assert.equal(result.status, "needs_eval");
  assert.equal(calls, 0);
});

test("fixture guardrails gate a lock independently from quality", async () => {
  const strict = defineEval({
    id: "strict",
    approved: true,
    input: "x",
    quality: [{ type: "contains", fragments: ["ok"] }],
    guardrails: [{ type: "latency_threshold", p95_ms: 50 }],
  });
  const result = await compile(baseInput({ evals: [strict] }));
  assert.equal(result.status, "no_passing_build");
});

test("cache prefix drift blocks lock even when quality and cost pass", async () => {
  const input = baseInput({
    candidates: [{ plan: plan("baseline"), estimated_cost_usd_per_run: 0.01 }],
  });
  input.runner = async ({ seed }) =>
    evidence({ cachePrefix: seed === 5 ? hex("d") : hex("c") });
  const result = await compile(input);
  assert.equal(result.status, "no_passing_build");
  assert.equal(result.lock, undefined);
});

test("warm cache cost regression rejects cheaper cold candidate", async () => {
  const baseline = plan("baseline");
  const candidate = transformedPlan("warm-regression");
  const input = baseInput({
    baselinePlan: baseline,
    candidates: [
      { plan: baseline, estimated_cost_usd_per_run: 0.01 },
      { plan: candidate, estimated_cost_usd_per_run: 0.01 },
    ],
    runner: async ({ plan: selected, seed }) => selected.plan_id === baseline.plan_id
      ? evidence({ cost: 0.01, cachePrefix: hex("c") })
      : evidence({
        cost: seed === 1 ? 0.001 : 0.011,
        cachePrefix: hex("d"),
        cacheRead: seed === 1 ? 0 : 10,
        cacheWrite: seed === 1 ? 10 : 0,
      }),
  });
  const result = await compile(input);
  assert.equal(result.status, "locked");
  assert.equal(result.lock.selected_plan_id, baseline.plan_id);
});

test("new cache epoch cannot lock without lower cold-plus-warm cost", async () => {
  const baseline = plan("baseline");
  const candidate = transformedPlan("equal-new-epoch");
  const input = baseInput({
    baselinePlan: baseline,
    candidates: [
      { plan: baseline, estimated_cost_usd_per_run: 0.01 },
      { plan: candidate, estimated_cost_usd_per_run: 0.01 },
    ],
    runner: async ({ plan: selected, seed }) =>
      evidence({
        cost: 0.01,
        cachePrefix: selected.plan_id === baseline.plan_id ? hex("c") : hex("d"),
        cacheRead: selected.plan_id === baseline.plan_id || seed === 1 ? 0 : 10,
        cacheWrite: selected.plan_id === baseline.plan_id || seed !== 1 ? 0 : 10,
      }),
  });
  const result = await compile(input);
  assert.equal(result.status, "locked");
  assert.equal(result.lock.selected_plan_id, baseline.plan_id);
});

test("registry frontier covers profiled candidates and locks per-segment best-of", async () => {
  const baseline = plan("baseline");
  const segment = (id, kind, opaque = false) => ({
    id,
    kind,
    stability: kind === "tool_schema" ? "build" : "turn",
    safety: "S4",
    priority: "normal",
    recovery: "exact_ccr",
    cacheRegion: kind === "tool_schema" ? "frozen_prefix" : "live_zone",
    privacy: "local_sensitive",
    opaque,
    provenanceDigest: hex(id.length.toString(16).slice(-1)),
    tokenCount: 500,
    bodyHandle: `cave_local_sha256:${hex(id.length.toString(16).slice(-1))}`,
  });
  const contextIR = {
    schemaVersion: 1,
    segments: [
      segment("fixture.json", "skill"),
      segment("fixture.logs", "history"),
      segment("fixture.code", "artifact"),
      segment("fixture.tool-schema", "tool_schema"),
      segment("fixture.large-result", "tool_result"),
      segment("fixture.opaque-signed", "artifact", true),
    ],
  };
  const preferred = new Map([
    ["fixture.json", "caveman.engine.json.v1"],
    ["fixture.logs", "caveman.engine.log.v1"],
    ["fixture.code", "caveman.engine.code.v1"],
    ["fixture.tool-schema", "caveman.engine.toolschema.v1"],
    ["fixture.large-result", "caveman.engine.repetition.v1"],
    ["fixture.opaque-signed", "caveman.engine.text.v1"],
  ]);
  const generated = generateCandidatePlans(
    agent({ id: "frontier", instructions: "test", model: auto() }),
    contextIR,
    baseline,
    [baseline.model],
    true,
    preferred,
  );
  const best = generated.find((candidate) => candidate.plan.plan_id.endsWith(".profiled-best-of"));
  assert.ok(best);
  assert.deepEqual(
    best.plan.segment_routes.map((route) => [route.segment_id, route.segment_kind, route.transform_id]),
    [
      ["fixture.json", "skill", "caveman.engine.json.v1"],
      ["fixture.logs", "history", "caveman.engine.log.v1"],
      ["fixture.code", "artifact", "caveman.engine.code.v1"],
      ["fixture.tool-schema", "tool_schema", "caveman.engine.toolschema.v1"],
      ["fixture.large-result", "tool_result", "caveman.engine.repetition.v1"],
    ],
  );
  assert.equal(best.plan.segment_routes.some((route) => route.transform_id === "caveman.engine.text.v1"), false);
  const input = baseInput({
    contextIR,
    baselinePlan: baseline,
    candidates: [
      { plan: baseline, estimated_cost_usd_per_run: 0.02 },
      { ...best, estimated_cost_usd_per_run: 0.001 },
    ],
    evals: [fixture()],
    runner: async ({ plan: candidate, seed }) =>
      evidence({
        cost: candidate.plan_id === baseline.plan_id ? 0.02 : 0.001,
        cacheRead: candidate.plan_id === baseline.plan_id || seed === 1 ? 0 : 10,
        cacheWrite: candidate.plan_id === baseline.plan_id || seed !== 1 ? 0 : 10,
      }),
  });
  const result = await compile(input);
  assert.equal(result.status, "locked");
  assert.equal(result.lock.selected_plan_id, best.plan.plan_id);
});

test("runtime-only history and tool results enter evaluated candidate frontier", () => {
  const baseline = plan("baseline");
  const defined = agent({
    id: "dynamic-frontier",
    instructions: "test",
    model: auto(),
    tools: [{
      kind: "tool",
      name: "lookup",
      description: "lookup",
      input: {},
      effect: "read",
      result: "inline",
      timeoutMs: 100,
      execute: async () => "ok",
    }],
  });
  const generated = generateCandidatePlans(
    defined,
    { schemaVersion: 1, segments: [] },
    baseline,
    [baseline.model],
    true,
    new Map(),
    undefined,
    new Set(["history", "tool_result"]),
  );
  assert.equal(generated.some(({ plan: candidate }) =>
    candidate.segment_routes.some((route) => route.segment_kind === "history")), true);
  assert.equal(generated.some(({ plan: candidate }) =>
    candidate.segment_routes.some((route) => route.segment_kind === "tool_result")), true);
  assert.equal(generated.some(({ plan: candidate }) =>
    candidate.segment_routes.some((route) => route.segment_kind === "history") &&
    candidate.segment_routes.some((route) => route.segment_kind === "tool_result")), true);
});

test("normal tool declarations produce provider-tool schema candidates", async () => {
  const defined = agent({
    id: "tool-frontier",
    instructions: "Use tools.",
    model: auto(),
    tools: [{
      kind: "tool",
      name: "lookup",
      description: "Lookup a record. Extra selection detail that can be compacted.",
      input: { type: "object", title: "Lookup", properties: { id: { type: "string" } }, required: ["id"] },
      effect: "read",
      result: "inline",
      timeoutMs: 1_000,
      execute: async () => "ok",
    }],
    sandbox: "fixture",
  });
  const lowered = await lowerContext({
    instructions: defined.instructions,
    tools: defined.tools,
    input: "lookup",
  });
  const toolSegment = lowered.ir.segments.find((segment) => segment.id === "tool.lookup");
  assert.equal(toolSegment?.safety, "S4");
  const generated = generateCandidatePlans(
    defined,
    lowered.ir,
    plan("baseline"),
    ["anthropic/claude-haiku-4-5"],
    true,
  );
  assert.equal(generated.some((candidate) => candidate.plan.segment_routes.some((route) =>
    route.segment_kind === "tool_schema" &&
    route.transform_id === "caveman.engine.toolschema.v1")), true);
});

test("five independent zero-cache transform runs cannot lock", async () => {
  const baseline = plan("baseline");
  const candidate = transformedPlan("zero-cache");
  const result = await compile(baseInput({
    baselinePlan: baseline,
    candidates: [
      { plan: baseline, estimated_cost_usd_per_run: 0.02 },
      { plan: candidate, estimated_cost_usd_per_run: 0.001 },
    ],
    evals: [fixture()],
    runner: async ({ plan: selected }) =>
      evidence({ cost: selected.plan_id === baseline.plan_id ? 0.02 : 0.001 }),
  }));
  assert.equal(result.status, "locked");
  assert.equal(result.lock.selected_plan_id, baseline.plan_id);
});

test("lock drift names every reproducibility input that changed", async () => {
  const result = await compile(baseInput());
  const checked = checkLock(result.lock, {
    sourceSha256: hex("9"),
    agentDefinitionSha256: result.lock.agent_definition_sha256,
    contextIRSha256: result.lock.context_ir_sha256,
    evalSuiteSha256: result.lock.eval_suite_sha256,
    runtimeVersion: "2.0.0",
    adapterVersion: "2.0.0",
    upstreamVersion: "0.84.0",
    transformRegistrySha256: hex("8"),
    externalProvenanceSha256: hex("7"),
    catalogSha256: hex("6"),
  });
  assert.equal(checked.valid, false);
  assert.deepEqual(checked.stale, [
    "source",
    "runtime",
    "adapter",
    "upstream",
    "transform_registry",
    "external_provenance",
    "catalog",
  ]);
});

test("strict lock binds fully evaluated agent values and lowered context bytes", async () => {
  const firstAgent = agent({
    id: "evaluated-lock",
    instructions: "environment says ALLOW",
    model: auto(),
  });
  const secondAgent = agent({
    id: "evaluated-lock",
    instructions: "environment says DENY",
    model: auto(),
  });
  const firstContext = await lowerContext({
    instructions: firstAgent.instructions,
    tools: firstAgent.tools,
    input: "fixture input",
  });
  const secondContext = await lowerContext({
    instructions: secondAgent.instructions,
    tools: secondAgent.tools,
    input: "fixture input",
  });
  assert.notEqual(agentDefinitionSHA256(firstAgent), agentDefinitionSHA256(secondAgent));
  assert.notEqual(contextIRSHA256(firstContext.ir), contextIRSHA256(secondContext.ir));

  const result = await compile(baseInput({ agent: firstAgent, contextIR: firstContext.ir }));
  const checked = checkLock(result.lock, {
    sourceSha256: result.lock.source_sha256,
    agentDefinitionSha256: agentDefinitionSHA256(secondAgent),
    contextIRSha256: contextIRSHA256(secondContext.ir),
    evalSuiteSha256: result.lock.eval_suite_sha256,
    runtimeVersion: result.lock.runtime.caveman_version,
    adapterVersion: result.lock.harness.adapter_version,
    upstreamVersion: result.lock.harness.upstream_version,
    transformRegistrySha256: result.lock.runtime.transform_registry_sha256,
    externalProvenanceSha256: result.lock.runtime.external_provenance_sha256,
    catalogSha256: result.lock.catalog_sha256,
  });
  assert.deepEqual(checked.stale, ["agent_definition", "context_ir"]);
});

test("strict lock parser rejects altered selected plan with copied digests", async () => {
  const result = await compile(baseInput());
  const forged = structuredClone(result.lock);
  forged.selected_plan.model = "openai/gpt-5.4-mini";
  assert.throws(() => parseCaveBuildLock(forged), /plan_digest/);
});

test("catalog pricing treats Pi input/cache classes as disjoint", () => {
  const priced = catalogCost({
    provider: "anthropic",
    model: "claude-haiku-4-5",
    inputTokens: 100,
    outputTokens: 50,
    cacheReadTokens: 1_000,
    cacheWriteTokens: 200,
    reasoningTokens: 10,
  });
  assert.equal(priced.priced, true);
  assert.equal(priced.usd, 0.0007);
});

test("search ceiling uses worst catalog input class and rejects unknown models", () => {
  assert.equal(catalogSearchCeiling("anthropic/claude-haiku-4-5", 1_000_000, 1_000_000), 6.25);
  assert.equal(catalogSearchCeiling("unknown/model", 1, 1), undefined);
});

test("generateCandidatePlans enforces catalog pricing and policy when a policy is supplied", () => {
  const ir = { schemaVersion: 1, segments: [] };
  const ag = agent({ id: "policy-test", instructions: "x", model: auto() });
  // A priced model gets its real public-catalog ceiling and stays runnable.
  const priced = generateCandidatePlans(
    ag, ir, plan("priced"), ["anthropic/claude-haiku-4-5"], false, undefined, undefined, undefined, {},
  );
  assert.ok(priced.length >= 1);
  assert.ok(priced.every((candidate) => candidate.static_rejection === undefined));
  assert.ok(priced[0].estimated_cost_usd_per_run > 0);
  // An unpriced model is statically rejected — the public compile() enforces
  // what the CLI did, not a placeholder cost.
  const unpriced = generateCandidatePlans(
    ag, ir, plan("unpriced", "unknown/no-price"), ["unknown/no-price"], false, undefined, undefined, undefined, {},
  );
  assert.ok(unpriced.length >= 1);
  assert.ok(unpriced.every((candidate) => candidate.static_rejection === "unpriced_model"));
  // A denied model is rejected as policy_denied.
  const denied = generateCandidatePlans(
    ag, ir, plan("denied"), ["anthropic/claude-haiku-4-5"], false, undefined, undefined, undefined,
    { deniedModels: ["anthropic/claude-haiku-4-5"] },
  );
  assert.ok(denied.every((candidate) => candidate.static_rejection === "policy_denied"));
  // Without a policy, generation is unchanged: no catalog rejection.
  const noPolicy = generateCandidatePlans(
    ag, ir, plan("nopolicy", "unknown/no-price"), ["unknown/no-price"], false,
  );
  assert.ok(noPolicy.every((candidate) => candidate.static_rejection === undefined));
});

test("knownGrader recognizes the full locked-evidence taxonomy and rejects the rest", () => {
  const supported = [
    "exact_match", "contains", "not_contains", "regex", "not_regex", "blocklist",
    "json_schema", "json_path_assertion", "tool_called", "tool_not_called", "tool_sequence",
    "tool_argument_assertion", "http_status", "latency_threshold", "cost_threshold",
    "token_threshold", "bleu_score", "rouge_score", "context_f1", "no_pii", "custom_webhook",
    "localization_f1", "llm_judge", "llm_score", "llm_category", "llm_pairwise",
    "llm_answer_match",
  ];
  assert.equal(supported.length, 27);
  for (const type of supported) {
    assert.equal(knownGrader(type), true, `knownGrader should accept ${type}`);
  }
  assert.equal(knownGrader("semantic"), false);
  assert.equal(knownGrader("custom"), false);
  assert.equal(knownGrader("totally_made_up"), false);
});
