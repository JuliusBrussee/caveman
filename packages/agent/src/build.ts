import { mkdir, rename, rm, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { catalogSearchCeiling } from "./catalog.js";
import type { AgentDefinition } from "./index.js";
import { graphUsesHostSandbox } from "./definition-graph.js";
import type { ContextIR } from "./context-ir.js";
import { contextIRToWire, sha256, stableStringify } from "./context-ir.js";
import type { ContextKind, EvalDefinition, ToolDefinition } from "./primitives.js";

export interface BuildConfig {
  entry: string;
  evals: string;
  efficiency: "max";
  requiredFixturePassRate: number;
  qualityRetention: number;
  maxSearchCostUsd: number;
  lock: "strict";
  sandbox: "required";
  allowedModels?: string[];
  deniedModels?: string[];
  maxP95LatencyMs?: number;
  forbiddenSafetyClasses?: string[];
  dataResidency?: string;
}

export function defineBuild(options: Partial<BuildConfig> & Pick<BuildConfig, "entry" | "evals">): BuildConfig {
  if (options.dataResidency !== undefined) {
    throw new Error(
      "caveman build: dataResidency is not enforced yet; refusing to ignore residency policy",
    );
  }
  const config: BuildConfig = {
    entry: options.entry,
    evals: options.evals,
    efficiency: options.efficiency ?? "max",
    requiredFixturePassRate: options.requiredFixturePassRate ?? 1,
    qualityRetention: options.qualityRetention ?? 0.98,
    maxSearchCostUsd: options.maxSearchCostUsd ?? 2,
    lock: options.lock ?? "strict",
    sandbox: options.sandbox ?? "required",
    ...(options.allowedModels === undefined ? {} : { allowedModels: [...options.allowedModels] }),
    ...(options.deniedModels === undefined ? {} : { deniedModels: [...options.deniedModels] }),
    ...(options.maxP95LatencyMs === undefined ? {} : { maxP95LatencyMs: options.maxP95LatencyMs }),
    ...(options.forbiddenSafetyClasses === undefined ? {} : { forbiddenSafetyClasses: [...options.forbiddenSafetyClasses] }),
  };
  if (!(config.requiredFixturePassRate > 0 && config.requiredFixturePassRate <= 1)) {
    throw new Error("caveman build: requiredFixturePassRate must be in (0,1]");
  }
  if (!(config.qualityRetention > 0 && config.qualityRetention <= 1)) {
    throw new Error("caveman build: qualityRetention must be in (0,1]");
  }
  if (!(config.maxSearchCostUsd > 0)) {
    throw new Error("caveman build: maxSearchCostUsd must be positive");
  }
  return Object.freeze(config);
}

export interface CavePlan {
  schema_version: 1;
  plan_id: string;
  model: string;
  reasoning: "none" | "minimal" | "low" | "medium" | "high";
  segment_routes: Array<{
    segment_kind: ContextKind;
    segment_id?: string;
    transform_id: string;
    fallback: "original";
  }>;
  budgets: {
    instructions: number;
    tools: number;
    memory: number;
    history: number;
    results_artifacts: number;
    reasoning: number;
    output: number;
    retry_cascade_reserve: number;
  };
  recovery: {
    namespace: string;
    tools: Array<"cave_retrieve" | "cave_search_tools" | "cave_memory_search" | "cave_artifact_page">;
  };
  fallbacks: {
    unknown: "original";
    transform_error: "original";
    not_smaller: "original";
  };
}

export interface CandidatePlan {
  plan: CavePlan;
  estimated_cost_usd_per_run: number;
  static_rejection?: "unsupported_provider" | "forbidden_transform" | "unpriced_model" | "dominated" | "policy_denied";
  requires_entitlement?: boolean;
}

export interface RunEvidence {
  terminal: boolean;
  usage_basis: "provider_reported" | "estimated" | "missing";
  price_basis: "public_catalog" | "unpriced";
  catalog_cost_usd: number;
  quality_score: number;
  graders: Array<{ type: string; passed: boolean }>;
  latency_ms: number;
  provider_visible_tokens: number;
  cache_prefix_sha256: string;
  cache_boundary_known: boolean;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cache_bust: boolean;
  error: boolean;
  recovery_resolved: boolean;
  privacy_passed: boolean;
  sandbox_passed: boolean;
  unknown_event?: boolean;
  unknown_transform?: boolean;
  output_digest: string;
}

export type BuildHarnessID = "pi" | "claude" | "vercel-ai-sdk" | "eve" | "mastra";

export interface CompileInput {
  agent: AgentDefinition;
  contextIR: ContextIR;
  evals: EvalDefinition[];
  candidates?: CandidatePlan[];
  baselinePlan: CavePlan;
  modelCandidates?: string[];
  seeds?: number[];
  config: BuildConfig;
  entitled: boolean;
  sourceSha256: string;
  catalogSha256: string;
  transformRegistrySha256: string;
  externalProvenanceSha256?: string;
  runtimeVersion: string;
  adapterVersion: string;
  upstreamVersion: string;
  harnessId?: BuildHarnessID;
  /** Harness deadline override. Production uses its configured default. */
  runnerTimeoutMs?: number;
  runner: (request: {
    plan: CavePlan;
    eval: EvalDefinition;
    seed: number;
    signal: AbortSignal;
  }) => Promise<RunEvidence>;
}

export type CompileStatus =
  | "locked"
  | "no_passing_build"
  | "needs_eval"
  | "needs_login"
  | "search_budget_exceeded"
  | "incomplete_evidence";

export interface CaveBuildLock {
  schema_version: 2;
  agent_id: string;
  build_sha256: string;
  plan_sha256: string;
  source_sha256: string;
  agent_definition_sha256: string;
  context_ir_sha256: string;
  eval_suite_sha256: string;
  context_ir_schema: "1";
  harness: {
    id: BuildHarnessID;
    adapter_version: string;
    upstream_version: string;
  };
  runtime: {
    caveman_version: string;
    transform_registry_sha256: string;
    external_provenance_sha256: string;
  };
  catalog_sha256: string;
  baseline_plan_id: string;
  selected_plan_id: string;
  selected_plan: CavePlan;
  evidence: {
    status: "locked";
    basis: "inferred";
    quality_retention_lcb95: number;
    error_rate: number;
    p95_latency_ms: number;
    catalog_cost_usd_per_task: number;
    completed_runs: number;
  };
}

export interface CompileResult {
  status: CompileStatus;
  estimated_ceiling_usd: number;
  planned_runs: number;
  completed_runs: number;
  static_rejections: number;
  actual_cost_usd: number | null;
  best_observed_plan_id?: string;
  baseline_catalog_cost_usd_per_task?: number;
  selected_catalog_cost_usd_per_task?: number;
  lock?: CaveBuildLock;
  reason?: string;
}

type Completed = {
  candidate: CandidatePlan;
  evidence: Array<{ fixture: string; seed: number; value: RunEvidence }>;
  costPerTask: number;
  errorRate: number;
  p95Latency: number;
  fixturePassRate: number;
  retentionLCB95: number;
  passing: boolean;
};

export async function compile(input: CompileInput): Promise<CompileResult> {
  // An unsandboxed run cannot produce a lock. Host mode runs tool
  // closures in the host process, so its evidence shows no containment and is
  // refused here rather than downgraded to a soft status. The whole definition
  // graph is checked, not just the root: a host subagent under a fixture root
  // runs its closures in this process exactly as a host root would, and the CLI
  // saves that lock all the same. Coding agents lock by compiling against
  // fixture corpora with a contained sandbox mode.
  if (graphUsesHostSandbox(input.agent)) {
    throw new Error("cave_host_sandbox_lock_ineligible");
  }
  const approved = input.evals.filter((fixture) => fixture.approved && fixture.required);
  if (approved.length === 0) {
    return emptyResult("needs_eval", "no approved required eval fixture");
  }
  const seeds = input.seeds ?? [1, 2, 3, 4, 5];
  if (seeds.length === 0 || new Set(seeds).size !== seeds.length) {
    throw new Error("caveman build: seeds must be a non-empty unique set");
  }
  const candidates = input.candidates ?? generateCandidatePlans(
    input.agent,
    input.contextIR,
    input.baselinePlan,
    input.modelCandidates ?? [input.baselinePlan.model],
    true,
    new Map(),
    ENGINE_TRANSFORM_CAPABILITIES,
    new Set(input.evals.some((fixture) =>
      fixture.quality.some((grader) => grader.type === "tool_called"))
      ? ["history", "tool_result"] as ContextKind[]
      : []),
    // The public compile() enforces the same model/safety policy and catalog
    // pricing the CLI does — an unpriced or policy-denied model is rejected
    // here, not only in the CLI.
    {
      ...(input.config.allowedModels === undefined ? {} : { allowedModels: input.config.allowedModels }),
      ...(input.config.deniedModels === undefined ? {} : { deniedModels: input.config.deniedModels }),
      ...(input.config.forbiddenSafetyClasses === undefined
        ? {}
        : { forbiddenSafetyClasses: input.config.forbiddenSafetyClasses }),
    },
  );
  if (candidates.length === 0) throw new Error("caveman build: generated no candidate plans");
  if (!input.entitled && candidates.some((candidate) => candidate.requires_entitlement && !candidate.static_rejection)) {
    return emptyResult("needs_login", "entitled engine required for eligible candidates");
  }

  const runnable = candidates.filter((candidate) => !candidate.static_rejection);
  const staticRejections = candidates.length - runnable.length;
  const plannedRuns = runnable.length * approved.length * seeds.length;
  const estimatedCeiling = roundUsd(
    runnable.reduce((sum, candidate) => sum + candidate.estimated_cost_usd_per_run * approved.length * seeds.length, 0),
  );
  if (estimatedCeiling > input.config.maxSearchCostUsd) {
    return {
      status: "search_budget_exceeded",
      estimated_ceiling_usd: estimatedCeiling,
      planned_runs: plannedRuns,
      completed_runs: 0,
      static_rejections: staticRejections,
      actual_cost_usd: 0,
      reason: "estimated public-catalog ceiling exceeds configured cap",
    };
  }

  let actualCost = 0;
  let completedRuns = 0;
  let incomplete = false;
  let unknownGraderType: string | undefined;
  let budgetExceeded = false;
  const byPlan = new Map<string, Completed["evidence"]>();

  outer:
  for (const candidate of runnable) {
    const evidence: Completed["evidence"] = [];
    byPlan.set(candidate.plan.plan_id, evidence);
    for (const fixture of approved) {
      for (const seed of seeds) {
        if (actualCost >= input.config.maxSearchCostUsd) {
          budgetExceeded = true;
          break outer;
        }
        let value: RunEvidence;
        try {
          value = await withDeadline(
            (signal) => input.runner({ plan: candidate.plan, eval: fixture, seed, signal }),
            input.runnerTimeoutMs ?? 300_000,
          );
        } catch {
          return {
            estimated_ceiling_usd: estimatedCeiling,
            planned_runs: plannedRuns,
            completed_runs: completedRuns,
            static_rejections: staticRejections,
            actual_cost_usd: null,
            status: "incomplete_evidence",
            reason: "runner failed or timed out before terminal usage evidence; actual cost unknown",
          };
        }
        actualCost = roundUsd(actualCost + Math.max(0, value.catalog_cost_usd));
        completedRuns++;
        evidence.push({ fixture: fixture.id, seed, value });
        if (!completeEvidence(value)) {
          incomplete = true;
          unknownGraderType ??= firstUnknownGrader(value);
        }
      }
    }
  }

  const base = {
    estimated_ceiling_usd: estimatedCeiling,
    planned_runs: plannedRuns,
    completed_runs: completedRuns,
    static_rejections: staticRejections,
    actual_cost_usd: actualCost,
  };
  if (budgetExceeded || completedRuns !== plannedRuns) {
    return {
      ...base,
      status: budgetExceeded ? "search_budget_exceeded" : "incomplete_evidence",
      reason: budgetExceeded ? "actual public-catalog search cap reached" : "run cardinality incomplete",
    };
  }
  if (incomplete) {
    return {
      ...base,
      status: "incomplete_evidence",
      reason: unknownGraderType !== undefined
        ? `unknown grader type "${unknownGraderType}": not in the supported grader taxonomy (public/evals)`
        : "usage, terminal, grader, recovery, privacy, or sandbox evidence missing",
    };
  }

  const baselineEvidence = byPlan.get(input.baselinePlan.plan_id);
  if (!baselineEvidence || baselineEvidence.length !== approved.length * seeds.length) {
    return { ...base, status: "incomplete_evidence", reason: "complete baseline evidence missing" };
  }
  const evalSuiteSha256 = sha256(stableStringify(approved));
  const completed = runnable.map((candidate) => summarizeCandidate(
    candidate,
    byPlan.get(candidate.plan.plan_id) ?? [],
    baselineEvidence,
    input.config,
    evalSuiteSha256,
    approved,
  ));
  const passing = completed.filter((candidate) => candidate.passing);
  const bestObserved = [...completed].sort(selectionOrder)[0];
  const baselineCompleted = completed.find((candidate) =>
    candidate.candidate.plan.plan_id === input.baselinePlan.plan_id
  );
  if (baselineCompleted === undefined) {
    return { ...base, status: "incomplete_evidence", reason: "baseline candidate summary missing" };
  }
  if (passing.length === 0) {
    return {
      ...base,
      status: "no_passing_build",
      baseline_catalog_cost_usd_per_task: baselineCompleted.costPerTask,
      ...(bestObserved === undefined ? {} : { best_observed_plan_id: bestObserved.candidate.plan.plan_id }),
      reason: "no complete candidate met quality and guardrail floors",
    };
  }
  passing.sort(selectionOrder);
  const selected = passing[0]!;
  const planSha256 = sha256(stableStringify(selected.candidate.plan));
  const lockWithoutBuild = {
    schema_version: 2 as const,
    agent_id: input.agent.id,
    plan_sha256: planSha256,
    source_sha256: input.sourceSha256,
    agent_definition_sha256: agentDefinitionSHA256(input.agent),
    context_ir_sha256: contextIRSHA256(input.contextIR),
    eval_suite_sha256: evalSuiteSha256,
    context_ir_schema: "1" as const,
    harness: {
      id: input.harnessId ?? "pi",
      adapter_version: input.adapterVersion,
      upstream_version: input.upstreamVersion,
    },
    runtime: {
      caveman_version: input.runtimeVersion,
      transform_registry_sha256: input.transformRegistrySha256,
      external_provenance_sha256: input.externalProvenanceSha256 ?? "",
    },
    catalog_sha256: input.catalogSha256,
    baseline_plan_id: input.baselinePlan.plan_id,
    selected_plan_id: selected.candidate.plan.plan_id,
    selected_plan: selected.candidate.plan,
    evidence: {
      status: "locked" as const,
      basis: "inferred" as const,
      quality_retention_lcb95: selected.retentionLCB95,
      error_rate: selected.errorRate,
      p95_latency_ms: selected.p95Latency,
      catalog_cost_usd_per_task: selected.costPerTask,
      completed_runs: selected.evidence.length,
    },
  };
  const buildSha256 = sha256(stableStringify(lockWithoutBuild));
  const lock: CaveBuildLock = {
    ...lockWithoutBuild,
    build_sha256: buildSha256,
  };
  return {
    ...base,
    status: "locked",
    best_observed_plan_id: selected.candidate.plan.plan_id,
    baseline_catalog_cost_usd_per_task: baselineCompleted.costPerTask,
    selected_catalog_cost_usd_per_task: selected.costPerTask,
    lock,
  };
}

async function withDeadline<T>(
  invoke: (signal: AbortSignal) => Promise<T>,
  timeoutMs: number,
): Promise<T> {
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0) {
    throw new Error("caveman build: runner timeout must be a positive integer");
  }
  const controller = new AbortController();
  let timer: NodeJS.Timeout | undefined;
  const deadline = new Promise<never>((_resolve, reject) => {
    timer = setTimeout(() => {
      const error = new Error("cave_build_runner_timeout");
      controller.abort(error);
      reject(error);
    }, timeoutMs);
  });
  try {
    // Abort asks cooperative runners to stop; the independent race prevents an
    // adapter that ignores AbortSignal from pinning the compiler forever. A late
    // runner can never mint a lock or known actual cost after this branch returns.
    return await Promise.race([Promise.resolve().then(() => invoke(controller.signal)), deadline]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

export async function compileAndWrite(input: CompileInput, lockPath = ".caveman/agent.lock.json"): Promise<CompileResult> {
  const result = await compile(input);
  if (!result.lock) return result;
  const target = resolve(lockPath);
  await mkdir(dirname(target), { recursive: true });
  const temporary = `${target}.${process.pid}.${crypto.randomUUID()}.tmp`;
  try {
    await writeFile(temporary, `${JSON.stringify(result.lock, null, 2)}\n`, { flag: "wx", mode: 0o600 });
    await rename(temporary, target);
  } finally {
    await rm(temporary, { force: true });
  }
  return result;
}

export function checkLock(lock: CaveBuildLock, current: {
  sourceSha256: string;
  agentDefinitionSha256: string;
  contextIRSha256: string;
  evalSuiteSha256: string;
  runtimeVersion: string;
  adapterVersion: string;
  upstreamVersion: string;
  transformRegistrySha256: string;
  externalProvenanceSha256?: string;
  catalogSha256: string;
}): { valid: boolean; stale: string[] } {
  const stale: string[] = lockIntegrityErrors(lock);
  if (lock.source_sha256 !== current.sourceSha256) stale.push("source");
  if (lock.agent_definition_sha256 !== current.agentDefinitionSha256) stale.push("agent_definition");
  if (lock.context_ir_sha256 !== current.contextIRSha256) stale.push("context_ir");
  if (lock.eval_suite_sha256 !== current.evalSuiteSha256) stale.push("eval_suite");
  if (lock.runtime.caveman_version !== current.runtimeVersion) stale.push("runtime");
  if (lock.harness.adapter_version !== current.adapterVersion) stale.push("adapter");
  if (lock.harness.upstream_version !== current.upstreamVersion) stale.push("upstream");
  if (lock.runtime.transform_registry_sha256 !== current.transformRegistrySha256) stale.push("transform_registry");
  if (lock.runtime.external_provenance_sha256 !== (current.externalProvenanceSha256 ?? "")) stale.push("external_provenance");
  if (lock.catalog_sha256 !== current.catalogSha256) stale.push("catalog");
  return { valid: stale.length === 0, stale };
}

export function parseCaveBuildLock(value: unknown): CaveBuildLock {
  if (!isRecord(value) || !exactKeys(value, [
    "schema_version", "agent_id", "build_sha256", "plan_sha256", "source_sha256",
    "agent_definition_sha256", "context_ir_sha256", "eval_suite_sha256",
    "context_ir_schema", "harness", "runtime", "catalog_sha256",
    "baseline_plan_id", "selected_plan_id", "selected_plan", "evidence",
  ])) {
    throw new Error("cave_invalid_lock:shape");
  }
  const lock = value as unknown as CaveBuildLock;
  if (lock.schema_version !== 2 || lock.context_ir_schema !== "1" ||
      !isRecord(lock.harness) || !exactKeys(lock.harness, ["id", "adapter_version", "upstream_version"]) ||
      !["pi", "claude", "vercel-ai-sdk", "eve", "mastra"].includes(lock.harness.id) ||
      !isRecord(lock.runtime) || !exactKeys(lock.runtime, ["caveman_version", "transform_registry_sha256", "external_provenance_sha256"]) ||
      !isRecord(lock.evidence) || !exactKeys(lock.evidence, [
        "status", "basis", "quality_retention_lcb95", "error_rate", "p95_latency_ms",
        "catalog_cost_usd_per_task", "completed_runs",
      ]) ||
      lock.evidence.status !== "locked" || lock.evidence.basis !== "inferred" ||
      !isRecord(lock.selected_plan) || !exactKeys(lock.selected_plan, [
        "schema_version", "plan_id", "model", "reasoning", "segment_routes",
        "budgets", "recovery", "fallbacks",
      ]) || !validPlanShape(lock.selected_plan)) {
    throw new Error("cave_invalid_lock:shape");
  }
  const integrity = lockIntegrityErrors(lock);
  if (integrity.length > 0) throw new Error(`cave_invalid_lock:${integrity.join(",")}`);
  return lock;
}

function validPlanShape(plan: Record<string, unknown>): boolean {
  if (plan.schema_version !== 1 || typeof plan.plan_id !== "string" || typeof plan.model !== "string" ||
      !["none", "minimal", "low", "medium", "high"].includes(String(plan.reasoning)) ||
      !isRecord(plan.budgets) || !exactKeys(plan.budgets, [
        "instructions", "tools", "memory", "history", "results_artifacts",
        "reasoning", "output", "retry_cascade_reserve",
      ]) ||
      Object.values(plan.budgets).some((value) => !Number.isSafeInteger(value) || Number(value) < 0) ||
      !isRecord(plan.recovery) || !exactKeys(plan.recovery, ["namespace", "tools"]) ||
      !isRecord(plan.fallbacks) || !exactKeys(plan.fallbacks, ["unknown", "transform_error", "not_smaller"]) ||
      !Array.isArray(plan.segment_routes)) {
    return false;
  }
  return plan.segment_routes.every((route) =>
    isRecord(route) &&
    (exactKeys(route, ["segment_kind", "transform_id", "fallback"]) ||
      exactKeys(route, ["segment_kind", "segment_id", "transform_id", "fallback"])) &&
    typeof route.segment_kind === "string" && typeof route.transform_id === "string" &&
    (route.segment_id === undefined || typeof route.segment_id === "string") &&
    route.fallback === "original");
}

function lockIntegrityErrors(lock: CaveBuildLock): string[] {
  const errors: string[] = [];
  if (sha256(stableStringify(lock.selected_plan)) !== lock.plan_sha256) errors.push("plan_digest");
  const { build_sha256: _build, ...withoutBuild } = lock;
  if (sha256(stableStringify(withoutBuild)) !== lock.build_sha256) errors.push("build_digest");
  return errors;
}

export function agentDefinitionSHA256(agent: AgentDefinition): string {
  return sha256(stableStringify(lockableValue(agent, new WeakSet<object>())));
}

export function toolDefinitionSHA256(tool: ToolDefinition): string {
  return sha256(stableStringify(lockableValue(tool, new WeakSet<object>())));
}

export function contextIRSHA256(contextIR: ContextIR): string {
  return sha256(stableStringify(contextIRToWire(contextIR)));
}

function lockableValue(value: unknown, seen: WeakSet<object>): unknown {
  if (value === null || typeof value === "string" || typeof value === "boolean" ||
      typeof value === "number") {
    return value;
  }
  if (typeof value === "bigint") return { cave_bigint: value.toString() };
  if (typeof value === "function" || typeof value === "undefined" || typeof value === "symbol") {
    return undefined;
  }
  if (typeof value !== "object") {
    throw new Error("caveman build: agent definition contains an unsupported value");
  }
  if (seen.has(value)) throw new Error("caveman build: cyclic agent definition is not lockable");
  seen.add(value);
  try {
    if (Array.isArray(value)) {
      return value.map((item) => lockableValue(item, seen) ?? null);
    }
    const projected: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value)) {
      const locked = lockableValue(item, seen);
      if (locked !== undefined) projected[key] = locked;
    }
    const implementationSource = Reflect.get(
      value,
      Symbol.for("@caveman-ai/agent:tool-implementation-source"),
    );
    if (typeof implementationSource === "string") {
      projected.cave_tool_implementation_sha256 = sha256(implementationSource);
    }
    return projected;
  } finally {
    seen.delete(value);
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function exactKeys(value: Record<string, unknown>, expected: string[]): boolean {
  const actual = Object.keys(value).sort();
  return actual.length === expected.length &&
    actual.every((key, index) => key === [...expected].sort()[index]);
}

export function generateCandidatePlans(
  agent: AgentDefinition,
  contextIR: ContextIR,
  baseline: CavePlan,
  models: string[],
  approvedEval: boolean,
  preferredTransforms: ReadonlyMap<string, string> = new Map(),
  transformCapabilities: readonly TransformCapability[] = ENGINE_TRANSFORM_CAPABILITIES,
  observedDynamicKinds: ReadonlySet<ContextKind> = new Set(),
  policy?: CandidatePolicy,
): CandidatePlan[] {
  const candidates: CandidatePlan[] = [{
    plan: baseline,
    estimated_cost_usd_per_run: 0.01,
  }];
  if (approvedEval) {
    const candidateRoute = (
      segmentKind: ContextKind,
      transformID: string,
      segmentID?: string,
    ): CavePlan["segment_routes"][number] => ({
      ...(segmentID === undefined ? {} : { segment_id: segmentID }),
      segment_kind: segmentKind,
      transform_id: transformID,
      fallback: "original",
    });
    for (const segment of contextIR.segments) {
      if (segment.safety !== "S4" || segment.opaque || opaqueSegmentID(segment.id)) continue;
      for (const capability of transformCapabilities) {
        if (!capability.segmentKinds.includes(segment.kind)) continue;
        const routes: CavePlan["segment_routes"] = [
          candidateRoute(segment.kind, capability.transformID, segment.id),
        ];
        candidates.push({
          plan: {
            ...baseline,
            plan_id: `${baseline.plan_id}.segment.${stableID(segment.id)}.${stableID(capability.transformID)}`,
            segment_routes: routes,
            recovery: { namespace: agent.id, tools: recoveryTools(routes) },
          },
          estimated_cost_usd_per_run: 0.008,
          requires_entitlement: true,
        });
      }
    }
    // History and tool results do not exist until runtime. They still need a
    // finite, eval-gated frontier so compiler can select a locked route using
    // real fixture traffic rather than silently leaving dynamic context out.
    const representedKinds = new Set(contextIR.segments.map((segment) => segment.kind));
    const dynamicKinds = [...observedDynamicKinds]
      .filter((kind): kind is "history" | "tool_result" =>
        kind === "history" || kind === "tool_result");
    for (const segmentKind of dynamicKinds) {
      if (representedKinds.has(segmentKind)) continue;
      for (const capability of transformCapabilities) {
        if (!capability.segmentKinds.includes(segmentKind)) continue;
        const routes: CavePlan["segment_routes"] = [
          candidateRoute(segmentKind, capability.transformID),
        ];
        candidates.push({
          plan: {
            ...baseline,
            plan_id: `${baseline.plan_id}.dynamic.${segmentKind}.${stableID(capability.transformID)}`,
            segment_routes: routes,
            recovery: { namespace: agent.id, tools: recoveryTools(routes) },
          },
          estimated_cost_usd_per_run: 0.008,
          requires_entitlement: true,
        });
      }
    }
    if (dynamicKinds.length > 1) {
      let combinations: CavePlan["segment_routes"][] = [[]];
      for (const segmentKind of dynamicKinds) {
        const options = transformCapabilities
          .filter((capability) => capability.segmentKinds.includes(segmentKind))
          .map((capability) => candidateRoute(segmentKind, capability.transformID));
        combinations = combinations.flatMap((routes) =>
          options.map((route) => [...routes, route]));
      }
      for (const routes of combinations) {
        candidates.push({
          plan: {
            ...baseline,
            plan_id: `${baseline.plan_id}.dynamic-joint.${routes.map((route) =>
              `${route.segment_kind}.${stableID(route.transform_id)}`).join(".")}`,
            segment_routes: routes,
            recovery: { namespace: agent.id, tools: recoveryTools(routes) },
          },
          estimated_cost_usd_per_run: 0.006,
          requires_entitlement: true,
        });
      }
    }
    const profiledRoutes: CavePlan["segment_routes"] = [];
    for (const segment of contextIR.segments) {
      const transformID = preferredTransforms.get(segment.id);
      if (!transformID || segment.safety !== "S4" || segment.opaque || opaqueSegmentID(segment.id)) continue;
      const capability = transformCapabilities.find((item) => item.transformID === transformID);
      if (!capability?.segmentKinds.includes(segment.kind)) continue;
      profiledRoutes.push(candidateRoute(segment.kind, transformID, segment.id));
    }
    if (profiledRoutes.length > 0) {
      candidates.push({
        plan: {
          ...baseline,
          plan_id: `${baseline.plan_id}.profiled-best-of`,
          segment_routes: profiledRoutes,
          recovery: { namespace: agent.id, tools: recoveryTools(profiledRoutes) },
        },
        estimated_cost_usd_per_run: 0.006,
        requires_entitlement: true,
      });
    }
  }

  const contextPlans = [...candidates];
  for (const contextPlan of contextPlans) {
    for (const model of models) {
      if (model === contextPlan.plan.model) continue;
      candidates.push({
        ...contextPlan,
        plan: {
          ...contextPlan.plan,
          plan_id: `${contextPlan.plan.plan_id}.model.${stableID(model)}`,
          model,
        },
      });
    }
  }
  const modelPlans = [...candidates];
  const reasoningOrder: CavePlan["reasoning"][] = ["none", "minimal", "low", "medium", "high"];
  for (const modelPlan of modelPlans) {
    const baselineIndex = reasoningOrder.indexOf(modelPlan.plan.reasoning);
    for (const reasoning of reasoningOrder.slice(0, baselineIndex)) {
      candidates.push({
        ...modelPlan,
        plan: {
          ...modelPlan.plan,
          plan_id: `${modelPlan.plan.plan_id}.reasoning.${reasoning}`,
          reasoning,
        },
      });
    }
  }
  const deduplicated = deduplicatePlans(candidates);
  // The catalog pricing + model/safety-class policy filter live HERE, not just
  // in the CLI, so the public compile() API enforces exactly what the CLI does
  // An unpriced model becomes a static_rejection, a denied/
  // disallowed model or a forbidden safety class is rejected, and every runnable
  // candidate carries its real public-catalog search ceiling. When no policy is
  // supplied the candidates keep their generation-time placeholder costs.
  return policy === undefined ? deduplicated : applyCandidatePolicy(deduplicated, policy);
}

/** Model/safety-class policy applied to generated candidates. */
export interface CandidatePolicy {
  readonly allowedModels?: readonly string[];
  readonly deniedModels?: readonly string[];
  readonly forbiddenSafetyClasses?: readonly string[];
}

function applyCandidatePolicy(
  candidates: readonly CandidatePlan[],
  policy: CandidatePolicy,
): CandidatePlan[] {
  return candidates.map((candidate) => {
    if (candidate.static_rejection !== undefined) return candidate;
    if (policy.deniedModels?.includes(candidate.plan.model) ||
        (policy.allowedModels !== undefined && !policy.allowedModels.includes(candidate.plan.model))) {
      return { ...candidate, static_rejection: "policy_denied" as const };
    }
    if (policy.forbiddenSafetyClasses?.includes("S4") &&
        candidate.plan.segment_routes.some((route) => route.transform_id.startsWith("caveman.engine."))) {
      return { ...candidate, static_rejection: "forbidden_transform" as const };
    }
    const budgets = candidate.plan.budgets;
    const ceiling = catalogSearchCeiling(
      candidate.plan.model,
      budgets.instructions + budgets.tools + budgets.memory + budgets.history +
        budgets.results_artifacts + budgets.retry_cascade_reserve,
      budgets.output + budgets.reasoning,
    );
    if (ceiling === undefined) {
      return { ...candidate, static_rejection: "unpriced_model" as const };
    }
    return { ...candidate, estimated_cost_usd_per_run: ceiling };
  });
}

export interface TransformCapability {
  transformID: string;
  segmentKinds: readonly ContextKind[];
}

const ENGINE_TRANSFORM_CAPABILITIES: readonly TransformCapability[] = [
  { transformID: "caveman.engine.a11y.v1", segmentKinds: ["artifact", "tool_result"] },
  ...["code", "config", "diff", "html", "json", "log", "search-result", "tabular", "terminal", "text", "toon"]
    .map((name) => ({
      transformID: `caveman.engine.${name}.v1`,
      segmentKinds: ["artifact", "history", "skill", "tool_result"] as const,
    })),
  { transformID: "caveman.engine.repetition.v1", segmentKinds: ["history", "tool_result"] },
  { transformID: "caveman.engine.toolschema.v1", segmentKinds: ["tool_schema"] },
];

function opaqueSegmentID(id: string): boolean {
  return /(?:opaque|signed|signature|jwt|token|cipher|encrypted)/i.test(id);
}

function summarizeCandidate(
  candidate: CandidatePlan,
  evidence: Completed["evidence"],
  baseline: Completed["evidence"],
  config: BuildConfig,
  seedDigest: string,
  evals: EvalDefinition[],
): Completed {
  const requiredPassed = evidence.filter(({ value }) =>
    value.graders.length > 0 && value.graders.every((grader) => grader.passed)).length;
  const fixturePassRate = evidence.length === 0 ? 0 : requiredPassed / evidence.length;
  const errorRate = evidence.length === 0 ? 1 : evidence.filter(({ value }) => value.error).length / evidence.length;
  const p95Latency = percentile(evidence.map(({ value }) => value.latency_ms), 0.95);
  const costPerTask = roundUsd(evidence.reduce((sum, run) => sum + run.value.catalog_cost_usd, 0) / Math.max(1, evidence.length));
  const baselineByKey = new Map(baseline.map((run) => [`${run.fixture}:${run.seed}`, run.value.quality_score]));
  const paired = evidence.map((run) => {
    const base = baselineByKey.get(`${run.fixture}:${run.seed}`);
    if (base === undefined || base <= 0) return undefined;
    return Math.min(1, Math.max(0, run.value.quality_score / base));
  }).filter((value): value is number => value !== undefined);
  const retentionLCB95 = bootstrapLCB95(paired, seedDigest);
  const fixtureGuardrailsPass = evals.every((fixture) => {
    const runs = evidence.filter((run) => run.fixture === fixture.id).map((run) => run.value);
    return runs.length > 0 && fixture.guardrails.every((guardrail) => {
      if (guardrail.type === "latency_threshold") {
        return percentile(runs.map((run) => run.latency_ms), 0.95) <= guardrail.p95_ms;
      }
      if (guardrail.type === "error_rate") {
        return runs.filter((run) => run.error).length / runs.length <= guardrail.max;
      }
      return false;
    });
  });
  const passing = fixturePassRate >= config.requiredFixturePassRate &&
    errorRate === 0 &&
    fixtureGuardrailsPass &&
    cacheGatePass(evidence, baseline, candidate.plan) &&
    retentionLCB95 >= config.qualityRetention &&
    (config.maxP95LatencyMs === undefined || p95Latency <= config.maxP95LatencyMs);
  return { candidate, evidence, costPerTask, errorRate, p95Latency, fixturePassRate, retentionLCB95, passing };
}

function cacheGatePass(
  evidence: Completed["evidence"],
  baseline: Completed["evidence"],
  plan: CavePlan,
): boolean {
  const byFixture = new Map<string, Completed["evidence"]>();
  const baselineByFixture = new Map<string, Completed["evidence"]>();
  for (const run of evidence) {
    const rows = byFixture.get(run.fixture) ?? [];
    rows.push(run);
    byFixture.set(run.fixture, rows);
  }
  for (const run of baseline) {
    const rows = baselineByFixture.get(run.fixture) ?? [];
    rows.push(run);
    baselineByFixture.set(run.fixture, rows);
  }
  for (const [fixture, rows] of byFixture) {
    const baseRows = baselineByFixture.get(fixture);
    if (!baseRows || rows.length < 5 || baseRows.length < 5) return false;
    rows.sort((a, b) => a.seed - b.seed);
    baseRows.sort((a, b) => a.seed - b.seed);
    const prefixes = new Set(rows.map((run) => run.value.cache_prefix_sha256));
    if (prefixes.size !== 1 ||
        rows.some((run) => !run.value.cache_boundary_known || run.value.cache_bust)) return false;
    const cacheSensitive = plan.segment_routes.length > 0;
    if (cacheSensitive &&
        (rows[0]!.value.cache_read_tokens !== 0 ||
          rows.slice(1).some((run) => run.value.cache_read_tokens <= 0))) return false;
    const warmCost = rows.slice(1).reduce((sum, run) => sum + run.value.catalog_cost_usd, 0);
    const baselineWarmCost = baseRows.slice(1).reduce((sum, run) => sum + run.value.catalog_cost_usd, 0);
    if (warmCost > baselineWarmCost + 1e-12) return false;
    const baselinePrefixes = new Set(baseRows.map((run) => run.value.cache_prefix_sha256));
    const startsNewEpoch = plan.segment_routes.length > 0 &&
      (baselinePrefixes.size !== 1 || !baselinePrefixes.has(rows[0]!.value.cache_prefix_sha256));
    if (startsNewEpoch) {
      const totalCost = rows.reduce((sum, run) => sum + run.value.catalog_cost_usd, 0);
      const baselineTotalCost = baseRows.reduce((sum, run) => sum + run.value.catalog_cost_usd, 0);
      if (totalCost >= baselineTotalCost - 1e-12) return false;
    }
  }
  return true;
}

function completeEvidence(value: RunEvidence): boolean {
  return value.terminal &&
    value.usage_basis === "provider_reported" &&
    value.price_basis === "public_catalog" &&
    Number.isFinite(value.catalog_cost_usd) &&
    value.catalog_cost_usd >= 0 &&
    Number.isFinite(value.quality_score) &&
    value.quality_score >= 0 &&
    value.quality_score <= 1 &&
    Number.isSafeInteger(value.provider_visible_tokens) &&
    value.provider_visible_tokens >= 0 &&
    /^[0-9a-f]{64}$/.test(value.cache_prefix_sha256) &&
    value.cache_boundary_known &&
    Number.isSafeInteger(value.cache_read_tokens) &&
    value.cache_read_tokens >= 0 &&
    Number.isSafeInteger(value.cache_write_tokens) &&
    value.cache_write_tokens >= 0 &&
    value.cache_bust === false &&
    value.graders.length > 0 &&
    value.graders.every((grader) => knownGrader(grader.type)) &&
    !value.unknown_event &&
    !value.unknown_transform &&
    value.recovery_resolved &&
    value.privacy_passed &&
    value.sandbox_passed &&
    /^[0-9a-f]{64}$/.test(value.output_digest);
}

// Full supported grader taxonomy. MUST match @caveman/graders
// `SUPPORTED_GRADER_TYPES`; compiler tests enforce parity.
const KNOWN_GRADER_TYPES: ReadonlySet<string> = new Set([
  "exact_match",
  "contains",
  "not_contains",
  "regex",
  "not_regex",
  "blocklist",
  "json_schema",
  "json_path_assertion",
  "tool_called",
  "tool_not_called",
  "tool_sequence",
  "tool_argument_assertion",
  "http_status",
  "latency_threshold",
  "cost_threshold",
  "token_threshold",
  "bleu_score",
  "rouge_score",
  "context_f1",
  "no_pii",
  "custom_webhook",
  "localization_f1",
  "llm_judge",
  "llm_score",
  "llm_category",
  "llm_pairwise",
  "llm_answer_match",
]);

export function knownGrader(type: string): boolean {
  return KNOWN_GRADER_TYPES.has(type);
}

/** The first grader type an evidence row carries that is outside the taxonomy. */
function firstUnknownGrader(value: RunEvidence): string | undefined {
  return value.graders.find((grader) => !knownGrader(grader.type))?.type;
}

function selectionOrder(a: Completed, b: Completed): number {
  return a.costPerTask - b.costPerTask ||
    a.p95Latency - b.p95Latency ||
    providerVisibleTokens(a) - providerVisibleTokens(b) ||
    a.candidate.plan.segment_routes.length - b.candidate.plan.segment_routes.length ||
    a.candidate.plan.plan_id.localeCompare(b.candidate.plan.plan_id);
}

function providerVisibleTokens(candidate: Completed): number {
  return candidate.evidence.reduce((sum, run) => sum + run.value.provider_visible_tokens, 0);
}

function percentile(values: number[], fraction: number): number {
  if (values.length === 0) return Number.POSITIVE_INFINITY;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.min(sorted.length - 1, Math.ceil(sorted.length * fraction) - 1)]!;
}

function bootstrapLCB95(values: number[], digest: string): number {
  if (values.length < 2) return 0;
  let state = Number.parseInt(digest.slice(0, 8), 16) || 1;
  const means = new Array<number>(10_000);
  for (let sample = 0; sample < means.length; sample++) {
    let total = 0;
    for (let i = 0; i < values.length; i++) {
      state ^= state << 13;
      state ^= state >>> 17;
      state ^= state << 5;
      const index = (state >>> 0) % values.length;
      total += values[index]!;
    }
    means[sample] = total / values.length;
  }
  means.sort((a, b) => a - b);
  return round(means[Math.floor(means.length * 0.05)]!, 6);
}

function recoveryTools(routes: CavePlan["segment_routes"]): CavePlan["recovery"]["tools"] {
  const tools = new Set<CavePlan["recovery"]["tools"][number]>();
  for (const route of routes) {
    tools.add("cave_retrieve");
    if (route.segment_kind === "tool_schema") tools.add("cave_search_tools");
    if (route.segment_kind === "memory") tools.add("cave_memory_search");
  }
  return [...tools].sort();
}

function deduplicatePlans(candidates: CandidatePlan[]): CandidatePlan[] {
  const seen = new Set<string>();
  return candidates.filter((candidate) => {
    const digest = sha256(stableStringify(candidate.plan));
    if (seen.has(digest)) return false;
    seen.add(digest);
    return true;
  });
}

function stableID(value: string): string {
  return value.replace(/[^a-zA-Z0-9_.-]/g, "-");
}

function emptyResult(status: CompileStatus, reason: string): CompileResult {
  return {
    status,
    estimated_ceiling_usd: 0,
    planned_runs: 0,
    completed_runs: 0,
    static_rejections: 0,
    actual_cost_usd: 0,
    reason,
  };
}

function roundUsd(value: number): number {
  return round(value, 10);
}

function round(value: number, digits: number): number {
  const scale = 10 ** digits;
  return Math.round((value + Number.EPSILON) * scale) / scale;
}
