import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtempSync, mkdirSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const hook = join(root, "dist", "native-hook-fast.js");
const home = mkdtempSync(join(process.env.CAVEMAN_BENCH_TMPDIR ?? tmpdir(), "caveman-native-bench-"));
let nativeHookBin = process.env.CAVEMAN_NATIVE_HOOK_BIN ?? process.env.CAVEMAN_PROXY_BIN;
if (!nativeHookBin) {
  const candidate = join(home, process.platform === "win32" ? "caveman-proxy.exe" : "caveman-proxy");
  const build = spawnSync("go", ["build", "-o", candidate, "./proxy/cmd/caveman-proxy"], {
    cwd: join(root, ".."),
    env: process.env,
    encoding: "utf8",
  });
  if (build.status === 0) nativeHookBin = candidate;
  else process.stderr.write(`native hook benchmark: Go bridge build unavailable; measuring Node fallback (${build.stderr.trim()})\n`);
}
const runDir = join(home, "run");
const socketPath = process.platform === "win32"
  ? `\\\\.\\pipe\\caveman-native-${createHash("sha256").update(resolve(home).replaceAll("/", "\\").toLowerCase()).digest("hex").slice(0, 16)}`
  : join(runDir, "native.sock");
mkdirSync(runDir, { recursive: true, mode: 0o700 });

const runtime = createServer((socket) => {
  let raw = "";
  let answered = false;
  socket.setEncoding("utf8");
  socket.on("error", () => {});
  socket.on("data", (chunk) => {
    raw += chunk;
    if (answered || !raw.includes("\n")) return;
    answered = true;
    let event = "unknown";
    try { event = JSON.parse(raw).event.type; } catch { /* hook owns fail-open behavior */ }
    socket.end(`${JSON.stringify({
      protocol_version: 1,
      action: "allow",
      decision_id: `bench-${event}`,
      visibility: "silent",
      fail_open: true,
    })}\n`);
  });
});
const route = createServer((socket) => socket.end());

function listen(server, ...args) {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(...args, resolve);
  });
}

function close(server) {
  return new Promise((resolve) => server.close(resolve));
}

function runHook(payload, env) {
  const started = process.hrtime.bigint();
  return new Promise((resolve, reject) => {
    const child = nativeHookBin
      ? spawn(nativeHookBin, ["native-hook", "claude", "--adapter", hook], { env })
      : spawn(process.execPath, [hook, "native-hook", "claude"], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("error", reject);
    child.on("exit", (code) => resolve({
      code,
      stdout,
      stderr,
      ms: Number(process.hrtime.bigint() - started) / 1e6,
    }));
    child.stdin.end(JSON.stringify(payload));
  });
}

function p95(values) {
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.max(0, Math.ceil(sorted.length * 0.95) - 1)];
}

const requested = Number.parseInt(process.env.CAVEMAN_BENCH_SAMPLES ?? "30", 10);
const samples = Number.isFinite(requested) ? Math.min(200, Math.max(10, requested)) : 30;
const warmups = 5;

try {
  await listen(runtime, socketPath);
  await listen(route, 0, "127.0.0.1");
  const address = route.address();
  if (!address || typeof address === "string") throw new Error("route benchmark listener has no TCP address");
  const env = {
    ...process.env,
    HOME: home,
    CAVEMAN_HOME: home,
    CAVEMAN_NATIVE_MODE: "safe",
    CAVEMAN_TELEMETRY: "0",
    CAVE_GATEWAY_URL: `http://127.0.0.1:${address.port}`,
  };
  const noOpPayload = {
    hook_event_name: "PreToolUse",
    session_id: "bench-hot",
    tool_name: "unknown_tool",
    tool_input: {},
  };
  const startupPayload = { hook_event_name: "SessionStart", session_id: "bench-startup" };
  for (let i = 0; i < warmups; i += 1) {
    const result = await runHook(noOpPayload, env);
    if (result.code !== 0) throw new Error(`warmup failed: ${result.stderr}`);
  }
  const noOp = [];
  for (let i = 0; i < samples; i += 1) {
    const result = await runHook(noOpPayload, env);
    if (result.code !== 0 || result.stdout !== "" || result.stderr !== "") {
      throw new Error(`no-op hook emitted intervention: code=${result.code} stdout=${JSON.stringify(result.stdout)} stderr=${JSON.stringify(result.stderr)}`);
    }
    noOp.push(result.ms);
  }
  const startup = [];
  for (let i = 0; i < samples; i += 1) {
    const result = await runHook({ ...startupPayload, session_id: `bench-startup-${i}` }, env);
    if (result.code !== 0 || !result.stdout.includes("smallest coherent change") || result.stderr !== "") {
      throw new Error(`startup hook failed: code=${result.code} stdout=${JSON.stringify(result.stdout)} stderr=${JSON.stringify(result.stderr)}`);
    }
    startup.push(result.ms);
  }
  const noOpP95 = p95(noOp);
  const startupP95 = p95(startup);
  const report = {
    schema: "caveman.native-hook-benchmark.v1",
    basis: "local_engineering_measurement_not_product_savings_evidence",
    adapter: nativeHookBin ? "caveman-proxy-native-bridge" : "node-fast-adapter-fallback",
    samples,
    warmups,
    no_op_hot_path: { p95_ms: Number(noOpP95.toFixed(3)), target_ms: 50, passed: noOpP95 < 50 },
    warm_session_startup: { p95_ms: Number(startupP95.toFixed(3)), target_ms: 300, passed: startupP95 < 300 },
    hard_pre_tool_timeout_ms: 250,
  };
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  if (process.argv.includes("--enforce") && (!report.no_op_hot_path.passed || !report.warm_session_startup.passed)) process.exitCode = 1;
} finally {
  await close(runtime);
  await close(route);
  rmSync(home, { recursive: true, force: true });
}
