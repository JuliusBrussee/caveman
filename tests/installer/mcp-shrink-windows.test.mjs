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

test("pnpm cross-drive shims with a drive-absolute target parse", () => {
  // pnpm emits an absolute target when the global bin dir and the store sit on
  // different drives (path.relative crosses drives as absolute). This parser
  // used to return null for that form and the caller threw
  // "non-Node Windows command shim" on a perfectly ordinary pnpm install.
  assert.equal(
    options.parseWindowsNodeShim(
      '@SETLOCAL\r\n@IF EXIST "%~dp0\\node.exe" (\r\n  "%~dp0\\node.exe"   "D:\\pnpm-store\\pkg\\cli.js" %*\r\n) ELSE (\r\n  node   "D:\\pnpm-store\\pkg\\cli.js" %*\r\n)\r\n',
    ),
    "D:\\pnpm-store\\pkg\\cli.js",
  );
  // The %~dp0-relative form still wins when a line could match both.
  assert.equal(
    options.parseWindowsNodeShim('@SETLOCAL\r\n@"C:\\Program Files\\nodejs\\node.exe"  "%~dp0\\..\\pkg\\cli.js" %*\r\n'),
    "..\\pkg\\cli.js",
  );
});
