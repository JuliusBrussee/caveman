import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");
const { PROFILES } = await import(pathToFileURL(join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "agents.generated.js")).href);
const UNION_BASE_URL_VARS = ["ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "GOOGLE_GEMINI_BASE_URL"];

async function freePort() {
  return await new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      server.close(() => resolve(address.port));
    });
  });
}

function attributed(gw, id) {
  return `${gw.replace(/\/+$/, "")}/w/${id}`;
}

// A valid, future-dated wrap entitlement — the account-gated wrap only
// runs the local proxy in compress/pixel mode when config.json carries one. Tests
// that assert the compress/pixel wiring seed this so compression stays ON.
function validEntitlement() {
  return {
    wrapEntitlement: {
      entitled: true,
      plan: "free",
      telemetry_level: "metadata",
      seats_used: 1,
      seats_limit: 1,
      devices_used: 1,
      devices_limit: 3,
      evicted_device_hash: null,
      expires_at: new Date(Date.now() + 72 * 3600 * 1000).toISOString(),
    },
    wrapEntitlementFetchedAt: new Date().toISOString(),
  };
}

// Raw `caveman wrap <command>` must inject the provider base URL union into the child
// environment before exec, so the wrapped agent's LLM traffic flows through the
// local proxy with no code change. No profile means no attribution suffix.
test("raw wrap injects bare provider base URL union before exec", async () => {
  const { mkdtempSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  // Isolate HOME: gateway resolution is now dynamic (reads config.json), so a real
  // logged-in ~/.caveman-cloud/config.json carrying a persisted gatewayUrl must not
  // be able to flip this logged-out, local-proxy assertion.
  const home = mkdtempSync(join(tmpdir(), "cave-wrap-local-"));
  const childEnv = { ...process.env, HOME: home, CAVEMAN_HOME: home };
  delete childEnv.CAVE_GATEWAY_URL;
  const printEnv = `const keys=${JSON.stringify(UNION_BASE_URL_VARS)};process.stdout.write(JSON.stringify(Object.fromEntries(keys.map((k)=>[k,process.env[k]||null]))))`;
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "wrap", "node", "-e", printEnv], { env: childEnv });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });

  assert.equal(out.code, 0, `cli exited ${out.code}: ${out.stderr}`);
  const env = JSON.parse(out.stdout);
  for (const key of UNION_BASE_URL_VARS) {
    assert.equal(env[key], "http://127.0.0.1:8787", `${key} must point at the bare local proxy`);
    assert.ok(!env[key].includes("/w/"), `${key} must not be path-attributed for raw wraps`);
  }
});

// `caveman wrap` with no command is a usage error (non-zero exit), never a hang.
test("wrap with no command exits non-zero", async () => {
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "wrap"]);
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  assert.notEqual(out.code, 0, "wrap with no command must exit non-zero");
  assert.equal(out.stderr, "usage: caveman wrap [--off|--pixel] <agent> [args...]\n");
});

test("wrap default (entitled) auto-starts the local proxy in compress mode with TOON best-of", async () => {
  const { mkdtempSync, writeFileSync, readFileSync, existsSync, mkdirSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const dir = mkdtempSync(join(tmpdir(), "cave-wrap-proxy-"));
  // Isolate HOME and seed a valid entitlement so compression is enabled.
  const home = mkdtempSync(join(tmpdir(), "cave-wrap-proxy-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify(validEntitlement(), null, 2));
  const modeFile = join(dir, "proxy-env.json");
  const pidFile = join(dir, "pid.txt");
  const proxy = join(dir, "proxy.mjs");
  const agent = join(dir, "agent");
  writeFileSync(proxy, `#!/usr/bin/env node
import { createServer } from "node:http";
import { writeFileSync } from "node:fs";
if (process.argv[2] === "stats") { process.stdout.write("{}"); process.exit(0); }
if (process.argv[2] === "version" && process.argv[3] === "--json") {
  process.stdout.write('{"version":"fixture","capabilities":["run_state","native_runtime_v1","typed_ccr"]}');
  process.exit(0);
}
if (process.argv[2] === "status") { process.stdout.write('{"owner":"unknown"}'); process.exit(0); }
const url = new URL(process.env.CAVE_GATEWAY_URL);
writeFileSync(${JSON.stringify(modeFile)}, JSON.stringify({
  mode: process.env.CAVEMAN_MODE || "",
  toon: process.env.CAVE_ENGINE_TOON || "",
  observe: process.env.CAVEMAN_OBSERVE_ESTIMATE || ""
}));
writeFileSync(${JSON.stringify(pidFile)}, String(process.pid));
createServer((req, res) => res.end("ok")).listen(Number(url.port), url.hostname);
setInterval(() => {}, 1000);
`, { mode: 0o755 });
  writeFileSync(agent, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const port = await freePort();

  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    CAVEMAN_HOME: home,
    CAVE_GATEWAY_URL: `http://127.0.0.1:${port}`,
    CAVEMAN_PROXY_BIN: proxy,
    PATH: `${dir}:${process.env.PATH}`,
  };
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "wrap", "agent"], { env });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  try {
    if (existsSync(pidFile)) process.kill(Number(readFileSync(pidFile, "utf8")), "SIGTERM");
  } catch {}

  assert.equal(out.code, 0, out.stderr);
  const proxyEnv = JSON.parse(readFileSync(modeFile, "utf8"));
  assert.equal(proxyEnv.mode, "compress");
  assert.equal(proxyEnv.toon, "best-of");
  assert.equal(proxyEnv.observe, "", "an entitled session must not run the observe estimate");
});

test("wrap --off starts record mode and does not set TOON", async () => {
  const out = await runWithProxyCapture(["wrap", "--off", "agent"]);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "record");
  assert.equal(out.proxyEnv.toon, "");
});

test("wrap config can select record mode and disable TOON/MCP/browse", async () => {
  const out = await runWithProxyCapture(["wrap", "agent"], { mode: "record", toon: false, mcp: false, browse: false });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "record");
  assert.equal(out.proxyEnv.toon, "");
});

test("wrap config toon:false disables default TOON in compress mode", async () => {
  const out = await runWithProxyCapture(["wrap", "agent"], { toon: false });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "compress");
  assert.equal(out.proxyEnv.toon, "");
});

test("CAVEMAN_TOON=0 disables default TOON in compress mode", async () => {
  const out = await runWithProxyCapture(["wrap", "agent"], undefined, { CAVEMAN_TOON: "0" });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "compress");
  assert.equal(out.proxyEnv.toon, "");
});

test("CAVEMAN_WRAP_MODE=record still selects record mode", async () => {
  const out = await runWithProxyCapture(["wrap", "agent"], undefined, { CAVEMAN_WRAP_MODE: "record" });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "record");
  assert.equal(out.proxyEnv.toon, "");
});

test("malformed config does not crash wrap and still compresses", async () => {
  // An unreadable config means no readable entitlement. That changes
  // nothing about compression — it is not an account gate — and never crashes.
  const out = await runWithProxyCapture(["wrap", "agent"], "{ not json");
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "compress");
  assert.equal(out.proxyEnv.observe, "", "there is no observe-only fallback any more");
});

// With no account at all, `caveman wrap` still runs the proxy in compress
// mode, and the run banner must not blame an account for anything.
test("wrap with no account still compresses and prints the run banner", async () => {
  const out = await runWithProxyCapture(["wrap", "agent"], "{}"); // valid but entitlement-less config
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "compress", "no account must not downgrade the mode");
  assert.equal(out.proxyEnv.observe, "");
  assert.match(out.stderr, /caveman · owner: unknown · agent/);
  assert.doesNotMatch(out.stderr, /compression off until you sign in/);
  assert.doesNotMatch(out.stderr, /observe mode/);
  assert.match(out.stderr, /watching…/);
});

test("wrap rejects deleted flags with config pointer", async () => {
  for (const flag of ["--compress", "--record", "--toon", "--pixel-models", "--no-shrink", "--no-mcp", "--minimal", "--auto-recall", "--no-proxy"]) {
    const out = await new Promise((resolve, reject) => {
      const child = spawn("node", [cli, "wrap", flag, "agent"], { env: { ...process.env, NO_COLOR: "1" } });
      let stderr = "";
      child.stderr.on("data", (d) => (stderr += d));
      child.on("exit", (code) => resolve({ code, stderr }));
      child.on("error", reject);
    });
    assert.equal(out.code, 2, `${flag} exit code`);
    assert.match(out.stderr, new RegExp(`${flag.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")} moved to capability config`), `${flag} message`);
    assert.match(out.stderr, /caveman tools config get/);
  }
});

// Spawn the CLI with a stub agent on PATH that echoes one env var to stdout, so a
// test can inspect exactly what `caveman wrap <agent>` injected into the child.
async function wrapAndEchoEnv(agentId, envVar, extraEnv = {}) {
  const { mkdtempSync, writeFileSync, mkdirSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const binDir = mkdtempSync(join(tmpdir(), `cave-${agentId}-`));
  const stub = join(binDir, agentId);
  writeFileSync(stub, `#!/bin/sh\nprintf '%s' "$${envVar}"\n`, { mode: 0o755 });
  // Isolate HOME/CAVEMAN_HOME. Wrap must leave host config untouched.
  const home = mkdtempSync(join(tmpdir(), "cave-wrap-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false, browse: false } }, null, 2));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: home, PATH: `${binDir}:${process.env.PATH}` };
  delete env.CAVE_GATEWAY_URL;
  Object.assign(env, extraEnv);
  return await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "wrap", agentId], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

async function wrapAndEchoEnvJson(agentId, envVars, extraEnv = {}) {
  const { mkdtempSync, writeFileSync, mkdirSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const binDir = mkdtempSync(join(tmpdir(), `cave-${agentId}-`));
  const stub = join(binDir, agentId);
  writeFileSync(stub, `#!/usr/bin/env node
const keys = ${JSON.stringify(envVars)};
process.stdout.write(JSON.stringify(Object.fromEntries(keys.map((key) => [key, process.env[key] ?? null]))));
`, { mode: 0o755 });
  const home = mkdtempSync(join(tmpdir(), "cave-wrap-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false, browse: false } }, null, 2));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: home, PATH: `${binDir}:${process.env.PATH}` };
  delete env.CAVE_GATEWAY_URL;
  Object.assign(env, extraEnv);
  return await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "wrap", agentId], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

async function runWithProxyCapture(cliArgs, wrapConfig, extraEnv = {}) {
  const { mkdtempSync, writeFileSync, readFileSync, existsSync, mkdirSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const dir = mkdtempSync(join(tmpdir(), "cave-wrap-capture-"));
  const home = mkdtempSync(join(tmpdir(), "cave-wrap-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  // Always write config.json seeded with a valid entitlement (so the default is an
  // entitled, compressing session). A string wrapConfig is written verbatim (the
  // malformed-config case, which intentionally carries no readable entitlement).
  const configContent =
    typeof wrapConfig === "string"
      ? wrapConfig
      : JSON.stringify({ ...(wrapConfig !== undefined ? { wrap: wrapConfig } : {}), ...validEntitlement() }, null, 2);
  writeFileSync(join(home, ".caveman-cloud", "config.json"), configContent);
  const proxyEnvFile = join(dir, "proxy-env.json");
  const pidFile = join(dir, "pid.txt");
  const proxy = join(dir, "proxy.mjs");
  const agent = join(dir, "agent");
  const port = await freePort();
  writeFileSync(proxy, `#!/usr/bin/env node
import { createServer } from "node:http";
import { writeFileSync } from "node:fs";
if (process.argv[2] === "stats") { process.stdout.write("{}"); process.exit(0); }
const url = new URL(process.env.CAVE_GATEWAY_URL);
writeFileSync(${JSON.stringify(proxyEnvFile)}, JSON.stringify({
  mode: process.env.CAVEMAN_MODE || "",
  toon: process.env.CAVE_ENGINE_TOON || "",
  pixelModels: process.env.CAVE_PIXEL_MODELS || "",
  recovery: process.env.CAVEMAN_RECOVERY || "",
  observe: process.env.CAVEMAN_OBSERVE_ESTIMATE || "",
  entitled: process.env.CAVEMAN_WRAP_ENTITLED || "",
  subscription: process.env.CAVEMAN_SUBSCRIPTION_COMPRESS || "",
  mantle: process.env.CAVE_BEDROCK_MANTLE_ENABLED || ""
}));
writeFileSync(${JSON.stringify(pidFile)}, String(process.pid));
createServer((req, res) => res.end("ok")).listen(Number(url.port), url.hostname);
setInterval(() => {}, 1000);
`, { mode: 0o755 });
  writeFileSync(agent, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    CAVEMAN_HOME: home,
    CAVE_GATEWAY_URL: `http://127.0.0.1:${port}`,
    CAVEMAN_PROXY_BIN: proxy,
    PATH: `${dir}:${process.env.PATH}`,
    ...extraEnv,
  };
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...cliArgs], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
  try {
    if (existsSync(pidFile)) process.kill(Number(readFileSync(pidFile, "utf8")), "SIGTERM");
  } catch {}
  const proxyEnv = existsSync(proxyEnvFile) ? JSON.parse(readFileSync(proxyEnvFile, "utf8")) : {};
  return { ...out, proxyEnv, home };
}

// The 2026-07-25 local-wrap decision: an entitled LOCAL wrap stamps the
// account signal that lets the proxy compress subscription/OAuth logins; every
// other state must stamp it explicitly off, so an inherited env var can never grant
// the capability to an unentitled session. (honesty rule: byte-safe)
test("a compress wrap stamps no account signal at all", async () => {
  const out = await runWithProxyCapture(["wrap", "agent"]);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "compress");
  assert.equal(out.proxyEnv.entitled, "", "no account signal is stamped any more");
  assert.equal(out.proxyEnv.subscription, "", "the operator off-switch stays the operator's — wrap never sets it");
  // Recovery is still a real gate: this bare binary has no caveman MCP retrieve tool,
  // so the proxy stays pass-through for subscription traffic and the disclosure says
  // so rather than announcing compression. The claim path is covered in
  // subscription-recovery.runtime.mjs.
  assert.match(out.stderr, /stay byte-identical pass-through here/);
  assert.match(out.stderr, /caveman mcp install/);
  assert.doesNotMatch(out.stderr, /compress locally too/);
});

test("an unentitled wrap compresses exactly like an entitled one", async () => {
  const out = await runWithProxyCapture(["wrap", "agent"], "{ not json", { CAVEMAN_WRAP_ENTITLED: "1" });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "compress", "no readable entitlement must not downgrade the mode");
  assert.equal(out.proxyEnv.entitled, "", "a stray env claim is neither honored nor forwarded");
});

test("--off keeps subscription compression closed, account or not", async () => {
  const out = await runWithProxyCapture(["wrap", "--off", "agent"]);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "record");
  assert.equal(out.proxyEnv.entitled, "");
});

// `caveman start` is the sibling entry point: it launches the SAME proxy, so it must
// run the SAME account gate. It spawns a foreground proxy (stdio inherited, exit code
// forwarded), so this stub records its env and exits instead of listening.
async function runStartWithProxyCapture(config, extraEnv = {}, startArgs = []) {
  const { mkdtempSync, writeFileSync, readFileSync, existsSync, mkdirSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const dir = mkdtempSync(join(tmpdir(), "cave-start-capture-"));
  const home = mkdtempSync(join(tmpdir(), "cave-start-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  const configContent = typeof config === "string" ? config : JSON.stringify(validEntitlement(), null, 2);
  writeFileSync(join(home, ".caveman-cloud", "config.json"), configContent);
  const proxyEnvFile = join(dir, "proxy-env.json");
  const proxy = join(dir, "proxy.mjs");
  const port = 19000 + Math.floor(Math.random() * 1000);
  writeFileSync(proxy, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
writeFileSync(${JSON.stringify(proxyEnvFile)}, JSON.stringify({
  mode: process.env.CAVEMAN_MODE || "",
  entitled: process.env.CAVEMAN_WRAP_ENTITLED || "",
  listen: process.env.CAVEMAN_LISTEN || "",
  config: process.env.CAVEMAN_CONFIG || ""
}));
process.exit(0);
`, { mode: 0o755 });
  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    CAVEMAN_HOME: home,
    CAVE_GATEWAY_URL: "https://gateway.example",
    CAVEMAN_LISTEN: `127.0.0.1:${port}`,
    CAVEMAN_PROXY_BIN: proxy,
    ...extraEnv,
  };
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "start", ...startArgs], { env, stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
  const proxyEnv = existsSync(proxyEnvFile) ? JSON.parse(readFileSync(proxyEnvFile, "utf8")) : {};
  return { ...out, proxyEnv, expectedListen: `127.0.0.1:${port}` };
}

test("start owns its standalone listener even when login persisted a managed gateway", async () => {
  const out = await runStartWithProxyCapture(undefined);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.listen, out.expectedListen);
});

test("start validates and applies documented host, port, and config flags", async () => {
  const out = await runStartWithProxyCapture(undefined, {}, ["--host", "127.0.0.1", "--port", "1", "--config", "/tmp/caveman-launch.yaml"]);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.listen, "127.0.0.1:1");
  assert.equal(out.proxyEnv.config, "/tmp/caveman-launch.yaml");

  const invalid = await runStartWithProxyCapture(undefined, {}, ["--porrt", "9"]);
  assert.equal(invalid.code, 2);
  assert.match(invalid.stderr, /start \[--port 8787\]/);
  assert.deepEqual(invalid.proxyEnv, {});
});

test("a compress start stamps no account signal", async () => {
  const out = await runStartWithProxyCapture(undefined, { CAVEMAN_MODE: "compress" });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.entitled, "", "no account signal is stamped any more");
  // No agent on this machine has the caveman MCP retrieve tool installed, so the
  // recovery condition is absent and start says subscription compression is off.
  assert.match(out.stderr, /stay byte-identical pass-through here/);
  assert.doesNotMatch(out.stderr, /compress locally too/);
});

// A stray exported CAVEMAN_WRAP_ENTITLED must not reach the proxy — the variable no
// longer exists as a contract, and forwarding it would resurrect a dead gate.
test("an inherited entitlement claim is not forwarded by `caveman start`", async () => {
  const out = await runStartWithProxyCapture("{ not json", {
    CAVEMAN_MODE: "compress",
    CAVEMAN_WRAP_ENTITLED: "1",
  });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.entitled, "", "the dead account variable must not be re-stamped");
});

test("a plain record start still stamps no account signal", async () => {
  const out = await runStartWithProxyCapture(undefined, { CAVEMAN_WRAP_ENTITLED: "1" });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.entitled, "");
});

test("explicit Mantle wrap enables the opt-in proxy route", async () => {
  const out = await runWithProxyCapture(["wrap", "agent"], undefined, {
    CAVEMAN_WRAP_PROVIDER: "bedrock",
    CAVEMAN_BEDROCK_ENDPOINT: "mantle",
  });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mantle, "1");
});

test("Mantle endpoint alone does not enable the proxy route for another provider", async () => {
  const out = await runWithProxyCapture(["wrap", "agent"], undefined, {
    CAVEMAN_BEDROCK_ENDPOINT: "mantle",
  });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mantle, "");
});

test("profiled wrap attributes generic provider base URL union", async () => {
  const out = await wrapAndEchoEnvJson("claude", UNION_BASE_URL_VARS);
  assert.equal(out.code, 0, `wrap claude exited ${out.code}: ${out.stderr}`);
  const env = JSON.parse(out.stdout);
  for (const key of UNION_BASE_URL_VARS) {
    assert.equal(env[key], attributed("http://127.0.0.1:8787", "claude"), `${key} must be attributed to claude`);
  }
});

// Claude Code drops first-party-only capabilities (1M context window → ~600k
// auto-compact window) whenever ANTHROPIC_BASE_URL is not api.anthropic.com.
// The local wrap targets a byte-safe pass-through whose anthropic upstream IS
// api.anthropic.com by default, so it must assert first-party through Claude
// Code's own escape hatch or every wrapped session shrinks to 200k (#865).
const ASSUME_FIRST_PARTY = "_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL";

test("local claude wrap asserts the first-party base URL escape hatch", async () => {
  const out = await wrapAndEchoEnvJson("claude", [ASSUME_FIRST_PARTY], { [ASSUME_FIRST_PARTY]: undefined });
  assert.equal(out.code, 0, `wrap claude exited ${out.code}: ${out.stderr}`);
  assert.equal(JSON.parse(out.stdout)[ASSUME_FIRST_PARTY], "1");
});

test("a user-exported first-party assertion value is never overridden", async () => {
  const out = await wrapAndEchoEnvJson("claude", [ASSUME_FIRST_PARTY], { [ASSUME_FIRST_PARTY]: "0" });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(JSON.parse(out.stdout)[ASSUME_FIRST_PARTY], "0");
});

test("an anthropic upstream override in caveman.yaml withholds the first-party assertion", async () => {
  const { mkdtempSync, writeFileSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const dir = mkdtempSync(join(tmpdir(), "cave-yaml-"));
  const config = join(dir, "caveman.yaml");
  writeFileSync(config, "mode: record\nproviders:\n  anthropic:\n    base_url: https://claude-mirror.example.com\n");
  const out = await wrapAndEchoEnvJson("claude", [ASSUME_FIRST_PARTY], { [ASSUME_FIRST_PARTY]: undefined, CAVEMAN_CONFIG: config });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(JSON.parse(out.stdout)[ASSUME_FIRST_PARTY], null);
});

test("an explicit api.anthropic.com upstream override keeps the first-party assertion", async () => {
  const { mkdtempSync, writeFileSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const dir = mkdtempSync(join(tmpdir(), "cave-yaml-fp-"));
  const config = join(dir, "caveman.yaml");
  // Sibling provider overrides must not disturb the anthropic answer.
  writeFileSync(config, "providers:\n  openai:\n    base_url: https://openai-mirror.example.com\n  anthropic:\n    base_url: \"https://api.anthropic.com\"\n");
  const out = await wrapAndEchoEnvJson("claude", [ASSUME_FIRST_PARTY], { [ASSUME_FIRST_PARTY]: undefined, CAVEMAN_CONFIG: config });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(JSON.parse(out.stdout)[ASSUME_FIRST_PARTY], "1");
});

test("a flow-style providers block is unverifiable and withholds the assertion", async () => {
  const { mkdtempSync, writeFileSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const dir = mkdtempSync(join(tmpdir(), "cave-yaml-flow-"));
  const config = join(dir, "caveman.yaml");
  writeFileSync(config, "providers: {anthropic: {base_url: https://claude-mirror.example.com}}\n");
  const out = await wrapAndEchoEnvJson("claude", [ASSUME_FIRST_PARTY], { [ASSUME_FIRST_PARTY]: undefined, CAVEMAN_CONFIG: config });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(JSON.parse(out.stdout)[ASSUME_FIRST_PARTY], null);
});

test("managed claude wrap does not assert first-party (upstream unverifiable)", async () => {
  const out = await wrapAndEchoEnvJson("claude", [ASSUME_FIRST_PARTY], {
    [ASSUME_FIRST_PARTY]: undefined,
    CAVE_GATEWAY_URL: "https://gateway.example.com",
  });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(JSON.parse(out.stdout)[ASSUME_FIRST_PARTY], null);
});

test("gemini profile injects distinct Gemini and Vertex local routes", async () => {
  const gemini = PROFILES.find((p) => p.id === "gemini");
  assert.ok(gemini, "missing gemini profile");
  // Gemini CLI reads GOOGLE_GEMINI_BASE_URL; the legacy GEMINI_BASE_URL never existed in
  // gemini-cli and is dead — the profile must not inject it (issue #135).
  assert.equal(gemini.injection.env.GOOGLE_GEMINI_BASE_URL, "{{cave_base_url}}");
  assert.equal(gemini.injection.env.GOOGLE_VERTEX_BASE_URL, "{{cave_base_url}}/vertex");
  assert.equal(gemini.injection.env.GEMINI_BASE_URL, undefined, "GEMINI_BASE_URL is a dead var and must not be a profile-injected key");

  const out = await wrapAndEchoEnvJson("gemini", ["GOOGLE_GEMINI_BASE_URL", "GOOGLE_VERTEX_BASE_URL"]);
  assert.equal(out.code, 0, `wrap gemini exited ${out.code}: ${out.stderr}`);
  const env = JSON.parse(out.stdout);
  assert.equal(env.GOOGLE_GEMINI_BASE_URL, attributed("http://127.0.0.1:8787", "gemini"));
  assert.equal(env.GOOGLE_VERTEX_BASE_URL, `${attributed("http://127.0.0.1:8787", "gemini")}/vertex`);
});

test("managed Gemini wrap fails closed to direct provider routing", async () => {
  const out = await wrapAndEchoEnvJson("gemini", ["GOOGLE_GEMINI_BASE_URL", "GOOGLE_VERTEX_BASE_URL", "CAVE_API_KEY"], {
    CAVE_GATEWAY_URL: "https://gateway.example.com",
  });
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(JSON.parse(out.stdout), {
    GOOGLE_GEMINI_BASE_URL: null,
    GOOGLE_VERTEX_BASE_URL: null,
    CAVE_API_KEY: null,
  });
  assert.match(out.stderr, /managed Gemini CLI wrap is unsupported .* launching directly/);
});

// opencode ignores OPENAI_BASE_URL — it must be pointed at the gateway via the
// inline-config env var. `caveman wrap opencode` (local) must set
// OPENCODE_CONFIG_CONTENT to valid JSON that overrides opencode's built-in
// openai/anthropic providers' baseURL and tags requests with X-Cave-Agent.
test("wrap opencode injects OPENCODE_CONFIG_CONTENT pointing at the gateway (local)", async () => {
  const out = await wrapAndEchoEnv("opencode", "OPENCODE_CONFIG_CONTENT");
  assert.equal(out.code, 0, `wrap opencode exited ${out.code}: ${out.stderr}`);
  const cfg = JSON.parse(out.stdout);
  assert.equal(cfg.provider.openai.options.baseURL, "http://127.0.0.1:8787/w/opencode/v1", "openai baseURL must point at the attributed gateway");
  assert.equal(cfg.provider.anthropic.options.baseURL, "http://127.0.0.1:8787/w/opencode/v1", "anthropic baseURL must point at the attributed gateway");
  assert.equal(cfg.provider.openai.options.headers["X-Cave-Agent"], "opencode", "requests must be attributed to opencode");
});

// In managed mode (CAVE_GATEWAY_URL points off-loopback), wrap opencode registers
// an explicit `caveman` provider whose Bearer is the cave key and whose upstream
// key + agent ride in headers. opencode's own {env:VAR} tokens must pass through
// untouched (Caveman only renders {{cave_*}} templates).
test("wrap opencode registers the caveman provider with cave-key auth (managed)", async () => {
  const out = await wrapAndEchoEnv("opencode", "OPENCODE_CONFIG_CONTENT", { CAVE_GATEWAY_URL: "https://gw.example.com" });
  assert.equal(out.code, 0, `wrap opencode exited ${out.code}: ${out.stderr}`);
  const cfg = JSON.parse(out.stdout);
  assert.equal(cfg.provider.caveman.options.baseURL, "https://gw.example.com/w/opencode/v1");
  assert.equal(cfg.provider.caveman.options.apiKey, "{env:CAVE_API_KEY}", "opencode {env:VAR} tokens must be left intact");
  assert.equal(cfg.provider.caveman.options.headers["x-cave-upstream-key"], "{env:OPENAI_API_KEY}");
  assert.equal(cfg.provider.caveman.options.headers["X-Cave-Agent"], "opencode");
  assert.equal(cfg.model, "caveman/gpt-5.5");
});

// wrap opencode must NOT mutate the user's on-disk opencode config — it points the
// agent at the gateway purely via the inline-config env var.
test("wrap opencode never writes the user's opencode config", async () => {
  const { mkdtempSync, writeFileSync, mkdirSync, readFileSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cfgDir = join(home, ".config", "opencode");
  mkdirSync(cfgDir, { recursive: true });
  const userCfg = join(cfgDir, "opencode.json");
  const sentinel = JSON.stringify({ theme: "tokyonight" }, null, 2);
  writeFileSync(userCfg, sentinel);

  const binDir = mkdtempSync(join(tmpdir(), "cave-opencode-"));
  const stub = join(binDir, "opencode");
  writeFileSync(stub, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const env = { ...process.env, NO_COLOR: "1", HOME: home, PATH: `${binDir}:${process.env.PATH}` };
  delete env.CAVE_GATEWAY_URL;

  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "wrap", "opencode"], { env });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(readFileSync(userCfg, "utf8"), sentinel, "the user's opencode.json must be left byte-for-byte unchanged");
});

// `caveman mcp install <agent>` registers the caveman MCP server in the agent's
// own config (so it can recover proxy-elided detail) and records a marker wrap
// reads. For codex it writes a [mcp_servers.caveman] block to ~/.codex/config.toml.
test("mcp install codex writes the codex MCP config and a marker", async () => {
  const { mkdtempSync, readFileSync, existsSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const home = mkdtempSync(join(tmpdir(), "cave-mcp-home-"));
  const caveHome = join(home, ".caveman");
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveHome, CAVEMAN_MCP_BIN: "/opt/caveman-mcp" };
  delete env.CAVE_GATEWAY_URL;
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "mcp", "install", "codex"], { env });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  assert.equal(out.code, 0, out.stderr);
  const toml = readFileSync(join(home, ".codex", "config.toml"), "utf8");
  assert.match(toml, /\[mcp_servers\.caveman\]/, "codex config must register the caveman MCP server");
  assert.match(toml, /command = "\/opt\/caveman-mcp"/, "codex config must point at the resolved MCP binary");
  assert.ok(existsSync(join(caveHome, "mcp", "codex.json")), "an install marker must be written for wrap to read");
});

test("wrap does not persistently register caveman-browse when binary resolves", async () => {
  const { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const home = mkdtempSync(join(tmpdir(), "cave-browse-home-"));
  const binDir = mkdtempSync(join(tmpdir(), "cave-browse-bin-"));
  const browse = join(binDir, "caveman-browse");
  writeFileSync(join(binDir, "codex"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  writeFileSync(browse, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false } }, null, 2));
  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    CAVEMAN_HOME: join(home, ".caveman"),
    CAVEMAN_BROWSE_BIN: browse,
    CAVE_GATEWAY_URL: "http://127.0.0.1:9",
    PATH: `${binDir}:${process.env.PATH}`,
  };
  const out = await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, "wrap", "codex"], { env });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(existsSync(join(home, ".codex", "config.toml")), false);
  assert.equal(existsSync(join(home, ".caveman", "mcp", "codex.caveman-browse.json")), false);
});

test("wrap config mcp:false and browse:false skip MCP registration even when binaries exist", async () => {
  const { mkdtempSync, mkdirSync, writeFileSync, existsSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const home = mkdtempSync(join(tmpdir(), "cave-mcp-disabled-home-"));
  const binDir = mkdtempSync(join(tmpdir(), "cave-mcp-disabled-bin-"));
  writeFileSync(join(binDir, "codex"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  writeFileSync(join(binDir, "caveman-mcp"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  writeFileSync(join(binDir, "caveman-browse"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false, browse: false } }, null, 2));
  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    CAVEMAN_HOME: join(home, ".caveman"),
    CAVE_GATEWAY_URL: "http://127.0.0.1:9",
    PATH: `${binDir}:${process.env.PATH}`,
  };
  const out = await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, "wrap", "codex"], { env });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(existsSync(join(home, ".codex", "config.toml")), false, "MCP config must not be written");
  assert.equal(existsSync(join(home, ".caveman", "mcp", "codex.json")), false, "caveman marker must not be written");
  assert.equal(existsSync(join(home, ".caveman", "mcp", "codex.caveman-browse.json")), false, "browse marker must not be written");
});

test("wrap skips caveman-browse MCP silently when the binary is missing", async () => {
  const { mkdtempSync, mkdirSync, writeFileSync, existsSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const home = mkdtempSync(join(tmpdir(), "cave-browse-missing-home-"));
  const binDir = mkdtempSync(join(tmpdir(), "cave-browse-missing-bin-"));
  const emptyPathDir = mkdtempSync(join(tmpdir(), "cave-browse-empty-path-"));
  writeFileSync(join(binDir, "codex"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false } }, null, 2));
  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    CAVEMAN_HOME: join(home, ".caveman"),
    CAVE_GATEWAY_URL: "http://127.0.0.1:9",
    PATH: `${binDir}:${emptyPathDir}`,
  };
  delete env.CAVEMAN_BROWSE_BIN;
  const out = await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, "wrap", "codex"], { env });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(existsSync(join(home, ".codex", "config.toml")), false, "missing browse must not create MCP config");
  assert.doesNotMatch(out.stderr, /browse/i);
});

// Temp native pack gives wrapped Claude/Codex MCP recovery without a persistent marker.
test("wrap default sets CAVEMAN_RECOVERY=mcp from ephemeral native pack", async () => {
  const { mkdtempSync, writeFileSync, mkdirSync, readFileSync, existsSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const dir = mkdtempSync(join(tmpdir(), "cave-mcp-wrap-"));
  const recoveryFile = join(dir, "recovery.txt");
  const pidFile = join(dir, "pid.txt");
  const proxy = join(dir, "proxy.mjs");
  const caveHome = join(dir, "home");
  mkdirSync(join(caveHome, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(caveHome, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { browse: false }, ...validEntitlement() }, null, 2));
  const agent = join(dir, "codex");
  writeFileSync(agent, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  writeFileSync(join(dir, "caveman-mcp"), `#!/bin/sh
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  printf '%s\\n' '{"version":"test","capabilities":["mcp_recovery"]}'
fi
exit 0
`, { mode: 0o755 });
  writeFileSync(proxy, `#!/usr/bin/env node
import { createServer } from "node:http";
import { writeFileSync } from "node:fs";
if (process.argv[2] === "stats") { process.stdout.write("{}"); process.exit(0); }
if (process.argv[2] === "version" && process.argv[3] === "--json") {
  process.stdout.write('{"version":"fixture","capabilities":["run_state","native_runtime_v1","typed_ccr"]}');
  process.exit(0);
}
if (process.argv[2] === "status") { process.stdout.write('{"owner":"unknown"}'); process.exit(0); }
const url = new URL(process.env.CAVE_GATEWAY_URL);
writeFileSync(${JSON.stringify(recoveryFile)}, process.env.CAVEMAN_RECOVERY || "");
writeFileSync(${JSON.stringify(pidFile)}, String(process.pid));
createServer((req, res) => res.end("ok")).listen(Number(url.port), url.hostname);
setInterval(() => {}, 1000);
`, { mode: 0o755 });

  const port = await freePort();

  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: caveHome,
    CAVE_GATEWAY_URL: `http://127.0.0.1:${port}`,
    CAVEMAN_PROXY_BIN: proxy,
    CAVEMAN_HOME: caveHome,
    CAVE_BINARY_PROBE_TIMEOUT_MS: "10000",
    PATH: `${dir}:${process.env.PATH}`,
  };
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "wrap", "codex"], { env });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  try {
    if (existsSync(pidFile)) process.kill(Number(readFileSync(pidFile, "utf8")), "SIGTERM");
  } catch {}
  assert.equal(out.code, 0, out.stderr);
  assert.equal(readFileSync(recoveryFile, "utf8"), "mcp", "wrap must signal MCP recovery when the install marker is present");
});

// `caveman start` resolves the proxy binary via CAVEMAN_PROXY_BIN; a stub binary
// is launched and its exit code is forwarded.
test("start launches the proxy binary resolved from CAVEMAN_PROXY_BIN", async () => {
  const out = await new Promise((resolve, reject) => {
    // Use `true` as a stand-in proxy binary: it exits 0 immediately.
    const child = spawn("node", [cli, "start"], { env: { ...process.env, CAVEMAN_PROXY_BIN: "true", CAVEMAN_LISTEN: "127.0.0.1:1" } });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  assert.equal(out.code, 0, `start should forward the proxy's exit code 0: ${out.stderr}`);
});

test("wrap injects delegate MCP before startup without persisting agent config", async () => {
  const { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const script = join(mkdtempSync(join(tmpdir(), "cave-delegate-script-")), "caveman-delegate-mcp.mjs");
  writeFileSync(script, "// stub delegate server\n");
  const run = async (agentId, configJson, tag) => {
    const home = mkdtempSync(join(tmpdir(), `cave-delegate-${tag}-home-`));
    const binDir = mkdtempSync(join(tmpdir(), `cave-delegate-${tag}-bin-`));
    const capture = join(home, "delegate-startup-config");
    const userConfig = join(home, agentId === "codex" ? ".codex/config.toml" : ".claude.json");
    const userConfigBefore = agentId === "codex" ? 'model = "gpt-5.5"\n' : '{"sentinel":"keep"}\n';
    mkdirSync(dirname(userConfig), { recursive: true });
    writeFileSync(userConfig, userConfigBefore);
    const stub = agentId === "codex"
      ? '#!/bin/sh\ncp "$CODEX_HOME/config.toml" "$CAVE_DELEGATE_CAPTURE"\n'
      : '#!/bin/sh\nif [ "$1" = "--plugin-dir" ] && [ -f "$2/.mcp.json" ]; then cp "$2/.mcp.json" "$CAVE_DELEGATE_CAPTURE"; fi\n';
    writeFileSync(join(binDir, agentId), `${stub}exit 0\n`, { mode: 0o755 });
    mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
    writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify(configJson, null, 2));
    const env = {
      ...process.env,
      NO_COLOR: "1",
      HOME: home,
      CAVEMAN_HOME: join(home, ".caveman"),
      CAVEMAN_DELEGATE_MCP: script,
      CAVE_DELEGATE_CAPTURE: capture,
      CAVE_GATEWAY_URL: "http://127.0.0.1:9",
      PATH: `${binDir}:${process.env.PATH}`,
    };
    const out = await new Promise((resolve, reject) => {
      const child = spawn(process.execPath, [cli, "wrap", agentId], { env });
      let stderr = "";
      child.stderr.on("data", (d) => (stderr += d));
      child.on("exit", (code) => resolve({ code, stderr }));
      child.on("error", reject);
    });
    assert.equal(out.code, 0, out.stderr);
    return {
      home,
      capture: existsSync(capture) ? readFileSync(capture, "utf8") : "",
      userConfig,
      userConfigBefore,
      existsSync,
      readFileSync,
    };
  };
  for (const agentId of ["claude", "codex"]) {
    const on = await run(agentId, { execute: { delegate: true }, wrap: { proxy: false, shrink: false, mcp: false, browse: false } }, `${agentId}-on`);
    assert.match(on.capture, /caveman-delegate/);
    assert.ok(on.capture.includes(script));
    assert.equal(on.readFileSync(on.userConfig, "utf8"), on.userConfigBefore);
    assert.equal(on.existsSync(join(on.home, ".caveman", "mcp", `${agentId}.caveman-delegate.json`)), false);

    const off = await run(agentId, { wrap: { proxy: false, shrink: false, mcp: false, browse: false } }, `${agentId}-off`);
    assert.doesNotMatch(off.capture, /caveman-delegate/, "delegate must stay off by default");
    assert.equal(off.readFileSync(off.userConfig, "utf8"), off.userConfigBefore);
    assert.equal(off.existsSync(join(off.home, ".caveman", "mcp", `${agentId}.caveman-delegate.json`)), false);
  }
});
