// Caveman native extension for Pi. One model-visible tool (caveman_retrieve),
// lifecycle bridged into the Caveman native runtime, gated provider routing to
// the local proxy, byte-stable Core injection, and post-tool output shrinking.
//
// Factory rule: register everything synchronously, start nothing. Pi runs
// factories for non-session invocations (model listing), so every background
// resource (proxy check, MCP child) waits for session_start.

import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { Type } from "typebox";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { HookBridge, promptDigest, taskContinuation, taskTerms, taskType } from "./lifecycle.ts";
import { MAX_CONTEXT_BYTES, additionalContextOf } from "./protocol.ts";
import { ProviderRouter } from "./provider.ts";
import { RecoveryClient } from "./recovery.ts";
import { shrinkToolResult } from "./tool-output.ts";

const HEALTH_TIMEOUT_MS = 750;
const RECOVERY_TOOL = "caveman_retrieve";

function cavemanHome(): string {
  return process.env.CAVEMAN_HOME || join(homedir(), ".caveman");
}

function gatewayUrl(): string {
  const env = process.env.CAVE_GATEWAY_URL?.trim();
  if (env) return env;
  try {
    const config = JSON.parse(readFileSync(join(homedir(), ".caveman-cloud", "config.json"), "utf8"));
    if (typeof config.gatewayUrl === "string" && config.gatewayUrl) return config.gatewayUrl;
  } catch { /* no config — default below */ }
  return "http://127.0.0.1:8787";
}

function runStateRecoveryViaMcp(gateway: string): boolean | undefined {
  let port: string;
  try {
    const url = new URL(gateway);
    port = url.port || (url.protocol === "https:" ? "443" : "80");
  } catch {
    return undefined;
  }
  try {
    const state = JSON.parse(readFileSync(join(cavemanHome(), "run", `${port}.json`), "utf8"));
    // A SIGKILL'd proxy leaves its run-state file behind by design; mirror the
    // CLI's field validation and require the recorded pid to still be alive so
    // a stale file (or a stranger on the port) can never open the gate.
    if (state?.schema !== "caveman.proxy.run.v1") return undefined;
    if (typeof state.pid !== "number" || typeof state.instance_token !== "string") return undefined;
    if (state.owner !== "wrap" && state.owner !== "start") return undefined;
    try {
      process.kill(state.pid, 0);
    } catch {
      return undefined;
    }
    return Boolean(state.recovery_via_mcp);
  } catch {
    return undefined;
  }
}

async function proxyAliveOnce(gateway: string): Promise<boolean> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), HEALTH_TIMEOUT_MS);
  try {
    const response = await fetch(`${gateway.replace(/\/+$/, "")}/health/live`, { signal: controller.signal });
    await response.body?.cancel();
    return response.ok;
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
  }
}

// A proxy the SessionStart hook just autostarted may be a few hundred ms behind
// the first probe; retry briefly before declaring the session direct.
async function proxyAlive(gateway: string): Promise<boolean> {
  for (let attempt = 0; attempt < 3; attempt++) {
    if (await proxyAliveOnce(gateway)) return true;
    await new Promise((resolve) => setTimeout(resolve, 300));
  }
  return false;
}

export default function (pi: ExtensionAPI) {
  const bridge = new HookBridge();
  const recovery = new RecoveryClient();
  let router: ProviderRouter | undefined;
  let sessionId = "default";
  let coreContext: string | undefined;
  let pendingContext: string[] = [];
  let pendingBytes = 0;
  let gateDone = false;

  const notify = (ctx: ExtensionContext | undefined, message: string, kind: "warning" | "info") => {
    if (ctx?.hasUI) ctx.ui.notify(message, kind);
    else process.stderr.write(`${message}\n`);
  };

  // Diagnostics only; CAVEMAN_PI_DEBUG=1 traces handler entry/exit to stderr.
  const debug = (message: string) => {
    if (process.env.CAVEMAN_PI_DEBUG === "1") process.stderr.write(`[caveman-pi] ${message}\n`);
  };

  // An extension exception must never crash Pi.
  const guardedBase = <H extends (...args: never[]) => unknown>(fn: H, label = fn.name || "handler"): H =>
    (async (...args: Parameters<H>) => {
      try {
        debug(`enter ${label}`);
        const result = await fn(...args);
        debug(`exit ${label}`);
        return result;
      } catch (error) {
        process.stderr.write(`caveman pi extension: ${(error as Error).message}\n`);
        return undefined;
      }
    }) as unknown as H;

  const GUARD = (label: string) => <H extends (...args: never[]) => unknown>(fn: H): H => guardedBase(fn, label);
  const GUARD_session_start = GUARD("session_start");
  const GUARD_model_select = GUARD("model_select");
  const GUARD_before_agent_start = GUARD("before_agent_start");
  const GUARD_turn_start = GUARD("turn_start");
  const GUARD_turn_end = GUARD("turn_end");
  const GUARD_tool_call = GUARD("tool_call");
  const GUARD_tool_result = GUARD("tool_result");
  const GUARD_session_before_compact = GUARD("session_before_compact");
  const GUARD_session_compact = GUARD("session_compact");
  const GUARD_session_shutdown = GUARD("session_shutdown");

  pi.registerTool({
    name: RECOVERY_TOOL,
    label: "Retrieve compressed context",
    description: "Recover exact original content from a Caveman recovery handle.",
    parameters: Type.Object({
      recovery_handle: Type.String({ description: "Exact ccr_ handle returned by Caveman or copied from a <<ccr:HANDLE>> marker." }),
      query: Type.Optional(Type.String({ description: "One broad description covering every detail needed from this handle." })),
    }),
    async execute(_toolCallId, params, signal) {
      const result = await recovery.retrieve(params.recovery_handle, params.query, signal);
      // MCP errors surface as Pi tool errors (thrown), never as fabricated content.
      if (result.isError) throw new Error(result.text || "caveman_retrieve failed");
      return { content: [{ type: "text", text: result.text }], details: { recovery_handle: params.recovery_handle } };
    },
  });

  pi.on("session_start", GUARD_session_start(async (_event: unknown, ctx: ExtensionContext) => {
    router ??= new ProviderRouter(pi, (message, kind) => notify(ctx, message, kind));
    // A new/resumed/forked session replaces the runtime; reset everything.
    gateDone = false;
    coreContext = undefined;
    pendingContext = [];
    pendingBytes = 0;
    try {
      sessionId = ctx.sessionManager.getSessionId() || "default";
    } catch {
      sessionId = "default";
    }
    const start = await bridge.call("SessionStart", { session_id: sessionId });
    coreContext = additionalContextOf(start);
    if (!start) {
      // No native runtime reachable means no Core and no lifecycle decisions;
      // routing without them would be compression theater.
      gateDone = true;
      notify(ctx, "Caveman: direct mode, no compression this session (caveman native runtime unreachable)", "warning");
      return;
    }

    // Recovery gate: route only when the proxy is alive AND it published MCP
    // recovery AND this session's recovery client actually initialized. Both
    // must be TRUE — this extension always registers caveman_retrieve, which
    // disables the proxy's server-side retrieve fallback, so a "both false"
    // session could never compress anything and must stay direct instead.
    const gateway = gatewayUrl();
    const alive = await proxyAlive(gateway);
    const published = runStateRecoveryViaMcp(gateway);
    const recoveryReady = alive ? await recovery.ensure() : false;
    gateDone = true;
    if (!alive || published === undefined) {
      notify(ctx, "Caveman: direct mode, no compression this session (local proxy not running)", "warning");
      return;
    }
    if (!published || !recoveryReady) {
      notify(ctx, "Caveman: direct mode, no compression this session (recovery not available — run `caveman doctor pi`)", "warning");
      return;
    }
    await router.openGate(gateway, ctx);
  }));

  pi.on("model_select", GUARD_model_select(async (event: { model: ExtensionContext["model"] }, ctx: ExtensionContext) => {
    // Covers /model, Ctrl+P cycling, and resume/fork restores. Before the gate
    // has run (restore fires early), routing is deferred to session_start.
    if (gateDone) await router?.apply(event.model, ctx);
  }));

  pi.on("before_agent_start", GUARD_before_agent_start(async (event: { prompt: string; systemPrompt: string }, ctx: ExtensionContext) => {
    const response = await bridge.call("UserPromptSubmit", {
      session_id: sessionId,
      model: ctx.model?.id,
      provider: ctx.model?.provider,
      prompt: promptDigest(event.prompt),
      task_type: taskType(event.prompt),
      task_terms: taskTerms(event.prompt),
      task_continuation: taskContinuation(event.prompt),
    });
    const dynamic = [additionalContextOf(response), ...pendingContext].filter(Boolean) as string[];
    pendingContext = [];
    pendingBytes = 0;
    if (!coreContext && dynamic.length === 0) return undefined;
    // Core is appended byte-stably every turn (prefix caching); dynamic context
    // rides only the turn it arrived on.
    const parts = [event.systemPrompt, coreContext, ...dynamic].filter(Boolean) as string[];
    return { systemPrompt: parts.join("\n\n") };
  }));

  pi.on("turn_start", GUARD_turn_start(() => { void bridge.call("ModelBefore", { session_id: sessionId }); }));
  pi.on("turn_end", GUARD_turn_end(async () => {
    await bridge.call("ModelAfter", { session_id: sessionId });
    void bridge.call("Stop", { session_id: sessionId });
  }));

  pi.on("tool_call", GUARD_tool_call(async (event: { toolName: string; input: Record<string, unknown> }) => {
    const response = await bridge.call("PreToolUse", {
      session_id: sessionId,
      tool_name: event.toolName,
      tool_input: event.input,
    });
    const context = additionalContextOf(response);
    // MAX_CONTEXT_BYTES caps ONE response, but these accumulate across every
    // tool call of a turn and drain only at the next before_agent_start — a
    // tool-heavy turn otherwise joins unbounded bytes into the system prompt.
    const bytes = context ? Buffer.byteLength(context, "utf8") : 0;
    if (context && pendingBytes + bytes <= MAX_CONTEXT_BYTES) {
      pendingContext.push(context);
      pendingBytes += bytes;
    }
  }));

  // caveman_retrieve's own output is the recovered ORIGINAL. Shrinking it hands
  // the model a fresh ccr:// mask (the proxy's classifyTool files it as an
  // ObjectCommandResult and masks anything past ~448 bytes), so recovery loops
  // instead of terminating — and registering this tool disabled the proxy's
  // server-side retrieve loop, so nothing else would strip it.
  pi.on("tool_result", GUARD_tool_result((event: Parameters<typeof shrinkToolResult>[2]) =>
    event.toolName === RECOVERY_TOOL ? undefined : shrinkToolResult(bridge, sessionId, event)));

  pi.on("session_before_compact", GUARD_session_before_compact(() => { void bridge.call("PreCompact", { session_id: sessionId }); }));

  pi.on("session_compact", GUARD_session_compact(async () => {
    const response = await bridge.call("PostCompact", { session_id: sessionId });
    const context = additionalContextOf(response);
    if (context) coreContext = context;
  }));

  pi.on("session_shutdown", GUARD_session_shutdown(async () => {
    await bridge.call("SessionEnd", { session_id: sessionId });
    router?.closeGate();
    recovery.dispose();
  }));
}
