import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, unlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function fixture({ opencodeVersion = "opencode 1.0.0" } = {}) {
  const home = mkdtempSync(join(tmpdir(), "cave-native-enable-"));
  const bin = join(home, "bin");
  mkdirSync(bin, { recursive: true });
  for (const agent of ["claude", "codex", "hermes", "gemini", "aider"]) {
    writeFileSync(join(bin, agent), `#!/bin/sh\nif [ "$1" = "--version" ]; then echo '${agent} 1.0.0'; fi\n`, { mode: 0o755 });
  }
  writeFileSync(join(bin, "opencode"), `#!/bin/sh\nif [ "$1" = "--version" ]; then echo '${opencodeVersion}'; fi\n`, { mode: 0o755 });
  const mcp = join(bin, "caveman-mcp");
  writeFileSync(mcp, `#!/bin/sh
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  printf '%s\n' '{"version":"1.0.0","capabilities":["mcp_recovery"]}'
fi
`, { mode: 0o755 });
  const proxy = join(bin, "caveman-proxy");
  // The bare-spawn branch (no "version --json" args — how enable/wrap actually
  // launch the proxy) only records anything when CAVEMAN_PROXY_SPAWN_LOG is
  // set, so it stays silent for the other tests in this file that never opt in.
  writeFileSync(proxy, `#!/bin/sh
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  printf '%s\n' '{"version":"1.0.0","capabilities":["run_state","native_runtime_v1","native_hook_bridge_v1","typed_ccr"]}'
elif [ -n "$CAVEMAN_PROXY_SPAWN_LOG" ]; then
  printf 'listen=%s recovery=%s owner=%s\n' "$CAVEMAN_LISTEN" "$CAVEMAN_RECOVERY" "$CAVEMAN_PROXY_OWNER" >> "$CAVEMAN_PROXY_SPAWN_LOG"
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
} else if (event === "UserPromptSubmit") {
  process.stdout.write(JSON.stringify({ hookSpecificOutput: { additionalContext: "prompt hint fixture" } }));
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
  // Default local anthropic upstream is api.anthropic.com, so the install must
  // assert first-party or Claude Code shrinks the context/auto-compact window
  // to 200k behind the proxy route (#865).
  assert.equal(settings.env._CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL, "1");
  // Redirecting the base URL makes Claude Code drop tool search and inline every
  // MCP tool schema, so enable must restore it alongside the route.
  assert.equal(settings.env.ENABLE_TOOL_SEARCH, "auto");
  assert.match(installedBytes, /native-hook-fast\.js/);
  assert.match(installedBytes, /native-hook claude/);
  assert.match(installedBytes, /caveman-proxy/);
  assert.match(installedBytes, /shrink-hook/);
  assert.match(JSON.stringify(settings.hooks.PreToolUse), /native-hook claude/);
  assert.match(JSON.stringify(settings.hooks.PostToolUseFailure), /native-hook claude/);
  assert.equal(settings.hooks.PostCompact, undefined, "Claude compact context is delivered by SessionStart source=compact");
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
  assert.equal(after.env._CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL, undefined);
  assert.equal(after.env.ENABLE_TOOL_SEARCH, undefined, "disable must withdraw the tool-search default it introduced");
  assert.equal(after.theme, "dark");
  assert.match(JSON.stringify(after), /later-user-hook/);
  assert.doesNotMatch(JSON.stringify(after), /native-hook|shrink-hook/);
  const afterMcp = JSON.parse(readFileSync(mcpPath, "utf8"));
  assert.equal(afterMcp.mcpServers.other.command, "other-mcp");
  assert.equal(afterMcp.mcpServers.later.command, "later-mcp");
  assert.equal(afterMcp.mcpServers.caveman, undefined);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")), false);
});

test("enable claude preserves a user-set first-party assertion across enable and disable", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".claude"), { recursive: true });
  const settingsPath = join(fx.home, ".claude", "settings.json");
  writeFileSync(settingsPath, JSON.stringify({
    env: { _CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL: "0" },
  }, null, 2) + "\n");

  const enabled = await run(["enable", "claude"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);
  const settings = JSON.parse(readFileSync(settingsPath, "utf8"));
  assert.equal(settings.env._CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL, "0", "user value must survive enable");

  const disabled = await run(["disable", "claude"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  const after = JSON.parse(readFileSync(settingsPath, "utf8"));
  assert.equal(after.env._CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL, "0", "user value must survive disable");
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
  assert.match(installed, /base_url = "http:\/\/127\.0\.0\.1:8787\/w\/codex\/v1"/);
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

// The tables block ends with [mcp_servers.caveman] followed by the end marker.
// The legacy table stripper used to run before marker removal and swallowed
// that end marker, so the block enable had itself written was rejected as
// "corrupted" on the very next enable/wrap, and doctor reported drift forever.
test("enable codex twice re-parses its own block instead of calling it corrupted", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const configPath = join(fx.home, ".codex", "config.toml");
  writeFileSync(configPath, 'approval_policy = "never"\n');
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  const first = readFileSync(configPath, "utf8");
  const again = await run(["enable", "codex"], fx.env);
  assert.equal(again.code, 0, again.stderr);
  assert.doesNotMatch(again.stderr, /corrupted/);
  const second = readFileSync(configPath, "utf8");
  assert.equal(second.split("# >>> caveman:native-tables").length, 2, "exactly one tables begin marker");
  assert.equal(second.split("# <<< caveman:native-tables").length, 2, "exactly one tables end marker");
  assert.equal(second, first, "a second enable is byte-idempotent");
});

// An upgrade or a moved install changes the binary path inside the hook
// command. That is the same hook, so enable must replace the stale entry, not
// append a second (then third) caveman hook per event.
test("enable codex after a binary path change keeps one caveman hook per event", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  writeFileSync(join(fx.home, ".codex", "config.toml"), 'approval_policy = "never"\n');
  writeFileSync(join(fx.home, ".codex", "hooks.json"), JSON.stringify({ hooks: {} }, null, 2) + "\n");
  const first = await run(["enable", "codex"], fx.env);
  assert.equal(first.code, 0, first.stderr);
  const movedProxy = join(fx.home, "moved-caveman-proxy");
  writeFileSync(movedProxy, readFileSync(fx.env.CAVEMAN_PROXY_BIN), { mode: 0o755 });
  // The journal from the earlier install is gone (a different CAVEMAN_HOME, a
  // reinstall), so enable cannot refuse as "degraded" and must merge into the
  // host file it finds. This is the flow that stacked three hook sets.
  rmSync(join(fx.home, ".caveman", "integrations", "codex.json"), { force: true });
  const again = await run(["enable", "codex"], { ...fx.env, CAVEMAN_PROXY_BIN: movedProxy });
  assert.equal(again.code, 0, again.stderr);
  const hooks = JSON.parse(readFileSync(join(fx.home, ".codex", "hooks.json"), "utf8")).hooks;
  for (const [event, list] of Object.entries(hooks)) {
    const native = list.filter((entry) => JSON.stringify(entry).includes("native-hook codex"));
    assert.equal(native.length, 1, `${event} has ${native.length} caveman native hooks`);
    assert.match(JSON.stringify(native[0]), /moved-caveman-proxy/, `${event} must point at the current binary`);
  }
});

// enable writes config.toml to route every Codex request through the local
// proxy but, until this test, nothing asserted the proxy is actually spawned
// — config.toml pointed at a proxy that might never be running (#1051). Also
// guards the two follow-up review findings on that fix: CAVEMAN_RECOVERY must
// be recomputed, never let a stray inherited value survive into the spawned
// proxy's env, and CAVEMAN_PROXY_OWNER must be "wrap" (the hook-revived
// proxy's 30-minute-idle-exit lifecycle), not the immortal one "start" gets.
test("enable codex spawns the local proxy with explicit recovery/owner, not inherited env", async () => {
  const fx = fixture();
  const spawnLog = join(fx.home, "proxy-spawn.log");
  // A stray CAVEMAN_RECOVERY in the parent env (left over from an earlier
  // wrap/start in the same shell) must not leak into the proxy this spawns —
  // the fixture's caveman-mcp stub reports mcp_recovery, so the correctly
  // recomputed value is "mcp"; this planted value is neither that nor empty,
  // so it only proves anything if it does NOT show up in the log.
  const env = { ...fx.env, CAVEMAN_PROXY_SPAWN_LOG: spawnLog, CAVEMAN_RECOVERY: "stale-leaked-value" };
  mkdirSync(join(fx.home, ".codex"), { recursive: true });

  const out = await run(["enable", "codex"], env);
  assert.equal(out.code, 0, out.stderr);

  // The spawn is fire-and-forget from enable's own perspective; give the
  // detached stub a moment to write its log line.
  for (let i = 0; i < 20 && !existsSync(spawnLog); i++) {
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  assert.ok(existsSync(spawnLog), "enable never spawned the local proxy");
  const logged = readFileSync(spawnLog, "utf8").trim();
  assert.match(logged, /listen=127\.0\.0\.1:8787\b/, "spawned proxy must listen where config.toml just routed Codex to");
  assert.match(logged, /recovery=mcp\b/, "CAVEMAN_RECOVERY must be recomputed from the current MCP install, not inherited");
  assert.doesNotMatch(logged, /stale-leaked-value/, "a stray parent-env CAVEMAN_RECOVERY must never survive into the spawn");
  assert.match(logged, /owner=wrap\b/, "enable's proxy must share the hook-revived (wrap) lifecycle, not the immortal one \"start\" gets");
});

// Re-running `enable` is exactly what someone does when the route is dead, so
// the installed-state branch has to reach the proxy startup too. It returns
// "already" before the mutation work, so gating startup on a fresh install made
// it an accidental side effect of the first install rather than something the
// command does.
test("a second enable still starts the proxy when nothing is listening", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });

  const first = await run(["enable", "codex"], fx.env);
  assert.equal(first.code, 0, first.stderr);

  // Only now start recording, so the log can only contain the second run's spawn.
  const spawnLog = join(fx.home, "proxy-spawn-second.log");
  const second = await run(["enable", "codex"], { ...fx.env, CAVEMAN_PROXY_SPAWN_LOG: spawnLog });
  assert.equal(second.code, 0, second.stderr);
  assert.match(second.stderr, /already enabled/, "precondition: the second run must take the installed-state branch");

  for (let i = 0; i < 20 && !existsSync(spawnLog); i++) {
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  assert.ok(existsSync(spawnLog), "a repeated enable must still revive a dead proxy");
  assert.match(readFileSync(spawnLog, "utf8").trim(), /listen=127\.0\.0\.1:8787\b/);
});

// Every other spawn site (agentShortcut, the native hook) gates on !opts.noProxy.
test("enable does not start the proxy when the user's config disables it", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  mkdirSync(join(fx.home, ".caveman-cloud"), { recursive: true });
  writeFileSync(
    join(fx.home, ".caveman-cloud", "config.json"),
    JSON.stringify({ wrap: { proxy: false } }, null, 2),
  );

  const spawnLog = join(fx.home, "proxy-spawn-disabled.log");
  const out = await run(["enable", "codex"], { ...fx.env, CAVEMAN_PROXY_SPAWN_LOG: spawnLog });
  assert.equal(out.code, 0, out.stderr);

  // Give a spawn that should never happen the same grace the positive test gives one.
  await new Promise((resolve) => setTimeout(resolve, 600));
  assert.equal(existsSync(spawnLog), false, "enable must not start a proxy the config switched off");
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

// `caveman enable codex` still writes a shrink-hook entry into ~/.codex/hooks.json,
// but since #1037 that hook declines every Codex tool event. Reporting the component
// off a substring of the hooks file therefore claimed a rewrite that no longer
// happens. Codex is an installed, healthy integration WITHOUT command-output rewrite.
test("doctor does not claim a Codex tool rewrite that shrink-hook declines", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  writeFileSync(join(fx.home, ".codex", "config.toml"), 'approval_policy = "never"\n');
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  const out = await run(["doctor", "codex"], fx.env);
  const result = JSON.parse(out.stdout);
  assert.equal(result.components.tool_rewrite, false, "Codex commands are no longer rewritten");
  // The rest of the integration is untouched: this is a claim fix, not a downgrade.
  assert.equal(result.components.lifecycle_hooks, true);
  assert.equal(result.components.routing, true);
});

// Everyone who ran `caveman enable codex` on an api key before #1045 has the
// route-less base_url in ~/.codex/config.toml and a 404 on every `codex exec`.
// They must land in the state the CLI already knows how to fix, not in a
// silently-wrong install that reads healthy.
test("a codex install carrying the pre-/v1 route reads degraded and repairs to /v1", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const configPath = join(fx.home, ".codex", "config.toml");
  writeFileSync(join(fx.home, ".codex", "auth.json"), JSON.stringify({ OPENAI_API_KEY: "sk-local" }));
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  assert.match(readFileSync(configPath, "utf8"), /base_url = "http:\/\/127\.0\.0\.1:8787\/w\/codex\/v1"/);

  // Rewind to exactly what the old writer produced, journal included.
  const journalPath = join(fx.home, ".caveman", "integrations", "codex.json");
  const rewind = (text) => text.replaceAll("/w/codex/v1", "/w/codex");
  writeFileSync(configPath, rewind(readFileSync(configPath, "utf8")));
  writeFileSync(journalPath, rewind(readFileSync(journalPath, "utf8")));

  const doctor = await run(["doctor", "codex"], fx.env);
  assert.notEqual(doctor.code, 0);
  const result = JSON.parse(doctor.stdout);
  assert.equal(result.state, "degraded");
  assert.equal(result.components.routing, false);
  assert.equal(result.repair, "caveman doctor codex --fix");

  assert.equal((await run(["doctor", "codex", "--fix"], fx.env)).code, 0);
  assert.match(readFileSync(configPath, "utf8"), /base_url = "http:\/\/127\.0\.0\.1:8787\/w\/codex\/v1"/);
  assert.equal(JSON.parse((await run(["doctor", "codex"], fx.env)).stdout).state, "installed");
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

test("a native install honors think.shrink=false, and a repair keeps the entry out", async () => {
  const fx = fixture();

  // Accept control first: with the rewrite ON the entry is written, so the
  // assertions below separate "off is honored" from "nothing was written".
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const settingsPath = join(fx.home, ".claude", "settings.json");
  assert.match(readFileSync(settingsPath, "utf8"), /shrink-hook/);

  const configDir = join(fx.home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "config.json"), JSON.stringify({ think: { shrink: false } }, null, 2));

  // Turning the switch off makes the existing install genuinely out of sync,
  // and doctor says so instead of calling an unwanted entry healthy.
  const degraded = await run(["doctor", "claude"], fx.env);
  assert.notEqual(degraded.code, 0);
  assert.equal(JSON.parse(degraded.stdout).state, "degraded");

  // ...and the repair the CLI itself recommends now HONORS the choice. Before
  // #1049 this is where the manual removal was undone: --fix rewrote the entry
  // back in, every time, and `caveman disable` was the only way out.
  const repaired = await run(["doctor", "claude", "--fix"], fx.env);
  assert.equal(repaired.code, 0, repaired.stderr);
  const afterFix = readFileSync(settingsPath, "utf8");
  assert.doesNotMatch(afterFix, /shrink-hook/, "doctor --fix must honor think.shrink=false");
  // ...and takes nothing else with it.
  assert.match(afterFix, /native-hook claude/);
  assert.equal(JSON.parse(afterFix).env.ANTHROPIC_BASE_URL, "http://127.0.0.1:8787/w/claude");

  // The install is healthy again, so nothing keeps nagging the user to --fix.
  const healthy = await run(["doctor", "claude"], fx.env);
  assert.equal(JSON.parse(healthy.stdout).state, "installed");

  // A second repair is a no-op rather than a reinstatement.
  assert.equal((await run(["doctor", "claude", "--fix"], fx.env)).code, 0);
  assert.doesNotMatch(readFileSync(settingsPath, "utf8"), /shrink-hook/);

  // And turning it back on is still a one-command round trip.
  writeFileSync(join(configDir, "config.json"), JSON.stringify({ think: { shrink: true } }, null, 2));
  assert.equal((await run(["doctor", "claude", "--fix"], fx.env)).code, 0);
  assert.match(readFileSync(settingsPath, "utf8"), /shrink-hook/);
});

test("the env switch honors think.shrink=false the same way", async () => {
  const fx = fixture();
  const off = { ...fx.env, CAVEMAN_SHRINK: "0" };
  assert.equal((await run(["enable", "claude"], off)).code, 0);
  const settings = readFileSync(join(fx.home, ".claude", "settings.json"), "utf8");
  assert.doesNotMatch(settings, /shrink-hook/);
  assert.match(settings, /native-hook claude/);
});

test("a shrink entry an earlier install left behind does not survive think.shrink=false", async () => {
  const fx = fixture();
  // A standalone/plugin install, or any caveman old enough to predate #1049,
  // leaves this entry in the host file. `enable` merges into that file rather
  // than starting from an empty one, so honoring the switch only on the
  // entries we ADD leaves the rewrite live on exactly the machines that asked
  // for it to be off — and the brand new install is born degraded, because
  // nativeHookEntriesHealthy rejects a managed entry the expected document lacks.
  mkdirSync(join(fx.home, ".claude"), { recursive: true });
  const settingsPath = join(fx.home, ".claude", "settings.json");
  writeFileSync(settingsPath, JSON.stringify({
    hooks: { PreToolUse: [{ hooks: [{ type: "command", command: "/usr/local/bin/caveman shrink-hook" }] }] },
  }, null, 2));
  writeFileSync(join(fx.home, ".claude", "keep.txt"), "unrelated");

  const configDir = join(fx.home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "config.json"), JSON.stringify({ think: { shrink: false } }, null, 2));

  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const settings = readFileSync(settingsPath, "utf8");
  assert.doesNotMatch(settings, /shrink-hook/, "a stale shrink entry must be withdrawn, not merged through");
  assert.match(settings, /native-hook claude/);

  // Born healthy, not degraded — otherwise the very next `caveman enable`
  // refuses and the user is told to repair an install nothing broke.
  const doctor = await run(["doctor", "claude"], fx.env);
  assert.equal(JSON.parse(doctor.stdout).state, "installed");

  // ...and `disable` still restores the host file it found, stale entry included.
  assert.equal((await run(["disable", "claude"], fx.env)).code, 0);
  assert.match(readFileSync(settingsPath, "utf8"), /shrink-hook/);
});

test("the degraded gate names the repair that actually repairs", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const configDir = join(fx.home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "config.json"), JSON.stringify({ think: { shrink: false } }, null, 2));

  // `caveman doctor claude` alone only prints JSON saying `degraded`; nothing in
  // it says how to get out. Pointing at the bare command dead-ends the user.
  const blocked = await run(["enable", "claude"], fx.env);
  assert.equal(blocked.code, 1);
  assert.match(blocked.stderr, /caveman doctor claude --fix/);
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
  settings.hooks.SessionStart = [{ hooks: [{ type: "command", command: "echo native-hook claude", timeout: 30 }] }];
  writeFileSync(path, JSON.stringify(settings, null, 2) + "\n");
  const out = await run(["doctor", "claude"], fx.env);
  assert.notEqual(out.code, 0);
  const result = JSON.parse(out.stdout);
  assert.equal(result.state, "degraded");
  assert.equal(result.components.lifecycle_hooks, false);
});

test("doctor and disable tolerate executable path drift with unchanged hook semantics", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const path = join(fx.home, ".claude", "settings.json");
  const settings = JSON.parse(readFileSync(path, "utf8"));
  for (const entries of Object.values(settings.hooks)) {
    for (const entry of entries) {
      const hook = entry.hooks?.[0];
      if (typeof hook?.command === "string" && /native-hook claude|shrink-hook|mem recall-hook/.test(hook.command)) {
        hook.command = hook.command.includes("native-hook claude")
          ? hook.command
              .replace(/^.*?(?=native-hook claude)/, "'/new/caveman/bin/caveman-proxy' ")
              .replace(/--adapter\s+.*$/, "--adapter '/new/caveman/native-hook-fast.js'")
          : hook.command.replace(/^.*?(?=shrink-hook|mem recall-hook)/, "'/new/fnm/node' '/new/caveman/index.js' ");
      }
    }
  }
  writeFileSync(path, JSON.stringify(settings, null, 2) + "\n");

  const doctor = await run(["doctor", "claude"], fx.env);
  assert.equal(doctor.code, 0, doctor.stderr);
  assert.equal(JSON.parse(doctor.stdout).state, "installed");
  const disabled = await run(["disable", "claude"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  assert.doesNotMatch(readFileSync(path, "utf8"), /native-hook claude|shrink-hook|mem recall-hook/);
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
  assert.match(installed, /base_url: "http:\/\/127\.0\.0\.1:8787\/w\/hermes\/v1"/);
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
  const journal = JSON.parse(readFileSync(join(fx.home, ".caveman", "integrations", "hermes.json"), "utf8"));
  assert.equal(journal.operations.find((operation) => operation.kind === "hermes-config").owned.route, "http://127.0.0.1:8787/w/hermes/v1");
  const status = await run(["doctor", "hermes"], env);
  assert.equal(status.code, 0, status.stderr);
  assert.equal(JSON.parse(status.stdout).components.routing, true);

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
  assert.match(plugin, /export const CavemanNative/, "an OpenCode 1.x host keeps the V1 hook map (#1083)");
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

test("enable opencode on major 2 writes a V2 plugin whose setup hooks round-trip native calls", async () => {
  const fx = fixture({ opencodeVersion: "opencode 2.0.7" });
  const configDir = join(fx.home, ".config", "opencode");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "opencode.json"), JSON.stringify({}) + "\n");

  const enabled = await run(["enable", "opencode"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);
  const pluginPath = join(configDir, "plugins", "caveman-native.js");
  const plugin = readFileSync(pluginPath, "utf8");
  assert.match(plugin, /caveman:native-opencode/);
  assert.match(plugin, /id: "caveman-native"/, "the V2 plugin must carry a stable id (#1083)");
  assert.match(plugin, /async setup\(ctx\)/);
  assert.doesNotMatch(plugin, /export const CavemanNative/, "no V1 hook map on an OpenCode 2 host");
  const syntax = spawnSync(process.execPath, ["--check", pluginPath], { encoding: "utf8" });
  assert.equal(syntax.status, 0, syntax.stderr);

  const pluginModule = await import(`${pathToFileURL(pluginPath).href}?test=${Date.now()}`);
  assert.equal(pluginModule.default.id, "caveman-native");
  assert.equal(typeof pluginModule.default.setup, "function");

  const sessionHooks = new Map();
  const toolHooks = new Map();
  const scripted = [
    { type: "session.created", location: { directory: fx.home }, data: { sessionID: "oc2-1" } },
    { type: "session.created", location: { directory: "/elsewhere" }, data: { sessionID: "oc2-x" } },
    { type: "session.idle", location: { directory: fx.home }, data: { sessionID: "oc2-1" } },
    { type: "session.compaction.ended", location: { directory: fx.home }, data: { sessionID: "oc2-1" } },
    { type: "session.deleted", location: { directory: fx.home }, data: { sessionID: "oc2-1" } },
  ];
  const fakeCtx = {
    location: { directory: fx.home, workspaceID: undefined },
    event: { async *subscribe() { for (const event of scripted) yield event; } },
    session: { hook: async (name, cb) => { sessionHooks.set(name, cb); return { dispose: async () => {} }; } },
    tool: { hook: async (name, cb) => { toolHooks.set(name, cb); return { dispose: async () => {} }; } },
  };
  const readCapture = () => readFileSync(fx.env.CAVE_NATIVE_CAPTURE, "utf8").trim().split("\n").filter(Boolean)
    .map((line) => JSON.parse(Buffer.from(line, "base64").toString("utf8")));
  const flush = async () => { for (let i = 0; i < 25; i++) await new Promise((r) => setImmediate(r)); };
  const previousCapture = process.env.CAVE_NATIVE_CAPTURE;
  process.env.CAVE_NATIVE_CAPTURE = fx.env.CAVE_NATIVE_CAPTURE;
  writeFileSync(fx.env.CAVE_NATIVE_CAPTURE, "");
  try {
    const cleanup = await pluginModule.default.setup(fakeCtx);
    assert.deepEqual([...sessionHooks.keys()].sort(), ["compaction", "context", "prompt"]);
    assert.deepEqual([...toolHooks.keys()].sort(), ["execute.after", "execute.before"]);
    await flush();
    assert.deepEqual(readCapture().map((item) => [item.event_name, item.session_id]), [
      ["SessionStart", "oc2-1"],
      ["Stop", "oc2-1"],
      ["PostCompact", "oc2-1"],
      ["SessionEnd", "oc2-1"],
    ]);

    writeFileSync(fx.env.CAVE_NATIVE_CAPTURE, "");
    await sessionHooks.get("prompt")({ sessionID: "oc2-2", prompt: { text: "Yes, add billing support" } });
    await sessionHooks.get("prompt")({ sessionID: "oc2-2", prompt: { text: "fix that" } });
    const profiles = readCapture();
    assert.equal(profiles.length, 2, JSON.stringify(profiles));
    assert.equal(profiles[0].task_continuation, false);
    assert.equal(profiles[1].task_continuation, true);

    const system = { sessionID: "oc2-2", system: [] };
    await sessionHooks.get("context")(system);
    assert.deepEqual(system.system, [
      { type: "text", text: "Caveman Core fixture" },
      { type: "text", text: "prompt hint fixture" },
    ]);
    const once = { sessionID: "oc2-2", system: [] };
    await sessionHooks.get("context")(once);
    assert.deepEqual(once.system, [{ type: "text", text: "Caveman Core fixture" }]);

    const shrinkable = { tool: "shell", sessionID: "oc2-2", input: { command: "git status" } };
    await toolHooks.get("execute.before")(shrinkable);
    assert.equal(shrinkable.input.command, "caveman shrink -- git status");
    const legacy = { tool: "bash", sessionID: "oc2-2", input: { command: "git status" } };
    await toolHooks.get("execute.before")(legacy);
    assert.equal(legacy.input.command, "caveman shrink -- git status");
    const other = { tool: "read", sessionID: "oc2-2", input: { filePath: "x" } };
    await toolHooks.get("execute.before")(other);
    assert.deepEqual(other.input, { filePath: "x" });

    const replaced = { status: "completed", tool: "shell", sessionID: "oc2-2", input: {}, result: { content: "large exact output" } };
    await toolHooks.get("execute.after")(replaced);
    assert.equal(replaced.result.content, "[CommandResult] full: ccr://fixture");
    const failed = { status: "error", tool: "shell", sessionID: "oc2-2", error: { message: "x" } };
    await toolHooks.get("execute.after")(failed);
    assert.equal(failed.result, undefined);

    writeFileSync(fx.env.CAVE_NATIVE_CAPTURE, "");
    const compacting = { sessionID: "oc2-3", system: [] };
    await sessionHooks.get("compaction")(compacting);
    assert.deepEqual(compacting.system, [{ type: "text", text: "Caveman Core fixture" }]);
    assert.deepEqual(readCapture().map((item) => [item.event_name, item.session_id]), [
      ["PreCompact", "oc2-3"],
      ["SessionStart", "oc2-3"],
    ]);

    writeFileSync(fx.env.CAVE_NATIVE_CAPTURE, "");
    await cleanup();
    assert.deepEqual(readCapture().map((item) => [item.event_name, item.session_id]).sort(), [
      ["SessionEnd", "oc2-2"],
      ["SessionEnd", "oc2-3"],
    ]);
  } finally {
    if (previousCapture === undefined) delete process.env.CAVE_NATIVE_CAPTURE;
    else process.env.CAVE_NATIVE_CAPTURE = previousCapture;
  }
});

test("enable opencode with an unreadable version keeps the V1 plugin", async () => {
  // nativeHostProbe reports version: null whenever `opencode --version` yields
  // nothing, exits non-zero, or cannot be spawned ("version_probe_failed").
  // #1081 records that state on a live OpenCode 1.18.31 host, so "unknown" is
  // not a proxy for "new": defaulting it to V2 would hand a 1.x user whose
  // probe merely flaked a plugin their host cannot load, breaking an install
  // that works today. Unknown therefore keeps the status quo (V1); only a
  // version that positively reads as major >= 2 opts into the V2 API.
  const fx = fixture({ opencodeVersion: "" });
  const configDir = join(fx.home, ".config", "opencode");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "opencode.json"), JSON.stringify({}) + "\n");

  const enabled = await run(["enable", "opencode"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);
  const plugin = readFileSync(join(configDir, "plugins", "caveman-native.js"), "utf8");
  assert.match(plugin, /export const CavemanNative/,
    "an unreadable version must not silently upgrade a V1 host to the V2 API (#1083, #1081)");
  assert.doesNotMatch(plugin, /async setup\(ctx\)/);
});

test("doctor reports opencode degraded after the host upgrades past the installed plugin API", async () => {
  // The plugin API is chosen while building native mutations, so a V1 install
  // stays on disk after the host becomes V2 — and `caveman opencode` skips
  // enableNative whenever a journal exists, by design (status probes spawn
  // subprocesses). That makes doctor the repair door for this drift, exactly
  // as the comment on that skip says. Before this check, doctor compared
  // journaled bytes and pack version only, never the installed plugin API
  // against the current host major, so it called a plugin OpenCode 2 refuses
  // to load "installed".
  const fx = fixture({ opencodeVersion: "opencode 1.18.31" });
  const configDir = join(fx.home, ".config", "opencode");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "opencode.json"), JSON.stringify({}) + "\n");

  assert.equal((await run(["enable", "opencode"], fx.env)).code, 0);
  const pluginPath = join(configDir, "plugins", "caveman-native.js");
  assert.match(readFileSync(pluginPath, "utf8"), /export const CavemanNative/, "V1 host gets the V1 plugin");
  assert.equal(JSON.parse((await run(["doctor", "opencode"], fx.env)).stdout).state, "installed");

  // The user upgrades OpenCode. Nothing else changes: same journal, same bytes.
  writeFileSync(join(fx.home, "bin", "opencode"),
    `#!/bin/sh\nif [ "$1" = "--version" ]; then echo 'opencode 2.0.7'; fi\n`, { mode: 0o755 });

  const doctor = await run(["doctor", "opencode"], fx.env);
  assert.notEqual(doctor.code, 0, "a plugin the host cannot load must not report healthy");
  const result = JSON.parse(doctor.stdout);
  assert.equal(result.state, "degraded");
  assert.equal(result.components.lifecycle_hooks, false);
  assert.equal(result.repair, "caveman doctor opencode --fix");

  assert.equal((await run(["doctor", "opencode", "--fix"], fx.env)).code, 0);
  assert.match(readFileSync(pluginPath, "utf8"), /async setup\(ctx\)/, "--fix regenerates against the new host major");
  assert.equal(JSON.parse((await run(["doctor", "opencode"], fx.env)).stdout).state, "installed");
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
