import { test } from "node:test";
import assert from "node:assert/strict";
import {
  SUMMARY_SCHEMA_VERSION,
  agent,
  run,
  schema,
  tool,
} from "../dist/index.js";
import {
  evictMessage,
  messagesTokens,
  normalizeCompaction,
  parseContextSummary,
  pinnedContentSurvives,
  planCompaction,
  summarizationInstruction,
} from "../dist/compaction.js";
import { fauxProvider as upstreamFauxProvider } from "@earendil-works/pi-ai/providers/faux";
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";

const PRICED_MODEL = "claude-haiku-4-5";

/**
 * A small output cap keeps the reserve arithmetic legible: the ceiling
 * saturates quickly, so the budget binds after a knowable number of turns
 * rather than after hundreds.
 */
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

/**
 * The summarizer is reserved COLD, so its input ceiling is priced at fresh-input
 * rates. Cheap-class summarizers remain useful; own-model reachability gets a
 * separate regression because it is the default contract.
 */
const EXPENSIVE_WORKING_MODEL = "claude-opus-4-1";

function usage(input, outputTokens, cacheRead = 0) {
  return {
    input,
    output: outputTokens,
    cacheRead,
    cacheWrite: 0,
    reasoning: 0,
    totalTokens: input + outputTokens + cacheRead,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
  };
}

/** Provider-reported warm prefix — the evidence the affordability model needs. */
function warmUsage(input, outputTokens, cacheRead) {
  return usage(input, outputTokens, cacheRead);
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

function validSummaryJSON() {
  return JSON.stringify({
    schema_version: SUMMARY_SCHEMA_VERSION,
    objective: "Answer the user's question.",
    constraints_restated: ["reply in one word"],
    decisions: [{ decision: "polled the queue", why: "the job was pending" }],
    artifacts: [{ path: "queue", change: "observed" }],
    facts: ["job id 7"],
    state: { completed: ["polled"], active: ["waiting"], blocked: [] },
    next: ["poll again"],
    citations: [{ segment_id: "runtime.tool_result.4", digest: "a".repeat(64), what: "poll output" }],
    lookup_hints: ["queue status"],
  });
}

/** ~2,000 tokens of filler per tool result, so a run accumulates real context. */
const BULK = "x".repeat(8_000);

function isSummarizerRequest(context) {
  const last = context.messages.at(-1);
  return typeof last?.content === "string" &&
    last.content.includes("Reply with a single JSON object");
}

function bulkyAgent(id) {
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

test("a compaction rewrites the context, is metered, and lands on the receipt", async () => {
  const defined = bulkyAgent("compaction-summarize");
  let working = 0;
  let summarizerSawTools;
  let summarizerSawSystemPrompt;
  let summarizerMaxTokens;
  let workingSystemPrompt;
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel({ id: EXPENSIVE_WORKING_MODEL }),
    budget: {
      maxUsd: 4,
      onExhausted: "compact",
      compaction: {
        minYieldTokens: 1_000,
        headroomCalls: 1,
        keepRecentTokens: 2_000,
        summarizerModel: pricedFauxModel(),
      },
    },
    streamFn: (selected, context, streamOptions) => {
      if (isSummarizerRequest(context)) {
        // The summarization request is built like a working call, so it can
        // extend the same cached prefix instead of starting a second one.
        summarizerSawTools = context.tools?.length ?? 0;
        summarizerSawSystemPrompt = context.systemPrompt;
        summarizerMaxTokens = streamOptions.maxTokens;
        return pushMessage(
          selected,
          [{ type: "text", text: validSummaryJSON() }],
          "stop",
          usage(5_000, 500),
        );
      }
      working++;
      workingSystemPrompt = context.systemPrompt;
      if (working > 55) {
        return pushMessage(selected, [{ type: "text", text: "done" }], "stop", usage(100, 10));
      }
      return pushMessage(
        selected,
        [
          // The bulk is in the model's own output, which no free eviction can
          // reach — only a summarizer compresses it.
          { type: "text", text: `${BULK}-${working}` },
          {
            type: "toolCall",
            id: `poll-${working}`,
            name: "poll",
            arguments: { key: `job-${working}` },
          },
        ],
        "toolUse",
        warmUsage(500, 800, 8_000),
      );
    },
  });
  const summarized = result.receipt.compactions.find((entry) => entry.tier === "summarized");
  assert.notEqual(summarized, undefined);
  assert.equal(summarized.summarySchemaVersion, SUMMARY_SCHEMA_VERSION);
  assert.equal(summarized.postTokens < summarized.preTokens, true);
  assert.equal(summarized.pinnedSegmentIds.length > 0, true);
  // Real metered cost and modeled effect are separate fields with separate bases.
  assert.equal(summarized.meteredBasis, "measured");
  assert.equal(summarized.modeledBasis, "modeled");
  assert.equal(summarized.meteredCost > 0, true);
  assert.equal(Number.isFinite(summarized.modeledNetTokens), true);
  // The observed cache state is recorded as evidence; the reserve prices the
  // summarizer cold regardless, because its prefix diverges from the working
  // call's at the first rewritten message.
  assert.equal(["warm", "cold", "unknown"].includes(summarized.cacheState), true);
  // The compaction spent inside the cap, like everything else.
  assert.equal(result.receipt.spent <= result.receipt.max, true);
  // Same system prompt and same tools as a working call: shared prefix.
  assert.equal(summarizerSawSystemPrompt, workingSystemPrompt);
  assert.equal(summarizerSawTools > 0, true);
  assert.equal(summarizerMaxTokens, 2_048);
  // The summarizer call is in the receipt like any other provider call.
  assert.equal(result.receipt.calls.some((entry) => entry.outputTokens === 500), true);
  // One compaction by default, however long the run goes on.
  assert.equal(result.receipt.compactions.length <= 1, true);
});

test("default own-model compaction is reachable before exhaustion", async () => {
  let working = 0;
  let summarizerCalls = 0;
  const selected = pricedFauxModel();
  const result = await run(bulkyAgent("compaction-own-model"), "go", {
    ensureRuntime: false,
    model: selected,
    budget: {
      maxTokens: 200_000,
      onExhausted: "compact",
      compaction: {
        minYieldTokens: 1_000,
        keepRecentTokens: 2_000,
      },
    },
    streamFn: (model, context) => {
      if (isSummarizerRequest(context)) {
        summarizerCalls++;
        assert.equal(model.id, selected.id);
        return pushMessage(
          model,
          [{ type: "text", text: validSummaryJSON() }],
          "stop",
          usage(5_000, 500),
        );
      }
      working++;
      if (working > 20) {
        return pushMessage(model, [{ type: "text", text: "done" }], "stop", usage(100, 10));
      }
      return pushMessage(
        model,
        [
          { type: "text", text: `${BULK}-${working}` },
          {
            type: "toolCall",
            id: `own-model-poll-${working}`,
            name: "poll",
            arguments: { key: `own-${working}` },
          },
        ],
        "toolUse",
        warmUsage(500, 800, 8_000),
      );
    },
  });
  assert.equal(summarizerCalls, 1, JSON.stringify({
    message: "same-model default never entered compaction",
    working,
    stopReason: result.stopReason,
    spent: result.receipt.spent,
    remaining: result.receipt.released - result.receipt.spent,
    calls: result.receipt.calls.length,
    compactions: result.receipt.compactions,
  }));
  const compacted = result.receipt.compactions.find((entry) => entry.tier === "summarized");
  assert.notEqual(compacted, undefined);
  assert.equal(compacted.workingCallsAfter >= 1, true);
  assert.equal(result.receipt.spent <= result.receipt.max, true);
});

test("onExhausted stop skips the rung a compact run would have taken", async () => {
  // Both arms run the identical scenario on the expensive-working /
  // cheap-summarizer pair, so the compact arm genuinely compacts. Without the
  // control the assertion cannot tell "the toggle worked" from "nothing would
  // have compacted anyway".
  const compacting = await compactionRun("compaction-toggle-on", {
    options: { budgetOverrides: { onExhausted: "compact" } },
  });
  assert.equal(compacting.summarizerCalls, 1, "the control arm did not compact");
  assert.equal(
    compacting.result.receipt.compactions.some((entry) => entry.tier === "summarized"),
    true,
  );

  const stopping = await compactionRun("compaction-toggle-off", {
    options: { budgetOverrides: { onExhausted: "stop" } },
  });
  assert.equal(stopping.summarizerCalls, 0);
  assert.deepEqual(stopping.result.receipt.compactions, []);
  assert.equal(stopping.result.stopReason, "budget_exhausted");
  assert.equal(stopping.result.receipt.spent <= stopping.result.receipt.max, true);
});

test("an unvalidatable summary is discarded and the run falls through", async () => {
  const defined = bulkyAgent("compaction-invalid-summary");
  let working = 0;
  let summarizerCalls = 0;
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedFauxModel({ id: EXPENSIVE_WORKING_MODEL }),
    budget: {
      maxUsd: 4,
      onExhausted: "compact",
      compaction: {
        minYieldTokens: 1_000,
        headroomCalls: 1,
        keepRecentTokens: 2_000,
        summarizerModel: pricedFauxModel(),
      },
    },
    streamFn: (selected, context) => {
      if (isSummarizerRequest(context)) {
        summarizerCalls++;
        // Structurally wrong: no objective, wrong schema version.
        return pushMessage(
          selected,
          [{ type: "text", text: JSON.stringify({ schema_version: 99, note: "trust me" }) }],
          "stop",
          usage(5_000, 500),
        );
      }
      working++;
      return pushMessage(
        selected,
        [
          // The bulk is in the model's own output, which no free eviction can
          // reach — only a summarizer compresses it.
          { type: "text", text: `${BULK}-${working}` },
          {
            type: "toolCall",
            id: `poll-${working}`,
            name: "poll",
            arguments: { key: `job-${working}` },
          },
        ],
        "toolUse",
        warmUsage(500, 800, 8_000),
      );
    },
  });
  assert.equal(summarizerCalls > 0, true);
  assert.equal(result.stopReason, "budget_exhausted");
  assert.equal(
    result.receipt.compactions.some((entry) => entry.tier === "summarized"),
    false,
  );
  // The discarded summarizer call is still real spend, and it is still counted.
  assert.equal(result.receipt.spent <= result.receipt.max, true);
  assert.equal(result.receipt.calls.some((entry) => entry.outputTokens === 500), true);
});

test("the summary schema fails closed on anything malformed", () => {
  assert.equal(parseContextSummary("not json"), undefined);
  assert.equal(parseContextSummary(JSON.stringify({ schema_version: 2 })), undefined);
  assert.equal(
    parseContextSummary(JSON.stringify({ schema_version: SUMMARY_SCHEMA_VERSION })),
    undefined,
  );
  const missingState = JSON.parse(validSummaryJSON());
  delete missingState.state;
  assert.equal(parseContextSummary(JSON.stringify(missingState)), undefined);
  const wrongDecision = JSON.parse(validSummaryJSON());
  wrongDecision.decisions = [{ decision: "only half" }];
  assert.equal(parseContextSummary(JSON.stringify(wrongDecision)), undefined);
  const valid = parseContextSummary(validSummaryJSON());
  assert.equal(valid.schemaVersion, SUMMARY_SCHEMA_VERSION);
  assert.equal(valid.objective, "Answer the user's question.");
  assert.deepEqual(valid.state.completed, ["polled"]);
  assert.equal(valid.citations[0].segmentId, "runtime.tool_result.4");
});

test("the plan pins user intent, keeps the tail self-contained, and elides stale tool output", () => {
  const config = normalizeCompaction({ keepRecentTokens: 1, pinnedUserTokens: 1_000 });
  const messages = [
    { role: "user", content: "the task", timestamp: 1 },
    {
      role: "assistant",
      content: [{ type: "toolCall", id: "t1", name: "read", arguments: {} }],
      api: "a", provider: "anthropic", model: PRICED_MODEL,
      usage: usage(1, 1), stopReason: "toolUse", timestamp: 2,
    },
    { role: "toolResult", toolCallId: "t1", toolName: "read", content: [{ type: "text", text: "old output" }] },
    {
      role: "assistant",
      content: [{ type: "toolCall", id: "t2", name: "read", arguments: {} }],
      api: "a", provider: "anthropic", model: PRICED_MODEL,
      usage: usage(1, 1), stopReason: "toolUse", timestamp: 3,
    },
    { role: "toolResult", toolCallId: "t2", toolName: "read", content: [{ type: "text", text: "fresh output" }] },
  ];
  const plan = planCompaction(messages, config);
  assert.deepEqual(plan.pinned, [0]);
  // The stale first result is reversible, so it can become a citation for free.
  assert.deepEqual(plan.evictable, [2]);
  // Whatever the tail keeps, it never keeps a tool result without its caller.
  const kept = new Set(plan.recent);
  for (const index of kept) {
    const message = messages[index];
    if (message.role !== "toolResult") continue;
    const owner = messages.findIndex((item) =>
      item.role === "assistant" &&
      Array.isArray(item.content) &&
      item.content.some((part) => part.type === "toolCall" && part.id === message.toolCallId));
    assert.equal(kept.has(owner), true);
  }
  const evicted = evictMessage(messages[2], 2);
  assert.match(evicted.content[0].text, /cave_elided/);
  assert.match(evicted.content[0].text, /sha256=[0-9a-f]{64}/);
  assert.equal(messagesTokens([evicted]) < messagesTokens([messages[2]]) + 100, true);
});

test("the compaction instruction carries the anti-drift and untrusted-content rules", () => {
  const fresh = summarizationInstruction(undefined);
  assert.match(fresh, /Reply with a single JSON object/);
  assert.match(fresh, /Keep completed work separate/);
  assert.match(fresh, /untrusted/);
  const update = summarizationInstruction(parseContextSummary(validSummaryJSON()));
  // A second compaction updates the previous summary rather than re-summarizing.
  assert.match(update, /Update this previous summary/);
  assert.match(update, /Answer the user's question/);
});

test("the compaction rung is closed once the run has decided to stop", async () => {
  // A tripped breaker, an expired deadline, and a turn that asked for no tools
  // all mean no working call follows. Paying a summarizer at any of those
  // points buys a context nothing will read.
  // Self-calibrating: the control run reports the turn at which the reserve
  // check fails, and the gated run is then arranged so the breaker trips on
  // exactly that turn. Otherwise the two events never coincide and the test
  // would prove nothing.
  const control = await compactionRun("compaction-closed-control", {});
  assert.equal(control.summarizerCalls, 1);
  assert.equal(control.summarizerAtTurn > 2, true);

  const gated = await compactionRun("compaction-closed-breaker", {
    // Identical conclusions from one turn before the trigger, so the
    // no-progress window closes on the trigger turn itself.
    repeatFromTurn: control.summarizerAtTurn - 1,
    options: { breakers: { noProgressTurns: 2 } },
  });
  assert.equal(gated.summarizerCalls, 0, "a tripped breaker paid for a summarization");
  assert.deepEqual(gated.result.receipt.compactions, []);
  assert.equal(gated.result.stopReason, "no_progress");

  // An expired deadline closes the rung for the rest of the run.
  const expired = await compactionRun("compaction-closed-deadline", {
    options: { deadlineMs: 1 },
    slowFirstCall: true,
  });
  assert.equal(expired.summarizerCalls, 0, "an expired deadline paid for a summarization");
  assert.deepEqual(expired.result.receipt.compactions, []);
  assert.equal(expired.result.stopReason, "deadline");
});

test("a terminal turn never triggers a compaction nothing can use", async () => {
  let summarizerCalls = 0;
  let working = 0;
  const result = await run(bulkyAgent("compaction-terminal-turn"), "go", {
    ensureRuntime: false,
    model: pricedFauxModel({ id: EXPENSIVE_WORKING_MODEL }),
    budget: {
      maxUsd: 4,
      onExhausted: "compact",
      compaction: {
        minYieldTokens: 1_000,
        headroomCalls: 1,
        keepRecentTokens: 2_000,
        summarizerModel: pricedFauxModel(),
      },
    },
    streamFn: (selected, context) => {
      if (isSummarizerRequest(context)) {
        summarizerCalls++;
        return pushMessage(
          selected,
          [{ type: "text", text: validSummaryJSON() }],
          "stop",
          usage(5_000, 500),
        );
      }
      working++;
      // A context already large enough to trigger the reserve check, delivered
      // by a turn that asks for nothing further.
      return pushMessage(
        selected,
        [{ type: "text", text: `${BULK.repeat(20)}-${working}` }],
        "stop",
        warmUsage(500, 800, 8_000),
      );
    },
  });
  assert.equal(working, 1);
  assert.equal(summarizerCalls, 0);
  assert.deepEqual(result.receipt.compactions, []);
  assert.equal(result.stopReason, "complete");
});

test("the summarizer carries the same gateway credential as a working call", async () => {
  const priorKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "test-cave-key";
  const seen = { working: undefined, summarizer: undefined };
  try {
    await compactionRun("compaction-gateway-headers", {
      options: {
        // A reachable gateway is asserted rather than probed: the run is told
        // it is routed, so every call must carry the account headers.
        gatewayURL: "http://127.0.0.1:8787",
        cave: "auto",
      },
      captureHeaders: seen,
    });
  } finally {
    if (priorKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = priorKey;
  }
  // Whichever route the run resolved, a compaction is not a special case for
  // credentials: off the gateway neither call carries an x-cave-* header, and
  // on it the summarizer carries the same account-linked set as a working call.
  const workingCave = caveHeaders(seen.working);
  const summarizerCave = caveHeaders(seen.summarizer);
  assert.equal(
    Object.keys(workingCave).length === 0,
    Object.keys(summarizerCave).length === 0,
    "one call carried gateway headers and the other did not",
  );
  for (const name of ["x-cave-api-key", "x-cave-agent", "x-cave-session", "x-cave-workflow"]) {
    assert.equal(summarizerCave[name], workingCave[name], name);
  }
  if (Object.keys(summarizerCave).length > 0) {
    // A compaction is a framework-local rewrite, never a gateway transform.
    assert.equal(summarizerCave["x-cave-transforms"], "caveman.pass-through.v1");
    assert.equal(summarizerCave["x-cave-transform-location"], "local");
  }
});

function caveHeaders(headers) {
  return Object.fromEntries(
    Object.entries(headers ?? {}).filter(([name]) => name.toLowerCase().startsWith("x-cave-")),
  );
}

test("the pinned-survival check reads the rewrite's content, and can fail", () => {
  const original = [
    { role: "user", content: "the task, stated once", timestamp: 1 },
    { role: "user", content: "and a constraint", timestamp: 2 },
    { role: "user", content: "filler", timestamp: 3 },
  ];
  const pinned = [0, 1];
  // A rewrite carrying the pinned text passes even when the objects are new —
  // content is what matters, not identity.
  assert.equal(
    pinnedContentSurvives(original, pinned, [
      { role: "user", content: "the task, stated once", timestamp: 9 },
      { role: "user", content: "<cave-context-summary>{}</cave-context-summary>", timestamp: 9 },
      { role: "user", content: "and a constraint", timestamp: 9 },
    ]),
    true,
  );
  // Dropping one fails. The old identity comparison could not have caught this.
  assert.equal(
    pinnedContentSurvives(original, pinned, [
      { role: "user", content: "the task, stated once", timestamp: 9 },
      { role: "user", content: "<cave-context-summary>{}</cave-context-summary>", timestamp: 9 },
    ]),
    false,
  );
  // So does altering one, however slightly.
  assert.equal(
    pinnedContentSurvives(original, pinned, [
      { role: "user", content: "the task, stated once", timestamp: 9 },
      { role: "user", content: "and a constraint (paraphrased)", timestamp: 9 },
    ]),
    false,
  );
});

test("a precondition decline does not burn the attempt budget", async () => {
  // maxCompactions is 1. A run whose first trigger cannot afford a summarizer
  // must still be able to compact later, when eviction or a smaller context
  // brings it back into reach — only a PAID attempt counts.
  const paid = await compactionRun("compaction-attempt-budget", {});
  assert.equal(paid.summarizerCalls, 1);
  // The declines that happened before the paid attempt did not consume it.
  assert.equal(paid.result.receipt.compactions.length, 1);
});

/** One Opus-working / Haiku-summarizer run, reporting where the rung fired. */
async function compactionRun(id, setup) {
  const observed = { summarizerCalls: 0, summarizerAtTurn: 0, working: 0 };
  const result = await run(bulkyAgent(id), "go", {
    ensureRuntime: false,
    model: pricedFauxModel({ id: EXPENSIVE_WORKING_MODEL }),
    budget: {
      maxUsd: 4,
      onExhausted: "compact",
      compaction: {
        minYieldTokens: 1_000,
        headroomCalls: 1,
        keepRecentTokens: 2_000,
        summarizerModel: pricedFauxModel(),
      },
      ...(setup.options?.budgetOverrides ?? {}),
    },
    ...(setup.options ?? {}),
    streamFn: (selected, context, streamOptions) => {
      if (isSummarizerRequest(context)) {
        observed.summarizerCalls++;
        observed.summarizerAtTurn = observed.working;
        setup.onSummarizer?.();
        if (setup.captureHeaders !== undefined) {
          setup.captureHeaders.summarizer = streamOptions?.headers;
        }
        return pushMessage(
          selected,
          [{ type: "text", text: validSummaryJSON() }],
          "stop",
          usage(5_000, 500),
        );
      }
      observed.working++;
      if (setup.captureHeaders !== undefined) {
        setup.captureHeaders.working = streamOptions?.headers;
      }
      if (setup.slowFirstCall === true && observed.working === 1) {
        const until = performance.now() + 5;
        while (performance.now() < until) { /* deliberate busy wait */ }
      }
      if (observed.working > 55) {
        return pushMessage(selected, [{ type: "text", text: "done" }], "stop", usage(100, 10));
      }
      const repeating = setup.repeatFromTurn !== undefined &&
        observed.working >= setup.repeatFromTurn;
      const marker = repeating ? "same" : String(observed.working);
      return pushMessage(
        selected,
        [
          { type: "text", text: `${BULK}-${marker}` },
          {
            type: "toolCall",
            id: `poll-${observed.working}`,
            name: "poll",
            arguments: { key: marker },
          },
        ],
        "toolUse",
        warmUsage(500, 800, 8_000),
      );
    },
  });
  return { result, ...observed };
}

test("compaction options fail closed on nonsense", () => {
  assert.throws(() => normalizeCompaction({ maxCompactions: 0 }), /cave_compaction_option_invalid/);
  assert.throws(() => normalizeCompaction({ summaryMaxTokens: -1 }), /cave_compaction_option_invalid/);
  assert.throws(() => normalizeCompaction({ headroomCalls: 1.5 }), /cave_compaction_option_invalid/);
  const defaults = normalizeCompaction();
  assert.equal(defaults.maxCompactions, 1);
  assert.equal(defaults.summaryMaxTokens, 2_048);
  assert.equal(defaults.minYieldTokens, 20_000);
  assert.equal(defaults.headroomCalls, 3);
});
