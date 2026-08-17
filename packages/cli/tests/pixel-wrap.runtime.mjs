import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

// pixel mode is compression, so the account gate requires a valid
// entitlement in config.json — seed one so the pixel wiring stays testable.
function validEntitlement() {
  return {
    wrapEntitlement: {
      entitled: true, plan: "free", telemetry_level: "metadata",
      seats_used: 1, seats_limit: 1, devices_used: 1, devices_limit: 3,
      evicted_device_hash: null, expires_at: new Date(Date.now() + 72 * 3600 * 1000).toISOString(),
    },
    wrapEntitlementFetchedAt: new Date().toISOString(),
  };
}

async function runWithProxyStub(cliArgs, agentName = "agent", extraEnv = {}) {
  const dir = mkdtempSync(join(tmpdir(), "cave-pixel-wrap-"));
  const modeFile = join(dir, "proxy-env.json");
  const proxy = join(dir, "proxy.mjs");
  const agent = join(dir, agentName);
  writeFileSync(proxy, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
if (process.argv[2] === "stats") { process.stdout.write("{}"); process.exit(0); }
if (process.argv[2] === "version" && process.argv[3] === "--json") {
  process.stdout.write('{"version":"fixture","capabilities":["run_state","native_runtime_v1","typed_ccr"]}');
  process.exit(0);
}
if (process.argv[2] === "status") { process.stdout.write('{"owner":"unknown"}'); process.exit(0); }
writeFileSync(${JSON.stringify(modeFile)}, JSON.stringify({
  mode: process.env.CAVEMAN_MODE || "",
  pixelModels: process.env.CAVE_PIXEL_MODELS || "",
  recovery: process.env.CAVEMAN_RECOVERY || ""
}));
`, { mode: 0o755 });
  writeFileSync(agent, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const home = mkdtempSync(join(tmpdir(), "cave-pixel-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify(validEntitlement(), null, 2));
  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    CAVEMAN_HOME: home,
    CAVEMAN_MCP_BIN: "/nonexistent/caveman-mcp",
    CAVEMAN_BROWSE_BIN: "/nonexistent/caveman-browse",
    CAVE_GATEWAY_URL: "http://127.0.0.1:18799",
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
  const proxyEnv = existsSync(modeFile) ? JSON.parse(readFileSync(modeFile, "utf8")) : {};
  return { ...out, proxyEnv };
}

test("wrap --pixel starts the local proxy in pixel mode and forwards CAVE_PIXEL_MODELS", async () => {
  const out = await runWithProxyStub(["wrap", "--pixel", "agent"], "agent", { CAVE_PIXEL_MODELS: "claude-fable-5,gpt-5.6" });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "pixel");
  assert.equal(out.proxyEnv.pixelModels, "claude-fable-5,gpt-5.6");
});

test("bare agent shortcut hoists --pixel into wrap mode", async () => {
  const out = await runWithProxyStub(["claude", "--pixel"], "claude", { CAVE_PIXEL_MODELS: "claude-fable-5" });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "pixel");
  assert.equal(out.proxyEnv.pixelModels, "claude-fable-5");
});

test("wrap rejects --pixel with --off", async () => {
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "wrap", "--pixel", "--off", "agent"], { env: { ...process.env, NO_COLOR: "1" } });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  assert.equal(out.code, 1);
  assert.match(out.stderr, /--off and --pixel cannot be used together/);
});
