import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
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
    assert.match(out.stdout, /  on  scripts      —          caveman-blocks not installed/);
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
    assert.match(out.stdout, /^✗ scripts: caveman-blocks not installed · fix: install it, or caveman off scripts$/m);
    assert.match(out.stdout, /^· routing: sign in to turn on routing · caveman login$/m);
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

    // Signed in with Cloud unreachable: the local result still prints, then the
    // Cloud failure, and the exit is non-zero.
    const signed = await runCli(["doctor"], { ...healthy.env, CAVE_TOKEN: "test-token" });
    assert.equal(signed.code, 1);
    assert.match(signed.stdout, /^· routing: waiting for Cloud routing$/m);
    assert.match(signed.stdout, /^✗ cloud: .* · fix: caveman login$/m);
  } finally {
    healthy.cleanup();
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
