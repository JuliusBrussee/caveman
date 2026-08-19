// The Windows hook-invocation contract, asserted from any platform.
//
// Every pi-extension hook call went through execFile("caveman", …). On Windows
// that name resolves to a `.cmd` shim — or worse, to the NON-EXECUTABLE Unix
// shim npm/pnpm parks beside it under the bare name — and execFile can run
// neither, dying with `spawn EFTYPE`. engine-ci's windows job caught it only
// after the package shipped; these cases catch it on a laptop.
//
// portableInvocation takes an explicit platform/env, so the win32 behaviour is
// exercised on macOS and Linux too.

import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const { portableInvocation, parseWindowsNodeShim } = await import("../dist/portable-command.mjs")
  .catch(() => import("../src/portable-command.ts"));

function fixture() {
  const root = mkdtempSync(join(tmpdir(), "pi-portable-"));
  const bin = join(root, "bin");
  mkdirSync(bin, { recursive: true });
  return { root, bin };
}

test("non-win32 is an untouched pass-through", () => {
  const r = portableInvocation("caveman", ["native-hook", "pi", "SessionStart"], "darwin", {});
  assert.deepEqual(r, { command: "caveman", args: ["native-hook", "pi", "SessionStart"] });
});

test("win32: a bare name resolves through PATHEXT, not to the Unix shim beside it", () => {
  const { root, bin } = fixture();
  try {
    // Exactly the npm/pnpm .bin layout: an extensionless Unix shim next to the
    // real .CMD. Picking the bare name is what produced spawn EFTYPE.
    writeFileSync(join(bin, "caveman"), "#!/bin/sh\nexec node caveman.js\n");
    // Verbatim npm cmd-shim shape — the `node.exe` token and the `%~dp0`
    // relative target are both load-bearing for the shim parser.
    writeFileSync(
      join(bin, "caveman.cmd"),
      '@IF EXIST "%~dp0\\node.exe" (\r\n'
      + '  "%~dp0\\node.exe"  "%~dp0\\cli\\dist\\index.js" %*\r\n'
      + ') ELSE (\r\n'
      + '  node  "%~dp0\\cli\\dist\\index.js" %*\r\n'
      + ')\r\n',
    );
    mkdirSync(join(bin, "cli", "dist"), { recursive: true });
    writeFileSync(join(bin, "cli", "dist", "index.js"), "//\n");

    const r = portableInvocation("caveman", ["native-hook", "pi", "SessionStart"], "win32", {
      PATH: bin,
      PATHEXT: ".COM;.EXE;.BAT;.CMD",
    });
    assert.equal(r.command, process.execPath, "a .cmd Node shim must launch through node, not execFile");
    assert.equal(r.args[0], join(bin, "cli", "dist", "index.js"));
    assert.deepEqual(r.args.slice(1), ["native-hook", "pi", "SessionStart"]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("win32: a real .exe is invoked directly", () => {
  const { root, bin } = fixture();
  try {
    writeFileSync(join(bin, "caveman.exe"), "MZ");
    const r = portableInvocation("caveman", ["native-hook", "pi", "Stop"], "win32", {
      PATH: bin,
      PATHEXT: ".COM;.EXE;.BAT;.CMD",
    });
    // PATHEXT entries are uppercase, so the probe finds "caveman.EXE".
    assert.equal(r.command.toLowerCase(), join(bin, "caveman.exe").toLowerCase());
    assert.deepEqual(r.args, ["native-hook", "pi", "Stop"]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("win32: a shim whose target cannot be parsed throws rather than spawning garbage", () => {
  const { root, bin } = fixture();
  try {
    writeFileSync(join(bin, "caveman.cmd"), "@echo off\r\necho not a node shim\r\n");
    assert.throws(
      () => portableInvocation("caveman", [], "win32", { PATH: bin, PATHEXT: ".CMD" }),
      /non-Node Windows command shim/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("parseWindowsNodeShim reads both the shim-relative and drive-absolute forms", () => {
  assert.equal(
    parseWindowsNodeShim('  "%~dp0\\node.exe"  "%~dp0\\..\\caveman\\dist\\index.js" %*'),
    "..\\caveman\\dist\\index.js",
  );
  // pnpm emits a drive-absolute target when the global bin dir and the store
  // sit on different drives.
  assert.equal(
    parseWindowsNodeShim('@node  "C:\\store\\caveman\\dist\\index.js" %*'),
    "C:\\store\\caveman\\dist\\index.js",
  );
  assert.equal(parseWindowsNodeShim("@echo off\r\necho hi\r\n"), null);
});
