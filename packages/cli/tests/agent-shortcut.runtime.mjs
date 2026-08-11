import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

// Spawn the CLI with a stub agent binary on PATH, isolated HOME (so the loadout
// hook installer and gateway resolution never touch the real home dir), and no
// CAVE_GATEWAY_URL. The stub prints its args + the injected base URL to stdout.
function runShortcut(agentId, cliArgs) {
  const binDir = mkdtempSync(join(tmpdir(), `cave-shortcut-${agentId}-`));
  const stub = join(binDir, agentId);
  writeFileSync(stub, `#!/bin/sh\nprintf '%s|%s' "$*" "$ANTHROPIC_BASE_URL"\n`, { mode: 0o755 });
  const home = mkdtempSync(join(tmpdir(), "cave-shortcut-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false, browse: false } }, null, 2));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: home, PATH: `${binDir}:${process.env.PATH}` };
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

// `caveman claude` must behave exactly like `caveman wrap claude`: launch the
// agent with the local-proxy base URL injected.
test("caveman claude is shorthand for caveman wrap claude", async () => {
  const out = await runShortcut("claude", ["claude"]);
  assert.equal(out.code, 0, `cli exited ${out.code}: ${out.stderr}`);
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
