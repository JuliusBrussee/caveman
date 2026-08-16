import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

// Spawn the CLI with a stub agent binary on PATH, isolated HOME (so the loadout
// hook installer and gateway resolution never touch the real home dir), and no
// CAVE_GATEWAY_URL. The stub prints its args + the injected base URL to stdout.
// CAVEMAN_MCP_BIN / CAVEMAN_PROXY_BIN point at nonexistent paths so the
// shortcut's native-enable attempt deterministically fails and falls back to
// the session-only wrap door, regardless of what the host machine has on PATH.
function runShortcut(agentId, cliArgs) {
  const binDir = mkdtempSync(join(tmpdir(), `cave-shortcut-${agentId}-`));
  const stub = join(binDir, agentId);
  writeFileSync(stub, `#!/bin/sh\nprintf '%s|%s' "$*" "$ANTHROPIC_BASE_URL"\n`, { mode: 0o755 });
  const home = mkdtempSync(join(tmpdir(), "cave-shortcut-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false, browse: false } }, null, 2));
  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    CAVEMAN_HOME: home,
    PATH: `${binDir}:${process.env.PATH}`,
    CAVEMAN_MCP_BIN: join(binDir, "missing-caveman-mcp"),
    CAVEMAN_PROXY_BIN: join(binDir, "missing-caveman-proxy"),
  };
  delete env.CAVE_GATEWAY_URL;
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...cliArgs], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

function userAgentArgs(raw) {
  const parts = raw ? raw.split(" ") : [];
  assert.equal(parts[0], "--plugin-dir", "Claude wrap must load session-only native pack");
  assert.match(parts[1] ?? "", /caveman-wrap-claude-/);
  return parts.slice(2).join(" ");
}

function runLockedClaude(routes, { harness = "pi", mutateDuringCheck = false } = {}) {
  const binDir = mkdtempSync(join(tmpdir(), "cave-locked-claude-bin-"));
  writeFileSync(join(binDir, "claude"), "#!/bin/sh\nprintf '%s|%s|%s' \"$ANTHROPIC_CUSTOM_HEADERS\" \"$CAVE_TRANSFORM_IDS\" \"$CAVE_AGENT_BUILD_STATE\"\n", { mode: 0o755 });
  writeFileSync(
    join(binDir, "caveman-agent"),
    mutateDuringCheck
      ? "#!/bin/sh\nnode -e 'const fs=require(\"node:fs\");const p=\".caveman/agent.lock.json\";const x=JSON.parse(fs.readFileSync(p));x.plan_sha256=\"c\".repeat(64);fs.writeFileSync(p,JSON.stringify(x))'\n"
      : "#!/bin/sh\nexit 0\n",
    { mode: 0o755 },
  );
  const home = mkdtempSync(join(tmpdir(), "cave-locked-claude-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false, browse: false } }));
  const project = mkdtempSync(join(tmpdir(), "cave-locked-claude-project-"));
  mkdirSync(join(project, ".caveman"));
  writeFileSync(join(project, ".caveman", "agent.lock.json"), JSON.stringify({
    build_sha256: "a".repeat(64),
    plan_sha256: "b".repeat(64),
    harness: { id: harness },
    selected_plan: { segment_routes: routes },
  }));
  const env = {
    ...process.env,
    NO_COLOR: "1",
    HOME: home,
    CAVEMAN_HOME: home,
    PATH: `${binDir}:${process.env.PATH}`,
  };
  delete env.CAVE_TRANSFORM_IDS;
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "claude"], { cwd: project, env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

// `caveman claude` defaults to the persistent native enable, but when the
// native path can't hold (here: caveman-mcp/proxy binaries missing) it must
// warn and fall back to exactly the `caveman wrap claude` behavior: launch the
// agent with the local-proxy base URL injected.
test("caveman claude falls back to wrap when native enable cannot hold", async () => {
  const out = await runShortcut("claude", ["claude"]);
  assert.equal(out.code, 0, `cli exited ${out.code}: ${out.stderr}`);
  assert.match(out.stderr, /native enable failed: .*using session-only wrap for this run/);
  const [agentArgs, baseURL] = out.stdout.split("|");
  assert.equal(userAgentArgs(agentArgs), "", "no user args should be added");
  assert.equal(baseURL, "http://127.0.0.1:8787/w/claude", "ANTHROPIC_BASE_URL must point at the attributed local proxy");
});

// Everything after the agent name passes to the agent verbatim — flags included.
test("caveman claude passes trailing args to the agent verbatim", async () => {
  const out = await runShortcut("claude", ["claude", "--resume", "-p", "hi"]);
  assert.equal(out.code, 0, `cli exited ${out.code}: ${out.stderr}`);
  const [agentArgs] = out.stdout.split("|");
  assert.equal(userAgentArgs(agentArgs), "--resume -p hi", "trailing flags must reach the agent untouched");
});

test("caveman claude --off hoists --off into wrap mode", async () => {
  const out = await runShortcut("claude", ["claude", "--off"]);
  assert.equal(out.code, 0, `cli exited ${out.code}: ${out.stderr}`);
  const [agentArgs] = out.stdout.split("|");
  assert.equal(userAgentArgs(agentArgs), "", "--off is a Caveman wrap flag and must not reach user args");
});

test("caveman claude leaves --pixel-models as an agent argument", async () => {
  const out = await runShortcut("claude", ["claude", "--pixel-models", "claude-fable-5"]);
  assert.equal(out.code, 0, `cli exited ${out.code}: ${out.stderr}`);
  const [agentArgs] = out.stdout.split("|");
  assert.equal(userAgentArgs(agentArgs), "--pixel-models claude-fable-5", "--pixel-models moved to config and must not be hoisted");
});

test("Claude wrapper rejects Pi-specific Cave Build before agent launch", async () => {
  const out = await runLockedClaude([{
    segment_kind: "history",
    transform_id: "caveman.engine.json.v1",
    fallback: "original",
  }]);
  assert.notEqual(out.code, 0);
  assert.equal(out.stdout, "");
  assert.match(out.stderr, /Cave Build is Pi-specific/);
});

test("Claude-specific Cave Build fails closed until full plan is enforceable", async () => {
  const out = await runLockedClaude([], { harness: "claude" });
  assert.notEqual(out.code, 0);
  assert.equal(out.stdout, "");
  assert.match(out.stderr, /model, reasoning, budget, recovery, and wire selectors are enforced/);
});

test("Claude wrapper rejects lock changed during checker validation", async () => {
  const out = await runLockedClaude([], { harness: "claude", mutateDuringCheck: true });
  assert.notEqual(out.code, 0);
  assert.equal(out.stdout, "");
  assert.match(out.stderr, /lock changed during validation/);
});

// The shortcut resolves binary names too (findAgent matches binary_names), and
// a non-agent unknown command must still fail loudly, not silently wrap.
test("unknown command still errors; agent shortcut does not swallow typos", async () => {
  const out = await runShortcut("claude", ["clade"]);
  assert.notEqual(out.code, 0, "a typo'd command must exit non-zero");
  assert.match(out.stderr, /unknown command "clade" — did you mean `caveman claude`\?/);
});

// ===========================================================================
// Persistent default: with conforming caveman-mcp/caveman-proxy binaries the
// shortcut performs the `caveman enable claude` native install, then launches
// the host binary DIRECTLY — no temp wrap pack, no env injection; routing lives
// in the installed native settings/hooks.
// ===========================================================================

// Same env/home reused across calls so the second run exercises idempotency.
function nativeShortcutEnv() {
  const binDir = mkdtempSync(join(tmpdir(), "cave-shortcut-native-bin-"));
  const stub = join(binDir, "claude");
  writeFileSync(stub, `#!/bin/sh\nif [ "$1" = "--version" ]; then echo 'claude 1.0.0'; exit 0; fi\nprintf '%s|%s' "$*" "$ANTHROPIC_BASE_URL"\n`, { mode: 0o755 });
  const mcp = join(binDir, "caveman-mcp");
  const proxy = join(binDir, "caveman-proxy");
  writeFileSync(mcp, "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"mcp_recovery\"]}'; fi\n", { mode: 0o755 });
  writeFileSync(proxy, "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"native_runtime_v1\",\"native_hook_bridge_v1\",\"typed_ccr\"]}'; fi\n", { mode: 0o755 });
  const home = mkdtempSync(join(tmpdir(), "cave-shortcut-native-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false, browse: false } }, null, 2));
  const env = {
    ...process.env,
    NO_COLOR: "1",
    CI: "1",
    HOME: home,
    CAVEMAN_HOME: home,
    PATH: `${binDir}:${process.env.PATH}`,
    CAVEMAN_MCP_BIN: mcp,
    CAVEMAN_PROXY_BIN: proxy,
  };
  delete env.CAVE_GATEWAY_URL;
  delete env.CLAUDE_CONFIG_DIR;
  return { env, home };
}

function runWithEnv(env, cliArgs) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...cliArgs], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

test("caveman claude enables the native integration and launches the agent directly", async () => {
  const { env, home } = nativeShortcutEnv();
  const out = await runWithEnv(env, ["claude", "-p", "hi"]);
  assert.equal(out.code, 0, `cli exited ${out.code}: ${out.stderr}`);
  assert.match(out.stderr, /caveman enable claude: planned user-scoped writes/);
  assert.match(out.stderr, /native Caveman enabled/);
  assert.ok(existsSync(join(home, "integrations", "claude.json")), "enable must journal the native install");
  const settings = readFileSync(join(home, ".claude", "settings.json"), "utf8");
  assert.match(settings, /ANTHROPIC_BASE_URL/, "native install must own routing in Claude settings");
  const [agentArgs, baseURL] = out.stdout.split("|");
  assert.equal(agentArgs, "-p hi", "direct launch must pass args verbatim with no temp wrap pack");
  assert.equal(baseURL, "", "direct launch must not inject ANTHROPIC_BASE_URL into the environment");

  // Second run: already enabled — quiet, still a direct launch.
  const again = await runWithEnv(env, ["claude", "-p", "hi"]);
  assert.equal(again.code, 0, `cli exited ${again.code}: ${again.stderr}`);
  assert.doesNotMatch(again.stderr, /planned user-scoped writes/, "an installed integration must not re-plan writes");
  const [againArgs] = again.stdout.split("|");
  assert.equal(againArgs, "-p hi");
});

// timeout(1)/supervisors SIGTERM the launcher pid, not the process group. The
// launcher must forward it to the child and then die by the same signal so
// callers see a signal death, not a clean exit.
test("process-directed SIGTERM reaches the child and re-raises on the launcher", async () => {
  const { env, home } = nativeShortcutEnv();
  const setup = await runWithEnv(env, ["claude", "-p", "hi"]);
  assert.equal(setup.code, 0, `enable run exited ${setup.code}: ${setup.stderr}`);
  const pidFile = join(home, "stub.pid");
  const binDir = dirname(env.CAVEMAN_MCP_BIN);
  writeFileSync(join(binDir, "claude"), `#!/bin/sh\nif [ "$1" = "--version" ]; then echo 'claude 1.0.0'; exit 0; fi\necho $$ > ${JSON.stringify(pidFile)}\nexec sleep 30\n`, { mode: 0o755 });
  const child = spawn("node", [cli, "claude"], { env });
  let stderr = "";
  child.stderr.on("data", (d) => (stderr += d));
  const deadline = Date.now() + 8000;
  while (!existsSync(pidFile) && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 50));
  assert.ok(existsSync(pidFile), `stub agent never started: ${stderr}`);
  const stubPid = Number(readFileSync(pidFile, "utf8").trim());
  child.kill("SIGTERM");
  const exit = await new Promise((resolve) => child.on("exit", (code, signal) => resolve({ code, signal })));
  assert.equal(exit.signal, "SIGTERM", `launcher must die by SIGTERM (code=${exit.code}, stderr=${stderr})`);
  await new Promise((resolve) => setTimeout(resolve, 200));
  assert.throws(() => process.kill(stubPid, 0), "forwarded SIGTERM must terminate the child");
});

test("caveman claude --off keeps the session-only wrap door even when native binaries conform", async () => {
  const { env } = nativeShortcutEnv();
  const out = await runWithEnv(env, ["claude", "--off"]);
  assert.equal(out.code, 0, `cli exited ${out.code}: ${out.stderr}`);
  const [agentArgs, baseURL] = out.stdout.split("|");
  assert.equal(userAgentArgs(agentArgs), "", "--off must route through the ephemeral wrap pack");
  assert.equal(baseURL, "http://127.0.0.1:8787/w/claude", "wrap must still inject the attributed local proxy URL");
});
