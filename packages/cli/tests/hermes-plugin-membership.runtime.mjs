import test from "node:test";
import assert from "node:assert/strict";
import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { delimiter, join } from "node:path";
import { isolatedCliEnv, runCli } from "./_cli.mjs";

const isWindows = process.platform === "win32";

function hermesFixture(config) {
  const fixture = isolatedCliEnv();
  const bin = join(fixture.home, "bin");
  const hermesHome = join(fixture.home, "hermes");
  mkdirSync(hermesHome, { recursive: true });
  const hermesPath = join(bin, isWindows ? "hermes.cmd" : "hermes");
  writeFileSync(
    hermesPath,
    isWindows ? "@echo off\r\nexit /b 0\r\n" : "#!/bin/sh\nexit 0\n",
  );
  if (!isWindows) chmodSync(hermesPath, 0o755);
  const configPath = join(hermesHome, "config.yaml");
  writeFileSync(configPath, config);
  fixture.env.HERMES_HOME = hermesHome;
  fixture.env.PATH = `${bin}${delimiter}${fixture.env.PATH}`;
  return {
    fixture,
    hermesHome,
    configPath,
    markerPath: join(fixture.home, "hooks", "hermes.json"),
    pluginDir: join(hermesHome, "plugins", "caveman_shrink"),
  };
}

test("Hermes plugin membership uses exact enabled and disabled lists", async () => {
  const disabledConfigs = [
    "plugins:\n  \"enabled\": []\n  \"disabled\": [\"caveman_\\x73hrink\"]\n",
    "plugins:\n  enabled:\n    - caveman_shrink\n  disabled:\n    - \"caveman_shrink\"\n",
    "plugins:\n  enabled: [caveman_shrink]\n  disabled: [\"caveman_shrink\"]\n",
    "plugins:\n  enabled: []\n  disabled:\n    - caveman_shrink\n",
    "plugins:\n  'disabled':\n    - 'caveman_shrink'\n",
  ];
  for (const config of disabledConfigs) {
    const fx = hermesFixture(config);
    try {
      const result = await runCli(["tools", "hooks", "install", "hermes"], { env: fx.fixture.env });
      assert.equal(result.code, 1, result.stderr);
      assert.match(result.stderr, /explicitly disables caveman_shrink/);
      assert.doesNotMatch(result.stderr, /✓ Hermes Agent:/);
      assert.equal(existsSync(fx.markerPath), false);
      assert.equal(existsSync(fx.pluginDir), false);
      assert.equal(readFileSync(fx.configPath, "utf8"), config);
    } finally {
      fx.fixture.cleanup();
    }
  }

  const unsupportedEscapeConfig = "plugins:\n  enabled: []\n  \"disabled\": [\"caveman_\\q\"]\n";
  const unsupportedEscape = hermesFixture(unsupportedEscapeConfig);
  try {
    const result = await runCli(["tools", "hooks", "install", "hermes"], { env: unsupportedEscape.fixture.env });
    assert.equal(result.code, 1, result.stderr);
    assert.match(result.stderr, /unsupported plugins\.enabled or plugins\.disabled YAML form/);
    assert.equal(readFileSync(unsupportedEscape.configPath, "utf8"), unsupportedEscapeConfig);
    assert.equal(existsSync(unsupportedEscape.markerPath), false);
    assert.equal(existsSync(unsupportedEscape.pluginDir), false);
  } finally {
    unsupportedEscape.fixture.cleanup();
  }

  const unsupportedKeyEscapeConfig = "plugins:\n  enabled: []\n  \"dis\\u0061bled\": [caveman_shrink]\n";
  const unsupportedKeyEscape = hermesFixture(unsupportedKeyEscapeConfig);
  try {
    const result = await runCli(["tools", "hooks", "install", "hermes"], { env: unsupportedKeyEscape.fixture.env });
    assert.equal(result.code, 1, result.stderr);
    assert.match(result.stderr, /unsupported plugins\.enabled or plugins\.disabled YAML form/);
    assert.equal(readFileSync(unsupportedKeyEscape.configPath, "utf8"), unsupportedKeyEscapeConfig);
    assert.equal(existsSync(unsupportedKeyEscape.markerPath), false);
    assert.equal(existsSync(unsupportedKeyEscape.pluginDir), false);
  } finally {
    unsupportedKeyEscape.fixture.cleanup();
  }

  const malformedDisabledListConfig = "plugins:\n  enabled:\n    - caveman_shrink\n  disabled: [foreign_plugin\n";
  const malformedDisabledList = hermesFixture(malformedDisabledListConfig);
  try {
    const result = await runCli(["tools", "hooks", "install", "hermes"], { env: malformedDisabledList.fixture.env });
    assert.equal(result.code, 1, result.stderr);
    assert.match(result.stderr, /unsupported plugins\.enabled or plugins\.disabled YAML form/);
    assert.equal(readFileSync(malformedDisabledList.configPath, "utf8"), malformedDisabledListConfig);
    assert.equal(existsSync(malformedDisabledList.markerPath), false);
    assert.equal(existsSync(malformedDisabledList.pluginDir), false);
  } finally {
    malformedDisabledList.fixture.cleanup();
  }

  const unmatchedFlowDelimiterConfig = "plugins:\n  enabled:\n    - caveman_shrink\n  disabled: [foreign_plugin}]\n";
  const unmatchedFlowDelimiter = hermesFixture(unmatchedFlowDelimiterConfig);
  try {
    const result = await runCli(["tools", "hooks", "install", "hermes"], { env: unmatchedFlowDelimiter.fixture.env });
    assert.equal(result.code, 1, result.stderr);
    assert.match(result.stderr, /unsupported plugins\.enabled or plugins\.disabled YAML form/);
    assert.equal(readFileSync(unmatchedFlowDelimiter.configPath, "utf8"), unmatchedFlowDelimiterConfig);
    assert.equal(existsSync(unmatchedFlowDelimiter.markerPath), false);
    assert.equal(existsSync(unmatchedFlowDelimiter.pluginDir), false);
  } finally {
    unmatchedFlowDelimiter.fixture.cleanup();
  }

  const duplicateDisabledListConfig = "plugins:\n  enabled: []\n  disabled: []\n  'disabled':\n    - caveman_shrink\n";
  const duplicateDisabledList = hermesFixture(duplicateDisabledListConfig);
  try {
    const result = await runCli(["tools", "hooks", "install", "hermes"], { env: duplicateDisabledList.fixture.env });
    assert.equal(result.code, 1, result.stderr);
    assert.match(result.stderr, /unsupported plugins\.enabled or plugins\.disabled YAML form/);
    assert.equal(readFileSync(duplicateDisabledList.configPath, "utf8"), duplicateDisabledListConfig);
    assert.equal(existsSync(duplicateDisabledList.markerPath), false);
    assert.equal(existsSync(duplicateDisabledList.pluginDir), false);
  } finally {
    duplicateDisabledList.fixture.cleanup();
  }

  const wrongFieldConfig = "plugins:\n  enabled:\nother:\n  - caveman_shrink\n";
  const wrongField = hermesFixture(wrongFieldConfig);
  try {
    const result = await runCli(["tools", "hooks", "install", "hermes"], { env: wrongField.fixture.env });
    assert.equal(result.code, 0, result.stderr);
    const updated = readFileSync(wrongField.configPath, "utf8");
    const pluginsSection = updated.split(/^other:\s*$/m)[0];
    assert.match(pluginsSection, /^  enabled:\n    # >>> caveman:hermes-plugin-enable\n    - caveman_shrink\n    # <<< caveman:hermes-plugin-enable$/m);
    assert.equal(JSON.parse(readFileSync(wrongField.markerPath, "utf8")).installed, true);
  } finally {
    wrongField.fixture.cleanup();
  }

  const prefixConfig = "plugins:\n  enabled: [caveman_shrink_extra]\n";
  const prefix = hermesFixture(prefixConfig);
  try {
    const result = await runCli(["tools", "hooks", "install", "hermes"], { env: prefix.fixture.env });
    assert.equal(result.code, 1, result.stderr);
    assert.match(result.stderr, /inline plugins\.enabled/);
    assert.equal(readFileSync(prefix.configPath, "utf8"), prefixConfig);
    assert.equal(existsSync(prefix.markerPath), false);
    assert.equal(existsSync(prefix.pluginDir), false);
  } finally {
    prefix.fixture.cleanup();
  }

  const exactEnabledConfig = "plugins:\n  enabled:\n    - \"caveman_shrink\" # exact name\n  disabled:\n    - caveman_shrink_extra\n";
  const exactEnabled = hermesFixture(exactEnabledConfig);
  try {
    const result = await runCli(["tools", "hooks", "install", "hermes"], { env: exactEnabled.fixture.env });
    assert.equal(result.code, 0, result.stderr);
    assert.equal(readFileSync(exactEnabled.configPath, "utf8"), exactEnabledConfig);
    assert.equal(JSON.parse(readFileSync(exactEnabled.markerPath, "utf8")).installed, true);
  } finally {
    exactEnabled.fixture.cleanup();
  }

  const native = hermesFixture([
    "model:",
    "  default: gpt-5.5",
    "  provider: openai-codex",
    "  api_mode: codex_responses",
    "  base_url: https://chatgpt.com/backend-api/codex",
    "plugins:",
    "  enabled:",
    "    - keep_plugin",
    "  \"disabled\": [\"caveman_\\x6eative\"]",
    "",
  ].join("\n"));
  const originalNativeConfig = readFileSync(native.configPath, "utf8");
  const proxy = join(native.fixture.home, "bin", isWindows ? "caveman-proxy.cmd" : "caveman-proxy");
  const mcp = join(native.fixture.home, "bin", isWindows ? "caveman-mcp.cmd" : "caveman-mcp");
  writeFileSync(
    proxy,
    isWindows
      ? "@echo off\r\nif \"%~1\"==\"version\" if \"%~2\"==\"--json\" echo {\"version\":\"1.0.0\",\"capabilities\":[\"run_state\",\"native_runtime_v1\",\"native_hook_bridge_v1\",\"typed_ccr\"]}\r\nexit /b 0\r\n"
      : "#!/bin/sh\nif [ \"$1\" = \"version\" ] && [ \"$2\" = \"--json\" ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"run_state\",\"native_runtime_v1\",\"native_hook_bridge_v1\",\"typed_ccr\"]}'; fi\nexit 0\n",
  );
  writeFileSync(
    mcp,
    isWindows
      ? "@echo off\r\nif \"%~1\"==\"version\" if \"%~2\"==\"--json\" echo {\"version\":\"1.0.0\",\"capabilities\":[\"mcp_recovery\"]}\r\nexit /b 0\r\n"
      : "#!/bin/sh\nif [ \"$1\" = \"version\" ] && [ \"$2\" = \"--json\" ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"mcp_recovery\"]}'; fi\nexit 0\n",
  );
  if (!isWindows) chmodSync(proxy, 0o755);
  if (!isWindows) chmodSync(mcp, 0o755);
  native.fixture.env.CAVEMAN_PROXY_BIN = proxy;
  native.fixture.env.CAVEMAN_MCP_BIN = mcp;
  native.fixture.env.CAVEMAN_LISTEN = "127.0.0.1:48767";
  try {
    const result = await runCli(["enable", "hermes"], { env: native.fixture.env });
    assert.notEqual(result.code, 0, result.stderr);
    assert.match(result.stderr, /explicitly disabled in plugins\.disabled/);
    assert.equal(readFileSync(native.configPath, "utf8"), originalNativeConfig);
    assert.equal(existsSync(join(native.hermesHome, "plugins", "caveman_native")), false);
    assert.equal(existsSync(join(native.fixture.home, "integrations", "hermes.json")), false);
  } finally {
    native.fixture.cleanup();
  }
});
