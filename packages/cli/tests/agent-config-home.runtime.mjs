import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { delimiter, dirname, join, relative, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { claudeConfigDir, claudeGlobalConfigPath, geminiConfigDir } from "../dist/index.js";

const cli = fileURLToPath(new URL("../dist/index.js", import.meta.url));

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), "cave-agent-home-contract-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const home = join(root, "home");
  const custom = join(root, "work account");
  const bin = join(root, "bin");
  for (const directory of [home, custom, bin, join(root, "other-project")]) mkdirSync(directory, { recursive: true });
  const executable = (name, body) => {
    const source = `#!/usr/bin/env node\n${body}\n`;
    writeFileSync(join(bin, `${name}.cjs`), source, { mode: 0o755 });
    const path = join(bin, process.platform === "win32" ? `${name}.cmd` : name);
    writeFileSync(path, process.platform === "win32" ? `@node "%~dp0\\${name}.cjs" %*\r\n` : source, { mode: 0o755 });
    return path;
  };
  for (const agent of ["claude", "gemini", "codex"]) {
    executable(agent, `if (process.argv[2] === "--version") console.log(${JSON.stringify(`${agent} 1.0.0`)});`);
  }
  const version = `if (process.argv[2] === "version") console.log(JSON.stringify({ version: "1.0.0", capabilities: ["mcp_recovery", "native_runtime_v1", "native_hook_bridge_v1", "typed_ccr", "run_state"] }));`;
  const env = {
    PATH: `${bin}${delimiter}${dirname(process.execPath)}`,
    ...(process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
    ...(process.env.PATHEXT ? { PATHEXT: process.env.PATHEXT } : {}),
    HOME: home,
    USERPROFILE: home,
    CAVEMAN_HOME: join(home, ".caveman"),
    CAVEMAN_MCP_BIN: executable("caveman-mcp", version),
    CAVEMAN_PROXY_BIN: executable("caveman-proxy", version),
    CAVE_GATEWAY_URL: "http://127.0.0.1:9",
    CAVEMAN_TELEMETRY: "0",
    CAVEMAN_OFFLINE: "1",
    NO_COLOR: "1",
  };
  const run = (args, overrides = {}, cwd = root) => spawnSync(process.execPath, [cli, ...args], {
    env: { ...env, ...overrides }, cwd, encoding: "utf8", timeout: 20_000,
  });
  return { root, home, custom, run };
}

function write(path, text) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, text);
}

function succeeds(result) {
  assert.equal(result.status, 0, `${result.stderr}\n${result.error ?? ""}`);
}

test("Claude keeps its special default global config path and resolves nonempty overrides", (t) => {
  const fx = fixture(t);
  for (const value of [undefined, ""]) {
    assert.equal(claudeConfigDir({ CLAUDE_CONFIG_DIR: value }), join(homedir(), ".claude"));
    assert.equal(claudeGlobalConfigPath({ CLAUDE_CONFIG_DIR: value }), join(homedir(), ".claude.json"));
  }
  for (const value of [fx.custom, relative(process.cwd(), fx.custom), "   leading-space-profile"]) {
    assert.equal(claudeConfigDir({ CLAUDE_CONFIG_DIR: value }), resolve(value));
    assert.equal(claudeGlobalConfigPath({ CLAUDE_CONFIG_DIR: value }), join(resolve(value), ".claude.json"));
  }
  assert.equal(claudeConfigDir({ CLAUDE_CONFIG_DIR: join(fx.root, "new-profile") }), join(fx.root, "new-profile"));
});

test("Gemini home override points to the parent of .gemini and need not already exist", (t) => {
  const fx = fixture(t);
  for (const value of [undefined, ""]) assert.equal(geminiConfigDir({ GEMINI_CLI_HOME: value }), join(homedir(), ".gemini"));
  for (const value of [fx.custom, relative(process.cwd(), fx.custom), "   leading-space-profile", join(fx.root, "new-profile")]) {
    assert.equal(geminiConfigDir({ GEMINI_CLI_HOME: value }), resolve(value, ".gemini"));
  }
});

test("Claude user skill install honors its profile while project skills remain in the project", (t) => {
  const fx = fixture(t);
  const env = { CLAUDE_CONFIG_DIR: relative(fx.root, fx.custom) };
  succeeds(fx.run(["skills", "install", "caveman-learn", "--agent", "claude", "--user", "--no-pixel"], env));
  assert.ok(existsSync(join(fx.custom, "skills", "caveman-learn", "SKILL.md")));
  assert.equal(existsSync(join(fx.home, ".claude")), false);
  succeeds(fx.run(["skills", "install", "caveman-learn", "--agent", "claude", "--no-pixel"], env));
  assert.ok(existsSync(join(fx.root, ".claude", "skills", "caveman-learn", "SKILL.md")));
});

for (const [agent, variable] of [["claude", "CLAUDE_CONFIG_DIR"], ["codex", "CODEX_HOME"]]) {
  test(`skill conversion discovers ${agent}'s configured user skills`, (t) => {
    const fx = fixture(t);
    const original = "---\nname: account-only\ndescription: fixture\n---\nKeep this body.\n";
    write(join(fx.custom, "skills", "account-only", "SKILL.md"), original);
    write(join(fx.home, `.${agent}`, "skills", "wrong-account", "SKILL.md"), original);
    const result = fx.run(["convert", "--agent", agent, "--revert"], { [variable]: fx.custom });
    succeeds(result);
    assert.match(result.stdout + result.stderr, /account-only/);
    assert.doesNotMatch(result.stdout + result.stderr, /wrong-account/);
    assert.equal(readFileSync(join(fx.custom, "skills", "account-only", "SKILL.md"), "utf8"), original);
  });
}

test("Claude shrink and recall hooks install and uninstall in the selected profile", (t) => {
  const fx = fixture(t);
  const env = { CLAUDE_CONFIG_DIR: fx.custom };
  const path = join(fx.custom, "settings.json");
  const other = join(fx.home, ".claude", "settings.json");
  write(path, JSON.stringify({ theme: "custom", hooks: { SessionStart: [{ hooks: [{ command: "keep" }] }] } }));
  write(other, '{"theme":"other-account"}\n');
  succeeds(fx.run(["hooks", "install", "claude"], env));
  succeeds(fx.run(["mem", "hook", "install", "claude"], env));
  assert.match(readFileSync(path, "utf8"), /shrink-hook/);
  assert.match(readFileSync(path, "utf8"), /mem recall-hook/);
  succeeds(fx.run(["hooks", "uninstall", "claude"], env));
  succeeds(fx.run(["mem", "hook", "uninstall", "claude"], env));
  assert.deepEqual(JSON.parse(readFileSync(path, "utf8")), {
    theme: "custom", hooks: { SessionStart: [{ hooks: [{ command: "keep" }] }], PreToolUse: [], UserPromptSubmit: [] },
  });
  assert.equal(readFileSync(other, "utf8"), '{"theme":"other-account"}\n');
});

test("Gemini MCP and shrink hooks use settings inside the configured home", (t) => {
  const fx = fixture(t);
  const env = { GEMINI_CLI_HOME: relative(fx.root, fx.custom) };
  const path = join(fx.custom, ".gemini", "settings.json");
  const other = join(fx.home, ".gemini", "settings.json");
  const baseline = { theme: "custom", mcpServers: { other: { command: "keep" } } };
  write(path, JSON.stringify(baseline));
  write(other, '{"theme":"other-account"}\n');
  succeeds(fx.run(["mcp", "install", "gemini"], env));
  succeeds(fx.run(["hooks", "install", "gemini"], env));
  assert.ok(JSON.parse(readFileSync(path, "utf8")).mcpServers.caveman);
  assert.match(readFileSync(path, "utf8"), /shrink-hook/);
  succeeds(fx.run(["mcp", "uninstall", "gemini"], env));
  succeeds(fx.run(["hooks", "uninstall", "gemini"], env));
  assert.deepEqual(JSON.parse(readFileSync(path, "utf8")), { ...baseline, hooks: { BeforeTool: [] } });
  assert.equal(readFileSync(other, "utf8"), '{"theme":"other-account"}\n');
  assert.equal(existsSync(join(fx.custom, "settings.json")), false);
});

for (const [agent, variable, directory] of [["claude", "CLAUDE_CONFIG_DIR", ""], ["gemini", "GEMINI_CLI_HOME", ".gemini"]]) {
  test(`native ${agent} enable and disable restore the selected profile across working directories`, (t) => {
    const fx = fixture(t);
    const env = { [variable]: relative(fx.root, fx.custom) };
    const settings = join(fx.custom, directory, "settings.json");
    const companion = join(fx.custom, directory, agent === "claude" ? ".claude.json" : ".env");
    const settingsBefore = '{ "theme": "custom-account" }\n';
    const companionBefore = agent === "claude" ? '{ "mcpServers": { "other": { "command": "keep" } } }\n' : 'GEMINI_API_KEY=fake-local-test-token\n';
    write(settings, settingsBefore);
    write(companion, companionBefore);
    const defaultSettings = join(fx.home, `.${agent}`, "settings.json");
    write(defaultSettings, '{"theme":"other-account"}\n');
    succeeds(fx.run(["enable", agent], env));
    assert.match(readFileSync(settings, "utf8"), /native-hook/);
    assert.match(readFileSync(companion, "utf8"), agent === "claude" ? /caveman-mcp/ : /127\.0\.0\.1:9\/w\/gemini/);
    const journal = JSON.parse(readFileSync(join(fx.home, ".caveman", "integrations", `${agent}.json`), "utf8"));
    assert.deepEqual(journal.operations.map((operation) => realpathSync(operation.file)), [settings, companion].map((path) => realpathSync(path)));
    succeeds(fx.run(["disable", agent], { [variable]: fx.custom }, join(fx.root, "other-project")));
    assert.equal(readFileSync(settings, "utf8"), settingsBefore);
    assert.equal(readFileSync(companion, "utf8"), companionBefore);
    assert.equal(readFileSync(defaultSettings, "utf8"), '{"theme":"other-account"}\n');
    assert.equal(existsSync(join(fx.home, ".claude.json")), false);
  });
}

test("Codex directives honor CODEX_HOME for installation and removal", (t) => {
  const fx = fixture(t);
  const env = { CODEX_HOME: fx.custom };
  const path = join(fx.custom, "AGENTS.md");
  write(path, "Keep these instructions.\n");
  const args = ["codex", "--directive", "exploration-offload-directive"];
  succeeds(fx.run(["hooks", "install", ...args], env));
  assert.match(readFileSync(path, "utf8"), /caveman:directive/);
  succeeds(fx.run(["hooks", "uninstall", ...args], env));
  assert.equal(readFileSync(path, "utf8"), "Keep these instructions.\n");
  assert.equal(existsSync(join(fx.home, ".codex", "AGENTS.md")), false);
});
