import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:net";
import { EventEmitter } from "node:events";
import { runInNewContext } from "node:vm";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

// Execute the built launcher's real registration/dispatch code with an injected
// platform. This checks our routing policy, not native Windows console delivery.
for (const isTTY of [true, false]) test(`Windows start console dispatch does not force-kill its shared child (TTY=${isTTY})`, () => {
  const source = readFileSync(cli, "utf8");
  const begin = source.indexOf("const child = spawn(resolved, [],");
  const end = source.indexOf("// This handler hard-exits", begin);
  assert.ok(begin >= 0 && end > begin, "built start signal block must exist");
  const proc = Object.assign(new EventEmitter(), { platform: "win32", stdin: { isTTY } });
  const kills = [];
  let spawnOptions;
  const { removeSignalHandlers } = runInNewContext(`${source.slice(begin, end)}\n({ removeSignalHandlers });`, {
    process: proc, resolved: "caveman-proxy.exe", env: {},
    spawn: (_file, _args, options) => { spawnOptions = options; return { kill: (signal) => kills.push(signal) }; },
  });
  assert.equal(spawnOptions.detached, false, "the proxy must retain the Windows console");
  for (const signal of ["SIGINT", "SIGBREAK", "SIGHUP"]) {
    assert.equal(proc.listenerCount(signal), 1, `${signal} must keep the launcher alive while the child drains`);
    proc.emit(signal);
    assert.deepEqual(kills, [], `${signal} must not be forwarded as forced termination`);
  }
  assert.deepEqual(kills, [], "console broadcasts already reach the child; kill would terminate it abruptly");
  removeSignalHandlers();
  assert.deepEqual(proc.eventNames(), [], "cleanup removes all owned listeners");
});

for (const isTTY of [true, false]) for (const [signal, code] of [["SIGINT", 130], ["SIGBREAK", 149], ["SIGHUP", 129]]) {
  test(`Windows native shortcut waits for shared-console ${signal} before exiting (TTY=${isTTY})`, () => {
    const source = readFileSync(cli, "utf8");
    const begin = source.indexOf("let fatal;", source.indexOf("async function agentShortcut("));
    const end = source.lastIndexOf("});", source.indexOf("stopProxyKeepalive();", begin));
    const signalExitCode = source.match(/function signalExitCode\(signal\) \{[\s\S]*?\n\}/)?.[0];
    assert.ok(begin >= 0 && end > begin && signalExitCode, "built shortcut signal block must exist");
    const kills = [];
    const proc = Object.assign(new EventEmitter(), { platform: "win32", stdin: { isTTY }, kill: (...args) => kills.push(args) });
    const child = Object.assign(new EventEmitter(), { kill: (...args) => kills.push(args) });
    let exitCode;
    runInNewContext(`${signalExitCode}\n${source.slice(begin, end)}`, {
      process: proc, child, bin: "agent.exe", resolve: (code) => { exitCode = code; },
      reject: (error) => { throw error; },
      setTimeout: () => { assert.fail("console drain must not gain a force-kill timer"); }, clearTimeout() {},
    });
    assert.equal(proc.listenerCount(signal), 1);
    proc.emit(signal);
    assert.equal(exitCode, undefined, "the launcher waits until its child exits");
    assert.deepEqual(kills, []);
    child.emit("exit", 0, null);
    assert.equal(exitCode, code, "Windows reports the console stop without trying an unsupported process.kill");
    assert.deepEqual(kills, []);
    assert.deepEqual(proc.eventNames(), []);
  });
}

async function waitForFile(path, message) {
  const deadline = Date.now() + 2_000;
  while (!existsSync(path) && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 10));
  assert.ok(existsSync(path), message);
}

function wrapperSignalPolicy(platform, isTTY = false) {
  const source = readFileSync(cli, "utf8");
  const begin = source.indexOf("const signalHandlers =", source.indexOf("async function spawnWrapped("));
  const end = source.lastIndexOf("});", source.indexOf("stopProxyKeepalive();", begin));
  const signalExitCode = source.match(/function signalExitCode\(signal\) \{[\s\S]*?\n\}/)?.[0];
  assert.ok(begin >= 0 && end > begin && signalExitCode, "built wrapper signal block must exist");
  const calls = { childKills: [], parentKills: [], parentExits: [], cleaned: 0, markersRemoved: 0 };
  const proc = Object.assign(new EventEmitter(), {
    platform, stdin: { isTTY }, pid: 123,
    kill: (...args) => calls.parentKills.push(args), exit: (code) => calls.parentExits.push(code),
  });
  const child = Object.assign(new EventEmitter(), { kill: (signal) => calls.childKills.push(signal) });
  const timers = new Map();
  runInNewContext(`${signalExitCode}\n${source.slice(begin, end)}`, {
    process: proc, child, bin: "agent", sessionMarker: "fixture-marker", resolve() {},
    reject: (error) => { throw error; },
    cleanupWrapTempDirs: () => { calls.cleaned++; },
    removeProxySessionMarker: () => { calls.markersRemoved++; },
    setTimeout: (callback, delay) => { const timer = { unref() {} }; timers.set(timer, { callback, delay }); return timer; },
    clearTimeout: (timer) => timers.delete(timer),
  });
  return { proc, child, calls, timers };
}

for (const isTTY of [true, false]) for (const [signal, code] of [["SIGINT", 130], ["SIGBREAK", 149], ["SIGHUP", 129]]) {
  test(`Windows wrapper drains shared-console ${signal} before temporary-pack cleanup (TTY=${isTTY})`, () => {
    const fx = wrapperSignalPolicy("win32", isTTY);
    assert.equal(fx.proc.listenerCount(signal), 1);
    fx.proc.emit(signal);
    assert.deepEqual(fx.calls.childKills, [], "console broadcast already reached the child; forwarding would force-kill it");
    assert.equal(fx.calls.cleaned, 0, "SessionEnd hooks still need the temporary pack while draining");
    assert.equal(fx.calls.markersRemoved, 0);
    assert.deepEqual(fx.calls.parentExits, []);
    assert.equal(fx.timers.size, 1);
    assert.equal([...fx.timers.values()][0].delay, 10_000, "retain the existing bounded shutdown escape");
    fx.child.emit("exit", 0, null);
    assert.equal(fx.calls.cleaned, 1);
    assert.equal(fx.calls.markersRemoved, 1);
    assert.deepEqual(fx.calls.parentExits, [code]);
    assert.deepEqual(fx.calls.parentKills, [], "Windows cannot re-raise console signals with process.kill");
    assert.equal(fx.timers.size, 0);
    assert.deepEqual(fx.proc.eventNames(), []);
  });
}

for (const platform of ["win32", "linux"]) for (const escape of ["second signal", "timeout"]) {
  test(`${platform} wrapper preserves forced-stop cleanup on ${escape}`, () => {
    const fx = wrapperSignalPolicy(platform);
    const signal = platform === "win32" ? "SIGINT" : "SIGTERM";
    fx.proc.emit(signal);
    assert.deepEqual(fx.calls.childKills, platform === "win32" ? [] : [signal]);
    assert.equal(fx.calls.cleaned, 0);
    if (escape === "second signal") fx.proc.emit(signal);
    else [...fx.timers.values()][0].callback();
    assert.equal(fx.calls.childKills.at(-1), "SIGKILL");
    assert.equal(fx.calls.cleaned, 1);
    assert.equal(fx.calls.markersRemoved, 1);
    assert.deepEqual(fx.calls.parentExits, platform === "win32" ? [130] : []);
    assert.deepEqual(fx.calls.parentKills, platform === "win32" ? [] : [[123, "SIGTERM"]]);
    assert.deepEqual(fx.proc.eventNames(), []);
  });
}

async function startFixture(t, body) {
  const home = mkdtempSync(join(tmpdir(), "caveman-start-lifecycle-"));
  const proxy = join(home, "proxy.mjs");
  const pidFile = join(home, "proxy.pid");
  const stoppedFile = join(home, "stopping");
  const releaseFile = join(home, "release");
  writeFileSync(proxy, `#!/usr/bin/env node
import { existsSync, writeFileSync } from "node:fs";
const stoppedFile = ${JSON.stringify(stoppedFile)}, releaseFile = ${JSON.stringify(releaseFile)};
${body}
writeFileSync(${JSON.stringify(pidFile)}, String(process.pid));
`, { mode: 0o755 });
  const server = createServer();
  await new Promise((resolve, reject) => { server.once("error", reject); server.listen(0, "127.0.0.1", resolve); });
  const port = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  const child = spawn(process.execPath, [cli, "start", "--port", String(port)], {
    env: { ...process.env, HOME: home, CAVEMAN_HOME: home, CAVEMAN_PROXY_BIN: proxy, CAVEMAN_TELEMETRY: "0", CAVE_NO_KEYCHAIN: "1" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.stdout.resume();
  child.stderr.resume();
  const exited = new Promise((resolve, reject) => {
    child.once("exit", (code, signal) => resolve({ code, signal }));
    child.once("error", reject);
  });
  t.after(async () => {
    child.kill("SIGKILL");
    if (existsSync(pidFile)) { try { process.kill(Number(readFileSync(pidFile, "utf8")), "SIGKILL"); } catch {} }
    await exited;
    rmSync(home, { recursive: true, force: true });
  });
  await waitForFile(pidFile, "proxy fixture did not start");
  return { child, stoppedFile, releaseFile, exited };
}

for (const signal of ["SIGTERM", "SIGINT"]) test(`start forwards ${signal} and waits for the proxy to drain`, { skip: process.platform === "win32", timeout: 10_000 }, async (t) => {
  const fx = await startFixture(t, `
for (const signal of ["SIGTERM", "SIGINT"]) process.on(signal, () => writeFileSync(stoppedFile, signal));
setInterval(() => { if (existsSync(releaseFile)) process.exit(0); }, 10);
`);
  fx.child.kill(signal);
  await waitForFile(fx.stoppedFile, `${signal} did not reach the owned proxy`);
  assert.equal(readFileSync(fx.stoppedFile, "utf8"), signal);
  assert.equal(fx.child.exitCode, null, "launcher must wait for graceful proxy shutdown");
  assert.equal(fx.child.signalCode, null, "launcher must not abandon the draining proxy");
  writeFileSync(fx.releaseFile, "done");
  assert.deepEqual(await fx.exited, { code: 0, signal: null });
});

test("start reports a signal-killed proxy as failure", { skip: process.platform === "win32", timeout: 10_000 }, async (t) => {
  const fx = await startFixture(t, `setTimeout(() => process.kill(process.pid, "SIGTERM"), 100);`);
  assert.deepEqual(await fx.exited, { code: 143, signal: null });
});

for (const delivery of ["pid", "group"]) test(`start forwards one SIGINT from ${delivery} delivery while stdin is a TTY`, { skip: process.platform === "win32", timeout: 20_000 }, () => {
  const probe = join(dirname(cli), "..", "..", "..", ".blocks", "cli-signal-check.py");
  const output = execFileSync("python3", [probe, "--cli", cli, "--node", process.execPath, "--delivery", delivery], { encoding: "utf8", timeout: 15_000 });
  assert.deepEqual(JSON.parse(output), { delivery, signals: 1, drained: true, proxy_exited: true });
});

for (const signal of ["SIGINT", "SIGQUIT"]) for (const [delivery, input] of [["group", "tty"], ["pid", "pipe"]]) {
  test(`wrap delivers one ${signal} for ${delivery} stop with ${input} input`, { skip: process.platform === "win32", timeout: 20_000 }, () => {
    const probe = join(dirname(cli), "..", "..", "..", ".blocks", "cli-signal-check.py");
    const output = execFileSync("python3", [probe, "--cli", cli, "--node", process.execPath,
      "--launch", "wrap", "--delivery", delivery, "--input", input, "--signal", signal],
    { encoding: "utf8", timeout: 15_000 });
    assert.deepEqual(JSON.parse(output), { delivery, signals: 1, drained: true, child_exited: true });
  });
}
