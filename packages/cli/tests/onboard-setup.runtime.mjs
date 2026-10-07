import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { cpSync, existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { isolatedCliEnv, runCli as runIsolated } from "./_cli.mjs";
import { modulesFixture, runCli, snapshot } from "./_modules.mjs";

// Non-interactive first run (no TTY, or CI): the plan is printed, and only
// --yes applies it — every module on, every detected agent, no sign-in prompt. Module state
// lands in $CAVEMAN_HOME/cloud.json. The fixture stubs every module binary, so
// nothing downloads.
const skip = process.platform === "win32" ? "shell agent stubs" : false;
const here = dirname(fileURLToPath(import.meta.url));

function modules(fx) {
  return JSON.parse(readFileSync(join(fx.env.CAVEMAN_HOME, "cloud.json"), "utf8")).modules;
}

test("setup --yes on a fresh home turns every module on for the detected agents", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const out = await runCli(["setup", "--yes"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /^caveman · make your coding agent cheaper\n\nFound Claude Code and Codex\n/);
    assert.match(out.stdout, /\nModules {2}output · input · waste fixes · routing · scripts · browse\nAgents {3}Claude Code · Codex\n/);
    assert.match(out.stdout, /\nThis will\n(?: {2}[A-Z]+ .*\n)*  CREATE +~\/\.caveman\/cloud\.json +modules on: output, input, waste-fixes, routing, scripts, browse\n/, "the plan is printed");
    assert.match(out.stdout, /\n {2}CREATE +~\/\.claude\/settings\.json +claude settings\n/);
    assert.match(out.stdout, /\n✓ Claude Code wired\n✓ Codex wired\n/, "one progress line per step");
    assert.doesNotMatch(out.stderr, /planned user-scoped writes|native Caveman enabled/, "enable's own report stays out of the first run");
    assert.match(out.stdout, /routing is on and starts after you sign in · caveman login/, "no sign-in prompt without a terminal");
    assert.match(out.stdout, /✓ Ready\. Try: {2}caveman claude {6}See it: {2}caveman status\n/);
    assert.deepEqual(modules(fx), { output: true, input: true, "waste-fixes": true, routing: true, scripts: true, browse: true });
  } finally {
    fx.cleanup();
  }
});

// Signed in, setup turns routing on and it starts right away: setup says what
// routing sends.
test("setup --yes signed in says what routing sends", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const out = await runCli(["setup", "--yes"], { ...fx.env, CAVE_TOKEN: "test-token" });
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /^routing is on · sends your latest ask \(with what your agent attaches to it\), the one before it, the end of the agent's last reply and request facts \(tools, effort, agent headers, token counts\) to Caveman Cloud to pick the model and effort; on the Free plan Caveman may keep them to improve routing · caveman off routing to stop$/m);
    assert.doesNotMatch(out.stdout, /starts after you sign in/);
    assert.equal(modules(fx).routing, true);
  } finally {
    fx.cleanup();
  }
});

// Blocks' installer talks (binary copied, one line per harness, trust notes);
// setup says one line, plus the one thing the user still has to do.
test("setup --yes says one line for scripts and keeps only the caveat that needs the user", { skip }, async () => {
  const fx = modulesFixture({ blocks: true });
  try {
    const out = await runCli(["setup", "--yes"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /\n✓ scripts ready \(Claude Code, Codex\)\n {2}Codex asks once: open \/hooks in Codex and approve the caveman-blocks hook\n/);
    assert.doesNotMatch(out.stdout + out.stderr, /binary:|: installed|trusts each/, "Blocks' own output stays out");
    const again = await runCli(["off", "scripts", "--yes"], fx.env);
    assert.equal(again.code, 0, again.stderr);
    const on = await runCli(["on", "scripts", "--yes"], fx.env);
    assert.match(on.stdout, /^✓ scripts ready \(Claude Code, Codex\)$/m);
  } finally {
    fx.cleanup();
  }
});

test("setup without --yes and no terminal prints the plan, applies nothing, exits 0", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const before = snapshot(fx.home);
    const out = await runCli(["setup"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /\nThis will\n/);
    assert.match(out.stdout, /Nothing changed: pass --yes to apply · caveman setup --yes\n$/);
    assert.deepEqual(snapshot(fx.home), before);
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
    assert.match(again.stdout, /\nFound Claude Code and Codex 1\.0\n/, "a wired agent's version comes from its journal, not from running it");
  } finally {
    fx.cleanup();
  }
});

// Under npx nothing named caveman is on PATH, so every hint names the npx form.
test("under npx the hints print the npx command, and Try prefers Claude Code", { skip }, async () => {
  const fx = modulesFixture({ agents: ["codex", "claude"] });
  const npxDist = join(fx.home, "_npx", "0a1b", "node_modules", "@caveman-ai", "cli");
  cpSync(join(here, "..", "dist"), join(npxDist, "dist"), { recursive: true });
  cpSync(join(here, "..", "package.json"), join(npxDist, "package.json"));
  try {
    const out = await new Promise((resolve, reject) => {
      const child = spawn(process.execPath, [join(npxDist, "dist", "index.js"), "setup", "--yes", "--agents", "codex,claude"], { env: fx.env, stdio: ["ignore", "pipe", "pipe"] });
      let stdout = "";
      child.stdout.on("data", (d) => (stdout += d));
      child.on("exit", (code) => resolve({ code, stdout }));
      child.on("error", reject);
    });
    assert.equal(out.code, 0, out.stdout);
    assert.match(out.stdout, /routing is on and starts after you sign in · npx @caveman-ai\/cli login/);
    assert.match(out.stdout, /Try: {2}npx @caveman-ai\/cli claude {6}See it: {2}npx @caveman-ai\/cli status/);
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
