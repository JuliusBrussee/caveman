import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const portable = require(join(dirname(fileURLToPath(import.meta.url)), "..", "..", "bin", "lib", "portable-process.js"));

test("absolute Windows commands use PATHEXT and skip Unix shims and directories", (t) => {
  const root = mkdtempSync(join(tmpdir(), "caveman explicit win "));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const command = join(root, "npx");
  writeFileSync(command, "#!/bin/sh\n");
  mkdirSync(`${command}.EXE`);
  writeFileSync(`${command}.CMD`, 'node "%~dp0\\cli.js" %*\r\n');
  const script = join(root, "cli.js");
  writeFileSync(script, "");
  const env = { PATHEXT: ".EXE;.CMD" };
  assert.equal(portable.resolveWindowsCommand(command, env), `${command}.CMD`);
  assert.deepEqual(portable.portableInvocation(command, ["C:\\path with spaces\\", "%PATH%"], {
    platform: "win32", env, execPath: "node.exe",
  }), { command: "node.exe", args: [script, "C:\\path with spaces\\", "%PATH%"] });
  assert.equal(portable.resolveWindowsCommand("npx", { ...env, Path: `"${root}"` }), `${command}.CMD`);
  assert.equal(portable.resolveWindowsCommand(join(root, "missing"), env), null);
});

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

test("OMP npm shims use Bun without interpreting argument bytes in a shell", (t) => {
  const root = mkdtempSync(join(tmpdir(), "caveman-omp-bun-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const script = join(root, "cli.js");
  writeFileSync(script, "#!/usr/bin/env bun\nBun.version;\n");
  writeFileSync(join(root, "omp.CMD"), '@echo off\r\nendLocal & "%_prog%" "%dp0%\\cli.js" %*\r\n');
  const env = { Path: root, PATHEXT: ".EXE;.CMD" };
  const args = ["plugin", "install", "space & %PATH% ! value"];
  const options = { platform: "win32", env, execPath: "node.exe", allowBun: true };
  assert.throws(() => portable.portableInvocation("omp", args, options), /requires bun.exe/);
  writeFileSync(join(root, "bun.CMD"), "@echo off\r\necho unsafe %*\r\n");
  assert.throws(() => portable.portableInvocation("omp", args, options), /requires bun.exe/);
  const bun = join(root, "bun.EXE");
  writeFileSync(bun, "fixture");
  assert.deepEqual(portable.portableInvocation("omp", args, options), { command: bun, args: [script, ...args] });
  // Other callers retain their Node-only launch contract.
  assert.equal(portable.portableInvocation("omp", args, { ...options, allowBun: false }).command, "node.exe");
});
