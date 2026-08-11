import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  forceKillTree,
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
