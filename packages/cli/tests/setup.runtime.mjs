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
    const child = spawn(process.execPath, [cli, "setup"], { env });
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

// A fresh npm install has none of the Go binaries. `caveman setup` must make
// that impossible to miss: name every missing binary, say what degrades (loud
// byte-safe pass-through, savings honestly 0), show the one install command,
// and exit non-zero so scripts can gate on it. (no-fake-savings)
test("setup reports every missing binary, the install command, and exits non-zero", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-setup-empty-"));
  const out = await runSetup(baseEnv(home));
  assert.notEqual(out.code, 0, "missing required binaries must exit non-zero");
  for (const name of ["caveman-proxy", "caveman-engine", "caveman-mcp", "cavemem", "caveman-browse", "caveman-shrink"]) {
    assert.match(out.stdout, new RegExp(name), `must name ${name}`);
  }
  assert.match(out.stdout, /missing/i);
  assert.match(out.stdout, /pass-through/i, "must say affected commands degrade to pass-through");
  assert.match(out.stdout, /agent-side compressed browsing MCP tools/, "browse line must say what installing it unlocks");
  assert.match(out.stdout, /wrap auto-registers once installed/, "browse line must say wrap auto-registers it once present");
  assert.match(out.stdout, /caveman setup --install/, "must show the signed install command");
  assert.match(out.stdout, /login/, "must say connected verbs still work");
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
  assert.match(out.stdout, /All required binaries found/);
  assert.match(out.stdout, new RegExp(binDir.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")), "must print the resolved path");
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
  assert.match(out.stdout, /caveman-browse\s+missing\s+\(optional\)/, "browse must be marked optional");
  assert.match(out.stdout, /caveman-shrink\s+missing\s+\(optional\)/, "shrink must be marked optional");
});
