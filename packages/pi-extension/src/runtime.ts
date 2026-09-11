// Shared Caveman session runtime. Pi and OMP own event/schema registration in
// their separate entries; the native hook protocol and recovery client are shared.

import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { RoutingContext, RoutingHost, RoutingModel } from "./provider.ts";
import { HookBridge, promptDigest, taskContinuation, taskTerms, taskType } from "./lifecycle.ts";
import { COMPAT_NAME_RE, MAX_CONTEXT_BYTES, additionalContextOf } from "./protocol.ts";
import { ProviderRouter } from "./provider.ts";
import { RecoveryClient } from "./recovery.ts";
import { shrinkToolResult, type ToolOutputEvent } from "./tool-output.ts";

const HEALTH_TIMEOUT_MS = 750;

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

type RunState = { recoveryViaMcp: boolean; compatUpstreams: Record<string, string> };

// compatUpstreamsOf keeps only entries that could name a real mount: a
// proxy-shaped name and an http(s) URL. A null-prototype object so a name like
// "constructor" can never resolve to something inherited.
function compatUpstreamsOf(raw: unknown): Record<string, string> {
  const out: Record<string, string> = Object.create(null);
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return out;
  for (const [name, url] of Object.entries(raw as Record<string, unknown>)) {
    if (!COMPAT_NAME_RE.test(name) || typeof url !== "string") continue;
    try {
      const protocol = new URL(url).protocol;
      if (protocol !== "http:" && protocol !== "https:") continue;
    } catch {
      continue;
    }
    out[name] = url;
  }
  return out;
}

// readRunState returns the gate inputs the running proxy published, or undefined
// when no valid run-state file for this gateway exists.
function readRunState(gateway: string): RunState | undefined {
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
    // compat_upstreams is absent in files written by an older proxy.
    return { recoveryViaMcp: Boolean(state.recovery_via_mcp), compatUpstreams: compatUpstreamsOf(state.compat_upstreams) };
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

export type CavemanContext<M extends RoutingModel> = RoutingContext<M> & {
  sessionManager: { getSessionId(): string };
  hasUI: boolean;
  ui: { notify(message: string, kind: "warning" | "info"): void };
};

export function createCavemanRuntime<M extends RoutingModel>(host: RoutingHost<M>) {
  type Session = {
    id: string;
    ctx: CavemanContext<M>;
    bridge: HookBridge;
    recovery: RecoveryClient;
    router: ProviderRouter<M>;
    core?: string;
    pending: string[];
    pendingBytes: number;
    gateDone: boolean;
  };
  let current: Session | undefined;
  let generation = 0;
  let transition = Promise.resolve();

  const notify = (ctx: CavemanContext<M>, message: string, kind: "warning" | "info") => {
    if (ctx.hasUI) ctx.ui.notify(message, kind);
    else process.stderr.write(`${message}\n`);
  };
  const guard = <A extends unknown[], R>(label: string, fn: (...args: A) => R) =>
    async (...args: A): Promise<Awaited<R> | undefined> => {
      try {
        if (process.env.CAVEMAN_PI_DEBUG === "1") process.stderr.write(`[caveman-pi] enter ${label}\n`);
        return await fn(...args);
      } catch (error) {
        process.stderr.write(`caveman extension: ${(error as Error).message}\n`);
        return undefined;
      }
    };

  // Invalidate synchronously, even while SessionStart/health/MCP awaits. Stop
  // provider overrides and recovery immediately, then serialize native hooks so
  // an old SessionEnd cannot end the newly navigated session.
  const navigate = (ctx?: CavemanContext<M>) => {
    const next = ++generation;
    const previous = current;
    current = undefined;
    previous?.recovery.dispose();
    const closed = previous?.router.closeGate(previous.ctx).catch((error: unknown) => {
      process.stderr.write(`caveman extension: ${(error as Error).message}\n`);
    });
    transition = transition.then(async () => {
      await closed;
      if (previous) await previous.bridge.call("SessionEnd", { session_id: previous.id });
      if (!ctx || next !== generation) return;
      let id = "default";
      try { id = ctx.sessionManager.getSessionId() || "default"; } catch { /* unsaved session */ }
      const state: Session = {
        id, ctx, bridge: new HookBridge(), recovery: new RecoveryClient(),
        router: new ProviderRouter(host, (message, kind) => notify(ctx, message, kind)),
        pending: [], pendingBytes: 0, gateDone: false,
      };
      current = state;
      const start = await state.bridge.call("SessionStart", { session_id: id });
      if (current !== state) return;
      state.core = additionalContextOf(start);
      if (!start) {
        state.gateDone = true;
        notify(ctx, "Caveman: direct mode, no compression this session (caveman native runtime unreachable)", "warning");
        return;
      }
      const gateway = gatewayUrl();
      const alive = await proxyAlive(gateway);
      if (current !== state) return;
      const published = readRunState(gateway);
      const recoveryReady = alive ? await state.recovery.ensure() : false;
      if (current !== state) return;
      state.gateDone = true;
      if (!alive || !published) {
        notify(ctx, "Caveman: direct mode, no compression this session (local proxy not running)", "warning");
        return;
      }
      if (!published.recoveryViaMcp || !recoveryReady) {
        notify(ctx, "Caveman: direct mode, no compression this session (recovery not available — run `caveman doctor pi`)", "warning");
        return;
      }
      await state.router.openGate(gateway, ctx, published.compatUpstreams);
    }).catch((error: unknown) => {
      process.stderr.write(`caveman extension: ${(error as Error).message}\n`);
    });
    return transition;
  };

  return {
    start: navigate,
    shutdown: () => navigate(),
    async retrieve(_toolCallId: string, params: { recovery_handle: string; query?: string }, signal?: AbortSignal) {
      const requested = generation;
      await transition;
      if (requested !== generation) throw new Error("Caveman session changed before recovery");
      const state = current;
      if (!state) throw new Error("Caveman recovery is not active");
      const result = await state.recovery.retrieve(params.recovery_handle, params.query, signal);
      if (current !== state) throw new Error("Caveman session changed during recovery");
      if (result.isError) throw new Error(result.text || "caveman_retrieve failed");
      return { content: [{ type: "text" as const, text: result.text }], details: { recovery_handle: params.recovery_handle } };
    },
    modelSelect: guard("model_select", async (model: M | undefined, ctx: CavemanContext<M>) => {
      if (current?.gateDone) await current.router.apply(model, ctx);
    }),
    beforeAgentStart: guard("before_agent_start", async (prompt: string, ctx: CavemanContext<M>) => {
      const requested = generation;
      await transition;
      if (requested !== generation) return undefined;
      const state = current;
      if (!state) return undefined;
      const response = await state.bridge.call("UserPromptSubmit", {
        session_id: state.id, model: ctx.model?.id, provider: ctx.model?.provider,
        prompt: promptDigest(prompt), task_type: taskType(prompt),
        task_terms: taskTerms(prompt), task_continuation: taskContinuation(prompt),
      });
      if (current !== state) return undefined;
      const parts = [state.core, additionalContextOf(response), ...state.pending].filter(Boolean) as string[];
      state.pending = [];
      state.pendingBytes = 0;
      return parts.length ? parts : undefined;
    }),
    turnStart: guard("turn_start", () => {
      if (current) void current.bridge.call("ModelBefore", { session_id: current.id });
    }),
    turnEnd: guard("turn_end", async () => {
      const state = current;
      if (!state) return;
      await state.bridge.call("ModelAfter", { session_id: state.id });
      if (current === state) void state.bridge.call("Stop", { session_id: state.id });
    }),
    toolCall: guard("tool_call", async (event: { toolName: string; input: object }) => {
      const state = current;
      if (!state) return;
      const response = await state.bridge.call("PreToolUse", {
        session_id: state.id, tool_name: event.toolName, tool_input: event.input,
      });
      if (current !== state) return;
      const context = additionalContextOf(response);
      const bytes = context ? Buffer.byteLength(context, "utf8") : 0;
      if (context && state.pendingBytes + bytes <= MAX_CONTEXT_BYTES) {
        state.pending.push(context);
        state.pendingBytes += bytes;
      }
    }),
    toolResult: guard("tool_result", async (event: ToolOutputEvent) => {
      const state = current;
      if (!state) return undefined;
      const result = await shrinkToolResult(state.bridge, state.id, event);
      return current === state ? result : undefined;
    }),
    beforeCompact: guard("session_before_compact", () => {
      if (current) void current.bridge.call("PreCompact", { session_id: current.id });
    }),
    compact: guard("session_compact", async () => {
      const state = current;
      if (!state) return;
      const response = await state.bridge.call("PostCompact", { session_id: state.id });
      if (current !== state) return;
      const context = additionalContextOf(response);
      if (context) state.core = context;
    }),
  };
}
