import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function fixture() {
  const home = mkdtempSync(join(tmpdir(), "cave-native-enable-"));
  const bin = join(home, "bin");
  mkdirSync(bin, { recursive: true });
  for (const agent of ["claude", "codex", "hermes", "gemini", "opencode", "aider"]) {
    writeFileSync(join(bin, agent), `#!/bin/sh\nif [ "$1" = "--version" ]; then echo '${agent} 1.0.0'; fi\n`, { mode: 0o755 });
  }
  const mcp = join(bin, "caveman-mcp");
  writeFileSync(mcp, `#!/bin/sh
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  printf '%s\n' '{"version":"1.0.0","capabilities":["mcp_recovery"]}'
fi
`, { mode: 0o755 });
  const proxy = join(bin, "caveman-proxy");
  writeFileSync(proxy, `#!/bin/sh
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  printf '%s\n' '{"version":"1.0.0","capabilities":["run_state","native_runtime_v1","native_hook_bridge_v1","typed_ccr"]}'
fi
`, { mode: 0o755 });
  writeFileSync(join(bin, "caveman"), `#!/usr/bin/env node
const fs = require("node:fs");
const input = fs.readFileSync(0, "utf8");
if (process.env.CAVE_NATIVE_CAPTURE && input) fs.appendFileSync(process.env.CAVE_NATIVE_CAPTURE, Buffer.from(input).toString("base64") + "\\n");
const event = process.argv[4];
if (process.argv[2] === "shrink-hook") {
  process.stdout.write(JSON.stringify({ hookSpecificOutput: { updatedInput: { command: "caveman shrink -- git status" } } }));
} else if (event === "SessionStart" || event === "PostCompact") {
  process.stdout.write(JSON.stringify({ hookSpecificOutput: { additionalContext: "Caveman Core fixture" } }));
} else if (event === "PreToolUse") {
  process.stdout.write(JSON.stringify({ hookSpecificOutput: { additionalContext: "current observation fixture" } }));
} else if (event === "PostToolUse") {
  process.stdout.write(JSON.stringify({ output_replacement: "[CommandResult] full: ccr://fixture" }));
}
`, { mode: 0o755 });
  return {
    home,
    env: {
      ...process.env,
      HOME: home,
      CAVEMAN_HOME: join(home, ".caveman"),
      CAVEMAN_MCP_BIN: mcp,
      CAVEMAN_PROXY_BIN: proxy,
      // Full CLI suite runs several process-heavy files concurrently. Keep this
      // fixture's valid shell probes distinct from dedicated 2s hung-probe tests.
      CAVE_BINARY_PROBE_TIMEOUT_MS: "10000",
      CAVEMAN_TELEMETRY: "0",
      CAVE_NATIVE_CAPTURE: join(home, "native-capture.jsonl"),
      NO_COLOR: "1",
      PATH: `${bin}:${process.env.PATH}`,
    },
  };
}

function run(argv, env, input = undefined) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
    if (input !== undefined) child.stdin.end(input);
  });
}

test("enable/disable claude is idempotent and preserves unrelated later edits", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".claude"), { recursive: true });
  writeFileSync(join(fx.home, ".claude", "settings.json"), JSON.stringify({
    env: { KEEP: "yes" },
    hooks: { SessionStart: [{ hooks: [{ type: "command", command: "keep-start" }] }] },
  }, null, 2) + "\n");
  writeFileSync(join(fx.home, ".claude.json"), JSON.stringify({
    mcpServers: { other: { command: "other-mcp" } },
  }, null, 2) + "\n");

  const first = await run(["enable", "claude"], fx.env);
  assert.equal(first.code, 0, first.stderr);
  assert.match(first.stderr, /host trust remains authoritative/);
  assert.match(first.stderr, /run claude normally/);
  const settingsPath = join(fx.home, ".claude", "settings.json");
  const mcpPath = join(fx.home, ".claude.json");
  const installedBytes = readFileSync(settingsPath, "utf8");
  const settings = JSON.parse(installedBytes);
  assert.equal(settings.env.KEEP, "yes");
  assert.equal(settings.env.ANTHROPIC_BASE_URL, "http://127.0.0.1:8787/w/claude");
  assert.match(installedBytes, /native-hook-fast\.js/);
  assert.match(installedBytes, /native-hook claude/);
  assert.match(installedBytes, /caveman-proxy/);
  assert.match(installedBytes, /shrink-hook/);
  assert.match(JSON.stringify(settings.hooks.PreToolUse), /native-hook claude/);
  assert.match(JSON.stringify(settings.hooks.PostToolUseFailure), /native-hook claude/);
  assert.match(JSON.stringify(settings.hooks.PostCompact), /native-hook claude/);
  assert.equal(JSON.parse(readFileSync(mcpPath, "utf8")).mcpServers.other.command, "other-mcp");
  assert.match(readFileSync(mcpPath, "utf8"), /caveman-mcp/);
  assert.ok(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")));

  const second = await run(["enable", "claude"], fx.env);
  assert.equal(second.code, 0, second.stderr);
  assert.equal(readFileSync(settingsPath, "utf8"), installedBytes, "second enable must not replace original backup");

  const editedSettings = JSON.parse(readFileSync(settingsPath, "utf8"));
  editedSettings.theme = "dark";
  editedSettings.hooks.SessionStart.push({ hooks: [{ type: "command", command: "later-user-hook" }] });
  writeFileSync(settingsPath, JSON.stringify(editedSettings, null, 2) + "\n");
  const editedMcp = JSON.parse(readFileSync(mcpPath, "utf8"));
  editedMcp.mcpServers.later = { command: "later-mcp" };
  writeFileSync(mcpPath, JSON.stringify(editedMcp, null, 2) + "\n");

  const disabled = await run(["disable", "claude"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  const after = JSON.parse(readFileSync(settingsPath, "utf8"));
  assert.equal(after.env.KEEP, "yes");
  assert.equal(after.env.ANTHROPIC_BASE_URL, undefined);
  assert.equal(after.theme, "dark");
  assert.match(JSON.stringify(after), /later-user-hook/);
  assert.doesNotMatch(JSON.stringify(after), /native-hook|shrink-hook/);
  const afterMcp = JSON.parse(readFileSync(mcpPath, "utf8"));
  assert.equal(afterMcp.mcpServers.other.command, "other-mcp");
  assert.equal(afterMcp.mcpServers.later.command, "later-mcp");
  assert.equal(afterMcp.mcpServers.caveman, undefined);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")), false);
});

test("disable claude refuses owned-value conflict and keeps journal", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const path = join(fx.home, ".claude", "settings.json");
  const settings = JSON.parse(readFileSync(path, "utf8"));
  settings.env.ANTHROPIC_BASE_URL = "http://user-changed.example";
  writeFileSync(path, JSON.stringify(settings, null, 2) + "\n");
  const before = readFileSync(path, "utf8");

  const out = await run(["disable", "claude"], fx.env);
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /changed after enable|refusing destructive disable/i);
  assert.equal(readFileSync(path, "utf8"), before);
  assert.ok(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")));
});

test("clean enable/disable restores exact original bytes", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".claude"), { recursive: true });
  const settingsPath = join(fx.home, ".claude", "settings.json");
  const mcpPath = join(fx.home, ".claude.json");
  const settingsBefore = '{\n  "env": { "EXISTING": "1" },\n  "theme": "light"\n}\n';
  const mcpBefore = '{"mcpServers":{"keep":{"command":"keep"}}}\n';
  writeFileSync(settingsPath, settingsBefore);
  writeFileSync(mcpPath, mcpBefore);
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const disabled = await run(["disable", "claude"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  assert.equal(readFileSync(settingsPath, "utf8"), settingsBefore);
  assert.equal(readFileSync(mcpPath, "utf8"), mcpBefore);
});

test("enable/disable codex owns marked config blocks and preserves unrelated drift", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const configPath = join(fx.home, ".codex", "config.toml");
  const hooksPath = join(fx.home, ".codex", "hooks.json");
  writeFileSync(configPath, 'approval_policy = "never"\n\n[profiles.work]\nmodel = "gpt-5"\n');
  writeFileSync(hooksPath, JSON.stringify({ hooks: { SessionStart: [{ hooks: [{ type: "command", command: "keep-codex" }] }] } }, null, 2) + "\n");

  const enabled = await run(["enable", "codex"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);
  const installed = readFileSync(configPath, "utf8");
  assert.match(installed, /^# >>> caveman:native-root\nmodel_provider = "caveman"/);
  assert.match(installed, /\[model_providers\.caveman\]/);
  assert.match(installed, /\[mcp_servers\.caveman\]/);
  assert.match(readFileSync(hooksPath, "utf8"), /native-hook codex/);
  assert.match(readFileSync(hooksPath, "utf8"), /keep-codex/);
  const installedHooks = JSON.parse(readFileSync(hooksPath, "utf8")).hooks;
  assert.match(JSON.stringify(installedHooks.PreToolUse), /native-hook codex/);
  assert.match(JSON.stringify(installedHooks.PermissionRequest), /native-hook codex/);
  assert.match(JSON.stringify(installedHooks.PostToolUseFailure), /native-hook codex/);

  writeFileSync(configPath, `${installed}\n# later user comment\n`);
  const hooks = JSON.parse(readFileSync(hooksPath, "utf8"));
  hooks.extra = "keep";
  writeFileSync(hooksPath, JSON.stringify(hooks, null, 2) + "\n");
  const disabled = await run(["disable", "codex"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  const afterConfig = readFileSync(configPath, "utf8");
  assert.doesNotMatch(afterConfig, /caveman:native|model_providers\.caveman|mcp_servers\.caveman/);
  assert.match(afterConfig, /approval_policy = "never"/);
  assert.match(afterConfig, /# later user comment/);
  const afterHooks = JSON.parse(readFileSync(hooksPath, "utf8"));
  assert.equal(afterHooks.extra, "keep");
  assert.match(JSON.stringify(afterHooks), /keep-codex/);
  assert.doesNotMatch(JSON.stringify(afterHooks), /native-hook|shrink-hook/);
});

test("enable fails before host writes when current MCP binary is missing", async () => {
  const fx = fixture();
  const env = { ...fx.env, CAVEMAN_MCP_BIN: join(fx.home, "missing-mcp"), PATH: "/usr/bin:/bin" };
  const out = await run(["enable", "claude"], env);
  assert.notEqual(out.code, 0);
  assert.equal(existsSync(join(fx.home, ".claude", "settings.json")), false);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")), false);
});

test("enable fails before host writes when current local proxy binary is missing", async () => {
  const fx = fixture();
  const env = { ...fx.env, CAVEMAN_PROXY_BIN: join(fx.home, "missing-proxy") };
  const out = await run(["enable", "claude"], env);
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /caveman-proxy not found/);
  assert.equal(existsSync(join(fx.home, ".claude", "settings.json")), false);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")), false);
});

test("native hook bridge capability is probed independently from runtime capability", async () => {
  const fx = fixture();
  writeFileSync(fx.env.CAVEMAN_PROXY_BIN, `#!/bin/sh
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  printf '%s\n' '{"version":"older","capabilities":["run_state","native_runtime_v1","typed_ccr"]}'
fi
`, { mode: 0o755 });
  const out = await run(["enable", "claude"], fx.env);
  assert.equal(out.code, 0, out.stderr);
  const settings = readFileSync(join(fx.home, ".claude", "settings.json"), "utf8");
  assert.match(settings, /native-hook-fast\.js.*native-hook claude/);
  assert.doesNotMatch(settings, /caveman-proxy[^\n]*native-hook claude/);
});

test("enable refuses invalid Claude-owned container shapes without writes", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".claude"), { recursive: true });
  const path = join(fx.home, ".claude", "settings.json");
  const before = '{"env":"keep-this-invalid-value"}\n';
  writeFileSync(path, before);
  const out = await run(["enable", "claude"], fx.env);
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /env must be a JSON object/);
  assert.equal(readFileSync(path, "utf8"), before);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")), false);
});

test("disable refuses a removed pre-existing file and keeps journal", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".claude"), { recursive: true });
  const path = join(fx.home, ".claude", "settings.json");
  writeFileSync(path, '{"theme":"before"}\n');
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const { unlinkSync } = await import("node:fs");
  unlinkSync(path);
  const out = await run(["disable", "claude"], fx.env);
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /was removed after enable/);
  assert.ok(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")));
});

test("doctor reports Codex routing degraded when auth lane changes", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const authPath = join(fx.home, ".codex", "auth.json");
  writeFileSync(authPath, JSON.stringify({ OPENAI_API_KEY: "sk-local" }));
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  writeFileSync(authPath, JSON.stringify({ tokens: { account_id: "acct_1" } }));
  const out = await run(["doctor", "codex"], fx.env);
  assert.notEqual(out.code, 0);
  const result = JSON.parse(out.stdout);
  assert.equal(result.state, "degraded");
  assert.equal(result.components.routing, false);
  assert.equal(result.launchable, true);
  assert.equal(result.tested, false);
  assert.equal(result.version_status, "newer_unknown");
  assert.equal(result.capabilities.provider_proxy.supported, true);
  assert.equal(result.capabilities.provider_proxy.active, false);
  assert.equal(result.capabilities.post_tool_rewrite.supported, false);
  assert.equal(result.repair, "caveman doctor codex --fix");
});

test("doctor reports a present but unlaunchable host as unavailable", async () => {
  const fx = fixture();
  writeFileSync(join(fx.home, "bin", "codex"), "#!/bin/sh\nexit 127\n", { mode: 0o755 });
  const out = await run(["doctor", "codex"], fx.env);
  assert.notEqual(out.code, 0);
  const result = JSON.parse(out.stdout);
  assert.equal(result.binary_present, true);
  assert.equal(result.launchable, false);
  assert.equal(result.available, false);
  assert.equal(result.state, "unavailable");
  assert.equal(result.version_probe_error, "version_probe_exit_127");
});

test("doctor surfaces independently disabled Core without degrading native integration", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const configDir = join(fx.home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "config.json"), JSON.stringify({ think: { mode: "compress", core: false } }, null, 2));
  const out = await run(["doctor", "claude"], fx.env);
  assert.equal(out.code, 0, out.stderr);
  const result = JSON.parse(out.stdout);
  assert.equal(result.state, "installed");
  assert.equal(result.core_configured, false);
  assert.equal(result.core_supported, true);
  assert.equal(result.core_active, false);
  assert.equal(result.core_enabled, false);
  assert.equal(result.core_source, "global");
  assert.equal(result.coding_policy, "off");
  assert.equal(result.components.core, false);
  assert.equal(result.components.routing, true);
});

test("doctor reports persisted and environment record modes as Core inactive", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const configDir = join(fx.home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "config.json"), JSON.stringify({ think: { mode: "record", core: true } }, null, 2));
  const out = await run(["doctor", "claude"], fx.env);
  assert.equal(out.code, 0, out.stderr);
  const result = JSON.parse(out.stdout);
  assert.equal(result.core_configured, true);
  assert.equal(result.core_supported, true);
  assert.equal(result.core_active, false);
  assert.equal(result.core_enabled, false);
  assert.equal(result.coding_policy, "off");
  assert.equal(result.components.core, false);

  writeFileSync(join(configDir, "config.json"), JSON.stringify({ think: { mode: "compress", core: true } }, null, 2));
  const envMode = JSON.parse((await run(["doctor", "claude"], { ...fx.env, CAVEMAN_NATIVE_MODE: "record" })).stdout);
  assert.equal(envMode.core_active, false);
  const envProfile = JSON.parse((await run(["doctor", "claude"], { ...fx.env, CAVEMAN_NATIVE_PROFILE: "record-only" })).stdout);
  assert.equal(envProfile.core_active, false);
});

test("doctor generic reports proxy-only safe subset without lifecycle claims", async () => {
  const fx = fixture();
  const out = await run(["doctor", "generic"], { ...fx.env, CAVE_GATEWAY_URL: "http://127.0.0.1:1" });
  assert.equal(out.code, 0, out.stderr);
  const result = JSON.parse(out.stdout);
  assert.equal(result.integration_depth, "fallback");
  assert.equal(result.components.shared_runtime, true);
  assert.equal(result.components.lifecycle_hooks, false);
  assert.equal(result.capabilities.provider_proxy.supported, true);
  assert.equal(result.capabilities.provider_proxy.active, false);
  assert.equal(result.capabilities.pre_tool.supported, false);
  assert.equal(result.trust, "no host lifecycle hooks");
});

test("doctor requires exact native hook entries, not matching text", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const path = join(fx.home, ".claude", "settings.json");
  const settings = JSON.parse(readFileSync(path, "utf8"));
  settings.hooks.SessionStart = [{ hooks: [{ type: "command", command: "echo native-hook claude" }] }];
  writeFileSync(path, JSON.stringify(settings, null, 2) + "\n");
  const out = await run(["doctor", "claude"], fx.env);
  assert.notEqual(out.code, 0);
  const result = JSON.parse(out.stdout);
  assert.equal(result.state, "degraded");
  assert.equal(result.components.lifecycle_hooks, false);
});

test("doctor --fix transactionally repairs missing owned hooks and preserves unrelated edits", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const path = join(fx.home, ".claude", "settings.json");
  const settings = JSON.parse(readFileSync(path, "utf8"));
  settings.theme = "later-user-theme";
  settings.hooks.SessionStart = settings.hooks.SessionStart.filter(
    (entry) => !JSON.stringify(entry).includes("native-hook claude"),
  );
  writeFileSync(path, JSON.stringify(settings, null, 2) + "\n");

  const degraded = await run(["doctor", "claude"], fx.env);
  assert.notEqual(degraded.code, 0);
  assert.equal(JSON.parse(degraded.stdout).state, "degraded");

  const fixed = await run(["doctor", "claude", "--fix"], fx.env);
  assert.equal(fixed.code, 0, fixed.stderr);
  const result = JSON.parse(fixed.stdout);
  assert.equal(result.state, "installed");
  assert.equal(result.fix.result, "repaired");
  const repaired = JSON.parse(readFileSync(path, "utf8"));
  assert.equal(repaired.theme, "later-user-theme");
  assert.match(JSON.stringify(repaired.hooks.SessionStart), /native-hook claude/);
  assert.ok(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")));

  const disabled = await run(["disable", "claude"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  const restored = JSON.parse(readFileSync(path, "utf8"));
  assert.equal(restored.theme, "later-user-theme");
  assert.doesNotMatch(JSON.stringify(restored), /native-hook claude|shrink-hook/);
});

test("doctor --fix refuses owned-value conflicts without partial writes", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const path = join(fx.home, ".claude", "settings.json");
  const settings = JSON.parse(readFileSync(path, "utf8"));
  settings.env.ANTHROPIC_BASE_URL = "https://user-route.example";
  writeFileSync(path, JSON.stringify(settings, null, 2) + "\n");
  const before = readFileSync(path, "utf8");

  const fixed = await run(["doctor", "claude", "--fix"], fx.env);
  assert.notEqual(fixed.code, 0);
  assert.match(fixed.stderr, /changed after enable|refusing destructive disable/i);
  assert.equal(readFileSync(path, "utf8"), before);
  assert.ok(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")));
});

test("disable --all removes every journaled integration and preserves unrelated config", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".claude"), { recursive: true });
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const claudePath = join(fx.home, ".claude", "settings.json");
  const codexPath = join(fx.home, ".codex", "config.toml");
  writeFileSync(claudePath, JSON.stringify({ theme: "keep" }) + "\n");
  writeFileSync(codexPath, 'approval_policy = "never"\n');
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);

  const out = await run(["disable", "--all"], fx.env);
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stderr, /Claude Code: native Caveman disabled/);
  assert.match(out.stderr, /OpenAI Codex CLI: native Caveman disabled/);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")), false);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "codex.json")), false);
  assert.equal(JSON.parse(readFileSync(claudePath, "utf8")).theme, "keep");
  assert.match(readFileSync(codexPath, "utf8"), /approval_policy = "never"/);
  assert.doesNotMatch(readFileSync(codexPath, "utf8"), /caveman:native/);
});

test("doctor --fix upgrades a stale native pack journal without losing later user edits", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const settingsPath = join(fx.home, ".claude", "settings.json");
  const settings = JSON.parse(readFileSync(settingsPath, "utf8"));
  settings.later = "preserve-through-upgrade";
  writeFileSync(settingsPath, JSON.stringify(settings, null, 2) + "\n");
  const journalPath = join(fx.home, ".caveman", "integrations", "claude.json");
  const journal = JSON.parse(readFileSync(journalPath, "utf8"));
  journal.pack_version = "0.9.0";
  writeFileSync(journalPath, JSON.stringify(journal, null, 2) + "\n");

  const stale = await run(["doctor", "claude"], fx.env);
  assert.notEqual(stale.code, 0);
  const staleStatus = JSON.parse(stale.stdout);
  assert.equal(staleStatus.state, "degraded");
  assert.equal(staleStatus.pack_current, false);
  assert.equal(staleStatus.pack_version, "0.9.0");

  const fixed = await run(["doctor", "claude", "--fix"], fx.env);
  assert.equal(fixed.code, 0, fixed.stderr);
  const fixedStatus = JSON.parse(fixed.stdout);
  assert.equal(fixedStatus.state, "installed");
  assert.equal(fixedStatus.pack_current, true);
  assert.equal(fixedStatus.fix.result, "repaired");
  assert.equal(JSON.parse(readFileSync(settingsPath, "utf8")).later, "preserve-through-upgrade");
});

test("concurrent native installer lock refuses mutation before touching host config", async () => {
  const fx = fixture();
  const lock = join(fx.home, ".caveman", "integrations", ".lock-claude");
  mkdirSync(lock, { recursive: true });
  const out = await run(["enable", "claude"], fx.env);
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /integration change already running for claude/);
  assert.equal(existsSync(join(fx.home, ".claude", "settings.json")), false);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")), false);
});

test("native installer reclaims a lock owned by a dead process", async () => {
  const fx = fixture();
  const lock = join(fx.home, ".caveman", "integrations", ".lock-claude");
  mkdirSync(lock, { recursive: true });
  writeFileSync(join(lock, "owner.json"), JSON.stringify({ pid: 99999999, token: "dead", started_at: "2026-01-01T00:00:00.000Z" }) + "\n");
  const out = await run(["enable", "claude"], fx.env);
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stderr, /reclaimed stale integration lock for claude/);
  assert.ok(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")));
  assert.equal(existsSync(lock), false);
});

test("doctor --fix recovers an interrupted partial install before enabling cleanly", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const integrations = join(fx.home, ".caveman", "integrations");
  const journalPath = join(integrations, "claude.json");
  const pendingPath = join(integrations, ".pending-claude.json");
  const journal = JSON.parse(readFileSync(journalPath, "utf8"));
  writeFileSync(pendingPath, JSON.stringify(journal, null, 2) + "\n");
  unlinkSync(journalPath);
  // Simulate death after first host write: settings installed, MCP file untouched.
  unlinkSync(join(fx.home, ".claude.json"));

  const broken = await run(["doctor", "claude"], fx.env);
  assert.notEqual(broken.code, 0);
  const brokenStatus = JSON.parse(broken.stdout);
  assert.equal(brokenStatus.state, "degraded");
  assert.equal(brokenStatus.transaction_pending, true);

  const fixed = await run(["doctor", "claude", "--fix"], fx.env);
  assert.equal(fixed.code, 0, fixed.stderr);
  assert.match(fixed.stderr, /recovered interrupted claude integration change/);
  const status = JSON.parse(fixed.stdout);
  assert.equal(status.state, "installed");
  assert.equal(status.transaction_pending, false);
  assert.equal(existsSync(pendingPath), false);
  assert.ok(existsSync(journalPath));
  assert.match(readFileSync(join(fx.home, ".claude", "settings.json"), "utf8"), /native-hook claude/);
  assert.match(readFileSync(join(fx.home, ".claude.json"), "utf8"), /caveman-mcp/);
});

test("doctor --fix rolls back an interrupted install even when host binary disappeared", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const integrations = join(fx.home, ".caveman", "integrations");
  const journalPath = join(integrations, "claude.json");
  const pendingPath = join(integrations, ".pending-claude.json");
  writeFileSync(pendingPath, readFileSync(journalPath));
  unlinkSync(journalPath);
  unlinkSync(join(fx.home, ".claude.json"));
  unlinkSync(join(fx.home, "bin", "claude"));

  const fixed = await run(["doctor", "claude", "--fix"], { ...fx.env, PATH: join(fx.home, "bin") });
  assert.notEqual(fixed.code, 0, "host remains unavailable after safe rollback");
  assert.match(fixed.stderr, /recovered interrupted claude integration change/);
  const status = JSON.parse(fixed.stdout);
  assert.equal(status.state, "unavailable");
  assert.equal(status.fix.result, "recovered");
  assert.equal(status.transaction_pending, false);
  assert.equal(existsSync(pendingPath), false);
  assert.equal(existsSync(journalPath), false);
  assert.equal(existsSync(join(fx.home, ".claude", "settings.json")), false);
});

test("enable/disable hermes installs native lifecycle pack and preserves unrelated YAML drift", async () => {
  const fx = fixture();
  const hermesHome = join(fx.home, ".hermes");
  mkdirSync(hermesHome, { recursive: true });
  const configPath = join(hermesHome, "config.yaml");
  writeFileSync(configPath, [
    "model:",
    "  default: gpt-5.5",
    "  provider: openai-codex",
    "  base_url: https://chatgpt.com/backend-api/codex",
    "plugins:",
    "  enabled:",
    "    - keep_plugin",
    "mcp_servers:",
    "  keep:",
    "    command: keep-mcp",
    "",
  ].join("\n"));
  const env = { ...fx.env, HERMES_HOME: hermesHome };

  const enabled = await run(["enable", "hermes"], env);
  assert.equal(enabled.code, 0, enabled.stderr);
  const installed = readFileSync(configPath, "utf8");
  assert.match(installed, /caveman:native-hermes-routing/);
  assert.match(installed, /provider: "custom"/);
  assert.match(installed, /base_url: "http:\/\/127\.0\.0\.1:8787\/w\/hermes"/);
  assert.match(installed, /caveman_native/);
  assert.match(installed, /caveman-native/);
  const pluginDir = join(hermesHome, "plugins", "caveman_native");
  assert.match(readFileSync(join(pluginDir, "plugin.yaml"), "utf8"), /pre_llm_call/);
  const plugin = readFileSync(join(pluginDir, "__init__.py"), "utf8");
  assert.match(plugin, /native-hook/);
  assert.match(plugin, /"task_continuation": _task_continuation\(user_message\)/);
  assert.match(plugin, /ctx\.register_hook\("pre_tool_call"/);
  const compiled = spawnSync("python3", ["-m", "py_compile", join(pluginDir, "__init__.py")], {
    env: { ...env, PYTHONPYCACHEPREFIX: join(fx.home, "pycache") },
    encoding: "utf8",
  });
  assert.equal(compiled.status, 0, compiled.stderr);
  assert.ok(existsSync(join(fx.home, ".caveman", "integrations", "hermes.json")));

  writeFileSync(configPath, `${installed}# later user setting\n`);
  const disabled = await run(["disable", "hermes"], env);
  assert.equal(disabled.code, 0, disabled.stderr);
  const restored = readFileSync(configPath, "utf8");
  assert.match(restored, /provider: openai-codex/);
  assert.match(restored, /base_url: https:\/\/chatgpt\.com\/backend-api\/codex/);
  assert.match(restored, /keep_plugin/);
  assert.match(restored, /command: keep-mcp/);
  assert.match(restored, /# later user setting/);
  assert.doesNotMatch(restored, /caveman:native-hermes|caveman_native|caveman-native/);
  assert.equal(existsSync(pluginDir), false);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "hermes.json")), false);
});

test("Hermes lifecycle bridge returns stable Core without persisting raw prompt", async () => {
  const fx = fixture();
  const secretPrompt = "fix auth with sk-secret-never-store";
  const out = await run(
    ["native-hook", "hermes", "UserPromptSubmit"],
    fx.env,
    JSON.stringify({ event_name: "UserPromptSubmit", session_id: "s1", prompt: { bytes: secretPrompt.length, sha256: "sha256:abc" } }),
  );
  assert.equal(out.code, 0, out.stderr);
  const response = JSON.parse(out.stdout);
  assert.match(response.context, /Build simplest complete system/);
  assert.match(response.context, /Coherent wider change beats cramped patch/);
  const events = readFileSync(join(fx.home, ".caveman", "runtime", "native-events.jsonl"), "utf8");
  assert.match(events, /"agent":"hermes"/);
  assert.doesNotMatch(events, /sk-secret-never-store/);
});

test("enable/disable gemini installs lifecycle, MCP, routing and preserves later user edits", async () => {
  const fx = fixture();
  const geminiDir = join(fx.home, ".gemini");
  mkdirSync(geminiDir, { recursive: true });
  const settingsPath = join(geminiDir, "settings.json");
  const envPath = join(geminiDir, ".env");
  writeFileSync(settingsPath, JSON.stringify({
    theme: "keep",
    hooks: { SessionStart: [{ hooks: [{ type: "command", command: "keep-start" }] }] },
    mcpServers: { other: { command: "other-mcp" } },
  }, null, 2) + "\n");
  writeFileSync(envPath, "GEMINI_API_KEY=keep\n");

  const enabled = await run(["enable", "gemini"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);
  const settings = JSON.parse(readFileSync(settingsPath, "utf8"));
  assert.equal(settings.theme, "keep");
  assert.equal(settings.mcpServers.other.command, "other-mcp");
  assert.match(settings.mcpServers.caveman.command, /caveman-mcp/);
  for (const event of ["SessionStart", "BeforeAgent", "BeforeModel", "BeforeTool", "AfterTool", "AfterModel", "PreCompress", "AfterAgent", "SessionEnd"]) {
    assert.match(JSON.stringify(settings.hooks[event]), /native-hook gemini/, event);
  }
  assert.match(JSON.stringify(settings.hooks.BeforeTool), /shrink-hook/);
  const installedEnv = readFileSync(envPath, "utf8");
  assert.match(installedEnv, /GOOGLE_GEMINI_BASE_URL=http:\/\/127\.0\.0\.1:8787\/w\/gemini/);
  assert.match(installedEnv, /GOOGLE_VERTEX_BASE_URL=http:\/\/127\.0\.0\.1:8787\/w\/gemini\/vertex/);
  assert.ok(existsSync(join(fx.home, ".caveman", "integrations", "gemini.json")));

  settings.later = true;
  settings.hooks.SessionStart.push({ hooks: [{ type: "command", command: "later-user-hook" }] });
  writeFileSync(settingsPath, JSON.stringify(settings, null, 2) + "\n");
  writeFileSync(envPath, `${installedEnv}LATER_USER_VALUE=yes\n`);
  const disabled = await run(["disable", "gemini"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  const restored = JSON.parse(readFileSync(settingsPath, "utf8"));
  assert.equal(restored.theme, "keep");
  assert.equal(restored.later, true);
  assert.equal(restored.mcpServers.other.command, "other-mcp");
  assert.equal(restored.mcpServers.caveman, undefined);
  assert.match(JSON.stringify(restored), /later-user-hook/);
  assert.doesNotMatch(JSON.stringify(restored), /native-hook gemini|shrink-hook/);
  const restoredEnv = readFileSync(envPath, "utf8");
  assert.match(restoredEnv, /GEMINI_API_KEY=keep/);
  assert.match(restoredEnv, /LATER_USER_VALUE=yes/);
  assert.doesNotMatch(restoredEnv, /caveman:native-routing|GEMINI_BASE_URL/);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "gemini.json")), false);
});

test("enable/disable opencode installs one native plugin, routed providers and reversible MCP", async () => {
  const fx = fixture();
  const configDir = join(fx.home, ".config", "opencode");
  mkdirSync(configDir, { recursive: true });
  const configPath = join(configDir, "opencode.json");
  writeFileSync(configPath, JSON.stringify({
    theme: "keep",
    provider: {
      openai: { options: { baseURL: "https://openai.before", keep: true } },
      custom: { options: { baseURL: "https://custom.example" } },
    },
    mcp: { other: { type: "local", command: ["other"] } },
  }, null, 2) + "\n");

  const enabled = await run(["enable", "opencode"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);
  const installed = JSON.parse(readFileSync(configPath, "utf8"));
  assert.equal(installed.provider.openai.options.baseURL, "http://127.0.0.1:8787/w/opencode/openai/v1");
  assert.equal(installed.provider.openai.options.keep, true);
  assert.equal(installed.provider.anthropic.options.baseURL, "http://127.0.0.1:8787/w/opencode/anthropic/v1");
  assert.equal(installed.provider.custom.options.baseURL, "https://custom.example");
  assert.match(installed.mcp.caveman.command[0], /caveman-mcp/);
  const pluginPath = join(configDir, "plugins", "caveman-native.js");
  const plugin = readFileSync(pluginPath, "utf8");
  for (const surface of ["chat.message", "experimental.chat.system.transform", "tool.execute.before", "tool.execute.after", "experimental.session.compacting"]) {
    assert.match(plugin, new RegExp(surface.replaceAll(".", "\\.")));
  }
  assert.match(plugin, /native-hook", "opencode/);
  const syntax = spawnSync(process.execPath, ["--check", pluginPath], { encoding: "utf8" });
  assert.equal(syntax.status, 0, syntax.stderr);
  const pluginModule = await import(`${pathToFileURL(pluginPath).href}?test=${Date.now()}`);
  const hooks = await pluginModule.CavemanNative();
  const system = { system: [] };
  await hooks["experimental.chat.system.transform"]({ sessionID: "oc-1", model: {} }, system);
  assert.deepEqual(system.system, ["Caveman Core fixture"]);
  writeFileSync(fx.env.CAVE_NATIVE_CAPTURE, "");
  const previousCapture = process.env.CAVE_NATIVE_CAPTURE;
  process.env.CAVE_NATIVE_CAPTURE = fx.env.CAVE_NATIVE_CAPTURE;
  await hooks["chat.message"](
    { sessionID: "oc-1", model: { modelID: "m", providerID: "p" } },
    { parts: [{ type: "text", text: "Yes, add billing support" }] },
  );
  await hooks["chat.message"](
    { sessionID: "oc-1", model: { modelID: "m", providerID: "p" } },
    { parts: [{ type: "text", text: "fix that" }] },
  );
  const capturedPrompts = readFileSync(fx.env.CAVE_NATIVE_CAPTURE, "utf8").trim().split("\n").filter(Boolean)
    .map((line) => JSON.parse(Buffer.from(line, "base64").toString("utf8")));
  const taskProfiles = capturedPrompts.filter((item) => Object.hasOwn(item, "task_continuation"));
  assert.equal(taskProfiles.length, 2, JSON.stringify(capturedPrompts));
  assert.equal(taskProfiles[0].task_continuation, false);
  assert.equal(taskProfiles[1].task_continuation, true);
  if (previousCapture === undefined) delete process.env.CAVE_NATIVE_CAPTURE;
  else process.env.CAVE_NATIVE_CAPTURE = previousCapture;
  const before = { args: { command: "git status" } };
  await hooks["tool.execute.before"]({ tool: "bash", sessionID: "oc-1", callID: "c1" }, before);
  assert.equal(before.args.command, "caveman shrink -- git status");
  const after = { title: "", output: "large exact output", metadata: {} };
  await hooks["tool.execute.after"]({ tool: "bash", sessionID: "oc-1", callID: "c1", args: before.args }, after);
  assert.equal(after.output, "[CommandResult] full: ccr://fixture");
  await hooks.dispose();

  installed.later = "preserve";
  installed.provider.openai.options.later = 1;
  installed.mcp.later = { type: "local", command: ["later"] };
  writeFileSync(configPath, JSON.stringify(installed, null, 2) + "\n");
  const disabled = await run(["disable", "opencode"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  const restored = JSON.parse(readFileSync(configPath, "utf8"));
  assert.equal(restored.theme, "keep");
  assert.equal(restored.later, "preserve");
  assert.equal(restored.provider.openai.options.baseURL, "https://openai.before");
  assert.equal(restored.provider.openai.options.later, 1);
  assert.equal(restored.provider.anthropic, undefined);
  assert.equal(restored.provider.custom.options.baseURL, "https://custom.example");
  assert.equal(restored.mcp.other.command[0], "other");
  assert.equal(restored.mcp.later.command[0], "later");
  assert.equal(restored.mcp.caveman, undefined);
  assert.equal(existsSync(pluginPath), false);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "opencode.json")), false);
});

test("enable/disable aider stays shallow, preserves native repo map, and restores config", async () => {
  const fx = fixture();
  const configPath = join(fx.home, ".aider.conf.yml");
  const before = [
    "openai-api-base: https://before.example/v1",
    "read:",
    "  - USER_CONVENTIONS.md",
    "map-tokens: 2048",
    "",
  ].join("\n");
  writeFileSync(configPath, before);

  const aiderEnv = { ...fx.env, CAVEMAN_MCP_BIN: join(fx.home, "missing-mcp"), CAVEMAN_CORE: "off" };
  const enabled = await run(["enable", "aider"], aiderEnv);
  assert.equal(enabled.code, 0, enabled.stderr);
  assert.match(enabled.stderr, /shallow Caveman enabled/);
  assert.match(enabled.stderr, /lifecycle\/tool interception unavailable; Ledger observational/);
  assert.match(enabled.stderr, /Core .* static on; Aider cannot apply think\.core live/);
  const installed = readFileSync(configPath, "utf8");
  assert.match(installed, /openai-api-base: "http:\/\/127\.0\.0\.1:8787\/w\/aider\/openai\/v1"/);
  assert.match(installed, /USER_CONVENTIONS\.md/);
  assert.match(installed, /caveman:native-aider-core-read/);
  assert.match(installed, /map-tokens: 2048/);
  const corePath = join(fx.home, ".caveman", "packs", "aider", "CAVEMAN.md");
  assert.match(readFileSync(corePath, "utf8"), /caveman:native-aider-core/);

  const doctor = await run(["doctor", "aider"], aiderEnv);
  assert.equal(doctor.code, 0, doctor.stderr);
  const status = JSON.parse(doctor.stdout);
  assert.equal(status.state, "installed");
  assert.equal(status.integration_depth, "shallow");
  assert.equal(status.repository_map, "host_native_authoritative");
  assert.equal(status.ledger_mode, "observational");
  assert.equal(status.core_configured, false);
  assert.equal(status.core_supported, true);
  assert.equal(status.core_active, true);
  assert.equal(status.core_toggle_supported, false);
  assert.equal(status.core_source, "static_agent_read");
  assert.equal(status.coding_policy, "core-static");
  assert.equal(status.components.routing, true);
  assert.equal(status.components.core, true);
  assert.equal(status.components.lifecycle_hooks, false);
  assert.equal(status.components.mcp_recovery, false);
  assert.equal(status.capabilities.provider_proxy.active, true);
  assert.equal(status.capabilities.pre_tool.supported, false);

  writeFileSync(configPath, `${installed}later-user-option: keep\n`);
  const disabled = await run(["disable", "aider"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  const restored = readFileSync(configPath, "utf8");
  assert.match(restored, /openai-api-base: https:\/\/before\.example\/v1/);
  assert.match(restored, /USER_CONVENTIONS\.md/);
  assert.match(restored, /map-tokens: 2048/);
  assert.match(restored, /later-user-option: keep/);
  assert.doesNotMatch(restored, /caveman:native-aider|127\.0\.0\.1:8787/);
  assert.equal(existsSync(corePath), false);
});
