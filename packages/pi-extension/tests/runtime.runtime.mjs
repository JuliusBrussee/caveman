import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
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
    schema: "caveman.proxy.run.v1", pid: process.pid, port: 8787, instance_token: "test", owner: "wrap",
    recovery_via_mcp: true, provider_upstreams: { openai: "https://api.openai.com" },
  }));
  t.mock.method(globalThis, "fetch", async () => new Response("{}", { headers: { "x-caveman-instance": "test" } }));
  const events = [];
  const calls = [];
  t.mock.method(HookBridge.prototype, "call", async (event, payload) => {
    events.push([event, payload.session_id]);
    calls.push({ event, payload });
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
  const original = { provider: "openai", id: "test", api: "openai-responses", baseUrl: "https://api.openai.com/v1" };
  let selected = original;
  const host = {
    async setModel(model) { selected = model; return true; },
  };
  // `extra.agent` is OMP's ctx.agent (absent on older OMP and on Pi); `getSessionFile`
  // and `getHeader` are the session manager's accessors; `model` overrides the shared
  // selection; `notify` receives ctx.ui notices.
  const context = (id, extra = {}) => ({
    get model() { return extra.model ?? selected; }, hasUI: true, ui: { notify: extra.notify ?? (() => {}) },
    sessionManager: {
      getSessionId: () => id,
      ...(extra.getSessionFile && { getSessionFile: extra.getSessionFile }),
      ...(extra.getHeader && { getHeader: extra.getHeader }),
    },
    ...(extra.agent && { agent: extra.agent }),
    modelRegistry: { isUsingOAuth: () => false, getApiKeyAndHeaders: async () => ({ ok: true, headers: {} }) },
  });
  const runtime = createCavemanRuntime(host);
  t.after(async () => {
    await runtime.shutdown();
    for (const [key, value] of Object.entries(saved)) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
    rmSync(root, { recursive: true, force: true });
  });
  return { runtime, context, events, calls, root, selected: () => selected };
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

test("compression delegates eligibility to the native runtime for every non-recovery tool, uses PostToolUseFailure on error, and preserves images", async () => {
  let calls = 0;
  const seenEvents = [];
  const bridge = { async call(event, payload) { calls++; seenEvents.push(event); return { output_replacement: `summary <<ccr:ccr_obj_handle>> [${payload.tool_name}]` }; } };
  const image = { type: "image", data: "aGVsbG8=", mimeType: "image/png" };
  const result = (toolName, isError = false) => shrinkToolResult(bridge, { session_id: "session" }, {
    toolName, isError, input: {}, content: [{ type: "text", text: "original" }, image],
  }, { verify: async () => true });
  for (const tool of ["read", "bash", "write", "edit", "grep", "read_file", "custom"]) {
    assert.deepEqual((await result(tool)).content, [{ type: "text", text: `summary <<ccr:ccr_obj_handle>> [${tool}]` }, image]);
    assert.deepEqual((await result(tool, true)).content, [{ type: "text", text: `summary <<ccr:ccr_obj_handle>> [${tool}]` }, image]);
  }
  assert.equal(await result("caveman_retrieve"), undefined, "caveman_retrieve's own output must never be re-shrunk (recovery loop hazard)");
  assert.equal(calls, 14, "every non-recovery tool result must reach the native runtime's own classifyTool decision");
  assert.ok(seenEvents.includes("PostToolUse") && seenEvents.includes("PostToolUseFailure"), "isError must select PostToolUseFailure, not PostToolUse");
});

// OMP's ctx.agent for a subagent. OMP names a top-level session file
// `<timestamp>_<session id>.jsonl` and writes every session it spawns to
// `<that file minus .jsonl>/<agentId>.jsonl`; a session file's first line is its header.
const SUBAGENT = { kind: "sub", id: "0-Explore", name: "explore", depth: 1 };
const stem = (id) => `2026-10-11T12-00-00-000Z_${id}`;
const writeSession = (path, header = { type: "session" }) => {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, `${JSON.stringify(header)}\n{"type":"message"}\n`);
  return path;
};
// The top-level session file and a spawned session's file under it.
const layout = (h, id = "root-id", agent = "0-Explore") => ({
  main: join(h.root, "sessions", `${stem(id)}.jsonl`),
  child: join(h.root, "sessions", stem(id), `${agent}.jsonl`),
});
const subagentContext = (h, id, file, extra = {}) => h.context(id, { agent: SUBAGENT, getSessionFile: () => file, ...extra });

test("a subagent runs under the main session's id with agent fields, never opens or ends a session, and still routes through the live proxy", async (t) => {
  // The CLI prints nothing for SubagentStart; an empty response is not "runtime unreachable".
  const h = harness(t, async (event) => event === "SubagentStart" ? undefined : {});
  const { main, child } = layout(h);
  writeSession(main, { type: "session", id: "root-id" });
  const ctx = subagentContext(h, "child-id", child);
  await h.runtime.start(ctx);
  assert.equal(h.selected().baseUrl, "http://127.0.0.1:8787/w/pi/openai/v1", "a live run-state must open the gate for a subagent");
  assert.equal(await h.runtime.beforeAgentStart("hello", ctx), undefined, "Core is not injected into a subagent");
  await h.runtime.turnStart();
  await h.runtime.toolCall({ toolName: "read", input: {} });
  await h.runtime.toolResult({ toolName: "read", input: {}, content: [{ type: "text", text: "out" }], isError: false });
  await h.runtime.turnEnd();
  await h.runtime.shutdown();
  assert.deepEqual(h.calls.map(({ event }) => event), ["SubagentStart", "ModelBefore", "PreToolUse", "PostToolUse", "ModelAfter", "SubagentStop"]);
  for (const { event, payload } of h.calls) {
    assert.deepEqual(
      { session_id: payload.session_id, agent_id: payload.agent_id, agent_type: payload.agent_type },
      { session_id: "root-id", agent_id: "0-Explore", agent_type: "explore" },
      event,
    );
  }
});

test("a subagent receives only the context SubagentStart returns, plus tool nudges", async (t) => {
  const h = harness(t, async (event) => ({
    SubagentStart: { hookSpecificOutput: { hookEventName: "SubagentStart", additionalContext: "SUBAGENT_CONTEXT" } },
    SessionStart: { context: "CORE" }, UserPromptSubmit: { context: "PROMPT" }, PreToolUse: { context: "NUDGE" },
  })[event] ?? {});
  const ctx = subagentContext(h, "child-id");
  await h.runtime.start(ctx);
  assert.deepEqual(await h.runtime.beforeAgentStart("hello", ctx), ["SUBAGENT_CONTEXT"]);
  await h.runtime.toolCall({ toolName: "read", input: {} });
  assert.deepEqual(await h.runtime.beforeAgentStart("again", ctx), ["SUBAGENT_CONTEXT", "NUDGE"]);
});

test("a subagent sends neither PreCompact nor PostCompact, and its compaction never installs Core", async (t) => {
  // The runtime keys compaction state by session id; under the main session's id a
  // subagent's compaction would demote the main session's evidence.
  const h = harness(t, async (event) => event === "PostCompact" ? { context: "RECOMPACTED" } : {});
  const ctx = subagentContext(h, "child-id");
  await h.runtime.start(ctx);
  await h.runtime.beforeCompact();
  await h.runtime.compact();
  assert.equal(await h.runtime.beforeAgentStart("hello", ctx), undefined);
  assert.deepEqual(h.calls.map(({ event }) => event), ["SubagentStart"]);
  // A main session still reports compaction and takes the context PostCompact returns.
  await h.runtime.shutdown();
  h.calls.length = 0;
  const main = h.context("main-id");
  await h.runtime.start(main);
  await h.runtime.beforeCompact();
  await h.runtime.compact();
  assert.deepEqual(h.calls.map(({ event }) => event), ["SessionStart", "PreCompact", "PostCompact"]);
  assert.deepEqual(await h.runtime.beforeAgentStart("hello", main), ["RECOMPACTED"]);
});

test("a main session, with or without ctx.agent, keeps the full session lifecycle and sends no agent fields", async (t) => {
  const h = harness(t);
  // A forked main session names its source in parentSession; that is not a subagent parent.
  const { main } = layout(h, "main-id");
  writeSession(main, { type: "session", id: "main-id" });
  const getHeader = () => ({ parentSession: "source-id" });
  for (const agent of [undefined, { kind: "main", id: "0-Main", name: "main", depth: 0 }]) {
    h.calls.length = 0;
    const ctx = h.context("main-id", { agent, getHeader, getSessionFile: () => main });
    await h.runtime.start(ctx);
    assert.deepEqual(await h.runtime.beforeAgentStart("hello", ctx), ["CORE_main-id"]);
    await h.runtime.turnEnd();
    await h.runtime.shutdown();
    assert.deepEqual(h.calls.map(({ event }) => event), ["SessionStart", "UserPromptSubmit", "ModelAfter", "Stop", "SessionEnd"]);
    for (const { payload } of h.calls) {
      assert.equal(payload.session_id, "main-id");
      assert.equal("agent_id" in payload || "agent_type" in payload, false);
    }
  }
});

test("navigating away from a subagent ends it with SubagentStop, not SessionEnd", async (t) => {
  const h = harness(t);
  await h.runtime.start(subagentContext(h, "child-id"));
  await h.runtime.start(h.context("main-id"));
  assert.deepEqual(h.calls.map(({ event }) => event), ["SubagentStart", "SubagentStop", "SessionStart"]);
});

test("a /tan clone reports under its main session although its parentSession is a session id, not a path", async (t) => {
  const h = harness(t);
  const { main, child: clone } = layout(h, "root-id", "Tan-1");
  writeSession(main, { type: "session", id: "root-id" });
  writeSession(clone, { type: "session", id: "tan-id", parentSession: "root-id" });
  await h.runtime.start(h.context("tan-id", {
    agent: { kind: "sub", id: "Tan-1", name: "sub", depth: 0 },
    getSessionFile: () => clone,
    getHeader: () => ({ parentSession: "root-id" }),
  }));
  assert.equal(h.calls[0].event, "SubagentStart");
  assert.deepEqual(
    { session_id: h.calls[0].payload.session_id, agent_id: h.calls[0].payload.agent_id, agent_type: h.calls[0].payload.agent_type },
    { session_id: "root-id", agent_id: "Tan-1", agent_type: "sub" },
  );
});

test("the main session's id comes from its header, else from its file name, however deep the subagent and whatever is on disk", async (t) => {
  const h = harness(t);
  // Each case builds its own sessions directory and returns [subagent file, expected session_id].
  const cases = {
    "the root header's id wins over its file name": (dir) => {
      writeSession(join(dir, `${stem("file-id")}.jsonl`), { type: "session", id: "header-id" });
      return [join(dir, stem("file-id"), "0-Explore.jsonl"), "header-id"];
    },
    "two levels deep, never the intermediate subagent's id": (dir) => {
      writeSession(join(dir, `${stem("file-id")}.jsonl`), { type: "session", id: "header-id" });
      writeSession(join(dir, stem("file-id"), "0-Plan.jsonl"), { type: "session", id: "planner-id" });
      return [join(dir, stem("file-id"), "0-Plan", "1-Explore.jsonl"), "header-id"];
    },
    "two levels deep with the intermediate and root files not yet on disk": (dir) =>
      [join(dir, stem("file-id"), "0-Plan", "1-Explore.jsonl"), "file-id"],
    "root file missing": (dir) => [join(dir, stem("file-id"), "0-Explore.jsonl"), "file-id"],
    "root file is not JSON": (dir) => {
      mkdirSync(dir, { recursive: true });
      writeFileSync(join(dir, `${stem("file-id")}.jsonl`), "not json\n");
      return [join(dir, stem("file-id"), "0-Explore.jsonl"), "file-id"];
    },
    "root header is not a session header": (dir) => {
      writeSession(join(dir, `${stem("file-id")}.jsonl`), { type: "message", id: "header-id" });
      return [join(dir, stem("file-id"), "0-Explore.jsonl"), "file-id"];
    },
    "root header has no id": (dir) => {
      writeSession(join(dir, `${stem("file-id")}.jsonl`), { type: "session" });
      return [join(dir, stem("file-id"), "0-Explore.jsonl"), "file-id"];
    },
    "root header line just under the 64 KiB read cap": (dir) => {
      writeSession(join(dir, `${stem("file-id")}.jsonl`), { type: "session", id: "header-id", padding: "x".repeat(60 * 1024) });
      return [join(dir, stem("file-id"), "0-Explore.jsonl"), "header-id"];
    },
    "root header line over the 64 KiB read cap": (dir) => {
      writeSession(join(dir, `${stem("file-id")}.jsonl`), { type: "session", id: "header-id", padding: "x".repeat(70 * 1024) });
      return [join(dir, stem("file-id"), "0-Explore.jsonl"), "file-id"];
    },
    "root path is a directory, not a file": (dir) => {
      mkdirSync(join(dir, `${stem("file-id")}.jsonl`), { recursive: true });
      return [join(dir, stem("file-id"), "0-Explore.jsonl"), "file-id"];
    },
  };
  let n = 0;
  for (const [name, build] of Object.entries(cases)) {
    const [file, expected] = build(join(h.root, "sessions", String(n++)));
    await h.runtime.shutdown();
    h.calls.length = 0;
    await h.runtime.start(h.context("own-id", { agent: SUBAGENT, getSessionFile: () => file }));
    assert.equal(h.calls[0].event, "SubagentStart", name);
    assert.equal(h.calls[0].payload.session_id, expected, name);
  }
});

test("a subagent whose main session cannot be found by file layout keeps its own session id", async (t) => {
  const h = harness(t);
  const getSessionFiles = {
    "no getSessionFile": undefined,
    "in-memory session": () => undefined,
    "getSessionFile throws": () => { throw new Error("session closed"); },
    "a path outside OMP's layout": () => join(h.root, "elsewhere", "0-Explore.jsonl"),
    "a relative path outside OMP's layout": () => "0-Explore.jsonl",
  };
  for (const [name, getSessionFile] of Object.entries(getSessionFiles)) {
    await h.runtime.shutdown();
    h.calls.length = 0;
    await h.runtime.start(h.context("child-id", { agent: SUBAGENT, getSessionFile }));
    assert.equal(h.calls[0].event, "SubagentStart", name);
    assert.equal(h.calls[0].payload.session_id, "child-id", name);
  }
});

test("a subagent prints no direct-mode notice and does not wait for a proxy that nothing is starting", async (t) => {
  const h = harness(t);
  rmSync(join(h.root, "run", "8787.json"));
  // Count the run-state retry sleeps, but do not wait them out.
  const realSetTimeout = globalThis.setTimeout;
  let retrySleeps = 0;
  t.mock.method(globalThis, "setTimeout", (fn, ms, ...args) => {
    if (ms === 300) { retrySleeps++; ms = 0; }
    return realSetTimeout(fn, ms, ...args);
  });
  const notices = [];
  const notify = (message) => notices.push(message);
  // Control: a main session waits for the proxy its SessionStart may have just started, then says so.
  await h.runtime.start(h.context("main-id", { notify }));
  assert.deepEqual(notices.map((message) => message.match(/\(([^)]*)\)/)[1]), ["local proxy not running"]);
  assert.equal(retrySleeps, 3);
  notices.length = 0;
  retrySleeps = 0;
  await h.runtime.start(subagentContext(h, "child-id", undefined, { notify }));
  assert.deepEqual(notices, []);
  assert.equal(retrySleeps, 0, "SubagentStart starts no proxy, so a missing run-state is final");
});

test("a subagent prints no pass-through notice for a model the proxy cannot route", async (t) => {
  const h = harness(t);
  const foreign = { provider: "unlisted", id: "foreign", api: "openai-responses", baseUrl: "http://127.0.0.1:1" };
  const notices = [];
  const notify = (message) => notices.push(message);
  // Control: the main session announces the pass-through once.
  await h.runtime.start(h.context("main-id", { notify, model: foreign }));
  assert.equal(notices.length, 1);
  assert.match(notices[0], /pass-through for unlisted\/foreign/);
  notices.length = 0;
  await h.runtime.start(subagentContext(h, "child-id", undefined, { notify, model: foreign }));
  assert.deepEqual(notices, []);
});
