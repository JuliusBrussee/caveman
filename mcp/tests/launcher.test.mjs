import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

const packageRoot = resolve(import.meta.dirname, "..");
const launcher = join(packageRoot, "bin", "caveman-mcp.mjs");

test("launcher skips its npm .bin shim and uses cached verified binary", { skip: process.platform === "win32" }, async () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-mcp-self-"));
  const npmBin = join(root, "node_modules", ".bin");
  const home = join(root, "home");
  mkdirSync(npmBin, { recursive: true });
  mkdirSync(join(home, "bin"), { recursive: true });
  symlinkSync(launcher, join(npmBin, "caveman-mcp"));
  writeFileSync(join(home, "bin", "caveman-mcp"), "#!/bin/sh\nprintf 'cached'\n", { mode: 0o755 });
  const env = { ...process.env, PATH: npmBin, CAVEMAN_HOME: home };
  delete env.CAVEMAN_MCP_BIN;
  const out = await runNode([launcher], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, "cached");
});

function runNode(args, env) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(process.execPath, args, { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("close", (code) => resolvePromise({ code, stdout, stderr }));
    child.on("error", reject);
  });
}
