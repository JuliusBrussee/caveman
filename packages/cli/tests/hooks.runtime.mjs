import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, writeFileSync, readFileSync, mkdirSync, existsSync, symlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

// runHook feeds a Claude PreToolUse event to `caveman shrink-hook` on stdin and
// captures what it prints (the rewrite, or nothing = "run as-is").
function runHook(payload, env = process.env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "shrink-hook"], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
    child.stdin.end(JSON.stringify(payload));
  });
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
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap }, null, 2));
}

// A noisy, finite, non-interactive command is rewritten to run through
// `caveman shrink` — the RTK-style command-output compression, recoverable.
test("shrink-hook rewrites a noisy command through caveman shrink", async () => {
  const out = await runHook({ tool_name: "Bash", tool_input: { command: "git status" } });
  assert.equal(out.code, 0, out.stderr);
  const o = JSON.parse(out.stdout);
  assert.equal(o.hookSpecificOutput.hookEventName, "PreToolUse");
  assert.match(o.hookSpecificOutput.updatedInput.command, /shrink -- git status$/);
});

test("shrink-hook rewrites cargo test", async () => {
  const out = await runHook({ tool_name: "Bash", tool_input: { command: "cargo test --all" } });
  const o = JSON.parse(out.stdout);
  assert.match(o.hookSpecificOutput.updatedInput.command, /shrink -- cargo test --all$/);
});

// Conservative by design: anything it can't safely shrink passes through with NO
// output (= no rewrite). Shell operators, streaming/interactive flags,
// editor-opening subcommands, non-allowlisted commands, and our own wrapper.
for (const command of [
  "echo hello",                       // not a noisy-read command
  "cd src && ls",                     // shell operator (&&)
  "git status | head",                // pipe — shrink execs argv, would change meaning
  "cat big.log > out.txt",            // redirect
  "caveman shrink -- git status",     // already wrapped — never double-wrap
  "git commit -m wip",                // opens an editor / interactive
  "kubectl logs -f web",              // -f streams forever — shrink must terminate
  "docker run -it ubuntu bash",       // interactive tty
  "vim notes.md",                     // not allowlisted / interactive
]) {
  test(`shrink-hook passes through (no rewrite): ${command}`, async () => {
    const out = await runHook({ tool_name: "Bash", tool_input: { command } });
    assert.equal(out.code, 0);
    assert.equal(out.stdout, "", `must not rewrite: ${command}`);
  });
}

test("shrink-hook ignores non-Bash tools", async () => {
  const out = await runHook({ tool_name: "Read", tool_input: { file_path: "/etc/hosts" } });
  assert.equal(out.code, 0);
  assert.equal(out.stdout, "");
});

test("shrink-hook tolerates malformed input", async () => {
  const child = spawn("node", [cli, "shrink-hook"]);
  let stdout = "";
  child.stdout.on("data", (d) => (stdout += d));
  const code = await new Promise((r) => { child.on("exit", r); child.stdin.end("not json"); });
  assert.equal(code, 0);
  assert.equal(stdout, "");
});

// `hooks install claude` writes a Bash PreToolUse hook into ~/.claude/settings.json
// whose command calls back into `caveman shrink-hook`, plus an install marker.
test("hooks install claude registers a Bash PreToolUse hook and a marker", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir };

  const out = await runCli(["hooks", "install", "claude"], env);
  assert.equal(out.code, 0, out.stderr);

  const settings = JSON.parse(readFileSync(join(home, ".claude", "settings.json"), "utf8"));
  const pre = settings.hooks.PreToolUse;
  assert.ok(Array.isArray(pre), "PreToolUse must be an array");
  const entry = pre.find((e) => e.matcher === "Bash");
  assert.ok(entry, "must add a Bash matcher");
  assert.ok(entry.hooks.some((h) => h.type === "command" && h.command.includes("shrink-hook")), "command must call shrink-hook");
  assert.ok(existsSync(join(caveDir, "hooks", "claude.json")), "an install marker must be written");
});

// Installing twice must not duplicate the caveman hook, and must never disturb a
// hook the user already had.
test("hooks install is idempotent and preserves the user's own hooks", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  mkdirSync(join(home, ".claude"), { recursive: true });
  writeFileSync(
    join(home, ".claude", "settings.json"),
    JSON.stringify({ hooks: { PreToolUse: [{ matcher: "Bash", hooks: [{ type: "command", command: "my-own-linter.sh" }] }] } }, null, 2),
  );

  await runCli(["hooks", "install", "claude"], env);
  await runCli(["hooks", "install", "claude"], env);

  const settings = JSON.parse(readFileSync(join(home, ".claude", "settings.json"), "utf8"));
  const pre = settings.hooks.PreToolUse;
  const caveEntries = pre.filter((e) => e.hooks.some((h) => (h.command || "").includes("shrink-hook")));
  assert.equal(caveEntries.length, 1, "exactly one caveman hook after two installs");
  assert.ok(pre.some((e) => e.hooks.some((h) => h.command === "my-own-linter.sh")), "the user's own hook must survive");
});

test("hooks uninstall removes the caveman hook, keeps the user's, and drops the marker", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".claude"), { recursive: true });
  writeFileSync(
    join(home, ".claude", "settings.json"),
    JSON.stringify({ hooks: { PreToolUse: [{ matcher: "Bash", hooks: [{ type: "command", command: "my-own-linter.sh" }] }] } }, null, 2),
  );

  await runCli(["hooks", "install", "claude"], env);
  await runCli(["hooks", "uninstall", "claude"], env);

  const settings = JSON.parse(readFileSync(join(home, ".claude", "settings.json"), "utf8"));
  const pre = settings.hooks.PreToolUse;
  assert.ok(!pre.some((e) => e.hooks.some((h) => (h.command || "").includes("shrink-hook"))), "caveman hook must be gone");
  assert.ok(pre.some((e) => e.hooks.some((h) => h.command === "my-own-linter.sh")), "the user's own hook must remain");
  assert.ok(!existsSync(join(caveDir, "hooks", "claude.json")), "the marker must be removed");
});

// The loadout: `caveman wrap claude` auto-loads the command-output hook with one
// command; config shrink:false opts out. (Stub `claude` on PATH; proxy:false keeps it quiet.)
test("wrap auto-loads the command-output hook for claude, and config shrink:false opts out", async () => {
  const binDir = mkdtempSync(join(tmpdir(), "cave-claude-"));
  writeFileSync(join(binDir, "claude"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });

  // default loadout → installs
  const home1 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cave1 = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env1 = { ...process.env, NO_COLOR: "1", HOME: home1, CAVEMAN_HOME: cave1, PATH: `${binDir}:${process.env.PATH}` };
  delete env1.CAVE_GATEWAY_URL;
  writeWrapConfig(home1, { proxy: false, browse: false });
  const w1 = await runCli(["wrap", "claude"], env1);
  assert.equal(w1.code, 0, w1.stderr);
  assert.ok(existsSync(join(home1, ".claude", "settings.json")), "wrap must auto-install the hook");
  const s1 = JSON.parse(readFileSync(join(home1, ".claude", "settings.json"), "utf8"));
  assert.ok(s1.hooks.PreToolUse.some((e) => e.hooks.some((h) => (h.command || "").includes("shrink-hook"))), "the loadout hook must be present");
  assert.ok(existsSync(join(cave1, "hooks", "claude.json")), "marker must be written");

  // shrink:false → does NOT install
  const home2 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cave2 = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env2 = { ...process.env, NO_COLOR: "1", HOME: home2, CAVEMAN_HOME: cave2, PATH: `${binDir}:${process.env.PATH}` };
  delete env2.CAVE_GATEWAY_URL;
  writeWrapConfig(home2, { proxy: false, shrink: false, browse: false });
  const w2 = await runCli(["wrap", "claude"], env2);
  assert.equal(w2.code, 0, w2.stderr);
  assert.ok(!existsSync(join(home2, ".claude", "settings.json")), "shrink:false must not install the hook");
});

// ── Codex: instruction-note (soft tier) ──────────────────────────────────────
// Codex has no deterministic command-rewrite hook (its runtime rejects updatedInput,
// openai/codex#18491), so the honest mechanism is a delimited "prefer caveman shrink"
// note appended to the file Codex auto-reads (~/.codex/AGENTS.md). It must preserve the
// user's own content and never duplicate.
test("hooks install codex appends a delimited shrink note, idempotent, preserving user content", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".codex"), { recursive: true });
  writeFileSync(join(home, ".codex", "AGENTS.md"), "# My rules\nAlways write tests.\n");

  await runCli(["hooks", "install", "codex"], env);
  await runCli(["hooks", "install", "codex"], env); // twice → still exactly one block

  const md = readFileSync(join(home, ".codex", "AGENTS.md"), "utf8");
  assert.ok(md.includes("# My rules"), "the user's own heading must survive");
  assert.ok(md.includes("Always write tests."), "the user's own content must survive");
  assert.equal((md.match(/caveman:shrink-hook \(managed/g) || []).length, 1, "exactly one note after two installs");
  assert.match(md, /caveman shrink -- <command>/, "the note must point at caveman shrink");
  assert.ok(existsSync(join(caveDir, "hooks", "codex.json")), "a marker must be written");
});

test("hooks install codex creates the instructions file when absent", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  const out = await runCli(["hooks", "install", "codex"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.ok(existsSync(join(home, ".codex", "AGENTS.md")), "the note file must be created");
});

test("hooks uninstall codex removes only the caveman note, keeping the user's content", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".codex"), { recursive: true });
  writeFileSync(join(home, ".codex", "AGENTS.md"), "# Keep me\n");

  await runCli(["hooks", "install", "codex"], env);
  await runCli(["hooks", "uninstall", "codex"], env);

  const md = readFileSync(join(home, ".codex", "AGENTS.md"), "utf8");
  assert.ok(md.includes("# Keep me"), "the user's content must remain");
  assert.ok(!md.includes("caveman:shrink-hook"), "the caveman note must be gone");
  assert.ok(!existsSync(join(caveDir, "hooks", "codex.json")), "the marker must be removed");
});

// ── opencode: plugin (hard tier, real RTK parity) ────────────────────────────
// opencode CAN deterministically rewrite a command (a plugin's tool.execute.before
// mutates output.args.command), so caveman ships a plugin file.
test("hooks install opencode writes a plugin, uninstall removes it", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };

  await runCli(["hooks", "install", "opencode"], env);
  const plugin = join(home, ".config", "opencode", "plugins", "caveman-shrink.js");
  assert.ok(existsSync(plugin), "the opencode plugin must be written");
  const src = readFileSync(plugin, "utf8");
  assert.match(src, /tool\.execute\.before/, "the plugin must hook tool.execute.before");
  assert.match(src, /caveman:shrink-plugin/, "the plugin must carry our marker");
  assert.ok(existsSync(join(caveDir, "hooks", "opencode.json")), "a marker must be written");

  await runCli(["hooks", "uninstall", "opencode"], env);
  assert.ok(!existsSync(plugin), "uninstall must remove the plugin");
  assert.ok(!existsSync(join(caveDir, "hooks", "opencode.json")), "uninstall must drop the marker");
});

test("hooks uninstall opencode leaves a plugin it does not own", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  const plugin = join(home, ".config", "opencode", "plugins", "caveman-shrink.js");
  mkdirSync(dirname(plugin), { recursive: true });
  writeFileSync(plugin, "// someone else's plugin\n");
  await runCli(["hooks", "uninstall", "opencode"], env);
  assert.ok(existsSync(plugin), "a non-caveman plugin must be left untouched");
});

// ── Hermes: Python plugin under $HERMES_HOME/plugins/caveman_shrink ──────────
test("hooks install hermes writes marker-fenced plugin files, idempotent, and uninstall removes cleanly", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const hermesHome = mkdtempSync(join(tmpdir(), "cave-hermes-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, HERMES_HOME: hermesHome, CAVEMAN_HOME: caveDir };

  await runCli(["hooks", "install", "hermes"], env);
  await runCli(["hooks", "install", "hermes"], env);

  const pluginDir = join(hermesHome, "plugins", "caveman_shrink");
  const initPath = join(pluginDir, "__init__.py");
  const manifestPath = join(pluginDir, "plugin.yaml");
  assert.ok(existsSync(initPath), "Hermes plugin __init__.py must be written");
  assert.ok(existsSync(manifestPath), "Hermes plugin manifest must be written");
  const init = readFileSync(initPath, "utf8");
  const manifest = readFileSync(manifestPath, "utf8");
  assert.match(init, /# >>> caveman:shrink-plugin/, "plugin code must carry begin marker");
  assert.match(init, /# <<< caveman:shrink-plugin/, "plugin code must carry end marker");
  assert.match(init, /register_hook\("transform_terminal_output"/, "plugin must register Hermes terminal-output hook");
  assert.match(init, /"shrink", "--stdin"/, "plugin must pipe output through caveman shrink --stdin");
  assert.match(manifest, /# >>> caveman:shrink-plugin/, "manifest must carry marker");
  assert.match(manifest, /provides_hooks:\n  - transform_terminal_output/, "manifest must declare the hook");

  const configPath = join(hermesHome, "config.yaml");
  const config = readFileSync(configPath, "utf8");
  assert.equal((config.match(/caveman:hermes-plugin-enable/g) || []).length, 2, "enable marker begin+end only once after two installs");
  assert.equal((config.match(/- caveman_shrink/g) || []).length, 1, "plugins.enabled entry must be idempotent");
  assert.ok(existsSync(join(caveDir, "hooks", "hermes.json")), "hook marker must be written");

  await runCli(["hooks", "uninstall", "hermes"], env);
  assert.ok(!existsSync(pluginDir), "uninstall must remove the owned Hermes plugin directory");
  const after = existsSync(configPath) ? readFileSync(configPath, "utf8") : "";
  assert.ok(!after.includes("caveman:hermes-plugin-enable"), "uninstall must remove the enable marker block");
  assert.ok(!existsSync(join(caveDir, "hooks", "hermes.json")), "uninstall must drop the hook marker");
});

// ── OpenClaw: plugin (hard tier, tool_result_persist) ───────────────────────
test("hooks install openclaw writes plugin package and config; uninstall removes only caveman entries", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const stateDir = mkdtempSync(join(tmpdir(), "openclaw-state-"));
  const configPath = join(stateDir, "openclaw.json");
  writeFileSync(configPath, JSON.stringify({
    theme: "midnight",
    plugins: {
      load: { paths: ["/existing/plugin"] },
      entries: { other: { enabled: true } },
      allow: ["other"],
    },
  }, null, 2));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir, OPENCLAW_STATE_DIR: stateDir };
  delete env.OPENCLAW_CONFIG_PATH;

  const installed = await runCli(["hooks", "install", "openclaw"], env);
  assert.equal(installed.code, 0, installed.stderr);
  const pluginDir = join(caveDir, "openclaw", "plugins", "caveman-shrink");
  assert.ok(existsSync(join(pluginDir, "openclaw.plugin.json")), "native plugin manifest must be written");
  assert.ok(existsSync(join(pluginDir, "package.json")), "native plugin package metadata must be written");
  const source = readFileSync(join(pluginDir, "index.mjs"), "utf8");
  assert.match(source, /tool_result_persist/, "OpenClaw plugin must use the verified persist hook");
  assert.match(source, /caveman:openclaw-shrink-plugin/, "plugin source must carry the Caveman ownership marker");

  const cfg = JSON.parse(readFileSync(configPath, "utf8"));
  assert.equal(cfg.theme, "midnight", "unrelated config must be preserved");
  assert.deepEqual(cfg.plugins.load.paths, ["/existing/plugin", pluginDir]);
  assert.equal(cfg.plugins.entries.other.enabled, true, "existing plugin entries must be preserved");
  assert.equal(cfg.plugins.entries["caveman-shrink"].enabled, true);
  assert.equal(cfg.plugins.entries["caveman-shrink"].hooks.allowConversationAccess, true);
  assert.deepEqual(cfg.plugins.allow, ["other", "caveman-shrink"], "restrictive allowlists must include the new plugin");
  assert.ok(existsSync(join(caveDir, "hooks", "openclaw.json")), "a persistent hook marker must be written");

  const uninstalled = await runCli(["hooks", "uninstall", "openclaw"], env);
  assert.equal(uninstalled.code, 0, uninstalled.stderr);
  const after = JSON.parse(readFileSync(configPath, "utf8"));
  assert.equal(after.theme, "midnight");
  assert.deepEqual(after.plugins.load.paths, ["/existing/plugin"]);
  assert.equal(after.plugins.entries.other.enabled, true);
  assert.equal(after.plugins.entries["caveman-shrink"], undefined);
  assert.deepEqual(after.plugins.allow, ["other"]);
  assert.ok(!existsSync(pluginDir), "owned plugin dir must be removed");
  assert.ok(!existsSync(join(caveDir, "hooks", "openclaw.json")), "uninstall must drop the marker");
});

// ── aider: manual-only (no installable surface) ──────────────────────────────
test("hooks install aider is an honest no-op (no marker, points at the manual path)", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  const out = await runCli(["hooks", "install", "aider"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.ok(!existsSync(join(caveDir, "hooks", "aider.json")), "a manual-only agent gets no marker");
  assert.match(out.stderr, /caveman shrink -- <cmd>/, "must point at the manual shrink path");
});

// ── the loadout: wrap codex auto-loads the soft note; shrink:false opts out ───
test("wrap auto-loads the shrink note for codex, and config shrink:false opts out", async () => {
  const binDir = mkdtempSync(join(tmpdir(), "cave-codex-"));
  writeFileSync(join(binDir, "codex"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });

  // default loadout → installs the note
  const home1 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cave1 = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env1 = { ...process.env, NO_COLOR: "1", HOME: home1, CAVEMAN_HOME: cave1, PATH: `${binDir}:${process.env.PATH}` };
  delete env1.CAVE_GATEWAY_URL;
  writeWrapConfig(home1, { proxy: false, browse: false });
  const w1 = await runCli(["wrap", "codex"], env1);
  assert.equal(w1.code, 0, w1.stderr);
  const notePath = join(home1, ".codex", "AGENTS.md");
  assert.ok(existsSync(notePath), "wrap must auto-install the codex note");
  assert.ok(readFileSync(notePath, "utf8").includes("caveman:shrink-hook"), "the note must be present");
  assert.ok(existsSync(join(cave1, "hooks", "codex.json")), "marker must be written");

  // shrink:false → does NOT install
  const home2 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cave2 = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env2 = { ...process.env, NO_COLOR: "1", HOME: home2, CAVEMAN_HOME: cave2, PATH: `${binDir}:${process.env.PATH}` };
  delete env2.CAVE_GATEWAY_URL;
  writeWrapConfig(home2, { proxy: false, shrink: false, browse: false });
  const w2 = await runCli(["wrap", "codex"], env2);
  assert.equal(w2.code, 0, w2.stderr);
  assert.ok(!existsSync(join(home2, ".codex", "AGENTS.md")), "shrink:false must not install the note");
});

// ── byte-safe: a non-object ~/.claude/settings.json must be left untouched ─────
// (honesty: byte-safe — on a parse/shape problem we pass through, never corrupt.)
for (const original of ["[]", "\"a string\"", "42", "{ not json"]) {
  test(`hooks install claude leaves a non-object settings.json byte-for-byte unchanged: ${original}`, async () => {
    const home = mkdtempSync(join(tmpdir(), "cave-home-"));
    const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
    mkdirSync(join(home, ".claude"), { recursive: true });
    const sp = join(home, ".claude", "settings.json");
    writeFileSync(sp, original);
    const out = await runCli(["hooks", "install", "claude"], env);
    assert.equal(out.code, 0, out.stderr);
    assert.equal(readFileSync(sp, "utf8"), original, "a non-object settings file must not be modified");
    assert.ok(!readFileSync(sp, "utf8").includes("shrink-hook"), "no hook must be injected into a non-object settings file");
  });
}

// ── the opencode plugin actually rewrites at runtime (not just grep-asserted) ──
// Dynamic-import the generated plugin and drive its tool.execute.before hook. In the
// test env `caveman` is not on PATH, so the baked invocation resolves to this very
// built CLI (node dist/index.js shrink-hook) — the hook genuinely round-trips through
// the real shrink decision.
test("the generated opencode plugin rewrites a noisy bash command and is byte-safe otherwise", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  await runCli(["hooks", "install", "opencode"], env);
  const plugin = join(home, ".config", "opencode", "plugins", "caveman-shrink.js");
  assert.ok(existsSync(plugin), "plugin must exist");

  const mod = await import(pathToFileURL(plugin).href);
  const hooks = await mod.CavemanShrink();
  const before = hooks["tool.execute.before"];

  // (a) a noisy, allowlisted command is rewritten to run through `caveman shrink`
  const o1 = { args: { command: "git status" } };
  await before({ tool: "bash" }, o1);
  assert.match(o1.args.command, /shrink -- git status$/, "git status must be rewritten through caveman shrink");

  // (b) a non-bash tool is never touched
  const o2 = { args: { command: "git status" } };
  await before({ tool: "read" }, o2);
  assert.equal(o2.args.command, "git status", "non-bash tools must be left unchanged");

  // (c) byte-safe: a command shrink-hook declines (pipe → meaning would change) passes through
  const o3 = { args: { command: "git status | head" } };
  await before({ tool: "bash" }, o3);
  assert.equal(o3.args.command, "git status | head", "a non-eligible command must be left unchanged");

  // (d) byte-safe: an empty/garbage command is left as-is
  const o4 = { args: { command: "" } };
  await before({ tool: "bash" }, o4);
  assert.equal(o4.args.command, "", "an empty command must be left unchanged");
});

// ── Gemini: shrink-hook emits Gemini's override shape (not Claude's) ──────────
// Gemini's run_shell_command uses hookSpecificOutput.tool_input (snake_case, merge),
// NOT Claude's updatedInput/hookEventName — emitting the wrong shape is a silent no-op.
test("shrink-hook emits Gemini's tool_input override for run_shell_command", async () => {
  const out = await runHook({ tool_name: "run_shell_command", tool_input: { command: "git status" } });
  assert.equal(out.code, 0, out.stderr);
  const o = JSON.parse(out.stdout);
  assert.ok(o.hookSpecificOutput.tool_input, "must use Gemini's tool_input shape");
  assert.match(o.hookSpecificOutput.tool_input.command, /shrink -- git status$/);
  assert.equal(o.hookSpecificOutput.hookEventName, undefined, "Gemini output must NOT carry Claude's hookEventName");
  assert.equal(o.hookSpecificOutput.updatedInput, undefined, "Gemini output must NOT use Claude's updatedInput");
});

test("shrink-hook passes through a non-eligible Gemini command (pipe)", async () => {
  const out = await runHook({ tool_name: "run_shell_command", tool_input: { command: "git status | head" } });
  assert.equal(out.code, 0);
  assert.equal(out.stdout, "", "a piped command must not be rewritten");
});

// ── Gemini: hard BeforeTool rewrite hook in ~/.gemini/settings.json ───────────
test("hooks install gemini registers a run_shell_command BeforeTool hook, idempotent, preserving user hooks", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".gemini"), { recursive: true });
  writeFileSync(
    join(home, ".gemini", "settings.json"),
    JSON.stringify({ hooks: { BeforeTool: [{ matcher: "run_shell_command", hooks: [{ type: "command", command: "my-own.sh" }] }] } }, null, 2),
  );

  await runCli(["hooks", "install", "gemini"], env);
  await runCli(["hooks", "install", "gemini"], env); // idempotent

  const s = JSON.parse(readFileSync(join(home, ".gemini", "settings.json"), "utf8"));
  const bt = s.hooks.BeforeTool;
  assert.ok(Array.isArray(bt), "BeforeTool must be an array");
  const cave = bt.filter((e) => e.hooks.some((h) => (h.command || "").includes("shrink-hook")));
  assert.equal(cave.length, 1, "exactly one caveman hook after two installs");
  assert.equal(cave[0].matcher, "run_shell_command", "must match the gemini shell tool");
  assert.ok(bt.some((e) => e.hooks.some((h) => h.command === "my-own.sh")), "the user's own hook must survive");
  assert.ok(existsSync(join(caveDir, "hooks", "gemini.json")), "a marker must be written");
});

test("hooks uninstall gemini removes the caveman hook, keeps the user's, drops the marker", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".gemini"), { recursive: true });
  writeFileSync(
    join(home, ".gemini", "settings.json"),
    JSON.stringify({ hooks: { BeforeTool: [{ matcher: "run_shell_command", hooks: [{ type: "command", command: "my-own.sh" }] }] } }, null, 2),
  );

  await runCli(["hooks", "install", "gemini"], env);
  await runCli(["hooks", "uninstall", "gemini"], env);

  const s = JSON.parse(readFileSync(join(home, ".gemini", "settings.json"), "utf8"));
  assert.ok(!s.hooks.BeforeTool.some((e) => e.hooks.some((h) => (h.command || "").includes("shrink-hook"))), "caveman hook gone");
  assert.ok(s.hooks.BeforeTool.some((e) => e.hooks.some((h) => h.command === "my-own.sh")), "user hook remains");
  assert.ok(!existsSync(join(caveDir, "hooks", "gemini.json")), "marker removed");
});

test("wrap auto-loads the hard hook for gemini, and config shrink:false opts out", async () => {
  const binDir = mkdtempSync(join(tmpdir(), "cave-gemini-"));
  writeFileSync(join(binDir, "gemini"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });

  const home1 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cave1 = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env1 = { ...process.env, NO_COLOR: "1", HOME: home1, CAVEMAN_HOME: cave1, PATH: `${binDir}:${process.env.PATH}` };
  delete env1.CAVE_GATEWAY_URL;
  writeWrapConfig(home1, { proxy: false, browse: false });
  const w1 = await runCli(["wrap", "gemini"], env1);
  assert.equal(w1.code, 0, w1.stderr);
  const s1 = JSON.parse(readFileSync(join(home1, ".gemini", "settings.json"), "utf8"));
  assert.ok(s1.hooks.BeforeTool.some((e) => e.hooks.some((h) => (h.command || "").includes("shrink-hook"))), "the loadout hard hook must be present");
  assert.ok(existsSync(join(cave1, "hooks", "gemini.json")), "marker written");

  const home2 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env2 = { ...process.env, NO_COLOR: "1", HOME: home2, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")), PATH: `${binDir}:${process.env.PATH}` };
  delete env2.CAVE_GATEWAY_URL;
  writeWrapConfig(home2, { proxy: false, shrink: false, browse: false });
  const w2 = await runCli(["wrap", "gemini"], env2);
  assert.equal(w2.code, 0, w2.stderr);
  assert.ok(!existsSync(join(home2, ".gemini", "settings.json")), "shrink:false must not install the hook");
});

// ── honesty: install message must distinguish hard rewrite from soft nudge ────
test("hooks install labels hard rewrite vs soft nudge honestly on stderr", async () => {
  const mk = () => ({ ...process.env, NO_COLOR: "1", HOME: mkdtempSync(join(tmpdir(), "cave-home-")), CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) });

  const hard = await runCli(["hooks", "install", "claude"], mk());
  assert.match(hard.stderr, /rewrite hook installed/, "claude (hard) must be called a rewrite hook");
  assert.doesNotMatch(hard.stderr, /model nudge/, "a hard install must not be called a nudge");
  assert.match(hard.stderr, /auto-shrunk/, "hard footer claims auto-shrink");

  const soft = await runCli(["hooks", "install", "codex"], mk());
  assert.match(soft.stderr, /model nudge, not a hard rewrite/, "codex (soft) must be labeled a nudge, not a rewrite");
  assert.doesNotMatch(soft.stderr, /auto-shrunk/, "a soft-only install must NOT claim auto-shrink");
});

// ── hooks install with no agent: fan out to every hookable agent on PATH ───────
test("hooks install (no agent) installs for every hookable agent detected on PATH", async () => {
  const binDir = mkdtempSync(join(tmpdir(), "cave-fanout-"));
  symlinkSync(process.execPath, join(binDir, "node")); // so the spawned CLI can find node
  for (const a of ["claude", "codex", "opencode", "aider"]) writeFileSync(join(binDir, a), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir, PATH: binDir };
  delete env.CAVE_GATEWAY_URL;

  const out = await runCli(["hooks", "install"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.ok(existsSync(join(home, ".claude", "settings.json")), "claude (hard) hook installed");
  assert.ok(existsSync(join(home, ".codex", "AGENTS.md")), "codex (soft) note installed");
  assert.ok(existsSync(join(home, ".config", "opencode", "plugins", "caveman-shrink.js")), "opencode (hard) plugin installed");
  for (const id of ["claude", "codex", "opencode"]) assert.ok(existsSync(join(caveDir, "hooks", `${id}.json`)), `${id} marker written`);
  assert.ok(!existsSync(join(caveDir, "hooks", "aider.json")), "aider (no surface) is skipped — no marker");
});

test("hooks install (no agent) with no hookable agent on PATH fails closed (exit 1)", async () => {
  const binDir = mkdtempSync(join(tmpdir(), "cave-empty-"));
  symlinkSync(process.execPath, join(binDir, "node"));
  const env = { ...process.env, NO_COLOR: "1", HOME: mkdtempSync(join(tmpdir(), "cave-home-")), CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")), PATH: binDir };
  delete env.CAVE_GATEWAY_URL;
  const out = await runCli(["hooks", "install"], env);
  assert.equal(out.code, 1, "must fail closed when no hookable agent is present");
  assert.match(out.stderr, /no hookable agents detected on PATH/);
});

// ── note install→uninstall is an exact round-trip (preserves user whitespace) ──
test("note install then uninstall restores the user's file byte-for-byte", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  mkdirSync(join(home, ".codex"), { recursive: true });
  const original = "# Title\n\n\nSection with intentional blank lines above.\n\n- a\n- b\n";
  const p = join(home, ".codex", "AGENTS.md");
  writeFileSync(p, original);

  await runCli(["hooks", "install", "codex"], env);
  assert.notEqual(readFileSync(p, "utf8"), original, "install must add the note");
  await runCli(["hooks", "uninstall", "codex"], env);
  assert.equal(readFileSync(p, "utf8"), original, "uninstall must restore the file exactly, internal blank lines included");
});
