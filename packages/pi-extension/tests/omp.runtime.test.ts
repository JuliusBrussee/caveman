import assert from "node:assert/strict";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "bun:test";

// Run with Bun and a native OMP installation. The manifest entry is the built,
// published artifact, not a Pi shim or a test-only copy of its source.
test("published OMP entry preserves prompt blocks and renews recovery across navigation", async () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-omp-"));
  const server = createServer((_req, res) => { res.writeHead(200); res.end("{}"); });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  assert.ok(address && typeof address === "object");
  const port = address.port;
  const hook = join(root, "hook.mjs");
  const hookLog = join(root, "hooks.jsonl");
  const spawnLog = join(root, "mcp-pids");
  writeFileSync(hook, `
import { appendFileSync } from "node:fs";
const event = process.argv.at(-1);
let input = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => { input += chunk; });
process.stdin.on("end", () => {
  const payload = JSON.parse(input);
  appendFileSync(${JSON.stringify(hookLog)}, JSON.stringify({ event, id: payload.session_id }) + "\\n");
  const result = event === "SessionStart" ? { context: "CORE_" + payload.session_id }
    : event === "UserPromptSubmit" ? { context: "DYNAMIC" }
    : event === "PostToolUse" ? { output_replacement: "SHRUNK <<ccr:handle>>" } : {};
  process.stdout.write(JSON.stringify(result));
});
`);
  const fixture = fileURLToPath(new URL("./fixtures/stub-caveman-mcp.mjs", import.meta.url));
  const mcp = join(root, process.platform === "win32" ? "mcp.cmd" : "mcp");
  writeFileSync(mcp, process.platform === "win32" ? `@node "${fixture}" %*\r\n` : `#!/bin/sh\nexec "${process.execPath}" "${fixture}" "$@"\n`);
  if (process.platform !== "win32") chmodSync(mcp, 0o755);
  mkdirSync(join(root, "run"));
  writeFileSync(join(root, "run", `${port}.json`), JSON.stringify({
    schema: "caveman.proxy.run.v1", pid: process.pid, instance_token: "test", owner: "wrap", recovery_via_mcp: true,
  }));
  const env = {
    HOME: root, USERPROFILE: root,
    CAVEMAN_HOME: root, CAVE_GATEWAY_URL: `http://127.0.0.1:${port}`, CAVEMAN_MCP_BIN: mcp,
    CAVEMAN_PI_HOOK_CMD: JSON.stringify([process.execPath, hook]), STUB_MCP_SPAWN_LOG: spawnLog,
    STUB_MCP_DROP_CAPABILITY: "", STUB_MCP_EXIT_AFTER_INIT: "", STUB_MCP_EXIT_ONCE_FLAG: "", STUB_MCP_HANG_INIT: "",
  };
  const saved = Object.fromEntries(Object.keys(env).map((key) => [key, process.env[key]]));
  Object.assign(process.env, env);
  const handlers = new Map<string, Function>();
  type RecoveryTool = {
    execute(id: string, params: { recovery_handle: string }): Promise<{ content: { text: string }[] }>;
  };
  const tools: RecoveryTool[] = [];
  const original = { provider: "openai", id: "test", api: "openai-completions", baseUrl: "https://api.openai.com/v1" };
  let selected = original;
  let override: string | undefined;
  const model = () => ({ ...original, baseUrl: override ?? original.baseUrl });
  const ctx = (id: string) => ({
    get model() { return selected; }, hasUI: true, ui: { notify() {} },
    sessionManager: { getSessionId: () => id },
    modelRegistry: { getAll: () => [model()], find: () => model(), isUsingOAuth: () => false },
  });
  try {
    const manifest = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8"));
    const { default: factory } = await import(new URL(`../${manifest.omp.extensions[0]}`, import.meta.url).href);
    factory({
      on(name: string, handler: Function) { handlers.set(name, handler); },
      registerTool(value: RecoveryTool) { tools.push(value); },
      registerProvider(_name: string, config: { baseUrl: string }) { override = config.baseUrl; },
      unregisterProvider() { override = undefined; },
      async setModel(value: typeof original) { selected = value; return true; },
    });
    const tool = tools[0];
    assert.ok(tool);
    assert.equal(existsSync(spawnLog), false, "factory must not launch a recovery child");
    const prefix = ["system\nblock", "", "second\n\nblock"];
    for (const event of ["session_start", "session_switch", "session_tree", "session_branch"]) {
      await handlers.get(event)!({ type: event }, ctx(event));
      const prompt = await handlers.get("before_agent_start")!({ prompt: "hello", systemPrompt: prefix }, ctx(event));
      assert.deepEqual(prompt.systemPrompt, [...prefix, `CORE_${event}`, "DYNAMIC"]);
      const recovered = await tool.execute("read-original", { recovery_handle: "ccr_0123456789abcdef0123456789abcdef" });
      assert.equal(recovered.content[0].text, "exact original bytes\nline two éø bytes");
      assert.equal(selected.baseUrl, `http://127.0.0.1:${port}/w/pi/openai/v1`);
    }
    // OMP has no model_select extension event. A different selected provider
    // must be rechecked at the next user-run boundary, without a session reset.
    selected = { ...original, provider: "unlisted", id: "foreign", baseUrl: "http://127.0.0.1:1" };
    await handlers.get("before_agent_start")!({ prompt: "hello", systemPrompt: prefix }, ctx("session_branch"));
    assert.equal(override, undefined, "switching to an unsupported provider must clear the previous override");
    assert.equal(selected.baseUrl, "http://127.0.0.1:1");
    selected = original;
    await handlers.get("before_agent_start")!({ prompt: "hello", systemPrompt: prefix }, ctx("session_branch"));
    assert.equal(selected.baseUrl, `http://127.0.0.1:${port}/w/pi/openai/v1`);
    assert.deepEqual(prefix, ["system\nblock", "", "second\n\nblock"], "injection must not mutate OMP's input blocks");
    const result = { toolName: "read", isError: false, input: {}, content: [{ type: "text", text: "original" }] };
    assert.equal((await handlers.get("tool_result")!(result)).content[0].text, "SHRUNK <<ccr:handle>>");
    assert.equal(await handlers.get("tool_result")!({ ...result, isError: true }), undefined);
    assert.equal(await handlers.get("tool_result")!({ ...result, toolName: "caveman_retrieve" }), undefined);
    await handlers.get("session_shutdown")!();
    assert.equal(override, undefined);
    assert.equal(selected.baseUrl, original.baseUrl);
    await assert.rejects(tool.execute("after-close", { recovery_handle: "handle" }), /not active/);
    const events = readFileSync(hookLog, "utf8").trim().split("\n").map((line) => JSON.parse(line));
    assert.deepEqual(events.filter(({ event }) => event === "SessionEnd").map(({ id }) => id), [
      "session_start", "session_switch", "session_tree", "session_branch",
    ]);
    const pids = readFileSync(spawnLog, "utf8").trim().split("\n").map(Number);
    assert.equal(new Set(pids).size, 4, "each navigation needs a fresh per-session recovery process");
    const alive = (pid: number) => { try { process.kill(pid, 0); return true; } catch { return false; } };
    // Real child-process shutdown is the contract here; fake timers cannot
    // advance a subprocess's stdin/exit lifecycle.
    const deadline = Date.now() + 5000;
    while (pids.some(alive) && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 20));
    assert.deepEqual(pids.filter(alive), [], "shutdown must reap all recovery children");
  } finally {
    await handlers.get("session_shutdown")?.();
    for (const [key, value] of Object.entries(saved)) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
    await new Promise<void>((resolve) => server.close(() => resolve()));
    rmSync(root, { recursive: true, force: true });
  }
}, 30_000);
