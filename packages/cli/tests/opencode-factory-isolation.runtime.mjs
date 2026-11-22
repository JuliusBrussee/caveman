import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

// Evaluate the actual generator, not a copied adapter. Only its invocation
// resolver and generated child-process import are replaced; no CLI install,
// dependencies, user configuration, subprocesses or network are needed.
async function fixture(t, variant = "V1") {
  const root = mkdtempSync(join(tmpdir(), "cave-opencode-factories-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const source = readFileSync(new URL(process.env.CAVE_OPENCODE_GENERATOR ?? "../src/index.ts", import.meta.url), "utf8");
  const start = source.indexOf("function opencodeNativePluginSourceBoth()");
  const end = source.indexOf("\nfunction opencodeNativeRoutes(", start);
  assert.ok(start >= 0 && end > start, "locate the production generators");
  const generate = new Function("cavemanInvocation", source.slice(start, end)
    .replaceAll("(): string {", "() {") + `\nreturn opencodeNativePluginSource${variant}();`);
  const generated = generate(() => ({ cmd: "isolated-recorder", pre: [] }));
  const recorderPath = join(root, "recorder.mjs");
  writeFileSync(recorderPath, `
export const calls = [];
export let invoking;
export let failure;
export function invokeAs(owner, callback) {
  invoking = owner;
  try { return callback(); } finally { invoking = undefined; }
}
export function failWith(value) { failure = value; }
export function execFileSync(command, args, options) {
  const payload = JSON.parse(options.input);
  const event = args[0] === "shrink-hook" ? "shrink-hook" : args[2];
  calls.push({ command, event, invoking, childCwd: options.cwd, payload, options });
  if (failure === "throw") throw new Error("isolated child failure");
  if (failure === "json") return "not json";
  if (event === "shrink-hook") return JSON.stringify({ hookSpecificOutput: { updatedInput: { command: "rewritten command" } } });
  if (event === "PostToolUse") return JSON.stringify({ output_replacement: "recovered output" });
  const context = event + ":" + options.cwd + ":" + payload.session_id;
  return JSON.stringify({ hookSpecificOutput: { additionalContext: context } });
}
`);
  const pluginPath = join(root, "plugin.mjs");
  writeFileSync(pluginPath, generated.replace('from "node:child_process"', `from ${JSON.stringify(pathToFileURL(recorderPath).href)}`));
  const recorder = await import(pathToFileURL(recorderPath).href);
  const plugin = await import(pathToFileURL(pluginPath).href);
  const dirs = Object.fromEntries(["A", "B"].map((name) => [name, join(root, `project-${name}`)]));
  for (const dir of Object.values(dirs)) mkdirSync(dir);
  const invoke = (owner, callback) => recorder.invokeAs(owner, callback);
  const create = (owner) => plugin.CavemanNative({ directory: dirs[owner] });
  const event = (owner, hooks, type, sessionID = `session-${owner}`) =>
    invoke(owner, () => hooks.event({ event: { type, properties: { sessionID } } }));
  const system = async (owner, hooks, sessionID = `session-${owner}`) => {
    const output = { system: [] };
    await invoke(owner, () => hooks["experimental.chat.system.transform"]({ sessionID }, output));
    return output.system;
  };
  return { ...recorder, plugin, dirs, invoke, create, event, system };
}

for (const variant of ["V1", "Both"]) {
// Prevents two factory calls returning the same lifecycle/callback owner.
test(`${variant}: V1 factories own independent hook maps and callbacks`, async (t) => {
  const fx = await fixture(t, variant);
  const a = await fx.create("A");
  const b = await fx.create("B");
  assert.notEqual(a, b, "factories must not share the hook map");
  for (const name of Object.keys(a)) assert.notEqual(a[name], b[name], name);
});

// Prevents the last initialized project replacing an earlier factory's cwd.
for (const order of [["A", "B"], ["B", "A"]]) {
  test(`${variant}: V1 cwd and session isolation: initialize ${order.join(" then ")}`, async (t) => {
    const fx = await fixture(t, variant);
    const hooks = {};
    for (const owner of order) hooks[owner] = await fx.create(owner);
    for (const owner of ["A", "B"]) {
      await fx.event(owner, hooks[owner], "session.created");
      const output = { args: { command: "original command" } };
      await fx.invoke(owner, () => hooks[owner]["tool.execute.before"]({ sessionID: `session-${owner}`, tool: "bash" }, output));
      assert.equal(output.args.command, "rewritten command");
      const calls = fx.calls.filter((call) => call.invoking === owner);
      assert.deepEqual(calls.map((call) => call.event), ["SessionStart", "PreToolUse", "shrink-hook"]);
      for (const call of [calls[1], calls[2], calls[0]]) {
        assert.equal(call.childCwd, fx.dirs[owner], `${owner} ${call.event} child cwd`);
        if (call.event !== "shrink-hook") {
          assert.equal(call.payload.cwd, fx.dirs[owner], `${owner} payload cwd`);
          assert.equal(call.payload.session_id, `session-${owner}`);
        }
      }
    }
  });
}

// Prevents disposal ending foreign sessions, losing their stable context or
// consuming their queued prompt hint. Also proves the surviving hooks still run.
test(`${variant}: V1 disposing A preserves B context, pending hints and usable callbacks`, async (t) => {
  const fx = await fixture(t, variant);
  const a = await fx.create("A");
  const b = await fx.create("B");
  for (const [owner, hooks] of [["A", a], ["B", b]]) {
    await fx.event(owner, hooks, "session.created");
    await fx.invoke(owner, () => hooks["chat.message"]({ sessionID: `session-${owner}` }, { parts: [{ text: "fix bug" }] }));
  }
  await fx.invoke("A", () => a.dispose());
  assert.deepEqual(fx.calls.filter((call) => call.event === "SessionEnd").map((call) => call.payload.session_id), ["session-A"]);
  const before = fx.calls.length;
  assert.deepEqual(await fx.system("B", b), [
    `SessionStart:${fx.dirs.B}:session-B`, `UserPromptSubmit:${fx.dirs.B}:session-B`,
  ]);
  assert.equal(fx.calls.length, before, "B uses its existing cached context");
  assert.deepEqual(await fx.system("B", b), [`SessionStart:${fx.dirs.B}:session-B`], "pending hint consumed once");
  await fx.event("B", b, "session.idle");
  assert.equal(fx.calls.at(-1).payload.cwd, fx.dirs.B);
  assert.equal(fx.calls.at(-1).event, "Stop");
  await fx.invoke("A", () => a.dispose());
  await fx.invoke("B", () => b.dispose());
  await fx.invoke("B", () => b.dispose());
  assert.deepEqual(fx.calls.filter((call) => call.event === "SessionEnd").map((call) => call.payload.session_id), ["session-A", "session-B"]);
});
}

// Prevents context/hint collisions even when independent hosts reuse a session
// ID; pending operations are queued strings, not asynchronous child jobs.
test("V1 reused session IDs keep context and pending hints independent", async (t) => {
  const fx = await fixture(t);
  const a = await fx.create("A");
  const b = await fx.create("B");
  const pending = [];
  for (const [owner, hooks] of [["A", a], ["B", b]]) {
    pending.push(fx.event(owner, hooks, "session.created", "shared"));
    pending.push(fx.invoke(owner, () => hooks["chat.message"]({ sessionID: "shared" }, { parts: [] })));
  }
  // Dispose before awaiting the callbacks' promises: generated execFileSync
  // callbacks complete their state mutations before yielding to the host.
  await fx.invoke("A", () => a.dispose());
  await Promise.all(pending);
  assert.deepEqual(await fx.system("B", b, "shared"), [
    `SessionStart:${fx.dirs.B}:shared`, `UserPromptSubmit:${fx.dirs.B}:shared`,
  ]);
  await fx.invoke("B", () => b.dispose());
  assert.deepEqual(fx.calls.filter((call) => call.event === "SessionEnd").map((call) => [call.invoking, call.childCwd]), [
    ["A", fx.dirs.A], ["B", fx.dirs.B],
  ]);
});

// Prevents single-factory lifecycle or payload changes while relocating state.
test("V1 single factory preserves prompt, tool, compaction and deletion contracts", async (t) => {
  const fx = await fixture(t);
  const a = await fx.create("A");
  await fx.invoke("A", () => a.event({ event: { type: "session.created", properties: { info: { id: "session-A" } } } }));
  await fx.invoke("A", () => a["chat.message"]({ sessionID: "session-A", model: { modelID: "model", providerID: "provider" } }, { parts: [{ text: "fix crash" }] }));
  const prompt = fx.calls.at(-1).payload;
  assert.equal(prompt.model, "model");
  assert.equal(prompt.provider, "provider");
  assert.equal(prompt.task_type, "bugfix");
  assert.match(prompt.prompt.sha256, /^sha256:[a-f0-9]{64}$/);
  assert.deepEqual(await fx.system("A", a), [`SessionStart:${fx.dirs.A}:session-A`, `UserPromptSubmit:${fx.dirs.A}:session-A`]);
  const tool = { sessionID: "session-A", tool: "read", args: { path: "file" } };
  await fx.invoke("A", () => a["tool.execute.before"](tool, { args: tool.args }));
  assert.deepEqual(await fx.system("A", a), [`SessionStart:${fx.dirs.A}:session-A`, `PreToolUse:${fx.dirs.A}:session-A`]);
  const output = { output: "original output" };
  await fx.invoke("A", () => a["tool.execute.after"](tool, output));
  assert.equal(output.output, "recovered output");
  assert.equal(fx.calls.at(-1).payload.tool_output, "original output");
  await fx.event("A", a, "session.compacted");
  const compact = { context: [] };
  await fx.invoke("A", () => a["experimental.session.compacting"]({ sessionID: "session-A" }, compact));
  assert.deepEqual(compact.context, [`PostCompact:${fx.dirs.A}:session-A`]);
  await fx.event("A", a, "session.idle");
  await fx.event("A", a, "session.deleted");
  await fx.invoke("A", () => a.dispose());
  assert.deepEqual(fx.calls.map((call) => call.event), ["SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostCompact", "PreCompact", "Stop", "SessionEnd"]);
  assert.deepEqual(await fx.system("A", a), [`SessionStart:${fx.dirs.A}:session-A`], "deleted context is recreated without a stale hint");
  await a.dispose();
});

// Prevents a contextless factory inheriting the previous project's directory.
test("V1 absent or empty directory preserves inherited child cwd", async (t) => {
  const fx = await fixture(t);
  await fx.create("B");
  for (const ctx of [undefined, {}, { directory: "" }, { directory: 42 }]) {
    const hooks = await fx.plugin.CavemanNative(ctx);
    await fx.event("A", hooks, "session.created");
    assert.equal(fx.calls.at(-1).childCwd, undefined);
    assert.equal(Object.hasOwn(fx.calls.at(-1).payload, "cwd"), false);
    await hooks.dispose();
  }
});

// Prevents child failures or malformed replies escaping the existing fail-open
// contract, changing commands, or contaminating a second factory's cache.
test("V1 failed subprocesses preserve input and leave B operational", async (t) => {
  const fx = await fixture(t);
  const a = await fx.create("A");
  const b = await fx.create("B");
  await fx.event("B", b, "session.created");
  for (const failure of ["throw", "json"]) {
    fx.failWith(failure);
    const output = { args: { command: "original command" } };
    await fx.invoke("A", () => a["tool.execute.before"]({ sessionID: "session-A", tool: "bash" }, output));
    assert.equal(output.args.command, "original command");
    assert.deepEqual(await fx.system("A", a), []);
  }
  fx.failWith(undefined);
  assert.deepEqual(await fx.system("B", b), [`SessionStart:${fx.dirs.B}:session-B`]);
  await b.dispose();
});

// Prevents breaking the dual-entrypoint's shared pure helpers while moving V1
// state. V2 uses the unchanged setup contract and stays alive after V1 disposal.
for (const variant of ["V2", "Both"]) {
  test(`${variant}: V2 setup preserves cwd, hooks and independent disposal`, async (t) => {
    const fx = await fixture(t, variant);
    const setups = {};
    for (const owner of ["A", "B"]) {
      const hooks = new Map();
      let drained;
      const done = new Promise((resolve) => { drained = resolve; });
      const location = { directory: fx.dirs[owner], workspaceID: owner };
      let signal;
      const dispose = await fx.invoke(owner, () => fx.plugin.default.setup({
        location,
        event: { async *subscribe(options) {
          signal = options.signal;
          yield { type: "session.created", location, data: { sessionID: `session-${owner}` } };
          drained();
        } },
        session: { hook: async (name, callback) => { hooks.set(name, callback); } },
        tool: { hook: async (name, callback) => { hooks.set(name, callback); } },
      }));
      await done;
      setups[owner] = { hooks, dispose, signal };
      await fx.invoke(owner, () => hooks.get("prompt")({ sessionID: `session-${owner}`, prompt: { text: "fix bug" } }));
    }
    if (variant === "Both") {
      assert.equal(fx.plugin.default.server, fx.plugin.CavemanNative);
      const v1 = await fx.create("A");
      await fx.event("A", v1, "session.created", "v1-session");
      await fx.invoke("A", () => v1.dispose());
    }
    await fx.invoke("A", () => setups.A.dispose());
    assert.equal(setups.A.signal.aborted, true);
    assert.equal(setups.B.signal.aborted, false);
    const context = { sessionID: "session-B", system: [] };
    await setups.B.hooks.get("context")(context);
    assert.deepEqual(context.system.map((part) => part.text), [`SessionStart:${fx.dirs.B}:session-B`, `UserPromptSubmit:${fx.dirs.B}:session-B`]);
    const before = { sessionID: "session-B", tool: "shell", input: { command: "original" } };
    await fx.invoke("B", () => setups.B.hooks.get("execute.before")(before));
    assert.equal(before.input.command, "rewritten command");
    assert.equal(fx.calls.at(-1).childCwd, fx.dirs.B);
    await fx.invoke("B", () => setups.B.dispose());
    await setups.B.dispose();
    assert.deepEqual(fx.calls.filter((call) => call.event === "SessionEnd").map((call) => call.payload.session_id), variant === "Both" ? ["v1-session", "session-A", "session-B"] : ["session-A", "session-B"]);
    for (const call of fx.calls.filter((call) => call.event !== "shrink-hook")) {
      assert.equal(call.payload.cwd, call.childCwd);
    }
  });
}
