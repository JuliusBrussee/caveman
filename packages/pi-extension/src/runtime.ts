// Shared Caveman session runtime. Pi and OMP own event/schema registration in
// their separate entries; the native hook protocol and recovery client are shared.

import { closeSync, openSync, readFileSync, readSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import { publishedForwardHeadersOf, publishedUpstreamsOf } from "../../cli/src/provider-routing.ts";
import type { RoutingContext, RoutingHost, RoutingModel } from "./provider.ts";
import { HookBridge, promptDigest, taskContinuation, taskTerms, taskType } from "./lifecycle.ts";
import { MAX_CONTEXT_BYTES, additionalContextOf, isLoopbackUrl, type HookIds } from "./protocol.ts";
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

type RunState = { instanceToken: string; recoveryViaMcp: boolean; compatUpstreams: Record<string, string>; providerUpstreams: Record<string, string>; compatForwardHeaders: Record<string, string[]> };

// readRunState returns the gate inputs the running proxy published, or undefined
// when no valid run-state file for this gateway exists.
function readRunState(gateway: string): RunState | undefined {
  let port: string;
  try {
    const url = new URL(gateway);
    if (!isLoopbackUrl(gateway) || url.pathname !== "/" || url.search || url.hash || url.username || url.password) return undefined;
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
    if (!Number.isSafeInteger(state.pid) || state.pid < 1 || state.port !== Number(port)
      || typeof state.instance_token !== "string" || !state.instance_token) return undefined;
    if (state.owner !== "wrap" && state.owner !== "start") return undefined;
    try {
      process.kill(state.pid, 0);
    } catch {
      return undefined;
    }
    // compat_upstreams is absent in files written by an older proxy.
    return { instanceToken: state.instance_token, recoveryViaMcp: state.recovery_via_mcp === true, compatUpstreams: publishedUpstreamsOf(state.compat_upstreams), providerUpstreams: publishedUpstreamsOf(state.provider_upstreams), compatForwardHeaders: publishedForwardHeadersOf(state.compat_forward_headers) };
  } catch {
    return undefined;
  }
}

async function proxyAliveOnce(gateway: string, instanceToken: string): Promise<boolean> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), HEALTH_TIMEOUT_MS);
  try {
    const response = await fetch(`${gateway.replace(/\/+$/, "")}/health/live`, { signal: controller.signal, redirect: "error" });
    await response.body?.cancel();
    return response.ok && response.headers.get("x-caveman-instance") === instanceToken;
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
  }
}

// A proxy the SessionStart hook just autostarted may be a few hundred ms behind
// the first probe; retry briefly before declaring the session direct. SubagentStart
// autostarts nothing, so for a subagent a missing run-state file is final: the
// parent already ran the retry loop and warned.
async function readLiveRunState(gateway: string, mayBeStarting: boolean): Promise<RunState | undefined> {
  for (let attempt = 0; attempt < 3; attempt++) {
    const state = readRunState(gateway);
    if (state && await proxyAliveOnce(gateway, state.instanceToken)) return state;
    if (!state && !mayBeStarting) return undefined;
    await new Promise((resolve) => setTimeout(resolve, 300));
  }
  return undefined;
}

export type CavemanContext<M extends RoutingModel> = RoutingContext<M> & {
  // OMP >= 18.5.1 sets `agent` on every extension context; Pi and older OMP leave it unset.
  agent?: { kind: string; id: string; name: string };
  sessionManager: { getSessionId(): string; getSessionFile?(): string | undefined };
  hasUI: boolean;
  ui: { notify(message: string, kind: "warning" | "info"): void };
};

const HEADER_BYTES = 64 * 1024;

// The first line of an OMP session file is its header: {"type":"session","id":…}.
// shortcut: a header line over HEADER_BYTES reads as unreadable and the id comes from
// the file name instead; raise the cap if OMP session headers ever grow that large.
function sessionFileId(file: string): string | undefined {
  try {
    if (!statSync(file).isFile()) return undefined;
    const fd = openSync(file, "r");
    try {
      const buffer = Buffer.alloc(HEADER_BYTES);
      const head = buffer.toString("utf8", 0, readSync(fd, buffer, 0, HEADER_BYTES, 0));
      const header = JSON.parse(head.split("\n", 1)[0]!);
      return header?.type === "session" && typeof header.id === "string" && header.id ? header.id : undefined;
    } finally {
      closeSync(fd);
    }
  } catch {
    return undefined;
  }
}

// OMP names a top-level session file `<timestamp>_<session id>.jsonl` and writes every
// session that session spawns (task subagents, /tan clones, advisors) to
// `<that file minus .jsonl>/<agentId>.jsonl`, one directory deeper per generation.
const TOP_LEVEL_STEM = /^\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-\d{3}Z_(.+)$/;

// rootSessionId climbs a spawned session's file path to the top-level session's stem,
// by layout alone: OMP persists lazily, so an intermediate or root file may not exist
// yet. The root's header id wins; its file name carries the same id when the file is
// missing or unreadable. A path outside this layout resolves to nothing.
function rootSessionId(file: string): string | undefined {
  let stem = dirname(resolve(file));
  for (let depth = 0; depth < 8; depth++) {
    const top = TOP_LEVEL_STEM.exec(basename(stem));
    if (top) return sessionFileId(`${stem}.jsonl`) ?? top[1];
    const parent = dirname(stem);
    if (parent === stem) return undefined;
    stem = parent;
  }
  return undefined;
}

// A subagent reports under the main session's id, so Caveman mode, telemetry and the
// proxy see one session, plus its own agent_id/agent_type. The main session's id comes
// from the subagent's own file path, never from its header: for a /tan clone,
// parentSession is a session id, for a task subagent a path. With no resolvable file
// (in-memory session, unknown layout) the subagent keeps its own id.
function hookIdsOf(ctx: Pick<CavemanContext<RoutingModel>, "agent" | "sessionManager">): HookIds {
  let own = "default";
  try { own = ctx.sessionManager.getSessionId() || "default"; } catch { /* unsaved session */ }
  const agent = ctx.agent;
  if (agent?.kind !== "sub") return { session_id: own };
  let root: string | undefined;
  try {
    const file = ctx.sessionManager.getSessionFile?.();
    if (file) root = rootSessionId(file);
  } catch { /* session file unavailable */ }
  return { session_id: root ?? own, agent_id: agent.id, agent_type: agent.name };
}

export function createCavemanRuntime<M extends RoutingModel>(host: RoutingHost<M>) {
  type Session = {
    ids: HookIds;
    sub: boolean;
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

  // The parent session already told the user about direct mode and pass-through; a
  // subagent repeating it would print the same notice once per spawned agent.
  const notify = (ctx: CavemanContext<M>, message: string, kind: "warning" | "info") => {
    if (ctx.agent?.kind === "sub") return;
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
  //
  // closeGate(previous.ctx) reads previous.ctx.model at shutdown/switch time,
  // not at the ctx object's construction time — this is only correct because
  // ctx.model is a live getter over current selection state in both hosts.
  // Verified for OMP: @oh-my-pi/pi-coding-agent's real session-context
  // objects define `get model()` reading live agent state, never a captured
  // snapshot; Pi's createContext() does the same.
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
      if (previous) await previous.bridge.call(previous.sub ? "SubagentStop" : "SessionEnd", previous.ids);
      if (!ctx || next !== generation) return;
      const state: Session = {
        ids: hookIdsOf(ctx), sub: ctx.agent?.kind === "sub", ctx, bridge: new HookBridge(), recovery: new RecoveryClient(),
        router: new ProviderRouter(host, (message, kind) => notify(ctx, message, kind)),
        pending: [], pendingBytes: 0, gateDone: false,
      };
      current = state;
      // The CLI prints nothing for SubagentStart, so only an empty SessionStart
      // response means "native runtime unreachable". A subagent goes straight to the
      // run-state gate and routes through a live proxy exactly like its parent.
      const start = await state.bridge.call(state.sub ? "SubagentStart" : "SessionStart", state.ids);
      if (current !== state) return;
      state.core = additionalContextOf(start);
      if (!start && !state.sub) {
        state.gateDone = true;
        notify(ctx, "Caveman: direct mode, no compression this session (caveman native runtime unreachable)", "warning");
        return;
      }
      const gateway = gatewayUrl();
      const published = await readLiveRunState(gateway, !state.sub);
      if (current !== state) return;
      const recoveryReady = published ? await state.recovery.ensure() : false;
      if (current !== state) return;
      state.gateDone = true;
      if (!published) {
        notify(ctx, "Caveman: direct mode, no compression this session (local proxy not running)", "warning");
        return;
      }
      if (!published.recoveryViaMcp || !recoveryReady) {
        notify(ctx, "Caveman: direct mode, no compression this session (recovery not available — run `caveman doctor pi`)", "warning");
        return;
      }
      await state.router.openGate(gateway, ctx, published.compatUpstreams, published.providerUpstreams, published.compatForwardHeaders);
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
      // Provider registration changes (OMP has no model_select event at all;
      // Pi can refresh its active model without firing one) can leave the
      // route stale. Recheck before every turn, not just on model_select.
      if (state.gateDone) await state.router.apply(ctx.model, ctx);
      // A subagent has no user prompt of its own, and Core never reaches it (Claude
      // Code sends subagents no SessionStart context): only SubagentStart's context does.
      const response = state.sub ? undefined : await state.bridge.call("UserPromptSubmit", {
        ...state.ids, model: ctx.model?.id, provider: ctx.model?.provider,
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
      if (current) void current.bridge.call("ModelBefore", current.ids);
    }),
    turnEnd: guard("turn_end", async () => {
      const state = current;
      if (!state) return;
      await state.bridge.call("ModelAfter", state.ids);
      if (current === state && !state.sub) void state.bridge.call("Stop", state.ids);
    }),
    toolCall: guard("tool_call", async (event: { toolName: string; input: object }) => {
      const state = current;
      if (!state) return;
      const response = await state.bridge.call("PreToolUse", {
        ...state.ids, tool_name: event.toolName, tool_input: event.input,
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
      const result = await shrinkToolResult(state.bridge, state.ids, event, state.recovery);
      return current === state ? result : undefined;
    }),
    // A subagent's compaction is its own: the runtime keys compaction state by
    // session id, and under the parent's id it would demote the parent's evidence.
    beforeCompact: guard("session_before_compact", () => {
      if (current && !current.sub) void current.bridge.call("PreCompact", current.ids);
    }),
    compact: guard("session_compact", async () => {
      const state = current;
      if (!state || state.sub) return;
      const response = await state.bridge.call("PostCompact", state.ids);
      if (current !== state) return;
      const context = additionalContextOf(response);
      if (context) state.core = context;
    }),
  };
}
