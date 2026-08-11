import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import {
  fauxAssistantMessage as upstreamFauxAssistantMessage,
  fauxProvider as upstreamFauxProvider,
  fauxToolCall,
} from "@earendil-works/pi-ai/providers/faux";
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";
import {
  CODING_TOOL_OUTPUT_CAPS,
  OBSERVE_ONLY_BANNER,
  RECOVERABLE_CODING_TRANSFORMS,
  classifyTurnFailure,
  createCodingAgent,
  defaultCodingPlan,
  formatRecoveryProof,
  formatSessionBill,
  formatTurnBill,
  proveRecovery,
  runCodingSession,
  runCodingTurn,
  sessionBill,
  startCodingSession,
} from "../dist/code.js";

const FAKE_ENGINE = resolve(import.meta.dirname, "fixtures/fake-engine.mjs");

function fauxAnthropic() {
  return fauxNamedProvider("anthropic");
}

function fauxNamedProvider(provider) {
  const handle = upstreamFauxProvider({ provider });
  const streamSimple = handle.provider.streamSimple.bind(handle.provider);
  return {
    ...handle,
    provider: {
      ...handle.provider,
      streamSimple: (...args) => withReportedReasoning(streamSimple(...args)),
    },
  };
}

function withReportedReasoning(source) {
  const output = createAssistantMessageEventStream();
  queueMicrotask(async () => {
    for await (const event of source) {
      const partial = event.partial === undefined
        ? {}
        : { partial: zeroReasoning(event.partial) };
      if (event.type === "done") {
        output.push({ ...event, ...partial, message: zeroReasoning(event.message) });
      } else if (event.type === "error") {
        output.push({ ...event, ...partial, error: zeroReasoning(event.error) });
      } else {
        output.push({ ...event, ...partial });
      }
    }
  });
  return output;
}

function zeroReasoning(message) {
  return { ...message, usage: { ...message.usage, reasoning: 0 } };
}

function fauxAssistantMessage(...args) {
  const message = upstreamFauxAssistantMessage(...args);
  return { ...message, usage: { ...message.usage, reasoning: message.usage.reasoning ?? 0 } };
}

function payloadStreamFn(faux, seen = []) {
  return (selected, context, options) => {
    seen.push(structuredClone(context.messages));
    options.onPayload({
      system: context.systemPrompt,
      tools: context.tools.map((item) => ({
        name: item.name,
        description: item.description,
        input_schema: item.parameters,
      })),
      messages: context.messages,
    }, selected);
    return faux.provider.streamSimple(selected, context, { ...options, onPayload: undefined });
  };
}

async function withWorkspace(body) {
  const workspace = await mkdtemp(resolve(tmpdir(), "caveman-code-ws-"));
  const store = await mkdtemp(resolve(tmpdir(), "caveman-code-engine-"));
  const previous = process.env.CAVE_FAKE_ENGINE_STORE;
  process.env.CAVE_FAKE_ENGINE_STORE = store;
  try {
    return await body(workspace);
  } finally {
    if (previous === undefined) delete process.env.CAVE_FAKE_ENGINE_STORE;
    else process.env.CAVE_FAKE_ENGINE_STORE = previous;
    await rm(workspace, { recursive: true, force: true });
    await rm(store, { recursive: true, force: true });
  }
}

test("default coding plan routes only recoverable transforms over the live zone", () => {
  const plan = defaultCodingPlan("anthropic/claude-sonnet-4-6", "caveman-code");
  const kinds = plan.segment_routes.map((route) => route.segment_kind).sort();
  assert.deepEqual(kinds, ["history", "tool_result"]);
  for (const route of plan.segment_routes) {
    assert.equal(
      RECOVERABLE_CODING_TRANSFORMS.includes(route.transform_id),
      true,
      `${route.transform_id} is not a recoverable coding transform`,
    );
    assert.equal(route.fallback, "original");
    assert.equal(route.transform_id.includes("toon"), false);
    assert.equal(route.transform_id.includes("pixel"), false);
    assert.equal(route.segment_id, undefined, "an unqualified route per kind avoids ambiguity");
  }
  // One route per dynamic kind: two routes matching one runtime segment collapse
  // into dynamic_route_ambiguous and the segment silently passes through.
  assert.equal(new Set(kinds).size, kinds.length);
  assert.deepEqual(plan.recovery.tools, ["cave_retrieve"]);
  assert.equal(plan.reasoning, "none");
  // 1 + ceil(reserve / 256) model calls per turn.
  assert.equal(1 + Math.ceil(plan.budgets.retry_cascade_reserve / 256), 64);
  assert.equal(plan.budgets.history >= 1_000_000, true);
  assert.equal(plan.budgets.results_artifacts >= 1_000_000, true);
});

test("created coding agent is host sandboxed, unlocked, and capped before compression", () => {
  const codingAgent = createCodingAgent({ workspace: tmpdir(), model: "anthropic/faux-1" });
  assert.equal(codingAgent.definition.sandbox, "host");
  assert.equal(codingAgent.definition.reasoning, "off");
  assert.deepEqual(
    codingAgent.definition.tools.map((item) => item.name).sort(),
    ["bash", "edit_file", "grep", "read_file"],
  );
  assert.deepEqual(
    codingAgent.definition.tools.map((item) => [item.name, item.effect]).sort(),
    [["bash", "external"], ["edit_file", "write"], ["grep", "read"], ["read_file", "read"]],
  );
  // Every cap sits under the runtime's 32 KiB inline tool-result ceiling so an
  // observe-only session with no engine present still works.
  for (const [name, cap] of Object.entries(CODING_TOOL_OUTPUT_CAPS)) {
    assert.equal(cap < 32_768, true, `${name} cap must stay under the inline ceiling`);
  }
  assert.equal(codingAgent.plan.model, "anthropic/faux-1");
});

test("cold machine starts observe-only and says so loudly", async () => {
  const previous = process.env.CAVEMAN_CLI_BIN;
  process.env.CAVEMAN_CLI_BIN = resolve(tmpdir(), "caveman-absent-cli-binary");
  try {
    const codingAgent = createCodingAgent({ workspace: tmpdir(), model: "anthropic/faux-1" });
    const notices = [];
    const session = await startCodingSession(codingAgent, {
      fetch: async () => { throw new Error("gateway unreachable"); },
      onNotice: (line) => notices.push(line),
    });
    assert.equal(session.mode, "observe-only");
    assert.deepEqual(notices, [OBSERVE_ONLY_BANNER]);
    assert.deepEqual(session.notices, [OBSERVE_ONLY_BANNER]);
    assert.match(OBSERVE_ONLY_BANNER, /caveman start/);
    assert.match(OBSERVE_ONLY_BANNER, /OBSERVE-ONLY/);
  } finally {
    if (previous === undefined) delete process.env.CAVEMAN_CLI_BIN;
    else process.env.CAVEMAN_CLI_BIN = previous;
  }
});

test("only a gateway-required plan failure earns the observe-only retry", () => {
  assert.equal(
    classifyTurnFailure(new Error("cave_gateway_required_for_locked_plan: no runtime")),
    "degrade_to_observe_only",
  );
  assert.equal(classifyTurnFailure(new Error("cave_output_schema_mismatch")), "fatal");
  assert.equal(classifyTurnFailure("cave_tool_call_budget_exceeded"), "fatal");
});

test("a provider the gateway does not proxy is third-party traffic, not optimized", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave_live_testkey_must_never_leave_the_gateway";
  try {
    await withWorkspace(async (workspace) => {
      // The session is told the caller manages the runtime, so routing is "on".
      // The model's provider is not one of the three the gateway proxies, so
      // this request goes straight to xAI — with none of Caveman's headers.
      const codingAgent = createCodingAgent({ workspace, model: "xai/faux-1" });
      const session = await startCodingSession(codingAgent, {
        ensureRuntime: false,
        engineBin: FAKE_ENGINE,
      });
      assert.equal(session.mode, "optimized");
      const faux = fauxNamedProvider("xai");
      faux.setResponses([fauxAssistantMessage("answered by the provider itself")]);
      const native = faux.getModel().baseUrl;
      const sent = [];
      const turn = await runCodingTurn(session, "who answers this?", {
        model: faux.getModel(),
        streamFn: (selected, context, options) => {
          sent.push({ baseUrl: selected.baseUrl, headers: { ...options.headers } });
          return faux.provider.streamSimple(selected, context, options);
        },
      });
      assert.equal(turn.text, "answered by the provider itself");
      assert.equal(sent.length, 1);
      // The account key is a credential and the rest are account-linked
      // identifiers; a third party receives neither.
      assert.equal(sent[0].headers["x-cave-api-key"], undefined);
      assert.deepEqual(Object.keys(sent[0].headers).filter((name) => name.startsWith("x-cave-")), []);
      assert.equal(sent[0].baseUrl, native);
      // Nothing the gateway did not see may be called optimized.
      assert.equal(turn.bill.mode, "observe-only");
      assert.equal(turn.degraded, true);
      assert.equal(session.mode, "observe-only");
      assert.deepEqual(session.notices, [OBSERVE_ONLY_BANNER]);
    });
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("a routed provider still reaches the gateway with its telemetry", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave_live_testkey_for_the_gateway";
  try {
    await withWorkspace(async (workspace) => {
      const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
      const session = await startCodingSession(codingAgent, {
        ensureRuntime: false,
        engineBin: FAKE_ENGINE,
        gatewayURL: "http://127.0.0.1:8787",
      });
      const faux = fauxAnthropic();
      faux.setResponses([fauxAssistantMessage("answered through the gateway")]);
      const model = { ...faux.getModel(), api: "anthropic-messages", provider: "anthropic" };
      const sent = [];
      const turn = await runCodingTurn(session, "who answers this?", {
        model,
        streamFn: (selected, context, options) => {
          sent.push({ baseUrl: selected.baseUrl, headers: { ...options.headers } });
          return faux.provider.streamSimple(selected, context, options);
        },
      });
      assert.equal(sent[0].baseUrl, "http://127.0.0.1:8787/anthropic");
      assert.equal(sent[0].headers["x-cave-api-key"], "cave_live_testkey_for_the_gateway");
      assert.equal(sent[0].headers["x-cave-agent"], "caveman-code");
      assert.equal(turn.bill.mode, "optimized");
      assert.equal(session.mode, "optimized");
    });
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("a session tries to start the runtime once, however many turns it runs", async () => {
  const previousCLI = process.env.CAVEMAN_CLI_BIN;
  const directory = await mkdtemp(resolve(tmpdir(), "caveman-code-cli-"));
  const log = resolve(directory, "spawns.log");
  const cli = resolve(directory, "caveman");
  // Logs the attempt and fails, which is what a machine without a working
  // runtime does. Before the session pinned its route, every turn paid this
  // again — and paid the full ten-second readiness wait when the spawn hung.
  await writeFile(
    cli,
    `#!/usr/bin/env node
require("node:fs").appendFileSync(${JSON.stringify(log)}, process.argv.slice(2).join(" ") + "\\n");
process.exit(1);
`,
    { mode: 0o755 },
  );
  process.env.CAVEMAN_CLI_BIN = cli;
  try {
    await withWorkspace(async (workspace) => {
      const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
      const session = await startCodingSession(codingAgent, {
        engineBin: FAKE_ENGINE,
        fetch: async () => { throw new Error("gateway unreachable"); },
      });
      assert.equal(session.mode, "observe-only");
      const faux = fauxAnthropic();
      const model = { ...faux.getModel(), api: "anthropic-messages", provider: "anthropic" };
      const startedAt = performance.now();
      for (const line of ["first turn", "second turn", "third turn"]) {
        faux.setResponses([fauxAssistantMessage(`answered ${line}`)]);
        const turn = await runCodingTurn(session, line, {
          model,
          streamFn: payloadStreamFn(faux),
          providerPayloadContract: "pi-on-payload-v1",
        });
        assert.equal(turn.bill.mode, "observe-only");
      }
      assert.equal(performance.now() - startedAt < 1_000, true, "turns must not re-probe the runtime");
      const attempts = (await readFile(log, "utf8")).trim().split("\n");
      assert.deepEqual(attempts, ["start"]);
      // The banner is the session's, not one per turn.
      assert.deepEqual(session.notices, [OBSERVE_ONLY_BANNER]);
    });
  } finally {
    if (previousCLI === undefined) delete process.env.CAVEMAN_CLI_BIN;
    else process.env.CAVEMAN_CLI_BIN = previousCLI;
    await rm(directory, { recursive: true, force: true });
  }
});

test("caller run overrides cannot forge build identity, plan, or route", async () => {
  await withWorkspace(async (workspace) => {
    const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
    const session = await startCodingSession(codingAgent, {
      ensureRuntime: false,
      engineBin: FAKE_ENGINE,
    });
    const forged = [
      { lockedBuild: { build_sha256: "0".repeat(64), plan_sha256: "0".repeat(64) } },
      { candidatePlan: defaultCodingPlan("anthropic/faux-1", "forged") },
      { caveRoute: { useGateway: true } },
    ];
    for (const overrides of forged) {
      await assert.rejects(
        () => runCodingTurn(session, "forge it", overrides),
        /cave_internal_run_option/,
        `${Object.keys(overrides)[0]} reached the internal run path`,
      );
      await assert.rejects(
        () => runCodingSession({ agent: codingAgent, runOverrides: overrides }),
        /cave_internal_run_option/,
      );
    }
    assert.equal(session.turns.length, 0);
  });
});

test("host sandbox runs bash and edit_file against a real temp workspace", async () => {
  await withWorkspace(async (workspace) => {
    await writeFile(resolve(workspace, "target.txt"), "hello original world\n", "utf8");
    const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
    const session = await startCodingSession(codingAgent, {
      ensureRuntime: false,
      engineBin: FAKE_ENGINE,
    });
    const faux = fauxAnthropic();
    faux.setResponses([
      fauxAssistantMessage(fauxToolCall("bash", { command: "ls target.txt" }, { id: "bash-1" })),
      fauxAssistantMessage(fauxToolCall("edit_file", {
        path: "target.txt",
        old_string: "original",
        new_string: "edited",
      }, { id: "edit-1" })),
      fauxAssistantMessage("renamed the word"),
    ]);
    const model = { ...faux.getModel(), api: "anthropic-messages", provider: "anthropic" };
    const turn = await runCodingTurn(session, "swap original for edited in target.txt", {
      model,
      streamFn: payloadStreamFn(faux),
      providerPayloadContract: "pi-on-payload-v1",
    });
    assert.deepEqual(turn.toolCalls, ["bash", "edit_file"]);
    assert.equal(await readFile(resolve(workspace, "target.txt"), "utf8"), "hello edited world\n");
    const bashSample = codingAgent.samples.find((item) => item.label.startsWith("bash:"));
    assert.equal(bashSample !== undefined, true);
    assert.match(bashSample.text, /target\.txt/);
  });
});

test("bash cannot read the framework's account/provider credentials", async () => {
  await withWorkspace(async (workspace) => {
    const priorCave = process.env.CAVE_API_KEY;
    const priorAnthropic = process.env.ANTHROPIC_API_KEY;
    process.env.CAVE_API_KEY = "cave-account-secret-xyz";
    process.env.ANTHROPIC_API_KEY = "sk-ant-secret-xyz";
    try {
      const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
      const bash = codingAgent.definition.tools.find((item) => item.name === "bash");
      const leak = await bash.execute({ command: "echo cave=$CAVE_API_KEY anthropic=$ANTHROPIC_API_KEY" });
      const leakText = typeof leak === "string" ? leak : JSON.stringify(leak);
      assert.doesNotMatch(leakText, /cave-account-secret-xyz/);
      assert.doesNotMatch(leakText, /sk-ant-secret-xyz/);
      // The shell baseline still passes through, so bash stays usable.
      const path = await bash.execute({ command: "echo path=$PATH" });
      const pathText = typeof path === "string" ? path : JSON.stringify(path);
      assert.match(pathText, /path=\//);
    } finally {
      if (priorCave === undefined) delete process.env.CAVE_API_KEY;
      else process.env.CAVE_API_KEY = priorCave;
      if (priorAnthropic === undefined) delete process.env.ANTHROPIC_API_KEY;
      else process.env.ANTHROPIC_API_KEY = priorAnthropic;
    }
  });
});

test("tools refuse to leave the workspace", async () => {
  await withWorkspace(async (workspace) => {
    const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
    const readTool = codingAgent.definition.tools.find((item) => item.name === "read_file");
    await assert.rejects(
      () => readTool.execute({ path: "../escape.txt" }),
      /path escapes the workspace/,
    );
  });
});

test("optimized turn bills token counts from run telemetry and proves recovery", async () => {
  await withWorkspace(async (workspace) => {
    const body = Array.from(
      { length: 240 },
      (_, index) => `line ${index}: the quick brown fox jumps over the lazy dog repeatedly`,
    ).join("\n");
    await writeFile(resolve(workspace, "big.txt"), body, "utf8");
    const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
    const session = await startCodingSession(codingAgent, {
      ensureRuntime: false,
      engineBin: FAKE_ENGINE,
    });
    assert.equal(session.mode, "optimized");
    const faux = fauxAnthropic();
    const model = { ...faux.getModel(), api: "anthropic-messages", provider: "anthropic" };

    faux.setResponses([fauxAssistantMessage("ready when you are")]);
    await runCodingTurn(session, "hello, this is the first turn of the session", {
      model,
      streamFn: payloadStreamFn(faux),
      providerPayloadContract: "pi-on-payload-v1",
    });

    const seen = [];
    faux.setResponses([
      fauxAssistantMessage(fauxToolCall("read_file", { path: "big.txt" }, { id: "read-1" })),
      fauxAssistantMessage("big.txt is a repeated pangram"),
    ]);
    const turn = await runCodingTurn(session, "read big.txt and summarize it", {
      model,
      streamFn: payloadStreamFn(faux, seen),
      providerPayloadContract: "pi-on-payload-v1",
    });

    assert.equal(turn.bill.mode, "optimized");
    assert.deepEqual(turn.bill.transformFailures, []);
    assert.equal(turn.bill.recoveryResolved, true);
    assert.deepEqual(turn.bill.transformIDs, [
      "caveman.engine.terminal.v1",
      "caveman.engine.text.v1",
    ]);
    // Numbers come from RunResult.transformTrace, not from anything invented here.
    assert.equal(turn.bill.transformedTokensBefore > turn.bill.transformedTokensAfter, true);
    assert.equal(
      turn.bill.tokensSavedInferred,
      turn.bill.transformedTokensBefore - turn.bill.transformedTokensAfter,
    );
    assert.equal(turn.bill.usageBasis, "provider_reported");
    assert.equal(turn.bill.contextBill.tool_result > 0, true);
    // The provider actually saw the compressed tool result on the second call.
    assert.match(JSON.stringify(seen[1]), /cave-compressed/);

    const total = sessionBill(session);
    assert.equal(total.turns, 2);
    assert.equal(total.tokensSavedInferred, turn.bill.tokensSavedInferred);

    const printed = [
      ...formatTurnBill(turn.bill, total.tokensSavedInferred),
      ...formatSessionBill(total),
    ];
    const savings = printed.filter((line) => line.includes("tokens saved"));
    assert.equal(savings.length, 2);
    for (const line of savings) {
      assert.match(line, /tokens/);
      assert.match(line, /local estimate/);
      assert.doesNotMatch(line, /\$/);
    }
    // No dollar figure anywhere in the bill; spend is labelled in USD with its basis.
    assert.doesNotMatch(printed.join("\n"), /\$/);
    assert.match(printed.join("\n"), /USD measured at public catalog list prices \((public_catalog|unpriced)\)/);

    const proof = await proveRecovery(session);
    assert.equal(proof.outcome, "recovered");
    assert.equal(proof.originalSHA256, proof.recoveredSHA256);
    assert.match(proof.segment, /^read_file:big\.txt$/);
    assert.match(formatRecoveryProof(proof), /round-trip OK \(sha256 match/);
  });
});

test("recovery proof reports a mismatch instead of claiming a round trip", async () => {
  await withWorkspace(async (workspace) => {
    const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
    codingAgent.samples.push({ label: "tool_result:seeded", text: "x".repeat(4_096) });
    const session = await startCodingSession(codingAgent, {
      ensureRuntime: false,
      engineBin: resolve(import.meta.dirname, "fixtures/lying-engine.mjs"),
    });
    const proof = await proveRecovery(session);
    assert.equal(proof.outcome, "mismatch");
    assert.notEqual(proof.originalSHA256, proof.recoveredSHA256);
    assert.match(formatRecoveryProof(proof), /FAILED \(sha256 mismatch\)/);
  });
});

test("a symlink out of the workspace is not inside the workspace", async () => {
  await withWorkspace(async (workspace) => {
    const outside = await mkdtemp(resolve(tmpdir(), "caveman-code-outside-"));
    try {
      await writeFile(resolve(outside, "secret.txt"), "not yours to read\n", "utf8");
      await mkdir(resolve(workspace, "nested"), { recursive: true });
      await symlink(outside, resolve(workspace, "nested/escape"), "dir");
      const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
      const tools = Object.fromEntries(
        codingAgent.definition.tools.map((item) => [item.name, item]),
      );
      // A lexical prefix check passes all three of these: the string starts with
      // the workspace path, the file it names does not live there.
      await assert.rejects(
        () => tools.read_file.execute({ path: "nested/escape/secret.txt" }),
        /path escapes the workspace/,
      );
      await assert.rejects(
        () => tools.edit_file.execute({
          path: "nested/escape/secret.txt",
          old_string: "not yours",
          new_string: "mine now",
        }),
        /path escapes the workspace/,
      );
      // Not-yet-existing leaf: the deepest existing ancestor is canonicalized,
      // so a file the agent would create outside is refused before the write.
      await assert.rejects(
        () => tools.edit_file.execute({
          path: "nested/escape/new-file.txt",
          old_string: "a",
          new_string: "b",
        }),
        /path escapes the workspace/,
      );
      assert.equal(
        await readFile(resolve(outside, "secret.txt"), "utf8"),
        "not yours to read\n",
      );
      // A real file inside the workspace still reads.
      await writeFile(resolve(workspace, "nested/inside.txt"), "yours\n", "utf8");
      assert.match(await tools.read_file.execute({ path: "nested/inside.txt" }), /yours/);
    } finally {
      await rm(outside, { recursive: true, force: true });
    }
  });
});

test("edit_file writes new_string verbatim even with $-substitution sequences", async () => {
  await withWorkspace(async (workspace) => {
    await writeFile(resolve(workspace, "code.js"), "const price = PLACEHOLDER;\n", "utf8");
    const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
    const tools = Object.fromEntries(
      codingAgent.definition.tools.map((item) => [item.name, item]),
    );
    // Every $-sequence String.replace would have interpreted, in one payload:
    // $& (whole match), $` (pre-match), $' (post-match), $$ (literal $), $1.
    const literal = "$& $` $' $$ $1 cost($100)";
    await tools.edit_file.execute({
      path: "code.js",
      old_string: "PLACEHOLDER",
      new_string: literal,
    });
    const after = await readFile(resolve(workspace, "code.js"), "utf8");
    assert.equal(after, `const price = ${literal};\n`);
  });
});

test("a backgrounded child does not hold the bash tool past its timeout", async () => {
  await withWorkspace(async (workspace) => {
    const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
    const bash = codingAgent.definition.tools.find((item) => item.name === "bash");
    const startedAt = performance.now();
    // The backgrounded sleep inherits stdout, so waiting for stdio EOF waits for
    // the sleep. The timeout kills the process group and the run settles on the
    // shell's own exit with the output that did arrive.
    const text = await bash.execute({ command: "sleep 20 & echo hi", timeoutMs: 2_000 });
    const elapsed = performance.now() - startedAt;
    assert.equal(elapsed < 10_000, true, `bash took ${Math.round(elapsed)} ms to settle`);
    assert.match(text, /hi/);
  });
});

test("a session with no turns claims no price or usage basis", async () => {
  await withWorkspace(async (workspace) => {
    const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
    const session = await startCodingSession(codingAgent, {
      ensureRuntime: false,
      engineBin: FAKE_ENGINE,
    });
    const bill = sessionBill(session);
    assert.equal(bill.turns, 0);
    assert.equal(bill.priceBasis, "unpriced");
    assert.equal(bill.usageBasis, "unavailable");
    const printed = formatSessionBill(bill).join("\n");
    assert.doesNotMatch(printed, /public_catalog/);
    assert.doesNotMatch(printed, /provider_reported/);
    assert.match(printed, /no provider calls this session/);
  });
});
