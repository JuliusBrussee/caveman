// Integration against the REAL pinned Pi CLI (devDependency 0.84.2): proves the
// extension loads via --extension, the FIRST provider request routes through
// /w/pi, Core rides the system prompt, the hook lifecycle fires, and a closed
// gate keeps every request off the proxy.

import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const packageRoot = join(here, "..");
const piCli = join(packageRoot, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "cli.js");
const extension = join(packageRoot, "dist", "index.mjs");
const mcpStub = join(here, "fixtures", "stub-caveman-mcp.mjs");
const stubProviderExtension = join(here, "fixtures", "stub-provider-extension.mjs");
const havePi = existsSync(piCli);

function startStub() {
  const requests = [];
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", () => {
      requests.push({ method: req.method, path: req.url, body });
      if (req.method === "GET" && req.url === "/health/live") {
        res.writeHead(200, { "content-type": "application/json" });
        res.end("{}");
        return;
      }
      let streaming = true;
      try { streaming = JSON.parse(body).stream !== false; } catch { /* default streaming */ }
      if (streaming) {
        res.writeHead(200, { "content-type": "text/event-stream" });
        res.write(`data: ${JSON.stringify({ id: "s1", object: "chat.completion.chunk", model: "stub", choices: [{ index: 0, delta: { role: "assistant", content: "CAVEMAN_STUB_OK" }, finish_reason: null }] })}\n\n`);
        res.write(`data: ${JSON.stringify({ id: "s1", object: "chat.completion.chunk", model: "stub", choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 10, completion_tokens: 3, total_tokens: 13 } })}\n\n`);
        res.write("data: [DONE]\n\n");
        res.end();
      } else {
        res.writeHead(200, { "content-type": "application/json" });
        res.end(JSON.stringify({ id: "s1", object: "chat.completion", model: "stub", choices: [{ index: 0, message: { role: "assistant", content: "CAVEMAN_STUB_OK" }, finish_reason: "stop" }], usage: { prompt_tokens: 10, completion_tokens: 3, total_tokens: 13 } }));
      }
    });
  });
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => resolve({ server, requests, port: server.address().port }));
  });
}

function fixture(port, { runState = true, recoveryViaMcp = true } = {}) {
  const root = mkdtempSync(join(tmpdir(), "cave-pi-int-"));
  const home = join(root, "home");
  const cavemanHome = join(root, "caveman");
  mkdirSync(home, { recursive: true });
  mkdirSync(join(cavemanHome, "run"), { recursive: true });
  const hookLog = join(root, "hooks.log");
  // A Node script invoked through process.execPath, not a #!/bin/sh shim:
  // Windows cannot execute a shebang script, so the old fixture failed the
  // whole suite there with `spawn EFTYPE` and told us nothing about the code
  // under test. The event is the last argv entry either way.
  const hook = join(root, "caveman-hook.mjs");
  writeFileSync(hook, `
import { appendFileSync } from "node:fs";
const event = process.argv[process.argv.length - 1];
appendFileSync(${JSON.stringify(hookLog)}, event + "\\n");
process.stdin.resume();
process.stdin.on("data", () => {});
const bodies = {
  SessionStart: '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"CORE_MARKER_XYZ"}}',
  UserPromptSubmit: '{"hookSpecificOutput":{"additionalContext":"DYNAMIC_MARKER_ABC"}}',
};
process.stdout.write(bodies[event] ?? "{}");
process.stdin.pause();
process.stdin.unref();
`);
  // CAVEMAN_MCP_BIN is a single path, so the shim has to be directly
  // spawnable: a .cmd on Windows (which RecoveryClient now resolves through
  // portableInvocation), a shebang script elsewhere.
  const mcpShim = join(root, process.platform === "win32" ? "caveman-mcp.cmd" : "caveman-mcp");
  if (process.platform === "win32") {
    writeFileSync(mcpShim, `@node  "${mcpStub}" %*\r\n`);
  } else {
    writeFileSync(mcpShim, `#!/bin/sh\nexec "${process.execPath}" "${mcpStub}" "$@"\n`);
    chmodSync(mcpShim, 0o755);
  }
  if (runState) {
    writeFileSync(join(cavemanHome, "run", `${port}.json`), JSON.stringify({
      schema: "caveman.proxy.run.v1",
      pid: process.pid,
      port,
      listen: `127.0.0.1:${port}`,
      mode: "local",
      owner: "wrap",
      instance_token: "test-token",
      started_at: new Date().toISOString(),
      version: "test",
      recovery_via_mcp: recoveryViaMcp,
    }));
  }
  const env = {
    ...process.env,
    HOME: home,
    CAVEMAN_HOME: cavemanHome,
    CAVE_GATEWAY_URL: `http://127.0.0.1:${port}`,
    CAVEMAN_PI_HOOK_CMD: JSON.stringify([process.execPath, hook, "placeholder"]),
    CAVEMAN_MCP_BIN: mcpShim,
    NO_COLOR: "1",
  };
  delete env.CAVEMAN_PI_EXTENSION;
  return { root, env, hookLog, cleanup: () => rmSync(root, { recursive: true, force: true }) };
}

// The hook script receives argv: <script> placeholder native-hook pi <Event>;
// $4 is the event because "placeholder" occupies $1.

function runPi(env, args) {
  return new Promise((resolve) => {
    // stdin must EOF immediately: pi's non-TTY modes read stdin as attached
    // input and hang forever on an open pipe.
    const child = spawn(process.execPath, [piCli, ...args], { env, cwd: dirname(piCli), stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    const timer = setTimeout(() => child.kill("SIGKILL"), 90_000);
    child.on("exit", (code) => {
      clearTimeout(timer);
      resolve({ code, stdout, stderr });
    });
  });
}

// Drives the BUILT extension directly (no pi CLI needed): the tool_result
// handler used to shrink every tool including caveman_retrieve's own output.
// The proxy files that output as an ObjectCommandResult and masks anything past
// ~448 bytes, so the recovered original came back to the model as a fresh ccr://
// mask and recovery looped instead of terminating — and registering the tool
// disables the proxy's server-side retrieve loop, so nothing else strips it.
test("caveman_retrieve output is never shrunk; other tools still are", async () => {
  const root = mkdtempSync(join(tmpdir(), "cave-pi-shrink-"));
  const hookLog = join(root, "hooks.log");
  const hook = join(root, "hook.mjs");
  writeFileSync(hook, `
import { appendFileSync } from "node:fs";
const event = process.argv[process.argv.length - 1];
let input = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => { input += chunk; });
process.stdin.on("end", () => {
  let toolName = "";
  try { toolName = JSON.parse(input).tool_name ?? ""; } catch { /* logged as empty */ }
  appendFileSync(${JSON.stringify(hookLog)}, event + " " + toolName + "\\n");
  process.stdout.write('{"output_replacement":"SHRUNK <<ccr:handle>>"}');
});
`);
  const prior = process.env.CAVEMAN_PI_HOOK_CMD;
  process.env.CAVEMAN_PI_HOOK_CMD = JSON.stringify([process.execPath, hook, "placeholder"]);
  try {
    const { default: factory } = await import(pathToFileURL(extension).href);
    const handlers = new Map();
    factory({ registerTool: () => {}, on: (name, fn) => handlers.set(name, fn) });
    const result = (toolName) => handlers.get("tool_result")({ toolName, input: {}, isError: false, content: [{ type: "text", text: "exact original bytes" }] });

    const shrunk = await result("read_file");
    assert.equal(shrunk?.content?.[0]?.text, "SHRUNK <<ccr:handle>>", "ordinary tool output must still shrink");
    assert.equal(await result("caveman_retrieve"), undefined, "recovered originals must reach the model unmasked");
    // Not merely unchanged output: the runtime is never even asked, so no
    // handle can be minted for bytes that were already recovered.
    assert.deepEqual(readFileSync(hookLog, "utf8").trim().split("\n"), ["PostToolUse read_file"]);
  } finally {
    if (prior === undefined) delete process.env.CAVEMAN_PI_HOOK_CMD; else process.env.CAVEMAN_PI_HOOK_CMD = prior;
    rmSync(root, { recursive: true, force: true });
  }
});

test("open gate: first request routes through /w/pi with Core in the system prompt", { skip: !havePi && "pi devDependency missing" }, async () => {
  const { server, requests, port } = await startStub();
  const fx = fixture(port);
  try {
    const out = await runPi(fx.env, [
      "--extension", stubProviderExtension, "--extension", extension,
      "--no-session", "--no-skills", "--no-context-files", "--no-prompt-templates", "--no-themes", "--no-extensions",
      "--provider", "stubprov", "--model", "stub-model",
      "-p", "say hi",
    ]);
    assert.match(out.stdout, /CAVEMAN_STUB_OK/, `stdout: ${out.stdout}\nstderr: ${out.stderr}`);
    const providerHits = requests.filter((r) => r.method === "POST");
    assert.ok(providerHits.length >= 1, `no provider request reached the stub; all: ${JSON.stringify(requests)}`);
    // FIRST provider request — not a later one — must already ride /w/pi.
    assert.equal(providerHits[0].path, "/w/pi/openai/v1/chat/completions");
    assert.match(providerHits[0].body, /CORE_MARKER_XYZ/, "Core must ride the system prompt of the first request");
    const events = readFileSync(fx.hookLog, "utf8").trim().split("\n");
    assert.ok(events.includes("SessionStart"), `hook log: ${events}`);
    assert.ok(events.includes("UserPromptSubmit"), `hook log: ${events}`);
  } finally {
    fx.cleanup();
    server.close();
  }
});

test("closed gate (no run-state): zero proxy requests and a visible direct-mode notice", { skip: !havePi && "pi devDependency missing" }, async () => {
  const { server, requests, port } = await startStub();
  const fx = fixture(port, { runState: false });
  try {
    const out = await runPi(fx.env, [
      "--extension", stubProviderExtension, "--extension", extension,
      "--no-session", "--no-skills", "--no-context-files", "--no-prompt-templates", "--no-themes", "--no-extensions",
      "--provider", "stubprov", "--model", "stub-model",
      "-p", "say hi",
    ]);
    // Direct mode points at the dead loopback port the stub provider declares —
    // the call fast-fails locally; what matters is honesty and zero routed traffic.
    const providerHits = requests.filter((r) => r.method === "POST");
    assert.equal(providerHits.length, 0, `gate closed but stub saw: ${JSON.stringify(providerHits)}`);
    assert.match(out.stderr + out.stdout, /direct mode, no compression/, `stderr: ${out.stderr}`);
  } finally {
    fx.cleanup();
    server.close();
  }
});

test("published recovery=false: gate refuses even with a live proxy and working MCP", { skip: !havePi && "pi devDependency missing" }, async () => {
  const { server, requests, port } = await startStub();
  const fx = fixture(port, { recoveryViaMcp: false });
  try {
    const out = await runPi(fx.env, [
      "--extension", stubProviderExtension, "--extension", extension,
      "--no-session", "--no-skills", "--no-context-files", "--no-prompt-templates", "--no-themes", "--no-extensions",
      "--provider", "stubprov", "--model", "stub-model",
      "-p", "say hi",
    ]);
    // Proxy is alive (health probe hits the stub) but published recovery is
    // false — this session could never compress, so nothing may route.
    const providerHits = requests.filter((r) => r.method === "POST");
    assert.equal(providerHits.length, 0, `gate must refuse (false, *): ${JSON.stringify(providerHits)}`);
    assert.match(out.stderr + out.stdout, /recovery not available/, `stderr: ${out.stderr}`);
  } finally {
    fx.cleanup();
    server.close();
  }
});
