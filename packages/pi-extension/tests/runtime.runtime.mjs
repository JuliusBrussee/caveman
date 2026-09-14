import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { createCavemanRuntime, HookBridge, RecoveryClient, shrinkToolResult } from "../dist/testable.mjs";

function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}

function harness(t, hook = async (event, payload) => event === "SessionStart" ? { context: `CORE_${payload.session_id}` } : {}) {
  const root = mkdtempSync(join(tmpdir(), "caveman-session-"));
  const env = { CAVEMAN_HOME: root, CAVE_GATEWAY_URL: "http://127.0.0.1:8787", CAVEMAN_MCP_BIN: join(root, "unused") };
  const saved = Object.fromEntries(Object.keys(env).map((key) => [key, process.env[key]]));
  Object.assign(process.env, env);
  mkdirSync(join(root, "run"));
  writeFileSync(join(root, "run", "8787.json"), JSON.stringify({
    schema: "caveman.proxy.run.v1", pid: process.pid, instance_token: "test", owner: "wrap", recovery_via_mcp: true,
  }));
  t.mock.method(globalThis, "fetch", async () => new Response("{}"));
  const events = [];
  t.mock.method(HookBridge.prototype, "call", async (event, payload) => {
    events.push([event, payload.session_id]);
    return hook(event, payload);
  });
  const disposed = new WeakSet();
  const clients = new WeakMap();
  let nextClient = 0;
  t.mock.method(RecoveryClient.prototype, "ensure", async function () {
    if (disposed.has(this)) return false;
    if (!clients.has(this)) clients.set(this, ++nextClient);
    return true;
  });
  t.mock.method(RecoveryClient.prototype, "dispose", function () { disposed.add(this); });
  t.mock.method(RecoveryClient.prototype, "retrieve", async function () {
    const ready = await this.ensure();
    return { text: ready ? `recovered by client ${clients.get(this)}` : "disposed", isError: !ready };
  });
  const original = { provider: "openai", id: "test", api: "openai-completions", baseUrl: "https://api.openai.com/v1" };
  let selected = original;
  let override;
  const host = {
    registerProvider(_name, config) { override = config.baseUrl; },
    unregisterProvider() { override = undefined; },
    async setModel(model) { selected = model; return true; },
  };
  const model = () => ({ ...original, baseUrl: override ?? original.baseUrl });
  const context = (id) => ({
    get model() { return selected; }, hasUI: true, ui: { notify() {} },
    sessionManager: { getSessionId: () => id },
    modelRegistry: { getAll: () => [model()], find: () => model(), isUsingOAuth: () => false },
  });
  const runtime = createCavemanRuntime(host);
  t.after(async () => {
    await runtime.shutdown();
    for (const [key, value] of Object.entries(saved)) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
    rmSync(root, { recursive: true, force: true });
  });
  return { runtime, context, events, selected: () => selected };
}

test("navigation supersedes an in-flight startup before it can route or install Core", async (t) => {
  const entered = deferred();
  const release = deferred();
  const h = harness(t, async (event, payload) => {
    if (event !== "SessionStart") return {};
    if (payload.session_id === "old") { entered.resolve(); await release.promise; }
    return { context: `CORE_${payload.session_id}` };
  });
  const old = h.runtime.start(h.context("old"));
  await entered.promise;
  const next = h.runtime.start(h.context("new"));
  release.resolve();
  await Promise.all([old, next]);
  assert.deepEqual(await h.runtime.beforeAgentStart("hello", h.context("new")), ["CORE_new"]);
  assert.equal(h.selected().baseUrl, "http://127.0.0.1:8787/w/pi/openai/v1");
  assert.deepEqual(h.events.filter(([event]) => event.startsWith("Session")), [
    ["SessionStart", "old"], ["SessionEnd", "old"], ["SessionStart", "new"],
  ]);
});

test("navigation renews recovery and discards late prompt, tool and compaction results", async (t) => {
  const entered = deferred();
  const release = deferred();
  let waiting = 0;
  let delay = false;
  const h = harness(t, async (event, payload) => {
    if (delay && payload.session_id === "old" && ["UserPromptSubmit", "PostToolUse", "PostCompact"].includes(event)) {
      if (++waiting === 3) entered.resolve();
      await release.promise;
      return { context: "STALE_CONTEXT", output_replacement: "STALE_RESULT" };
    }
    if (event === "PreToolUse") return { context: "OLD_PENDING" };
    return event === "SessionStart" ? { context: `CORE_${payload.session_id}` } : {};
  });
  await h.runtime.start(h.context("old"));
  const first = await h.runtime.retrieve("r1", { recovery_handle: "handle" });
  await h.runtime.toolCall({ toolName: "read", input: {} });
  delay = true;
  const prompt = h.runtime.beforeAgentStart("hello", h.context("old"));
  const tool = h.runtime.toolResult({ toolName: "read", input: {}, content: [{ type: "text", text: "old" }], isError: false });
  const compact = h.runtime.compact();
  await entered.promise;
  await h.runtime.start(h.context("new"));
  release.resolve();
  assert.equal(await prompt, undefined);
  assert.equal(await tool, undefined);
  await compact;
  const second = await h.runtime.retrieve("r2", { recovery_handle: "handle" });
  assert.notEqual(second.content[0].text, first.content[0].text, "a fresh session must not reuse its predecessor's recovery client");
  assert.deepEqual(await h.runtime.beforeAgentStart("hello", h.context("new")), ["CORE_new"]);
  await h.runtime.shutdown();
  assert.equal(h.selected().baseUrl, "https://api.openai.com/v1", "selected model must stop pointing at the gateway");
  await assert.rejects(h.runtime.retrieve("r3", { recovery_handle: "handle" }), /not active/);
});

test("shutdown invalidates startup and permits a later fresh start", async (t) => {
  const entered = deferred();
  const release = deferred();
  const h = harness(t, async (event, payload) => {
    if (event === "SessionStart" && payload.session_id === "old") { entered.resolve(); await release.promise; }
    return event === "SessionStart" ? { context: `CORE_${payload.session_id}` } : {};
  });
  const startup = h.runtime.start(h.context("old"));
  await entered.promise;
  const shutdown = h.runtime.shutdown();
  release.resolve();
  await Promise.all([startup, shutdown]);
  assert.equal(h.selected().baseUrl, "https://api.openai.com/v1");
  assert.equal(await h.runtime.beforeAgentStart("hello", h.context("old")), undefined);
  await h.runtime.start(h.context("new"));
  assert.deepEqual(await h.runtime.beforeAgentStart("hello", h.context("new")), ["CORE_new"]);
});

test("a recovery result from the old session cannot escape after navigation", async (t) => {
  const h = harness(t);
  await h.runtime.start(h.context("old"));
  const entered = deferred();
  const release = deferred();
  t.mock.method(RecoveryClient.prototype, "retrieve", async () => {
    entered.resolve();
    await release.promise;
    return { text: "old session bytes", isError: false };
  });
  const retrieval = h.runtime.retrieve("r1", { recovery_handle: "handle" });
  await entered.promise;
  await h.runtime.start(h.context("new"));
  release.resolve();
  await assert.rejects(retrieval, /session changed during recovery/);
});

test("compression accepts only successful read/bash output and preserves images", async () => {
  let calls = 0;
  const bridge = { async call() { calls++; return { output_replacement: "summary <<ccr:handle>>" }; } };
  const image = { type: "image", data: "aGVsbG8=", mimeType: "image/png" };
  const result = (toolName, isError = false) => shrinkToolResult(bridge, "session", {
    toolName, isError, input: {}, content: [{ type: "text", text: "original" }, image],
  });
  for (const tool of ["read", "bash"]) {
    assert.deepEqual((await result(tool)).content, [{ type: "text", text: "summary <<ccr:handle>>" }, image]);
    assert.equal(await result(tool, true), undefined);
  }
  for (const tool of ["caveman_retrieve", "write", "edit", "grep", "read_file", "custom"]) assert.equal(await result(tool), undefined);
  assert.equal(calls, 2, "unsupported and failed outputs must never be sent to the compressor");
});
