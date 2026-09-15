import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function fixtureEnv() {
  const home = mkdtempSync(join(tmpdir(), "cave-aider-home-"));
  const binDir = mkdtempSync(join(tmpdir(), "cave-aider-bin-"));
  const caveDir = join(home, ".caveman-cloud");
  mkdirSync(caveDir, { recursive: true });
  writeFileSync(join(caveDir, "config.json"), JSON.stringify({
    firstRunAt: new Date().toISOString(),
    wrap: { proxy: false, browse: false },
  }));
  writeFileSync(join(binDir, "aider"), `#!/usr/bin/env node
process.stdout.write(JSON.stringify({
  args: process.argv.slice(2),
  baseUrl: process.env.OPENAI_API_BASE || null,
}));
`, { mode: 0o755 });
  const env = {
    ...process.env,
    HOME: home,
    NO_COLOR: "1",
    PATH: `${binDir}:${process.env.PATH}`,
  };
  delete env.CAVE_GATEWAY_URL;
  delete env.CAVE_API_KEY;
  return env;
}

function runCli(argv, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

test("wrap aider selects its OpenAI-compatible provider when model is absent", async () => {
  const out = await runCli(["wrap", "aider", "--yes"], fixtureEnv());
  assert.equal(out.code, 0, out.stderr);
  const child = JSON.parse(out.stdout);
  assert.deepEqual(child.args, ["--model", "openai/gpt-5.5", "--yes"]);
  assert.equal(child.baseUrl, "http://127.0.0.1:8787/w/aider/openai/v1");
  assert.match(out.stderr, /aider model not set; routing with --model openai\/gpt-5\.5/);
});

test("wrap aider preserves an explicit OpenAI-compatible model", async () => {
  const out = await runCli(["wrap", "aider", "--model=openai/gpt-5.4-mini", "--yes"], fixtureEnv());
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(JSON.parse(out.stdout).args, ["--model=openai/gpt-5.4-mini", "--yes"]);
  assert.doesNotMatch(out.stderr, /traffic may bypass Caveman/);
});

test("wrap aider treats -m as message and still inserts a routed model", async () => {
  const out = await runCli(["wrap", "aider", "-m", "fix the bug"], fixtureEnv());
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(JSON.parse(out.stdout).args, ["--model", "openai/gpt-5.5", "-m", "fix the bug"]);
  assert.doesNotMatch(out.stderr, /traffic may bypass Caveman/);
});

test("wrap aider warns when an explicit model can bypass the routed provider", async () => {
  const out = await runCli(["wrap", "aider", "--model", "anthropic/claude-sonnet-4", "--yes"], fixtureEnv());
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(JSON.parse(out.stdout).args, ["--model", "anthropic/claude-sonnet-4", "--yes"]);
  assert.match(out.stderr, /aider model "anthropic\/claude-sonnet-4" does not select its OpenAI-compatible provider; traffic may bypass Caveman/);
});

test("wrap aider warns from the last effective repeated model selector", async () => {
  const out = await runCli([
    "wrap", "aider",
    "--model", "openai/gpt-5.5",
    "--model=anthropic/claude-sonnet-4",
    "--yes",
  ], fixtureEnv());
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(JSON.parse(out.stdout).args, [
    "--model", "openai/gpt-5.5",
    "--model=anthropic/claude-sonnet-4",
    "--yes",
  ]);
  assert.match(out.stderr, /aider model "anthropic\/claude-sonnet-4" does not select its OpenAI-compatible provider; traffic may bypass Caveman/);
});

test("wrap aider warns on a malformed model selector instead of claiming a routed model", async () => {
  const out = await runCli(["wrap", "aider", "--model", "--yes"], fixtureEnv());
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(JSON.parse(out.stdout).args, ["--model", "--yes"]);
  assert.match(out.stderr, /aider --model requires a value; Aider may reject this launch before routing/);
  assert.doesNotMatch(out.stderr, /model not set; routing with/);
});
