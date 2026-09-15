import { test } from "node:test";
import assert from "node:assert";
import { spawn, spawnSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readlinkSync,
  realpathSync,
  renameSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");
const { PROFILES } = await import(pathToFileURL(join(here, "..", "dist", "agents.generated.js")).href);
const { buildWrapEnv } = await import(`${pathToFileURL(cli).href}?kilo-wrap`);
const kilo = PROFILES.find((profile) => profile.id === "kilo");

assert.ok(kilo, "compiled registry must contain Kilo Code");

// Keep unit fixtures independent from developer Kilo account state. Dedicated
// tests override this with isolated on-disk databases.
process.env.KILO_DB = ":memory:";
delete process.env.KILO_TEST_MANAGED_CONFIG_DIR;

function runCli(args, env, cwd) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...args], { env, cwd });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("error", reject);
    child.on("exit", (code, signal) => resolve({ code, signal, stdout, stderr }));
  });
}

function withEnv(patch, fn) {
  const previous = new Map();
  for (const [key, value] of Object.entries(patch)) {
    previous.set(key, process.env[key]);
    if (value === undefined) delete process.env[key];
    else process.env[key] = value;
  }
  try {
    return fn();
  } finally {
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  }
}

function kiloMcpFixture() {
  const root = mkdtempSync(join(tmpdir(), "cave-kilo-mcp-"));
  const home = join(root, "home");
  const cavemanHome = join(root, "caveman-home");
  const xdgConfigHome = join(home, ".config");
  const configPath = join(xdgConfigHome, "kilo", "kilo.json");
  const jsoncPath = join(xdgConfigHome, "kilo", "kilo.jsonc");
  const markerPath = join(cavemanHome, "mcp", "kilo.json");
  const mcpV1 = join(root, "caveman-mcp-v1");
  const mcpV2 = join(root, "caveman-mcp-v2");
  mkdirSync(dirname(configPath), { recursive: true });
  const env = {
    ...process.env,
    HOME: home,
    XDG_CONFIG_HOME: xdgConfigHome,
    CAVEMAN_HOME: cavemanHome,
    CAVEMAN_MCP_BIN: mcpV1,
    NO_COLOR: "1",
  };
  delete env.KILO_CONFIG;
  delete env.KILO_CONFIG_DIR;
  return {
    root,
    configPath,
    jsoncPath,
    markerPath,
    mcpV1,
    mcpV2,
    env,
    cleanup: () => rmSync(root, { recursive: true, force: true }),
  };
}

function stateHash(bytes) {
  return bytes === null ? null : `sha256:${createHash("sha256").update(bytes).digest("hex")}`;
}

function canonicalTestPath(path) {
  try {
    return realpathSync(path);
  } catch {
    return join(realpathSync(dirname(path)), basename(path));
  }
}

function configPendingPath(configPath) {
  const canonicalPath = canonicalTestPath(configPath);
  return join(dirname(canonicalPath), `.${basename(canonicalPath)}.caveman-mcp.pending.json`);
}

function kiloMarkerBytes(command, configPath, tool = "caveman_retrieve", args = []) {
  return Buffer.from(JSON.stringify({
    schema_version: 1,
    tool,
    command,
    args,
    config_path: canonicalTestPath(configPath),
  }, null, 2) + "\n");
}

function writePendingMcpInstall(
  agent,
  markerPath,
  configPath,
  configBefore,
  configAfter,
  markerAfter,
  configMode = 0o600,
  serverName = "caveman",
) {
  const canonicalConfigPath = canonicalTestPath(configPath);
  const canonicalMarkerPath = canonicalTestPath(markerPath);
  const journal = {
    schema_version: 1,
    transaction_id: randomUUID(),
    agent,
    server_name: serverName,
    action: "install",
    config_path: canonicalConfigPath,
    marker_path: canonicalMarkerPath,
    config_before_base64: configBefore?.toString("base64") ?? null,
    config_before_mode: configMode,
    config_before_sha256: stateHash(configBefore),
    config_after_sha256: stateHash(configAfter),
    marker_before_base64: null,
    marker_before_mode: 0o600,
    marker_before_sha256: null,
    marker_after_base64: markerAfter.toString("base64"),
    marker_after_sha256: stateHash(markerAfter),
  };
  const bytes = JSON.stringify(journal, null, 2) + "\n";
  const locatorPath = `${canonicalMarkerPath}.pending`;
  const adjacentPath = configPendingPath(canonicalConfigPath);
  writeFileSync(locatorPath, bytes, { mode: 0o600 });
  writeFileSync(adjacentPath, bytes, { mode: 0o600 });
  assert.equal(readFileSync(locatorPath, "utf8"), readFileSync(adjacentPath, "utf8"));
  return { adjacentPath, journal, locatorPath };
}

function writePendingMcpUninstall(agent, markerPath, configPath, configBefore, configAfter, markerBefore, configMode = 0o600) {
  const canonicalConfigPath = canonicalTestPath(configPath);
  const canonicalMarkerPath = canonicalTestPath(markerPath);
  const journal = {
    schema_version: 1,
    transaction_id: randomUUID(),
    agent,
    server_name: "caveman",
    action: "uninstall",
    config_path: canonicalConfigPath,
    marker_path: canonicalMarkerPath,
    config_before_base64: configBefore.toString("base64"),
    config_before_mode: configMode,
    config_before_sha256: stateHash(configBefore),
    config_after_sha256: stateHash(configAfter),
    marker_before_base64: markerBefore.toString("base64"),
    marker_before_mode: 0o600,
    marker_before_sha256: stateHash(markerBefore),
    marker_after_base64: null,
    marker_after_sha256: null,
  };
  const bytes = JSON.stringify(journal, null, 2) + "\n";
  const locatorPath = `${canonicalMarkerPath}.pending`;
  const adjacentPath = configPendingPath(canonicalConfigPath);
  writeFileSync(locatorPath, bytes, { mode: 0o600 });
  writeFileSync(adjacentPath, bytes, { mode: 0o600 });
  assert.equal(readFileSync(locatorPath, "utf8"), readFileSync(adjacentPath, "utf8"));
  return { adjacentPath, journal, locatorPath };
}

function runKiloAlias(
  invokedAs,
  installedBinary,
  agentArgs = ["run", "hello", "--model", "caveman/gpt-5.5"],
  options = {},
) {
  const root = mkdtempSync(join(tmpdir(), `cave-${invokedAs}-`));
  const home = join(root, "home");
  const binDir = join(root, "bin");
  const configDir = join(home, ".config", "kilo");
  mkdirSync(binDir, { recursive: true });
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  mkdirSync(configDir, { recursive: true });

  const sentinel = '{"theme":"caveman-test","provider":{"custom":true}}\n';
  const userConfig = join(configDir, "kilo.json");
  const proxy = join(root, "caveman-proxy-sentinel");
  const proxySentinel = join(root, "proxy-was-started");
  writeFileSync(userConfig, sentinel);
  writeFileSync(
    join(home, ".caveman-cloud", "config.json"),
    JSON.stringify({ wrap: { proxy: options.proxy === true, shrink: false, mcp: false, browse: false } }),
  );
  writeFileSync(proxy, `#!/bin/sh\nprintf invoked > ${JSON.stringify(proxySentinel)}\nexit 1\n`, { mode: 0o755 });
  writeFileSync(
    join(binDir, installedBinary),
    `#!/usr/bin/env node
process.stdout.write(JSON.stringify({
  argv: process.argv.slice(2),
  config: process.env.KILO_CONFIG_CONTENT || "",
  openaiKey: process.env.OPENAI_API_KEY || ""
}));
`,
    { mode: 0o755 },
  );

  const env = {
    ...process.env,
    HOME: home,
    CAVEMAN_HOME: home,
    PATH: `${binDir}:${process.env.PATH}`,
    OPENAI_API_KEY: "sk-kilo-upstream-test",
    CAVEMAN_PROXY_BIN: proxy,
    NO_COLOR: "1",
    ...options.env,
  };
  delete env.CAVE_GATEWAY_URL;
  delete env.CAVE_API_KEY;

  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, invokedAs, ...agentArgs], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("error", reject);
    child.on("exit", (code, signal) => {
      const userConfigAfter = readFileSync(userConfig, "utf8");
      const proxyStarted = existsSync(proxySentinel);
      rmSync(root, { recursive: true, force: true });
      resolve({ code, signal, stdout, stderr, userConfigAfter, sentinel, proxyStarted });
    });
  });
}

function writeKiloAccountState(path, { activeAccount = "account-1", activeOrg = null } = {}) {
  mkdirSync(dirname(path), { recursive: true });
  const script = [
    'const { DatabaseSync } = require("node:sqlite");',
    'const db = new DatabaseSync(process.argv[1]);',
    'db.exec("CREATE TABLE account_state (id INTEGER PRIMARY KEY, active_account_id TEXT, active_org_id TEXT)");',
    'db.prepare("INSERT INTO account_state (id, active_account_id, active_org_id) VALUES (1, ?, ?)").run(process.argv[2] || null, process.argv[3] || null);',
    'db.close();',
  ].join("\n");
  const result = spawnSync(process.execPath, ["--no-warnings", "-e", script, path, activeAccount ?? "", activeOrg ?? ""], {
    encoding: "utf8",
    env: { ...process.env, NODE_NO_WARNINGS: "1", NODE_OPTIONS: "" },
  });
  assert.equal(result.status, 0, result.stderr);
}

test("Kilo profile exposes both published CLI binaries", () => {
  assert.deepEqual(kilo.binary_names, ["kilo", "kilocode"]);
  // The pin itself is owned by the nightly drift bump and guarded by
  // agents/compile.mjs; here only the shape matters: install pins the tested version.
  assert.match(kilo.tested_agent_version, /^\d+\.\d+\.\d+$/);
  assert.equal(kilo.install, `npm install -g @kilocode/cli@${kilo.tested_agent_version}`);
  assert.equal(kilo.injection.method, "config-env-content");
  assert.equal(kilo.injection.env_var, "KILO_CONFIG_CONTENT");
  for (const content of [kilo.injection.config_content.local, kilo.injection.config_content.managed]) {
    assert.deepEqual(content.enabled_providers, ["caveman"]);
  }
});

for (const binary of ["kilo", "kilocode"]) {
  test(`caveman ${binary} routes Kilo without mutating user config`, async () => {
    const out = await runKiloAlias(binary, binary);
    assert.equal(out.code, 0, `exit ${out.code}/${out.signal}: ${out.stderr}`);
    const child = JSON.parse(out.stdout);
    assert.deepEqual(child.argv, ["run", "hello", "--model", "caveman/gpt-5.5"]);
    assert.equal(child.openaiKey, "sk-kilo-upstream-test");

    const config = JSON.parse(child.config);
    assert.equal(config.provider.caveman.options.baseURL, "http://127.0.0.1:8787/w/kilo/v1");
    assert.equal(config.provider.caveman.options.apiKey, "{env:OPENAI_API_KEY}");
    assert.equal(config.provider.caveman.options.headers["X-Cave-Agent"], "kilo");
    assert.equal(config.model, "caveman/gpt-5.5");
    assert.deepEqual(config.enabled_providers, ["caveman"]);
    assert.equal(out.userConfigAfter, out.sentinel);
  });
}

test("Kilo route guard accepts profile-backed model spellings", async (t) => {
  for (const fixture of [
    { name: "long equals", args: ["run", "--model=caveman/gpt-5.4-mini", "hello"] },
    { name: "long alias spaced", args: ["run", "--m", "caveman/gpt-5.5", "hello"] },
    { name: "long alias equals", args: ["run", "--m=caveman/gpt-5.4-mini", "hello"] },
    { name: "short spaced", args: ["run", "-m", "caveman/gpt-5.5", "hello"] },
    { name: "short equals", args: ["-m=caveman/gpt-5.4-mini", "run", "hello"] },
    { name: "global model before command", args: ["--model", "caveman/gpt-5.5", "run", "hello"] },
    { name: "value containing command before run", args: ["--dir", "run", "run", "hello"] },
    { name: "spaced boolean before run", args: ["--pure", "false", "run", "hello"] },
    { name: "inline boolean after run", args: ["run", "--thinking=false", "hello"] },
  ]) {
    await t.test(fixture.name, async () => {
      const out = await runKiloAlias("kilo", "kilo", fixture.args);
      assert.equal(out.code, 0, out.stderr);
      const child = JSON.parse(out.stdout);
      assert.deepEqual(child.argv, fixture.args);
      assert.equal(JSON.parse(child.config).model, "caveman/gpt-5.5");
      assert.doesNotMatch(out.stderr, /launching directly/);
    });
  }
});

test("Kilo route guard launches direct for model and attach bypasses", async (t) => {
  const fixtures = [
    { name: "foreign model", surface: "--model", args: ["run", "hello", "--model", "openai/test"] },
    { name: "foreign short model", surface: "--model", args: ["run", "-m", "openai/test", "hello"] },
    { name: "foreign long alias", surface: "--model", args: ["run", "--m=openai/test", "hello"] },
    { name: "foreign short equals", surface: "--model", args: ["run", "-m=openai/test", "hello"] },
    { name: "ambiguous attached model", surface: "-m", args: ["run", "-mopenai/test", "hello"] },
    { name: "repeated model", surface: "--model", args: ["run", "--model", "caveman/gpt-5.5", "--model=caveman/gpt-5.4-mini"] },
    { name: "repeated alias", surface: "--model", args: ["run", "--m", "caveman/gpt-5.5", "--m=caveman/gpt-5.4-mini"] },
    { name: "missing model", surface: "--model", args: ["run", "hello", "--model"] },
    { name: "missing alias", surface: "--model", args: ["run", "hello", "--m"] },
    { name: "top-level attach", surface: "attach", args: ["attach", "https://remote.example/session"] },
    { name: "attach after dir value named run", surface: "attach", args: ["--dir", "run", "attach", "https://remote.example/session"] },
    { name: "attach after session value named run", surface: "attach", args: ["--session", "run", "attach", "https://remote.example/session"] },
    { name: "attach after password value named run", surface: "attach", args: ["--password", "run", "attach", "https://remote.example/session"] },
    { name: "attach after username value named run", surface: "attach", args: ["--username", "run", "attach", "https://remote.example/session"] },
    { name: "run attach", surface: "--attach", args: ["run", "--attach", "https://remote.example/session", "hello"] },
    { name: "run attach equals", surface: "--attach", args: ["run", "--attach=https://remote.example/session", "hello"] },
    { name: "cloud agent", surface: "cloud", args: ["--print-logs", "cloud", "run", "task"] },
    { name: "cloud after valued global", surface: "cloud", args: ["--log-level", "INFO", "cloud", "start"] },
    { name: "cloud after command option", surface: "cloud", args: ["--repo", "run", "cloud", "start"] },
    { name: "cloud after spaced root boolean", surface: "cloud", args: ["--pure", "false", "cloud", "start"] },
    { name: "cloud after spaced logging boolean", surface: "cloud", args: ["--print-logs", "false", "cloud", "start"] },
    { name: "attach after spaced short boolean", surface: "attach", args: ["--continue", "false", "attach", "https://remote.example/session"] },
    { name: "cloud after spaced command boolean", surface: "cloud", args: ["--stream", "false", "cloud", "start"] },
    { name: "roll call", surface: "roll-call", args: ["roll-call", "gpt"] },
    { name: "roll call after spaced boolean", surface: "roll-call", args: ["--verbose", "false", "roll-call", "gpt"] },
    { name: "daemon", surface: "daemon", args: ["daemon"] },
    { name: "daemon start", surface: "daemon", args: ["daemon", "start"] },
    { name: "daemon restart after valued global", surface: "daemon", args: ["--log-level", "INFO", "daemon", "restart"] },
    { name: "console", surface: "console", args: ["console"] },
    { name: "console after spaced boolean", surface: "console", args: ["--print-logs", "false", "console"] },
    { name: "unknown option arity", surface: "arguments", args: ["--future-option", "run", "cloud", "start"] },
  ];
  for (const fixture of fixtures) {
    await t.test(fixture.name, async () => {
      const out = await runKiloAlias("kilo", "kilo", fixture.args, {
        proxy: true,
        env: { KILO_CONFIG_CONTENT: undefined },
      });
      assert.equal(out.code, 0, `exit ${out.code}/${out.signal}: ${out.stderr}`);
      const child = JSON.parse(out.stdout);
      assert.deepEqual(child.argv, fixture.args);
      assert.equal(child.config, "");
      assert.equal(out.proxyStarted, false);
      assert.ok(out.stderr.includes(`Kilo ${fixture.surface}`), out.stderr);
      assert.match(out.stderr, /launching directly/);
      assert.doesNotMatch(out.stderr, /openai\/test|remote\.example/);
    });
  }
});

test("Kilo route guard ignores model-looking arguments after separator", async () => {
  const args = ["run", "hello", "--", "--model", "openai/test", "attach", "https://remote.example/session"];
  const out = await runKiloAlias("kilo", "kilo", args);
  assert.equal(out.code, 0, out.stderr);
  const child = JSON.parse(out.stdout);
  assert.deepEqual(child.argv, args);
  assert.equal(JSON.parse(child.config).model, "caveman/gpt-5.5");
  assert.doesNotMatch(out.stderr, /launching directly/);
});

test("Kilo route guard recognizes exact higher-priority managed files", async (t) => {
  for (const name of ["kilo.jsonc", "kilo.json", "opencode.jsonc", "opencode.json"]) {
    await t.test(name, async () => {
      const root = mkdtempSync(join(tmpdir(), "cave-kilo-managed-"));
      try {
        writeFileSync(join(root, name), "{}\n");
        const out = await runKiloAlias("kilo", "kilo", ["run", "hello"], {
          proxy: true,
          env: { KILO_CONFIG_CONTENT: undefined, KILO_DB: ":memory:", KILO_TEST_MANAGED_CONFIG_DIR: root },
        });
        assert.equal(out.code, 0, out.stderr);
        assert.equal(JSON.parse(out.stdout).config, "");
        assert.equal(out.proxyStarted, false);
        assert.match(out.stderr, /Kilo managed policy loads after Caveman's inline routing config; launching directly/);
        assert.doesNotMatch(out.stderr, new RegExp(name.replace(".", "\\.")));
      } finally {
        rmSync(root, { recursive: true, force: true });
      }
    });
  }

  await t.test("unloaded legacy config.json", async () => {
    const root = mkdtempSync(join(tmpdir(), "cave-kilo-managed-legacy-"));
    try {
      writeFileSync(join(root, "config.json"), "{}\n");
      const out = await runKiloAlias("kilo", "kilo", ["run", "hello"], {
        env: { KILO_DB: ":memory:", KILO_TEST_MANAGED_CONFIG_DIR: root },
      });
      assert.equal(out.code, 0, out.stderr);
      assert.equal(JSON.parse(JSON.parse(out.stdout).config).model, "caveman/gpt-5.5");
      assert.doesNotMatch(out.stderr, /launching directly/);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});

test("Kilo route guard launches direct for active or unreadable organization state", async (t) => {
  await t.test("active organization", async () => {
    const root = mkdtempSync(join(tmpdir(), "cave-kilo-org-"));
    try {
      const db = join(root, "kilo.db");
      writeKiloAccountState(db, { activeOrg: "org-secret-value" });
      const out = await runKiloAlias("kilo", "kilo", ["run", "hello"], {
        proxy: true,
        env: { KILO_CONFIG_CONTENT: undefined, KILO_DB: db },
      });
      assert.equal(out.code, 0, out.stderr);
      assert.equal(JSON.parse(out.stdout).config, "");
      assert.equal(out.proxyStarted, false);
      assert.match(out.stderr, /Kilo active organization can load routing policy after Caveman's inline config; launching directly/);
      assert.doesNotMatch(out.stderr, /org-secret-value/);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  await t.test("unreadable organization state", async () => {
    const root = mkdtempSync(join(tmpdir(), "cave-kilo-org-unknown-"));
    try {
      const out = await runKiloAlias("kilo", "kilo", ["run", "hello"], {
        proxy: true,
        env: { KILO_CONFIG_CONTENT: undefined, KILO_DB: root },
      });
      assert.equal(out.code, 0, out.stderr);
      assert.equal(JSON.parse(out.stdout).config, "");
      assert.equal(out.proxyStarted, false);
      assert.match(out.stderr, /Kilo organization state cannot be verified safely; launching directly/);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});

test("Kilo route guard permits verified account state without an active organization", async () => {
  const root = mkdtempSync(join(tmpdir(), "cave-kilo-no-org-"));
  try {
    const db = join(root, "kilo.db");
    writeKiloAccountState(db);
    const out = await runKiloAlias("kilo", "kilo", ["run", "hello"], { env: { KILO_DB: db } });
    assert.equal(out.code, 0, out.stderr);
    assert.equal(JSON.parse(JSON.parse(out.stdout).config).model, "caveman/gpt-5.5");
    assert.doesNotMatch(out.stderr, /launching directly/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("Kilo route guard fails closed when called through buildWrapEnv", () => {
  assert.throws(
    () => buildWrapEnv(kilo, "http://127.0.0.1:8787", "auto", ["run", "--model", "openai/test"]),
    /Kilo --model selects a model outside Caveman's routed profile/,
  );
  assert.throws(
    () => buildWrapEnv(kilo, "http://127.0.0.1:8787", "auto", ["attach", "https:\/\/remote.example\/session"]),
    /Kilo attach uses another server's provider route/,
  );
  assert.throws(
    () => buildWrapEnv(kilo, "http://127.0.0.1:8787", "auto", ["daemon", "start"]),
    /Kilo daemon can outlive Caveman's wrap session/,
  );
});

test("managed Kilo config keeps gateway and upstream credentials separate", () => {
  const previousCaveKey = process.env.CAVE_API_KEY;
  const previousOpenAIKey = process.env.OPENAI_API_KEY;
  process.env.CAVE_API_KEY = "cave_gateway_secret";
  process.env.OPENAI_API_KEY = "sk_upstream_secret";
  try {
    const env = buildWrapEnv(kilo, "https://gateway.example");
    const raw = env.KILO_CONFIG_CONTENT;
    assert.ok(raw);
    const config = JSON.parse(raw);
    const options = config.provider.caveman.options;
    assert.equal(options.baseURL, "https://gateway.example/w/kilo/v1");
    assert.equal(options.apiKey, "{env:CAVE_API_KEY}");
    assert.equal(options.headers["x-cave-upstream-key"], "{env:OPENAI_API_KEY}");
    assert.equal(options.headers["X-Cave-Agent"], "kilo");
    assert.deepEqual(config.enabled_providers, ["caveman"]);
    assert.doesNotMatch(raw, /cave_gateway_secret|sk_upstream_secret/);
  } finally {
    if (previousCaveKey === undefined) delete process.env.CAVE_API_KEY;
    else process.env.CAVE_API_KEY = previousCaveKey;
    if (previousOpenAIKey === undefined) delete process.env.OPENAI_API_KEY;
    else process.env.OPENAI_API_KEY = previousOpenAIKey;
  }
});

test("managed Kilo omits upstream header when no OpenAI credential is inherited", () => {
  withEnv({ CAVE_API_KEY: "cave_gateway_secret", OPENAI_API_KEY: undefined }, () => {
    const raw = buildWrapEnv(kilo, "https://gateway.example").KILO_CONFIG_CONTENT;
    assert.ok(raw);
    const headers = JSON.parse(raw).provider.caveman.options.headers;
    assert.equal(Object.hasOwn(headers, "x-cave-upstream-key"), false);
    assert.doesNotMatch(raw, /cave_optional_openai_key_env|OPENAI_API_KEY/);
  });
});

test("Kilo MCP install is idempotent, marker-owned, upgradeable, and reversible", async () => {
  const fx = kiloMcpFixture();
  try {
    const sibling = { type: "remote", url: "https://mcp.example.test" };
    writeFileSync(fx.configPath, JSON.stringify({ theme: "dark", mcp: { sibling } }, null, 2) + "\n", { mode: 0o640 });

    const installed = await runCli(["mcp", "install", "kilo"], fx.env);
    assert.equal(installed.code, 0, installed.stderr);
    const firstBytes = readFileSync(fx.configPath, "utf8");
    const first = JSON.parse(firstBytes);
    assert.equal(statSync(fx.configPath).mode & 0o777, 0o640);
    assert.equal(first.theme, "dark");
    assert.deepEqual(first.mcp.sibling, sibling);
    assert.deepEqual(first.mcp.caveman, { type: "local", command: [fx.mcpV1], enabled: true });
    assert.deepEqual(JSON.parse(readFileSync(fx.markerPath, "utf8")), {
      schema_version: 1,
      tool: "caveman_retrieve",
      command: fx.mcpV1,
      args: [],
      config_path: canonicalTestPath(fx.configPath),
    });
    withEnv({ HOME: fx.env.HOME, XDG_CONFIG_HOME: fx.env.XDG_CONFIG_HOME, CAVEMAN_HOME: fx.env.CAVEMAN_HOME }, () => {
      const automatic = JSON.parse(buildWrapEnv(kilo, "http://127.0.0.1:8787", "auto").KILO_CONFIG_CONTENT);
      assert.deepEqual(automatic.mcp.caveman, { type: "local", command: [fx.mcpV1], enabled: true });
      for (const mode of ["marker-only", "off"]) {
        const suppressed = JSON.parse(buildWrapEnv(kilo, "http://127.0.0.1:8787", mode).KILO_CONFIG_CONTENT);
        assert.equal(suppressed.mcp?.caveman, undefined, `${mode} must not project Kilo MCP into the temporary config`);
      }
      assert.equal(readFileSync(fx.configPath, "utf8"), firstBytes);
    });

    const repeated = await runCli(["mcp", "install", "kilo"], fx.env);
    assert.equal(repeated.code, 0, repeated.stderr);
    assert.equal(readFileSync(fx.configPath, "utf8"), firstBytes);

    const upgraded = await runCli(["mcp", "install", "kilo"], { ...fx.env, CAVEMAN_MCP_BIN: fx.mcpV2 });
    assert.equal(upgraded.code, 0, upgraded.stderr);
    assert.deepEqual(JSON.parse(readFileSync(fx.configPath, "utf8")).mcp.caveman, {
      type: "local",
      command: [fx.mcpV2],
      enabled: true,
    });
    assert.equal(JSON.parse(readFileSync(fx.markerPath, "utf8")).command, fx.mcpV2);

    const removed = await runCli(["mcp", "uninstall", "kilo"], fx.env);
    assert.equal(removed.code, 0, removed.stderr);
    const final = JSON.parse(readFileSync(fx.configPath, "utf8"));
    assert.equal(final.theme, "dark");
    assert.deepEqual(final.mcp, { sibling });
    assert.equal(statSync(fx.configPath).mode & 0o777, 0o640);
    assert.equal(existsSync(fx.markerPath), false);
  } finally {
    fx.cleanup();
  }
});

test("Kilo MCP follows XDG_CONFIG_HOME instead of writing the fallback path", async () => {
  const fx = kiloMcpFixture();
  try {
    const xdgConfigHome = join(fx.root, "xdg-config");
    const activePath = join(xdgConfigHome, "kilo", "kilo.json");
    mkdirSync(dirname(activePath), { recursive: true });
    writeFileSync(activePath, JSON.stringify({ theme: "xdg" }, null, 2) + "\n");
    const env = { ...fx.env, XDG_CONFIG_HOME: xdgConfigHome };

    const installed = await runCli(["mcp", "install", "kilo"], env);
    assert.equal(installed.code, 0, installed.stderr);
    assert.deepEqual(JSON.parse(readFileSync(activePath, "utf8")).mcp.caveman, {
      type: "local",
      command: [fx.mcpV1],
      enabled: true,
    });
    assert.equal(existsSync(fx.configPath), false);
    assert.equal(JSON.parse(readFileSync(fx.markerPath, "utf8")).config_path, canonicalTestPath(activePath));

    const removed = await runCli(["mcp", "uninstall", "kilo"], env);
    assert.equal(removed.code, 0, removed.stderr);
    assert.equal(JSON.parse(readFileSync(activePath, "utf8")).mcp, undefined);
    assert.equal(existsSync(fx.markerPath), false);
  } finally {
    fx.cleanup();
  }
});

test("Kilo MCP gives explicit KILO_CONFIG precedence over XDG", async () => {
  const fx = kiloMcpFixture();
  try {
    const xdgConfigHome = join(fx.root, "unused-xdg");
    const activePath = join(fx.root, "custom", "settings.json");
    mkdirSync(dirname(activePath), { recursive: true });
    writeFileSync(activePath, JSON.stringify({ theme: "explicit" }, null, 2) + "\n");
    const env = { ...fx.env, XDG_CONFIG_HOME: xdgConfigHome, KILO_CONFIG: activePath };

    const installed = await runCli(["mcp", "install", "kilo"], env);
    assert.equal(installed.code, 0, installed.stderr);
    assert.deepEqual(JSON.parse(readFileSync(activePath, "utf8")).mcp.caveman, {
      type: "local",
      command: [fx.mcpV1],
      enabled: true,
    });
    assert.equal(existsSync(join(xdgConfigHome, "kilo", "kilo.json")), false);
    assert.equal(JSON.parse(readFileSync(fx.markerPath, "utf8")).config_path, canonicalTestPath(activePath));

    const removed = await runCli(["mcp", "uninstall", "kilo"], env);
    assert.equal(removed.code, 0, removed.stderr);
    assert.equal(JSON.parse(readFileSync(activePath, "utf8")).mcp, undefined);
  } finally {
    fx.cleanup();
  }
});

test("Kilo MCP gives KILO_CONFIG_DIR precedence over explicit and XDG config", async () => {
  const fx = kiloMcpFixture();
  try {
    const configDirectory = join(fx.root, "config-dir");
    const activePath = join(configDirectory, "kilo.json");
    const explicitPath = join(fx.root, "explicit", "settings.json");
    const xdgPath = join(fx.root, "unused-xdg", "kilo", "kilo.json");
    for (const path of [activePath, explicitPath, xdgPath]) {
      mkdirSync(dirname(path), { recursive: true });
      writeFileSync(path, JSON.stringify({ source: basename(dirname(path)) }, null, 2) + "\n");
    }
    const env = {
      ...fx.env,
      KILO_CONFIG_DIR: configDirectory,
      KILO_CONFIG: explicitPath,
      XDG_CONFIG_HOME: join(fx.root, "unused-xdg"),
    };

    const installed = await runCli(["mcp", "install", "kilo"], env);
    assert.equal(installed.code, 0, installed.stderr);
    assert.deepEqual(JSON.parse(readFileSync(activePath, "utf8")).mcp.caveman, {
      type: "local",
      command: [fx.mcpV1],
      enabled: true,
    });
    assert.equal(JSON.parse(readFileSync(explicitPath, "utf8")).mcp, undefined);
    assert.equal(JSON.parse(readFileSync(xdgPath, "utf8")).mcp, undefined);
    assert.equal(JSON.parse(readFileSync(fx.markerPath, "utf8")).config_path, canonicalTestPath(activePath));

    const removed = await runCli(["mcp", "uninstall", "kilo"], env);
    assert.equal(removed.code, 0, removed.stderr);
    assert.equal(JSON.parse(readFileSync(activePath, "utf8")).mcp, undefined);
  } finally {
    fx.cleanup();
  }
});

test("Kilo MCP resolves tilde in KILO_CONFIG literally from cwd", async () => {
  const fx = kiloMcpFixture();
  try {
    const cwd = join(fx.root, "cwd");
    const literalPath = join(cwd, "~", "custom.json");
    const expandedPath = join(fx.env.HOME, "custom.json");
    mkdirSync(dirname(literalPath), { recursive: true });
    writeFileSync(literalPath, JSON.stringify({ source: "literal" }, null, 2) + "\n");
    writeFileSync(expandedPath, JSON.stringify({ source: "home" }, null, 2) + "\n");
    const env = { ...fx.env, KILO_CONFIG: "~/custom.json" };

    const installed = await runCli(["mcp", "install", "kilo"], env, cwd);
    assert.equal(installed.code, 0, installed.stderr);
    assert.ok(JSON.parse(readFileSync(literalPath, "utf8")).mcp.caveman);
    assert.equal(JSON.parse(readFileSync(expandedPath, "utf8")).mcp, undefined);
    assert.equal(JSON.parse(readFileSync(fx.markerPath, "utf8")).config_path, canonicalTestPath(literalPath));

    const removed = await runCli(["mcp", "uninstall", "kilo"], env, cwd);
    assert.equal(removed.code, 0, removed.stderr);
    assert.equal(JSON.parse(readFileSync(literalPath, "utf8")).mcp, undefined);
  } finally {
    fx.cleanup();
  }
});

test("Kilo MCP refuses comment-destructive explicit JSONC mutation", async () => {
  const fx = kiloMcpFixture();
  try {
    const activePath = join(fx.root, "custom", "settings.jsonc");
    const source = "{\n  // owned by user\n  \"theme\": \"dark\"\n}\n";
    mkdirSync(dirname(activePath), { recursive: true });
    writeFileSync(activePath, source);
    const installed = await runCli(["mcp", "install", "kilo"], { ...fx.env, KILO_CONFIG: activePath });
    assert.equal(installed.code, 0, installed.stderr);
    assert.match(installed.stderr, /KILO_CONFIG selects JSONC .* refusing comment-destructive mutation/);
    assert.equal(readFileSync(activePath, "utf8"), source);
    assert.equal(existsSync(fx.markerPath), false);
  } finally {
    fx.cleanup();
  }
});

test("Kilo MCP refuses JSONC selected inside KILO_CONFIG_DIR", async () => {
  const fx = kiloMcpFixture();
  try {
    const configDirectory = join(fx.root, "config-dir");
    const activePath = join(configDirectory, "kilo.jsonc");
    const lowerPath = join(configDirectory, "kilo.json");
    const source = "{\n  // custom directory\n  \"theme\": \"dark\"\n}\n";
    mkdirSync(configDirectory, { recursive: true });
    writeFileSync(activePath, source);
    const installed = await runCli(["mcp", "install", "kilo"], { ...fx.env, KILO_CONFIG_DIR: configDirectory });
    assert.equal(installed.code, 0, installed.stderr);
    assert.match(installed.stderr, /KILO_CONFIG_DIR selects JSONC .* refusing comment-destructive mutation/);
    assert.equal(readFileSync(activePath, "utf8"), source);
    assert.equal(existsSync(lowerPath), false);
    assert.equal(existsSync(fx.markerPath), false);
  } finally {
    fx.cleanup();
  }
});

test("Kilo MCP config and ownership journal commit together", async (t) => {
  await t.test("unusable journal storage blocks native config mutation", async () => {
    const fx = kiloMcpFixture();
    try {
      const source = JSON.stringify({ theme: "dark" }, null, 2) + "\n";
      writeFileSync(fx.configPath, source);
      const blockedHome = join(fx.root, "caveman-home-is-a-file");
      writeFileSync(blockedHome, "not a directory\n");

      const installed = await runCli(["mcp", "install", "kilo"], { ...fx.env, CAVEMAN_HOME: blockedHome });
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /cannot persist kilo caveman MCP ownership journal/);
      assert.doesNotMatch(installed.stderr, /caveman_retrieve installed/);
      assert.equal(readFileSync(fx.configPath, "utf8"), source);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("foreign marker target blocks before native config mutation", async () => {
    const fx = kiloMcpFixture();
    try {
      const source = JSON.stringify({ theme: "dark" }, null, 2) + "\n";
      writeFileSync(fx.configPath, source, { mode: 0o640 });
      const originalMode = statSync(fx.configPath).mode & 0o777;
      mkdirSync(fx.markerPath, { recursive: true });

      const installed = await runCli(["mcp", "install", "kilo"], fx.env);
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /EISDIR|directory/);
      assert.doesNotMatch(installed.stderr, /caveman_retrieve installed/);
      assert.equal(readFileSync(fx.configPath, "utf8"), source);
      assert.equal(statSync(fx.configPath).mode & 0o777, originalMode);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("dangling Kilo config symlink fails without replacing link", async () => {
    const fx = kiloMcpFixture();
    try {
      const missingTarget = join(fx.root, "missing", "kilo.json");
      symlinkSync(missingTarget, fx.configPath);

      const installed = await runCli(["mcp", "install", "kilo"], fx.env);
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /MCP config path contains dangling symlink .*kilo\.json; refusing mutation/);
      assert.equal(readlinkSync(fx.configPath), missingTarget);
      assert.equal(existsSync(missingTarget), false);
      assert.equal(existsSync(fx.markerPath), false);
      assert.equal(existsSync(join(dirname(fx.configPath), ".kilo.json.caveman-mcp.pending.json")), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("interrupted config commit rolls back then retries cleanly", async () => {
    const fx = kiloMcpFixture();
    try {
      const before = Buffer.from(JSON.stringify({ theme: "dark" }, null, 2) + "\n");
      const after = Buffer.from(JSON.stringify({
        theme: "dark",
        mcp: { caveman: { type: "local", command: [fx.mcpV1], enabled: true } },
      }, null, 2) + "\n");
      const markerAfter = kiloMarkerBytes(fx.mcpV1, fx.configPath);
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      writeFileSync(fx.configPath, after, { mode: 0o600 });
      const pending = writePendingMcpInstall("kilo", fx.markerPath, fx.configPath, before, after, markerAfter);

      const installed = await runCli(["mcp", "install", "kilo"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      assert.match(installed.stderr, /rolled back interrupted kilo caveman MCP transaction/);
      assert.equal(readFileSync(fx.configPath, "utf8"), after.toString("utf8"));
      assert.equal(readFileSync(fx.markerPath, "utf8"), markerAfter.toString("utf8"));
      assert.equal(existsSync(pending.locatorPath), false);
      assert.equal(existsSync(pending.adjacentPath), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("foreign edit blocks interrupted-transaction rollback", async () => {
    const fx = kiloMcpFixture();
    try {
      const before = Buffer.from(JSON.stringify({ theme: "dark" }, null, 2) + "\n");
      const after = Buffer.from(JSON.stringify({
        theme: "dark",
        mcp: { caveman: { type: "local", command: [fx.mcpV1], enabled: true } },
      }, null, 2) + "\n");
      const foreign = Buffer.from(JSON.stringify({ theme: "user-edited" }, null, 2) + "\n");
      const markerAfter = kiloMarkerBytes(fx.mcpV1, fx.configPath);
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      writeFileSync(fx.configPath, foreign);
      const pending = writePendingMcpInstall("kilo", fx.markerPath, fx.configPath, before, after, markerAfter);

      const installed = await runCli(["mcp", "install", "kilo"], fx.env);
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /changed during interrupted transaction; refusing destructive recovery/);
      assert.equal(readFileSync(fx.configPath, "utf8"), foreign.toString("utf8"));
      assert.equal(existsSync(pending.locatorPath), true);
      assert.equal(existsSync(pending.adjacentPath), true);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("config journal recovers interrupted caveman install across server and CAVEMAN_HOME", async () => {
    const fx = kiloMcpFixture();
    try {
      const before = Buffer.from(JSON.stringify({ theme: "dark" }, null, 2) + "\n");
      const interruptedAfter = Buffer.from(JSON.stringify({
        theme: "dark",
        mcp: { caveman: { type: "local", command: [fx.mcpV1], enabled: true } },
      }, null, 2) + "\n");
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      writeFileSync(fx.configPath, interruptedAfter, { mode: 0o600 });
      const pending = writePendingMcpInstall(
        "kilo",
        fx.markerPath,
        fx.configPath,
        before,
        interruptedAfter,
        kiloMarkerBytes(fx.mcpV1, fx.configPath),
      );

      const otherCavemanHome = join(fx.root, "other-caveman-home");
      const browseMarkerPath = join(otherCavemanHome, "mcp", "kilo.caveman-browse.json");
      writeFileSync(fx.mcpV2, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
      const installed = await runCli(["mcp", "install", "kilo", "--server", "caveman-browse"], {
        ...fx.env,
        CAVEMAN_HOME: otherCavemanHome,
        CAVEMAN_BROWSE_BIN: fx.mcpV2,
      });

      assert.equal(installed.code, 0, installed.stderr);
      assert.match(installed.stderr, /rolled back interrupted kilo caveman MCP transaction/);
      assert.match(installed.stderr, /Kilo Code: caveman_browse installed/);
      assert.deepEqual(JSON.parse(readFileSync(fx.configPath, "utf8")), {
        theme: "dark",
        mcp: {
          "caveman-browse": { type: "local", command: [fx.mcpV2], enabled: true },
        },
      });
      assert.deepEqual(JSON.parse(readFileSync(browseMarkerPath, "utf8")), {
        schema_version: 1,
        tool: "caveman_browse",
        command: fx.mcpV2,
        args: [],
        config_path: canonicalTestPath(fx.configPath),
      });
      assert.equal(existsSync(fx.markerPath), false);
      assert.equal(existsSync(pending.locatorPath), false);
      assert.equal(existsSync(pending.adjacentPath), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("malformed pending journal fails closed", async () => {
    const fx = kiloMcpFixture();
    try {
      const source = JSON.stringify({ theme: "dark" }, null, 2) + "\n";
      writeFileSync(fx.configPath, source);
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      writeFileSync(`${fx.markerPath}.pending`, "{}\n", { mode: 0o600 });

      const installed = await runCli(["mcp", "install", "kilo"], fx.env);
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /pending kilo caveman MCP transaction is malformed/);
      assert.equal(readFileSync(fx.configPath, "utf8"), source);
      assert.equal(existsSync(fx.markerPath), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("interrupted uninstall restores ownership before retrying", async () => {
    const fx = kiloMcpFixture();
    try {
      const installed = await runCli(["mcp", "install", "kilo"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      const configBefore = readFileSync(fx.configPath);
      const markerBefore = readFileSync(fx.markerPath);
      const configAfter = Buffer.from("{}\n");
      rmSync(fx.markerPath);
      const pending = writePendingMcpUninstall("kilo", fx.markerPath, fx.configPath, configBefore, configAfter, markerBefore);

      const removed = await runCli(["mcp", "uninstall"], fx.env);
      assert.equal(removed.code, 0, removed.stderr);
      assert.match(removed.stderr, /rolled back interrupted kilo caveman MCP transaction/);
      assert.equal(readFileSync(fx.configPath, "utf8"), configAfter.toString("utf8"));
      assert.equal(existsSync(fx.markerPath), false);
      assert.equal(existsSync(pending.locatorPath), false);
      assert.equal(existsSync(pending.adjacentPath), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("installed marker binds original Kilo config root until explicit uninstall", async () => {
    const fx = kiloMcpFixture();
    try {
      writeFileSync(fx.configPath, JSON.stringify({ theme: "original" }, null, 2) + "\n");
      const installed = await runCli(["mcp", "install", "kilo"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      const originalBytes = readFileSync(fx.configPath, "utf8");

      const relocatedHome = join(fx.root, "relocated-home");
      const relocatedConfig = join(relocatedHome, ".config", "kilo", "kilo.json");
      mkdirSync(dirname(relocatedConfig), { recursive: true });
      const relocatedBytes = JSON.stringify({ theme: "relocated" }, null, 2) + "\n";
      writeFileSync(relocatedConfig, relocatedBytes);
      const relocatedEnv = { ...fx.env, HOME: relocatedHome, XDG_CONFIG_HOME: join(relocatedHome, ".config") };

      const blocked = await runCli(["mcp", "install", "kilo"], relocatedEnv);
      assert.equal(blocked.code, 0, blocked.stderr);
      assert.match(blocked.stderr, /caveman is owned in .*kilo\.json; uninstall it before installing into/);
      assert.equal(readFileSync(fx.configPath, "utf8"), originalBytes);
      assert.equal(readFileSync(relocatedConfig, "utf8"), relocatedBytes);
      assert.equal(JSON.parse(readFileSync(fx.markerPath, "utf8")).config_path, canonicalTestPath(fx.configPath));

      const removed = await runCli(["mcp", "uninstall", "kilo"], relocatedEnv);
      assert.equal(removed.code, 0, removed.stderr);
      assert.deepEqual(JSON.parse(readFileSync(fx.configPath, "utf8")), { theme: "original" });
      assert.equal(readFileSync(relocatedConfig, "utf8"), relocatedBytes);
      assert.equal(existsSync(fx.markerPath), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("physical Kilo config move retains ownership marker", async () => {
    const fx = kiloMcpFixture();
    try {
      const installed = await runCli(["mcp", "install", "kilo"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      const relocatedHome = join(fx.root, "physically-moved-home");
      const relocatedConfig = join(relocatedHome, ".config", "kilo", "kilo.json");
      mkdirSync(dirname(relocatedConfig), { recursive: true });
      renameSync(fx.configPath, relocatedConfig);
      const relocatedEnv = { ...fx.env, HOME: relocatedHome, XDG_CONFIG_HOME: join(relocatedHome, ".config") };

      const removed = await runCli(["mcp", "uninstall", "kilo"], relocatedEnv);
      assert.equal(removed.code, 0, removed.stderr);
      assert.match(removed.stderr, /contains moved caveman state; retaining ownership marker/);
      assert.equal(existsSync(fx.markerPath), true);
      assert.deepEqual(JSON.parse(readFileSync(relocatedConfig, "utf8")).mcp.caveman, {
        type: "local",
        command: [fx.mcpV1],
        enabled: true,
      });
    } finally {
      fx.cleanup();
    }
  });
});

test("Kilo MCP refuses conflicting unowned entries", async () => {
  const fx = kiloMcpFixture();
  try {
    const source = JSON.stringify({
      theme: "dark",
      mcp: { caveman: { type: "local", command: ["user-owned-mcp"], enabled: true } },
    }, null, 2) + "\n";
    writeFileSync(fx.configPath, source);

    const installed = await runCli(["mcp", "install", "kilo"], fx.env);
    assert.equal(installed.code, 0, installed.stderr);
    assert.match(installed.stderr, /not Caveman-journaled; refusing overwrite/);
    assert.equal(readFileSync(fx.configPath, "utf8"), source);
    assert.equal(existsSync(fx.markerPath), false);

    const removed = await runCli(["mcp", "uninstall", "kilo"], fx.env);
    assert.equal(removed.code, 0, removed.stderr);
    assert.match(removed.stderr, /not Caveman-journaled; refusing removal/);
    assert.equal(readFileSync(fx.configPath, "utf8"), source);
  } finally {
    fx.cleanup();
  }
});

test("Kilo MCP refuses user-modified owned entries and keeps marker", async () => {
  const fx = kiloMcpFixture();
  try {
    const installed = await runCli(["mcp", "install", "kilo"], fx.env);
    assert.equal(installed.code, 0, installed.stderr);
    const config = JSON.parse(readFileSync(fx.configPath, "utf8"));
    config.mcp.caveman.command = ["user-modified-mcp"];
    const modified = JSON.stringify(config, null, 2) + "\n";
    writeFileSync(fx.configPath, modified);

    const removed = await runCli(["mcp", "uninstall", "kilo"], fx.env);
    assert.equal(removed.code, 0, removed.stderr);
    assert.match(removed.stderr, /changed since Caveman installed it; refusing removal/);
    assert.equal(readFileSync(fx.configPath, "utf8"), modified);
    assert.equal(existsSync(fx.markerPath), true);
    withEnv({ HOME: fx.env.HOME, XDG_CONFIG_HOME: fx.env.XDG_CONFIG_HOME, CAVEMAN_HOME: fx.env.CAVEMAN_HOME }, () => {
      const config = JSON.parse(buildWrapEnv(kilo).KILO_CONFIG_CONTENT);
      assert.equal(config.mcp?.caveman, undefined, "changed native entry must invalidate stale marker");
    });
  } finally {
    fx.cleanup();
  }
});

test("Kilo wrap rejects deleted registrations and malformed ownership journals", async (t) => {
  for (const mutation of ["deleted registration", "wrong marker tool", "extra marker field", "blank marker command"]) {
    await t.test(mutation, async () => {
      const fx = kiloMcpFixture();
      try {
        const installed = await runCli(["mcp", "install", "kilo"], fx.env);
        assert.equal(installed.code, 0, installed.stderr);
        if (mutation === "deleted registration") {
          writeFileSync(fx.configPath, "{}\n");
        } else {
          const marker = JSON.parse(readFileSync(fx.markerPath, "utf8"));
          if (mutation === "wrong marker tool") marker.tool = "different_tool";
          if (mutation === "extra marker field") marker.untrusted = true;
          if (mutation === "blank marker command") marker.command = " \t";
          writeFileSync(fx.markerPath, JSON.stringify(marker, null, 2) + "\n");
        }
        withEnv({ HOME: fx.env.HOME, XDG_CONFIG_HOME: fx.env.XDG_CONFIG_HOME, CAVEMAN_HOME: fx.env.CAVEMAN_HOME }, () => {
          const config = JSON.parse(buildWrapEnv(kilo).KILO_CONFIG_CONTENT);
          assert.equal(config.mcp?.caveman, undefined);
        });
      } finally {
        fx.cleanup();
      }
    });
  }
});

test("Kilo MCP refuses malformed JSON and non-object mcp", async (t) => {
  await t.test("empty existing file", async () => {
    const fx = kiloMcpFixture();
    try {
      writeFileSync(fx.configPath, "");
      const installed = await runCli(["mcp", "install", "kilo"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      assert.match(installed.stderr, /kilo\.json is empty; not modifying it/);
      assert.equal(readFileSync(fx.configPath, "utf8"), "");
      assert.equal(existsSync(fx.markerPath), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("malformed JSON", async () => {
    const fx = kiloMcpFixture();
    try {
      const source = "{not-json\n";
      writeFileSync(fx.configPath, source);
      const installed = await runCli(["mcp", "install", "kilo"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      assert.match(installed.stderr, /cannot read .*kilo\.json/);
      assert.equal(readFileSync(fx.configPath, "utf8"), source);
      assert.equal(existsSync(fx.markerPath), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("non-object mcp", async () => {
    const fx = kiloMcpFixture();
    try {
      const source = JSON.stringify({ theme: "dark", mcp: [] }, null, 2) + "\n";
      writeFileSync(fx.configPath, source);
      const installed = await runCli(["mcp", "install", "kilo"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      assert.match(installed.stderr, /mcp must be a JSON object; not modifying it/);
      assert.equal(readFileSync(fx.configPath, "utf8"), source);
      assert.equal(existsSync(fx.markerPath), false);
    } finally {
      fx.cleanup();
    }
  });
});

test("Kilo MCP preserves JSONC when kilo.json is absent", async () => {
  const fx = kiloMcpFixture();
  try {
    const source = "{\n  // user Kilo config\n  \"theme\": \"dark\"\n}\n";
    writeFileSync(fx.jsoncPath, source);
    const installed = await runCli(["mcp", "install", "kilo"], fx.env);
    assert.equal(installed.code, 0, installed.stderr);
    assert.match(installed.stderr, /XDG_CONFIG_HOME selects JSONC .* refusing comment-destructive mutation/);
    assert.equal(existsSync(fx.configPath), false);
    assert.equal(readFileSync(fx.jsoncPath, "utf8"), source);
    assert.equal(existsSync(fx.markerPath), false);

    mkdirSync(dirname(fx.markerPath), { recursive: true });
    writeFileSync(fx.markerPath, kiloMarkerBytes(fx.mcpV1, fx.configPath));
    const removed = await runCli(["mcp", "uninstall", "kilo"], fx.env);
    assert.equal(removed.code, 0, removed.stderr);
    assert.match(removed.stderr, /Kilo Code: caveman MCP tool removed/);
    assert.equal(readFileSync(fx.jsoncPath, "utf8"), source);
    assert.equal(existsSync(fx.markerPath), false);
  } finally {
    fx.cleanup();
  }
});
