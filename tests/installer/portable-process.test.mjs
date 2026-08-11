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
