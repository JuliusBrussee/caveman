// execute.mcp is the MCP-surface knob: how much caveman injects into the wrapped
// agent's prompt prefix. The five engine MCP tools are a measured ~11k tokens of
// tool schema on every call, so "marker-only" exists to stop paying that while
// keeping the recovery contract honest.
//
// The binding property these tests defend: the proxy is NEVER told an MCP
// retrieval tool exists when none is available, and never told one is absent when
// it IS available. Claude and Codex auto mode injects a verified MCP binary only
// into the current launch; explicit prior installs remain valid evidence under the
// non-injecting modes. No config value can make the proxy elide bytes the agent
// cannot expand.
import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { isolatedCliEnv, runCli } from "./_cli.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");
const { buildWrapEnv } = await import(`${pathToFileURL(cli).href}?mcp-surface`);
const { PROFILES } = await import(pathToFileURL(join(here, "..", "dist", "agents.generated.js")).href);

function writeGlobal(home, value) {
  const dir = join(home, ".caveman-cloud");
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, "config.json"), JSON.stringify(value, null, 2), { mode: 0o600 });
  return join(dir, "config.json");
}

// ── config parsing: the four accepted values, and everything else refused ──────

test("config accepts marker-only alongside auto and the boolean pair", async () => {
  const isolated = isolatedCliEnv();
  const path = writeGlobal(isolated.home, { deviceId: "device-stable" });
  try {
    // default
    const initial = await runCli(["config", "get", "execute.mcp"], { env: isolated.env, prefix: "tools" });
    assert.equal(initial.stdout, "execute.mcp = auto  (default)\n");

    for (const [written, shown] of [["marker-only", "marker-only"], ["false", "false"], ["true", "true"], ["auto", "auto"]]) {
      const set = await runCli(["config", "set", "execute.mcp", written], { env: isolated.env, prefix: "tools" });
      assert.equal(set.code, 0, set.stderr);
      assert.equal(set.stdout, `execute.mcp = ${shown}  (global)\n`);
      const get = await runCli(["config", "get", "execute.mcp"], { env: isolated.env, prefix: "tools" });
      assert.equal(get.stdout, `execute.mcp = ${shown}  (global)\n`);
    }
    // booleans stay booleans on disk; the named modes stay strings.
    await runCli(["config", "set", "execute.mcp", "false"], { env: isolated.env, prefix: "tools" });
    assert.equal(JSON.parse(readFileSync(path, "utf8")).execute.mcp, false);
    await runCli(["config", "set", "execute.mcp", "marker-only"], { env: isolated.env, prefix: "tools" });
    assert.equal(JSON.parse(readFileSync(path, "utf8")).execute.mcp, "marker-only");
    assert.equal(JSON.parse(readFileSync(path, "utf8")).deviceId, "device-stable");
  } finally {
    isolated.cleanup();
  }
});

test("config rejects an unknown MCP surface and leaves the file byte-identical", async () => {
  const isolated = isolatedCliEnv();
  const path = writeGlobal(isolated.home, { deviceId: "device-stable", execute: { mcp: "marker-only" } });
  const before = readFileSync(path);
  try {
    const out = await runCli(["config", "set", "execute.mcp", "markers"], { env: isolated.env, prefix: "tools" });
    assert.equal(out.code, 2);
    assert.equal(out.stderr, 'not an MCP surface: "markers" — valid: auto | marker-only | true | false\n');
    assert.deepEqual(readFileSync(path), before);
  } finally {
    isolated.cleanup();
  }
});

test("a stored garbage MCP surface falls back to auto rather than inventing a surface", async () => {
  const isolated = isolatedCliEnv();
  writeGlobal(isolated.home, { execute: { mcp: "markers" } });
  try {
    const out = await runCli(["config", "get", "execute.mcp"], { env: isolated.env, prefix: "tools" });
    assert.equal(out.code, 0, out.stderr);
    assert.equal(out.stdout, "execute.mcp = auto  (default)\n");
  } finally {
    isolated.cleanup();
  }
});

test("legacy wrap.mcp and the project overlay both reach the new surface values", async () => {
  const legacy = isolatedCliEnv();
  writeGlobal(legacy.home, { wrap: { mcp: "marker-only" } });
  const overlaid = isolatedCliEnv();
  writeGlobal(overlaid.home, { execute: { mcp: "auto" } });
  const cwd = mkdtempSync(join(tmpdir(), "cave-mcp-overlay-"));
  mkdirSync(join(cwd, ".caveman"), { recursive: true });
  writeFileSync(join(cwd, ".caveman", "config.json"), JSON.stringify({ execute: { mcp: "marker-only" } }));
  try {
    const legacyOut = await runCli(["config", "get", "execute.mcp"], { env: legacy.env, prefix: "tools" });
    assert.equal(legacyOut.stdout, "execute.mcp = marker-only  (legacy-wrap)\n");
    const overlayOut = await runCli(["config", "get", "execute.mcp"], { env: overlaid.env, prefix: "tools", cwd });
    assert.equal(overlayOut.stdout, "execute.mcp = marker-only  (project)\n");
  } finally {
    legacy.cleanup();
    overlaid.cleanup();
    rmSync(cwd, { recursive: true, force: true });
  }
});

// ── mode → install decision, and mode → CAVEMAN_RECOVERY stamping ─────────────

// One wrap run against a stub codex and a stub proxy that records the recovery
// env it was launched with. `preinstalled` seeds the marker a previous explicit
// `caveman mcp install codex` would have left, so the coexistence case is real.
async function wrapCodex({ mcp, preinstalled = false }) {
  const home = mkdtempSync(join(tmpdir(), "cave-mcp-surface-"));
  const binDir = join(home, "bin");
  mkdirSync(binDir, { recursive: true });
  mkdirSync(join(home, ".caveman", "mcp"), { recursive: true });
  writeGlobal(home, { execute: { mcp, browse_tool: false }, think: { shrink: false } });
  if (preinstalled) writeFileSync(join(home, ".caveman", "mcp", "codex.json"), "{}");

  writeFileSync(join(binDir, "codex"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  writeFileSync(join(binDir, "caveman-mcp"), `#!/bin/sh
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  printf '%s\\n' '{"version":"test","capabilities":["mcp_recovery"]}'
fi
exit 0
`, { mode: 0o755 });

  const recoveryFile = join(home, "recovery.txt");
  const pidFile = join(home, "proxy.pid");
  const proxy = join(binDir, "proxy.mjs");
  writeFileSync(proxy, `#!/usr/bin/env node
import { createServer } from "node:http";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
if (process.argv[2] === "version") {
  process.stdout.write(JSON.stringify({ version: "test", capabilities: ["run_state", "native_runtime_v1", "typed_ccr"] }));
  process.exit(0);
}
const url = new URL(process.env.CAVE_GATEWAY_URL);
const runState = join(process.env.CAVEMAN_HOME, "run", url.port + ".json");
if (process.argv[2] === "status") {
  try { process.stdout.write(readFileSync(runState, "utf8")); }
  catch { process.stdout.write(JSON.stringify({ owner: "unknown" })); }
  process.exit(0);
}
if (process.argv[2] === "stats") { process.stdout.write("{}"); process.exit(0); }
writeFileSync(${JSON.stringify(recoveryFile)}, process.env.CAVEMAN_RECOVERY ?? "<unset>");
writeFileSync(${JSON.stringify(pidFile)}, String(process.pid));
mkdirSync(join(process.env.CAVEMAN_HOME, "run"), { recursive: true });
writeFileSync(runState, JSON.stringify({
  schema: "caveman.proxy.run.v1",
  owner: process.env.CAVEMAN_PROXY_OWNER || "wrap",
  mode: process.env.CAVEMAN_MODE || "record",
  instance_token: "mcp-surface",
  pid: process.pid,
  port: Number(url.port),
  listen: url.hostname + ":" + url.port,
  version: "test",
  recovery_via_mcp: process.env.CAVEMAN_RECOVERY === "mcp",
}));
createServer((req, res) => res.end("ok")).listen(Number(url.port), url.hostname);
setInterval(() => {}, 1000);
`, { mode: 0o755 });

  const env = {
    ...process.env,
    NO_COLOR: "1",
    CI: "1",
    HOME: home,
    CAVEMAN_HOME: join(home, ".caveman"),
    CAVE_NO_KEYCHAIN: "1",
    CAVEMAN_PROXY_BIN: proxy,
    CAVE_GATEWAY_URL: `http://127.0.0.1:${20000 + Math.floor(Math.random() * 15000)}`,
    PATH: `${binDir}:${process.env.PATH}`,
  };
  delete env.CAVEMAN_RECOVERY;
  delete env.CAVEMAN_BROWSE_BIN;
  delete env.CAVEMAN_MCP_BIN;

  const out = await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, "wrap", "codex"], { env });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  try {
    if (existsSync(pidFile)) process.kill(Number(readFileSync(pidFile, "utf8")), "SIGTERM");
  } catch { /* proxy already gone */ }

  const tomlPath = join(home, ".codex", "config.toml");
  return {
    ...out,
    recovery: existsSync(recoveryFile) ? readFileSync(recoveryFile, "utf8") : null,
    registered: existsSync(tomlPath) && /\[mcp_servers\.caveman\]/.test(readFileSync(tomlPath, "utf8")),
    marker: existsSync(join(home, ".caveman", "mcp", "codex.json")),
    cleanup: () => rmSync(home, { recursive: true, force: true }),
  };
}

test("auto injects ephemeral Codex MCP recovery without mutating persistent config", async () => {
  const out = await wrapCodex({ mcp: "auto" });
  try {
    assert.equal(out.code, 0, out.stderr);
    assert.ok(!out.registered, "auto must keep the user's Codex config untouched");
    assert.ok(!out.marker, "ephemeral injection must not claim a persistent install");
    assert.equal(out.recovery, "mcp", "the verified ephemeral retrieve tool must be signalled");
  } finally {
    out.cleanup();
  }
});

test("marker-only injects nothing and tells the proxy there is no MCP recovery", async () => {
  const out = await wrapCodex({ mcp: "marker-only" });
  try {
    assert.equal(out.code, 0, out.stderr);
    assert.ok(!out.registered, "marker-only must not write the agent's MCP config");
    assert.ok(!out.marker, "marker-only must not write an install marker");
    // The honest zero: nothing is installed, so nothing may be claimed. The proxy
    // falls back to its own server-side retrieve on non-streaming API-key traffic
    // and passes everything else through.
    assert.equal(out.recovery, "", "no installed tool must never be reported as MCP recovery");
    assert.match(out.stderr, /MCP surface marker-only by your config/);
    assert.doesNotMatch(out.stderr, /MCP recovery missing/);
    // Nothing may tell the operator to run `mcp install` to undo a choice they
    // made in config; every remedy points at the knob instead.
    assert.doesNotMatch(out.stderr, /mcp install/);
    assert.match(out.stderr, /execute\.mcp=marker-only, so no caveman_retrieve tool is injected/);
  } finally {
    out.cleanup();
  }
});

test("marker-only leaves a previous install alone and keeps its recovery signal", async () => {
  const out = await wrapCodex({ mcp: "marker-only", preinstalled: true });
  try {
    assert.equal(out.code, 0, out.stderr);
    assert.ok(!out.registered, "marker-only must still not write the agent's MCP config");
    assert.ok(out.marker, "marker-only must not remove what a previous run or the user installed");
    assert.equal(out.recovery, "mcp", "a tool that really is installed must not be denied to the proxy");
    assert.doesNotMatch(out.stderr, /no caveman_retrieve tool is injected/);
  } finally {
    out.cleanup();
  }
});

test("execute.mcp false keeps its pre-existing skip-install behavior", async () => {
  const out = await wrapCodex({ mcp: false });
  try {
    assert.equal(out.code, 0, out.stderr);
    assert.ok(!out.registered);
    assert.ok(!out.marker);
    assert.equal(out.recovery, "");
    // false is not marker-only: it keeps the original off-state wording.
    assert.match(out.stderr, /MCP recovery missing/);
  } finally {
    out.cleanup();
  }
});

// `false` stops new injection but does not erase an explicit prior install.
test("execute.mcp false preserves recovery from a previous explicit install", async () => {
  const out = await wrapCodex({ mcp: false, preinstalled: true });
  try {
    assert.equal(out.code, 0, out.stderr);
    assert.equal(out.recovery, "mcp", "an install that really exists is still real recovery");
    assert.doesNotMatch(out.stderr, /MCP recovery missing/);
  } finally {
    out.cleanup();
  }
});

// ── config-file agents: the SECOND injection site ─────────────────────────────

// openclaw does not get the caveman MCP server from `mcp install`; it gets it
// from the profile's own config_overlay, re-rendered into a temp config on every
// launch. Skipping the install therefore did nothing for this whole agent class,
// and `mcp uninstall` could never have removed it.
function openclawBase() {
  const dir = mkdtempSync(join(tmpdir(), "cave-mcp-openclaw-"));
  const base = join(dir, "openclaw.json");
  writeFileSync(base, JSON.stringify({
    agents: { defaults: { model: { primary: "myprov/gpt-test" } } },
    models: {
      providers: {
        myprov: {
          apiKey: "${MYPROV_API_KEY}",
          api: "openai-completions",
          models: [{ id: "gpt-test", name: "GPT Test" }],
        },
      },
    },
  }));
  return { dir, base };
}

function renderOpenclawConfig(mcpMode) {
  const { dir, base } = openclawBase();
  const saved = { ...process.env };
  Object.assign(process.env, {
    HOME: dir,
    CAVEMAN_HOME: join(dir, ".caveman"),
    OPENCLAW_CONFIG_PATH: base,
    MYPROV_API_KEY: "sk-openclaw",
  });
  try {
    const agent = PROFILES.find((p) => p.id === "openclaw");
    assert.ok(agent, "missing generated openclaw profile");
    const env = buildWrapEnv(agent, "http://127.0.0.1:19500", mcpMode);
    return JSON.parse(readFileSync(env.OPENCLAW_CONFIG_PATH, "utf8"));
  } finally {
    for (const key of Object.keys(process.env)) if (!(key in saved)) delete process.env[key];
    Object.assign(process.env, saved);
    rmSync(dir, { recursive: true, force: true });
  }
}

test("config-file agents get the caveman MCP server under auto and not under marker-only", () => {
  const auto = renderOpenclawConfig("auto");
  assert.ok(auto.mcp?.servers?.caveman, "auto must still merge the profile's caveman MCP server");

  for (const mode of ["marker-only", "off"]) {
    const stripped = renderOpenclawConfig(mode);
    assert.ok(!stripped.mcp?.servers?.caveman, `${mode} must strip the profile's caveman MCP server`);
    // Pruned, not left as an empty husk: a stripped overlay merges to the same
    // bytes a config with no overlay would have.
    assert.ok(stripped.mcp === undefined || stripped.mcp.servers === undefined
      || Object.keys(stripped.mcp.servers).length > 0);
    // Everything else the overlay does must survive untouched.
    assert.equal(stripped.models.providers.caveman.headers["x-cave-agent"], "openclaw");
  }
});

test("a config-file agent's recovery signal follows what this launch actually injects", async () => {
  const runs = {};
  for (const mode of ["auto", "marker-only"]) {
    const home = mkdtempSync(join(tmpdir(), "cave-mcp-oc-wrap-"));
    const binDir = join(home, "bin");
    mkdirSync(binDir, { recursive: true });
    mkdirSync(join(home, ".caveman", "mcp"), { recursive: true });
    writeGlobal(home, { execute: { mcp: mode, browse_tool: false }, think: { shrink: false } });
    const { base } = openclawBase();
    writeFileSync(join(binDir, "openclaw"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
    writeFileSync(join(binDir, "caveman-mcp"), `#!/bin/sh
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  printf '%s\\n' '{"version":"test","capabilities":["mcp_recovery"]}'
fi
exit 0
`, { mode: 0o755 });
    const recoveryFile = join(home, "recovery.txt");
    const pidFile = join(home, "proxy.pid");
    const proxy = join(binDir, "proxy.mjs");
    writeFileSync(proxy, `#!/usr/bin/env node
import { createServer } from "node:http";
import { writeFileSync } from "node:fs";
if (process.argv[2] === "stats") { process.stdout.write("{}"); process.exit(0); }
const url = new URL(process.env.CAVE_GATEWAY_URL);
writeFileSync(${JSON.stringify(recoveryFile)}, process.env.CAVEMAN_RECOVERY ?? "<unset>");
writeFileSync(${JSON.stringify(pidFile)}, String(process.pid));
createServer((req, res) => res.end("ok")).listen(Number(url.port), url.hostname);
setInterval(() => {}, 1000);
`, { mode: 0o755 });
    const env = {
      ...process.env,
      NO_COLOR: "1",
      CI: "1",
      HOME: home,
      CAVEMAN_HOME: join(home, ".caveman"),
      CAVE_NO_KEYCHAIN: "1",
      CAVE_BINARY_PROBE_TIMEOUT_MS: "10000",
      CAVEMAN_PROXY_BIN: proxy,
      OPENCLAW_CONFIG_PATH: base,
      MYPROV_API_KEY: "sk-openclaw",
      CAVE_GATEWAY_URL: `http://127.0.0.1:${20000 + Math.floor(Math.random() * 15000)}`,
      PATH: `${binDir}:${process.env.PATH}`,
    };
    delete env.CAVEMAN_RECOVERY;
    delete env.CAVEMAN_BROWSE_BIN;
    delete env.CAVEMAN_MCP_BIN;
    const out = await new Promise((resolve, reject) => {
      const child = spawn(process.execPath, [cli, "wrap", "openclaw"], { env });
      let stderr = "";
      child.stderr.on("data", (d) => (stderr += d));
      child.on("exit", (code) => resolve({ code, stderr }));
      child.on("error", reject);
    });
    try {
      if (existsSync(pidFile)) process.kill(Number(readFileSync(pidFile, "utf8")), "SIGTERM");
    } catch { /* proxy already gone */ }
    runs[mode] = {
      ...out,
      recovery: existsSync(recoveryFile) ? readFileSync(recoveryFile, "utf8") : null,
      cleanup: () => rmSync(home, { recursive: true, force: true }),
    };
  }
  try {
    assert.equal(runs.auto.code, 0, runs.auto.stderr);
    assert.equal(runs.auto.recovery, "mcp", "auto injects the overlay, so the tool really is there");
    assert.equal(runs["marker-only"].code, 0, runs["marker-only"].stderr);
    assert.equal(
      runs["marker-only"].recovery,
      "",
      "marker-only strips the overlay, so claiming MCP recovery would be a lie",
    );
    // No inert remedy: nothing was installed, so nothing is uninstallable.
    assert.doesNotMatch(runs["marker-only"].stderr, /mcp uninstall/);
  } finally {
    runs.auto.cleanup();
    runs["marker-only"].cleanup();
  }
});

// ── the start door: a different machine, so a different remedy ────────────────

// `caveman start` launches a bare proxy and installs nothing, so
// `config set execute.mcp auto` would change nothing there. Its remedy must stay
// the command that actually works at that door.
test("start under marker-only names a remedy that works at the start door", async () => {
  const isolated = isolatedCliEnv();
  writeGlobal(isolated.home, { think: { mode: "compress" }, execute: { mcp: "marker-only" } });
  const proxy = join(isolated.home, "proxy.mjs");
  writeFileSync(proxy, "#!/usr/bin/env node\nprocess.exit(0);\n", { mode: 0o755 });
  isolated.env.CAVEMAN_PROXY_BIN = proxy;
  isolated.env.CAVE_GATEWAY_URL = `http://127.0.0.1:${26000 + Math.floor(Math.random() * 6000)}`;
  delete isolated.env.CAVEMAN_RECOVERY;
  try {
    const port = new URL(isolated.env.CAVE_GATEWAY_URL).port;
    const out = await runCli(["start", "--port", port], { env: isolated.env });
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stderr, /caveman mcp install <agent>/);
    assert.doesNotMatch(out.stderr, /config set execute\.mcp auto/);
  } finally {
    isolated.cleanup();
  }
});
