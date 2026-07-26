import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
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
