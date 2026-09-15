import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, renameSync, rmSync, statSync, symlinkSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");
const { PROFILES } = await import(pathToFileURL(join(here, "..", "dist", "agents.generated.js")).href);
const { buildWrapEnv } = await import(`${pathToFileURL(cli).href}?qwen-wrap`);
const qwen = PROFILES.find((profile) => profile.id === "qwen");

assert.ok(qwen, "compiled registry must contain Qwen Code");

function routedQwenArgs(args) {
  return [...qwen.args, ...args];
}

function runCli(args, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...args], { env });
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

function withCwd(path, fn) {
  const previous = process.cwd();
  process.chdir(path);
  try {
    return fn();
  } finally {
    process.chdir(previous);
  }
}

function readInjected(env) {
  const path = env.QWEN_CODE_SYSTEM_SETTINGS_PATH;
  assert.ok(path, "Qwen system settings path must be injected");
  return { path, raw: readFileSync(path, "utf8"), config: JSON.parse(readFileSync(path, "utf8")) };
}

function qwenFixture() {
  const root = mkdtempSync(join(tmpdir(), "cave-qwen-wrap-"));
  const home = join(root, "home");
  const binDir = join(root, "bin");
  const systemConfig = join(root, "enterprise-system-settings.json");
  const userConfig = join(home, ".qwen", "settings.json");
  mkdirSync(binDir, { recursive: true });
  mkdirSync(dirname(userConfig), { recursive: true });
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });

  const systemBytes = JSON.stringify({
    general: { enableAutoUpdate: false },
    security: { folderTrust: { enabled: true } },
    providerProtocol: { openai: "sibling-protocol" },
    modelProviders: { sibling: [{ id: "sibling-model", envKey: "SIBLING_KEY" }] },
    fastModel: "qwen-oauth-fast",
    advisorModel: "qwen-oauth-advisor",
    visionModel: "qwen-oauth-vision",
    compactionModel: "qwen-oauth-compaction",
    imageModel: "qwen-oauth-image",
    voiceModel: "qwen-oauth-voice",
    disableAllHooks: false,
    tools: { webSearch: { enabled: true, model: "qwen-oauth-search" } },
    agents: { builtin: { exploreModel: "qwen-oauth-explore" }, allowedGrades: ["full"] },
    skills: { disabledLevels: [] },
    slashCommands: { disabled: [] },
  }, null, 2) + "\n";
  const userBytes = JSON.stringify({
    security: { auth: { selectedType: "qwen-oauth" } },
    model: { name: "user-model" },
  }, null, 2) + "\n";
  writeFileSync(systemConfig, systemBytes);
  writeFileSync(userConfig, userBytes);
  writeFileSync(
    join(home, ".caveman-cloud", "config.json"),
    JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false, browse: false } }),
  );
  writeFileSync(
    join(binDir, "qwen"),
    `#!/usr/bin/env node
import { readFileSync } from "node:fs";
const path = process.env.QWEN_CODE_SYSTEM_SETTINGS_PATH || "";
process.stdout.write(JSON.stringify({
  argv: process.argv.slice(2),
  config: path ? JSON.parse(readFileSync(path, "utf8")) : null,
  openaiKey: process.env.OPENAI_API_KEY || "",
  caveKey: process.env.CAVE_API_KEY || "",
  webSearchEnabled: process.env.ENABLE_WEB_SEARCH || "",
  workflowsDisabled: process.env.QWEN_CODE_DISABLE_WORKFLOWS || ""
}));
`,
    { mode: 0o755 },
  );

  const env = {
    ...process.env,
    HOME: home,
    CAVEMAN_HOME: home,
    PATH: `${binDir}:${process.env.PATH}`,
    QWEN_CODE_SYSTEM_SETTINGS_PATH: systemConfig,
    OPENAI_API_KEY: "sk-qwen-upstream-test",
    NO_COLOR: "1",
  };
  delete env.CAVE_GATEWAY_URL;
  delete env.CAVE_API_KEY;
  return {
    root,
    env,
    systemConfig,
    systemBytes,
    userConfig,
    userBytes,
    cleanup: () => rmSync(root, { recursive: true, force: true }),
  };
}

function qwenMcpFixture() {
  const root = mkdtempSync(join(tmpdir(), "cave-qwen-mcp-"));
  const home = join(root, "home");
  const cavemanHome = join(root, "caveman-home");
  const qwenHome = join(root, "relocated-qwen-home");
  const configPath = join(qwenHome, "settings.json");
  const systemConfigPath = join(root, "system-settings.json");
  const systemDefaultsPath = join(root, "system-defaults.json");
  const markerPath = join(cavemanHome, "mcp", "qwen.json");
  const mcpV1 = join(root, "caveman-mcp-v1");
  const mcpV2 = join(root, "caveman-mcp-v2");
  mkdirSync(dirname(configPath), { recursive: true });
  return {
    root,
    configPath,
    systemConfigPath,
    markerPath,
    mcpV1,
    mcpV2,
    env: {
      ...process.env,
      HOME: home,
      CAVEMAN_HOME: cavemanHome,
      QWEN_HOME: qwenHome,
      QWEN_CODE_SYSTEM_SETTINGS_PATH: systemConfigPath,
      QWEN_CODE_SYSTEM_DEFAULTS_PATH: systemDefaultsPath,
      CAVEMAN_MCP_BIN: mcpV1,
      NO_COLOR: "1",
    },
    cleanup: () => rmSync(root, { recursive: true, force: true }),
  };
}

function stateHash(bytes) {
  return bytes === null ? null : `sha256:${createHash("sha256").update(bytes).digest("hex")}`;
}

function canonicalPath(path) {
  try { return realpathSync(path); }
  catch { return join(realpathSync(dirname(path)), path.slice(dirname(path).length + 1)); }
}

function configPendingPath(configPath) {
  const canonical = canonicalPath(configPath);
  return join(dirname(canonical), `.${canonical.slice(dirname(canonical).length + 1)}.caveman-mcp.pending.json`);
}

function mcpLockPath(resourcePath) {
  const canonical = canonicalPath(resourcePath);
  const key = createHash("sha256").update(canonical).digest("hex").slice(0, 20);
  return join(dirname(canonical), `.caveman-mcp-${key}.lock`);
}

function writePendingCopies(markerPath, configPath, journal) {
  const bytes = JSON.stringify(journal, null, 2) + "\n";
  writeFileSync(`${markerPath}.pending`, bytes, { mode: 0o600 });
  writeFileSync(configPendingPath(configPath), bytes, { mode: 0o600 });
}

function ownedMarker(configPath, command, tool = "caveman_retrieve") {
  return {
    schema_version: 1,
    tool,
    command,
    args: [],
    config_path: canonicalPath(configPath),
  };
}

function ownedMarkerBytes(configPath, command, tool = "caveman_retrieve") {
  return Buffer.from(JSON.stringify(ownedMarker(configPath, command, tool), null, 2) + "\n");
}

function writePendingMcpInstall(agent, markerPath, configPath, configBefore, configAfter, markerAfter, configMode = 0o600) {
  const canonicalConfig = canonicalPath(configPath);
  const canonicalMarker = canonicalPath(markerPath);
  const journal = {
    schema_version: 1,
    transaction_id: "00000000-0000-4000-8000-000000000001",
    agent,
    server_name: "caveman",
    action: "install",
    config_path: canonicalConfig,
    marker_path: canonicalMarker,
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
  writePendingCopies(markerPath, configPath, journal);
}

function writePendingMcpUninstall(agent, markerPath, configPath, configBefore, configAfter, markerBefore, configMode = 0o600) {
  const canonicalConfig = canonicalPath(configPath);
  const canonicalMarker = canonicalPath(markerPath);
  const journal = {
    schema_version: 1,
    transaction_id: "00000000-0000-4000-8000-000000000002",
    agent,
    server_name: "caveman",
    action: "uninstall",
    config_path: canonicalConfig,
    marker_path: canonicalMarker,
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
  writePendingCopies(markerPath, configPath, journal);
}

test("Qwen profile uses high-precedence system settings with a bounded extension guard", () => {
  assert.deepEqual(qwen.binary_names, ["qwen"]);
  assert.deepEqual(qwen.args, ["--extensions=none"]);
  assert.equal(qwen.tested_agent_version, "0.22.3");
  assert.equal(qwen.install, "npm i -g @qwen-code/qwen-code@0.22.3");
  assert.equal(qwen.injection.method, "config-file");
  assert.equal(qwen.injection.env_var, "QWEN_CODE_SYSTEM_SETTINGS_PATH");
  assert.equal(qwen.injection.base_config.platform_default, "qwen-system-settings");
  for (const overlay of [qwen.injection.config_overlay.local, qwen.injection.config_overlay.managed]) {
    assert.equal(overlay.security.auth.selectedType, "openai");
    assert.equal(overlay.security.auth.enforcedType, "openai");
    assert.equal(overlay.modelFallbacks, "");
    assert.deepEqual(overlay.providerProtocol, {});
    assert.equal(overlay.model.baseUrl, "{{cave_base_url}}/v1");
    assert.equal(overlay.fastModel, "");
    assert.equal(overlay.advisorModel, "");
    assert.equal(overlay.visionModel, "");
    assert.equal(overlay.compactionModel, "");
    assert.equal(overlay.imageModel, "");
    assert.equal(overlay.voiceModel, "");
    assert.equal(overlay.disableAllHooks, true);
    assert.deepEqual(overlay.tools.webSearch, { enabled: false, model: "" });
    assert.equal(overlay.tools.workflowsEnabled, false);
    assert.deepEqual(overlay.tools.disabled, ["agent", "skill", "web_search", "create_sub_session", "workflow"]);
    assert.deepEqual(overlay.agents, { builtin: { exploreModel: "inherit" }, allowedGrades: [] });
    assert.deepEqual(overlay.permissions.deny, ["Agent", "Skill", "WebSearch", "CreateSubSession", "Workflow"]);
    assert.deepEqual(overlay.skills.disabledLevels, ["project", "user", "extension", "bundled"]);
    assert.deepEqual(overlay.experimental.liveVoice, { enabled: false, apiKey: "", model: "" });
    for (const command of ["auth", "model", "arena", "agents", "workflows", "permissions", "extensions", "reload-plugins", "mcp", "resume", "continue", "hooks"]) {
      assert.ok(overlay.slashCommands.disabled.includes(command), command);
    }
  }
});

test("local Qwen config preserves non-routing system policy and strips alternate routes", () => {
  const fx = qwenFixture();
  try {
    withEnv({
      HOME: fx.env.HOME,
      CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
      QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
      OPENAI_API_KEY: "sk-local-secret",
      CAVE_API_KEY: undefined,
    }, () => {
      const injected = readInjected(buildWrapEnv(qwen, "http://127.0.0.1:8787"));
      assert.notEqual(injected.path, fx.systemConfig);
      assert.equal(injected.config.general.enableAutoUpdate, false);
      assert.equal(injected.config.security.folderTrust.enabled, true);
      assert.equal(injected.config.security.auth.selectedType, "openai");
      assert.equal(injected.config.security.auth.enforcedType, "openai");
      assert.equal(injected.config.modelFallbacks, "");
      assert.equal(injected.config.modelProviders.sibling, undefined);
      assert.deepEqual(injected.config.providerProtocol, {});
      assert.equal(injected.config.model.name, "gpt-5.5");
      assert.equal(injected.config.model.baseUrl, "http://127.0.0.1:8787/w/qwen/v1");
      assert.equal(injected.config.fastModel, "");
      assert.equal(injected.config.advisorModel, "");
      assert.equal(injected.config.visionModel, "");
      assert.equal(injected.config.compactionModel, "");
      assert.equal(injected.config.imageModel, "");
      assert.equal(injected.config.voiceModel, "");
      assert.equal(injected.config.disableAllHooks, true);
      assert.deepEqual(injected.config.tools.webSearch, { enabled: false, model: "" });
      assert.equal(injected.config.tools.workflowsEnabled, false);
      assert.deepEqual(injected.config.tools.disabled, ["agent", "skill", "web_search", "create_sub_session", "workflow"]);
      assert.deepEqual(injected.config.agents, { builtin: { exploreModel: "inherit" }, allowedGrades: [] });
      assert.deepEqual(injected.config.skills.disabledLevels, ["project", "user", "extension", "bundled"]);
      assert.deepEqual(injected.config.experimental.liveVoice, { enabled: false, apiKey: "", model: "" });
      assert.deepEqual(injected.config.modelProviders.openai.map((model) => model.id), ["gpt-5.5", "gpt-5.4-mini"]);
      for (const model of injected.config.modelProviders.openai) {
        assert.equal(model.envKey, "OPENAI_API_KEY");
        assert.equal(model.baseUrl, "http://127.0.0.1:8787/w/qwen/v1");
        assert.equal(model.generationConfig.customHeaders["X-Cave-Agent"], "qwen");
      }
      assert.doesNotMatch(injected.raw, /sk-local-secret/);
      assert.equal(readFileSync(fx.systemConfig, "utf8"), fx.systemBytes);
      assert.equal(readFileSync(fx.userConfig, "utf8"), fx.userBytes);
    });
  } finally {
    fx.cleanup();
  }
});

test("managed Qwen config separates gateway bearer from upstream credential", () => {
  const fx = qwenFixture();
  try {
    withEnv({
      HOME: fx.env.HOME,
      CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
      QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
      CAVE_API_KEY: "cave-managed-secret",
      OPENAI_API_KEY: "sk-upstream-secret",
    }, () => {
      const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
      for (const model of injected.config.modelProviders.openai) {
        assert.equal(model.envKey, "CAVE_API_KEY");
        assert.equal(model.baseUrl, "https://gateway.example/w/qwen/v1");
        assert.equal(model.generationConfig.customHeaders["x-cave-upstream-key"], "$OPENAI_API_KEY");
        assert.equal(model.generationConfig.customHeaders["X-Cave-Agent"], "qwen");
      }
      assert.doesNotMatch(injected.raw, /cave-managed-secret|sk-upstream-secret/);
    });
  } finally {
    fx.cleanup();
  }
});

test("managed Qwen omits upstream header when only stored gateway credentials are available", () => {
  const fx = qwenFixture();
  try {
    const base = JSON.parse(fx.systemBytes);
    base.modelProviders.openai = [{
      id: "stale-provider",
      generationConfig: { customHeaders: { "x-cave-upstream-key": "stale-literal" } },
    }];
    writeFileSync(fx.systemConfig, JSON.stringify(base));
    for (const upstreamKey of [undefined, "  "]) {
      withEnv({
        HOME: fx.env.HOME,
        CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
        QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
        CAVE_API_KEY: "cave-managed-secret",
        OPENAI_API_KEY: upstreamKey,
      }, () => {
        const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
        for (const model of injected.config.modelProviders.openai) {
          assert.equal(model.envKey, "CAVE_API_KEY");
          assert.equal(Object.hasOwn(model.generationConfig.customHeaders, "x-cave-upstream-key"), false);
          assert.equal(model.generationConfig.customHeaders["X-Cave-Agent"], "qwen");
        }
        assert.doesNotMatch(injected.raw, /\$OPENAI_API_KEY|cave_optional_openai_key_env|cave-managed-secret|stale-literal/);
      });
    }
  } finally {
    fx.cleanup();
  }
});

test("managed Qwen retains native key reference for effective dotenv and settings env keys", async (t) => {
  for (const source of ["dotenv", "settings env"]) {
    await t.test(source, () => {
      const fx = qwenFixture();
      const secret = `sk-${source.replace(" ", "-")}-secret`;
      try {
        if (source === "dotenv") {
          writeFileSync(join(dirname(fx.userConfig), ".env"), `OPENAI_API_KEY=${secret}\n`);
        } else {
          const user = JSON.parse(fx.userBytes);
          user.env = { OPENAI_API_KEY: secret };
          writeFileSync(fx.userConfig, JSON.stringify(user));
        }
        withEnv({
          HOME: fx.env.HOME,
          CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
          QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
          CAVE_API_KEY: "cave-managed-secret",
          OPENAI_API_KEY: undefined,
        }, () => {
          const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
          for (const model of injected.config.modelProviders.openai) {
            assert.equal(model.generationConfig.customHeaders["x-cave-upstream-key"], "$OPENAI_API_KEY");
          }
          assert.doesNotMatch(injected.raw, new RegExp(secret));
        });
      } finally {
        fx.cleanup();
      }
    });
  }
});

test("managed Qwen requires a second settings pass for late credential sources", async (t) => {
  await t.test("no-relaunch omits user settings env", () => {
    const fx = qwenFixture();
    try {
      const user = JSON.parse(fx.userBytes);
      user.env = { OPENAI_API_KEY: "sk-no-relaunch-settings" };
      writeFileSync(fx.userConfig, JSON.stringify(user));
      withEnv({
        HOME: fx.env.HOME,
        CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
        QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
        QWEN_CODE_NO_RELAUNCH: "1",
        SANDBOX: undefined,
        CAVE_API_KEY: "cave-managed-secret",
        OPENAI_API_KEY: undefined,
      }, () => {
        const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
        for (const model of injected.config.modelProviders.openai) {
          assert.equal(Object.hasOwn(model.generationConfig.customHeaders, "x-cave-upstream-key"), false);
        }
        assert.doesNotMatch(injected.raw, /\$OPENAI_API_KEY|sk-no-relaunch-settings/);
      });
    } finally {
      fx.cleanup();
    }
  });

  await t.test("sandbox omits project dotenv", () => {
    const fx = qwenFixture();
    const workspace = join(fx.root, "workspace");
    try {
      mkdirSync(join(workspace, ".qwen"), { recursive: true });
      writeFileSync(join(workspace, ".qwen", ".env"), "OPENAI_API_KEY=sk-sandbox-project\n");
      withEnv({
        HOME: fx.env.HOME,
        CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
        QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
        QWEN_CODE_NO_RELAUNCH: undefined,
        SANDBOX: "sandbox",
        CAVE_API_KEY: "cave-managed-secret",
        OPENAI_API_KEY: undefined,
      }, () => withCwd(workspace, () => {
        const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
        for (const model of injected.config.modelProviders.openai) {
          assert.equal(Object.hasOwn(model.generationConfig.customHeaders, "x-cave-upstream-key"), false);
        }
        assert.doesNotMatch(injected.raw, /\$OPENAI_API_KEY|sk-sandbox-project/);
      }));
    } finally {
      fx.cleanup();
    }
  });

  await t.test("project dotenv cannot enable no-relaunch after wrapper inspection", () => {
    const fx = qwenFixture();
    const workspace = join(fx.root, "workspace");
    try {
      mkdirSync(join(workspace, ".qwen"), { recursive: true });
      writeFileSync(
        join(workspace, ".qwen", ".env"),
        "OPENAI_API_KEY=sk-late-no-relaunch\nQWEN_CODE_NO_RELAUNCH=1\n",
      );
      withEnv({
        HOME: fx.env.HOME,
        CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
        QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
        QWEN_CODE_NO_RELAUNCH: undefined,
        SANDBOX: undefined,
        CAVE_API_KEY: "cave-managed-secret",
        OPENAI_API_KEY: undefined,
      }, () => withCwd(workspace, () => {
        const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
        for (const model of injected.config.modelProviders.openai) {
          assert.equal(Object.hasOwn(model.generationConfig.customHeaders, "x-cave-upstream-key"), false);
        }
        assert.doesNotMatch(injected.raw, /\$OPENAI_API_KEY|sk-late-no-relaunch/);
      }));
    } finally {
      fx.cleanup();
    }
  });

  await t.test("settings env cannot enable sandbox after wrapper inspection", () => {
    const fx = qwenFixture();
    try {
      const user = JSON.parse(fx.userBytes);
      user.env = { OPENAI_API_KEY: "sk-late-sandbox", SANDBOX: "sandbox" };
      writeFileSync(fx.userConfig, JSON.stringify(user));
      withEnv({
        HOME: fx.env.HOME,
        CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
        QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
        QWEN_CODE_NO_RELAUNCH: undefined,
        SANDBOX: undefined,
        CAVE_API_KEY: "cave-managed-secret",
        OPENAI_API_KEY: undefined,
      }, () => {
        const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
        for (const model of injected.config.modelProviders.openai) {
          assert.equal(Object.hasOwn(model.generationConfig.customHeaders, "x-cave-upstream-key"), false);
        }
        assert.doesNotMatch(injected.raw, /\$OPENAI_API_KEY|sk-late-sandbox/);
      });
    } finally {
      fx.cleanup();
    }
  });

  await t.test("no-relaunch retains home dotenv fallback", () => {
    const fx = qwenFixture();
    try {
      writeFileSync(join(dirname(fx.userConfig), ".env"), "OPENAI_API_KEY=sk-home-fallback\n");
      withEnv({
        HOME: fx.env.HOME,
        CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
        QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
        QWEN_CODE_NO_RELAUNCH: "1",
        SANDBOX: undefined,
        CAVE_API_KEY: "cave-managed-secret",
        OPENAI_API_KEY: undefined,
      }, () => {
        const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
        for (const model of injected.config.modelProviders.openai) {
          assert.equal(model.generationConfig.customHeaders["x-cave-upstream-key"], "$OPENAI_API_KEY");
        }
        assert.doesNotMatch(injected.raw, /sk-home-fallback/);
      });
    } finally {
      fx.cleanup();
    }
  });
});

test("managed Qwen follows workspace trust for project credential sources", async (t) => {
  for (const trust of ["DO_NOT_TRUST", "TRUST_FOLDER"]) {
    for (const source of ["project dotenv", "workspace settings env"]) {
      await t.test(`${trust}: ${source}`, () => {
        const fx = qwenFixture();
        const workspace = join(fx.root, "workspace");
        const workspaceConfigDir = join(workspace, ".qwen");
        const secret = `sk-${trust.toLowerCase()}-${source.replaceAll(" ", "-")}`;
        try {
          mkdirSync(workspaceConfigDir, { recursive: true });
          writeFileSync(
            join(dirname(fx.userConfig), "trustedFolders.json"),
            JSON.stringify({ [fx.root]: "TRUST_FOLDER", [workspace]: trust }),
          );
          if (source === "project dotenv") {
            writeFileSync(join(workspaceConfigDir, ".env"), `OPENAI_API_KEY=${secret}\n`);
          } else {
            writeFileSync(join(workspaceConfigDir, "settings.json"), JSON.stringify({ env: { OPENAI_API_KEY: secret } }));
          }
          withEnv({
            HOME: fx.env.HOME,
            CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
            QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
            CAVE_API_KEY: "cave-managed-secret",
            OPENAI_API_KEY: undefined,
          }, () => withCwd(workspace, () => {
            const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
            for (const model of injected.config.modelProviders.openai) {
              if (trust === "TRUST_FOLDER") {
                assert.equal(model.generationConfig.customHeaders["x-cave-upstream-key"], "$OPENAI_API_KEY");
              } else {
                assert.equal(Object.hasOwn(model.generationConfig.customHeaders, "x-cave-upstream-key"), false);
              }
            }
            assert.doesNotMatch(injected.raw, new RegExp(secret));
            if (trust === "DO_NOT_TRUST") assert.doesNotMatch(injected.raw, /\$OPENAI_API_KEY/);
          }));
        } finally {
          fx.cleanup();
        }
      });
    }
  }
});

test("managed Qwen migrates legacy folder trust before reading project credentials", async (t) => {
  for (const source of ["project dotenv", "workspace settings env"]) {
    await t.test(source, () => {
      const fx = qwenFixture();
      const workspace = join(fx.root, "workspace");
      const workspaceConfigDir = join(workspace, ".qwen");
      const secret = `sk-legacy-trust-${source.replaceAll(" ", "-")}`;
      try {
        mkdirSync(workspaceConfigDir, { recursive: true });
        const system = JSON.parse(fx.systemBytes);
        delete system.security;
        system.folderTrust = true;
        writeFileSync(fx.systemConfig, JSON.stringify(system));
        writeFileSync(
          join(dirname(fx.userConfig), "trustedFolders.json"),
          JSON.stringify({ [workspace]: "DO_NOT_TRUST" }),
        );
        if (source === "project dotenv") {
          writeFileSync(join(workspaceConfigDir, ".env"), `OPENAI_API_KEY=${secret}\n`);
        } else {
          writeFileSync(join(workspaceConfigDir, "settings.json"), JSON.stringify({ env: { OPENAI_API_KEY: secret } }));
        }
        withEnv({
          HOME: fx.env.HOME,
          CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
          QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
          CAVE_API_KEY: "cave-managed-secret",
          OPENAI_API_KEY: undefined,
        }, () => withCwd(workspace, () => {
          const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
          for (const model of injected.config.modelProviders.openai) {
            assert.equal(Object.hasOwn(model.generationConfig.customHeaders, "x-cave-upstream-key"), false);
          }
          assert.doesNotMatch(injected.raw, /\$OPENAI_API_KEY/);
          assert.doesNotMatch(injected.raw, new RegExp(secret));
        }));
      } finally {
        fx.cleanup();
      }
    });
  }
});

test("managed Qwen applies effective excluded env policy only to plain project dotenv", async (t) => {
  for (const legacy of [false, true]) {
    for (const source of ["plain project dotenv", "scoped project dotenv", "workspace settings env"]) {
      await t.test(`${legacy ? "legacy" : "current"}: ${source}`, () => {
        const fx = qwenFixture();
        const workspace = join(fx.root, "workspace");
        const workspaceConfigDir = join(workspace, ".qwen");
        const secret = `sk-excluded-${legacy}-${source.replaceAll(" ", "-")}`;
        try {
          mkdirSync(workspaceConfigDir, { recursive: true });
          const system = JSON.parse(fx.systemBytes);
          if (legacy) system.excludedProjectEnvVars = ["OPENAI_API_KEY"];
          else system.advanced = { excludedEnvVars: ["OPENAI_API_KEY"] };
          writeFileSync(fx.systemConfig, JSON.stringify(system));
          if (source === "plain project dotenv") {
            writeFileSync(join(workspace, ".env"), `OPENAI_API_KEY=${secret}\n`);
          } else if (source === "scoped project dotenv") {
            writeFileSync(join(workspaceConfigDir, ".env"), `OPENAI_API_KEY=${secret}\n`);
          } else {
            writeFileSync(join(workspaceConfigDir, "settings.json"), JSON.stringify({ env: { OPENAI_API_KEY: secret } }));
          }
          withEnv({
            HOME: fx.env.HOME,
            CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
            QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
            CAVE_API_KEY: "cave-managed-secret",
            OPENAI_API_KEY: undefined,
          }, () => withCwd(workspace, () => {
            const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
            for (const model of injected.config.modelProviders.openai) {
              if (source === "plain project dotenv") {
                assert.equal(Object.hasOwn(model.generationConfig.customHeaders, "x-cave-upstream-key"), false);
              } else {
                assert.equal(model.generationConfig.customHeaders["x-cave-upstream-key"], "$OPENAI_API_KEY");
              }
            }
            assert.doesNotMatch(injected.raw, new RegExp(secret));
            if (source === "plain project dotenv") assert.doesNotMatch(injected.raw, /\$OPENAI_API_KEY/);
          }));
        } finally {
          fx.cleanup();
        }
      });
    }
  }
});

test("managed Qwen resolves settings env before deciding upstream key availability", () => {
  const fx = qwenFixture();
  try {
    const user = JSON.parse(fx.userBytes);
    user.env = { OPENAI_API_KEY: "$EMPTY_VAR" };
    writeFileSync(fx.userConfig, JSON.stringify(user));
    withEnv({
      HOME: fx.env.HOME,
      CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
      QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
      CAVE_API_KEY: "cave-managed-secret",
      OPENAI_API_KEY: undefined,
      EMPTY_VAR: "",
    }, () => {
      const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
      for (const model of injected.config.modelProviders.openai) {
        assert.equal(Object.hasOwn(model.generationConfig.customHeaders, "x-cave-upstream-key"), false);
      }
      assert.doesNotMatch(injected.raw, /\$OPENAI_API_KEY|\$EMPTY_VAR/);
    });
  } finally {
    fx.cleanup();
  }
});

test("managed Qwen bootstraps trust path from a newly discovered QWEN_HOME", () => {
  const fx = qwenFixture();
  const workspace = join(fx.root, "workspace");
  const relocatedHome = join(fx.root, "relocated-qwen-home");
  const trustPath = join(fx.root, "operator-trusted-folders.json");
  const secret = "sk-relocated-trust-secret";
  try {
    mkdirSync(join(workspace, ".qwen"), { recursive: true });
    mkdirSync(relocatedHome, { recursive: true });
    writeFileSync(join(dirname(fx.userConfig), ".env"), `QWEN_HOME=${relocatedHome}\n`);
    writeFileSync(join(relocatedHome, ".env"), `QWEN_CODE_TRUSTED_FOLDERS_PATH=${trustPath}\n`);
    writeFileSync(join(relocatedHome, "settings.json"), "{}\n");
    writeFileSync(trustPath, JSON.stringify({ [workspace]: "DO_NOT_TRUST" }));
    writeFileSync(join(workspace, ".qwen", ".env"), `OPENAI_API_KEY=${secret}\n`);
    withEnv({
      HOME: fx.env.HOME,
      CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
      QWEN_HOME: undefined,
      QWEN_CODE_TRUSTED_FOLDERS_PATH: undefined,
      QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
      CAVE_API_KEY: "cave-managed-secret",
      OPENAI_API_KEY: undefined,
    }, () => withCwd(workspace, () => {
      const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
      for (const model of injected.config.modelProviders.openai) {
        assert.equal(Object.hasOwn(model.generationConfig.customHeaders, "x-cave-upstream-key"), false);
      }
      assert.doesNotMatch(injected.raw, /\$OPENAI_API_KEY/);
      assert.doesNotMatch(injected.raw, new RegExp(secret));
    }));
  } finally {
    fx.cleanup();
  }
});

test("managed Qwen accepts comments but rejects trailing-comma policy files", async (t) => {
  await t.test("comments are valid settings syntax", () => {
    const fx = qwenFixture();
    const workspace = join(fx.root, "workspace");
    const secret = "sk-jsonc-comment-secret";
    try {
      mkdirSync(join(workspace, ".qwen"), { recursive: true });
      writeFileSync(
        join(workspace, ".qwen", "settings.json"),
        `{\n  // Qwen accepts line comments.\n  /* Qwen accepts block comments too. */\n  "env": { "OPENAI_API_KEY": "${secret}" }\n}\n`,
      );
      withEnv({
        HOME: fx.env.HOME,
        CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
        QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
        CAVE_API_KEY: "cave-managed-secret",
        OPENAI_API_KEY: undefined,
      }, () => withCwd(workspace, () => {
        const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
        for (const model of injected.config.modelProviders.openai) {
          assert.equal(model.generationConfig.customHeaders["x-cave-upstream-key"], "$OPENAI_API_KEY");
        }
        assert.doesNotMatch(injected.raw, new RegExp(secret));
      }));
    } finally {
      fx.cleanup();
    }
  });

  await t.test("block comments cannot join otherwise invalid JSON tokens", () => {
    const fx = qwenFixture();
    const workspace = join(fx.root, "workspace");
    try {
      mkdirSync(join(workspace, ".qwen"), { recursive: true });
      writeFileSync(
        join(workspace, ".qwen", "settings.json"),
        '{"$version":2/* joining this comment would change syntax */.0,"env":{"OPENAI_API_KEY":"sk-comment-join"}}\n',
      );
      withEnv({
        HOME: fx.env.HOME,
        CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
        QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
        CAVE_API_KEY: "cave-managed-secret",
        OPENAI_API_KEY: undefined,
      }, () => withCwd(workspace, () => {
        assert.throws(
          () => buildWrapEnv(qwen, "https://gateway.example"),
          /cannot safely resolve Qwen's effective settings/,
        );
      }));
    } finally {
      fx.cleanup();
    }
  });

  await t.test("prototype-chain keys cannot create an effective upstream credential", () => {
    const fx = qwenFixture();
    const workspace = join(fx.root, "workspace");
    try {
      mkdirSync(join(workspace, ".qwen"), { recursive: true });
      writeFileSync(fx.userConfig, '{"env":{"SAFE":"x"}}\n');
      writeFileSync(
        join(workspace, ".qwen", "settings.json"),
        '{"env":{"__proto__":{"OPENAI_API_KEY":"sk-prototype"}}}\n',
      );
      withEnv({
        HOME: fx.env.HOME,
        CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
        QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
        CAVE_API_KEY: "cave-managed-secret",
        OPENAI_API_KEY: undefined,
      }, () => withCwd(workspace, () => {
        const injected = readInjected(buildWrapEnv(qwen, "https://gateway.example"));
        for (const model of injected.config.modelProviders.openai) {
          assert.equal(Object.hasOwn(model.generationConfig.customHeaders, "x-cave-upstream-key"), false);
        }
        assert.doesNotMatch(injected.raw, /\$OPENAI_API_KEY|sk-prototype/);
        assert.equal(Object.prototype.OPENAI_API_KEY, undefined);
      }));
    } finally {
      fx.cleanup();
    }
  });

  for (const source of ["workspace settings", "trusted folders"]) {
    await t.test(`${source} with trailing comma`, () => {
      const fx = qwenFixture();
      const workspace = join(fx.root, "workspace");
      try {
        mkdirSync(join(workspace, ".qwen"), { recursive: true });
        writeFileSync(join(workspace, ".qwen", ".env"), "OPENAI_API_KEY=sk-invalid-json-source\n");
        if (source === "workspace settings") {
          writeFileSync(join(workspace, ".qwen", "settings.json"), '{ "env": { "OPENAI_API_KEY": "sk-invalid" }, }\n');
        } else {
          writeFileSync(
            join(dirname(fx.userConfig), "trustedFolders.json"),
            `{ ${JSON.stringify(workspace)}: "DO_NOT_TRUST", }\n`,
          );
        }
        withEnv({
          HOME: fx.env.HOME,
          CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
          QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
          CAVE_API_KEY: "cave-managed-secret",
          OPENAI_API_KEY: undefined,
        }, () => withCwd(workspace, () => {
          assert.throws(
            () => buildWrapEnv(qwen, "https://gateway.example"),
            /cannot safely resolve Qwen's effective settings/,
          );
        }));
      } finally {
        fx.cleanup();
      }
    });
  }
});

test("managed Qwen fails closed when workspace trust state is malformed", () => {
  const fx = qwenFixture();
  const workspace = join(fx.root, "workspace");
  try {
    mkdirSync(workspace, { recursive: true });
    writeFileSync(join(dirname(fx.userConfig), "trustedFolders.json"), JSON.stringify({ [workspace]: "NOT_A_TRUST_LEVEL" }));
    withEnv({
      HOME: fx.env.HOME,
      CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
      QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
      CAVE_API_KEY: "cave-managed-secret",
      OPENAI_API_KEY: undefined,
    }, () => withCwd(workspace, () => {
      assert.throws(
        () => buildWrapEnv(qwen, "https://gateway.example"),
        /cannot safely resolve Qwen's effective settings/,
      );
    }));
  } finally {
    fx.cleanup();
  }
});

test("caveman qwen passes user args and never mutates Qwen settings", async () => {
  const fx = qwenFixture();
  try {
    const out = await new Promise((resolve, reject) => {
      const child = spawn(process.execPath, [cli, "qwen", "--model", "gpt-5.4-mini", "-p", "review this"], { env: fx.env });
      let stdout = "";
      let stderr = "";
      child.stdout.on("data", (chunk) => (stdout += chunk));
      child.stderr.on("data", (chunk) => (stderr += chunk));
      child.on("error", reject);
      child.on("exit", (code, signal) => resolve({ code, signal, stdout, stderr }));
    });
    assert.equal(out.code, 0, `exit ${out.code}/${out.signal}: ${out.stderr}`);
    const child = JSON.parse(out.stdout);
    assert.deepEqual(child.argv, routedQwenArgs(["--model", "gpt-5.4-mini", "-p", "review this"]));
    assert.equal(child.openaiKey, "sk-qwen-upstream-test");
    assert.equal(child.caveKey, "");
    assert.equal(child.config.model.name, "gpt-5.5");
    assert.equal(child.config.modelProviders.openai[0].baseUrl, "http://127.0.0.1:8787/w/qwen/v1");
    assert.equal(child.webSearchEnabled, "0");
    assert.equal(child.workflowsDisabled, "1");
    assert.equal(readFileSync(fx.systemConfig, "utf8"), fx.systemBytes);
    assert.equal(readFileSync(fx.userConfig, "utf8"), fx.userBytes);
  } finally {
    fx.cleanup();
  }
});

test("Qwen route guard accepts only profile-backed CLI selectors", async (t) => {
  const routed = [
    { name: "profile model", args: ["--model", "gpt-5.5", "-p", "hello"] },
    { name: "profile model equals", args: ["--model=gpt-5.4-mini", "-p", "hello"] },
    { name: "profile long alias model", args: ["--m=gpt-5.5", "-p", "hello"] },
    { name: "profile spaced alias model", args: ["--m", "gpt-5.5", "-p", "hello"] },
    { name: "profile short model", args: ["-m=gpt-5.5", "-p", "hello"] },
    { name: "profile fallback list", args: ["--fallback-model", "gpt-5.4-mini,gpt-5.5", "-p", "hello"] },
    { name: "profile camel fallback", args: ["--fallbackModel=gpt-5.4-mini", "-p", "hello"] },
    { name: "repeatable profile fallback", args: ["--fallback-model=gpt-5.5", "--fallbackModel=gpt-5.4-mini", "-p", "hello"] },
    { name: "OpenAI auth", args: ["--auth-type", "openai", "-p", "hello"] },
    { name: "camel OpenAI auth", args: ["--authType=openai", "-p", "hello"] },
    { name: "explicit modes off", args: ["--safe-mode=false", "--bare=false", "-p", "hello"] },
    { name: "negated modes", args: ["--no-safeMode", "--no-bare", "-p", "hello"] },
    { name: "experimental surfaces off", args: ["--acp=false", "--experimental-skills=false", "-p", "hello"] },
    { name: "separator", args: ["-p", "hello", "--", "--model", "outside-wrapper-parser"] },
  ];
  for (const fixture of routed) {
    await t.test(fixture.name, async () => {
      const fx = qwenFixture();
      try {
        const out = await runCli(["qwen", ...fixture.args], { ...fx.env, CAVEMAN_OFFLINE: "1" });
        assert.equal(out.code, 0, out.stderr);
        const child = JSON.parse(out.stdout);
        assert.deepEqual(child.argv, routedQwenArgs(fixture.args));
        assert.equal(child.config.security.auth.enforcedType, "openai");
        assert.equal(child.config.modelFallbacks, "");
        assert.equal(child.config.modelProviders.openai[0].baseUrl, "http://127.0.0.1:8787/w/qwen/v1");
        assert.doesNotMatch(out.stderr, /launching directly/);
        assert.equal(readFileSync(fx.systemConfig, "utf8"), fx.systemBytes);
        assert.equal(readFileSync(fx.userConfig, "utf8"), fx.userBytes);
      } finally {
        fx.cleanup();
      }
    });
  }
});

test("Qwen route guard launches direct for bypassing or ambiguous selectors", async (t) => {
  const secret = "sk-test-qwen-cli-secret";
  const direct = [
    { name: "foreign model", surface: "--model", args: ["--model", "qwen3-coder-plus", "-p", "hello"] },
    { name: "foreign spaced alias model", surface: "--model", args: ["--m", "qwen3-coder-plus", "-p", "hello"] },
    { name: "foreign short model", surface: "-m", args: ["-mqwen3-coder-plus", "-p", "hello"] },
    { name: "ambiguous model cluster", surface: "-m", args: ["-dm", "-p", "hello"] },
    { name: "repeated model", surface: "--model", args: ["--model", "gpt-5.5", "--model=gpt-5.4-mini", "-p", "hello"] },
    { name: "malformed model", surface: "--model", args: ["--model", "--debug", "-p", "hello"] },
    { name: "foreign fallback", surface: "--fallback-model", args: ["--fallback-model=qwen3-coder-plus", "-p", "hello"] },
    { name: "inline fallback tail", surface: "--fallback-model", args: ["--fallback-model=gpt-5.5", "qwen3-coder-plus", "-p", "hello"] },
    { name: "foreign auth", surface: "--auth-type", args: ["--authType=anthropic", "-p", "hello"] },
    { name: "repeated auth", surface: "--auth-type", args: ["--auth-type", "openai", "--authType=openai", "-p", "hello"] },
    { name: "CLI API key", surface: "--openai-api-key", args: ["--openai-api-key", secret, "-p", "hello"] },
    { name: "camel CLI API key", surface: "--openai-api-key", args: [`--openaiApiKey=${secret}`, "-p", "hello"] },
    { name: "CLI base URL", surface: "--openai-base-url", args: ["--openaiBaseUrl", "https://other.example/v1", "-p", "hello"] },
    { name: "safe mode", surface: "--safe-mode", args: ["--safeMode=true", "-p", "hello"] },
    { name: "malformed safe mode", surface: "--safe-mode", args: ["--safe-mode=maybe", "-p", "hello"] },
    { name: "bare mode", surface: "--bare", args: ["--bare", "false", "-p", "hello"] },
    { name: "bare token after separator", surface: "--bare", args: ["-p", "hello", "--", "--bare"] },
    { name: "safe mode environment", surface: "--safe-mode", args: ["-p", "hello"], env: { QWEN_CODE_SAFE_MODE: "yes" } },
    { name: "bare environment defeats CLI false", surface: "--bare", args: ["--bare=false", "-p", "hello"], env: { QWEN_CODE_SIMPLE: "on" } },
    { name: "extension value", surface: "--extensions", args: ["--extensions", "foreign", "-p", "hello"] },
    { name: "extension equals", surface: "--extensions", args: ["--extensions=foreign", "-p", "hello"] },
    { name: "extension short equals", surface: "--extensions", args: ["-e=foreign", "-p", "hello"] },
    { name: "extension compact short", surface: "--extensions", args: ["-eforeign", "-p", "hello"] },
    { name: "extension short cluster", surface: "--extensions", args: ["-de", "foreign", "-p", "hello"] },
    { name: "repeated extension lock", surface: "--extensions", args: ["--extensions=none", "-p", "hello"] },
    { name: "ACP", surface: "--acp", args: ["--acp", "-p", "hello"] },
    { name: "experimental ACP", surface: "--experimental-acp", args: ["--experimental-acp=true", "-p", "hello"] },
    { name: "experimental skills", surface: "--experimental-skills", args: ["--experimental-skills", "-p", "hello"] },
    { name: "experimental ACP camel", surface: "--experimentalAcp", args: ["--experimentalAcp", "-p", "hello"] },
    { name: "experimental skills camel", surface: "--experimentalSkills", args: ["--experimentalSkills=true", "-p", "hello"] },
    { name: "extension listing camel", surface: "control mode", args: ["--listExtensions"] },
    { name: "extension listing", surface: "control mode", args: ["--list-extensions"] },
    { name: "extension listing short", surface: "control mode", args: ["-l"] },
    { name: "extension listing short cluster", surface: "control mode", args: ["-dl"] },
    { name: "channel mode", surface: "control mode", args: ["--channel", "stdio"] },
    { name: "serve command", surface: "serve", args: ["serve"] },
    { name: "option-prefixed serve command", surface: "serve", args: ["--debug", "serve"] },
    { name: "review command", surface: "review", args: ["review", "run"] },
    { name: "continue latest session", surface: "session restore", args: ["--continue", "-p", "hello"] },
    { name: "continue latest session short", surface: "session restore", args: ["-c", "-p", "hello"] },
    { name: "continue latest session short cluster", surface: "session restore", args: ["-dc", "-p", "hello"] },
    { name: "resume session", surface: "session restore", args: ["--resume", "session-id", "-p", "hello"] },
    { name: "resume session equals", surface: "session restore", args: ["--resume=session-id", "-p", "hello"] },
    { name: "resume session short", surface: "session restore", args: ["-r", "session-id", "-p", "hello"] },
    { name: "resume session compact short", surface: "session restore", args: ["-rsession-id", "-p", "hello"] },
    { name: "resume session short cluster", surface: "session restore", args: ["-drsession-id", "-p", "hello"] },
    { name: "fork restored session", surface: "session restore", args: ["--fork-session", "-p", "hello"] },
    { name: "fork restored session camel", surface: "session restore", args: ["--forkSession=true", "-p", "hello"] },
  ];
  for (const fixture of direct) {
    await t.test(fixture.name, async () => {
      const fx = qwenFixture();
      try {
        const out = await runCli(["qwen", ...fixture.args], {
          ...fx.env,
          CAVEMAN_OFFLINE: "1",
          QWEN_CODE_SAFE_MODE: undefined,
          QWEN_CODE_SIMPLE: undefined,
          ...fixture.env,
        });
        assert.equal(out.code, 0, out.stderr);
        const child = JSON.parse(out.stdout);
        assert.deepEqual(child.argv, fixture.args);
        assert.equal(child.config.modelProviders.openai, undefined);
        assert.ok(out.stderr.includes(`Qwen ${fixture.surface}`), out.stderr);
        assert.match(out.stderr, /launching directly/);
        assert.doesNotMatch(out.stderr, /sk-test-qwen-cli-secret|other\.example|qwen3-coder-plus/);
        assert.equal(readFileSync(fx.systemConfig, "utf8"), fx.systemBytes);
        assert.equal(readFileSync(fx.userConfig, "utf8"), fx.userBytes);
      } finally {
        fx.cleanup();
      }
    });
  }
});

test("Qwen direct selector bypass starts no proxy and buildWrapEnv fails closed", async () => {
  const fx = qwenFixture();
  const proxy = join(fx.root, "caveman-proxy-sentinel");
  const sentinel = join(fx.root, "proxy-was-started");
  try {
    writeFileSync(join(fx.env.HOME, ".caveman-cloud", "config.json"), JSON.stringify({
      wrap: { proxy: true, shrink: false, mcp: false, browse: false },
    }));
    writeFileSync(proxy, `#!/bin/sh\nprintf invoked > ${JSON.stringify(sentinel)}\nexit 1\n`, { mode: 0o755 });
    const args = ["--auth-type", "anthropic", "-p", "hello"];
    const out = await runCli(["qwen", ...args], {
      ...fx.env,
      CAVEMAN_OFFLINE: "1",
      CAVEMAN_PROXY_BIN: proxy,
    });
    assert.equal(out.code, 0, out.stderr);
    assert.deepEqual(JSON.parse(out.stdout).argv, args);
    assert.equal(existsSync(sentinel), false, "direct Qwen route override must not start the proxy binary");
    withEnv({
      HOME: fx.env.HOME,
      CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
      QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
    }, () => {
      assert.throws(
        () => buildWrapEnv(qwen, "http://127.0.0.1:8787", "auto", args),
        /Qwen --auth-type selects an auth provider outside Caveman's routed profile/,
      );
    });
  } finally {
    fx.cleanup();
  }
});

test("Qwen enforced auth policy conflicts launch direct without profile injection", async (t) => {
  for (const fixture of [
    { name: "foreign provider", enforcedType: "qwen-oauth" },
    { name: "malformed provider", enforcedType: { provider: "openai" } },
  ]) {
    await t.test(fixture.name, async () => {
      const fx = qwenFixture();
      const proxy = join(fx.root, "caveman-proxy-sentinel");
      const sentinel = join(fx.root, "proxy-was-started");
      try {
        const system = JSON.parse(fx.systemBytes);
        system.security.auth = { enforcedType: fixture.enforcedType };
        writeFileSync(fx.systemConfig, JSON.stringify(system, null, 2) + "\n");
        writeFileSync(join(fx.env.HOME, ".caveman-cloud", "config.json"), JSON.stringify({
          wrap: { proxy: true, shrink: false, mcp: false, browse: false },
        }));
        writeFileSync(proxy, `#!/bin/sh\nprintf invoked > ${JSON.stringify(sentinel)}\nexit 1\n`, { mode: 0o755 });

        const args = ["-p", "hello"];
        const out = await runCli(["qwen", ...args], {
          ...fx.env,
          CAVEMAN_OFFLINE: "1",
          CAVEMAN_PROXY_BIN: proxy,
        });
        assert.equal(out.code, 0, out.stderr);
        const child = JSON.parse(out.stdout);
        assert.deepEqual(child.argv, args);
        assert.deepEqual(child.config.security.auth.enforcedType, fixture.enforcedType);
        assert.equal(child.config.modelProviders.openai, undefined);
        assert.equal(child.webSearchEnabled, "");
        assert.equal(child.workflowsDisabled, "");
        assert.equal(existsSync(sentinel), false);
        assert.match(out.stderr, /Qwen enforced auth policy requires a provider outside Caveman's routed profile; launching directly/);

        withEnv({
          HOME: fx.env.HOME,
          CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
          QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfig,
        }, () => {
          assert.throws(
            () => buildWrapEnv(qwen, "http://127.0.0.1:8787", "auto", qwen.args),
            /Qwen enforced auth policy requires a provider outside Caveman's routed profile/,
          );
        });
      } finally {
        fx.cleanup();
      }
    });
  }
});

test("Qwen MCP install is idempotent, marker-owned, upgradeable, and reversible", async () => {
  const fx = qwenMcpFixture();
  try {
    const sibling = { command: "sibling-mcp", args: ["serve"] };
    writeFileSync(fx.configPath, JSON.stringify({ general: { vimMode: true }, mcpServers: { sibling } }, null, 2) + "\n", { mode: 0o640 });

    withEnv({
      HOME: fx.env.HOME,
      CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
      QWEN_HOME: fx.env.QWEN_HOME,
      QWEN_CODE_SYSTEM_SETTINGS_PATH: join(fx.root, "missing-system-settings.json"),
      QWEN_CODE_SYSTEM_DEFAULTS_PATH: fx.env.QWEN_CODE_SYSTEM_DEFAULTS_PATH,
      QWEN_CODE_LEGACY_MCP_BLOCKING: "0",
    }, () => {
      assert.equal(buildWrapEnv(qwen).QWEN_CODE_LEGACY_MCP_BLOCKING, "0");
    });

    const installed = await runCli(["mcp", "install", "qwen"], fx.env);
    assert.equal(installed.code, 0, installed.stderr);
    const firstBytes = readFileSync(fx.configPath, "utf8");
    const first = JSON.parse(firstBytes);
    assert.equal(statSync(fx.configPath).mode & 0o777, 0o640);
    assert.equal(first.general.vimMode, true);
    assert.deepEqual(first.mcpServers.sibling, sibling);
    assert.deepEqual(first.mcpServers.caveman, { command: fx.mcpV1, args: [] });
    assert.deepEqual(JSON.parse(readFileSync(fx.markerPath, "utf8")), ownedMarker(fx.configPath, fx.mcpV1));
    withEnv({
      HOME: fx.env.HOME,
      CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
      QWEN_HOME: fx.env.QWEN_HOME,
      QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfigPath,
      QWEN_CODE_SYSTEM_DEFAULTS_PATH: fx.env.QWEN_CODE_SYSTEM_DEFAULTS_PATH,
      QWEN_CODE_LEGACY_MCP_BLOCKING: "0",
    }, () => {
      const automatic = buildWrapEnv(qwen, "http://127.0.0.1:8787", "auto");
      assert.equal(automatic.QWEN_CODE_LEGACY_MCP_BLOCKING, "1");
      assert.deepEqual(readInjected(automatic).config.mcpServers.caveman, { command: fx.mcpV1, args: [] });
      for (const mode of ["marker-only", "off"]) {
        const suppressed = buildWrapEnv(qwen, "http://127.0.0.1:8787", mode);
        assert.equal(suppressed.QWEN_CODE_LEGACY_MCP_BLOCKING, "1");
        const config = readInjected(suppressed).config;
        assert.equal(config.mcpServers?.caveman, undefined, `${mode} must not project Qwen MCP into the temporary config`);
        assert.equal(config.mcp?.excluded?.includes("caveman") ?? false, false, `${mode} must not inject Qwen MCP exclusion policy`);
      }
      assert.equal(readFileSync(fx.configPath, "utf8"), firstBytes);
    });

    const repeated = await runCli(["mcp", "install", "qwen"], fx.env);
    assert.equal(repeated.code, 0, repeated.stderr);
    assert.equal(readFileSync(fx.configPath, "utf8"), firstBytes);

    const upgraded = await runCli(["mcp", "install", "qwen"], { ...fx.env, CAVEMAN_MCP_BIN: fx.mcpV2 });
    assert.equal(upgraded.code, 0, upgraded.stderr);
    assert.deepEqual(JSON.parse(readFileSync(fx.configPath, "utf8")).mcpServers.caveman, {
      command: fx.mcpV2,
      args: [],
    });
    assert.equal(JSON.parse(readFileSync(fx.markerPath, "utf8")).command, fx.mcpV2);

    const removed = await runCli(["mcp", "uninstall", "qwen"], fx.env);
    assert.equal(removed.code, 0, removed.stderr);
    const final = JSON.parse(readFileSync(fx.configPath, "utf8"));
    assert.equal(final.general.vimMode, true);
    assert.deepEqual(final.mcpServers, { sibling });
    assert.equal(statSync(fx.configPath).mode & 0o777, 0o640);
    assert.equal(existsSync(fx.markerPath), false);
  } finally {
    fx.cleanup();
  }
});

test("Qwen MCP config and ownership journal commit together", async (t) => {
  await t.test("unusable journal storage blocks native config mutation", async () => {
    const fx = qwenMcpFixture();
    try {
      const source = JSON.stringify({ general: { vimMode: true } }, null, 2) + "\n";
      writeFileSync(fx.configPath, source);
      const blockedHome = join(fx.root, "caveman-home-is-a-file");
      writeFileSync(blockedHome, "not a directory\n");

      const installed = await runCli(["mcp", "install", "qwen"], { ...fx.env, CAVEMAN_HOME: blockedHome });
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /cannot persist qwen caveman MCP ownership journal/);
      assert.doesNotMatch(installed.stderr, /caveman_retrieve installed/);
      assert.equal(readFileSync(fx.configPath, "utf8"), source);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("foreign marker target blocks before native config mutation", async () => {
    const fx = qwenMcpFixture();
    try {
      const source = JSON.stringify({ general: { vimMode: true } }, null, 2) + "\n";
      writeFileSync(fx.configPath, source, { mode: 0o640 });
      const originalMode = statSync(fx.configPath).mode & 0o777;
      mkdirSync(fx.markerPath, { recursive: true });

      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /EISDIR|directory/);
      assert.doesNotMatch(installed.stderr, /caveman_retrieve installed/);
      assert.equal(readFileSync(fx.configPath, "utf8"), source);
      assert.equal(statSync(fx.configPath).mode & 0o777, originalMode);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("dangling config symlink fails closed without replacing topology", async () => {
    const fx = qwenMcpFixture();
    try {
      symlinkSync(join(fx.root, "missing-settings-target.json"), fx.configPath);
      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /contains dangling symlink/);
      assert.equal(lstatSync(fx.configPath).isSymbolicLink(), true);
      assert.equal(existsSync(fx.markerPath), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("ownership marker symlink fails closed", async () => {
    const fx = qwenMcpFixture();
    try {
      const source = "{}\n";
      const markerTarget = join(fx.root, "external-marker.json");
      writeFileSync(fx.configPath, source, { mode: 0o600 });
      writeFileSync(markerTarget, "external\n", { mode: 0o600 });
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      symlinkSync(markerTarget, fx.markerPath);

      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /is a symlink; refusing transactional ownership mutation/);
      assert.equal(readFileSync(fx.configPath, "utf8"), source);
      assert.equal(readFileSync(markerTarget, "utf8"), "external\n");
      assert.equal(lstatSync(fx.markerPath).isSymbolicLink(), true);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("interrupted config commit rolls back then retries cleanly", async () => {
    const fx = qwenMcpFixture();
    try {
      const before = Buffer.from(JSON.stringify({ general: { vimMode: true } }, null, 2) + "\n");
      const after = Buffer.from(JSON.stringify({
        general: { vimMode: true },
        mcpServers: { caveman: { command: fx.mcpV1, args: [] } },
      }, null, 2) + "\n");
      const markerAfter = ownedMarkerBytes(fx.configPath, fx.mcpV1);
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      writeFileSync(fx.configPath, after, { mode: 0o600 });
      writePendingMcpInstall("qwen", fx.markerPath, fx.configPath, before, after, markerAfter);

      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      assert.match(installed.stderr, /rolled back interrupted qwen caveman MCP transaction/);
      assert.equal(readFileSync(fx.configPath, "utf8"), after.toString("utf8"));
      assert.equal(readFileSync(fx.markerPath, "utf8"), markerAfter.toString("utf8"));
      assert.equal(existsSync(`${fx.markerPath}.pending`), false);
      assert.equal(existsSync(configPendingPath(fx.configPath)), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("recovery stays bound to original QWEN_HOME", async () => {
    const fx = qwenMcpFixture();
    try {
      const before = Buffer.from(JSON.stringify({ general: { vimMode: true } }, null, 2) + "\n");
      const after = Buffer.from(JSON.stringify({
        general: { vimMode: true },
        mcpServers: { caveman: { command: fx.mcpV1, args: [] } },
      }, null, 2) + "\n");
      const markerAfter = ownedMarkerBytes(fx.configPath, fx.mcpV1);
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      writeFileSync(fx.configPath, after, { mode: 0o600 });
      writePendingMcpInstall("qwen", fx.markerPath, fx.configPath, before, after, markerAfter);
      const shiftedHome = join(fx.root, "shifted-qwen-home");
      const shiftedConfig = join(shiftedHome, "settings.json");

      const installed = await runCli(["mcp", "install", "qwen"], { ...fx.env, QWEN_HOME: shiftedHome });
      assert.equal(installed.code, 0, installed.stderr);
      assert.match(installed.stderr, /rolled back interrupted qwen caveman MCP transaction/);
      assert.equal(readFileSync(fx.configPath, "utf8"), before.toString("utf8"));
      assert.deepEqual(JSON.parse(readFileSync(shiftedConfig, "utf8")).mcpServers.caveman, {
        command: fx.mcpV1,
        args: [],
      });
      assert.deepEqual(JSON.parse(readFileSync(fx.markerPath, "utf8")), ownedMarker(shiftedConfig, fx.mcpV1));
      assert.equal(existsSync(`${fx.markerPath}.pending`), false);
      assert.equal(existsSync(configPendingPath(fx.configPath)), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("foreign edit blocks interrupted-transaction rollback", async () => {
    const fx = qwenMcpFixture();
    try {
      const before = Buffer.from(JSON.stringify({ general: { vimMode: true } }, null, 2) + "\n");
      const after = Buffer.from(JSON.stringify({
        general: { vimMode: true },
        mcpServers: { caveman: { command: fx.mcpV1, args: [] } },
      }, null, 2) + "\n");
      const foreign = Buffer.from(JSON.stringify({ general: { vimMode: false } }, null, 2) + "\n");
      const markerAfter = ownedMarkerBytes(fx.configPath, fx.mcpV1);
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      writeFileSync(fx.configPath, foreign);
      writePendingMcpInstall("qwen", fx.markerPath, fx.configPath, before, after, markerAfter);

      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /changed during interrupted transaction; refusing destructive recovery/);
      assert.equal(readFileSync(fx.configPath, "utf8"), foreign.toString("utf8"));
      assert.equal(existsSync(`${fx.markerPath}.pending`), true);
      assert.equal(existsSync(configPendingPath(fx.configPath)), true);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("marker-only precommit intent is discarded before retry", async () => {
    const fx = qwenMcpFixture();
    try {
      const before = Buffer.from("{}\n");
      const after = Buffer.from(JSON.stringify({
        mcpServers: { caveman: { command: fx.mcpV1, args: [] } },
      }, null, 2) + "\n");
      const markerAfter = ownedMarkerBytes(fx.configPath, fx.mcpV1);
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      writeFileSync(fx.configPath, before, { mode: 0o600 });
      writePendingMcpInstall("qwen", fx.markerPath, fx.configPath, before, after, markerAfter);
      rmSync(configPendingPath(fx.configPath));

      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      assert.match(installed.stderr, /discarded uncommitted qwen caveman MCP transaction/);
      assert.equal(readFileSync(fx.configPath, "utf8"), after.toString("utf8"));
      assert.equal(existsSync(`${fx.markerPath}.pending`), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("mismatched durable journal copies fail closed", async () => {
    const fx = qwenMcpFixture();
    try {
      const before = Buffer.from("{}\n");
      const after = Buffer.from(JSON.stringify({
        mcpServers: { caveman: { command: fx.mcpV1, args: [] } },
      }, null, 2) + "\n");
      const markerAfter = ownedMarkerBytes(fx.configPath, fx.mcpV1);
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      writeFileSync(fx.configPath, after, { mode: 0o600 });
      writePendingMcpInstall("qwen", fx.markerPath, fx.configPath, before, after, markerAfter);
      const scopedPath = configPendingPath(fx.configPath);
      const scoped = JSON.parse(readFileSync(scopedPath, "utf8"));
      scoped.transaction_id = "00000000-0000-4000-8000-000000000099";
      writeFileSync(scopedPath, JSON.stringify(scoped, null, 2) + "\n", { mode: 0o600 });

      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /transaction journals disagree; refusing recovery/);
      assert.equal(readFileSync(fx.configPath, "utf8"), after.toString("utf8"));
      assert.equal(existsSync(fx.markerPath), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("completed transaction with locator cleanup lag finalizes safely", async () => {
    const fx = qwenMcpFixture();
    try {
      const before = Buffer.from("{}\n");
      const after = Buffer.from(JSON.stringify({
        mcpServers: { caveman: { command: fx.mcpV1, args: [] } },
      }, null, 2) + "\n");
      const markerAfter = ownedMarkerBytes(fx.configPath, fx.mcpV1);
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      writeFileSync(fx.configPath, after, { mode: 0o600 });
      writeFileSync(fx.markerPath, markerAfter, { mode: 0o600 });
      writePendingMcpInstall("qwen", fx.markerPath, fx.configPath, before, after, markerAfter);
      rmSync(configPendingPath(fx.configPath));

      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      assert.match(installed.stderr, /finalized interrupted qwen caveman MCP transaction/);
      assert.equal(readFileSync(fx.configPath, "utf8"), after.toString("utf8"));
      assert.equal(readFileSync(fx.markerPath, "utf8"), markerAfter.toString("utf8"));
      assert.equal(existsSync(`${fx.markerPath}.pending`), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("malformed pending journal fails closed", async () => {
    const fx = qwenMcpFixture();
    try {
      const source = JSON.stringify({ general: { vimMode: true } }, null, 2) + "\n";
      writeFileSync(fx.configPath, source);
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      writeFileSync(`${fx.markerPath}.pending`, "{}\n", { mode: 0o600 });

      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /pending qwen caveman MCP transaction is malformed/);
      assert.equal(readFileSync(fx.configPath, "utf8"), source);
      assert.equal(existsSync(fx.markerPath), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("interrupted uninstall restores ownership before retrying", async () => {
    const fx = qwenMcpFixture();
    try {
      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      const configBefore = readFileSync(fx.configPath);
      const markerBefore = readFileSync(fx.markerPath);
      const configAfter = Buffer.from("{}\n");
      rmSync(fx.markerPath);
      writePendingMcpUninstall("qwen", fx.markerPath, fx.configPath, configBefore, configAfter, markerBefore);

      const removed = await runCli(["mcp", "uninstall"], fx.env);
      assert.equal(removed.code, 0, removed.stderr);
      assert.match(removed.stderr, /rolled back interrupted qwen caveman MCP transaction/);
      assert.equal(readFileSync(fx.configPath, "utf8"), configAfter.toString("utf8"));
      assert.equal(existsSync(fx.markerPath), false);
      assert.equal(existsSync(`${fx.markerPath}.pending`), false);
      assert.equal(existsSync(configPendingPath(fx.configPath)), false);
    } finally {
      fx.cleanup();
    }
  });
});

test("Qwen MCP config lock is shared across CAVEMAN_HOME values", async () => {
  const fx = qwenMcpFixture();
  try {
    const caveOne = join(fx.root, "caveman-one");
    const caveTwo = join(fx.root, "caveman-two");
    const [one, two] = await Promise.all([
      runCli(["mcp", "install", "qwen"], { ...fx.env, CAVEMAN_HOME: caveOne, CAVEMAN_MCP_BIN: fx.mcpV1 }),
      runCli(["mcp", "install", "qwen"], { ...fx.env, CAVEMAN_HOME: caveTwo, CAVEMAN_MCP_BIN: fx.mcpV2 }),
    ]);
    const winners = [
      { out: one, marker: join(caveOne, "mcp", "qwen.json"), command: fx.mcpV1 },
      { out: two, marker: join(caveTwo, "mcp", "qwen.json"), command: fx.mcpV2 },
    ].filter((item) => /caveman_retrieve installed/.test(item.out.stderr));
    assert.equal(winners.length, 1, `writer results:\n${one.stderr}\n${two.stderr}`);
    const winner = winners[0];
    const loser = winner.command === fx.mcpV1
      ? { out: two, marker: join(caveTwo, "mcp", "qwen.json") }
      : { out: one, marker: join(caveOne, "mcp", "qwen.json") };
    assert.equal(JSON.parse(readFileSync(fx.configPath, "utf8")).mcpServers.caveman.command, winner.command);
    assert.equal(JSON.parse(readFileSync(winner.marker, "utf8")).command, winner.command);
    assert.equal(existsSync(loser.marker), false);
    assert.match(loser.out.stderr, /MCP config change already running|not Caveman-journaled/);
  } finally {
    fx.cleanup();
  }
});

test("Qwen marker lock serializes distinct QWEN_HOME targets in one CAVEMAN_HOME", async () => {
  const fx = qwenMcpFixture();
  try {
    const homeOne = fx.env.QWEN_HOME;
    const homeTwo = join(fx.root, "second-qwen-home");
    const configOne = join(homeOne, "settings.json");
    const configTwo = join(homeTwo, "settings.json");
    const [one, two] = await Promise.all([
      runCli(["mcp", "install", "qwen"], { ...fx.env, QWEN_HOME: homeOne, CAVEMAN_MCP_BIN: fx.mcpV1 }),
      runCli(["mcp", "install", "qwen"], { ...fx.env, QWEN_HOME: homeTwo, CAVEMAN_MCP_BIN: fx.mcpV2 }),
    ]);
    const installed = [
      { out: one, config: configOne, other: configTwo, command: fx.mcpV1 },
      { out: two, config: configTwo, other: configOne, command: fx.mcpV2 },
    ].filter(({ out }) => /caveman_retrieve installed/.test(out.stderr));
    assert.equal(installed.length, 1, `writer results:\n${one.stderr}\n${two.stderr}`);
    const winner = installed[0];
    assert.equal(JSON.parse(readFileSync(winner.config, "utf8")).mcpServers.caveman.command, winner.command);
    assert.equal(existsSync(winner.other), false);
    assert.deepEqual(JSON.parse(readFileSync(fx.markerPath, "utf8")), ownedMarker(winner.config, winner.command));
  } finally {
    fx.cleanup();
  }
});

test("Qwen MCP lock reclamation never deletes malformed foreign state", async (t) => {
  await t.test("old malformed lock stays untouched", async () => {
    const fx = qwenMcpFixture();
    try {
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      const lock = mcpLockPath(fx.markerPath);
      const sentinel = join(lock, "important.txt");
      mkdirSync(lock, { mode: 0o700 });
      writeFileSync(join(lock, "owner.json"), "{}\n", { mode: 0o600 });
      writeFileSync(sentinel, "keep\n", { mode: 0o600 });
      const old = new Date(Date.now() - 60_000);
      utimesSync(lock, old, old);

      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 1, installed.stderr);
      assert.match(installed.stderr, /MCP config change already running/);
      assert.equal(readFileSync(sentinel, "utf8"), "keep\n");
      assert.equal(existsSync(fx.markerPath), false);
      assert.equal(existsSync(fx.configPath), false);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("exact dead-owner lock is reclaimed", async () => {
    const fx = qwenMcpFixture();
    try {
      mkdirSync(dirname(fx.markerPath), { recursive: true });
      const canonicalMarker = canonicalPath(fx.markerPath);
      const lock = mcpLockPath(fx.markerPath);
      mkdirSync(lock, { mode: 0o700 });
      writeFileSync(join(lock, "owner.json"), JSON.stringify({
        schema_version: 1,
        pid: 2147483647,
        token: "00000000-0000-4000-8000-000000000077",
        config_path: canonicalMarker,
        started_at: "2026-01-01T00:00:00.000Z",
      }) + "\n", { mode: 0o600 });

      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      assert.match(installed.stderr, /reclaimed stale MCP config lock/);
      assert.equal(JSON.parse(readFileSync(fx.markerPath, "utf8")).config_path, canonicalPath(fx.configPath));
    } finally {
      fx.cleanup();
    }
  });
});

test("Qwen config-scoped recovery crosses CAVEMAN_HOME boundaries", async () => {
  const fx = qwenMcpFixture();
  try {
    const caveOne = join(fx.root, "caveman-one");
    const caveTwo = join(fx.root, "caveman-two");
    const markerOne = join(caveOne, "mcp", "qwen.json");
    const markerTwo = join(caveTwo, "mcp", "qwen.json");
    const before = Buffer.from("{}\n");
    const after = Buffer.from(JSON.stringify({
      mcpServers: { caveman: { command: fx.mcpV1, args: [] } },
    }, null, 2) + "\n");
    const markerAfter = ownedMarkerBytes(fx.configPath, fx.mcpV1);
    mkdirSync(dirname(markerOne), { recursive: true });
    writeFileSync(fx.configPath, after, { mode: 0o600 });
    writePendingMcpInstall("qwen", markerOne, fx.configPath, before, after, markerAfter);

    const installed = await runCli(["mcp", "install", "qwen"], { ...fx.env, CAVEMAN_HOME: caveTwo });
    assert.equal(installed.code, 0, installed.stderr);
    assert.match(installed.stderr, /rolled back interrupted qwen caveman MCP transaction/);
    assert.equal(existsSync(`${markerOne}.pending`), false);
    assert.equal(existsSync(markerOne), false);
    assert.deepEqual(JSON.parse(readFileSync(markerTwo, "utf8")), ownedMarker(fx.configPath, fx.mcpV1));
    assert.equal(readFileSync(fx.configPath, "utf8"), after.toString("utf8"));
  } finally {
    fx.cleanup();
  }
});

test("Qwen ownership remains reversible after QWEN_HOME relocation", async () => {
  const fx = qwenMcpFixture();
  try {
    writeFileSync(fx.configPath, "{}\n", { mode: 0o640 });
    const installed = await runCli(["mcp", "install", "qwen"], fx.env);
    assert.equal(installed.code, 0, installed.stderr);
    assert.equal(JSON.parse(readFileSync(fx.markerPath, "utf8")).config_path, canonicalPath(fx.configPath));

    const shiftedHome = join(fx.root, "shifted-qwen-home");
    const shiftedConfig = join(shiftedHome, "settings.json");
    const shiftedEnv = { ...fx.env, QWEN_HOME: shiftedHome };
    const refused = await runCli(["mcp", "install", "qwen"], shiftedEnv);
    assert.equal(refused.code, 0, refused.stderr);
    assert.match(refused.stderr, /uninstall it before installing/);
    assert.equal(existsSync(shiftedConfig), false);

    const removed = await runCli(["mcp", "uninstall", "qwen"], shiftedEnv);
    assert.equal(removed.code, 0, removed.stderr);
    assert.equal(JSON.parse(readFileSync(fx.configPath, "utf8")).mcpServers, undefined);
    assert.equal(statSync(fx.configPath).mode & 0o777, 0o640);
    assert.equal(existsSync(fx.markerPath), false);

    const reinstalled = await runCli(["mcp", "install", "qwen"], shiftedEnv);
    assert.equal(reinstalled.code, 0, reinstalled.stderr);
    assert.deepEqual(JSON.parse(readFileSync(fx.markerPath, "utf8")), ownedMarker(shiftedConfig, fx.mcpV1));
    assert.equal(JSON.parse(readFileSync(shiftedConfig, "utf8")).mcpServers.caveman.command, fx.mcpV1);
  } finally {
    fx.cleanup();
  }
});

test("Qwen ownership marker survives physical config relocation", async () => {
  const fx = qwenMcpFixture();
  try {
    const installed = await runCli(["mcp", "install", "qwen"], fx.env);
    assert.equal(installed.code, 0, installed.stderr);
    const shiftedHome = join(fx.root, "physically-moved-qwen-home");
    const shiftedConfig = join(shiftedHome, "settings.json");
    mkdirSync(shiftedHome, { recursive: true });
    renameSync(fx.configPath, shiftedConfig);
    const shiftedEnv = { ...fx.env, QWEN_HOME: shiftedHome };

    const removed = await runCli(["mcp", "uninstall", "qwen"], shiftedEnv);
    assert.equal(removed.code, 0, removed.stderr);
    assert.match(removed.stderr, /contains moved caveman state; retaining ownership marker/);
    assert.equal(existsSync(fx.markerPath), true);
    assert.equal(JSON.parse(readFileSync(shiftedConfig, "utf8")).mcpServers.caveman.command, fx.mcpV1);

    const refused = await runCli(["mcp", "install", "qwen"], shiftedEnv);
    assert.equal(refused.code, 0, refused.stderr);
    assert.match(refused.stderr, /uninstall it before installing/);
    assert.equal(existsSync(fx.markerPath), true);
  } finally {
    fx.cleanup();
  }
});

test("Qwen legacy ownership migrates only from matching active config", async () => {
  const fx = qwenMcpFixture();
  try {
    const legacyMarker = { tool: "caveman_retrieve", command: fx.mcpV1, args: [] };
    const config = { mcpServers: { caveman: { command: fx.mcpV1, args: [] } } };
    mkdirSync(dirname(fx.markerPath), { recursive: true });
    writeFileSync(fx.configPath, JSON.stringify(config, null, 2) + "\n", { mode: 0o600 });
    writeFileSync(fx.markerPath, JSON.stringify(legacyMarker, null, 2) + "\n", { mode: 0o600 });

    withEnv(fx.env, () => {
      const wrapped = buildWrapEnv(qwen, "http://127.0.0.1:8787", "auto");
      assert.deepEqual(readInjected(wrapped).config.mcpServers.caveman, config.mcpServers.caveman);
    });
    const migrated = await runCli(["mcp", "install", "qwen"], fx.env);
    assert.equal(migrated.code, 0, migrated.stderr);
    assert.deepEqual(JSON.parse(readFileSync(fx.markerPath, "utf8")), ownedMarker(fx.configPath, fx.mcpV1));

    writeFileSync(fx.markerPath, JSON.stringify(legacyMarker, null, 2) + "\n", { mode: 0o600 });
    const shiftedConfig = join(fx.root, "legacy-shifted", "settings.json");
    const shiftedEnv = { ...fx.env, QWEN_HOME: dirname(shiftedConfig) };
    const refusedInstall = await runCli(["mcp", "install", "qwen"], shiftedEnv);
    assert.equal(refusedInstall.code, 0, refusedInstall.stderr);
    assert.match(refusedInstall.stderr, /legacy caveman ownership marker does not identify its Qwen config; refusing relocation/);
    const refusedRemove = await runCli(["mcp", "uninstall", "qwen"], shiftedEnv);
    assert.equal(refusedRemove.code, 0, refusedRemove.stderr);
    assert.match(refusedRemove.stderr, /legacy caveman ownership marker does not identify its Qwen config; refusing removal/);
    assert.equal(existsSync(fx.markerPath), true);
    assert.deepEqual(JSON.parse(readFileSync(fx.configPath, "utf8")), config);
    assert.equal(existsSync(shiftedConfig), false);
  } finally {
    fx.cleanup();
  }
});

test("Qwen MCP refuses conflicting or user-modified entries", async (t) => {
  await t.test("unowned conflict", async () => {
    const fx = qwenMcpFixture();
    try {
      const source = JSON.stringify({
        mcpServers: { caveman: { command: "user-owned-mcp", args: [] } },
      }, null, 2) + "\n";
      writeFileSync(fx.configPath, source);
      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      assert.match(installed.stderr, /not Caveman-journaled; refusing overwrite/);
      assert.equal(readFileSync(fx.configPath, "utf8"), source);
      assert.equal(existsSync(fx.markerPath), false);

      const removed = await runCli(["mcp", "uninstall", "qwen"], fx.env);
      assert.equal(removed.code, 0, removed.stderr);
      assert.match(removed.stderr, /not Caveman-journaled; refusing removal/);
      assert.equal(readFileSync(fx.configPath, "utf8"), source);
    } finally {
      fx.cleanup();
    }
  });

  await t.test("owned entry changed by user", async () => {
    const fx = qwenMcpFixture();
    try {
      const installed = await runCli(["mcp", "install", "qwen"], fx.env);
      assert.equal(installed.code, 0, installed.stderr);
      const config = JSON.parse(readFileSync(fx.configPath, "utf8"));
      config.mcpServers.caveman.command = "user-modified-mcp";
      const modified = JSON.stringify(config, null, 2) + "\n";
      writeFileSync(fx.configPath, modified);

      const removed = await runCli(["mcp", "uninstall", "qwen"], fx.env);
      assert.equal(removed.code, 0, removed.stderr);
      assert.match(removed.stderr, /changed since Caveman installed it; refusing removal/);
      assert.equal(readFileSync(fx.configPath, "utf8"), modified);
      assert.equal(existsSync(fx.markerPath), true);
      withEnv({
        HOME: fx.env.HOME,
        CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
        QWEN_HOME: fx.env.QWEN_HOME,
        QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfigPath,
        QWEN_CODE_SYSTEM_DEFAULTS_PATH: fx.env.QWEN_CODE_SYSTEM_DEFAULTS_PATH,
        QWEN_CODE_LEGACY_MCP_BLOCKING: "0",
      }, () => {
        const wrapped = buildWrapEnv(qwen);
        assert.equal(wrapped.QWEN_CODE_LEGACY_MCP_BLOCKING, "0");
        const effective = readInjected(wrapped).config;
        assert.equal(effective.mcpServers?.caveman, undefined);
        assert.ok(effective.mcp.excluded.includes("caveman"));
      });
    } finally {
      fx.cleanup();
    }
  });
});

test("Qwen wrap fails closed for stale, shadowed, filtered, and CLI-replaced MCP registrations", async (t) => {
  const cases = [
    {
      name: "deleted native entry",
      mutate: (fx) => writeFileSync(fx.configPath, "{}\n"),
    },
    {
      name: "wrong marker tool",
      mutate: (fx) => {
        const marker = JSON.parse(readFileSync(fx.markerPath, "utf8"));
        marker.tool = "different_tool";
        writeFileSync(fx.markerPath, JSON.stringify(marker, null, 2) + "\n");
      },
    },
    {
      name: "enterprise server shadow",
      mutate: (fx) => writeFileSync(fx.systemConfigPath, JSON.stringify({
        mcpServers: { caveman: { command: "enterprise-owned", args: [] } },
      })),
    },
    {
      name: "enterprise exclusion",
      mutate: (fx) => writeFileSync(fx.systemConfigPath, JSON.stringify({ mcp: { excluded: ["cave*"] } })),
    },
    {
      name: "enterprise allowlist omission",
      mutate: (fx) => writeFileSync(fx.systemConfigPath, JSON.stringify({ mcp: { allowed: ["approved-only"] } })),
    },
    {
      name: "enterprise permission denial",
      mutate: (fx) => writeFileSync(fx.systemConfigPath, JSON.stringify({ permissions: { deny: ["mcp__caveman"] } })),
    },
    {
      name: "enterprise empty-call permission denial",
      mutate: (fx) => writeFileSync(fx.systemConfigPath, JSON.stringify({ permissions: { deny: ["mcp__caveman__caveman_retrieve()"] } })),
    },
    {
      name: "enterprise wildcard permission denial",
      mutate: (fx) => writeFileSync(fx.systemConfigPath, JSON.stringify({ permissions: { deny: ["mcp__cave*"] } })),
    },
    {
      name: "enterprise disabled tool",
      mutate: (fx) => writeFileSync(fx.systemConfigPath, JSON.stringify({ tools: { disabled: ["mcp__caveman__caveman_retrieve"] } })),
    },
    {
      name: "unresolved enterprise deny variable",
      mutate: (fx) => writeFileSync(fx.systemConfigPath, JSON.stringify({ permissions: { deny: ["$QWEN_DENY_RECOVERY"] } })),
    },
  ];

  for (const fixture of cases) {
    await t.test(fixture.name, async () => {
      const fx = qwenMcpFixture();
      try {
        const installed = await runCli(["mcp", "install", "qwen"], fx.env);
        assert.equal(installed.code, 0, installed.stderr);
        fixture.mutate(fx);
        withEnv({
          HOME: fx.env.HOME,
          CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
          QWEN_HOME: fx.env.QWEN_HOME,
          QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfigPath,
          QWEN_CODE_SYSTEM_DEFAULTS_PATH: fx.env.QWEN_CODE_SYSTEM_DEFAULTS_PATH,
          QWEN_CODE_LEGACY_MCP_BLOCKING: "0",
          QWEN_CODE_SAFE_MODE: undefined,
          QWEN_CODE_SIMPLE: undefined,
          QWEN_DENY_RECOVERY: undefined,
        }, () => {
          const wrapped = buildWrapEnv(qwen);
          assert.equal(wrapped.QWEN_CODE_LEGACY_MCP_BLOCKING, "0");
          const effective = readInjected(wrapped).config;
          assert.notDeepEqual(effective.mcpServers?.caveman, { command: fx.mcpV1, args: [] });
          assert.ok(effective.mcp.excluded.includes("caveman"));
        });
      } finally {
        fx.cleanup();
      }
    });
  }

  for (const blocked of [
    { name: "CLI MCP replacement", args: ["--mcp-config", "replacement.json"] },
    { name: "CLI excluded tool", args: ["--exclude-tools", "mcp__cave*"] },
    { name: "CLI excluded tool equals form", args: ["--exclude-tools=mcp*"] },
  ]) {
    await t.test(blocked.name, async () => {
      const fx = qwenMcpFixture();
      try {
        const installed = await runCli(["mcp", "install", "qwen"], fx.env);
        assert.equal(installed.code, 0, installed.stderr);
        withEnv({
          HOME: fx.env.HOME,
          CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
          QWEN_HOME: fx.env.QWEN_HOME,
          QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfigPath,
          QWEN_CODE_SYSTEM_DEFAULTS_PATH: fx.env.QWEN_CODE_SYSTEM_DEFAULTS_PATH,
          QWEN_CODE_LEGACY_MCP_BLOCKING: "0",
          QWEN_CODE_SAFE_MODE: undefined,
          QWEN_CODE_SIMPLE: undefined,
          ...blocked.env,
        }, () => {
          const wrapped = buildWrapEnv(qwen, "http://127.0.0.1:8787", "auto", blocked.args);
          assert.equal(wrapped.QWEN_CODE_LEGACY_MCP_BLOCKING, "0");
          const effective = readInjected(wrapped).config;
          assert.equal(effective.mcpServers?.caveman, undefined);
          assert.ok(effective.mcp.excluded.includes("caveman"));
        });
      } finally {
        fx.cleanup();
      }
    });
  }

  for (const blocked of [
    { name: "CLI bare mode", args: ["--bare=true"] },
    { name: "bare-mode environment", args: [], env: { QWEN_CODE_SIMPLE: "on" } },
  ]) {
    await t.test(blocked.name, async () => {
      const fx = qwenMcpFixture();
      try {
        const installed = await runCli(["mcp", "install", "qwen"], fx.env);
        assert.equal(installed.code, 0, installed.stderr);
        withEnv({
          HOME: fx.env.HOME,
          CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
          QWEN_HOME: fx.env.QWEN_HOME,
          QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfigPath,
          QWEN_CODE_SYSTEM_DEFAULTS_PATH: fx.env.QWEN_CODE_SYSTEM_DEFAULTS_PATH,
          QWEN_CODE_SAFE_MODE: undefined,
          QWEN_CODE_SIMPLE: undefined,
          ...blocked.env,
        }, () => {
          assert.throws(
            () => buildWrapEnv(qwen, "http://127.0.0.1:8787", "auto", blocked.args),
            /Qwen --bare ignores Caveman system settings/,
          );
        });
      } finally {
        fx.cleanup();
      }
    });
  }

  for (const blocked of [
    { name: "CLI safe mode", args: ["--safe-mode"] },
    { name: "safe-mode environment", args: [], env: { QWEN_CODE_SAFE_MODE: "yes" } },
    {
      name: "safe-mode settings environment",
      args: [],
      mutate: (fx) => writeFileSync(fx.systemConfigPath, JSON.stringify({ env: { QWEN_CODE_SAFE_MODE: "on" } })),
    },
  ]) {
    await t.test(blocked.name, async () => {
      const fx = qwenMcpFixture();
      try {
        const installed = await runCli(["mcp", "install", "qwen"], fx.env);
        assert.equal(installed.code, 0, installed.stderr);
        blocked.mutate?.(fx);
        withEnv({
          HOME: fx.env.HOME,
          CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
          QWEN_HOME: fx.env.QWEN_HOME,
          QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfigPath,
          QWEN_CODE_SYSTEM_DEFAULTS_PATH: fx.env.QWEN_CODE_SYSTEM_DEFAULTS_PATH,
          QWEN_CODE_SAFE_MODE: undefined,
          ...blocked.env,
        }, () => {
          assert.throws(
            () => buildWrapEnv(qwen, "http://127.0.0.1:8787", "auto", blocked.args),
            /Qwen safe mode ignores Caveman system settings/,
          );
        });
      } finally {
        fx.cleanup();
      }
    });
  }
});

test("Qwen policy parser preserves reachable MCP registrations", async (t) => {
  for (const allowed of [
    { name: "bare permission wildcard", settings: { permissions: { deny: ["*"] } } },
    { name: "nonempty permission specifier", settings: { permissions: { deny: ["mcp__caveman__caveman_retrieve(handle)"] } } },
    { name: "malformed permission specifier", settings: { permissions: { deny: ["mcp__caveman__caveman_retrieve)"] } } },
    { name: "disabled wildcard is exact-only", settings: { tools: { disabled: ["mcp__*"] } } },
    { name: "argument scan stops at separator", args: ["--", "--safe-mode", "--exclude-tools=mcp*"] },
    { name: "explicit safe false overrides environment", args: ["--safe-mode=false"], env: { QWEN_CODE_SAFE_MODE: "yes" } },
  ]) {
    await t.test(allowed.name, async () => {
      const fx = qwenMcpFixture();
      try {
        const installed = await runCli(["mcp", "install", "qwen"], fx.env);
        assert.equal(installed.code, 0, installed.stderr);
        if (allowed.settings) writeFileSync(fx.systemConfigPath, JSON.stringify(allowed.settings));
        withEnv({
          HOME: fx.env.HOME,
          CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
          QWEN_HOME: fx.env.QWEN_HOME,
          QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfigPath,
          QWEN_CODE_SYSTEM_DEFAULTS_PATH: fx.env.QWEN_CODE_SYSTEM_DEFAULTS_PATH,
          QWEN_CODE_LEGACY_MCP_BLOCKING: "0",
          QWEN_CODE_SAFE_MODE: undefined,
          QWEN_CODE_SIMPLE: undefined,
          ...allowed.env,
        }, () => {
          const wrapped = buildWrapEnv(qwen, "http://127.0.0.1:8787", "auto", allowed.args ?? []);
          assert.equal(wrapped.QWEN_CODE_LEGACY_MCP_BLOCKING, "1");
          assert.deepEqual(readInjected(wrapped).config.mcpServers.caveman, { command: fx.mcpV1, args: [] });
        });
      } finally {
        fx.cleanup();
      }
    });
  }
});

test("Qwen recovery follows QWEN_HOME and policy values loaded from dotenv", async () => {
  const fx = qwenMcpFixture();
  try {
    const installed = await runCli(["mcp", "install", "qwen"], fx.env);
    assert.equal(installed.code, 0, installed.stderr);
    mkdirSync(fx.env.HOME, { recursive: true });
    writeFileSync(join(fx.env.HOME, ".env"), `QWEN_HOME=${fx.env.QWEN_HOME}\n`);
    writeFileSync(join(fx.env.QWEN_HOME, ".env"), "QWEN_DENY_RECOVERY=mcp__caveman\n");
    writeFileSync(fx.systemConfigPath, JSON.stringify({ permissions: { deny: ["$QWEN_DENY_RECOVERY"] } }));
    withEnv({
      HOME: fx.env.HOME,
      CAVEMAN_HOME: fx.env.CAVEMAN_HOME,
      QWEN_HOME: undefined,
      QWEN_CODE_SYSTEM_SETTINGS_PATH: fx.systemConfigPath,
      QWEN_CODE_SYSTEM_DEFAULTS_PATH: fx.env.QWEN_CODE_SYSTEM_DEFAULTS_PATH,
      QWEN_DENY_RECOVERY: undefined,
      QWEN_CODE_LEGACY_MCP_BLOCKING: "0",
    }, () => {
      const wrapped = buildWrapEnv(qwen);
      assert.equal(wrapped.QWEN_CODE_LEGACY_MCP_BLOCKING, "0");
      const effective = readInjected(wrapped).config;
      assert.equal(effective.mcpServers?.caveman, undefined);
      assert.ok(effective.mcp.excluded.includes("caveman"));
    });
  } finally {
    fx.cleanup();
  }
});

test("Qwen MCP fails closed on malformed or incompatible settings", async (t) => {
  for (const fixture of [
    { name: "empty file", source: "", message: /settings\.json is empty; not modifying it/ },
    { name: "JSON with comments", source: "{\n  // keep comment\n  \"general\": {}\n}\n", message: /cannot read .*settings\.json/ },
    { name: "non-object mcpServers", source: "{\n  \"mcpServers\": []\n}\n", message: /mcpServers must be a JSON object/ },
  ]) {
    await t.test(fixture.name, async () => {
      const fx = qwenMcpFixture();
      try {
        writeFileSync(fx.configPath, fixture.source);
        const installed = await runCli(["mcp", "install", "qwen"], fx.env);
        assert.equal(installed.code, 0, installed.stderr);
        assert.match(installed.stderr, fixture.message);
        assert.equal(readFileSync(fx.configPath, "utf8"), fixture.source);
        assert.equal(existsSync(fx.markerPath), false);
      } finally {
        fx.cleanup();
      }
    });
  }
});
