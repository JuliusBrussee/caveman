import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function runCli(argv, env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...argv], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

function hermesStubDir(prefix = "cave-hermes-") {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  writeFileSync(
    join(dir, "hermes"),
    `#!/usr/bin/env node
const keyNames = [
  "CAVE_API_KEY",
  "CUSTOM_API_KEY",
  "EXAMPLE_CAVE_API_KEY",
  "LOCALHOST_API_KEY",
  "127_0_0_1_API_KEY",
  "OPENAI_API_KEY",
  "ANTHROPIC_API_KEY",
  "GEMINI_API_KEY",
  "GOOGLE_API_KEY",
  "OPENROUTER_API_KEY"
];
process.stdout.write(JSON.stringify({
  argv: process.argv.slice(2),
  customBaseUrl: process.env.CUSTOM_BASE_URL || "",
  hasCustomApiKey: Object.prototype.hasOwnProperty.call(process.env, "CUSTOM_API_KEY"),
  customApiKey: process.env.CUSTOM_API_KEY || "",
  apiKeyVars: Object.fromEntries(keyNames.filter((key) => Object.prototype.hasOwnProperty.call(process.env, key)).map((key) => [key, process.env[key]]))
}));
`,
    { mode: 0o755 },
  );
  return dir;
}

function clearHermesKeys(env) {
  for (const key of Object.keys(env)) {
    if (key.endsWith("_API_KEY")) delete env[key];
  }
}

function hermesEnv(binDir, home, gatewayUrl, extra = {}) {
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false, browse: false } }, null, 2));
  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    HERMES_HOME: join(home, ".hermes"),
    CAVEMAN_HOME: join(home, ".caveman"),
    CAVEMAN_MCP_BIN: "/nonexistent/caveman-mcp",
    CAVE_GATEWAY_URL: gatewayUrl,
    PATH: `${binDir}:${process.env.PATH}`,
  };
  clearHermesKeys(env);
  return { ...env, ...extra };
}

test("wrap hermes local injects CUSTOM_BASE_URL, never appends --api-key, and warns without proxy-forwardable key", async () => {
  const binDir = hermesStubDir();
  const home = mkdtempSync(join(tmpdir(), "cave-hermes-home-"));
  const env = hermesEnv(binDir, home, "http://127.0.0.1:18801");

  const out = await runCli(["wrap", "hermes", "-z", "hi", "-m", "gpt-5.6"], env);
  assert.equal(out.code, 0, out.stderr);
  const seen = JSON.parse(out.stdout);
  assert.equal(seen.customBaseUrl, "http://127.0.0.1:18801/w/hermes");
  assert.equal(seen.hasCustomApiKey, false, "empty cave_api_key render must omit CUSTOM_API_KEY");
  assert.deepEqual(seen.argv.slice(0, 2), ["--provider", "custom"], "profile provider args must precede user args");
  assert.deepEqual(seen.argv.slice(2), ["-z", "hi", "-m", "gpt-5.6"], "user args must remain after profile args");
  assert.ok(!seen.argv.includes("--api-key"), "Caveman must never append Hermes --api-key");
  assert.match(out.stderr, /^caveman: Hermes local wrap has no upstream provider key/m, "local missing-key note must be honest and non-fatal");
});

test("wrap hermes local suppresses warning when the proxy can forward an upstream key", async () => {
  const binDir = hermesStubDir();
  const home = mkdtempSync(join(tmpdir(), "cave-hermes-home-"));
  const env = hermesEnv(binDir, home, "http://127.0.0.1:18803", { ANTHROPIC_API_KEY: "sk-local-anthropic" });

  const out = await runCli(["wrap", "hermes", "-z", "hi"], env);
  assert.equal(out.code, 0, out.stderr);
  const seen = JSON.parse(out.stdout);
  assert.deepEqual(seen.argv, ["--provider", "custom", "-z", "hi"]);
  assert.ok(!seen.argv.includes("--api-key"), "Caveman must never append Hermes --api-key in local mode");
  assert.doesNotMatch(out.stderr, /Hermes local wrap has no upstream provider key/);
});

test("wrap hermes managed injects host-derived API key env and never appends --api-key", async () => {
  const binDir = hermesStubDir();
  const home = mkdtempSync(join(tmpdir(), "cave-hermes-home-"));
  const env = hermesEnv(binDir, home, "https://gateway.example-cave.com", { CAVE_API_KEY: "cave_test_key" });

  const out = await runCli(["wrap", "hermes"], env);
  assert.equal(out.code, 0, out.stderr);
  const seen = JSON.parse(out.stdout);
  assert.equal(seen.customBaseUrl, "https://gateway.example-cave.com/w/hermes");
  assert.equal(seen.hasCustomApiKey, false, "Hermes request auth must use host-derived env, not CUSTOM_API_KEY");
  assert.equal(seen.apiKeyVars.EXAMPLE_CAVE_API_KEY, "cave_test_key");
  assert.deepEqual(seen.argv, ["--provider", "custom"]);
  assert.ok(!seen.argv.includes("--api-key"), "Caveman must never append Hermes --api-key in managed mode");
  assert.doesNotMatch(out.stderr, /Hermes managed wrap has no CAVE_API_KEY/);
  assert.doesNotMatch(out.stderr, /cannot derive a managed API-key env var/);
});

test("wrap hermes loopback gateway does not derive an API key env and does not crash", async () => {
  const binDir = hermesStubDir();
  const home = mkdtempSync(join(tmpdir(), "cave-hermes-home-"));
  const env = hermesEnv(binDir, home, "http://127.0.0.1:18804", {
    CAVE_API_KEY: "cave_test_key",
    OPENAI_API_KEY: "sk-local-openai",
  });

  const out = await runCli(["wrap", "hermes"], env);
  assert.equal(out.code, 0, out.stderr);
  const seen = JSON.parse(out.stdout);
  assert.equal(seen.customBaseUrl, "http://127.0.0.1:18804/w/hermes");
  assert.equal(seen.hasCustomApiKey, false, "loopback Hermes auth stays on the local proxy side");
  assert.equal(seen.apiKeyVars.EXAMPLE_CAVE_API_KEY, undefined);
  assert.equal(seen.apiKeyVars.LOCALHOST_API_KEY, undefined);
  assert.equal(seen.apiKeyVars["127_0_0_1_API_KEY"], undefined);
  assert.ok(!seen.argv.includes("--api-key"), "Caveman must never append Hermes --api-key for loopback gateways");
});

test("wrap hermes managed warns when CAVE_API_KEY is missing", async () => {
  const binDir = hermesStubDir();
  const home = mkdtempSync(join(tmpdir(), "cave-hermes-home-"));
  const env = hermesEnv(binDir, home, "https://gateway.example-cave.com");

  const out = await runCli(["wrap", "hermes"], env);
  assert.equal(out.code, 0, out.stderr);
  const seen = JSON.parse(out.stdout);
  assert.ok(!seen.argv.includes("--api-key"), "Caveman must never append Hermes --api-key when managed key is missing");
  assert.match(out.stderr, /^caveman: Hermes managed wrap has no CAVE_API_KEY/m, "managed missing-key note must be honest and non-fatal");
});

test("wrap hermes managed warns when no host-derived env var can be computed", async () => {
  const binDir = hermesStubDir();
  const home = mkdtempSync(join(tmpdir(), "cave-hermes-home-"));
  const env = hermesEnv(binDir, home, "https://api.openai.com", { CAVE_API_KEY: "cave_test_key" });

  const out = await runCli(["wrap", "hermes"], env);
  assert.equal(out.code, 0, out.stderr);
  const seen = JSON.parse(out.stdout);
  assert.equal(seen.apiKeyVars.OPENAI_API_KEY, undefined);
  assert.ok(!seen.argv.includes("--api-key"), "Caveman must never append Hermes --api-key when derivation is blocked");
  assert.match(out.stderr, /^caveman: Hermes cannot derive a managed API-key env var/m, "managed no-derived-key note must be honest and non-fatal");
});

test("bare caveman hermes --pixel hoists --pixel into wrap mode", async () => {
  const dir = hermesStubDir("cave-hermes-pixel-");
  const modeFile = join(dir, "proxy-env.json");
  const proxy = join(dir, "proxy.mjs");
  writeFileSync(proxy, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
if (process.argv[2] === "stats") { process.stdout.write("{}"); process.exit(0); }
writeFileSync(${JSON.stringify(modeFile)}, JSON.stringify({ mode: process.env.CAVEMAN_MODE || "" }));
`, { mode: 0o755 });
  const home = mkdtempSync(join(tmpdir(), "cave-hermes-home-"));
  // pixel mode is compression: seed a valid entitlement so the account gate (ADR
  // 0022) keeps compression on and the proxy starts in pixel mode.
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(
    join(home, ".caveman-cloud", "config.json"),
    JSON.stringify(
      {
        wrapEntitlement: {
          entitled: true, plan: "free", telemetry_level: "metadata",
          seats_used: 1, seats_limit: 1, devices_used: 1, devices_limit: 3,
          evicted_device_hash: null, expires_at: new Date(Date.now() + 72 * 3600 * 1000).toISOString(),
        },
        wrapEntitlementFetchedAt: new Date().toISOString(),
      },
      null,
      2,
    ),
  );
  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    HERMES_HOME: join(home, ".hermes"),
    CAVEMAN_HOME: join(home, ".caveman"),
    CAVEMAN_MCP_BIN: "/nonexistent/caveman-mcp",
    CAVEMAN_BROWSE_BIN: "/nonexistent/caveman-browse",
    CAVEMAN_PROXY_BIN: proxy,
    CAVE_GATEWAY_URL: "http://127.0.0.1:18802",
    PATH: `${dir}:${process.env.PATH}`,
  };

  const out = await runCli(["hermes", "--pixel"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.ok(existsSync(modeFile), "pixel shortcut must invoke proxy startup");
  assert.equal(JSON.parse(readFileSync(modeFile, "utf8")).mode, "pixel");
});
