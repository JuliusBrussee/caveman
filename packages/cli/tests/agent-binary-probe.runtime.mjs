import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { nodeStub } from "./harness/stub-bin.mjs";

const cliDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const packageParent = join(cliDir, "..");
const surfaceRoot = existsSync(join(packageParent, "agents")) ? packageParent : join(packageParent, "..");
const probe = join(surfaceRoot, "agents", "probe-installed.mjs");

function fixture(t, exitHelp = 0) {
  const dir = mkdtempSync(join(tmpdir(), "cave agent probe test "));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  nodeStub(dir, "aider", `if (ARGV[0] === "--version") console.log("aider v0.86.2 (2026.7.1)");
else if (ARGV[0] === "--help") { console.log("usage: aider"); process.exit(${exitHelp}); }
else process.exit(2);`);
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => key.toLowerCase() !== "path"));
  // Only fixture agents may be probed. nodeStub embeds process.execPath on POSIX;
  // the Windows .cmd is unwrapped into that same executable without a shell.
  env.PATH = dir;
  env.PATHEXT = ".EXE;.CMD";
  return { dir, env };
}

function run(item, required = "aider") {
  return spawnSync(process.execPath, [probe, "--require", required, "--json"], { env: item.env, encoding: "utf8" });
}

test("agent binary probe withholds credentials and isolates every OS home", (t) => {
  const item = fixture(t);
  const capture = join(item.dir, "captured-env.json");
  nodeStub(item.dir, "aider", `import { writeFileSync, existsSync } from "node:fs";
writeFileSync(${JSON.stringify(capture)}, JSON.stringify({ env: process.env, codexHomeExists: existsSync(process.env.CODEX_HOME) }));
if (ARGV[0] === "--version") console.log("aider v0.86.2 (2026.7.1)");
else if (ARGV[0] === "--help") console.log("usage: aider");
else process.exit(2);`);
  Object.assign(item.env, {
    OPENAI_API_KEY: "secret-openai", CAVE_API_KEY: "secret-cave", AWS_SECRET_ACCESS_KEY: "secret-aws",
    USERPROFILE: join(item.dir, "real user"), APPDATA: join(item.dir, "real roaming"),
    LOCALAPPDATA: join(item.dir, "real local"), XDG_CONFIG_HOME: join(item.dir, "real xdg"),
  });
  const result = run(item);
  assert.equal(result.status, 0, result.stderr);
  const captured = JSON.parse(readFileSync(capture, "utf8"));
  for (const key of ["OPENAI_API_KEY", "CAVE_API_KEY", "AWS_SECRET_ACCESS_KEY"]) {
    assert.equal(key in captured.env, false, `${key} reached probed binary`);
  }
  for (const key of ["HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "CODEX_HOME"]) {
    assert.match(captured.env[key], /cave-probe-aider-/, `${key} must stay isolated`);
  }
  assert.equal(captured.codexHomeExists, true, "explicit CODEX_HOME must be valid before launch");
  assert.equal(existsSync(captured.env.HOME), false, "isolated home cleaned up after probes");
});

test("agent binary probe requires both tested version and runnable help surface", (t) => {
  const ok = fixture(t);
  const broken = fixture(t, 9);
  const pass = run(ok);
  assert.equal(pass.status, 0, pass.stderr);
  const result = JSON.parse(pass.stdout).results.find((entry) => entry.id === "aider");
  assert.equal(result.status, "ok");
  assert.equal(result.version_matches, true);

  const fail = run(broken);
  assert.equal(fail.status, 1, "a binary whose help command fails must block release");
  assert.equal(JSON.parse(fail.stdout).results.find((entry) => entry.id === "aider").help_ok, false);
});

test("agent binary probe isolates a required profile from unrelated global drift", (t) => {
  const item = fixture(t);
  nodeStub(item.dir, "claude", `if (ARGV[0] === "--version") console.log("0.0.1");
else if (ARGV[0] === "--help") process.exit(9);`);
  const result = run(item);
  assert.equal(result.status, 0, result.stderr);
  const parsed = JSON.parse(result.stdout).results;
  assert.equal(parsed.find((entry) => entry.id === "aider").status, "ok");
  assert.equal(parsed.find((entry) => entry.id === "claude").status, "broken", "drift stays visible without breaking another matrix cell");
});

for (const [version, status, exit] of [
  ["0.86.2-rc.1", "broken", 1],
  ["0.86.1", "broken", 1],
  ["0.86.3-beta.1", "drift", 0],
  ["0.86.3", "drift", 0],
]) {
  test(`allow-newer classifies ${version} against stable pin 0.86.2 as ${status}`, (t) => {
    const item = fixture(t);
    nodeStub(item.dir, "aider", `console.log(ARGV[0] === "--version" ? ${JSON.stringify(version)} : "usage: aider");`);
    const result = spawnSync(process.execPath, [probe, "--require", "aider", "--allow-newer", "--json"], { env: item.env, encoding: "utf8" });
    assert.equal(result.status, exit, result.stderr);
    assert.equal(JSON.parse(result.stdout).results.find((entry) => entry.id === "aider").status, status);
  });
}
