import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  forceKillTree,
  parseWindowsNodeShim,
  harnessSpawnOptions,
  portableProcessInvocation,
  resolveWindowsCommand,
  stopTree,
} from "../lib/process-tree.mjs";

test("Windows harnesses use cmd shims without detached POSIX groups", () => {
  assert.deepEqual(harnessSpawnOptions("win32"), {
    detached: false,
    windowsHide: true,
  });
  assert.deepEqual(harnessSpawnOptions("darwin"), {
    detached: true,
    windowsHide: true,
  });
});

test("Windows PATH shims resolve through PATHEXT and unwrap without a shell", () => {
  const root = mkdtempSync(join(tmpdir(), "subagent-tax-win-"));
  const bin = join(root, "bin");
  const pkg = join(root, "pkg");
  mkdirSync(bin);
  mkdirSync(pkg);
  const shim = join(bin, "claude.CMD");
  const script = join(pkg, "cli.js");
  writeFileSync(shim, '@echo off\r\nendLocal & "%_prog%" "%dp0%\\..\\pkg\\cli.js" %*\r\n');
  writeFileSync(script, "");
  const env = { Path: bin, PATHEXT: ".EXE;.CMD" };
  assert.equal(resolveWindowsCommand("claude", env), shim);
  assert.deepEqual(portableProcessInvocation("claude", ["x&y"], {
    platform: "win32",
    env,
    execPath: "node.exe",
  }), { command: "node.exe", args: [script, "x&y"] });
});

test("Windows cleanup kills complete descendant tree", () => {
  const calls = [];
  const child = { pid: 412, kill: () => calls.push(["fallback"]) };
  forceKillTree(child, {
    platform: "win32",
    taskkill: (command, args, options) => {
      calls.push([command, args, options]);
      return { status: 0 };
    },
  });
  assert.deepEqual(calls, [[
    "taskkill.exe",
    ["/pid", "412", "/t", "/f"],
    { stdio: "ignore", windowsHide: true },
  ]]);
});

test("Windows cleanup falls back when taskkill exits non-zero", () => {
  const calls = [];
  forceKillTree({ pid: 413, kill: () => calls.push("fallback") }, {
    platform: "win32",
    taskkill: () => ({ status: 1 }),
  });
  assert.deepEqual(calls, ["fallback"]);
});

test("POSIX cleanup signals negative process-group id", () => {
  const calls = [];
  forceKillTree({ pid: 73 }, {
    platform: "darwin",
    kill: (...args) => calls.push(args),
  });
  assert.deepEqual(calls, [[-73, "SIGKILL"]]);
});

test("POSIX graceful stop returns when child exits", async () => {
  const child = Object.assign(new EventEmitter(), { pid: 91 });
  const calls = [];
  const pending = stopTree(child, {
    platform: "linux",
    graceMs: 10_000,
    kill: (...args) => calls.push(args),
  });
  child.emit("exit", 0);
  await pending;
  assert.deepEqual(calls, [[-91, "SIGTERM"]]);
});

test("pnpm cross-drive shims with a drive-absolute target parse", () => {
  // pnpm emits an absolute target when the global bin dir and the store sit on
  // different drives (path.relative crosses drives as absolute). This parser
  // used to return null for that form and the caller threw
  // "non-Node Windows command shim" on a perfectly ordinary pnpm install.
  assert.equal(
    parseWindowsNodeShim(
      '@SETLOCAL\r\n@IF EXIST "%~dp0\\node.exe" (\r\n  "%~dp0\\node.exe"   "D:\\pnpm-store\\pkg\\cli.js" %*\r\n) ELSE (\r\n  node   "D:\\pnpm-store\\pkg\\cli.js" %*\r\n)\r\n',
    ),
    "D:\\pnpm-store\\pkg\\cli.js",
  );
  // The %~dp0-relative form still wins when a line could match both.
  assert.equal(
    parseWindowsNodeShim('@SETLOCAL\r\n@"C:\\Program Files\\nodejs\\node.exe"  "%~dp0\\..\\pkg\\cli.js" %*\r\n'),
    "..\\pkg\\cli.js",
  );
});
