import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import test from "node:test";

import { delegateSpawnOptions, killProcessTree } from "./portable-process.mjs";

test("delegate workers use isolated POSIX process groups", () => {
  assert.deepEqual(delegateSpawnOptions("linux"), { detached: true, windowsHide: true });
  assert.deepEqual(delegateSpawnOptions("win32"), { detached: false, windowsHide: true });
});

test("POSIX cleanup sends TERM then KILL to worker process group", async () => {
  const calls = [];
  await killProcessTree(
    { pid: 73 },
    "linux",
    spawnSync,
    (...args) => calls.push(args),
    1,
  );
  assert.deepEqual(calls, [[-73, "SIGTERM"], [-73, "SIGKILL"]]);
});

test("POSIX cleanup kills descendants that ignore TERM", { skip: process.platform === "win32" }, async () => {
  const script = [
    'const { spawn } = require("node:child_process");',
    'const child = spawn(process.execPath, ["-e", "process.on(\\"SIGTERM\\",()=>{});setInterval(()=>{},1000)"], { stdio: "ignore" });',
    'process.stdout.write(String(child.pid) + "\\n");',
    'setInterval(()=>{},1000);',
  ].join("");
  const leader = spawn(process.execPath, ["-e", script], {
    detached: true,
    stdio: ["ignore", "pipe", "ignore"],
  });
  const descendant = await new Promise((resolve, reject) => {
    let output = "";
    const timer = setTimeout(() => reject(new Error("descendant pid timeout")), 2000);
    leader.stdout.on("data", (chunk) => {
      output += chunk;
      const match = output.match(/^(\d+)\n/);
      if (!match) return;
      clearTimeout(timer);
      resolve(Number(match[1]));
    });
    leader.once("error", reject);
  });
  try {
    await killProcessTree(leader, process.platform, spawnSync, process.kill, 100);
    await new Promise((done) => setTimeout(done, 50));
    assert.throws(() => process.kill(descendant, 0), (error) => error?.code === "ESRCH");
  } finally {
    try { process.kill(-leader.pid, "SIGKILL"); } catch {}
  }
});
