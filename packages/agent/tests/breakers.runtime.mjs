import { test } from "node:test";
import assert from "node:assert/strict";
import {
  CavemanRunError,
  agent,
  memory,
  run,
  schema,
  tool,
} from "../dist/index.js";
import { mkdtemp, rm } from "node:fs/promises";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import Ajv2020 from "ajv/dist/2020.js";
import { BreakerState, normalizeRunBreakers } from "../dist/breakers.js";
import { fauxProvider as upstreamFauxProvider } from "@earendil-works/pi-ai/providers/faux";
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";

const PRICED_MODEL = "claude-haiku-4-5";
const validateReceipt = new Ajv2020({ strict: true, allErrors: true }).compile(JSON.parse(
  readFileSync(
    new URL("../../shared/contracts/schemas/agent-run-receipt.schema.json", import.meta.url),
    "utf8",
  ),
));

function assertSharedReceiptContract(receipt) {
  assert.equal(
    validateReceipt(JSON.parse(JSON.stringify(receipt))),
    true,
    validateReceipt.errors === null ? "invalid receipt" : JSON.stringify(validateReceipt.errors),
  );
}

function pricedFauxModel() {
  const handle = upstreamFauxProvider({ provider: "anthropic" });
  return { ...handle.getModel(), id: PRICED_MODEL };
}

function usage(input, outputTokens) {
  return {
    input,
    output: outputTokens,
    cacheRead: 0,
    cacheWrite: 0,
    reasoning: 0,
    totalTokens: input + outputTokens,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
  };
}

function pushMessage(selected, content, stopReason, used) {
  const stream = createAssistantMessageEventStream();
  const message = {
    role: "assistant",
    content,
    api: selected.api,
    provider: selected.provider,
    model: selected.id,
    usage: used,
    stopReason,
    timestamp: Date.now(),
  };
  queueMicrotask(() => {
    stream.push({ type: "start", partial: { ...message, content: [], stopReason: "pending" } });
    stream.push({ type: "done", reason: stopReason, message });
    stream.end(message);
  });
  return stream;
}

function repeaterAgent(options = {}) {
  return agent({
    id: options.id,
    instructions: "Poll.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
    tools: [tool({
      name: "poll",
      description: "Poll a queue.",
      input: schema.object({ key: schema.string() }),
      effect: "read",
      ...(options.allowRepeat === undefined ? {} : { allowRepeat: options.allowRepeat }),
      execute: () => options.result ?? "still working",
    })],
  });
}

/** Always asks for the same tool with the same arguments. */
function repeatingProvider(observed, args = { key: "job-1" }) {
  return (selected) => {
    observed.calls++;
    return pushMessage(
      selected,
      [{ type: "toolCall", id: `poll-${observed.calls}`, name: "poll", arguments: args }],
      "toolUse",
      usage(200, 20),
    );
  };
}

test("identical repeated tool calls break the run deterministically", async () => {
  const defined = repeaterAgent({ id: "breaker-loop" });
  const observed = { calls: 0 };
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    breakers: { repeatedToolCalls: 3 },
    streamFn: repeatingProvider(observed),
  });
  assert.equal(result.stopReason, "loop_detected");
  const trips = result.receipt.breakers.filter((event) => event.kind === "loop_detected");
  assert.equal(trips.length >= 1, true);
  assert.equal(trips[0].tool, "poll");
  assert.equal(trips[0].count, 3);
  // The offending window is identifiable: the exact call hash that repeated.
  assert.match(String(trips[0].signature), /^[0-9a-f]{64}$/);
});

test("a tool that opts out of repeat detection is never broken for repeating", async () => {
  const defined = repeaterAgent({ id: "breaker-allow-repeat", allowRepeat: true });
  const observed = { calls: 0 };
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    // The no-progress breaker is what stops this run; the loop breaker never
    // fires, because polling the same queue is exactly what this tool is for.
    breakers: { repeatedToolCalls: 3, noProgressTurns: 3 },
    streamFn: repeatingProvider(observed),
  });
  assert.equal(result.stopReason, "no_progress");
  assert.equal(
    result.receipt.breakers.some((event) => event.kind === "loop_detected"),
    false,
  );
});

test("a run with no breakers configured keeps its previous behaviour", async () => {
  const defined = agent({
    id: "breaker-absent",
    instructions: "Reply.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    streamFn: (selected) => pushMessage(
      selected,
      [{ type: "text", text: "done" }],
      "stop",
      usage(10, 2),
    ),
  });
  assert.equal(result.stopReason, "complete");
  assert.deepEqual(result.receipt.breakers, []);
});

test("successful writes reset no-progress even when display results repeat", async () => {
  let mutations = 0;
  const defined = agent({
    id: "breaker-write-progress",
    instructions: "Mutate twice.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "host",
    tools: [tool({
      name: "advance",
      description: "Advance host state.",
      input: schema.object({ key: schema.string() }),
      effect: "write",
      allowRepeat: true,
      execute: () => {
        mutations++;
        return "ok";
      },
    })],
  });
  let call = 0;
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    breakers: { noProgressTurns: 2 },
    streamFn: (selected) => {
      call++;
      if (call <= 2) {
        return pushMessage(
          selected,
          [{ type: "toolCall", id: `advance-${call}`, name: "advance", arguments: { key: "x" } }],
          "toolUse",
          usage(100, 10),
        );
      }
      return pushMessage(selected, [{ type: "text", text: "done" }], "stop", usage(100, 10));
    },
  });
  assert.equal(mutations, 2);
  assert.equal(result.stopReason, "complete");
  assert.equal(
    result.receipt.breakers.some((event) => event.kind === "no_progress"),
    false,
  );
});

test("framework memory mutations reset no-progress when results repeat", async () => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-breaker-memory-"));
  try {
    const defined = agent({
      id: "breaker-memory-progress",
      instructions: "Remember two facts.",
      model: "anthropic/claude-haiku-4-5",
      sandbox: "fixture",
      memory: memory({ namespace: "breaker-state", ttl: "1d", recallBudget: 20 }),
    });
    let call = 0;
    const result = await run(defined, "go", {
      ensureRuntime: false,
      model: pricedFauxModel(),
      memory: { root },
      breakers: { noProgressTurns: 2 },
      streamFn: (selected) => {
        call++;
        if (call <= 2) {
          return pushMessage(
            selected,
            [{
              type: "toolCall",
              id: `remember-${call}`,
              name: "cave_memory_remember",
              arguments: { text: `fact ${call}` },
            }],
            "toolUse",
            usage(100, 10),
          );
        }
        return pushMessage(selected, [{ type: "text", text: "done" }], "stop", usage(100, 10));
      },
    });
    assert.equal(result.stopReason, "complete");
    assert.deepEqual(result.toolCalls, ["cave_memory_remember", "cave_memory_remember"]);
    assert.equal(
      result.receipt.breakers.some((event) => event.kind === "no_progress"),
      false,
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("the fan-out cap blocks the extra calls in a turn without ending the run", async () => {
  const defined = agent({
    id: "breaker-fan-out",
    instructions: "Fan out.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
    tools: [tool({
      name: "lookup",
      description: "Look something up.",
      input: schema.object({ key: schema.string() }),
      effect: "read",
      execute: (input) => `value:${input.key}`,
    })],
  });
  let call = 0;
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    breakers: { maxToolCallsPerTurn: 2 },
    streamFn: (selected) => {
      call++;
      if (call === 1) {
        return pushMessage(
          selected,
          ["a", "b", "c", "d"].map((key, index) => ({
            type: "toolCall",
            id: `lookup-${index}`,
            name: "lookup",
            arguments: { key },
          })),
          "toolUse",
          usage(200, 20),
        );
      }
      return pushMessage(selected, [{ type: "text", text: "done" }], "stop", usage(100, 10));
    },
  });
  assert.equal(result.stopReason, "complete");
  const blocked = result.receipt.breakers.filter((event) => event.kind === "fan_out_blocked");
  assert.equal(blocked.length, 2);
  assert.deepEqual(result.receipt.tools, [{ name: "lookup", calls: 4, errors: 2 }]);
});

test("retry is budgeted in the run's denomination and needs a budget to exist", async () => {
  const defined = agent({
    id: "breaker-retry",
    instructions: "Reply.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  await assert.rejects(
    run(defined, "go", {
      ensureRuntime: false,
      model: pricedFauxModel(),
      breakers: { retry: { maxSpend: 1_000 } },
      streamFn: () => {
        throw new Error("must not reach the provider");
      },
    }),
    /cave_breaker_retry_requires_budget/,
  );
  let attempts = 0;
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 200_000 },
    breakers: { retry: { maxSpend: 200_000, backoffMs: 0 } },
    streamFn: (selected) => {
      attempts++;
      if (attempts <= 2) throw new Error("provider unavailable");
      return pushMessage(selected, [{ type: "text", text: "done" }], "stop", usage(100, 10));
    },
  });
  assert.equal(attempts, 3);
  assert.equal(result.stopReason, "complete");
  assert.equal(result.text, "done");
  const retried = result.receipt.breakers.filter((event) => event.kind === "retry_attempted");
  assert.equal(retried.length, 2);
  assert.deepEqual(retried.map((event) => event.spendBasis), [
    "pre_stream_no_usage",
    "provider_reported",
  ]);
  assert.deepEqual(retried.map((event) => event.measuredSpend), [0, 110]);
  assert.equal(retried.every((event) => event.reservedSpend > 0), true);
  assertSharedReceiptContract(result.receipt);
});

test("a retried pre-stream failure books zero spend — only the successful call is metered", async () => {
  // The retry allowance is only sound because a retried attempt fails BEFORE
  // any stream opens, so it spends nothing. Prove it: two attempts throw before
  // producing a message, the third succeeds, and the ledger (meter spend, call
  // count, tokens) reflects ONLY that one successful call — the failed attempts
  // leave no phantom spend behind, while still being audited on the receipt.
  const defined = agent({
    id: "breaker-retry-nospend",
    instructions: "Reply.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  let attempts = 0;
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 200_000 },
    breakers: { retry: { maxSpend: 200_000, backoffMs: 0 } },
    streamFn: (selected) => {
      attempts++;
      if (attempts <= 2) throw new Error("provider unavailable");
      return pushMessage(selected, [{ type: "text", text: "done" }], "stop", usage(100, 10));
    },
  });
  assert.equal(attempts, 3);
  assert.equal(result.stopReason, "complete");
  // Only the one call that actually produced a stream is on the ledger.
  assert.equal(result.receipt.calls.length, 1);
  assert.equal(result.inputTokens, 100);
  assert.equal(result.outputTokens, 10);
  // The metered spend is exactly the successful call's 110 tokens — the two
  // failed attempts booked nothing.
  assert.equal(result.receipt.spent, 110);
  // ...yet the retries are still auditable on the receipt.
  const retried = result.receipt.breakers.filter((event) => event.kind === "retry_attempted");
  assert.equal(retried.length, 2);
  assert.deepEqual(retried.map((event) => event.measuredSpend), [0, 110]);
  assert.equal(retried.every((event) => event.reservedSpend > 0), true);
});

test("retry settlement targets the current model call when attempt numbers repeat", async () => {
  const defined = repeaterAgent({ id: "breaker-retry-multiple-calls", allowRepeat: true });
  let transportAttempts = 0;
  let completedCalls = 0;
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 500_000 },
    breakers: {
      noProgressTurns: 5,
      retry: { maxSpend: 500_000, backoffMs: 0 },
    },
    streamFn: (selected) => {
      transportAttempts++;
      if (transportAttempts % 2 === 1) throw new Error("provider unavailable");
      completedCalls++;
      if (completedCalls === 1) {
        return pushMessage(
          selected,
          [{ type: "toolCall", id: "retry-poll", name: "poll", arguments: { key: "job" } }],
          "toolUse",
          usage(200, 20),
        );
      }
      return pushMessage(selected, [{ type: "text", text: "done" }], "stop", usage(100, 10));
    },
  });
  assert.equal(transportAttempts, 4);
  const retries = result.receipt.breakers.filter((event) => event.kind === "retry_attempted");
  assert.deepEqual(retries.map((event) => event.count), [1, 1]);
  assert.deepEqual(retries.map((event) => event.measuredSpend), [220, 110]);
  assert.deepEqual(retries.map((event) => event.spendBasis), [
    "provider_reported",
    "provider_reported",
  ]);
  assertSharedReceiptContract(result.receipt);
});

test("terminal abort never opens or records a retry attempt", async () => {
  const defined = agent({
    id: "breaker-retry-abort",
    instructions: "Reply.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const controller = new AbortController();
  let attempts = 0;
  let failure;
  try {
    await run(defined, "go", {
      ensureRuntime: false,
      model: pricedFauxModel(),
      signal: controller.signal,
      budget: { maxTokens: 200_000 },
      breakers: { retry: { maxSpend: 200_000, backoffMs: 0 } },
      streamFn: () => {
        attempts++;
        controller.abort(new Error("test abort"));
        throw new DOMException("aborted", "AbortError");
      },
    });
  } catch (error) {
    failure = error;
  }
  assert.ok(failure instanceof CavemanRunError);
  assert.equal(attempts, 1);
  assert.deepEqual(
    failure.receipt.breakers.filter((event) => event.kind === "retry_attempted"),
    [],
  );
  assert.equal(failure.receipt.spent, 0);
  assertSharedReceiptContract(failure.receipt);
});

test("deadline expiry during backoff cancels the retry before provider I/O", async () => {
  const defined = agent({
    id: "breaker-retry-deadline",
    instructions: "Reply.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  let attempts = 0;
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    deadlineMs: 100,
    budget: { maxTokens: 200_000 },
    breakers: { retry: { maxSpend: 200_000, backoffMs: 200 } },
    streamFn: () => {
      attempts++;
      throw new Error("provider unavailable");
    },
  });
  assert.equal(attempts, 1);
  assert.equal(result.stopReason, "deadline");
  const retries = result.receipt.breakers.filter((event) => event.kind === "retry_attempted");
  assert.equal(retries.length, 1);
  assert.equal(retries[0].measuredSpend, 0);
  assert.equal(retries[0].spendBasis, "pre_stream_no_usage");
  assert.equal(result.receipt.spent, 0);
  assertSharedReceiptContract(result.receipt);
});

test("an error storm exhausts the retry allowance instead of the wallet", async () => {
  const defined = agent({
    id: "breaker-retry-storm",
    instructions: "Reply.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  let attempts = 0;
  let failure;
  try {
    await run(defined, "go", {
      ensureRuntime: false,
      model: pricedFauxModel(),
      budget: { maxTokens: 500_000 },
      // Room for a bounded number of worst-case attempts, not an unbounded
      // number of retries.
      breakers: { retry: { maxSpend: 40_000, backoffMs: 0 } },
      streamFn: () => {
        attempts++;
        throw new Error("provider unavailable");
      },
    });
  } catch (error) {
    failure = error;
  }
  assert.ok(failure instanceof CavemanRunError);
  assert.match(failure.message, /cave_provider_terminal_error/);
  assert.equal(attempts > 1, true);
  assert.equal(attempts < 50, true);
  const retries = failure.receipt.breakers.filter((event) => event.kind === "retry_attempted");
  assert.equal(retries.length, attempts - 1);
  assert.equal(retries.every((event) =>
    event.reservedSpend > 0 && event.measuredSpend === 0 &&
    event.spendBasis === "pre_stream_no_usage"), true);
  assert.equal(
    retries.reduce((total, event) => total + event.reservedSpend, 0) <= 40_000,
    true,
  );
  assert.equal(
    failure.receipt.breakers.some((event) => event.kind === "retry_exhausted"),
    true,
  );
  assert.equal(failure.receipt.spent, 0);
  assertSharedReceiptContract(failure.receipt);
});

test("breaker thresholds fail closed on nonsense", () => {
  assert.throws(
    () => normalizeRunBreakers({ repeatedToolCalls: 0 }, false),
    /cave_breaker_threshold_invalid/,
  );
  assert.throws(
    () => normalizeRunBreakers({ noProgressTurns: 1.5 }, false),
    /cave_breaker_threshold_invalid/,
  );
  assert.throws(
    () => normalizeRunBreakers({ retry: { maxSpend: 0 } }, true),
    /cave_breaker_retry_spend_invalid/,
  );
  assert.throws(
    () => normalizeRunBreakers({ retry: { maxSpend: 1, backoffMs: -1 } }, true),
    /cave_breaker_retry_backoff_invalid/,
  );
});

test("a repeat after a failed attempt is a retry, not a loop", () => {
  // Matches the worker's loop-detector arithmetic: a repeat whose immediately
  // preceding same-hash attempt failed had to be paid, so it is excluded.
  const state = new BreakerState(normalizeRunBreakers({ repeatedToolCalls: 2 }, false));
  const call = (id) => state.observeToolCall({
    toolCallId: id,
    toolName: "poll",
    args: { key: "job-1" },
    allowRepeat: false,
    turnKey: id,
  });
  assert.equal(call("a").block, false);
  state.observeToolResult("a", true);
  state.observeTurn("failed", [{ isError: true }]);
  // Second identical call follows a failure: the window restarts.
  assert.equal(call("b").block, false);
  assert.equal(state.tripped, undefined);
  state.observeToolResult("b", false);
  state.observeTurn("succeeded", [{ isError: false }]);
  // Third one follows a success, so it is a genuine re-traversal.
  assert.equal(call("c").block, true);
  assert.equal(state.tripped, "loop_detected");
});

test("old identical calls decay out of the configured turn window", () => {
  const state = new BreakerState(normalizeRunBreakers({
    repeatedToolCalls: 3,
    repeatedToolCallWindowTurns: 3,
  }, false));
  const call = (id, toolName = "read", args = { path: "same.txt" }) =>
    state.observeToolCall({
      toolCallId: id,
      toolName,
      args,
      allowRepeat: false,
      turnKey: id,
    });

  assert.equal(call("read-1").block, false);
  state.observeTurn("read-1", []);
  for (let turn = 1; turn <= 40; turn++) {
    state.observeTurn(`tool-free-${turn}`, []);
  }
  assert.equal(call("read-2").block, false);
  state.observeTurn("read-2", []);
  for (let turn = 41; turn <= 80; turn++) {
    state.observeTurn(`tool-free-${turn}`, []);
  }
  assert.equal(call("read-3").block, false);
  assert.equal(state.tripped, undefined);
  assert.equal(state.recorded.some((event) => event.kind === "loop_detected"), false);
});

test("identical calls inside the configured turn window still trip", () => {
  const state = new BreakerState(normalizeRunBreakers({
    repeatedToolCalls: 3,
    repeatedToolCallWindowTurns: 5,
  }, false));
  const call = (id) => state.observeToolCall({
    toolCallId: id,
    toolName: "read",
    args: { path: "same.txt" },
    allowRepeat: false,
    turnKey: id,
  });

  assert.equal(call("a").block, false);
  state.observeTurn("a", []);
  assert.equal(call("b").block, false);
  state.observeTurn("b", []);
  assert.equal(call("c").block, true);
  assert.equal(state.tripped, "loop_detected");
});
