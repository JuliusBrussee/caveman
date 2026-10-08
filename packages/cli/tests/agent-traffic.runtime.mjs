import { test } from "node:test";
import assert from "node:assert/strict";
import { harness, modulesFixture, runCli } from "./_modules.mjs";

// Wiring an earlier login pointed at the managed gateway stays as it is until
// the person re-runs setup, on/off or enable; then it moves to the local runtime.
test("an earlier login's managed wiring moves to the local runtime only on the next module change", async () => {
  const fx = modulesFixture();
  try {
    const managed = { ...fx.env, CAVE_GATEWAY_URL: "https://gateway.example.test" };
    const on = await runCli(["on", "output", "--yes"], managed);
    assert.equal(on.code, 0, on.stderr);
    assert.match(harness(fx.home)[".claude/settings.json"], /gateway\.example\.test/);

    const local = { ...fx.env };
    delete local.CAVE_GATEWAY_URL;
    const status = await runCli(["status"], local);
    assert.match(status.stdout, /^agent traffic: managed gateway \(https:\/\/gateway\.example\.test, from an earlier login\) · caveman setup to use the local runtime$/m);
    assert.match(status.stdout, /^next: caveman setup$/m);
    assert.match(harness(fx.home)[".claude/settings.json"], /gateway\.example\.test/, "status never rewrites wiring");

    const plan = await runCli(["on", "output", "--dry-run"], local);
    assert.equal(plan.code, 0, plan.stderr);
    assert.match(plan.stdout, /UPDATE +~\/\.claude\/settings\.json +point claude at the local runtime \(was https:\/\/gateway\.example\.test\)/);

    const moved = await runCli(["on", "output", "--yes"], local);
    assert.equal(moved.code, 0, moved.stderr);
    assert.match(moved.stdout, /✓ Claude Code routing: local runtime/);
    const settings = harness(fx.home)[".claude/settings.json"];
    assert.doesNotMatch(settings, /gateway\.example\.test/);
    assert.match(settings, /"ANTHROPIC_BASE_URL": "http:\/\/127\.0\.0\.1:8787\/w\/claude"/);
    const after = await runCli(["status"], local);
    assert.match(after.stdout, /^agent traffic: local runtime$/m);
  } finally {
    fx.cleanup();
  }
});

// `caveman enable` re-points a stale route to the local runtime and says so,
// instead of refusing it as degraded.
test("enable moves an earlier login's managed wiring to the local runtime", async () => {
  const fx = modulesFixture({ agents: ["claude"] });
  try {
    const managed = { ...fx.env, CAVE_GATEWAY_URL: "https://gateway.example.test" };
    assert.equal((await runCli(["on", "output", "--yes"], managed)).code, 0);
    const local = { ...fx.env };
    delete local.CAVE_GATEWAY_URL;
    const out = await runCli(["enable", "claude"], local);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stderr, /routing: https:\/\/gateway\.example\.test → http:\/\/127\.0\.0\.1:8787/);
    assert.match(harness(fx.home)[".claude/settings.json"], /"ANTHROPIC_BASE_URL": "http:\/\/127\.0\.0\.1:8787\/w\/claude"/);
  } finally {
    fx.cleanup();
  }
});
