import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const portable = require(join(dirname(fileURLToPath(import.meta.url)), "..", "..", "bin", "lib", "portable-process.js"));

test("root installer unwraps Windows Node shims without a shell", () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-installer-win-"));
  const bin = join(root, "bin");
  const pkg = join(root, "pkg");
  mkdirSync(bin);
  mkdirSync(pkg);
  const shim = join(bin, "npx.CMD");
  const script = join(pkg, "cli.js");
  writeFileSync(shim, '@echo off\r\nendLocal & "%_prog%" "%dp0%\\..\\pkg\\cli.js" %*\r\n');
  writeFileSync(script, "");
  const env = { Path: bin, PATHEXT: ".EXE;.CMD" };
  assert.equal(portable.resolveWindowsCommand("npx", env), shim);
  assert.deepEqual(portable.portableInvocation("npx", ["space value", "x&y", "%PATH%"], {
    platform: "win32",
    env,
    execPath: "node.exe",
  }), {
    command: "node.exe",
    args: [script, "space value", "x&y", "%PATH%"],
  });
});

test("root installer rejects non-Node command shims", () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-installer-win-"));
  const shim = join(root, "unsafe.cmd");
  writeFileSync(shim, "@echo off\r\necho %*\r\n");
  assert.throws(
    () => portable.portableInvocation(shim, ["x&y"], { platform: "win32" }),
    /cannot safely launch non-Node Windows command shim/,
  );
});

test("root installer parses pnpm cross-drive shims whose target is drive-absolute", () => {
  // pnpm emits an absolute target when the global bin dir and the store sit on
  // different drives (path.relative crosses drives as absolute). Only the CLI's
  // copy of this parser handled that form; here it returned null and the throw
  // took out every `npx skills add` provider install with no diagnostic.
  assert.equal(
    portable.parseWindowsNodeShim(
      '@SETLOCAL\r\n@IF EXIST "%~dp0\\node.exe" (\r\n  "%~dp0\\node.exe"   "D:\\pnpm-store\\pkg\\cli.js" %*\r\n) ELSE (\r\n  node   "D:\\pnpm-store\\pkg\\cli.js" %*\r\n)\r\n',
    ),
    "D:\\pnpm-store\\pkg\\cli.js",
  );
  // %~dp0-relative form still wins when both could match.
  assert.equal(
    portable.parseWindowsNodeShim('@SETLOCAL\r\n@"C:\\Program Files\\nodejs\\node.exe"  "%~dp0\\..\\pkg\\cli.js" %*\r\n'),
    "..\\pkg\\cli.js",
  );
});
