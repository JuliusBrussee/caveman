import { test } from "node:test";
import assert from "node:assert/strict";
import {
  agent,
  auto,
  run,
  schema,
  subagent,
  tool,
} from "../dist/index.js";
import {
  BudgetMeter,
  ReceiptRecorder,
  inputTokenCeiling,
  normalizeRunBudget,
} from "../dist/budget.js";
import { callCeilingFor, restorableRequestBytes } from "../dist/runtime.js";
import { fauxProvider as upstreamFauxProvider } from "@earendil-works/pi-ai/providers/faux";
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";

const PRICED_MODEL = "claude-haiku-4-5";

function pricedFauxModel(overrides = {}) {
  const handle = upstreamFauxProvider({ provider: "anthropic" });
  return {
    ...handle.getModel(),
    id: PRICED_MODEL,
    contextWindow: 200_000,
    maxTokens: 4_000,
    ...overrides,
  };
}

function usage(fields) {
  const input = fields.input ?? 0;
  const output = fields.output ?? 0;
  const cacheRead = fields.cacheRead ?? 0;
  const cacheWrite = fields.cacheWrite ?? 0;
  return {
    input,
    output,
    cacheRead,
    cacheWrite,
    reasoning: fields.reasoning ?? 0,
    totalTokens: input + output + cacheRead + cacheWrite,
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

const BULK = "x".repeat(8_000);

function pollingAgent(id) {
  return agent({
    id,
    instructions: "Poll until told to stop.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
    tools: [tool({
      name: "poll",
      description: "Poll a queue.",
      input: schema.object({ key: schema.string() }),
      effect: "read",
      allowRepeat: true,
      execute: (input) => `${input.key}:ok`,
    })],
  });
}

// ---------------------------------------------------------------------------
// B1 — the ledger recorded settled spend with no ceiling, and the compaction
// reserve was priced warm from the PREVIOUS working call's evidence while the
// summarizer is a different request whose prefix is cold. Reproduced by the
// an earlier implementation could silently overshoot a dollar cap.
// ---------------------------------------------------------------------------

test("B1: the reviewer's overshoot scenario never books spend past max unflagged", async () => {
  // Working calls read a warm prefix; the summarizer, when it ran, did not.
  for (const max of [0.09, 0.10, 0.12, 0.15]) {
    let working = 0;
    const result = await run(pollingAgent(`b1-${String(max).replace(".", "-")}`), "go", {
      ensureRuntime: false,
      model: pricedFauxModel(),
      budget: {
        maxUsd: max,
        onExhausted: "compact",
        compaction: { minYieldTokens: 1_000, headroomCalls: 1, keepRecentTokens: 2_000 },
      },
      streamFn: (selected, context) => {
        const last = context.messages.at(-1);
        if (typeof last?.content === "string" &&
            last.content.includes("Reply with a single JSON object")) {
          // A cold summarizer read: exactly the case the warm reserve mispriced.
          return pushMessage(
            selected,
            [{ type: "text", text: validSummaryJSON() }],
            "stop",
            usage({ input: 100_000, output: 2_000 }),
          );
        }
        working++;
        if (working > 60) {
          return pushMessage(selected, [{ type: "text", text: "done" }], "stop", usage({ input: 10, output: 2 }));
        }
        return pushMessage(
          selected,
          [
            { type: "text", text: `${BULK}-${working}` },
            {
              type: "toolCall",
              id: `poll-${working}`,
              name: "poll",
              arguments: { key: `job-${working}` },
            },
          ],
          "toolUse",
          usage({ input: 500, output: 800, cacheRead: 20_000 }),
        );
      },
    });
    const { receipt } = result;
    // The invariant, stated as the receipt must state it: either spend stayed
    // inside the cap, or the breach is loud, signed, and terminal.
    if (receipt.capBreached) {
      assert.equal(receipt.overspent > 0, true);
      assert.equal(receipt.spent > receipt.max, true);
    } else {
      assert.equal(receipt.overspent, 0);
      assert.equal(
        receipt.spent <= receipt.max,
        true,
        `spent ${receipt.spent} exceeded max ${receipt.max} without a flag`,
      );
    }
  }
});

test("B1: a provider surprise is settled at its real cost, flagged, and terminal", async () => {
  let calls = 0;
  const result = await run(pollingAgent("b1-surprise"), "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxUsd: 0.05, onExhausted: "stop" },
    streamFn: (selected) => {
      calls++;
      // Far beyond anything the byte-derived ceiling could have reserved: the
      // provider reports a request an order of magnitude larger than the one
      // this process serialized.
      return pushMessage(
        selected,
        [{
          type: "toolCall",
          id: `poll-${calls}`,
          name: "poll",
          arguments: { key: `job-${calls}` },
        }],
        "toolUse",
        usage({ input: 40_000_000, output: 100 }),
      );
    },
  });
  const { receipt } = result;
  // Real spend, recorded as real — never clamped down to the reservation.
  assert.equal(receipt.capBreached, true);
  assert.equal(receipt.spent > receipt.max, true);
  assert.equal(
    Math.abs(receipt.overspent - (receipt.spent - receipt.max)) < 1e-9,
    true,
  );
  // Terminal: the breach stops the run rather than letting it keep spending.
  assert.equal(calls, 1);
  assert.equal(result.stopReason, "budget_exhausted");
  // N4: a caller switching on stopReason alone cannot tell a clean stop from a
  // breached one, so the flags sit at the top level too.
  assert.equal(result.capBreached, true);
  assert.equal(result.overspent, receipt.overspent);
});

test("N4: a clean stop at the cap is distinguishable from a breached one", async () => {
  const observed = { calls: [], inputTokens: 400, outputTokens: 100 };
  const result = await run(pollingAgent("n4-clean"), "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 12_000, onExhausted: "stop" },
    streamFn: (selected) => {
      observed.calls.push(1);
      return pushMessage(
        selected,
        [{
          type: "toolCall",
          id: `poll-${observed.calls.length}`,
          name: "poll",
          arguments: { key: "same" },
        }],
        "toolUse",
        usage({ input: observed.inputTokens, output: observed.outputTokens }),
      );
    },
  });
  // Same stopReason as the breached run above, opposite meaning.
  assert.equal(result.stopReason, "budget_exhausted");
  assert.equal(result.capBreached, false);
  assert.equal(result.overspent, 0);
  assert.equal(result.capBreached, result.receipt.capBreached);
  assert.equal(result.overspent, result.receipt.overspent);
});

test("B1: a breached meter funds nothing further, not even a clamped call", () => {
  const meter = new BudgetMeter(normalizeRunBudget({ maxUsd: 1 }));
  const held = meter.reserve(0.5, 1_000);
  meter.settle(held, 1.4);
  assert.equal(meter.capBreached, true);
  assert.equal(Math.abs(meter.overspent - 0.4) < 1e-9, true);
  assert.equal(meter.settled, 1.4);
  assert.equal(meter.reserve(0.0001, 256), undefined);
  assert.equal(meter.carve(0.0001), undefined);
});

test("N5: a breached ledger refuses a tranche release", () => {
  // A tranche released into a dead ledger would be money that can never be
  // spent, recorded on the receipt as if the run were still being funded.
  const meter = new BudgetMeter(normalizeRunBudget({ maxUsd: 10, initialUsd: 1 }));
  meter.release(1, "checkpoint one");
  assert.equal(meter.tranches.length, 1);
  const held = meter.reserve(1, 1_000);
  meter.settle(held, 12);
  assert.equal(meter.capBreached, true);
  assert.throws(() => meter.release(1, "after the breach"), /cave_budget_cap_breached/);
  // Nothing was recorded and nothing was released.
  assert.equal(meter.tranches.length, 1);
  assert.equal(meter.released, 2);
});

// ---------------------------------------------------------------------------
// B2 — the summarizer's usage reached the receipt but not RunResult's own
// totals, so costUsd understated the run by the whole compaction and parents
// aggregating child.costUsd inherited the gap.
// ---------------------------------------------------------------------------

test("B2: a compaction's usage lands in the run totals, not only the receipt", async () => {
  const observed = await runWithOneCompaction("b2-parent");
  const { result } = observed;
  assert.equal(observed.summarizerCalls, 1);
  const { receipt } = result;
  assert.equal(receipt.compactions.some((entry) => entry.tier === "summarized"), true);
  // Every provider call the receipt knows about is in the result's own totals.
  const receiptUsd = receipt.calls.reduce((total, entry) => total + entry.estimatedUsd, 0);
  const receiptInput = receipt.calls.reduce((total, entry) => total + entry.inputTokens, 0);
  const receiptOutput = receipt.calls.reduce((total, entry) => total + entry.outputTokens, 0);
  assert.equal(Math.abs(result.costUsd - receiptUsd) < 1e-9, true);
  assert.equal(result.inputTokens, receiptInput);
  assert.equal(result.outputTokens, receiptOutput);
  // The summarizer's own tokens are in there, not silently dropped.
  assert.equal(result.outputTokens >= 2_000, true);
});

test("B2: a subagent's provider usage propagates into the parent's totals", async () => {
  const child = agent({
    id: "b2-child",
    instructions: "Answer.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "b2-aggregate-parent",
    instructions: "Delegate.",
    model: auto(),
    sandbox: "fixture",
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxCostUsd: 10,
    })],
  });
  let call = 0;
  const result = await run(parent, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxUsd: 20 },
    streamFn: (selected) => {
      call++;
      if (call === 1) {
        return pushMessage(
          selected,
          [{ type: "toolCall", id: "d1", name: "delegate", arguments: { task: "sub" } }],
          "toolUse",
          usage({ input: 500, output: 50 }),
        );
      }
      return pushMessage(
        selected,
        [{ type: "text", text: "answer" }],
        "stop",
        usage({ input: 200, output: 20 }),
      );
    },
  });
  const nested = result.receipt.subagents[0];
  const nestedUsd = nested.calls.reduce((total, entry) => total + entry.estimatedUsd, 0);
  // The parent's own total covers the child's calls exactly once, and matches
  // what the child's receipt says the child spent.
  assert.equal(Math.abs(nested.totalEstimatedUsd - nestedUsd) < 1e-9, true);
  assert.equal(
    Math.abs(result.costUsd - result.receipt.totalEstimatedUsd) < 1e-9,
    true,
  );
});

// ---------------------------------------------------------------------------
// B3 — workingCallsAfter counted the summarizer's own receipt entry, so every
// compaction claimed one working call it never bought.
// ---------------------------------------------------------------------------

test("B3: the summarizer's own call is not a working call that followed it", async () => {
  const observed = await runWithOneCompaction("b3-terminal", { stopAfterCompaction: true });
  const summarized = observed.result.receipt.compactions
    .find((entry) => entry.tier === "summarized");
  assert.notEqual(summarized, undefined);
  // Exactly one working call carried the rewritten context before the run
  // ended. The old watermark counted the summarizer's own receipt entry too
  // and reported two — the reviewer's "always exactly one high".
  assert.equal(observed.workingAfterCompaction, 1);
  assert.equal(summarized.workingCallsAfter, 1);
});

test("B3: a compaction nothing used bought nothing", () => {
  // Recorded directly, because the honest zero is the case where the run stops
  // before any call carries the smaller context.
  const recorder = new ReceiptRecorder();
  recorder.recordCall(fauxReceiptCall());
  recorder.recordCompaction({
    tier: "summarized",
    preTokens: 40_000,
    postTokens: 4_000,
    pinnedSegmentIds: [],
    elidedSegmentDigests: [],
    summarySchemaVersion: 1,
    cacheState: "cold",
    meteredCost: 0.02,
  });
  const receipt = recorder.build({
    runId: "r",
    agentId: "a",
    stopReason: "budget_exhausted",
    meter: undefined,
  });
  assert.equal(receipt.compactions[0].workingCallsAfter, 0);
  assert.equal(receipt.compactions[0].modeledNetTokens, 0);
  // And one call after it counts exactly one, net of the cache-rewrite penalty.
  recorder.recordCall(fauxReceiptCall());
  const after = recorder.build({
    runId: "r",
    agentId: "a",
    stopReason: "complete",
    meter: undefined,
  });
  assert.equal(after.compactions[0].workingCallsAfter, 1);
  assert.equal(after.compactions[0].modeledNetTokens, 40_000 - 4_000 - 4_000);
});

// ---------------------------------------------------------------------------
// M5 — a paid child compaction must settle the carved BudgetMeter and roll up
// through receipts exactly once. Legacy SpendLedgers remain only for runs with
// no carved meter.
// ---------------------------------------------------------------------------

test("M5: a subagent's compaction is accounted against its wallet", async () => {
  const child = agent({
    id: "m5-child",
    instructions: "Poll until told to stop.",
    model: "anthropic/claude-opus-4-1",
    sandbox: "fixture",
    tools: [tool({
      name: "poll",
      description: "Poll a queue.",
      input: schema.object({ key: schema.string() }),
      effect: "read",
      allowRepeat: true,
      execute: (input) => `${input.key}:ok`,
    })],
  });
  const parent = agent({
    id: "m5-parent",
    instructions: "Delegate.",
    model: auto(),
    sandbox: "fixture",
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxCostUsd: 4,
    })],
  });
  let parentCalls = 0;
  let childCalls = 0;
  let summarizerCalls = 0;
  let childActive = false;
  const workingModel = pricedFauxModel({ id: "claude-opus-4-1" });
  const result = await run(parent, "go", {
    ensureRuntime: false,
    model: workingModel,
    models: { getModel: () => workingModel, streamSimple: () => undefined },
    budget: {
      maxUsd: 60,
      onExhausted: "compact",
      compaction: {
        minYieldTokens: 1_000,
        headroomCalls: 1,
        keepRecentTokens: 2_000,
        summarizerModel: pricedFauxModel(),
      },
    },
    streamFn: (selected, context) => {
      const last = context.messages.at(-1);
      if (typeof last?.content === "string" &&
          last.content.includes("Reply with a single JSON object")) {
        summarizerCalls++;
        return pushMessage(
          selected,
          [{ type: "text", text: validSummaryJSON() }],
          "stop",
          usage({ input: 5_000, output: 500 }),
        );
      }
      if (childActive) {
        childCalls++;
        if (childCalls > 55) {
          childActive = false;
          return pushMessage(selected, [{ type: "text", text: "child done" }], "stop", usage({ input: 10, output: 2 }));
        }
        return pushMessage(
          selected,
          [
            { type: "text", text: `${BULK}-${childCalls}` },
            { type: "toolCall", id: `poll-${childCalls}`, name: "poll", arguments: { key: `job-${childCalls}` } },
          ],
          "toolUse",
          usage({ input: 500, output: 800, cacheRead: 8_000 }),
        );
      }
      parentCalls++;
      if (parentCalls === 1) {
        childActive = true;
        return pushMessage(
          selected,
          [{ type: "toolCall", id: "d1", name: "delegate", arguments: { task: "sub" } }],
          "toolUse",
          usage({ input: 500, output: 50 }),
        );
      }
      return pushMessage(selected, [{ type: "text", text: "answer" }], "stop", usage({ input: 200, output: 20 }));
    },
  });
  const nested = result.receipt.subagents[0];
  assert.notEqual(nested, undefined);
  assert.equal(summarizerCalls, 1, JSON.stringify({
    message: "the carved child never reached compaction",
    childCalls,
    stopReason: nested.stopReason,
    spent: nested.spent,
    max: nested.max,
    calls: nested.calls.length,
    compactions: nested.compactions,
  }));
  const compacted = nested.compactions.find((entry) => entry.tier === "summarized");
  assert.notEqual(compacted, undefined);
  const summaryCalls = nested.calls.filter((entry) =>
    entry.inputTokens === 5_000 && entry.outputTokens === 500);
  assert.equal(summaryCalls.length, 1);
  assert.equal(Math.abs(compacted.meteredCost - summaryCalls[0].estimatedUsd) < 1e-9, true);
  // Child meter, child receipt, and rolled-up parent each count the paid
  // compaction exactly once.
  assert.equal(Math.abs(nested.spent - nested.totalEstimatedUsd) < 1e-9, true);
  assert.equal(nested.spent <= nested.max, true);
  // The parent's total covers the child exactly once.
  assert.equal(
    Math.abs(result.costUsd - result.receipt.totalEstimatedUsd) < 1e-9,
    true,
  );
});

test("M5: the legacy subagent cost ledger is live without a carved meter", async () => {
  // Separate legacy path, asserted end to end: with no parent BudgetMeter to
  // carve, a child call that settles above maxCostUsd raises the legacy error.
  const child = agent({
    id: "m5-ledger-child",
    instructions: "Answer.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "m5-ledger-parent",
    instructions: "Delegate.",
    model: auto(),
    sandbox: "fixture",
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxCostUsd: 1,
    })],
  });
  let call = 0;
  const expensive = (selected) => {
    call++;
    if (call === 1) {
      return pushMessage(
        selected,
        [{ type: "toolCall", id: "d1", name: "delegate", arguments: { task: "sub" } }],
        "toolUse",
        usage({ input: 500, output: 50 }),
      );
    }
    // The child's call costs far more than its wallet's ledger held for it.
    return pushMessage(
      selected,
      [{ type: "text", text: "child answer" }],
      "stop",
      usage({ input: 2_000_000, output: 100 }),
    );
  };
  // The ledger refuses, the child run fails on it, and the failure reaches the
  // caller rather than being absorbed as a completed delegation.
  await assert.rejects(
    run(parent, "go", { ensureRuntime: false, model: pricedFauxModel(), streamFn: expensive }),
    /cave_subagent_cost_budget|cave_nested_usage_incomplete|cave_provider_terminal_error/,
  );
  assert.equal(call >= 2, true, "the child never reached the provider");

  // And the same ledger mechanism at the root, where its own code surfaces
  // directly instead of through the nested-usage chain.
  let rootCalls = 0;
  await assert.rejects(
    run(child, "go", {
      ensureRuntime: false,
      model: pricedFauxModel(),
      maxCostUsd: 0.5,
      streamFn: (selected) => {
        rootCalls++;
        return pushMessage(
          selected,
          [{ type: "text", text: "answer" }],
          "stop",
          usage({ input: 2_000_000, output: 100 }),
        );
      },
    }),
    /cave_run_cost_budget_exceeded/,
  );
  assert.equal(rootCalls, 1);
});

// ---------------------------------------------------------------------------
// M1 — the Pi lane gated maxUsd on catalog pricing alone, so a run
// authenticated with a Claude Pro/Max subscription passed and reported
// list-price dollars for a run that is not billed per token.
// ---------------------------------------------------------------------------

test("M1: a subscription-authenticated run refuses a dollar budget", async () => {
  const model = pricedFauxModel();
  const subscription = {
    getModel: () => model,
    streamSimple: () => undefined,
    checkAuth: async () => ({ type: "oauth", source: "OAuth" }),
  };
  // Pi's own transport, off the gateway: the credential store IS the authority
  // for how this run is billed, and it says subscription.
  await assert.rejects(
    run(pollingAgent("m1-oauth"), "go", {
      ensureRuntime: false,
      model,
      models: subscription,
      cave: "off",
      budget: { maxUsd: 1 },
    }),
    /cave_budget_denomination_unavailable/,
  );

  // The same subscription meters tokens perfectly well.
  const tokenCapped = await run(pollingAgent("m1-oauth-tokens"), "go", {
    ensureRuntime: false,
    model,
    models: subscription,
    budget: { maxTokens: 4_000 },
    streamFn: (selected) => pushMessage(
      selected,
      [{ type: "text", text: "done" }],
      "stop",
      usage({ input: 100, output: 10 }),
    ),
  });
  assert.equal(tokenCapped.stopReason, "complete");
  assert.equal(tokenCapped.receipt.denomination, "tokens");
});

test("M1: an unprovable credential regime refuses a dollar budget unless asserted", async () => {
  const model = pricedFauxModel();
  // No checkAuth: Pi cannot prove the credential is metered, so a dollar budget
  // on its own transport off the gateway must fail closed rather than book
  // fictional dollars against what could be a subscription.
  const unknown = {
    getModel: () => model,
    streamSimple: () => undefined,
  };
  await assert.rejects(
    run(pollingAgent("m1-unknown"), "go", {
      ensureRuntime: false,
      model,
      models: unknown,
      cave: "off",
      budget: { maxUsd: 1 },
    }),
    /cave_budget_denomination_unavailable/,
  );
  // A throwing checkAuth is equally unprovable.
  const throwing = {
    getModel: () => model,
    streamSimple: () => undefined,
    checkAuth: async () => { throw new Error("cannot reach credential store"); },
  };
  await assert.rejects(
    run(pollingAgent("m1-throwing"), "go", {
      ensureRuntime: false,
      model,
      models: throwing,
      cave: "off",
      budget: { maxUsd: 1 },
    }),
    /cave_budget_denomination_unavailable/,
  );
  // The escape hatch: the caller asserts the credential is metered. The gate is
  // still exercised (Pi's own transport, no streamFn), and the assertion lets a
  // dollar budget through. streamSimple supplies the transport.
  const metered = {
    getModel: () => model,
    streamSimple: (selected) => pushMessage(
      selected,
      [{ type: "text", text: "done" }],
      "stop",
      usage({ input: 100, output: 10 }),
    ),
  };
  const asserted = await run(pollingAgent("m1-asserted"), "go", {
    ensureRuntime: false,
    model,
    models: metered,
    cave: "off",
    assumeMeteredCredential: true,
    budget: { maxUsd: 1 },
  });
  assert.equal(asserted.receipt.denomination, "usd");
});

test("M1: assumeMeteredCredential cannot override a PROVEN subscription", async () => {
  const model = pricedFauxModel();
  const subscription = {
    getModel: () => model,
    streamSimple: () => undefined,
    checkAuth: async () => ({ type: "oauth", source: "OAuth" }),
  };
  // The escape hatch is only for an UNPROVABLE regime. A credential store that
  // positively identifies a subscription still refuses a dollar budget — the
  // assertion cannot manufacture metered billing that isn't there.
  await assert.rejects(
    run(pollingAgent("m1-oauth-asserted"), "go", {
      ensureRuntime: false,
      model,
      models: subscription,
      cave: "off",
      assumeMeteredCredential: true,
      budget: { maxUsd: 1 },
    }),
    /cave_budget_denomination_unavailable/,
  );
});

test("M1: an api-key run keeps its dollar budget", async () => {
  const model = pricedFauxModel();
  const apiKey = {
    getModel: () => model,
    streamSimple: () => undefined,
    checkAuth: async () => ({ type: "api_key", source: "ANTHROPIC_API_KEY" }),
  };
  const result = await run(pollingAgent("m1-api-key"), "go", {
    ensureRuntime: false,
    model,
    models: apiKey,
    budget: { maxUsd: 1 },
    streamFn: (selected) => pushMessage(
      selected,
      [{ type: "text", text: "done" }],
      "stop",
      usage({ input: 100, output: 10 }),
    ),
  });
  assert.equal(result.stopReason, "complete");
  assert.equal(result.receipt.denomination, "usd");
});

// ---------------------------------------------------------------------------
// M6 — the reserve was computed on the pre-onPayload context, but onPayload can
// restore uncompressed originals on cache-prefix drift, sending a payload
// larger than the one that was priced.
// ---------------------------------------------------------------------------

test("M6: the reserve bounds the payload that actually leaves, restorations included", async () => {
  // The transform substitutes a short string for a long one; drift puts the
  // long one back. Whatever onPayload does, the request that leaves must be
  // covered by the hold that was taken before it.
  const model = pricedFauxModel();
  const seen = [];
  // The substitution an applied transform leaves behind: the request carries
  // the compressed form, and onPayload restores the original on cache drift.
  const COMPRESSED = "<cave-compressed>ref-1</cave-compressed>";
  const ORIGINAL = "the original tool result, at length, ".repeat(300);
  const result = await run(pollingAgent("m6-restore"), "go", {
    ensureRuntime: false,
    model,
    budget: { maxUsd: 0.5, onExhausted: "stop" },
    providerPayloadContract: "pi-on-payload-v1",
    streamFn: (selected, context, streamOptions) => {
      // The compressed request, as the reserve saw it.
      const outgoing = {
        systemPrompt: context.systemPrompt,
        messages: [
          ...context.messages,
          { role: "toolResult", toolCallId: "t1", toolName: "poll", content: [{ type: "text", text: COMPRESSED }] },
        ],
      };
      streamOptions?.onPayload?.(outgoing, selected);
      // Drift: onPayload puts the originals back, and THIS is what leaves.
      const conversationOriginals = new Map([[COMPRESSED, ORIGINAL]]);
      const restorable = restorableRequestBytes(
        conversationOriginals,
        context.systemPrompt ?? "",
        context.systemPrompt ?? "",
      );
      assert.equal(restorable > 0, true, "the scenario has no restorable substitution");
      const restored = {
        systemPrompt: outgoing.systemPrompt,
        messages: outgoing.messages.map((message) => (
          message.role === "toolResult"
            ? { ...message, content: [{ type: "text", text: ORIGINAL }] }
            : message
        )),
      };
      seen.push({
        ceiling: callCeilingFor(
          selected,
          context.systemPrompt ?? "",
          outgoing.messages,
          context.tools,
          selected.maxTokens,
          restorable,
        ).inputTokenCeiling,
        naiveCeiling: callCeilingFor(
          selected,
          context.systemPrompt ?? "",
          outgoing.messages,
          context.tools,
          selected.maxTokens,
        ).inputTokenCeiling,
        payloadBytes: Buffer.byteLength(JSON.stringify(restored), "utf8"),
      });
      return pushMessage(
        selected,
        [{ type: "text", text: "done" }],
        "stop",
        usage({ input: 100, output: 10 }),
      );
    },
  });
  assert.equal(seen.length > 0, true);
  // The tie the finding asked for: the ceiling the call was held at covers the
  // bytes that actually left, AFTER onPayload restored the originals. Bytes
  // bound tokens from above, so covering the byte count covers the token count.
  for (const call of seen) {
    assert.equal(
      call.ceiling >= call.payloadBytes,
      true,
      `ceiling ${call.ceiling} does not cover the ${call.payloadBytes} bytes that left`,
    );
    // And without the restorable term it does not — which is the gap.
    assert.equal(call.naiveCeiling < call.payloadBytes, true);
  }
  assert.equal(result.receipt.capBreached, false);
  assert.equal(result.receipt.spent <= result.receipt.max, true);
});

test("M6: the reserve's ceiling covers the payload onPayload would restore", () => {
  // The discriminating assertion: build the compressed request the reserve is
  // computed on, and the restored request onPayload can put back in its place,
  // then require the ceiling to cover the RESTORED one. Drop the restorable
  // term from callCeilingFor and this fails — the ceiling would only cover the
  // compressed bytes.
  const model = pricedFauxModel();
  const originalText = "the original tool result, at length, ".repeat(400);
  const compressedText = "<cave-compressed>ref</cave-compressed>";
  const originalInstructions = `system prompt ${"and its own long preamble ".repeat(200)}`;
  const compressedInstructions = "system prompt <compressed>";
  const compressedMessages = [
    { role: "user", content: "go", timestamp: 1 },
    { role: "toolResult", toolCallId: "t1", toolName: "read", content: [{ type: "text", text: compressedText }] },
  ];
  const restoredMessages = [
    { role: "user", content: "go", timestamp: 1 },
    { role: "toolResult", toolCallId: "t1", toolName: "read", content: [{ type: "text", text: originalText }] },
  ];

  const originals = new Map([[compressedText, originalText]]);
  const restorable = restorableRequestBytes(
    originals,
    compressedInstructions,
    originalInstructions,
  );
  assert.equal(restorable > 0, true);

  const ceiling = callCeilingFor(
    model,
    compressedInstructions,
    compressedMessages,
    undefined,
    1_000,
    restorable,
  );
  // Bytes bound tokens from above, so the restored payload's byte count is the
  // number the ceiling has to cover.
  const restoredBytes = Buffer.byteLength(
    JSON.stringify({
      systemPrompt: originalInstructions,
      messages: restoredMessages,
      tools: undefined,
    }),
    "utf8",
  );
  assert.equal(
    ceiling.inputTokenCeiling >= restoredBytes,
    true,
    `ceiling ${ceiling.inputTokenCeiling} does not cover restored payload ${restoredBytes}`,
  );

  // Without the term the ceiling covers only what was compressed — which is
  // exactly the gap this finding named.
  const naive = callCeilingFor(model, compressedInstructions, compressedMessages, undefined, 1_000);
  assert.equal(naive.inputTokenCeiling < restoredBytes, true);

  // The context window still caps the result: a request larger than the window
  // cannot be served at all.
  const capped = callCeilingFor(
    { ...model, contextWindow: 4_000 },
    compressedInstructions,
    compressedMessages,
    undefined,
    1_000,
    restorable,
  );
  assert.equal(capped.inputTokenCeiling, 4_000);
});

test("M6: restorable growth counts every substitution the run could reverse", () => {
  // Each original's length over its replacement's, plus the system prompt's,
  // added whole — an over-estimate whenever no drift occurs, which is the only
  // safe direction for a ceiling.
  const originals = new Map([
    ["short", "a much longer original"],
    ["tiny", "another original that is longer"],
  ]);
  const growth = restorableRequestBytes(originals, "sys", "sys and more");
  assert.equal(
    growth,
    ("a much longer original".length - "short".length) +
      ("another original that is longer".length - "tiny".length) +
      ("sys and more".length - "sys".length),
  );
  // A substitution that did not shrink anything contributes nothing, never a
  // negative that would eat another's growth.
  assert.equal(restorableRequestBytes(new Map([["long replacement", "sh"]]), "sys", "sys"), 0);
});

// ---------------------------------------------------------------------------
// N1 — the breach flags read only the run's own meter, so a child that
// outspent its wallet reported the breach on its own receipt while the
// top-level receipt said the cap held. Wallets are small carves, so this is
// the common case rather than an edge one.
// ---------------------------------------------------------------------------

test("N1: a breached subagent wallet surfaces on the top-level receipt", async () => {
  const child = agent({
    id: "n1-child",
    instructions: "Answer.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "n1-parent",
    instructions: "Delegate.",
    model: auto(),
    sandbox: "fixture",
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      // A token-denominated run carves the token wallet; maxCostUsd is the
      // separate legacy ledger and is left generous so it is not what fires.
      maxTokens: 5_000,
      maxCostUsd: 50,
    })],
  });
  let call = 0;
  const result = await run(parent, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 1_000_000 },
    streamFn: (selected) => {
      call++;
      if (call === 1) {
        return pushMessage(
          selected,
          [{ type: "toolCall", id: "d1", name: "delegate", arguments: { task: "sub" } }],
          "toolUse",
          usage({ input: 500, output: 50 }),
        );
      }
      if (call === 2) {
        // The child's call reports far more tokens than its wallet holds,
        // while staying cheap enough in dollars that the legacy per-call
        // ceiling is not what stops it.
        return pushMessage(
          selected,
          [{ type: "text", text: "child answer" }],
          "stop",
          usage({ input: 100, output: 50, cacheRead: 200_000 }),
        );
      }
      return pushMessage(
        selected,
        [{ type: "text", text: "answer" }],
        "stop",
        usage({ input: 200, output: 20 }),
      );
    },
  });
  const nested = result.receipt.subagents[0];
  assert.notEqual(nested, undefined);
  assert.equal(nested.capBreached, true, "the child did not breach its wallet");
  assert.equal(nested.overspent > 0, true);
  // The whole point: a caller reading the top-level receipt learns that
  // something under this run outspent its cap.
  assert.equal(result.receipt.capBreached, true);
  assert.equal(result.capBreached, true);
  // The parent's own meter is not itself breached, so its own overspend is
  // zero — the FLAG rolls up, the amount does not. The child's figure stays on
  // the child's receipt where it cannot be double-counted.
  assert.equal(result.receipt.spent <= result.receipt.max, true);
  assert.equal(result.receipt.overspent, 0);
  assert.equal(result.overspent, 0);
  assert.equal(nested.overspent > 0, true);
});

test("N7: overspent is this level's own, never a double-counted tree total", () => {
  // The reviewer's repro. settleCarve books the child's REAL spend against the
  // parent, so the same dollars appear in both meters: parent max 2, child
  // wallet 1, child settles 5 → the parent's own cap is blown by 3 and the
  // child's by 4. Adding them printed 7 next to a tree that spent 5.
  const parentMeter = new BudgetMeter(normalizeRunBudget({ maxUsd: 2 }));
  const carve = parentMeter.carve(1);
  const childHold = carve.child.reserve(1, 1_000);
  carve.child.settle(childHold, 5);
  parentMeter.settleCarve(carve);

  assert.equal(carve.child.settled, 5);
  assert.equal(carve.child.overspent, 4);
  assert.equal(parentMeter.settled, 5);
  assert.equal(parentMeter.overspent, 3);

  const childReceipt = new ReceiptRecorder().build({
    runId: "c",
    agentId: "child",
    stopReason: "budget_exhausted",
    meter: carve.child,
  });
  const recorder = new ReceiptRecorder();
  recorder.recordSubagent(childReceipt);
  const parent = recorder.build({
    runId: "p",
    agentId: "parent",
    stopReason: "budget_exhausted",
    meter: parentMeter,
  });

  // Each level reports its own breach; the flag is what rolls up.
  assert.equal(parent.overspent, 3);
  assert.equal(parent.subagents[0].overspent, 4);
  assert.equal(parent.capBreached, true);
  assert.equal(parent.subagents[0].capBreached, true);
  // And the figure printed beside `spent` can never exceed it.
  assert.equal(parent.overspent <= parent.spent, true);
});

test("N1: the breach flag rolls up even when the parent's own cap held", () => {
  const child = new ReceiptRecorder().build({
    runId: "c1",
    agentId: "child-one",
    stopReason: "budget_exhausted",
    meter: breachedMeter(1, 3),
  });
  const sibling = new ReceiptRecorder().build({
    runId: "c2",
    agentId: "child-two",
    stopReason: "budget_exhausted",
    meter: breachedMeter(2, 5),
  });
  const clean = new ReceiptRecorder().build({
    runId: "c3",
    agentId: "child-three",
    stopReason: "complete",
    meter: undefined,
  });
  assert.equal(child.overspent, 2);
  assert.equal(sibling.overspent, 3);
  assert.equal(clean.capBreached, false);

  const parent = new ReceiptRecorder();
  parent.recordSubagent(child);
  parent.recordSubagent(sibling);
  parent.recordSubagent(clean);
  const rolled = parent.build({
    runId: "p",
    agentId: "parent",
    stopReason: "complete",
    meter: undefined,
  });
  // The flag rolls up; the amount does not. This parent has no meter of its
  // own, so it has no overspend of its own — the children's amounts stay on
  // the children's receipts, where each is reported exactly once.
  assert.equal(rolled.capBreached, true);
  assert.equal(rolled.overspent, 0);
  assert.deepEqual(rolled.subagents.map((entry) => entry.overspent), [2, 3, 0]);

  // A parent with its own breach reports its own amount, not a total.
  const both = new ReceiptRecorder();
  both.recordSubagent(child);
  const withOwn = both.build({
    runId: "p2",
    agentId: "parent-two",
    stopReason: "budget_exhausted",
    meter: breachedMeter(10, 17),
  });
  assert.equal(withOwn.capBreached, true);
  assert.equal(withOwn.overspent, 7);
  assert.equal(withOwn.subagents[0].overspent, 2);
});

// ---------------------------------------------------------------------------
// N2 — the credential-regime gate refused two paths its own comment exempted:
// a caller-supplied transport that never touches Pi's credential store, and a
// gateway-routed run whose dollars come from the account key rather than the
// local OAuth login.
// ---------------------------------------------------------------------------

test("N2: a caller-supplied transport is not judged by Pi's credential store", async () => {
  // The run never asks Pi to authenticate anything: the caller owns the
  // transport. Whatever is logged in locally says nothing about how this run
  // is billed, so it cannot be grounds for refusing a dollar budget.
  const model = pricedFauxModel();
  let checked = 0;
  const subscription = {
    getModel: () => model,
    streamSimple: () => undefined,
    checkAuth: async () => {
      checked++;
      return { type: "oauth", source: "OAuth" };
    },
  };
  const result = await run(pollingAgent("n2-streamfn"), "go", {
    ensureRuntime: false,
    model,
    models: subscription,
    budget: { maxUsd: 1 },
    streamFn: (selected) => pushMessage(
      selected,
      [{ type: "text", text: "done" }],
      "stop",
      usage({ input: 100, output: 10 }),
    ),
  });
  assert.equal(result.stopReason, "complete");
  assert.equal(result.receipt.denomination, "usd");
  assert.equal(checked, 0, "the credential store was consulted for a run it does not authenticate");
});

test("N2: a managed gateway USD run is judged on the gateway credential", async () => {
  // Managed readiness proves the gateway supplies the provider key, so the
  // local login is not the payer.
  // A developer signed in to Claude Pro on the same machine must still be able
  // to cap a gateway run in dollars — it is the flagship paid path.
  const priorKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "test-cave-key";
  const model = pricedFauxModel();
  let providerCalls = 0;
  const subscription = {
    getModel: () => model,
    streamSimple: (selected) => {
      providerCalls++;
      return pushMessage(
        selected,
        [{ type: "text", text: "done" }],
        "stop",
        usage({ input: 100, output: 10 }),
      );
    },
    checkAuth: async () => ({ type: "oauth", source: "OAuth" }),
  };
  try {
    const result = await run(pollingAgent("n2-gateway"), "go", {
      ensureRuntime: false,
      model,
      models: subscription,
      gatewayURL: "http://127.0.0.1:8787",
      cave: "auto",
      budget: { maxUsd: 1 },
      fetch: async () => Response.json({
        ok: true,
        service: "caveman-proxy",
        schema: "caveman.proxy.health.v1",
        billing: "managed",
        adapters: 4,
      }),
    });
    assert.equal(result.stopReason, "complete");
    assert.equal(result.receipt.denomination, "usd");
    assert.equal(providerCalls, 1);
  } finally {
    if (priorKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = priorKey;
  }
});

test("N8: a BYOK gateway cannot turn a local subscription into dollars", async () => {
  const model = pricedFauxModel();
  let providerCalls = 0;
  const subscription = {
    getModel: () => model,
    streamSimple: () => {
      providerCalls++;
      throw new Error("must not reach provider");
    },
    checkAuth: async () => ({ type: "oauth", source: "OAuth" }),
  };
  await assert.rejects(
    run(pollingAgent("n8-byok-gateway"), "go", {
      ensureRuntime: false,
      model,
      models: subscription,
      gatewayURL: "http://127.0.0.1:8787",
      cave: "auto",
      budget: { maxUsd: 1 },
      fetch: async () => Response.json({
        ok: true,
        service: "caveman-proxy",
        schema: "caveman.proxy.health.v1",
        billing: "byok",
        adapters: 4,
      }),
    }),
    /cave_budget_denomination_unavailable/,
  );
  assert.equal(providerCalls, 0);
});

test("N8: legacy gateway billing provenance fails toward the local credential gate", async () => {
  const model = pricedFauxModel();
  const subscription = {
    getModel: () => model,
    streamSimple: () => {
      throw new Error("must not reach provider");
    },
    checkAuth: async () => ({ type: "oauth", source: "OAuth" }),
  };
  await assert.rejects(
    run(pollingAgent("n8-unknown-gateway"), "go", {
      ensureRuntime: false,
      model,
      models: subscription,
      gatewayURL: "http://127.0.0.1:8787",
      cave: "auto",
      budget: { maxUsd: 1 },
      fetch: async () => Response.json({
        ok: true,
        service: "caveman-proxy",
        schema: "caveman.proxy.health.v1",
        adapters: 4,
      }),
    }),
    /cave_budget_denomination_unavailable/,
  );
});

test("N2: a local subscription run on Pi's own transport still refuses dollars", async () => {
  // The case the gate exists for, unchanged: Pi authenticates, the credential
  // is a subscription, and the run is not billed per token.
  const model = pricedFauxModel();
  const models = {
    getModel: () => model,
    streamSimple: () => {
      throw new Error("must not reach the provider");
    },
    checkAuth: async () => ({ type: "oauth", source: "OAuth" }),
  };
  await assert.rejects(
    run(pollingAgent("n2-local-oauth"), "go", {
      ensureRuntime: false,
      model,
      models,
      cave: "off",
      budget: { maxUsd: 1 },
    }),
    /cave_budget_denomination_unavailable/,
  );
});

function breachedMeter(max, settled) {
  const meter = new BudgetMeter(normalizeRunBudget({ maxUsd: max }));
  const held = meter.reserve(max, 1_000);
  meter.settle(held, settled);
  return meter;
}

function fauxReceiptCall() {
  return {
    provider: "anthropic",
    model: PRICED_MODEL,
    inputTokens: 10,
    outputTokens: 2,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    reasoningTokens: 0,
    estimatedUsd: 0,
    unpriced: false,
    usageBasis: "provider_reported",
    clampedOutputTokens: undefined,
  };
}

test("B3: workingCallsAfter counts only the calls that followed the rewrite", async () => {
  const observed = await runWithOneCompaction("b3-counted");
  const summarized = observed.result.receipt.compactions
    .find((entry) => entry.tier === "summarized");
  assert.notEqual(summarized, undefined);
  // Every receipt call recorded after the compaction, and no others: the
  // summarizer's own entry precedes the compaction record and is excluded.
  const summarizerIndex = observed.result.receipt.calls
    .findIndex((entry) => entry.outputTokens === 2_000);
  assert.notEqual(summarizerIndex, -1);
  const after = observed.result.receipt.calls.length - (summarizerIndex + 1);
  assert.equal(summarized.workingCallsAfter, after);
  assert.equal(observed.workingAfterCompaction, after);
});

// ---------------------------------------------------------------------------

export function validSummaryJSON() {
  return JSON.stringify({
    schema_version: 1,
    objective: "Answer the user's question.",
    constraints_restated: ["reply in one word"],
    decisions: [{ decision: "polled", why: "pending" }],
    artifacts: [{ path: "queue", change: "observed" }],
    facts: ["job id 7"],
    state: { completed: ["polled"], active: ["waiting"], blocked: [] },
    next: ["poll again"],
    citations: [],
    lookup_hints: ["queue status"],
  });
}

/**
 * Drives one run through exactly one summarizing compaction, on a cheap
 * summarizer over an expensive working model — the shape where a
 * cold-reserved compaction is genuinely affordable.
 */
async function runWithOneCompaction(id, options = {}) {
  const working = { count: 0 };
  const observed = { summarizerCalls: 0, workingAfterCompaction: 0, compacted: false };
  const workingModel = pricedFauxModel({
    id: "claude-opus-4-1",
    contextWindow: 200_000,
    maxTokens: 4_000,
  });
  const summarizerModel = pricedFauxModel({ id: PRICED_MODEL, contextWindow: 200_000 });
  const result = await run(pollingAgent(id), "go", {
    ensureRuntime: false,
    model: workingModel,
    budget: {
      maxUsd: 4,
      onExhausted: "compact",
      compaction: {
        minYieldTokens: 1_000,
        headroomCalls: 1,
        keepRecentTokens: 2_000,
        summarizerModel,
      },
    },
    streamFn: (selected, context) => {
      const last = context.messages.at(-1);
      if (typeof last?.content === "string" &&
          last.content.includes("Reply with a single JSON object")) {
        observed.summarizerCalls++;
        observed.compacted = true;
        return pushMessage(
          selected,
          [{ type: "text", text: validSummaryJSON() }],
          "stop",
          usage({ input: 100_000, output: 2_000 }),
        );
      }
      working.count++;
      if (observed.compacted) observed.workingAfterCompaction++;
      const finish = observed.compacted && options.stopAfterCompaction === true;
      if (finish || working.count > 60) {
        return pushMessage(
          selected,
          [{ type: "text", text: "done" }],
          "stop",
          usage({ input: 10, output: 2 }),
        );
      }
      return pushMessage(
        selected,
        [
          { type: "text", text: `${BULK}-${working.count}` },
          {
            type: "toolCall",
            id: `poll-${working.count}`,
            name: "poll",
            arguments: { key: `job-${working.count}` },
          },
        ],
        "toolUse",
        usage({ input: 500, output: 800, cacheRead: 20_000 }),
      );
    },
  });
  return { result, ...observed };
}
