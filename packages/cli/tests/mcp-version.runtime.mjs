import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");
const cliVersion = JSON.parse(readFileSync(join(dirname(fileURLToPath(import.meta.url)), "..", "package.json"), "utf8")).version;

function fixture(mcpBody) {
  const home = mkdtempSync(join(tmpdir(), "cave-mcp-version-home-"));
  const bin = join(home, "bin");
  const caveHome = join(home, ".caveman");
  mkdirSync(bin, { recursive: true });
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({
    wrap: { proxy: false, shrink: false, browse: false },
  }));
  writeFileSync(join(bin, "codex"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  writeFileSync(join(bin, "cavemem"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const mcp = join(bin, "caveman-mcp");
  writeFileSync(mcp, `#!/usr/bin/env node\n${mcpBody}\n`, { mode: 0o755 });
  return {
    home,
    caveHome,
    mcp,
    env: {
      ...process.env,
      HOME: home,
      CAVEMAN_HOME: caveHome,
      CAVEMAN_MCP_BIN: mcp,
      CAVEMEM_BIN: join(bin, "cavemem"),
      CAVE_GATEWAY_URL: "http://127.0.0.1:9",
      NO_COLOR: "1",
      CI: "1",
      PATH: `${bin}:${process.env.PATH}`,
    },
  };
}

function runWrap(env, timeoutMs = 8_000) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, "wrap", "codex"], {
      env,
      stdio: ["pipe", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error("wrap did not return after MCP version-probe timeout"));
    }, timeoutMs);
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("error", reject);
    child.on("exit", (code) => {
      clearTimeout(timer);
      resolve({ code, stdout, stderr });
    });
    child.stdin.end();
  });
}

test("wrap probes current MCP once and uses it without persistent install", async () => {
  const count = join(tmpdir(), `cave-mcp-probe-count-${process.pid}-${Date.now()}`);
  const f = fixture(`
import { existsSync, readFileSync, writeFileSync } from "node:fs";
if (process.argv[2] === "version" && process.argv[3] === "--json") {
  const path = process.env.CAVE_TEST_MCP_PROBE_COUNT;
  const before = existsSync(path) ? Number(readFileSync(path, "utf8")) : 0;
  writeFileSync(path, String(before + 1));
  process.stdout.write(JSON.stringify({
    version: "1.0.0-test",
    schema: "caveman.mcp.version.v1",
    capabilities: ["mcp_recovery", "build_stamped_version"],
  }) + "\\n");
  process.exit(0);
}
process.exit(2);
`);
  const out = await runWrap({ ...f.env, CAVE_TEST_MCP_PROBE_COUNT: count });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(readFileSync(count, "utf8"), "1", "install and recovery checks must share cached probe");
  assert.doesNotMatch(out.stderr, /installed streaming-compression recovery/);
  assert.equal(existsSync(join(f.caveHome, "mcp", "codex.json")), false, "wrap must not write persistent marker");
});

test("hung pre-versioned MCP is bounded, explicit, and never installed", async () => {
  const f = fixture("setInterval(() => {}, 1000);");
  const started = Date.now();
  const out = await runWrap(f.env);
  const elapsed = Date.now() - started;
  assert.equal(out.code, 0, out.stderr);
  assert.ok(elapsed >= 1_800 && elapsed < 5_000, `probe elapsed ${elapsed}ms; want hard 2s bound`);
  assert.match(out.stderr, new RegExp(`caveman-mcp pre-versioned is older than ${cliVersion.replace(/\./g, "\\.")} — update before compressing`));
  assert.equal(existsSync(join(f.caveHome, "mcp", "codex.json")), false, "stale MCP must not be installed");
});
