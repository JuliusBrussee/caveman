import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function sha256(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function writeOpenClawStub(binDir, body = "") {
  const stub = join(binDir, "openclaw");
  writeFileSync(stub, `#!/usr/bin/env node
${body || `import { existsSync, readFileSync, writeFileSync } from "node:fs";
const args = process.argv.slice(2);
const configPath = process.env.OPENCLAW_CONFIG_PATH || "";
if (args[0] === "mcp") {
  if (configPath) writeFileSync(configPath, (existsSync(configPath) ? readFileSync(configPath, "utf8") : "") + "\\nMUTATED_BY_MCP\\n");
  process.exit(0);
}
let config = "";
try { config = readFileSync(configPath, "utf8"); } catch {}
process.stdout.write(JSON.stringify({
  configPath,
  args,
  config,
  configExistedDuringChild: configPath ? existsSync(configPath) : false,
}));`}
`, { mode: 0o755 });
  return stub;
}

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

function writeWrapConfig(home, wrap) {
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  // Seed a valid wrap entitlement so the account gate keeps local
  // compression on — these tests verify the compress-mode overlay + MCP recovery.
  const wrapEntitlement = {
    entitled: true, plan: "free", telemetry_level: "metadata",
    seats_used: 1, seats_limit: 1, devices_used: 1, devices_limit: 3,
    evicted_device_hash: null, expires_at: new Date(Date.now() + 72 * 3600 * 1000).toISOString(),
  };
  writeFileSync(
    join(home, ".caveman-cloud", "config.json"),
    JSON.stringify({ wrap, wrapEntitlement, wrapEntitlementFetchedAt: new Date().toISOString() }, null, 2),
  );
}

function fixtureEnv(baseConfigPath, extra = {}) {
  const home = mkdtempSync(join(tmpdir(), "cave-openclaw-home-"));
  const caveHome = mkdtempSync(join(tmpdir(), "cave-openclaw-dot-"));
  const binDir = mkdtempSync(join(tmpdir(), "cave-openclaw-bin-"));
  writeOpenClawStub(binDir);
  writeWrapConfig(home, { browse: false });
  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    CAVEMAN_HOME: caveHome,
    PATH: `${binDir}:${process.env.PATH}`,
  };
  if (baseConfigPath) env.OPENCLAW_CONFIG_PATH = baseConfigPath;
  else delete env.OPENCLAW_CONFIG_PATH;
  delete env.CAVE_GATEWAY_URL;
  for (const key of ["OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "CAVE_API_KEY", "MYPROV_API_KEY"]) {
    delete env[key];
  }
  Object.assign(env, extra);
  return { env, home, caveHome, binDir };
}

function apiKeyConfig() {
  return JSON.stringify({
    agents: {
      defaults: {
        model: { primary: "myprov/gpt-test" },
        models: { "myprov/gpt-test": { params: { fastMode: true } } },
      },
    },
    models: {
      providers: {
        myprov: {
          baseUrl: "https://provider.example/v1",
          apiKey: "${MYPROV_API_KEY}",
          api: "openai-completions",
          models: [{
            id: "gpt-test",
            name: "GPT Test",
            reasoning: true,
            input: ["text"],
            cost: { input: 1, output: 2, cacheRead: 0.1, cacheWrite: 0.2 },
            contextWindow: 123456,
            maxTokens: 4096,
          }],
        },
      },
    },
  }, null, 2);
}

test("wrap openclaw writes a temp overlay config, preserves user config, and reroutes API-key primary", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-openclaw-config-"));
  const base = join(dir, "openclaw.json");
  writeFileSync(base, apiKeyConfig());
  const before = sha256(base);
  const proxyEnv = join(dir, "proxy-env.json");
  const proxyBin = join(dir, "proxy.mjs");
  writeFileSync(proxyBin, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
if (process.argv[2] === "stats") { process.stdout.write("{}"); process.exit(0); }
writeFileSync(${JSON.stringify(proxyEnv)}, JSON.stringify({ recovery: process.env.CAVEMAN_RECOVERY || "" }));
`, { mode: 0o755 });
  const { env, caveHome, binDir } = fixtureEnv(base, {
    CAVE_GATEWAY_URL: "http://127.0.0.1:18809",
    CAVEMAN_PROXY_BIN: proxyBin,
    MYPROV_API_KEY: "sk-myprov-local",
  });
  writeFileSync(join(binDir, "caveman-mcp"), `#!/usr/bin/env sh
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  printf '%s\\n' '{"version":"test","capabilities":["mcp_recovery"]}'
fi
exit 0
`, { mode: 0o755 });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(sha256(base), before, "wrap must not mutate the user-owned OpenClaw config");
  assert.equal(JSON.parse(readFileSync(proxyEnv, "utf8")).recovery, "mcp", "overlay MCP must make proxy recovery eligible without persistent install");

  const child = JSON.parse(out.stdout);
  assert.deepEqual(child.args, ["chat"], "profile args must launch openclaw chat");
  assert.match(basename(dirname(child.configPath)), /^caveman-wrap-/, "child must receive a Caveman temp config directory");
  assert.equal(basename(child.configPath), "openclaw.json");
  assert.equal(child.configExistedDuringChild, true, "temp overlay config must exist while child runs");
  assert.equal(existsSync(child.configPath), false, "temp overlay config must be cleaned after child exits");
  assert.equal(existsSync(dirname(child.configPath)), false, "temp overlay directory must be cleaned after child exits");

  const cfg = JSON.parse(child.config);
  assert.equal(cfg.models.providers.caveman.baseUrl, "http://127.0.0.1:18809/w/openclaw/v1");
  assert.equal(cfg.models.providers.caveman.api, "openai-completions");
  assert.equal(cfg.models.providers.caveman.apiKey, "sk-myprov-local");
  assert.equal(cfg.models.providers.caveman.headers["x-cave-agent"], "openclaw");
  assert.equal(cfg.models.providers.caveman.models[0].id, "gpt-test");
  assert.equal(cfg.models.providers.caveman.models[0].contextWindow, 123456);
  assert.equal(cfg.models.providers.caveman.models[0].maxTokens, 4096);
  assert.equal(cfg.agents.defaults.model.primary, "caveman/gpt-test");
  assert.deepEqual(cfg.agents.defaults.models["caveman/gpt-test"], { params: { fastMode: true } });
  assert.equal(cfg.mcp.servers.caveman.command, "caveman-mcp");
  assert.deepEqual(cfg.mcp.servers.caveman.args, []);
  assert.equal(cfg.plugins.entries["caveman-shrink"].enabled, true);
  assert.equal(cfg.plugins.entries["caveman-shrink"].hooks.allowConversationAccess, true);
  assert.ok(cfg.plugins.load.paths.some((p) => p.startsWith(join(caveHome, "openclaw", "plugins"))));
});

test("wrap openclaw leaves OAuth primary unchanged and still injects MCP/plugin", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-openclaw-oauth-"));
  const base = join(dir, "openclaw.json");
  writeFileSync(base, JSON.stringify({
    agents: { defaults: { model: { primary: "openai-codex/gpt-5" } } },
    models: { providers: { "openai-codex": { auth: "oauth", api: "openai-chatgpt-responses", models: [{ id: "gpt-5", name: "GPT-5" }] } } },
  }));
  const { env, home } = fixtureEnv(base);
  writeWrapConfig(home, { proxy: false, browse: false });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stderr, /uses OAuth; leaving primary model unchanged/);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.equal(cfg.agents.defaults.model.primary, "openai-codex/gpt-5");
  assert.equal(cfg.models.providers.caveman, undefined);
  assert.equal(cfg.mcp.servers.caveman.command, "caveman-mcp");
  assert.equal(cfg.plugins.entries["caveman-shrink"].enabled, true);
});

test("wrap openclaw treats bare well-known openai primary without env key as OAuth default", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-openclaw-openai-oauth-"));
  const base = join(dir, "openclaw.json");
  writeFileSync(base, JSON.stringify({
    agents: { defaults: { model: { primary: "openai/gpt-5.5" } } },
  }));
  const { env, home } = fixtureEnv(base);
  writeWrapConfig(home, { proxy: false, browse: false });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stderr, /uses OAuth; leaving primary model unchanged/);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.equal(cfg.agents.defaults.model.primary, "openai/gpt-5.5");
  assert.equal(cfg.models?.providers?.caveman, undefined);
  assert.equal(cfg.mcp.servers.caveman.command, "caveman-mcp");
});

test("wrap openclaw reroutes bare well-known openai primary when OPENAI_API_KEY is set", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-openclaw-openai-key-"));
  const base = join(dir, "openclaw.json");
  writeFileSync(base, JSON.stringify({
    agents: { defaults: { model: { primary: "openai/gpt-5.5" } } },
  }));
  const { env, home } = fixtureEnv(base, { OPENAI_API_KEY: "sk-openai-real" });
  writeWrapConfig(home, { proxy: false, browse: false });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.equal(cfg.agents.defaults.model.primary, "caveman/gpt-5.5");
  assert.equal(cfg.models.providers.caveman.api, "openai-responses");
  assert.equal(cfg.models.providers.caveman.apiKey, "sk-openai-real");
  assert.equal(cfg.models.providers.caveman.baseUrl, "http://127.0.0.1:8787/w/openclaw/v1");
});

test("wrap openclaw managed mode uses Caveman key auth and preserves upstream key header", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-openclaw-managed-"));
  const base = join(dir, "openclaw.json");
  writeFileSync(base, apiKeyConfig());
  const { env, home } = fixtureEnv(base, { CAVE_GATEWAY_URL: "https://gw.example.com", CAVE_API_KEY: "cave-managed-key", MYPROV_API_KEY: "sk-myprov-managed" });
  writeWrapConfig(home, { proxy: false, browse: false });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.equal(cfg.models.providers.caveman.baseUrl, "https://gw.example.com/w/openclaw/v1");
  assert.equal(cfg.models.providers.caveman.apiKey, "cave-managed-key");
  assert.equal(cfg.models.providers.caveman.headers["x-cave-upstream-key"], "sk-myprov-managed");
  assert.equal(cfg.models.providers.caveman.headers["x-cave-agent"], "openclaw");
});

test("wrap openclaw honors OPENCLAW_STATE_DIR when OPENCLAW_CONFIG_PATH is unset", async () => {
  const stateDir = mkdtempSync(join(tmpdir(), "cave-openclaw-state-dir-"));
  writeFileSync(join(stateDir, "openclaw.json"), apiKeyConfig());
  const { env, home } = fixtureEnv(undefined, { OPENCLAW_STATE_DIR: stateDir, MYPROV_API_KEY: "sk-state-dir" });
  writeWrapConfig(home, { proxy: false, browse: false });
  delete env.OPENCLAW_CONFIG_PATH;

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.equal(cfg.agents.defaults.model.primary, "caveman/gpt-test");
  assert.equal(cfg.models.providers.caveman.apiKey, "sk-state-dir");
});

test("wrap openclaw parses JSON5 base config with comments and trailing commas", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-openclaw-json5-"));
  const base = join(dir, "openclaw.json");
  writeFileSync(base, `{
    // OpenClaw supports JSON5-style config files.
    "agents": { "defaults": { "model": { "primary": "myprov/gpt-json5" } } },
    "models": {
      "providers": {
        "myprov": {
          "apiKey": "\${MYPROV_API_KEY}",
          "api": "openai-completions",
          "models": [{ "id": "gpt-json5", "name": "JSON5 Model", }],
        },
      },
    },
  }`);
  const { env, home } = fixtureEnv(base, { MYPROV_API_KEY: "sk-json5" });
  writeWrapConfig(home, { proxy: false, browse: false });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.equal(cfg.agents.defaults.model.primary, "caveman/gpt-json5");
  assert.equal(cfg.models.providers.caveman.models[0].name, "JSON5 Model");
  assert.equal(cfg.models.providers.caveman.apiKey, "sk-json5");
});

test("wrap openclaw with missing base config synthesizes a routed provider when OpenAI auth exists", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-openclaw-missing-"));
  const missing = join(dir, "does-not-exist.json");
  const { env, home } = fixtureEnv(missing, { OPENAI_API_KEY: "sk-fresh-openai" });
  writeWrapConfig(home, { proxy: false, browse: false });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stderr, /primary model not found; routing fresh config through caveman\/gpt-5\.5/);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.equal(cfg.mcp.servers.caveman.command, "caveman-mcp");
  assert.equal(cfg.plugins.entries["caveman-shrink"].enabled, true);
  assert.equal(cfg.agents.defaults.model.primary, "caveman/gpt-5.5");
  assert.equal(cfg.models.providers.caveman.api, "openai-responses");
  assert.equal(cfg.models.providers.caveman.apiKey, "sk-fresh-openai");
  assert.equal(cfg.models.providers.caveman.baseUrl, "http://127.0.0.1:8787/w/openclaw/v1");
  assert.equal(cfg.models.providers.caveman.models[0].id, "gpt-5.5");
});

test("wrap openclaw with missing base config fails closed to direct launch without usable auth", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-openclaw-unroutable-"));
  const missing = join(dir, "does-not-exist.json");
  const { env, home } = fixtureEnv(missing);
  writeWrapConfig(home, { proxy: false, browse: false });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stderr, /openclaw fresh config cannot route through Caveman without OPENAI_API_KEY; launching OpenClaw directly/);
  assert.doesNotMatch(out.stderr, /using generic env wrap/);
  const child = JSON.parse(out.stdout);
  assert.equal(child.configPath, missing);
  assert.equal(child.configExistedDuringChild, false);
  assert.equal(child.config, "");
});

test("wrap openclaw managed fresh config uses gateway auth without requiring a BYOK key", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-openclaw-managed-fresh-"));
  const missing = join(dir, "does-not-exist.json");
  const { env, home } = fixtureEnv(missing, {
    CAVE_GATEWAY_URL: "https://gw.example.com",
    CAVE_API_KEY: "cave-managed-key",
  });
  writeWrapConfig(home, { proxy: false, browse: false });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.equal(cfg.agents.defaults.model.primary, "caveman/gpt-5.5");
  assert.equal(cfg.models.providers.caveman.apiKey, "cave-managed-key");
  assert.equal(cfg.models.providers.caveman.baseUrl, "https://gw.example.com/w/openclaw/v1");
  assert.equal(cfg.models.providers.caveman.headers["x-cave-upstream-key"], undefined);
});

test("bare caveman openclaw hoists --pixel to wrap and does not pass it to OpenClaw", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-openclaw-pixel-"));
  const base = join(dir, "openclaw.json");
  writeFileSync(base, apiKeyConfig());
  const { env, binDir } = fixtureEnv(base);
  writeOpenClawStub(binDir, "process.stdout.write(process.argv.slice(2).join('|'));");

  const out = await runCli(["openclaw", "--pixel"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, "chat", "--pixel is a Caveman wrap flag and must be hoisted off the agent argv");
});
