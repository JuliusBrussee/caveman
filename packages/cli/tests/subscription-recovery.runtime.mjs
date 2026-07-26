import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

// Local compression of subscription/OAuth logins needs BOTH halves of the proxy's
// gate (`accountGatedLiveZoneAllowed`): a wrap entitlement AND a recovery path the
// agent owns (CAVEMAN_RECOVERY=mcp). These tests pin that the CLI stamps the second
// half and never announces compression the proxy has switched off.

const CLAIM = /compress locally too/;
const NO_RECOVERY = /stay byte-identical pass-through here/;

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

// run boots the CLI against a stub proxy that records the env it was launched with,
// an empty stub agent binary, and a throwaway CAVEMAN_HOME. mcpAgent, when set,
// writes the marker `caveman mcp install <agent>` leaves behind.
async function run(cliArgs, { entitled = true, mcpAgent = null, extraEnv = {}, agentName = "claude", listen = false } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "cave-subrec-"));
  const envFile = join(dir, "proxy-env.json");
  const pidFile = join(dir, "proxy-pid.txt");
  const proxy = join(dir, "proxy.mjs");
  const port = 21000 + Math.floor(Math.random() * 9000);
  writeFileSync(proxy, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
if (process.argv[2] === "stats") { process.stdout.write("{}"); process.exit(0); }
writeFileSync(${JSON.stringify(envFile)}, JSON.stringify({
  mode: process.env.CAVEMAN_MODE || "<unset>",
  entitled: process.env.CAVEMAN_WRAP_ENTITLED || "<unset>",
  recovery: process.env.CAVEMAN_RECOVERY || "<unset>",
}));
${listen
      ? `import { createServer } from "node:http";
writeFileSync(${JSON.stringify(pidFile)}, String(process.pid));
createServer((req, res) => res.end("ok")).listen(${port}, "127.0.0.1");
setInterval(() => {}, 1000);`
      : "process.exit(0);"}
`, { mode: 0o755 });
  writeFileSync(join(dir, agentName), "#!/bin/sh\nexit 0\n", { mode: 0o755 });

  const home = mkdtempSync(join(tmpdir(), "cave-subrec-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify(entitled ? validEntitlement() : {}, null, 2));
  if (mcpAgent) {
    mkdirSync(join(home, "mcp"), { recursive: true });
    writeFileSync(join(home, "mcp", `${mcpAgent}.json`), JSON.stringify({ server: "caveman" }));
    writeFileSync(join(dir, "caveman-mcp"), `#!/bin/sh
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  printf '%s\\n' '{"version":"test","capabilities":["mcp_recovery"]}'
fi
exit 0
`, { mode: 0o755 });
  }

  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    CAVEMAN_HOME: home,
    CAVEMAN_MCP_BIN: mcpAgent ? join(dir, "caveman-mcp") : "/nonexistent/caveman-mcp",
    CAVEMAN_BROWSE_BIN: "/nonexistent/caveman-browse",
    CAVE_GATEWAY_URL: `http://127.0.0.1:${port}`,
    CAVEMAN_PROXY_BIN: proxy,
    PATH: `${dir}:${process.env.PATH}`,
    CAVEMAN_MODE: "compress",
    ...extraEnv,
  };
  delete env.CAVEMAN_RECOVERY;
  if (extraEnv.CAVEMAN_RECOVERY) env.CAVEMAN_RECOVERY = extraEnv.CAVEMAN_RECOVERY;

  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...cliArgs], { env });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.stdout.on("data", () => {});
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  if (existsSync(pidFile)) {
    try { process.kill(Number(readFileSync(pidFile, "utf8")), "SIGTERM"); } catch { /* already gone */ }
  }
  return { ...out, proxyEnv: existsSync(envFile) ? JSON.parse(readFileSync(envFile, "utf8")) : {} };
}

test("start: entitled with no MCP recovery stamps nothing and says compression is off", async () => {
  const out = await run(["start"]);
  assert.equal(out.proxyEnv.entitled, "1", "the account half of the gate is stamped");
  assert.equal(out.proxyEnv.recovery, "<unset>", "no MCP install evidence must not claim a recovery path");
  assert.match(out.stderr, NO_RECOVERY);
  assert.match(out.stderr, /caveman mcp install/);
  assert.doesNotMatch(out.stderr, CLAIM, "must not announce compression the proxy has off");
});

test("start: an installed caveman MCP server stamps CAVEMAN_RECOVERY=mcp and earns the claim", async () => {
  const out = await run(["start"], { mcpAgent: "claude" });
  assert.equal(out.proxyEnv.entitled, "1");
  assert.equal(out.proxyEnv.recovery, "mcp");
  assert.match(out.stderr, CLAIM);
  assert.match(out.stderr, /re-sent byte-identically so the provider cache stays warm/);
  assert.match(out.stderr, /counted in tokens only .*no dollar figure is claimed/);
  assert.doesNotMatch(out.stderr, NO_RECOVERY);
});

test("start: an explicit CAVEMAN_RECOVERY=mcp is the operator's own opt-in", async () => {
  const out = await run(["start"], { extraEnv: { CAVEMAN_RECOVERY: "mcp" } });
  assert.equal(out.proxyEnv.recovery, "mcp");
  assert.match(out.stderr, CLAIM);
});

// The recovery signal is stamped, not inherited: start resolves the question once
// (an explicit CAVEMAN_RECOVERY=mcp is the operator's own opt-in, the test above)
// and the child sees THAT answer — never some other value the environment carried.
test("start: an inherited non-mcp CAVEMAN_RECOVERY is replaced by start's own answer", async () => {
  const out = await run(["start"], { extraEnv: { CAVEMAN_RECOVERY: "definitely-not-mcp" } });
  assert.equal(out.proxyEnv.recovery, "<unset>", "start must stamp the answer it resolved, not pass an inherited claim through");
  assert.match(out.stderr, NO_RECOVERY);
  assert.doesNotMatch(out.stderr, CLAIM);
});

test("start: no entitlement never claims compression, whatever the environment says", async () => {
  const out = await run(["start"], { entitled: false, mcpAgent: "claude", extraEnv: { CAVEMAN_WRAP_ENTITLED: "1" } });
  assert.equal(out.proxyEnv.entitled, "0", "the account signal is stamped explicitly, never inherited");
  assert.doesNotMatch(out.stderr, CLAIM);
});

test("wrap: entitled agent without the MCP retrieve tool says compression is off", async () => {
  const out = await run(["wrap", "claude"], { listen: true });
  assert.equal(out.proxyEnv.entitled, "1");
  assert.equal(out.proxyEnv.recovery, "<unset>");
  assert.match(out.stderr, NO_RECOVERY);
  assert.doesNotMatch(out.stderr, CLAIM);
});

// The gate bypass this closes: wrap inherited its env verbatim, so an exported
// CAVEMAN_RECOVERY=mcp reached the proxy even when THIS agent has no caveman_retrieve
// tool. The proxy would then elide bytes behind markers the agent cannot expand,
// while the CLI printed that compression was off. wrap answers that question from the
// agent's own MCP install and stamps the answer. (honesty rule: no-placeholder)
test("wrap: an inherited CAVEMAN_RECOVERY=mcp cannot outlive the agent's own MCP check", async () => {
  const out = await run(["wrap", "claude"], { listen: true, extraEnv: { CAVEMAN_RECOVERY: "mcp" } });
  assert.equal(out.proxyEnv.entitled, "1");
  assert.equal(out.proxyEnv.recovery, "<unset>", "an inherited recovery claim must not survive the agent's MCP check");
  assert.match(out.stderr, NO_RECOVERY);
  assert.doesNotMatch(out.stderr, CLAIM);
});

test("wrap: entitled agent with the MCP retrieve tool installed earns the claim", async () => {
  const out = await run(["wrap", "claude"], { mcpAgent: "claude", listen: true });
  assert.equal(out.proxyEnv.entitled, "1");
  assert.equal(out.proxyEnv.recovery, "mcp");
  assert.match(out.stderr, CLAIM);
  assert.match(out.stderr, /counted in tokens only .*no dollar figure is claimed/);
  assert.doesNotMatch(out.stderr, NO_RECOVERY);
});
