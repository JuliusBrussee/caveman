import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cliDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const packageParent = join(cliDir, "..");
const surfaceRoot = existsSync(join(packageParent, "agents")) ? packageParent : join(packageParent, "..");
const probe = join(surfaceRoot, "agents", "probe-installed.mjs");

function fixture(exitHelp = 0) {
  const dir = mkdtempSync(join(tmpdir(), "cave-agent-probe-test-"));
  const binary = join(dir, "aider");
  writeFileSync(binary, `#!/bin/sh\nif [ "$1" = "--version" ]; then echo 'aider v0.86.2 (2026.7.1)'; exit 0; fi\nif [ "$1" = "--help" ]; then echo 'usage: aider'; exit ${exitHelp}; fi\nexit 2\n`, { mode: 0o755 });
  return { dir, env: { ...process.env, PATH: `${dir}${delimiter}/usr/bin${delimiter}/bin` } };
}

test("agent binary probe withholds ambient credentials from installed binaries", () => {
  const item = fixture();
  const capture = join(item.dir, "captured-env.json");
  const binary = join(item.dir, "aider");
  symlinkSync(process.execPath, join(item.dir, "node"));
  writeFileSync(binary, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
writeFileSync(${JSON.stringify(capture)}, JSON.stringify(process.env));
if (process.argv[2] === "--version") console.log("aider v0.86.2 (2026.7.1)");
else if (process.argv[2] === "--help") console.log("usage: aider");
else process.exit(2);
`, { mode: 0o755 });
  item.env.OPENAI_API_KEY = "secret-openai";
  item.env.CAVE_API_KEY = "secret-cave";
  item.env.AWS_SECRET_ACCESS_KEY = "secret-aws";
  try {
    const result = spawnSync(process.execPath, [probe, "--require", "aider", "--json"], { env: item.env, encoding: "utf8" });
    assert.equal(result.status, 0, result.stderr);
    const captured = JSON.parse(readFileSync(capture, "utf8"));
    for (const key of ["OPENAI_API_KEY", "CAVE_API_KEY", "AWS_SECRET_ACCESS_KEY"]) {
      assert.equal(key in captured, false, `${key} reached probed binary`);
    }
    assert.equal(typeof captured.PATH, "string");
    assert.match(captured.HOME, /cave-probe-aider-/);
  } finally {
    rmSync(item.dir, { recursive: true, force: true });
  }
});

test("agent binary probe requires both tested version and runnable help surface", () => {
  const ok = fixture();
  const broken = fixture(9);
  try {
    const pass = spawnSync(process.execPath, [probe, "--require", "aider", "--json"], { env: ok.env, encoding: "utf8" });
    assert.equal(pass.status, 0, pass.stderr);
    const result = JSON.parse(pass.stdout).results.find((entry) => entry.id === "aider");
    assert.equal(result.status, "ok");
    assert.equal(result.version_matches, true);

    const fail = spawnSync(process.execPath, [probe, "--require", "aider", "--json"], { env: broken.env, encoding: "utf8" });
    assert.equal(fail.status, 1, "a binary wrapper whose native help command fails must block release");
    assert.equal(JSON.parse(fail.stdout).results.find((entry) => entry.id === "aider").help_ok, false);
  } finally {
    rmSync(ok.dir, { recursive: true, force: true });
    rmSync(broken.dir, { recursive: true, force: true });
  }
});

test("agent binary probe isolates an explicit required profile from unrelated global drift", () => {
  const item = fixture();
  const unrelated = join(item.dir, "claude");
  writeFileSync(unrelated, `#!/bin/sh\nif [ "$1" = "--version" ]; then echo '0.0.1'; exit 0; fi\nif [ "$1" = "--help" ]; then exit 9; fi\n`, { mode: 0o755 });
  try {
    const result = spawnSync(process.execPath, [probe, "--require", "aider", "--json"], { env: item.env, encoding: "utf8" });
    assert.equal(result.status, 0, result.stderr);
    const parsed = JSON.parse(result.stdout).results;
    assert.equal(parsed.find((entry) => entry.id === "aider").status, "ok");
    assert.equal(parsed.find((entry) => entry.id === "claude").status, "broken", "drift remains visible without breaking another matrix cell");
  } finally {
    rmSync(item.dir, { recursive: true, force: true });
  }
});
