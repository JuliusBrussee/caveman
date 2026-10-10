import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { join } from "node:path";
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

// Moving that wiring to the local runtime gives the agent a new address, so it
// gets a first run's port check: never a port another program answers on.
test("setup never moves an earlier login's managed wiring onto a port another program holds", async () => {
  const fx = modulesFixture({ agents: ["claude"] });
  const holder = createServer();
  await new Promise((resolve) => holder.listen(0, "127.0.0.1", resolve));
  const held = holder.address().port;
  try {
    assert.equal((await runCli(["on", "output", "--yes"], { ...fx.env, CAVE_GATEWAY_URL: "https://gateway.example.test" })).code, 0);
    const local = { ...fx.env };
    delete local.CAVE_GATEWAY_URL;
    delete local.CAVEMAN_LISTEN;
    // The runtime's port is the held one, as 8787 is on a machine running wrangler dev.
    const config = join(fx.env.CAVEMAN_HOME, "cloud.json");
    writeFileSync(config, JSON.stringify({ ...JSON.parse(readFileSync(config, "utf8")), localPort: held }));
    const out = await runCli(["setup", "--yes"], local);
    assert.equal(out.code, 0, out.stdout + out.stderr);
    const port = out.stdout.match(new RegExp(`○ 127\\.0\\.0\\.1:${held} is in use by another program · local runtime on port (\\d+)\\n`))?.[1];
    assert.ok(port, out.stdout);
    assert.match(harness(fx.home)[".claude/settings.json"], new RegExp(`"ANTHROPIC_BASE_URL": "http://127\\.0\\.0\\.1:${port}/w/claude"`));
  } finally {
    holder.close();
    fx.cleanup();
  }
});
