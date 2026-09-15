import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, writeFileSync, readFileSync, mkdirSync, existsSync, chmodSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

// A fake `cavemem` whose `recall` returns hits (or an empty set when emptyHits).
function fakeCavemem(emptyHits = false) {
  const dir = mkdtempSync(join(tmpdir(), "cave-cavemem-"));
  const bin = join(dir, "cavemem");
  const recall = emptyHits
    ? `echo '{"hits":[],"basis":"inferred"}'`
    : `echo '{"hits":[{"id":"mem_a","text":"the billing service owns gainshare math","score":0.6,"tokens_added":9,"basis":"inferred","recovery_handle":"ccr_abc"}],"basis":"inferred"}'`;
  writeFileSync(bin, `#!/usr/bin/env bash\ncase "$1" in\n  recall) ${recall};;\n  *) echo '{}';;\nesac\n`, { mode: 0o755 });
  chmodSync(bin, 0o755);
  return bin;
}

function runHook(payloadRaw, env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "mem", "recall-hook"], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
    child.stdin.end(payloadRaw);
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
    child.stdin.end();
  });
}

function writeWrapConfig(home, wrap) {
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap }, null, 2));
}

// ── the recall-hook callback ─────────────────────────────────────────────────

test("recall-hook injects above-threshold hits as priced additionalContext", async () => {
  const env = { ...process.env, CAVEMEM_BIN: fakeCavemem() };
  const out = await runHook(JSON.stringify({ prompt: "what owns gainshare math?" }), env);
  assert.equal(out.code, 0, out.stderr);
  const o = JSON.parse(out.stdout);
  assert.equal(o.hookSpecificOutput.hookEventName, "UserPromptSubmit");
  assert.match(o.hookSpecificOutput.additionalContext, /\+9 tokens · basis inferred/);
  assert.match(o.hookSpecificOutput.additionalContext, /recover: caveman mem recover ccr_abc/);
});

// Fail-open matrix: every problem path must exit 0 with no output (never blocks).
for (const [label, payload, emptyHits, bin] of [
  ["malformed stdin", "not json", false, undefined],
  ["missing prompt", JSON.stringify({ foo: 1 }), false, undefined],
  ["empty hits", JSON.stringify({ prompt: "nothing relevant" }), true, undefined],
  ["missing cavemem", JSON.stringify({ prompt: "x" }), false, join(tmpdir(), "no-cavemem-xyz")],
]) {
  test(`recall-hook is a silent no-op: ${label}`, async () => {
    const env = { ...process.env, CAVEMEM_BIN: bin ?? fakeCavemem(emptyHits) };
    const out = await runHook(payload, env);
    assert.equal(out.code, 0);
    assert.equal(out.stdout, "", `must not inject on: ${label}`);
  });
}

// ── install / uninstall ──────────────────────────────────────────────────────

test("mem hook install claude registers a UserPromptSubmit hook (no matcher) and a marker", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  const out = await runCli(["mem", "hook", "install", "claude"], env);
  assert.equal(out.code, 0, out.stderr);
  const settings = JSON.parse(readFileSync(join(home, ".claude", "settings.json"), "utf8"));
  const ups = settings.hooks.UserPromptSubmit;
  assert.ok(Array.isArray(ups) && ups.length === 1, "one UserPromptSubmit entry");
  assert.equal(ups[0].matcher, undefined, "UserPromptSubmit entries carry no tool matcher");
  assert.ok(ups[0].hooks.some((h) => (h.command || "").includes("mem recall-hook")), "calls mem recall-hook");
  assert.ok(existsSync(join(caveDir, "recall-hooks", "claude.json")), "a recall-hook marker must be written");
});

test("mem hook install is idempotent and preserves the user's own UserPromptSubmit hooks", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".claude"), { recursive: true });
  writeFileSync(
    join(home, ".claude", "settings.json"),
    JSON.stringify({ hooks: { UserPromptSubmit: [{ hooks: [{ type: "command", command: "my-own-prompt-hook.sh" }] }] } }, null, 2),
  );
  await runCli(["mem", "hook", "install", "claude"], env);
  await runCli(["mem", "hook", "install", "claude"], env); // twice → still one caveman entry
  const ups = JSON.parse(readFileSync(join(home, ".claude", "settings.json"), "utf8")).hooks.UserPromptSubmit;
  assert.equal(ups.filter((e) => e.hooks.some((h) => (h.command || "").includes("mem recall-hook"))).length, 1, "exactly one caveman recall hook");
  assert.ok(ups.some((e) => e.hooks.some((h) => h.command === "my-own-prompt-hook.sh")), "the user's own hook must remain");
});

test("mem hook install refuses to corrupt a non-object settings.json", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  mkdirSync(join(home, ".claude"), { recursive: true });
  const original = JSON.stringify("not an object");
  writeFileSync(join(home, ".claude", "settings.json"), original);
  await runCli(["mem", "hook", "install", "claude"], env);
  assert.equal(readFileSync(join(home, ".claude", "settings.json"), "utf8"), original, "must leave a non-object file unchanged");
});

test("mem hook uninstall removes only the caveman hook and drops the marker", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".claude"), { recursive: true });
  writeFileSync(
    join(home, ".claude", "settings.json"),
    JSON.stringify({ hooks: { UserPromptSubmit: [{ hooks: [{ type: "command", command: "my-own-prompt-hook.sh" }] }] } }, null, 2),
  );
  await runCli(["mem", "hook", "install", "claude"], env);
  await runCli(["mem", "hook", "uninstall", "claude"], env);
  const ups = JSON.parse(readFileSync(join(home, ".claude", "settings.json"), "utf8")).hooks.UserPromptSubmit;
  assert.ok(!ups.some((e) => e.hooks.some((h) => (h.command || "").includes("mem recall-hook"))), "caveman hook must be gone");
  assert.ok(ups.some((e) => e.hooks.some((h) => h.command === "my-own-prompt-hook.sh")), "the user's own hook must remain");
  assert.ok(!existsSync(join(caveDir, "recall-hooks", "claude.json")), "the marker must be removed");
});

// ── wrap auto_recall config is opt-in (off by default) ───────────────────────

test("wrap auto_recall stays ephemeral; plain wrap writes no recall state", async () => {
  const binDir = mkdtempSync(join(tmpdir(), "cave-claude-"));
  writeFileSync(join(binDir, "claude"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });

  // opt-in is loaded through temp Claude plugin, never user settings.
  const home1 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cave1 = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env1 = { ...process.env, NO_COLOR: "1", HOME: home1, CAVEMAN_HOME: cave1, PATH: `${binDir}:${process.env.PATH}` };
  delete env1.CAVE_GATEWAY_URL;
  writeWrapConfig(home1, { proxy: false, browse: false, auto_recall: true });
  const w1 = await runCli(["wrap", "claude"], env1);
  assert.equal(w1.code, 0, w1.stderr);
  assert.ok(!existsSync(join(home1, ".claude", "settings.json")));
  assert.ok(!existsSync(join(cave1, "recall-hooks", "claude.json")));

  // default → does NOT install auto-recall
  const home2 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cave2 = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env2 = { ...process.env, NO_COLOR: "1", HOME: home2, CAVEMAN_HOME: cave2, PATH: `${binDir}:${process.env.PATH}` };
  delete env2.CAVE_GATEWAY_URL;
  writeWrapConfig(home2, { proxy: false, browse: false });
  const w2 = await runCli(["wrap", "claude"], env2);
  assert.equal(w2.code, 0, w2.stderr);
  assert.ok(!existsSync(join(cave2, "recall-hooks", "claude.json")), "plain wrap must not enable auto-recall");
  assert.ok(!existsSync(join(home2, ".claude", "settings.json")));
});
