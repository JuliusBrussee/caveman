import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { parseWindowsNodeShim, portableInvocation } from "../dist/portable-command.js";

test("parses managed Pi's Node command shim", () => {
  assert.equal(parseWindowsNodeShim('@ECHO off\r\nnode "%~dp0pi-launcher.js" %*\r\n'), "pi-launcher.js");
});

test("parses npm and pnpm Node command shims", () => {
  assert.equal(
    parseWindowsNodeShim('endLocal & "%_prog%" "%dp0%\\..\\pkg\\cli.js" %*'),
    "..\\pkg\\cli.js",
  );
  assert.equal(
    parseWindowsNodeShim('node "%~dp0\\..\\pkg\\cli.mjs" %*'),
    "..\\pkg\\cli.mjs",
  );
  // Exact npm cmd-shim@7 payload line (PATHEXT strip + two quoted segments).
  assert.equal(
    parseWindowsNodeShim(
      'endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & set PATHEXT=%PATHEXT:;.JS;=;% & "%_prog%"  "%dp0%\\..\\pkg\\cli.js" %*\r\n',
    ),
    "..\\pkg\\cli.js",
  );
  // pnpm / yarn-classic (@zkochan/cmd-shim) IF EXIST form — the node.exe quoted
  // prefix must not shadow the .js target.
  assert.equal(
    parseWindowsNodeShim(
      '@SETLOCAL\r\n@IF EXIST "%~dp0\\node.exe" (\r\n  "%~dp0\\node.exe"   "%~dp0\\..\\pkg\\cli.js" %*\r\n) ELSE (\r\n  @SET PATHEXT=%PATHEXT:;.JS;=;%\r\n  node   "%~dp0\\..\\pkg\\cli.js" %*\r\n)\r\n',
    ),
    "..\\pkg\\cli.js",
  );
  // pnpm cross-drive: bin dir and store on different drives makes the target
  // absolute instead of %~dp0-relative.
  assert.equal(
    parseWindowsNodeShim(
      '@SETLOCAL\r\n@IF EXIST "%~dp0\\node.exe" (\r\n  "%~dp0\\node.exe"   "D:\\pnpm-store\\pkg\\cli.js" %*\r\n) ELSE (\r\n  @SET PATHEXT=%PATHEXT:;.JS;=;%\r\n  node   "D:\\pnpm-store\\pkg\\cli.js" %*\r\n)\r\n',
    ),
    "D:\\pnpm-store\\pkg\\cli.js",
  );
  // pnpm pinned-node variant: absolute node interpreter, %~dp0-relative target.
  assert.equal(
    parseWindowsNodeShim('@SETLOCAL\r\n@"C:\\Program Files\\nodejs\\node.exe"  "%~dp0\\..\\pkg\\cli.js" %*\r\n'),
    "..\\pkg\\cli.js",
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

test("Pi-style nested Windows shim launches JS without a shell", () => {
  const root = mkdtempSync(join(tmpdir(), "cave nested shim "));
  try {
    const child = join(root, "node_modules", ".bin", "pi.cmd");
    const script = join(root, "pi package", "cli.mjs");
    mkdirSync(join(root, "node_modules", ".bin"), { recursive: true });
    mkdirSync(join(root, "pi package"), { recursive: true });
    writeFileSync(script, "// fixture\n");
    writeFileSync(child, 'node "%~dp0\\..\\..\\pi package\\cli.mjs" %*\r\n');
    const shim = join(root, "pi.CMD");
    writeFileSync(shim, '@ECHO off\r\n"%~dp0node_modules\\.bin\\pi.cmd" %*\r\n');
    const args = ["space & %PATH%", 'quote"kept'];
    assert.deepEqual(portableInvocation(shim, args, "win32"), {
      command: process.execPath, args: [script, ...args],
    });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("nested shims reject unsafe wrappers, missing targets, cycles and excessive depth", () => {
  const root = mkdtempSync(join(tmpdir(), "cave nested reject "));
  try {
    const shim = join(root, "pi.cmd");
    const child = join(root, "child.bat");
    writeFileSync(join(root, "cli.js"), "// fixture\n");
    const args = ["x&y"];
    const invoke = () => portableInvocation(shim, args, "win32");
    for (const content of [
      '"%~dp0child.bat" %*\r\necho unsafe\r\n',
      '"%~dp0child.bat" %* & echo unsafe\r\n',
      '"%~dp0child.exe" %*\r\n',
      '"%~dp0child.ps1" %*\r\n',
      '"%~dp0child.bat" %*\r\nnode "%~dp0missing.js" %*\r\n',
      '"%~dp0child.bat" %*\r\nnode "%~dp0cli.js" %*\r\n',
    ]) {
      writeFileSync(shim, content);
      assert.throws(invoke, /cannot safely launch non-Node Windows command shim/);
    }
    writeFileSync(shim, '"%~dp0child.bat" %*\r\n');
    assert.throws(invoke, /Windows command shim target is missing/);
    writeFileSync(child, '"%~dp0pi.cmd" %*\r\n');
    assert.throws(invoke, /Windows command shim cycle/);
    for (let i = 0; i <= 5; i++) {
      writeFileSync(join(root, `shim${i}.cmd`), `"%~dp0shim${i + 1}.cmd" %*\r\n`);
    }
    assert.throws(() => portableInvocation(join(root, "shim0.cmd"), args, "win32"), /nesting too deep/);
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
