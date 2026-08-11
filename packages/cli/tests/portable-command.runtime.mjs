import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { parseWindowsNodeShim, portableInvocation } from "../dist/portable-command.js";

test("parses npm and pnpm Node command shims", () => {
  assert.equal(
    parseWindowsNodeShim('endLocal & "%_prog%" "%dp0%\\..\\pkg\\cli.js" %*'),
    "..\\pkg\\cli.js",
  );
  assert.equal(
    parseWindowsNodeShim('node "%~dp0\\..\\pkg\\cli.mjs" %*'),
    "..\\pkg\\cli.mjs",
  );
});

test("Windows Node shim launches target with Node and preserves argument bytes", () => {
  const root = mkdtempSync(join(tmpdir(), "cave-win-shim-"));
  try {
    const target = join(root, "pkg", "cli.js");
    mkdirSync(join(root, "pkg"), { recursive: true });
    writeFileSync(target, "process.exit(0);\n");
    const shim = join(root, "agent.cmd");
    writeFileSync(shim, 'endLocal & "%_prog%" "%dp0%\\pkg\\cli.js" %*\r\n');
    const args = ["--prompt", "100% & literal", 'quote"kept'];
    assert.deepEqual(portableInvocation(shim, args, "win32"), {
      command: process.execPath,
      args: [target, ...args],
    });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("non-Node Windows shims fail closed instead of using injectable shell mode", () => {
  const root = mkdtempSync(join(tmpdir(), "cave-win-shim-"));
  try {
    const shim = join(root, "agent.cmd");
    writeFileSync(shim, "@echo off\r\necho %*\r\n");
    assert.throws(
      () => portableInvocation(shim, ["unsafe&arg"], "win32"),
      /cannot safely launch non-Node Windows command shim/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("native executables and POSIX commands pass through", () => {
  assert.deepEqual(portableInvocation("agent.exe", ["x"], "win32"), {
    command: "agent.exe",
    args: ["x"],
  });
  assert.deepEqual(portableInvocation("agent", ["x"], "darwin"), {
    command: "agent",
    args: ["x"],
  });
});
