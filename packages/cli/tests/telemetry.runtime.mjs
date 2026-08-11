import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { createServer as createNetServer } from "node:net";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");
const uuidRe = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function isolatedEnv(extra = {}) {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, ...extra };
  delete env.DO_NOT_TRACK;
  delete env.CAVEMAN_TELEMETRY;
  delete env.CAVEMAN_TELEMETRY_URL;
  Object.assign(env, extra);
  return { env, home, caveDir };
}

function runCli(argv, env, opts = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...argv], { env, stdio: ["pipe", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    const started = Date.now();
    let timer;
    if (opts.timeoutMs) {
      timer = setTimeout(() => {
        child.kill("SIGKILL");
        reject(new Error(`CLI timed out after ${opts.timeoutMs}ms`));
      }, opts.timeoutMs);
    }
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => {
      if (timer) clearTimeout(timer);
      resolve({ code, stdout, stderr, elapsedMs: Date.now() - started });
    });
    child.on("error", reject);
    if (opts.input !== undefined) child.stdin.end(opts.input);
    else child.stdin.end();
  });
}

function startTelemetryStub({ hang = false } = {}) {
  const posts = [];
  const sockets = new Set();
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      posts.push({ method: req.method, url: req.url, body });
      if (hang) return;
      res.writeHead(202, { "content-type": "application/json" });
      res.end("{}");
    });
  });
  server.on("connection", (socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
  });
  return { server, posts, close: () => { for (const socket of sockets) socket.destroy(); server.close(); } };
}

function listen(server) {
  return new Promise((resolve, reject) => {
    const onError = (error) => {
      server.off("listening", onListening);
      reject(error);
    };
    const onListening = () => {
      server.off("error", onError);
      resolve(server.address().port);
    };
    server.once("error", onError);
    server.listen(0, "127.0.0.1", onListening);
  });
}

async function listenOrSkip(t, stub) {
  try {
    return await listen(stub.server);
  } catch (error) {
    stub.close();
    if (error?.code === "EPERM") {
      t.skip("local HTTP server listen denied in this sandbox");
      return null;
    }
    throw error;
  }
}

test("non-TTY run does not prompt or post telemetry", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });

  const out = await runCli(["compress"], { ...env, CAVEMAN_ENGINE_BIN: join(tmpdir(), "missing-caveman-engine") }, { input: "hello" });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, "hello");
  assert.doesNotMatch(out.stderr, /Help improve Caveman|Send anonymous usage data/);
  assert.equal(stub.posts.length, 0, "telemetry must stay off in non-interactive runs unless env opts in");

  stub.close();
});

test("fresh non-TTY telemetry status is runtime-off and writes no config", async () => {
  const { env, home } = isolatedEnv();
  const out = await runCli(["telemetry", "status"], env);
  assert.equal(out.code, 0, out.stderr);
  const status = JSON.parse(out.stdout);
  assert.deepEqual(
    { state: status.state, enabled: status.enabled, source: status.source, anonymous_id: status.anonymous_id },
    { state: "off", enabled: false, source: "runtime", anonymous_id: "none" },
  );
  assert.throws(() => readFileSync(join(home, ".caveman-cloud", "config.json")), /ENOENT/);
});

test("telemetry on is the explicit persisted opt-in", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env, home } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });

  const out = await runCli(["telemetry", "on"], env);
  assert.equal(out.code, 0, out.stderr);
  const result = JSON.parse(out.stdout);
  assert.equal(result.telemetry, "on");
  assert.match(result.anonymous_id, uuidRe);
  const config = JSON.parse(readFileSync(join(home, ".caveman-cloud", "config.json"), "utf8"));
  assert.equal(config.telemetry.enabled, true);
  assert.equal(config.telemetry.anonymousId, result.anonymous_id);
  assert.equal(config.telemetry.promptVersion, 3);
  assert.ok(stub.posts.length >= 1, "explicit opt-in should emit consent telemetry");

  stub.close();
});

// runCliPty runs the CLI under a real pty via script(1), proving interactive
// commands follow the same default-off contract as automation.
function runCliPty(argv, env) {
  const cmd = process.platform === "darwin"
    ? ["script", ["-q", "/dev/null", "node", cli, ...argv]]
    : ["script", ["-qec", ["node", cli, ...argv].map((part) => `'${part}'`).join(" "), "/dev/null"]];
  return new Promise((resolve) => {
    let child;
    try {
      // stdin must be a real fd, not a socketpair: macOS script(1) runs
      // tcgetattr on it and dies with "Operation not supported on socket".
      child = spawn(cmd[0], cmd[1], { env, stdio: ["ignore", "pipe", "pipe"] });
    } catch {
      resolve(null);
      return;
    }
    let output = "";
    const timer = setTimeout(() => child.kill("SIGKILL"), 30000);
    child.stdout.on("data", (d) => (output += d));
    child.stderr.on("data", (d) => (output += d));
    child.on("exit", (code) => {
      clearTimeout(timer);
      resolve({ code, output });
    });
    child.on("error", () => {
      clearTimeout(timer);
      resolve(null);
    });
  });
}

test("interactive first command stays off without persisting an identifier", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env, home } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });

  const first = await runCliPty(["tools", "config", "get"], env);
  if (first === null || first.code !== 0) {
    stub.close();
    t.skip("script(1) pty unavailable in this environment");
    return;
  }
  assert.doesNotMatch(first.output, /anonymous usage stats on/);
  let cfg = {};
  try {
    cfg = JSON.parse(readFileSync(join(home, ".caveman-cloud", "config.json"), "utf8"));
  } catch {
    // No config file is expected on a fresh default-off install.
  }
  assert.ok(!("telemetry" in cfg), "default-off must not mint consent or an anonymous id");
  assert.equal(stub.posts.length, 0, "default-off interactive command must not post");

  stub.close();
});

test("non-TTY run never persists a telemetry decision", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env, home } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });

  const out = await runCli(["compress"], { ...env, CAVEMAN_ENGINE_BIN: join(tmpdir(), "missing-caveman-engine") }, { input: "hello" });
  assert.equal(out.code, 0, out.stderr);
  assert.doesNotMatch(out.stderr, /anonymous usage stats on/);
  let persisted = {};
  try {
    persisted = JSON.parse(readFileSync(join(home, ".caveman-cloud", "config.json"), "utf8"));
  } catch {
    // no config written at all is the expected outcome
  }
  assert.ok(!("telemetry" in persisted), "automation must never mint consent or an anonymous id");
  assert.equal(stub.posts.length, 0);

  stub.close();
});

test("persisted v1 opt-out remains authoritative", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env, home } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });
  const configDir = join(home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  const optOut = { enabled: false, decidedAt: "2026-07-03T00:00:00.000Z", promptVersion: 1 };
  writeFileSync(join(configDir, "config.json"), JSON.stringify({ telemetry: optOut }));

  const out = await runCli(["telemetry", "status"], env);
  assert.equal(out.code, 0, out.stderr);
  const status = JSON.parse(out.stdout);
  assert.equal(status.state, "off", "an old explicit No must never be flipped by the new default");
  const cfg = JSON.parse(readFileSync(join(configDir, "config.json"), "utf8"));
  assert.deepEqual(cfg.telemetry, optOut, "the v1 decision must not be rewritten");
  assert.equal(stub.posts.length, 0);

  stub.close();
});

test("welcome degrades to a silent no-op without a TTY", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });

  const out = await runCli(["welcome"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.doesNotMatch(out.stderr, /caveman|scanning|would have cut/i, "non-TTY welcome prints nothing");

  stub.close();
});

test("DO_NOT_TRACK=1 overrides CAVEMAN_TELEMETRY=1", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env } = isolatedEnv({
    CAVEMAN_TELEMETRY: "1",
    DO_NOT_TRACK: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry`,
  });

  const out = await runCli(["version"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(stub.posts.length, 0, "DO_NOT_TRACK must suppress env opt-in telemetry");

  stub.close();
});

test("CAVEMAN_TELEMETRY=1 emits one allowlisted command_run event", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env } = isolatedEnv({
    CAVEMAN_TELEMETRY: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry`,
  });

  const out = await runCli(["version", "leaky-argv-sentinel", "/tmp/secret-path"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(stub.posts.length, 1, "exactly one telemetry POST");
  assert.doesNotMatch(stub.posts[0].body, /leaky-argv-sentinel|secret-path/, "payload must not contain raw argv strings");
  const events = JSON.parse(stub.posts[0].body);
  assert.equal(events.length, 1);
  assert.equal(events[0].schema, "cli/v1");
  assert.equal(events[0].event, "command_run");
  assert.equal(events[0].command, "version");
  assert.match(events[0].anonymous_id, uuidRe);
  assert.equal(events[0].exit_class, "ok");
  assert.equal(typeof events[0].duration_ms, "number");

  stub.close();
});

test("local wrap emits measured engine_session aggregates and real child outcome", async (t) => {
  const stub = startTelemetryStub();
  const telemetryPort = await listenOrSkip(t, stub);
  if (telemetryPort === null) return;
  const gateway = createNetServer(() => {});
  let gatewayPort;
  try {
    gatewayPort = await listen(gateway);
  } catch (error) {
    stub.close();
    if (error?.code === "EPERM") {
      t.skip("local TCP listener denied in this sandbox");
      return;
    }
    throw error;
  }
  const { env, caveDir } = isolatedEnv({
    CAVEMAN_TELEMETRY: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${telemetryPort}/telemetry`,
    CAVE_GATEWAY_URL: `http://127.0.0.1:${gatewayPort}`,
    NO_COLOR: "1",
  });
  const binDir = join(caveDir, "bin");
  const runDir = join(caveDir, "run");
  mkdirSync(binDir, { recursive: true });
  mkdirSync(runDir, { recursive: true });
  const proxyOwner = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], { stdio: "ignore" });
  const runState = {
    schema: "caveman.proxy.run.v1",
    pid: proxyOwner.pid,
    port: gatewayPort,
    listen: `127.0.0.1:${gatewayPort}`,
    mode: "compress",
    owner: "wrap",
    recovery_via_mcp: false,
    instance_token: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
    started_at: new Date().toISOString(),
    version: "test",
  };
  writeFileSync(join(runDir, `${gatewayPort}.json`), JSON.stringify(runState));
  const proxy = join(binDir, "caveman-proxy");
  writeFileSync(proxy, `#!/usr/bin/env node
const fs = require("node:fs"), path = require("node:path");
if (process.argv[2] === "version") { console.log(JSON.stringify({version:"test",schema:"caveman.proxy.run.v1",capabilities:["run_state"]})); process.exit(0); }
if (process.argv[2] === "status") { const p = process.argv[process.argv.indexOf("--port") + 1]; console.log(fs.readFileSync(path.join(process.env.CAVEMAN_HOME,"run",p+".json"),"utf8")); process.exit(0); }
if (process.argv[2] === "stats" && process.argv.includes("--recent")) { console.log("[]"); process.exit(0); }
if (process.argv[2] === "stats") { console.log(JSON.stringify({spans:12,tokens_in:480000,requests_eligible_for_compression:10,compression_tokens_before:210000,compression_tokens_after:90000,compression_tokens_saved:120000,cached_input_tokens:60000,cache_creation_input_tokens:20000,cache_bust_requests:2,headline_compression_refused:false,basis:"inferred"})); process.exit(0); }
process.exit(1);
`, { mode: 0o755 });
  const agent = join(binDir, "fixture-agent");
  writeFileSync(agent, "#!/bin/sh\nexit 7\n", { mode: 0o755 });
  env.CAVEMAN_PROXY_BIN = proxy;
  env.PATH = `${binDir}:${env.PATH}`;
  t.after(() => {
    if (proxyOwner.exitCode === null && proxyOwner.signalCode === null) proxyOwner.kill("SIGTERM");
    gateway.close();
    stub.close();
  });

  const out = await runCli(["wrap", "fixture-agent", "secret-argv-sentinel"], env, { timeoutMs: 5000 });
  assert.equal(out.code, 7, out.stderr);
  assert.equal(stub.posts.length, 1, "wrapped command outcome + Engine measurement must share one batch");
  const events = stub.posts.flatMap((post) => JSON.parse(post.body));
  const command = events.find((event) => event.event === "command_run");
  const session = events.find((event) => event.event === "engine_session");
  assert.ok(command, `missing command_run: ${JSON.stringify(events)}`);
  assert.equal(command.exit_class, "error", "wrapped child failure must not be logged as success");
  assert.ok(session, `missing engine_session: ${JSON.stringify(events)}`);
  assert.equal(session.exit_class, "error");
  assert.equal(session.measurement_ok, true);
  assert.equal(session.measurement_mode, "compress");
  assert.equal(session.requests_observed, 12);
  assert.equal(session.input_tokens_observed, 480000);
  assert.equal(session.compression_eligible_requests, 10);
  assert.equal(session.compression_tokens_saved, 120000);
  assert.equal(session.compression_pair_known, true);
  assert.equal(session.cache_read_tokens, 60000);
  assert.equal(session.cache_write_tokens, 20000);
  assert.doesNotMatch(JSON.stringify(session), /secret-argv-sentinel|fixture-agent\/secret|prompt|completion|provider|model|path/i);
});

test("telemetry POST timeout does not hold the CLI past roughly two seconds", async (t) => {
  const stub = startTelemetryStub({ hang: true });
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env } = isolatedEnv({
    CAVEMAN_TELEMETRY: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry`,
  });

  const out = await runCli(["version"], env, { timeoutMs: 5000 });
  assert.equal(out.code, 0, out.stderr);
  assert.ok(out.elapsedMs < 2500, `CLI should exit after AbortSignal timeout, elapsed=${out.elapsedMs}ms`);
  assert.equal(stub.posts.length, 1, "the hung endpoint should still receive the attempted POST");

  stub.close();
});

test("logout preserves telemetry config", async () => {
  const { env, home } = isolatedEnv({ CAVE_NO_KEYCHAIN: "1" });
  const configDir = join(home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  const telemetry = {
    enabled: true,
    anonymousId: "123e4567-e89b-12d3-a456-426614174000",
    decidedAt: "2026-07-03T00:00:00.000Z",
    promptVersion: 1,
  };
  writeFileSync(join(configDir, "config.json"), JSON.stringify({
    baseURL: "http://localhost:8080",
    token: "legacy-token",
    tokenStore: "file",
    gatewayUrl: "https://gateway.example.com",
    telemetry,
    futureField: { keep: true },
  }, null, 2));

  const out = await runCli(["logout"], env);
  assert.equal(out.code, 0, out.stderr);
  const cfg = JSON.parse(readFileSync(join(configDir, "config.json"), "utf8"));
  assert.deepEqual(cfg.telemetry, telemetry, "logout must not wipe consent");
  assert.deepEqual(cfg.futureField, { keep: true }, "saveConfig must preserve unknown config fields");
  assert.ok(!("token" in cfg), "logout still clears legacy inline token");
  assert.ok(!("gatewayUrl" in cfg), "logout still clears managed gateway URL");
});
