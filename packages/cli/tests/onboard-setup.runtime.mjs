import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, relative } from "node:path";
import { isolatedCliEnv, runCli } from "./_cli.mjs";

// Non-interactive first run (`caveman setup --yes`, CI, no TTY): every module
// on, every detected agent, the plan printed, no sign-in prompt. Module state
// lands in $CAVEMAN_HOME/cloud.json (isolatedCliEnv sets CI=1 and
// CAVEMAN_HOME=HOME).
const skip = process.platform === "win32" ? "shell agent stubs" : false;

function setupEnv() {
  const isolated = isolatedCliEnv();
  const bin = join(isolated.home, "bin");
  writeFileSync(join(bin, "claude"), "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '2.1.37 (Claude Code)'; exit 0; fi\necho \"claude $*\"\n", { mode: 0o755 });
  writeFileSync(join(bin, "codex"), "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'codex-cli 0.48.0'; exit 0; fi\necho \"codex $*\"\n", { mode: 0o755 });
  // Only the stub agents are detectable, never whatever the host has installed.
  isolated.env.PATH = `${bin}:/usr/bin:/bin`;
  return isolated;
}

function modules(home) {
  return JSON.parse(readFileSync(join(home, "cloud.json"), "utf8")).modules;
}

function tree(root) {
  const out = [];
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      out.push(relative(root, path));
      if (entry.isDirectory()) walk(path);
    }
  };
  walk(root);
  return out.sort();
}

test("setup --yes on a fresh home turns every module on for the detected agents", { skip }, async () => {
  const isolated = setupEnv();
  try {
    const out = await runCli(["setup", "--yes"], { env: isolated.env });
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /^caveman · make your coding agent cheaper\n\nFound Claude Code 2\.1 and Codex 0\.48\n/);
    assert.match(out.stdout, /\nModules {2}output · input · waste fixes · routing · scripts · browse\nAgents {3}Claude Code · Codex\n/);
    assert.match(out.stdout, /\nThis will\n {2}[A-Z]+ /, "the plan is printed");
    assert.match(out.stdout, /routing is on and starts after you sign in · caveman login/, "no sign-in prompt without a terminal");
    assert.match(out.stdout, /✓ Ready\. Try: {2}caveman claude {6}See it: {2}caveman status\n/);
    assert.deepEqual(modules(isolated.home), { output: true, input: true, "waste-fixes": true, routing: true, scripts: true, browse: true });
  } finally {
    isolated.cleanup();
  }
});

test("setup --dry-run prints the plan and writes nothing", { skip }, async () => {
  const isolated = setupEnv();
  try {
    const before = tree(isolated.home);
    const out = await runCli(["setup", "--dry-run"], { env: isolated.env });
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /\nThis will\n/);
    assert.match(out.stdout, /Dry run: nothing was written\.\n$/);
    assert.deepEqual(tree(isolated.home), before);
  } finally {
    isolated.cleanup();
  }
});

test("--only, --skip and --agents pick modules and agents; a re-run keeps the current state", { skip }, async () => {
  const isolated = setupEnv();
  try {
    const first = await runCli(["setup", "--yes", "--only", "output,input,browse", "--skip=browse", "--agents", "codex"], { env: isolated.env });
    assert.equal(first.code, 0, first.stderr);
    assert.match(first.stdout, /\nModules {2}output · input\nAgents {3}Codex\n/);
    assert.doesNotMatch(first.stdout, /routing is on/, "routing off means no sign-in line");
    assert.match(first.stdout, /Try: {2}caveman codex /);
    assert.deepEqual(modules(isolated.home), { output: true, input: true, "waste-fixes": false, routing: false, scripts: false, browse: false });

    const again = await runCli(["setup", "--yes"], { env: isolated.env });
    assert.equal(again.code, 0, again.stderr);
    assert.match(again.stdout, /\nModules {2}output · input\n/, "re-running starts from what is on, not the defaults");
  } finally {
    isolated.cleanup();
  }
});

test("setup refuses unknown modules and agents that are not installed", { skip }, async () => {
  const isolated = setupEnv();
  try {
    const badModule = await runCli(["setup", "--only", "output,teleport"], { env: isolated.env });
    assert.equal(badModule.code, 2);
    assert.match(badModule.stderr, /unknown module teleport · modules: output, input, waste-fixes, routing, scripts, browse/);
    const badAgent = await runCli(["setup", "--yes", "--agents", "gemini"], { env: isolated.env });
    assert.equal(badAgent.code, 1);
    assert.match(badAgent.stderr, /--agents: gemini is not installed here · found: claude, codex/);
    assert.equal(existsSync(join(isolated.home, "cloud.json")), false);
  } finally {
    isolated.cleanup();
  }
});

test("setup --json still reports binary status for scripts", async () => {
  const isolated = isolatedCliEnv();
  try {
    const out = await runCli(["setup", "--json"], { env: isolated.env });
    const report = JSON.parse(out.stdout);
    assert.ok(Array.isArray(report.binaries) && report.binaries.some((b) => b.name === "caveman-proxy"));
    assert.equal(typeof report.ready, "boolean");
    assert.equal(existsSync(join(isolated.home, "cloud.json")), false, "--json is read-only");
  } finally {
    isolated.cleanup();
  }
});
