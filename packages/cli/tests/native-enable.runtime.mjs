import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { createServer as createHttpServer } from "node:http";
import { chmodSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, statSync, symlinkSync, unlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { createServer } from "node:net";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { isolatedCliEnv } from "./_cli.mjs";

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
elif [ "$1" = "status" ]; then
  # A Caveman runtime answers where agents are wired, unless the test stopped it.
  if [ -f "$CAVEMAN_HOME/runtime-stopped" ]; then printf '%s\n' '{"owner":"unknown"}'; else printf '%s\n' '{"owner":"start"}'; fi
elif [ $# -eq 0 ] && [ -f "$CAVEMAN_HOME/runtime-stopped" ]; then
  rm -f "$CAVEMAN_HOME/runtime-stopped"
elif [ -n "$CAVEMAN_PROXY_SPAWN_LOG" ]; then
  printf 'listen=%s recovery=%s owner=%s cwd=%s\n' "$CAVEMAN_LISTEN" "$CAVEMAN_RECOVERY" "$CAVEMAN_PROXY_OWNER" "$(pwd -P)" >> "$CAVEMAN_PROXY_SPAWN_LOG"
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
  const env = {
    ...process.env,
    HOME: home,
    USERPROFILE: home,
    CLAUDE_CONFIG_DIR: "",
    CODEX_HOME: "",
    GEMINI_CLI_HOME: "",
    HERMES_HOME: "",
    XDG_CONFIG_HOME: join(home, ".config"),
    CAVEMAN_HOME: join(home, ".caveman"),
    CAVEMAN_MCP_BIN: mcp,
    CAVEMAN_PROXY_BIN: proxy,
    // These tests are about wiring, not the port check: a pinned address keeps a
    // machine that already runs something on 8787 from moving the runtime.
    CAVEMAN_LISTEN: "127.0.0.1:8787",
    // Full CLI suite runs several process-heavy files concurrently. Keep this
    // fixture's valid shell probes distinct from dedicated 2s hung-probe tests.
    CAVE_BINARY_PROBE_TIMEOUT_MS: "10000",
    CAVEMAN_TELEMETRY: "0",
    CAVE_NATIVE_CAPTURE: join(home, "native-capture.jsonl"),
    NO_COLOR: "1",
    PATH: `${bin}:${process.env.PATH}`,
  };
  // Codex auth mode comes from auth.json alone; still keep the runner's own
  // OPENAI_API_KEY out so no fixture depends on the shell it runs from.
  delete env.OPENAI_API_KEY;
  return { home, env };
}

function run(argv, env, input = undefined, cwd = undefined) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env, cwd });
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
  // Codex has no PostToolUseFailure event (hooks/list drops it), and shrink-hook
  // declines every Codex tool call (#1037): neither is written.
  assert.equal(installedHooks.PostToolUseFailure, undefined);
  assert.doesNotMatch(JSON.stringify(installedHooks), /shrink-hook/);

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

// Codex rewrites config.toml itself (toml_edit): `codex mcp add` regroups the
// mcp_servers tables around Caveman's, `codex features enable` appends a table
// between Caveman's markers, and a Windows path comes back 'literal'-quoted.
// None of that changes what Caveman wrote, so doctor must stay installed and
// disable must take out Caveman's items alone instead of refusing.
test("codex rewriting config.toml around Caveman's tables keeps doctor and disable working", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const configPath = join(fx.home, ".codex", "config.toml");
  writeFileSync(configPath, 'approval_policy = "never"\n');
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  writeFileSync(configPath, readFileSync(configPath, "utf8")
    .replace(/^command = "([^"]+)"$/m, "command = '$1'")
    .replace("[mcp_servers.caveman]", '[mcp_servers.github]\ncommand = "npx"\n\n[mcp_servers.caveman]')
    .replace("# <<< caveman:native-tables", '\n[features]\nartifact = true\n\n[[skills.config]]\npath = "/x/SKILL.md"\nenabled = false\n# <<< caveman:native-tables'));
  assert.equal(JSON.parse((await run(["doctor", "codex"], fx.env)).stdout).state, "installed");
  const disabled = await run(["disable", "codex"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  const after = readFileSync(configPath, "utf8");
  assert.doesNotMatch(after, /caveman/);
  assert.match(after, /^approval_policy = "never"$/m);
  assert.match(after, /^\[mcp_servers\.github\]\ncommand = "npx"$/m);
  assert.match(after, /^\[features\]\nartifact = true$/m);
  assert.match(after, /^\[\[skills\.config\]\]\npath = "\/x\/SKILL\.md"\nenabled = false$/m);
});

// Enable takes the root model_provider line out to route through Caveman.
// Disable has to put it back even after Codex saved something else in the file
// (a /model choice, a trusted folder). Enable now leaves a provider of the
// user's own (Azure, Ollama) alone, but installs from earlier CLIs replaced it,
// and disable restores through the same backup either way.
test("disable codex restores the user's own model_provider after Codex edited config.toml", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const configPath = join(fx.home, ".codex", "config.toml");
  writeFileSync(configPath, 'model = "gpt-5.5"\nmodel_provider = "openai"  # pinned on purpose\n');
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  assert.doesNotMatch(readFileSync(configPath, "utf8"), /"openai"/);
  writeFileSync(configPath, readFileSync(configPath, "utf8").replace('model = "gpt-5.5"', 'model = "gpt-5.6"'));
  const disabled = await run(["disable", "codex"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  const after = readFileSync(configPath, "utf8");
  assert.match(after, /^model_provider = "openai"  # pinned on purpose$/m);
  assert.match(after, /^model = "gpt-5\.6"$/m);
  assert.doesNotMatch(after, /caveman/);
});

// PowerShell 5.1 and older Notepad save UTF-8 with a BOM. Codex accepts one at
// the start of config.toml, but not in the middle, where prepending Caveman's
// root block used to leave it: Codex then refused to start at all.
test("enable and disable codex keep a UTF-8 BOM at the start of config.toml", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const configPath = join(fx.home, ".codex", "config.toml");
  writeFileSync(configPath, '\uFEFFmodel = "gpt-5.5"\r\n\r\n[mcp_servers.github]\r\ncommand = "npx"\r\n');
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  const installed = readFileSync(configPath, "utf8");
  assert.ok(installed.startsWith("\uFEFF# >>> caveman:native-root\n"), JSON.stringify(installed.slice(0, 40)));
  assert.equal(installed.indexOf("\uFEFF", 1), -1, "no BOM after offset 0");
  writeFileSync(configPath, `${installed}# later\n`);
  assert.equal((await run(["disable", "codex"], fx.env)).code, 0);
  const after = readFileSync(configPath, "utf8");
  assert.ok(after.startsWith('\uFEFFmodel = "gpt-5.5"'), JSON.stringify(after.slice(0, 40)));
  assert.equal(after.indexOf("\uFEFF", 1), -1, "no BOM after offset 0");
});

// Codex never reads OPENAI_API_KEY while auth.json holds a ChatGPT login: its
// stored auth_mode (or a key saved in auth.json) decides. A key exported in the
// shell must not wire the api-key route, nor flip doctor from shell to shell.
test("a codex ChatGPT login stays on the subscription route with OPENAI_API_KEY exported", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const configPath = join(fx.home, ".codex", "config.toml");
  writeFileSync(join(fx.home, ".codex", "auth.json"), JSON.stringify({ auth_mode: "chatgpt", OPENAI_API_KEY: null, tokens: { id_token: "z", access_token: "x", refresh_token: "y", account_id: "acc" } }));
  const keyed = { ...fx.env, OPENAI_API_KEY: "sk-env" };
  assert.equal((await run(["enable", "codex"], keyed)).code, 0);
  assert.match(readFileSync(configPath, "utf8"), /base_url = "http:\/\/127\.0\.0\.1:8787\/chatgpt"/);
  for (const env of [keyed, fx.env]) {
    assert.equal(JSON.parse((await run(["doctor", "codex"], env)).stdout).components.routing, true);
  }
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
async function unusedGateway() {
  const server = createServer();
  await new Promise((resolve, reject) => { server.once("error", reject); server.listen(0, "127.0.0.1", resolve); });
  const url = `http://127.0.0.1:${server.address().port}`;
  await new Promise((resolve) => server.close(resolve));
  return url;
}

test("enable codex spawns the local proxy with explicit recovery/owner, not inherited env", async () => {
  const fx = fixture();
  const spawnLog = join(fx.home, "proxy-spawn.log");
  // A stray CAVEMAN_RECOVERY in the parent env (left over from an earlier
  // wrap/start in the same shell) must not leak into the proxy this spawns —
  // the fixture's caveman-mcp stub reports mcp_recovery, so the correctly
  // recomputed value is "mcp"; this planted value is neither that nor empty,
  // so it only proves anything if it does NOT show up in the log.
  const gateway = await unusedGateway();
  const env = { ...fx.env, CAVE_GATEWAY_URL: gateway, CAVEMAN_PROXY_SPAWN_LOG: spawnLog, CAVEMAN_RECOVERY: "stale-leaked-value" };
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
  assert.ok(logged.includes(`listen=${new URL(gateway).host} `), "spawned proxy must listen where config.toml just routed Codex to");
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
  fx.env.CAVE_GATEWAY_URL = await unusedGateway();
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
  assert.ok(readFileSync(spawnLog, "utf8").includes(`listen=${new URL(fx.env.CAVE_GATEWAY_URL).host} `));
});

// The runtime outlives the command that starts it. Started in a project, it
// would hold that directory: the volume cannot be ejected, and on Windows the
// folder cannot be deleted or renamed, until `caveman stop`.
test("the background runtime never keeps the project it was started from as its working directory", async () => {
  const fx = fixture();
  const project = mkdtempSync(join(tmpdir(), "cave-project-"));
  const spawnLog = join(fx.home, "proxy-spawn-cwd.log");
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  try {
    const out = await run(["enable", "codex"], { ...fx.env, CAVE_GATEWAY_URL: await unusedGateway(), CAVEMAN_PROXY_SPAWN_LOG: spawnLog }, undefined, project);
    assert.equal(out.code, 0, out.stderr);
    for (let i = 0; i < 20 && !existsSync(spawnLog); i++) {
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    assert.ok(existsSync(spawnLog), "enable never spawned the local proxy");
    const logged = readFileSync(spawnLog, "utf8");
    assert.ok(logged.includes(` cwd=${realpathSync(fx.home)}\n`), logged);
  } finally {
    rmSync(project, { recursive: true, force: true });
  }
});

// Every other spawn site (agentShortcut, the native hook) gates on !opts.noProxy.
test("enable does not start the proxy when the user's config disables it", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  mkdirSync(fx.env.CAVEMAN_HOME, { recursive: true });
  writeFileSync(
    join(fx.env.CAVEMAN_HOME, "cloud.json"),
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

// The installer's always-on Codex hook (`--only codex`) injects the caveman voice
// every session. Once Caveman wires Codex natively, output is the one injection:
// enable leaves the installer's entry out, and disable does not bring it back,
// so `caveman disable codex` / `off --all` leave Codex with none.
test("enable codex takes over from the installer's always-on hook, and disable leaves neither", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const hooksPath = join(fx.home, ".codex", "hooks.json");
  const foreign = { hooks: [{ type: "command", command: "echo foreign" }] };
  const installer = { matcher: "startup|resume|clear|compact", hooks: [{ type: "command", command: `node "${join(fx.home, ".codex", "caveman", "hooks", "codex-sessionstart.js")}"`, timeout: 5 }] };
  writeFileSync(hooksPath, JSON.stringify({ hooks: { SessionStart: [foreign, installer] } }, null, 2) + "\n");

  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  const wired = JSON.parse(readFileSync(hooksPath, "utf8")).hooks.SessionStart;
  assert.doesNotMatch(JSON.stringify(wired), /codex-sessionstart/);
  assert.deepEqual(wired[0], foreign);
  assert.match(JSON.stringify(wired), /native-hook codex/);
  assert.equal(JSON.parse((await run(["doctor", "codex"], fx.env)).stdout).state, "installed");

  const disabled = await run(["disable", "codex"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  assert.deepEqual(JSON.parse(readFileSync(hooksPath, "utf8")), { hooks: { SessionStart: [foreign] } });
});

// An install from before carries a shrink-hook entry (a second PreToolUse hook
// on every Codex tool call that declines it) and a PostToolUseFailure entry
// Codex never runs. Doctor sends it to --fix, which takes both out.
test("doctor --fix takes the shrink-hook and PostToolUseFailure entries out of an older Codex install", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const hooksPath = join(fx.home, ".codex", "hooks.json");
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  const hooks = JSON.parse(readFileSync(hooksPath, "utf8"));
  const native = hooks.hooks.SessionStart.find((entry) => /native-hook codex/.test(entry.hooks[0].command));
  hooks.hooks.PreToolUse.push({ hooks: [{ type: "command", command: `${join(fx.home, "bin", "caveman")} shrink-hook` }] });
  hooks.hooks.PostToolUseFailure = [native];
  writeFileSync(hooksPath, JSON.stringify(hooks, null, 2) + "\n");

  assert.equal(JSON.parse((await run(["doctor", "codex"], fx.env)).stdout).state, "degraded");
  const fixed = await run(["doctor", "codex", "--fix"], fx.env);
  assert.equal(fixed.code, 0, fixed.stdout + fixed.stderr);
  const after = JSON.parse(readFileSync(hooksPath, "utf8")).hooks;
  assert.equal(after.PostToolUseFailure, undefined);
  assert.doesNotMatch(JSON.stringify(after), /shrink-hook/);
  assert.equal(after.PreToolUse.length, 1);
});

// What Codex's /hooks records once the user trusts Caveman's SessionStart hook.
// The key names CODEX_HOME canonicalized when it is set, ~/.codex as is otherwise.
function trustCodexHooks(home, { canonical = true } = {}) {
  const hooksPath = join(home, ".codex", "hooks.json");
  const configPath = join(home, ".codex", "config.toml");
  const group = JSON.parse(readFileSync(hooksPath, "utf8")).hooks.SessionStart.findIndex((entry) => /native-hook codex/.test(entry.hooks[0].command));
  const key = `${canonical ? realpathSync(hooksPath) : hooksPath}:session_start:${group}:0`;
  writeFileSync(configPath, `${readFileSync(configPath, "utf8")}\n[hooks.state.${JSON.stringify(key)}]\ntrusted_hash = "sha256:test"\n`);
}

// Codex runs a hook from hooks.json only once the user trusts it in /hooks.
// Until then no Caveman hook runs: no Core, and nothing restarts the runtime
// after a reboot. Untrusted is the default (Caveman never trusts for the user),
// so it is not a broken install; doctor says it and how to trust them.
test("doctor codex reports Caveman's hooks untrusted until /hooks trusts them", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  const untrusted = JSON.parse((await run(["doctor", "codex"], fx.env)).stdout);
  assert.equal(untrusted.state, "installed");
  assert.equal(untrusted.components.lifecycle_hooks, false);
  assert.equal(untrusted.core_active, false);
  assert.equal(untrusted.capabilities.session_start.active, false);
  assert.deepEqual(untrusted.warnings, ["Caveman's hooks do not run until Codex trusts them · open /hooks in Codex once and trust them, so the local runtime restarts by itself"]);
  trustCodexHooks(fx.home, { canonical: false });
  const trusted = JSON.parse((await run(["doctor", "codex"], fx.env)).stdout);
  assert.equal(trusted.components.lifecycle_hooks, true);
  assert.equal(trusted.core_active, true);
  assert.equal(trusted.capabilities.session_start.active, true);
  assert.deepEqual(trusted.warnings, []);
});

// Codex declines command-output rewrite since #1037, so doctor never claims one.
test("doctor does not claim a Codex tool rewrite that shrink-hook declines", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  writeFileSync(join(fx.home, ".codex", "config.toml"), 'approval_policy = "never"\n');
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  trustCodexHooks(fx.home);
  const out = await run(["doctor", "codex"], fx.env);
  const result = JSON.parse(out.stdout);
  assert.equal(result.components.tool_rewrite, false, "Codex commands are no longer rewritten");
  // The rest of the integration is untouched: this is a claim fix, not a downgrade.
  assert.equal(result.components.lifecycle_hooks, true);
  assert.equal(result.components.routing, true);
});

// A reboot or `caveman stop` leaves Codex wired to a runtime that is down, and
// `codex exec` retries forever. Doctor says so instead of "installed", and
// --fix starts it the way enable does.
test("doctor codex reads degraded while the runtime is down, and --fix starts it", async () => {
  const fx = fixture();
  // Port 9: nothing listens there, so the runtime is the stub's, never this machine's 8787.
  const spawned = join(fx.home, "proxy-spawns.log");
  const env = { ...fx.env, CAVE_GATEWAY_URL: "http://127.0.0.1:9", CAVEMAN_LISTEN: "127.0.0.1:9", CAVEMAN_PROXY_SPAWN_LOG: spawned };
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  assert.equal((await run(["enable", "codex"], env)).code, 0);
  // Enable starts the runtime detached; it has to have run before it is stopped.
  for (let i = 0; i < 100 && !existsSync(spawned); i++) await new Promise((resolve) => setTimeout(resolve, 50));
  writeFileSync(join(fx.home, ".caveman", "runtime-stopped"), "");
  const down = await run(["doctor", "codex"], env);
  assert.notEqual(down.code, 0);
  const result = JSON.parse(down.stdout);
  assert.equal(result.state, "degraded");
  assert.equal(result.components.routing, false);
  assert.equal(result.components.shared_runtime, false);
  assert.equal(result.warnings[0], "the local runtime is not running · start it: caveman doctor codex --fix");
  const fixed = await run(["doctor", "codex", "--fix"], env);
  assert.equal(fixed.code, 0, fixed.stdout + fixed.stderr);
  assert.equal(JSON.parse(fixed.stdout).fix.result, "started");
  assert.equal(JSON.parse(fixed.stdout).state, "installed");
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

test("codex SessionStart hook self-heals a stale route after api-key to subscription auth switch", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const authPath = join(fx.home, ".codex", "auth.json");
  const configPath = join(fx.home, ".codex", "config.toml");
  writeFileSync(authPath, JSON.stringify({ OPENAI_API_KEY: "sk-local" }));
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  assert.match(readFileSync(configPath, "utf8"), /base_url = "http:\/\/127\.0\.0\.1:8787\/w\/codex\/v1"/);
  writeFileSync(authPath, JSON.stringify({ tokens: { account_id: "acct_1" } }));
  const hookOut = await run(["native-hook", "codex"], fx.env, JSON.stringify({ hook_event_name: "SessionStart", session_id: "s1" }));
  assert.equal(hookOut.code, 0, hookOut.stderr);
  assert.match(readFileSync(configPath, "utf8"), /base_url = "http:\/\/127\.0\.0\.1:8787\/chatgpt"/);
  const after = JSON.parse((await run(["doctor", "codex"], fx.env)).stdout);
  assert.equal(after.state, "installed");
  assert.equal(after.components.routing, true);
});

test("codex SessionStart hook self-heals a stale route after subscription to api-key auth switch", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const authPath = join(fx.home, ".codex", "auth.json");
  const configPath = join(fx.home, ".codex", "config.toml");
  writeFileSync(authPath, JSON.stringify({ tokens: { account_id: "acct_1" } }));
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  assert.match(readFileSync(configPath, "utf8"), /base_url = "http:\/\/127\.0\.0\.1:8787\/chatgpt"/);
  writeFileSync(authPath, JSON.stringify({ OPENAI_API_KEY: "sk-local" }));
  const hookOut = await run(["native-hook", "codex"], fx.env, JSON.stringify({ hook_event_name: "SessionStart", session_id: "s1" }));
  assert.equal(hookOut.code, 0, hookOut.stderr);
  assert.match(readFileSync(configPath, "utf8"), /base_url = "http:\/\/127\.0\.0\.1:8787\/w\/codex\/v1"/);
  const after = JSON.parse((await run(["doctor", "codex"], fx.env)).stdout);
  assert.equal(after.state, "installed");
  assert.equal(after.components.routing, true);
});

test("codex SessionStart hook leaves config alone when degraded for an unrelated reason", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  const authPath = join(fx.home, ".codex", "auth.json");
  const configPath = join(fx.home, ".codex", "config.toml");
  writeFileSync(authPath, JSON.stringify({ OPENAI_API_KEY: "sk-local" }));
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  const configBefore = readFileSync(configPath, "utf8");
  // Force a degraded state that has nothing to do with routing: mark the
  // installed pack as older than what this build ships, same as an in-place
  // CLI upgrade would leave behind. Routing itself is untouched and still
  // matches the current auth mode.
  const journalPath = join(fx.home, ".caveman", "integrations", "codex.json");
  const journal = JSON.parse(readFileSync(journalPath, "utf8"));
  journal.pack_version = "0.0.1";
  writeFileSync(journalPath, JSON.stringify(journal, null, 2));
  const before = JSON.parse((await run(["doctor", "codex"], fx.env)).stdout);
  assert.equal(before.state, "degraded");
  assert.equal(before.components.routing, true, "routing itself must still be healthy in this fixture");
  const hookOut = await run(["native-hook", "codex"], fx.env, JSON.stringify({ hook_event_name: "SessionStart", session_id: "s1" }));
  assert.equal(hookOut.code, 0, hookOut.stderr);
  assert.equal(readFileSync(configPath, "utf8"), configBefore, "config.toml must not be rewritten for non-routing drift");
});

test("codex SessionStart route check never spawns the codex binary when the route is current", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".codex"), { recursive: true });
  writeFileSync(join(fx.home, ".codex", "auth.json"), JSON.stringify({ OPENAI_API_KEY: "sk-local" }));
  assert.equal((await run(["enable", "codex"], fx.env)).code, 0);
  // The delegated SessionStart gets 3s in total; a `codex --version` probe
  // per launch spends part of that on every session start for nothing.
  const spawnLog = join(fx.home, "codex-spawns.log");
  writeFileSync(join(fx.home, "bin", "codex"), `#!/bin/sh\necho "$@" >> '${spawnLog}'\nif [ "$1" = "--version" ]; then echo 'codex 1.0.0'; fi\n`, { mode: 0o755 });
  const hookOut = await run(["native-hook", "codex"], fx.env, JSON.stringify({ hook_event_name: "SessionStart", session_id: "s1" }));
  assert.equal(hookOut.code, 0, hookOut.stderr);
  assert.equal(existsSync(spawnLog) ? readFileSync(spawnLog, "utf8") : "", "");
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

  const configDir = fx.env.CAVEMAN_HOME;
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "cloud.json"), JSON.stringify({ think: { shrink: false } }, null, 2));

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
  writeFileSync(join(configDir, "cloud.json"), JSON.stringify({ think: { shrink: true } }, null, 2));
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

  const configDir = fx.env.CAVEMAN_HOME;
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "cloud.json"), JSON.stringify({ think: { shrink: false } }, null, 2));

  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const settings = readFileSync(settingsPath, "utf8");
  assert.doesNotMatch(settings, /shrink-hook/, "a stale shrink entry must be withdrawn, not merged through");
  assert.match(settings, /native-hook claude/);

  // Born healthy, not degraded — otherwise the very next `caveman enable`
  // refuses and the user is told to repair an install nothing broke.
  const doctor = await run(["doctor", "claude"], fx.env);
  assert.equal(JSON.parse(doctor.stdout).state, "installed");

  // Disable withdraws stale Caveman hooks too; restoring the old hook would
  // silently re-enable part of the runtime the user explicitly turned off.
  assert.equal((await run(["disable", "claude"], fx.env)).code, 0);
  assert.doesNotMatch(readFileSync(settingsPath, "utf8"), /shrink-hook/);
});

test("the degraded gate names the repair that actually repairs", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const configDir = fx.env.CAVEMAN_HOME;
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "cloud.json"), JSON.stringify({ think: { shrink: false } }, null, 2));

  // `caveman doctor claude` alone only prints JSON saying `degraded`; nothing in
  // it says how to get out. Pointing at the bare command dead-ends the user.
  const blocked = await run(["enable", "claude"], fx.env);
  assert.equal(blocked.code, 1);
  assert.match(blocked.stderr, /caveman doctor claude --fix/);
});

test("doctor surfaces independently disabled Core without degrading native integration", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const configDir = fx.env.CAVEMAN_HOME;
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "cloud.json"), JSON.stringify({ think: { mode: "compress", core: false } }, null, 2));
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
  const configDir = fx.env.CAVEMAN_HOME;
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "cloud.json"), JSON.stringify({ think: { mode: "record", core: true } }, null, 2));
  const out = await run(["doctor", "claude"], fx.env);
  assert.equal(out.code, 0, out.stderr);
  const result = JSON.parse(out.stdout);
  assert.equal(result.core_configured, true);
  assert.equal(result.core_supported, true);
  assert.equal(result.core_active, false);
  assert.equal(result.core_enabled, false);
  assert.equal(result.coding_policy, "off");
  assert.equal(result.components.core, false);

  writeFileSync(join(configDir, "cloud.json"), JSON.stringify({ think: { mode: "compress", core: true } }, null, 2));
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
  // Drift is only tolerated while the relocated files exist (#1137).
  const moved = join(fx.home, "new");
  mkdirSync(join(moved, "caveman", "bin"), { recursive: true });
  mkdirSync(join(moved, "fnm"), { recursive: true });
  for (const file of ["caveman/bin/caveman-proxy", "fnm/node"]) writeFileSync(join(moved, file), "#!/bin/sh\n", { mode: 0o755 });
  for (const file of ["caveman/native-hook-fast.js", "caveman/index.js"]) writeFileSync(join(moved, file), "");
  const settings = JSON.parse(readFileSync(path, "utf8"));
  for (const entries of Object.values(settings.hooks)) {
    for (const entry of entries) {
      const hook = entry.hooks?.[0];
      if (typeof hook?.command === "string" && /native-hook claude|shrink-hook|mem recall-hook/.test(hook.command)) {
        hook.command = hook.command.includes("native-hook claude")
          ? hook.command
              .replace(/^.*?(?=native-hook claude)/, `'${moved}/caveman/bin/caveman-proxy' `)
              .replace(/--adapter\s+.*$/, `--adapter '${moved}/caveman/native-hook-fast.js'`)
          : hook.command.replace(/^.*?(?=shrink-hook|mem recall-hook)/, `'${moved}/fnm/node' '${moved}/caveman/index.js' `);
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

// A managed hook whose executable or --adapter file no longer exists (#1137,
// an nvm Node upgrade removing the versioned directory) is degraded, and
// doctor --fix re-renders it.
for (const [name, rewrite] of [
  ["adapter", (command, dead) => command.replace(/--adapter\s+.*$/, `--adapter '${dead}/native-hook-fast.js'`)],
  ["executable", (command, dead) => command.replace(/^.*?(?=native-hook claude|shrink-hook)/, `'${dead}/bin/${command.includes("shrink-hook") ? "caveman" : "caveman-proxy"}' `)],
  ["node", (command, dead) => command.replace(/--node\s+.*$/, `--node '${dead}/bin/node'`)],
]) {
  test(`doctor flags a managed hook whose ${name} no longer exists and --fix re-renders it`, async () => {
    const fx = fixture();
    assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
    const path = join(fx.home, ".claude", "settings.json");
    const dead = join(fx.home, ".nvm", "versions", "node", "v26.9.0");
    const settings = JSON.parse(readFileSync(path, "utf8"));
    for (const entries of Object.values(settings.hooks)) {
      for (const entry of entries) {
        const hook = entry.hooks?.[0];
        if (typeof hook?.command === "string" && /native-hook claude|shrink-hook/.test(hook.command)) {
          hook.command = rewrite(hook.command, dead);
        }
      }
    }
    writeFileSync(path, JSON.stringify(settings, null, 2) + "\n");
    assert.match(readFileSync(path, "utf8"), /v26\.9\.0/);

    const degraded = await run(["doctor", "claude"], fx.env);
    assert.notEqual(degraded.code, 0);
    assert.equal(JSON.parse(degraded.stdout).state, "degraded");

    const fixed = await run(["doctor", "claude", "--fix"], fx.env);
    assert.equal(fixed.code, 0, fixed.stderr);
    const result = JSON.parse(fixed.stdout);
    assert.equal(result.fix.result, "repaired");
    assert.equal(result.state, "installed");
    assert.doesNotMatch(readFileSync(path, "utf8"), /v26\.9\.0/);

    const disabled = await run(["disable", "claude"], fx.env);
    assert.equal(disabled.code, 0, disabled.stderr);
    assert.doesNotMatch(readFileSync(path, "utf8"), /native-hook claude|shrink-hook/);
  });
}

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

for (const args of [[], ["claude"], ["--all"]]) {
  test(`disable ${args.join(" ")} clears orphaned Claude profiles without a journal`, async () => {
    const fx = fixture();
    const custom = join(fx.home, "accounts", "work");
    const profiles = [".claude", ".claude-max20", ".claude_max5"].map((name) => join(fx.home, name));
    profiles.push(custom);
    const native = { type: "command", command: "'/deleted/test/bin/caveman-proxy' native-hook claude --adapter '/deleted/test/native-hook-fast.js'" };
    const keep = { type: "command", command: "caveman-blocks hook --harness claude" };
    for (const root of profiles) {
      mkdirSync(root, { recursive: true });
      writeFileSync(join(root, "settings.json"), JSON.stringify({
        theme: "keep", env: { KEEP: "yes", ANTHROPIC_BASE_URL: "http://127.0.0.1:8787/w/claude", _CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL: "1", ANTHROPIC_API_KEY: "sk-ant-preserve" },
        hooks: { SessionStart: [{ matcher: "startup", hooks: [native, keep] }] },
      }));
      writeFileSync(join(root, "settings.local.json"), JSON.stringify({ env: { ANTHROPIC_BASE_URL: "https://gateway.caveman.so/w/claude", ANTHROPIC_AUTH_TOKEN: "cave_live_fixture" } }));
      writeFileSync(join(root, ".claude.json"), JSON.stringify({ mcpServers: { caveman: { command: "/deleted/test/caveman-mcp" }, other: { command: "keep" } } }));
    }
    const alias = join(fx.home, ".claude-alias");
    mkdirSync(alias);
    symlinkSync(join(profiles[0], "settings.json"), join(alias, "settings.json"));
    const env = { ...fx.env, CLAUDE_CONFIG_DIR: custom };
    const out = await run(["disable", ...args], env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stderr, /Restart running Claude sessions/);
    for (const root of profiles) {
      assert.deepEqual(JSON.parse(readFileSync(join(root, "settings.json"), "utf8")), {
        theme: "keep", env: { KEEP: "yes", ANTHROPIC_API_KEY: "sk-ant-preserve" },
        hooks: { SessionStart: [{ matcher: "startup", hooks: [keep] }] },
      });
      assert.deepEqual(JSON.parse(readFileSync(join(root, "settings.local.json"), "utf8")), {});
      assert.deepEqual(JSON.parse(readFileSync(join(root, ".claude.json"), "utf8")), { mcpServers: { other: { command: "keep" } } });
    }
    assert.equal(lstatSync(join(alias, "settings.json")).isSymbolicLink(), true);
    const backupRoot = join(fx.home, ".caveman", "integrations", "backups");
    const backups = readdirSync(backupRoot);
    const manifest = JSON.parse(readFileSync(join(backupRoot, backups[0], "manifest.json"), "utf8"));
    assert.equal(manifest.length, profiles.length * 3, "symlink aliases must be deduplicated");
    assert.match(readFileSync(manifest[0].backup, "utf8"), /caveman/);
    assert.equal((await run(["disable", ...args], env)).code, 0);
    assert.deepEqual(readdirSync(backupRoot), backups, "second disable must make no changes");
  });
}

test("disable discovers a previously enabled custom profile after its journal is lost", async () => {
  const fx = fixture();
  const custom = join(fx.home, "accounts", "work");
  assert.equal((await run(["enable", "claude"], { ...fx.env, CLAUDE_CONFIG_DIR: custom })).code, 0);
  unlinkSync(join(fx.home, ".caveman", "integrations", "claude.json"));
  const out = await run(["disable", "claude"], fx.env);
  assert.equal(out.code, 0, out.stderr);
  assert.doesNotMatch(readFileSync(join(custom, "settings.json"), "utf8"), /ANTHROPIC_BASE_URL|native-hook|shrink-hook/);
  assert.doesNotMatch(readFileSync(join(custom, ".claude.json"), "utf8"), /caveman-mcp/);
});

test("disable preserves foreign routes and MCP registrations while removing only runtime hooks", async () => {
  const fx = fixture();
  const root = join(fx.home, ".claude-work");
  mkdirSync(root);
  const env = { ANTHROPIC_BASE_URL: "https://other.example/v1", _CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL: "0", ANTHROPIC_API_KEY: "sk-ant-preserve" };
  writeFileSync(join(root, "settings.json"), `// user comment\n${JSON.stringify({ env, hooks: { PreToolUse: [{ hooks: [{ type: "command", command: "caveman shrink-hook" }] }] } })}`);
  const mcp = '{"mcpServers":{"caveman":{"command":"my-custom-server"}}}\n';
  writeFileSync(join(root, ".claude.json"), mcp);
  const out = await run(["disable", "claude"], fx.env);
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(JSON.parse(readFileSync(join(root, "settings.json"), "utf8")), { env });
  assert.equal(readFileSync(join(root, ".claude.json"), "utf8"), mcp);
});

test("disable preflights every file Caveman wrote, names one it cannot read, and skips unreadable files it never wrote", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const settingsPath = join(fx.home, ".claude", "settings.json");
  const installed = readFileSync(settingsPath, "utf8");
  const journalPath = join(fx.home, ".caveman", "integrations", "claude.json");
  // A file Caveman wrote that no longer parses stops disable before any write.
  const mcpPath = join(fx.home, ".claude.json");
  const mcp = readFileSync(mcpPath, "utf8");
  writeFileSync(mcpPath, '{"mcpServers":');
  const refused = await run(["disable", "claude"], fx.env);
  assert.notEqual(refused.code, 0);
  assert.ok(refused.stderr.includes(`${mcpPath} is not valid JSON`), refused.stderr);
  assert.equal(readFileSync(settingsPath, "utf8"), installed);
  assert.ok(existsSync(journalPath));
  writeFileSync(mcpPath, mcp);
  // Files Caveman never wrote, empty or not JSON: Claude Code cannot read them
  // either, so they hold no hook to remove and must not block the undo.
  writeFileSync(join(fx.home, ".claude", "settings.local.json"), "");
  const bad = join(fx.home, ".claude-broken");
  mkdirSync(bad);
  writeFileSync(join(bad, "settings.json"), "not json at all");
  const out = await run(["disable", "claude"], fx.env);
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stderr, /left \S*\.claude-broken\/settings\.json as is: it is not a JSON object/);
  assert.equal(existsSync(settingsPath), false);
  assert.equal(existsSync(mcpPath), false);
  assert.equal(readFileSync(join(bad, "settings.json"), "utf8"), "not json at all");
  assert.equal(existsSync(journalPath), false);
});

test("native and shared fixtures isolate inherited Claude profiles from enable and disable", async () => {
  const external = mkdtempSync(join(tmpdir(), "cave-real-profile-"));
  const path = join(external, "settings.json");
  const original = '{"env":{"ANTHROPIC_BASE_URL":"https://keep.example"}}\n';
  writeFileSync(path, original);
  const before = process.env.CLAUDE_CONFIG_DIR;
  let shared;
  try {
    process.env.CLAUDE_CONFIG_DIR = external;
    const fx = fixture();
    shared = isolatedCliEnv({ PATH: fx.env.PATH, CAVEMAN_MCP_BIN: fx.env.CAVEMAN_MCP_BIN, CAVEMAN_PROXY_BIN: fx.env.CAVEMAN_PROXY_BIN });
    for (const env of [fx.env, shared.env]) {
      assert.equal((await run(["enable", "claude"], env)).code, 0);
      assert.equal((await run(["disable"], env)).code, 0);
      assert.equal(readFileSync(path, "utf8"), original);
    }
  } finally {
    if (before === undefined) delete process.env.CLAUDE_CONFIG_DIR;
    else process.env.CLAUDE_CONFIG_DIR = before;
    shared?.cleanup();
    rmSync(external, { recursive: true, force: true });
  }
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
      // An older Caveman route: re-pointed, and put back on disable. A
      // baseURL of the user's own is left as is (see below).
      openai: { options: { baseURL: "http://127.0.0.1:9999/w/opencode/openai/v1", keep: true } },
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
  // opencode-go serves OpenAI and Anthropic wire shapes from opencode.ai, so it
  // needs the proxy's opencode-go mount, not the openai/anthropic routes (#1090).
  assert.equal(installed.provider["opencode-go"].options.baseURL, "http://127.0.0.1:8787/w/opencode/compat/opencode-go/v1");
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
  assert.equal(restored.provider.openai.options.baseURL, "http://127.0.0.1:9999/w/opencode/openai/v1");
  assert.equal(restored.provider.openai.options.later, 1);
  assert.equal(restored.provider.anthropic, undefined);
  assert.equal(restored.provider["opencode-go"], undefined);
  assert.equal(restored.provider.custom.options.baseURL, "https://custom.example");
  assert.equal(restored.mcp.other.command[0], "other");
  assert.equal(restored.mcp.later.command[0], "later");
  assert.equal(restored.mcp.caveman, undefined);
  assert.equal(existsSync(pluginPath), false);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "opencode.json")), false);
});

test("status recognizes native OpenCode MCP recovery without a legacy marker", async () => {
  const fx = fixture();
  const configDir = join(fx.home, ".config", "opencode");
  mkdirSync(configDir, { recursive: true });

  const configPath = join(configDir, "opencode.json");
  writeFileSync(configPath, JSON.stringify({}) + "\n");

  const enabled = await run(["enable", "opencode"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);

  assert.equal(
    existsSync(join(fx.home, ".caveman", "mcp", "opencode.json")),
    false,
  );

  const installed = JSON.parse(readFileSync(configPath, "utf8"));
  assert.ok(installed.mcp?.caveman);

  const status = await run(["status"], fx.env);
  assert.equal(status.code, 0, status.stderr);
  assert.doesNotMatch(status.stdout + status.stderr, /MCP recovery missing/);
});

test("status keeps native OpenCode MCP recovery when provider routing drifts", async () => {
  const fx = fixture();
  const configDir = join(fx.home, ".config", "opencode");
  mkdirSync(configDir, { recursive: true });

  const configPath = join(configDir, "opencode.json");
  writeFileSync(configPath, JSON.stringify({}) + "\n");

  const enabled = await run(["enable", "opencode"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);

  const installed = JSON.parse(readFileSync(configPath, "utf8"));
  installed.provider.openai.options.baseURL =
    "http://127.0.0.1:8787/chatgpt";
  writeFileSync(configPath, JSON.stringify(installed, null, 2) + "\n");

  const status = await run(["status"], fx.env);
  assert.equal(status.code, 0, status.stderr);

  const output = status.stdout + status.stderr;
  assert.doesNotMatch(output, /MCP recovery missing/);
});

test("doctor and status warn when OpenCode's active provider is not routed (#1190)", async () => {
  const fx = fixture();
  const env = { ...fx.env, XDG_DATA_HOME: join(fx.home, ".local", "share") };
  const configDir = join(fx.home, ".config", "opencode");
  mkdirSync(configDir, { recursive: true });
  const configPath = join(configDir, "opencode.json");
  writeFileSync(configPath, JSON.stringify({ model: "github-copilot/gpt-5" }) + "\n");
  assert.equal((await run(["enable", "opencode"], env)).code, 0);
  const warning = /OpenCode's active provider "github-copilot" is not routed through Caveman/;

  const doctor = JSON.parse((await run(["doctor", "opencode"], env)).stdout);
  assert.equal(doctor.state, "installed", "an unrouted provider is a warning, not a broken install");
  assert.match(doctor.warnings.join("\n"), warning);
  const status = await run(["status"], env);
  assert.equal(status.code, 0, status.stderr);
  assert.match(status.stdout, warning);

  // No model set: a Copilot-only sign-in is the active provider.
  const config = JSON.parse(readFileSync(configPath, "utf8"));
  delete config.model;
  writeFileSync(configPath, JSON.stringify(config, null, 2) + "\n");
  mkdirSync(join(fx.home, ".local", "share", "opencode"), { recursive: true });
  writeFileSync(join(fx.home, ".local", "share", "opencode", "auth.json"), JSON.stringify({ "github-copilot": { type: "oauth" } }));
  assert.match(JSON.parse((await run(["doctor", "opencode"], env)).stdout).warnings.join("\n"), warning);

  config.model = "openai/gpt-5";
  writeFileSync(configPath, JSON.stringify(config, null, 2) + "\n");
  assert.deepEqual(JSON.parse((await run(["doctor", "opencode"], env)).stdout).warnings, []);
  config.model = "opencode-go/glm-5.2";
  writeFileSync(configPath, JSON.stringify(config, null, 2) + "\n");
  assert.deepEqual(JSON.parse((await run(["doctor", "opencode"], env)).stdout).warnings, []);
});

test("status recognizes native OpenCode MCP recovery when the config rewrites key order", async () => {
  const fx = fixture();
  const configDir = join(fx.home, ".config", "opencode");
  mkdirSync(configDir, { recursive: true });

  const configPath = join(configDir, "opencode.json");
  writeFileSync(configPath, JSON.stringify({}) + "\n");

  const enabled = await run(["enable", "opencode"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);

  const installed = JSON.parse(readFileSync(configPath, "utf8"));
  const caveman = installed.mcp.caveman;
  const keys = Object.keys(caveman);
  assert.ok(keys.length > 1, "registration needs >1 key for a reorder to be meaningful");

  // Same registration, keys serialized in the opposite order. Any writer that
  // round-trips this file through a sorted or rebuilt map produces this, and the
  // registration is still byte-for-byte equivalent as a value.
  const reordered = {};
  for (const key of keys.slice().reverse()) reordered[key] = caveman[key];
  installed.mcp.caveman = reordered;
  writeFileSync(configPath, JSON.stringify(installed, null, 2) + "\n");

  const status = await run(["status"], fx.env);
  assert.equal(status.code, 0, status.stderr);
  assert.doesNotMatch(status.stdout + status.stderr, /MCP recovery missing/);
});

test("status reports native OpenCode MCP recovery missing when its registration is removed", async () => {
  const fx = fixture();
  const configDir = join(fx.home, ".config", "opencode");
  mkdirSync(configDir, { recursive: true });

  const configPath = join(configDir, "opencode.json");
  writeFileSync(configPath, JSON.stringify({}) + "\n");

  const enabled = await run(["enable", "opencode"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);

  const installed = JSON.parse(readFileSync(configPath, "utf8"));
  delete installed.mcp.caveman;
  writeFileSync(configPath, JSON.stringify(installed, null, 2) + "\n");

  const status = await run(["status"], fx.env);
  assert.equal(status.code, 0, status.stderr);

  // The broken registration degrades the wiring, and the grid says so.
  assert.match(status.stdout, /^ {2}on {2}output .* degraded /m);
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

test("doctor flags an opencode install that predates the opencode-go route and --fix adds it", async () => {
  const fx = fixture();
  const configDir = join(fx.home, ".config", "opencode");
  mkdirSync(configDir, { recursive: true });
  const configPath = join(configDir, "opencode.json");
  writeFileSync(configPath, JSON.stringify({}) + "\n");
  assert.equal((await run(["enable", "opencode"], fx.env)).code, 0);

  // Rewind config and journal to what an enable before #1090 wrote.
  const config = JSON.parse(readFileSync(configPath, "utf8"));
  delete config.provider["opencode-go"];
  writeFileSync(configPath, JSON.stringify(config, null, 2) + "\n");
  const journalPath = join(fx.home, ".caveman", "integrations", "opencode.json");
  const journal = JSON.parse(readFileSync(journalPath, "utf8"));
  const op = journal.operations.find((item) => item.kind === "opencode-config");
  delete op.owned.routes["opencode-go"];
  delete op.owned.previous_routes["opencode-go"];
  writeFileSync(journalPath, JSON.stringify(journal, null, 2));

  const doctor = JSON.parse((await run(["doctor", "opencode"], fx.env)).stdout);
  assert.equal(doctor.state, "degraded");
  assert.equal(doctor.components.routing, false);
  assert.equal((await run(["doctor", "opencode", "--fix"], fx.env)).code, 0);
  assert.equal(JSON.parse(readFileSync(configPath, "utf8")).provider["opencode-go"].options.baseURL, "http://127.0.0.1:8787/w/opencode/compat/opencode-go/v1");
  assert.equal(JSON.parse((await run(["doctor", "opencode"], fx.env)).stdout).state, "installed");
});

test("enable/disable aider stays shallow, preserves native repo map, and restores config", async () => {
  const fx = fixture();
  const configPath = join(fx.home, ".aider.conf.yml");
  const before = [
    // An older Caveman route; the user's own endpoint is left as is (below).
    "openai-api-base: http://127.0.0.1:9999/w/aider/openai/v1",
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
  assert.match(restored, /openai-api-base: http:\/\/127\.0\.0\.1:9999\/w\/aider\/openai\/v1/);
  assert.match(restored, /USER_CONVENTIONS\.md/);
  assert.match(restored, /map-tokens: 2048/);
  assert.match(restored, /later-user-option: keep/);
  assert.doesNotMatch(restored, /caveman:native-aider|127\.0\.0\.1:8787/);
  assert.equal(existsSync(corePath), false);
});

// The generated opencode plugin bakes the invocation `enable` resolved, exactly
// as the claude/codex hook documents bake theirs. Judging its ownership by the
// marker comment alone left the same #1137 hole a step further along: the file
// is byte-identical to what enable wrote, so nothing looks drifted, while the
// path it names has gone with the removed nvm Node directory and every native
// call fails silently.
test("doctor flags an opencode plugin whose baked invocation no longer exists and --fix re-renders it", async () => {
  const fx = fixture();
  // A `caveman` earlier on PATH than the fixture's own, standing in for
  // ~/.nvm/versions/node/<version>/bin — the directory nvm deletes on
  // `nvm uninstall <old>`.
  const versioned = join(fx.home, ".nvm", "versions", "node", "v26.9.0", "bin");
  mkdirSync(versioned, { recursive: true });
  writeFileSync(join(versioned, "caveman"), readFileSync(join(fx.home, "bin", "caveman")), { mode: 0o755 });
  const env = { ...fx.env, PATH: `${versioned}:${fx.env.PATH}` };
  const configDir = join(fx.home, ".config", "opencode");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "opencode.json"), JSON.stringify({}) + "\n");

  assert.equal((await run(["enable", "opencode"], env)).code, 0);
  const pluginPath = join(configDir, "plugins", "caveman-native.js");
  // Pin the shape the health check parses: if the generator stops emitting a
  // `const command = "..."` line, the check silently verifies nothing.
  assert.match(readFileSync(pluginPath, "utf8"), /^const command = ".*v26\.9\.0.*";$/m);
  assert.equal(JSON.parse((await run(["doctor", "opencode"], env)).stdout).state, "installed");

  // The Node upgrade. The plugin's bytes do not change; its target disappears.
  rmSync(dirname(versioned), { recursive: true, force: true });
  const degraded = await run(["doctor", "opencode"], fx.env);
  assert.notEqual(degraded.code, 0, "a plugin naming a missing executable must not report healthy");
  const before = JSON.parse(degraded.stdout);
  assert.equal(before.state, "degraded");
  assert.equal(before.components.lifecycle_hooks, false);

  const fixed = await run(["doctor", "opencode", "--fix"], fx.env);
  assert.equal(fixed.code, 0, fixed.stderr);
  assert.equal(JSON.parse(fixed.stdout).fix.result, "repaired");
  assert.equal(JSON.parse(fixed.stdout).state, "installed");
  assert.doesNotMatch(readFileSync(pluginPath, "utf8"), /v26\.9\.0/);
});

// Voice skills ride along with the Claude/Codex native install. Explicit
// CLAUDE_CONFIG_DIR: the fixture env inherits the host's, and this must never
// land in a real config dir.
function voiceFixture() {
  const fx = fixture();
  const configDir = join(fx.home, "claude-config");
  const env = { ...fx.env, CLAUDE_CONFIG_DIR: configDir, CODEX_HOME: join(fx.home, "codex-home"), HERMES_HOME: "" };
  const skill = (name, root = configDir) => join(root, "skills", name, "SKILL.md");
  return { ...fx, env, configDir, skill };
}

test("enable claude installs the voice skills and discloses the write", async () => {
  const fx = voiceFixture();
  const enabled = await run(["enable", "claude"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);
  for (const name of ["caveman", "ultracave", "megacave"]) {
    assert.match(readFileSync(fx.skill(name), "utf8"), new RegExp(`^---\\nname: ${name}\\n`));
    assert.ok(enabled.stderr.includes(fx.skill(name)), enabled.stderr);
  }
  assert.equal(existsSync(join(fx.home, ".claude", "skills")), false, "CLAUDE_CONFIG_DIR must be honored");

  const disabled = await run(["disable", "claude"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  // disable turns off routing and hooks; the skills are the user's and stay.
  for (const name of ["caveman", "ultracave", "megacave"]) {
    assert.match(readFileSync(fx.skill(name), "utf8"), new RegExp(`^---\\nname: ${name}\\n`));
  }
});

test("enable codex installs the voice skills under CODEX_HOME; hermes installs none", async () => {
  const fx = voiceFixture();
  mkdirSync(join(fx.home, "codex-home"));
  const codex = await run(["enable", "codex"], fx.env);
  assert.equal(codex.code, 0, codex.stderr);
  assert.ok(existsSync(fx.skill("caveman", join(fx.home, "codex-home"))));
  assert.equal(existsSync(join(fx.home, ".codex", "skills")), false, "CODEX_HOME must be honored");
  const hermes = await run(["enable", "hermes"], fx.env);
  assert.equal(hermes.code, 0, hermes.stderr);
  assert.doesNotMatch(hermes.stderr, /voice skills/);
  assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "hermes.voice-skills.json")), false);
});

test("a pre-existing voice skill is never clobbered and survives disable", async () => {
  const fx = voiceFixture();
  mkdirSync(dirname(fx.skill("caveman")), { recursive: true });
  writeFileSync(fx.skill("caveman"), "mine\n");
  const enabled = await run(["enable", "claude"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);
  assert.equal(readFileSync(fx.skill("caveman"), "utf8"), "mine\n");
  assert.equal(enabled.stderr.includes(fx.skill("caveman")), false, "must not claim a file it did not write");
  assert.ok(existsSync(fx.skill("ultracave")));
  assert.equal((await run(["disable", "claude"], fx.env)).code, 0);
  assert.equal(readFileSync(fx.skill("caveman"), "utf8"), "mine\n");
  assert.ok(existsSync(fx.skill("ultracave")));
});

test("editing or deleting a voice skill never degrades the integration or blocks enable", async () => {
  const fx = voiceFixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  writeFileSync(fx.skill("caveman"), "pixelized\n");
  rmSync(dirname(fx.skill("megacave")), { recursive: true });
  const doctor = await run(["doctor", "claude"], fx.env);
  assert.equal(doctor.code, 0, doctor.stderr);
  assert.equal(JSON.parse(doctor.stdout).state, "installed");
  const again = await run(["enable", "claude"], fx.env);
  assert.equal(again.code, 0, again.stderr);
  assert.match(again.stderr, /already enabled/);
  assert.equal(readFileSync(fx.skill("caveman"), "utf8"), "pixelized\n");
  assert.equal(existsSync(fx.skill("megacave")), false, "a deleted skill is not written back");
});

test("enable on an install that predates voice skills picks them up once", async () => {
  const fx = voiceFixture();
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  rmSync(join(fx.configDir, "skills"), { recursive: true });
  unlinkSync(join(fx.home, ".caveman", "integrations", "claude.voice-skills.json"));
  const again = await run(["enable", "claude"], fx.env);
  assert.equal(again.code, 0, again.stderr);
  assert.match(again.stderr, /already enabled/);
  assert.ok(existsSync(fx.skill("caveman")));
});

test("a voice-skill write failure does not fail enable", async () => {
  const fx = voiceFixture();
  mkdirSync(fx.configDir, { recursive: true });
  writeFileSync(join(fx.configDir, "skills"), "not a directory\n");
  const enabled = await run(["enable", "claude"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);
  assert.match(enabled.stderr, /voice skills not installed/);
  assert.equal(JSON.parse((await run(["doctor", "claude"], fx.env)).stdout).state, "installed");
});

test("enable codex skips a voice skill the Skills CLI already put in ~/.agents/skills", async () => {
  const fx = voiceFixture();
  mkdirSync(join(fx.home, "codex-home"));
  mkdirSync(join(fx.home, ".agents", "skills", "caveman"), { recursive: true });
  writeFileSync(join(fx.home, ".agents", "skills", "caveman", "SKILL.md"), "from skills cli\n");
  const codex = await run(["enable", "codex"], fx.env);
  assert.equal(codex.code, 0, codex.stderr);
  assert.equal(existsSync(fx.skill("caveman", join(fx.home, "codex-home"))), false);
  assert.ok(existsSync(fx.skill("ultracave", join(fx.home, "codex-home"))));
});

test("routing on puts Auto in Claude's and OpenCode's pickers; disable takes it out", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".caveman"), { recursive: true });
  writeFileSync(join(fx.home, ".caveman", "cloud.json"), JSON.stringify({ modules: { routing: true }, baseURL: "https://api.caveman.so", tokenStore: "file" }) + "\n");
  mkdirSync(join(fx.home, ".config", "opencode"), { recursive: true });
  const opencodePath = join(fx.home, ".config", "opencode", "opencode.json");
  writeFileSync(opencodePath, JSON.stringify({ provider: { anthropic: { models: { mine: { name: "Mine" } } } } }, null, 2) + "\n");
  const settingsPath = join(fx.home, ".claude", "settings.json");
  mkdirSync(join(fx.home, ".claude"), { recursive: true });
  writeFileSync(settingsPath, JSON.stringify({ env: { KEEP: "yes" } }) + "\n");

  for (const agent of ["claude", "opencode"]) {
    const out = await run(["enable", agent], fx.env);
    assert.equal(out.code, 0, out.stderr);
  }
  const env = JSON.parse(readFileSync(settingsPath, "utf8")).env;
  assert.equal(env.ANTHROPIC_CUSTOM_MODEL_OPTION, "caveman-auto[1m]", "the [1m] id gives Auto a 1M window in Claude Code");
  assert.equal(env.ANTHROPIC_CUSTOM_MODEL_OPTION_NAME, "Auto");
  assert.equal(env.ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION, "Caveman pick model + effort each turn. Hard ask, big brain. Easy ask, save rocks.");
  assert.equal(env.ANTHROPIC_CUSTOM_MODEL_OPTION_SUPPORTED_CAPABILITIES, "effort,max_effort,xhigh_effort,thinking,adaptive_thinking,interleaved_thinking");
  const providers = JSON.parse(readFileSync(opencodePath, "utf8")).provider;
  for (const id of ["openai", "anthropic"]) assert.equal(providers[id].models["caveman-auto"].name, "Auto", id);
  assert.equal(providers.anthropic.models["caveman-auto"].limit.context, 1000000, "Opus and Sonnet 5.5 run at 1M");
  assert.equal(providers.openai.models["caveman-auto"].limit.context, 872000, "the most a ChatGPT login serves");
  assert.equal(providers.anthropic.models.mine.name, "Mine");
  assert.equal(providers["opencode-go"].models?.["caveman-auto"], undefined, "Auto runs on OpenAI and Anthropic only");
  // Ownership is the journal, not byte-equality: an entry the user tuned still goes.
  const tuned = JSON.parse(readFileSync(opencodePath, "utf8"));
  tuned.provider.openai.models["caveman-auto"].limit = { context: 1, output: 1 };
  writeFileSync(opencodePath, JSON.stringify(tuned, null, 2) + "\n");

  for (const agent of ["claude", "opencode"]) {
    const out = await run(["disable", agent], fx.env);
    assert.equal(out.code, 0, out.stderr);
  }
  assert.deepEqual(JSON.parse(readFileSync(settingsPath, "utf8")), { env: { KEEP: "yes" } });
  assert.deepEqual(JSON.parse(readFileSync(opencodePath, "utf8")), { provider: { anthropic: { models: { mine: { name: "Mine" } } } } });
});

test("routing off, or a custom option of the user's own, adds no Auto", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".claude"), { recursive: true });
  const settingsPath = join(fx.home, ".claude", "settings.json");
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  assert.doesNotMatch(readFileSync(settingsPath, "utf8"), /CUSTOM_MODEL_OPTION/);
  assert.equal((await run(["disable", "claude"], fx.env)).code, 0);

  mkdirSync(join(fx.home, ".caveman"), { recursive: true });
  writeFileSync(join(fx.home, ".caveman", "cloud.json"), JSON.stringify({ modules: { routing: true }, baseURL: "https://api.caveman.so", tokenStore: "file" }) + "\n");
  writeFileSync(settingsPath, JSON.stringify({ env: { ANTHROPIC_CUSTOM_MODEL_OPTION: "my-model" } }) + "\n");
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  const env = JSON.parse(readFileSync(settingsPath, "utf8")).env;
  assert.equal(env.ANTHROPIC_CUSTOM_MODEL_OPTION, "my-model");
  assert.equal(env.ANTHROPIC_CUSTOM_MODEL_OPTION_NAME, undefined);
  assert.equal((await run(["disable", "claude"], fx.env)).code, 0);
  assert.equal(JSON.parse(readFileSync(settingsPath, "utf8")).env.ANTHROPIC_CUSTOM_MODEL_OPTION, "my-model");
});

test("Auto follows the login: logout takes it out of Claude Code's settings", async () => {
  const fx = fixture();
  const server = createHttpServer((req, res) => { res.writeHead(200, { "content-type": "application/json" }); res.end("{}"); });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const baseURL = `http://127.0.0.1:${server.address().port}`;
    mkdirSync(join(fx.home, ".caveman"), { recursive: true });
    writeFileSync(join(fx.home, ".caveman", "cloud.json"), JSON.stringify({ modules: { routing: true }, baseURL, token: "tok" }) + "\n");
    const settingsPath = join(fx.home, ".claude", "settings.json");
    assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
    assert.equal(JSON.parse(readFileSync(settingsPath, "utf8")).env.ANTHROPIC_CUSTOM_MODEL_OPTION, "caveman-auto[1m]");
    const out = await run(["logout"], { ...fx.env, CAVE_NO_KEYCHAIN: "1" });
    assert.equal(out.code, 0, out.stderr);
    assert.equal(JSON.parse(readFileSync(settingsPath, "utf8")).env.ANTHROPIC_CUSTOM_MODEL_OPTION, undefined, "signed out: no Auto");
    assert.equal(JSON.parse(readFileSync(settingsPath, "utf8")).env.ANTHROPIC_BASE_URL, "http://127.0.0.1:8787/w/claude", "the rest of the wiring stays");
  } finally {
    server.close();
  }
});

test("a Claude Code lane that bypasses the proxy gets no Auto", async () => {
  const fx = fixture();
  mkdirSync(join(fx.home, ".caveman"), { recursive: true });
  writeFileSync(join(fx.home, ".caveman", "cloud.json"), JSON.stringify({ modules: { routing: true }, baseURL: "https://api.caveman.so", tokenStore: "file" }) + "\n");
  mkdirSync(join(fx.home, ".claude"), { recursive: true });
  const settingsPath = join(fx.home, ".claude", "settings.json");
  writeFileSync(settingsPath, JSON.stringify({ env: { CLAUDE_CODE_USE_BEDROCK: "1" } }) + "\n");
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  assert.doesNotMatch(readFileSync(settingsPath, "utf8"), /CUSTOM_MODEL_OPTION/);
});

const signedInRouting = (fx, extra = {}) => {
  mkdirSync(join(fx.home, ".caveman"), { recursive: true });
  writeFileSync(join(fx.home, ".caveman", "cloud.json"), JSON.stringify({ modules: { routing: true }, baseURL: "https://api.caveman.so", tokenStore: "file", ...extra }) + "\n");
};

test("a saved choice of Auto goes on disable: Claude Code, OpenCode and Codex", async () => {
  const fx = fixture();
  signedInRouting(fx);
  for (const agent of ["claude", "opencode", "codex"]) assert.equal((await run(["enable", agent], fx.env)).code, 0, agent);
  const claudePath = join(fx.home, ".claude", "settings.json");
  const claude = JSON.parse(readFileSync(claudePath, "utf8"));
  writeFileSync(claudePath, JSON.stringify({ ...claude, model: "caveman-auto", theme: "dark" }, null, 2) + "\n");
  const opencodePath = join(fx.home, ".config", "opencode", "opencode.json");
  writeFileSync(opencodePath, JSON.stringify({ ...JSON.parse(readFileSync(opencodePath, "utf8")), model: "openai/caveman-auto" }, null, 2) + "\n");
  const codexPath = join(fx.home, ".codex", "config.toml");
  writeFileSync(codexPath, `model = "caveman-auto"\n` + readFileSync(codexPath, "utf8") + `\n[profiles.x]\nmodel = "caveman-auto"\n`);
  for (const agent of ["claude", "opencode", "codex"]) {
    const out = await run(["disable", agent], fx.env);
    assert.equal(out.code, 0, `${agent}: ${out.stderr}`);
  }
  const after = JSON.parse(readFileSync(claudePath, "utf8"));
  assert.equal(after.model, undefined);
  assert.equal(after.theme, "dark");
  if (existsSync(opencodePath)) assert.equal(JSON.parse(readFileSync(opencodePath, "utf8")).model, undefined);
  const codex = readFileSync(codexPath, "utf8");
  assert.ok(!codex.startsWith('model = "caveman-auto"'), codex);
  assert.match(codex, /\[profiles\.x\]\nmodel = "caveman-auto"/, "only the top-level choice goes");
});

test("logout takes Auto and a saved choice of it out without any binary on PATH", async () => {
  const fx = fixture();
  const server = createHttpServer((req, res) => { res.writeHead(200, { "content-type": "application/json" }); res.end("{}"); });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    mkdirSync(join(fx.home, ".caveman"), { recursive: true });
    writeFileSync(join(fx.home, ".caveman", "cloud.json"), JSON.stringify({ modules: { routing: true }, baseURL: `http://127.0.0.1:${server.address().port}`, token: "tok" }) + "\n");
    assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
    const settingsPath = join(fx.home, ".claude", "settings.json");
    writeFileSync(settingsPath, JSON.stringify({ ...JSON.parse(readFileSync(settingsPath, "utf8")), model: "caveman-auto[1m]" }, null, 2) + "\n");
    const out = await run(["logout"], { ...fx.env, CAVE_NO_KEYCHAIN: "1", PATH: "/usr/bin:/bin", CAVEMAN_MCP_BIN: "/missing", CAVEMAN_PROXY_BIN: "/missing" });
    assert.equal(out.code, 0, out.stderr);
    const settings = JSON.parse(readFileSync(settingsPath, "utf8"));
    assert.equal(settings.env.ANTHROPIC_CUSTOM_MODEL_OPTION, undefined);
    assert.equal(settings.model, undefined);
    assert.equal((await run(["disable", "claude"], fx.env)).code, 0, "the journal still reverses cleanly");
  } finally {
    server.close();
  }
});

test("a commented settings.json enables, and a third-party Anthropic upstream gets no Auto", async () => {
  const fx = fixture();
  signedInRouting(fx);
  mkdirSync(join(fx.home, ".claude"), { recursive: true });
  const settingsPath = join(fx.home, ".claude", "settings.json");
  writeFileSync(settingsPath, '{\n  // mine\n  "env": { "KEEP": "yes" },\n}\n');
  const out = await run(["enable", "claude"], fx.env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(JSON.parse(readFileSync(settingsPath, "utf8")).env.ANTHROPIC_CUSTOM_MODEL_OPTION, "caveman-auto[1m]");
  assert.equal((await run(["disable", "claude"], fx.env)).code, 0);
  writeFileSync(join(fx.home, ".caveman", "caveman.yaml"), "providers:\n  anthropic:\n    base_url: https://gateway.example.com\n");
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  assert.doesNotMatch(readFileSync(settingsPath, "utf8"), /CUSTOM_MODEL_OPTION/);
});

// What login and logout run once the credential is stored or gone.
const syncAuto = (env) => new Promise((resolve, reject) => {
  const child = spawn(process.execPath, ["--input-type=module", "-e", `const cli = await import(${JSON.stringify(pathToFileURL(cli).href)}); cli.syncAutoEntries();`], { env });
  let stderr = "";
  child.stderr.on("data", (chunk) => (stderr += chunk));
  child.on("exit", (code) => resolve({ code, stderr }));
  child.on("error", reject);
});

test("OpenCode's saved Auto goes with its own provider's entry, not only with the last one", async () => {
  const fx = fixture();
  signedInRouting(fx);
  assert.equal((await run(["enable", "opencode"], fx.env)).code, 0);
  const opencodePath = join(fx.home, ".config", "opencode", "opencode.json");
  const read = () => JSON.parse(readFileSync(opencodePath, "utf8"));
  const save = (model) => writeFileSync(opencodePath, JSON.stringify({ ...read(), model }, null, 2) + "\n");
  // OpenAI now goes to a third party, which cannot serve Auto; Anthropic still can.
  writeFileSync(join(fx.home, ".caveman", "caveman.yaml"), "providers:\n  openai:\n    base_url: https://gateway.example.com/v1\n");
  save("anthropic/caveman-auto");
  let out = await syncAuto(fx.env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(read().model, "anthropic/caveman-auto", "a provider that still offers Auto keeps the choice");
  assert.equal(read().provider.openai.models?.["caveman-auto"], undefined);
  assert.equal(read().provider.anthropic.models["caveman-auto"].name, "Auto");
  writeFileSync(join(fx.home, ".caveman", "caveman.yaml"), "");
  assert.equal((await syncAuto(fx.env)).code, 0);
  save("openai/caveman-auto");
  writeFileSync(join(fx.home, ".caveman", "caveman.yaml"), "providers:\n  openai:\n    base_url: https://gateway.example.com/v1\n");
  out = await syncAuto(fx.env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(read().model, undefined, "Auto left OpenAI: the saved choice of it goes too");
  assert.equal(read().provider.anthropic.models["caveman-auto"].name, "Auto");
});

test("a sync that changes nothing leaves the user's layout and comments alone", async () => {
  const fx = fixture();
  signedInRouting(fx);
  for (const agent of ["claude", "opencode"]) assert.equal((await run(["enable", agent], fx.env)).code, 0, agent);
  for (const path of [join(fx.home, ".claude", "settings.json"), join(fx.home, ".config", "opencode", "opencode.json")]) {
    // The same values, one line, with a comment: not JSON.stringify(…, 2).
    const mine = `// mine\n${JSON.stringify(JSON.parse(readFileSync(path, "utf8")))}\n`;
    writeFileSync(path, mine);
    const out = await syncAuto(fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.equal(out.stderr, "");
    assert.equal(readFileSync(path, "utf8"), mine, path);
  }
});

for (const [name, enabledSignedIn] of [["login", false], ["logout", true]]) {
  test(`disable after a ${name} sync keeps what the user added since enable`, async () => {
    const fx = fixture();
    const signedOut = () => writeFileSync(join(fx.home, ".caveman", "cloud.json"), "{}\n");
    signedInRouting(fx);
    if (!enabledSignedIn) signedOut();
    const settingsPath = join(fx.home, ".claude", "settings.json");
    const opencodePath = join(fx.home, ".config", "opencode", "opencode.json");
    mkdirSync(dirname(settingsPath), { recursive: true });
    mkdirSync(dirname(opencodePath), { recursive: true });
    writeFileSync(settingsPath, JSON.stringify({ theme: "dark" }) + "\n");
    writeFileSync(opencodePath, JSON.stringify({ theme: "system" }) + "\n");
    for (const agent of ["claude", "opencode"]) assert.equal((await run(["enable", agent], fx.env)).code, 0, agent);
    // What the user and the agent add after enable.
    const mine = { permissions: { allow: ["Bash(npm test)"] }, statusLine: { type: "command", command: "mine" } };
    writeFileSync(settingsPath, JSON.stringify({ ...JSON.parse(readFileSync(settingsPath, "utf8")), ...mine }, null, 2) + "\n");
    writeFileSync(opencodePath, JSON.stringify({ ...JSON.parse(readFileSync(opencodePath, "utf8")), keybinds: { leader: "ctrl+x" } }, null, 2) + "\n");
    if (enabledSignedIn) signedOut(); else signedInRouting(fx);
    const synced = await syncAuto(fx.env);
    assert.equal(synced.code, 0, synced.stderr);
    assert.equal(/caveman-auto/.test(readFileSync(settingsPath, "utf8")), !enabledSignedIn, "the sync rewrote settings.json");
    assert.equal(/caveman-auto/.test(readFileSync(opencodePath, "utf8")), !enabledSignedIn, "the sync rewrote opencode.json");
    for (const agent of ["claude", "opencode"]) {
      const out = await run(["doctor", agent], fx.env);
      assert.equal(JSON.parse(out.stdout).state, "installed", `${agent}: a synced file is not a degraded install`);
    }
    for (const agent of ["claude", "opencode"]) {
      const out = await run(["disable", agent], fx.env);
      assert.equal(out.code, 0, `${agent}: ${out.stderr}`);
    }
    assert.deepEqual(JSON.parse(readFileSync(settingsPath, "utf8")), { theme: "dark", ...mine }, "settings.json keeps the user's later keys");
    assert.deepEqual(JSON.parse(readFileSync(opencodePath, "utf8")), { theme: "system", keybinds: { leader: "ctrl+x" } }, "opencode.json keeps the user's later keys");
  });
}

test("disable across profiles takes Caveman's Auto and a saved choice of it out of each, never the user's own option", async () => {
  const fx = fixture();
  signedInRouting(fx);
  const work = join(fx.home, "accounts", "work", "settings.json");
  assert.equal((await run(["enable", "claude"], { ...fx.env, CLAUDE_CONFIG_DIR: dirname(work) })).code, 0);
  const wired = JSON.parse(readFileSync(work, "utf8"));
  assert.equal(wired.env.ANTHROPIC_CUSTOM_MODEL_OPTION, "caveman-auto[1m]");
  // /model → Auto in the journaled profile, and in a copy of it no journal knows.
  writeFileSync(work, JSON.stringify({ ...wired, model: "caveman-auto[1m]", theme: "dark" }, null, 2) + "\n");
  const copy = join(fx.home, ".claude-copy", "settings.json");
  mkdirSync(dirname(copy));
  writeFileSync(copy, JSON.stringify({ ...wired, model: "caveman-auto[1m]", theme: "dark" }, null, 2) + "\n");
  const own = join(fx.home, ".claude-own", "settings.json");
  mkdirSync(dirname(own));
  const ownEnv = { ANTHROPIC_CUSTOM_MODEL_OPTION: "my-model", ANTHROPIC_CUSTOM_MODEL_OPTION_NAME: "Auto" };
  writeFileSync(own, JSON.stringify({ model: "my-model", env: { ...ownEnv, ANTHROPIC_BASE_URL: "http://127.0.0.1:8787/w/claude" } }) + "\n");

  // The shell that disables is on the default profile, not on either of them.
  const out = await run(["disable", "claude"], fx.env);
  assert.equal(out.code, 0, out.stderr);
  for (const path of [work, copy]) {
    const after = JSON.parse(readFileSync(path, "utf8"));
    assert.equal(after.env?.ANTHROPIC_BASE_URL, undefined, path);
    assert.equal(after.model, undefined, `${path}: a saved Auto would reach Anthropic directly`);
    assert.doesNotMatch(JSON.stringify(after.env ?? {}), /CUSTOM_MODEL_OPTION/, path);
    assert.equal(after.theme, "dark", path);
  }
  assert.deepEqual(JSON.parse(readFileSync(own, "utf8")), { model: "my-model", env: ownEnv });
});

test("logout clears a saved choice of Auto made in a session-only caveman claude, with no native wiring", async () => {
  const fx = fixture();
  signedInRouting(fx);
  const settingsPath = join(fx.home, ".claude", "settings.json");
  mkdirSync(dirname(settingsPath), { recursive: true });
  writeFileSync(settingsPath, JSON.stringify({ model: "caveman-auto[1m]", theme: "dark" }) + "\n");
  assert.equal((await syncAuto(fx.env)).code, 0);
  assert.equal(JSON.parse(readFileSync(settingsPath, "utf8")).model, "caveman-auto[1m]", "signed in with routing on: Auto still answers");
  writeFileSync(join(fx.home, ".caveman", "cloud.json"), "{}\n");
  const out = await syncAuto(fx.env);
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(JSON.parse(readFileSync(settingsPath, "utf8")), { theme: "dark" });
  writeFileSync(settingsPath, '// mine\n{"model":"opus"}\n');
  assert.equal((await syncAuto(fx.env)).code, 0);
  assert.equal(readFileSync(settingsPath, "utf8"), '// mine\n{"model":"opus"}\n', "any other choice is left alone");
});

// A config kept in a dotfiles repo stays a link, and a 0644 file stays 0644.
test("a linked or 0644 agent config keeps its link and mode through enable and disable", async () => {
  const fx = fixture();
  const dotfiles = join(fx.home, "dotfiles");
  mkdirSync(dotfiles);
  const target = join(dotfiles, "config.toml");
  writeFileSync(target, 'model = "gpt-5.5"\n');
  const link = join(fx.home, ".codex", "config.toml");
  mkdirSync(dirname(link), { recursive: true });
  symlinkSync(target, link);
  const hooks = join(fx.home, ".codex", "hooks.json");
  writeFileSync(hooks, "{}\n");
  chmodSync(hooks, 0o644);
  const enabled = await run(["enable", "codex"], fx.env);
  assert.equal(enabled.code, 0, enabled.stderr);
  assert.ok(lstatSync(link).isSymbolicLink(), "enable replaced the link with a copy");
  assert.match(readFileSync(target, "utf8"), /caveman:native-root/);
  assert.equal(statSync(hooks).mode & 0o777, 0o644);
  const disabled = await run(["disable", "codex"], fx.env);
  assert.equal(disabled.code, 0, disabled.stderr);
  assert.ok(lstatSync(link).isSymbolicLink(), "disable replaced the link with a copy");
  assert.equal(readFileSync(target, "utf8"), 'model = "gpt-5.5"\n');
  assert.equal(statSync(hooks).mode & 0o777, 0o644);
});

// OpenCode reads $XDG_CONFIG_HOME/opencode and prefers opencode.jsonc there.
test("enable opencode writes where OpenCode reads: XDG_CONFIG_HOME and opencode.jsonc", async () => {
  const fx = fixture();
  const xdg = join(fx.home, "xdg");
  const env = { ...fx.env, XDG_CONFIG_HOME: xdg };
  const configPath = join(xdg, "opencode", "opencode.jsonc");
  mkdirSync(dirname(configPath), { recursive: true });
  const original = '{\n  // mine\n  "model": "openai/gpt-5.5",\n}\n';
  writeFileSync(configPath, original);
  const enabled = await run(["enable", "opencode"], env);
  assert.equal(enabled.code, 0, enabled.stderr);
  const installed = JSON.parse(readFileSync(configPath, "utf8"));
  assert.equal(installed.model, "openai/gpt-5.5");
  assert.equal(installed.provider.openai.options.baseURL, "http://127.0.0.1:8787/w/opencode/openai/v1");
  assert.ok(existsSync(join(xdg, "opencode", "plugins", "caveman-native.js")));
  assert.equal(existsSync(join(fx.home, ".config", "opencode")), false);
  assert.equal(existsSync(join(xdg, "opencode", "opencode.json")), false);
  // Rewritten as plain JSON: the comment goes, and enable says where it is kept.
  const kept = enabled.stderr.match(/comments in \S+opencode\.jsonc were not kept; the original is saved at (\S+)/);
  assert.ok(kept, enabled.stderr);
  assert.equal(readFileSync(kept[1], "utf8"), original);
  assert.equal(JSON.parse((await run(["doctor", "opencode"], env)).stdout).state, "installed");
  assert.equal((await run(["disable", "opencode"], env)).code, 0);
  assert.equal(readFileSync(configPath, "utf8"), original);
});

// An endpoint of the user's own (a gateway, LiteLLM, a local model) is never
// swapped for the proxy, whose upstream is the provider's public API: the
// agent's requests and key would go there. Enable names it and writes nothing.
test("enable leaves an agent on its own endpoint as is and says how to opt in", async () => {
  const fx = fixture();
  const hermesHome = join(fx.home, ".hermes");
  const cases = [
    ["codex", join(fx.home, ".codex", "config.toml"), 'model_provider = "ollama"\n', /its own endpoint ollama \(model_provider in \S+config\.toml\)/],
    ["aider", join(fx.home, ".aider.conf.yml"), "openai-api-base: \"http://localhost:1234/v1\" # LM Studio\n", /its own endpoint http:\/\/localhost:1234\/v1 \(openai-api-base in /],
    ["opencode", join(fx.home, ".config", "opencode", "opencode.json"), JSON.stringify({ provider: { anthropic: { options: { baseURL: "https://llm-gw.corp.example/anthropic", apiKey: "{env:CORP_KEY}" } } } }) + "\n", /\(provider\.anthropic\.options\.baseURL in /],
    ["gemini", join(fx.home, ".gemini", ".env"), "GOOGLE_GEMINI_BASE_URL=https://llm-gw.corp.example/gemini\n", /\(GOOGLE_GEMINI_BASE_URL in \S+\.env\)/],
    ["hermes", join(hermesHome, "config.yaml"), "model:\n  provider: custom\n  base_url: http://localhost:11434/v1\n", /\(model\.base_url in \S+config\.yaml\)/],
  ];
  for (const [agent, file, body, where] of cases) {
    mkdirSync(dirname(file), { recursive: true });
    writeFileSync(file, body);
    const out = await run(["enable", agent], { ...fx.env, HERMES_HOME: hermesHome });
    assert.notEqual(out.code, 0, agent);
    assert.match(out.stderr, where, agent);
    assert.match(out.stderr, new RegExp(`was left as is\\. To route it through Caveman anyway, remove \\S+ there and run \`caveman enable ${agent}\``), agent);
    assert.equal(readFileSync(file, "utf8"), body, agent);
    assert.equal(existsSync(join(fx.home, ".caveman", "integrations", `${agent}.json`)), false, agent);
  }
  // `--detected` says so for each and goes on to wire the rest.
  const detected = await run(["enable", "--detected"], { ...fx.env, HERMES_HOME: hermesHome });
  assert.equal(detected.code, 0, detected.stderr);
  assert.equal(detected.stderr.match(/was left as is/g)?.length, cases.length, detected.stderr);
  assert.ok(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")));
  // Codex's built-in provider takes its endpoint from the shell, and Caveman's
  // provider would replace it.
  writeFileSync(cases[0][1], "");
  const shell = await run(["enable", "codex"], { ...fx.env, OPENAI_BASE_URL: "https://user:secret@llm-gw.corp.example/v1?key=k" });
  assert.notEqual(shell.code, 0);
  assert.match(shell.stderr, /its own endpoint https:\/\/llm-gw\.corp\.example\/v1 \(OPENAI_BASE_URL in your shell\).* remove OPENAI_BASE_URL from your shell and run `caveman enable codex`/);
  assert.doesNotMatch(shell.stderr, /secret|key=k/, "credentials in the URL are not printed");
  // Not the user's own: the provider's public API, where the proxy sends it
  // anyway, and the gateway a wrapped shell exports, with or without a route.
  for (const value of ["https://api.openai.com/v1", "http://127.0.0.1:8787", "http://127.0.0.1:8787/w/codex"]) {
    const out = await run(["enable", "codex"], { ...fx.env, OPENAI_BASE_URL: value });
    assert.equal(out.code, 0, `${value}: ${out.stderr}`);
    assert.equal((await run(["disable", "codex"], fx.env)).code, 0, value);
  }
});

// An install from before this check routes the user's own endpoint; a repair
// must not call that "left as is".
test("repairing an earlier install over the user's own endpoint says how to put it back", async () => {
  const fx = fixture();
  const settingsPath = join(fx.home, ".claude", "settings.json");
  mkdirSync(dirname(settingsPath), { recursive: true });
  writeFileSync(settingsPath, "{}\n");
  assert.equal((await run(["enable", "claude"], fx.env)).code, 0);
  // Rewind the journal to what an earlier enable over a gateway recorded.
  const journalPath = join(fx.home, ".caveman", "integrations", "claude.json");
  const journal = JSON.parse(readFileSync(journalPath, "utf8"));
  const op = journal.operations.find((item) => item.kind === "claude-settings");
  const original = JSON.stringify({ env: { ANTHROPIC_BASE_URL: "https://llm-gw.corp.example/anthropic" } }) + "\n";
  writeFileSync(op.backup, original);
  op.before_sha256 = `sha256:${createHash("sha256").update(original).digest("hex")}`;
  op.owned.previous_route = "https://llm-gw.corp.example/anthropic";
  writeFileSync(journalPath, JSON.stringify(journal, null, 2));
  const wired = readFileSync(settingsPath, "utf8");
  // A moved runtime port makes the next enable repair the route.
  const out = await run(["enable", "claude"], { ...fx.env, CAVE_GATEWAY_URL: "http://127.0.0.1:8799" });
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /was routed through Caveman before, over its own endpoint https:\/\/llm-gw\.corp\.example\/anthropic.* Run `caveman disable claude` to put that endpoint back/);
  assert.equal(readFileSync(settingsPath, "utf8"), wired);
  assert.equal((await run(["disable", "claude"], fx.env)).code, 0);
  assert.equal(readFileSync(settingsPath, "utf8"), original);
});

// Gemini CLI reads the first .env walking up from where it runs; the global
// one holding Caveman's route is skipped in a folder that has its own.
test("doctor gemini warns where a project .env or the shell overrides Caveman's route", async () => {
  const fx = fixture();
  assert.equal((await run(["enable", "gemini"], fx.env)).code, 0);
  const project = join(fx.home, "project");
  mkdirSync(project);
  const doctor = (env, cwd) => JSON.parse(spawnSync(process.execPath, [cli, "doctor", "gemini"], { env, cwd, encoding: "utf8" }).stdout);
  assert.deepEqual(doctor(fx.env, project).warnings, []);
  writeFileSync(join(project, ".env"), "FOO=bar\n");
  assert.match(doctor(fx.env, project).warnings.join("\n"), /Gemini CLI reads \S+project\/\.env here instead of \S+\.gemini\/\.env/);
  writeFileSync(join(project, ".env"), "FOO=bar\nGOOGLE_GEMINI_BASE_URL=http://127.0.0.1:8787/w/gemini\n");
  assert.deepEqual(doctor(fx.env, project).warnings, []);
  assert.match(doctor({ ...fx.env, GOOGLE_GEMINI_BASE_URL: "https://llm-gw.corp.example" }, fx.home).warnings.join("\n"), /takes GOOGLE_GEMINI_BASE_URL from your shell/);
  // A virtualenv named .env is still the first hit; it must not crash doctor.
  rmSync(join(project, ".env"));
  mkdirSync(join(project, ".env"));
  assert.match(doctor(fx.env, project).warnings.join("\n"), /Gemini CLI reads \S+project\/\.env here/);
});
