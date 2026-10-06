import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { isolatedCliEnv, runCli as runIsolated } from "./_cli.mjs";
import { modulesFixture, runCli, snapshot } from "./_modules.mjs";

// Non-interactive first run (`caveman setup --yes`, CI, no TTY): every module
// on, every detected agent, the plan printed, no sign-in prompt. Module state
// lands in $CAVEMAN_HOME/cloud.json. The fixture stubs every module binary, so
// nothing downloads.
const skip = process.platform === "win32" ? "shell agent stubs" : false;

function modules(fx) {
  return JSON.parse(readFileSync(join(fx.env.CAVEMAN_HOME, "cloud.json"), "utf8")).modules;
}

test("setup --yes on a fresh home turns every module on for the detected agents", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const out = await runCli(["setup", "--yes"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /^caveman · make your coding agent cheaper\n\nFound Claude Code 1\.0 and Codex 1\.0\n/);
    assert.match(out.stdout, /\nModules {2}output · input · waste fixes · routing · scripts · browse\nAgents {3}Claude Code · Codex\n/);
    assert.match(out.stdout, /\nThis will\n(?: {2}[A-Z]+ .*\n)*  CREATE +~\/\.caveman\/cloud\.json +modules on: output, input, waste-fixes, routing, scripts, browse\n/, "the plan is printed");
    assert.match(out.stdout, /\n {2}CREATE +~\/\.claude\/settings\.json +claude settings\n/);
    assert.match(out.stdout, /routing is on and starts after you sign in · caveman login/, "no sign-in prompt without a terminal");
    assert.match(out.stdout, /✓ Ready\. Try: {2}caveman claude {6}See it: {2}caveman status\n/);
    assert.deepEqual(modules(fx), { output: true, input: true, "waste-fixes": true, routing: true, scripts: true, browse: true });
  } finally {
    fx.cleanup();
  }
});

test("setup --dry-run prints the plan and writes nothing", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const before = snapshot(fx.home);
    const out = await runCli(["setup", "--dry-run"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /\nThis will\n/);
    assert.match(out.stdout, /Dry run: nothing was written\.\n$/);
    assert.deepEqual(snapshot(fx.home), before);
  } finally {
    fx.cleanup();
  }
});

test("--only, --skip and --agents pick modules and agents; a re-run keeps the current state", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const first = await runCli(["setup", "--yes", "--only", "output,input,browse", "--skip=browse", "--agents", "codex"], fx.env);
    assert.equal(first.code, 0, first.stderr);
    assert.match(first.stdout, /\nModules {2}output · input\nAgents {3}Codex\n/);
    assert.doesNotMatch(first.stdout, /routing is on/, "routing off means no sign-in line");
    assert.match(first.stdout, /Try: {2}caveman codex /);
    assert.deepEqual(modules(fx), { output: true, input: true, "waste-fixes": false, routing: false, scripts: false, browse: false });

    const again = await runCli(["setup", "--yes"], fx.env);
    assert.equal(again.code, 0, again.stderr);
    assert.match(again.stdout, /\nModules {2}output · input\n/, "re-running starts from what is on, not the defaults");
  } finally {
    fx.cleanup();
  }
});

test("setup refuses unknown modules and agents that are not installed", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const badModule = await runCli(["setup", "--only", "output,teleport"], fx.env);
    assert.equal(badModule.code, 2);
    assert.match(badModule.stderr, /unknown module teleport · modules: output, input, waste-fixes, routing, scripts, browse/);
    const badAgent = await runCli(["setup", "--yes", "--agents", "gemini"], fx.env);
    assert.equal(badAgent.code, 1);
    assert.match(badAgent.stderr, /--agents: gemini is not installed here · found: claude, codex/);
    assert.equal(existsSync(join(fx.env.CAVEMAN_HOME, "cloud.json")), false);
  } finally {
    fx.cleanup();
  }
});

test("setup --json still reports binary status for scripts", async () => {
  const isolated = isolatedCliEnv();
  try {
    const out = await runIsolated(["setup", "--json"], { env: isolated.env });
    const report = JSON.parse(out.stdout);
    assert.ok(Array.isArray(report.binaries) && report.binaries.some((b) => b.name === "caveman-proxy"));
    assert.equal(typeof report.ready, "boolean");
    assert.equal(existsSync(join(isolated.home, "cloud.json")), false, "--json is read-only");
  } finally {
    isolated.cleanup();
  }
});
