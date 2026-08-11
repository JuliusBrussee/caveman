import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const options = require(join(dirname(fileURLToPath(import.meta.url)), "..", "..", "src", "mcp-servers", "caveman-shrink", "spawn-options.js"));

test("MCP shrink unwraps Windows upstream shims without shell interpolation", () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-shrink-win-"));
  const bin = join(root, "bin");
  const pkg = join(root, "pkg");
  mkdirSync(bin);
  mkdirSync(pkg);
  const shim = join(bin, "npx.CMD");
  const script = join(pkg, "cli.js");
  writeFileSync(shim, '@echo off\r\nendLocal & "%_prog%" "%dp0%\\..\\pkg\\cli.js" %*\r\n');
  writeFileSync(script, "");
  const invocation = options.getSpawnInvocation("npx", ["x&y", "%PATH%"], "win32", {
    Path: bin,
    PATHEXT: ".EXE;.CMD",
  });
  assert.equal(invocation.command, process.execPath);
  assert.deepEqual(invocation.args, [script, "x&y", "%PATH%"]);
  assert.deepEqual(options.getSpawnOptions("win32"), {
    stdio: ["pipe", "pipe", "inherit"],
    windowsHide: true,
  });
});
