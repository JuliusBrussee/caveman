// RecoveryClient — minimal stdio MCP client for the existing Go `caveman-mcp`
// binary. One long-lived child per session: the retrieve anti-storm ledger lives
// in that process, so a spawn-per-call client would disarm it. CCR itself is
// durable (~/.caveman/ccr.db), so a crashed child can be respawned and still
// resolve the same handles.

import { type ChildProcess, execFile, spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { homedir } from "node:os";
import { delimiter, join } from "node:path";
import { type PortableInvocation, portableInvocation } from "./portable-command.ts";

const PROBE_TIMEOUT_MS = 2000;
const CALL_TIMEOUT_MS = 30_000;
// The handshake does NOT get the retrieve budget: ensure() is awaited inside
// session_start, so a caveman-mcp that starts but never answers initialize (an
// orphan holding ccr.db) froze Pi startup for the full 30s. Every other budget
// in this package is 750ms–6s; a timed-out handshake degrades the session to
// "no recovery available" instead.
const INIT_TIMEOUT_MS = 2000;

export type RetrieveResult = { text: string; isError: boolean };

export function resolveMcpBinary(env: NodeJS.ProcessEnv = process.env): string | undefined {
  const explicit = env.CAVEMAN_MCP_BIN?.trim();
  if (explicit) return explicit;
  const name = process.platform === "win32" ? "caveman-mcp.exe" : "caveman-mcp";
  for (const dir of (env.PATH ?? "").split(delimiter)) {
    if (dir && existsSync(join(dir, name))) return join(dir, name);
  }
  const local = join(env.CAVEMAN_HOME || join(homedir(), ".caveman"), "bin", name);
  return existsSync(local) ? local : undefined;
}

type Pending = { resolve: (value: unknown) => void; reject: (error: Error) => void; timer: NodeJS.Timeout };

export class RecoveryClient {
  private binary: string | undefined;
  private child: ChildProcess | undefined;
  private initialized = false;
  private probed: boolean | undefined;
  private ensuring: Promise<boolean> | undefined;
  private nextId = 1;
  private pending = new Map<number, Pending>();
  private buffer = "";
  private disposed = false;

  constructor(binary: string | undefined = resolveMcpBinary()) {
    this.binary = binary;
  }

  // ensure probes the binary once and brings up an initialized child. Returns
  // false (never throws) when recovery cannot hold — the caller passes through.
  async ensure(): Promise<boolean> {
    if (this.disposed || !this.binary) return false;
    if (this.child && this.initialized) return true;
    // probed/child are assigned only after an await, so N concurrent callers
    // used to run N probes and N spawns with only the LAST child tracked — the
    // rest stayed alive as orphans holding ccr.db. One in-flight bring-up,
    // shared by everyone who asks while it runs.
    this.ensuring ??= this.bringUp().finally(() => { this.ensuring = undefined; });
    return this.ensuring;
  }

  private async bringUp(): Promise<boolean> {
    if (this.probed === undefined) this.probed = await this.probe();
    if (!this.probed || this.disposed) return false;
    if (this.child && this.initialized) return true;
    return this.start();
  }

  ready(): boolean {
    return Boolean(this.child && this.initialized && !this.disposed);
  }

  async retrieve(handle: string, query: string | undefined, signal: AbortSignal | undefined): Promise<RetrieveResult> {
    if (!(await this.ensure())) {
      return { text: JSON.stringify({ error: "cave_recovery_unavailable", message: "caveman-mcp is not available this session" }), isError: true };
    }
    try {
      const result = await this.call("tools/call", {
        name: "caveman_retrieve",
        arguments: { recovery_handle: handle, ...(query ? { query } : {}) },
      }, signal);
      const value = result as { content?: Array<{ type?: string; text?: string }>; isError?: boolean };
      const text = (value.content ?? []).map((block) => (block.type === "text" && typeof block.text === "string" ? block.text : "")).join("");
      return { text, isError: Boolean(value.isError) };
    } catch (error) {
      // Transport failure ≠ content: surface the mechanism, never fabricate bytes.
      return { text: JSON.stringify({ error: "cave_recovery_transport", message: (error as Error).message }), isError: true };
    }
  }

  dispose(): void {
    this.disposed = true;
    const child = this.child;
    this.child = undefined;
    this.initialized = false;
    for (const [, entry] of this.pending) {
      clearTimeout(entry.timer);
      entry.reject(new Error("recovery client disposed"));
    }
    this.pending.clear();
    this.stop(child);
    // A bring-up still in flight spawns after this returns; start() sees the
    // disposed flag and reaps its own child, but await it here too so a caller
    // that disposes mid-ensure() cannot leave one behind.
    void this.ensuring?.then(() => { const late = this.child; this.child = undefined; this.stop(late); });
  }

  // resolveMcpBinary appends .exe on win32, but CAVEMAN_MCP_BIN is an operator
  // override that can name anything — including a .cmd shim, which execFile
  // and spawn both refuse with EFTYPE. Route through the same resolver the hook
  // bridge uses so the override is as spawnable as the default. Fail-closed:
  // callers treat a throw as "recovery cannot hold" and pass through.
  private invocation(args: string[]): PortableInvocation | undefined {
    try {
      return portableInvocation(this.binary!, args);
    } catch {
      return undefined;
    }
  }

  private probe(): Promise<boolean> {
    return new Promise((resolve) => {
      const portable = this.invocation(["version", "--json"]);
      if (!portable) return resolve(false);
      execFile(portable.command, portable.args, { timeout: PROBE_TIMEOUT_MS, encoding: "utf8" }, (error, stdout) => {
        if (error) return resolve(false);
        try {
          const parsed = JSON.parse(stdout);
          resolve(Array.isArray(parsed?.capabilities) && parsed.capabilities.includes("mcp_recovery"));
        } catch {
          resolve(false);
        }
      });
    });
  }

  private async start(): Promise<boolean> {
    let child: ChildProcess;
    try {
      const portable = this.invocation([]);
      if (!portable) return false;
      child = spawn(portable.command, portable.args, { stdio: ["pipe", "pipe", "ignore"] });
    } catch {
      return false;
    }
    this.child = child;
    this.initialized = false;
    this.buffer = "";
    child.on("error", () => this.onChildGone(child));
    child.on("exit", () => this.onChildGone(child));
    // stdin needs its own listener. Node emits 'error' ON THE STREAM in addition
    // to invoking the write callback, so when caveman-mcp dies mid-call the EPIPE
    // became an unhandled 'error' and killed the Pi host — the one thing
    // guardedBase promises never happens. The write callback in call() rejects
    // that request; this reaps the child so the next call degrades to "recovery
    // unavailable" instead of throwing. lifecycle.ts does the same for its hooks.
    child.stdin?.on("error", () => this.onChildGone(child));
    child.stdout?.setEncoding("utf8");
    child.stdout?.on("data", (chunk: string) => this.onData(chunk));
    try {
      await this.call("initialize", {
        protocolVersion: "2024-11-05",
        capabilities: {},
        clientInfo: { name: "caveman-pi-extension", version: "0.1.0" },
      }, undefined, INIT_TIMEOUT_MS);
      this.notify("notifications/initialized");
      if (this.disposed) { this.onChildGone(child); this.stop(child); return false; }
      this.initialized = true;
      return true;
    } catch {
      // A child that failed (or never finished) the handshake is unusable and
      // still holds ccr.db — reap it rather than leaving it for the next spawn
      // to collide with.
      this.onChildGone(child);
      this.stop(child);
      return false;
    }
  }

  private stop(child: ChildProcess | undefined): void {
    if (!child) return;
    try { child.stdin?.end(); } catch { /* already gone */ }
    // Deliberately NOT unref'd. On session_shutdown "quit" the event loop drains
    // straight away, so an unref'd timer never fires and a child that ignored the
    // stdin close was never signalled at all — it outlived the host still holding
    // ccr.db. Both timers are cleared the moment the child exits, so a
    // well-behaved child still costs nothing.
    const term = setTimeout(() => { try { child.kill("SIGTERM"); } catch { /* already gone */ } }, 500);
    const kill = setTimeout(() => { try { child.kill("SIGKILL"); } catch { /* already gone */ } }, 2000);
    child.once("exit", () => { clearTimeout(term); clearTimeout(kill); });
  }

  private onChildGone(child: ChildProcess): void {
    if (this.child !== child) return;
    this.child = undefined;
    this.initialized = false;
    for (const [, entry] of this.pending) {
      clearTimeout(entry.timer);
      entry.reject(new Error("caveman-mcp exited"));
    }
    this.pending.clear();
  }

  private onData(chunk: string): void {
    this.buffer += chunk;
    let newline: number;
    while ((newline = this.buffer.indexOf("\n")) !== -1) {
      const line = this.buffer.slice(0, newline).trim();
      this.buffer = this.buffer.slice(newline + 1);
      if (!line) continue;
      let message: { id?: unknown; result?: unknown; error?: { message?: string } };
      try {
        message = JSON.parse(line);
      } catch {
        continue;
      }
      if (typeof message.id !== "number") continue;
      const entry = this.pending.get(message.id);
      if (!entry) continue;
      this.pending.delete(message.id);
      clearTimeout(entry.timer);
      if (message.error) entry.reject(new Error(message.error.message || "caveman-mcp error"));
      else entry.resolve(message.result);
    }
  }

  private call(method: string, params: Record<string, unknown>, signal: AbortSignal | undefined, timeoutMs = CALL_TIMEOUT_MS): Promise<unknown> {
    const child = this.child;
    if (!child?.stdin?.writable) return Promise.reject(new Error("caveman-mcp not running"));
    if (signal?.aborted) return Promise.reject(new Error("retrieve cancelled"));
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`caveman-mcp ${method} timed out`));
      }, timeoutMs);
      timer.unref?.();
      const onAbort = () => {
        this.pending.delete(id);
        clearTimeout(timer);
        reject(new Error("retrieve cancelled"));
      };
      signal?.addEventListener("abort", onAbort, { once: true });
      this.pending.set(id, {
        resolve: (value) => { signal?.removeEventListener("abort", onAbort); resolve(value); },
        reject: (error) => { signal?.removeEventListener("abort", onAbort); reject(error); },
        timer,
      });
      child.stdin!.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n", (error) => {
        if (error) {
          this.pending.delete(id);
          clearTimeout(timer);
          signal?.removeEventListener("abort", onAbort);
          reject(error);
        }
      });
    });
  }

  private notify(method: string): void {
    try {
      this.child?.stdin?.write(JSON.stringify({ jsonrpc: "2.0", method }) + "\n");
    } catch { /* fail-open */ }
  }
}
