import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function runSetup(env) {
  return new Promise((resolve, reject) => {
    // node is invoked by absolute path so an empty PATH can't break the spawn.
    const child = spawn(process.execPath, [cli, "setup", "--json"], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

// baseEnv makes binary resolution deterministic: empty PATH, an empty
// CAVEMAN_HOME, and no CAVEMAN_*_BIN overrides leaking in from the host.
function baseEnv(home) {
  const env = { ...process.env, NO_COLOR: "1", PATH: "", CAVEMAN_HOME: home };
  for (const k of ["CAVEMAN_PROXY_BIN", "CAVEMAN_ENGINE_BIN", "CAVEMAN_MCP_BIN", "CAVEMEM_BIN", "CAVEMAN_BROWSE_BIN", "CAVEMAN_SHRINK_BIN"]) delete env[k];
  return env;
}

// `caveman setup` is the first run now; `setup --json` keeps the binary report
// for scripts. A fresh npm install has none of the Go binaries: name every one,
// say what each powers and what degrades without it, and exit non-zero so
// scripts can gate on it. (no-fake-savings)
test("setup --json reports every missing binary and exits non-zero", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-setup-empty-"));
  const out = await runSetup(baseEnv(home));
  assert.notEqual(out.code, 0, "missing required binaries must exit non-zero");
  const report = JSON.parse(out.stdout);
  assert.equal(report.ready, false);
  for (const name of ["caveman-proxy", "caveman-engine", "caveman-mcp", "cavemem", "caveman-browse", "caveman-shrink"]) {
    const row = report.binaries.find((b) => b.name === name);
    assert.ok(row, `must name ${name}`);
    assert.equal(row.path, null);
    assert.ok(row.without, `${name} must say what degrades without it`);
  }
  assert.match(report.binaries.find((b) => b.name === "caveman-browse").powers, /agent-side compressed browsing MCP tools/);
});

// With binaries present in ~/.caveman/bin (where the install script builds
// them), setup must show where each resolved from and exit 0.
test("setup finds binaries in ~/.caveman/bin and exits 0", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-setup-full-"));
  const binDir = join(home, "bin");
  mkdirSync(binDir, { recursive: true });
  for (const name of ["caveman-proxy", "caveman-engine", "caveman-mcp", "cavemem", "caveman-browse", "caveman-shrink"]) {
    writeFileSync(join(binDir, name), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  }
  const out = await runSetup(baseEnv(home));
  assert.equal(out.code, 0, `all binaries present must exit 0: ${out.stdout}${out.stderr}`);
  const report = JSON.parse(out.stdout);
  assert.equal(report.ready, true);
  assert.equal(report.binaries.find((b) => b.name === "caveman-proxy").path, join(binDir, "caveman-proxy"), "must report the resolved path");
});

// Optional browse and tool-catalog binaries must not fail gate: required-only present → 0.
test("setup treats caveman-browse and caveman-shrink as optional", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-setup-req-"));
  const binDir = join(home, "bin");
  mkdirSync(binDir, { recursive: true });
  for (const name of ["caveman-proxy", "caveman-engine", "caveman-mcp", "cavemem"]) {
    writeFileSync(join(binDir, name), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  }
  const out = await runSetup(baseEnv(home));
  assert.equal(out.code, 0, `browse alone missing must still exit 0: ${out.stdout}`);
  const report = JSON.parse(out.stdout);
  for (const name of ["caveman-browse", "caveman-shrink"]) {
    const row = report.binaries.find((b) => b.name === name);
    assert.deepEqual([row.required, row.path], [false, null], `${name} must be optional and missing`);
  }
});
