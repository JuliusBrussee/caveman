import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

import {
  hostShellInvocation,
  killProcessTree,
  portableInvocation,
  resolveWindowsCommand,
} from "../dist/portable-process.js";
import { proxyBinaryCandidates } from "../dist/runtime.js";

test("Windows command shims launch their Node target without shell interpolation", () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-agent-win-"));
  const bin = join(root, "bin");
  const pkg = join(root, "pkg");
  mkdirSync(bin);
  mkdirSync(pkg);
  const shim = join(bin, "caveman.CMD");
  const script = join(pkg, "cli.js");
  writeFileSync(shim, '@echo off\r\nendLocal & "%_prog%" "%dp0%\\..\\pkg\\cli.js" %*\r\n');
  writeFileSync(script, "");
  const env = { Path: bin, PATHEXT: ".EXE;.CMD" };
  assert.equal(resolveWindowsCommand("caveman", env), shim);
  assert.deepEqual(portableInvocation("caveman", ["x&y"], {
    platform: "win32",
    env,
    execPath: "node.exe",
  }), { command: "node.exe", args: [script, "x&y"] });
});

test("host shell selection is native on Windows and POSIX", () => {
  assert.deepEqual(hostShellInvocation("echo hi", "win32", { ComSpec: "C:\\Windows\\System32\\cmd.exe" }), {
    command: "C:\\Windows\\System32\\cmd.exe",
    args: ["/d", "/s", "/c", "echo hi"],
  });
  assert.deepEqual(hostShellInvocation("echo hi", "darwin", {}), {
    command: "/bin/sh",
    args: ["-c", "echo hi"],
  });
});

test("Windows process-tree cleanup uses taskkill and falls back on failure", () => {
  const calls = [];
  const child = { pid: 91, kill: () => calls.push("fallback") };
  killProcessTree(child, "SIGKILL", {
    platform: "win32",
    taskkill: (command, args, options) => {
      calls.push([command, args, options]);
      return { status: 0 };
    },
  });
  assert.deepEqual(calls, [[
    "taskkill.exe",
    ["/pid", "91", "/t", "/f"],
    { stdio: "ignore", windowsHide: true },
  ]]);
  killProcessTree(child, "SIGKILL", {
    platform: "win32",
    taskkill: () => ({ status: 1 }),
  });
  assert.equal(calls.at(-1), "fallback");
});

test("runtime probes use .exe companions from Windows Caveman home", () => {
  const home = resolve(tmpdir(), "caveman-agent-windows-home");
  assert.deepEqual(
    proxyBinaryCandidates("win32", home),
    [resolve(home, "bin", "caveman-proxy.exe"), "caveman-proxy.exe"],
  );
});
