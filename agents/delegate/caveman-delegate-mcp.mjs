#!/usr/bin/env node
// caveman-delegate-mcp — stdio MCP server exposing ONE tool: caveman_delegate.
// Runs a bounded subtask in the pi harness (measured ~4.5k-token prefix vs a
// ~30k-token minimum for a stripped Claude Code child) and returns
// the worker's report plus its provider-reported usage. Dependency-free.
//
// Env:
//   CAVE_DELEGATE_PROVIDER    pi provider id (default: openai-codex)
//   CAVE_DELEGATE_MODEL       model id (default: model= from ~/.codex/config.toml)
//   CAVE_DELEGATE_PI_BIN      pi binary (default: pi)
//   CAVE_DELEGATE_PI_DIR      PI_CODING_AGENT_DIR override (isolation/testing)
//   CAVE_DELEGATE_TIMEOUT_MS  worker timeout (default: 300000)
import { spawn } from "node:child_process";
import { mkdtempSync, readdirSync, readFileSync, existsSync, rmSync } from "node:fs";
import { tmpdir, homedir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { delegateSpawnOptions, killProcessTree, portableInvocation } from "./portable-process.mjs";

const PROTOCOL_FALLBACK = "2025-06-18";
const DEFAULT_TIMEOUT_MS = 300_000;

function delegateTimeoutMs() {
  const parsed = Number(process.env.CAVE_DELEGATE_TIMEOUT_MS ?? DEFAULT_TIMEOUT_MS);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : DEFAULT_TIMEOUT_MS;
}

function defaultModel() {
  if (process.env.CAVE_DELEGATE_MODEL) return process.env.CAVE_DELEGATE_MODEL;
  try {
    const toml = readFileSync(join(homedir(), ".codex", "config.toml"), "utf8");
    const m = toml.match(/^\s*model\s*=\s*"([^"]+)"/m);
    if (m) return m[1];
  } catch {}
  return "gpt-5.2-codex";
}

function collectUsage(sessionDir) {
  const sum = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, cost: 0 };
  let saw = false;
  try {
    for (const f of readdirSync(sessionDir)) {
      if (!f.endsWith(".jsonl")) continue;
      for (const line of readFileSync(join(sessionDir, f), "utf8").split("\n")) {
        if (!line.trim()) continue;
        let obj;
        try { obj = JSON.parse(line); } catch { continue; }
        const u = obj?.message?.usage ?? obj?.usage;
        if (!u || typeof u.input !== "number") continue;
        saw = true;
        sum.input += u.input || 0;
        sum.output += u.output || 0;
        sum.cacheRead += u.cacheRead || 0;
        sum.cacheWrite += u.cacheWrite || 0;
        sum.cost += u.cost?.total || 0;
      }
    }
  } catch {}
  return saw ? sum : null;
}

function runWorker(task, cwd) {
  return new Promise((resolve) => {
    const sessionDir = mkdtempSync(join(tmpdir(), "cave-delegate-"));
    const provider = process.env.CAVE_DELEGATE_PROVIDER || "openai-codex";
    const args = [
      "--provider", provider,
      "--model", defaultModel(),
      "--session-dir", sessionDir,
      "-p", task,
    ];
    const env = { ...process.env, CAVEMAN_DELEGATE_CHILD: "1" };
    if (process.env.CAVE_DELEGATE_PI_DIR) env.PI_CODING_AGENT_DIR = process.env.CAVE_DELEGATE_PI_DIR;
    let invocation;
    try {
      invocation = portableInvocation(process.env.CAVE_DELEGATE_PI_BIN || "pi", args, process.platform, env);
    } catch (error) {
      try { rmSync(sessionDir, { recursive: true, force: true }); } catch {}
      resolve({ code: -1, out: "", err: String(error), usage: null });
      return;
    }
    const child = spawn(invocation.command, invocation.args, {
      cwd: cwd && existsSync(cwd) ? cwd : process.cwd(),
      env,
      stdio: ["ignore", "pipe", "pipe"],
      ...delegateSpawnOptions(),
    });
    let out = "", err = "";
    child.stdout.on("data", (d) => (out += d));
    child.stderr.on("data", (d) => (err += d));
    const timer = setTimeout(() => {
      void killProcessTree(child).catch(() => {
        try { child.kill("SIGKILL"); } catch {}
      });
    }, delegateTimeoutMs());
    let settled = false;
    const finish = (code, error, includeUsage) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      const usage = includeUsage ? collectUsage(sessionDir) : null;
      try { rmSync(sessionDir, { recursive: true, force: true }); } catch {}
      resolve({ code, out: out.trim(), err: error, usage });
    };
    child.on("close", (code) => {
      finish(code ?? -1, err.trim(), true);
    });
    child.on("error", (e) => {
      finish(-1, String(e), false);
    });
  });
}

const TOOL = {
  name: "caveman_delegate",
  description:
    "Run a bounded, mechanical subtask (search, test run, log triage, bulk edit) in a token-efficient worker agent and return its report. Prefer this over spawning a subagent for bounded mechanical work.",
  inputSchema: {
    type: "object",
    properties: {
      task: { type: "string", description: "Complete, self-contained task instructions for the worker" },
      cwd: { type: "string", description: "Working directory (default: current)" },
    },
    required: ["task"],
  },
};

async function handle(msg) {
  const { id, method, params } = msg;
  if (method === "initialize") {
    return {
      protocolVersion: params?.protocolVersion || PROTOCOL_FALLBACK,
      capabilities: { tools: { listChanged: false } },
      serverInfo: { name: "caveman-delegate", version: "0.1.0" },
    };
  }
  if (method === "tools/list") return { tools: [TOOL] };
  if (method === "ping") return {};
  if (method === "tools/call") {
    if (params?.name !== TOOL.name) throw new Error(`unknown tool: ${params?.name}`);
    if (process.env.CAVEMAN_DELEGATE_CHILD === "1") throw new Error("cave_delegate_depth_exceeded");
    const task = params?.arguments?.task;
    if (!task || typeof task !== "string") throw new Error("cave_delegate_missing_task");
    const r = await runWorker(task, params?.arguments?.cwd);
    const lines = [r.out || "(worker produced no output)"];
    if (r.usage) {
      const u = r.usage;
      let usageLine = `delegate usage (measured, provider-reported): input ${u.input + u.cacheRead + u.cacheWrite} (cacheRead ${u.cacheRead}, cacheWrite ${u.cacheWrite}), output ${u.output}`;
      if (u.cost > 0) usageLine += `, cost $${u.cost.toFixed(4)}`;
      lines.push("---", usageLine);
    }
    if (r.code !== 0) {
      lines.push("---", `worker exit ${r.code}: ${r.err.slice(-500)}`);
      return { content: [{ type: "text", text: lines.join("\n") }], isError: true };
    }
    return { content: [{ type: "text", text: lines.join("\n") }] };
  }
  if (method?.startsWith("notifications/")) return undefined;
  throw Object.assign(new Error(`method not found: ${method}`), { code: -32601 });
}

const rl = createInterface({ input: process.stdin, terminal: false });
rl.on("line", async (line) => {
  if (!line.trim()) return;
  let msg;
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.id === undefined) { try { await handle(msg); } catch {} return; }
  try {
    const result = await handle(msg);
    process.stdout.write(JSON.stringify({ jsonrpc: "2.0", id: msg.id, result }) + "\n");
  } catch (e) {
    process.stdout.write(JSON.stringify({
      jsonrpc: "2.0", id: msg.id,
      error: { code: e.code || -32000, message: e.message || String(e) },
    }) + "\n");
  }
});
