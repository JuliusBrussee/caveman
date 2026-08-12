import { test } from "node:test";
import assert from "node:assert/strict";
import {
  CavemanRunError,
  agent,
  eval as defineEval,
  memory,
  output,
  schema,
  tool,
} from "../dist/index.js";
import { compile, defineBuild } from "../dist/build.js";
import { lowerAgentContext } from "../dist/execution-kernel.js";
import { runClaudeAgent, runClaudeAgentInternal } from "../dist/claude-runtime.js";
import {
  CLAUDE_ADAPTER_VERSION,
  CLAUDE_CODE_VERSION,
  CLAUDE_UPSTREAM_VERSION,
  FRAMEWORK_VERSION,
} from "../dist/runtime-identity.js";

const hex = (value) => value.repeat(64);

function definition() {
  return agent({
    id: "claude-support",
    instructions: "Answer from policy evidence only.",
    model: "anthropic/claude-haiku-4-5",
    reasoning: "low",
    tools: [tool({
      name: "lookup_policy",
      description: "Read policy.",
      input: schema.object({ region: schema.string() }),
      effect: "read",
      result: "inline",
      async execute({ region }) {
        return { region, days: 14 };
      },
    })],
    output: output({
      maxTokens: 2_048,
      schema: schema.object({ answer: schema.string() }),
    }),
    sandbox: "fixture",
  });
}

function plan() {
  return {
    schema_version: 1,
    plan_id: "claude-history",
    model: "anthropic/claude-haiku-4-5",
    reasoning: "low",
    segment_routes: [{
      segment_kind: "history",
      transform_id: "caveman.engine.text.v1",
      fallback: "original",
    }],
    budgets: {
      instructions: 2_000,
      tools: 2_000,
      memory: 0,
      history: 4_000,
      results_artifacts: 2_000,
      reasoning: 100,
      output: 100,
      retry_cascade_reserve: 256,
    },
    recovery: { namespace: "claude-support", tools: ["cave_retrieve"] },
    fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
  };
}

async function lockedBuild() {
  const defined = definition();
  const selected = plan();
  const staticContext = await lowerAgentContext(defined);
  const result = await compile({
    agent: defined,
    contextIR: staticContext.ir,
    evals: [defineEval({
      id: "claude-support",
      approved: true,
      input: "fixture",
      quality: [{ type: "exact_match", expected: "ok" }],
    })],
    candidates: [{ plan: selected, estimated_cost_usd_per_run: 0.001 }],
    baselinePlan: selected,
    seeds: [1, 2, 3, 4, 5],
    config: defineBuild({ entry: "src/agent.ts", evals: "evals/*.ts" }),
    entitled: true,
    sourceSha256: hex("1"),
    catalogSha256: hex("2"),
    transformRegistrySha256: hex("3"),
    runtimeVersion: FRAMEWORK_VERSION,
    adapterVersion: CLAUDE_ADAPTER_VERSION,
    upstreamVersion: CLAUDE_UPSTREAM_VERSION,
    harnessId: "claude",
    runner: async ({ seed }) => ({
      terminal: true,
      usage_basis: "provider_reported",
      price_basis: "public_catalog",
      catalog_cost_usd: 0.001,
      quality_score: 1,
      graders: [{ type: "exact_match", passed: true }],
      latency_ms: 1,
      provider_visible_tokens: 100,
      cache_prefix_sha256: hex("4"),
      cache_boundary_known: true,
      cache_read_tokens: seed === 1 ? 0 : 20,
      cache_write_tokens: 10,
      cache_bust: false,
      error: false,
      recovery_resolved: true,
      privacy_passed: true,
      sandbox_passed: true,
      output_digest: hex("5"),
    }),
  });
  assert.equal(result.status, "locked");
  return result.lock;
}

function queryFn(capture, outputTokens = 5, model = "claude-haiku-4-5", apiKeySource = "user") {
  return ({ prompt, options }) => {
    capture.calls++;
    capture.prompt = prompt;
    capture.options = options;
    return {
      async *[Symbol.asyncIterator]() {
        yield {
          type: "system",
          subtype: "init",
          claude_code_version: CLAUDE_CODE_VERSION,
          model,
          apiKeySource,
        };
        yield {
          type: "assistant",
          message: { model, content: [] },
        };
        yield {
          type: "result",
          subtype: "success",
          is_error: false,
          result: '{"answer":"ok"}',
          structured_output: { answer: "ok" },
          usage: {
            input_tokens: 70,
            output_tokens: outputTokens,
            cache_read_input_tokens: 20,
            cache_creation_input_tokens: 10,
          },
        };
      },
    };
  };
}

test("Claude public execution path owns SDK policy, tool boundary, output, and disjoint usage", async () => {
  const capture = { calls: 0 };
  const priorHeaders = process.env.ANTHROPIC_CUSTOM_HEADERS;
  process.env.ANTHROPIC_CUSTOM_HEADERS = [
    `X-Cave-Agent-Build: ${hex("a")}`,
    `X-Cave-Efficiency-Plan: ${hex("b")}`,
    "X-Cave-Transform-Routes: forged",
    "X-Third-Party: preserved",
  ].join("\n");
  // A dollar cap only means something where the run is billed in dollars, so
  // this case declares the API-key regime it is asserting.
  const priorKey = process.env.ANTHROPIC_API_KEY;
  process.env.ANTHROPIC_API_KEY = "test-anthropic-key";
  let result;
  try {
    result = await runClaudeAgentInternal(definition(), "refund in France", {
      ensureRuntime: false,
      maxBudgetUsd: 0.5,
      queryFn: queryFn(capture),
    });
  } finally {
    if (priorHeaders === undefined) delete process.env.ANTHROPIC_CUSTOM_HEADERS;
    else process.env.ANTHROPIC_CUSTOM_HEADERS = priorHeaders;
    if (priorKey === undefined) delete process.env.ANTHROPIC_API_KEY;
    else process.env.ANTHROPIC_API_KEY = priorKey;
  }

  assert.equal(capture.calls, 1);
  assert.equal(capture.prompt, "refund in France");
  assert.equal(capture.options.model, "claude-haiku-4-5");
  assert.deepEqual(capture.options.tools, []);
  assert.deepEqual(capture.options.settingSources, []);
  assert.equal(capture.options.permissionMode, "dontAsk");
  assert.equal(capture.options.persistSession, false);
  assert.equal(capture.options.maxTurns, 16);
  assert.equal(capture.options.maxBudgetUsd, 0.5);
  assert.deepEqual(capture.options.taskBudget, { total: 2_048 });
  assert.deepEqual(capture.options.thinking, { type: "enabled", budgetTokens: 1_024 });
  assert.equal("effort" in capture.options, false);
  assert.match(capture.options.systemPrompt, /Answer from policy evidence only/);
  assert.equal(capture.options.env.ANTHROPIC_BASE_URL, "http://127.0.0.1:8787/anthropic");
  assert.match(
    capture.options.env.ANTHROPIC_CUSTOM_HEADERS,
    /x-cave-transforms: caveman\.pass-through\.v1/,
  );
  assert.doesNotMatch(capture.options.env.ANTHROPIC_CUSTOM_HEADERS, /agent-build/i);
  assert.doesNotMatch(capture.options.env.ANTHROPIC_CUSTOM_HEADERS, /transform-routes/i);
  assert.match(capture.options.env.ANTHROPIC_CUSTOM_HEADERS, /X-Third-Party: preserved/);
  assert.ok(capture.options.mcpServers.caveman_agent);
  assert.equal("caveman_recovery" in capture.options.mcpServers, false);
  assert.deepEqual(
    await capture.options.canUseTool("mcp__caveman_recovery__caveman_retrieve", {}),
    { behavior: "deny", message: "cave_unknown_tool", interrupt: true },
  );
  assert.equal(result.unlocked, true);
  assert.equal(result.inputTokens, 70);
  assert.equal(result.outputTokens, 5);
  assert.equal(result.cacheReadTokens, 20);
  assert.equal(result.cacheWriteTokens, 10);
  assert.equal(result.reasoningUsageBasis, "unavailable");
  assert.equal(result.reasoningTokens, 0);
  assert.equal(result.cacheBoundaryKnown, false);
  assert.deepEqual(result.transformIDs, []);
  assert.equal(result.claimBasis, "inferred");
  assert.equal(result.mode, "optimized");
  assert.equal(result.priceBasis, "public_catalog");
  assert.equal(result.costUsd > 0, true);
  assert.equal(result.receipt.calls[0].unpriced, false);
  assert.equal(result.receipt.calls[0].estimatedUsd, result.costUsd);
});

test("Claude subscription usage keeps tokens but reports an honest zero everywhere", async () => {
  const capture = { calls: 0 };
  const result = await runClaudeAgentInternal(definition(), "subscription", {
    cave: "off",
    queryFn: queryFn(capture, 5, "claude-haiku-4-5", "oauth"),
  });
  assert.equal(result.inputTokens, 70);
  assert.equal(result.outputTokens, 5);
  assert.equal(result.cacheReadTokens, 20);
  assert.equal(result.cacheWriteTokens, 10);
  assert.equal(result.costUsd, 0);
  assert.equal(result.priceBasis, "unpriced");
  assert.equal(result.receipt.totalEstimatedUsd, 0);
  assert.equal(result.receipt.calls.length, 1);
  assert.equal(result.receipt.calls[0].estimatedUsd, 0);
  assert.equal(result.receipt.calls[0].unpriced, true);
});

test("Claude dollar caps trust selected SDK auth, not ambient API-key env", async () => {
  const priorKey = process.env.ANTHROPIC_API_KEY;
  try {
    delete process.env.ANTHROPIC_API_KEY;
    const storedKey = { calls: 0 };
    const metered = await runClaudeAgentInternal(definition(), "stored key", {
      cave: "off",
      maxBudgetUsd: 0.5,
      queryFn: queryFn(storedKey, 5, "claude-haiku-4-5", "project"),
    });
    assert.equal(metered.priceBasis, "public_catalog");

    process.env.ANTHROPIC_API_KEY = "ambient-but-not-selected";
    let yielded = 0;
    let closed = false;
    const subscriptionQuery = () => ({
      close() { closed = true; },
      async *[Symbol.asyncIterator]() {
        yielded++;
        yield {
          type: "system",
          subtype: "init",
          claude_code_version: CLAUDE_CODE_VERSION,
          model: "claude-haiku-4-5",
          apiKeySource: "oauth",
        };
        yielded++;
        yield { type: "assistant", message: { model: "claude-haiku-4-5", content: [] } };
      },
    });
    await assert.rejects(
      runClaudeAgentInternal(definition(), "subscription", {
        cave: "off",
        maxBudgetUsd: 0.5,
        queryFn: subscriptionQuery,
      }),
      /cave_budget_denomination_unavailable/,
    );
    assert.equal(yielded, 1);
    assert.equal(closed, true);
  } finally {
    if (priorKey === undefined) delete process.env.ANTHROPIC_API_KEY;
    else process.env.ANTHROPIC_API_KEY = priorKey;
  }
});

test("Claude observe-only keeps Anthropic's own base URL", async () => {
  const capture = { calls: 0 };
  const result = await runClaudeAgentInternal(definition(), "refund in France", {
    cave: "off",
    gatewayURL: "http://127.0.0.1:1",
    queryFn: queryFn(capture),
  });
  assert.equal(capture.calls, 1);
  assert.equal("ANTHROPIC_BASE_URL" in capture.options.env, false);
  assert.equal(result.mode, "observe-only");
  assert.deepEqual(result.transformIDs, []);
});

test("all Claude Cave Builds fail before SDK or MCP launch", async () => {
  const capture = { calls: 0 };
  await assert.rejects(
    runClaudeAgentInternal(definition(), "must not spend", {
      ensureRuntime: false,
      lockedBuild: await lockedBuild(),
      queryFn: queryFn(capture),
    }),
    /cave_claude_locked_execution_unavailable/,
  );
  assert.equal(capture.calls, 0);
  const budgetCapture = { calls: 0 };
  await assert.rejects(
    runClaudeAgentInternal(definition(), "must enforce output", {
      ensureRuntime: false,
      queryFn: queryFn(budgetCapture, 2_049),
    }),
    /cave_output_budget_exceeded/,
  );
  assert.equal(budgetCapture.calls, 1);
});

test("Claude reasoning policy matches pinned model capabilities before spend", async () => {
  const adaptiveCapture = { calls: 0 };
  const adaptive = agent({
    ...definition(),
    id: "claude-adaptive",
    model: "anthropic/claude-sonnet-4-6",
    output: output({ maxTokens: 100, schema: schema.object({ answer: schema.string() }) }),
  });
  await runClaudeAgentInternal(adaptive, "adaptive", {
    ensureRuntime: false,
    queryFn: queryFn(adaptiveCapture, 5, "claude-sonnet-4-6"),
  });
  assert.deepEqual(adaptiveCapture.options.thinking, { type: "adaptive" });
  assert.equal(adaptiveCapture.options.effort, "low");

  const offCapture = { calls: 0 };
  const disabled = agent({ ...definition(), id: "claude-off", reasoning: "off" });
  await runClaudeAgentInternal(disabled, "disabled", {
    ensureRuntime: false,
    queryFn: queryFn(offCapture),
  });
  assert.deepEqual(offCapture.options.thinking, { type: "disabled" });
  assert.equal("effort" in offCapture.options, false);

  const tooSmallCapture = { calls: 0 };
  const tooSmall = agent({
    ...definition(),
    id: "claude-manual-too-small",
    output: output({ maxTokens: 1_024 }),
  });
  await assert.rejects(
    runClaudeAgentInternal(tooSmall, "must not spend", {
      ensureRuntime: false,
      queryFn: queryFn(tooSmallCapture),
    }),
    /cave_claude_output_budget_too_small_for_reasoning/,
  );
  assert.equal(tooSmallCapture.calls, 0);

  const unknownCapture = { calls: 0 };
  const unknown = agent({
    ...definition(),
    id: "claude-unknown-capability",
    model: "anthropic/claude-future-unknown",
  });
  await assert.rejects(
    runClaudeAgentInternal(unknown, "must not spend", {
      ensureRuntime: false,
      queryFn: queryFn(unknownCapture),
    }),
    /cave_claude_reasoning_capability_unknown/,
  );
  assert.equal(unknownCapture.calls, 0);
});

test("public Claude lane rejects private build injection, memory, and unsafe tool contracts", async () => {
  const build = await lockedBuild();
  await assert.rejects(
    runClaudeAgent(definition(), "must not spend", { lockedBuild: build }),
    /cave_execution_authorization_private/,
  );
  const capture = { calls: 0 };
  const withMemory = agent({
    ...definition(),
    id: "claude-memory",
    memory: memory({ namespace: "claude-memory", ttl: "1d", recallBudget: 100 }),
  });
  await assert.rejects(
    runClaudeAgentInternal(withMemory, "must not spend", {
      ensureRuntime: false,
      queryFn: queryFn(capture),
    }),
    /cave_claude_memory_bridge_unavailable/,
  );
  const unsafeTool = agent({
    ...definition(),
    id: "claude-write-tool",
    tools: [tool({
      name: "send_message",
      description: "Send external message.",
      input: schema.object({ text: schema.string() }),
      effect: "external",
      result: "inline",
      async execute() {
        throw new Error("must not execute");
      },
    })],
  });
  await assert.rejects(
    runClaudeAgentInternal(unsafeTool, "must not spend", {
      ensureRuntime: false,
      queryFn: queryFn(capture),
    }),
    /cave_claude_tool_contract_unsupported:send_message/,
  );
  assert.equal(capture.calls, 0);
});

test("Claude candidate execution stays fail-closed until compiler runner exists", async () => {
  const capture = { calls: 0 };
  await assert.rejects(
    runClaudeAgentInternal(definition(), "must not spend", {
      ensureRuntime: false,
      candidatePlan: plan(),
      queryFn: queryFn(capture),
    }),
    /cave_claude_candidate_execution_unavailable/,
  );
  assert.equal(capture.calls, 0);
});

test("Claude version pin aborts at the init message, before draining or spending", async () => {
  // A mismatched harness version must be caught at the first message the SDK
  // emits — before it drives any model call — not after the whole run drains.
  let drained = 0;
  let closed = false;
  const queryWrongVersion = () => ({
    close() { closed = true; },
    async *[Symbol.asyncIterator]() {
      drained++;
      yield {
        type: "system",
        subtype: "init",
        claude_code_version: "0.0.0-not-the-pin",
        model: "claude-haiku-4-5",
        apiKeySource: "user",
      };
      // Only reached if the loop kept draining past the version check.
      drained++;
      yield {
        type: "result",
        subtype: "success",
        is_error: false,
        result: '{"answer":"ok"}',
        structured_output: { answer: "ok" },
        usage: {
          input_tokens: 70,
          output_tokens: 5,
          cache_read_input_tokens: 0,
          cache_creation_input_tokens: 0,
        },
      };
    },
  });
  await assert.rejects(
    runClaudeAgentInternal(definition(), "abort early", {
      ensureRuntime: false,
      queryFn: queryWrongVersion,
    }),
    /cave_harness_upstream_version_mismatch/,
  );
  // Only the init message was pulled; the run never reached the result/usage.
  assert.equal(drained, 1);
  assert.equal(closed, true);
});

test("a Claude subscription run that errors after spending surfaces its receipt", async () => {
  // error_max_turns after burning turns must not throw the ledger away:
  // the error result carries provider usage just as a success does.
  const queryMaxTurns = () => ({
    async *[Symbol.asyncIterator]() {
      yield {
        type: "system",
        subtype: "init",
        claude_code_version: CLAUDE_CODE_VERSION,
        model: "claude-haiku-4-5",
        apiKeySource: "oauth",
      };
      yield {
        type: "assistant",
        message: { model: "claude-haiku-4-5", content: [] },
      };
      yield {
        type: "result",
        subtype: "error_max_turns",
        is_error: true,
        num_turns: 30,
        total_cost_usd: 2,
        usage: {
          input_tokens: 900,
          output_tokens: 120,
          cache_read_input_tokens: 40,
          cache_creation_input_tokens: 10,
        },
      };
    },
  });
  const error = await runClaudeAgentInternal(definition(), "spend then fail", {
    ensureRuntime: false,
    queryFn: queryMaxTurns,
  }).then(() => undefined, (e) => e);
  assert.ok(error instanceof CavemanRunError, "expected CavemanRunError");
  assert.match(error.message, /cave_claude_terminal_error_max_turns/);
  // The ledger of what the failed run spent survives on the receipt.
  assert.ok(error.receipt, "expected a receipt on the failure");
  assert.equal(error.receipt.calls.length, 1);
  assert.equal(error.receipt.totalTokens, 900 + 120 + 40 + 10);
  assert.equal(error.receipt.totalEstimatedUsd, 0);
  assert.equal(error.receipt.calls[0].estimatedUsd, 0);
  assert.equal(error.receipt.calls[0].unpriced, true);
  assert.equal(error.cause, error.receipt);
});

test("a Claude error result with usage absent still yields a receipt, not a TypeError", async () => {
  // Defensive against a runtime-absent usage on an error subtype: the receipt is
  // still built (zero-usage) via CavemanRunError rather than throwing a raw
  // TypeError before the ledger exists.
  const queryNoUsage = () => ({
    async *[Symbol.asyncIterator]() {
      yield {
        type: "system",
        subtype: "init",
        claude_code_version: CLAUDE_CODE_VERSION,
        model: "claude-haiku-4-5",
        apiKeySource: "user",
      };
      yield { type: "result", subtype: "error_during_execution", is_error: true };
    },
  });
  const error = await runClaudeAgentInternal(definition(), "no usage", {
    ensureRuntime: false,
    queryFn: queryNoUsage,
  }).then(() => undefined, (e) => e);
  assert.ok(error instanceof CavemanRunError, "expected CavemanRunError, not a TypeError");
  assert.match(error.message, /cave_claude_terminal_error_during_execution/);
  // A failed run with no accountable usage carries an honest EMPTY ledger — the
  // receipt exists (no TypeError), it just records no call.
  assert.ok(error.receipt, "expected a receipt");
  assert.equal(error.receipt.calls.length, 0);
  assert.equal(error.receipt.totalTokens, 0);
});
