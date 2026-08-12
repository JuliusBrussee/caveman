import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import Ajv2020 from "ajv/dist/2020.js";
import {
  CavemanRunError,
  agent,
  auto,
  createBudgetController,
  output,
  run,
  schema,
  subagent,
  tool,
} from "../dist/index.js";
import {
  BudgetMeter,
  OUTPUT_CLAMP_FLOOR_TOKENS,
  affordableOutputTokens,
  callCeilingCost,
  inputTokenCeiling,
  normalizeRunBudget,
  planCall,
} from "../dist/budget.js";
import { catalogCost } from "../dist/catalog.js";
import {
  fauxProvider as upstreamFauxProvider,
} from "@earendil-works/pi-ai/providers/faux";
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";

const PRICED_MODEL = "claude-haiku-4-5";
const receiptSchema = JSON.parse(readFileSync(
  new URL("../../shared/contracts/schemas/agent-run-receipt.schema.json", import.meta.url),
  "utf8",
));
const validateReceipt = new Ajv2020({ strict: true, allErrors: true }).compile(receiptSchema);

function assertSharedReceiptContract(receipt) {
  const wire = JSON.parse(JSON.stringify(receipt));
  assert.equal(
    validateReceipt(wire),
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

function assistantMessage(selected, content, stopReason, used) {
  return {
    role: "assistant",
    content,
    api: selected.api,
    provider: selected.provider,
    model: selected.id,
    usage: used,
    stopReason,
    timestamp: Date.now(),
  };
}

function pushMessage(stream, message) {
  queueMicrotask(() => {
    const partial = { ...message, content: [], stopReason: "pending" };
    stream.push({ type: "start", partial: { ...partial } });
    stream.push({ type: "done", reason: message.stopReason, message });
    stream.end(message);
  });
  return stream;
}

/**
 * A provider that never finishes on its own: every turn asks for the same tool
 * again, so the only thing that can end the loop is the runtime's own
 * between-calls decision.
 */
function endlessToolProvider(observed) {
  return (selected, _context, streamOptions) => {
    observed.calls.push({ maxTokens: streamOptions?.maxTokens });
    const message = assistantMessage(
      selected,
      [{ type: "toolCall", id: `call-${observed.calls.length}`, name: "peek", arguments: {} }],
      "toolUse",
      usage(observed.inputTokens, observed.outputTokens),
    );
    return pushMessage(createAssistantMessageEventStream(), message);
  };
}

function loopingAgent(options = {}) {
  return agent({
    id: options.id ?? "budget-loop",
    instructions: "Keep calling peek.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
    tools: [tool({
      name: "peek",
      description: "Return a fixed value.",
      input: schema.object({}),
      effect: "read",
      execute: () => {
        options.onPeek?.();
        return "peeked";
      },
    })],
    ...(options.output === undefined ? {} : { output: options.output }),
  });
}

test("budget shape is settled before the first call and fails closed on ambiguity", async () => {
  const defined = loopingAgent({ id: "budget-validation" });
  const model = pricedFauxModel();
  const never = () => {
    throw new Error("must not reach the provider");
  };
  const cases = [
    [{}, /cave_budget_denomination_ambiguous/],
    [{ maxUsd: 1, maxTokens: 1_000 }, /cave_budget_denomination_ambiguous/],
    [{ maxUsd: 1, initialTokens: 100 }, /cave_budget_denomination_ambiguous/],
    [{ maxUsd: 0 }, /cave_budget_max_invalid/],
    [{ maxUsd: Number.POSITIVE_INFINITY }, /cave_budget_max_invalid/],
    [{ maxTokens: 10.5 }, /cave_budget_max_invalid/],
    [{ maxUsd: 1, initialUsd: 2 }, /cave_budget_initial_invalid/],
    [{ maxUsd: 1, initialUsd: 0 }, /cave_budget_initial_invalid/],
    [{ maxUsd: 1, outputFloorTokens: 0 }, /cave_budget_output_floor_invalid/],
  ];
  for (const [budget, expected] of cases) {
    await assert.rejects(
      run(defined, "go", { ensureRuntime: false, model, streamFn: never, budget }),
      expected,
    );
  }
  await assert.rejects(
    run(defined, "go", {
      ensureRuntime: false,
      model,
      streamFn: never,
      budget: { maxUsd: 1 },
      maxCostUsd: 1,
    }),
    /cave_budget_conflicting_cap/,
  );
  await assert.rejects(
    run(defined, "go", {
      ensureRuntime: false,
      model,
      streamFn: never,
      budget: { maxUsd: 1 },
      deadlineMs: 0,
    }),
    /cave_run_deadline_invalid/,
  );
});

test("a USD budget refuses a model the public catalog cannot price", async () => {
  const defined = loopingAgent({ id: "budget-denomination" });
  const handle = upstreamFauxProvider({ provider: "anthropic" });
  let calls = 0;
  await assert.rejects(
    run(defined, "go", {
      ensureRuntime: false,
      model: handle.getModel(),
      budget: { maxUsd: 1 },
      streamFn: () => {
        calls++;
        throw new Error("must not reach the provider");
      },
    }),
    /cave_budget_denomination_unavailable/,
  );
  assert.equal(calls, 0);
  // The same unpriced model is perfectly meterable in tokens, so a token
  // budget is accepted where a dollar budget is not.
  const observed = { calls: [], inputTokens: 200, outputTokens: 40 };
  const tokenCapped = await run(defined, "go", {
    ensureRuntime: false,
    model: handle.getModel(),
    budget: { maxTokens: 4_000 },
    streamFn: endlessToolProvider(observed),
  });
  assert.equal(tokenCapped.stopReason, "budget_exhausted");
  assert.equal(observed.calls.length > 0, true);
});

test("a budget stops the run between calls and returns partial work, never a throw", async () => {
  const defined = loopingAgent({ id: "budget-stop" });
  const observed = { calls: [], inputTokens: 900, outputTokens: 200 };
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxUsd: 0.05 },
    streamFn: endlessToolProvider(observed),
  });
  assert.equal(result.stopReason, "budget_exhausted");
  assert.equal(result.usageBasis, "provider_reported");
  assert.equal(result.priceBasis, "public_catalog");
  // Every call that started also finished and was counted: the run stops
  // between calls, so its totals cover exactly the calls the provider saw.
  assert.equal(result.inputTokens, observed.calls.length * 900);
  assert.equal(result.outputTokens, observed.calls.length * 200);
  assert.equal(result.costUsd <= 0.05, true);
  assert.equal(result.costUsd > 0, true);
  assert.equal(result.toolCalls.length, observed.calls.length);
});

test("a budget too small for the first call spends nothing and says so", async () => {
  const defined = loopingAgent({ id: "budget-zero-call" });
  let calls = 0;
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxUsd: 0.0000001 },
    streamFn: () => {
      calls++;
      throw new Error("must not reach the provider");
    },
  });
  assert.equal(calls, 0);
  assert.equal(result.stopReason, "budget_exhausted");
  assert.equal(result.text, "");
  assert.equal(result.costUsd, 0);
  assert.equal(result.inputTokens, 0);
  assert.equal(result.provider, "anthropic");
  assert.equal(result.model, PRICED_MODEL);
});

test("the last affordable calls are clamped down to what the budget can still cover", async () => {
  const defined = loopingAgent({
    id: "budget-clamp",
    output: output({ maxTokens: 3_000 }),
  });
  const observed = { calls: [], inputTokens: 800, outputTokens: 300 };
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 12_000 },
    streamFn: endlessToolProvider(observed),
  });
  assert.equal(result.stopReason, "budget_exhausted");
  const asked = observed.calls.map((call) => call.maxTokens);
  assert.equal(asked.every((value) => value <= 3_000), true);
  const clamped = asked.filter((value) => value < 3_000);
  assert.equal(clamped.length > 0, true);
  assert.equal(clamped.every((value) => value >= OUTPUT_CLAMP_FLOOR_TOKENS), true);
  // Clamping only ever lowers the allowance as the budget drains.
  assert.deepEqual(clamped, [...clamped].sort((a, b) => b - a));
  assert.equal(result.inputTokens + result.outputTokens <= 12_000, true);
});

test("a deadline stops the run at the same between-calls point as a budget", async () => {
  let toolExecutions = 0;
  const defined = loopingAgent({
    id: "budget-deadline",
    onPeek: () => toolExecutions++,
  });
  const observed = { calls: [], inputTokens: 100, outputTokens: 20 };
  const endless = endlessToolProvider(observed);
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    deadlineMs: 1,
    streamFn: (selected, context, streamOptions) => {
      // Spend the whole deadline inside the first call: an in-flight call is
      // never cut short, so the stop lands before the second one.
      const until = performance.now() + 5;
      while (performance.now() < until) { /* deliberate busy wait */ }
      return endless(selected, context, streamOptions);
    },
  });
  assert.equal(result.stopReason, "deadline");
  // At most one call: the deadline either lands before the run reaches the
  // provider at all or after the first call finished. It never cuts one short,
  // so the totals always cover exactly the calls that happened.
  assert.equal(observed.calls.length <= 1, true);
  assert.equal(toolExecutions, 0);
  assert.equal(result.inputTokens, observed.calls.length * 100);
  assert.equal(result.receipt.calls.length, observed.calls.length);
});

test("an unbudgeted run reports the ordinary complete stop reason", async () => {
  const defined = agent({
    id: "budget-absent",
    instructions: "Reply.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    streamFn: (selected) => pushMessage(
      createAssistantMessageEventStream(),
      assistantMessage(selected, [{ type: "text", text: "done" }], "stop", usage(10, 2)),
    ),
  });
  assert.equal(result.stopReason, "complete");
  assert.equal(result.text, "done");
});

test("the input ceiling bounds tokens from above and never below", () => {
  // Every catalog tokenizer is byte-level BPE, so bytes bound tokens.
  assert.equal(inputTokenCeiling(1_000, 0, 128_000), 1_000);
  assert.equal(inputTokenCeiling(1_000, 4, 128_000), 1_064);
  // A request cannot exceed the context window, so neither can its ceiling.
  assert.equal(inputTokenCeiling(1_000_000, 10, 128_000), 128_000);
  assert.equal(inputTokenCeiling(0, 0, 128_000), 0);
});

test("the clamp finds the largest output the remainder affords, or nothing", () => {
  const meter = new BudgetMeter(normalizeRunBudget({ maxTokens: 5_000 }));
  const call = {
    provider: "anthropic",
    model: PRICED_MODEL,
    inputTokenCeiling: 1_000,
    outputTokenCap: 8_000,
  };
  assert.equal(affordableOutputTokens(meter, call), 4_000);
  const spent = meter.reserve(4_800, 4_000);
  meter.settle(spent, 4_800);
  // 200 left, and the input ceiling alone already costs more than that.
  assert.equal(affordableOutputTokens(meter, call), undefined);
  const tight = new BudgetMeter(normalizeRunBudget({ maxTokens: 1_100 }));
  // 100 of output on top of the input ceiling is below the floor, so the
  // ladder refuses rather than buying a truncated fragment.
  assert.equal(affordableOutputTokens(tight, call), undefined);
});

test("settle books a NaN measured cost at the reservation's worst case, never $0", () => {
  const meter = new BudgetMeter(normalizeRunBudget({ maxUsd: 1 }));
  const reservation = meter.reserve(0.4, 1_000);
  assert.ok(reservation, "expected a reservation");
  // An unreadable measured cost must not book zero — it fails closed at the
  // held worst case so an unknowable call is never a free call.
  meter.settle(reservation, Number.NaN);
  assert.equal(meter.settled, 0.4);
  // The same fail-closed rule holds for a non-finite Infinity.
  const other = new BudgetMeter(normalizeRunBudget({ maxUsd: 1 }));
  const held = other.reserve(0.25, 1_000);
  other.settle(held, Number.POSITIVE_INFINITY);
  assert.equal(other.settled, 0.25);
});

test("tranche release is bounded by max and never by the released pool", () => {
  const meter = new BudgetMeter(normalizeRunBudget({ maxUsd: 1, initialUsd: 0.25 }));
  assert.equal(meter.released, 0.25);
  assert.equal(meter.releasable(), 0.75);
  meter.release(0.5, "plan produced");
  assert.equal(meter.released, 0.75);
  assert.throws(() => meter.release(0.5, "too much"), /cave_budget_release_exceeds_max/);
  assert.throws(() => meter.release(0.1, "  "), /cave_budget_release_reason_required/);
  assert.throws(() => meter.release(-1, "negative"), /cave_budget_release_invalid/);
  meter.revoke();
  assert.throws(() => meter.release(0.1, "after revoke"), /cave_budget_revoked/);
  assert.equal(meter.reserve(0.01, 100), undefined);
});

test("no randomized sequence of real usage shapes settles past the cap unflagged", () => {
  // Settlement is driven by real provider usage shapes priced through the
  // catalog, not by a fraction of the reservation. The old version multiplied
  // the worst case by a number below 1, which assumed the very property the
  // meter exists to enforce — so it could not have caught a call that came in
  // above its hold.
  //
  // The invariant asserted here is the one the receipt promises: settled spend
  // stays inside max, or the breach is flagged and the meter funds nothing
  // further. Nothing is assumed about what a provider reports.
  let breaches = 0;
  let carves = 0;
  let unreadable = 0;
  let compactions = 0;
  for (let seed = 1; seed <= 600; seed++) {
    const random = mulberry32(seed);
    const tokens = seed % 2 === 0;
    const max = tokens
      ? 1 + Math.floor(random() * 200_000)
      : Math.max(1e-6, random() * 5);
    const meter = new BudgetMeter(normalizeRunBudget(
      tokens ? { maxTokens: max } : { maxUsd: max },
    ));
    let calls = 0;
    while (calls < 5_000) {
      if (meter.capBreached) break;
      // Every few calls, carve a subagent wallet: the child spends inside it
      // and the remainder returns to the parent.
      if (random() < 0.15) {
        const wallet = tokens
          ? 1 + Math.floor(meter.remaining() * random())
          : meter.remaining() * random();
        const carve = meter.carve(wallet);
        if (carve !== undefined) {
          carves++;
          const childHold = carve.child.reserve(carve.child.remaining() * random(), 512);
          if (childHold !== undefined) {
            carve.child.settle(childHold, childHold.amount * random());
          }
          meter.settleCarve(carve);
          assert.equal(
            carve.child.settled <= carve.child.max + 1e-9,
            true,
            "a wallet outspent its carve",
          );
        }
      }
      const inputCeiling = Math.floor(random() * 20_000);
      const call = {
        provider: "anthropic",
        model: PRICED_MODEL,
        inputTokenCeiling: inputCeiling,
        // Compaction reserves run at the summary cap rather than the working
        // allowance, so both shapes are walked.
        outputTokenCap: random() < 0.2
          ? 2_048
          : OUTPUT_CLAMP_FLOOR_TOKENS + Math.floor(random() * 16_000),
      };
      if (call.outputTokenCap === 2_048) compactions++;
      const plan = planCall(meter, call, false);
      if (plan.action !== "proceed") break;
      assert.notEqual(plan.reservation, undefined);
      assert.equal(plan.outputTokenCap >= OUTPUT_CLAMP_FLOOR_TOKENS, true);
      assert.equal(plan.outputTokenCap <= call.outputTokenCap, true);
      assert.equal(
        plan.reservation.amount,
        callCeilingCost(meter.denomination, call, plan.outputTokenCap),
      );

      // A real usage report: input split across fresh, cache-read and
      // cache-write classes, output split between visible and reasoning. The
      // splits are drawn independently of the reservation, and once in a while
      // the provider reports MORE than the ceiling anticipated — the case the
      // reviewer proved the old test could not reach.
      const surprise = random() < 0.05;
      const scale = surprise ? 1 + random() * 8 : random();
      const totalInput = Math.max(0, Math.floor(inputCeiling * scale));
      const fresh = Math.floor(totalInput * random());
      const cacheRead = Math.floor((totalInput - fresh) * random());
      const cacheWrite = totalInput - fresh - cacheRead;
      const outputTokens = Math.floor(plan.outputTokenCap * (surprise ? 1 + random() : random()));
      const reasoningTokens = Math.floor(outputTokens * random());

      if (random() < 0.05) {
        // Unreadable usage: the call happened and cost something we cannot
        // measure, so it settles at its worst case rather than reading as free.
        unreadable++;
        meter.settle(plan.reservation, plan.reservation.amount);
      } else {
        const priced = catalogCost({
          provider: call.provider,
          model: call.model,
          inputTokens: fresh,
          outputTokens,
          cacheReadTokens: cacheRead,
          cacheWriteTokens: cacheWrite,
          reasoningTokens,
        });
        assert.equal(priced.priced, true);
        meter.settle(
          plan.reservation,
          tokens ? totalInput + outputTokens : priced.usd,
        );
      }

      if (meter.capBreached) {
        breaches++;
        assert.equal(meter.settled > meter.max, true);
        assert.equal(meter.overspent > 0, true);
        // Terminal: nothing else may be funded from a breached meter.
        assert.equal(meter.reserve(0, 256), undefined);
        assert.equal(meter.carve(1), undefined);
      } else {
        assert.equal(meter.overspent, 0);
        assert.equal(meter.settled <= meter.max + 1e-9, true);
      }
      calls++;
    }
    assert.equal(calls < 5_000, true);
    if (!meter.capBreached) {
      assert.equal(meter.settled <= meter.max + 1e-9, true);
    }
  }
  // The walk actually reached every shape it claims to cover.
  assert.equal(breaches > 0, true, "no provider surprise was exercised");
  assert.equal(carves > 0, true, "no subagent wallet was exercised");
  assert.equal(unreadable > 0, true, "no unreadable usage was exercised");
  assert.equal(compactions > 0, true, "no compaction-shaped reserve was exercised");
});

test("every run carries a receipt that reconciles with its own result", async () => {
  const defined = agent({
    id: "receipt-basic",
    instructions: "Reply.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    streamFn: (selected) => pushMessage(
      createAssistantMessageEventStream(),
      assistantMessage(selected, [{ type: "text", text: "done" }], "stop", usage(1_000, 100)),
    ),
  });
  const { receipt } = result;
  assert.equal(receipt.schema, "caveman.agent.run-receipt.v1");
  assertSharedReceiptContract(receipt);
  assert.equal(receipt.basis, "estimated_list_price_subtotal");
  assert.equal(receipt.claimBasis, "inferred");
  assert.equal(receipt.runId, result.runId);
  assert.equal(receipt.agentId, "receipt-basic");
  assert.equal(receipt.stopReason, "complete");
  // No budget declared: the receipt still reports, nothing enforces.
  assert.equal(receipt.denomination, "none");
  assert.equal(receipt.max, undefined);
  assert.equal(receipt.spent, undefined);
  assert.deepEqual(receipt.tranches, []);
  assert.equal(receipt.calls.length, 1);
  assert.equal(receipt.calls[0].provider, "anthropic");
  assert.equal(receipt.calls[0].model, PRICED_MODEL);
  assert.equal(receipt.calls[0].inputTokens, 1_000);
  assert.equal(receipt.calls[0].outputTokens, 100);
  assert.equal(receipt.calls[0].usageBasis, "provider_reported");
  assert.equal(receipt.calls[0].unpriced, false);
  assert.equal(receipt.calls[0].clampedOutputTokens, undefined);
  // haiku-4-5 lists at $1/M input and $5/M output.
  assert.equal(receipt.calls[0].estimatedUsd, 0.0015);
  assert.equal(receipt.totalEstimatedUsd, result.costUsd);
  assert.equal(receipt.totalTokens, result.inputTokens + result.outputTokens);
  assert.equal(receipt.unpriced, false);
});

test("the receipt records the budget, its clamps, and every tool the run drove", async () => {
  const defined = loopingAgent({
    id: "receipt-budget",
    output: output({ maxTokens: 3_000 }),
  });
  const observed = { calls: [], inputTokens: 800, outputTokens: 300 };
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 12_000 },
    streamFn: endlessToolProvider(observed),
  });
  const { receipt } = result;
  assertSharedReceiptContract(receipt);
  assert.equal(receipt.denomination, "tokens");
  assert.equal(receipt.max, 12_000);
  assert.equal(receipt.released, 12_000);
  assert.equal(receipt.spent, receipt.totalTokens);
  assert.equal(receipt.spent <= receipt.max, true);
  assert.equal(receipt.stopReason, "budget_exhausted");
  assert.equal(receipt.calls.length, observed.calls.length);
  const clamped = receipt.calls.filter((call) => call.clampedOutputTokens !== undefined);
  assert.equal(clamped.length > 0, true);
  assert.equal(
    clamped.every((call) => call.clampedOutputTokens < 3_000),
    true,
  );
  assert.deepEqual(receipt.tools, [{
    name: "peek",
    calls: observed.calls.length,
    errors: 0,
  }]);
});

test("the receipt shows the spend tree down through subagents", async () => {
  const child = agent({
    id: "receipt-child",
    instructions: "Answer.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "receipt-parent",
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
    streamFn: (selected) => {
      call++;
      const stream = createAssistantMessageEventStream();
      if (call === 1) {
        return pushMessage(stream, assistantMessage(
          selected,
          [{ type: "toolCall", id: "delegate-1", name: "delegate", arguments: { task: "sub" } }],
          "toolUse",
          usage(500, 50),
        ));
      }
      return pushMessage(stream, assistantMessage(
        selected,
        [{ type: "text", text: call === 2 ? "child answer" : "parent answer" }],
        "stop",
        usage(200, 20),
      ));
    },
  });
  const { receipt } = result;
  assertSharedReceiptContract(receipt);
  assert.equal(receipt.subagents.length, 1);
  const nested = receipt.subagents[0];
  assert.equal(nested.agentId, "receipt-child");
  assert.equal(nested.calls.length, 1);
  assert.equal(nested.stopReason, "complete");
  // The parent's total covers everything below it, exactly once.
  const own = receipt.calls.reduce((total, entry) => total + entry.estimatedUsd, 0);
  assert.equal(
    Math.abs(receipt.totalEstimatedUsd - (own + nested.totalEstimatedUsd)) < 1e-10,
    true,
  );
  assert.equal(Math.abs(receipt.totalEstimatedUsd - result.costUsd) < 1e-10, true);
  assert.deepEqual(receipt.tools, [{ name: "delegate", calls: 1, errors: 0 }]);
});

test("an unpriced call is flagged rather than counted as free", async () => {
  const defined = agent({
    id: "receipt-unpriced",
    instructions: "Reply.",
    model: auto(),
    sandbox: "fixture",
  });
  const handle = upstreamFauxProvider({ provider: "anthropic" });
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: handle.getModel(),
    streamFn: (selected) => pushMessage(
      createAssistantMessageEventStream(),
      assistantMessage(selected, [{ type: "text", text: "done" }], "stop", usage(1_000, 100)),
    ),
  });
  assert.equal(result.priceBasis, "unpriced");
  assert.equal(result.receipt.unpriced, true);
  assert.equal(result.receipt.calls[0].unpriced, true);
  assert.equal(result.receipt.calls[0].estimatedUsd, 0);
  assert.equal(result.receipt.calls[0].inputTokens, 1_000);
  assert.equal(result.receipt.totalEstimatedUsd, 0);
});

function walletParent(options) {
  const child = agent({
    id: options.childId,
    instructions: "Answer.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  return agent({
    id: options.id,
    instructions: "Delegate.",
    model: auto(),
    sandbox: "fixture",
    tools: options.names.map((name) => subagent({
      name,
      description: "Delegate.",
      agent: child,
      ...(options.maxCostUsd === undefined ? {} : { maxCostUsd: options.maxCostUsd }),
      ...(options.maxTokens === undefined ? {} : { maxTokens: options.maxTokens }),
    })),
  });
}

test("a subagent wallet is carved from the parent's remaining budget and returns unspent", async () => {
  const parent = walletParent({
    id: "wallet-parent",
    childId: "wallet-child",
    names: ["delegate"],
    maxCostUsd: 5,
  });
  let call = 0;
  const result = await run(parent, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxUsd: 20 },
    streamFn: (selected) => {
      call++;
      const stream = createAssistantMessageEventStream();
      if (call === 1) {
        return pushMessage(stream, assistantMessage(
          selected,
          [{ type: "toolCall", id: "d1", name: "delegate", arguments: { task: "sub" } }],
          "toolUse",
          usage(500, 50),
        ));
      }
      return pushMessage(stream, assistantMessage(
        selected,
        [{ type: "text", text: "answer" }],
        "stop",
        usage(200, 20),
      ));
    },
  });
  assert.equal(result.stopReason, "complete");
  const { receipt } = result;
  assert.equal(receipt.subagents.length, 1);
  const nested = receipt.subagents[0];
  assert.equal(nested.denomination, "usd");
  assert.equal(nested.max, 5);
  // The parent's own meter counts the child's real spend, not its whole
  // wallet: what the child did not use came back.
  assert.equal(Math.abs(receipt.spent - receipt.totalEstimatedUsd) < 1e-10, true);
  assert.equal(receipt.spent < 0.01, true);
});

test("parallel subagent spawns cannot both be funded from the same remainder", async () => {
  const parent = walletParent({
    id: "wallet-race",
    childId: "wallet-race-child",
    names: ["delegate_one", "delegate_two"],
    maxCostUsd: 5,
  });
  let call = 0;
  const result = await run(parent, "go", {
    ensureRuntime: false,
    // Two 5.00 wallets cannot both come out of 8.00, and the carve happens
    // synchronously at spawn, so exactly one of the pair is funded.
    budget: { maxUsd: 8 },
    model: pricedFauxModel(),
    streamFn: (selected) => {
      call++;
      const stream = createAssistantMessageEventStream();
      if (call === 1) {
        return pushMessage(stream, assistantMessage(
          selected,
          [
            { type: "toolCall", id: "d1", name: "delegate_one", arguments: { task: "a" } },
            { type: "toolCall", id: "d2", name: "delegate_two", arguments: { task: "b" } },
          ],
          "toolUse",
          usage(500, 50),
        ));
      }
      return pushMessage(stream, assistantMessage(
        selected,
        [{ type: "text", text: "answer" }],
        "stop",
        usage(200, 20),
      ));
    },
  });
  assert.equal(result.receipt.subagents.length, 1);
  const errored = result.receipt.tools.filter((entry) => entry.errors > 0);
  assert.equal(errored.length, 1);
  assert.equal(result.receipt.spent <= 8, true);
});

test("a token-metered run cannot fund a subagent that declares no token wallet", async () => {
  const parent = walletParent({
    id: "wallet-denomination",
    childId: "wallet-denomination-child",
    names: ["delegate"],
    maxCostUsd: 5,
  });
  let call = 0;
  const result = await run(parent, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 100_000 },
    streamFn: (selected) => {
      call++;
      const stream = createAssistantMessageEventStream();
      if (call === 1) {
        return pushMessage(stream, assistantMessage(
          selected,
          [{ type: "toolCall", id: "d1", name: "delegate", arguments: { task: "sub" } }],
          "toolUse",
          usage(500, 50),
        ));
      }
      return pushMessage(stream, assistantMessage(
        selected,
        [{ type: "text", text: "answer" }],
        "stop",
        usage(200, 20),
      ));
    },
  });
  assert.equal(result.receipt.subagents.length, 0);
  assert.deepEqual(result.receipt.tools, [{ name: "delegate", calls: 1, errors: 1 }]);
});

test("a token wallet runs an unpriced child without inventing a USD cap", async () => {
  const child = agent({
    id: "wallet-token-unpriced-child",
    instructions: "Answer.",
    model: auto(),
    sandbox: "fixture",
  });
  const parent = agent({
    id: "wallet-token-unpriced-parent",
    instructions: "Delegate.",
    model: auto(),
    sandbox: "fixture",
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxTokens: 10_000,
    })],
  });
  const unpriced = upstreamFauxProvider({ provider: "anthropic" }).getModel();
  let call = 0;
  const result = await run(parent, "go", {
    ensureRuntime: false,
    model: unpriced,
    budget: { maxTokens: 50_000 },
    streamFn: (selected) => {
      call++;
      if (call === 1) {
        return pushMessage(createAssistantMessageEventStream(), assistantMessage(
          selected,
          [{ type: "toolCall", id: "d1", name: "delegate", arguments: { task: "sub" } }],
          "toolUse",
          usage(500, 50),
        ));
      }
      return pushMessage(createAssistantMessageEventStream(), assistantMessage(
        selected,
        [{ type: "text", text: call === 2 ? "child answer" : "parent answer" }],
        "stop",
        usage(200, 20),
      ));
    },
  });
  assert.equal(result.stopReason, "complete");
  assert.equal(result.receipt.subagents.length, 1);
  assert.equal(result.receipt.subagents[0].denomination, "tokens");
  assert.equal(result.receipt.subagents[0].unpriced, true);
  assert.equal(result.receipt.subagents[0].calls.length, 1);
});

test("a paid failed child remains on the parent's terminal receipt", async () => {
  const child = agent({
    id: "receipt-failed-child",
    instructions: "Fail after provider work.",
    model: auto(),
    sandbox: "fixture",
  });
  const parent = agent({
    id: "receipt-failed-parent",
    instructions: "Delegate.",
    model: auto(),
    sandbox: "fixture",
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxCostUsd: 5,
    })],
  });
  let call = 0;
  let failure;
  await assert.rejects(
    run(parent, "go", {
      ensureRuntime: false,
      model: pricedFauxModel(),
      streamFn: (selected) => {
        call++;
        if (call === 1) {
          return pushMessage(createAssistantMessageEventStream(), assistantMessage(
            selected,
            [{ type: "toolCall", id: "d1", name: "delegate", arguments: { task: "sub" } }],
            "toolUse",
            usage(500, 50),
          ));
        }
        return pushMessage(createAssistantMessageEventStream(), assistantMessage(
          selected,
          [],
          "error",
          usage(300, 30),
        ));
      },
    }),
    (error) => {
      failure = error;
      return error instanceof CavemanRunError;
    },
  );
  assert.equal(failure.receipt.subagents.length, 1);
  const failedChild = failure.receipt.subagents[0];
  assert.equal(failedChild.agentId, "receipt-failed-child");
  assert.equal(failedChild.calls.length, 1);
  assert.equal(failedChild.calls[0].inputTokens, 300);
  assert.equal(failedChild.calls[0].outputTokens, 30);
  assert.equal(failure.receipt.totalEstimatedUsd > failedChild.totalEstimatedUsd, true);
});

test("the subagent recursion depth cap defaults small and is bounded above", async () => {
  const leaf = agent({
    id: "depth-leaf",
    instructions: "Answer.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const build = (levels) => {
    let current = leaf;
    for (let index = 1; index <= levels; index++) {
      current = agent({
        id: `depth-${index}`,
        instructions: "Delegate.",
        model: auto(),
        sandbox: "fixture",
        tools: [subagent({ name: `level_${index}`, description: "Delegate.", agent: current })],
      });
    }
    return current;
  };
  const model = pricedFauxModel();
  const delegateThenAnswer = (names) => {
    let call = 0;
    return (selected) => {
      const stream = createAssistantMessageEventStream();
      const name = names[call];
      call++;
      if (name !== undefined) {
        return pushMessage(stream, assistantMessage(
          selected,
          [{ type: "toolCall", id: `d${call}`, name, arguments: { task: "deeper" } }],
          "toolUse",
          usage(100, 10),
        ));
      }
      return pushMessage(stream, assistantMessage(
        selected,
        [{ type: "text", text: "answer" }],
        "stop",
        usage(100, 10),
      ));
    };
  };
  // Root -> child -> grandchild is exactly the default depth of 2.
  const allowed = await run(build(2), "go", {
    ensureRuntime: false,
    model,
    streamFn: delegateThenAnswer(["level_2", "level_1"]),
  });
  assert.equal(allowed.receipt.subagents.length, 1);
  assert.equal(allowed.receipt.subagents[0].subagents.length, 1);
  // One level deeper needs an explicit opt-in, and the opt-in is bounded.
  const blocked = await run(build(3), "go", {
    ensureRuntime: false,
    model,
    streamFn: delegateThenAnswer(["level_3", "level_2", "level_1"]),
  });
  const deepest = blocked.receipt.subagents[0].subagents[0];
  assert.equal(deepest.subagents.length, 0);
  await assert.rejects(
    run(build(1), "go", {
      ensureRuntime: false,
      model,
      maxSubagentDepth: 9,
      streamFn: delegateThenAnswer([]),
    }),
    /cave_subagent_depth_limit_invalid/,
  );
});

test("revoking a parent meter revokes every wallet carved from it", () => {
  const parent = new BudgetMeter(normalizeRunBudget({ maxUsd: 1 }));
  const first = parent.carve(0.4);
  const second = parent.carve(0.4);
  assert.notEqual(first, undefined);
  assert.notEqual(second, undefined);
  // Two 0.4 wallets exhaust a 1.0 parent for a third.
  assert.equal(parent.carve(0.4), undefined);
  parent.revoke();
  assert.equal(first.child.revoked, true);
  assert.equal(second.child.revoked, true);
  assert.equal(first.child.reserve(0.01, 100), undefined);
  // A settled carve returns its unspent remainder to the parent.
  const fresh = new BudgetMeter(normalizeRunBudget({ maxUsd: 1 }));
  const carve = fresh.carve(0.4);
  const held = carve.child.reserve(0.4, 100);
  carve.child.settle(held, 0.05);
  fresh.settleCarve(carve);
  assert.equal(Math.abs(fresh.settled - 0.05) < 1e-12, true);
  assert.equal(Math.abs(fresh.remaining() - 0.95) < 1e-12, true);
});

test("a run is metered against its initial tranche, not its max", async () => {
  const defined = loopingAgent({ id: "tranche-initial", output: output({ maxTokens: 2_000 }) });
  const observed = { calls: [], inputTokens: 400, outputTokens: 100 };
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 500_000, initialTokens: 8_000 },
    streamFn: endlessToolProvider(observed),
  });
  assert.equal(result.stopReason, "budget_exhausted");
  assert.equal(result.receipt.max, 500_000);
  assert.equal(result.receipt.released, 8_000);
  assert.equal(result.receipt.spent <= 8_000, true);
  assert.deepEqual(result.receipt.tranches, []);
});

test("a developer checkpoint releases a tranche and the receipt records it", async () => {
  const controller = createBudgetController();
  let checkpoints = 0;
  const defined = agent({
    id: "tranche-release",
    instructions: "Keep calling peek.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
    output: output({ maxTokens: 2_000 }),
    tools: [tool({
      name: "peek",
      description: "Return a fixed value.",
      input: schema.object({}),
      effect: "read",
      execute: () => {
        // A deterministic, developer-defined checkpoint. No model decides this.
        checkpoints++;
        if (checkpoints === 1) controller.releaseBudget(8_000, "first result in hand");
        return "peeked";
      },
    })],
  });
  const observed = { calls: [], inputTokens: 400, outputTokens: 100 };
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 500_000, initialTokens: 8_000 },
    budgetController: controller,
    streamFn: endlessToolProvider(observed),
  });
  assert.equal(result.stopReason, "budget_exhausted");
  assert.equal(result.receipt.released, 16_000);
  assert.equal(result.receipt.spent <= 16_000, true);
  assert.equal(result.receipt.tranches.length, 1);
  assert.equal(result.receipt.tranches[0].amount, 8_000);
  assert.equal(result.receipt.tranches[0].reason, "first result in hand");
  assert.equal(result.receipt.tranches[0].atCall >= 1, true);
  // The handle is inert once the run it belonged to is over.
  assert.throws(() => controller.releaseBudget(1, "after"), /cave_budget_controller_unbound/);
});

test("releasing past max throws at the release site", async () => {
  const controller = createBudgetController();
  let thrown;
  const defined = agent({
    id: "tranche-overflow",
    instructions: "Reply.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
    tools: [tool({
      name: "peek",
      description: "Return a fixed value.",
      input: schema.object({}),
      effect: "read",
      execute: () => {
        try {
          controller.releaseBudget(9_000, "more than the contract allows");
        } catch (error) {
          thrown = error;
        }
        return "peeked";
      },
    })],
  });
  let call = 0;
  await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 10_000, initialTokens: 2_000 },
    budgetController: controller,
    streamFn: (selected) => {
      call++;
      const stream = createAssistantMessageEventStream();
      if (call === 1) {
        return pushMessage(stream, assistantMessage(
          selected,
          [{ type: "toolCall", id: "p1", name: "peek", arguments: {} }],
          "toolUse",
          usage(100, 10),
        ));
      }
      return pushMessage(stream, assistantMessage(
        selected,
        [{ type: "text", text: "done" }],
        "stop",
        usage(100, 10),
      ));
    },
  });
  assert.match(String(thrown?.message), /cave_budget_release_exceeds_max/);
});

test("a budget controller is bound to exactly one live run and needs a budget", async () => {
  const defined = loopingAgent({ id: "tranche-binding" });
  const controller = createBudgetController();
  assert.throws(() => controller.releaseBudget(1, "unbound"), /cave_budget_controller_unbound/);
  await assert.rejects(
    run(defined, "go", {
      ensureRuntime: false,
      model: pricedFauxModel(),
      budgetController: controller,
      streamFn: () => {
        throw new Error("must not reach the provider");
      },
    }),
    /cave_budget_controller_without_budget/,
  );
  const observed = { calls: [], inputTokens: 100, outputTokens: 20 };
  const first = run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 6_000 },
    budgetController: controller,
    streamFn: endlessToolProvider(observed),
  });
  await assert.rejects(
    run(defined, "go", {
      ensureRuntime: false,
      model: pricedFauxModel(),
      budget: { maxTokens: 6_000 },
      budgetController: controller,
      streamFn: endlessToolProvider({ calls: [], inputTokens: 100, outputTokens: 20 }),
    }),
    /cave_budget_controller_in_use/,
  );
  await first;
});

test("the escalation hook tops up a tranche or takes the default stop", async () => {
  const defined = loopingAgent({ id: "escalation-release", output: output({ maxTokens: 2_000 }) });
  const observed = { calls: [], inputTokens: 400, outputTokens: 100 };
  const seen = [];
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 500_000, initialTokens: 8_000 },
    onBudgetExhausted: async (context) => {
      seen.push(context);
      // Escalate once, then let the run take the default outcome.
      if (seen.length === 1) return { release: 8_000, reason: "operator approved" };
      return "stop";
    },
    streamFn: endlessToolProvider(observed),
  });
  assert.equal(result.stopReason, "budget_exhausted");
  assert.equal(seen.length, 2);
  assert.equal(seen[0].denomination, "tokens");
  assert.equal(seen[0].max, 500_000);
  assert.equal(seen[0].released, 8_000);
  assert.equal(seen[0].releasable, 492_000);
  assert.equal(seen[0].calls > 0, true);
  assert.equal(result.receipt.released, 16_000);
  assert.equal(result.receipt.spent <= 16_000, true);
  assert.deepEqual(
    result.receipt.tranches.map((entry) => entry.reason),
    ["operator approved"],
  );
});

test("an escalation cannot cross max, and a malformed answer fails closed", async () => {
  const defined = loopingAgent({ id: "escalation-bounds", output: output({ maxTokens: 2_000 }) });
  const observed = { calls: [], inputTokens: 400, outputTokens: 100 };
  await assert.rejects(
    run(defined, "go", {
      ensureRuntime: false,
      model: pricedFauxModel(),
      budget: { maxTokens: 10_000, initialTokens: 8_000 },
      onBudgetExhausted: async () => ({ release: 50_000, reason: "past the contract" }),
      streamFn: endlessToolProvider(observed),
    }),
    /cave_budget_release_exceeds_max/,
  );
  await assert.rejects(
    run(defined, "go", {
      ensureRuntime: false,
      model: pricedFauxModel(),
      budget: { maxTokens: 10_000, initialTokens: 8_000 },
      onBudgetExhausted: async () => "carry on",
      streamFn: endlessToolProvider({ calls: [], inputTokens: 400, outputTokens: 100 }),
    }),
    /cave_budget_escalation_result_invalid/,
  );
});

test("the default escalation is the plain stop, and the hook is never called mid-tool", async () => {
  let insideTool = false;
  let calledInsideTool = false;
  const defined = agent({
    id: "escalation-default",
    instructions: "Keep calling peek.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
    output: output({ maxTokens: 2_000 }),
    tools: [tool({
      name: "peek",
      description: "Return a fixed value.",
      input: schema.object({}),
      effect: "read",
      execute: async () => {
        insideTool = true;
        await new Promise((settle) => setTimeout(settle, 1));
        insideTool = false;
        return "peeked";
      },
    })],
  });
  const observed = { calls: [], inputTokens: 400, outputTokens: 100 };
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel(),
    budget: { maxTokens: 8_000 },
    onBudgetExhausted: async () => {
      if (insideTool) calledInsideTool = true;
      return "stop";
    },
    streamFn: endlessToolProvider(observed),
  });
  assert.equal(result.stopReason, "budget_exhausted");
  assert.equal(calledInsideTool, false);
  assert.equal(result.toolCalls.length, observed.calls.length);
});

function mulberry32(seed) {
  let state = seed >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let value = Math.imul(state ^ (state >>> 15), 1 | state);
    value = (value + Math.imul(value ^ (value >>> 7), 61 | value)) ^ value;
    return ((value ^ (value >>> 14)) >>> 0) / 4294967296;
  };
}
