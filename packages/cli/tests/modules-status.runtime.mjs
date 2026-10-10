import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { createServer as createHttpServer } from "node:http";
import { createServer } from "node:net";
import { join } from "node:path";
import { modulesFixture, runCli } from "./_modules.mjs";

test("status shows the modules × agents grid and one next line; --json adds modules", async () => {
  const fx = modulesFixture({ blocks: true });
  try {
    assert.equal((await runCli(["on", "--all", "--yes"], fx.env)).code, 0);
    assert.equal((await runCli(["off", "browse", "--yes"], fx.env)).code, 0);
    const out = await runCli(["status"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    const json = JSON.parse((await runCli(["status", "--json"], fx.env)).stdout);
    assert.equal(out.stdout, [
      "caveman status",
      "                   claude     codex",
      "  on  output       wired      wired",
      "  on  input        wired      wired",
      "  on  waste-fixes  wired      wired",
      "  on  routing      —          —          sign in to turn on routing · caveman login",
      "  on  scripts      wired      wired",
      "  off browse                             caveman on browse",
      // The existing off-state lines keep their place under the grid.
      ...json.off_states.map((state) => state.fix ? `${state.line} · ${state.fix}` : state.line),
      "agent traffic: local runtime",
      "next: caveman login",
      "",
    ].join("\n"));

    assert.deepEqual(Object.keys(json).slice(-2), ["native_integrations", "modules"]);
    assert.deepEqual(json.modules.find((state) => state.id === "routing"), {
      id: "routing", on: true, active: false, reason: "sign in to turn on routing", perAgent: { claude: "wired", codex: "wired" },
    });
    assert.deepEqual(json.modules.find((state) => state.id === "browse"), {
      id: "browse", on: false, active: false, perAgent: { claude: "n/a", codex: "n/a" },
    });

    const signedIn = await runCli(["status", "--json"], { ...fx.env, CAVE_TOKEN: "test-token" });
    assert.equal(JSON.parse(signedIn.stdout).modules.find((state) => state.id === "routing").reason, "waiting for Cloud routing");
  } finally {
    fx.cleanup();
  }
});

test("status on a fresh home points at the missing binaries first", async () => {
  const fx = modulesFixture({ binaries: false, agents: ["claude"] });
  try {
    const out = await runCli(["status"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /  on  input        —          caveman-proxy, caveman-engine, caveman-mcp, cavemem, caveman-shrink not installed · caveman setup --install/);
    assert.match(out.stdout, /  on  scripts      —          not set up · caveman setup/);
    assert.match(out.stdout, /\nnext: caveman setup --install\n$/);
  } finally {
    fx.cleanup();
  }
});

test("doctor fails a broken module offline and passes a healthy one", async () => {
  const broken = modulesFixture({ binaries: false });
  try {
    const out = await runCli(["doctor"], broken.env);
    assert.equal(out.code, 1, out.stderr);
    assert.match(out.stdout, /^✗ input: caveman-proxy, caveman-engine, caveman-mcp, cavemem, caveman-shrink not installed · fix: caveman setup --install$/m);
    assert.match(out.stdout, /^· scripts: not set up · caveman setup$/m);
    assert.match(out.stdout, /^· routing: not set up · caveman setup$/m);
    assert.doesNotMatch(out.stdout, /cloud/);
  } finally {
    broken.cleanup();
  }

  const healthy = modulesFixture({ blocks: true });
  try {
    assert.equal((await runCli(["on", "--all", "--yes"], healthy.env)).code, 0);
    const out = await runCli(["doctor"], healthy.env);
    assert.equal(out.code, 0, out.stdout + out.stderr);
    assert.match(out.stdout, /^✓ healthy · 6 modules on · 2 agents wired$/m);

    // Signed in with Cloud unreachable: signing in again cannot fix an outage,
    // so it is a note, not a failure, and never says caveman login.
    const signed = await runCli(["doctor"], { ...healthy.env, CAVE_TOKEN: "test-token" });
    assert.equal(signed.code, 0, signed.stdout + signed.stderr);
    assert.match(signed.stdout, /^· routing: waiting for Cloud routing$/m);
    assert.match(signed.stdout, /^· cloud: no answer from http:\/\/127\.0\.0\.1:9 · try again later$/m);
    assert.doesNotMatch(signed.stdout, /caveman login/);

    // A Cloud that refuses the sign-in is the one case login fixes.
    const refusing = createHttpServer((req, res) => { res.statusCode = 401; res.end("{}"); });
    await new Promise((resolve) => refusing.listen(0, "127.0.0.1", resolve));
    try {
      const refused = await runCli(["doctor"], { ...healthy.env, CAVE_TOKEN: "test-token", CAVE_API_URL: `http://127.0.0.1:${refusing.address().port}` });
      assert.equal(refused.code, 1);
      assert.match(refused.stdout, /^✗ cloud: .* answered 401 · fix: caveman login$/m);
    } finally {
      await new Promise((resolve) => refusing.close(resolve));
    }
  } finally {
    healthy.cleanup();
  }
});

// An upgrader from before modules (agents wired with `enable`) has no module
// state yet: routing and scripts do nothing until setup records them, which
// is not a failure.
test("before setup, modules that wait on it say not set up and point at setup", async () => {
  const fx = modulesFixture();
  try {
    for (const agent of ["claude", "codex"]) assert.equal((await runCli(["enable", agent], fx.env)).code, 0);
    const status = await runCli(["status"], fx.env);
    assert.equal(status.code, 0, status.stderr);
    assert.match(status.stdout, /^  on  routing .*not set up · caveman setup$/m);
    assert.match(status.stdout, /^  on  scripts .*not set up · caveman setup$/m);
    assert.match(status.stdout, /\nnext: caveman setup\n$/);
    const doctor = await runCli(["doctor"], fx.env);
    assert.equal(doctor.code, 0, doctor.stdout);
    assert.match(doctor.stdout, /^· routing: not set up · caveman setup$/m);
    assert.match(doctor.stdout, /^· scripts: not set up · caveman setup$/m);
  } finally {
    fx.cleanup();
  }
});

test("doctor reports degraded agent wiring with its fix", async () => {
  const fx = modulesFixture({ agents: ["claude"] });
  try {
    assert.equal((await runCli(["on", "--all", "--yes"], fx.env)).code, 0);
    writeFileSync(join(fx.home, ".claude", "settings.json"), "{}\n");
    const out = await runCli(["doctor"], fx.env);
    assert.equal(out.code, 1);
    assert.match(out.stdout, /^✗ claude: wiring degraded · fix: caveman doctor claude --fix$/m);
  } finally {
    fx.cleanup();
  }
});

async function freePort() {
  const server = createServer();
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  await new Promise((resolve) => server.close(resolve));
  return port;
}

test("stop ends the runtime it started and is idempotent", async () => {
  const fx = modulesFixture();
  try {
    const idle = await runCli(["stop"], fx.env);
    assert.equal(idle.code, 0, idle.stderr);
    assert.equal(idle.stdout, "not running\n");

    const port = await freePort();
    const runDir = join(fx.env.CAVEMAN_HOME, "run");
    mkdirSync(runDir, { recursive: true });
    const runState = join(runDir, `${port}.json`);
    // Stands in for caveman-proxy: listens, publishes run state, removes it on SIGTERM.
    const runtime = spawn(process.execPath, ["-e", `
      const fs = require("node:fs");
      const server = require("node:net").createServer().listen(${port}, "127.0.0.1", () => {
        fs.writeFileSync(${JSON.stringify(runState)}, JSON.stringify({ schema: "caveman.proxy.run.v1", owner: "start", pid: process.pid, port: ${port}, instance_token: "t" }));
        process.stdout.write("ready\\n");
      });
      process.on("SIGTERM", () => { fs.rmSync(${JSON.stringify(runState)}, { force: true }); server.close(); process.exit(0); });
    `], { stdio: ["ignore", "pipe", "inherit"] });
    await new Promise((resolve) => runtime.stdout.once("data", resolve));
    const exited = new Promise((resolve) => runtime.once("exit", resolve));
    const env = { ...fx.env, CAVE_GATEWAY_URL: `http://127.0.0.1:${port}`, CAVEMAN_LISTEN: `127.0.0.1:${port}` };

    const stopped = await runCli(["stop"], env);
    assert.equal(stopped.code, 0, stopped.stderr);
    assert.equal(stopped.stdout, `✓ stopped · 127.0.0.1:${port}\n`);
    await exited;

    const again = await runCli(["stop"], env);
    assert.equal(again.code, 0, again.stderr);
    assert.equal(again.stdout, "not running\n");
  } finally {
    fx.cleanup();
  }
});

// A runtime keeps the binary it started from; once that binary is replaced,
// doctor and status say the running one is old instead of "healthy".
test("doctor and status name a runtime still running an older proxy", async () => {
  const fx = modulesFixture();
  const port = await freePort();
  const runFile = join(fx.env.CAVEMAN_HOME, "run", `${port}.json`);
  mkdirSync(join(fx.env.CAVEMAN_HOME, "run"), { recursive: true });
  const runtime = spawn(process.execPath, ["-e", `
    const fs = require("node:fs");
    require("node:net").createServer().listen(${port}, "127.0.0.1", () => {
      fs.writeFileSync(${JSON.stringify(runFile)}, JSON.stringify({ schema: "caveman.proxy.run.v1", owner: "wrap", pid: process.pid, port: ${port}, version: "bin-v0.0.1", mode: "compress", instance_token: "t" }));
      process.stdout.write("ready\\n");
    });
  `], { stdio: ["ignore", "pipe", "inherit"] });
  try {
    await new Promise((resolve) => runtime.stdout.once("data", resolve));
    const env = { ...fx.env, CAVE_GATEWAY_URL: `http://127.0.0.1:${port}`, CAVEMAN_LISTEN: `127.0.0.1:${port}` };
    const doctor = await runCli(["doctor"], env);
    assert.equal(doctor.code, 1, doctor.stdout);
    assert.match(doctor.stdout, new RegExp(`^✗ 127\\.0\\.0\\.1:${port} still runs caveman-proxy bin-v0\\.0\\.1; 1\\.0\\.0 is installed · fix: caveman stop, then start your agent again$`, "m"));
    const status = await runCli(["status"], env);
    assert.match(status.stdout, /^the running caveman proxy is bin-v0\.0\.1, but 1\.0\.0 is installed — it keeps the old one until it restarts · caveman stop, then start your agent again$/m);
  } finally {
    runtime.kill("SIGKILL");
    fx.cleanup();
  }
});

// Windows has no SIGTERM: process.kill ends a process at once, so stop first
// asks the proxy over its listener to drain, and kills only one that refuses.
test("stop on Windows asks the runtime to drain before ending it", async () => {
  const { endRuntimes } = await import("../dist/modules/stop.js");
  const fake = async () => {
    const child = spawn(process.execPath, ["-e", `
      const server = require("node:http").createServer((req, res) => {
        if (req.method === "POST" && req.url === "/caveman/shutdown" && req.headers["x-caveman-shutdown"] === "t") {
          res.writeHead(202).end(() => { process.stdout.write("drained\\n"); process.exit(0); });
        } else res.writeHead(404).end();
      }).listen(0, "127.0.0.1", () => process.stdout.write(server.address().port + "\\n"));
      process.on("SIGTERM", () => { process.stdout.write("killed\\n"); process.exit(0); });
    `], { stdio: ["ignore", "pipe", "inherit"] });
    let out = "";
    child.stdout.on("data", (chunk) => (out += chunk));
    const port = Number(await new Promise((resolve) => child.stdout.once("data", (chunk) => resolve(String(chunk).trim()))));
    out = "";
    const exited = new Promise((resolve) => child.once("exit", resolve));
    return { child, port, out: async () => { await exited; return out; } };
  };
  const current = await fake();
  assert.deepEqual(await endRuntimes([{ host: "127.0.0.1", port: current.port, listening: true, foreign: false, pid: current.child.pid, token: "t" }], "win32"), []);
  assert.equal(await current.out(), "drained\n");
  // An older proxy has no such endpoint (404): ended the hard way.
  const older = await fake();
  assert.deepEqual(await endRuntimes([{ host: "127.0.0.1", port: older.port, listening: true, foreign: false, pid: older.child.pid, token: "old" }], "win32"), []);
  assert.equal(await older.out(), "killed\n");

  // The token stop sends is the run-state file's shutdown token. The instance
  // token is no secret: /health/live hands it to any local caller.
  const fx = modulesFixture();
  const viaRunState = await fake();
  mkdirSync(join(fx.env.CAVEMAN_HOME, "run"), { recursive: true });
  writeFileSync(join(fx.env.CAVEMAN_HOME, "run", `${viaRunState.port}.json`), JSON.stringify({ schema: "caveman.proxy.run.v1", owner: "start", pid: viaRunState.child.pid, port: viaRunState.port, instance_token: "public", shutdown_token: "t" }));
  const env = { ...fx.env, CAVE_GATEWAY_URL: `http://127.0.0.1:${viaRunState.port}`, CAVEMAN_LISTEN: `127.0.0.1:${viaRunState.port}` };
  const saved = Object.fromEntries(Object.keys(env).map((key) => [key, process.env[key]]));
  Object.assign(process.env, env);
  try {
    await import("../dist/index.js");
    const { moduleHost } = await import("../dist/modules/apply.js");
    const runtimes = await moduleHost().localRuntimes();
    assert.deepEqual(runtimes.map((runtime) => runtime.token), ["t"]);
    assert.deepEqual(await endRuntimes(runtimes, "win32"), []);
  } finally {
    for (const [key, value] of Object.entries(saved)) value === undefined ? delete process.env[key] : process.env[key] = value;
    fx.cleanup();
  }
  assert.equal(await viaRunState.out(), "drained\n");
});

test("status counts the MCP entry native wiring writes for Claude and Codex", async () => {
  const fx = modulesFixture({ blocks: true });
  try {
    for (const agent of ["claude", "codex"]) assert.equal((await runCli(["enable", agent], fx.env)).code, 0);
    const wired = await runCli(["status"], fx.env);
    assert.equal(wired.code, 0, wired.stderr);
    assert.doesNotMatch(wired.stdout, /MCP recovery missing/);
    const doctor = await runCli(["doctor"], fx.env);
    assert.equal(doctor.code, 0, doctor.stdout);

    const claudePath = join(fx.home, ".claude.json");
    const claude = JSON.parse(readFileSync(claudePath, "utf8"));
    delete claude.mcpServers.caveman;
    writeFileSync(claudePath, JSON.stringify(claude, null, 2) + "\n");
    const codexPath = join(fx.home, ".codex", "config.toml");
    writeFileSync(codexPath, readFileSync(codexPath, "utf8").replace(/\[mcp_servers\.caveman\]\n[\s\S]*?(?=# <<< caveman:native-tables)/, ""));
    const missing = await runCli(["status"], fx.env);
    assert.match(missing.stdout, /^MCP recovery missing — .* · caveman tools mcp install <agent>$/m);
  } finally {
    fx.cleanup();
  }
});
