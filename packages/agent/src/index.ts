import type { Api, Model } from "@earendil-works/pi-ai";
import type {
  Auto,
  ContextDefinition,
  FileSource,
  MemoryDefinition,
  OutputDefinition,
  ToolDefinition,
} from "./primitives.js";
import { schema, tool } from "./primitives.js";
import {
  createConversation,
  runAgent,
  streamAgent,
  type RunOptions,
} from "./runtime.js";

export * from "./context-ir.js";
export * from "./primitives.js";
export {
  AGENT_RUN_RECEIPT_SCHEMA,
  OUTPUT_CLAMP_FLOOR_TOKENS,
  createBudgetController,
} from "./budget.js";
export type { BreakerEvent, RunBreakers } from "./breakers.js";
export { SUMMARY_SCHEMA_VERSION } from "./compaction.js";
export type { CompactionOptions, ContextSummary } from "./compaction.js";
export type {
  BudgetController,
  BudgetDenomination,
  BudgetExhaustionContext,
  BudgetExhaustionHandler,
  BudgetTranche,
  ReceiptCall,
  ReceiptCompaction,
  ReceiptTool,
  RunBudget,
  RunReceipt,
  RunStopReason,
} from "./budget.js";
export type {
  CavemanRunEvent,
  ConversationState,
  RunOptions,
  RunResult,
} from "./runtime.js";
export { CavemanRunError, createConversation, verifySandboxConformance } from "./runtime.js";
export type { MemoryStoreConfig } from "./memory-store.js";

export interface AgentDefinition {
  readonly kind: "agent";
  readonly id: string;
  readonly instructions: string | FileSource;
  readonly model: Auto | string | Model<Api>;
  readonly reasoning: "off" | "minimal" | "low" | "medium" | "high";
  readonly tools: readonly ToolDefinition[];
  readonly contexts: readonly ContextDefinition[];
  readonly memory?: MemoryDefinition;
  readonly output?: OutputDefinition;
  /**
   * Tool containment posture.
   *
   * - `"required"` (default): tool closures run in network-denied isolated
   *   Node workers imported from an immutable staged source graph, so
   *   `RunOptions.entryPath` is mandatory.
   * - `"fixture"`: trusted tests only — closures run in the host process and
   *   `effect: "write"` tools are blocked before they execute.
   * - `"host"`: explicit opt-in for interactive and coding agents whose tools
   *   need real host access. Closures run in the host process with no worker
   *   spawn and no `entryPath` requirement, and `effect: "write"` tools do
   *   execute. Effect declarations stay mandatory — host mode changes
   *   enforcement, not declaration. A host-mode agent is never lock-eligible
   *   `compile` refuses it with `cave_host_sandbox_lock_ineligible`,
   *   and locked builds for coding agents compile against fixture corpora
   *   instead. Host mode is refused under a sandbox-required ancestor so a
   *   subagent cannot escape its root's containment.
   */
  readonly sandbox: "required" | "fixture" | "host";
}

const SANDBOX_MODES: readonly AgentDefinition["sandbox"][] = [
  "required",
  "fixture",
  "host",
];

export function agent(options: {
  id: string;
  instructions: string | FileSource;
  model: Auto | string | Model<Api>;
  reasoning?: AgentDefinition["reasoning"];
  tools?: ToolDefinition[];
  contexts?: ContextDefinition[];
  memory?: MemoryDefinition;
  output?: OutputDefinition;
  sandbox?: AgentDefinition["sandbox"];
}): AgentDefinition {
  if (!/^[a-z0-9][a-z0-9_-]{0,95}$/.test(options.id)) {
    throw new Error(`caveman agent: invalid agent id ${JSON.stringify(options.id)}`);
  }
  const tools = Object.freeze([...(options.tools ?? [])]);
  if (new Set(tools.map((item) => item.name)).size !== tools.length) {
    throw new Error("caveman agent: duplicate tool name");
  }
  const sandbox = options.sandbox ?? "required";
  if (!SANDBOX_MODES.includes(sandbox)) {
    throw new Error(`caveman agent: unknown sandbox mode ${JSON.stringify(sandbox)}`);
  }
  const reserved = tools.find((item) => item.name.startsWith("cave_"));
  if (reserved) {
    throw new Error(
      `caveman agent: tool prefix cave_ is reserved by framework (${reserved.name})`,
    );
  }
  const definition: AgentDefinition = {
    kind: "agent",
    id: options.id,
    instructions: options.instructions,
    model: options.model,
    reasoning: options.reasoning ?? "low",
    tools,
    contexts: Object.freeze([...(options.contexts ?? [])]),
    sandbox,
    ...(options.memory === undefined ? {} : { memory: options.memory }),
    ...(options.output === undefined ? {} : { output: options.output }),
  };
  return Object.freeze(definition);
}

export function run(
  definition: AgentDefinition,
  input: string,
  options?: RunOptions,
) {
  return runAgent(definition, input, options);
}

export function stream(
  definition: AgentDefinition,
  input: string,
  options?: RunOptions,
) {
  return streamAgent(definition, input, options);
}

export function subagent(options: {
  name: string;
  description: string;
  agent: AgentDefinition;
  timeoutMs?: number;
  maxInputChars?: number;
  maxCalls?: number;
  /**
   * This child's wallet in USD. Under a USD-metered run the wallet is carved
   * out of the parent's **remaining** budget when the child is spawned, and its
   * unspent remainder returns to the parent when the child finishes.
   */
  maxCostUsd?: number;
  /**
   * This child's wallet in tokens — the denomination sibling of `maxCostUsd`,
   * used by a token-metered run. A token-metered run cannot fund a subagent
   * that declares no token wallet.
   */
  maxTokens?: number;
  maxContextTokens?: number;
}): ToolDefinition {
  const maxInputChars = options.maxInputChars ?? 32_768;
  if (!Number.isSafeInteger(maxInputChars) || maxInputChars <= 0) {
    throw new Error("caveman agent: subagent maxInputChars must be a positive integer");
  }
  const maxCalls = options.maxCalls ?? 1;
  const maxCostUsd = options.maxCostUsd ?? 1;
  const maxContextTokens = options.maxContextTokens ?? 128_000;
  if (!Number.isSafeInteger(maxCalls) || maxCalls <= 0) {
    throw new Error("caveman agent: subagent maxCalls must be a positive integer");
  }
  if (!Number.isFinite(maxCostUsd) || maxCostUsd <= 0) {
    throw new Error("caveman agent: subagent maxCostUsd must be positive");
  }
  if (options.maxTokens !== undefined &&
      (!Number.isSafeInteger(options.maxTokens) || options.maxTokens <= 0)) {
    throw new Error("caveman agent: subagent maxTokens must be a positive integer");
  }
  if (!Number.isSafeInteger(maxContextTokens) || maxContextTokens <= 0) {
    throw new Error("caveman agent: subagent maxContextTokens must be a positive integer");
  }
  return tool({
    name: options.name,
    description: options.description,
    input: schema.object({ task: schema.string() }),
    effect: "read",
    result: "auto",
    ...(options.timeoutMs === undefined ? {} : { timeoutMs: options.timeoutMs }),
    runtime: {
      kind: "subagent",
      definition: options.agent,
      maxInputChars,
      maxCalls,
      maxCostUsd,
      ...(options.maxTokens === undefined ? {} : { maxTokens: options.maxTokens }),
      maxContextTokens,
    },
    async execute() {
      throw new Error("cave_subagent_framework_runner_required");
    },
  });
}
