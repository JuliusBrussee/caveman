import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { runCli } from "./harness/index.mjs";

const cliDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const repoRoot = join(cliDir, "..", "..");
const cli = join(cliDir, "dist", "index.js");
const suiteDir = mkdtempSync(join(tmpdir(), "cave-wrap-restart-"));
const proxyBin = join(suiteDir, "caveman-proxy");

before(() => {
  execFileSync("go", ["build", "-o", proxyBin, "./public/proxy/cmd/caveman-proxy"], {
    cwd: repoRoot,
    stdio: "pipe",
  });
});

after(() => rmSync(suiteDir, { recursive: true, force: true }));

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

async function waitFor(predicate, timeoutMs = 5000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = predicate();
    if (value) return value;
    await new Promise((resolve) => setTimeout(resolve, 40));
  }
  throw new Error(`condition not met within ${timeoutMs}ms`);
}

function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

function writeAgent(dir) {
  const path = join(dir, "agent");
  writeFileSync(path, "#!/usr/bin/env node\nsetTimeout(() => process.exit(0), 80);\n", { mode: 0o755 });
  return path;
}

function writeEntitledConfig(home) {
  mkdirSync(home, { recursive: true });
  const configDir = join(dirname(home), ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "config.json"), JSON.stringify({
    deviceId: "wrap-restart-device",
    wrapEntitlement: {
      entitled: true,
      plan: "free",
      telemetry_level: "metadata",
      seats_used: 1,
      seats_limit: 1,
      devices_used: 1,
      devices_limit: 3,
      evicted_device_hash: null,
      expires_at: new Date(Date.now() + 3_600_000).toISOString(),
    },
  }), { mode: 0o600 });
}

function runState(home, port) {
  return JSON.parse(readFileSync(join(home, "run", `${port}.json`), "utf8"));
}

async function startRealProxy(home, port, mode = "record") {
  const child = spawn(proxyBin, [], {
    env: {
      ...process.env,
      CAVEMAN_HOME: home,
      CAVEMAN_LISTEN: `127.0.0.1:${port}`,
      CAVEMAN_MODE: mode,
      CAVEMAN_PROXY_OWNER: "wrap",
    },
    stdio: "ignore",
  });
  const state = await waitFor(() => {
    try {
      return runState(home, port);
    } catch {
      return null;
    }
  });
  return { child, state };
}

async function stopPid(pid) {
  if (!alive(pid)) return;
  process.kill(pid, "SIGTERM");
  await waitFor(() => !alive(pid), 3000).catch(() => {
    if (alive(pid)) process.kill(pid, "SIGKILL");
  });
}

function baseEnv(home, binDir, port, binary = proxyBin) {
  return {
    ...process.env,
    HOME: dirname(home),
    PATH: `${binDir}:${process.env.PATH}`,
    CAVEMAN_HOME: home,
    CAVEMAN_PROXY_BIN: binary,
    CAVE_GATEWAY_URL: `http://127.0.0.1:${port}`,
    CAVEMAN_TELEMETRY: "0",
    NO_COLOR: "1",
  };
}

test("leftover record proxy is SIGTERM-restarted into live compress mode", async () => {
  const dir = mkdtempSync(join(suiteDir, "flip-"));
  const home = join(dir, "home");
  const binDir = join(dir, "bin");
  mkdirSync(binDir, { recursive: true });
  writeAgent(binDir);
  writeEntitledConfig(home);
  const port = await freePort();
  const original = await startRealProxy(home, port);
  let successorPid = original.state.pid;
  try {
    const out = await runCli(cli, ["wrap", "agent"], {
      env: baseEnv(home, binDir, port),
      cwd: dir,
      timeoutMs: 12_000,
    });
    assert.equal(out.code, 0, out.stderr);
    const successor = await waitFor(() => {
      try {
        const state = runState(home, port);
        return state.pid !== original.state.pid ? state : null;
      } catch {
        return null;
      }
    });
    successorPid = successor.pid;
    assert.equal(successor.mode, "compress");
    assert.equal(successor.owner, "wrap");
    assert.equal(alive(original.state.pid), false, "pre-login generation must exit");
    assert.match(out.stderr, /caveman · compress · agent/);
    assert.doesNotMatch(out.stderr, /already running in record mode/);
    const markers = join(home, "run", `${port}.sessions`);
    assert.equal(existsSync(markers) ? readdirSync(markers).length : 0, 0);
  } finally {
    await stopPid(successorPid);
  }
});

test("another live session marker prevents restart and preserves running mode", async () => {
  const dir = mkdtempSync(join(suiteDir, "held-"));
  const home = join(dir, "home");
  const binDir = join(dir, "bin");
  mkdirSync(binDir, { recursive: true });
  writeAgent(binDir);
  writeEntitledConfig(home);
  const port = await freePort();
  const original = await startRealProxy(home, port);
  const markerDir = join(home, "run", `${port}.sessions`);
  mkdirSync(markerDir, { recursive: true, mode: 0o700 });
  const held = join(markerDir, `${process.pid}-held`);
  writeFileSync(held, "held\n", { mode: 0o600 });
  try {
    const out = await runCli(cli, ["wrap", "agent"], {
      env: baseEnv(home, binDir, port),
      cwd: dir,
      timeoutMs: 8000,
    });
    assert.equal(out.code, 0, out.stderr);
    assert.equal(runState(home, port).pid, original.state.pid);
    assert.equal(alive(original.state.pid), true);
    assert.match(out.stderr, /caveman · record · agent/);
    assert.match(out.stderr, /another live session holds it/);
  } finally {
    rmSync(held, { force: true });
    await stopPid(original.state.pid);
  }
});

test("restart timeout never escalates to SIGKILL and still launches agent", async () => {
  const dir = mkdtempSync(join(suiteDir, "timeout-"));
  const home = join(dir, "home");
  const binDir = join(dir, "bin");
  mkdirSync(binDir, { recursive: true });
  writeAgent(binDir);
  writeEntitledConfig(home);
  const port = await freePort();
  const listener = spawn(process.execPath, ["-e", `
    const {createServer}=require("node:net");
    process.on("SIGTERM",()=>{});
    createServer(()=>{}).listen(${port},"127.0.0.1");
  `], { stdio: "ignore" });
  await waitFor(() => alive(listener.pid));
  await new Promise((resolve) => setTimeout(resolve, 100));

  const runDir = join(home, "run");
  mkdirSync(runDir, { recursive: true, mode: 0o700 });
  writeFileSync(join(runDir, `${port}.json`), JSON.stringify({
    schema: "caveman.proxy.run.v1",
    pid: listener.pid,
    port,
    listen: `127.0.0.1:${port}`,
    mode: "record",
    owner: "wrap",
    instance_token: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    started_at: new Date().toISOString(),
    version: "test",
  }), { mode: 0o600 });
  const fakeProxy = join(binDir, "caveman-proxy");
  writeFileSync(fakeProxy, `#!/usr/bin/env node
const fs=require("node:fs"), path=require("node:path");
if(process.argv[2]==="version"){console.log(JSON.stringify({version:"test",schema:"caveman.proxy.run.v1",capabilities:["run_state","sessions_scanned","observe_token_accounting"]}));process.exit(0)}
if(process.argv[2]==="status"){const p=Number(process.argv[process.argv.indexOf("--port")+1]);const s=JSON.parse(fs.readFileSync(path.join(process.env.CAVEMAN_HOME,"run",p+".json")));console.log(JSON.stringify(s));process.exit(0)}
process.exit(0);
`, { mode: 0o755 });
  chmodSync(fakeProxy, 0o755);
  try {
    const started = Date.now();
    const out = await runCli(cli, ["wrap", "agent"], {
      env: { ...baseEnv(home, binDir, port, fakeProxy), CAVE_PROXY_RESTART_TIMEOUT: "0.2" },
      cwd: dir,
      timeoutMs: 5000,
    });
    assert.equal(out.code, 0, out.stderr);
    assert.ok(Date.now() - started < 3000, "restart wait must remain bounded");
    assert.equal(alive(listener.pid), true, "a surviving process proves no SIGKILL escalation");
    assert.match(out.stderr, /already running in record mode/);
  } finally {
    if (alive(listener.pid)) process.kill(listener.pid, "SIGKILL");
  }
});

test("generation takeover aborts signal and reports successor live mode", async () => {
  const dir = mkdtempSync(join(suiteDir, "takeover-"));
  const home = join(dir, "home");
  const binDir = join(dir, "bin");
  const signalFile = join(dir, "signalled");
  const counterFile = join(dir, "status-count");
  mkdirSync(binDir, { recursive: true });
  writeAgent(binDir);
  writeEntitledConfig(home);
  const port = await freePort();
  const listener = spawn(process.execPath, ["-e", `
    const fs=require("node:fs"), {createServer}=require("node:net");
    process.on("SIGTERM",()=>fs.writeFileSync(${JSON.stringify(signalFile)},"term"));
    createServer(()=>{}).listen(${port},"127.0.0.1");
  `], { stdio: "ignore" });
  await new Promise((resolve) => setTimeout(resolve, 120));
  const runDir = join(home, "run");
  mkdirSync(runDir, { recursive: true, mode: 0o700 });
  writeFileSync(join(runDir, `${port}.json`), JSON.stringify({
    schema: "caveman.proxy.run.v1",
    pid: listener.pid,
    port,
    listen: `127.0.0.1:${port}`,
    mode: "compress",
    owner: "wrap",
    instance_token: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    started_at: new Date().toISOString(),
    version: "successor",
  }), { mode: 0o600 });
  const fakeProxy = join(binDir, "caveman-proxy");
  writeFileSync(fakeProxy, `#!/usr/bin/env node
const fs=require("node:fs");
if(process.argv[2]==="version"){console.log(JSON.stringify({version:"test",schema:"caveman.proxy.run.v1",capabilities:["run_state","sessions_scanned","observe_token_accounting"]}));process.exit(0)}
if(process.argv[2]==="status"){
  let n=0;try{n=Number(fs.readFileSync(${JSON.stringify(counterFile)},"utf8"))||0}catch{}
  fs.writeFileSync(${JSON.stringify(counterFile)},String(n+1));
  const state=JSON.parse(fs.readFileSync(${JSON.stringify(join(runDir, `${port}.json`))},"utf8"));
  if(n===0){state.mode="record";state.instance_token="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";state.version="predecessor"}
  console.log(JSON.stringify(state));process.exit(0)
}
process.exit(0);
`, { mode: 0o755 });
  try {
    const out = await runCli(cli, ["wrap", "agent"], {
      env: baseEnv(home, binDir, port, fakeProxy),
      cwd: dir,
      timeoutMs: 5000,
    });
    assert.equal(out.code, 0, out.stderr);
    assert.equal(alive(listener.pid), true);
    assert.equal(existsSync(signalFile), false, "changed generation must abort before SIGTERM");
    assert.match(out.stderr, /caveman · compress · agent/);
    assert.doesNotMatch(out.stderr, /already running in/);
  } finally {
    if (alive(listener.pid)) process.kill(listener.pid, "SIGKILL");
  }
});

test("foreign listener is never signalled and gets owner-unknown banner", async () => {
  const dir = mkdtempSync(join(suiteDir, "foreign-"));
  const home = join(dir, "home");
  const binDir = join(dir, "bin");
  const signalFile = join(dir, "signalled");
  mkdirSync(binDir, { recursive: true });
  writeAgent(binDir);
  writeEntitledConfig(home);
  const port = await freePort();
  const listener = spawn(process.execPath, ["-e", `
    const fs=require("node:fs"), {createServer}=require("node:net");
    process.on("SIGTERM",()=>fs.writeFileSync(${JSON.stringify(signalFile)},"term"));
    createServer(()=>{}).listen(${port},"127.0.0.1");
  `], { stdio: "ignore" });
  await new Promise((resolve) => setTimeout(resolve, 120));
  const fakeProxy = join(binDir, "caveman-proxy");
  writeFileSync(fakeProxy, `#!/usr/bin/env node
if(process.argv[2]==="version"){console.log(JSON.stringify({version:"test",schema:"caveman.proxy.run.v1",capabilities:["run_state","sessions_scanned","observe_token_accounting"]}));process.exit(0)}
if(process.argv[2]==="status"){console.log(JSON.stringify({owner:"unknown"}));process.exit(0)}
process.exit(0);
`, { mode: 0o755 });
  try {
    const out = await runCli(cli, ["wrap", "agent"], {
      env: baseEnv(home, binDir, port, fakeProxy),
      cwd: dir,
      timeoutMs: 5000,
    });
    assert.equal(out.code, 0, out.stderr);
    assert.equal(alive(listener.pid), true);
    assert.equal(existsSync(signalFile), false);
    assert.match(out.stderr, /caveman · owner: unknown · agent/);
    assert.match(out.stderr, /something else is listening/);
  } finally {
    if (alive(listener.pid)) process.kill(listener.pid, "SIGKILL");
  }
});
