import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { harness, modulesFixture, runCli, snapshot } from "./_modules.mjs";

const planLines = (stdout) => stdout.split("\n").filter((line) => /^ {2}[A-Z]+ /.test(line)).map((line) => line.trim().split(/\s{2,}/));

test("fresh home: on --all --dry-run prints the whole plan and writes nothing", async () => {
  const fx = modulesFixture({ binaries: false });
  try {
    const before = snapshot(fx.home);
    const out = await runCli(["on", "--all", "--dry-run"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /^This will\n/);
    assert.deepEqual(planLines(out.stdout).map((line) => line.join(" | ").replace(/bin-v[\d.]+/, "bin-vX")), [
      "DOWNLOAD | caveman-proxy, caveman-engine, caveman-mcp, cavemem, caveman-shrink, caveman-browse | signed, bin-vX",
      "CREATE | ~/.caveman-cloud/config.json | modules on: output, input, waste-fixes, routing, scripts, browse",
      "CREATE | ~/.claude/settings.json | claude settings",
      "CREATE | ~/.claude.json | claude mcp",
      "CREATE | ~/.codex/hooks.json | codex hooks",
      "CREATE | ~/.codex/config.toml | codex config",
      "RUN | caveman-blocks hooks install",
    ]);
    assert.deepEqual(snapshot(fx.home), before, "--dry-run wrote to HOME");
  } finally {
    fx.cleanup();
  }
});

test("without --yes and without a terminal nothing is applied", async () => {
  const fx = modulesFixture();
  try {
    const before = snapshot(fx.home);
    const out = await runCli(["on", "--all"], fx.env);
    assert.equal(out.code, 1);
    assert.match(out.stderr, /nothing changed: pass --yes/);
    assert.deepEqual(snapshot(fx.home), before);
  } finally {
    fx.cleanup();
  }
});

test("on → off → on round-trips harness files byte for byte", async () => {
  const fx = modulesFixture({ blocks: true });
  try {
    mkdirSync(join(fx.home, ".claude"), { recursive: true });
    writeFileSync(join(fx.home, ".claude", "settings.json"), JSON.stringify({ env: { KEEP: "yes" }, theme: "dark" }, null, 2) + "\n");
    mkdirSync(join(fx.home, ".codex"), { recursive: true });
    writeFileSync(join(fx.home, ".codex", "config.toml"), 'model = "gpt-5"\n');
    const original = harness(fx.home);

    const on = await runCli(["on", "--all", "--yes"], fx.env);
    assert.equal(on.code, 0, on.stderr);
    const wired = harness(fx.home);
    assert.match(wired[".claude/settings.json"], /"ANTHROPIC_BASE_URL": "http:\/\/127\.0\.0\.1:9\/w\/claude"/);
    assert.match(wired[".claude/settings.json"], /"KEEP": "yes"/);
    assert.match(wired[".codex/config.toml"], /caveman:native-root/);
    assert.equal(readFileSync(join(fx.home, "blocks.log"), "utf8"), "install\n");

    const off = await runCli(["off", "--all", "--yes"], fx.env);
    assert.equal(off.code, 0, off.stderr);
    assert.deepEqual(harness(fx.home), original, "off --all left harness bytes behind");
    assert.equal(readFileSync(join(fx.home, "blocks.log"), "utf8"), "install\nuninstall\n");
    const config = JSON.parse(readFileSync(join(fx.home, ".caveman-cloud", "config.json"), "utf8"));
    assert.deepEqual(config.modules, { output: false, input: false, "waste-fixes": false, routing: false, scripts: false, browse: false });
    assert.deepEqual(config.think, { core: false, mode: "record", toon: false, shrink: false });
    assert.equal(config.execute.browse_tool, false);
    assert.equal(config.learnAutopilot, false);

    const again = await runCli(["on", "--all", "--yes"], fx.env);
    assert.equal(again.code, 0, again.stderr);
    assert.deepEqual(harness(fx.home), wired, "second on wrote different bytes");

    const idle = await runCli(["on", "--all", "--yes"], fx.env);
    assert.equal(idle.code, 0, idle.stderr);
    assert.match(idle.stdout, /✓ every module already on/);
  } finally {
    fx.cleanup();
  }
});

test("off keeps agent wiring while another module needs it", async () => {
  const fx = modulesFixture({ agents: ["claude"] });
  const journal = () => existsSync(join(fx.home, ".caveman", "integrations", "claude.json"));
  try {
    assert.equal((await runCli(["on", "--all", "--yes"], fx.env)).code, 0);
    assert.ok(journal());

    const input = await runCli(["off", "input", "--yes"], fx.env);
    assert.equal(input.code, 0, input.stderr);
    assert.deepEqual(planLines(input.stdout), [
      ["UPDATE", "~/.caveman-cloud/config.json", "modules off: input · think.mode = record · think.toon = false · think.shrink = false"],
    ]);
    assert.ok(journal(), "off input removed wiring output still needs");

    assert.equal((await runCli(["off", "output", "waste-fixes", "--yes"], fx.env)).code, 0);
    assert.ok(journal(), "wiring must stay while routing is on");

    const before = snapshot(fx.home);
    const dry = await runCli(["off", "routing", "--dry-run"], fx.env);
    assert.equal(dry.code, 0, dry.stderr);
    assert.deepEqual(planLines(dry.stdout), [
      ["UPDATE", "~/.caveman-cloud/config.json", "modules off: routing"],
      ["UPDATE", "~/.claude/settings.json", "remove claude wiring"],
      ["UPDATE", "~/.claude.json", "remove claude wiring"],
    ]);
    assert.deepEqual(snapshot(fx.home), before, "--dry-run wrote to HOME");

    const last = await runCli(["off", "routing", "--yes"], fx.env);
    assert.equal(last.code, 0, last.stderr);
    assert.equal(journal(), false, "last agent-wired module off must unwire");
    assert.equal(existsSync(join(fx.home, ".claude", "settings.json")), false);
  } finally {
    fx.cleanup();
  }
});

test("on/off reject unknown modules and bad flags", async () => {
  const fx = modulesFixture();
  try {
    const unknown = await runCli(["on", "nope"], fx.env);
    assert.equal(unknown.code, 2);
    assert.match(unknown.stderr, /unknown module: nope · modules: output, input, waste-fixes, routing, scripts, browse/);
    assert.equal((await runCli(["off"], fx.env)).code, 2);
    assert.equal((await runCli(["on", "--all", "output"], fx.env)).code, 2);
    assert.equal((await runCli(["on", "output", "--force"], fx.env)).code, 2);
  } finally {
    fx.cleanup();
  }
});
