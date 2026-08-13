import { afterEach, test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { createServer as createNetServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { runCli } from "./harness/index.mjs";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");
const cleanups = [];

afterEach(async () => {
  while (cleanups.length) await cleanups.pop()();
});

async function listen(server) {
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  return server.address().port;
}

function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

function entitlement() {
  return {
    entitled: true,
    plan: "free",
    telemetry_level: "metadata",
    seats_used: 1,
    seats_limit: 1,
    devices_used: 1,
    devices_limit: 3,
    evicted_device_hash: null,
    expires_at: new Date(Date.now() + 3_600_000).toISOString(),
  };
}

async function fixture({ responseDelayMs = 0 } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "cave-weekly-refresh-"));
  const userHome = join(dir, "user");
  const caveHome = join(userHome, ".caveman");
  const configDir = join(userHome, ".caveman-cloud");
  const binDir = join(dir, "bin");
  mkdirSync(configDir, { recursive: true });
  mkdirSync(binDir, { recursive: true });

  const requests = [];
  const control = createServer(async (req, res) => {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    requests.push(JSON.parse(Buffer.concat(chunks).toString("utf8")));
    if (responseDelayMs) await new Promise((resolve) => setTimeout(resolve, responseDelayMs));
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify(entitlement()));
  });
  const apiPort = await listen(control);

  const gateway = createNetServer(() => {});
  const gatewayPort = await listen(gateway);
  // Run-state PID is signalable by wrap restart logic. Never point a fixture at
  // the node:test worker itself: a gate mismatch would SIGTERM the test runner
  // and surface only as an opaque file-level failure.
  const proxyOwner = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
    stdio: "ignore",
  });
  const runDir = join(caveHome, "run");
  mkdirSync(runDir, { recursive: true, mode: 0o700 });
  writeFileSync(join(runDir, `${gatewayPort}.json`), JSON.stringify({
    schema: "caveman.proxy.run.v1",
    pid: proxyOwner.pid,
    port: gatewayPort,
    listen: `127.0.0.1:${gatewayPort}`,
    mode: "compress",
    owner: "wrap",
    recovery_via_mcp: false,
    instance_token: "cccccccccccccccccccccccccccccccc",
    started_at: new Date().toISOString(),
    version: "test",
  }), { mode: 0o600 });

  const proxy = join(binDir, "caveman-proxy");
  writeFileSync(proxy, `#!/usr/bin/env node
const fs=require("node:fs"),path=require("node:path");
if(process.argv[2]==="version"){console.log(JSON.stringify({version:"test",schema:"caveman.proxy.run.v1",capabilities:["run_state","sessions_scanned","observe_token_accounting"]}));process.exit(0)}
if(process.argv[2]==="status"){const p=Number(process.argv[process.argv.indexOf("--port")+1]);console.log(fs.readFileSync(path.join(process.env.CAVEMAN_HOME,"run",p+".json"),"utf8"));process.exit(0)}
if(process.argv[2]==="stats"){console.log(JSON.stringify({spans:0,token_accounting:{}}));process.exit(0)}
process.exit(0);
`, { mode: 0o755 });
  const agent = join(binDir, "agent");
  writeFileSync(agent, "#!/usr/bin/env node\nsetTimeout(() => process.exit(0), 250);\n", { mode: 0o755 });

  const configPath = join(configDir, "config.json");
  writeFileSync(configPath, JSON.stringify({
    baseURL: `http://127.0.0.1:${apiPort}`,
    token: "connected-token",
    deviceId: "weekly-device",
    wrapEntitlement: entitlement(),
    wrapEntitlementState: { kind: "ok", at: new Date().toISOString() },
  }), { mode: 0o600 });
  const env = {
    ...process.env,
    HOME: userHome,
    PATH: `${binDir}:${process.env.PATH}`,
    CAVEMAN_HOME: caveHome,
    CAVEMAN_PROXY_BIN: proxy,
    CAVE_GATEWAY_URL: `http://127.0.0.1:${gatewayPort}`,
    CAVEMAN_TELEMETRY: "0",
    NO_COLOR: "1",
  };
  cleanups.push(async () => {
    if (proxyOwner.exitCode === null && proxyOwner.signalCode === null) proxyOwner.kill("SIGTERM");
    await new Promise((resolve) => control.close(resolve));
    await new Promise((resolve) => gateway.close(resolve));
    rmSync(dir, { recursive: true, force: true });
  });
  return { dir, env, requests, configPath };
}

async function runAndSettle(f) {
  const out = await runCli(cli, ["wrap", "agent"], { env: f.env, cwd: f.dir, timeoutMs: 5000 });
  await new Promise((resolve) => setTimeout(resolve, 80));
  return out;
}

test("connected run refreshes at most once per ISO week and skips wall/denied states", async () => {
  const f = await fixture();
  assert.equal((await runAndSettle(f)).code, 0);
  assert.equal(f.requests.length, 1);
  assert.equal(f.requests[0].wrapped_run, true);

  assert.equal((await runAndSettle(f)).code, 0);
  assert.equal(f.requests.length, 1, "second run in same ISO week must not POST");

  const nextWeek = JSON.parse(readFileSync(f.configPath, "utf8"));
  nextWeek.wrapEntitlementRunRefreshWeek = "2000-01-03";
  writeFileSync(f.configPath, JSON.stringify(nextWeek), { mode: 0o600 });
  assert.equal((await runAndSettle(f)).code, 0);
  assert.equal(f.requests.length, 2, "different week marker must permit one new POST");

  for (const kind of ["seat-wall", "denied"]) {
    const blocked = JSON.parse(readFileSync(f.configPath, "utf8"));
    delete blocked.wrapEntitlement;
    blocked.wrapEntitlementState = { kind, at: new Date().toISOString() };
    blocked.wrapEntitlementRunRefreshWeek = "2000-01-03";
    writeFileSync(f.configPath, JSON.stringify(blocked), { mode: 0o600 });
    const statePath = join(f.env.CAVEMAN_HOME, "run", `${new URL(f.env.CAVE_GATEWAY_URL).port}.json`);
    // The mode stays compress: a seat wall or a denied entitlement no
    // longer downgrades the local proxy, so the running state must still match.
    const state = JSON.parse(readFileSync(statePath, "utf8"));
    state.recovery_via_mcp = false;
    writeFileSync(statePath, JSON.stringify(state), { mode: 0o600 });
    assert.equal((await runAndSettle(f)).code, 0);
    assert.equal(f.requests.length, 2, `${kind} must issue no run refresh`);
  }
});

test("weekly refresh never blocks run start when entitlement service stalls", async () => {
  const f = await fixture({ responseDelayMs: 5000 });
  const started = Date.now();
  const out = await runCli(cli, ["wrap", "agent"], { env: f.env, cwd: f.dir, timeoutMs: 3500 });
  assert.equal(out.code, 0, out.stderr);
  assert.ok(Date.now() - started < 3000, "agent completion must not wait for entitlement response");
  assert.equal(alive(process.pid), true);
});
