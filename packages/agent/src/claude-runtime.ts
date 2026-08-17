import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import {
  createSdkMcpServer,
  query as claudeQuery,
  tool as claudeTool,
  type Options as ClaudeSDKOptions,
  type SDKMessage,
  type SDKResultMessage,
} from "@anthropic-ai/claude-agent-sdk";
import { Value } from "typebox/value";
import { z } from "zod";
import type { AgentDefinition } from "./index.js";
import { ReceiptRecorder } from "./budget.js";
import { contextBill, sha256, stableStringify } from "./context-ir.js";
import { lowerAgentContext, validateProviderUsage } from "./execution-kernel.js";
import {
  assembleSystemPrompt,
  CavemanRunError,
  createHarnessToolExecutor,
  prepareHarnessToolSandbox,
  resolveCaveRoute,
  resolveGatewayURL,
  type RunOptions,
  type RunResult,
} from "./runtime.js";
import {
  CLAUDE_CODE_VERSION,
  FRAMEWORK_VERSION,
} from "./runtime-identity.js";

export interface ClaudeRunOptions {
  rootDir?: string;
  entryPath?: string;
  gatewayURL?: string;
  ensureRuntime?: boolean;
  /** Same contract as `RunOptions.cave`. Claude runs are always unlocked, so an
   * unreachable local gateway degrades to observe-only instead of failing. */
  cave?: "auto" | "off";
  fetch?: typeof globalThis.fetch;
  engineBin?: string;
  signal?: AbortSignal;
  sandboxProfile?: RunOptions["sandboxProfile"];
  claudeCodePath?: string;
  maxTurns?: number;
  maxBudgetUsd?: number;
}

type ClaudeQuery = AsyncIterable<SDKMessage> & { close?: () => void };
type ClaudeQueryFn = (input: {
  prompt: string;
  options?: ClaudeSDKOptions;
}) => ClaudeQuery;

interface ClaudeInternalRunOptions extends ClaudeRunOptions {
  lockedBuild?: unknown;
  candidatePlan?: unknown;
  queryFn?: ClaudeQueryFn;
}

/** Claude Agent SDK lane. Public calls are unlocked and never claim a Cave Build. */
export async function runClaudeAgent(
  definition: AgentDefinition,
  input: string,
  options: ClaudeRunOptions = {},
): Promise<RunResult> {
  rejectInternalOptions(options);
  return runClaudeAgentWithOptions(definition, input, options);
}

/** CLI/test lane. Locked execution fails closed before provider use. */
export async function runClaudeAgentInternal(
  definition: AgentDefinition,
  input: string,
  options: ClaudeInternalRunOptions,
): Promise<RunResult> {
  return runClaudeAgentWithOptions(definition, input, options);
}

async function runClaudeAgentWithOptions(
  definition: AgentDefinition,
  input: string,
  options: ClaudeInternalRunOptions,
): Promise<RunResult> {
  if (options.lockedBuild !== undefined && options.candidatePlan !== undefined) {
    throw new Error("cave_execution_authorization_ambiguous");
  }
  if (options.candidatePlan !== undefined) {
    throw new Error("cave_claude_candidate_execution_unavailable");
  }
  if (options.lockedBuild !== undefined) {
    throw new Error("cave_claude_locked_execution_unavailable");
  }
  if (definition.memory !== undefined) {
    throw new Error("cave_claude_memory_bridge_unavailable");
  }
  if (definition.tools.some((item) => item.runtime?.kind === "subagent")) {
    throw new Error("cave_claude_subagent_bridge_unavailable");
  }
  const unsupportedTool = definition.tools.find((item) =>
    item.effect !== "read" || item.result !== "inline");
  if (unsupportedTool !== undefined) {
    throw new Error(`cave_claude_tool_contract_unsupported:${unsupportedTool.name}`);
  }
  if (options.maxTurns !== undefined &&
      (!Number.isSafeInteger(options.maxTurns) || options.maxTurns <= 0 || options.maxTurns > 1_000)) {
    throw new Error("cave_claude_max_turns_invalid");
  }
  if (options.maxBudgetUsd !== undefined &&
      (!Number.isFinite(options.maxBudgetUsd) || options.maxBudgetUsd <= 0)) {
    throw new Error("cave_claude_max_budget_invalid");
  }
  const rootDir = resolve(options.rootDir ?? process.cwd());
  const lowered = await lowerAgentContext(definition, { rootDir, input });
  const bill = contextBill(lowered.ir);
  const selected = resolveClaudeModel(definition, rootDir);
  const reasoningOptions = claudeReasoningOptions(
    selected,
    definition.reasoning,
    definition.output?.maxTokens,
  );
  const sandbox = await prepareHarnessToolSandbox(definition, {
    rootDir,
    ...(options.entryPath === undefined ? {} : { entryPath: options.entryPath }),
  });
  const controller = new AbortController();
  const abort = () => controller.abort(options.signal?.reason ?? new Error("cave_run_aborted"));
  options.signal?.addEventListener("abort", abort, { once: true });
  if (options.signal?.aborted) abort();
  const gatewayURL = resolveGatewayURL(options.gatewayURL);
  try {
    // Locked and candidate Claude execution already failed closed above, so this
    // lane can only ever be an unlocked run: a missing gateway degrades to
    // observe-only rather than blocking a first response.
    const { useGateway } = await resolveCaveRoute(gatewayURL, options, false);
    const instructions = assembleSystemPrompt(definition, lowered);
    const runID = crypto.randomUUID();
    const sessionID = `claude-${definition.id}-${runID}`;
    const prefixSHA256 = sha256(stableStringify({
      instructions,
      tools: definition.tools.map((item) => ({
        name: item.name,
        description: item.description,
        input: item.input,
      })),
    }));
    const toolNames = new Set<string>();
    const mcpTools = definition.tools.map((item) => {
      const converted = z.fromJSONSchema(item.input as Record<string, unknown>);
      if (!(converted instanceof z.ZodObject)) {
        throw new Error(`cave_claude_tool_schema_unsupported:${item.name}`);
      }
      const execute = createHarnessToolExecutor({
        definition,
        tool: item,
        sandbox,
        ...(options.sandboxProfile === undefined ? {} : { sandboxProfile: options.sandboxProfile }),
        ...(options.engineBin === undefined ? {} : { engineBin: options.engineBin }),
      });
      const wireName = `mcp__caveman_agent__${item.name}`;
      toolNames.add(wireName);
      return claudeTool(
        item.name,
        item.description,
        converted.shape,
        async (args) => {
          const value = await execute(args, controller.signal);
          if (!isRecord(value) || !Array.isArray(value.content)) {
            throw new Error(`cave_claude_tool_result_invalid:${item.name}`);
          }
          return value as { content: Array<{ type: "text"; text: string }> };
        },
        { alwaysLoad: true },
      );
    });
    const mcpServers: NonNullable<ClaudeSDKOptions["mcpServers"]> = {};
    if (mcpTools.length > 0) {
      mcpServers.caveman_agent = createSdkMcpServer({
        name: "caveman_agent",
        version: FRAMEWORK_VERSION,
        tools: mcpTools,
        alwaysLoad: true,
      });
    }
    const sdkOptions: ClaudeSDKOptions = {
      abortController: controller,
      cwd: rootDir,
      env: {
        ...process.env,
        // Observe-only keeps the provider's own base URL: the SDK talks to
        // Anthropic directly, no transform runs, and the gateway measures
        // nothing about this run.
        ...(useGateway ? { ANTHROPIC_BASE_URL: `${gatewayURL}/anthropic` } : {}),
        ANTHROPIC_CUSTOM_HEADERS: claudeHeaders({
          definition,
          sessionID,
          bill,
          prefixSHA256,
        }),
        CLAUDE_AGENT_SDK_CLIENT_APP: `caveman-agent/${FRAMEWORK_VERSION}`,
      },
      systemPrompt: instructions,
      model: selected,
      tools: [],
      allowedTools: [...toolNames],
      canUseTool: async (name, args) => {
        if (toolNames.has(name)) return { behavior: "allow", updatedInput: args };
        return { behavior: "deny", message: "cave_unknown_tool", interrupt: true };
      },
      mcpServers,
      settingSources: [],
      permissionMode: "dontAsk",
      persistSession: false,
      maxTurns: options.maxTurns ?? 16,
      ...reasoningOptions,
      ...(options.maxBudgetUsd === undefined ? {} : { maxBudgetUsd: options.maxBudgetUsd }),
      ...(definition.output === undefined ? {} : {
        taskBudget: { total: definition.output.maxTokens },
      }),
      ...(definition.output?.schema === undefined ? {} : {
        outputFormat: {
          type: "json_schema",
          schema: JSON.parse(stableStringify(definition.output.schema)) as Record<string, unknown>,
        },
      }),
      ...(options.claudeCodePath === undefined
        ? {}
        : { pathToClaudeCodeExecutable: options.claudeCodePath }),
    };

    const startedAt = performance.now();
    const query = (options.queryFn ?? claudeQuery)({ prompt: input, options: sdkOptions });
    let initVersion: string | undefined;
    let assistantModel: string | undefined;
    let credentialRegime: ClaudeCredentialRegime = "unknown";
    let result: SDKResultMessage | undefined;
    const toolCalls: string[] = [];
    try {
      for await (const message of query) {
        if (message.type === "system" && message.subtype === "init") {
          initVersion = message.claude_code_version;
          // The exact-pin is enforced at the FIRST message the SDK emits, before
          // it drives any model call, so a version mismatch costs nothing rather
          // than being caught only after the whole run has drained and spent
          // `finally` closes the query.
          if (initVersion !== CLAUDE_CODE_VERSION) {
            throw new Error("cave_harness_upstream_version_mismatch");
          }
          credentialRegime = claudeCredentialRegime(message.apiKeySource);
          // apiKeySource is emitted on init before the SDK drives a model call.
          // It is the credential the SDK actually selected, unlike ambient env
          // presence. A subscription or unknown regime cannot authorize a USD
          // cap because no per-token dollar charge is proven.
          if (options.maxBudgetUsd !== undefined && credentialRegime !== "metered") {
            throw new Error("cave_budget_denomination_unavailable");
          }
          assistantModel ??= message.model;
        }
        if (message.type === "assistant") {
          assistantModel = message.message.model;
          for (const block of message.message.content) {
            if (block.type === "tool_use") toolCalls.push(unprefixClaudeTool(block.name));
          }
        }
        if (message.type === "result") result = message;
      }
    } finally {
      query.close?.();
    }
    if (initVersion === undefined) {
      // The SDK never announced its version — the exact-pin cannot be proven,
      // so this fails closed the same as a mismatch.
      throw new Error("cave_harness_upstream_version_mismatch");
    }
    // Build the ledger from whatever the SDK reported — SUCCESS OR ERROR — before
    // any post-spend gate. An error subtype (error_max_turns,
    // error_during_execution, …) carries the same provider usage a success does,
    // so a run that burned turns/dollars and then errored, mismatched its model
    // identity, or failed output-schema validation still surfaces its receipt
    // instead of a bare throw that destroys the ledger. The Claude
    // Agent SDK owns its own loop, so this lane records one aggregate call with
    // the tools it drove.
    let usage: ReturnType<typeof usageFromSDKResult> | undefined;
    let usageError: Error | undefined;
    if (result !== undefined) {
      try {
        usage = usageFromSDKResult(result, selected, credentialRegime);
      } catch (error) {
        // A FAILED run can report absent or zero usage that validateProviderUsage
        // legitimately rejects (cave_provider_usage_incomplete). That rejection
        // must not bypass the receipt: record no call, remember the
        // error, and let the terminal-error gate below fire with an empty ledger.
        // A SUCCESS whose usage is unaccountable surfaces this error after the
        // gates, still carrying the receipt.
        usageError = error instanceof Error ? error : new Error(String(error));
      }
    }
    const receipt = new ReceiptRecorder();
    if (usage !== undefined) {
      receipt.recordCall({
        provider: "anthropic",
        model: selected,
        inputTokens: usage.inputTokens,
        outputTokens: usage.outputTokens,
        cacheReadTokens: usage.cacheReadTokens,
        cacheWriteTokens: usage.cacheWriteTokens,
        reasoningTokens: 0,
        estimatedUsd: usage.catalogCostUsd,
        unpriced: !usage.priced,
        usageBasis: "provider_reported",
        clampedOutputTokens: undefined,
      });
    }
    for (const name of toolCalls) receipt.recordToolCall(name);
    const carry = (code: string, message: string): CavemanRunError =>
      new CavemanRunError(code, message, receipt.build({
        runId: runID,
        agentId: definition.id,
        stopReason: "complete",
        meter: undefined,
      }));

    if (result === undefined || result.subtype !== "success" || result.is_error) {
      throw carry(
        `cave_claude_terminal_${result?.subtype ?? "missing"}`,
        `the Claude Agent SDK ended with ${result?.subtype ?? "no result"}`,
      );
    }
    if (assistantModel !== undefined && normalizeClaudeModel(assistantModel) !== selected) {
      throw carry(
        "cave_provider_model_identity_mismatch",
        `expected ${selected}, the SDK answered as ${assistantModel}`,
      );
    }
    const text = result.result;
    if (definition.output?.schema !== undefined) {
      let parsed: unknown;
      try {
        parsed = result.structured_output ?? JSON.parse(text);
      } catch {
        throw carry("cave_output_schema_invalid_json", "the SDK output was not valid JSON");
      }
      if (!Value.Check(definition.output.schema, parsed)) {
        throw carry("cave_output_schema_mismatch", "the SDK output did not match the declared schema");
      }
    }
    // Past the success gate, usage must be accountable. A success whose usage
    // validateProviderUsage rejected is a real evidence failure — surface it
    // carrying the receipt, never mask it.
    if (usage === undefined) {
      throw carry(
        usageError?.message ?? "cave_claude_terminal_missing",
        "the SDK reported no accountable usage on a successful result",
      );
    }
    if (definition.output !== undefined && usage.outputTokens > definition.output.maxTokens) {
      // The SDK already ran and spent by the time its aggregate usage is known,
      // so this post-hoc budget breach carries the receipt of what was spent
      // rather than throwing it away.
      throw carry(
        "cave_output_budget_exceeded",
        `output ${usage.outputTokens} exceeded budget ${definition.output.maxTokens}`,
      );
    }
    return {
      runId: runID,
      agentId: definition.id,
      text,
      contextIR: lowered.ir,
      contextBill: bill,
      cachePrefixSHA256: prefixSHA256,
      cacheBoundaryKnown: false,
      cacheBust: false,
      usageBasis: "provider_reported",
      inputTokens: usage.inputTokens,
      outputTokens: usage.outputTokens,
      cacheReadTokens: usage.cacheReadTokens,
      cacheWriteTokens: usage.cacheWriteTokens,
      reasoningUsageBasis: "unavailable",
      reasoningTokens: 0,
      costUsd: usage.catalogCostUsd,
      priceBasis: usage.priced ? "public_catalog" : "unpriced",
      mode: useGateway ? "optimized" : "observe-only",
      provider: "anthropic",
      model: selected,
      latencyMs: Math.round(performance.now() - startedAt),
      toolCalls,
      evaluatedTransformIDs: [],
      transformIDs: [],
      transformFailures: [],
      transformTrace: [],
      recoveryResolved: true,
      // This lane hands the loop to the Claude Agent SDK, so a run either
      // reaches its own terminal result or fails; there is no between-calls
      // stop the framework could report here.
      stopReason: "complete",
      // This lane carries no budget meter, so there is no cap to breach.
      capBreached: false,
      overspent: 0,
      receipt: receipt.build({
        runId: runID,
        agentId: definition.id,
        stopReason: "complete",
        meter: undefined,
      }),
      claimBasis: "inferred",
      unlocked: true,
    };
  } finally {
    options.signal?.removeEventListener("abort", abort);
    await sandbox.dispose();
  }
}

function claudeHeaders(input: {
  definition: AgentDefinition;
  sessionID: string;
  bill: Record<string, number>;
  prefixSHA256: string;
}): string {
  let raw = stripCaveHeaders(process.env.ANTHROPIC_CUSTOM_HEADERS);
  const headers: Record<string, string> = {
    "x-cave-agent": input.definition.id,
    "x-cave-workflow": input.definition.id,
    "x-cave-session": input.sessionID,
    "x-cave-context-bill": Object.entries(input.bill).sort(([a], [b]) => a.localeCompare(b))
      .map(([kind, tokens]) => `${kind}=${tokens}`).join(","),
    "x-cave-transforms": "caveman.pass-through.v1",
    "x-cave-transform-location": "gateway",
    "x-cave-cache-epoch": `${input.definition.id}:${input.sessionID}:${input.prefixSHA256.slice(0, 16)}`,
    "x-cave-cache-prefix-sha256": input.prefixSHA256,
  };
  for (const [name, value] of Object.entries(headers)) {
    raw = mergeClaudeHeader(raw, name, value);
  }
  return raw ?? "";
}

function stripCaveHeaders(raw: string | undefined): string | undefined {
  const kept = (raw ?? "").split(/\r\n|\n|\r/).filter((line) => {
    if (!line.trim()) return false;
    const colon = line.indexOf(":");
    const name = (colon < 0 ? line : line.slice(0, colon)).trim().toLowerCase();
    return !name.startsWith("x-cave-");
  });
  return kept.length === 0 ? undefined : kept.join("\n");
}

function mergeClaudeHeader(raw: string | undefined, name: string, value: string): string {
  if (/[:\r\n]/.test(name) || /[\r\n]/.test(value)) {
    throw new Error("cave_claude_header_invalid");
  }
  const target = name.toLowerCase();
  const kept = (raw ?? "").split(/\r\n|\n|\r/).filter((line) => {
    if (!line.trim()) return false;
    const colon = line.indexOf(":");
    return (colon < 0 ? line : line.slice(0, colon)).trim().toLowerCase() !== target;
  });
  kept.push(`${name}: ${value}`);
  return kept.join("\n");
}

function resolveClaudeModel(definition: AgentDefinition, rootDir: string): string {
  const configured = typeof definition.model === "string"
    ? definition.model
    : process.env.CAVE_MODEL ?? localModel(rootDir) ??
      (process.env.ANTHROPIC_API_KEY ? "anthropic/claude-haiku-4-5" : undefined);
  if (configured === undefined) throw new Error("cave_claude_model_required");
  const separator = configured.indexOf("/");
  if (separator < 1 || configured.slice(0, separator) !== "anthropic") {
    throw new Error("cave_claude_provider_unsupported");
  }
  return normalizeClaudeModel(configured.slice(separator + 1));
}

function localModel(rootDir: string): string | undefined {
  try {
    const parsed = JSON.parse(readFileSync(resolve(rootDir, ".caveman/provider.json"), "utf8")) as {
      model?: unknown;
    };
    return typeof parsed.model === "string" ? parsed.model : undefined;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw new Error("caveman agent: invalid .caveman/provider.json");
  }
}

function normalizeClaudeModel(model: string): string {
  return model.startsWith("anthropic/") ? model.slice("anthropic/".length) : model;
}

function claudeEffort(reasoning: AgentDefinition["reasoning"]): "low" | "medium" | "high" {
  if (reasoning === "medium") return "medium";
  if (reasoning === "high") return "high";
  return "low";
}

function claudeReasoningOptions(
  model: string,
  reasoning: AgentDefinition["reasoning"],
  outputMaxTokens: number | undefined,
): Pick<ClaudeSDKOptions, "thinking" | "effort"> {
  if (reasoning === "off") return { thinking: { type: "disabled" } };
  const capability = claudeThinkingCapability(model);
  if (capability === "adaptive") {
    return {
      thinking: { type: "adaptive" },
      effort: claudeEffort(reasoning),
    };
  }
  if (capability === "manual") {
    const budgetTokens = reasoning === "high" ? 8_192 : reasoning === "medium" ? 4_096 : 1_024;
    if (outputMaxTokens !== undefined && outputMaxTokens <= budgetTokens) {
      throw new Error("cave_claude_output_budget_too_small_for_reasoning");
    }
    return { thinking: { type: "enabled", budgetTokens } };
  }
  throw new Error(`cave_claude_reasoning_capability_unknown:${model}`);
}

function claudeThinkingCapability(model: string): "adaptive" | "manual" | "unknown" {
  if (/^claude-(?:haiku-4-5|sonnet-4-5|opus-4-(?:1|5))(?:-\d{8})?$/.test(model)) {
    return "manual";
  }
  if (/^claude-(?:sonnet|opus)-4-[678](?:-\d{8})?$/.test(model) ||
      /^claude-(?:fable|mythos|sonnet|opus)-5(?:-\d+)?$/.test(model)) {
    return "adaptive";
  }
  return "unknown";
}

// Accepts ANY result subtype: error subtypes (error_max_turns, …) carry the
// same provider `usage` a success does, so the receipt can be built from a
// failed run too.
type ClaudeCredentialRegime = "metered" | "subscription" | "unknown";

function claudeCredentialRegime(source: unknown): ClaudeCredentialRegime {
  if (source === "oauth") return "subscription";
  if (source === "user" || source === "project" || source === "org" || source === "temporary") {
    return "metered";
  }
  return "unknown";
}

function usageFromSDKResult(
  result: SDKResultMessage,
  model: string,
  credentialRegime: ClaudeCredentialRegime,
) {
  // The SDK type guarantees `usage` on every result subtype, but if the pinned
  // SDK ever returns one without it AT RUNTIME, reading fields off `undefined`
  // would throw a raw TypeError before the receipt is built — reintroducing the
  // ledger-loss class on this one path. Default to {} and let `integer()`
  // coerce the missing fields to an honest zero.
  const raw = (result.usage ?? {}) as unknown as Record<string, unknown>;
  const inputTokens = integer(raw.input_tokens);
  const outputTokens = integer(raw.output_tokens);
  const cacheReadTokens = integer(raw.cache_read_input_tokens);
  const cacheWriteTokens = integer(raw.cache_creation_input_tokens);
  const usage = validateProviderUsage({
    provider: "anthropic",
    model,
    inputTokens,
    outputTokens,
    cacheReadTokens,
    cacheWriteTokens,
    // Pinned Agent SDK exposes provider-reported aggregate output only. Its
    // thinking-token event is an approximate UI estimate, not billing evidence.
    reasoningTokens: 0,
    totalTokens: inputTokens + outputTokens + cacheReadTokens + cacheWriteTokens,
  });
  if (credentialRegime === "metered") return usage;
  // OAuth subscription runs still expose provider token counts, but catalog
  // list price is not money this run paid. Unknown auth also fails closed to
  // the honest zero rather than guessing a metered credential.
  return { ...usage, priced: false, catalogCostUsd: 0 };
}

function integer(value: unknown): number {
  return Number.isSafeInteger(value) && Number(value) >= 0 ? Number(value) : 0;
}

function unprefixClaudeTool(name: string): string {
  const prefix = "mcp__caveman_agent__";
  return name.startsWith(prefix) ? name.slice(prefix.length) : name;
}

function rejectInternalOptions(options: ClaudeRunOptions): void {
  const value = options as Record<string, unknown>;
  if ("lockedBuild" in value || "candidatePlan" in value || "queryFn" in value) {
    throw new Error("cave_execution_authorization_private");
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
