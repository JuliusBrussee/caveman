import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { killProcessTree, portableInvocation } from "../../../agents/delegate/portable-process.mjs";

test("delegate unwraps Windows pi shim and preserves task bytes", () => {
  const root = mkdtempSync(join(tmpdir(), "cave-delegate-win-"));
  const bin = join(root, "bin");
  const pkg = join(root, "pkg");
  mkdirSync(bin);
  mkdirSync(pkg);
  const shim = join(bin, "pi.CMD");
  const script = join(pkg, "cli.js");
  writeFileSync(shim, '@echo off\r\nendLocal & "%_prog%" "%dp0%\\..\\pkg\\cli.js" %*\r\n');
  writeFileSync(script, "");
  assert.deepEqual(portableInvocation("pi", ["x&y", "%PATH%"], "win32", {
    Path: bin,
    PATHEXT: ".EXE;.CMD",
  }), { command: process.execPath, args: [script, "x&y", "%PATH%"] });
});

test("delegate kills Windows worker descendants", () => {
  const calls = [];
  killProcessTree({ pid: 75, kill: () => calls.push("fallback") }, "win32", (command, args, options) => {
    calls.push([command, args, options]);
    return { status: 0 };
  });
  assert.deepEqual(calls, [[
    "taskkill.exe",
    ["/pid", "75", "/t", "/f"],
    { stdio: "ignore", windowsHide: true },
  ]]);
});
