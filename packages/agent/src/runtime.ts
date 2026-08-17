import { spawn, type ChildProcess } from "node:child_process";
import { randomBytes } from "node:crypto";
import { readFileSync } from "node:fs";
import {
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  realpath,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import { homedir, tmpdir } from "node:os";
import { dirname, isAbsolute, relative, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { Agent, type AgentEvent, type AgentMessage, type AgentTool, type StreamFn } from "@earendil-works/pi-agent-core";
import {
  Type,
  type Api,
  type AssistantMessage,
  type Context,
  type Model,
  type Models,
  type ProviderHeaders,
  type TSchema,
} from "@earendil-works/pi-ai";
import { builtinModels } from "@earendil-works/pi-ai/providers/all";
import { Value } from "typebox/value";
import {
  agentDefinitionSHA256,
  toolDefinitionSHA256,
  type CaveBuildLock,
  type CavePlan,
} from "./build.js";
import {
  BudgetMeter,
  ReceiptRecorder,
  bindBudgetController,
  budgetExhaustionContext,
  callCeilingCost,
  callFitsBudget,
  inputTokenCeiling,
  modelIsPriced,
  normalizeRunBudget,
  planCall,
  serializedContextBytes,
  unbindBudgetController,
  type BudgetController,
  type BudgetExhaustionHandler,
  type BudgetReservation,
  type CallCeiling,
  type RunBudget,
  type RunReceipt,
  type RunStopReason,
} from "./budget.js";
import {
  BreakerState,
  normalizeRunBreakers,
  type RunBreakers,
} from "./breakers.js";
import { catalogCost, catalogSearchCeiling } from "./catalog.js";
import {
  SUMMARY_SCHEMA_VERSION,
  elidedDigest,
  evictMessage,
  messagesTokens,
  parseContextSummary,
  planCompaction,
  renderSummary,
  summarizationInstruction,
  type ContextSummary,
} from "./compaction.js";
import { validateAgentGraph } from "./definition-graph.js";
import {
  lowerAgentContext,
  prepareLockedHarnessExecution,
  validatePlanSelection,
  validateProviderUsage,
  type ValidatedProviderUsage,
} from "./execution-kernel.js";
import { PI_ADAPTER_VERSION, PI_UPSTREAM_VERSION } from "./runtime-identity.js";
import type { AgentDefinition } from "./index.js";
import {
  appendRuntimeContextSegment,
  contextBill,
  sha256,
  stableStringify,
  type ContextIR,
  type LoweredContext,
} from "./context-ir.js";
import { memoryTTLMilliseconds, type ToolDefinition } from "./primitives.js";
import {
  memoryFilePath,
  mutateMemories,
  type MemoryEntry,
  type MemoryStoreConfig,
} from "./memory-store.js";
import { networkIsolatedNode } from "./sandbox-network.js";
import { killProcessTree, portableInvocation } from "./portable-process.js";
import { expandSourceGraph } from "./source-graph.js";

// Compaction needs room for its own same-model call plus useful work after the
// rewrite. Waiting until the next call no longer fits makes that inequality
// impossible, so default-on compaction starts while four cold next-call
// ceilings remain. Every call still reserves and settles against the hard cap.
const COMPACTION_TRIGGER_MULTIPLIER = 4;

export type CavemanRunEvent =
  | { type: "run_start"; runId: string; agentId: string }
  | { type: "context_ready"; runId: string; contextIR: ContextIR; bill: Record<string, number> }
  | { type: "pi"; runId: string; event: AgentEvent }
  | { type: "run_end"; runId: string; result: RunResult }
  // A run that fails still carries its ledger: `receipt` is the
  // partial receipt built from the same ReceiptRecorder the run_end path uses,
  // so the calls, tools, and metered spend that DID happen before the failure
  // are never lost. It is always present.
  | { type: "run_error"; runId: string; code: string; message: string; receipt: RunReceipt };

/**
 * The error thrown by the promise-returning run entry points (`runAgent`,
 * `runAgentInternal`) when a run fails. Unlike a bare `Error`, it carries the
 * failing run's partial `receipt` so a caller that only awaits the promise —
 * never consuming the event stream — can still read the ledger of what was
 * spent before the failure. The `cave_*` code is on `code`.
 */
export class CavemanRunError extends Error {
  readonly code: string;
  readonly receipt: RunReceipt;
  constructor(code: string, message: string, receipt: RunReceipt) {
    super(`${code}: ${message}`);
    this.name = "CavemanRunError";
    this.code = code;
    this.receipt = receipt;
    this.cause = receipt;
  }
}

export interface RunResult {
  runId: string;
  agentId: string;
  text: string;
  contextIR: ContextIR;
  contextBill: Record<string, number>;
  cachePrefixSHA256: string;
  cacheBoundaryKnown: boolean;
  cacheBust: boolean;
  usageBasis: "provider_reported" | "unavailable";
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  /** Only interpret reasoningTokens when this basis is provider_reported. */
  reasoningUsageBasis: "provider_reported" | "unavailable";
  reasoningTokens: number;
  costUsd: number;
  /**
   * How costUsd was arrived at. `unpriced` means at least one accumulated model
   * call had no public-catalog price, so costUsd is an honest zero-contribution
   * for that call rather than a measured amount — never read it as "$0 spent".
   */
  priceBasis: "public_catalog" | "unpriced";
  /**
   * `optimized` means every model call this run made — its own and every
   * subagent's — actually went through the Caveman gateway, so transforms and
   * gateway telemetry were available to all of it. `observe-only` means at least
   * one call went straight to the provider: either the gateway was unreachable
   * or `cave: "off"`, or the model's provider is not one the gateway proxies
   * (only anthropic, openai, and google are). A mixed graph reports
   * `observe-only`; the label under-claims rather than averaging, because the
   * gateway measured nothing about the calls that bypassed it. Local results are
   * estimates in both modes; no provider savings claim is made.
   */
  mode: "optimized" | "observe-only";
  provider: string;
  model: string;
  latencyMs: number;
  toolCalls: string[];
  evaluatedTransformIDs: string[];
  transformIDs: string[];
  transformFailures: string[];
  transformTrace: TransformTrace[];
  recoveryResolved: boolean;
  /**
   * Why the run stopped issuing calls. `complete` is the ordinary end of the
   * agent loop. Every other value means the runtime stopped between calls with
   * partial work in hand: the text, usage, and cost above cover exactly the
   * calls that did happen, and no call was interrupted mid-flight.
   */
  stopReason: RunStopReason;
  /**
   * True when measured spend crossed the budget's `max`, here or in any
   * subagent wallet beneath this run.
   *
   * It sits beside `stopReason` because the two answer different questions: a
   * run can stop cleanly at its cap, or stop having already gone past it, and
   * a caller switching on `stopReason` alone cannot tell those apart. The
   * runtime never chooses to spend past max — this means a provider reported
   * more than the hold could bound.
   */
  capBreached: boolean;
  /**
   * Signed amount **this run's own meter** went past its `max`, zero when its
   * own cap held. `capBreached` rolls up from subagents; this figure does not,
   * because settling a carve books the child's real spend here too and adding
   * both would count the same money twice. Each subagent's own amount is on
   * its receipt under `receipt.subagents`.
   */
  overspent: number;
  /**
   * This run's economic receipt: every provider call, every tool, every
   * subagent's own receipt, and the budget's tranche history. Its money figures
   * are estimated public-catalog list-price subtotals, never invoices.
   */
  receipt: RunReceipt;
  claimBasis: "inferred";
  unlocked: boolean;
}

export interface TransformTrace {
  segmentKind: string;
  transformID: string;
  safetyClass: string;
  /**
   * Tokens before and after this transform. `afterTokens` for an applied
   * transform is measured from the BYTES ACTUALLY SENT — the full provider body
   * including the framework's own `<cave-compressed …>` wrapper and instruction
   * line — never the engine's count of the compressed payload alone.
   * Both figures share `tokensBasis`; a delta is only meaningful within one
   * basis, so consumers must not sum across bases.
   */
  beforeTokens: number;
  afterTokens: number;
  /**
   * How `beforeTokens`/`afterTokens` were counted. `byte_derived` is bytes/4 of
   * the real payloads; `engine_counted` is the engine's own tokenizer. A
   * reduction summed across mixed bases is invalid — callers refuse to blend them.
   */
  tokensBasis: "byte_derived" | "engine_counted";
  recoveryKind: string;
  recoveryUsed: boolean;
  latencyMs: number;
  outcome: "applied" | "not_smaller" | "failed_open";
}

type MutableTransformTrace = TransformTrace & { recoveryHandle?: string };
type ProviderFrozenView = {
  api: string;
  bytes: Uint8Array;
  fields: Record<string, unknown>;
  messageField?: "messages" | "input" | "contents" | "context.messages";
  messagePrefix?: unknown[];
};

interface InternalConversationState {
  messages: AgentMessage[];
  sessionId: string;
  busy: boolean;
  fingerprint?: string;
  cachePrefixDigest?: string;
  providerFrozen?: ProviderFrozenView;
  originalFrozen?: ProviderFrozenView;
}

const conversationStates = new WeakMap<Conversation, InternalConversationState>();

export class Conversation {
  readonly sessionId: string;

  constructor() {
    this.sessionId = `conversation-${crypto.randomUUID()}`;
    conversationStates.set(this, {
      messages: [],
      sessionId: this.sessionId,
      busy: false,
    });
  }

  snapshot(): { readonly messages: readonly unknown[] } {
    const state = conversationStates.get(this);
    if (!state) throw new Error("cave_conversation_invalid");
    return Object.freeze({ messages: structuredClone(state.messages) });
  }
}

export type ConversationState = Conversation;

export function createConversation(): Conversation {
  return new Conversation();
}

type ConversationTransaction = {
  readonly owner: Conversation;
  readonly sessionId: string;
  messages: AgentMessage[];
  fingerprint: string | undefined;
  cachePrefixDigest: string | undefined;
  providerFrozen: ProviderFrozenView | undefined;
  originalFrozen: ProviderFrozenView | undefined;
};

function beginConversation(owner: Conversation): ConversationTransaction {
  const state = conversationStates.get(owner);
  if (!state) throw new Error("cave_conversation_invalid");
  if (state.busy) throw new Error("cave_conversation_in_use");
  const transaction: ConversationTransaction = {
    owner,
    sessionId: state.sessionId,
    messages: structuredClone(state.messages),
    fingerprint: state.fingerprint,
    cachePrefixDigest: state.cachePrefixDigest,
    providerFrozen: state.providerFrozen === undefined
      ? undefined
      : structuredClone(state.providerFrozen),
    originalFrozen: state.originalFrozen === undefined
      ? undefined
      : structuredClone(state.originalFrozen),
  };
  state.busy = true;
  return transaction;
}

function commitConversation(
  transaction: ConversationTransaction,
  next: {
    messages: AgentMessage[];
    fingerprint: string | undefined;
    cachePrefixDigest: string | undefined;
    providerFrozen: ProviderFrozenView | undefined;
    originalFrozen: ProviderFrozenView | undefined;
  },
): void {
  const state = conversationStates.get(transaction.owner);
  if (!state || !state.busy) throw new Error("cave_conversation_invalid");
  const prepared = {
    messages: structuredClone(next.messages),
    providerFrozen: next.providerFrozen === undefined
      ? undefined
      : structuredClone(next.providerFrozen),
    originalFrozen: next.originalFrozen === undefined
      ? undefined
      : structuredClone(next.originalFrozen),
  };
  state.messages = prepared.messages;
  setOptional(state, "fingerprint", next.fingerprint);
  setOptional(state, "cachePrefixDigest", next.cachePrefixDigest);
  setOptional(state, "providerFrozen", prepared.providerFrozen);
  setOptional(state, "originalFrozen", prepared.originalFrozen);
}

function endConversation(transaction: ConversationTransaction): void {
  const state = conversationStates.get(transaction.owner);
  if (state) state.busy = false;
}

function setOptional<
  T extends object,
  K extends keyof T,
>(target: T, key: K, value: T[K] | undefined): void {
  if (value === undefined) delete target[key];
  else target[key] = value;
}

export interface RunOptions {
  rootDir?: string;
  workflow?: string;
  sessionId?: string;
  gatewayURL?: string;
  ensureRuntime?: boolean;
  /**
   * How this run treats the Caveman gateway. `auto` (default) routes through it
   * and, when the local gateway cannot be reached, degrades to observe-only:
   * the provider's own base URL, no transforms, no gateway telemetry, and
   * `RunResult.mode: "observe-only"`. `off` never contacts or starts the
   * gateway. A run carrying a Cave Build lock or candidate plan never degrades
   * silently — it fails with `cave_gateway_required_for_locked_plan` instead.
   */
  cave?: "auto" | "off";
  /**
   * Best-effort local spend cap for this run, in USD at public catalog list
   * prices. It is not financial enforcement: no provider invoice, no
   * platform quota, and no reservation outside this process is involved. Unset
   * leaves the run bounded only by its model/tool call ceilings.
   * When set, every root and descendant model call reserves the catalog
   * worst-case price of the call against the cap before the provider request
   * and settles measured catalog cost after it. Exhaustion terminates the run
   * with `cave_run_cost_budget_exceeded` before the next model call; partial
   * records stay in the emitted events and no `run_end` result is produced.
   * A model the public catalog cannot price cannot be capped: with the cap set,
   * such a call fails closed with `cave_subagent_unpriced_budget` rather than
   * consuming $0 of budget. Runs on unpriced models must stay uncapped and rely
   * on call ceilings instead.
   */
  maxCostUsd?: number;
  /**
   * The run's budget contract. Declares exactly one denomination:
   * `maxUsd` at public catalog list prices, or `maxTokens` in provider-counted
   * tokens. A denomination this runtime cannot honestly meter fails closed
   * before the first provider call — `maxUsd` needs a catalog-priced model,
   * because a cap that cannot be measured is not a cap.
   *
   * Enforcement is reserve-and-clamp with one mode and no soft option. Before
   * every provider call the runtime holds that call's worst-case price against
   * the budget; when the remainder cannot cover the configured output
   * allowance it clamps the call's output down to what the remainder affords,
   * and below the output floor it stops. Stopping is a normal result carrying
   * `stopReason`, never a throw: an in-flight call always finishes and is
   * counted, and the runtime never stops mid-tool.
   *
   * The guarantee, exactly: **the runtime never CHOOSES to spend past `max`.**
   * Every call is held at its worst case first, and that hold bounds the
   * payload that actually leaves. When a provider nonetheless reports more
   * than could be bounded, the run settles at the TRUE amount — a ledger that
   * clamps what a call cost is a fake ledger — sets `capBreached` with a
   * signed `overspent` on the result and its receipt, and stops. A breach is
   * loud and terminal; `spent > max` never appears without that flag, and the
   * flag rolls up from any subagent wallet that breached beneath the run.
   *
   * Unlike `maxCostUsd`, which terminates the run with an error, a budget is a
   * planned end state. The two are mutually exclusive.
   */
  budget?: RunBudget;
  /**
   * Escape hatch for the credential-regime gate. A USD budget on
   * Pi's own transport, off the gateway, is refused when the local credential
   * store cannot prove the credential is a metered API key — an unprovable
   * regime fails closed rather than booking fictional dollars against what may
   * be a subscription. Set this to `true` to assert, on your own authority,
   * that the credential IS metered per token, so a dollar budget is honest.
   * Ignored for a caller-supplied `streamFn` and for an actually routed gateway
   * whose identified readiness response proves `billing: "managed"`. BYOK or
   * unknown gateway billing still uses this local credential assertion.
   */
  assumeMeteredCredential?: boolean;
  /**
   * Where durable agent memory lives, and for whom. `memory()`
   * entries persist to a per-namespace file scoped by (tenant, agentId,
   * namespace), so a `ttl: "30d"` is a real 30-day promise across process
   * restarts. Set `root` to point an embedding server at its own location, and
   * `tenant` to isolate one tenant's memories from another's even when they
   * share an agent id and namespace. Defaults: `CAVE_AGENT_MEMORY_ROOT` or
   * `~/.caveman/agent-memory`, single-tenant.
   */
  memory?: MemoryStoreConfig;
  /**
   * Handle for releasing staged budget during this run. Pair it
   * with `budget.initialUsd` / `budget.initialTokens`: the run starts metered
   * against the initial tranche, and the developer's own checkpoint logic calls
   * `releaseBudget(amount, reason)` to hand it more, up to `max`. Nothing the
   * model produces can reach it.
   */
  budgetController?: BudgetController;
  /**
   * What happens when the budget binds. `"stop"` is the default:
   * stop between calls, return partial work plus the receipt. A handler instead
   * lets the embedding application decide — ask a human, check a policy, top up
   * a tranche through the budget controller, or stop. It is called between calls with
   * nothing in flight, and never mid-tool-side-effect.
   *
   * Escalating never crosses `max`: a handler that asks for more than the
   * contract allows throws at the release, exactly as a checkpoint would.
   */
  onBudgetExhausted?: "stop" | BudgetExhaustionHandler;
  /**
   * Wall-clock allowance for this run in milliseconds, enforced at the same
   * between-calls points as the budget: the runtime finishes what is in flight,
   * declines to start another call, and returns `stopReason: "deadline"`.
   */
  deadlineMs?: number;
  /**
   * How deep a subagent graph this run may spawn, counted from the root. The
   * default is deliberately small: delegation multiplies spend, so a graph that
   * wants to go deeper says so. Capped at {@link ABSOLUTE_SUBAGENT_DEPTH_LIMIT}.
   */
  maxSubagentDepth?: number;
  /** Root-wide cap across every admitted descendant invocation. Opt-in. */
  maxSubagentInvocations?: number;
  /** Root-wide cap on simultaneously active descendant invocations. Opt-in. */
  maxConcurrentSubagents?: number;
  /**
   * Hard ceiling on the number of provider model calls this run may issue
   * Defaults to 64. When the run reaches it, the runtime stops
   * gracefully between calls with `stopReason: "call_budget_exhausted"` — the
   * partial text, usage, and receipt are returned, never a throw that would
   * destroy the ledger of what was already spent. An efficiency-plan run
   * derives a tighter default from its retry-cascade reserve; a caller value
   * here always overrides. Must be a positive integer.
   */
  maxModelCalls?: number;
  /**
   * Hard ceiling on the number of tool calls this run may issue.
   * Defaults to 64. A tool call beyond it is blocked (the model sees a blocked
   * result and the run continues), never a throw. An efficiency-plan run
   * derives its default from the model-call ceiling and tool count; a caller
   * value here always overrides. Must be a positive integer.
   */
  maxToolCalls?: number;
  /**
   * Deterministic circuit breakers: repeated-tool-call loop
   * detection, a no-progress window, a per-turn fan-out cap, and cost-aware
   * retry. Opt-in — a run that declares none behaves exactly as before, because
   * a false loop break is worse than the loop it thought it saw. Breaking is a
   * between-calls stop like any other, with `stopReason: "loop_detected"` or
   * `"no_progress"` and the offending window on the receipt.
   */
  breakers?: RunBreakers;
  model?: Model<Api>;
  models?: Models;
  streamFn?: StreamFn;
  providerPayloadContract?: "pi-on-payload-v1";
  conversation?: Conversation;
  fetch?: typeof globalThis.fetch;
  entryPath?: string;
  engineBin?: string;
  signal?: AbortSignal;
  sandboxProfile?: {
    /**
     * `false` (the only accepted value) runs the tool under the OS network
     * namespace. `true` requests unbounded egress and FAILS CLOSED with
     * `cave_sandbox_network_egress_unbounded`: there is no
     * scoped-egress mechanism yet — unrestricted egress from tool
     * code that holds credentials is refused rather than granted.
     */
    network: boolean;
    childProcess: boolean;
    credentialEnv: readonly string[];
  };
}

// Live-eval profiles are repository-controlled input. They must not turn the
// parent CI environment into a secret broker by naming arbitrary variables.
// These are the only provider credentials a live tool may receive. Keep the
// mapping in the runtime (not just the CLI loader) because callers can invoke
// runAgentInternal directly, and keep one provider family per profile so a
// profile cannot harvest every provider credential in the parent job.
export const SANDBOX_CREDENTIAL_ENV_BY_CAPABILITY = Object.freeze({
  anthropic: Object.freeze(["ANTHROPIC_API_KEY"]),
  openai: Object.freeze(["OPENAI_API_KEY"]),
  google: Object.freeze(["GEMINI_API_KEY", "GOOGLE_API_KEY"]),
});

type SandboxCredentialCapability = keyof typeof SANDBOX_CREDENTIAL_ENV_BY_CAPABILITY;

const SANDBOX_CREDENTIAL_ENV_TO_CAPABILITY = new Map<string, SandboxCredentialCapability>(
  Object.entries(SANDBOX_CREDENTIAL_ENV_BY_CAPABILITY).flatMap(([capability, names]) =>
    names.map((name) => [name, capability as SandboxCredentialCapability]),
  ),
);

// Explicitly document the high-impact families that are never eligible. Names
// outside the provider map are denied too; the prefixes make that deny rule
// durable if the provider map grows later.
const SANDBOX_CREDENTIAL_DENY_PREFIXES = [
  "AWS_",
  "AZURE_",
  "BOOTSTRAP_",
  "CAVE_",
  "CAVEBENCH_",
  "CLOUD_",
  "COSIGN_",
  "DATABASE_",
  "DB_",
  "DEPLOY_",
  "GCP_",
  "GOOGLE_APPLICATION_",
  "GOOGLE_CLOUD_",
  "GITHUB_",
  "KMS_",
  "MINIO_",
  "PG",
  "POSTGRES_",
  "RECEIPT_",
  "SCW_",
  "SIGN_",
  "STRIPE_",
  "VALKEY_",
].concat(["REDIS_"]);
const SANDBOX_CREDENTIAL_DENY_NAMES = new Set([
  "HOME",
  "LD_PRELOAD",
  "NODE_EXTRA_CA_CERTS",
  "NODE_OPTIONS",
  "PATH",
  "PWD",
  "SHELL",
  "DYLD_INSERT_LIBRARIES",
]);

export function validateSandboxCredentialEnv(names: readonly string[]): void {
  const capabilities = new Set<SandboxCredentialCapability>();
  for (const name of names) {
    const denied = typeof name === "string" &&
      (SANDBOX_CREDENTIAL_DENY_NAMES.has(name) ||
        SANDBOX_CREDENTIAL_DENY_PREFIXES.some((prefix) => name.startsWith(prefix)));
    const capability = denied || typeof name !== "string"
      ? undefined
      : SANDBOX_CREDENTIAL_ENV_TO_CAPABILITY.get(name);
    if (capability === undefined) {
      // Keep profile-controlled names out of errors. This is a policy result,
      // not a diagnostic surface, and the name may itself identify a secret.
      throw new Error("cave_sandbox_credential_env_not_allowlisted");
    }
    capabilities.add(capability);
  }
  if (capabilities.size > 1) {
    throw new Error("cave_sandbox_credential_capability_ambiguous");
  }
}

/** Build the complete environment for an isolated tool child. No spread of
 * `process.env`: only deterministic runtime baseline plus an exact provider
 * capability selected by the validated live profile. */
export function buildSandboxToolEnv(names: readonly string[] = []): NodeJS.ProcessEnv {
  validateSandboxCredentialEnv(names);
  const env: NodeJS.ProcessEnv = {
    LANG: process.env.LANG ?? "C",
    LC_ALL: process.env.LC_ALL ?? "C",
    PATH: process.env.PATH ?? "",
    TZ: process.env.TZ ?? "UTC",
    CAVE_EVAL_FIXTURE: "1",
  };
  for (const name of names) {
    const value = process.env[name];
    if (value === undefined) throw new Error("cave_sandbox_credential_missing");
    env[name] = value;
  }
  return env;
}

interface InternalRunOptions extends RunOptions {
  lockedBuild?: CaveBuildLock;
  candidatePlan?: CavePlan;
  /**
   * Resolved once per root run and handed to descendants so a nested agent
   * neither re-probes the gateway nor disagrees with its parent about whether
   * this run is optimized or observe-only.
   */
  caveRoute?: ResolvedCaveRoute;
}

export type ResolvedCaveRoute = {
  readonly useGateway: boolean;
  /** Credential payer advertised by the identified gateway readiness response. */
  readonly providerBilling?: "managed" | "byok" | "unknown";
};

/** Nothing in this framework nests deeper than this, whatever a caller asks for. */
export const ABSOLUTE_SUBAGENT_DEPTH_LIMIT = 8;

/** Default recursion depth for subagent graphs: root spawns child spawns grandchild. */
const DEFAULT_SUBAGENT_DEPTH_LIMIT = 2;

/** Bounded above so one malformed option cannot create meaningless counters. */
const ABSOLUTE_SUBAGENT_INVOCATION_LIMIT = 1_000_000;
const MAX_INVOCATION_SPANS = 1_024;

type InvocationTrace = {
  readonly traceId: string;
  readonly spanId: string;
  readonly parentSpanId: string;
};

type InvocationLedger = {
  readonly maxInvocations?: number;
  readonly maxConcurrent?: number;
  readonly rootBudget: "absent" | "usd" | "tokens" | "legacy_usd";
  readonly turnFanout: boolean;
  admitted: number;
  active: number;
  peakActive: number;
  invocationRejections: number;
  concurrencyRejections: number;
};

type InvocationBatch = {
  readonly gatewayURL: string;
  readonly fetch: typeof globalThis.fetch;
  readonly apiKey?: string;
  readonly agentId: string;
  readonly workflow: string;
  readonly sessionId: string;
  readonly spans: Record<string, unknown>[];
  dropped: number;
  exported: boolean;
};

type InvocationState = {
  readonly ledger: InvocationLedger;
  batch?: InvocationBatch;
};

type SpendLedger = {
  readonly limitUsd: number;
  readonly maxContextTokens: number;
  readonly exceededCode: string;
  actualUsd: number;
  reservedUsd: number;
  incomplete: boolean;
};

type SpendReservation = {
  readonly ledger: SpendLedger;
  readonly ceilingUsd: number;
  readonly provider: string;
  readonly model: string;
};

type InternalExecutionContext = {
  readonly rootDefinitionSha256: string;
  readonly agentPath: readonly string[];
  readonly spendLedgers: readonly SpendLedger[];
  readonly sandboxRequired: boolean;
  readonly depth: number;
  readonly invocationState: InvocationState;
  readonly invocationTrace: InvocationTrace;
  /**
   * A subagent's carved wallet. Present only on descendants: a child never
   * builds its own meter from the parent's `budget` option, it is handed the
   * wallet the parent already reserved for it.
   */
  readonly budgetMeter?: BudgetMeter;
  /**
   * The root run's absolute deadline. Descendants inherit the instant, not the
   * duration, so a deep graph cannot keep resetting the clock.
   */
  readonly deadlineAt?: number;
};

type AppliedPlan = {
  bodies: Map<string, Uint8Array>;
  evaluatedTransformIDs: string[];
  appliedTransformIDs: string[];
  failures: string[];
  trace: MutableTransformTrace[];
  recoveryResolved: boolean;
  recoveryHandles: Set<string>;
};

type SandboxSourceSnapshot = {
  readonly stagingRoot: string;
  readonly entryPath: string;
  readonly sourceFiles: readonly string[];
  dispose(): Promise<void>;
};

type NestedUsage = {
  calls: Map<string, number>;
  budgets: Map<string, SpendLedger>;
  /** Each completed subagent run's own receipt, in spawn-completion order. */
  receipts: RunReceipt[];
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  reasoningTokens: number;
  costUsd: number;
  unpriced: boolean;
  incomplete: boolean;
  /**
   * True once any descendant run reported `observe-only`. A graph whose traffic
   * partly bypassed the gateway cannot be labelled plainly optimized, so the
   * root under-claims instead of averaging.
   */
  observeOnly: boolean;
};

function rootExecutionContext(
  definition: AgentDefinition,
  maxCostUsd?: number,
  options: Pick<RunOptions, "budget" | "breakers" | "maxSubagentInvocations" | "maxConcurrentSubagents"> = {},
): InternalExecutionContext {
  validateAgentGraph(definition);
  if (maxCostUsd !== undefined && (!Number.isFinite(maxCostUsd) || maxCostUsd <= 0)) {
    throw new Error("caveman agent: run maxCostUsd must be positive");
  }
  const rootBudget: InvocationLedger["rootBudget"] = maxCostUsd !== undefined
    ? "legacy_usd"
    : options.budget !== undefined && "maxUsd" in options.budget
      ? "usd"
      : options.budget !== undefined && "maxTokens" in options.budget ? "tokens" : "absent";
  const invocationTrace = Object.freeze({
    traceId: randomBytes(16).toString("hex"),
    spanId: randomBytes(8).toString("hex"),
    parentSpanId: "",
  });
  return Object.freeze({
    rootDefinitionSha256: agentDefinitionSHA256(definition),
    agentPath: Object.freeze([]),
    // Root cap is one more ancestor ledger: root turns reserve against it, and
    // every descendant stacks its own ledger on top of it.
    spendLedgers: Object.freeze(maxCostUsd === undefined ? [] : [{
      limitUsd: maxCostUsd,
      maxContextTokens: Number.MAX_SAFE_INTEGER,
      exceededCode: "cave_run_cost_budget_exceeded",
      actualUsd: 0,
      reservedUsd: 0,
      incomplete: false,
    }]),
    sandboxRequired: definition.sandbox === "required",
    depth: 0,
    invocationState: {
      ledger: {
        ...(options.maxSubagentInvocations === undefined ? {} : { maxInvocations: options.maxSubagentInvocations }),
        ...(options.maxConcurrentSubagents === undefined ? {} : { maxConcurrent: options.maxConcurrentSubagents }),
        rootBudget,
        turnFanout: options.breakers !== undefined,
        admitted: 0,
        active: 0,
        peakActive: 0,
        invocationRejections: 0,
        concurrencyRejections: 0,
      },
    },
    invocationTrace,
  });
}

export async function verifySandboxConformance(): Promise<boolean> {
  const worker = fileURLToPath(new URL("./sandbox-probe-worker.js", import.meta.url));
  const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));
  const deniedRoot = await mkdtemp(`${tmpdir()}/caveman-agent-probe-`);
  const deniedFile = resolve(deniedRoot, "known-secret");
  await writeFile(deniedFile, "must-not-read");
  const nodeArgs = [
      "--permission",
      `--allow-fs-read=${packageRoot}`,
      worker,
  ];
  const isolated = networkIsolatedNode(nodeArgs);
  try {
    const result = await new Promise<Record<string, unknown>>((accept, reject) => {
      const child = spawn(isolated.command, isolated.args, {
      cwd: tmpdir(),
      env: {
        PATH: process.env.PATH ?? "",
        TZ: "UTC",
        CAVE_SANDBOX_HOME_PROBE: deniedFile,
      },
      stdio: ["ignore", "pipe", "pipe"],
    });
    const stdout: Buffer[] = [];
    const stderr: Buffer[] = [];
    child.stdout.on("data", (value: Buffer) => stdout.push(value));
    child.stderr.on("data", (value: Buffer) => stderr.push(value));
    child.once("error", reject);
    child.once("close", (code) => {
      if (code !== 0) {
        reject(new Error(`cave_sandbox_probe_failed:${redactSandboxError(Buffer.concat(stderr).toString("utf8"))}`));
        return;
      }
      try {
        accept(JSON.parse(Buffer.concat(stdout).toString("utf8")) as Record<string, unknown>);
      } catch {
        reject(new Error("cave_sandbox_probe_invalid_output"));
      }
    });
    });
    return result.home_read_denied === true &&
      result.home_read_code === "ERR_ACCESS_DENIED" &&
      result.child_process_denied === true &&
      result.network_denied === true &&
      result.dns_denied === true &&
      result.udp_denied === true;
  } finally {
    await rm(deniedRoot, { recursive: true, force: true });
  }
}

/** Package-internal harness bridge. Not exported from package entry point. */
export interface HarnessToolSandbox {
  readonly stagingRoot?: string;
  readonly entryPath?: string;
  readonly sourceFiles: readonly string[];
  readonly executionContext: InternalExecutionContext;
  dispose(): Promise<void>;
}

/** Package-internal harness bridge. Stages one immutable source graph. */
export async function prepareHarnessToolSandbox(
  definition: AgentDefinition,
  options: Pick<RunOptions, "rootDir" | "entryPath">,
): Promise<HarnessToolSandbox> {
  const executionContext = rootExecutionContext(definition);
  if (options.entryPath === undefined &&
      requiresSandboxEntry(definition, executionContext.sandboxRequired)) {
    throw new Error(
      "cave_tool_sandbox_entry_required: sandboxed tools need entryPath",
    );
  }
  if (options.entryPath === undefined) {
    return {
      sourceFiles: Object.freeze([]),
      executionContext,
      async dispose() {},
    };
  }
  const rootDir = options.rootDir ?? process.cwd();
  const requested = resolve(rootDir, options.entryPath);
  const snapshot = await stageSandboxSourceGraph(
    rootDir,
    requested,
    dirname(fileURLToPath(import.meta.url)),
  );
  return {
    stagingRoot: snapshot.stagingRoot,
    entryPath: snapshot.entryPath,
    sourceFiles: snapshot.sourceFiles,
    executionContext,
    dispose: snapshot.dispose,
  };
}

/** Package-internal harness bridge. Reuses Pi's exact tool/result policy. */
export function createHarnessToolExecutor(input: {
  definition: AgentDefinition;
  tool: ToolDefinition;
  sandbox: HarnessToolSandbox;
  sandboxProfile?: RunOptions["sandboxProfile"];
  engineBin?: string;
  providerDefinition?: { description: string; input: TSchema };
  deferLockedToolResult?: boolean;
}): (params: unknown, signal?: AbortSignal) => Promise<unknown> {
  if (input.tool.runtime?.kind === "subagent") {
    throw new Error("cave_claude_subagent_bridge_unavailable");
  }
  const sandboxExecute = input.definition.sandbox === "required"
    ? (params: unknown, signal?: AbortSignal) => executeSandboxedTool(
      input.sandbox.entryPath!,
      input.sandbox.sourceFiles,
      input.sandbox.stagingRoot!,
      input.tool.name,
      params,
      input.tool.timeoutMs,
      true,
      input.sandboxProfile,
      input.sandbox.executionContext,
      toolDefinitionSHA256(input.tool),
      signal,
    )
    : undefined;
  const executor = toPiTool(
    input.tool,
    sandboxExecute,
    new Set<string>(),
    input.engineBin,
    input.providerDefinition,
    input.deferLockedToolResult,
  );
  return (params, signal) => executor.execute("cave_harness_tool", params, signal);
}

export async function runAgent(
  definition: AgentDefinition,
  input: string,
  options: RunOptions = {},
): Promise<RunResult> {
  rejectInternalRunOptions(options);
  return runAgentWithOptions(
    definition,
    input,
    options,
    rootExecutionContext(definition, options.maxCostUsd, options),
  );
}

/** Package-internal compiler/CLI path. Not exported from package entry point. */
export async function runAgentInternal(
  definition: AgentDefinition,
  input: string,
  options: InternalRunOptions,
): Promise<RunResult> {
  return runAgentWithOptions(
    definition,
    input,
    options,
    rootExecutionContext(definition, options.maxCostUsd, options),
  );
}

async function runAgentWithOptions(
  definition: AgentDefinition,
  input: string,
  options: InternalRunOptions,
  executionContext: InternalExecutionContext,
): Promise<RunResult> {
  let final: RunResult | undefined;
  for await (const event of streamAgentWithOptions(
    definition,
    input,
    options,
    executionContext,
  )) {
    if (event.type === "run_end") final = event.result;
    if (event.type === "run_error") {
      // The ledger is not lost on the throwing path either: the partial receipt
      // rides on a typed `cause` so a caller that only awaits the promise can
      // still read what was spent before the failure.
      throw new CavemanRunError(event.code, event.message, event.receipt);
    }
  }
  if (!final) throw new Error("caveman agent: run ended without terminal evidence");
  return final;
}

export function streamAgent(
  definition: AgentDefinition,
  input: string,
  options: RunOptions = {},
): AsyncGenerator<CavemanRunEvent> {
  rejectInternalRunOptions(options);
  return streamAgentWithOptions(
    definition,
    input,
    options,
    rootExecutionContext(definition, options.maxCostUsd, options),
  );
}

function streamAgentWithOptions(
  definition: AgentDefinition,
  input: string,
  options: InternalRunOptions,
  executionContext: InternalExecutionContext,
): AsyncGenerator<CavemanRunEvent> {
  const controller = new AbortController();
  const signal = options.signal === undefined
    ? controller.signal
    : AbortSignal.any([options.signal, controller.signal]);
  const inner = streamAgentInternal(
    definition,
    input,
    { ...options, signal },
    executionContext,
  );
  return {
    next(value?: unknown) {
      return inner.next(value);
    },
    return(value?: void) {
      controller.abort(new Error("cave_stream_cancelled"));
      return inner.return(value);
    },
    throw(error?: unknown) {
      controller.abort(error);
      return inner.throw(error);
    },
    [Symbol.asyncIterator]() {
      return this;
    },
  } as AsyncGenerator<CavemanRunEvent>;
}

async function* streamAgentInternal(
  definition: AgentDefinition,
  input: string,
  options: InternalRunOptions,
  executionContext: InternalExecutionContext,
): AsyncGenerator<CavemanRunEvent> {
  if (options.maxSubagentInvocations !== undefined &&
      (!Number.isSafeInteger(options.maxSubagentInvocations) || options.maxSubagentInvocations <= 0 ||
        options.maxSubagentInvocations > ABSOLUTE_SUBAGENT_INVOCATION_LIMIT)) {
    throw new Error("cave_subagent_invocation_limit_invalid");
  }
  if (options.maxConcurrentSubagents !== undefined &&
      (!Number.isSafeInteger(options.maxConcurrentSubagents) || options.maxConcurrentSubagents <= 0 ||
        options.maxConcurrentSubagents > ABSOLUTE_SUBAGENT_INVOCATION_LIMIT)) {
    throw new Error("cave_subagent_concurrency_limit_invalid");
  }
  if (options.lockedBuild !== undefined && options.candidatePlan !== undefined) {
    throw new Error("cave_execution_authorization_ambiguous");
  }
  // Budget shape is settled before anything else happens: an ambiguous or
  // unbounded budget must fail at run() start, not after the first dollar.
  // maxCostUsd and budget are two different contracts for the same money —
  // one terminates with an error, the other returns a planned partial result —
  // so carrying both would leave the run's own stop semantics undecided.
  if (options.budget !== undefined && options.maxCostUsd !== undefined) {
    throw new Error("cave_budget_conflicting_cap");
  }
  const budgetMeter = executionContext.budgetMeter ?? (options.budget === undefined
    ? undefined
    : new BudgetMeter(normalizeRunBudget(options.budget)));
  if (options.deadlineMs !== undefined &&
      (!Number.isSafeInteger(options.deadlineMs) || options.deadlineMs <= 0)) {
    throw new Error("cave_run_deadline_invalid");
  }
  if (options.maxSubagentDepth !== undefined &&
      (!Number.isSafeInteger(options.maxSubagentDepth) || options.maxSubagentDepth <= 0 ||
        options.maxSubagentDepth > ABSOLUTE_SUBAGENT_DEPTH_LIMIT)) {
    throw new Error("cave_subagent_depth_limit_invalid");
  }
  const deadlineAt = executionContext.deadlineAt ?? (options.deadlineMs === undefined
    ? undefined
    : performance.now() + options.deadlineMs);
  // A controller with nothing to release would be a silent no-op at the
  // checkpoint that expected it to matter.
  if (options.budgetController !== undefined && budgetMeter === undefined) {
    throw new Error("cave_budget_controller_without_budget");
  }
  const breakers = options.breakers === undefined
    ? undefined
    : new BreakerState(normalizeRunBreakers(options.breakers, budgetMeter !== undefined));
  const efficiencyPlan = options.lockedBuild?.selected_plan ?? options.candidatePlan;
  const buildIdentity = options.lockedBuild === undefined
    ? undefined
    : {
      buildSha256: options.lockedBuild.build_sha256,
      planSha256: options.lockedBuild.plan_sha256,
    };
  const runId = crypto.randomUUID();
  yield { type: "run_start", runId, agentId: definition.id };
  let conversation: ConversationTransaction | undefined;
  let activeAbort: (() => void) | undefined;
  let activeExecution: Promise<void> | undefined;
  let sandboxSourceSnapshot: SandboxSourceSnapshot | undefined;
  const pendingSpendReservations: SpendReservation[][] = [];
  const pendingCallRecords: PendingCallRecord[] = [];
  let spendFailure: Error | undefined;
  // Set when the ladder declines to start another call. Pi answers the refusal
  // with one synthesized zero-usage error turn; that turn is our own stop, not
  // provider evidence, so it is skipped by the usage accounting below and
  // dropped from the committed conversation.
  let stopReason: RunStopReason | undefined;
  let ladderFailure: Error | undefined;
  let refusalPending = false;
  let refusalMessage: AssistantMessage | undefined;
  const receipt = new ReceiptRecorder();
  // Declared outside the main try so failed descendants can still be attached
  // to this run's terminal receipt in the catch path.
  const nestedReceipts: RunReceipt[] = [];
  let boundController: BudgetController | undefined;
  const releaseConversation = () => {
    if (!conversation) return;
    endConversation(conversation);
    conversation = undefined;
  };
  // Owned resources released BEFORE the terminal event is yielded, so a manual
  // `.next()` consumer that stops the moment it receives run_end/run_error does
  // not strand the staged source-graph tempdir or leave the budget controller
  // bound to a meter nothing reads any more.
  // Idempotent — the finally calls it again for the abort/throw paths.
  let runResourcesReleased = false;
  let invocationFinished = false;
  const finishCurrentInvocation = (ok: boolean) => {
    if (invocationFinished) return;
    invocationFinished = true;
    finishInvocation(executionContext, ok);
    if (executionContext.depth === 0) exportInvocationBatch(executionContext.invocationState);
  };
  const releaseRunResources = async (): Promise<void> => {
    if (runResourcesReleased) return;
    runResourcesReleased = true;
    if (boundController !== undefined) unbindBudgetController(boundController);
    await sandboxSourceSnapshot?.dispose();
  };

  try {
    if (options.budgetController !== undefined && budgetMeter !== undefined) {
      bindBudgetController(options.budgetController, budgetMeter);
      boundController = options.budgetController;
    }
    conversation = options.conversation === undefined
      ? undefined
      : beginConversation(options.conversation);
    if (options.entryPath === undefined &&
        requiresSandboxEntry(definition, executionContext.sandboxRequired)) {
      throw new Error(
        "cave_tool_sandbox_entry_required: sandboxed tools need RunOptions.entryPath; use sandbox fixture only for trusted tests",
      );
    }
    if (options.lockedBuild !== undefined) {
      const lockedContext = await lowerAgentContext(definition, {
        ...(options.rootDir === undefined ? {} : { rootDir: options.rootDir }),
      });
      prepareLockedHarnessExecution({
        build: options.lockedBuild,
        harness: "pi",
        adapterVersion: PI_ADAPTER_VERSION,
        upstreamVersion: PI_UPSTREAM_VERSION,
        agentId: definition.id,
        contextIR: lockedContext.ir,
        plan: options.lockedBuild.selected_plan,
      });
    }
    const originalConversationMessages = conversation?.messages.slice() ?? [];
    const initialConversationSegments = conversationTextSegments(originalConversationMessages);
    const lowered = await lowerAgentContext(definition, {
      ...(options.rootDir === undefined ? {} : { rootDir: options.rootDir }),
      runtimeSegments: initialConversationSegments.map((segment) => ({
        id: segment.id,
        kind: segment.kind,
        body: new TextEncoder().encode(segment.text),
      })),
      input,
    });
    const bill = contextBill(lowered.ir);
    if (efficiencyPlan) enforceSemanticBudgets(
      bill,
      definition.output?.maxTokens ?? 0,
      efficiencyPlan,
    );
    yield { type: "context_ready", runId, contextIR: lowered.ir, bill };
    const appliedPlan = await applyEfficiencyPlan(lowered, efficiencyPlan, options.engineBin, options.signal);
    const frameworkDistRoot = dirname(fileURLToPath(import.meta.url));
    const requestedSandboxEntry = options.entryPath === undefined
      ? undefined
      : resolve(options.rootDir ?? process.cwd(), options.entryPath);
    sandboxSourceSnapshot = requestedSandboxEntry === undefined
      ? undefined
      : await stageSandboxSourceGraph(
        options.rootDir ?? process.cwd(),
        requestedSandboxEntry,
        frameworkDistRoot,
      );
    const sandboxEntry = sandboxSourceSnapshot?.entryPath;
    const sandboxSourceFiles = sandboxSourceSnapshot?.sourceFiles ?? [];
    const sandboxStagingRoot = sandboxSourceSnapshot?.stagingRoot;
    const conversationOriginals = new Map<string, string>();
    const initialProviderMessages = replaceConversationSegments(
      originalConversationMessages,
      initialConversationSegments,
      lowered,
      appliedPlan,
      conversationOriginals,
    );

    const gatewayURL = resolveGatewayURL(options.gatewayURL);
    const caveRoute = await resolveCaveRoute(gatewayURL, {
      ...options,
      billingProofRequired: budgetMeter?.denomination === "usd" && options.streamFn === undefined,
    }, efficiencyPlan !== undefined);
    const nestedOptions: InternalRunOptions = { ...options, caveRoute };

    const models = options.models ?? builtinModels();
    const model = options.model ?? resolveModel(definition, models, options.rootDir ?? process.cwd());
    // Runtime-gated denomination, first ground: the catalog must
    // price the model, or a USD cap meters an honest zero and never binds. The
    // second ground — the credential regime — is checked after routing below,
    // because which credential pays depends on where the request goes.
    if (budgetMeter?.denomination === "usd" &&
        !modelIsPriced(model.provider, model.id)) {
      throw new Error("cave_budget_denomination_unavailable");
    }
    // Actual routing is the source of truth, not the route decision: the
    // gateway only speaks the three provider dialects it proxies, so a model
    // outside them keeps its own base URL even on a reachable gateway. Every
    // downstream honesty question — which headers may be sent, what mode this
    // run may claim — reads gatewayActive, never caveRoute.useGateway alone.
    const routing = caveRoute.useGateway
      ? routeModelThroughCave(model, gatewayURL)
      : { model, routed: false };
    const routedModel = routing.model;
    const gatewayActive = routing.routed;
    // Runtime-gated denomination, second ground: the run must actually be
    // BILLED in dollars. A Claude Pro/Max subscription reached through Pi's
    // credential store is not billed per token, so every dollar this ledger
    // reported for it would be fiction.
    //
    // Which credential pays is what decides this. A caller-supplied streamFn
    // owns its transport, so local login says nothing about billing. A routed
    // gateway skips the local credential gate ONLY when its identified
    // readiness response says it supplies a managed provider credential.
    // Standalone BYOK and unknown/legacy gateways fall through to Pi's actual
    // credential regime; ambient account-key presence proves no payer.
    if (budgetMeter?.denomination === "usd" &&
        options.streamFn === undefined &&
        (!gatewayActive || caveRoute.providerBilling !== "managed")) {
      const regime = await credentialRegime(models, model.provider);
      // A subscription is fiction in dollars; an unprovable regime fails closed
      // rather than fail-open into fictional dollars. The escape
      // hatch is the caller asserting, on its own authority, that this
      // credential is a metered API key.
      if (regime === "subscription" ||
          (regime === "unknown" && options.assumeMeteredCredential !== true)) {
        throw new Error("cave_budget_denomination_unavailable");
      }
    }
    if (efficiencyPlan !== undefined) {
      validatePlanSelection(efficiencyPlan, {
        provider: model.provider,
        model: model.id,
        reasoning: definition.reasoning,
      });
    }
    const sessionId = options.sessionId ?? conversation?.sessionId ?? `${definition.id}-${runId}`;
    if (gatewayActive && executionContext.depth === 0 && executionContext.invocationState.batch === undefined) {
      executionContext.invocationState.batch = {
        gatewayURL,
        fetch: options.fetch ?? globalThis.fetch,
        ...(process.env.CAVE_API_KEY === undefined ? {} : { apiKey: process.env.CAVE_API_KEY }),
        agentId: definition.id,
        workflow: options.workflow ?? definition.id,
        sessionId,
        spans: [],
        dropped: 0,
        exported: false,
      };
    }
    const recoveryHandles = appliedPlan.recoveryHandles;
    const nestedUsage: NestedUsage = {
      calls: new Map(),
      budgets: new Map(),
      receipts: nestedReceipts,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
      reasoningTokens: 0,
      costUsd: 0,
      unpriced: false,
      incomplete: false,
      observeOnly: false,
    };
    const hasDynamicRecoveryRoute = efficiencyPlan?.segment_routes.some((route) =>
      route.segment_kind === "history" || route.segment_kind === "tool_result") ?? false;
    const hasToolResultRoute = efficiencyPlan?.segment_routes.some((route) =>
      route.segment_kind === "tool_result") ?? false;
    const needsRecoveryTool = recoveryHandles.size > 0 || hasDynamicRecoveryRoute ||
      definition.tools.some((item) => item.result !== "inline");
    const needsToolSearch = appliedPlan.appliedTransformIDs.includes("caveman.engine.toolschema.v1");
    const memoryTools = definition.memory === undefined
      ? []
      : localMemoryTools(definition.memory, definition.id, options.memory);
    const piTools: AgentTool<TSchema>[] = [
      ...definition.tools.map((item) => {
        const providerTool = providerToolDefinition(item, lowered, appliedPlan);
        return toPiTool(
          item,
          item.runtime?.kind === "subagent"
          ? (params, signal) => executeSubagent(
            item,
            params,
            signal,
            { ...nestedOptions, model },
            nestedUsage,
            executionContext,
            budgetMeter,
            deadlineAt,
          )
          // Host mode runs closures in this process by explicit opt-in, so a
          // staged entry path never promotes them into a tool worker.
          : options.entryPath === undefined || definition.sandbox === "host"
          ? undefined
          : (params, signal) => executeSandboxedTool(
            sandboxEntry!,
            sandboxSourceFiles,
            sandboxStagingRoot!,
            item.name,
            params,
            item.timeoutMs,
            definition.sandbox === "required",
            options.sandboxProfile,
            executionContext,
            toolDefinitionSHA256(item),
            signal,
          ),
          recoveryHandles,
          options.engineBin,
          providerTool,
          hasToolResultRoute,
        );
      }),
      ...(needsRecoveryTool ? [recoveryTool(recoveryHandles, options.engineBin, appliedPlan.trace)] : []),
      ...(needsToolSearch ? [toolSchemaSearchTool(recoveryHandles, options.engineBin, appliedPlan.trace)] : []),
      ...memoryTools,
    ];
    const originalInstructions = assembleSystemPrompt(definition, lowered);
    const ambiguousCustomAdapter = options.streamFn !== undefined &&
      options.providerPayloadContract !== "pi-on-payload-v1" &&
      appliedPlan.appliedTransformIDs.length > 0;
    if (ambiguousCustomAdapter) {
      markAppliedPlanFailedOpen(appliedPlan, "cache_adapter_ambiguous");
    }
    const instructions = ambiguousCustomAdapter
      ? originalInstructions
      : assembleSystemPrompt(definition, lowered, appliedPlan.bodies);
    const prefixDigest = sha256(stableStringify({
      instructions,
      tools: piTools.map((item) => ({
        name: item.name,
        description: item.description,
        parameters: item.parameters,
      })),
    }));
    const conversationFingerprint = sha256(stableStringify({
      agentId: definition.id,
      provider: routedModel.provider,
      model: routedModel.id,
      api: routedModel.api,
      prefixDigest,
      buildIdentity: buildIdentity ?? null,
      lockedPlanSHA256: buildIdentity?.planSha256 ?? null,
      executionPlanSHA256: efficiencyPlan === undefined
        ? null
        : sha256(stableStringify(efficiencyPlan)),
    }));
    if (conversation?.fingerprint !== undefined &&
        conversation.fingerprint !== conversationFingerprint) {
      conversation.cachePrefixDigest = undefined;
      conversation.providerFrozen = undefined;
      conversation.originalFrozen = undefined;
    }
    if (conversation) conversation.fingerprint = conversationFingerprint;
    if (efficiencyPlan?.segment_routes.length && volatileStablePrefix(instructions, definition.tools)) {
      throw new Error("cave_cache_volatile_stable_slot");
    }
    // Every x-cave-* header exists for the Caveman gateway: x-cave-api-key is
    // an account credential, and agent/workflow/session/cache-epoch/prefix
    // digest/context bill/build+plan digests are account-linked identifiers and
    // internal telemetry. A request that does not go through the gateway goes to
    // a third party, so it carries none of them.
    const headers = gatewayActive
      ? runtimeHeaders(
        definition.id,
        options.workflow ?? definition.id,
        sessionId,
        buildIdentity,
        bill,
        appliedPlan.appliedTransformIDs,
        prefixDigest,
        conversationFingerprint,
        executionContext.invocationState.batch?.apiKey,
        executionContext.invocationState.batch === undefined ? undefined : executionContext.invocationTrace,
      )
      : undefined;
    const baseStream = options.streamFn ?? models.streamSimple.bind(models);
    let providerPrefixDigest = conversation?.cachePrefixDigest;
    let providerFrozen = conversation?.providerFrozen;
    let originalFrozen = conversation?.originalFrozen;
    let cacheBoundaryKnown = false;
    let cacheBust = false;
    let finalMessage: AssistantMessage | undefined;
    let inputTokens = 0;
    let outputTokens = 0;
    let cacheReadTokens = 0;
    let cacheWriteTokens = 0;
    let reasoningTokens = 0;
    let reasoningUsageUnavailable = false;
    let costUsd = 0;
    let unpricedCall = false;
    let usageFailure: Error | undefined;
    let modelCalls = 0;
    let compactionsUsed = 0;
    // Incremented the moment a compaction takes a reservation, so a paid
    // attempt counts even when its summary is later discarded.
    let compactionsSpent = 0;
    let previousSummary: ContextSummary | undefined;
    // Provider-reported, never assumed: the last call either read a cached
    // prefix or it did not. Before the first call there is nothing to report.
    let lastCallCacheState: "warm" | "cold" | "unknown" = "unknown";
    const toolCalls: string[] = [];
    let turnStateChanged = false;
    const startedAt = performance.now();
    // Caller-supplied ceilings override the derived defaults. They
    // fail closed: a non-positive or non-integer value is a malformed cap, not
    // "no cap", so it is rejected before the first provider call.
    const callCeilingOverride = (value: number | undefined, field: string): number | undefined => {
      if (value === undefined) return undefined;
      if (!Number.isInteger(value) || value < 1) {
        throw new Error(`cave_${field}_invalid`);
      }
      return value;
    };
    const maxModelCalls = callCeilingOverride(options.maxModelCalls, "max_model_calls") ??
      (efficiencyPlan === undefined
        ? 64
        : 1 + Math.max(1, Math.ceil(efficiencyPlan.budgets.retry_cascade_reserve / 256)));
    const maxToolCalls = callCeilingOverride(options.maxToolCalls, "max_tool_calls") ??
      (efficiencyPlan === undefined
        ? 64
        : Math.max(1, definition.tools.length) * Math.max(1, maxModelCalls - 1));
    const accountedAssistantMessages = new WeakSet<object>();
    const accountProviderMessage = (message: AssistantMessage): void => {
      // beforeToolCall runs after the provider message but before its tools.
      // Account there so a call that consumed the deadline or wallet cannot
      // launch fresh side effects. turn_end calls this too for tool-free turns;
      // object identity makes the operation exactly once on either path.
      if (accountedAssistantMessages.has(message)) {
        finalMessage = message;
        return;
      }
      accountedAssistantMessages.add(message);
      const reservation = pendingSpendReservations.shift();
      const pendingCall = pendingCallRecords.shift();
      const budgetReservation = pendingCall?.reservation;
      const reservationMeter = pendingCall?.reservationMeter;
      try {
        if (definition.reasoning !== "off" && routedModel.reasoning === true &&
            message.usage.reasoning === undefined) {
          reasoningUsageUnavailable = true;
        }
        const usage = validateProviderUsage({
          provider: message.provider,
          model: message.model,
          inputTokens: message.usage.input,
          outputTokens: message.usage.output,
          cacheReadTokens: message.usage.cacheRead,
          cacheWriteTokens: message.usage.cacheWrite,
          reasoningTokens: message.usage.reasoning ?? 0,
          totalTokens: message.usage.totalTokens,
        }, {
          expected: { provider: routedModel.provider, model: routedModel.id },
          requirePriced: reservation !== undefined && reservation.length > 0,
        });
        if (reservation !== undefined) {
          spendFailure ??= settleProviderSpend(reservation, usage);
        }
        if (budgetReservation !== undefined) {
          const measuredSpend = reservationMeter?.denomination === "usd"
            ? usage.catalogCostUsd
            : usage.totalTokens;
          reservationMeter?.settle(
            budgetReservation,
            measuredSpend,
          );
          if (pendingCall?.retryAttempt !== undefined) {
            breakers?.settleRetry(pendingCall.retryAttempt, measuredSpend, "provider_reported");
          }
        }
        inputTokens += usage.inputTokens;
        outputTokens += usage.outputTokens;
        cacheReadTokens += usage.cacheReadTokens;
        cacheWriteTokens += usage.cacheWriteTokens;
        reasoningTokens += usage.reasoningTokens;
        costUsd += usage.catalogCostUsd;
        if (!usage.priced) unpricedCall = true;
        lastCallCacheState = usage.cacheReadTokens > 0 ? "warm" : "cold";
        receipt.recordCall({
          provider: usage.provider,
          model: usage.model,
          inputTokens: usage.inputTokens,
          outputTokens: usage.outputTokens,
          cacheReadTokens: usage.cacheReadTokens,
          cacheWriteTokens: usage.cacheWriteTokens,
          reasoningTokens: usage.reasoningTokens,
          estimatedUsd: usage.catalogCostUsd,
          unpriced: !usage.priced,
          usageBasis: "provider_reported",
          clampedOutputTokens: pendingCall?.clampedOutputTokens,
        });
      } catch (error) {
        const failure = error instanceof Error ? error : new Error(String(error));
        usageFailure ??= failure;
        unpricedCall = true;
        receipt.recordCall({
          provider: routedModel.provider,
          model: routedModel.id,
          inputTokens: 0,
          outputTokens: 0,
          cacheReadTokens: 0,
          cacheWriteTokens: 0,
          reasoningTokens: 0,
          estimatedUsd: 0,
          unpriced: true,
          usageBasis: "unavailable",
          clampedOutputTokens: pendingCall?.clampedOutputTokens,
        });
        // Worst-case settlement keeps unreadable provider usage from looking
        // free. A later tool must see the resulting dead wallet.
        if (budgetReservation !== undefined) {
          reservationMeter?.settle(budgetReservation, budgetReservation.amount);
          if (pendingCall?.retryAttempt !== undefined) {
            breakers?.settleRetry(
              pendingCall.retryAttempt,
              budgetReservation.amount,
              "unavailable_worst_case",
            );
          }
        }
        if (reservation !== undefined && reservation.length > 0) {
          markSpendIncomplete(reservation.map((item) => item.ledger));
          spendFailure ??= failure;
        }
      }
      finalMessage = message;
    };
    const streamFn: StreamFn = async (selected, context, streamOptions) => {
      if (options.signal?.aborted) {
        throw options.signal.reason ?? new Error("cave_run_aborted");
      }
      if (spendFailure) throw spendFailure;
      if (usageFailure) throw usageFailure;
      if (nestedUsage.incomplete) throw new Error("cave_nested_usage_incomplete");
      if (efficiencyPlan && reasoningUsageUnavailable) {
        throw new Error("cave_reasoning_usage_unavailable");
      }
      if (efficiencyPlan) {
        enforceSemanticBudgets(contextBill(lowered.ir), outputTokens, efficiencyPlan);
        if (reasoningTokens > efficiencyPlan.budgets.reasoning) {
          throw new Error("cave_reasoning_budget_exceeded");
        }
      }
      // The hard model-call ceiling is a stop condition, not a failure: ending
      // the run through the same graceful path as every other stop keeps the
      // partial work and the receipt intact. Checked before the
      // increment so exactly `maxModelCalls` calls are allowed.
      if (modelCalls >= maxModelCalls) {
        stopReason = "call_budget_exhausted";
        refusalPending = true;
        throw new Error("cave_run_stopped");
      }
      modelCalls++;
      // Between-calls stop point. Nothing is in flight here: the previous turn
      // and its tools have finished and settled, and this call has not started.
      const plan = () => decideNextCall({
        meter: budgetMeter,
        breakers,
        deadlineAt,
        selected,
        context,
        requestedOutputTokens: streamOptions?.maxTokens,
        outputMaxTokens: definition.output?.maxTokens,
        planOutputTokens: efficiencyPlan?.budgets.output,
        restorableBytes: restorableRequestBytes(
          conversationOriginals,
          instructions,
          originalInstructions,
        ),
      });
      let decided: NextCallDecision;
      try {
        decided = plan();
        // Escalation gets exactly one attempt per exhaustion: the handler either
        // funds the next call or it does not, and a handler that keeps releasing
        // slivers must not turn one stop into an unbounded loop.
        if (decided.action === "stop" && decided.reason === "budget_exhausted" &&
            budgetMeter !== undefined && typeof options.onBudgetExhausted === "function") {
          const outcome = await options.onBudgetExhausted(budgetExhaustionContext(budgetMeter));
          if (outcome !== "stop") {
            if (!isRecord(outcome) || typeof outcome.release !== "number" ||
                typeof outcome.reason !== "string") {
              throw new Error("cave_budget_escalation_result_invalid");
            }
            budgetMeter.release(outcome.release, outcome.reason);
            decided = plan();
          }
        }
      } catch (error) {
        // Pi answers a thrown streamFn with a synthesized terminal error turn,
        // which would otherwise replace this cause with a generic provider
        // failure. Record it first so the run reports what actually went wrong.
        ladderFailure ??= error instanceof Error ? error : new Error(String(error));
        throw ladderFailure;
      }
      if (decided.action === "stop") {
        // Pi answers a thrown streamFn with a synthesized zero-usage error turn,
        // so record the refusal before throwing or the stop becomes the usage
        // failure that turn produces.
        stopReason = decided.reason;
        refusalPending = true;
        throw new Error("cave_run_stopped");
      }
      if (decided.action === "compact") {
        // Compaction happens between turns, where a rewritten context can
        // persist. By the time a call is being made the ladder has only the
        // clamp and stop rungs left, so this branch is unreachable and fails
        // closed rather than silently proceeding at an unreserved cost.
        stopReason = "budget_exhausted";
        refusalPending = true;
        throw new Error("cave_run_stopped");
      }
      const pendingCall: PendingCallRecord = {
        reservation: decided.reservation,
        reservationMeter: budgetMeter,
        retryAttempt: undefined,
        clampedOutputTokens: decided.clampedOutputTokens,
      };
      pendingCallRecords.push(pendingCall);
      // Pi answers a thrown streamFn with a synthesized zero-usage error turn,
      // so record the refusal before throwing or the terminal error becomes the
      // usage failure that turn produces.
      let spendReservations: SpendReservation[];
      try {
        spendReservations = reserveProviderSpend(
          executionContext.spendLedgers,
          selected,
          definition.output?.maxTokens ?? efficiencyPlan?.budgets.output,
        );
      } catch (error) {
        spendFailure ??= error instanceof Error ? error : new Error(String(error));
        const orphaned = pendingCallRecords.pop();
        if (orphaned?.reservation !== undefined) {
          orphaned.reservationMeter?.cancel(orphaned.reservation);
        }
        throw spendFailure;
      }
      pendingSpendReservations.push(spendReservations);
      const transformTrace = serializeTransformTrace(appliedPlan.trace);
      const requestHeaders = mergeHeaders(
        streamOptions?.headers,
        headers === undefined ? {} : {
          ...headers,
          "x-cave-context-bill": serializeContextBill(contextBill(lowered.ir)),
          "x-cave-transforms": appliedPlan.appliedTransformIDs.length > 0
            ? appliedPlan.appliedTransformIDs.join(",")
            : "caveman.pass-through.v1",
          ...(transformTrace === "" ? {} : { "x-cave-transform-trace": transformTrace }),
        },
      );
      // Cache-boundary bookkeeping writes gateway headers as it goes. Off the
      // gateway there is nothing to write them to, so the same code path leaks
      // nothing to the provider.
      const caveHeaders = headers === undefined ? undefined : requestHeaders;
      const setCaveHeader = (name: string, value: string): void => {
        if (caveHeaders !== undefined) caveHeaders[name] = value;
      };
      const upstreamOnPayload = streamOptions?.onPayload;
      const callSignal = options.signal === undefined
        ? streamOptions?.signal
        : streamOptions?.signal === undefined
          ? options.signal
          : AbortSignal.any([streamOptions.signal, options.signal]);
      const streamOnce = () => baseStream(selected, context, {
        ...streamOptions,
        // Pi resolves GEMINI_API_KEY only. SDK advertises GOOGLE_API_KEY too,
        // so expose alias under Pi's expected env name. Env overlay preserves
        // Pi's stored-credential precedence; explicit apiKey still wins.
        ...(selected.provider === "google" && streamOptions?.apiKey === undefined &&
            !streamOptions?.env?.GEMINI_API_KEY && !process.env.GEMINI_API_KEY &&
            process.env.GOOGLE_API_KEY
          ? { env: { ...(streamOptions?.env ?? {}), GEMINI_API_KEY: process.env.GOOGLE_API_KEY } }
          : {}),
        ...(callSignal === undefined ? {} : { signal: callSignal }),
        // The budget clamp is one more ceiling on the same allowance: it only
        // ever lowers what the call may ask for, never raises it.
        ...((definition.output === undefined && efficiencyPlan === undefined &&
            decided.clampedOutputTokens === undefined) ? {} : {
          maxTokens: Math.min(
            streamOptions?.maxTokens ?? definition.output?.maxTokens ??
              efficiencyPlan?.budgets.output ?? Number.MAX_SAFE_INTEGER,
            definition.output?.maxTokens ?? Number.MAX_SAFE_INTEGER,
            efficiencyPlan?.budgets.output ?? Number.MAX_SAFE_INTEGER,
            decided.clampedOutputTokens ?? Number.MAX_SAFE_INTEGER,
          ),
        }),
        sessionId,
        headers: requestHeaders,
        onPayload(payload, providerModel) {
          const inspect = (candidate: unknown): unknown => {
            let outgoing = candidate;
            let frozen = providerFrozenView(outgoing, providerModel.api);
            if (!frozen) {
              if (appliedPlan.appliedTransformIDs.length > 0) {
                cacheBust = true;
                outgoing = replaceExactStrings(outgoing, instructions, originalInstructions);
                markRequestPassThrough(caveHeaders, appliedPlan, "cache_boundary_unknown");
              }
              return outgoing;
            }
            cacheBoundaryKnown = true;
            const nextDigest = sha256(frozen.bytes);
            if (providerPrefixDigest === undefined) {
              providerPrefixDigest = nextDigest;
              providerFrozen = frozen;
              originalFrozen = providerFrozenView(
                restoreOriginalPayload(
                  replaceExactStrings(outgoing, instructions, originalInstructions),
                  conversationOriginals,
                ),
                providerModel.api,
              );
              setCaveHeader("x-cave-cache-prefix-sha256", nextDigest);
              return outgoing;
            }
            if (providerFrozen === undefined || !providerFrozenExtends(frozen, providerFrozen)) {
              cacheBust = true;
              outgoing = originalFrozen === undefined
                ? restoreOriginalPayload(
                  replaceExactStrings(outgoing, instructions, originalInstructions),
                  conversationOriginals,
                )
                : restoreProviderFrozen(outgoing, originalFrozen, frozen);
              frozen = providerFrozenView(outgoing, providerModel.api);
              const fallbackDigest = frozen === undefined ? prefixDigest : sha256(frozen.bytes);
              setCaveHeader(
                "x-cave-cache-epoch",
                `${definition.id}:${options.workflow ?? definition.id}:${sessionId}:fallback:${fallbackDigest.slice(0, 16)}`,
              );
              setCaveHeader("x-cave-cache-prefix-sha256", fallbackDigest);
              markRequestPassThrough(caveHeaders, appliedPlan, "cache_prefix_drift");
            } else {
              providerFrozen = frozen;
              originalFrozen = providerFrozenView(
                restoreOriginalPayload(
                  replaceExactStrings(outgoing, instructions, originalInstructions),
                  conversationOriginals,
                ),
                providerModel.api,
              );
              setCaveHeader("x-cave-cache-prefix-sha256", providerPrefixDigest);
            }
            return outgoing;
          };
          const replaced = upstreamOnPayload?.(payload, providerModel);
          if (replaced instanceof Promise) {
            return replaced.then((value) => inspect(value === undefined ? payload : value));
          }
          return inspect(replaced === undefined ? payload : replaced);
        },
      });
      // Cost-aware retry. Only a call that fails before producing any stream at
      // all is retried, because that is the one shape where nothing was spent
      // and nothing was consumed. Each retry takes a real hold from the run's
      // meter, then cancels it at measured zero if the pre-stream failure
      // repeats. Receipt events also sum worst-case reserved exposure, so a
      // zero-spend error storm still exhausts its declared allowance. Backoff
      // stays deterministic — a breaker has to be reproducible.
      if (breakers?.config.retryMaxSpend === undefined || decided.reservation === undefined) {
        return streamOnce();
      }
      const retryStopError = (): Error | undefined => {
        if (callSignal?.aborted) {
          return callSignal.reason instanceof Error
            ? callSignal.reason
            : new Error("cave_run_aborted");
        }
        if (deadlineAt !== undefined && performance.now() >= deadlineAt) {
          stopReason = "deadline";
          refusalPending = true;
          return new Error("cave_run_stopped");
        }
        if (budgetMeter?.revoked) {
          stopReason = "wallet_revoked";
          refusalPending = true;
          return new Error("cave_run_stopped");
        }
        if (budgetMeter?.capBreached) {
          stopReason = "budget_exhausted";
          refusalPending = true;
          return new Error("cave_run_stopped");
        }
        return undefined;
      };
      const abandonPendingCall = (): void => {
        const index = pendingCallRecords.indexOf(pendingCall);
        if (index < 0) return;
        pendingCallRecords.splice(index, 1);
        const [legacyReservations] = pendingSpendReservations.splice(index, 1);
        if (legacyReservations !== undefined) releaseLedgerHolds(legacyReservations);
      };
      return retryModelCall(
        streamOnce,
        breakers,
        pendingCall,
        retryStopError,
        abandonPendingCall,
      );
    };

    const pi = new Agent({
      initialState: {
        systemPrompt: instructions,
        model: routedModel,
        thinkingLevel: definition.reasoning,
        tools: piTools,
        ...(conversation === undefined ? {} : {
          messages: initialProviderMessages,
        }),
      },
      sessionId,
      streamFn,
      transformContext: async (messages) => transformConversationContext(
        messages,
        originalConversationMessages.length,
        lowered,
        efficiencyPlan,
        options.engineBin,
        appliedPlan,
        conversationOriginals,
        options.signal,
      ),
      // The between-calls point where a compaction can persist: the turn has
      // settled, no tool is in flight, and the replacement context this returns
      // is what the next call is built from.
      prepareNextTurnWithContext: async (turn) => {
        if (budgetMeter === undefined || budgetMeter.onExhausted !== "compact") return undefined;
        if (compactionsUsed >= budgetMeter.compaction.maxCompactions) return undefined;
        // Pi runs this hook after EVERY turn, including the last one. A turn
        // that asked for no tools is the end of the loop: no working call
        // follows it, so a rewrite here would buy a context nothing reads.
        const requestedTools = turn.message.role === "assistant" &&
          turn.message.content.some((part) => part.type === "toolCall");
        if (!requestedTools) return undefined;
        // The run has already decided to stop; paying a summarizer now buys
        // nothing. The ladder's other stop conditions bind here exactly as they
        // bind at the call boundary.
        if (breakers?.tripped !== undefined) return undefined;
        if (deadlineAt !== undefined && performance.now() >= deadlineAt) return undefined;
        const outputTokenCap = Math.min(
          definition.output?.maxTokens ?? Number.MAX_SAFE_INTEGER,
          efficiencyPlan?.budgets.output ?? Number.MAX_SAFE_INTEGER,
          routedModel.maxTokens,
        );
        const nextCall = callCeilingFor(
          routedModel,
          instructions,
          turn.context.messages,
          turn.context.tools,
          outputTokenCap,
        );
        const nextCallCost = callCeilingCost(
          budgetMeter.denomination,
          nextCall,
          nextCall.outputTokenCap,
        );
        // Trigger before exhaustion. At-max same-model compaction is
        // mathematically unreachable: once one working call no longer fits,
        // that same-sized summarizer plus post-rewrite headroom cannot fit
        // either. Four call ceilings preserves default-on compact-and-continue
        // while every actual provider call remains hard-reserved.
        if (nextCallCost === undefined ||
            budgetMeter.remaining() >= COMPACTION_TRIGGER_MULTIPLIER * nextCallCost) {
          return undefined;
        }
        const compacted = await compactContext({
          messages: turn.context.messages,
          systemPrompt: instructions,
          tools: turn.context.tools,
          meter: budgetMeter,
          selected: routedModel,
          outputTokenCap,
          baseStream,
          sessionId,
          signal: options.signal,
          previousSummary,
          receipt,
          headers,
          spendLedgers: executionContext.spendLedgers,
          onReserved: () => { compactionsSpent++; },
          // A compaction is a provider call this run made. Its usage joins the
          // run's own totals so RunResult and the receipt cannot disagree, and
          // so a parent aggregating this child sees the whole cost.
          accrue: (usage) => {
            if (usage === undefined) {
              usageFailure ??= new Error("cave_provider_usage_incomplete");
              unpricedCall = true;
              return;
            }
            inputTokens += usage.inputTokens;
            outputTokens += usage.outputTokens;
            cacheReadTokens += usage.cacheReadTokens;
            cacheWriteTokens += usage.cacheWriteTokens;
            reasoningTokens += usage.reasoningTokens;
            costUsd += usage.catalogCostUsd;
            if (!usage.priced) unpricedCall = true;
          },
          cacheState: lastCallCacheState,
        });
        // Only an attempt that actually reserved counts against the cap. A
        // precondition decline costs nothing, so burning the budget on one
        // would close the rung for a later turn that could have afforded it —
        // while a summarizer producing unusable output must still be stopped
        // from being retried every turn at a paid call each time.
        if (compactionsSpent > compactionsUsed) compactionsUsed = compactionsSpent;
        if (compacted === undefined) return undefined;
        previousSummary = compacted.summary;
        return { context: { ...turn.context, messages: compacted.messages } };
      },
      beforeToolCall: async ({ toolCall, args, assistantMessage }) => {
        accountProviderMessage(assistantMessage);
        if (deadlineAt !== undefined && performance.now() >= deadlineAt) {
          stopReason = "deadline";
          return { block: true, reason: "cave_run_deadline_exceeded" };
        }
        if (budgetMeter?.revoked) {
          stopReason = "wallet_revoked";
          return { block: true, reason: "cave_budget_revoked" };
        }
        if (budgetMeter !== undefined &&
            (budgetMeter.capBreached || budgetMeter.remaining() <= 0)) {
          stopReason = "budget_exhausted";
          return { block: true, reason: "cave_budget_exhausted" };
        }
        // Pi emits tool_execution_start before this hook, so current call is
        // already present. Block only calls beyond locked ceiling.
        if (toolCalls.length > maxToolCalls) {
          return { block: true, reason: "cave_tool_call_budget_exceeded" };
        }
        if (breakers !== undefined) {
          const decision = breakers.observeToolCall({
            toolCallId: toolCall.id,
            toolName: toolCall.name,
            args,
            allowRepeat: definition.tools.find((item) => item.name === toolCall.name)
              ?.allowRepeat === true,
            turnKey: assistantMessage,
          });
          if (decision.block) return { block: true, reason: decision.reason! };
        }
        if ((toolCall.name === "cave_retrieve" && needsRecoveryTool) ||
            (toolCall.name.startsWith("cave_memory_") && definition.memory !== undefined)) {
          if (args === null || typeof args !== "object" || Array.isArray(args)) {
            return { block: true, reason: "cave_tool_arguments_invalid" };
          }
          return undefined;
        }
        const configured = definition.tools.find((item) => item.name === toolCall.name);
        if (!configured) return { block: true, reason: "cave_unknown_tool" };
        if (configured.effect === "write" && definition.sandbox === "fixture") {
          return { block: true, reason: "cave_side_effect_blocked" };
        }
        if (definition.sandbox === "required" &&
            configured.runtime?.kind !== "subagent" &&
            options.entryPath === undefined) {
          return { block: true, reason: "cave_sandbox_entry_required" };
        }
        if (args === null || typeof args !== "object" || Array.isArray(args)) {
          return { block: true, reason: "cave_tool_arguments_invalid" };
        }
        return undefined;
      },
    });

    const queued: AgentEvent[] = [];
    let wake: (() => void) | undefined;
    let terminal = false;
    pi.subscribe((event) => {
      // The one turn Pi synthesizes for a refused call carries no provider
      // evidence. It is recorded as the run's own stop, never accounted.
      let synthesizedRefusal = false;
      if (event.type === "turn_end" && event.message.role === "assistant" && refusalPending) {
        synthesizedRefusal = true;
        refusalPending = false;
        refusalMessage = event.message;
      }
      if (event.type === "turn_end" && event.message.role === "assistant" &&
          !synthesizedRefusal) {
        accountProviderMessage(event.message);
        breakers?.observeTurn(
          assistantText(event.message),
          event.toolResults.map((item) => ({
            toolName: item.toolName,
            isError: item.isError,
            content: item.content,
          })),
          turnStateChanged,
        );
        turnStateChanged = false;
      }
      if (event.type === "tool_execution_start") {
        toolCalls.push(event.toolName);
        receipt.recordToolCall(event.toolName);
      }
      if (event.type === "tool_execution_end") {
        receipt.recordToolOutcome(event.toolName, event.isError);
        breakers?.observeToolResult(event.toolCallId, event.isError);
        const configured = definition.tools.find((item) => item.name === event.toolName);
        // Progress follows state, not display text. Declared writes are the
        // obvious case; framework-owned memory tools and successful subagents
        // can also mutate durable or opaque child state while returning the
        // same short result each turn.
        if (!event.isError && (configured?.effect === "write" ||
            configured?.runtime?.kind === "subagent" ||
            event.toolName.startsWith("cave_memory_"))) {
          turnStateChanged = true;
        }
      }
      queued.push(event);
      if (event.type === "agent_end") terminal = true;
      wake?.();
      wake = undefined;
    });

    const abort = () => pi.abort();
    activeAbort = abort;
    options.signal?.addEventListener("abort", abort, { once: true });
    if (options.signal?.aborted) pi.abort();
    const execution = pi.prompt(input).catch((error: unknown) => {
      terminal = true;
      wake?.();
      wake = undefined;
      throw error;
    });
    activeExecution = execution;

    while (!terminal || queued.length > 0) {
      if (queued.length === 0) {
        await new Promise<void>((resolve) => {
          wake = resolve;
        });
      }
      while (queued.length > 0) {
        yield { type: "pi", runId, event: queued.shift()! };
      }
    }
    await execution;
    if (efficiencyPlan && reasoningUsageUnavailable) {
      throw new Error("cave_reasoning_usage_unavailable");
    }
    if (ladderFailure) throw ladderFailure;
    if (spendFailure) throw spendFailure;
    if (usageFailure !== undefined &&
        (efficiencyPlan !== undefined ||
          executionContext.spendLedgers.length > 0 ||
          usageFailure.message === "cave_provider_model_identity_mismatch" ||
          usageFailure.message === "cave_provider_identity_missing")) {
      throw usageFailure;
    }
    if (pendingSpendReservations.length > 0) {
      markSpendIncomplete(executionContext.spendLedgers);
      throw new Error("cave_subagent_spend_evidence_incomplete");
    }
    // A run stopped before its first call has no assistant message, and that is
    // the honest outcome rather than missing evidence: the caller gets an empty
    // answer, zero usage, and the reason the runtime declined to spend.
    if (!finalMessage && stopReason === undefined) {
      throw new Error("cave_incomplete_evidence: Pi emitted no final assistant message");
    }
    if (finalMessage &&
        (finalMessage.stopReason === "error" || finalMessage.stopReason === "aborted")) {
      throw new Error(`cave_provider_terminal_${finalMessage.stopReason}`);
    }
    const text = finalMessage === undefined ? "" : assistantText(finalMessage);
    if (definition.output?.schema && finalMessage !== undefined) {
      let parsed: unknown;
      try {
        parsed = JSON.parse(text);
      } catch {
        throw new Error("cave_output_schema_invalid_json");
      }
      if (!Value.Check(definition.output.schema, parsed)) {
        throw new Error("cave_output_schema_mismatch");
      }
    }
    if (appliedPlan.appliedTransformIDs.length > 0 && !cacheBoundaryKnown) {
      cacheBust = true;
      markRequestPassThrough(headers, appliedPlan, "cache_boundary_unobserved");
    }
    for (const child of nestedReceipts) receipt.recordSubagent(child);
    // Built once so the result and its receipt cannot disagree about a breach.
    const runReceipt = receipt.build({
      runId,
      agentId: definition.id,
      stopReason: stopReason ?? "complete",
      meter: budgetMeter,
      ...(breakers === undefined ? {} : { breakers: breakers.recorded }),
    });
    const result: RunResult = {
      runId,
      agentId: definition.id,
      text,
      contextIR: lowered.ir,
      contextBill: contextBill(lowered.ir),
      cachePrefixSHA256: providerPrefixDigest ?? prefixDigest,
      cacheBoundaryKnown,
      cacheBust,
      usageBasis: usageFailure === undefined ? "provider_reported" : "unavailable",
      inputTokens: usageFailure === undefined ? inputTokens + nestedUsage.inputTokens : 0,
      outputTokens: usageFailure === undefined ? outputTokens + nestedUsage.outputTokens : 0,
      cacheReadTokens: usageFailure === undefined ? cacheReadTokens + nestedUsage.cacheReadTokens : 0,
      cacheWriteTokens: usageFailure === undefined ? cacheWriteTokens + nestedUsage.cacheWriteTokens : 0,
      reasoningUsageBasis: usageFailure === undefined && !reasoningUsageUnavailable
        ? "provider_reported"
        : "unavailable",
      reasoningTokens: usageFailure === undefined ? reasoningTokens + nestedUsage.reasoningTokens : 0,
      costUsd: usageFailure === undefined ? costUsd + nestedUsage.costUsd : 0,
      priceBasis: usageFailure === undefined && !unpricedCall && !nestedUsage.unpriced
        ? "public_catalog"
        : "unpriced",
      mode: gatewayActive && !nestedUsage.observeOnly ? "optimized" : "observe-only",
      provider: finalMessage?.provider ?? routedModel.provider,
      model: finalMessage?.model ?? routedModel.id,
      latencyMs: Math.round(performance.now() - startedAt),
      toolCalls,
      evaluatedTransformIDs: appliedPlan.evaluatedTransformIDs,
      transformIDs: appliedPlan.appliedTransformIDs,
      transformFailures: appliedPlan.failures,
      transformTrace: appliedPlan.trace.map(({ recoveryHandle: _handle, ...item }) => item),
      recoveryResolved: appliedPlan.recoveryResolved,
      stopReason: stopReason ?? "complete",
      capBreached: runReceipt.capBreached,
      overspent: runReceipt.overspent,
      receipt: runReceipt,
      claimBasis: "inferred",
      unlocked: buildIdentity === undefined,
    };
    if (efficiencyPlan) {
      if (result.reasoningUsageBasis !== "provider_reported") {
        throw new Error("cave_reasoning_usage_unavailable");
      }
      if (result.reasoningTokens > efficiencyPlan.budgets.reasoning) {
        throw new Error("cave_reasoning_budget_exceeded");
      }
    }
    if (efficiencyPlan) {
      enforceSemanticBudgets(result.contextBill, result.outputTokens, efficiencyPlan);
    }
    if (conversation) {
      const produced = pi.state.messages.slice(initialProviderMessages.length);
      // The refusal turn is the runtime declining to spend, not something the
      // model said. Resuming this conversation must not replay it.
      if (refusalMessage !== undefined && produced.at(-1) === refusalMessage) produced.pop();
      commitConversation(conversation, {
        messages: [
          ...originalConversationMessages,
          ...produced,
        ],
        cachePrefixDigest: providerPrefixDigest,
        providerFrozen,
        originalFrozen,
        fingerprint: conversationFingerprint,
      });
    }
    releaseConversation();
    await releaseRunResources();
    finishCurrentInvocation(true);
    yield { type: "run_end", runId, result };
  } catch (error) {
    if (pendingSpendReservations.length > 0) {
      markSpendIncomplete(executionContext.spendLedgers);
    }
    // The run is over; nothing carved from its budget may keep spending. Any
    // subagent still in flight stops between its own calls.
    budgetMeter?.revoke();
    activeAbort?.();
    if (activeExecution) await activeExecution.catch(() => undefined);
    releaseConversation();
    // Every run — budgeted or not, succeeded or failed — returns its ledger.
    // Built from the same ReceiptRecorder the success path uses, so the calls,
    // tools, and metered spend that happened before the failure survive it
    for (const child of nestedReceipts) receipt.recordSubagent(child);
    // Built defensively: a receipt-build failure must never mask
    // the original error.
    let partialReceipt: RunReceipt;
    try {
      partialReceipt = receipt.build({
        runId,
        agentId: definition.id,
        stopReason: stopReason ?? "complete",
        meter: budgetMeter,
        ...(breakers === undefined ? {} : { breakers: breakers.recorded }),
      });
    } catch {
      partialReceipt = receipt.build({
        runId,
        agentId: definition.id,
        stopReason: stopReason ?? "complete",
        meter: undefined,
      });
    }
    await releaseRunResources();
    finishCurrentInvocation(false);
    yield {
      type: "run_error",
      runId,
      code: errorCode(error),
      message: error instanceof Error ? error.message : String(error),
      receipt: partialReceipt,
    };
  } finally {
    activeAbort?.();
    if (activeExecution) await activeExecution.catch(() => undefined);
    if (activeAbort) options.signal?.removeEventListener("abort", activeAbort);
    releaseConversation();
    // Idempotent backstop for the abort/return() paths that never reached a
    // terminal yield; the success and error paths already released above.
    await releaseRunResources();
  }
}

async function stageSandboxSourceGraph(
  root: string,
  entryPath: string,
  frameworkDistRoot: string,
): Promise<SandboxSourceSnapshot> {
  const canonicalRoot = await realpath(root);
  const canonicalEntry = await realpath(entryPath);
  const entryName = relative(canonicalRoot, canonicalEntry);
  if (escapesRoot(entryName)) {
    throw new Error("cave_tool_sandbox_entry_escapes_root");
  }
  const canonicalFrameworkDist = await realpath(frameworkDistRoot);
  const graph = [...await expandSourceGraph(
    canonicalRoot,
    [canonicalEntry],
    false,
    [canonicalFrameworkDist],
  )].sort();
  const staging = await realpath(await mkdtemp(resolve(tmpdir(), "caveman-agent-source-")));
  try {
    const stagedFiles: string[] = [];
    await copyOptionalSandboxFile(
      resolve(canonicalRoot, "package.json"),
      resolve(staging, "package.json"),
      stagedFiles,
    );
    for (const source of graph) {
      if (!escapesRoot(relative(canonicalFrameworkDist, source))) {
        continue;
      }
      const name = relative(canonicalRoot, source);
      if (escapesRoot(name)) throw new Error("cave_tool_sandbox_source_escapes_root");
      const destination = resolve(staging, name);
      await mkdir(dirname(destination), { recursive: true });
      await copyFile(source, destination);
      stagedFiles.push(destination);
    }
    const frameworkName = relative(canonicalRoot, canonicalFrameworkDist);
    if (!escapesRoot(frameworkName)) {
      const frameworkDestination = resolve(staging, frameworkName);
      await mkdir(dirname(frameworkDestination), { recursive: true });
      await symlink(
        canonicalFrameworkDist,
        frameworkDestination,
        process.platform === "win32" ? "junction" : "dir",
      );
      stagedFiles.push(frameworkDestination);
    }
    const nodeModules = await realpath(resolve(canonicalRoot, "node_modules"));
    await symlink(
      nodeModules,
      resolve(staging, "node_modules"),
      process.platform === "win32" ? "junction" : "dir",
    );
    stagedFiles.push(resolve(staging, "node_modules"));
    let disposed = false;
    return {
      stagingRoot: staging,
      entryPath: resolve(staging, entryName),
      sourceFiles: Object.freeze(stagedFiles),
      async dispose() {
        if (disposed) return;
        disposed = true;
        await rm(staging, { recursive: true, force: true });
      },
    };
  } catch (error) {
    await rm(staging, { recursive: true, force: true });
    throw error;
  }
}

async function copyOptionalSandboxFile(
  source: string,
  destination: string,
  stagedFiles: string[],
): Promise<void> {
  try {
    await readFile(source);
    await mkdir(dirname(destination), { recursive: true });
    await copyFile(source, destination);
    stagedFiles.push(destination);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  }
}

function escapesRoot(path: string): boolean {
  return path === ".." || path.startsWith("../") || path.startsWith("..\\") || isAbsolute(path);
}

type ConversationTextSegment = {
  id: string;
  kind: "history" | "tool_result";
  messageIndex: number;
  contentIndex?: number;
  text: string;
};

/**
 * Content-derived runtime segment id. Naming a live-zone segment
 * by a digest of its text — not its position — means the same message always
 * gets the same id and, crucially, a different message never inherits another
 * message's id after compaction shifts the indices. The 16-hex prefix is ample
 * for per-run uniqueness; `appendRuntimeContextSegment` still fails closed on a
 * same-id/different-bytes collision.
 */
function runtimeSegmentId(kind: "history" | "tool_result", text: string): string {
  return `runtime.${kind}.${sha256(text).slice(0, 16)}`;
}

function conversationTextSegments(
  messages: readonly AgentMessage[],
  currentPromptIndex = -1,
): ConversationTextSegment[] {
  const segments: ConversationTextSegment[] = [];
  for (let messageIndex = 0; messageIndex < messages.length; messageIndex++) {
    if (messageIndex === currentPromptIndex) continue;
    const message = messages[messageIndex];
    if (!isRecord(message) ||
        !["user", "assistant", "toolResult"].includes(String(message.role))) {
      continue;
    }
    const kind = message.role === "toolResult" ? "tool_result" : "history";
    if (typeof message.content === "string") {
      segments.push({
        // Content-derived identity: a segment is named by the
        // bytes it carries, never by its position. Compaction rewrites the
        // message list and shifts every index, so a positional id would let a
        // NEW message inherit a PRIOR turn's message's compressed body. The
        // messageIndex/contentIndex below still locate the message to
        // substitute; they no longer decide identity.
        id: runtimeSegmentId(kind, message.content),
        kind,
        messageIndex,
        text: message.content,
      });
      continue;
    }
    if (!Array.isArray(message.content)) continue;
    for (let contentIndex = 0; contentIndex < message.content.length; contentIndex++) {
      const content = message.content[contentIndex];
      if (!isRecord(content) || content.type !== "text" || typeof content.text !== "string") continue;
      segments.push({
        id: runtimeSegmentId(kind, content.text),
        kind,
        messageIndex,
        contentIndex,
        text: content.text,
      });
    }
  }
  return segments;
}

function replaceConversationSegments(
  messages: readonly AgentMessage[],
  segments: readonly ConversationTextSegment[],
  lowered: LoweredContext,
  applied: AppliedPlan,
  originals: Map<string, string>,
): AgentMessage[] {
  const output = structuredClone(messages) as AgentMessage[];
  for (const descriptor of segments) {
    const segment = lowered.ir.segments.find((item) => item.id === descriptor.id);
    if (!segment) continue;
    const providerBody = applied.bodies.get(segment.bodyHandle);
    if (!providerBody) continue;
    const replacement = new TextDecoder().decode(providerBody);
    if (replacement === descriptor.text) continue;
    setConversationText(output, descriptor, replacement);
    originals.set(replacement, descriptor.text);
  }
  return output;
}

function setConversationText(
  messages: AgentMessage[],
  descriptor: ConversationTextSegment,
  replacement: string,
): void {
  const message = messages[descriptor.messageIndex] as unknown as {
    content: string | Array<Record<string, unknown>>;
  } | undefined;
  if (!message) return;
  if (descriptor.contentIndex === undefined) {
    message.content = replacement;
    return;
  }
  if (!Array.isArray(message.content)) return;
  const content = message.content[descriptor.contentIndex];
  if (content?.type === "text") content.text = replacement;
}

async function transformConversationContext(
  messages: AgentMessage[],
  currentPromptIndex: number,
  lowered: LoweredContext,
  plan: CavePlan | undefined,
  engineBin: string | undefined,
  applied: AppliedPlan,
  originals: Map<string, string>,
  signal?: AbortSignal,
): Promise<AgentMessage[]> {
  if (!plan) return messages;
  const descriptors = conversationTextSegments(messages, currentPromptIndex);
  const output = structuredClone(messages) as AgentMessage[];
  for (const descriptor of descriptors) {
    if (descriptor.text.startsWith("<cave-compressed ")) continue;
    const routes = plan.segment_routes.filter((route) =>
      route.segment_kind === descriptor.kind &&
      (route.segment_id === undefined || route.segment_id === descriptor.id));
    if (routes.length === 0) continue;
    if (routes.length > 1) {
      const failure = `dynamic_route_ambiguous:${descriptor.id}`;
      if (!applied.failures.includes(failure)) applied.failures.push(failure);
      continue;
    }
    const body = new TextEncoder().encode(descriptor.text);
    const segment = appendRuntimeContextSegment(lowered, {
      id: descriptor.id,
      kind: descriptor.kind,
      body,
    });
    const existing = applied.bodies.get(segment.bodyHandle);
    if (existing && !bytesEqual(existing, body)) {
      const replacement = new TextDecoder().decode(existing);
      setConversationText(output, descriptor, replacement);
      originals.set(replacement, descriptor.text);
      continue;
    }
    const dynamic = await applyEfficiencyPlan({
      ir: { schemaVersion: 1, segments: [segment] },
      bodies: new Map([[segment.bodyHandle, body]]),
    }, {
      ...plan,
      segment_routes: [routes[0]!],
    }, engineBin, signal);
    mergeAppliedPlan(applied, dynamic, plan);
    const replacementBody = dynamic.bodies.get(segment.bodyHandle);
    if (!replacementBody || bytesEqual(replacementBody, body)) continue;
    const replacement = new TextDecoder().decode(replacementBody);
    applied.bodies.set(segment.bodyHandle, replacementBody);
    setConversationText(output, descriptor, replacement);
    originals.set(replacement, descriptor.text);
  }
  return output;
}

function mergeAppliedPlan(target: AppliedPlan, source: AppliedPlan, plan: CavePlan): void {
  for (const [handle, body] of source.bodies) target.bodies.set(handle, body);
  for (const id of source.evaluatedTransformIDs) {
    if (!target.evaluatedTransformIDs.includes(id)) target.evaluatedTransformIDs.push(id);
  }
  for (const id of source.appliedTransformIDs) {
    if (!target.appliedTransformIDs.includes(id)) target.appliedTransformIDs.push(id);
  }
  for (const failure of source.failures) {
    if (!target.failures.includes(failure)) target.failures.push(failure);
  }
  target.trace.push(...source.trace);
  target.evaluatedTransformIDs = orderedTransformIDs(plan, target.evaluatedTransformIDs);
  target.appliedTransformIDs = orderedTransformIDs(plan, target.appliedTransformIDs);
  const routeOrder = new Map(plan.segment_routes.map((route, index) => [
    `${route.segment_kind}\u0000${route.transform_id}`,
    index,
  ]));
  target.trace.sort((left, right) =>
    (routeOrder.get(`${left.segmentKind}\u0000${left.transformID}`) ?? Number.MAX_SAFE_INTEGER) -
    (routeOrder.get(`${right.segmentKind}\u0000${right.transformID}`) ?? Number.MAX_SAFE_INTEGER));
  target.recoveryResolved = target.recoveryResolved && source.recoveryResolved;
  for (const handle of source.recoveryHandles) target.recoveryHandles.add(handle);
}

function orderedTransformIDs(plan: CavePlan, ids: readonly string[]): string[] {
  const remaining = new Map<string, number>();
  for (const id of ids) remaining.set(id, (remaining.get(id) ?? 0) + 1);
  const ordered: string[] = [];
  for (const route of plan.segment_routes) {
    const count = remaining.get(route.transform_id) ?? 0;
    if (count > 0) {
      ordered.push(route.transform_id);
      if (count === 1) remaining.delete(route.transform_id);
      else remaining.set(route.transform_id, count - 1);
    }
  }
  for (const id of ids) {
    const count = remaining.get(id) ?? 0;
    if (count > 0) {
      ordered.push(id);
      if (count === 1) remaining.delete(id);
      else remaining.set(id, count - 1);
    }
  }
  return ordered;
}

export function enforceSemanticBudgets(
  bill: Readonly<Record<string, number>>,
  outputMaxTokens: number,
  plan: CavePlan,
): void {
  const slots: Array<[keyof CavePlan["budgets"], number]> = [
    ["instructions", bill.instruction ?? 0],
    ["tools", bill.tool_schema ?? 0],
    ["memory", bill.memory ?? 0],
    ["history", bill.history ?? 0],
    ["results_artifacts", (bill.artifact ?? 0) + (bill.skill ?? 0) + (bill.tool_result ?? 0)],
    ["output", outputMaxTokens],
  ];
  for (const [slot, used] of slots) {
    if (used > plan.budgets[slot]) throw new Error(`cave_${slot}_budget_exceeded`);
  }
}

export function assembleSystemPrompt(
  definition: AgentDefinition,
  lowered: LoweredContext,
  bodies: ReadonlyMap<string, Uint8Array> = lowered.bodies,
): string {
  const decoder = new TextDecoder();
  const required = ["agent.instructions", ...definition.contexts.map((item) => item.id)];
  const parts: string[] = [];
  for (const id of required) {
    const segment = lowered.ir.segments.find((item) => item.id === id);
    if (!segment) throw new Error(`cave_context_segment_missing:${id}`);
    const body = bodies.get(segment.bodyHandle);
    if (!body) throw new Error(`cave_context_body_missing:${id}`);
    const text = decoder.decode(body);
    parts.push(id === "agent.instructions" ? text : `<cave-context id=${JSON.stringify(id)}>\n${text}\n</cave-context>`);
  }
  if (definition.output) {
    parts.push(`<cave-output max_tokens=${definition.output.maxTokens}>Return output matching declared schema when present.</cave-output>`);
  }
  if (definition.memory) {
    parts.push("<cave-memory>Use cave_memory_search before relying on prior-session facts. Use cave_memory_remember only for durable facts the user intended to retain.</cave-memory>");
  }
  return parts.join("\n\n");
}

async function applyEfficiencyPlan(
  lowered: LoweredContext,
  plan: CavePlan | undefined,
  engineBin: string | undefined,
  signal?: AbortSignal,
): Promise<AppliedPlan> {
  const bodies = new Map(lowered.bodies);
  if (!plan || plan.segment_routes.length === 0) {
    return {
      bodies,
      evaluatedTransformIDs: [],
      appliedTransformIDs: [],
      failures: [],
      recoveryResolved: true,
      recoveryHandles: new Set(),
      trace: [],
    };
  }
  const known = new Set([
    "a11y", "code", "config", "diff", "html", "json", "log", "repetition",
    "search-result", "tabular", "terminal", "text", "toolschema", "toon",
  ].map((name) => `caveman.engine.${name}.v1`));
  const evaluated: string[] = [];
  const applied: string[] = [];
  const failures: string[] = [];
  const handles = new Set<string>();
  const trace: MutableTransformTrace[] = [];
  let recoveryResolved = true;
  for (const route of plan.segment_routes) {
    if (!known.has(route.transform_id)) {
      throw new Error(`cave_unknown_transform:${route.transform_id}`);
    }
    const targets = lowered.ir.segments.filter((segment) =>
      segment.kind === route.segment_kind &&
      (route.segment_id === undefined || segment.id === route.segment_id));
    if (targets.length === 0) {
      if (route.segment_kind === "history" || route.segment_kind === "tool_result") {
        continue;
      }
      throw new Error(`cave_plan_route_unmatched:${route.segment_id ?? route.segment_kind}`);
    }
    evaluated.push(route.transform_id);
    let routeApplied = false;
    for (const segment of targets) {
      const original = lowered.bodies.get(segment.bodyHandle);
      if (!original) throw new Error(`cave_context_body_missing:${segment.id}`);
      if (segment.safety !== "S4") throw new Error(`cave_transform_safety_mismatch:${segment.id}`);
      const startedAt = performance.now();
      // beforeTokens/afterTokens are byte-derived (bytes/4) throughout so a
      // delta is always within one basis. beforeTokens counts the ORIGINAL
      // bytes; afterTokens, for an applied transform, counts the FULL provider
      // body actually sent — wrapper included. segment.tokenCount
      // is already estimateTokens(original) = bytes/4.
      const beforeTokens = segment.tokenCount;
      if (segment.opaque) {
        trace.push({
          segmentKind: segment.kind,
          transformID: route.transform_id,
          safetyClass: segment.safety,
          beforeTokens,
          afterTokens: beforeTokens,
          tokensBasis: "byte_derived",
          recoveryKind: segment.recovery,
          recoveryUsed: false,
          latencyMs: Math.round(performance.now() - startedAt),
          outcome: "not_smaller",
        });
        continue;
      }
      try {
        const transformed = await engineCompress(original, engineBin, engineContentType(route.transform_id), signal);
        if (!transformed.handle) {
          trace.push({
            segmentKind: segment.kind,
            transformID: route.transform_id,
            safetyClass: segment.safety,
            beforeTokens,
            afterTokens: beforeTokens,
            tokensBasis: "byte_derived",
            recoveryKind: segment.recovery,
            recoveryUsed: false,
            latencyMs: Math.round(performance.now()-startedAt),
            outcome: "not_smaller",
          });
          continue;
        }
        const recovered = await engineRetrieve(transformed.handle, undefined, engineBin, signal);
        if (!bytesEqual(recovered, original)) {
          recoveryResolved = false;
          failures.push(`${route.transform_id}:recovery_mismatch:${segment.id}`);
          trace.push({
            segmentKind: segment.kind,
            transformID: route.transform_id,
            safetyClass: segment.safety,
            beforeTokens,
            afterTokens: beforeTokens,
            tokensBasis: "byte_derived",
            recoveryKind: segment.recovery,
            recoveryUsed: false,
            latencyMs: Math.round(performance.now()-startedAt),
            outcome: "failed_open",
          });
          continue;
        }
        const providerBody = segment.kind === "tool_schema"
          ? validatedToolSchemaBytes(transformed.output, segment.id)
          : new TextEncoder().encode([
            `<cave-compressed transform=${JSON.stringify(route.transform_id)} recovery_handle=${JSON.stringify(transformed.handle)}>`,
            new TextDecoder().decode(transformed.output),
            "Use cave_retrieve for omitted detail.",
            "</cave-compressed>",
          ].join("\n"));
        bodies.set(segment.bodyHandle, providerBody);
        handles.add(transformed.handle);
        routeApplied = true;
        trace.push({
          segmentKind: segment.kind,
          transformID: route.transform_id,
          safetyClass: segment.safety,
          beforeTokens,
          // The bytes that actually go on the wire, wrapper and instruction line
          // included — never the engine's compressed-payload-only count.
          afterTokens: Math.ceil(providerBody.byteLength / 4),
          tokensBasis: "byte_derived",
          recoveryKind: segment.recovery,
          recoveryUsed: false,
          latencyMs: Math.round(performance.now()-startedAt),
          outcome: "applied",
          recoveryHandle: transformed.handle,
        });
      } catch (error) {
        failures.push(`${route.transform_id}:transform_error:${segment.id}:${safeEngineError(error)}`);
        trace.push({
          segmentKind: segment.kind,
          transformID: route.transform_id,
          safetyClass: segment.safety,
          beforeTokens,
          afterTokens: beforeTokens,
          tokensBasis: "byte_derived",
          recoveryKind: segment.recovery,
          recoveryUsed: false,
          latencyMs: Math.round(performance.now()-startedAt),
          outcome: "failed_open",
        });
      }
    }
    if (routeApplied) applied.push(route.transform_id);
  }
  return {
    bodies,
    evaluatedTransformIDs: orderedTransformIDs(plan, evaluated),
    appliedTransformIDs: orderedTransformIDs(plan, applied),
    failures,
    recoveryResolved,
    recoveryHandles: handles,
    trace,
  };
}

async function engineCompress(
  input: Uint8Array,
  engineBin: string | undefined,
  contentType = "text",
  signal?: AbortSignal,
): Promise<{ output: Uint8Array; handle?: string; tokensBefore?: number; tokensAfter?: number }> {
  const result = await runEngine(engineBin, ["compress", "--type", contentType], input, signal);
  const lastLine = result.stderr.trim().split("\n").pop() ?? "{}";
  const report = JSON.parse(lastLine) as {
    recovery_handle?: unknown;
    tokens_before?: unknown;
    tokens_after?: unknown;
  };
  const handle = typeof report.recovery_handle === "string" && report.recovery_handle.length > 0
    ? report.recovery_handle
    : undefined;
  if (!handle && !bytesEqual(result.stdout, input)) {
    throw new Error("engine_changed_bytes_without_recovery");
  }
  const tokensBefore = validEngineTokenCount(report.tokens_before);
  const tokensAfter = validEngineTokenCount(report.tokens_after);
  return {
    output: result.stdout,
    ...(handle === undefined ? {} : { handle }),
    ...(tokensBefore === undefined ? {} : { tokensBefore }),
    ...(tokensAfter === undefined ? {} : { tokensAfter }),
  };
}

function engineContentType(transformID: string): string {
  const match = /^caveman\.engine\.([a-z0-9-]+)\.v1$/.exec(transformID);
  if (!match) throw new Error(`cave_unknown_transform:${transformID}`);
  return match[1]!;
}


export interface SegmentRecoveryProof {
  transformID: string;
  /**
   * `recovered` = the engine returned bytes identical to the original.
   * `not_smaller` = the engine declined to compress, so there is nothing to
   * recover and the original bytes are what the model sees.
   * `mismatch` = recovery did not reproduce the original; the plan fails open
   * to the original body and nothing may be claimed for this segment.
   */
  outcome: "recovered" | "not_smaller" | "mismatch";
  handle?: string;
  originalSHA256: string;
  recoveredSHA256?: string;
  originalBytes: number;
  compressedBytes?: number;
  tokensBefore?: number;
  tokensAfter?: number;
}

/**
 * Compress one segment body and recover it through exactly the engine pair that
 * plan application and the `cave_retrieve` tool use, then compare bytes. This
 * checks reversibility and nothing else: it measures no spend and makes no
 * savings claim. Token counts it reports are local engine estimates.
 */
export async function proveSegmentRecovery(input: {
  body: Uint8Array;
  transformID: string;
  engineBin?: string;
}): Promise<SegmentRecoveryProof> {
  const originalSHA256 = sha256(input.body);
  const transformed = await engineCompress(
    input.body,
    input.engineBin,
    engineContentType(input.transformID),
  );
  const tokens = {
    ...(transformed.tokensBefore === undefined ? {} : { tokensBefore: transformed.tokensBefore }),
    ...(transformed.tokensAfter === undefined ? {} : { tokensAfter: transformed.tokensAfter }),
  };
  if (!transformed.handle) {
    return {
      transformID: input.transformID,
      outcome: "not_smaller",
      originalSHA256,
      originalBytes: input.body.byteLength,
      ...tokens,
    };
  }
  const recovered = await engineRetrieve(transformed.handle, undefined, input.engineBin);
  return {
    transformID: input.transformID,
    outcome: bytesEqual(recovered, input.body) ? "recovered" : "mismatch",
    handle: transformed.handle,
    originalSHA256,
    recoveredSHA256: sha256(recovered),
    originalBytes: input.body.byteLength,
    compressedBytes: transformed.output.byteLength,
    ...tokens,
  };
}

async function engineRetrieve(
  handle: string,
  query: string | undefined,
  engineBin: string | undefined,
  signal?: AbortSignal,
): Promise<Uint8Array> {
  const result = await runEngine(engineBin, [
    "retrieve",
    handle,
    ...(query === undefined || query === "" ? [] : [query]),
  ], undefined, signal);
  return result.stdout;
}

/** Default wall-clock ceiling for one engine subprocess. */
const ENGINE_TIMEOUT_MS = 30_000;

/**
 * The allow-listed environment the engine subprocess starts from.
 *
 * A compression engine never needs provider or account credentials, so the
 * parent's full environment — ANTHROPIC_API_KEY, CAVE_API_KEY, AWS_*, … — is
 * withheld. Namespace prefixes are not safe allowlists: Caveman also uses
 * `CAVEMAN_API_KEY`, `CAVEMAN_MCP_TOKEN`, and session-key variables. Pass only
 * exact configuration names read by Engine (plus test fixture store var).
 */
export function buildEngineEnv(): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {
    LANG: process.env.LANG ?? "C",
    LC_ALL: process.env.LC_ALL ?? "C",
    PATH: process.env.PATH ?? "",
    HOME: process.env.HOME ?? "",
    TZ: process.env.TZ ?? "UTC",
  };
  for (const key of [
    // Portable process-launch baseline; none carries account/provider secrets.
    "ComSpec", "PATHEXT", "SystemRoot", "TEMP", "TMP", "TMPDIR", "USERPROFILE",
    "CAVEMAN_CCR_DB",
    "CAVEMAN_HOME",
    "CAVE_ENGINE_TOON",
    "CAVE_PIXEL_DENSITY",
    "CAVE_PIXEL_GPT_PROFILES",
    "CAVE_PIXEL_MODELS",
    "CAVE_FAKE_ENGINE_STORE",
  ]) {
    const value = process.env[key];
    if (value !== undefined) env[key] = value;
  }
  return env;
}

/** Environment for zero-payload runtime startup and ownership probes.
 * These subprocesses never need provider, account, deployment, or session
 * credentials. Their binaries may resolve from a package-script PATH, so a
 * spread of process.env would turn PATH shadowing into secret exfiltration. */
export function buildRuntimeControlEnv(): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {
    LANG: process.env.LANG ?? "C",
    LC_ALL: process.env.LC_ALL ?? "C",
    PATH: process.env.PATH ?? "",
    HOME: process.env.HOME ?? "",
    TZ: process.env.TZ ?? "UTC",
  };
  for (const key of [
    // Portable process-launch baseline.
    "ComSpec", "PATHEXT", "SystemRoot", "TEMP", "TMP", "TMPDIR", "USERPROFILE",
    // Exact local-runtime configuration. No credentials or session keys.
    "CAVEMAN_CCR_DB", "CAVEMAN_CONFIG", "CAVEMAN_HOME", "CAVEMAN_MCP",
    "CAVEMAN_MODE", "CAVEMAN_OFFLINE", "CAVEMAN_PLAIN", "CAVEMAN_PROXY_BIN",
    "CAVEMAN_RECOVERY", "CAVEMAN_SHRINK", "CAVEMAN_TELEMETRY", "CAVEMAN_TOON",
    "CAVEMAN_WRAP_MODE", "CAVE_ENGINE_TOON", "CAVE_GATEWAY_URL",
    "CAVE_PIXEL_DENSITY", "CAVE_PIXEL_GPT_PROFILES", "CAVE_PIXEL_MODELS",
    "CI", "DO_NOT_TRACK", "NO_COLOR", "TERM",
  ]) {
    const value = process.env[key];
    if (value !== undefined) env[key] = value;
  }
  return env;
}

async function runEngine(
  engineBin: string | undefined,
  args: string[],
  input?: Uint8Array,
  signal?: AbortSignal,
): Promise<{ stdout: Uint8Array; stderr: string }> {
  const command = engineBin ?? process.env.CAVEMAN_ENGINE_BIN ?? "caveman-engine";
  // A hung engine must not run forever: a default timeout plus the caller's own
  // run/tool signal both terminate it, and the child is spawned detached so the
  // whole process group is killed, not just the direct child.
  const timeout = AbortSignal.timeout(ENGINE_TIMEOUT_MS);
  const combined = signal ? AbortSignal.any([signal, timeout]) : timeout;
  return new Promise((accept, reject) => {
    const env = buildEngineEnv();
    const invocation = portableInvocation(command, args, { env });
    const child = spawn(invocation.command, [...invocation.args], {
      env,
      detached: process.platform !== "win32",
      stdio: ["pipe", "pipe", "pipe"],
    });
    const stdout: Buffer[] = [];
    const stderr: Buffer[] = [];
    let outputBytes = 0;
    let settled = false;
    let killTimer: NodeJS.Timeout | undefined;
    let terminalError: Error | undefined;
    const cleanup = (): void => {
      combined.removeEventListener("abort", onAbort);
      if (killTimer !== undefined) clearTimeout(killTimer);
    };
    const terminate = (error: Error, immediate = false): void => {
      terminalError ??= error;
      killSandboxProcess(child, immediate ? "SIGKILL" : "SIGTERM");
      if (!immediate && killTimer === undefined) {
        killTimer = setTimeout(() => killSandboxProcess(child, "SIGKILL"), 250);
        killTimer.unref();
      }
    };
    function onAbort(): void {
      const reason = combined.reason;
      const isTimeout = reason instanceof DOMException && reason.name === "TimeoutError";
      terminate(isTimeout
        ? new Error("cave_engine_timeout")
        : (reason instanceof Error ? reason : new Error("cave_engine_aborted")));
    }
    const collect = (target: Buffer[], chunk: Buffer): void => {
      outputBytes += chunk.byteLength;
      if (outputBytes > 16 * 1024 * 1024) {
        terminate(new Error("engine_output_limit"), true);
        return;
      }
      target.push(chunk);
    };
    child.stdout.on("data", (chunk: Buffer) => collect(stdout, chunk));
    child.stderr.on("data", (chunk: Buffer) => collect(stderr, chunk));
    child.once("error", (error) => {
      if (settled) return;
      settled = true;
      cleanup();
      reject(error);
    });
    child.once("close", (code) => {
      if (settled) return;
      settled = true;
      cleanup();
      if (terminalError !== undefined) {
        reject(terminalError);
        return;
      }
      if (code !== 0) {
        reject(new Error(`engine_exit_${code}:${Buffer.concat(stderr).toString("utf8").slice(0, 256)}`));
        return;
      }
      accept({
        stdout: new Uint8Array(Buffer.concat(stdout)),
        stderr: Buffer.concat(stderr).toString("utf8"),
      });
    });
    if (combined.aborted) onAbort();
    else combined.addEventListener("abort", onAbort, { once: true });
    // A child that died before draining stdin makes end() emit EPIPE; swallow it
    // so the real terminal error (timeout/exit) is what surfaces.
    child.stdin.on("error", () => undefined);
    child.stdin.end(input);
  });
}

function recoveryTool(
  handles: ReadonlySet<string>,
  engineBin: string | undefined,
  trace?: MutableTransformTrace[],
): AgentTool<TSchema> {
  return {
    name: "cave_retrieve",
    label: "cave_retrieve",
    description: "Recover exact content omitted by an active Caveman transform. Handles are scoped to this run.",
    parameters: Type.Object({
      recovery_handle: Type.String(),
      query: Type.Optional(Type.String()),
    }),
    executionMode: "parallel",
    async execute(_toolCallId, params) {
      const input = params as { recovery_handle: string; query?: string };
      if (!handles.has(input.recovery_handle)) throw new Error("cave_recovery_handle_out_of_scope");
      const value = await engineRetrieve(input.recovery_handle, input.query, engineBin);
      for (const item of trace ?? []) {
        if (item.recoveryHandle === input.recovery_handle) item.recoveryUsed = true;
      }
      return {
        content: [{ type: "text", text: new TextDecoder().decode(value) }],
        details: { recovery: input.query ? "query" : "exact" },
      };
    },
  };
}

function toolSchemaSearchTool(
  handles: ReadonlySet<string>,
  engineBin: string | undefined,
  trace?: MutableTransformTrace[],
): AgentTool<TSchema> {
  return {
    name: "cave_search_tools",
    label: "cave_search_tools",
    description: "Search exact original tool definitions when compacted annotations are insufficient.",
    parameters: Type.Object({ query: Type.String() }),
    executionMode: "parallel",
    async execute(_toolCallId, params) {
      const query = (params as { query: string }).query.trim().toLowerCase();
      if (query === "") throw new Error("cave_tool_search_query_required");
      const terms = tokenize(query);
      const hits: Array<{ score: number; value: Record<string, unknown>; handle: string }> = [];
      for (const handle of handles) {
        const original = await engineRetrieve(handle, undefined, engineBin);
        let parsed: unknown;
        try {
          parsed = JSON.parse(new TextDecoder().decode(original));
        } catch {
          continue;
        }
        if (!isRecord(parsed) || typeof parsed.name !== "string" ||
            typeof parsed.description !== "string" || !isRecord(parsed.input)) continue;
        const haystack = `${parsed.name} ${parsed.description} ${JSON.stringify(parsed.input)}`.toLowerCase();
        const score = terms.reduce((total, term) => total + (haystack.includes(term) ? 1 : 0), 0);
        if (score > 0) hits.push({ score, value: parsed, handle });
      }
      hits.sort((first, second) => second.score - first.score ||
        String(first.value.name).localeCompare(String(second.value.name)));
      const selected = hits.slice(0, 4);
      for (const hit of selected) {
        for (const item of trace ?? []) {
          if (item.recoveryHandle === hit.handle) item.recoveryUsed = true;
        }
      }
      const text = JSON.stringify(selected.map((hit) => hit.value));
      if (text.length > 8_192) throw new Error("cave_tool_search_result_limit");
      return {
        content: [{ type: "text", text }],
        details: { recovery: "tool_schema_search", hits: selected.length },
      };
    },
  };
}

function validEngineTokenCount(value: unknown): number | undefined {
  return Number.isSafeInteger(value) && Number(value) >= 0 ? Number(value) : undefined;
}

function localMemoryTools(
  definition: NonNullable<AgentDefinition["memory"]>,
  agentId: string,
  config: MemoryStoreConfig | undefined,
): AgentTool<TSchema>[] {
  // Defense in depth: memory() already rejects a non-local provenance/consent at
  // construction, but a hand-built MemoryDefinition must still fail
  // BEFORE the run spends — not inside a tool call — so this throws at tool
  // setup, never at execute time.
  if (definition.provenance !== "local" || definition.consent !== "local_only") {
    throw new Error("cave_memory_shared_backend_unavailable");
  }
  // Resolved once so a validation failure surfaces at setup, and every entry is
  // scoped by (tenant, agentId, namespace): two AgentDefinitions declaring the
  // same namespace in one process write to different files and never bleed.
  const filePath = memoryFilePath(config, agentId, definition.namespace);
  const ttl = memoryTTLMilliseconds(definition.ttl);
  const recall: AgentTool<TSchema> = {
    name: "cave_memory_search",
    label: "cave_memory_search",
    description: "Recall relevant local memory from declared namespace. Returns bounded local evidence only.",
    parameters: Type.Object({ query: Type.String() }),
    executionMode: "parallel",
    async execute(_toolCallId, params) {
      const query = (params as { query: string }).query;
      const now = Date.now();
      // Eviction on read: a memory past its declared ttl is neither returned nor
      // kept — the durable set is rewritten with only the live entries.
      const live = await mutateMemories(filePath, (entries) =>
        entries.filter((entry) => now - entry.createdAt <= ttl));
      const terms = tokenize(query);
      const ranked = live
        .map((entry) => ({
          entry,
          score: terms.reduce((score, term) => score + tokenize(entry.text).filter((value) => value === term).length, 0),
        }))
        .filter((item) => item.score > 0)
        .sort((first, second) => second.score-first.score || second.entry.createdAt-first.entry.createdAt);
      const maxChars = definition.recallBudget * 4;
      const hits: string[] = [];
      let used = 0;
      for (const item of ranked) {
        if (used + item.entry.text.length > maxChars) continue;
        hits.push(item.entry.text);
        used += item.entry.text.length;
      }
      return {
        content: [{ type: "text", text: JSON.stringify({ hits, basis: "inferred", namespace: definition.namespace }) }],
        details: { namespace: definition.namespace, recallTokensEstimated: Math.ceil(used / 4) },
      };
    },
  };
  const remember: AgentTool<TSchema> = {
    name: "cave_memory_remember",
    label: "cave_memory_remember",
    description: "Remember one explicit fact in declared local namespace.",
    parameters: Type.Object({ text: Type.String() }),
    executionMode: "sequential",
    async execute(_toolCallId, params) {
      const text = (params as { text: string }).text.trim();
      if (text.length === 0 || text.length > 8_192) throw new Error("cave_memory_value_invalid");
      await mutateMemories(filePath, (entries) => {
        const next: MemoryEntry[] = entries.some((entry) => entry.text === text)
          ? entries
          : [...entries, { text, createdAt: Date.now() }];
        return next.length > 1_000 ? next.slice(next.length - 1_000) : next;
      });
      return {
        content: [{ type: "text", text: JSON.stringify({ remembered: true, basis: "inferred" }) }],
        details: { namespace: definition.namespace },
      };
    },
  };
  return [recall, remember];
}

function tokenize(value: string): string[] {
  return value.toLowerCase().match(/[a-z0-9]{2,}/g) ?? [];
}

function bytesEqual(first: Uint8Array, second: Uint8Array): boolean {
  return first.byteLength === second.byteLength && first.every((value, index) => value === second[index]);
}

function safeEngineError(error: unknown): string {
  return (error instanceof Error ? error.message : String(error))
    .replace(/[A-Za-z0-9_=-]{24,}/g, "<redacted>")
    .slice(0, 160);
}

/**
 * Which billing regime pays for this provider's configured credential, as a
 * tri-state:
 *
 * - `"subscription"` — Pi positively identified an unbilled OAuth session
 *   (e.g. Claude Pro/Max). Dollars are fiction here.
 * - `"metered"` — Pi positively identified a per-token API key.
 * - `"unknown"` — Pi has no `checkAuth`, or it threw. The regime cannot be
 *   proven, so it is NOT silently treated as metered: a caller that meant to
 *   meter dollars against an API key must say so with
 *   `RunOptions.assumeMeteredCredential`. Guessing "metered" is exactly the
 *   fail-open that would book fictional dollars against a subscription.
 *
 * Pi reports the credential type without refreshing an OAuth token, so this
 * costs nothing and touches no network. Callers decide whether Pi's credential
 * store is even the right authority for the run in hand — it is not, for a
 * caller-supplied transport or a gateway-routed request.
 */
async function credentialRegime(
  models: Models,
  provider: string,
): Promise<"subscription" | "metered" | "unknown"> {
  if (typeof models.checkAuth === "function") {
    try {
      const check = await models.checkAuth(provider);
      if (check?.type === "oauth") return "subscription";
      if (check?.type === "api_key") return "metered";
    } catch {
      // Alias fallback below still proves local Google API-key billing.
    }
  }
  // Runtime accepts GOOGLE_API_KEY as alias for Pi's GEMINI_API_KEY-only
  // Google provider. Use only after Pi failed to prove a stronger regime, so a
  // future OAuth-capable Google provider cannot be relabeled as metered.
  if (provider === "google" && process.env.GOOGLE_API_KEY) return "metered";
  return "unknown";
}

function resolveModel(definition: AgentDefinition, models: Models, rootDir: string): Model<Api> {
  if (typeof definition.model === "object" && "provider" in definition.model) {
    return definition.model;
  }
  const requested = typeof definition.model === "string"
    ? definition.model
    : process.env.CAVE_MODEL ?? localProviderModel(rootDir);
  if (requested) {
    const slash = requested.indexOf("/");
    if (slash <= 0 || slash === requested.length - 1) {
      throw new Error("caveman agent: pinned model must use provider/model format");
    }
    const model = models.getModel(requested.slice(0, slash), requested.slice(slash + 1));
    if (!model) throw new Error(`caveman agent: unknown model ${JSON.stringify(requested)}`);
    return model;
  }

  const configured: Array<[string, string]> = [];
  if (process.env.ANTHROPIC_API_KEY) configured.push(["anthropic", "claude-haiku-4-5"]);
  if (process.env.OPENAI_API_KEY) configured.push(["openai", "gpt-5.4-mini"]);
  if (process.env.GEMINI_API_KEY || process.env.GOOGLE_API_KEY) configured.push(["google", "gemini-2.5-flash"]);
  if (configured.length !== 1) {
    throw new Error(
      configured.length === 0
        ? "caveman agent: no supported provider credential found"
        : "caveman agent: multiple provider credentials found; set CAVE_MODEL once",
    );
  }
  const [provider, id] = configured[0]!;
  const model = models.getModel(provider, id);
  if (!model) throw new Error(`caveman agent: baseline model unavailable: ${provider}/${id}`);
  return model;
}

function localProviderModel(rootDir: string): string | undefined {
  try {
    const parsed = JSON.parse(readFileSync(resolve(rootDir, ".caveman/provider.json"), "utf8")) as { model?: unknown };
    return typeof parsed.model === "string" ? parsed.model : undefined;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw new Error("caveman agent: invalid .caveman/provider.json");
  }
}

/**
 * Point a model at the gateway when the gateway speaks its dialect.
 *
 * The three routes below are the only ones the Caveman gateway proxies. Every
 * other provider Pi can reach (xai, groq, bedrock, openrouter, …) keeps its own
 * base URL, and `routed: false` is what tells the caller this request is
 * third-party traffic: no account key, no telemetry headers, no `optimized`.
 */
function routeModelThroughCave(
  model: Model<Api>,
  gatewayURL: string,
): { model: Model<Api>; routed: boolean } {
  const prefix: Record<string, string> = {
    anthropic: "anthropic",
    // Pi provider clients append endpoint paths relative to these versioned
    // API roots. Dropping the version yields routes the gateway does not serve.
    openai: "openai/v1",
    google: "gemini/v1beta",
  };
  const route = prefix[model.provider];
  if (!route) return { model, routed: false };
  return { model: { ...model, baseUrl: `${gatewayURL}/${route}` }, routed: true };
}

function runtimeHeaders(
  agentId: string,
  workflow: string,
  sessionId: string,
  buildIdentity: { buildSha256: string; planSha256: string } | undefined,
  bill: Record<string, number>,
  transforms: string[],
  prefixDigest: string,
  conversationFingerprint: string,
  apiKey?: string,
  invocationTrace?: InvocationTrace,
): ProviderHeaders {
  const headers: ProviderHeaders = {
    "x-cave-agent": agentId,
    "x-cave-workflow": workflow,
    "x-cave-session": sessionId,
    "x-cave-context-bill": serializeContextBill(bill),
    "x-cave-transforms": transforms.length > 0 ? transforms.join(",") : "caveman.pass-through.v1",
    "x-cave-cache-epoch": `${agentId}:${workflow}:${sessionId}:${conversationFingerprint.slice(0, 16)}`,
    "x-cave-cache-prefix-sha256": prefixDigest,
    // Pi already applied locked transforms locally. Gateway remains metering
    // transport only; generic entitlement compression would corrupt compiler
    // baseline and double-transform selected candidates.
    "x-cave-transform-location": "local",
  };
  if (apiKey) headers["x-cave-api-key"] = apiKey;
  if (invocationTrace) {
    headers["x-cave-trace-id"] = invocationTrace.traceId;
    headers["x-cave-parent-span-id"] = invocationTrace.spanId;
  }
  if (buildIdentity) {
    headers["x-cave-agent-build"] = buildIdentity.buildSha256;
    headers["x-cave-efficiency-plan"] = buildIdentity.planSha256;
  }
  return headers;
}

function otlpAttributes(values: Record<string, string>): Array<Record<string, unknown>> {
  return Object.entries(values).map(([key, value]) => ({ key, value: { stringValue: value } }));
}

function childGuardManifest(ledger: InvocationLedger): Record<string, string> {
  return {
    "cave.guard.schema_version": "2",
    "cave.guard.basis": "client_runtime_declared",
    "cave.guard.framework": "caveman_agent",
    "cave.guard.child_calls": "active",
    "cave.guard.child_spend": ledger.rootBudget === "tokens" ? "tokens_pre_call" : "usd_pre_call",
    "cave.guard.child_context": "active",
    "cave.guard.depth": "active",
    "cave.guard.root_budget": ledger.rootBudget,
    "cave.guard.turn_fanout": ledger.turnFanout ? "active" : "absent",
    "cave.guard.model_calls": "active",
    "cave.guard.tool_calls": "active",
    "cave.guard.tree_invocations": ledger.maxInvocations === undefined ? "absent" : "active",
    "cave.guard.tree_concurrency": ledger.maxConcurrent === undefined ? "absent" : "active",
  };
}

function rootTreeOutcomes(ledger: InvocationLedger): Record<string, string> {
  return {
    "cave.agent.tree.admitted_descendants": String(ledger.admitted),
    "cave.agent.tree.peak_active_descendants": String(ledger.peakActive),
    "cave.agent.tree.invocation_limit_rejections": String(ledger.invocationRejections),
    "cave.agent.tree.concurrency_limit_rejections": String(ledger.concurrencyRejections),
  };
}

function finishInvocation(context: InternalExecutionContext, ok: boolean): void {
  const batch = context.invocationState.batch;
  if (batch === undefined) return;
  const root = context.invocationTrace.parentSpanId === "";
  const now = `${BigInt(Date.now()) * 1_000_000n}`;
  const attributes = root
    ? rootTreeOutcomes(context.invocationState.ledger)
    : childGuardManifest(context.invocationState.ledger);
  const span: Record<string, unknown> = {
    traceId: context.invocationTrace.traceId,
    spanId: context.invocationTrace.spanId,
    parentSpanId: context.invocationTrace.parentSpanId,
    name: "invoke_agent",
    kind: 1,
    startTimeUnixNano: now,
    endTimeUnixNano: now,
    attributes: otlpAttributes(attributes),
    status: { code: ok ? 1 : 2 },
  };
  if (root) {
    batch.spans.push(span);
  } else if (batch.spans.length < MAX_INVOCATION_SPANS - 1) {
    batch.spans.push(span);
  } else {
    batch.dropped++;
  }
}

function exportInvocationBatch(state: InvocationState): void {
  const batch = state.batch;
  if (batch === undefined || batch.exported || batch.apiKey === undefined) return;
  batch.exported = true;
  const payload = {
    resourceSpans: [{
      resource: {
        attributes: otlpAttributes({
          "service.name": "caveman-agent",
          "cave.telemetry.delivery_basis": "attempted_unconfirmed",
          "cave.invocation.batch.span_count": String(batch.spans.length),
          "cave.invocation.batch.dropped_spans": String(batch.dropped),
        }),
      },
      scopeSpans: [{
        scope: { name: "@caveman-ai/agent", version: "1" },
        spans: batch.spans,
      }],
    }],
  };
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new Error("cave_invocation_export_timeout")), 500);
  void Promise.resolve(batch.fetch(`${batch.gatewayURL.replace(/\/$/, "")}/otlp/v1/traces`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-cave-api-key": batch.apiKey,
      "x-cave-agent": batch.agentId,
      "x-cave-workflow": batch.workflow,
      "x-cave-session": batch.sessionId,
    },
    body: JSON.stringify(payload),
    signal: controller.signal,
  })).catch(() => undefined).finally(() => clearTimeout(timer));
}

function serializeContextBill(bill: Record<string, number>): string {
  return Object.entries(bill)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([kind, tokens]) => `${kind}=${tokens}`)
    .join(",");
}

function providerFrozenView(payload: unknown, api: string): ProviderFrozenView | undefined {
  if (!isRecord(payload)) return undefined;
  const fields: Record<string, unknown> = {};
  let messageField: ProviderFrozenView["messageField"];
  let messagePrefix: unknown[] | undefined;
  if (api === "pi-messages") {
    // Pi Messages sends one nested Context object, not Anthropic-shaped root
    // fields. Track provider-visible JSON fields at their real paths.
    const context = isRecord(payload.context) ? payload.context : undefined;
    if (context && "systemPrompt" in context) fields["context.systemPrompt"] = context.systemPrompt;
    if (context && "tools" in context) fields["context.tools"] = context.tools;
    messageField = "context.messages";
    messagePrefix = frozenConversationPrefix(context?.messages);
  } else if (api === "anthropic-messages" || api === "bedrock-converse-stream") {
    if ("system" in payload) fields.system = payload.system;
    if ("tools" in payload) fields.tools = payload.tools;
    // AWS Converse nests tool definitions and choice under toolConfig.
    if (api === "bedrock-converse-stream" && "toolConfig" in payload) fields.toolConfig = payload.toolConfig;
    messageField = "messages";
    messagePrefix = frozenConversationPrefix(payload.messages);
  } else if (api === "openai-completions" || api === "mistral-conversations") {
    // Pi's Mistral Conversations payload is OpenAI-chat-shaped: system prompt
    // is first message, tool definitions stay at root, and remaining history
    // follows in `messages`. Mistral also enables prefix reuse via x-affinity
    // plus promptCacheKey, so its frozen bytes need same drift guard.
    if ("tools" in payload) fields.tools = payload.tools;
    messageField = "messages";
    messagePrefix = frozenConversationPrefix(payload.messages);
  } else if (api === "openai-responses" || api === "openai-codex-responses" ||
      api === "azure-openai-responses") {
    if ("instructions" in payload) fields.instructions = payload.instructions;
    if ("tools" in payload) fields.tools = payload.tools;
    messageField = "input";
    messagePrefix = frozenConversationPrefix(payload.input);
  } else if (api === "google-generative-ai" || api === "google-vertex") {
    // Pi's Google adapters expose @google/genai GenerateContentParameters to
    // onPayload: provider-visible system/tool fields live under `config`, not
    // at the REST wire names on the root object.
    const config = isRecord(payload.config) ? payload.config : undefined;
    if (config && "systemInstruction" in config) fields["config.systemInstruction"] = config.systemInstruction;
    if (config && "tools" in config) fields["config.tools"] = config.tools;
    // Keep root aliases for caller-supplied adapters using raw REST payloads.
    if ("systemInstruction" in payload) fields.systemInstruction = payload.systemInstruction;
    if ("system_instruction" in payload) fields.system_instruction = payload.system_instruction;
    if ("tools" in payload) fields.tools = payload.tools;
    messageField = "contents";
    messagePrefix = frozenConversationPrefix(payload.contents);
  } else {
    return undefined;
  }
  if (messagePrefix?.length) fields[`${messageField}_prefix`] = messagePrefix;
  if (Object.keys(fields).length === 0 && messageField === undefined) return undefined;
  return {
    api,
    bytes: semanticFrozenBytes(fields, messageField, messagePrefix),
    fields,
    ...(messageField === undefined ? {} : { messageField }),
    ...(messagePrefix === undefined ? {} : { messagePrefix }),
  };
}

function frozenConversationPrefix(value: unknown): unknown[] {
  if (!Array.isArray(value)) return [];
  // Latest turn stays live. Everything before it is provider-visible history
  // whose exact serialization must remain frozen inside this cache epoch.
  return value.slice(0, Math.max(0, value.length - 1));
}

function semanticFrozenBytes(
  fields: Record<string, unknown>,
  messageField: ProviderFrozenView["messageField"],
  messagePrefix: unknown[] | undefined,
): Uint8Array {
  const ordered: Array<[string, unknown]> = [];
  // Provider cache hierarchy is explicit, not JavaScript object-key order.
  for (const key of [
    "tools",
    "toolConfig",
    "system",
    "systemInstruction",
    "system_instruction",
    "instructions",
    "config.tools",
    "config.systemInstruction",
    "context.systemPrompt",
    "context.tools",
  ]) {
    if (key in fields) ordered.push([key, fields[key]]);
  }
  if (messageField) ordered.push([messageField, messagePrefix ?? []]);
  return new TextEncoder().encode(ordered
    .map(([name, value]) => `${name}\u0000${JSON.stringify(value)}`)
    .join("\u0001"));
}

function providerFrozenExtends(current: ProviderFrozenView, prior: ProviderFrozenView): boolean {
  if (current.api !== prior.api || current.messageField !== prior.messageField) return false;
  const stableKeys = new Set([
    ...Object.keys(current.fields).filter((key) => !key.endsWith("_prefix")),
    ...Object.keys(prior.fields).filter((key) => !key.endsWith("_prefix")),
  ]);
  for (const key of stableKeys) {
    if (JSON.stringify(current.fields[key]) !== JSON.stringify(prior.fields[key])) return false;
  }
  const currentMessages = current.messagePrefix ?? [];
  const priorMessages = prior.messagePrefix ?? [];
  if (currentMessages.length < priorMessages.length) return false;
  return priorMessages.every((message, index) =>
    JSON.stringify(currentMessages[index]) === JSON.stringify(message));
}

function restoreProviderFrozen(
  payload: unknown,
  frozen: ProviderFrozenView,
  currentFrozen: ProviderFrozenView,
): unknown {
  if (!isRecord(payload)) return payload;
  const restored = structuredClone(payload);
  const priorKeys = new Set(Object.keys(frozen.fields));
  for (const key of Object.keys(currentFrozen.fields)) {
    if (!key.endsWith("_prefix") && !priorKeys.has(key)) {
      deleteNestedRecordValue(restored, key);
    }
  }
  for (const [key, value] of Object.entries(frozen.fields)) {
    if (key.endsWith("_prefix")) continue;
    setNestedRecordValue(restored, key, structuredClone(value));
  }
  if (frozen.messageField && frozen.messagePrefix) {
    const currentValue = nestedRecordValue(restored, frozen.messageField);
    const current = Array.isArray(currentValue) ? currentValue : [];
    const dynamicStart = Math.min(
      frozen.messagePrefix.length,
      frozenConversationPrefix(current).length,
    );
    const dynamic = current.slice(dynamicStart);
    setNestedRecordValue(restored, frozen.messageField, [...structuredClone(frozen.messagePrefix), ...dynamic]);
  }
  return restored;
}

function nestedRecordValue(value: Record<string, unknown>, path: string): unknown {
  let current: unknown = value;
  for (const key of path.split(".")) {
    if (!isRecord(current)) return undefined;
    current = current[key];
  }
  return current;
}

function setNestedRecordValue(value: Record<string, unknown>, path: string, next: unknown): void {
  const keys = path.split(".");
  let current = value;
  for (const key of keys.slice(0, -1)) {
    const child = isRecord(current[key]) ? current[key] : {};
    current[key] = child;
    current = child;
  }
  current[keys.at(-1)!] = next;
}

function deleteNestedRecordValue(value: Record<string, unknown>, path: string): void {
  const keys = path.split(".");
  let current = value;
  for (const key of keys.slice(0, -1)) {
    if (!isRecord(current[key])) return;
    current = current[key];
  }
  delete current[keys.at(-1)!];
}

function replaceExactStrings(value: unknown, from: string, to: string): unknown {
  if (typeof value === "string") return value === from ? to : value;
  if (Array.isArray(value)) return value.map((item) => replaceExactStrings(item, from, to));
  if (!isRecord(value)) return value;
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [
    key,
    replaceExactStrings(item, from, to),
  ]));
}

function restoreOriginalPayload(value: unknown, originals: ReadonlyMap<string, string>): unknown {
  let restored = value;
  for (const [transformed, original] of originals) {
    restored = replaceExactStrings(restored, transformed, original);
  }
  return restored;
}

// `headers` is undefined when this request does not go through the gateway:
// the plan still fails open, but no x-cave-* header is written for a third
// party to receive.
function markRequestPassThrough(
  headers: ProviderHeaders | undefined,
  plan: AppliedPlan,
  reason: string,
): void {
  markAppliedPlanFailedOpen(plan, reason);
  if (headers === undefined) return;
  headers["x-cave-transforms"] = "caveman.pass-through.v1";
  const trace = serializeTransformTrace(plan.trace);
  if (trace === "") delete headers["x-cave-transform-trace"];
  else headers["x-cave-transform-trace"] = trace;
}

function markAppliedPlanFailedOpen(plan: AppliedPlan, reason: string): void {
  if (!plan.failures.includes(reason)) plan.failures.push(reason);
  plan.appliedTransformIDs.splice(0);
  for (const item of plan.trace) {
    if (item.outcome === "applied") item.outcome = "failed_open";
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function volatileStablePrefix(instructions: string, tools: readonly ToolDefinition[]): boolean {
  const value = `${instructions}\n${stableStringify(tools.map((item) => ({
    name: item.name,
    description: item.description,
    input: item.input,
  })))}`;
  return [
    /\b20[0-9]{2}-[01][0-9]-[0-3][0-9][T ][0-2][0-9]:[0-5][0-9]/,
    /\b[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b/i,
    /["'](?:run|request|build|trace|span)[_-]?id["']\s*:/i,
    /["']nonce["']\s*:/i,
  ].some((pattern) => pattern.test(value));
}

function serializeTransformTrace(trace: readonly TransformTrace[]): string {
  if (trace.length > 64) throw new Error("cave_transform_trace_limit");
  const value = trace.map((item) => [
    item.transformID,
    item.segmentKind,
    item.safetyClass,
    item.outcome,
    item.beforeTokens,
    item.afterTokens,
    item.recoveryKind,
    item.recoveryUsed ? 1 : 0,
    item.latencyMs,
  ].join("|")).join(",");
  if (value.length > 8_192) throw new Error("cave_transform_trace_limit");
  return value;
}

function mergeHeaders(first: ProviderHeaders | undefined, second: ProviderHeaders): ProviderHeaders {
  return { ...(first ?? {}), ...second };
}

/**
 * Bound an in-process tool closure at `timeoutMs`. JavaScript
 * cannot preempt a running closure, so a tool that ignores its abort signal
 * keeps executing in the background; this only stops the RUN from waiting on it,
 * rejecting with cave_tool_timeout so a stuck host/fixture tool cannot hang a
 * run forever.
 */
function raceToolTimeout<T>(work: Promise<T>, timeoutMs: number): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  const timeout = new Promise<never>((_resolve, reject) => {
    // NOT unref'd: this timer must keep the loop alive to fire, otherwise a
    // hung closure would leave the run's promise pending forever.
    timer = setTimeout(() => reject(new Error("cave_tool_timeout")), timeoutMs);
  });
  return Promise.race([work, timeout]).finally(() => {
    if (timer !== undefined) clearTimeout(timer);
  });
}

function toPiTool(
  definition: ToolDefinition,
  sandboxExecute?: (params: unknown, signal?: AbortSignal) => Promise<unknown>,
  recoveryHandles?: Set<string>,
  engineBin?: string,
  providerDefinition?: { description: string; input: TSchema },
  deferLockedToolResult = false,
): AgentTool<TSchema> {
  return {
    name: definition.name,
    label: definition.name,
    description: providerDefinition?.description ?? definition.description,
    parameters: providerDefinition?.input ?? definition.input,
    executionMode: definition.effect === "read" ? "parallel" : "sequential",
    async execute(_toolCallId, params, signal) {
      const timeout = AbortSignal.timeout(definition.timeoutMs);
      const combined = signal ? AbortSignal.any([signal, timeout]) : timeout;
      // The worker path is bounded by killing its process; the IN-PROCESS
      // (host/fixture) path is a closure this process cannot preempt, so a
      // closure that ignores `combined` would hang the run forever. Race it
      // against a hard deadline so the run stops waiting at timeoutMs with
      // cave_tool_timeout (the closure keeps running in the background — JS
      // cannot preempt it — but it no longer blocks the run).
      const value = sandboxExecute
        ? await sandboxExecute(params, combined)
        : await raceToolTimeout(
          Promise.resolve(definition.execute(params, combined)),
          definition.timeoutMs,
        );
      const text = typeof value === "string" ? value : JSON.stringify(value);
      const raw = new TextEncoder().encode(text ?? "null");
      const maxInlineBytes = definition.artifact === undefined
        ? 32_768
        : definition.artifact.maxInlineTokens * 4;
      if (deferLockedToolResult) {
        if (raw.byteLength > 16 * 1024 * 1024) throw new Error("cave_tool_result_limit");
        return {
          content: [{ type: "text", text: text ?? "null" }],
          details: { effect: definition.effect, resultPolicy: definition.result, lockedRoutePending: true },
        };
      }
      if (definition.result === "inline") {
        if (raw.byteLength > maxInlineBytes) throw new Error("cave_tool_result_inline_limit");
        return {
          content: [{ type: "text", text: text ?? "null" }],
          details: { effect: definition.effect, resultPolicy: definition.result },
        };
      }
      const mustOffload = definition.result === "page" ||
        definition.result === "compress" ||
        definition.result === "exact_ccr" ||
        raw.byteLength > maxInlineBytes;
      if (!mustOffload) {
        return {
          content: [{ type: "text", text: text ?? "null" }],
          details: { effect: definition.effect, resultPolicy: definition.result },
        };
      }
      const transformed = await engineCompress(raw, engineBin);
      if (!transformed.handle || !recoveryHandles) {
        throw new Error("cave_tool_result_requires_recovery");
      }
      const recovered = await engineRetrieve(transformed.handle, undefined, engineBin);
      if (!bytesEqual(recovered, raw)) throw new Error("cave_tool_result_recovery_mismatch");
      recoveryHandles.add(transformed.handle);
      const compressed = new TextDecoder().decode(transformed.output);
      return {
        content: [{
          type: "text",
          text: [
            compressed,
            `<<cave_retrieve:${transformed.handle}>>`,
            definition.result === "page"
              ? "Use cave_retrieve with a narrow query to page relevant detail."
              : "Use cave_retrieve for omitted detail.",
          ].join("\n"),
        }],
        details: {
          effect: definition.effect,
          resultPolicy: definition.result,
          recoveryHandle: transformed.handle,
          recoveryVerified: true,
        },
      };
    },
  };
}

function validatedToolSchemaBytes(output: Uint8Array, segmentID: string): Uint8Array {
  const parsed = JSON.parse(new TextDecoder().decode(output)) as unknown;
  if (!isRecord(parsed) || typeof parsed.name !== "string" ||
      typeof parsed.description !== "string" || !isRecord(parsed.input)) {
    throw new Error(`tool_schema_transform_invalid:${segmentID}`);
  }
  return output;
}

function providerToolDefinition(
  definition: ToolDefinition,
  lowered: LoweredContext,
  appliedPlan: AppliedPlan,
): { description: string; input: TSchema } {
  const segment = lowered.ir.segments.find((item) => item.id === `tool.${definition.name}`);
  if (!segment) throw new Error(`cave_context_segment_missing:tool.${definition.name}`);
  const body = appliedPlan.bodies.get(segment.bodyHandle);
  if (!body) throw new Error(`cave_context_body_missing:tool.${definition.name}`);
  const parsed = JSON.parse(new TextDecoder().decode(body)) as unknown;
  if (!isRecord(parsed) || parsed.name !== definition.name ||
      typeof parsed.description !== "string" || !isRecord(parsed.input)) {
    throw new Error(`cave_tool_schema_invalid:${definition.name}`);
  }
  return { description: parsed.description, input: parsed.input as TSchema };
}

function admitSubagent(state: InvocationState): () => void {
  const ledger = state.ledger;
  if (ledger.maxInvocations !== undefined && ledger.admitted >= ledger.maxInvocations) {
    ledger.invocationRejections++;
    throw new Error("cave_subagent_invocation_limit");
  }
  if (ledger.maxConcurrent !== undefined && ledger.active >= ledger.maxConcurrent) {
    ledger.concurrencyRejections++;
    throw new Error("cave_subagent_concurrency_limit");
  }
  ledger.admitted++;
  ledger.active++;
  ledger.peakActive = Math.max(ledger.peakActive, ledger.active);
  let released = false;
  return () => {
    if (released) return;
    released = true;
    ledger.active = Math.max(0, ledger.active - 1);
  };
}

function childInvocationTrace(parent: InvocationTrace): InvocationTrace {
  return Object.freeze({
    traceId: parent.traceId,
    spanId: randomBytes(8).toString("hex"),
    parentSpanId: parent.spanId,
  });
}

async function executeSubagent(
  toolDefinition: ToolDefinition,
  params: unknown,
  signal: AbortSignal | undefined,
  parentOptions: InternalRunOptions,
  usage: NestedUsage,
  executionContext: InternalExecutionContext,
  parentMeter: BudgetMeter | undefined,
  parentDeadlineAt: number | undefined,
): Promise<unknown> {
  const runtime = toolDefinition.runtime;
  if (runtime?.kind !== "subagent") throw new Error("cave_subagent_runtime_missing");
  if (params === null || typeof params !== "object" || Array.isArray(params) ||
      typeof (params as { task?: unknown }).task !== "string") {
    throw new Error("cave_subagent_arguments_invalid");
  }
  const task = (params as { task: string }).task;
  if (task.length > runtime.maxInputChars) throw new Error("cave_subagent_input_limit");
  const calls = usage.calls.get(toolDefinition.name) ?? 0;
  if (calls >= runtime.maxCalls) throw new Error("cave_subagent_call_budget");
  // Reserve synchronously before any await so parallel Pi tool dispatch cannot
  // pass the same maxCalls check twice.
  usage.calls.set(toolDefinition.name, calls + 1);
  const depth = executionContext.depth;
  const depthLimit = Math.min(
    parentOptions.maxSubagentDepth ?? DEFAULT_SUBAGENT_DEPTH_LIMIT,
    ABSOLUTE_SUBAGENT_DEPTH_LIMIT,
  );
  if (depth + 1 > depthLimit) throw new Error("cave_subagent_depth_limit");
  // The wallet is carved here, still synchronously, for the same reason: two
  // subagents dispatched in one turn must not both be funded out of the same
  // remaining budget.
  const walletAmount = parentMeter === undefined
    ? undefined
    : parentMeter.denomination === "usd" ? runtime.maxCostUsd : runtime.maxTokens;
  if (parentMeter !== undefined && walletAmount === undefined) {
    throw new Error("cave_subagent_wallet_denomination_unavailable");
  }
  const carve = parentMeter === undefined || walletAmount === undefined
    ? undefined
    : parentMeter.carve(walletAmount);
  if (parentMeter !== undefined && carve === undefined) {
    throw new Error("cave_subagent_wallet_unavailable");
  }
  let releaseAdmission: (() => void) | undefined;
  try {
    releaseAdmission = admitSubagent(executionContext.invocationState);
    return await runSubagent({
      toolDefinition,
      runtime,
      task,
      signal,
      parentOptions,
      usage,
      executionContext,
      depth,
      childMeter: carve?.child,
      parentDeadlineAt,
    });
  } finally {
    releaseAdmission?.();
    // Whatever the child did not spend goes back to the parent, on every path.
    if (parentMeter !== undefined && carve !== undefined) parentMeter.settleCarve(carve);
  }
}

async function runSubagent(input: {
  toolDefinition: ToolDefinition;
  runtime: NonNullable<ToolDefinition["runtime"]>;
  task: string;
  signal: AbortSignal | undefined;
  parentOptions: InternalRunOptions;
  usage: NestedUsage;
  executionContext: InternalExecutionContext;
  depth: number;
  childMeter: BudgetMeter | undefined;
  parentDeadlineAt: number | undefined;
}): Promise<unknown> {
  const {
    toolDefinition,
    runtime,
    task,
    signal,
    parentOptions,
    usage,
    executionContext,
    depth,
  } = input;
  if (signal?.aborted) throw signal.reason;
  const childDefinition = runtime.definition as AgentDefinition;
  const childContext = await lowerAgentContext(childDefinition, {
    ...(parentOptions.rootDir === undefined ? {} : { rootDir: parentOptions.rootDir }),
    input: task,
  });
  const childContextTokens = childContext.ir.segments
    .reduce((total, segment) => total + segment.tokenCount, 0);
  if (childContextTokens > runtime.maxContextTokens) {
    throw new Error("cave_subagent_context_budget");
  }
  const childUsesAuto = childDefinition.model !== null &&
    typeof childDefinition.model === "object" &&
    "kind" in childDefinition.model &&
    childDefinition.model.kind === "auto";
  const childModel = childUsesAuto && parentOptions.model !== undefined
    ? parentOptions.model
    : resolveModel(
      childDefinition,
      parentOptions.models ?? builtinModels(),
      parentOptions.rootDir ?? process.cwd(),
    );
  // A carved wallet is already the child's hard economic boundary in the
  // parent's denomination. Stacking the legacy USD-only ledger on a token
  // wallet would require catalog pricing and reject otherwise valid raw-token
  // runs (including subscription/unpriced transports). Keep that ledger only
  // for standalone legacy subagents that have no carved BudgetMeter.
  let spendLedger: SpendLedger | undefined;
  if (input.childMeter === undefined) {
    spendLedger = usage.budgets.get(toolDefinition.name);
    if (spendLedger === undefined) {
      spendLedger = {
        limitUsd: runtime.maxCostUsd,
        maxContextTokens: runtime.maxContextTokens,
        exceededCode: "cave_subagent_cost_budget",
        actualUsd: 0,
        reservedUsd: 0,
        incomplete: false,
      };
      usage.budgets.set(toolDefinition.name, spendLedger);
    }
    if (spendLedger.incomplete) throw new Error("cave_subagent_spend_evidence_incomplete");
  }
  // budget and deadlineMs never travel down as options: the child is handed
  // the wallet its parent already carved and the parent's absolute deadline,
  // so it can neither mint itself a second budget nor restart the clock.
  const {
    lockedBuild: _lockedBuild,
    candidatePlan: _candidatePlan,
    conversation: _conversation,
    model: _parentModel,
    signal: _parentSignal,
    budget: _parentBudget,
    budgetController: _parentBudgetController,
    onBudgetExhausted: _parentOnBudgetExhausted,
    deadlineMs: _parentDeadlineMs,
    ...inherited
  } = parentOptions;
  const subagentPath = Object.freeze([
    ...executionContext.agentPath,
    toolDefinition.name,
  ]);
  const childExecutionContext: InternalExecutionContext = Object.freeze({
    rootDefinitionSha256: executionContext.rootDefinitionSha256,
    agentPath: subagentPath,
    spendLedgers: spendLedger === undefined
      ? executionContext.spendLedgers
      : Object.freeze([...executionContext.spendLedgers, spendLedger]),
    sandboxRequired: executionContext.sandboxRequired ||
      childDefinition.sandbox === "required",
    depth: depth + 1,
    invocationState: executionContext.invocationState,
    invocationTrace: childInvocationTrace(executionContext.invocationTrace),
    ...(input.childMeter === undefined ? {} : { budgetMeter: input.childMeter }),
    ...(input.parentDeadlineAt === undefined ? {} : { deadlineAt: input.parentDeadlineAt }),
  });
  const childSignal = signal === undefined
    ? _parentSignal
    : _parentSignal === undefined
      ? signal
      : AbortSignal.any([signal, _parentSignal]);
  let child: RunResult;
  try {
    child = await runAgentWithOptions(
      childDefinition,
      task,
      {
        ...inherited,
        ensureRuntime: false,
        model: childModel,
        ...(childSignal === undefined ? {} : { signal: childSignal }),
      },
      childExecutionContext,
    );
  } catch (error) {
    if (error instanceof CavemanRunError) usage.receipts.push(error.receipt);
    usage.incomplete = true;
    throw error;
  }
  if (child.usageBasis !== "provider_reported" || !completeUsage(child)) {
    usage.incomplete = true;
    throw new Error("cave_nested_usage_incomplete");
  }
  usage.inputTokens += child.inputTokens;
  usage.outputTokens += child.outputTokens;
  usage.cacheReadTokens += child.cacheReadTokens;
  usage.cacheWriteTokens += child.cacheWriteTokens;
  usage.reasoningTokens += child.reasoningTokens;
  usage.costUsd += child.costUsd;
  usage.receipts.push(child.receipt);
  if (child.mode === "observe-only") usage.observeOnly = true;
  if (child.priceBasis !== "public_catalog") usage.unpriced = true;
  if (child.inputTokens > runtime.maxContextTokens) {
    usage.incomplete = true;
    throw new Error("cave_subagent_context_budget");
  }
  if (spendLedger !== undefined &&
      (spendLedger.incomplete || spendLedger.actualUsd > runtime.maxCostUsd)) {
    usage.incomplete = true;
    throw new Error("cave_subagent_cost_budget");
  }
  return {
    text: child.text,
    agent_id: child.agentId,
    usage_basis: child.usageBasis,
    input_tokens: child.inputTokens,
    output_tokens: child.outputTokens,
    cost_usd: child.costUsd,
    // A child that ran out of wallet returns partial work; the caller sees why
    // rather than reading a truncated answer as a complete one.
    stop_reason: child.stopReason,
    claim_basis: "inferred",
  };
}

function completeUsage(result: RunResult): boolean {
  if (result.reasoningUsageBasis !== "provider_reported") return false;
  return [
    result.inputTokens,
    result.outputTokens,
    result.cacheReadTokens,
    result.cacheWriteTokens,
    result.reasoningTokens,
    result.costUsd,
  ].every((value) => Number.isFinite(value) && value >= 0);
}

/**
 * Ceiling on one prospective call over `messages`, on the same byte-derived
 * basis the meter reserves against.
 */
export function callCeilingFor(
  selected: Model<Api>,
  systemPrompt: string,
  messages: readonly AgentMessage[],
  tools: unknown,
  outputTokenCap: number,
  restorableBytes = 0,
): CallCeiling {
  const bytes = serializedContextBytes({ systemPrompt, messages, tools });
  return {
    provider: selected.provider,
    model: selected.id,
    inputTokenCeiling: bytes === undefined
      ? selected.contextWindow
      : inputTokenCeiling(
        bytes + Math.max(0, restorableBytes),
        messages.length,
        selected.contextWindow,
      ),
    outputTokenCap,
  };
}

/**
 * One compaction attempt: **evict, then summarize**, both inside the same
 * budget the run is defending.
 *
 * Eviction avoids a summarizer call — stale tool output becomes a citation
 * pointer — and is applied whenever it shrinks anything.
 * Summarization is only reached when eviction alone did not buy the next call,
 * and only when its own preconditions hold: affordable at the cold cache rate,
 * yielding more than the floor, and leaving room for several working calls
 * rather than exactly one.
 *
 * Every rejection path returns the best context it has and lets the caller fall
 * through to the clamp rung. A compaction that cannot be checked is never
 * accepted.
 */
async function compactContext(input: {
  messages: readonly AgentMessage[];
  systemPrompt: string;
  tools: unknown;
  meter: BudgetMeter;
  selected: Model<Api>;
  outputTokenCap: number;
  baseStream: StreamFn;
  sessionId: string;
  signal: AbortSignal | undefined;
  previousSummary: ContextSummary | undefined;
  receipt: ReceiptRecorder;
  /**
   * The run's gateway header set, or undefined when this run is not routed
   * through the gateway. A summarization on an optimized run must carry the
   * same account credential and identifiers as a working call — without them
   * the gateway rejects it and the rung dies silently on exactly the path it
   * was built for.
   */
  headers: ProviderHeaders | undefined;
  /**
   * Legacy outer spend ledgers this run reserves against, when present. A
   * carved child's own hard boundary is its BudgetMeter; this separate list
   * preserves older root/ancestor maxCostUsd limits without stacking another
   * USD ledger on that child meter.
   */
  spendLedgers: readonly SpendLedger[];
  /** Called once a compaction has taken a reservation, so a paid attempt counts. */
  onReserved: () => void;
  /**
   * Accrues the summarizer's provider usage into the run's own totals. A
   * compaction is a provider call the run made; it belongs in `RunResult`'s
   * usage and cost exactly like a working call, or the run under-reports
   * itself and every parent aggregating it inherits the gap.
   */
  accrue: (usage: ValidatedProviderUsage | undefined) => void;
  /**
   * Whether the provider's own usage reported a warm prefix on the last call.
   * `unknown` models cold — under-claim, never blend.
   */
  cacheState: "warm" | "cold" | "unknown";
}): Promise<{ messages: AgentMessage[]; summary: ContextSummary | undefined } | undefined> {
  const config = input.meter.compaction;
  const plan = planCompaction(input.messages, config);
  const preTokens = messagesTokens(input.messages);
  const elidedDigests: string[] = [];
  const evictable = new Set(plan.evictable);
  const evicted = input.messages.map((message, index) => {
    if (!evictable.has(index)) return message;
    const citation = evictMessage(message, index);
    // A citation longer than what it replaces is not a compaction.
    if (messagesTokens([citation]) >= messagesTokens([message])) return message;
    elidedDigests.push(elidedDigest(message));
    return citation;
  });
  const evictedTokens = messagesTokens(evicted);
  const pinnedIds = plan.pinned.map((index) => `runtime.user_intent.${index}`);
  const evictionHelped = evictedTokens < preTokens;

  const fitsAfterEviction = callFitsBudget(
    input.meter,
    callCeilingFor(
      input.selected,
      input.systemPrompt,
      evicted,
      input.tools,
      input.outputTokenCap,
    ),
  );
  if (fitsAfterEviction && evictionHelped) {
    input.receipt.recordCompaction({
      tier: "evicted",
      preTokens,
      postTokens: evictedTokens,
      pinnedSegmentIds: pinnedIds,
      elidedSegmentDigests: elidedDigests,
      summarySchemaVersion: undefined,
      cacheState: input.cacheState,
      meteredCost: 0,
      });
    return { messages: evicted, summary: input.previousSummary };
  }

  const recentSet = new Set(plan.recent);
  const head = plan.pinned
    .filter((index) => !recentSet.has(index))
    .map((index) => input.messages[index]!);
  const tail = plan.recent.map((index) => evicted[index]!);
  const summarizerModel = chooseSummarizer(config.summarizerModel, input.selected, preTokens);
  // The summarization request is built by the same shape as a working call —
  // same system prompt, same tools, same history — so it extends the cached
  // prefix instead of starting a second one.
  const summarizerMessages: AgentMessage[] = [
    ...evicted,
    {
      role: "user",
      content: summarizationInstruction(input.previousSummary),
      timestamp: Date.now(),
    } as AgentMessage,
  ];
  const summarizerCall = callCeilingFor(
    summarizerModel,
    input.systemPrompt,
    summarizerMessages,
    input.tools,
    config.summaryMaxTokens,
  );
  // Always reserved COLD, whatever the last working call's cache evidence said.
  //
  // The summarizer is a different request from the call that read that warm
  // prefix: the middle has been rewritten by eviction, an instruction is
  // appended, and the working call's context transform did not run. A prefix
  // that diverges at the first rewritten message is a cold read, so pricing it
  // as a cache read books roughly a tenth of what arrives.
  //
  // The same-model summarizer is intentionally reachable because the caller
  // triggers while four cold working-call ceilings remain. This reserve still
  // prices the summarizer cold; reachability comes from earlier timing, never
  // an unevidenced cache discount.
  const summarizerCost = callCeilingCost(
    input.meter.denomination,
    summarizerCall,
    config.summaryMaxTokens,
  );
  if (summarizerCost === undefined) return evictionHelped ? finishEviction() : undefined;
  // Model the post-compaction working call before paying for one: the summary
  // replaces the middle, so the projected context is head + summary + tail.
  const projected = [...head, summaryMessage(placeholderSummary(config.summaryMaxTokens)), ...tail];
  const projectedTokens = messagesTokens(projected);
  if (preTokens - projectedTokens < config.minYieldTokens) {
    return evictionHelped ? finishEviction() : undefined;
  }
  const projectedCall = callCeilingFor(
    input.selected,
    input.systemPrompt,
    projected,
    input.tools,
    input.outputTokenCap,
  );
  const projectedCost = callCeilingCost(
    input.meter.denomination,
    projectedCall,
    input.outputTokenCap,
  );
  if (projectedCost === undefined) return evictionHelped ? finishEviction() : undefined;
  // Budget floor: reserve enough for compaction and projected working calls;
  // otherwise eviction is the only safe fallback.
  if (input.meter.remaining() < summarizerCost + config.headroomCalls * projectedCost) {
    return evictionHelped ? finishEviction() : undefined;
  }

  const reservation = input.meter.reserve(summarizerCost, config.summaryMaxTokens);
  if (reservation === undefined) return evictionHelped ? finishEviction() : undefined;
  // Preserve any legacy outer maxCostUsd holds too. A ledger that cannot cover
  // this call declines compaction rather than throwing; carved child-wallet
  // enforcement is already the BudgetMeter reservation above.
  let ledgerReservations: SpendReservation[];
  try {
    ledgerReservations = reserveProviderSpend(
      input.spendLedgers,
      summarizerModel,
      config.summaryMaxTokens,
    );
  } catch {
    input.meter.cancel(reservation);
    return evictionHelped ? finishEviction() : undefined;
  }
  // Past this point the attempt is paid for whatever happens to its output.
  input.onReserved();
  let assistant: AssistantMessage;
  try {
    const stream = await input.baseStream(
      summarizerModel,
      {
        systemPrompt: input.systemPrompt,
        messages: summarizerMessages as never,
        // Same tool definitions as a working call. Dropping them is what makes
        // a compaction request diverge from the cached prefix and re-send the
        // whole history as fresh input.
        tools: input.tools as never,
      },
      {
        maxTokens: config.summaryMaxTokens,
        sessionId: input.sessionId,
        ...(input.signal === undefined ? {} : { signal: input.signal }),
        // Same account credential and identifiers as a working call. The
        // context bill and transform markers describe THIS request: a
        // compaction is a framework-local rewrite, not a gateway transform.
        ...(input.headers === undefined ? {} : {
          headers: {
            ...input.headers,
            "x-cave-transforms": "caveman.pass-through.v1",
            "x-cave-transform-location": "local",
          },
        }),
      },
    );
    for await (const _event of stream) { /* drain to terminal */ }
    assistant = await stream.result();
  } catch {
    input.meter.cancel(reservation);
    releaseLedgerHolds(ledgerReservations);
    return evictionHelped ? finishEviction() : undefined;
  }
  let metered = reservation.amount;
  let unpriced = true;
  let ledgerFailure: Error | undefined;
  try {
    const usage = validateProviderUsage({
      provider: assistant.provider,
      model: assistant.model,
      inputTokens: assistant.usage.input,
      outputTokens: assistant.usage.output,
      cacheReadTokens: assistant.usage.cacheRead,
      cacheWriteTokens: assistant.usage.cacheWrite,
      reasoningTokens: assistant.usage.reasoning ?? 0,
      totalTokens: assistant.usage.totalTokens,
    });
    metered = input.meter.denomination === "usd" ? usage.catalogCostUsd : usage.totalTokens;
    unpriced = !usage.priced;
    // Settle any legacy outer maxCostUsd ledgers. A carved child's own
    // compaction is settled by its BudgetMeter reservation above.
    ledgerFailure = settleProviderSpend(ledgerReservations, usage);
    input.accrue(usage);
    input.receipt.recordCall({
      provider: usage.provider,
      model: usage.model,
      inputTokens: usage.inputTokens,
      outputTokens: usage.outputTokens,
      cacheReadTokens: usage.cacheReadTokens,
      cacheWriteTokens: usage.cacheWriteTokens,
      reasoningTokens: usage.reasoningTokens,
      estimatedUsd: usage.catalogCostUsd,
      unpriced: !usage.priced,
      usageBasis: "provider_reported",
      clampedOutputTokens: undefined,
    });
  } catch {
    // The summarizer ran and cost something we cannot read. Settle at the
    // worst case rather than letting an unmeasurable call read as free, and
    // tell the run and any legacy outer ledgers the evidence is incomplete.
    markSpendIncomplete(ledgerReservations.map((item) => item.ledger));
    input.accrue(undefined);
    input.receipt.recordCall({
      provider: summarizerModel.provider,
      model: summarizerModel.id,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
      reasoningTokens: 0,
      estimatedUsd: 0,
      unpriced: true,
      usageBasis: "unavailable",
      clampedOutputTokens: undefined,
    });
  }
  input.meter.settle(reservation, metered);
  // A compaction that pushed a legacy outer ledger past its cap is that
  // ledger's failure to raise, exactly as a working call would be.
  if (ledgerFailure !== undefined) throw ledgerFailure;
  if (unpriced && input.meter.denomination === "usd") {
    return evictionHelped ? finishEviction(metered) : undefined;
  }

  const summary = parseContextSummary(assistantText(assistant));
  // A summary that does not validate is discarded, never shipped.
  if (summary === undefined) return evictionHelped ? finishEviction(metered) : undefined;
  const compacted = [...head, summaryMessage(renderSummary(summary)), ...tail];
  const postTokens = messagesTokens(compacted);
  // Pinned survival is STRUCTURAL here, not something to re-assert: `head` and
  // `tail` are verbatim slices of the original messages and the summary is
  // inserted strictly between them, so every pinned index is carried through
  // unchanged by construction. A `pinnedContentSurvives` call at
  // this site can only ever succeed, so it is retired rather than kept as a
  // check that cannot fail. Inflation is the real guard that remains: a rewrite
  // that bought nothing is discarded.
  if (postTokens >= preTokens) {
    return evictionHelped ? finishEviction(metered) : undefined;
  }
  input.receipt.recordCompaction({
    tier: "summarized",
    preTokens,
    postTokens,
    pinnedSegmentIds: pinnedIds,
    elidedSegmentDigests: elidedDigests,
    summarySchemaVersion: SUMMARY_SCHEMA_VERSION,
    cacheState: input.cacheState,
    meteredCost: metered,
  });
  return { messages: compacted, summary };

  function finishEviction(meteredCost = 0) {
    input.receipt.recordCompaction({
      tier: "evicted",
      preTokens,
      postTokens: evictedTokens,
      pinnedSegmentIds: pinnedIds,
      elidedSegmentDigests: elidedDigests,
      summarySchemaVersion: undefined,
      cacheState: input.cacheState,
      meteredCost,
      });
    return { messages: evicted, summary: input.previousSummary };
  }
}

/**
 * A cheaper summarizer is opt-in and gated: below a context window that covers
 * the history it must read, it would fail at exactly the moment it is needed,
 * so the run falls back to its own working model.
 */
function chooseSummarizer(
  requested: Model<Api> | undefined,
  working: Model<Api>,
  historyTokens: number,
): Model<Api> {
  if (requested === undefined) return working;
  return requested.contextWindow >= historyTokens ? requested : working;
}

function summaryMessage(body: Record<string, unknown>): AgentMessage {
  return {
    role: "user",
    content: `<cave-context-summary>\n${JSON.stringify(body)}\n</cave-context-summary>`,
    timestamp: Date.now(),
  } as AgentMessage;
}

/** Worst-case summary body used to model the post-compaction context before paying for one. */
function placeholderSummary(summaryMaxTokens: number): Record<string, unknown> {
  return { schema_version: SUMMARY_SCHEMA_VERSION, objective: "x".repeat(summaryMaxTokens * 4) };
}

async function retryModelCall(
  attemptCall: () => ReturnType<StreamFn>,
  breakers: BreakerState,
  pending: PendingCallRecord,
  stopError: () => Error | undefined,
  abandon: () => void,
): Promise<Awaited<ReturnType<StreamFn>>> {
  const meter = pending.reservationMeter;
  const initial = pending.reservation;
  if (meter === undefined || initial === undefined) return attemptCall();
  const worstCaseSpend = initial.amount;
  const outputTokenCap = initial.outputTokenCap;
  let attempt = 0;
  for (;;) {
    try {
      return await attemptCall();
    } catch (error) {
      if (pending.reservation !== undefined) {
        pending.reservationMeter?.cancel(pending.reservation);
        if (pending.retryAttempt !== undefined) {
          breakers.settleRetry(pending.retryAttempt, 0, "pre_stream_no_usage");
        }
        pending.reservation = undefined;
        pending.reservationMeter = undefined;
        pending.retryAttempt = undefined;
      }
      const terminal = stopError();
      if (terminal !== undefined || terminalRetryError(error)) {
        abandon();
        throw terminal ?? error;
      }
      attempt++;
      if (!breakers.retryExposureAvailable(worstCaseSpend, attempt)) {
        abandon();
        throw error;
      }
      const reservation = meter.reserve(worstCaseSpend, outputTokenCap);
      if (reservation === undefined) {
        breakers.recordRetryExhausted(attempt);
        abandon();
        throw error;
      }
      pending.reservation = reservation;
      pending.reservationMeter = meter;
      pending.retryAttempt = attempt;
      breakers.recordRetryAttempt(worstCaseSpend, attempt);
      const wait = breakers.backoffMs(attempt);
      if (wait > 0) await new Promise((settle) => setTimeout(settle, wait));
      const expired = stopError();
      if (expired !== undefined) {
        meter.cancel(reservation);
        breakers.settleRetry(attempt, 0, "pre_stream_no_usage");
        pending.reservation = undefined;
        pending.reservationMeter = undefined;
        pending.retryAttempt = undefined;
        abandon();
        throw expired;
      }
    }
  }
}

function terminalRetryError(error: unknown): boolean {
  if (!(error instanceof Error)) return false;
  return error.name === "AbortError" ||
    error.message === "cave_run_aborted" ||
    error.message === "cave_stream_cancelled" ||
    error.message === "cave_run_stopped" ||
    error.message === "cave_provider_terminal_aborted";
}

/**
 * How many bytes an outgoing payload could still grow by if `onPayload`
 * restores originals.
 *
 * A transformed request carries compressed text; on cache-prefix drift the
 * runtime puts the uncompressed original back before the request leaves. The
 * reserve is taken before that happens, so it adds the full delta of every
 * substitution the run could reverse — an over-estimate by construction, which
 * is the only safe direction for a ceiling.
 */
export function restorableRequestBytes(
  originals: ReadonlyMap<string, string>,
  instructions: string,
  originalInstructions: string,
): number {
  let growth = Math.max(0, originalInstructions.length - instructions.length);
  for (const [replacement, original] of originals) {
    growth += Math.max(0, original.length - replacement.length);
  }
  return growth;
}

/** One in-flight provider call: its budget hold and the allowance it was given. */
type PendingCallRecord = {
  reservation: BudgetReservation | undefined;
  reservationMeter: BudgetMeter | undefined;
  retryAttempt: number | undefined;
  readonly clampedOutputTokens: number | undefined;
};

type NextCallDecision =
  | {
    readonly action: "proceed";
    readonly reservation: BudgetReservation | undefined;
    /** Set only when the budget lowered this call's output allowance. */
    readonly clampedOutputTokens: number | undefined;
  }
  | { readonly action: "compact"; readonly call: CallCeiling }
  | { readonly action: "stop"; readonly reason: RunStopReason };

/**
 * The exhaustion ladder at one call boundary: deadline, then full allowance,
 * then a clamped allowance down to the floor, then stop.
 *
 * The input side of the reserve is a ceiling, not an estimate. Every catalog
 * tokenizer is byte-level BPE, so the serialized request's byte count bounds
 * its token count from above, and the model's context window bounds that in
 * turn. Reserving high stops a run at most one call early; reserving low would
 * let it spend past a cap it promised to hold.
 */
function decideNextCall(input: {
  meter: BudgetMeter | undefined;
  breakers: BreakerState | undefined;
  deadlineAt: number | undefined;
  selected: Model<Api>;
  context: Context;
  requestedOutputTokens: number | undefined;
  outputMaxTokens: number | undefined;
  planOutputTokens: number | undefined;
  /**
   * Bytes the outgoing payload can still GROW by after this reserve is taken.
   *
   * The reserve is computed on the context as it stands, but `onPayload` runs
   * afterwards and can restore uncompressed originals when the cache prefix
   * drifts — a request larger than the one that was priced. Adding the full
   * restorable delta keeps the reserve above the payload that actually leaves,
   * whichever branch onPayload takes.
   */
  restorableBytes?: number;
}): NextCallDecision {
  const tripped = input.breakers?.tripped;
  if (tripped !== undefined) return { action: "stop", reason: tripped };
  if (input.deadlineAt !== undefined && performance.now() >= input.deadlineAt) {
    return { action: "stop", reason: "deadline" };
  }
  if (input.meter === undefined) {
    return { action: "proceed", reservation: undefined, clampedOutputTokens: undefined };
  }
  if (input.meter.denomination === "usd" &&
      !modelIsPriced(input.selected.provider, input.selected.id)) {
    throw new Error("cave_budget_denomination_unavailable");
  }
  const outputTokenCap = Math.min(
    input.requestedOutputTokens ?? Number.MAX_SAFE_INTEGER,
    input.outputMaxTokens ?? Number.MAX_SAFE_INTEGER,
    input.planOutputTokens ?? Number.MAX_SAFE_INTEGER,
    input.selected.maxTokens,
  );
  // One construction for every reserve in the runtime, so the restorable term
  // cannot be present in one place and missing in another.
  const call = callCeilingFor(
    input.selected,
    input.context.systemPrompt ?? "",
    input.context.messages as unknown as readonly AgentMessage[],
    input.context.tools,
    outputTokenCap,
    input.restorableBytes ?? 0,
  );
  // Compaction is never available at the CALL boundary: it is a between-turns
  // rewrite decided by prepareNextTurnWithContext, so by the time a call is being
  // planned the ladder has only clamp and stop left — the always-
  // false compactionAvailable param was dead and is removed).
  const plan = planCall(input.meter, call, false);
  if (plan.action === "stop") return { action: "stop", reason: plan.reason };
  if (plan.action === "compact") return { action: "compact", call };
  return {
    action: "proceed",
    reservation: plan.reservation,
    clampedOutputTokens: plan.outputTokenCap < outputTokenCap ? plan.outputTokenCap : undefined,
  };
}

function reserveProviderSpend(
  ledgers: readonly SpendLedger[],
  model: Model<Api>,
  requestedOutputTokens: number | undefined,
): SpendReservation[] {
  if (ledgers.length === 0) return [];
  const outputTokens = Math.min(
    model.maxTokens,
    requestedOutputTokens ?? model.maxTokens,
  );
  const ceilingUsd = catalogSearchCeiling(
    `${model.provider}/${model.id}`,
    model.contextWindow,
    outputTokens,
  );
  if (ceilingUsd === undefined) {
    markSpendIncomplete(ledgers);
    throw new Error("cave_subagent_unpriced_budget");
  }
  for (const ledger of ledgers) {
    if (ledger.incomplete) throw new Error("cave_subagent_spend_evidence_incomplete");
    if (ledger.actualUsd + ledger.reservedUsd + ceilingUsd > ledger.limitUsd) {
      throw new Error(ledger.exceededCode);
    }
  }
  const reservations = ledgers.map((ledger) => ({
    ledger,
    ceilingUsd,
    provider: model.provider,
    model: model.id,
  }));
  for (const { ledger } of reservations) ledger.reservedUsd += ceilingUsd;
  return reservations;
}

function settleProviderSpend(
  reservations: readonly SpendReservation[],
  usage: ValidatedProviderUsage,
): Error | undefined {
  if (reservations.length === 0) return undefined;
  const requested = reservations[0]!;
  if (usage.provider !== requested.provider || usage.model !== requested.model) {
    markSpendIncomplete(reservations.map((item) => item.ledger));
    return new Error("cave_subagent_model_identity_mismatch");
  }
  if (!usage.priced) {
    markSpendIncomplete(reservations.map((item) => item.ledger));
    return new Error("cave_subagent_spend_evidence_incomplete");
  }
  let failure: Error | undefined;
  for (const reservation of reservations) {
    const { ledger, ceilingUsd } = reservation;
    ledger.reservedUsd = Math.max(0, ledger.reservedUsd - ceilingUsd);
    ledger.actualUsd += usage.catalogCostUsd;
    if (usage.catalogCostUsd > ceilingUsd || ledger.actualUsd > ledger.limitUsd) {
      ledger.incomplete = true;
      failure ??= new Error(ledger.exceededCode);
    }
  }
  return failure;
}

function markSpendIncomplete(ledgers: readonly SpendLedger[]): void {
  for (const ledger of ledgers) ledger.incomplete = true;
}

/** Give back holds for a call that never reached the provider. */
function releaseLedgerHolds(reservations: readonly SpendReservation[]): void {
  for (const { ledger, ceilingUsd } of reservations) {
    ledger.reservedUsd = Math.max(0, ledger.reservedUsd - ceilingUsd);
  }
}

async function executeSandboxedTool(
  entryPath: string,
  sourceFiles: readonly string[],
  stagingRoot: string,
  toolName: string,
  params: unknown,
  timeoutMs: number,
  allowSideEffects: boolean,
  profile: RunOptions["sandboxProfile"],
  executionContext: InternalExecutionContext,
  toolDefinitionSha256: string,
  signal?: AbortSignal,
): Promise<unknown> {
  if (profile?.childProcess === true) {
    throw new Error("cave_sandbox_child_process_containment_unavailable");
  }
  // `network: true` used to skip the OS network namespace entirely, granting the
  // tool UNRESTRICTED egress while credentials sit in its env — an exfiltration
  // hole, not a feature. There is no scoped-egress mechanism
  // yet (a parent-owned CONNECT proxy bound to an allow-list is the tracked
  // follow-up), so unbounded egress fails closed rather than being granted. Every
  // sandboxed tool now runs under the OS boundary below.
  if (profile?.network === true) {
    throw new Error("cave_sandbox_network_egress_unbounded");
  }
  const requestedCredentialEnv = profile?.credentialEnv ?? [];
  const childEnv = buildSandboxToolEnv(requestedCredentialEnv);
  // Validate every collapsed grant before allocating per-call state. Refused
  // roots must fail without leaving a caveman-agent-tool-* workspace behind.
  const sourceReadFlags = sandboxSourceReadFlags(sourceFiles, stagingRoot);
  const workspace = await realpath(await mkdtemp(`${tmpdir()}/caveman-agent-tool-`));
  const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));
  const worker = fileURLToPath(new URL("./tool-worker.js", import.meta.url));
  const timeout = AbortSignal.timeout(timeoutMs);
  const combined = signal ? AbortSignal.any([signal, timeout]) : timeout;
  const args = [
    "--permission",
    // One declared source file, framework runtime, dependencies, and ephemeral
    // workspace only. Never grant tool code a project-root read capability:
    // repositories commonly contain .env files, credentials, and local traces.
    ...sourceReadFlags,
    `--allow-fs-read=${packageRoot}`,
    ...sandboxDependencyReadRoots().map((path) => `--allow-fs-read=${path}`),
    `--allow-fs-read=${workspace}`,
    `--allow-fs-write=${workspace}`,
    worker,
  ];
  // Always under the OS boundary: `network: true` was refused above, so there is
  // no un-isolated spawn path left.
  const isolated = networkIsolatedNode(args);
  installSandboxReaping();
  try {
    const result = await new Promise<{ ok: boolean; value?: unknown; code?: string }>((accept, reject) => {
      // fd 3 carries the length-prefixed result; the tool's own stdout/stderr
      // are separate channels with their own byte budgets so a chatty dep can
      // neither corrupt the result nor SIGKILL a successful tool.
      const child = spawn(isolated.command, isolated.args, {
        cwd: workspace,
        detached: process.platform !== "win32",
        env: childEnv,
        stdio: ["pipe", "pipe", "pipe", "pipe"],
      });
      liveSandboxChildren.add(child);
      const resultChunks: Buffer[] = [];
      const stderr: Buffer[] = [];
      let resultBytes = 0;
      let stderrBytes = 0;
      let terminalError: Error | undefined;
      let killTimer: NodeJS.Timeout | undefined;
      const terminate = (error: Error, immediate = false) => {
        terminalError ??= error;
        killSandboxProcess(child, immediate ? "SIGKILL" : "SIGTERM");
        if (!immediate && killTimer === undefined) {
          killTimer = setTimeout(() => {
            killSandboxProcess(child, "SIGKILL");
          }, 250);
          killTimer.unref();
        }
      };
      const onAbort = () => {
        const signaledReason = signal?.aborted ? signal.reason : undefined;
        const reason = signaledReason instanceof DOMException &&
            signaledReason.name === "TimeoutError"
          ? new Error("cave_sandbox_timeout")
          : signaledReason ?? new Error("cave_sandbox_timeout");
        terminate(
          reason instanceof Error ? reason : new Error(String(reason)),
        );
      };
      // The tool's own stdout (console.log in its import graph) is not the
      // result and never fails the tool; drained and dropped.
      child.stdout.on("data", () => undefined);
      // stderr keeps its OWN budget: a runaway log caps its buffer but does not
      // terminate a tool that otherwise succeeded on fd 3.
      child.stderr.on("data", (chunk: Buffer) => {
        stderrBytes += chunk.byteLength;
        if (stderrBytes <= 1_048_576) stderr.push(chunk);
      });
      const resultStream = child.stdio[3] as NodeJS.ReadableStream | null | undefined;
      resultStream?.on("data", (chunk: Buffer) => {
        resultBytes += chunk.byteLength;
        if (resultBytes > 1_048_576) {
          terminate(new Error("cave_sandbox_output_limit"), true);
          return;
        }
        resultChunks.push(chunk);
      });
      child.stdin.on("error", () => undefined);
      child.once("error", (error) => {
        terminalError ??= error;
      });
      child.once("close", (code) => {
        liveSandboxChildren.delete(child);
        combined.removeEventListener("abort", onAbort);
        if (killTimer !== undefined) clearTimeout(killTimer);
        if (terminalError) {
          reject(terminalError);
          return;
        }
        const parsed = decodeResultFrame(Buffer.concat(resultChunks));
        if (parsed !== undefined) {
          accept(parsed);
          return;
        }
        // No valid result frame: fall back to the exit code and redacted stderr.
        if (code !== 0) {
          reject(new Error(`cave_sandbox_failed:${redactSandboxError(Buffer.concat(stderr).toString("utf8"))}`));
          return;
        }
        reject(new Error("cave_sandbox_invalid_output"));
      });
      combined.addEventListener("abort", onAbort, { once: true });
      if (combined.aborted) onAbort();
      child.stdin.end(JSON.stringify({
        entry: pathToFileURL(entryPath).href,
        agentPath: executionContext.agentPath,
        rootDefinitionSha256: executionContext.rootDefinitionSha256,
        toolDefinitionSha256,
        tool: toolName,
        params,
        allowSideEffects,
        allowNetwork: profile?.network === true,
      }));
    });
    if (!result.ok) throw new Error(result.code ?? "cave_sandbox_tool_failed");
    return result.value;
  } finally {
    await rm(workspace, { recursive: true, force: true });
  }
}

/**
 * Above this many per-file `--allow-fs-read` flags, collapse the staged source
 * files to their common ancestor directory. A large project would
 * otherwise blow the OS argument limit (E2BIG) and the tool could not spawn at
 * all. The collapse is safe here: `sourceFiles` are paths inside the per-run
 * STAGED COPY, which already contains only the reachable source graph — never
 * the real project root with its .env and credentials.
 */
const SANDBOX_FS_READ_FLAG_THRESHOLD = 1024;

function commonAncestorDir(paths: readonly string[]): string {
  const dirs = paths.map((path) => resolve(dirname(path)));
  const first = dirs[0];
  if (first === undefined) {
    throw new Error("cave_sandbox_source_staging_root_required");
  }
  let ancestor = first;
  while (dirs.some((path) => escapesRoot(relative(ancestor, path)))) {
    const parent = dirname(ancestor);
    if (parent === ancestor) {
      throw new Error("cave_sandbox_source_read_root_refused");
    }
    ancestor = parent;
  }
  if (dirname(ancestor) === ancestor) {
    throw new Error("cave_sandbox_source_read_root_refused");
  }
  return ancestor;
}

export function sandboxSourceReadFlags(
  sourceFiles: readonly string[],
  stagingRoot?: string,
): string[] {
  if (sourceFiles.length <= SANDBOX_FS_READ_FLAG_THRESHOLD) {
    return sourceFiles.map((path) => `--allow-fs-read=${path}`);
  }
  if (stagingRoot === undefined) {
    throw new Error("cave_sandbox_source_staging_root_required");
  }
  const resolvedStagingRoot = resolve(stagingRoot);
  if (dirname(resolvedStagingRoot) === resolvedStagingRoot) {
    throw new Error("cave_sandbox_source_read_root_refused");
  }
  const ancestor = commonAncestorDir(sourceFiles);
  if (escapesRoot(relative(resolvedStagingRoot, ancestor))) {
    throw new Error("cave_sandbox_source_read_grant_escapes_staging");
  }
  return [`--allow-fs-read=${ancestor}`];
}

/** Decode the length-prefixed result frame delivered on the worker's fd 3. */
function decodeResultFrame(
  buffer: Buffer,
): { ok: boolean; value?: unknown; code?: string } | undefined {
  if (buffer.byteLength < 4) return undefined;
  const length = buffer.readUInt32BE(0);
  if (buffer.byteLength < 4 + length) return undefined;
  try {
    return JSON.parse(buffer.subarray(4, 4 + length).toString("utf8"));
  } catch {
    return undefined;
  }
}

// Every live sandbox child (spawned detached, so it outlives an ungraceful
// parent exit). Reaped on parent exit and on a catchable termination signal so a
// SIGINT/SIGTERM does not strand detached tool process groups forever.
const liveSandboxChildren = new Set<ChildProcess>();
let sandboxReapingInstalled = false;
function installSandboxReaping(): void {
  if (sandboxReapingInstalled) return;
  sandboxReapingInstalled = true;
  const reap = (): void => {
    for (const child of liveSandboxChildren) killSandboxProcess(child, "SIGKILL");
    liveSandboxChildren.clear();
  };
  process.on("exit", reap);
  for (const sig of ["SIGINT", "SIGTERM"] as const) {
    const handler = (): void => {
      reap();
      // Non-intrusive: defer to the host's own handler if it has one; otherwise
      // restore the default action (terminate) by re-raising.
      process.removeListener(sig, handler);
      if (process.listenerCount(sig) === 0) process.kill(process.pid, sig);
    };
    process.on(sig, handler);
  }
}

function killSandboxProcess(
  child: ChildProcess,
  signal: NodeJS.Signals,
): void {
  killProcessTree(child, signal);
}

function requiresSandboxEntry(
  definition: AgentDefinition,
  inheritedRequired: boolean,
): boolean {
  const sandboxRequired = inheritedRequired || definition.sandbox === "required";
  for (const declared of definition.tools) {
    if (declared.runtime?.kind !== "subagent") {
      if (sandboxRequired) return true;
      continue;
    }
    const child = declared.runtime.definition;
    if (child !== null && typeof child === "object" &&
        (child as { kind?: unknown }).kind === "agent" &&
        requiresSandboxEntry(child as AgentDefinition, sandboxRequired)) {
      return true;
    }
  }
  return false;
}

/**
 * Refuse caller-supplied build identity, plan, or routing.
 *
 * Exported for the other public entry points that forward caller options into
 * `runAgentInternal` (`./code`): the guard belongs to whoever accepts the
 * untrusted object, and it must run BEFORE any session-internal field is merged
 * in, or the session's own plan and route would trip it.
 */
export function rejectInternalRunOptions(options: RunOptions): void {
  const value = options as Record<string, unknown>;
  if ("buildIdentity" in value || "efficiencyPlan" in value ||
      "lockedBuild" in value || "candidatePlan" in value || "caveRoute" in value ||
      "invocationTrace" in value || "invocationState" in value) {
    throw new Error(
      "cave_internal_run_option: Cave Build execution is available through caveman-agent dev/build after lock validation",
    );
  }
}

function sandboxDependencyReadRoots(): string[] {
  const resolved = [
    import.meta.resolve("@earendil-works/pi-agent-core"),
    import.meta.resolve("@earendil-works/pi-ai"),
  ].map((url) => fileURLToPath(url));
  const roots = new Set<string>();
  for (const path of resolved) {
    const pnpm = path.indexOf("/node_modules/.pnpm/");
    if (pnpm >= 0) {
      roots.add(path.slice(0, pnpm + "/node_modules/.pnpm".length));
    } else {
      roots.add(dirname(dirname(path)));
    }
  }
  return [...roots];
}

function redactSandboxError(value: string): string {
  return value
    .replaceAll(process.env.HOME ?? "\u0000", "<home>")
    .replace(/[A-Za-z0-9_=-]{24,}/g, "<redacted>")
    .slice(0, 512);
}

export function resolveGatewayURL(explicit: string | undefined): string {
  return (explicit ?? process.env.CAVE_GATEWAY_URL ?? "http://127.0.0.1:8787").replace(/\/+$/, "");
}

/** Health + ownership probe for diagnostics. Never starts anything. */
export async function caveGatewayReady(
  gatewayURL: string,
  fetchImpl: typeof globalThis.fetch = globalThis.fetch,
): Promise<boolean> {
  return (await runtimeReady(gatewayURL, fetchImpl)) !== undefined;
}

const observeOnlyAnnounced = new Set<string>();

// One line, on a terminal only. Library consumers read RunResult.mode instead of
// parsing stderr, and non-interactive pipelines stay clean.
function announceObserveOnly(gatewayURL: string): void {
  if (!process.stderr.isTTY) return;
  if (observeOnlyAnnounced.has(gatewayURL)) return;
  observeOnlyAnnounced.add(gatewayURL);
  process.stderr.write(
    "cave: observe-only — engine/gateway unavailable; transforms and gateway telemetry off; " +
    "provider usage and local context estimates remain available " +
    "(npm i -g @caveman-ai/cli && caveman start)\n",
  );
}

/**
 * Decide whether this run uses the gateway. Cold machines degrade to
 * observe-only instead of failing, but a run whose evidence depends on the
 * gateway (locked build or candidate plan) refuses to degrade silently.
 * `ensureRuntime: false` means the caller manages a loopback runtime, so startup
 * and ownership probing are skipped there. It never bypasses remote TLS and
 * identity checks: provider credentials must not be sent to an unverified host.
 */
export async function resolveCaveRoute(
  gatewayURL: string,
  options: {
    cave?: "auto" | "off";
    ensureRuntime?: boolean;
    fetch?: typeof globalThis.fetch;
    caveRoute?: ResolvedCaveRoute;
    /** Internal: resolve credential payer before authorizing a USD budget. */
    billingProofRequired?: boolean;
  },
  planPresent: boolean,
): Promise<ResolvedCaveRoute> {
  if (options.caveRoute !== undefined) {
    if (!options.billingProofRequired || !options.caveRoute.useGateway ||
        options.caveRoute.providerBilling === "managed" ||
        options.caveRoute.providerBilling === "byok") {
      return options.caveRoute;
    }
    const identity = await gatewayIdentity(gatewayURL, options.fetch ?? globalThis.fetch);
    return {
      ...options.caveRoute,
      providerBilling: identity?.providerBilling ?? "unknown",
    };
  }
  if (options.cave === "off") {
    if (planPresent) {
      throw new Error(
        "cave_gateway_required_for_locked_plan: Cave Build execution routes through the Caveman gateway; run unlocked for observe-only",
      );
    }
    return { useGateway: false, providerBilling: "unknown" };
  }
  if (options.ensureRuntime === false) {
    try {
      const url = new URL(gatewayURL);
      if (isLoopbackHostname(url.hostname)) {
        const providerBilling = options.billingProofRequired
          ? (await gatewayIdentity(gatewayURL, options.fetch ?? globalThis.fetch))?.providerBilling ??
            "unknown"
          : "unknown";
        return { useGateway: true, providerBilling };
      }
      // Remote gateways still have to prove Caveman identity. `ensureCaveRuntime`
      // never starts a process for non-loopback URLs; it only enforces HTTPS and
      // performs the content-blind ownership handshake.
      const providerBilling = await ensureCaveRuntime(
        gatewayURL,
        options.fetch ?? globalThis.fetch,
      );
      return { useGateway: true, providerBilling };
    } catch (error) {
      const reason = error instanceof Error ? error.message : String(error);
      if (planPresent) {
        throw new Error(`cave_gateway_required_for_locked_plan: ${reason}`);
      }
      announceObserveOnly(gatewayURL);
      return { useGateway: false, providerBilling: "unknown" };
    }
  }
  // Memoize the probe per gateway: a server embedding calls run()
  // per request, and re-probing — which can spawn `caveman start`/`status`
  // subprocesses — on every one is unusable at load. A short TTL with a negative
  // cache bounds it to one probe per window. The cache is keyed by URL and only
  // used with the DEFAULT transport; a caller-supplied `fetch` (a test seam or a
  // special transport) always probes fresh so it is never cross-contaminated.
  const useCache = options.fetch === undefined;
  const now = Date.now();
  if (useCache) {
    const cached = gatewayProbeCache.get(gatewayURL);
    if (cached !== undefined && now - cached.at < GATEWAY_PROBE_TTL_MS) {
      if (cached.ready) return { useGateway: true, providerBilling: cached.providerBilling };
      if (planPresent) {
        throw new Error(`cave_gateway_required_for_locked_plan: ${cached.reason}`);
      }
      return { useGateway: false, providerBilling: "unknown" };
    }
  }
  const probe = useCache
    ? await sharedGatewayProbe(gatewayURL)
    : await probeGateway(gatewayURL, options.fetch ?? globalThis.fetch);
  if (!probe.ready) {
    if (planPresent) {
      throw new Error(`cave_gateway_required_for_locked_plan: ${probe.reason}`);
    }
    announceObserveOnly(gatewayURL);
    return { useGateway: false, providerBilling: "unknown" };
  }
  return { useGateway: true, providerBilling: probe.providerBilling };
}

/**
 * Per-gateway completed-result memo plus in-flight coalescing. Bounds
 * `resolveCaveRoute` to one probe/start attempt per gateway under a cold
 * concurrent burst, then one refresh per {@link GATEWAY_PROBE_TTL_MS} window.
 * Caller-supplied transports bypass both maps.
 */
const GATEWAY_PROBE_TTL_MS = 5_000;
type GatewayProviderBilling = "managed" | "byok" | "unknown";
type GatewayProbe = {
  ready: boolean;
  reason: string;
  providerBilling: GatewayProviderBilling;
};
const gatewayProbeCache = new Map<string, GatewayProbe & { at: number }>();
const gatewayProbeInflight = new Map<string, Promise<GatewayProbe>>();

async function probeGateway(
  gatewayURL: string,
  fetchImpl: typeof globalThis.fetch,
): Promise<GatewayProbe> {
  try {
    const providerBilling = await ensureCaveRuntime(gatewayURL, fetchImpl);
    return { ready: true, reason: "", providerBilling };
  } catch (error) {
    return {
      ready: false,
      reason: error instanceof Error ? error.message : String(error),
      providerBilling: "unknown",
    };
  }
}

function sharedGatewayProbe(gatewayURL: string): Promise<GatewayProbe> {
  const active = gatewayProbeInflight.get(gatewayURL);
  if (active !== undefined) return active;

  const probe = probeGateway(gatewayURL, globalThis.fetch).then((result) => {
    gatewayProbeCache.set(gatewayURL, { ...result, at: Date.now() });
    return result;
  });
  gatewayProbeInflight.set(gatewayURL, probe);
  void probe.finally(() => {
    if (gatewayProbeInflight.get(gatewayURL) === probe) {
      gatewayProbeInflight.delete(gatewayURL);
    }
  });
  return probe;
}

export async function ensureCaveRuntime(
  gatewayURL: string,
  fetchImpl: typeof globalThis.fetch,
): Promise<GatewayProviderBilling> {
  const url = new URL(gatewayURL);
  const loopback = isLoopbackHostname(url.hostname);
  if (!loopback) {
    // A non-loopback gateway is never taken on faith: routing there sends the
    // provider credential plus every x-cave-* header to that host, so it must
    // prove Caveman identity before it gets any traffic. Plain http cannot
    // authenticate the peer at all, and an https host that fails the
    // /health/ready identity handshake is equally unverified — both fail
    // closed here so resolveCaveRoute degrades to observe-only passthrough.
    if (url.protocol !== "https:") {
      throw new Error(
        `cave_gateway_identity_unverified: non-loopback gateway ${gatewayURL} requires https`,
      );
    }
    const identity = await gatewayIdentity(gatewayURL, fetchImpl);
    if (identity !== undefined) return identity.providerBilling;
    throw new Error(
      `cave_gateway_identity_unverified: ${gatewayURL}/health/ready did not identify as caveman-proxy`,
    );
  }
  const readyBilling = await runtimeReady(gatewayURL, fetchImpl);
  if (readyBilling !== undefined) return readyBilling;

  const command = process.env.CAVEMAN_CLI_BIN ?? "caveman";
  let startupFailure: Error | undefined;
  let invocation;
  try {
    invocation = portableInvocation(command, ["start"], { env: buildRuntimeControlEnv() });
  } catch (error) {
    throw new Error(`caveman agent: Cave Runtime failed to start (${error instanceof Error ? error.message : String(error)})`);
  }
  const child = spawn(invocation.command, [...invocation.args], {
    detached: true,
    windowsHide: true,
    stdio: "ignore",
    env: buildRuntimeControlEnv(),
  });
  child.once("error", (error) => {
    startupFailure = (error as NodeJS.ErrnoException).code === "ENOENT"
      ? new Error(
        "caveman agent: Caveman CLI not found; run npm install, then caveman setup --install",
      )
      : new Error(`caveman agent: Cave Runtime failed to start (${error.message})`);
  });
  child.once("exit", (code, signal) => {
    if (startupFailure !== undefined || code === 0) return;
    startupFailure = new Error(
      `caveman agent: Cave Runtime failed to start (caveman start exited ${signal ?? code}); run caveman setup --install`,
    );
  });
  child.unref();
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 100));
    if (startupFailure !== undefined) throw startupFailure;
    const startedBilling = await runtimeReady(gatewayURL, fetchImpl);
    if (startedBilling !== undefined) return startedBilling;
  }
  throw new Error(
    `caveman agent: Cave Runtime did not become ready at ${gatewayURL}; run caveman setup --install`,
  );
}

async function runtimeReady(
  gatewayURL: string,
  fetchImpl: typeof globalThis.fetch,
): Promise<GatewayProviderBilling | undefined> {
  const identity = await gatewayIdentity(gatewayURL, fetchImpl);
  if (identity === undefined) return undefined;
  return await localProxyOwned(new URL(gatewayURL)) ? identity.providerBilling : undefined;
}

// A gateway hostname is loopback only if the traffic can never leave the host.
// WHATWG URL returns IPv6 literals bracketed ("[::1]"), so both forms are
// checked; 127.0.0.0/8 and 0.0.0.0 all route to localhost and must not be
// treated as remote (nor as needing https).
function isLoopbackHostname(hostname: string): boolean {
  if (hostname === "localhost" || hostname === "::1" || hostname === "[::1]") return true;
  if (hostname === "0.0.0.0") return true;
  return /^127(?:\.\d{1,3}){3}$/.test(hostname);
}

async function gatewayIdentity(
  gatewayURL: string,
  fetchImpl: typeof globalThis.fetch,
): Promise<{ providerBilling: GatewayProviderBilling } | undefined> {
  try {
    // redirect: "error" — a verified identity must come from the host that was
    // asked, not one it forwards to; following a 3xx would let an attacker's
    // host bounce the probe to the real proxy and pass the handshake.
    const response = await fetchImpl(`${gatewayURL}/health/ready`, {
      signal: AbortSignal.timeout(500),
      redirect: "error",
    });
    if (!response.ok) return undefined;
    const value: unknown = await response.json();
    if (value === null || typeof value !== "object" || Array.isArray(value)) return undefined;
    const record = value as Record<string, unknown>;
    if (record.ok !== true || record.service !== "caveman-proxy" ||
        record.schema !== "caveman.proxy.health.v1" ||
        !Number.isSafeInteger(record.adapters) || Number(record.adapters) <= 0) {
      return undefined;
    }
    const providerBilling = record.billing === "managed" || record.billing === "byok"
      ? record.billing
      : "unknown";
    return { providerBilling };
  } catch {
    return undefined;
  }
}

async function localProxyOwned(url: URL): Promise<boolean> {
  const port = Number(url.port || (url.protocol === "https:" ? "443" : "80"));
  if (!Number.isSafeInteger(port) || port < 1 || port > 65_535) return false;
  for (const binary of proxyBinaryCandidates()) {
    const state = await validatedProxyState(binary, port);
    if (state) return true;
  }
  return false;
}

export function proxyBinaryCandidates(
  platform: NodeJS.Platform = process.platform,
  home = process.env.CAVEMAN_HOME ?? resolve(homedir(), ".caveman"),
): string[] {
  if (process.env.CAVEMAN_PROXY_BIN) return [process.env.CAVEMAN_PROXY_BIN];
  const binary = platform === "win32" ? "caveman-proxy.exe" : "caveman-proxy";
  return [...new Set([resolve(home, "bin", binary), binary])];
}

async function validatedProxyState(binary: string, port: number): Promise<boolean> {
  return await new Promise<boolean>((done) => {
    let settled = false;
    const stdout: Buffer[] = [];
    const finish = (value: boolean) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      done(value);
    };
    const env = buildRuntimeControlEnv();
    const invocation = portableInvocation(binary, ["status", "--json", "--port", String(port)], { env });
    const child = spawn(invocation.command, [...invocation.args], {
      env,
      stdio: ["ignore", "pipe", "ignore"],
    });
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      finish(false);
    }, 2_000);
    child.stdout.on("data", (chunk: Buffer) => {
      stdout.push(chunk);
      if (stdout.reduce((total, value) => total + value.length, 0) > 8_192) {
        child.kill("SIGKILL");
        finish(false);
      }
    });
    child.once("error", () => finish(false));
    child.once("close", (code) => {
      if (code !== 0) return finish(false);
      try {
        const state = JSON.parse(Buffer.concat(stdout).toString("utf8")) as Record<string, unknown>;
        finish(
          (state.owner === "start" || state.owner === "wrap") &&
          state.port === port &&
          Number.isSafeInteger(state.pid) && Number(state.pid) > 0 &&
          typeof state.instance_token === "string" &&
          /^[0-9a-f]{32}$/.test(state.instance_token),
        );
      } catch {
        finish(false);
      }
    });
  });
}

function assistantText(message: AssistantMessage): string {
  return message.content
    .filter((part): part is Extract<AssistantMessage["content"][number], { type: "text" }> => part.type === "text")
    .map((part) => part.text)
    .join("");
}

function errorCode(error: unknown): string {
  const message = error instanceof Error ? error.message : String(error);
  if (message.startsWith("cave_cache_prefix_drift")) return "cave_cache_prefix_drift";
  if (message.includes("credential")) return "cave_provider_credentials";
  return "cave_agent_run_failed";
}
