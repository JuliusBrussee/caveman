import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { nativeStub, nodeStub, stubEnv } from "./harness/stub-bin.mjs";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function sha256(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function writeOpenClawStub(binDir, body = "") {
  return nodeStub(binDir, "openclaw", body || `import { existsSync, readFileSync, writeFileSync } from "node:fs";
const args = ARGV;
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
}));`);
}

function runCli(argv, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env, cwd: env.HOME, timeout: 15_000 });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("close", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

function writeWrapConfig(home, wrap) {
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(
    join(home, ".caveman-cloud", "config.json"),
    JSON.stringify({ wrap }, null, 2),
  );
}

function temporary(t, prefix) {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  t.after(() => rmSync(dir, { recursive: true, force: true, maxRetries: 10, retryDelay: 50 }));
  return dir;
}

function fixtureEnv(t, baseConfigPath, extra = {}) {
  const root = temporary(t, "cave-openclaw fixture ");
  const home = join(root, "home");
  const caveHome = join(home, ".caveman");
  const binDir = join(root, "bin with spaces");
  writeOpenClawStub(binDir);
  writeWrapConfig(home, { proxy: false, browse: false });
  const mcpBin = nativeStub(binDir, "caveman-mcp", `
if (ARGV[0] === "version") process.stdout.write(JSON.stringify({ version: "test", capabilities: ["mcp_recovery"] }));
`);
  const env = stubEnv({
    ...process.env,
    NO_COLOR: "1",
    CI: "1",
    CAVE_NO_KEYCHAIN: "1",
    CAVEMAN_TELEMETRY: "0",
    HOME: home,
    USERPROFILE: home,
    CAVEMAN_HOME: caveHome,
    CAVEMAN_CONFIG: join(caveHome, "caveman.yaml"),
    CAVEMAN_MCP_BIN: mcpBin,
    CAVEMAN_PROXY_BIN: join(root, "missing-caveman-proxy"),
  }, binDir);
  if (baseConfigPath) env.OPENCLAW_CONFIG_PATH = baseConfigPath;
  else delete env.OPENCLAW_CONFIG_PATH;
  delete env.CAVE_GATEWAY_URL;
  delete env.OPENCLAW_STATE_DIR;
  for (const key of ["OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "CAVE_API_KEY", "MYPROV_API_KEY"]) {
    delete env[key];
  }
  Object.assign(env, extra);
  return { env, home, caveHome, binDir };
}

async function listeningProxy(t, binDir) {
  const server = createServer((socket) => socket.end());
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  t.after(() => new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve())));
  const port = server.address().port;
  const calls = join(binDir, "proxy-calls.jsonl");
  const binary = nativeStub(binDir, "caveman-proxy", `
const { appendFileSync } = require("node:fs");
appendFileSync(${JSON.stringify(calls)}, JSON.stringify(ARGV) + "\\n");
if (ARGV[0] === "version") process.stdout.write(JSON.stringify({ version: "test", capabilities: ["run_state"] }));
else if (ARGV[0] === "status") process.stdout.write(JSON.stringify({
  owner: "start", mode: "compress", recovery_via_mcp: true,
  pid: ${process.pid}, port: ${port}, instance_token: "fixture-listener",
  provider_upstreams: { openai: "https://api.openai.com" },
  compat_upstreams: { myprov: "https://provider.example/v1" },
  compat_forward_headers: { myprov: ["x-provider-option"] },
}));
else if (ARGV[0] === "stats") process.stdout.write("{}");
else process.exit(1);
`);
  return { binary, calls, gateway: `http://127.0.0.1:${port}` };
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
          api: "openai-responses",
          headers: { "x-provider-option": "preserved" },
          models: [{
            id: "gpt-test",
            name: "GPT Test",
            reasoning: true,
            input: ["text"],
            cost: { input: 1, output: 2, cacheRead: 0.1, cacheWrite: 0.2 },
            contextWindow: 123456,
            maxTokens: 4096,
            compat: { supportsStore: false, supportsPromptCacheKey: true, supportsInstructions: true },
          }, { id: "other-model", name: "Other Model", contextWindow: 32768,
            compat: { supportsStore: true, supportsPromptCacheKey: false, supportsInstructions: false } }],
        },
      },
    },
  }, null, 2);
}

test("wrap openclaw uses a verified compat mount and preserves provider identity, auth, and catalog", async (t) => {
  const dir = temporary(t, "cave-openclaw-config-");
  const base = join(dir, "openclaw.json");
  writeFileSync(base, apiKeyConfig());
  const before = sha256(base);
  const { env, home, caveHome, binDir } = fixtureEnv(t, base, {
    MYPROV_API_KEY: "sk-myprov-local",
  });
  writeWrapConfig(home, { proxy: true, browse: false });
  const proxy = await listeningProxy(t, binDir);
  env.CAVE_GATEWAY_URL = proxy.gateway;
  env.CAVEMAN_PROXY_BIN = proxy.binary;

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(sha256(base), before, "wrap must not mutate the user-owned OpenClaw config");
  const calls = readFileSync(proxy.calls, "utf8").trim().split("\n").map((line) => JSON.parse(line));
  assert.ok(calls.some((args) => args[0] === "status"), "routing needs the running proxy's endpoint map");
  assert.ok(calls.every((args) => ["version", "status", "stats"].includes(args[0])), "wrap must reuse the verified listener without restarting it");

  const child = JSON.parse(out.stdout);
  assert.deepEqual(child.args, ["chat"], "profile args must launch openclaw chat");
  assert.match(basename(dirname(child.configPath)), /^caveman-wrap-/, "child must receive a Caveman temp config directory");
  assert.equal(basename(child.configPath), "openclaw.json");
  assert.equal(child.configExistedDuringChild, true, "temp overlay config must exist while child runs");
  assert.equal(existsSync(child.configPath), false, "temp overlay config must be cleaned after child exits");
  assert.equal(existsSync(dirname(child.configPath)), false, "temp overlay directory must be cleaned after child exits");

  const cfg = JSON.parse(child.config);
  const original = JSON.parse(apiKeyConfig());
  assert.equal(cfg.models.providers.caveman, undefined);
  assert.equal(cfg.models.providers.myprov.baseUrl, `${proxy.gateway}/w/openclaw/compat/myprov/v1`);
  assert.equal(cfg.models.providers.myprov.api, "openai-responses");
  assert.equal(cfg.models.providers.myprov.apiKey, "${MYPROV_API_KEY}");
  assert.deepEqual(cfg.models.providers.myprov.headers, { "x-provider-option": "preserved", "x-cave-agent": "openclaw" });
  assert.deepEqual(cfg.models.providers.myprov.models, original.models.providers.myprov.models);
  assert.deepEqual(cfg.agents, original.agents);
  assert.equal(cfg.mcp.servers.caveman.command, "caveman-mcp");
  assert.deepEqual(cfg.mcp.servers.caveman.args, []);
  assert.equal(cfg.plugins.entries["caveman-shrink"].enabled, true);
  assert.equal(cfg.plugins.entries["caveman-shrink"].hooks.allowConversationAccess, true);
  assert.ok(cfg.plugins.load.paths.some((p) => p.startsWith(join(caveHome, "openclaw", "plugins"))));
});

test("wrap openclaw leaves OAuth primary unchanged and still injects MCP/plugin", async (t) => {
  const dir = temporary(t, "cave-openclaw-oauth-");
  const base = join(dir, "openclaw.json");
  writeFileSync(base, JSON.stringify({
    agents: { defaults: { model: { primary: "openai-codex/gpt-5" } } },
    models: { providers: { "openai-codex": { auth: "oauth", api: "openai-chatgpt-responses", models: [{ id: "gpt-5", name: "GPT-5" }] } } },
  }));
  const { env, home } = fixtureEnv(t, base);
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

test("wrap openclaw treats bare well-known openai primary without env key as OAuth default", async (t) => {
  const dir = temporary(t, "cave-openclaw-openai-oauth-");
  const base = join(dir, "openclaw.json");
  writeFileSync(base, JSON.stringify({
    agents: { defaults: { model: { primary: "openai/gpt-5.5" } } },
  }));
  const { env, home } = fixtureEnv(t, base);
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

test("wrap openclaw preserves an API-key OpenAI primary without verified proxy routing", async (t) => {
  const dir = temporary(t, "cave-openclaw-openai-key-");
  const base = join(dir, "openclaw.json");
  writeFileSync(base, JSON.stringify({
    agents: { defaults: { model: { primary: "openai/gpt-5.5" } } },
  }));
  const { env, home } = fixtureEnv(t, base, { OPENAI_API_KEY: "sk-openai-real" });
  writeWrapConfig(home, { proxy: false, browse: false });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.equal(cfg.agents.defaults.model.primary, "openai/gpt-5.5");
  assert.equal(cfg.models?.providers?.caveman, undefined);
  assert.match(out.stderr, /endpoint is not verified by the running proxy/);
  assert.equal(cfg.mcp.servers.caveman.command, "caveman-mcp");
});

test("wrap openclaw managed mode preserves an existing provider without endpoint proof", async (t) => {
  const dir = temporary(t, "cave-openclaw-managed-");
  const base = join(dir, "openclaw.json");
  writeFileSync(base, apiKeyConfig());
  const { env, home } = fixtureEnv(t, base, { CAVE_GATEWAY_URL: "https://gw.example.com", CAVE_API_KEY: "cave-managed-key", MYPROV_API_KEY: "sk-myprov-managed" });
  writeWrapConfig(home, { proxy: false, browse: false });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.deepEqual(cfg.models, JSON.parse(apiKeyConfig()).models);
  assert.deepEqual(cfg.agents, JSON.parse(apiKeyConfig()).agents);
  assert.equal(cfg.mcp.servers.caveman.command, "caveman-mcp");
  assert.equal(cfg.plugins.entries["caveman-shrink"].enabled, true);
  assert.equal(readFileSync(base, "utf8"), apiKeyConfig());
});

test("wrap openclaw honors OPENCLAW_STATE_DIR when OPENCLAW_CONFIG_PATH is unset", async (t) => {
  const stateDir = temporary(t, "cave-openclaw-state-dir-");
  writeFileSync(join(stateDir, "openclaw.json"), apiKeyConfig());
  const { env, home } = fixtureEnv(t, undefined, { OPENCLAW_STATE_DIR: stateDir, MYPROV_API_KEY: "sk-state-dir" });
  writeWrapConfig(home, { proxy: false, browse: false });
  delete env.OPENCLAW_CONFIG_PATH;

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.equal(cfg.agents.defaults.model.primary, "myprov/gpt-test");
  assert.deepEqual(cfg.models, JSON.parse(apiKeyConfig()).models);
  assert.equal(readFileSync(join(stateDir, "openclaw.json"), "utf8"), apiKeyConfig());
});

test("wrap openclaw parses JSON5 base config with comments and trailing commas", async (t) => {
  const dir = temporary(t, "cave-openclaw-json5-");
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
  const { env, home } = fixtureEnv(t, base, { MYPROV_API_KEY: "sk-json5" });
  writeWrapConfig(home, { proxy: false, browse: false });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.equal(cfg.agents.defaults.model.primary, "myprov/gpt-json5");
  assert.equal(cfg.models.providers.myprov.models[0].name, "JSON5 Model");
  assert.equal(cfg.models.providers.myprov.apiKey, "${MYPROV_API_KEY}");
  assert.equal(cfg.models.providers.caveman, undefined);
});

test("wrap openclaw fresh local config keeps MCP/plugin without inventing an unverified route", async (t) => {
  const dir = temporary(t, "cave-openclaw-missing-");
  const missing = join(dir, "does-not-exist.json");
  const { env, home } = fixtureEnv(t, missing, { OPENAI_API_KEY: "sk-fresh-openai" });
  writeWrapConfig(home, { proxy: false, browse: false });

  const out = await runCli(["wrap", "openclaw"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stderr, /endpoint is not verified by the running proxy/);
  const child = JSON.parse(out.stdout);
  const cfg = JSON.parse(child.config);
  assert.equal(cfg.mcp.servers.caveman.command, "caveman-mcp");
  assert.equal(cfg.plugins.entries["caveman-shrink"].enabled, true);
  assert.equal(cfg.agents?.defaults?.model?.primary, undefined);
  assert.equal(cfg.models?.providers?.caveman, undefined);
  assert.equal(existsSync(missing), false);
});

test("wrap openclaw with missing base config fails closed to direct launch without usable auth", async (t) => {
  const dir = temporary(t, "cave-openclaw-unroutable-");
  const missing = join(dir, "does-not-exist.json");
  const { env, home } = fixtureEnv(t, missing);
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

test("wrap openclaw managed fresh config uses gateway auth without requiring a BYOK key", async (t) => {
  const dir = temporary(t, "cave-openclaw-managed-fresh-");
  const missing = join(dir, "does-not-exist.json");
  const { env, home } = fixtureEnv(t, missing, {
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

test("bare caveman openclaw hoists --pixel to wrap and does not pass it to OpenClaw", async (t) => {
  const dir = temporary(t, "cave-openclaw-pixel-");
  const base = join(dir, "openclaw.json");
  writeFileSync(base, apiKeyConfig());
  const { env, binDir } = fixtureEnv(t, base);
  writeOpenClawStub(binDir, "process.stdout.write(ARGV.join('|'));");

  const out = await runCli(["openclaw", "--pixel"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, "chat", "--pixel is a Caveman wrap flag and must be hoisted off the agent argv");
});
