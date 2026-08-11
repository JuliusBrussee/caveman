#!/usr/bin/env node
import { execFile, spawn } from "node:child_process";
import { readFileSync, watch } from "node:fs";
import { glob, readFile } from "node:fs/promises";
import { isAbsolute, relative, resolve } from "node:path";
import { createInterface } from "node:readline/promises";
import { pathToFileURL } from "node:url";
import { promisify } from "node:util";
import { Value } from "typebox/value";
import { catalogCost, CATALOG_SHA256 } from "./catalog.js";
import {
  agentDefinitionSHA256,
  checkLock,
  compileAndWrite,
  contextIRSHA256,
  generateCandidatePlans,
  parseCaveBuildLock,
  type BuildConfig,
  type CaveBuildLock,
  type CavePlan,
  type CompileResult,
  type RunEvidence,
  type TransformCapability,
} from "./build.js";
import {
  contextBill,
  lowerContext,
  sha256,
  stableStringify,
  type ContextIR,
} from "./context-ir.js";
import {
  agentFileSourcePaths,
  loadDevModule,
  type LoadedDevModule,
} from "./dev-loader.js";
import { lowerAgentContext } from "./execution-kernel.js";
import { createConversation, type AgentDefinition } from "./index.js";
import {
  caveGatewayReady,
  buildEngineEnv,
  buildRuntimeControlEnv,
  resolveGatewayURL,
  runAgentInternal,
  validateSandboxCredentialEnv,
  verifySandboxConformance,
  type ConversationState,
} from "./runtime.js";
import type { ContextKind, EvalDefinition, QualityGrader } from "./primitives.js";
import { sourceGraphSHA256 } from "./source-graph.js";
import { portableInvocation } from "./portable-process.js";
import {
  CLAUDE_AGENT_SDK_VERSION,
  CLAUDE_CODE_VERSION,
  FRAMEWORK_VERSION,
  PI_ADAPTER_VERSION,
  PI_UPSTREAM_VERSION,
} from "./runtime-identity.js";

const execFileAsync = promisify(execFile);

async function main(): Promise<void> {
  const [command = "help", ...args] = process.argv.slice(2);
  if (command === "help" || command === "--help" || command === "-h") {
    printHelp();
    return;
  }
  if (command === "--version" || command === "-v") {
    process.stdout.write(`${FRAMEWORK_VERSION}\n`);
    return;
  }
  if (command === "dev") {
    await dev(args);
    return;
  }
  if (command === "build") {
    await build(args);
    return;
  }
  if (command === "check") {
    await check(args);
    return;
  }
  if (command === "doctor") {
    await doctor(args);
    return;
  }
  if (command === "register") {
    await register(args);
    return;
  }
  throw new Error(`unknown command ${JSON.stringify(command)}; run caveman-agent --help`);
}

function printHelp(): void {
  process.stdout.write([
    "Caveman Agent — efficiency-native TypeScript agent framework",
    "",
    "Usage:",
    "caveman-agent dev [entry] [prompt]",
    "caveman-agent build [config]",
    "caveman-agent check [config]",
    "caveman-agent doctor [--json]",
    "caveman-agent register",
    "caveman-agent --version",
    "",
  ].join("\n"));
}

type DoctorStatus = "pass" | "warn" | "fail";

type DoctorCheck = {
  id: string;
  status: DoctorStatus;
  detail: string;
  fix?: string;
};

type DoctorReport = {
  schema_version: 1;
  framework_version: string;
  ready: boolean;
  /** What a run on this machine would do today, with nothing else installed. */
  execution_mode: "optimized" | "observe-only";
  checks: DoctorCheck[];
  harnesses: Array<{
    id: "pi" | "claude" | "vercel-ai-sdk" | "eve" | "mastra";
    locked_execution: boolean;
    detail: string;
  }>;
  next_action: string;
};

async function doctor(args: string[]): Promise<void> {
  if (args.some((value) => value !== "--json")) {
    throw new Error("usage: caveman-agent doctor [--json]");
  }
  const json = args.includes("--json");
  const root = process.cwd();
  const checks: DoctorCheck[] = [];
  const node = process.versions.node;
  checks.push(compareNodeVersion(node, "22.19.0") >= 0
    ? { id: "node", status: "pass", detail: `Node ${node}` }
    : {
      id: "node",
      status: "fail",
      detail: `Node ${node}; framework requires >=22.19.0`,
      fix: "install Node 22.19 or newer",
    });

  try {
    if (!await verifySandboxConformance()) throw new Error("probe returned false");
    checks.push({ id: "sandbox", status: "pass", detail: "tool sandbox containment probe passed" });
  } catch (error) {
    checks.push({
      id: "sandbox",
      status: "fail",
      detail: `tool sandbox containment unavailable: ${safeDiagnostic(error)}`,
      fix: process.platform === "win32"
        ? "run sandbox-required agents in WSL2; native Windows host-mode agents work, but required containment stays fail-closed"
        : "use supported Node runtime and OS; do not run production tools with sandbox fixture",
    });
  }

  // Missing engine and missing runtime are WARN, not FAIL: without them runs
  // still reach a real model response in observe-only mode (no transform or
  // gateway telemetry; provider usage and local context estimates remain). Only conditions that make even an
  // observe-only run untrustworthy fail this command.
  try {
    const registry = await loadTransformRegistry();
    checks.push({
      id: "engine",
      status: "pass",
      detail: `transform registry ${registry.sha256.slice(0, 12)} (${registry.capabilities.length} capabilities)`,
    });
  } catch (error) {
    checks.push({
      id: "engine",
      status: "warn",
      detail: `Caveman engine not found — transforms disabled (observe-only): ${safeDiagnostic(error)}`,
      fix: "npm i -g @caveman-ai/cli && caveman start",
    });
  }

  try {
    const command = process.env.CAVEMAN_CLI_BIN ?? "caveman";
    const env = buildRuntimeControlEnv();
    const invocation = portableInvocation(command, ["version"], { env });
    // Public Caveman porcelain exposes `version` as a command, not a GNU-style
    // flag. Keep doctor aligned with public CLI contract.
    const { stdout } = await execFileAsync(invocation.command, [...invocation.args], {
      encoding: "utf8",
      env,
      maxBuffer: 64 << 10,
      timeout: 10_000,
    });
    const version = runtimeVersionDiagnostic(stdout);
    checks.push({ id: "runtime_cli", status: "pass", detail: version });
  } catch (error) {
    checks.push({
      id: "runtime_cli",
      status: "warn",
      detail: `Caveman runtime CLI unavailable — runs stay observe-only: ${safeDiagnostic(error)}`,
      fix: "npm i -g @caveman-ai/cli && caveman start",
    });
  }

  const gatewayURL = resolveGatewayURL(undefined);
  checks.push(await caveGatewayReady(gatewayURL)
    ? { id: "gateway", status: "pass", detail: `Caveman gateway ready at ${gatewayURL}` }
    : {
      id: "gateway",
      status: "warn",
      detail: `gateway not reachable at ${gatewayURL} — telemetry off, runs are observe-only`,
      fix: "npm i -g @caveman-ai/cli && caveman start",
    });

  const configPath = resolve(root, "caveman.config.ts");
  try {
    await readFile(configPath);
    const loaded = await loadBuildInputs(root, "caveman.config.ts");
    await lowerBuildContext(root, loaded.agent);
    checks.push({
      id: "project",
      status: "pass",
      detail: `${loaded.agent.id}: config, entry, eval graph, and static Context IR load`,
    });
    try {
      await readFile(resolve(root, ".caveman/agent.lock.json"));
      const lock = await validLockIdentity(root, loaded.config.entry, loaded.agent);
      if (!lock) throw new Error("lock disappeared during validation");
      checks.push({
        id: "lock",
        status: "pass",
        detail: `${lock.harness.id} build ${lock.build_sha256.slice(0, 12)} is current`,
      });
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") {
        checks.push({
          id: "lock",
          status: "warn",
          detail: "no Cave Build lock; dev runs unlocked",
          fix: "approve required evals, then run caveman-agent build",
        });
      } else {
        checks.push({
          id: "lock",
          status: "fail",
          detail: safeDiagnostic(error),
          fix: "fix drift, then rebuild; do not deploy stale lock",
        });
      }
    }
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      checks.push({
        id: "project",
        status: "warn",
        detail: "no caveman.config.ts in current directory",
        fix: "run command from generated agent project",
      });
    } else {
      checks.push({
        id: "project",
        status: "fail",
        detail: safeDiagnostic(error),
        fix: "fix config, entry, eval, or context source error",
      });
    }
  }

  try {
    const credentialModels = [
      process.env.ANTHROPIC_API_KEY && "anthropic/claude-haiku-4-5",
      process.env.OPENAI_API_KEY && "openai/gpt-5.4-mini",
      (process.env.GEMINI_API_KEY || process.env.GOOGLE_API_KEY) && "google/gemini-2.5-flash",
    ].filter((value): value is string => typeof value === "string");
    const selectedModel = process.env.CAVE_MODEL ?? localProviderModel(root) ??
      (credentialModels.length === 1 ? credentialModels[0] : undefined);
    if (selectedModel === undefined) {
      checks.push({
        id: "provider",
        status: "warn",
        detail: credentialModels.length > 1
          ? "multiple provider credentials found but no model selected"
          : "no provider model selected",
        fix: credentialModels.length > 1
          ? "set CAVE_MODEL or configure .caveman/provider.json"
          : "set one supported provider credential or configure .caveman/provider.json",
      });
    } else {
      const credential = credentialForModel(selectedModel);
      checks.push(credential.available
        ? {
          id: "provider",
          status: "pass",
          detail: `${selectedModel} selected; ${credential.name} available`,
        }
        : {
          id: "provider",
          status: "warn",
          detail: `${selectedModel} selected; ${credential.name} missing`,
          fix: `set ${credential.name} in current shell`,
        });
    }
  } catch (error) {
    checks.push({
      id: "provider",
      status: "fail",
      detail: safeDiagnostic(error),
      fix: "fix .caveman/provider.json or CAVE_MODEL",
    });
  }

  let globalClaudeVersion = "global Claude Code not found (not required)";
  try {
    const command = process.env.CLAUDE_BIN ?? "claude";
    const env = buildRuntimeControlEnv();
    const invocation = portableInvocation(command, ["--version"], { env });
    const { stdout } = await execFileAsync(invocation.command, [...invocation.args], {
      encoding: "utf8",
      env,
      maxBuffer: 64 << 10,
      timeout: 10_000,
    });
    globalClaudeVersion = stdout.trim() || "global Claude Code returned empty version";
  } catch {
    // Claude is optional. Harness matrix below reports exact availability.
  }

  const ready = checks.every((check) => check.status !== "fail");
  const firstFailure = checks.find((check) => check.status === "fail");
  const firstWarning = checks.find((check) => check.status === "warn");
  // A machine without the engine or the runtime CLI can still run observe-only,
  // but it cannot execute a Cave Build: locked execution needs the transforms
  // and the local runtime, so it is reported separately from the exit code.
  const optimized = ["engine", "runtime_cli"].every((id) =>
    checks.find((check) => check.id === id)?.status === "pass");
  const lockedRequirements = ["project", "lock", "provider", "engine", "runtime_cli"];
  const lockedMissing = lockedRequirements.find((id) => checks.find((check) => check.id === id)?.status !== "pass");
  const lockedReady = ready && optimized && lockedMissing === undefined;
  const notLockedDetail = !ready
    ? "foundation check failed"
    : lockedMissing !== undefined
      ? `locked execution unavailable: ${lockedMissing} check is not ready`
      : "locked execution unavailable";
  const report: DoctorReport = {
    schema_version: 1,
    framework_version: FRAMEWORK_VERSION,
    ready,
    execution_mode: optimized ? "optimized" : "observe-only",
    checks,
    harnesses: [
      {
        id: "pi",
        locked_execution: lockedReady,
        detail: lockedReady
          ? `adapter ${PI_ADAPTER_VERSION}; upstream ${PI_UPSTREAM_VERSION}`
          : notLockedDetail,
      },
      {
        id: "claude",
        locked_execution: false,
        detail: `Agent SDK ${CLAUDE_AGENT_SDK_VERSION} / bundled Claude Code ${CLAUDE_CODE_VERSION}; ${globalClaudeVersion}; public lane exists, but locked execution stays fail-closed pending source/runtime provenance, per-turn budgets, CCR proof, cached-substitution evidence, and executable Pi/Claude replay`,
      },
      {
        id: "vercel-ai-sdk",
        locked_execution: lockedReady,
        detail: lockedReady
          ? "adapter 7.0.43; exact model identity and AI SDK v7 disjoint usage required"
          : notLockedDetail,
      },
      {
        id: "eve",
        locked_execution: lockedReady,
        detail: lockedReady
          ? "adapter 0.29.2; fixed-model, reasoning-off Cave Builds only because Eve stream omits reasoning usage"
          : notLockedDetail,
      },
      {
        id: "mastra",
        locked_execution: lockedReady,
        detail: lockedReady
          ? "adapter 1.55.0; exact response model and complete aggregate usage required"
          : notLockedDetail,
      },
    ],
    next_action: firstFailure?.fix ?? firstWarning?.fix ?? "run caveman-agent dev",
  };

  if (json) {
    process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  } else {
    process.stdout.write([
      `Caveman Agent ${FRAMEWORK_VERSION} doctor`,
      "",
      ...checks.map((check) => {
        const mark = check.status === "pass" ? "PASS" : check.status === "warn" ? "WARN" : "FAIL";
        return `${mark.padEnd(4)} ${check.id.padEnd(12)} ${check.detail}`;
      }),
      "",
      `run mode: ${report.execution_mode}${report.execution_mode === "observe-only" ? " (no transforms or gateway telemetry)" : ""}`,
      `locked harnesses: ${report.harnesses.filter((item) => item.locked_execution).map((item) => item.id).join(", ") || "none"}`,
      `next: ${report.next_action}`,
      "provider calls: 0",
      "provider savings: not claimed",
      "",
    ].join("\n"));
  }
  if (!ready) process.exitCode = 1;
}

function compareNodeVersion(current: string, minimum: string): number {
  const parse = (value: string) => value.split(".").slice(0, 3).map((part) => Number(part));
  const left = parse(current);
  const right = parse(minimum);
  for (let index = 0; index < 3; index++) {
    const difference = (left[index] ?? 0) - (right[index] ?? 0);
    if (difference !== 0) return difference;
  }
  return 0;
}

function safeDiagnostic(error: unknown): string {
  const value = error instanceof Error ? error.message : String(error);
  return value.replace(/[\r\n]+/g, " ").slice(0, 512);
}

function runtimeVersionDiagnostic(stdout: string): string {
  const value = stdout.trim();
  if (value === "") throw new Error("empty version output");
  try {
    const parsed = JSON.parse(value) as { version?: unknown; binary_release?: unknown };
    if (typeof parsed.version === "string" && parsed.version.trim() !== "") {
      const release = typeof parsed.binary_release === "string" && parsed.binary_release.trim() !== ""
        ? ` (${parsed.binary_release.trim()})`
        : "";
      return `caveman ${parsed.version.trim()}${release}`;
    }
  } catch {
    // Older CLIs may return one plain version line.
  }
  return value.replace(/[\r\n]+/g, " ").slice(0, 256);
}

function credentialForModel(model: string): { name: string; available: boolean } {
  const provider = model.split("/", 1)[0];
  if (provider === "anthropic") {
    return { name: "ANTHROPIC_API_KEY", available: Boolean(process.env.ANTHROPIC_API_KEY) };
  }
  if (provider === "openai") {
    return { name: "OPENAI_API_KEY", available: Boolean(process.env.OPENAI_API_KEY) };
  }
  if (provider === "google" || provider === "gemini") {
    return {
      name: "GEMINI_API_KEY or GOOGLE_API_KEY",
      available: Boolean(process.env.GEMINI_API_KEY || process.env.GOOGLE_API_KEY),
    };
  }
  return { name: `credential for ${provider || "unknown provider"}`, available: false };
}

async function register(_args: string[]): Promise<void> {
  const root = process.cwd();
  const controlURL = process.env.CAVE_CONTROL_URL?.replace(/\/+$/, "");
  const token = process.env.CAVE_TOKEN ?? process.env.CAVE_API_TOKEN;
  const projectID = process.env.CAVE_PROJECT_ID;
  if (!controlURL || !token || !projectID) {
    throw new Error("register requires CAVE_CONTROL_URL, CAVE_TOKEN, and CAVE_PROJECT_ID");
  }
  const lock = await readLock(root);
  const loaded = await loadBuildInputs(root, "caveman.config.ts");
  const checked = await validLockIdentity(root, loaded.config.entry);
  if (!checked || checked.build_sha256 !== lock.build_sha256) {
    throw new Error("cave_stale_lock:registration");
  }
  const response = await fetch(`${controlURL}/api/v1/projects/${encodeURIComponent(projectID)}/agent-builds`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${token}`,
      "content-type": "application/json",
    },
    body: JSON.stringify({
      agent_slug: lock.agent_id,
      build_sha256: lock.build_sha256,
      plan_sha256: lock.plan_sha256,
      source_sha256: lock.source_sha256,
      eval_suite_sha256: lock.eval_suite_sha256,
      catalog_sha256: lock.catalog_sha256,
      transform_registry_sha256: lock.runtime.transform_registry_sha256,
      harness: lock.harness.id,
      adapter_version: lock.harness.adapter_version,
      upstream_version: lock.harness.upstream_version,
      runtime_version: lock.runtime.caveman_version,
      evidence_status: lock.evidence.status,
      evidence_basis: lock.evidence.basis,
      lock,
    }),
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) {
    throw new Error(`registration failed: HTTP ${response.status}`);
  }
  const registered = await response.json() as { id?: unknown; build_sha256?: unknown };
  process.stdout.write([
    `registered build: ${String(registered.build_sha256 ?? lock.build_sha256)}`,
    `registration id: ${String(registered.id ?? "unavailable")}`,
    "registration does not activate plan or create a savings claim",
    "provider savings: not claimed",
    "",
  ].join("\n"));
}

async function dev(args: string[]): Promise<void> {
  const root = process.cwd();
  const entry = args[0] ?? "src/agent.ts";
  const prompt = args.slice(1).join(" ") || "Reply with one short greeting.";
  const conversation = createConversation();
  const sessionId = conversation.sessionId;
  const interactive = args.length <= 1 && process.stdin.isTTY && process.stdout.isTTY;
  if (!interactive) {
    const snapshot = await prepareDevSnapshot(root, entry);
    try {
      await runDevTurn(snapshot, prompt, sessionId, conversation);
    } finally {
      await snapshot.loaded.dispose();
    }
    return;
  }

  let revision = 0;
  const watcher = watch(root, { recursive: true }, (_event, filename) => {
    const path = filename?.toString().replaceAll("\\", "/") ?? "";
    if (path !== "" &&
        /(?:^|\/)(?:node_modules|\.git|dist|coverage|\.turbo)(?:\/|$)/.test(path)) {
      return;
    }
    revision += 1;
  });
  const readline = createInterface({ input: process.stdin, output: process.stdout });
  let snapshot: PreparedDevSnapshot | undefined;
  let snapshotRevision = -1;
  try {
    const initialRevision = revision;
    snapshot = await prepareDevSnapshot(root, entry);
    snapshotRevision = initialRevision;
    await runDevTurn(snapshot, prompt, sessionId, conversation);
    while (true) {
      const promptDirty = revision !== snapshotRevision;
      const next = await readline.question(
        promptDirty ? "caveman-agent (reloaded)> " : "caveman-agent> ",
      );
      if (next.trim() === "") continue;
      try {
        const dirty = revision !== snapshotRevision;
        if (dirty) {
          const replacementRevision = revision;
          const replacement = await prepareDevSnapshot(root, entry);
          const previous = snapshot;
          snapshot = replacement;
          snapshotRevision = replacementRevision;
          await previous.loaded.dispose();
        }
        await runDevTurn(snapshot, next, sessionId, conversation);
      } catch (error) {
        process.stderr.write(`${firstUsefulError(error)}\n`);
      }
    }
  } finally {
    await snapshot?.loaded.dispose();
    watcher.close();
    readline.close();
  }
}

type PreparedDevSnapshot = {
  loaded: LoadedDevModule;
  definition: AgentDefinition;
  identity?: CaveBuildLock;
};

async function prepareDevSnapshot(
  root: string,
  entry: string,
): Promise<PreparedDevSnapshot> {
  const loaded = await loadDevModule(root, resolve(root, entry));
  try {
    const definition = agentFromImported(loaded.imported);
    await loaded.includeAgentFiles(definition);
    const hasLockSnapshot = await stageDevLockInputs(root, loaded);
    const identity = hasLockSnapshot
      ? await validLockIdentity(
        loaded.rootDir,
        loaded.entryPath,
        definition,
        async ({ evals }) => {
          await loaded.includeFiles(evals.flatMap((fixture) =>
            fixture.tools.sandbox === undefined ? [] : [fixture.tools.sandbox]
          ));
        },
      )
      : undefined;
    return { loaded, definition, ...(identity === undefined ? {} : { identity }) };
  } catch (error) {
    await loaded.dispose();
    throw loaded.remapError(error);
  }
}

async function runDevTurn(
  snapshot: PreparedDevSnapshot,
  prompt: string,
  sessionId: string,
  conversation: ConversationState,
): Promise<void> {
  const { loaded, definition, identity } = snapshot;
  try {
    const runtimeDefinition = identity === undefined
      ? definition
      : {
        ...definition,
        model: identity.selected_plan.model,
        reasoning: runtimeReasoning(identity.selected_plan.reasoning),
      };
    const result = await runAgentInternal(runtimeDefinition as AgentDefinition, prompt, {
      rootDir: loaded.rootDir,
      entryPath: loaded.entryPath,
      sessionId,
      conversation,
      ...(identity === undefined ? {} : {
        lockedBuild: identity,
      }),
    });
    const priced = catalogCost(result);
    process.stdout.write([
      "",
      "agent response",
      result.text,
      "",
      `task cost: ${priced.priced ? `$${priced.usd.toFixed(6)} public catalog` : "$0 unpriced"}`,
      `context bill: ${formatBill(result.contextBill)}`,
      `cache: ${result.cacheReadTokens} read / ${result.cacheWriteTokens} create tokens`,
      `model: ${result.provider}/${result.model}`,
      `active safe transforms: ${result.transformIDs.length > 0 ? result.transformIDs.join(",") : "pass-through"} + stable-prefix guard`,
      `run mode: ${result.mode}${result.mode === "observe-only" ? " (direct to provider; usage and local context estimates available)" : ""}`,
      `next action: ${identity ? "run caveman-agent check before deployment" : "approve eval fixture, then npm run build"}`,
      "local evidence: estimate only",
      "provider savings: not claimed",
      "",
    ].join("\n"));
  } catch (error) {
    throw loaded.remapError(error);
  }
}

async function stageDevLockInputs(
  root: string,
  loaded: Awaited<ReturnType<typeof loadDevModule>>,
): Promise<boolean> {
  const lockPath = ".caveman/agent.lock.json";
  try {
    await readFile(resolve(root, lockPath));
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return false;
    throw error;
  }
  const configPath = "caveman.config.ts";
  await loaded.includeFiles([lockPath]);
  await loaded.includeSourceGraph([configPath]);
  const imported = await importFresh(resolve(loaded.rootDir, configPath)) as {
    default?: BuildConfig;
    config?: BuildConfig;
  };
  const config = imported.default ?? imported.config;
  if (!config || typeof config.evals !== "string") {
    throw new Error("caveman build: config must export defineBuild()");
  }
  const evalFiles: string[] = [];
  for await (const path of glob(config.evals, { cwd: root })) {
    evalFiles.push(resolve(root, path));
  }
  await loaded.includeSourceGraph(evalFiles);
  const sourceFiles: string[] = [];
  for (const pattern of SOURCE_PATTERNS) {
    for await (const path of glob(pattern, { cwd: root })) sourceFiles.push(resolve(root, path));
  }
  await loaded.includeFiles(sourceFiles);
  await loaded.includeOptionalFiles(PACKAGE_STATE_FILES);
  return true;
}

function firstUsefulError(error: unknown): string {
  const stack = error instanceof Error ? error.stack ?? error.message : String(error);
  return stack.split("\n").slice(0, 4).join("\n");
}

async function build(args: string[]): Promise<void> {
  const root = process.cwd();
  const configPath = args[0] ?? "caveman.config.ts";
  const loaded = await loadBuildInputs(root, configPath);
  const approved = loaded.evals.filter((item) => item.approved && item.required);
  if (approved.length === 0) {
    printBuildResult({
      status: "needs_eval",
      estimated_ceiling_usd: 0,
      planned_runs: 0,
      completed_runs: 0,
      static_rejections: 0,
      actual_cost_usd: null,
      reason: "no approved required eval fixture",
    });
    return;
  }

  const buildContexts = await Promise.all(approved.map((fixture) =>
    lowerBuildContext(root, loaded.agent, fixtureInput(fixture.input))));
  const lowered = await lowerBuildContext(root, loaded.agent);
  const baseline = baselinePlan(loaded.agent, root, buildContexts.map((item) => item.ir));
  const transformRegistry = await loadTransformRegistry();
  const preferredTransforms = await profilePreferredTransforms(lowered);
  // The catalog pricing + model/safety-class policy filter now live in
  // generateCandidatePlans so the public compile() enforces the same thing
  // — the CLI passes its policy and no longer post-filters.
  const candidates = generateCandidatePlans(
    loaded.agent,
    lowered.ir,
    baseline,
    loaded.config.allowedModels ?? configuredModelCandidates(baseline.model),
    true,
    preferredTransforms,
    transformRegistry.capabilities,
    evalDynamicKinds(approved),
    {
      ...(loaded.config.allowedModels === undefined ? {} : { allowedModels: loaded.config.allowedModels }),
      ...(loaded.config.deniedModels === undefined ? {} : { deniedModels: loaded.config.deniedModels }),
      ...(loaded.config.forbiddenSafetyClasses === undefined
        ? {}
        : { forbiddenSafetyClasses: loaded.config.forbiddenSafetyClasses }),
    },
  );
  const entitled = await engineEntitled();
  const plannedRuns = candidates.filter((candidate) => !candidate.static_rejection).length * approved.length * 5;
  const estimatedCeiling = candidates
    .filter((candidate) => !candidate.static_rejection)
    .reduce((sum, candidate) => sum + candidate.estimated_cost_usd_per_run * approved.length * 5, 0);
  process.stdout.write(`search ceiling: $${estimatedCeiling.toFixed(4)} public-catalog estimate · ${plannedRuns} runs\n`);
  const sandboxConformance = await verifySandboxConformance();
  if (!sandboxConformance) throw new Error("cave_sandbox_conformance_failed");
  const privacyConformance = contextIRIsContentBlind(lowered.ir);
  if (!privacyConformance) throw new Error("cave_privacy_conformance_failed");
  const conversations = new Map<string, ConversationState>();
  const result = await compileAndWrite({
    agent: loaded.agent,
    contextIR: lowered.ir,
    evals: loaded.evals,
    candidates,
    baselinePlan: baseline,
    seeds: [1, 2, 3, 4, 5],
    config: loaded.config,
    entitled,
    sourceSha256: loaded.sourceSha256,
    catalogSha256: CATALOG_SHA256,
    transformRegistrySha256: transformRegistry.sha256,
    runtimeVersion: FRAMEWORK_VERSION,
    adapterVersion: PI_ADAPTER_VERSION,
    upstreamVersion: PI_UPSTREAM_VERSION,
    runner: async ({ plan, eval: fixture, seed, signal }) => runFixture(
      root,
      loaded.config.entry,
      loaded.agent,
      plan,
      fixture,
      seed,
      sandboxConformance,
      privacyConformance,
      conversations,
      signal,
    ),
  });
  printBuildResult(result);
}

function printBuildResult(result: CompileResult): void {
  process.stdout.write([
    `build status: ${result.status}`,
    `completed: ${result.completed_runs}/${result.planned_runs} runs`,
    result.actual_cost_usd === null
      ? "actual public-catalog cost: unknown (no terminal usage evidence)"
      : `actual public-catalog cost: $${result.actual_cost_usd.toFixed(6)}`,
    ...(result.reason ? [`reason: ${result.reason}`] : []),
    ...(result.lock ? [
      `lock: .caveman/agent.lock.json`,
      `build: ${result.lock.build_sha256}`,
      `plan: ${result.lock.selected_plan_id}`,
      `model: ${result.lock.selected_plan.model} · reasoning ${result.lock.selected_plan.reasoning}`,
      `routes: ${result.lock.selected_plan.segment_routes.length === 0
        ? "pass-through"
        : result.lock.selected_plan.segment_routes.map((route) => route.transform_id).join(" → ")}`,
      `baseline cost/task: ${formatCatalogCost(result.baseline_catalog_cost_usd_per_task)}`,
      `selected cost/task: ${formatCatalogCost(result.selected_catalog_cost_usd_per_task)}`,
      `change: ${formatCatalogChange(
        result.baseline_catalog_cost_usd_per_task,
        result.selected_catalog_cost_usd_per_task,
      )}`,
      `eval runs: ${result.lock.evidence.completed_runs} passed build evidence`,
      "local evidence: passed (estimate only)",
      "provider savings: not claimed",
    ] : [
      "lock: not written",
      "local evidence: incomplete",
      "provider savings: not claimed",
    ]),
    `next action: ${buildNextAction(result.status)}`,
    "",
  ].join("\n"));
  if (!result.lock) process.exitCode = 1;
}

function formatCatalogCost(value: number | undefined): string {
  return value === undefined ? "unavailable" : `$${value.toFixed(6)} public catalog`;
}

function formatCatalogChange(baseline: number | undefined, selected: number | undefined): string {
  if (baseline === undefined || selected === undefined || baseline <= 0) return "unavailable";
  const percentage = ((selected / baseline) - 1) * 100;
  return `${percentage >= 0 ? "+" : ""}${percentage.toFixed(1)}% local estimate`;
}

function buildNextAction(status: CompileResult["status"]): string {
  switch (status) {
    case "locked":
      return "run npm run check before deployment";
    case "needs_eval":
      return "review required evals, set approved: true, then run npm run build";
    case "needs_login":
      return "run caveman login, then npm run build";
    case "search_budget_exceeded":
      return "raise maxSearchCostUsd or narrow allowed models, then run npm run build";
    case "no_passing_build":
      return "keep baseline and inspect failing eval evidence";
    case "incomplete_evidence":
      return "fix missing terminal usage or grader evidence, then run npm run build";
  }
}

async function profilePreferredTransforms(
  lowered: Awaited<ReturnType<typeof lowerContext>>,
): Promise<ReadonlyMap<string, string>> {
  const preferred = new Map<string, string>();
  for (const segment of lowered.ir.segments) {
    if (segment.safety !== "S4" ||
        /(?:opaque|signed|signature|jwt|token|cipher|encrypted)/i.test(segment.id)) continue;
    const body = lowered.bodies.get(segment.bodyHandle);
    if (!body) throw new Error(`cave_context_body_missing:${segment.id}`);
    const type = await detectEngineType(body);
    if (/^[a-z0-9-]+$/.test(type) && type !== "unknown") {
      preferred.set(segment.id, `caveman.engine.${type}.v1`);
    }
  }
  return preferred;
}

async function detectEngineType(input: Uint8Array): Promise<string> {
  const command = process.env.CAVEMAN_ENGINE_BIN ?? "caveman-engine";
  return new Promise((accept, reject) => {
    const env = buildEngineEnv();
    const invocation = portableInvocation(command, ["detect"], { env });
    const child = spawn(invocation.command, [...invocation.args], {
      env,
      stdio: ["pipe", "pipe", "pipe"],
    });
    const stdout: Buffer[] = [];
    const stderr: Buffer[] = [];
    let outputBytes = 0;
    let settled = false;
    const finish = (error?: Error, value?: string): void => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (error) reject(error);
      else accept(value ?? "");
    };
    const terminate = (error: Error): void => {
      child.kill("SIGKILL");
      child.stdin.destroy();
      child.stdout.destroy();
      child.stderr.destroy();
      finish(error);
    };
    const collect = (target: Buffer[], chunk: Buffer): void => {
      outputBytes += chunk.byteLength;
      if (outputBytes > 64 * 1024) {
        terminate(new Error("engine_detect_output_limit"));
        return;
      }
      target.push(chunk);
    };
    const timeoutMS = engineDetectTimeoutMS();
    const timer = setTimeout(() => terminate(new Error("engine_detect_timeout")), timeoutMS);
    timer.unref();
    child.stdout.on("data", (chunk: Buffer) => collect(stdout, chunk));
    child.stderr.on("data", (chunk: Buffer) => collect(stderr, chunk));
    child.once("error", (error) => finish(error));
    child.once("close", (code) => {
      if (settled) return;
      if (code !== 0) {
        finish(new Error(`engine_detect_exit_${code}:${Buffer.concat(stderr).toString("utf8").slice(0, 256)}`));
        return;
      }
      finish(undefined, Buffer.concat(stdout).toString("utf8").trim());
    });
    child.stdin.on("error", () => undefined);
    child.stdin.end(input);
  });
}

function engineDetectTimeoutMS(): number {
  const value = Number(process.env.CAVE_AGENT_ENGINE_DETECT_TIMEOUT_MS ?? 10_000);
  return Number.isSafeInteger(value) && value > 0 && value <= 10_000 ? value : 10_000;
}

function configuredModelCandidates(baseline: string): string[] {
  const models = new Set<string>([baseline]);
  if (process.env.ANTHROPIC_API_KEY) models.add("anthropic/claude-haiku-4-5");
  if (process.env.OPENAI_API_KEY) models.add("openai/gpt-5.4-mini");
  if (process.env.GEMINI_API_KEY || process.env.GOOGLE_API_KEY) models.add("google/gemini-2.5-flash");
  return [...models].sort();
}

async function check(args: string[]): Promise<void> {
  const root = process.cwd();
  const configPath = args[0] ?? "caveman.config.ts";
  const loaded = await loadBuildInputs(root, configPath);
  let lock: CaveBuildLock;
  try {
    lock = await readLock(root);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      throw new Error("cave_build_lock_missing: approve required evals, then run npm run build");
    }
    throw error;
  }
  const transformRegistrySha256 = await transformRegistrySHA256();
  const checked = checkLock(lock, {
    sourceSha256: loaded.sourceSha256,
    agentDefinitionSha256: agentDefinitionSHA256(loaded.agent),
    contextIRSha256: contextIRSHA256(await lowerBuildContext(
      root,
      loaded.agent,
    ).then((value) => value.ir)),
    evalSuiteSha256: sha256(stableStringify(loaded.evals.filter((item) => item.approved && item.required))),
    runtimeVersion: FRAMEWORK_VERSION,
    adapterVersion: PI_ADAPTER_VERSION,
    upstreamVersion: PI_UPSTREAM_VERSION,
    transformRegistrySha256,
    catalogSha256: CATALOG_SHA256,
  });
  if (!checked.valid) {
    throw new Error(`cave_stale_lock:${checked.stale.join(",")}: run npm run build to relock`);
  }
  process.stdout.write([
    `lock valid: ${lock.build_sha256}`,
    `plan: ${lock.selected_plan_id}`,
    `catalog cost/task: $${lock.evidence.catalog_cost_usd_per_task.toFixed(6)}`,
    "local evidence: passed (estimate only)",
    "provider savings: not claimed",
    "",
  ].join("\n"));
}

type LoadedBuildInputs = {
  config: BuildConfig;
  agent: AgentDefinition;
  evals: EvalDefinition[];
  sourceSha256: string;
};

const SOURCE_PATTERNS = [
  "src/**/*.ts",
  "src/**/*.tsx",
  "src/**/*.js",
  "src/**/*.mjs",
  "src/**/*.cjs",
  "src/**/*.json",
] as const;
const PACKAGE_STATE_FILES = [
  "package.json",
  "package-lock.json",
  "npm-shrinkwrap.json",
  "pnpm-lock.yaml",
  "yarn.lock",
] as const;

async function loadBuildInputs(
  root: string,
  configPath: string,
  beforeSourceHash?: (
    inputs: Omit<LoadedBuildInputs, "sourceSha256">,
  ) => Promise<void>,
): Promise<LoadedBuildInputs> {
  const configAbsolute = resolve(root, configPath);
  const imported = await importFresh(configAbsolute) as { default?: BuildConfig; config?: BuildConfig };
  const config = imported.default ?? imported.config;
  if (!config || config.lock !== "strict" || config.sandbox !== "required") {
    throw new Error("caveman build: config must use strict lock and required sandbox");
  }
  const entryAbsolute = resolve(root, config.entry);
  const agent = await loadAgent(entryAbsolute);
  const evalFiles: string[] = [];
  for await (const path of glob(config.evals, { cwd: root })) evalFiles.push(resolve(root, path));
  evalFiles.sort();
  const evals: EvalDefinition[] = [];
  for (const path of evalFiles) {
    const module = await importFresh(path) as Record<string, unknown>;
    for (const value of Object.values(module)) {
      if (isEval(value)) evals.push(value);
    }
  }
  await beforeSourceHash?.({ config, agent, evals });
  const sourceFiles = new Set<string>([configAbsolute, entryAbsolute, ...evalFiles]);
  for (const pattern of SOURCE_PATTERNS) {
    for await (const path of glob(pattern, { cwd: root })) sourceFiles.add(resolve(root, path));
  }
  for (const source of agentFileSourcePaths(agent)) {
    sourceFiles.add(resolve(root, source));
  }
  for (const fixture of evals) {
    if (fixture.tools.sandbox) sourceFiles.add(resolve(root, fixture.tools.sandbox));
  }
  for (const name of PACKAGE_STATE_FILES) {
    const path = resolve(root, name);
    try {
      await readFile(path);
      sourceFiles.add(path);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    }
  }
  const sourceSha256 = await sourceGraphSHA256(root, sourceFiles);
  return { config, agent, evals, sourceSha256 };
}

async function validLockIdentity(
  root: string,
  entry: string,
  expectedAgent?: AgentDefinition,
  beforeSourceHash?: Parameters<typeof loadBuildInputs>[2],
): Promise<CaveBuildLock | undefined> {
  try {
    const lock = await readLock(root);
    const loaded = await loadBuildInputs(root, "caveman.config.ts", beforeSourceHash);
    if (resolve(root, loaded.config.entry) !== resolve(root, entry)) {
      throw new Error("cave_stale_lock:entry");
    }
    if (expectedAgent !== undefined &&
        agentDefinitionSHA256(loaded.agent) !== agentDefinitionSHA256(expectedAgent)) {
      throw new Error("cave_stale_lock:dev_snapshot");
    }
    const checked = checkLock(lock, {
      sourceSha256: loaded.sourceSha256,
      agentDefinitionSha256: agentDefinitionSHA256(loaded.agent),
      contextIRSha256: contextIRSHA256(await lowerBuildContext(
        root,
        loaded.agent,
      ).then((value) => value.ir)),
      evalSuiteSha256: sha256(stableStringify(loaded.evals.filter((item) => item.approved && item.required))),
      runtimeVersion: FRAMEWORK_VERSION,
      adapterVersion: PI_ADAPTER_VERSION,
      upstreamVersion: PI_UPSTREAM_VERSION,
      transformRegistrySha256: await transformRegistrySHA256(),
      catalogSha256: CATALOG_SHA256,
    });
    if (!checked.valid) throw new Error(`cave_stale_lock:${checked.stale.join(",")}: run npm run build to relock`);
    return lock;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw error;
  }
}

function lowerBuildContext(root: string, definition: AgentDefinition, input?: string) {
  return lowerAgentContext(definition, {
    rootDir: root,
    ...(input === undefined ? {} : { input }),
  });
}

async function transformRegistrySHA256(): Promise<string> {
  return (await loadTransformRegistry()).sha256;
}

async function loadTransformRegistry(): Promise<{
  sha256: string;
  capabilities: TransformCapability[];
}> {
  const command = process.env.CAVEMAN_ENGINE_BIN ?? "caveman-engine";
  try {
    const env = buildEngineEnv();
    const invocation = portableInvocation(command, ["registry"], { env });
    const { stdout } = await execFileAsync(invocation.command, [...invocation.args], {
      encoding: "utf8",
      env,
      maxBuffer: 2 << 20,
      timeout: 10_000,
    });
    const parsed = JSON.parse(stdout) as {
      registry_sha256?: unknown;
      capabilities?: Array<{
        transform_id?: unknown;
        eligible_segment_kinds?: unknown;
      }>;
    };
    if (typeof parsed.registry_sha256 !== "string" || !/^[0-9a-f]{64}$/.test(parsed.registry_sha256)) {
      throw new Error("registry output has no valid registry_sha256");
    }
    if (!Array.isArray(parsed.capabilities) || parsed.capabilities.length === 0) {
      throw new Error("registry output has no capabilities");
    }
    const capabilities = parsed.capabilities.map((capability): TransformCapability => {
      if (typeof capability.transform_id !== "string" ||
          !Array.isArray(capability.eligible_segment_kinds) ||
          capability.eligible_segment_kinds.some((kind) => typeof kind !== "string")) {
        throw new Error("registry output has invalid capability");
      }
      return {
        transformID: capability.transform_id,
        segmentKinds: capability.eligible_segment_kinds as TransformCapability["segmentKinds"],
      };
    });
    return { sha256: parsed.registry_sha256, capabilities };
  } catch (error) {
    throw new Error(
      `cave_transform_registry_unavailable: run caveman setup or set CAVEMAN_ENGINE_BIN (${error instanceof Error ? error.message : String(error)})`,
    );
  }
}

async function engineEntitled(): Promise<boolean> {
  const command = process.env.CAVEMAN_CLI_BIN ?? "caveman";
  try {
    const env = buildRuntimeControlEnv();
    const invocation = portableInvocation(command, ["status", "--json"], { env });
    const { stdout } = await execFileAsync(invocation.command, [...invocation.args], {
      encoding: "utf8",
      env,
      maxBuffer: 2 << 20,
      timeout: 10_000,
    });
    const parsed = JSON.parse(stdout) as { mode?: unknown; seat?: { entitled?: unknown } };
    return parsed.seat?.entitled === true && parsed.mode === "compress";
  } catch {
    return false;
  }
}

function runtimeReasoning(value: CavePlan["reasoning"]): AgentDefinition["reasoning"] {
  return value === "none" ? "off" : value;
}

async function runFixture(
  root: string,
  entry: string,
  definition: AgentDefinition,
  plan: CavePlan,
  fixture: EvalDefinition,
  seed: number,
  sandboxConformance: boolean,
  privacyConformance: boolean,
  conversations: Map<string, ConversationState>,
  signal: AbortSignal,
): Promise<RunEvidence> {
  try {
    const selected = {
      ...definition,
      model: plan.model,
      reasoning: runtimeReasoning(plan.reasoning),
      sandbox: fixture.tools.mode === "live" ? "required" : "fixture",
    } as AgentDefinition;
    const conversationKey = `${plan.plan_id}\u0000${fixture.id}\u0000${seed}`;
    const conversation = conversations.get(conversationKey) ?? createConversation();
    conversations.set(conversationKey, conversation);
    const result = await runAgentInternal(selected, fixtureInput(fixture.input), {
      rootDir: root,
      entryPath: entry,
      candidatePlan: plan,
      sessionId: `compile:${sha256(plan.plan_id).slice(0, 16)}:${sha256(`${fixture.id}\u0000${seed}`).slice(0, 16)}`,
      conversation,
      signal,
      ...(fixture.tools.mode === "live" ? {
        sandboxProfile: await loadEvalSandboxProfile(root, fixture),
      } : {}),
    });
    const priced = catalogCost(result);
    const graders = fixture.quality.map((grader) => ({
      type: grader.type,
      passed: grade(grader, result.text, result.toolCalls),
    }));
    return {
      terminal: true,
      usage_basis: result.usageBasis === "provider_reported" ? "provider_reported" : "missing",
      price_basis: result.priceBasis,
      catalog_cost_usd: priced.usd,
      quality_score: graders.filter((item) => item.passed).length / graders.length,
      graders,
      latency_ms: result.latencyMs,
      provider_visible_tokens: result.inputTokens + result.cacheReadTokens + result.cacheWriteTokens,
      cache_prefix_sha256: result.cachePrefixSHA256,
      cache_boundary_known: result.cacheBoundaryKnown,
      cache_read_tokens: result.cacheReadTokens,
      cache_write_tokens: result.cacheWriteTokens,
      cache_bust: result.cacheBust,
      error: false,
      recovery_resolved: result.recoveryResolved && result.transformFailures.length === 0,
      privacy_passed: privacyConformance && contentBlindRunEvidence(result, fixture),
      sandbox_passed: sandboxConformance && (definition.tools.length === 0 ||
        (definition.sandbox === "required" && result.toolCalls.length > 0)),
      unknown_transform: plan.segment_routes.some((route) =>
        !result.evaluatedTransformIDs.includes(route.transform_id)),
      output_digest: sha256(result.text),
    };
  } catch (error) {
    throw new Error("cave_fixture_terminal_evidence_missing", { cause: error });
  }
}

async function loadEvalSandboxProfile(
  root: string,
  fixture: EvalDefinition,
): Promise<{
  network: boolean;
  childProcess: boolean;
  credentialEnv: readonly string[];
}> {
  const path = fixture.tools.sandbox;
  if (!path) throw new Error("cave_live_eval_sandbox_profile_missing");
  const fullPath = resolve(root, path);
  const relativePath = relative(root, fullPath);
  if (relativePath === ".." || relativePath.startsWith("../") ||
      relativePath.startsWith("..\\") || isAbsolute(relativePath)) {
    throw new Error("cave_live_eval_sandbox_profile_escapes_root");
  }
  const parsed = JSON.parse(await readFile(fullPath, "utf8")) as Record<string, unknown>;
  const keys = Object.keys(parsed).sort();
  const expected = ["child_process", "credential_env", "network", "schema_version"];
  if (keys.length !== expected.length || keys.some((key, index) => key !== expected[index]) ||
      parsed.schema_version !== 1 || typeof parsed.network !== "boolean" ||
      typeof parsed.child_process !== "boolean" || !Array.isArray(parsed.credential_env) ||
      parsed.credential_env.some((name) => typeof name !== "string" ||
        !/^[A-Z][A-Z0-9_]{0,127}$/.test(name))) {
    throw new Error("cave_live_eval_sandbox_profile_invalid");
  }
  validateSandboxCredentialEnv(parsed.credential_env as string[]);
  return {
    network: parsed.network,
    childProcess: parsed.child_process,
    credentialEnv: parsed.credential_env as string[],
  };
}

function contextIRIsContentBlind(ir: Awaited<ReturnType<typeof lowerContext>>["ir"]): boolean {
  const allowed = new Set([
    "id", "kind", "stability", "safety", "priority", "recovery", "cacheRegion",
    "privacy", "opaque", "ttlTurns", "provenanceDigest", "tokenCount", "bodyHandle",
  ]);
  return ir.segments.every((segment) =>
    Object.keys(segment).every((key) => allowed.has(key)) &&
    /^cave_local_sha256:[0-9a-f]{64}$/.test(segment.bodyHandle) &&
    /^[0-9a-f]{64}$/.test(segment.provenanceDigest));
}

function contentBlindRunEvidence(
  result: Awaited<ReturnType<typeof runAgentInternal>>,
  fixture: EvalDefinition,
): boolean {
  const exported = stableStringify({
    run_id: sha256(result.runId),
    agent_id: result.agentId,
    context_bill: result.contextBill,
    usage_basis: result.usageBasis,
    usage: {
      input: result.inputTokens,
      output: result.outputTokens,
      cache_read: result.cacheReadTokens,
      cache_write: result.cacheWriteTokens,
      reasoning_basis: result.reasoningUsageBasis,
      reasoning: result.reasoningTokens,
      cost_usd: result.costUsd,
    },
    provider: result.provider,
    model: result.model,
    latency_ms: result.latencyMs,
    tool_names: result.toolCalls,
    output_digest: sha256(result.text),
  });
  const forbidden = [fixtureInput(fixture.input), result.text]
    .filter((value) => value.length >= 4);
  return forbidden.every((value) => !exported.includes(value));
}

function grade(grader: QualityGrader, text: string, toolCalls: string[]): boolean {
  if (grader.type === "contains") return grader.fragments.every((fragment) => text.includes(fragment));
  if (grader.type === "tool_called") return grader.tools.every((tool) => toolCalls.includes(tool));
  if (grader.type === "exact_match") return text === grader.expected;
  if (grader.type === "json_schema") {
    try {
      return Value.Check(grader.schema, JSON.parse(text));
    } catch {
      return false;
    }
  }
  return false;
}

function baselinePlan(definition: AgentDefinition, root: string, contexts: readonly ContextIR[]): CavePlan {
  const model = typeof definition.model === "string"
    ? definition.model
    : process.env.CAVE_MODEL ?? localProviderModel(root) ?? detectedModel();
  const bills = contexts.map(contextBill);
  const max = (kinds: readonly string[]) => Math.max(0, ...bills.map((bill) =>
    kinds.reduce((sum, kind) => sum + (bill[kind] ?? 0), 0)));
  return {
    schema_version: 1,
    plan_id: "baseline.pass-through",
    model,
    reasoning: definition.reasoning === "off" ? "none" : definition.reasoning,
    segment_routes: [],
    budgets: {
      instructions: max(["instruction"]),
      tools: max(["tool_schema"]),
      memory: max(["memory"]),
      history: Math.max(max(["history"]), (definition.output?.maxTokens ?? 2_000) * 2),
      results_artifacts: Math.max(
        max(["artifact", "skill", "tool_result"]),
        definition.tools.length * (definition.output?.maxTokens ?? 2_000) * 2,
      ),
      reasoning: definition.reasoning === "off" ? 0 : definition.output?.maxTokens ?? 2_000,
      output: definition.output?.maxTokens ?? 2_000,
      retry_cascade_reserve: Math.max(256, Math.ceil(max(["history", "tool_result"]) * 0.1)),
    },
    recovery: { namespace: definition.id, tools: [] },
    fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
  };
}

function evalDynamicKinds(evals: readonly EvalDefinition[]): ReadonlySet<ContextKind> {
  const kinds = new Set<ContextKind>();
  if (evals.some((fixture) => fixture.quality.some((grader) => grader.type === "tool_called"))) {
    kinds.add("history");
    kinds.add("tool_result");
  }
  return kinds;
}

function localProviderModel(root: string): string | undefined {
  try {
    const parsed = JSON.parse(readFileSync(resolve(root, ".caveman/provider.json"), "utf8")) as { model?: unknown };
    return typeof parsed.model === "string" ? parsed.model : undefined;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw new Error("caveman build: invalid .caveman/provider.json");
  }
}

function detectedModel(): string {
  const configured = [
    process.env.ANTHROPIC_API_KEY && "anthropic/claude-haiku-4-5",
    process.env.OPENAI_API_KEY && "openai/gpt-5.4-mini",
    (process.env.GEMINI_API_KEY || process.env.GOOGLE_API_KEY) && "google/gemini-2.5-flash",
  ].filter((value): value is string => typeof value === "string");
  if (configured.length !== 1) {
    throw new Error("caveman build: set CAVE_MODEL when zero or multiple provider credentials exist");
  }
  return configured[0]!;
}

async function loadAgent(path: string): Promise<AgentDefinition> {
  return agentFromImported(await importFresh(path));
}

function agentFromImported(imported: unknown): AgentDefinition {
  const exported = imported as { default?: AgentDefinition; agent?: AgentDefinition };
  const definition = exported.default ?? exported.agent;
  if (!definition || definition.kind !== "agent") throw new Error("caveman agent: entry must export default agent()");
  return definition;
}

async function readLock(root: string): Promise<CaveBuildLock> {
  return parseCaveBuildLock(JSON.parse(await readFile(resolve(root, ".caveman/agent.lock.json"), "utf8")));
}

function importFresh(path: string): Promise<unknown> {
  return import(`${pathToFileURL(path).href}?cave=${Date.now()}-${crypto.randomUUID()}`);
}

function isEval(value: unknown): value is EvalDefinition {
  return value !== null && typeof value === "object" && (value as { kind?: unknown }).kind === "eval";
}

function fixtureInput(value: unknown): string {
  return typeof value === "string" ? value : JSON.stringify(value);
}

function formatBill(bill: Record<string, number>): string {
  return Object.entries(bill).sort(([a], [b]) => a.localeCompare(b)).map(([kind, tokens]) => `${kind}=${tokens}`).join(" ");
}

main().catch((error) => {
  const message = error instanceof Error ? error.message : String(error);
  process.stderr.write(`caveman-agent: ${message}\n`);
  process.exitCode = 1;
});
