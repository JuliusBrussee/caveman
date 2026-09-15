import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawn } from "node:child_process";
import { createSocket } from "node:dgram";
import { writeFileSync } from "node:fs";
import { chmod, mkdir, mkdtemp, readFile, readdir, rm, stat, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import {
  agent,
  artifact,
  auto,
  context,
  createConversation,
  eval as defineEval,
  file,
  lowerContext,
  memory,
  output,
  run,
  schema,
  sha256,
  stableStringify,
  stream,
  subagent,
  tool,
  verifySandboxConformance,
} from "../dist/index.js";
import { CavemanRunError, createBudgetController } from "../dist/index.js";
import { compile, defineBuild } from "../dist/build.js";
import {
  fauxAssistantMessage as upstreamFauxAssistantMessage,
  fauxProvider as upstreamFauxProvider,
  fauxToolCall,
} from "@earendil-works/pi-ai/providers/faux";
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";
import sandboxAgent from "./fixtures/sandbox-agent.mjs";
import nestedSandboxAgent from "./fixtures/nested-sandbox-agent.mjs";
import {
  agentFileSourcePaths,
  loadDevModule,
} from "../dist/dev-loader.js";
import { catalogSearchCeiling } from "../dist/catalog.js";
import {
  createHarnessToolExecutor,
  prepareHarnessToolSandbox,
  proveSegmentRecovery,
  resolveCaveRoute,
  runAgentInternal,
  sandboxSourceReadFlags,
} from "../dist/runtime.js";
import { memoryFilePath, mutateMemories, readMemories } from "../dist/memory-store.js";
import { sourceGraphSHA256 } from "../dist/source-graph.js";

function fauxAnthropic(options = {}) {
  return fauxProvider({ provider: "anthropic", ...options });
}

function fauxProvider(options = {}) {
  const handle = upstreamFauxProvider(options);
  const stream = handle.provider.stream.bind(handle.provider);
  const streamSimple = handle.provider.streamSimple.bind(handle.provider);
  return {
    ...handle,
    provider: {
      ...handle.provider,
      stream: (...args) => withReportedReasoning(stream(...args)),
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
        : { partial: reportZeroReasoning(event.partial) };
      if (event.type === "done") {
        output.push({ ...event, ...partial, message: reportZeroReasoning(event.message) });
      } else if (event.type === "error") {
        output.push({ ...event, ...partial, error: reportZeroReasoning(event.error) });
      } else {
        output.push({ ...event, ...partial });
      }
    }
  });
  return output;
}

function reportZeroReasoning(message) {
  return { ...message, usage: { ...message.usage, reasoning: 0 } };
}

function fauxAssistantMessage(...args) {
  const message = upstreamFauxAssistantMessage(...args);
  return {
    ...message,
    usage: { ...message.usage, reasoning: message.usage.reasoning ?? 0 },
  };
}

async function invokeAgentCLI(args, options = {}) {
  return new Promise((done) => {
    const packageRoot = resolve(import.meta.dirname, "..");
    const child = spawn(process.execPath, [resolve(packageRoot, "dist/cli.js"), ...args], {
      cwd: options.cwd ?? packageRoot,
      env: { PATH: process.env.PATH, ...(options.env ?? {}) },
      stdio: ["ignore", "pipe", "pipe"],
    });
    const stdout = [];
    const stderr = [];
    child.stdout.on("data", (chunk) => stdout.push(chunk));
    child.stderr.on("data", (chunk) => stderr.push(chunk));
    child.once("close", (code) => done({
      code,
      stdout: Buffer.concat(stdout).toString("utf8"),
      stderr: Buffer.concat(stderr).toString("utf8"),
    }));
  });
}

function manualToolCallStream(selected, call, usage) {
  const result = createAssistantMessageEventStream();
  const message = {
    ...fauxAssistantMessage(call),
    api: selected.api,
    provider: selected.provider,
    model: selected.id,
    ...(usage === undefined ? {} : { usage }),
  };
  queueMicrotask(() => {
    const partial = { ...message, content: [], stopReason: "pending" };
    result.push({ type: "start", partial: { ...partial } });
    partial.content = [{
      type: "toolCall",
      id: call.id,
      name: call.name,
      arguments: {},
    }];
    result.push({ type: "toolcall_start", contentIndex: 0, partial: { ...partial } });
    result.push({
      type: "toolcall_delta",
      contentIndex: 0,
      delta: JSON.stringify(call.arguments),
      partial: { ...partial },
    });
    partial.content[0].arguments = call.arguments;
    result.push({
      type: "toolcall_end",
      contentIndex: 0,
      toolCall: call,
      partial: { ...partial },
    });
    result.push({ type: "done", reason: "stop", message });
    result.end(message);
  });
  return result;
}

function manualTextStream(selected, text, identity = {}, usageOverride = {}) {
  const result = createAssistantMessageEventStream();
  const message = {
    ...fauxAssistantMessage(text),
    api: selected.api,
    provider: identity.provider ?? selected.provider,
    model: identity.model ?? selected.id,
    usage: {
      input: 10,
      output: 1,
      cacheRead: 0,
      cacheWrite: 0,
      reasoning: 0,
      totalTokens: 11,
      cost: {
        input: 0,
        output: 0,
        cacheRead: 0,
        cacheWrite: 0,
        total: 0,
      },
      ...usageOverride,
    },
  };
  queueMicrotask(() => {
    const partial = { ...message, content: [], stopReason: "pending" };
    result.push({ type: "start", partial: { ...partial } });
    partial.content = [{ type: "text", text: "" }];
    result.push({ type: "text_start", contentIndex: 0, partial: { ...partial } });
    partial.content[0].text = text;
    result.push({
      type: "text_delta",
      contentIndex: 0,
      delta: text,
      partial: { ...partial },
    });
    result.push({ type: "text_end", contentIndex: 0, content: text, partial: { ...partial } });
    result.push({ type: "done", reason: "stop", message });
    result.end(message);
  });
  return result;
}

function definition() {
  return agent({
    id: "support-reply",
    instructions: "Use policy tools. Never invent policy.",
    model: auto(),
    tools: [
      tool({
        name: "lookup_policy",
        description: "Read current policy.",
        input: schema.object({ region: schema.string() }),
        effect: "read",
        result: "auto",
        async execute({ region }) {
          return { region, days: 14 };
        },
      }),
    ],
    contexts: [
      context({
        id: "support.playbook",
        kind: "skill",
        source: "Escalate exceptions.",
        stability: "build",
        safety: "S1",
      }),
    ],
    memory: memory({
      namespace: "support",
      ttl: "30d",
      recallBudget: 1_200,
    }),
    output: output({ maxTokens: 500 }),
    sandbox: "fixture",
  });
}

test("package lifecycle builds executable framework CLI before packing", async () => {
  const packageRoot = resolve(import.meta.dirname, "..");
  const pkg = JSON.parse(await readFile(resolve(packageRoot, "package.json"), "utf8"));
  assert.equal(pkg.scripts.prepack, "npm run build");
  assert.deepEqual(pkg.repository, {
    type: "git",
    url: "git+https://github.com/JuliusBrussee/caveman.git",
    directory: "packages/agent",
  });
  assert.equal(pkg.homepage, "https://caveman.so/products/caveman-agent");
  assert.equal(pkg.bugs.url, "https://github.com/JuliusBrussee/caveman/issues");
  assert.deepEqual(pkg.exports["./adapters"], {
    types: "./dist/adapters.d.ts",
    import: "./dist/adapters.js",
  });
  assert.deepEqual(pkg.peerDependencies, {
    "@mastra/core": "1.55.0",
    ai: "7.0.43",
    eve: "0.29.2",
  });
  assert.deepEqual(pkg.peerDependenciesMeta, {
    "@mastra/core": { optional: true },
    ai: { optional: true },
    eve: { optional: true },
  });
  assert.equal(
    (await readFile(resolve(packageRoot, pkg.bin["caveman-agent"]), "utf8")).split("\n", 1)[0],
    "#!/usr/bin/env node",
  );
});

test("framework CLI exposes help/version and rejects typo commands", async () => {
  const help = await invokeAgentCLI(["--help"]);
  assert.equal(help.code, 0);
  assert.match(help.stdout, /Usage:/);
  assert.match(help.stdout, /caveman-agent dev/);
  const version = await invokeAgentCLI(["--version"]);
  assert.equal(version.code, 0);
  assert.equal(version.stdout, "0.1.0\n");
  const typo = await invokeAgentCLI(["bild"]);
  assert.equal(typo.code, 1);
  assert.match(typo.stderr, /unknown command "bild"/);
});

test("framework doctor reports actionable foundation and honest harness readiness without provider calls", async () => {
  const root = await mkdtemp(resolve(tmpdir(), "caveman-agent-doctor-"));
  const runtime = resolve(root, "fake-caveman.mjs");
  const engine = resolve(root, "fake-engine.mjs");
  const claude = resolve(root, "fake-claude.mjs");
  try {
    await mkdir(resolve(root, ".caveman"), { recursive: true });
    await writeFile(
      resolve(root, ".caveman/provider.json"),
      JSON.stringify({ provider: "openai", model: "openai/gpt-5.4-mini" }),
    );
    await writeFile(runtime, [
      "#!/usr/bin/env node",
      "if (process.argv[2] !== 'version') process.exit(2);",
      "if (process.env.UNRELATED_SECRET || process.env.CAVEMAN_API_KEY) process.exit(9);",
      "process.stdout.write('caveman 1.0.0\\n');",
      "",
    ].join("\n"));
    await writeFile(engine, [
      "#!/usr/bin/env node",
      "if (process.argv[2] !== 'registry') process.exit(2);",
      "if (process.env.UNRELATED_SECRET || process.env.CAVEMAN_API_KEY) process.exit(9);",
      `process.stdout.write(JSON.stringify({registry_sha256:'${"a".repeat(64)}',capabilities:[{transform_id:'caveman.engine.text.v1',eligible_segment_kinds:['instruction']}]})+'\\n');`,
      "",
    ].join("\n"));
    await writeFile(claude, [
      "#!/usr/bin/env node",
      "if (process.env.UNRELATED_SECRET || process.env.CAVEMAN_API_KEY) process.exit(9);",
      "process.stdout.write('2.1.220 (Claude Code)\\n');",
      "",
    ].join("\n"));
    await Promise.all([runtime, engine, claude].map((path) => chmod(path, 0o755)));
    const result = await invokeAgentCLI(["doctor", "--json"], {
      cwd: root,
      env: {
        CAVEMAN_CLI_BIN: runtime,
        CAVEMAN_ENGINE_BIN: engine,
        CLAUDE_BIN: claude,
        UNRELATED_SECRET: "secret-never-print",
        CAVEMAN_API_KEY: "legacy-secret-never-pass",
      },
    });
    assert.equal(result.code, 0, result.stderr);
    const report = JSON.parse(result.stdout);
    assert.equal(report.schema_version, 1);
    assert.equal(report.ready, true);
    assert.equal(report.checks.find((item) => item.id === "sandbox").status, "pass");
    assert.equal(report.checks.find((item) => item.id === "project").status, "warn");
    const providerState = report.checks.find((item) => item.id === "provider");
    assert.equal(providerState.status, "warn");
    assert.match(providerState.detail, /openai\/gpt-5\.4-mini selected; OPENAI_API_KEY missing/);
    assert.equal(report.harnesses.find((item) => item.id === "pi").locked_execution, false);
    const claudeState = report.harnesses.find((item) => item.id === "claude");
    assert.equal(claudeState.locked_execution, false);
    assert.match(claudeState.detail, /Agent SDK 0\.3\.220/);
    assert.match(claudeState.detail, /per-turn budgets/);
    assert.equal(report.harnesses.find((item) => item.id === "vercel-ai-sdk").locked_execution, false);
    assert.equal(report.harnesses.find((item) => item.id === "eve").locked_execution, false);
    assert.equal(report.harnesses.find((item) => item.id === "mastra").locked_execution, false);
    assert.match(report.harnesses.find((item) => item.id === "pi").detail, /project check is not ready/);
    assert.doesNotMatch(result.stdout, /secret-never-print/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("doctor warns instead of failing when engine and gateway are absent", async () => {
  const root = await mkdtemp(resolve(tmpdir(), "caveman-agent-doctor-cold-"));
  try {
    const result = await invokeAgentCLI(["doctor", "--json"], {
      cwd: root,
      env: {
        CAVEMAN_CLI_BIN: resolve(root, "absent-caveman"),
        CAVEMAN_ENGINE_BIN: resolve(root, "absent-caveman-engine"),
        CAVE_GATEWAY_URL: "http://127.0.0.1:1",
      },
    });
    assert.equal(result.code, 0, result.stderr);
    const report = JSON.parse(result.stdout);
    assert.equal(report.ready, true);
    assert.equal(report.execution_mode, "observe-only");
    const check = (id) => report.checks.find((item) => item.id === id);
    assert.equal(check("engine").status, "warn");
    assert.match(check("engine").fix, /npm i -g @caveman-ai\/cli && caveman start/);
    assert.equal(check("runtime_cli").status, "warn");
    assert.equal(check("gateway").status, "warn");
    assert.match(check("gateway").detail, /127\.0\.0\.1:1/);
    assert.equal(check("sandbox").status, "pass");
    for (const harness of report.harnesses) {
      assert.equal(harness.locked_execution, false);
    }
    assert.match(report.next_action, /npm i -g @caveman-ai\/cli && caveman start/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("dev loader reloads transitive project source graph", async () => {
  const root = await mkdtemp(resolve(tmpdir(), "caveman-dev-loader-"));
  const external = await mkdtemp(resolve(tmpdir(), "caveman-dev-loader-external-"));
  const packageRoot = resolve(import.meta.dirname, "..");
  try {
    await mkdir(resolve(root, "src"), { recursive: true });
    await writeFile(resolve(root, "package.json"), '{"type":"module"}\n');
    await symlink(
      resolve(packageRoot, "node_modules"),
      resolve(root, "node_modules"),
      process.platform === "win32" ? "junction" : "dir",
    );
    await writeFile(resolve(root, "src/helper.mjs"), 'export const instructions = "first";\n');
    await writeFile(
      resolve(root, "src/agent.mjs"),
      'import { instructions } from "./helper.mjs"; export default { kind: "agent", instructions };\n',
    );

    const first = await loadDevModule(root, resolve(root, "src/agent.mjs"));
    assert.equal(first.imported.default.instructions, "first");
    assert.equal(first.sourceFiles.length, 2);
    assert.equal(first.rootDir.startsWith(resolve(tmpdir(), "caveman-agent-dev-")), true);
    assert.equal(first.entryPath.startsWith(first.rootDir), true);
    await writeFile(resolve(root, "support.md"), "snapshot");
    await first.includeFiles(["support.md"]);
    assert.equal(await readFile(resolve(first.rootDir, "support.md"), "utf8"), "snapshot");
    await writeFile(resolve(root, "support.md"), "live edit");
    assert.equal(await readFile(resolve(first.rootDir, "support.md"), "utf8"), "snapshot");
    await writeFile(resolve(root, "child.md"), "child snapshot");
    await writeFile(resolve(root, "grandchild.md"), "grandchild snapshot");
    const grandchild = agent({
      id: "dev-grandchild",
      instructions: "Grandchild.",
      model: auto(),
      contexts: [context({
        id: "grandchild.file",
        kind: "skill",
        source: file("./grandchild.md"),
        stability: "build",
      })],
      sandbox: "fixture",
    });
    const child = agent({
      id: "dev-child",
      instructions: file("./child.md"),
      model: auto(),
      tools: [subagent({
        name: "grandchild",
        description: "Delegate again.",
        agent: grandchild,
      })],
      sandbox: "fixture",
    });
    const parent = agent({
      id: "dev-parent",
      instructions: "Parent.",
      model: auto(),
      tools: [subagent({
        name: "child",
        description: "Delegate.",
        agent: child,
      })],
      sandbox: "fixture",
    });
    await first.includeAgentFiles(parent);
    assert.deepEqual(
      new Set(agentFileSourcePaths(parent)),
      new Set(["./child.md", "./grandchild.md"]),
    );
    const nestedSourceSHA = await sourceGraphSHA256(
      root,
      agentFileSourcePaths(parent).map((path) => resolve(root, path)),
    );
    assert.equal(await readFile(resolve(first.rootDir, "child.md"), "utf8"), "child snapshot");
    assert.equal(
      await readFile(resolve(first.rootDir, "grandchild.md"), "utf8"),
      "grandchild snapshot",
    );
    await writeFile(resolve(root, "child.md"), "mutated child");
    assert.notEqual(
      await sourceGraphSHA256(
        root,
        agentFileSourcePaths(parent).map((path) => resolve(root, path)),
      ),
      nestedSourceSHA,
    );
    await first.dispose();

    await writeFile(resolve(root, "src/helper.mjs"), 'export const instructions = "second";\n');
    const second = await loadDevModule(root, resolve(root, "src/agent.mjs"));
    assert.equal(second.imported.default.instructions, "second");
    await second.dispose();

    await writeFile(
      resolve(root, "src/agent.mjs"),
      'throw new Error("dev import failed");\n',
    );
    await assert.rejects(
      loadDevModule(root, resolve(root, "src/agent.mjs")),
      (error) => {
        assert.match(error.stack, new RegExp(root.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
        assert.doesNotMatch(error.stack, /caveman-agent-dev-/);
        return true;
      },
    );

    await writeFile(resolve(external, "escaped.mjs"), 'export const escaped = true;\n');
    await symlink(resolve(external, "escaped.mjs"), resolve(root, "src/escaped.mjs"));
    await writeFile(
      resolve(root, "src/agent.mjs"),
      'import { escaped } from "./escaped.mjs"; export default { kind: "agent", escaped };\n',
    );
    await assert.rejects(
      loadDevModule(root, resolve(root, "src/agent.mjs")),
      /source graph symlink escapes project root/,
    );
  } finally {
    await rm(root, { recursive: true, force: true });
    await rm(external, { recursive: true, force: true });
  }
});

test("framework primitives lower directly to content-referenced Context IR", async () => {
  const defined = definition();
  const lowered = await lowerContext({
    instructions: defined.instructions,
    tools: defined.tools,
    contexts: defined.contexts,
    memory: defined.memory,
    output: defined.output,
    input: "refund request",
  });
  assert.equal(lowered.ir.schemaVersion, 1);
  assert.equal(lowered.ir.segments.some((segment) => segment.kind === "instruction"), true);
  assert.equal(lowered.ir.segments.some((segment) => segment.kind === "tool_schema"), true);
  assert.equal(lowered.ir.segments.some((segment) => segment.kind === "memory"), true);
  assert.equal(lowered.ir.segments.some((segment) => segment.kind === "user_intent"), true);
  for (const segment of lowered.ir.segments) {
    assert.match(segment.provenanceDigest, /^[0-9a-f]{64}$/);
    assert.match(segment.bodyHandle, /^cave_local_sha256:[0-9a-f]{64}$/);
    assert.equal("body" in segment, false);
  }
});

test("public run rejects unvalidated Cave Plan and build identity injection", async () => {
  const defined = agent({
    id: "public-boundary",
    instructions: "Reply.",
    model: auto(),
    sandbox: "fixture",
  });
  await assert.rejects(
    run(defined, "x", { candidatePlan: {} }),
    /cave_internal_run_option/,
  );
  await assert.rejects(
    run(defined, "x", {
      lockedBuild: {},
    }),
    /cave_internal_run_option/,
  );
  await assert.rejects(
    run(defined, "x", {
      invocationTrace: {
        traceId: "0".repeat(32),
        spanId: "0".repeat(16),
        parentSpanId: "",
      },
    }),
    /cave_internal_run_option/,
  );
});

test("tool accepts Standard Schema with JSON Schema conversion and output validation", async () => {
  const standard = {
    "~standard": {
      version: 1,
      vendor: "fixture",
      validate(value) {
        if (value === null || typeof value !== "object" || typeof value.id !== "string") {
          return { issues: [{ message: "id required" }] };
        }
        return { value: { id: value.id.toUpperCase() } };
      },
      jsonSchema: {
        input({ target }) {
          assert.equal(target, "draft-07");
          return {
            type: "object",
            properties: { id: { type: "string" } },
            required: ["id"],
            additionalProperties: false,
          };
        },
        output() {
          return { type: "object" };
        },
      },
    },
  };
  const lookup = tool({
    name: "standard_lookup",
    description: "Lookup.",
    input: standard,
    effect: "read",
    execute({ id }) {
      return { id };
    },
  });
  assert.deepEqual(lookup.input.required, ["id"]);
  assert.deepEqual(await lookup.execute({ id: "abc" }), { id: "ABC" });
  await assert.rejects(
    lookup.execute({}),
    /cave_tool_input_schema_mismatch:standard_lookup/,
  );
  const validateOnly = {
    "~standard": {
      version: 1,
      vendor: "fixture",
      validate(value) {
        return { value };
      },
    },
  };
  const explicit = tool({
    name: "explicit_schema",
    description: "Use explicit provider schema.",
    input: validateOnly,
    inputJSONSchema: schema.object({ id: schema.string() }),
    effect: "read",
    execute: (value) => value,
  });
  assert.deepEqual(explicit.input.required, ["id"]);
});

test("string user intent IR preserves exact provider-visible bytes", async () => {
  const value = "refund request\nwith exact bytes";
  const lowered = await lowerContext({
    instructions: "Be exact.",
    tools: [],
    input: value,
  });
  const segment = lowered.ir.segments.find((item) => item.id === "turn.input");
  assert.ok(segment);
  assert.equal(new TextDecoder().decode(lowered.bodies.get(segment.bodyHandle)), value);
});

test("run reuses upstream Pi loop and reports local estimates", async () => {
  const faux = fauxProvider();
  faux.setResponses([fauxAssistantMessage("Refund allowed within 14 days.")]);
  const result = await run(definition(), "Can I refund?", {
    ensureRuntime: false,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "Refund allowed within 14 days.");
  assert.equal(result.claimBasis, "inferred");
  assert.equal(result.unlocked, true);
  assert.equal(result.contextBill.instruction > 0, true);
  assert.equal(faux.state.callCount, 1);
});

test("run labels its price basis so a $0 cost is never read as free", async () => {
  const unpricedFaux = fauxProvider();
  unpricedFaux.setResponses([fauxAssistantMessage("no catalog price for this model")]);
  const unpriced = await run(definition(), "Can I refund?", {
    ensureRuntime: false,
    model: unpricedFaux.getModel(),
    streamFn: unpricedFaux.provider.streamSimple.bind(unpricedFaux.provider),
  });
  assert.equal(unpriced.usageBasis, "provider_reported");
  assert.equal(unpriced.priceBasis, "unpriced");
  assert.equal(unpriced.costUsd, 0);

  const pricedFaux = fauxProvider({
    provider: "anthropic",
    models: [{ id: "claude-haiku-4-5" }],
  });
  pricedFaux.setResponses([fauxAssistantMessage("priced from the public catalog")]);
  const priced = await run(definition(), "Can I refund?", {
    ensureRuntime: false,
    model: pricedFaux.getModel(),
    streamFn: pricedFaux.provider.streamSimple.bind(pricedFaux.provider),
  });
  assert.equal(priced.usageBasis, "provider_reported");
  assert.equal(priced.priceBasis, "public_catalog");
  assert.equal(priced.costUsd > 0, true);
});

test("shared execution kernel fails closed on model drift and locked missing usage", async () => {
  const faux = fauxProvider();
  const defined = agent({
    id: "kernel-evidence",
    instructions: "Reply.",
    model: auto(),
    sandbox: "fixture",
  });
  await assert.rejects(
    run(defined, "x", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn: (selected) => manualTextStream(
        selected,
        "wrong model",
        { provider: "openai", model: "gpt-5.4-mini" },
      ),
    }),
    /cave_provider_model_identity_mismatch/,
  );
  const plan = {
    schema_version: 1,
    plan_id: "kernel-usage",
    model: "faux/faux-1",
    reasoning: "low",
    segment_routes: [],
    budgets: {
      instructions: 1_000,
      tools: 0,
      memory: 0,
      history: 0,
      results_artifacts: 0,
      reasoning: 100,
      output: 100,
      retry_cascade_reserve: 0,
    },
    recovery: { namespace: "kernel", tools: [] },
    fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
  };
  let selectionMismatchCalls = 0;
  await assert.rejects(
    runAgentInternal(defined, "x", {
      ensureRuntime: false,
      model: faux.getModel(),
      candidatePlan: { ...plan, model: "faux/not-selected" },
      streamFn: (selected) => {
        selectionMismatchCalls++;
        return manualTextStream(selected, "must not execute");
      },
    }),
    /cave_execution_plan_selection_mismatch/,
  );
  assert.equal(selectionMismatchCalls, 0);
  await assert.rejects(
    runAgentInternal(defined, "x", {
      ensureRuntime: false,
      model: faux.getModel(),
      candidatePlan: plan,
      streamFn: (selected) => manualTextStream(
        selected,
        "missing usage",
        {},
        { input: 0, output: 0, totalTokens: 0 },
      ),
    }),
    /cave_provider_usage_incomplete/,
  );
  const reasoningFaux = fauxProvider({ models: [{ id: "faux-1", reasoning: true }] });
  const unavailableReasoning = await run(defined, "x", {
    ensureRuntime: false,
    model: reasoningFaux.getModel(),
    streamFn: (selected) => manualTextStream(
      selected,
      "aggregate usage only",
      {},
      { reasoning: undefined },
    ),
  });
  assert.equal(unavailableReasoning.usageBasis, "provider_reported");
  assert.equal(unavailableReasoning.reasoningUsageBasis, "unavailable");
  await assert.rejects(
    runAgentInternal(defined, "x", {
      ensureRuntime: false,
      model: reasoningFaux.getModel(),
      candidatePlan: plan,
      streamFn: (selected) => manualTextStream(
        selected,
        "missing reasoning split",
        {},
        { reasoning: undefined },
      ),
    }),
    /cave_reasoning_usage_unavailable/,
  );
});

test("locked Pi stops before another provider call when reasoning split disappears", async () => {
  const defined = agent({
    id: "reasoning-next-call",
    instructions: "Call continue_once, then answer.",
    model: auto(),
    tools: [tool({
      name: "continue_once",
      description: "Continue once.",
      input: schema.object({}),
      effect: "read",
      result: "inline",
      execute: () => "continue",
    })],
    sandbox: "fixture",
  });
  const faux = fauxProvider({ models: [{ id: "faux-1", reasoning: true }] });
  const candidatePlan = {
    schema_version: 1,
    plan_id: "reasoning-next-call",
    model: "faux/faux-1",
    reasoning: "low",
    segment_routes: [],
    budgets: {
      instructions: 1_000,
      tools: 1_000,
      memory: 0,
      history: 1_000,
      results_artifacts: 1_000,
      reasoning: 100,
      output: 100,
      retry_cascade_reserve: 256,
    },
    recovery: { namespace: "reasoning-next-call", tools: [] },
    fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
  };
  let providerCalls = 0;
  await assert.rejects(
    runAgentInternal(defined, "go", {
      ensureRuntime: false,
      model: faux.getModel(),
      candidatePlan,
      streamFn: (selected) => {
        providerCalls += 1;
        if (providerCalls !== 1) return manualTextStream(selected, "must not spend");
        return manualToolCallStream(
          selected,
          fauxToolCall("continue_once", {}, { id: "continue-once" }),
          {
            input: 10,
            output: 1,
            cacheRead: 0,
            cacheWrite: 0,
            reasoning: undefined,
            totalTokens: 11,
            cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
          },
        );
      },
    }),
    /cave_reasoning_usage_unavailable/,
  );
  assert.equal(providerCalls, 1);
});

test("a run that fails after spending still surfaces its ledger", async () => {
  // A locked/candidate run that records a paid call, then fails its fail-closed
  // reasoning-evidence gate, must not throw the ledger away with the error.
  const defined = agent({
    id: "receipt-on-failure",
    instructions: "Call continue_once, then answer.",
    model: auto(),
    tools: [tool({
      name: "continue_once",
      description: "Continue once.",
      input: schema.object({}),
      effect: "read",
      result: "inline",
      execute: () => "continue",
    })],
    sandbox: "fixture",
  });
  const faux = fauxProvider({ models: [{ id: "faux-1", reasoning: true }] });
  const candidatePlan = {
    schema_version: 1,
    plan_id: "receipt-on-failure",
    model: "faux/faux-1",
    reasoning: "low",
    segment_routes: [],
    budgets: {
      instructions: 1_000,
      tools: 1_000,
      memory: 0,
      history: 1_000,
      results_artifacts: 1_000,
      reasoning: 100,
      output: 100,
      retry_cascade_reserve: 256,
    },
    recovery: { namespace: "receipt-on-failure", tools: [] },
    fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
  };
  let providerCalls = 0;
  const error = await runAgentInternal(defined, "go", {
    ensureRuntime: false,
    model: faux.getModel(),
    candidatePlan,
    streamFn: (selected) => {
      providerCalls += 1;
      if (providerCalls !== 1) return manualTextStream(selected, "must not spend");
      return manualToolCallStream(
        selected,
        fauxToolCall("continue_once", {}, { id: "continue-once" }),
        {
          input: 10,
          output: 1,
          cacheRead: 0,
          cacheWrite: 0,
          reasoning: undefined,
          totalTokens: 11,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
        },
      );
    },
  }).then(() => undefined, (e) => e);
  // Still fails closed — the evidence gate is not weakened.
  assert.ok(error instanceof CavemanRunError, "expected CavemanRunError");
  assert.match(error.message, /cave_reasoning_usage_unavailable/);
  // ...but the ledger of the paid call(s) survives on the error's receipt.
  assert.ok(error.receipt, "expected a receipt on the failure");
  assert.ok(error.receipt.calls.length >= 1, "expected at least one recorded call");
  assert.ok(error.receipt.totalTokens >= 11, "expected the spent tokens on the receipt");
  assert.equal(error.cause, error.receipt);
});

test("hard model-call ceiling stops gracefully with call_budget_exhausted", async () => {
  const defined = agent({
    id: "call-cap",
    instructions: "Loop forever.",
    model: auto(),
    tools: [tool({
      name: "loop",
      description: "Loop.",
      input: schema.object({}),
      effect: "read",
      result: "inline",
      execute: () => "again",
    })],
    sandbox: "fixture",
  });
  const faux = fauxProvider({ models: [{ id: "faux-1" }] });
  let calls = 0;
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: faux.getModel(),
    maxModelCalls: 3,
    streamFn: (selected) => {
      calls += 1;
      return manualToolCallStream(
        selected,
        fauxToolCall("loop", {}, { id: `loop-${calls}` }),
        {
          input: 10,
          output: 1,
          cacheRead: 0,
          cacheWrite: 0,
          reasoning: 0,
          totalTokens: 11,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
        },
      );
    },
  });
  // Ends gracefully — a normal RunResult, not a throw.
  assert.equal(result.stopReason, "call_budget_exhausted");
  // Exactly maxModelCalls provider calls were made, no more.
  assert.equal(calls, 3);
  assert.equal(result.receipt.calls.length, 3);
});

test("maxModelCalls fails closed on a non-positive value", async () => {
  const defined = agent({
    id: "call-cap-invalid",
    instructions: "answer",
    model: auto(),
    tools: [],
    sandbox: "fixture",
  });
  const faux = fauxProvider({ models: [{ id: "faux-1" }] });
  await assert.rejects(
    run(defined, "go", {
      ensureRuntime: false,
      model: faux.getModel(),
      maxModelCalls: 0,
      streamFn: (selected) => manualTextStream(selected, "hi"),
    }),
    /cave_max_model_calls_invalid/,
  );
});

test("a manual consumer stopping at run_end releases the budget controller before delivery", async () => {
  const controller = createBudgetController();
  const defined = agent({
    id: "leak-controller",
    instructions: "answer",
    model: auto(),
    tools: [],
    sandbox: "fixture",
  });
  const faux = fauxProvider({ models: [{ id: "faux-1" }] });
  const iterator = stream(defined, "go", {
    ensureRuntime: false,
    model: faux.getModel(),
    budget: { maxTokens: 10_000 },
    budgetController: controller,
    streamFn: (selected) => manualTextStream(selected, "done"),
  });
  let sawRunEnd = false;
  for (;;) {
    const next = await iterator.next();
    if (next.done) break;
    // Stop the moment run_end arrives WITHOUT finishing the generator — the
    // finally never runs, so release-before-delivery is what must have freed it.
    if (next.value.type === "run_end") { sawRunEnd = true; break; }
  }
  assert.equal(sawRunEnd, true);
  // The controller was unbound before run_end was delivered, so it can bind to a
  // fresh run. On the old code (release only in finally) it would still be bound
  // and this second run would throw cave_budget_controller_in_use.
  const second = await run(defined, "go again", {
    ensureRuntime: false,
    model: faux.getModel(),
    budget: { maxTokens: 10_000 },
    budgetController: controller,
    streamFn: (selected) => manualTextStream(selected, "again"),
  });
  assert.equal(second.text, "again");
});

test("the engine subprocess starts from an allow-listed env, not the parent's secrets", async () => {
  const store = await mkdtemp(resolve(tmpdir(), "cave-engine-env-"));
  const priorStore = process.env.CAVE_FAKE_ENGINE_STORE;
  const priorKey = process.env.ANTHROPIC_API_KEY;
  const priorAws = process.env.AWS_SECRET_ACCESS_KEY;
  const priorCave = process.env.CAVE_API_KEY;
  const priorLegacyCave = process.env.CAVEMAN_API_KEY;
  const priorMcpToken = process.env.CAVEMAN_MCP_TOKEN;
  const priorClaudeSession = process.env.CAVEMAN_CLAUDE_SESSION_KEY;
  const priorEngineCfg = process.env.CAVEMAN_ENGINE_TESTCFG;
  const priorEngineToon = process.env.CAVE_ENGINE_TOON;
  process.env.CAVE_FAKE_ENGINE_STORE = store;
  process.env.ANTHROPIC_API_KEY = "sk-ant-secret";
  process.env.AWS_SECRET_ACCESS_KEY = "aws-secret";
  process.env.CAVE_API_KEY = "cave-account-secret";
  process.env.CAVEMAN_API_KEY = "legacy-cave-account-secret";
  process.env.CAVEMAN_MCP_TOKEN = "mcp-bearer-secret";
  process.env.CAVEMAN_CLAUDE_SESSION_KEY = "claude-session-secret";
  process.env.CAVEMAN_ENGINE_TESTCFG = "engine-config-value";
  process.env.CAVE_ENGINE_TOON = "best-of";
  try {
    await proveSegmentRecovery({
      body: new TextEncoder().encode("some segment body to compress"),
      transformID: "caveman.engine.text.v1",
      engineBin: resolve("tests/fixtures/fake-engine-env.mjs"),
    });
    const dumped = JSON.parse(await readFile(resolve(store, "engine-env.json"), "utf8"));
    // Provider and account secrets are withheld from the engine.
    assert.equal("ANTHROPIC_API_KEY" in dumped, false);
    assert.equal("AWS_SECRET_ACCESS_KEY" in dumped, false);
    assert.equal("CAVE_API_KEY" in dumped, false);
    assert.equal("CAVEMAN_API_KEY" in dumped, false);
    assert.equal("CAVEMAN_MCP_TOKEN" in dumped, false);
    assert.equal("CAVEMAN_CLAUDE_SESSION_KEY" in dumped, false);
    assert.equal("CAVEMAN_ENGINE_TESTCFG" in dumped, false);
    // Exact engine config names and a PATH baseline pass through.
    assert.equal(dumped.CAVE_ENGINE_TOON, "best-of");
    assert.ok(typeof dumped.PATH === "string");
  } finally {
    for (const [key, value] of Object.entries({
      CAVE_FAKE_ENGINE_STORE: priorStore,
      ANTHROPIC_API_KEY: priorKey,
      AWS_SECRET_ACCESS_KEY: priorAws,
      CAVE_API_KEY: priorCave,
      CAVEMAN_API_KEY: priorLegacyCave,
      CAVEMAN_MCP_TOKEN: priorMcpToken,
      CAVEMAN_CLAUDE_SESSION_KEY: priorClaudeSession,
      CAVEMAN_ENGINE_TESTCFG: priorEngineCfg,
      CAVE_ENGINE_TOON: priorEngineToon,
    })) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
    await rm(store, { recursive: true, force: true });
  }
});

test("runtime segment identity is content-derived and fails closed on reuse", async () => {
  const { appendRuntimeContextSegment, lowerContext } = await import("../dist/index.js");
  const lowered = await lowerContext({ instructions: "system", tools: [] });
  const enc = (text) => new TextEncoder().encode(text);
  const first = appendRuntimeContextSegment(lowered, {
    id: "runtime.history.deadbeefdeadbeef",
    kind: "history",
    body: enc("the original user message"),
  });
  // Same id + same bytes dedups safely to the same segment: content-identical
  // segments legitimately share identity.
  const again = appendRuntimeContextSegment(lowered, {
    id: "runtime.history.deadbeefdeadbeef",
    kind: "history",
    body: enc("the original user message"),
  });
  assert.equal(again, first);
  // Same id + DIFFERENT bytes is the exact confusion that once let compaction
  // substitute an OLD message's compressed body into a NEW message after the
  // indices shifted. Old code returned the stale segment silently; now it fails
  // closed rather than poisoning the CCR restore map.
  assert.throws(
    () => appendRuntimeContextSegment(lowered, {
      id: "runtime.history.deadbeefdeadbeef",
      kind: "history",
      body: enc("a completely different, newer message"),
    }),
    /cave_runtime_segment_id_collision/,
  );
});

test("locked Pi runtime binds static Context IR and installed adapter identity", async () => {
  const defined = definition();
  const staticContext = await lowerContext({
    instructions: defined.instructions,
    tools: defined.tools,
    contexts: defined.contexts,
    memory: defined.memory,
    output: { maxTokens: defined.output.maxTokens },
  });
  const plan = {
    schema_version: 1,
    plan_id: "locked-runtime",
    model: "faux/faux-1",
    reasoning: "low",
    segment_routes: [],
    budgets: {
      instructions: 2_000,
      tools: 2_000,
      memory: 2_000,
      history: 2_000,
      results_artifacts: 2_000,
      reasoning: 1_000,
      output: 500,
      retry_cascade_reserve: 0,
    },
    recovery: { namespace: "locked-runtime", tools: [] },
    fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
  };
  const compiled = await compile({
    agent: defined,
    contextIR: staticContext.ir,
    evals: [defineEval({
      id: "locked-runtime",
      approved: true,
      input: "fixture prompt differs from production turn",
      quality: [{ type: "exact_match", expected: "ok" }],
    })],
    candidates: [{ plan, estimated_cost_usd_per_run: 0.001 }],
    baselinePlan: plan,
    seeds: [1, 2, 3, 4, 5],
    config: defineBuild({ entry: "src/agent.ts", evals: "evals/*.ts" }),
    entitled: true,
    sourceSha256: "1".repeat(64),
    catalogSha256: "2".repeat(64),
    transformRegistrySha256: "3".repeat(64),
    runtimeVersion: "0.1.0",
    adapterVersion: "0.1.0",
    upstreamVersion: "0.83.0",
    runner: async () => ({
      terminal: true,
      usage_basis: "provider_reported",
      price_basis: "public_catalog",
      catalog_cost_usd: 0.001,
      quality_score: 1,
      graders: [{ type: "exact_match", passed: true }],
      latency_ms: 1,
      provider_visible_tokens: 1,
      cache_prefix_sha256: "4".repeat(64),
      cache_boundary_known: true,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      cache_bust: false,
      error: false,
      recovery_resolved: true,
      privacy_passed: true,
      sandbox_passed: true,
      output_digest: "5".repeat(64),
    }),
  });
  assert.equal(compiled.status, "locked");
  assert.ok(compiled.lock);

  const faux = fauxProvider();
  faux.setResponses([fauxAssistantMessage("ok")]);
  const result = await runAgentInternal(defined, "production turn is arbitrary", {
    ensureRuntime: false,
    lockedBuild: compiled.lock,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "ok");
  assert.equal(result.unlocked, false);

  const stale = structuredClone(compiled.lock);
  stale.harness.upstream_version = "0.84.0";
  const { build_sha256: _digest, ...withoutBuild } = stale;
  stale.build_sha256 = sha256(stableStringify(withoutBuild));
  let staleCalls = 0;
  await assert.rejects(
    runAgentInternal(defined, "must not spend", {
      ensureRuntime: false,
      lockedBuild: stale,
      model: faux.getModel(),
      streamFn: (selected) => {
        staleCalls++;
        return manualTextStream(selected, "must not execute");
      },
    }),
    /cave_harness_build_mismatch/,
  );
  assert.equal(staleCalls, 0);
});

test("public conversation state preserves multi-turn history", async () => {
  const conversation = createConversation();
  const faux = fauxProvider();
  faux.setResponses([
    fauxAssistantMessage("First answer."),
    fauxAssistantMessage("Second answer."),
  ]);

  await run(definition(), "First prompt.", {
    ensureRuntime: false,
    conversation,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  const firstTurnLength = conversation.snapshot().messages.length;
  assert.equal(firstTurnLength >= 2, true);

  await run(definition(), "Second prompt.", {
    ensureRuntime: false,
    conversation,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  const messages = conversation.snapshot().messages;
  assert.equal(messages.length > firstTurnLength, true);
  assert.match(JSON.stringify(messages), /First prompt/);
  assert.match(JSON.stringify(messages), /First answer/);
  assert.match(JSON.stringify(messages), /Second prompt/);
  assert.match(JSON.stringify(messages), /Second answer/);
});

test("GOOGLE_API_KEY alias reaches Pi under its GEMINI_API_KEY env name", async () => {
  const previousGoogle = process.env.GOOGLE_API_KEY;
  const previousGemini = process.env.GEMINI_API_KEY;
  process.env.GOOGLE_API_KEY = "google-alias-test-key";
  delete process.env.GEMINI_API_KEY;
  const faux = fauxProvider({ provider: "google", api: "google-generative-ai" });
  faux.setResponses([fauxAssistantMessage("google alias routed")]);
  let observedAlias;
  const streamFn = (selected, context, options) => {
    observedAlias = options?.env?.GEMINI_API_KEY;
    return faux.provider.streamSimple(selected, context, options);
  };
  try {
    const result = await run(definition(), "google alias", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn,
    });
    assert.equal(result.text, "google alias routed");
    assert.equal(observedAlias, "google-alias-test-key");
  } finally {
    if (previousGoogle === undefined) delete process.env.GOOGLE_API_KEY;
    else process.env.GOOGLE_API_KEY = previousGoogle;
    if (previousGemini === undefined) delete process.env.GEMINI_API_KEY;
    else process.env.GEMINI_API_KEY = previousGemini;
  }
});

test("conversation rotates cache epoch on definition change without losing history", async () => {
  const conversation = createConversation();
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage("First answer."),
    fauxAssistantMessage("Second answer."),
  ]);
  const model = { ...faux.getModel(), api: "anthropic-messages", provider: "anthropic" };
  const forwarded = [];
  const streamFn = (selected, context, options) => {
    const payload = {
      model: selected.id,
      system: context.systemPrompt,
      tools: context.tools,
      messages: context.messages,
    };
    forwarded.push({
      payload: structuredClone(options.onPayload(payload, selected)),
      headers: { ...options.headers },
    });
    return faux.provider.streamSimple(selected, context, { ...options, onPayload: undefined });
  };
  const first = agent({
    id: "reload",
    instructions: "OLD INSTRUCTIONS",
    model: auto(),
    sandbox: "fixture",
  });
  const second = agent({
    id: "reload",
    instructions: "NEW INSTRUCTIONS",
    model: auto(),
    sandbox: "fixture",
  });

  await run(first, "First prompt.", {
    ensureRuntime: false,
    conversation,
    model,
    streamFn,
    providerPayloadContract: "pi-on-payload-v1",
  });
  await run(second, "Second prompt.", {
    ensureRuntime: false,
    conversation,
    model,
    streamFn,
    providerPayloadContract: "pi-on-payload-v1",
  });

  assert.match(JSON.stringify(forwarded[1].payload.system), /NEW INSTRUCTIONS/);
  assert.doesNotMatch(JSON.stringify(forwarded[1].payload.system), /OLD INSTRUCTIONS/);
  assert.match(JSON.stringify(forwarded[1].payload.messages), /First prompt/);
  assert.match(JSON.stringify(forwarded[1].payload.messages), /First answer/);
  assert.match(JSON.stringify(forwarded[1].payload.messages), /Second prompt/);
  assert.notEqual(
    forwarded[0].headers["x-cave-cache-epoch"],
    forwarded[1].headers["x-cave-cache-epoch"],
  );
  assert.equal(forwarded[0].headers["x-cave-session"], conversation.sessionId);
  assert.equal(forwarded[1].headers["x-cave-session"], conversation.sessionId);
});

test("same-id plan content change rotates conversation cache epoch", async () => {
  const conversation = createConversation();
  // An anthropic-provider model, because x-cave-* headers exist only on traffic
  // that actually reaches the gateway: a provider the gateway does not proxy
  // keeps its own base URL and receives no Caveman header at all.
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage("First answer."),
    fauxAssistantMessage("Second answer."),
  ]);
  const headers = [];
  const streamFn = (selected, context, options) => {
    headers.push({ ...options.headers });
    return faux.provider.streamSimple(selected, context, options);
  };
  const plan = {
    schema_version: 1,
    plan_id: "same-id",
    model: "anthropic/faux-1",
    reasoning: "low",
    segment_routes: [],
    budgets: {
      instructions: 1_000,
      tools: 1_000,
      memory: 1_000,
      history: 1_000,
      results_artifacts: 1_000,
      reasoning: 1_000,
      output: 500,
      retry_cascade_reserve: 256,
    },
    recovery: { namespace: "same-id", tools: [] },
    fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
  };
  await runAgentInternal(definition(), "first", {
    ensureRuntime: false,
    conversation,
    candidatePlan: plan,
    model: faux.getModel(),
    streamFn,
  });
  await runAgentInternal(definition(), "second", {
    ensureRuntime: false,
    conversation,
    candidatePlan: {
      ...plan,
      budgets: { ...plan.budgets, output: 501 },
    },
    model: faux.getModel(),
    streamFn,
  });
  assert.notEqual(
    headers[0]["x-cave-cache-epoch"],
    headers[1]["x-cave-cache-epoch"],
  );
});

test("conversation rolls back failed turn after provider payload", async () => {
  const conversation = createConversation();
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage('{"answer":"accepted"}'),
    fauxAssistantMessage("invalid json"),
    fauxAssistantMessage('{"answer":"recovered"}'),
  ]);
  const defined = agent({
    id: "transaction",
    instructions: "Return JSON.",
    model: auto(),
    output: output({
      maxTokens: 100,
      schema: schema.object({ answer: schema.string() }),
    }),
    sandbox: "fixture",
  });
  const model = { ...faux.getModel(), api: "anthropic-messages", provider: "anthropic" };
  const providerMessages = [];
  const streamFn = (selected, context, options) => {
    providerMessages.push(structuredClone(context.messages));
    options.onPayload({
      model: selected.id,
      system: context.systemPrompt,
      messages: context.messages,
    }, selected);
    return faux.provider.streamSimple(selected, context, { ...options, onPayload: undefined });
  };

  await run(defined, "accepted prompt", {
    ensureRuntime: false,
    conversation,
    model,
    streamFn,
    providerPayloadContract: "pi-on-payload-v1",
  });
  const accepted = conversation.snapshot();
  await assert.rejects(
    run(defined, "failed prompt", {
      ensureRuntime: false,
      conversation,
      model,
      streamFn,
      providerPayloadContract: "pi-on-payload-v1",
    }),
    /cave_output_schema_invalid_json/,
  );
  assert.deepEqual(conversation.snapshot(), accepted);

  await run(defined, "recovery prompt", {
    ensureRuntime: false,
    conversation,
    model,
    streamFn,
    providerPayloadContract: "pi-on-payload-v1",
  });
  assert.doesNotMatch(JSON.stringify(providerMessages[2]), /failed prompt|invalid json/);
  assert.match(JSON.stringify(providerMessages[2]), /accepted prompt/);
});

test("terminal stream event releases conversation before consumer advances", async () => {
  const conversation = createConversation();
  const faux = fauxProvider();
  faux.setResponses([
    fauxAssistantMessage("first"),
    fauxAssistantMessage("second"),
  ]);
  const active = stream(definition(), "first", {
    ensureRuntime: false,
    conversation,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  while (true) {
    const event = await active.next();
    assert.equal(event.done, false);
    if (event.value.type === "run_end") break;
  }

  const second = await run(definition(), "second", {
    ensureRuntime: false,
    conversation,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(second.text, "second");
  await active.return();
});

test("stream return aborts in-flight provider before releasing conversation", async () => {
  const conversation = createConversation();
  const faux = fauxProvider({
    tokensPerSecond: 1,
    tokenSize: { min: 1, max: 1 },
  });
  faux.setResponses([
    fauxAssistantMessage("slow response ".repeat(100)),
  ]);
  const active = stream(definition(), "cancel", {
    ensureRuntime: false,
    conversation,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  while (faux.state.callCount === 0) {
    const event = await active.next();
    assert.equal(event.done, false);
  }
  const started = Date.now();
  await active.return();
  assert.equal(Date.now() - started < 2_000, true);
  assert.equal(conversation.snapshot().messages.length, 0);

  const recoveryFaux = fauxProvider();
  recoveryFaux.setResponses([fauxAssistantMessage("recovered")]);
  const recovered = await run(definition(), "after cancel", {
    ensureRuntime: false,
    conversation,
    model: recoveryFaux.getModel(),
    streamFn: recoveryFaux.provider.streamSimple.bind(recoveryFaux.provider),
  });
  assert.equal(recovered.text, "recovered");
});

test("conversation rejects concurrent reuse and releases on stream close", async () => {
  const conversation = createConversation();
  const faux = fauxProvider();
  faux.setResponses([fauxAssistantMessage("done")]);
  const active = stream(definition(), "first", {
    ensureRuntime: false,
    conversation,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal((await active.next()).value.type, "run_start");
  assert.equal((await active.next()).value.type, "context_ready");

  await assert.rejects(
    run(definition(), "second", {
      ensureRuntime: false,
      conversation,
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    }),
    /cave_conversation_in_use/,
  );
  await active.return();

  const recovered = await run(definition(), "third", {
    ensureRuntime: false,
    conversation,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(recovered.text, "done");
});

function routeCapture(faux) {
  const selected = [];
  return {
    selected,
    streamFn: (model, context, options) => {
      selected.push(model);
      return faux.provider.streamSimple(model, context, options);
    },
  };
}

function withMissingRuntimeBinaries(body) {
  return async () => {
    const previousCLI = process.env.CAVEMAN_CLI_BIN;
    const previousProxy = process.env.CAVEMAN_PROXY_BIN;
    process.env.CAVEMAN_CLI_BIN = resolve(
      tmpdir(),
      `missing-caveman-${process.pid}-${crypto.randomUUID()}`,
    );
    process.env.CAVEMAN_PROXY_BIN = resolve(
      tmpdir(),
      `missing-caveman-proxy-${process.pid}-${crypto.randomUUID()}`,
    );
    try {
      await body();
    } finally {
      if (previousCLI === undefined) delete process.env.CAVEMAN_CLI_BIN;
      else process.env.CAVEMAN_CLI_BIN = previousCLI;
      if (previousProxy === undefined) delete process.env.CAVEMAN_PROXY_BIN;
      else process.env.CAVEMAN_PROXY_BIN = previousProxy;
    }
  };
}

test(
  "cold machine without runtime CLI degrades to observe-only on the provider base URL",
  withMissingRuntimeBinaries(async () => {
    const faux = fauxAnthropic();
    faux.setResponses([fauxAssistantMessage("answered without a gateway")]);
    const capture = routeCapture(faux);
    const native = faux.getModel().baseUrl;
    const result = await run(definition(), "runtime check", {
      gatewayURL: "http://127.0.0.1:1",
      fetch: async () => {
        throw new Error("not ready");
      },
      model: faux.getModel(),
      streamFn: capture.streamFn,
    });
    assert.equal(result.text, "answered without a gateway");
    assert.equal(result.mode, "observe-only");
    assert.equal(faux.state.callCount, 1);
    assert.equal(capture.selected.length, 1);
    assert.equal(capture.selected[0].baseUrl, native);
    assert.deepEqual(result.transformIDs, []);
    assert.equal(result.claimBasis, "inferred");
  }),
);

test(
  "spoofed Caveman health JSON without authoritative proxy ownership never receives traffic",
  withMissingRuntimeBinaries(async () => {
    const faux = fauxAnthropic();
    faux.setResponses([fauxAssistantMessage("must not route")]);
    const capture = routeCapture(faux);
    const native = faux.getModel().baseUrl;
    const result = await run(definition(), "runtime identity check", {
      gatewayURL: "http://127.0.0.1:2",
      fetch: async () => Response.json({
        ok: true,
        service: "caveman-proxy",
        schema: "caveman.proxy.health.v1",
        adapters: 4,
      }),
      model: faux.getModel(),
      streamFn: capture.streamFn,
    });
    assert.equal(result.mode, "observe-only");
    assert.equal(capture.selected[0].baseUrl, native);
    assert.doesNotMatch(capture.selected[0].baseUrl, /127\.0\.0\.1:2/);
  }),
);

test("cave off keeps the provider base URL and never contacts the gateway", async () => {
  const faux = fauxAnthropic();
  faux.setResponses([fauxAssistantMessage("direct")]);
  const capture = routeCapture(faux);
  const native = faux.getModel().baseUrl;
  let probes = 0;
  const result = await run(definition(), "no gateway please", {
    cave: "off",
    gatewayURL: "http://127.0.0.1:1",
    fetch: async () => {
      probes++;
      throw new Error("gateway must not be probed");
    },
    model: faux.getModel(),
    streamFn: capture.streamFn,
  });
  assert.equal(result.text, "direct");
  assert.equal(result.mode, "observe-only");
  assert.equal(probes, 0);
  assert.equal(capture.selected[0].baseUrl, native);
  assert.deepEqual(result.transformIDs, []);
  assert.deepEqual(result.evaluatedTransformIDs, []);
  assert.deepEqual(result.transformTrace, []);
});

test("reachable gateway keeps the run optimized and routed", async () => {
  const directory = await mkdtemp(resolve(tmpdir(), "caveman-observe-optimized-"));
  const proxy = resolve(directory, "caveman-proxy");
  const previousProxy = process.env.CAVEMAN_PROXY_BIN;
  await writeFile(
    proxy,
    `#!/usr/bin/env node
if (process.argv[2] !== "status") process.exit(2);
const port = Number(process.argv[process.argv.indexOf("--port") + 1]);
process.stdout.write(JSON.stringify({
  owner: "start",
  instance_token: "0123456789abcdef0123456789abcdef",
  pid: process.pid,
  port,
}));
`,
    { mode: 0o755 },
  );
  process.env.CAVEMAN_PROXY_BIN = proxy;
  try {
    for (const [provider, api, expectedBaseUrl] of [
      ["anthropic", "anthropic-messages", "http://127.0.0.1:2/anthropic"],
      ["openai", "openai-responses", "http://127.0.0.1:2/openai/v1"],
      ["google", "google-generative-ai", "http://127.0.0.1:2/gemini/v1beta"],
    ]) {
      const faux = fauxProvider({ provider, api });
      faux.setResponses([fauxAssistantMessage(`routed ${provider}`)]);
      const capture = routeCapture(faux);
      const result = await run(definition(), `gateway present ${provider}`, {
        gatewayURL: "http://127.0.0.1:2",
        fetch: async () => Response.json({
          ok: true,
          service: "caveman-proxy",
          schema: "caveman.proxy.health.v1",
          adapters: 4,
        }),
        model: faux.getModel(),
        streamFn: capture.streamFn,
      });
      assert.equal(result.text, `routed ${provider}`);
      assert.equal(result.mode, "optimized");
      assert.equal(capture.selected[0].baseUrl, expectedBaseUrl);
    }
  } finally {
    if (previousProxy === undefined) delete process.env.CAVEMAN_PROXY_BIN;
    else process.env.CAVEMAN_PROXY_BIN = previousProxy;
    await rm(directory, { recursive: true, force: true });
  }
});

function routeAndHeaderCapture(faux) {
  const selected = [];
  const headers = [];
  return {
    selected,
    headers,
    streamFn: (model, context, options) => {
      selected.push(model);
      headers.push({ ...options.headers });
      return faux.provider.streamSimple(model, context, options);
    },
  };
}

function invocationAttributes(span) {
  return Object.fromEntries(span.attributes.map((attribute) => [
    attribute.key,
    attribute.value.stringValue ?? attribute.value.intValue,
  ]));
}

function invocationGuardAttributes(span) {
  return Object.fromEntries(Object.entries(invocationAttributes(span))
    .filter(([key]) => key.startsWith("cave.guard.")));
}

function invocationTreeOutcomeAttributes(span) {
  return Object.fromEntries(Object.entries(invocationAttributes(span))
    .filter(([key]) => key.startsWith("cave.agent.tree.")));
}

const zeroInvocationTreeOutcomes = {
  "cave.agent.tree.admitted_descendants": "0",
  "cave.agent.tree.peak_active_descendants": "0",
  "cave.agent.tree.invocation_limit_rejections": "0",
  "cave.agent.tree.concurrency_limit_rejections": "0",
};

const activeChildGuardWithoutRootBudget = {
  "cave.guard.schema_version": "2",
  "cave.guard.basis": "client_runtime_declared",
  "cave.guard.framework": "caveman_agent",
  "cave.guard.child_calls": "active",
  "cave.guard.child_spend": "usd_pre_call",
  "cave.guard.child_context": "active",
  "cave.guard.depth": "active",
  "cave.guard.root_budget": "absent",
  "cave.guard.turn_fanout": "absent",
  "cave.guard.model_calls": "active",
  "cave.guard.tool_calls": "active",
  "cave.guard.tree_invocations": "absent",
  "cave.guard.tree_concurrency": "absent",
};

test("root invocation reports zero tree outcomes with no caps and no descendants", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave-empty-tree-outcomes-key";
  const faux = fauxAnthropic();
  faux.setResponses([fauxAssistantMessage("root only")]);
  const exports = [];
  try {
    const result = await run(definition(), "no descendants", {
      ensureRuntime: false,
      gatewayURL: "https://gateway.example",
      fetch: async (url, init = {}) => {
        if (String(url).endsWith("/health/ready")) {
          return Response.json({
            ok: true,
            service: "caveman-proxy",
            schema: "caveman.proxy.health.v1",
            adapters: 4,
          });
        }
        exports.push(JSON.parse(init.body));
        return new Response(null, { status: 202 });
      },
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    });
    assert.equal(result.text, "root only");
    await new Promise((resolve) => setImmediate(resolve));
    const spans = exports[0].resourceSpans[0].scopeSpans[0].spans;
    assert.equal(spans.length, 1);
    assert.deepEqual(invocationTreeOutcomeAttributes(spans[0]), zeroInvocationTreeOutcomes);
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("root invocation reports exact parallel outcomes without leaking cap magnitudes", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave-parallel-tree-outcomes-key";
  const parent = distinctParallelDelegates("trace_tree_parallel");
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage([
      fauxToolCall("trace_tree_parallel_a", { task: "first" }, { id: "trace-tree-parallel-a" }),
      fauxToolCall("trace_tree_parallel_b", { task: "second" }, { id: "trace-tree-parallel-b" }),
    ]),
    fauxAssistantMessage("child A"),
    fauxAssistantMessage("child B"),
    fauxAssistantMessage("root answer"),
  ]);
  const exports = [];
  try {
    const result = await run(parent, "parallel descendants", {
      ensureRuntime: false,
      gatewayURL: "https://gateway.example",
      fetch: async (url, init = {}) => {
        if (String(url).endsWith("/health/ready")) {
          return Response.json({
            ok: true,
            service: "caveman-proxy",
            schema: "caveman.proxy.health.v1",
            adapters: 4,
          });
        }
        exports.push(JSON.parse(init.body));
        return new Response(null, { status: 202 });
      },
      model: faux.getModel(),
      maxSubagentInvocations: 997,
      maxConcurrentSubagents: 991,
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    });
    assert.equal(result.text, "root answer");
    await new Promise((resolve) => setImmediate(resolve));
    const spans = exports[0].resourceSpans[0].scopeSpans[0].spans;
    const rootSpan = spans.find((span) => span.parentSpanId === "");
    assert.deepEqual(invocationTreeOutcomeAttributes(rootSpan), {
      "cave.agent.tree.admitted_descendants": "2",
      "cave.agent.tree.peak_active_descendants": "2",
      "cave.agent.tree.invocation_limit_rejections": "0",
      "cave.agent.tree.concurrency_limit_rejections": "0",
    });
    for (const childSpan of spans.filter((span) => span.parentSpanId !== "")) {
      assert.deepEqual(invocationTreeOutcomeAttributes(childSpan), {});
    }
    const attributeValues = spans.flatMap((span) => span.attributes)
      .flatMap((attribute) => Object.values(attribute.value));
    assert.equal(attributeValues.includes("997"), false);
    assert.equal(attributeValues.includes("991"), false);
    assert.equal(spans.some((span) => span.attributes.some((attribute) =>
      /(?:max|limit_value|threshold)/.test(attribute.key))), false);
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("deep descendants share the root invocation outcome ledger", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave-deep-tree-outcomes-key";
  const leaf = agent({
    id: "trace-tree-depth-leaf",
    instructions: "Answer leaf.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const middle = agent({
    id: "trace-tree-depth-middle",
    instructions: "Delegate to leaf.",
    model: "anthropic/claude-haiku-4-5",
    tools: [subagent({
      name: "trace_tree_depth_inner",
      description: "Delegate inward.",
      agent: leaf,
      maxCalls: 1,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "fixture",
  });
  const root = agent({
    id: "trace-tree-depth-root",
    instructions: "Delegate to middle.",
    model: auto(),
    tools: [subagent({
      name: "trace_tree_depth_outer",
      description: "Delegate outward.",
      agent: middle,
      maxCalls: 1,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("trace_tree_depth_outer", { task: "middle" }, { id: "trace-tree-depth-outer" })),
    fauxAssistantMessage(fauxToolCall("trace_tree_depth_inner", { task: "leaf" }, { id: "trace-tree-depth-inner" })),
    fauxAssistantMessage("leaf answer"),
    fauxAssistantMessage("middle answer"),
    fauxAssistantMessage("root answer"),
  ]);
  const exports = [];
  try {
    const result = await run(root, "deep descendants", {
      ensureRuntime: false,
      gatewayURL: "https://gateway.example",
      fetch: async (url, init = {}) => {
        if (String(url).endsWith("/health/ready")) {
          return Response.json({
            ok: true,
            service: "caveman-proxy",
            schema: "caveman.proxy.health.v1",
            adapters: 4,
          });
        }
        exports.push(JSON.parse(init.body));
        return new Response(null, { status: 202 });
      },
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    });
    assert.equal(result.text, "root answer");
    await new Promise((resolve) => setImmediate(resolve));
    const spans = exports[0].resourceSpans[0].scopeSpans[0].spans;
    const rootSpan = spans.find((span) => span.parentSpanId === "");
    assert.deepEqual(invocationTreeOutcomeAttributes(rootSpan), {
      "cave.agent.tree.admitted_descendants": "2",
      "cave.agent.tree.peak_active_descendants": "2",
      "cave.agent.tree.invocation_limit_rejections": "0",
      "cave.agent.tree.concurrency_limit_rejections": "0",
    });
    assert.equal(spans.filter((span) => span.parentSpanId !== "").length, 2);
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("root invocation separates total and concurrency rejection outcomes", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave-tree-rejection-outcomes-key";
  try {
    for (const variant of [
      {
        name: "invocation",
        options: { maxSubagentInvocations: 1 },
        expected: {
          "cave.agent.tree.admitted_descendants": "1",
          "cave.agent.tree.peak_active_descendants": "1",
          "cave.agent.tree.invocation_limit_rejections": "1",
          "cave.agent.tree.concurrency_limit_rejections": "0",
        },
      },
      {
        name: "concurrency",
        options: { maxConcurrentSubagents: 1 },
        expected: {
          "cave.agent.tree.admitted_descendants": "1",
          "cave.agent.tree.peak_active_descendants": "1",
          "cave.agent.tree.invocation_limit_rejections": "0",
          "cave.agent.tree.concurrency_limit_rejections": "1",
        },
      },
    ]) {
      const parent = distinctParallelDelegates(`trace_tree_reject_${variant.name}`);
      const faux = fauxAnthropic();
      faux.setResponses([
        fauxAssistantMessage([
          fauxToolCall(`trace_tree_reject_${variant.name}_a`, { task: "first" }, { id: `trace-tree-reject-${variant.name}-a` }),
          fauxToolCall(`trace_tree_reject_${variant.name}_b`, { task: "second" }, { id: `trace-tree-reject-${variant.name}-b` }),
        ]),
        fauxAssistantMessage("one child answer"),
        fauxAssistantMessage("root answer"),
      ]);
      const exports = [];
      const result = await run(parent, `reject ${variant.name}`, {
        ensureRuntime: false,
        gatewayURL: "https://gateway.example",
        fetch: async (url, init = {}) => {
          if (String(url).endsWith("/health/ready")) {
            return Response.json({
              ok: true,
              service: "caveman-proxy",
              schema: "caveman.proxy.health.v1",
              adapters: 4,
            });
          }
          exports.push(JSON.parse(init.body));
          return new Response(null, { status: 202 });
        },
        model: faux.getModel(),
        streamFn: faux.provider.streamSimple.bind(faux.provider),
        ...variant.options,
      });
      assert.equal(result.text, "root answer");
      await new Promise((resolve) => setImmediate(resolve));
      const spans = exports[0].resourceSpans[0].scopeSpans[0].spans;
      const rootSpan = spans.find((span) => span.parentSpanId === "");
      assert.deepEqual(invocationTreeOutcomeAttributes(rootSpan), variant.expected);
    }
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("pre-admission call and wallet rejections do not become tree-limit outcomes", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave-pre-admission-outcomes-key";
  const admitted = agent({
    id: "trace-pre-admission-child",
    instructions: "Return.",
    model: "anthropic/claude-haiku-4-5",
    output: output({ maxTokens: 64 }),
    sandbox: "fixture",
  });
  const walletRejected = agent({
    id: "trace-pre-admission-wallet-child",
    instructions: "Must not start.",
    model: "anthropic/claude-haiku-4-5",
    output: output({ maxTokens: 64 }),
    sandbox: "fixture",
  });
  const root = agent({
    id: "trace-pre-admission-root",
    instructions: "Exercise pre-admission rejections.",
    model: auto(),
    output: output({ maxTokens: 64 }),
    tools: [
      subagent({
        name: "trace_pre_admission_child",
        description: "One admitted child.",
        agent: admitted,
        maxCalls: 1,
        maxCostUsd: 10,
        maxTokens: 1_000,
        maxContextTokens: 10_000,
      }),
      subagent({
        name: "trace_pre_admission_wallet",
        description: "Rejected before admission by wallet.",
        agent: walletRejected,
        maxCalls: 1,
        maxCostUsd: 10,
        maxTokens: 20_000,
        maxContextTokens: 10_000,
      }),
    ],
    sandbox: "required",
  });
  const parentFaux = fauxProvider({ provider: "openai" });
  let parentContext = "";
  parentFaux.setResponses([
    fauxAssistantMessage([
      fauxToolCall("trace_pre_admission_child", { task: "admit" }, { id: "trace-pre-admission-first" }),
      fauxToolCall("trace_pre_admission_child", { task: "duplicate" }, { id: "trace-pre-admission-duplicate" }),
      fauxToolCall("trace_pre_admission_wallet", { task: "oversized" }, { id: "trace-pre-admission-wallet" }),
    ]),
    (context) => {
      parentContext = JSON.stringify(context.messages);
      return fauxAssistantMessage("root recovered");
    },
  ]);
  const childFaux = fauxAnthropic();
  childFaux.setResponses([fauxAssistantMessage("child completed")]);
  const exports = [];
  try {
    const result = await run(root, "pre-admission ordering", {
      ensureRuntime: false,
      gatewayURL: "https://gateway.example",
      fetch: async (url, init = {}) => {
        if (String(url).endsWith("/health/ready")) {
          return Response.json({
            ok: true,
            service: "caveman-proxy",
            schema: "caveman.proxy.health.v1",
            adapters: 4,
          });
        }
        exports.push(JSON.parse(init.body));
        return new Response(null, { status: 202 });
      },
      model: parentFaux.getModel(),
      budget: { maxTokens: 10_000, onExhausted: "stop" },
      maxSubagentInvocations: 10,
      maxConcurrentSubagents: 10,
      streamFn(selected, context, options) {
        return selected.provider === "anthropic"
          ? childFaux.provider.streamSimple(selected, context, options)
          : parentFaux.provider.streamSimple(selected, context, options);
      },
    });
    assert.equal(result.text, "root recovered");
    assert.match(parentContext, /cave_subagent_call_budget/);
    assert.match(parentContext, /cave_subagent_wallet_unavailable/);
    await new Promise((resolve) => setImmediate(resolve));
    const spans = exports[0].resourceSpans[0].scopeSpans[0].spans;
    const rootSpan = spans.find((span) => span.parentSpanId === "");
    assert.deepEqual(invocationTreeOutcomeAttributes(rootSpan), {
      "cave.agent.tree.admitted_descendants": "1",
      "cave.agent.tree.peak_active_descendants": "1",
      "cave.agent.tree.invocation_limit_rejections": "0",
      "cave.agent.tree.concurrency_limit_rejections": "0",
    });
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("a depth rejection does not increment root tree-limit outcomes", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave-depth-rejection-outcomes-key";
  const leaf = agent({
    id: "trace-depth-rejection-leaf",
    instructions: "Must not start.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const middle = agent({
    id: "trace-depth-rejection-middle",
    instructions: "Try the depth-rejected leaf, then return.",
    model: "anthropic/claude-haiku-4-5",
    tools: [subagent({
      name: "trace_depth_rejection_inner",
      description: "Rejected by depth.",
      agent: leaf,
      maxCalls: 1,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "fixture",
  });
  const root = agent({
    id: "trace-depth-rejection-root",
    instructions: "Delegate once.",
    model: auto(),
    tools: [subagent({
      name: "trace_depth_rejection_outer",
      description: "Admitted outer child.",
      agent: middle,
      maxCalls: 1,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const faux = fauxAnthropic();
  let middleContext = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("trace_depth_rejection_outer", { task: "middle" }, { id: "trace-depth-rejection-outer" })),
    fauxAssistantMessage(fauxToolCall("trace_depth_rejection_inner", { task: "leaf" }, { id: "trace-depth-rejection-inner" })),
    (context) => {
      middleContext = JSON.stringify(context.messages);
      return fauxAssistantMessage("middle recovered");
    },
    fauxAssistantMessage("root answer"),
  ]);
  const exports = [];
  try {
    const result = await run(root, "depth ordering", {
      ensureRuntime: false,
      gatewayURL: "https://gateway.example",
      fetch: async (url, init = {}) => {
        if (String(url).endsWith("/health/ready")) {
          return Response.json({
            ok: true,
            service: "caveman-proxy",
            schema: "caveman.proxy.health.v1",
            adapters: 4,
          });
        }
        exports.push(JSON.parse(init.body));
        return new Response(null, { status: 202 });
      },
      model: faux.getModel(),
      maxSubagentDepth: 1,
      maxSubagentInvocations: 10,
      maxConcurrentSubagents: 10,
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    });
    assert.equal(result.text, "root answer");
    assert.match(middleContext, /cave_subagent_depth_limit/);
    await new Promise((resolve) => setImmediate(resolve));
    const spans = exports[0].resourceSpans[0].scopeSpans[0].spans;
    const rootSpan = spans.find((span) => span.parentSpanId === "");
    assert.deepEqual(invocationTreeOutcomeAttributes(rootSpan), {
      "cave.agent.tree.admitted_descendants": "1",
      "cave.agent.tree.peak_active_descendants": "1",
      "cave.agent.tree.invocation_limit_rejections": "0",
      "cave.agent.tree.concurrency_limit_rejections": "0",
    });
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("gateway runs export invocation hierarchy and route provider requests to the current span", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave-trace-test-key";
  const child = agent({
    id: "trace-child",
    instructions: "Never export this sensitive child instruction.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "trace-parent",
    instructions: "Never export this sensitive parent instruction.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate twice.",
      agent: child,
      maxCalls: 2,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("delegate", { task: "sensitive child task one" }, { id: "trace-1" })),
    fauxAssistantMessage("first child answer"),
    fauxAssistantMessage(fauxToolCall("delegate", { task: "sensitive child task two" }, { id: "trace-2" })),
    fauxAssistantMessage("second child answer"),
    fauxAssistantMessage("parent answer"),
  ]);
  const capture = routeAndHeaderCapture(faux);
  const exports = [];
  const exportScopes = [];
  let rotatedRouteKey = false;
  try {
    const result = await run(parent, "sensitive root input", {
      ensureRuntime: false,
      gatewayURL: "https://gateway.example",
      fetch: async (url, init = {}) => {
        if (String(url).endsWith("/health/ready")) {
          return Response.json({
            ok: true,
            service: "caveman-proxy",
            schema: "caveman.proxy.health.v1",
            adapters: 4,
          });
        }
        assert.equal(String(url), "https://gateway.example/otlp/v1/traces");
        assert.equal(init.method, "POST");
        assert.equal(init.headers["x-cave-api-key"], "cave-trace-test-key");
        exportScopes.push({ ...init.headers });
        exports.push(JSON.parse(init.body));
        return new Response(null, { status: 202 });
      },
      model: faux.getModel(),
      streamFn(model, context, options) {
        const response = capture.streamFn(model, context, options);
        if (!rotatedRouteKey) {
          // Route identity is immutable for the whole graph even if ambient
          // configuration rotates while provider and child work is in flight.
          process.env.CAVE_API_KEY = "rotated-after-route";
          rotatedRouteKey = true;
        }
        return response;
      },
    });
    assert.equal(result.text, "parent answer");
    await new Promise((resolve) => setImmediate(resolve));

    const rootHeaders = capture.headers.filter((headers) => headers["x-cave-agent"] === "trace-parent");
    const childHeaders = capture.headers.filter((headers) => headers["x-cave-agent"] === "trace-child");
    assert.equal(rootHeaders.length, 3);
    assert.equal(childHeaders.length, 2);
    const traceId = rootHeaders[0]["x-cave-trace-id"];
    const rootSpanId = rootHeaders[0]["x-cave-parent-span-id"];
    assert.match(traceId, /^[0-9a-f]{32}$/);
    assert.match(rootSpanId, /^[0-9a-f]{16}$/);
    assert.deepEqual(rootHeaders.map((headers) => headers["x-cave-trace-id"]), [traceId, traceId, traceId]);
    assert.deepEqual(rootHeaders.map((headers) => headers["x-cave-parent-span-id"]), [
      rootSpanId,
      rootSpanId,
      rootSpanId,
    ]);
    assert.deepEqual(childHeaders.map((headers) => headers["x-cave-trace-id"]), [traceId, traceId]);
    assert.deepEqual(capture.headers.map((headers) => headers["x-cave-api-key"]), [
      "cave-trace-test-key",
      "cave-trace-test-key",
      "cave-trace-test-key",
      "cave-trace-test-key",
      "cave-trace-test-key",
    ]);
    const childSpanIds = childHeaders.map((headers) => headers["x-cave-parent-span-id"]);
    assert.match(childSpanIds[0], /^[0-9a-f]{16}$/);
    assert.match(childSpanIds[1], /^[0-9a-f]{16}$/);
    assert.notEqual(childSpanIds[0], childSpanIds[1]);

    const spans = exports.flatMap((payload) => payload.resourceSpans)
      .flatMap((resource) => resource.scopeSpans)
      .flatMap((scope) => scope.spans);
    assert.equal(exports.length, 1);
    assert.equal(spans.length, 3);
    assert.deepEqual(exportScopes.map((headers) => headers["x-cave-agent"]), ["trace-parent"]);
    assert.deepEqual(new Set(exportScopes.map((headers) => headers["x-cave-workflow"])), new Set(["trace-parent"]));
    assert.equal(new Set(exportScopes.map((headers) => headers["x-cave-session"])).size, 1);
    const resourceAttributes = Object.fromEntries(exports[0].resourceSpans[0].resource.attributes.map((attribute) => [
      attribute.key,
      attribute.value.stringValue ?? attribute.value.intValue,
    ]));
    assert.equal(resourceAttributes["cave.telemetry.delivery_basis"], "attempted_unconfirmed");
    assert.equal(resourceAttributes["cave.invocation.batch.span_count"], "3");
    assert.equal(resourceAttributes["cave.invocation.batch.dropped_spans"], "0");
    const rootSpan = spans.find((span) => span.spanId === rootSpanId);
    assert.equal(rootSpan.traceId, traceId);
    assert.equal(rootSpan.parentSpanId, "");
    assert.equal(rootSpan.name, "invoke_agent");
    assert.equal(rootSpan.status.code, 1);
    assert.deepEqual(invocationGuardAttributes(rootSpan), {});
    for (const childSpanId of childSpanIds) {
      const childSpan = spans.find((span) => span.spanId === childSpanId);
      assert.equal(childSpan.traceId, traceId);
      assert.equal(childSpan.parentSpanId, rootSpanId);
      assert.equal(childSpan.name, "invoke_agent");
      assert.equal(childSpan.status.code, 1);
      assert.deepEqual(invocationGuardAttributes(childSpan), activeChildGuardWithoutRootBudget);
    }
    const guardJSON = JSON.stringify(childSpanIds.map((spanId) =>
      invocationGuardAttributes(spans.find((span) => span.spanId === spanId))));
    assert.doesNotMatch(guardJSON, /delegate|sensitive|10000|"10"/);
    const exportedJSON = JSON.stringify(exports);
    assert.doesNotMatch(exportedJSON, /sensitive root input|sensitive child task|sensitive parent instruction|sensitive child instruction/);
    assert.doesNotMatch(exportedJSON, /toolCalls|messages|prompt|instructions/);
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("child invocation guard manifest reflects effective root budget and optional fanout controls", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave-guard-manifest-key";
  const cases = [
    {
      name: "defaults",
      runOptions: {},
      childOptions: {},
      expected: { childSpend: "usd_pre_call", rootBudget: "absent", turnFanout: "absent", treeInvocations: "absent", treeConcurrency: "absent" },
    },
    {
      name: "usd",
      runOptions: { budget: { maxUsd: 50 }, breakers: {}, maxSubagentInvocations: 12 },
      childOptions: { maxCostUsd: 10, maxContextTokens: 10_000 },
      expected: { childSpend: "usd_pre_call", rootBudget: "usd", turnFanout: "active", treeInvocations: "active", treeConcurrency: "absent" },
    },
    {
      name: "tokens",
      runOptions: { budget: { maxTokens: 1_000_000 }, maxConcurrentSubagents: 3 },
      childOptions: { maxCostUsd: 10, maxTokens: 500_000, maxContextTokens: 10_000 },
      expected: { childSpend: "tokens_pre_call", rootBudget: "tokens", turnFanout: "absent", treeInvocations: "absent", treeConcurrency: "active" },
    },
    {
      name: "legacy-usd",
      runOptions: { maxCostUsd: 50, maxSubagentInvocations: 20, maxConcurrentSubagents: 4 },
      childOptions: { maxCostUsd: 10, maxContextTokens: 10_000 },
      expected: { childSpend: "usd_pre_call", rootBudget: "legacy_usd", turnFanout: "absent", treeInvocations: "active", treeConcurrency: "active" },
    },
  ];
  try {
    for (const variant of cases) {
      const child = agent({
        id: `guard-child-${variant.name}`,
        instructions: "Return the delegated result.",
        model: "anthropic/claude-haiku-4-5",
        sandbox: "fixture",
      });
      const parent = agent({
        id: `guard-parent-${variant.name}`,
        instructions: "Delegate once.",
        model: auto(),
        tools: [subagent({
          name: `guard_delegate_${variant.name}`,
          description: "Delegate one bounded task.",
          agent: child,
          ...variant.childOptions,
        })],
        sandbox: "required",
      });
      const faux = fauxAnthropic({
        models: [{ id: "claude-haiku-4-5", contextWindow: 200_000, maxTokens: 8_192 }],
      });
      faux.setResponses([
        fauxAssistantMessage(fauxToolCall(
          `guard_delegate_${variant.name}`,
          { task: `private task ${variant.name}` },
          { id: `guard-${variant.name}` },
        )),
        fauxAssistantMessage(`child answer ${variant.name}`),
        fauxAssistantMessage(`parent answer ${variant.name}`),
      ]);
      const capture = routeAndHeaderCapture(faux);
      const exports = [];
      const result = await run(parent, `private root ${variant.name}`, {
        ensureRuntime: false,
        gatewayURL: "https://gateway.example",
        fetch: async (url, init = {}) => {
          if (String(url).endsWith("/health/ready")) {
            return Response.json({
              ok: true,
              service: "caveman-proxy",
              schema: "caveman.proxy.health.v1",
              adapters: 4,
            });
          }
          exports.push(JSON.parse(init.body));
          return new Response(null, { status: 202 });
        },
        model: faux.getModel(),
        streamFn: capture.streamFn,
        ...variant.runOptions,
      });
      assert.equal(result.text, `parent answer ${variant.name}`);
      await new Promise((resolve) => setImmediate(resolve));
      assert.equal(exports.length, 1);
      const spans = exports[0].resourceSpans[0].scopeSpans[0].spans;
      const childSpan = spans.find((span) => span.parentSpanId !== "");
      const guard = invocationGuardAttributes(childSpan);
      assert.deepEqual(guard, {
        ...activeChildGuardWithoutRootBudget,
        "cave.guard.child_spend": variant.expected.childSpend,
        "cave.guard.root_budget": variant.expected.rootBudget,
        "cave.guard.turn_fanout": variant.expected.turnFanout,
        "cave.guard.tree_invocations": variant.expected.treeInvocations,
        "cave.guard.tree_concurrency": variant.expected.treeConcurrency,
      });
      const serializedGuard = JSON.stringify(guard);
      assert.doesNotMatch(serializedGuard, /private|guard_delegate|10000|500000|1000000|"12"|"20"|"3"|"4"|"10"|"50"/);
    }
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("large subagent fanout flushes one bounded root batch after children settle", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave-fanout-trace-key";
  const fanout = 24;
  const child = agent({
    id: "batch-child",
    instructions: "Answer delegated work.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "batch-parent",
    instructions: "Delegate all work in one turn.",
    model: auto(),
    tools: [subagent({
      name: "batch_delegate",
      description: "Delegate bounded parallel work.",
      agent: child,
      maxCalls: fanout,
      maxCostUsd: 100,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage(Array.from({ length: fanout }, (_, index) => fauxToolCall(
      "batch_delegate",
      { task: `child ${index}` },
      { id: `batch-${index}` },
    ))),
    ...Array.from({ length: fanout }, (_, index) => fauxAssistantMessage(`child answer ${index}`)),
    fauxAssistantMessage("batch parent answer"),
  ]);
  const capture = routeAndHeaderCapture(faux);
  const exports = [];
  try {
    const result = await run(parent, "fan out", {
      ensureRuntime: false,
      gatewayURL: "https://gateway.example",
      fetch: async (url, init = {}) => {
        if (String(url).endsWith("/health/ready")) {
          return Response.json({
            ok: true,
            service: "caveman-proxy",
            schema: "caveman.proxy.health.v1",
            adapters: 4,
          });
        }
        exports.push(JSON.parse(init.body));
        return new Response(null, { status: 202 });
      },
      model: faux.getModel(),
      streamFn: capture.streamFn,
    });
    assert.equal(result.text, "batch parent answer");
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(exports.length, 1);
    const spans = exports[0].resourceSpans[0].scopeSpans[0].spans;
    assert.equal(spans.length, fanout + 1);
    const rootSpan = spans.find((span) => span.parentSpanId === "");
    const childSpans = spans.filter((span) => span.parentSpanId !== "");
    assert.match(rootSpan.spanId, /^[0-9a-f]{16}$/);
    assert.equal(childSpans.length, fanout);
    assert.equal(new Set(childSpans.map((span) => span.spanId)).size, fanout);
    assert.deepEqual(new Set(childSpans.map((span) => span.parentSpanId)), new Set([rootSpan.spanId]));
    assert.deepEqual(
      new Set(childSpans.map((span) => JSON.stringify(invocationGuardAttributes(span)))),
      new Set([JSON.stringify(activeChildGuardWithoutRootBudget)]),
    );
    assert.equal(capture.headers.filter((headers) => headers["x-cave-agent"] === "batch-child").length, fanout);
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("root outcomes remain exact when the invocation batch drops child spans", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave-dropped-child-outcomes-key";
  const fanout = 1_025;
  const child = agent({
    id: "dropped-batch-child",
    instructions: "Return one bounded result.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "dropped-batch-parent",
    instructions: "Delegate one bounded turn.",
    model: auto(),
    tools: [subagent({
      name: "dropped_batch_delegate",
      description: "Delegate bounded parallel work.",
      agent: child,
      maxCalls: fanout,
      maxCostUsd: 10_000,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage(Array.from({ length: fanout }, (_, index) => fauxToolCall(
      "dropped_batch_delegate",
      { task: `private dropped child ${index}` },
      { id: `dropped-batch-${index}` },
    ))),
    ...Array.from({ length: fanout }, (_, index) => fauxAssistantMessage(`private answer ${index}`)),
    fauxAssistantMessage("dropped batch parent answer"),
  ]);
  const exports = [];
  try {
    const result = await run(parent, "private dropped batch root", {
      ensureRuntime: false,
      gatewayURL: "https://gateway.example",
      fetch: async (url, init = {}) => {
        if (String(url).endsWith("/health/ready")) {
          return Response.json({
            ok: true,
            service: "caveman-proxy",
            schema: "caveman.proxy.health.v1",
            adapters: 4,
          });
        }
        exports.push(JSON.parse(init.body));
        return new Response(null, { status: 202 });
      },
      model: faux.getModel(),
      maxModelCalls: 2,
      maxToolCalls: fanout,
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    });
    assert.equal(result.text, "dropped batch parent answer");
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(exports.length, 1);
    const resource = exports[0].resourceSpans[0];
    const resourceAttributes = Object.fromEntries(resource.resource.attributes.map((attribute) => [
      attribute.key,
      attribute.value.stringValue ?? attribute.value.intValue,
    ]));
    const spans = resource.scopeSpans[0].spans;
    const rootSpan = spans.find((span) => span.parentSpanId === "");
    const persistedChildren = spans.filter((span) => span.parentSpanId !== "").length;
    assert.equal(spans.length, 1_024);
    assert.equal(persistedChildren, 1_023);
    assert.equal(spans.at(-1), rootSpan, "the root outcome span must be appended after descendants settle");
    assert.equal(resourceAttributes["cave.invocation.batch.dropped_spans"], "2");
    assert.deepEqual(invocationTreeOutcomeAttributes(rootSpan), {
      "cave.agent.tree.admitted_descendants": "1025",
      "cave.agent.tree.peak_active_descendants": "1025",
      "cave.agent.tree.invocation_limit_rejections": "0",
      "cave.agent.tree.concurrency_limit_rejections": "0",
    });
    assert.equal(Number(invocationTreeOutcomeAttributes(rootSpan)[
      "cave.agent.tree.admitted_descendants"
    ]) > persistedChildren, true);
    assert.doesNotMatch(JSON.stringify(exports), /private dropped|private answer/);
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("observe-only runs emit neither trace headers nor invocation exports", async () => {
  const faux = fauxAnthropic();
  faux.setResponses([fauxAssistantMessage("provider answer")]);
  const capture = routeAndHeaderCapture(faux);
  let exportCalls = 0;
  const result = await run(definition(), "off gateway", {
    ensureRuntime: false,
    gatewayURL: "https://gateway.example",
    fetch: async (url) => {
      if (String(url).endsWith("/otlp/v1/traces")) exportCalls++;
      return Response.json({ ok: true, service: "impostor" });
    },
    model: faux.getModel(),
    streamFn: capture.streamFn,
  });
  assert.equal(result.mode, "observe-only");
  assert.equal(exportCalls, 0);
  assert.equal(capture.headers[0]["x-cave-trace-id"], undefined);
  assert.equal(capture.headers[0]["x-cave-parent-span-id"], undefined);
});

test("accountless gateway requests keep correlation headers but skip unauthenticated OTLP", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  delete process.env.CAVE_API_KEY;
  const faux = fauxAnthropic();
  faux.setResponses([fauxAssistantMessage("accountless gateway answer")]);
  const capture = routeAndHeaderCapture(faux);
  let exportCalls = 0;
  try {
    const result = await run(definition(), "accountless gateway", {
      ensureRuntime: false,
      gatewayURL: "https://gateway.example",
      fetch: async (url) => {
        if (String(url).endsWith("/otlp/v1/traces")) exportCalls++;
        return Response.json({
          ok: true,
          service: "caveman-proxy",
          schema: "caveman.proxy.health.v1",
          adapters: 4,
        });
      },
      model: faux.getModel(),
      streamFn: capture.streamFn,
    });
    assert.equal(result.mode, "optimized");
    assert.match(capture.headers[0]["x-cave-trace-id"], /^[0-9a-f]{32}$/);
    assert.match(capture.headers[0]["x-cave-parent-span-id"], /^[0-9a-f]{16}$/);
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(exportCalls, 0);
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("invocation export failure is bounded and cannot fail the paid request", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave-trace-test-key";
  const faux = fauxAnthropic();
  faux.setResponses([fauxAssistantMessage("paid answer survives")]);
  const capture = routeAndHeaderCapture(faux);
  let exportCalls = 0;
  let exportSignal;
  let exportPayload;
  try {
    const startedAt = performance.now();
    const result = await run(definition(), "export failure", {
      ensureRuntime: false,
      gatewayURL: "https://gateway.example",
      fetch: async (url, init = {}) => {
        if (String(url).endsWith("/health/ready")) {
          return Response.json({
            ok: true,
            service: "caveman-proxy",
            schema: "caveman.proxy.health.v1",
            adapters: 4,
          });
        }
        exportCalls++;
        exportSignal = init.signal;
        exportPayload = JSON.parse(init.body);
        throw new Error("collector unavailable");
      },
      model: faux.getModel(),
      streamFn: capture.streamFn,
    });
    assert.equal(result.text, "paid answer survives");
    assert.equal(performance.now() - startedAt < 1_000, true);
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(exportCalls, 1);
    assert.equal(exportSignal instanceof AbortSignal, true);
    const rootSpan = exportPayload.resourceSpans[0].scopeSpans[0].spans
      .find((span) => span.parentSpanId === "");
    assert.deepEqual(invocationTreeOutcomeAttributes(rootSpan), zeroInvocationTreeOutcomes);
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test(
  "http non-loopback gateway fails closed: observe-only, no x-cave-* headers",
  withMissingRuntimeBinaries(async () => {
    const previousKey = process.env.CAVE_API_KEY;
    process.env.CAVE_API_KEY = "cave-test-account-key";
    const faux = fauxAnthropic();
    faux.setResponses([fauxAssistantMessage("kept on the provider")]);
    const capture = routeAndHeaderCapture(faux);
    const native = faux.getModel().baseUrl;
    let probes = 0;
    try {
      const result = await run(definition(), "hostile gateway", {
        gatewayURL: "http://attacker.example",
        // Even a byte-perfect health identity must not matter: plain http to a
        // non-loopback host is rejected before any probe leaves the machine.
        fetch: async () => {
          probes++;
          return Response.json({
            ok: true,
            service: "caveman-proxy",
            schema: "caveman.proxy.health.v1",
            adapters: 4,
          });
        },
        model: faux.getModel(),
        streamFn: capture.streamFn,
      });
      assert.equal(result.mode, "observe-only");
      assert.equal(probes, 0);
      assert.equal(capture.selected[0].baseUrl, native);
      assert.doesNotMatch(capture.selected[0].baseUrl, /attacker\.example/);
      for (const sent of capture.headers) {
        assert.deepEqual(
          Object.keys(sent).filter((name) => name.toLowerCase().startsWith("x-cave-")),
          [],
        );
      }
    } finally {
      if (previousKey === undefined) delete process.env.CAVE_API_KEY;
      else process.env.CAVE_API_KEY = previousKey;
    }
  }),
);

test(
  "ensureRuntime false cannot bypass non-loopback gateway identity checks",
  async () => {
    const previousKey = process.env.CAVE_API_KEY;
    process.env.CAVE_API_KEY = "cave-test-account-key";
    const faux = fauxAnthropic();
    faux.setResponses([fauxAssistantMessage("kept on the provider")]);
    const capture = routeAndHeaderCapture(faux);
    const native = faux.getModel().baseUrl;
    let probes = 0;
    try {
      const result = await run(definition(), "caller-managed hostile gateway", {
        ensureRuntime: false,
        gatewayURL: "http://attacker.example",
        fetch: async () => {
          probes++;
          return Response.json({
            ok: true,
            service: "caveman-proxy",
            schema: "caveman.proxy.health.v1",
            adapters: 4,
          });
        },
        model: faux.getModel(),
        streamFn: capture.streamFn,
      });
      assert.equal(result.mode, "observe-only");
      assert.equal(probes, 0);
      assert.equal(capture.selected[0].baseUrl, native);
      assert.doesNotMatch(capture.selected[0].baseUrl, /attacker\.example/);
      for (const sent of capture.headers) {
        assert.deepEqual(
          Object.keys(sent).filter((name) => name.toLowerCase().startsWith("x-cave-")),
          [],
        );
      }
    } finally {
      if (previousKey === undefined) delete process.env.CAVE_API_KEY;
      else process.env.CAVE_API_KEY = previousKey;
    }
  },
);

test(
  "https non-loopback gateway with bogus health identity never receives traffic",
  withMissingRuntimeBinaries(async () => {
    const faux = fauxAnthropic();
    faux.setResponses([fauxAssistantMessage("kept on the provider")]);
    const capture = routeAndHeaderCapture(faux);
    const native = faux.getModel().baseUrl;
    const result = await run(definition(), "unverified gateway", {
      gatewayURL: "https://gateway.example",
      fetch: async () => Response.json({ ok: true, service: "impostor" }),
      model: faux.getModel(),
      streamFn: capture.streamFn,
    });
    assert.equal(result.mode, "observe-only");
    assert.equal(capture.selected[0].baseUrl, native);
    assert.doesNotMatch(capture.selected[0].baseUrl, /gateway\.example/);
    for (const sent of capture.headers) {
      assert.deepEqual(
        Object.keys(sent).filter((name) => name.toLowerCase().startsWith("x-cave-")),
        [],
      );
    }
  }),
);

test(
  "https non-loopback gateway passing the identity handshake is routed and optimized",
  withMissingRuntimeBinaries(async () => {
    const faux = fauxAnthropic();
    faux.setResponses([fauxAssistantMessage("routed remotely")]);
    const capture = routeAndHeaderCapture(faux);
    const result = await run(definition(), "verified remote gateway", {
      gatewayURL: "https://gateway.example",
      fetch: async () => Response.json({
        ok: true,
        service: "caveman-proxy",
        schema: "caveman.proxy.health.v1",
        adapters: 4,
      }),
      model: faux.getModel(),
      streamFn: capture.streamFn,
    });
    assert.equal(result.text, "routed remotely");
    assert.equal(result.mode, "optimized");
    assert.equal(capture.selected[0].baseUrl, "https://gateway.example/anthropic");
    assert.equal(typeof capture.headers[0]["x-cave-session"], "string");
  }),
);

test(
  "IPv6 loopback gateway stays loopback: degrades to observe-only, never rejected as non-https",
  withMissingRuntimeBinaries(async () => {
    // Regression: WHATWG URL yields hostname "[::1]" for http://[::1], so the
    // classifier must accept the bracketed form. A misclassification would send
    // this down the non-loopback branch and throw a requires-https error
    // instead of degrading like any other unreachable local proxy.
    const faux = fauxAnthropic();
    faux.setResponses([fauxAssistantMessage("kept on the provider")]);
    const capture = routeAndHeaderCapture(faux);
    const native = faux.getModel().baseUrl;
    const result = await run(definition(), "ipv6 loopback", {
      gatewayURL: "http://[::1]:1",
      fetch: async () => {
        throw new Error("not ready");
      },
      model: faux.getModel(),
      streamFn: capture.streamFn,
    });
    assert.equal(result.mode, "observe-only");
    assert.equal(capture.selected[0].baseUrl, native);
    for (const sent of capture.headers) {
      assert.deepEqual(
        Object.keys(sent).filter((name) => name.toLowerCase().startsWith("x-cave-")),
        [],
      );
    }
  }),
);

test(
  "locked plan refuses to degrade when the gateway is unreachable",
  withMissingRuntimeBinaries(async () => {
    const faux = fauxAnthropic();
    faux.setResponses([fauxAssistantMessage("must not spend")]);
    const capture = routeCapture(faux);
    await assert.rejects(
      runAgentInternal(definition(), "locked turn", {
        gatewayURL: "http://127.0.0.1:1",
        fetch: async () => {
          throw new Error("not ready");
        },
        candidatePlan: {
          schema_version: 1,
          plan_id: "gateway-required",
          model: "anthropic/faux-1",
          reasoning: "low",
          segment_routes: [],
          budgets: {
            instructions: 1_000,
            tools: 1_000,
            memory: 1_000,
            history: 1_000,
            results_artifacts: 1_000,
            reasoning: 1_000,
            output: 4_096,
            retry_cascade_reserve: 0,
          },
          recovery: { namespace: "gateway-required", tools: [] },
          fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
        },
        model: faux.getModel(),
        streamFn: capture.streamFn,
      }),
      /cave_gateway_required_for_locked_plan/,
    );
    assert.equal(faux.state.callCount, 0);
    assert.equal(capture.selected.length, 0);
  }),
);

test("local runtime auto-start launches Caveman CLI and waits for readiness", async () => {
  const directory = await mkdtemp(resolve(tmpdir(), "caveman-runtime-autostart-"));
  const marker = resolve(directory, "ready");
  const cli = resolve(directory, "caveman");
  const proxy = resolve(directory, "caveman-proxy");
  const previousCLI = process.env.CAVEMAN_CLI_BIN;
  const previousProxy = process.env.CAVEMAN_PROXY_BIN;
  const previousUnrelated = process.env.UNRELATED_SECRET;
  const previousAnthropic = process.env.ANTHROPIC_API_KEY;
  const previousCaveKey = process.env.CAVE_API_KEY;
  await writeFile(
    cli,
    `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
if (process.argv[2] !== "start") process.exit(2);
if (process.env.UNRELATED_SECRET || process.env.ANTHROPIC_API_KEY || process.env.CAVE_API_KEY) process.exit(9);
writeFileSync(${JSON.stringify(marker)}, "ready");
`,
    { mode: 0o755 },
  );
  await writeFile(
    proxy,
    `#!/usr/bin/env node
if (process.argv[2] !== "status") process.exit(2);
if (process.env.UNRELATED_SECRET || process.env.ANTHROPIC_API_KEY || process.env.CAVE_API_KEY) process.exit(9);
const port = Number(process.argv[process.argv.indexOf("--port") + 1]);
process.stdout.write(JSON.stringify({
  owner: "start",
  instance_token: "0123456789abcdef0123456789abcdef",
  pid: process.pid,
  port,
}));
`,
    { mode: 0o755 },
  );
  process.env.CAVEMAN_CLI_BIN = cli;
  process.env.CAVEMAN_PROXY_BIN = proxy;
  process.env.UNRELATED_SECRET = "must-not-reach-runtime-control";
  process.env.ANTHROPIC_API_KEY = "must-not-reach-runtime-control";
  process.env.CAVE_API_KEY = "must-not-reach-runtime-control";
  const faux = fauxProvider();
  faux.setResponses([fauxAssistantMessage("runtime ready")]);
  try {
    const result = await run(definition(), "runtime check", {
      gatewayURL: "http://127.0.0.1:2",
      fetch: async () => {
        try {
          await readFile(marker, "utf8");
          return Response.json({
            ok: true,
            service: "caveman-proxy",
            schema: "caveman.proxy.health.v1",
            adapters: 4,
          });
        } catch {
          throw new Error("not ready");
        }
      },
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    });
    assert.equal(result.text, "runtime ready");
    assert.equal(faux.state.callCount, 1);
  } finally {
    if (previousCLI === undefined) delete process.env.CAVEMAN_CLI_BIN;
    else process.env.CAVEMAN_CLI_BIN = previousCLI;
    if (previousProxy === undefined) delete process.env.CAVEMAN_PROXY_BIN;
    else process.env.CAVEMAN_PROXY_BIN = previousProxy;
    if (previousUnrelated === undefined) delete process.env.UNRELATED_SECRET;
    else process.env.UNRELATED_SECRET = previousUnrelated;
    if (previousAnthropic === undefined) delete process.env.ANTHROPIC_API_KEY;
    else process.env.ANTHROPIC_API_KEY = previousAnthropic;
    if (previousCaveKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousCaveKey;
    await rm(directory, { recursive: true, force: true });
  }
});

test("programmatic run fails before provider call when required tool sandbox has no entry path", async () => {
  const faux = fauxProvider();
  faux.setResponses([fauxAssistantMessage("must not run")]);
  const required = agent({
    id: "required-sandbox",
    instructions: "Use tool.",
    model: auto(),
    tools: [
      tool({
        name: "read_value",
        description: "Read value.",
        input: schema.object({}),
        effect: "read",
        execute: () => "unsafe in-process value",
      }),
    ],
  });
  await assert.rejects(
    run(required, "read", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    }),
    /cave_tool_sandbox_entry_required/,
  );
  assert.equal(faux.state.callCount, 0);
});

test("provider-visible cache drift fails request open to original frozen prefix", async () => {
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("lookup_policy", { region: "EU" }, { id: "lookup-1" })),
    fauxAssistantMessage("done"),
  ]);
  const model = {
    ...faux.getModel(),
    api: "anthropic-messages",
    provider: "anthropic",
  };
  let turn = 0;
  const forwarded = [];
  const streamFn = (selected, context, options) => {
    const text = turn++ === 0 ? context.systemPrompt : `${context.systemPrompt}\nMUTATED`;
    const payload = {
      model: selected.id,
      messages: [],
      max_tokens: 1024,
      stream: true,
      system: [{ type: "text", text, cache_control: { type: "ephemeral" } }],
      tools: (context.tools ?? []).map((item) => ({
        name: item.name,
        description: item.description,
        input_schema: item.parameters,
      })),
    };
    const outgoing = options.onPayload(payload, selected);
    assert.equal(outgoing instanceof Promise, false);
    forwarded.push({ payload: outgoing, headers: { ...options.headers } });
    return faux.provider.streamSimple(selected, context, options);
  };
  const result = await run(definition(), "drift test", {
    ensureRuntime: false,
    model,
    streamFn,
  });
  assert.equal(result.cacheBoundaryKnown, true);
  assert.equal(result.cacheBust, true);
  assert.deepEqual(forwarded[1].payload.system, forwarded[0].payload.system);
  assert.equal(forwarded[1].headers["x-cave-transforms"], "caveman.pass-through.v1");
});

test("mutated cached history byte fails open to prior provider-visible bytes", async () => {
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("lookup_policy", { region: "EU" }, { id: "history-1" })),
    fauxAssistantMessage(fauxToolCall("lookup_policy", { region: "US" }, { id: "history-2" })),
    fauxAssistantMessage("done"),
  ]);
  const model = { ...faux.getModel(), api: "anthropic-messages", provider: "anthropic" };
  const forwarded = [];
  const attempted = [];
  let call = 0;
  const streamFn = (selected, context, options) => {
    const payload = {
      model: selected.id,
      messages: structuredClone(context.messages),
      max_tokens: 1024,
      stream: true,
      system: context.systemPrompt,
      tools: context.tools.map((item) => ({
        name: item.name,
        description: item.description,
        input_schema: item.parameters,
      })),
    };
    if (call === 2 && payload.messages[0]?.role === "user") {
      payload.messages[0].content = "MUTATED HISTORY";
      payload.tools[0].description = "MUTATED TOOL";
    }
    call++;
    attempted.push(structuredClone(payload));
    const outgoing = options.onPayload(payload, selected);
    forwarded.push(structuredClone(outgoing));
    return faux.provider.streamSimple(selected, context, { ...options, onPayload: undefined });
  };
  const result = await run(definition(), "history drift", {
    ensureRuntime: false,
    model,
    streamFn,
    providerPayloadContract: "pi-on-payload-v1",
  });
  assert.equal(result.cacheBust, true);
  assert.deepEqual(forwarded[2].messages[0], forwarded[1].messages[0]);
  assert.deepEqual(forwarded[2].tools, forwarded[1].tools);
  assert.equal(forwarded[2].messages.length, attempted[2].messages.length);
  assert.deepEqual(forwarded[2].messages.slice(2), attempted[2].messages.slice(2));
});

test("Mistral Conversations cache drift fails open to prior provider-visible bytes", async () => {
  const faux = fauxProvider({ provider: "mistral", api: "mistral-conversations" });
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("lookup_policy", { region: "EU" }, { id: "mistral-cache-1" })),
    fauxAssistantMessage("done"),
  ]);
  const model = { ...faux.getModel(), api: "mistral-conversations", provider: "mistral" };
  const forwarded = [];
  let call = 0;
  const streamFn = (selected, context, options) => {
    const messages = [
      { role: "system", content: context.systemPrompt },
      ...structuredClone(context.messages),
    ];
    const payload = {
      model: selected.id,
      messages,
      tools: (context.tools ?? []).map((item) => ({
        type: "function",
        function: {
          name: item.name,
          description: call === 0 ? item.description : "MUTATED TOOL",
          parameters: item.parameters,
        },
      })),
      stream: true,
    };
    if (call === 1) payload.messages[0].content += "\nMUTATED";
    call++;
    const outgoing = options.onPayload(payload, selected);
    forwarded.push({ payload: structuredClone(outgoing), headers: { ...options.headers } });
    return faux.provider.streamSimple(selected, context, { ...options, onPayload: undefined });
  };
  const result = await run(definition(), "mistral cache drift", {
    ensureRuntime: false,
    model,
    streamFn,
    providerPayloadContract: "pi-on-payload-v1",
  });
  assert.equal(result.cacheBoundaryKnown, true);
  assert.equal(result.cacheBust, true);
  assert.deepEqual(forwarded[1].payload.messages[0], forwarded[0].payload.messages[0]);
  assert.deepEqual(forwarded[1].payload.tools, forwarded[0].payload.tools);
  assert.equal(forwarded[1].headers["x-cave-transforms"], undefined);
});

test("Google nested config drift fails open to prior system and tools", async () => {
  const faux = fauxProvider({ provider: "google", api: "google-generative-ai" });
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("lookup_policy", { region: "EU" }, { id: "google-cache-1" })),
    fauxAssistantMessage("done"),
  ]);
  const model = { ...faux.getModel(), api: "google-generative-ai", provider: "google" };
  const forwarded = [];
  let call = 0;
  const streamFn = (selected, context, options) => {
    const payload = {
      model: selected.id,
      contents: structuredClone(context.messages),
      config: {
        systemInstruction: call === 0 ? context.systemPrompt : `${context.systemPrompt}\nMUTATED`,
        tools: (context.tools ?? []).map((item) => ({
          name: item.name,
          description: call === 0 ? item.description : "MUTATED TOOL",
          parameters: item.parameters,
        })),
      },
      ...(call === 0 ? {} : { tools: [{ name: "NEW ROOT TOOL" }] }),
    };
    call++;
    const outgoing = options.onPayload(payload, selected);
    forwarded.push({ payload: structuredClone(outgoing), headers: { ...options.headers } });
    return faux.provider.streamSimple(selected, context, { ...options, onPayload: undefined });
  };
  const result = await run(definition(), "google cache drift", {
    ensureRuntime: false,
    model,
    streamFn,
    providerPayloadContract: "pi-on-payload-v1",
  });
  assert.equal(result.cacheBoundaryKnown, true);
  assert.equal(result.cacheBust, true);
  assert.deepEqual(forwarded[1].payload.config.systemInstruction, forwarded[0].payload.config.systemInstruction);
  assert.deepEqual(forwarded[1].payload.config.tools, forwarded[0].payload.config.tools);
  assert.equal("tools" in forwarded[1].payload, false);
  assert.equal(forwarded[1].headers["x-cave-transforms"], "caveman.pass-through.v1");
});

test("Bedrock toolConfig drift fails open to prior provider-visible tools", async () => {
  const faux = fauxProvider({ provider: "amazon-bedrock", api: "bedrock-converse-stream" });
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("lookup_policy", { region: "EU" }, { id: "bedrock-cache-1" })),
    fauxAssistantMessage("done"),
  ]);
  const model = { ...faux.getModel(), api: "bedrock-converse-stream", provider: "amazon-bedrock" };
  const forwarded = [];
  let call = 0;
  const streamFn = (selected, context, options) => {
    const payload = {
      modelId: selected.id,
      messages: structuredClone(context.messages),
      system: [{ text: context.systemPrompt }],
      toolConfig: {
        tools: (context.tools ?? []).map((item) => ({
          toolSpec: {
            name: item.name,
            description: call === 0 ? item.description : "MUTATED TOOL",
            inputSchema: { json: item.parameters },
          },
        })),
      },
    };
    call++;
    const outgoing = options.onPayload(payload, selected);
    forwarded.push({ payload: structuredClone(outgoing), headers: { ...options.headers } });
    return faux.provider.streamSimple(selected, context, { ...options, onPayload: undefined });
  };
  const result = await run(definition(), "bedrock cache drift", {
    ensureRuntime: false,
    model,
    streamFn,
    providerPayloadContract: "pi-on-payload-v1",
  });
  assert.equal(result.cacheBoundaryKnown, true);
  assert.equal(result.cacheBust, true);
  assert.deepEqual(forwarded[1].payload.toolConfig, forwarded[0].payload.toolConfig);
  assert.equal(forwarded[1].headers["x-cave-transforms"], undefined);
});

test("Pi Messages nested context drift fails open to prior provider-visible prefix", async () => {
  const faux = fauxAnthropic();
  faux.setResponses([fauxAssistantMessage("first"), fauxAssistantMessage("done")]);
  const model = faux.getModel();
  const conversation = createConversation();
  const forwarded = [];
  let call = 0;
  const streamFn = (selected, context, options) => {
    const payload = {
      model: selected.id,
      context: {
        systemPrompt: call === 0 ? context.systemPrompt : `${context.systemPrompt}\nMUTATED`,
        messages: structuredClone(context.messages),
        tools: (context.tools ?? []).map((item) => ({
          name: item.name,
          description: call === 0 ? item.description : "MUTATED TOOL",
          parameters: item.parameters,
        })),
      },
      options: {},
    };
    call++;
    const outgoing = options.onPayload(payload, { ...selected, api: "pi-messages" });
    forwarded.push(structuredClone(outgoing));
    return faux.provider.streamSimple(selected, context, { ...options, onPayload: undefined });
  };
  await run(definition(), "pi cache baseline", {
    ensureRuntime: false,
    conversation,
    model,
    streamFn,
    providerPayloadContract: "pi-on-payload-v1",
  });
  const result = await run(definition(), "pi cache drift", {
    ensureRuntime: false,
    conversation,
    model,
    streamFn,
    providerPayloadContract: "pi-on-payload-v1",
  });
  assert.equal(result.cacheBoundaryKnown, true);
  assert.equal(result.cacheBust, true);
  assert.equal(forwarded[1].context.systemPrompt, forwarded[0].context.systemPrompt);
  assert.deepEqual(forwarded[1].context.tools, forwarded[0].context.tools);
});

test("locked efficiency plan executes engine transform and proves byte-exact recovery", async () => {
  const store = await mkdtemp(resolve(tmpdir(), "cave-fake-engine-"));
  const previous = process.env.CAVE_FAKE_ENGINE_STORE;
  process.env.CAVE_FAKE_ENGINE_STORE = store;
  try {
    const longArtifact = Array.from({ length: 20 }, (_, index) =>
      `Section ${index}\nDetailed policy paragraph ${index} with enough repeated operational context.`).join("\n\n");
    const signedArtifact = JSON.stringify({
      payload: "release evidence must remain byte exact",
      signature: "a".repeat(64),
    });
    const defined = agent({
      id: "transformed",
      instructions: "Use supplied policy.",
      model: auto(),
      contexts: [
        context({
          id: "large.policy",
          kind: "artifact",
          source: longArtifact,
          stability: "build",
          safety: "S4",
          recovery: "exact_ccr",
        }),
        context({
          id: "release.proof",
          kind: "artifact",
          source: signedArtifact,
          stability: "build",
          safety: "S4",
          recovery: "exact_ccr",
        }),
      ],
      sandbox: "fixture",
    });
    let observed = "";
    const faux = fauxAnthropic();
    faux.setResponses([
      (context) => {
        observed = context.systemPrompt;
        return fauxAssistantMessage("done");
      },
    ]);
    const model = {
      ...faux.getModel(),
      api: "anthropic-messages",
      provider: "anthropic",
    };
    const streamFn = (selected, context, options) => {
      const payload = {
        system: [{ type: "text", text: context.systemPrompt, cache_control: { type: "ephemeral" } }],
        messages: [],
      };
      const outgoing = options.onPayload(payload, selected);
      assert.equal(outgoing instanceof Promise, false);
      return faux.provider.streamSimple(selected, context, options);
    };
    const result = await runAgentInternal(defined, "summarize", {
      ensureRuntime: false,
      engineBin: resolve("tests/fixtures/fake-engine.mjs"),
      candidatePlan: {
        schema_version: 1,
        plan_id: "transform.artifact",
        model: "anthropic/faux-1",
        reasoning: "low",
        segment_routes: [{
          segment_kind: "artifact",
          transform_id: "caveman.engine.text.v1",
          fallback: "original",
        }],
        budgets: {
          instructions: 1_000,
          tools: 0,
          memory: 0,
          history: 0,
          results_artifacts: 2_000,
          reasoning: 1_000,
          output: 500,
          retry_cascade_reserve: 0,
        },
        recovery: { namespace: "transformed", tools: ["cave_retrieve"] },
        fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
      },
      model,
      streamFn,
      providerPayloadContract: "pi-on-payload-v1",
    });
    assert.match(observed, /compressed summary/);
    assert.doesNotMatch(observed, /Detailed policy paragraph 19/);
    assert.match(observed, new RegExp(signedArtifact.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
    assert.deepEqual(result.evaluatedTransformIDs, ["caveman.engine.text.v1"]);
    assert.deepEqual(result.transformIDs, ["caveman.engine.text.v1"]);
    assert.deepEqual(result.transformFailures, []);
    assert.equal(result.recoveryResolved, true);
    assert.equal(result.cacheBoundaryKnown, true);
  } finally {
    if (previous === undefined) delete process.env.CAVE_FAKE_ENGINE_STORE;
    else process.env.CAVE_FAKE_ENGINE_STORE = previous;
    await rm(store, { recursive: true, force: true });
  }
});

test("transform afterTokens counts the wrapped bytes actually sent, not the engine's payload-only count", async () => {
  const store = await mkdtemp(resolve(tmpdir(), "cave-fake-engine-tokens-"));
  const previous = process.env.CAVE_FAKE_ENGINE_STORE;
  process.env.CAVE_FAKE_ENGINE_STORE = store;
  try {
    const longArtifact = Array.from({ length: 20 }, (_, index) =>
      `Section ${index}\nDetailed policy paragraph ${index} with enough repeated operational context.`).join("\n\n");
    const defined = agent({
      id: "transform-tokens",
      instructions: "Use supplied policy.",
      model: auto(),
      contexts: [
        context({
          id: "large.policy",
          kind: "artifact",
          source: longArtifact,
          stability: "build",
          safety: "S4",
          recovery: "exact_ccr",
        }),
      ],
      sandbox: "fixture",
    });
    let observed = "";
    const faux = fauxAnthropic();
    faux.setResponses([
      (context) => {
        observed = context.systemPrompt;
        return fauxAssistantMessage("done");
      },
    ]);
    const model = { ...faux.getModel(), api: "anthropic-messages", provider: "anthropic" };
    const streamFn = (selected, context, options) => {
      const payload = {
        system: [{ type: "text", text: context.systemPrompt, cache_control: { type: "ephemeral" } }],
        messages: [],
      };
      options.onPayload(payload, selected);
      return faux.provider.streamSimple(selected, context, options);
    };
    const result = await runAgentInternal(defined, "summarize", {
      ensureRuntime: false,
      engineBin: resolve("tests/fixtures/fake-engine-tokens.mjs"),
      candidatePlan: {
        schema_version: 1,
        plan_id: "transform.tokens",
        model: "anthropic/faux-1",
        reasoning: "low",
        segment_routes: [{
          segment_kind: "artifact",
          transform_id: "caveman.engine.text.v1",
          fallback: "original",
        }],
        budgets: {
          instructions: 1_000, tools: 0, memory: 0, history: 0,
          results_artifacts: 2_000, reasoning: 1_000, output: 500,
          retry_cascade_reserve: 0,
        },
        recovery: { namespace: "transform-tokens", tools: ["cave_retrieve"] },
        fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
      },
      model,
      streamFn,
      providerPayloadContract: "pi-on-payload-v1",
    });
    const applied = result.transformTrace.find((item) => item.outcome === "applied");
    assert.ok(applied, "expected an applied transform");
    assert.equal(applied.tokensBasis, "byte_derived");
    // The wrapped block actually placed into the request.
    const block = observed.match(/<cave-compressed[\s\S]*?<\/cave-compressed>/);
    assert.ok(block, "the wrapped compressed body must be on the wire");
    const wireBytes = Buffer.byteLength(block[0], "utf8");
    // afterTokens is the wire bytes/4 — wrapper and instruction line included —
    // NOT the engine's tokens_after of 2 (which omitted the wrapper).
    assert.equal(applied.afterTokens, Math.ceil(wireBytes / 4));
    assert.ok(applied.afterTokens > 2, "afterTokens must exceed the engine's payload-only count");
  } finally {
    if (previous === undefined) delete process.env.CAVE_FAKE_ENGINE_STORE;
    else process.env.CAVE_FAKE_ENGINE_STORE = previous;
    await rm(store, { recursive: true, force: true });
  }
});

test("locked tool-schema winner changes actual provider tools and preserves recovery", async () => {
  const store = await mkdtemp(resolve(tmpdir(), "cave-tool-schema-engine-"));
  const previous = process.env.CAVE_FAKE_ENGINE_STORE;
  process.env.CAVE_FAKE_ENGINE_STORE = store;
  try {
    const lookup = tool({
      name: "lookup",
      description: "Lookup a record. This annotation is intentionally verbose and removable.",
      input: {
        type: "object",
        title: "Lookup request",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
      effect: "read",
      result: "inline",
      execute: async () => "ok",
    });
    const defined = agent({
      id: "tool-schema-runtime",
      instructions: "Use tools.",
      model: auto(),
      tools: [lookup],
      sandbox: "fixture",
    });
    const faux = fauxAnthropic();
    faux.setResponses([fauxAssistantMessage("done")]);
    const model = { ...faux.getModel(), api: "anthropic-messages", provider: "anthropic" };
    let providerTools = [];
    const streamFn = (selected, context, options) => {
      const payload = {
        system: context.systemPrompt,
        tools: context.tools.map((item) => ({
          name: item.name,
          description: item.description,
          input_schema: item.parameters,
        })),
        messages: [],
      };
      providerTools = options.onPayload(payload, selected).tools;
      return faux.provider.streamSimple(selected, context, options);
    };
    const result = await runAgentInternal(defined, "lookup", {
      ensureRuntime: false,
      engineBin: resolve("tests/fixtures/fake-engine.mjs"),
      model,
      streamFn,
      providerPayloadContract: "pi-on-payload-v1",
      candidatePlan: {
        schema_version: 1,
        plan_id: "tool-schema",
        model: "anthropic/faux-1",
        reasoning: "low",
        segment_routes: [{
          segment_kind: "tool_schema",
          segment_id: "tool.lookup",
          transform_id: "caveman.engine.toolschema.v1",
          fallback: "original",
        }],
        budgets: {
          instructions: 1_000,
          tools: 1_000,
          memory: 0,
          history: 0,
          results_artifacts: 0,
          reasoning: 0,
          output: 100,
          retry_cascade_reserve: 0,
        },
        recovery: { namespace: "tool-schema", tools: ["cave_search_tools"] },
        fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
      },
    });
    assert.equal(providerTools[0].description, "Lookup a record.");
    assert.equal("title" in providerTools[0].input_schema, false);
    assert.equal(providerTools.some((item) => item.name === "cave_search_tools"), true);
    assert.deepEqual(result.transformIDs, ["caveman.engine.toolschema.v1"]);
    assert.equal(result.recoveryResolved, true);
  } finally {
    if (previous === undefined) delete process.env.CAVE_FAKE_ENGINE_STORE;
    else process.env.CAVE_FAKE_ENGINE_STORE = previous;
    await rm(store, { recursive: true, force: true });
  }
});

test("large auto tool result offloads through CCR and scoped recovery tool", async () => {
  const store = await mkdtemp(resolve(tmpdir(), "cave-fake-engine-tool-"));
  const previous = process.env.CAVE_FAKE_ENGINE_STORE;
  process.env.CAVE_FAKE_ENGINE_STORE = store;
  try {
    const payload = "full private tool result ".repeat(2_000);
    const defined = agent({
      id: "tool-offload",
      instructions: "Read result and recover detail.",
      model: auto(),
      tools: [tool({
        name: "large_read",
        description: "Return large result.",
        input: schema.object({}),
        effect: "read",
        result: "auto",
        execute: () => payload,
      })],
      sandbox: "fixture",
    });
    let recoveryHandle = "";
    let recovered = "";
    const faux = fauxProvider();
    faux.setResponses([
      fauxAssistantMessage(fauxToolCall("large_read", {}, { id: "call-large" })),
      (context) => {
        const serialized = JSON.stringify(context.messages);
        recoveryHandle = serialized.match(/ccr_[0-9a-f]{64}/)?.[0] ?? "";
        return fauxAssistantMessage(fauxToolCall(
          "cave_retrieve",
          { recovery_handle: recoveryHandle },
          { id: "call-recover" },
        ));
      },
      (context) => {
        recovered = JSON.stringify(context.messages);
        return fauxAssistantMessage("done");
      },
    ]);
    const result = await run(defined, "go", {
      ensureRuntime: false,
      engineBin: resolve("tests/fixtures/fake-engine.mjs"),
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    });
    assert.match(recoveryHandle, /^ccr_[0-9a-f]{64}$/);
    assert.match(recovered, /full private tool result/);
    assert.deepEqual(result.toolCalls, ["large_read", "cave_retrieve"]);
  } finally {
    if (previous === undefined) delete process.env.CAVE_FAKE_ENGINE_STORE;
    else process.env.CAVE_FAKE_ENGINE_STORE = previous;
    await rm(store, { recursive: true, force: true });
  }
});

test("locked history and tool-result winners transform real Pi transcript bytes", async () => {
  const store = await mkdtemp(resolve(tmpdir(), "cave-transcript-engine-"));
  const previous = process.env.CAVE_FAKE_ENGINE_STORE;
  process.env.CAVE_FAKE_ENGINE_STORE = store;
  try {
    const priorHistory = "prior provider-visible history ".repeat(40);
    const toolResult = JSON.stringify({
      rows: Array.from({ length: 80 }, (_, id) => ({ id, status: "ready" })),
    });
    const conversation = createConversation();
    const defined = agent({
      id: "transcript-routing",
      instructions: "Use lookup result.",
      model: auto(),
      tools: [tool({
        name: "lookup",
        description: "Return structured rows.",
        input: schema.object({}),
        effect: "read",
        result: "inline",
        execute: () => toolResult,
      })],
      sandbox: "fixture",
    });
    const faux = fauxAnthropic();
    faux.setResponses([
      fauxAssistantMessage("prior answer"),
      fauxAssistantMessage(fauxToolCall("lookup", {}, { id: "call-transcript" })),
      fauxAssistantMessage("done"),
    ]);
    const model = { ...faux.getModel(), api: "anthropic-messages", provider: "anthropic" };
    const providerContexts = [];
    const streamFn = (selected, context, options) => {
      providerContexts.push(structuredClone(context.messages));
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
    await run(defined, priorHistory, {
      ensureRuntime: false,
      conversation,
      model,
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    });
    const result = await runAgentInternal(defined, "perform lookup", {
      ensureRuntime: false,
      engineBin: resolve("tests/fixtures/fake-engine.mjs"),
      conversation,
      model,
      streamFn,
      providerPayloadContract: "pi-on-payload-v1",
      candidatePlan: {
        schema_version: 1,
        plan_id: "transcript-routes",
        model: "anthropic/faux-1",
        reasoning: "low",
        segment_routes: [
          {
            segment_kind: "history",
            transform_id: "caveman.engine.text.v1",
            fallback: "original",
          },
          {
            segment_kind: "tool_result",
            transform_id: "caveman.engine.json.v1",
            fallback: "original",
          },
        ],
        budgets: {
          instructions: 1_000,
          tools: 1_000,
          memory: 0,
          history: 2_000,
          results_artifacts: 2_000,
          reasoning: 0,
          output: 100,
          retry_cascade_reserve: 0,
        },
        recovery: { namespace: "transcript", tools: ["cave_retrieve"] },
        fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
      },
    });
    assert.match(JSON.stringify(providerContexts[0]), /caveman\.engine\.text\.v1/);
    assert.doesNotMatch(JSON.stringify(providerContexts[0]), /prior provider-visible history prior/);
    const expectedHandle = `ccr_${createHash("sha256").update(toolResult).digest("hex")}`;
    assert.match(JSON.stringify(providerContexts[1]), new RegExp(expectedHandle));
    assert.match(JSON.stringify(providerContexts[1]), /caveman\.engine\.json\.v1/);
    assert.deepEqual(result.transformIDs, [
      "caveman.engine.text.v1",
      "caveman.engine.json.v1",
    ]);
    assert.equal(result.contextIR.segments.some((segment) => segment.kind === "history"), true);
    assert.equal(result.contextIR.segments.some((segment) => segment.kind === "tool_result"), true);
    const messages = conversation.snapshot().messages;
    assert.equal(messages[0].content[0].text, priorHistory);
    assert.equal(
      messages.find((message) => message.role === "toolResult").content[0].text,
      toolResult,
    );
    assert.doesNotMatch(JSON.stringify(messages), /cave-compressed/);
  } finally {
    if (previous === undefined) delete process.env.CAVE_FAKE_ENGINE_STORE;
    else process.env.CAVE_FAKE_ENGINE_STORE = previous;
    await rm(store, { recursive: true, force: true });
  }
});

test("generated eval defaults unapproved and unknown graders fail closed", () => {
  const fixture = defineEval({
    id: "refund",
    input: "refund",
    quality: [{ type: "contains", fragments: ["refund"] }],
  });
  assert.equal(fixture.approved, false);
  assert.throws(
    () => defineEval({
      id: "unknown",
      input: "x",
      quality: [{ type: "unknown" }],
    }),
    /unknown grader/,
  );
});

test("artifact policy and subagent lower to ordinary isolated tool contracts", () => {
  const child = agent({
    id: "child",
    instructions: "Answer delegated task only.",
    model: auto(),
    sandbox: "fixture",
  });
  const delegated = subagent({
    name: "delegate",
    description: "Delegate isolated task.",
    agent: child,
    maxInputChars: 100,
  });
  assert.equal(delegated.kind, "tool");
  assert.equal(delegated.effect, "read");
  assert.equal(delegated.result, "auto");
  const paged = tool({
    name: "paged",
    description: "Page large artifact.",
    input: schema.object({}),
    effect: "read",
    result: artifact({ strategy: "page", recovery: "exact_ccr" }),
    execute: () => "large",
  });
  assert.equal(paged.result, "page");
});

test("framework reserves cave_ tool names before provider dispatch", () => {
  assert.throws(
    () => agent({
      id: "reserved-tool",
      instructions: "Invalid.",
      model: auto(),
      tools: [tool({
        name: "cave_retrieve",
        description: "Collide with framework recovery.",
        input: schema.object({}),
        effect: "read",
        execute: () => "wrong",
      })],
    }),
    /tool prefix cave_ is reserved/,
  );
});

test("agent graph rejects duplicate, cyclic, and over-depth definitions before spend", async () => {
  const sharedTool = tool({
    name: "lookup",
    description: "Lookup.",
    input: schema.object({}),
    effect: "read",
    execute: () => "value",
  });
  const duplicate = {
    ...agent({
      id: "duplicate-graph",
      instructions: "Invalid.",
      model: auto(),
      tools: [sharedTool],
      sandbox: "fixture",
    }),
    tools: [sharedTool, sharedTool],
  };
  const cyclic = {
    kind: "agent",
    id: "cyclic-graph",
    instructions: "Invalid.",
    model: auto(),
    reasoning: "low",
    tools: [],
    contexts: [],
    sandbox: "fixture",
  };
  cyclic.tools = [tool({
    name: "cycle",
    description: "Cycle.",
    input: schema.object({ task: schema.string() }),
    effect: "read",
    runtime: {
      kind: "subagent",
      definition: cyclic,
      maxInputChars: 100,
      maxCalls: 1,
      maxCostUsd: 1,
      maxContextTokens: 1_000,
    },
    execute: () => "never",
  })];
  let deep = agent({
    id: "deep-0",
    instructions: "Leaf.",
    model: auto(),
    sandbox: "fixture",
  });
  for (let index = 1; index <= 9; index++) {
    deep = agent({
      id: `deep-${index}`,
      instructions: "Delegate.",
      model: auto(),
      tools: [subagent({
        name: `level_${index}`,
        description: "Delegate.",
        agent: deep,
      })],
      sandbox: "fixture",
    });
  }
  const faux = fauxProvider();
  await assert.rejects(
    run(duplicate, "go", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    }),
    /cave_duplicate_tool_name/,
  );
  await assert.rejects(
    run(cyclic, "go", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    }),
    /cave_subagent_definition_cycle/,
  );
  await assert.rejects(
    run(deep, "go", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    }),
    /cave_subagent_depth_limit/,
  );
  assert.equal(faux.state.callCount, 0);
});

test("subagent accepts normal child tools but requires root sandbox entry before spend", async () => {
  const child = agent({
    id: "sandboxed-child-tools",
    instructions: "Use child tool.",
    model: "anthropic/claude-haiku-4-5",
    tools: [tool({
      name: "child_lookup",
      description: "Read child data.",
      input: schema.object({}),
      effect: "read",
      execute: () => "value",
    })],
  });
  const parent = agent({
    id: "sandboxed-child-parent",
    instructions: "Delegate.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const faux = fauxProvider();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("delegate", { task: "lookup" })),
  ]);
  await assert.rejects(
    run(parent, "go", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple,
    }),
    /cave_tool_sandbox_entry_required/,
  );
  assert.equal(faux.state.callCount, 0);
});

test("required parent propagates subprocess policy into fixture child tools", async () => {
  const child = agent({
    id: "fixture-child-tool",
    instructions: "Use child lookup.",
    model: "anthropic/claude-haiku-4-5",
    tools: [tool({
      name: "child_lookup",
      description: "Read child data.",
      input: schema.object({}),
      effect: "read",
      execute: () => "value",
    })],
    sandbox: "fixture",
  });
  const parent = agent({
    id: "required-fixture-parent",
    instructions: "Delegate.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
    })],
    sandbox: "required",
  });
  const faux = fauxAnthropic();
  await assert.rejects(
    run(parent, "go", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    }),
    /cave_tool_sandbox_entry_required/,
  );
  assert.equal(faux.state.callCount, 0);
});

test("nested child tool resolves exact agent path and runs in restricted subprocess", async () => {
  const faux = fauxAnthropic();
  let childContext = "";
  let parentContext = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall(
      "delegate_child",
      { task: "read child" },
      { id: "delegate-child" },
    )),
    fauxAssistantMessage(fauxToolCall("nested_read", {}, { id: "nested-read" })),
    (context) => {
      childContext = JSON.stringify(context.messages);
      return fauxAssistantMessage("child answer");
    },
    (context) => {
      parentContext = JSON.stringify(context.messages);
      return fauxAssistantMessage("parent answer");
    },
  ]);
  const result = await run(nestedSandboxAgent, "delegate", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/nested-sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple,
  });
  assert.equal(result.text, "parent answer");
  assert.match(childContext, /nested-child:modular-tool-ok/);
  assert.doesNotMatch(childContext, /root-wrong-tool/);
  assert.match(parentContext, /child answer/);
  assert.equal(faux.state.callCount, 4);
});

test("grandchild normal tool traverses full nested agent path", async () => {
  const faux = fauxAnthropic();
  let grandchildContext = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall(
      "delegate_child",
      { task: "delegate deeper" },
      { id: "delegate-child-deep" },
    )),
    fauxAssistantMessage(fauxToolCall(
      "delegate_deep",
      { task: "read deep" },
      { id: "delegate-deep" },
    )),
    fauxAssistantMessage(fauxToolCall("deep_read", {}, { id: "deep-read" })),
    (context) => {
      grandchildContext = JSON.stringify(context.messages);
      return fauxAssistantMessage("grandchild answer");
    },
    fauxAssistantMessage("child answer"),
    fauxAssistantMessage("parent answer"),
  ]);
  const result = await run(nestedSandboxAgent, "delegate deep", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/nested-sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple,
  });
  assert.equal(result.text, "parent answer");
  assert.match(grandchildContext, /nested-grandchild:modular-tool-ok/);
  assert.equal(faux.state.callCount, 6);
});

test("nested child tool keeps host filesystem denied", async () => {
  const faux = fauxAnthropic();
  let childContext = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall(
      "delegate_child",
      { task: "attempt home read" },
      { id: "delegate-child-home" },
    )),
    fauxAssistantMessage(fauxToolCall(
      "nested_home_read",
      {},
      { id: "nested-home-read" },
    )),
    (context) => {
      childContext = JSON.stringify(context.messages);
      return fauxAssistantMessage("blocked");
    },
    fauxAssistantMessage("parent answer"),
  ]);
  await run(nestedSandboxAgent, "delegate home read", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/nested-sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple,
  });
  assert.match(childContext, /Access to this API has been restricted|EACCES|EPERM/);
  assert.doesNotMatch(childContext, /BEGIN OPENSSH PRIVATE KEY/);
});

test("framework-owned subagent runs outside tool jail and aggregates nested usage", async () => {
  const child = agent({
    id: "runtime-child",
    instructions: "Answer delegated task only.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "runtime-parent",
    instructions: "Delegate once.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate isolated task.",
      agent: child,
      maxCalls: 1,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const faux = fauxAnthropic({
    models: [{
      id: "priced",
      reasoning: true,
      cost: { input: 1, output: 1, cacheRead: 0.1, cacheWrite: 1.2 },
    }],
  });
  let parentToolResult = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("delegate", { task: "child task" }, { id: "delegate-1" })),
    fauxAssistantMessage("child answer"),
    (context) => {
      parentToolResult = JSON.stringify(context.messages);
      return fauxAssistantMessage("parent answer");
    },
  ]);
  const selectedModels = [];
  const streamFn = (selected, context, options) => {
    selectedModels.push(`${selected.provider}/${selected.id}`);
    return faux.provider.streamSimple(selected, context, options);
  };
  const result = await run(parent, "parent task", {
    ensureRuntime: false,
    model: faux.getModel(),
    streamFn,
  });
  assert.equal(result.text, "parent answer");
  assert.equal(faux.state.callCount, 3);
  assert.match(parentToolResult, /child answer/);
  const nestedCost = Number(parentToolResult.match(/\\"cost_usd\\":([0-9.eE+-]+)/)?.[1]);
  const nestedInput = Number(parentToolResult.match(/\\"input_tokens\\":([0-9]+)/)?.[1]);
  assert.equal(Number.isFinite(nestedCost), true);
  assert.equal(Number.isSafeInteger(nestedInput), true);
  assert.equal(result.inputTokens > nestedInput, true);
  assert.equal(result.costUsd >= nestedCost, true);
  assert.equal(selectedModels[1], "anthropic/claude-haiku-4-5");
});

test("parallel duplicate delegation reserves maxCalls before either child awaits", async () => {
  const child = agent({
    id: "parallel-call-child",
    instructions: "Answer.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "parallel-call-parent",
    instructions: "Delegate.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate once.",
      agent: child,
      maxCalls: 1,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const faux = fauxAnthropic();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage([
      fauxToolCall("delegate", { task: "first" }, { id: "parallel-first" }),
      fauxToolCall("delegate", { task: "second" }, { id: "parallel-second" }),
    ]),
    fauxAssistantMessage("child answer"),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("parent answer");
    },
  ]);
  const result = await run(parent, "go", {
    ensureRuntime: false,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "parent answer");
  assert.equal(faux.state.callCount, 3);
  assert.match(observed, /cave_subagent_call_budget/);
});

function distinctParallelDelegates(prefix) {
  const childA = agent({
    id: `${prefix}-child-a`,
    instructions: "Answer A.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const childB = agent({
    id: `${prefix}-child-b`,
    instructions: "Answer B.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  return agent({
    id: `${prefix}-parent`,
    instructions: "Delegate in parallel.",
    model: auto(),
    tools: [
      subagent({ name: `${prefix}_a`, description: "Delegate A.", agent: childA, maxCalls: 1, maxCostUsd: 10, maxContextTokens: 10_000 }),
      subagent({ name: `${prefix}_b`, description: "Delegate B.", agent: childB, maxCalls: 1, maxCostUsd: 10, maxContextTokens: 10_000 }),
    ],
    sandbox: "required",
  });
}

test("distinct subagent tools cannot bypass the opt-in root total admission cap", async () => {
  const parent = distinctParallelDelegates("tree_total");
  const faux = fauxAnthropic();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage([
      fauxToolCall("tree_total_a", { task: "first" }, { id: "tree-total-a" }),
      fauxToolCall("tree_total_b", { task: "second" }, { id: "tree-total-b" }),
    ]),
    fauxAssistantMessage("one child answer"),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("parent answer");
    },
  ]);
  const result = await run(parent, "go", {
    ensureRuntime: false,
    model: faux.getModel(),
    maxSubagentInvocations: 1,
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "parent answer");
  assert.equal(faux.state.callCount, 3);
  assert.match(observed, /cave_subagent_invocation_limit/);
});

test("parallel distinct subagent tools share the opt-in root concurrency cap", async () => {
  const parent = distinctParallelDelegates("tree_concurrent");
  const faux = fauxAnthropic();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage([
      fauxToolCall("tree_concurrent_a", { task: "first" }, { id: "tree-concurrent-a" }),
      fauxToolCall("tree_concurrent_b", { task: "second" }, { id: "tree-concurrent-b" }),
    ]),
    fauxAssistantMessage("one child answer"),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("parent answer");
    },
  ]);
  const result = await run(parent, "go", {
    ensureRuntime: false,
    model: faux.getModel(),
    maxConcurrentSubagents: 1,
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "parent answer");
  assert.equal(faux.state.callCount, 3);
  assert.match(observed, /cave_subagent_concurrency_limit/);
});

test("deep descendants consume the same opt-in root admission ledger", async () => {
  const leaf = agent({
    id: "tree-deep-leaf",
    instructions: "Answer leaf.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const middle = agent({
    id: "tree-deep-middle",
    instructions: "Delegate to leaf.",
    model: "anthropic/claude-haiku-4-5",
    tools: [subagent({
      name: "tree_deep_inner",
      description: "Delegate inward.",
      agent: leaf,
      maxCalls: 1,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "fixture",
  });
  const root = agent({
    id: "tree-deep-root",
    instructions: "Delegate to middle.",
    model: auto(),
    tools: [subagent({
      name: "tree_deep_outer",
      description: "Delegate outward.",
      agent: middle,
      maxCalls: 1,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const faux = fauxAnthropic();
  let middleContext = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("tree_deep_outer", { task: "middle" }, { id: "tree-deep-outer" })),
    fauxAssistantMessage(fauxToolCall("tree_deep_inner", { task: "leaf" }, { id: "tree-deep-inner" })),
    (context) => {
      middleContext = JSON.stringify(context.messages);
      return fauxAssistantMessage("middle answer");
    },
    fauxAssistantMessage("root answer"),
  ]);
  const result = await run(root, "go", {
    ensureRuntime: false,
    model: faux.getModel(),
    maxSubagentInvocations: 1,
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "root answer");
  assert.equal(faux.state.callCount, 4);
  assert.match(middleContext, /cave_subagent_invocation_limit/);
});

test("a depth-rejected descendant does not consume a root-tree admission", async () => {
  const leaf = agent({
    id: "tree-depth-order-leaf",
    instructions: "Must not start.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const middle = agent({
    id: "tree-depth-order-middle",
    instructions: "Try the leaf, then return.",
    model: "anthropic/claude-haiku-4-5",
    tools: [subagent({
      name: "tree_depth_order_inner",
      description: "Rejected by depth.",
      agent: leaf,
      maxCalls: 1,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "fixture",
  });
  const sibling = agent({
    id: "tree-depth-order-sibling",
    instructions: "Return after the rejected descendant.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const root = agent({
    id: "tree-depth-order-root",
    instructions: "Call middle, then sibling.",
    model: auto(),
    tools: [
      subagent({ name: "tree_depth_order_outer", description: "Call middle.", agent: middle, maxCalls: 1, maxCostUsd: 10, maxContextTokens: 10_000 }),
      subagent({ name: "tree_depth_order_sibling", description: "Call sibling.", agent: sibling, maxCalls: 1, maxCostUsd: 10, maxContextTokens: 10_000 }),
    ],
    sandbox: "required",
  });
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("tree_depth_order_outer", { task: "middle" }, { id: "tree-depth-outer" })),
    fauxAssistantMessage(fauxToolCall("tree_depth_order_inner", { task: "leaf" }, { id: "tree-depth-inner" })),
    fauxAssistantMessage("middle recovered"),
    fauxAssistantMessage(fauxToolCall("tree_depth_order_sibling", { task: "sibling" }, { id: "tree-depth-sibling" })),
    fauxAssistantMessage("sibling admitted"),
    fauxAssistantMessage("root answer"),
  ]);
  const result = await run(root, "go", {
    ensureRuntime: false,
    model: faux.getModel(),
    maxSubagentDepth: 1,
    maxSubagentInvocations: 2,
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "root answer");
  assert.equal(faux.state.callCount, 6);
});

test("a wallet-rejected child does not consume a root-tree admission", async () => {
  const rejected = agent({
    id: "tree-wallet-order-rejected",
    instructions: "Must not start.",
    model: "anthropic/claude-haiku-4-5",
    output: output({ maxTokens: 64 }),
    sandbox: "fixture",
  });
  const admitted = agent({
    id: "tree-wallet-order-admitted",
    instructions: "Return after the rejected wallet.",
    model: "anthropic/claude-haiku-4-5",
    output: output({ maxTokens: 64 }),
    sandbox: "fixture",
  });
  const root = agent({
    id: "tree-wallet-order-root",
    instructions: "Try the oversized wallet, then the bounded wallet.",
    model: auto(),
    output: output({ maxTokens: 64 }),
    tools: [
      subagent({ name: "tree_wallet_order_rejected", description: "Oversized wallet.", agent: rejected, maxCalls: 1, maxCostUsd: 10, maxTokens: 20_000, maxContextTokens: 10_000 }),
      subagent({ name: "tree_wallet_order_admitted", description: "Bounded wallet.", agent: admitted, maxCalls: 1, maxCostUsd: 10, maxTokens: 1_000, maxContextTokens: 10_000 }),
    ],
    sandbox: "required",
  });
  const parentFaux = fauxProvider();
  parentFaux.setResponses([
    fauxAssistantMessage(fauxToolCall("tree_wallet_order_rejected", { task: "reject" }, { id: "tree-wallet-rejected" })),
    fauxAssistantMessage(fauxToolCall("tree_wallet_order_admitted", { task: "admit" }, { id: "tree-wallet-admitted" })),
    fauxAssistantMessage("root answer"),
  ]);
  const childFaux = fauxAnthropic();
  childFaux.setResponses([fauxAssistantMessage("child admitted")]);
  const result = await run(root, "go", {
    ensureRuntime: false,
    model: parentFaux.getModel(),
    budget: { maxTokens: 10_000, onExhausted: "stop" },
    maxSubagentInvocations: 1,
    streamFn(selected, context, options) {
      return selected.provider === "anthropic"
        ? childFaux.provider.streamSimple(selected, context, options)
        : parentFaux.provider.streamSimple(selected, context, options);
    },
  });
  assert.equal(result.text, "root answer");
  assert.equal(parentFaux.state.callCount, 3);
  assert.equal(childFaux.state.callCount, 1);
});

test("absent tree-wide subagent controls preserve existing parallel behavior", async () => {
  const parent = distinctParallelDelegates("tree_default");
  const faux = fauxAnthropic();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage([
      fauxToolCall("tree_default_a", { task: "first" }, { id: "tree-default-a" }),
      fauxToolCall("tree_default_b", { task: "second" }, { id: "tree-default-b" }),
    ]),
    fauxAssistantMessage("child A"),
    fauxAssistantMessage("child B"),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("parent answer");
    },
  ]);
  const result = await run(parent, "go", {
    ensureRuntime: false,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "parent answer");
  assert.equal(faux.state.callCount, 4);
  assert.doesNotMatch(observed, /cave_subagent_(?:invocation|concurrency)_limit/);
});

test("invalid tree-wide subagent controls fail before provider or tool execution", async () => {
  const parent = distinctParallelDelegates("tree_invalid");
  for (const [option, value, code] of [
    ["maxSubagentInvocations", 0, "cave_subagent_invocation_limit_invalid"],
    ["maxSubagentInvocations", Number.MAX_SAFE_INTEGER, "cave_subagent_invocation_limit_invalid"],
    ["maxConcurrentSubagents", 1.5, "cave_subagent_concurrency_limit_invalid"],
    ["maxConcurrentSubagents", Number.MAX_SAFE_INTEGER, "cave_subagent_concurrency_limit_invalid"],
  ]) {
    const faux = fauxAnthropic();
    await assert.rejects(
      run(parent, "go", {
        ensureRuntime: false,
        model: faux.getModel(),
        streamFn: faux.provider.streamSimple.bind(faux.provider),
        [option]: value,
      }),
      new RegExp(code),
    );
    assert.equal(faux.state.callCount, 0);
  }
});

test("runtime releases active tree capacity after child failure or abort without refunding total", async () => {
  const previousKey = process.env.CAVE_API_KEY;
  process.env.CAVE_API_KEY = "cave-tree-release-outcomes-key";
  try {
    for (const stopReason of ["error", "aborted"]) {
    const children = ["first", "second", "third"].map((name) => agent({
      id: `tree-release-${stopReason}-${name}`,
      instructions: `Answer ${name}.`,
      model: "anthropic/claude-haiku-4-5",
      sandbox: "fixture",
    }));
    const tools = children.map((child, index) => subagent({
      name: `tree_release_${stopReason}_${index + 1}`,
      description: `Delegate ${index + 1}.`,
      agent: child,
      maxCalls: 1,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    }));
    // Pi makes a turn sequential when any called tool has a sequential effect.
    // This trusted no-op lets the test prove executeSubagent's real finally
    // path between otherwise ordinary subagent calls.
    const sequence = tool({
      name: `tree_release_${stopReason}_sequence`,
      description: "Sequence this test turn.",
      input: schema.object({}),
      effect: "write",
      execute: () => "sequenced",
    });
    const parent = agent({
      id: `tree-release-${stopReason}-parent`,
      instructions: "Run all delegates.",
      model: auto(),
      tools: [...tools, sequence],
      sandbox: "host",
    });
    const parentFaux = fauxProvider({ provider: "openai" });
    parentFaux.setResponses([fauxAssistantMessage([
      fauxToolCall(`tree_release_${stopReason}_1`, { task: "fail" }, { id: `${stopReason}-first` }),
      fauxToolCall(`tree_release_${stopReason}_2`, { task: "succeed" }, { id: `${stopReason}-second` }),
      fauxToolCall(`tree_release_${stopReason}_3`, { task: "must block" }, { id: `${stopReason}-third` }),
      fauxToolCall(`tree_release_${stopReason}_sequence`, {}, { id: `${stopReason}-sequence` }),
    ])]);
    const childFaux = fauxAnthropic();
    childFaux.setResponses([
      fauxAssistantMessage("", { stopReason, errorMessage: `secret-terminal-detail-${stopReason}` }),
      fauxAssistantMessage("second child completed"),
      fauxAssistantMessage("third child must not start"),
    ]);
    const exports = [];
    await assert.rejects(
      run(parent, "go", {
        ensureRuntime: false,
        gatewayURL: "https://gateway.example",
        fetch: async (url, init = {}) => {
          if (String(url).endsWith("/health/ready")) {
            return Response.json({
              ok: true,
              service: "caveman-proxy",
              schema: "caveman.proxy.health.v1",
              adapters: 4,
            });
          }
          exports.push(JSON.parse(init.body));
          return new Response(null, { status: 202 });
        },
        model: parentFaux.getModel(),
        maxConcurrentSubagents: 1,
        maxSubagentInvocations: 2,
        streamFn(selected, context, options) {
          return selected.provider === "anthropic"
            ? childFaux.provider.streamSimple(selected, context, options)
            : parentFaux.provider.streamSimple(selected, context, options);
        },
      }),
      /provider_terminal_(?:error|aborted)|nested_usage_incomplete/,
    );
    assert.equal(parentFaux.state.callCount, 1);
    assert.equal(childFaux.state.callCount, 2, `${stopReason}: second child should start after active release and third should remain blocked by total`);
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(exports.length, 1);
    const spans = exports[0].resourceSpans[0].scopeSpans[0].spans;
    const rootSpan = spans.find((span) => span.parentSpanId === "");
    assert.deepEqual(invocationTreeOutcomeAttributes(rootSpan), {
      "cave.agent.tree.admitted_descendants": "2",
      "cave.agent.tree.peak_active_descendants": "1",
      "cave.agent.tree.invocation_limit_rejections": "1",
      "cave.agent.tree.concurrency_limit_rejections": "0",
    });
    assert.doesNotMatch(JSON.stringify(exports), /secret-terminal-detail|must block|succeed/);
    }
  } finally {
    if (previousKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousKey;
  }
});

test("parent cancellation aborts in-flight subagent spend", async () => {
  const child = agent({
    id: "cancel-child",
    instructions: "Answer delegated task only.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "cancel-parent",
    instructions: "Delegate once.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate isolated task.",
      agent: child,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const parentFaux = fauxProvider({
    models: [{
      id: "priced",
      cost: { input: 1, output: 1, cacheRead: 0.1, cacheWrite: 1.2 },
    }],
  });
  parentFaux.setResponses([
    fauxAssistantMessage(fauxToolCall("delegate", { task: "slow child" }, { id: "delegate-cancel" })),
  ]);
  const childFaux = fauxAnthropic({
    tokensPerSecond: 1,
    tokenSize: { min: 1, max: 1 },
  });
  childFaux.setResponses([fauxAssistantMessage("slow child response ".repeat(100))]);
  const controller = new AbortController();
  const execution = run(parent, "cancel task", {
    ensureRuntime: false,
    model: parentFaux.getModel(),
    signal: controller.signal,
    streamFn(selected, context, options) {
      return selected.provider === "anthropic"
        ? childFaux.provider.streamSimple(selected, context, options)
        : parentFaux.provider.streamSimple(selected, context, options);
    },
  });
  while (childFaux.state.callCount === 0) {
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const started = Date.now();
  controller.abort(new Error("test cancellation"));
  await assert.rejects(execution, /provider_terminal_(?:error|aborted)|cancel/i);
  assert.equal(Date.now() - started < 2_000, true);
  assert.equal(parentFaux.state.callCount, 1);
  assert.equal(childFaux.state.callCount, 1);
});

test("auto subagent inherits resolved parent model when caller supplies no override", async () => {
  const child = agent({
    id: "auto-child",
    instructions: "Answer child.",
    model: auto(),
    sandbox: "fixture",
  });
  const parent = agent({
    id: "pinned-parent",
    instructions: "Delegate.",
    model: "anthropic/claude-haiku-4-5",
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("delegate", { task: "child" }, { id: "delegate-auto" })),
    fauxAssistantMessage("child answer"),
    fauxAssistantMessage("parent answer"),
  ]);
  const selected = [];
  const result = await run(parent, "go", {
    ensureRuntime: false,
    streamFn(model, context, options) {
      selected.push(`${model.provider}/${model.id}`);
      return faux.provider.streamSimple(model, context, options);
    },
  });
  assert.equal(result.text, "parent answer");
  assert.deepEqual(selected, [
    "anthropic/claude-haiku-4-5",
    "anthropic/claude-haiku-4-5",
    "anthropic/claude-haiku-4-5",
  ]);
});

test("failed subagent usage stops parent before another provider call", async () => {
  const child = agent({
    id: "failed-child",
    instructions: "Fail.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "failed-child-parent",
    instructions: "Delegate.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxCalls: 2,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const parentFaux = fauxProvider();
  parentFaux.setResponses([
    fauxAssistantMessage(fauxToolCall("delegate", { task: "fail" }, { id: "delegate-fail" })),
    fauxAssistantMessage("must not spend"),
  ]);
  const childFaux = fauxAnthropic();
  childFaux.setResponses([
    fauxAssistantMessage("", { stopReason: "error", errorMessage: "child failed" }),
  ]);

  await assert.rejects(
    run(parent, "go", {
      ensureRuntime: false,
      model: parentFaux.getModel(),
      streamFn(selected, context, options) {
        return selected.provider === "anthropic"
          ? childFaux.provider.streamSimple(selected, context, options)
          : parentFaux.provider.streamSimple(selected, context, options);
      },
    }),
    /provider_terminal_error|nested_usage_incomplete/,
  );
  assert.equal(parentFaux.state.callCount, 1);
  assert.equal(childFaux.state.callCount, 1);
});

test("subagent with unavailable usage retains reservation and stops parent spend", async () => {
  const child = agent({
    id: "unmetered-child",
    instructions: "Return empty response.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "unmetered-child-parent",
    instructions: "Delegate.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxCalls: 2,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const faux = fauxAnthropic({
    models: [{
      id: "priced",
      cost: { input: 1, output: 1, cacheRead: 0.1, cacheWrite: 1.2 },
    }],
  });
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("delegate", { task: "empty" }, { id: "delegate-empty" })),
    fauxAssistantMessage("must not spend"),
  ]);
  let streamCalls = 0;

  await assert.rejects(
    run(parent, "go", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn(selected, context, options) {
        streamCalls += 1;
        if (streamCalls !== 2) return faux.provider.streamSimple(selected, context, options);
        const result = createAssistantMessageEventStream();
        const base = fauxAssistantMessage("unmetered child");
        const message = {
          ...base,
          api: selected.api,
          provider: selected.provider,
          model: selected.id,
          usage: { ...base.usage, reasoning: undefined },
        };
        queueMicrotask(() => {
          const partial = { ...message, content: [], stopReason: "pending" };
          result.push({ type: "start", partial: { ...partial } });
          partial.content = [{ type: "text", text: "" }];
          result.push({ type: "text_start", contentIndex: 0, partial: { ...partial } });
          partial.content[0].text = "unmetered child";
          result.push({
            type: "text_delta",
            contentIndex: 0,
            delta: "unmetered child",
            partial: { ...partial },
          });
          result.push({
            type: "text_end",
            contentIndex: 0,
            content: "unmetered child",
            partial: { ...partial },
          });
          result.push({ type: "done", reason: "stop", message });
          result.end(message);
        });
        return result;
      },
    }),
    /cave_nested_usage_incomplete|cave_provider_terminal_error/,
  );
  assert.equal(streamCalls, 2);
  assert.equal(faux.state.callCount, 1);
});

test("inconsistent intermediate usage retains reservation and stops spend", async () => {
  const child = agent({
    id: "intermediate-unmetered-child",
    instructions: "Call continue_once, then answer.",
    model: "anthropic/claude-haiku-4-5",
    tools: [tool({
      name: "continue_once",
      description: "Continue.",
      input: schema.object({}),
      effect: "read",
      execute: () => "continue",
    })],
    sandbox: "fixture",
  });
  const parent = agent({
    id: "intermediate-unmetered-parent",
    instructions: "Delegate.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "fixture",
  });
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("delegate", { task: "continue" }, { id: "unmetered-parent" })),
  ]);
  let streamCalls = 0;
  await assert.rejects(
    run(parent, "go", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn(selected, context, options) {
        streamCalls += 1;
        if (streamCalls === 2) {
          return manualToolCallStream(
            selected,
            fauxToolCall("continue_once", {}, { id: "unmetered-intermediate" }),
            {
              input: 0,
              output: 0,
              cacheRead: 0,
              cacheWrite: 0,
              reasoning: 0,
              totalTokens: 1_000,
              cost: {
                input: 0,
                output: 0,
                cacheRead: 0,
                cacheWrite: 0,
                total: 0,
              },
            },
          );
        }
        return faux.provider.streamSimple(selected, context, options);
      },
    }),
    /cave_subagent_spend_evidence_incomplete|cave_nested_usage_incomplete|cave_provider_terminal_error/,
  );
  assert.equal(streamCalls, 2);
  assert.equal(faux.state.callCount, 1);
});

test("subagent rejects provider-reported model identity drift", async () => {
  const child = agent({
    id: "identity-drift-child",
    instructions: "Answer.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "identity-drift-parent",
    instructions: "Delegate.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "fixture",
  });
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("delegate", { task: "answer" }, { id: "identity-parent" })),
  ]);
  let streamCalls = 0;
  await assert.rejects(
    run(parent, "go", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn(selected, context, options) {
        streamCalls += 1;
        if (streamCalls === 2) {
          return manualTextStream(selected, "wrong model", {
            provider: "openai",
            model: "gpt-5.4-mini",
          });
        }
        return faux.provider.streamSimple(selected, context, options);
      },
    }),
    /cave_subagent_model_identity_mismatch|cave_nested_usage_incomplete|cave_provider_terminal_error/,
  );
  assert.equal(streamCalls, 2);
  assert.equal(faux.state.callCount, 1);
});

test("subagent preflight budgets full child context before provider spend", async () => {
  const child = agent({
    id: "oversized-child",
    instructions: "large ".repeat(400),
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const parent = agent({
    id: "budgeting-parent",
    instructions: "Delegate once.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate isolated task.",
      agent: child,
      maxCalls: 1,
      maxCostUsd: 10,
      maxContextTokens: 32,
    })],
    sandbox: "required",
  });
  const faux = fauxProvider({
    models: [{
      id: "priced",
      cost: { input: 1, output: 1, cacheRead: 0.1, cacheWrite: 1.2 },
    }],
  });
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("delegate", { task: "short" }, { id: "delegate-budget" })),
    fauxAssistantMessage("budget blocked"),
  ]);
  const result = await run(parent, "parent task", {
    ensureRuntime: false,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "budget blocked");
  assert.equal(faux.state.callCount, 2, "oversized child must make zero provider calls");
});

test("subagent reserves every child model call before spend", async () => {
  const child = agent({
    id: "repeated-spend-child",
    instructions: "budget ".repeat(20_000),
    model: "anthropic/claude-haiku-4-5",
    tools: [tool({
      name: "continue_once",
      description: "Force another child model turn.",
      input: schema.object({}),
      effect: "read",
      execute: () => "continue",
    })],
    sandbox: "fixture",
  });
  const parent = agent({
    id: "repeated-spend-parent",
    instructions: "Delegate.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxCostUsd: 0.6,
      maxContextTokens: 500_000,
    })],
    sandbox: "fixture",
  });
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("delegate", { task: "loop" }, { id: "budget-parent" })),
    fauxAssistantMessage(fauxToolCall("continue_once", {}, { id: "budget-child" })),
    fauxAssistantMessage("must not spend"),
  ]);
  await assert.rejects(
    run(parent, "go", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    }),
    /cave_subagent_cost_budget|cave_nested_usage_incomplete|cave_provider_terminal_error/,
  );
  assert.equal(faux.state.callCount, 2);
});

test("grandchild provider calls reserve against ancestor cost cap", async () => {
  const grandchild = agent({
    id: "budget-grandchild",
    instructions: "Answer.",
    model: "anthropic/claude-haiku-4-5",
    sandbox: "fixture",
  });
  const child = agent({
    id: "budget-child",
    instructions: "budget ".repeat(20_000),
    model: "anthropic/claude-haiku-4-5",
    tools: [subagent({
      name: "delegate_deep",
      description: "Delegate deeper.",
      agent: grandchild,
      maxCostUsd: 10,
      maxContextTokens: 500_000,
    })],
    sandbox: "fixture",
  });
  const parent = agent({
    id: "budget-parent",
    instructions: "Delegate.",
    model: auto(),
    tools: [subagent({
      name: "delegate_child",
      description: "Delegate child.",
      agent: child,
      maxCostUsd: 0.6,
      maxContextTokens: 500_000,
    })],
    sandbox: "required",
  });
  const faux = fauxAnthropic();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall(
      "delegate_child",
      { task: "delegate deeper" },
      { id: "ancestor-parent" },
    )),
    fauxAssistantMessage(fauxToolCall(
      "delegate_deep",
      { task: "answer" },
      { id: "ancestor-child" },
    )),
    fauxAssistantMessage("must not spend"),
  ]);
  await assert.rejects(
    run(parent, "go", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    }),
    /cave_subagent_cost_budget|cave_nested_usage_incomplete|cave_provider_terminal_error/,
  );
  assert.equal(faux.state.callCount, 2);
});

function rootCostCapAgent(id) {
  return agent({
    id,
    instructions: "Answer.",
    model: auto(),
    tools: [tool({
      name: "continue_once",
      description: "Force another root model turn.",
      input: schema.object({}),
      effect: "read",
      execute: () => "continue",
    })],
    sandbox: "fixture",
  });
}

function pricedRootModel() {
  return fauxAnthropic({
    models: [{ id: "claude-haiku-4-5", contextWindow: 200_000, maxTokens: 8_192 }],
  }).getModel();
}

function rootCostCapStream(counter) {
  return (selected) => {
    counter.calls += 1;
    if (counter.calls > 1) return manualTextStream(selected, "must not spend");
    return manualToolCallStream(
      selected,
      fauxToolCall("continue_once", {}, { id: "root-cap-turn" }),
      {
        input: 1_000,
        output: 10,
        cacheRead: 0,
        cacheWrite: 0,
        reasoning: 0,
        totalTokens: 1_010,
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
      },
    );
  };
}

test("root maxCostUsd stops the run before the next model call", async () => {
  const defined = rootCostCapAgent("root-cost-cap");
  const model = pricedRootModel();
  // First call reserves exactly the catalog worst case, so the second reserve
  // exceeds the cap by the first call's settled cost.
  const maxCostUsd = catalogSearchCeiling(
    `${model.provider}/${model.id}`,
    model.contextWindow,
    model.maxTokens,
  );
  const counter = { calls: 0 };
  const events = [];
  for await (const event of stream(defined, "go", {
    ensureRuntime: false,
    maxCostUsd,
    model,
    streamFn: rootCostCapStream(counter),
  })) {
    events.push(event);
  }
  assert.equal(counter.calls, 1, "capped run must not make the second provider call");
  assert.equal(events.some((event) => event.type === "run_end"), false);
  const failure = events.at(-1);
  assert.equal(failure.type, "run_error");
  assert.equal(failure.message, "cave_run_cost_budget_exceeded");
  assert.ok(events.some((event) => event.type === "context_ready"));
  assert.ok(events.some((event) =>
    event.type === "pi" && event.event.type === "tool_execution_start"));
});

test("unset maxCostUsd leaves the same run uncapped", async () => {
  const defined = rootCostCapAgent("root-cost-uncapped");
  const counter = { calls: 0 };
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: pricedRootModel(),
    streamFn: rootCostCapStream(counter),
  });
  assert.equal(result.text, "must not spend");
  assert.equal(counter.calls, 2);
});

test("root maxCostUsd refuses a model the public catalog cannot price", async () => {
  const defined = rootCostCapAgent("root-cost-unpriced");
  const counter = { calls: 0 };
  await assert.rejects(
    run(defined, "go", {
      ensureRuntime: false,
      maxCostUsd: 1,
      model: fauxAnthropic().getModel(),
      streamFn: rootCostCapStream(counter),
    }),
    /cave_subagent_unpriced_budget/,
  );
  assert.equal(counter.calls, 0);
});

test("root maxCostUsd rejects non-positive and non-finite values", async () => {
  const defined = rootCostCapAgent("root-cost-invalid");
  const counter = { calls: 0 };
  for (const maxCostUsd of [0, -1, Number.NaN, Number.POSITIVE_INFINITY]) {
    await assert.rejects(
      run(defined, "go", {
        ensureRuntime: false,
        maxCostUsd,
        model: pricedRootModel(),
        streamFn: rootCostCapStream(counter),
      }),
      /run maxCostUsd must be positive/,
    );
    assert.throws(
      () => stream(defined, "go", { ensureRuntime: false, maxCostUsd }),
      /run maxCostUsd must be positive/,
    );
  }
  assert.equal(counter.calls, 0);
});

test("declared local memory remembers and recalls within namespace budget", async () => {
  const defined = agent({
    id: "memory-agent",
    instructions: "Use declared memory tools.",
    model: auto(),
    memory: memory({ namespace: "runtime-test", ttl: "1d", recallBudget: 20 }),
    sandbox: "fixture",
  });
  let observed = "";
  const faux = fauxProvider();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall(
      "cave_memory_remember",
      { text: "Refund window is fourteen days." },
      { id: "remember" },
    )),
    fauxAssistantMessage(fauxToolCall(
      "cave_memory_search",
      { query: "refund window" },
      { id: "search" },
    )),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("done");
    },
  ]);
  const root = await mkdtemp(resolve(tmpdir(), "cave-memory-"));
  try {
    const result = await run(defined, "remember and recall", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
      memory: { root },
    });
    assert.match(observed, /Refund window is fourteen days/);
    assert.deepEqual(result.toolCalls, ["cave_memory_remember", "cave_memory_search"]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("durable local memory is private to the current host user", {
  skip: process.platform === "win32",
}, async () => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-memory-permissions-"));
  const filePath = memoryFilePath({ root }, "private-agent", "private-ns");
  try {
    await mutateMemories(filePath, () => [{ text: "tenant secret", createdAt: Date.now() }]);
    assert.equal((await stat(filePath)).mode & 0o777, 0o600);
    assert.equal((await stat(resolve(root, "_", "private-agent"))).mode & 0o777, 0o700);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("failed durable memory replacement removes private temp data", async () => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-memory-cleanup-"));
  const filePath = memoryFilePath({ root }, "cleanup-agent", "cleanup-ns");
  try {
    // A directory at the final file path makes rename fail after temp write.
    await mkdir(filePath, { recursive: true });
    await assert.rejects(
      mutateMemories(filePath, () => [{ text: "must not remain in temp", createdAt: Date.now() }]),
    );
    const siblings = await readdir(resolve(root, "_", "cleanup-agent"));
    assert.deepEqual(siblings, ["cleanup-ns.json"]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

function memoryAgent(id, ttl = "30d") {
  return agent({
    id,
    instructions: "Use declared memory tools.",
    model: auto(),
    memory: memory({ namespace: "shared-ns", ttl, recallBudget: 80 }),
    sandbox: "fixture",
  });
}

async function rememberViaRun(defined, text, runOptions) {
  const faux = fauxProvider();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("cave_memory_remember", { text }, { id: "remember" })),
    fauxAssistantMessage("done"),
  ]);
  await run(defined, "remember", {
    ensureRuntime: false,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
    ...runOptions,
  });
}

async function searchViaRun(defined, query, runOptions) {
  const faux = fauxProvider();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("cave_memory_search", { query }, { id: "search" })),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("done");
    },
  ]);
  await run(defined, "search", {
    ensureRuntime: false,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
    ...runOptions,
  });
  return observed;
}

test("two agents sharing a namespace do not see each other's memories", async () => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-memory-iso-"));
  try {
    const agentA = memoryAgent("agent-a");
    const agentB = memoryAgent("agent-b");
    await rememberViaRun(agentA, "secret alpha belonging to agent a", { memory: { root } });
    // Agent B, same namespace, must NOT see agent A's memory (keyed by agentId).
    const bObserved = await searchViaRun(agentB, "secret alpha", { memory: { root } });
    assert.doesNotMatch(bObserved, /secret alpha belonging to agent a/);
    // Agent A still sees its own.
    const aObserved = await searchViaRun(agentA, "secret alpha", { memory: { root } });
    assert.match(aObserved, /secret alpha belonging to agent a/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("the same agent under different tenants keeps memories isolated", async () => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-memory-tenant-"));
  try {
    const shared = memoryAgent("multi-tenant-agent");
    await rememberViaRun(shared, "tenant one private note", { memory: { root, tenant: "tenant-1" } });
    const otherTenant = await searchViaRun(shared, "tenant one", { memory: { root, tenant: "tenant-2" } });
    assert.doesNotMatch(otherTenant, /tenant one private note/);
    const sameTenant = await searchViaRun(shared, "tenant one", { memory: { root, tenant: "tenant-1" } });
    assert.match(sameTenant, /tenant one private note/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("a remembered memory survives into a fresh store read — ttl:30d is durable", async () => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-memory-durable-"));
  try {
    const defined = memoryAgent("durable-agent");
    await rememberViaRun(defined, "the durable fact worth thirty days", { memory: { root } });
    // Simulate a NEW PROCESS: read the durable file straight off disk — no
    // process Map is involved, so a hit here is genuine persistence.
    const filePath = memoryFilePath({ root }, "durable-agent", "shared-ns");
    const persisted = await readMemories(filePath);
    assert.equal(persisted.some((entry) => entry.text === "the durable fact worth thirty days"), true);
    // And a fresh run still recalls it.
    const observed = await searchViaRun(defined, "durable fact", { memory: { root } });
    assert.match(observed, /the durable fact worth thirty days/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("a memory past its ttl is evicted on read", async () => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-memory-ttl-"));
  try {
    const defined = memoryAgent("ttl-agent", "1m");
    const filePath = memoryFilePath({ root }, "ttl-agent", "shared-ns");
    // Pre-seed one entry two minutes old (past the 1m ttl) and one fresh.
    await mutateMemories(filePath, () => [
      { text: "stale expired memory record", createdAt: Date.now() - 120_000 },
      { text: "fresh valid memory record", createdAt: Date.now() },
    ]);
    const observed = await searchViaRun(defined, "memory record", { memory: { root } });
    assert.doesNotMatch(observed, /stale expired memory record/);
    assert.match(observed, /fresh valid memory record/);
    // Evicted from DISK, not merely filtered from the response.
    const remaining = await readMemories(filePath);
    assert.equal(remaining.some((entry) => entry.text === "stale expired memory record"), false);
    assert.equal(remaining.some((entry) => entry.text === "fresh valid memory record"), true);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("unsupported memory provenance/consent is rejected at construction", () => {
  assert.throws(
    () => memory({ namespace: "ns", ttl: `${"9".repeat(100)}d`, recallBudget: 10 }),
    /memory ttl must use positive m, h, or d duration/,
  );
  assert.throws(
    () => memory({ namespace: "ns", ttl: "30d", recallBudget: 10, provenance: "project" }),
    /cave_memory_provenance_unsupported/,
  );
  assert.throws(
    () => memory({ namespace: "ns", ttl: "30d", recallBudget: 10, provenance: "external" }),
    /cave_memory_provenance_unsupported/,
  );
  assert.throws(
    () => memory({ namespace: "ns", ttl: "30d", recallBudget: 10, consent: "project_shared" }),
    /cave_memory_consent_unsupported/,
  );
  // The supported config still constructs.
  const ok = memory({ namespace: "ns", ttl: "30d", recallBudget: 10 });
  assert.equal(ok.provenance, "local");
  assert.equal(ok.consent, "local_only");
});

test("production sandbox conformance denies host read, network, and child process", async () => {
  assert.equal(await verifySandboxConformance(), true);
});

test("write-effect tools are blocked in fixture sandbox", async () => {
  let executed = false;
  const dangerous = agent({
    id: "dangerous",
    instructions: "Never write in fixture mode.",
    model: auto(),
    tools: [
      tool({
        name: "write_record",
        description: "Write record.",
        input: schema.object({ value: schema.string() }),
        effect: "write",
        async execute() {
          executed = true;
          return "written";
        },
      }),
    ],
    sandbox: "fixture",
  });
  const faux = fauxProvider();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("write_record", { value: "x" }, { id: "call-1" })),
    fauxAssistantMessage("blocked"),
  ]);
  await run(dangerous, "write", {
    ensureRuntime: false,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(executed, false);
});

test("host sandbox executes the write-effect tool that fixture sandbox blocks", async () => {
  const executed = { fixture: false, host: false };
  for (const mode of ["fixture", "host"]) {
    const defined = agent({
      id: `${mode}-write`,
      instructions: "Write when asked.",
      model: auto(),
      tools: [
        tool({
          name: "write_record",
          description: "Write record.",
          input: schema.object({ value: schema.string() }),
          effect: "write",
          async execute() {
            executed[mode] = true;
            return "written";
          },
        }),
      ],
      sandbox: mode,
    });
    const faux = fauxProvider();
    faux.setResponses([
      fauxAssistantMessage(fauxToolCall("write_record", { value: "x" }, { id: `call-${mode}` })),
      fauxAssistantMessage("done"),
    ]);
    await run(defined, "write", {
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    });
  }
  assert.deepEqual(executed, { fixture: false, host: true });
});

test("host sandbox runs tools without entry path while required sandbox still demands one", async () => {
  let hostCalls = 0;
  const readTool = () => tool({
    name: "read_value",
    description: "Read value.",
    input: schema.object({}),
    effect: "read",
    execute() {
      hostCalls += 1;
      return "host value";
    },
  });
  const hosted = agent({
    id: "host-without-entry",
    instructions: "Read value.",
    model: auto(),
    tools: [readTool()],
    sandbox: "host",
  });
  const faux = fauxProvider();
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("read_value", {}, { id: "host-call" })),
    fauxAssistantMessage("done"),
  ]);
  const result = await run(hosted, "read", {
    ensureRuntime: false,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "done");
  assert.deepEqual(result.toolCalls, ["read_value"]);
  assert.equal(hostCalls, 1);

  const required = agent({
    id: "required-without-entry",
    instructions: "Read value.",
    model: auto(),
    tools: [readTool()],
    sandbox: "required",
  });
  const strict = fauxProvider();
  strict.setResponses([fauxAssistantMessage("must not run")]);
  await assert.rejects(
    run(required, "read", {
      ensureRuntime: false,
      model: strict.getModel(),
      streamFn: strict.provider.streamSimple.bind(strict.provider),
    }),
    /cave_tool_sandbox_entry_required/,
  );
  assert.equal(strict.state.callCount, 0);
  assert.equal(hostCalls, 1);
});

test("host sandbox cannot escape a required-sandbox ancestor", async () => {
  const child = agent({
    id: "host-escape-child",
    instructions: "Write when asked.",
    model: "anthropic/claude-haiku-4-5",
    tools: [tool({
      name: "child_write",
      description: "Write child record.",
      input: schema.object({}),
      effect: "write",
      execute: () => "written",
    })],
    sandbox: "host",
  });
  const parent = agent({
    id: "host-escape-parent",
    instructions: "Delegate.",
    model: auto(),
    tools: [subagent({
      name: "delegate",
      description: "Delegate.",
      agent: child,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    })],
    sandbox: "required",
  });
  const faux = fauxProvider();
  faux.setResponses([fauxAssistantMessage("must not run")]);
  await assert.rejects(
    run(parent, "go", {
      ensureRuntime: false,
      entryPath: "tests/fixtures/sandbox-agent.mjs",
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    }),
    /cave_host_sandbox_nested_under_required/,
  );
  assert.equal(faux.state.callCount, 0);
});

test("unknown sandbox mode fails closed at definition time", () => {
  assert.throws(
    () => agent({
      id: "unknown-sandbox",
      instructions: "Do nothing.",
      model: auto(),
      sandbox: "none",
    }),
    /caveman agent: unknown sandbox mode "none"/,
  );
});

for (const attack of ["home_read", "network_read", "dns_read", "udp_write", "tcp_connect", "child_process"]) {
  test(`restricted tool subprocess blocks ${attack}`, async () => {
    const faux = fauxProvider();
    let observed = "";
    faux.setResponses([
      fauxAssistantMessage(fauxToolCall(attack, {}, { id: `call-${attack}` })),
      (context) => {
        observed = JSON.stringify(context.messages);
        return fauxAssistantMessage("blocked");
      },
    ]);
    const result = await run(sandboxAgent, attack, {
      ensureRuntime: false,
      entryPath: "tests/fixtures/sandbox-agent.mjs",
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
    });
    assert.equal(result.text, "blocked");
    assert.match(observed, /Access to this API has been restricted|cave_sandbox_network_denied|ECONNREFUSED|EACCES|EPERM|ENETUNREACH|EHOSTUNREACH|ENETDOWN/);
    if (attack === "tcp_connect") {
      // net.Socket bypasses the monkeypatch, so a block here proves the OS
      // boundary caught it, not the defense-in-depth layer.
      assert.doesNotMatch(observed, /cave_sandbox_network_denied/);
    }
  });
}

test("the gateway probe is memoized per gateway under the default transport", async () => {
  const gatewayURL = `https://memo-${crypto.randomUUID()}.example`;
  let probes = 0;
  let releaseProbe = () => {};
  const probeGate = new Promise((resolve) => {
    releaseProbe = resolve;
  });
  const priorFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    probes += 1;
    await probeGate;
    return Response.json({
      ok: true,
      service: "caveman-proxy",
      schema: "caveman.proxy.health.v1",
      adapters: 4,
    });
  };
  try {
    // Every request enters before the first probe resolves. The in-flight
    // promise, not only the completed TTL value, must coalesce this cold burst.
    const burst = Array.from(
      { length: 16 },
      () => resolveCaveRoute(gatewayURL, { ensureRuntime: true }, false),
    );
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(probes, 1, "a concurrent cold burst should start one probe");
    releaseProbe();
    const first = await Promise.all(burst);
    assert.ok(first.every((route) => route.useGateway), "all waiters should receive the shared ready result");

    const steady = await resolveCaveRoute(gatewayURL, { ensureRuntime: true }, false);
    assert.equal(steady.useGateway, true);
    assert.equal(probes, 1, "the completed result should stay memoized inside the TTL");
    // A caller-supplied fetch (test seam) always probes fresh — never cached.
    let customProbes = 0;
    const customFetch = async () => {
      customProbes += 1;
      return Response.json({
        ok: true, service: "caveman-proxy", schema: "caveman.proxy.health.v1", adapters: 4,
      });
    };
    await resolveCaveRoute(gatewayURL, { ensureRuntime: true, fetch: customFetch }, false);
    await resolveCaveRoute(gatewayURL, { ensureRuntime: true, fetch: customFetch }, false);
    assert.equal(customProbes, 2, "a custom transport must not be memoized");
  } finally {
    releaseProbe();
    globalThis.fetch = priorFetch;
  }
});

test("sandbox fs-read grants collapse to a common ancestor above the flag threshold", () => {
  // Under the threshold: one flag per file (precise).
  const few = ["/staged/a.js", "/staged/lib/b.js", "/staged/lib/c.js"];
  const fewFlags = sandboxSourceReadFlags(few);
  assert.equal(fewFlags.length, 3);
  assert.deepEqual(fewFlags, few.map((path) => `--allow-fs-read=${path}`));
  // Above it: a large project would blow ARG_MAX, so collapse to the common
  // ancestor directory of the STAGED copy (safe — it holds only the source graph).
  const many = Array.from({ length: 2_000 }, (_, index) => `/staged/copy/src/mod-${index}.js`);
  const manyFlags = sandboxSourceReadFlags(many, "/staged/copy");
  assert.equal(manyFlags.length, 1);
  assert.equal(manyFlags[0], "--allow-fs-read=/staged/copy/src");
  assert.throws(
    () => sandboxSourceReadFlags(many),
    /cave_sandbox_source_staging_root_required/,
  );
  assert.throws(
    () => sandboxSourceReadFlags(many, "/"),
    /cave_sandbox_source_read_root_refused/,
  );

  const divergentRoots = Array.from(
    { length: 2_000 },
    (_, index) => index % 2 === 0 ? `/staged/copy/src/mod-${index}.js` : `/outside/src/mod-${index}.js`,
  );
  assert.throws(
    () => sandboxSourceReadFlags(divergentRoots, "/staged/copy"),
    /cave_sandbox_source_read_root_refused/,
  );

  const divergentStagingDirs = Array.from(
    { length: 2_000 },
    (_, index) => `/staged/copy-${index % 2}/src/mod-${index}.js`,
  );
  assert.throws(
    () => sandboxSourceReadFlags(divergentStagingDirs, "/staged/copy-0"),
    /cave_sandbox_source_read_grant_escapes_staging/,
  );
});

test("a refused collapsed fs-read grant allocates no tool workspace", async () => {
  const toolWorkspaces = async () => (await readdir(tmpdir()))
    .filter((name) => name.startsWith("caveman-agent-tool-"))
    .sort();
  const before = await toolWorkspaces();
  const packageRoot = fileURLToPath(new URL("..", import.meta.url));
  const sandbox = await prepareHarnessToolSandbox(sandboxAgent, {
    rootDir: packageRoot,
    entryPath: "tests/fixtures/sandbox-agent.mjs",
  });
  try {
    const divergentRoots = Array.from(
      { length: 2_000 },
      (_, index) => index % 2 === 0 ? `/staged/copy/src/mod-${index}.js` : `/outside/src/mod-${index}.js`,
    );
    const execute = createHarnessToolExecutor({
      definition: sandboxAgent,
      tool: sandboxAgent.tools[0],
      sandbox: { ...sandbox, stagingRoot: "/staged/copy", sourceFiles: divergentRoots },
    });
    await assert.rejects(
      execute({}),
      /cave_sandbox_source_read_root_refused/,
    );
    assert.deepEqual(await toolWorkspaces(), before);
  } finally {
    await sandbox.dispose();
  }
});

test("an in-process tool that ignores its abort signal fails at timeoutMs with cave_tool_timeout", async () => {
  const defined = agent({
    id: "hang-tool",
    instructions: "Call hang.",
    model: auto(),
    tools: [tool({
      name: "hang",
      description: "Never resolves and ignores its abort signal.",
      input: schema.object({}),
      effect: "read",
      result: "inline",
      timeoutMs: 100,
      // Ignores the signal entirely and never settles.
      execute: () => new Promise(() => {}),
    })],
    sandbox: "fixture",
  });
  const faux = fauxProvider();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("hang", {}, { id: "hang-1" })),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("after");
    },
  ]);
  const result = await run(defined, "go", {
    ensureRuntime: false,
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "after");
  // The run stops waiting at 100ms instead of hanging forever on the closure.
  assert.match(observed, /cave_tool_timeout/);
});

test("a sandboxed tool that writes to stdout still returns its fd-3 result", async () => {
  const faux = fauxProvider();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("noisy_stdout", {}, { id: "noisy-1" })),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("done");
    },
  ]);
  const result = await run(sandboxAgent, "noisy", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "done");
  // The result survives the tool's own stdout noise on the dedicated channel.
  assert.match(observed, /structured-result/);
  assert.doesNotMatch(observed, /cave_sandbox_invalid_output/);
});

test("a sandboxed non-JSON tool result returns a stable failure frame", async () => {
  const faux = fauxProvider();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("nonserializable_result", {}, { id: "bigint-1" })),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("done");
    },
  ]);
  const result = await run(sandboxAgent, "nonserializable", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "done");
  assert.match(observed, /cave_sandbox_result_not_serializable/);
  assert.doesNotMatch(observed, /cave_sandbox_invalid_output/);
});

test("a late floating rejection does not fail a sandboxed tool that already succeeded", async () => {
  const faux = fauxProvider();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("late_reject", {}, { id: "late-1" })),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("done");
    },
  ]);
  const result = await run(sandboxAgent, "late", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "done");
  assert.match(observed, /success-despite-late-reject/);
  assert.doesNotMatch(observed, /cave_sandbox_failed/);
});

test("sandboxProfile.network:true fails closed as unbounded egress", async () => {
  const faux = fauxProvider();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("network_read", {}, { id: "net-true" })),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("blocked");
    },
  ]);
  const result = await run(sandboxAgent, "network", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
    // Requesting egress is not a way to get egress: it is refused outright,
    // because there is no scoped-egress mechanism and unrestricted egress from
    // credentialed tool code is an exfiltration hole.
    sandboxProfile: { network: true, childProcess: false, credentialEnv: [] },
  });
  assert.equal(result.text, "blocked");
  assert.match(observed, /cave_sandbox_network_egress_unbounded/);
});

test("sandbox fails closed on child-process permission without OS tree containment", async () => {
  const socket = createSocket("udp4");
  let received = false;
  socket.on("message", () => {
    received = true;
  });
  await new Promise((accept, reject) => {
    socket.once("error", reject);
    socket.bind(0, "127.0.0.1", accept);
  });
  try {
    const address = socket.address();
    assert.notEqual(typeof address, "string");
    const faux = fauxProvider();
    let observed = "";
    faux.setResponses([
      fauxAssistantMessage(fauxToolCall(
        "delayed_child_effect",
        { port: address.port },
        { id: "delayed-effect" },
      )),
      (context) => {
        observed = JSON.stringify(context.messages);
        return fauxAssistantMessage("terminated");
      },
    ]);
    const result = await run(sandboxAgent, "timeout", {
      ensureRuntime: false,
      entryPath: "tests/fixtures/sandbox-agent.mjs",
      model: faux.getModel(),
      streamFn: faux.provider.streamSimple.bind(faux.provider),
      sandboxProfile: {
        network: true,
        childProcess: true,
        credentialEnv: [],
      },
    });
    assert.equal(result.text, "terminated");
    assert.match(observed, /cave_sandbox_child_process_containment_unavailable/);
    await new Promise((accept) => setTimeout(accept, 1_000));
    assert.equal(received, false);
  } finally {
    socket.close();
  }
});

test("sandbox timeout waits for stubborn worker close after SIGKILL escalation", async () => {
  const faux = fauxProvider();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("ignore_timeout", {}, { id: "ignore-timeout" })),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("terminated");
    },
  ]);
  const started = Date.now();
  const result = await run(sandboxAgent, "timeout", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  const elapsed = Date.now() - started;
  assert.equal(result.text, "terminated");
  assert.match(observed, /cave_sandbox_timeout/);
  assert.equal(elapsed >= 1_200 && elapsed < 3_000, true);
});

test("sandbox rejects parent and worker definition drift before tool execution", async () => {
  process.env.CAVE_AGENT_PARENT_DEFINITION = "1";
  let conditional;
  try {
    conditional = (await import(
      `./fixtures/conditional-sandbox-agent.mjs?parent=${Date.now()}`
    )).default;
  } finally {
    delete process.env.CAVE_AGENT_PARENT_DEFINITION;
  }
  const faux = fauxProvider();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("conditional_read", {}, { id: "conditional" })),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("rejected");
    },
  ]);
  const result = await run(conditional, "read", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/conditional-sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "rejected");
  assert.match(observed, /cave_sandbox_definition_mismatch/);
  assert.doesNotMatch(observed, /worker-value/);
});

test("programmatic sandbox executes immutable source snapshot after original graph changes", async () => {
  const root = await mkdtemp(resolve(tmpdir(), "caveman-agent-source-snapshot-"));
  const helperPath = resolve(root, "helper.mjs");
  try {
    await writeFile(resolve(root, "package.json"), '{"type":"module"}\n');
    await symlink(
      resolve(import.meta.dirname, "../node_modules"),
      resolve(root, "node_modules"),
      process.platform === "win32" ? "junction" : "dir",
    );
    await writeFile(helperPath, 'export const value = "original-snapshot";\n');
    const frameworkURL = pathToFileURL(resolve(import.meta.dirname, "../dist/index.js")).href;
    await writeFile(resolve(root, "agent.mjs"), [
      `import { agent, auto, schema, tool } from ${JSON.stringify(frameworkURL)};`,
      'import { value } from "./helper.mjs";',
      "export default agent({",
      '  id: "source-snapshot",',
      '  instructions: "Call snapshot_read once.",',
      "  model: auto(),",
      "  tools: [tool({",
      '    name: "snapshot_read",',
      '    description: "Read snapshotted helper.",',
      "    input: schema.object({}),",
      '    effect: "read",',
      "    execute() { return value; },",
      "  })],",
      '  sandbox: "required",',
      "});",
      "",
    ].join("\n"));
    const defined = (await import(
      `${pathToFileURL(resolve(root, "agent.mjs")).href}?parent=${crypto.randomUUID()}`
    )).default;
    const faux = fauxProvider();
    let observed = "";
    faux.setResponses([
      fauxAssistantMessage(fauxToolCall("snapshot_read", {}, { id: "snapshot-read" })),
      (context) => {
        observed = JSON.stringify(context.messages);
        return fauxAssistantMessage("done");
      },
    ]);
    let changed = false;
    const streamFn = (model, context, options) => {
      if (!changed) {
        changed = true;
        writeFileSync(helperPath, 'export const value = "mutated-original";\n');
      }
      return faux.provider.streamSimple(model, context, options);
    };
    const result = await run(defined, "read", {
      rootDir: root,
      entryPath: "agent.mjs",
      ensureRuntime: false,
      model: faux.getModel(),
      streamFn,
    });
    assert.equal(result.text, "done");
    assert.match(observed, /original-snapshot/);
    assert.doesNotMatch(observed, /mutated-original/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("restricted tool subprocess tears down workspace between calls", async () => {
  const faux = fauxProvider();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("write_workspace", {}, { id: "call-write" })),
    fauxAssistantMessage(fauxToolCall("read_workspace", {}, { id: "call-read" })),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("isolated");
    },
  ]);
  const result = await run(sandboxAgent, "workspace", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "isolated");
  assert.match(observed, /ENOENT/);
});

test("restricted tool subprocess executes locked local module imports", async () => {
  const faux = fauxProvider();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("modular_read", {}, { id: "call-module" })),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("done");
    },
  ]);
  const result = await run(sandboxAgent, "module", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "done");
  assert.match(observed, /modular-tool-ok/);
});

test("required sandbox permits explicitly opted-in write tools", async () => {
  const faux = fauxProvider();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("live_write", {}, { id: "call-live-write" })),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("done");
    },
  ]);
  const result = await run(sandboxAgent, "live write", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
  });
  assert.equal(result.text, "done");
  assert.match(observed, /live-write-ok/);
});
