import { test } from "node:test";
import assert from "node:assert";
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync, readFileSync, mkdirSync, existsSync, symlinkSync, chmodSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { createServer } from "node:net";
import { createHmac } from "node:crypto";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");
const fastNativeHook = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "native-hook-fast.js");

// runHook feeds a Claude PreToolUse event to `caveman shrink-hook` on stdin and
// captures what it prints (the rewrite, or nothing = "run as-is").
function runHook(payload, env = process.env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "shrink-hook"], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
    child.stdin.end(JSON.stringify(payload));
  });
}

function runNativeHook(agent, payload, env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "native-hook", agent], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
    child.stdin.end(typeof payload === "string" ? payload : JSON.stringify(payload));
  });
}

function runFastNativeHook(agent, payload, env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [fastNativeHook, "native-hook", agent], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
    child.stdin.end(typeof payload === "string" ? payload : JSON.stringify(payload));
  });
}

function runCli(argv, env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...argv], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

function writeWrapConfig(home, wrap) {
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap }, null, 2));
}

// A noisy, finite, non-interactive command is rewritten to run through
// `caveman shrink` — the RTK-style command-output compression, recoverable.
test("shrink-hook rewrites a noisy command through caveman shrink", async () => {
  const out = await runHook({ tool_name: "Bash", tool_input: { command: "git status" } });
  assert.equal(out.code, 0, out.stderr);
  const o = JSON.parse(out.stdout);
  assert.equal(o.hookSpecificOutput.hookEventName, "PreToolUse");
  assert.match(o.hookSpecificOutput.updatedInput.command, /shrink -- git status$/);
});

test("shrink-hook rewrites cargo test", async () => {
  const out = await runHook({ tool_name: "Bash", tool_input: { command: "cargo test --all" } });
  const o = JSON.parse(out.stdout);
  assert.match(o.hookSpecificOutput.updatedInput.command, /shrink -- cargo test --all$/);
});

test("shrink-hook emits Codex allow + updatedInput for exec_command", async () => {
  const out = await runHook({ tool_name: "exec_command", tool_input: { command: "git status" } });
  assert.equal(out.code, 0, out.stderr);
  const parsed = JSON.parse(out.stdout);
  assert.equal(parsed.hookSpecificOutput.permissionDecision, "allow");
  assert.match(parsed.hookSpecificOutput.updatedInput.command, /shrink -- git status$/);
});

test("native-hook injects stable Core, stores bounded metadata, and emits no marker when proxy is off", async () => {
  const caveHome = mkdtempSync(join(tmpdir(), "cave-native-"));
  writeWrapConfig(caveHome, { proxy: false });
  const env = { ...process.env, HOME: caveHome, CAVEMAN_HOME: caveHome, CAVEMAN_TELEMETRY: "0" };
  const secret = "raw prompt must not persist";
  const out = await runNativeHook("codex", {
    hook_event_name: "SessionStart",
    session_id: "session-1",
    cwd: "/private/repo",
    prompt: secret,
  }, env);
  assert.equal(out.code, 0, out.stderr);
  const response = JSON.parse(out.stdout);
  assert.equal(response.hookSpecificOutput.hookEventName, "SessionStart");
  assert.match(response.hookSpecificOutput.additionalContext, /Build simplest complete system/);
  assert.match(response.hookSpecificOutput.additionalContext, /Standard library, native platform/);
  assert.match(response.hookSpecificOutput.additionalContext, /trust-boundary validation/);
  assert.doesNotMatch(response.hookSpecificOutput.additionalContext, /caveman-session-v1/);
  assert.equal(existsSync(join(caveHome, "runtime", "session.key")), false);
  const log = readFileSync(join(caveHome, "runtime", "native-events.jsonl"), "utf8");
  assert.doesNotMatch(log, new RegExp(secret));
  assert.doesNotMatch(log, /private\/repo/);
  const entry = JSON.parse(log.trim());
  assert.equal(entry.event, "SessionStart");
  assert.equal(entry.host_session_id, "session-1");
  assert.match(entry.cwd_sha256, /^sha256:/);
});

test("native Core bytes stay byte-identical across direct sessions", async () => {
  const caveHome = mkdtempSync(join(tmpdir(), "cave-native-prefix-"));
  writeWrapConfig(caveHome, { proxy: false });
  const env = { ...process.env, HOME: caveHome, CAVEMAN_HOME: caveHome, CAVEMAN_TELEMETRY: "0" };
  const contexts = [];
  for (const session_id of ["stable-a", "stable-b"]) {
    const out = await runNativeHook("claude", { hook_event_name: "SessionStart", session_id }, env);
    assert.equal(out.code, 0, out.stderr);
    contexts.push(JSON.parse(out.stdout).hookSpecificOutput.additionalContext);
  }
  assert.equal(contexts[0], contexts[1], "stable Core must be byte-identical");
  assert.doesNotMatch(contexts[0], /caveman-session-v1/);
});

test("native experiment profiles select real Core and shrink behavior", async () => {
  const caveHome = mkdtempSync(join(tmpdir(), "cave-native-profile-"));
  writeWrapConfig(caveHome, { proxy: false });
  const base = { ...process.env, HOME: caveHome, CAVEMAN_HOME: caveHome, CAVEMAN_TELEMETRY: "0" };
  const lean = await runNativeHook("claude", { hook_event_name: "SessionStart", session_id: "lean" }, {
    ...base, CAVEMAN_NATIVE_MODE: "safe", CAVEMAN_NATIVE_PROFILE: "core-lean-build",
  });
  assert.match(JSON.parse(lean.stdout).hookSpecificOutput.additionalContext, /# Lean build/);
  const noShrink = await runHook({ tool_name: "Bash", tool_input: { command: "git status" } }, {
    ...base, CAVEMAN_NATIVE_MODE: "safe", CAVEMAN_NATIVE_PROFILE: "core",
  });
  assert.equal(noShrink.stdout, "");
  const invalid = await runNativeHook("claude", { hook_event_name: "SessionStart", session_id: "invalid" }, {
    ...base, CAVEMAN_NATIVE_MODE: "safe", CAVEMAN_NATIVE_PROFILE: "future-profile",
  });
  assert.equal(invalid.stdout, "", "invalid profile degrades to record with no model-visible marker");
});

test("native-hook malformed input is silent fail-open", async () => {
  const caveHome = mkdtempSync(join(tmpdir(), "cave-native-"));
  const out = await runNativeHook("claude", "{bad", { ...process.env, HOME: caveHome, CAVEMAN_HOME: caveHome, CAVEMAN_TELEMETRY: "0" });
  assert.equal(out.code, 0);
  assert.equal(out.stdout, "");
  assert.equal(out.stderr, "");
  assert.equal(existsSync(join(caveHome, "runtime", "native-events.jsonl")), false);
});

test("native-hook invalid runtime response and timeout fail open without stdout pollution", { skip: process.platform === "win32" }, async () => {
  for (const behavior of ["invalid", "unknown-action", "bad-context", "timeout"]) {
    const caveHome = mkdtempSync(join(tmpdir(), `cave-native-${behavior}-`));
    const runDir = join(caveHome, "run");
    const socketPath = join(runDir, "native.sock");
    mkdirSync(runDir, { recursive: true, mode: 0o700 });
    const sockets = new Set();
    const server = createServer((socket) => {
      sockets.add(socket);
      socket.on("close", () => sockets.delete(socket));
      if (behavior === "invalid") {
        socket.on("data", () => {});
        socket.on("end", () => socket.end("not-json\n"));
      } else if (behavior === "unknown-action") {
        socket.on("data", () => {});
        socket.on("end", () => socket.end(JSON.stringify({
          protocol_version: 1, action: "erase", visibility: "silent", fail_open: true,
        }) + "\n"));
      } else if (behavior === "bad-context") {
        socket.on("data", () => {});
        socket.on("end", () => socket.end(JSON.stringify({
          protocol_version: 1, action: "allow", context: { unsafe: true }, visibility: "silent", fail_open: true,
        }) + "\n"));
      } else {
        socket.on("data", () => {});
      }
    });
    await new Promise((resolve, reject) => {
      server.once("error", reject);
      server.listen(socketPath, resolve);
    });
    const started = Date.now();
    try {
      const out = await runNativeHook("claude", {
        hook_event_name: "PreToolUse", session_id: `host-${behavior}`, tool_name: "read_file",
        tool_input: { path: "src/current.ts" },
      }, { ...process.env, HOME: caveHome, CAVEMAN_HOME: caveHome, CAVEMAN_TELEMETRY: "0" });
      assert.equal(out.code, 0, out.stderr);
      assert.equal(out.stdout, "");
      assert.equal(out.stderr, "");
      assert.ok(Date.now() - started < 1000, `${behavior} hook exceeded fail-open budget`);
      assert.ok(existsSync(join(caveHome, "runtime", "native-events.jsonl")), `${behavior} must record bounded fallback evidence`);
    } finally {
      for (const socket of sockets) socket.destroy();
      await new Promise((resolve) => server.close(resolve));
    }
  }
});

test("native session marker requires a successful runtime decision identity", { skip: process.platform === "win32" }, async () => {
  const caveHome = mkdtempSync(join(tmpdir(), "cave-native-unregistered-"));
  const runDir = join(caveHome, "run");
  const socketPath = join(runDir, "native.sock");
  mkdirSync(runDir, { recursive: true, mode: 0o700 });
  const runtime = createServer((socket) => {
    socket.on("data", () => {});
    socket.on("end", () => socket.end(JSON.stringify({
      protocol_version: 1, action: "allow", visibility: "silent", fail_open: true,
    }) + "\n"));
  });
  await new Promise((resolve, reject) => {
    runtime.once("error", reject);
    runtime.listen(socketPath, resolve);
  });
  const route = createServer((socket) => socket.end());
  await new Promise((resolve, reject) => {
    route.once("error", reject);
    route.listen(0, "127.0.0.1", resolve);
  });
  try {
    const address = route.address();
    assert.ok(address && typeof address === "object");
    const out = await runNativeHook("claude", {
      hook_event_name: "SessionStart", session_id: "unregistered-1",
    }, {
      ...process.env, HOME: caveHome, CAVEMAN_HOME: caveHome, CAVEMAN_TELEMETRY: "0",
      CAVE_GATEWAY_URL: `http://127.0.0.1:${address.port}`,
    });
    assert.equal(out.code, 0, out.stderr);
    const context = JSON.parse(out.stdout).hookSpecificOutput.additionalContext;
    assert.match(context, /Build simplest complete system/);
    assert.doesNotMatch(context, /caveman-session-v1/);
  } finally {
    await new Promise((resolve) => runtime.close(resolve));
    await new Promise((resolve) => route.close(resolve));
  }
});

test("native-hook normalizes prompt digest and exact tool output over local runtime socket", { skip: process.platform === "win32" }, async () => {
  const caveHome = mkdtempSync(join(tmpdir(), "cave-native-"));
  const runDir = join(caveHome, "run");
  const socketPath = join(runDir, "native.sock");
  mkdirSync(runDir, { recursive: true, mode: 0o700 });
  const requests = [];
  const server = createServer((socket) => {
    let raw = "";
    socket.setEncoding("utf8");
    socket.on("data", (chunk) => (raw += chunk));
    socket.on("end", () => {
      const request = JSON.parse(raw);
      requests.push(request);
      socket.end(JSON.stringify({
        protocol_version: 1,
        action: "allow",
        decision_id: `dec-${request.event.type}`,
        ...(request.event.type === "prompt.submit" ? { context: "Caveman task contract (dynamic tail): policy=surgical-patch" } : {}),
        ...(request.event.type === "tool.after" ? { output_replacement: "[FileObservation]\nfull: ccr://ccr_obj_123" } : {}),
        ...(request.event.type === "session.end" ? { message: "Caveman · tests passed · 1 repeats detected · 500 B exact context recorded" } : {}),
        visibility: "silent",
        fail_open: true,
      }) + "\n");
    });
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(socketPath, resolve);
  });
  const routeServer = createServer((socket) => socket.end());
  await new Promise((resolve, reject) => {
    routeServer.once("error", reject);
    routeServer.listen(0, "127.0.0.1", resolve);
  });
  try {
    const routeAddress = routeServer.address();
    assert.ok(routeAddress && typeof routeAddress === "object");
    const env = {
      ...process.env, HOME: caveHome, CAVEMAN_HOME: caveHome, CAVEMAN_TELEMETRY: "0",
      CAVE_GATEWAY_URL: `http://127.0.0.1:${routeAddress.port}`,
    };
    const secretPrompt = "fix auth without persisting sk-prompt-secret";
    const prompt = await runNativeHook("claude", {
      hook_event_name: "UserPromptSubmit",
      session_id: "host-7",
      model: { provider: "anthropic", model: "claude-sonnet-5" },
      prompt: secretPrompt,
    }, env);
    assert.equal(prompt.code, 0, prompt.stderr);
    assert.match(JSON.parse(prompt.stdout).hookSpecificOutput.additionalContext, /dynamic tail/);
    const output = "unicode ✓\n".repeat(100);
    mkdirSync(join(caveHome, "src"), { recursive: true });
    writeFileSync(join(caveHome, "src", "index.ts"), "export const fixture = true;\n");
    const before = await runNativeHook("claude", {
      hook_event_name: "PreToolUse",
      session_id: "host-7",
      cwd: caveHome,
      tool_name: "read_file",
      tool_input: { file_path: "src/index.ts" },
    }, env);
    assert.equal(before.code, 0, before.stderr);
    const tool = await runNativeHook("claude", {
      hook_event_name: "PostToolUse",
      session_id: "host-7",
      tool_name: "read_file",
      tool_input: { file_path: "src/auth.ts" },
      tool_response: output,
    }, env);
    assert.equal(tool.code, 0, tool.stderr);
    assert.equal(JSON.parse(tool.stdout).hookSpecificOutput.updatedToolOutput, "[FileObservation]\nfull: ccr://ccr_obj_123");
    const failed = await runNativeHook("claude", {
      hook_event_name: "PostToolUseFailure",
      session_id: "host-7",
      tool_name: "Bash",
      tool_input: { command: "go test ./..." },
      tool_response: "exit status 1",
    }, env);
    assert.equal(failed.code, 0, failed.stderr);
    assert.equal(requests.length, 4);
    assert.equal(requests[0].event.type, "prompt.submit");
    assert.equal(requests[0].session.caveman_session_id, "claude:host-7");
    assert.deepEqual(requests[0].model, { provider: "anthropic", model: "claude-sonnet-5" });
    assert.equal(requests[0].prompt.bytes, Buffer.byteLength(secretPrompt));
    assert.match(requests[0].prompt.sha256, /^sha256:[0-9a-f]{64}$/);
    assert.equal(requests[0].task_profile.type, "bugfix");
    assert.doesNotMatch(JSON.stringify(requests[0]), /sk-prompt-secret/);
    assert.equal(requests[1].event.type, "tool.before");
    assert.match(requests[1].tool.input_state, /^sha256:[0-9a-f]{64}$/);
    assert.equal(requests[2].event.type, "tool.after");
    assert.equal(Buffer.from(requests[2].tool.output, "base64").toString("utf8"), output);
    assert.equal(requests[3].event.type, "tool.failure");
    assert.equal(Buffer.from(requests[3].tool.output, "base64").toString("utf8"), "exit status 1");
    const mutedPolicy = await runNativeHook("claude", {
      hook_event_name: "UserPromptSubmit",
      session_id: "host-muted",
      prompt: "add another feature",
    }, { ...env, CAVEMAN_CORE: "off" });
    assert.equal(mutedPolicy.code, 0, mutedPolicy.stderr);
    assert.equal(mutedPolicy.stdout, "", "think.core off must suppress model-visible task policy while runtime remains active");
    assert.equal(requests[4].event.type, "prompt.submit");
    const geminiStart = await runNativeHook("gemini", {
      hook_event_name: "SessionStart",
      session_id: "gemini-8",
      source: "startup",
    }, env);
    assert.equal(geminiStart.code, 0, geminiStart.stderr);
    const geminiStartOutput = JSON.parse(geminiStart.stdout);
    assert.equal(geminiStartOutput.hookSpecificOutput.hookEventName, "SessionStart");
    assert.match(geminiStartOutput.hookSpecificOutput.additionalContext, /Build simplest complete system/);
    const marker = geminiStartOutput.hookSpecificOutput.additionalContext.match(/\[\[caveman-session-v1 sid="([A-Za-z0-9_-]+)" sig="([0-9a-f]{64})"\]\]/);
    assert.ok(marker, "live local runtime + route must inject signed correlation marker");
    assert.equal(Buffer.from(marker[1], "base64url").toString("utf8"), "gemini:gemini-8");
    const markerKey = readFileSync(join(caveHome, "runtime", "session.key"));
    assert.equal(createHmac("sha256", markerKey).update(`caveman-session-v1\0${marker[1]}`).digest("hex"), marker[2]);
    const geminiPrompt = await runNativeHook("gemini", {
      hook_event_name: "BeforeAgent",
      session_id: "gemini-8",
      prompt: "inspect current implementation",
    }, env);
    assert.equal(geminiPrompt.code, 0, geminiPrompt.stderr);
    const geminiTool = await runNativeHook("gemini", {
      hook_event_name: "AfterTool",
      session_id: "gemini-8",
      tool_name: "read_file",
      tool_input: { path: "src/index.ts" },
      tool_output: "gemini output",
    }, env);
    assert.equal(geminiTool.code, 0, geminiTool.stderr);
    assert.equal(requests.length, 8);
    assert.equal(requests[5].event.type, "session.start");
    assert.equal(requests[6].event.type, "prompt.submit");
    assert.equal(requests[6].session.caveman_session_id, "gemini:gemini-8");
    assert.equal(JSON.parse(geminiPrompt.stdout).hookSpecificOutput.additionalContext, "Caveman task contract (dynamic tail): policy=surgical-patch");
    assert.equal(requests[7].event.type, "tool.after");
    assert.equal(Buffer.from(requests[7].tool.output, "base64").toString("utf8"), "gemini output");
    const ended = await runNativeHook("claude", {
      hook_event_name: "SessionEnd",
      session_id: "host-7",
    }, env);
    assert.equal(ended.code, 0, ended.stderr);
    assert.match(ended.stderr, /^Caveman · tests passed · 1 repeats detected · 500 B exact context recorded\n$/);
    assert.equal(requests[8].event.type, "session.end");
    assert.equal(existsSync(join(caveHome, "runtime", "native-events.jsonl")), false);
  } finally {
    await new Promise((resolve) => server.close(resolve));
    await new Promise((resolve) => routeServer.close(resolve));
  }
});

test("installed fast native-hook preserves normalization and intervention contract", { skip: process.platform === "win32" }, async () => {
  const caveHome = mkdtempSync(join(tmpdir(), "cave-native-fast-"));
  const repository = mkdtempSync(join(tmpdir(), "cave-native-fast-repo-"));
  assert.equal(spawnSync("git", ["-C", repository, "init"]).status, 0);
  writeFileSync(join(repository, "current.ts"), "export const current = true;\n");
  assert.equal(spawnSync("git", ["-C", repository, "add", "current.ts"]).status, 0);
  const runDir = join(caveHome, "run");
  const socketPath = join(runDir, "native.sock");
  mkdirSync(runDir, { recursive: true, mode: 0o700 });
  const requests = [];
  const server = createServer((socket) => {
    let raw = "";
    socket.setEncoding("utf8");
    socket.on("data", (chunk) => (raw += chunk));
    socket.on("end", () => {
      const request = JSON.parse(raw);
      requests.push(request);
      socket.end(JSON.stringify({
        protocol_version: 1,
        action: "allow",
        decision_id: `dec-${request.event.type}`,
        ...(request.event.type === "prompt.submit" ? { context: "bounded current task state" } : {}),
        ...(request.event.type === "tool.after" ? { output_replacement: "[FileObservation] full: ccr://fast" } : {}),
        visibility: "silent",
        fail_open: true,
      }) + "\n");
    });
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(socketPath, resolve);
  });
  try {
    const env = { ...process.env, HOME: caveHome, CAVEMAN_HOME: caveHome, CAVEMAN_NATIVE_MODE: "safe", CAVEMAN_TELEMETRY: "0" };
    const prompt = await runFastNativeHook("claude", {
      hook_event_name: "UserPromptSubmit", session_id: "fast-1", prompt: "fix auth sk-fast-secret",
    }, env);
    assert.equal(prompt.code, 0, prompt.stderr);
    assert.equal(JSON.parse(prompt.stdout).hookSpecificOutput.additionalContext, "bounded current task state");
    mkdirSync(join(caveHome, ".caveman-cloud"), { recursive: true });
    writeFileSync(join(caveHome, ".caveman-cloud", "config.json"), JSON.stringify({ think: { core: false } }));
    const muted = await runFastNativeHook("claude", {
      hook_event_name: "UserPromptSubmit", session_id: "fast-muted", prompt: "add feature",
    }, { ...env, CAVEMAN_CORE: "invalid-falls-back-to-config" });
    assert.equal(muted.code, 0, muted.stderr);
    assert.equal(muted.stdout, "");
    const after = await runFastNativeHook("claude", {
      hook_event_name: "PostToolUse", session_id: "fast-1", tool_name: "read_file",
      tool_input: { path: "src/auth.ts" }, tool_output: "exact output",
    }, env);
    assert.equal(after.code, 0, after.stderr);
    assert.equal(JSON.parse(after.stdout).hookSpecificOutput.updatedToolOutput, "[FileObservation] full: ccr://fast");
    const search = await runFastNativeHook("claude", {
      hook_event_name: "PreToolUse", session_id: "fast-1", tool_name: "rg",
      tool_input: { pattern: "current", path: "." }, cwd: repository,
    }, env);
    assert.equal(search.code, 0, search.stderr);
    assert.equal(search.stdout, "");
    const continuation = await runFastNativeHook("claude", {
      hook_event_name: "UserPromptSubmit", session_id: "fast-1", prompt: "fix that",
    }, env);
    assert.equal(continuation.code, 0, continuation.stderr);
    assert.equal(requests.length, 5);
    assert.equal(requests[0].prompt.bytes, Buffer.byteLength("fix auth sk-fast-secret"));
    assert.match(requests[0].prompt.sha256, /^sha256:[0-9a-f]{64}$/);
    assert.equal(requests[0].task_profile.type, "bugfix");
    assert.doesNotMatch(JSON.stringify(requests[0]), /sk-fast-secret/);
    assert.equal(requests[1].event.type, "prompt.submit");
    assert.equal(Buffer.from(requests[2].tool.output, "base64").toString("utf8"), "exact output");
    assert.match(requests[3].session.repository_state, /^git:sha256:[0-9a-f]{64}$/);
    assert.equal(requests[4].task_profile.type, "bugfix");
    assert.equal(requests[4].task_profile.continuation, true);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

test("record policy keeps native hooks and shrink byte-pass-through", { skip: process.platform === "win32" }, async () => {
  const caveHome = mkdtempSync(join(tmpdir(), "cave-native-record-"));
  const runDir = join(caveHome, "run");
  const socketPath = join(runDir, "native.sock");
  mkdirSync(runDir, { recursive: true, mode: 0o700 });
  const requests = [];
  const server = createServer((socket) => {
    let raw = "";
    socket.setEncoding("utf8");
    socket.on("data", (chunk) => (raw += chunk));
    socket.on("end", () => {
      const request = JSON.parse(raw);
      requests.push(request);
      socket.end(JSON.stringify({
        protocol_version: 1,
        policy_mode: "record",
        action: "allow",
        context: "must not be injected in record",
        output_replacement: "must not replace in record",
        visibility: "silent",
        fail_open: true,
      }) + "\n");
    });
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(socketPath, resolve);
  });
  try {
    const env = {
      ...process.env, HOME: caveHome, CAVEMAN_HOME: caveHome, CAVEMAN_NATIVE_MODE: "record", CAVEMAN_TELEMETRY: "0",
      CAVE_GATEWAY_URL: "http://127.0.0.1:1",
    };
    const start = await runNativeHook("claude", { hook_event_name: "SessionStart", session_id: "record-1" }, env);
    assert.equal(start.code, 0, start.stderr);
    assert.equal(start.stdout, "", "record mode without a live local route must be model-byte-pass-through");
    const after = await runNativeHook("claude", {
      hook_event_name: "PostToolUse", session_id: "record-1", tool_name: "read_file",
      tool_input: { path: "src/a.ts" }, tool_output: "unchanged",
    }, env);
    assert.equal(after.code, 0, after.stderr);
    assert.equal(after.stdout, "");
    assert.equal(requests.length, 2);
    assert.ok(requests.every((request) => request.policy_mode === "record"));
    const shrink = await runHook({ tool_name: "Bash", tool_input: { command: "git status" } }, env);
    assert.equal(shrink.code, 0, shrink.stderr);
    assert.equal(shrink.stdout, "");
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

test("inspect renders latest honest native receipt and supports host session lookup", async () => {
  const caveHome = mkdtempSync(join(tmpdir(), "cave-native-"));
  const receiptDir = join(caveHome, "receipts");
  mkdirSync(receiptDir, { recursive: true, mode: 0o700 });
  writeFileSync(join(receiptDir, "session-abc123.json"), JSON.stringify({
    schema: "caveman.native.receipt.v1",
    session_id: "codex:host-22",
    host_session_id: "host-22",
    agent: "codex",
    generated_at: "2026-08-08T12:00:00Z",
    outcome: { verifier: "not_observed", tests: "not_observed" },
    execution: {
      native_events: { value: 9, unit: "events", basis: "observed" },
      repeated_actions_detected: { value: 2, unit: "actions", basis: "observed" },
      exact_context_recorded: { value: 4096, unit: "bytes", basis: "observed" },
      mask_candidate_context: { value: 2048, unit: "bytes", basis: "observed" },
    },
    provider_usage: {
      status: "correlated", correlation_basis: "unique_recent_session", requests: 3, provider_complete_requests: 2,
      input_tokens: 1200, output_tokens: 80, cached_input_tokens: 700,
      cache_creation_input_tokens: 0, reasoning_tokens: 20,
      catalog_list_price_subtotal_usd: 0.0123, inferred_savings_usd: 0.004,
      compression_tokens_before: 900, compression_tokens_after: 300,
      compression_token_count_basis: "estimated_engine_o200k", token_usage_coverage: "mixed_or_unavailable",
      cost_basis: "catalog_list_price_subtotal_provider_complete_priced_rows", savings_basis: "inferred_standalone_not_verified",
    },
    policy: { active: ["core", "inform-ledger"], unresolved_assumptions: 0, decision_ids: ["dec_1"] },
    claim_status: {
      compression_reduction: "not_attested_by_native_runtime",
      task_savings: "not_verified_without_paired_baseline",
    },
  }) + "\n", { mode: 0o600 });
  const env = { ...process.env, HOME: caveHome, CAVEMAN_HOME: caveHome, CAVEMAN_TELEMETRY: "0" };
  const human = await runCli(["inspect", "host-22"], env);
  assert.equal(human.code, 0, human.stderr);
  assert.match(human.stdout, /Caveman session host-22 · codex/);
  assert.match(human.stdout, /repeats detected\s+2 actions · observed/);
  assert.match(human.stdout, /session correlation\s+unique recent session · approximate/);
  assert.match(human.stdout, /provider tokens\s+1,200 input · 80 output · mixed_or_unavailable/);
  assert.match(human.stdout, /catalog list-price subtotal\s+\$0\.012300 · not provider invoice/);
  assert.match(human.stdout, /local inferred savings\s+\$0\.004000 · not verified/);
  assert.match(human.stdout, /task savings\s+not_verified_without_paired_baseline/);
  const machine = await runCli(["inspect", "host-22", "--json"], env);
  assert.equal(machine.code, 0, machine.stderr);
  assert.equal(JSON.parse(machine.stdout).session_id, "codex:host-22");
});

test("why renders deterministic Decision Ledger basis through local proxy", async () => {
  const caveHome = mkdtempSync(join(tmpdir(), "cave-native-"));
  const proxy = join(caveHome, "fake-proxy");
  writeFileSync(proxy, `#!/bin/sh
printf '%s\\n' '{"schema":"caveman.native.why.v1","decision_id":"dec_0123456789abcdef01234567","session_id":"codex:host-22","timestamp_ms":1770000000000,"action":"observe","reason":"current_observation_reused","input_basis":{"agent":"codex","event":"tool.before","tool_name":"read_file"},"alternatives_rejected":[],"task_state_before":"active","task_state_after":"active","currentness":"current","recovery_ref":"ccr://ccr_obj_123"}'
`, { mode: 0o700 });
  chmodSync(proxy, 0o700);
  const env = { ...process.env, HOME: caveHome, CAVEMAN_HOME: caveHome, CAVEMAN_PROXY_BIN: proxy, CAVEMAN_TELEMETRY: "0" };
  const human = await runCli(["why", "dec_0123456789abcdef01234567"], env);
  assert.equal(human.code, 0, human.stderr);
  assert.match(human.stdout, /reason\s+current_observation_reused/);
  assert.match(human.stdout, /recovery\s+ccr:\/\/ccr_obj_123/);
  const machine = await runCli(["why", "dec_0123456789abcdef01234567", "--json"], env);
  assert.equal(machine.code, 0, machine.stderr);
  assert.equal(JSON.parse(machine.stdout).session_id, "codex:host-22");
});

// Conservative by design: anything it can't safely shrink passes through with NO
// output (= no rewrite). Shell operators, streaming/interactive flags,
// editor-opening subcommands, non-allowlisted commands, and our own wrapper.
for (const command of [
  "echo hello",                       // not a noisy-read command
  "cd src && ls",                     // shell operator (&&)
  "git status | head",                // pipe — shrink execs argv, would change meaning
  "cat big.log > out.txt",            // redirect
  "caveman shrink -- git status",     // already wrapped — never double-wrap
  "git commit -m wip",                // opens an editor / interactive
  "kubectl logs -f web",              // -f streams forever — shrink must terminate
  "docker run -it ubuntu bash",       // interactive tty
  "vim notes.md",                     // not allowlisted / interactive
]) {
  test(`shrink-hook passes through (no rewrite): ${command}`, async () => {
    const out = await runHook({ tool_name: "Bash", tool_input: { command } });
    assert.equal(out.code, 0);
    assert.equal(out.stdout, "", `must not rewrite: ${command}`);
  });
}

test("shrink-hook ignores non-Bash tools", async () => {
  const out = await runHook({ tool_name: "Read", tool_input: { file_path: "/etc/hosts" } });
  assert.equal(out.code, 0);
  assert.equal(out.stdout, "");
});

test("shrink-hook tolerates malformed input", async () => {
  const child = spawn("node", [cli, "shrink-hook"]);
  let stdout = "";
  child.stdout.on("data", (d) => (stdout += d));
  const code = await new Promise((r) => { child.on("exit", r); child.stdin.end("not json"); });
  assert.equal(code, 0);
  assert.equal(stdout, "");
});

// `hooks install claude` installs only the conservative shell rewrite. Full
// native lifecycle belongs to `caveman enable claude`.
test("hooks install claude registers a Bash PreToolUse hook and a marker", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir };

  const out = await runCli(["hooks", "install", "claude"], env);
  assert.equal(out.code, 0, out.stderr);

  const settings = JSON.parse(readFileSync(join(home, ".claude", "settings.json"), "utf8"));
  const pre = settings.hooks.PreToolUse;
  assert.ok(Array.isArray(pre), "PreToolUse must be an array");
  const entry = pre.find((e) => e.matcher === "Bash");
  assert.ok(entry, "must add a Bash matcher");
  assert.ok(entry.hooks.some((h) => h.type === "command" && h.command.includes("shrink-hook")), "command must call shrink-hook");
  assert.ok(existsSync(join(caveDir, "hooks", "claude.json")), "an install marker must be written");
});

// Installing twice must not duplicate the caveman hook, and must never disturb a
// hook the user already had.
test("hooks install is idempotent and preserves the user's own hooks", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  mkdirSync(join(home, ".claude"), { recursive: true });
  writeFileSync(
    join(home, ".claude", "settings.json"),
    JSON.stringify({ hooks: { PreToolUse: [{ matcher: "Bash", hooks: [{ type: "command", command: "my-own-linter.sh" }] }] } }, null, 2),
  );

  await runCli(["hooks", "install", "claude"], env);
  await runCli(["hooks", "install", "claude"], env);

  const settings = JSON.parse(readFileSync(join(home, ".claude", "settings.json"), "utf8"));
  const pre = settings.hooks.PreToolUse;
  const caveEntries = pre.filter((e) => e.hooks.some((h) => (h.command || "").includes("shrink-hook")));
  assert.equal(caveEntries.length, 1, "exactly one caveman hook after two installs");
  assert.ok(pre.some((e) => e.hooks.some((h) => h.command === "my-own-linter.sh")), "the user's own hook must survive");
});

test("hooks uninstall removes the caveman hook, keeps the user's, and drops the marker", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".claude"), { recursive: true });
  writeFileSync(
    join(home, ".claude", "settings.json"),
    JSON.stringify({ hooks: { PreToolUse: [{ matcher: "Bash", hooks: [{ type: "command", command: "my-own-linter.sh" }] }] } }, null, 2),
  );

  await runCli(["hooks", "install", "claude"], env);
  await runCli(["hooks", "uninstall", "claude"], env);

  const settings = JSON.parse(readFileSync(join(home, ".claude", "settings.json"), "utf8"));
  const pre = settings.hooks.PreToolUse;
  assert.ok(!pre.some((e) => e.hooks.some((h) => (h.command || "").includes("shrink-hook"))), "caveman hook must be gone");
  assert.ok(pre.some((e) => e.hooks.some((h) => h.command === "my-own-linter.sh")), "the user's own hook must remain");
  assert.ok(!existsSync(join(caveDir, "hooks", "claude.json")), "the marker must be removed");
});

test("wrap claude loads a temp native pack and leaves user config untouched", async () => {
  const binDir = mkdtempSync(join(tmpdir(), "cave-claude-"));
  writeFileSync(join(binDir, "claude"), `#!/usr/bin/env node
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
const args = process.argv.slice(2);
const i = args.indexOf("--plugin-dir");
const dir = i >= 0 ? args[i + 1] : "";
writeFileSync(process.env.CAVE_WRAP_DUMP, JSON.stringify({ args, dir, hooks: dir ? readFileSync(join(dir, "hooks", "hooks.json"), "utf8") : "" }));
`, { mode: 0o755 });

  // default loadout → installs
  const home1 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cave1 = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const dump1 = join(home1, "dump.json");
  const env1 = { ...process.env, NO_COLOR: "1", HOME: home1, CAVEMAN_HOME: cave1, CAVE_WRAP_DUMP: dump1, PATH: `${binDir}:${process.env.PATH}` };
  delete env1.CAVE_GATEWAY_URL;
  writeWrapConfig(home1, { proxy: false, browse: false });
  const w1 = await runCli(["wrap", "claude"], env1);
  assert.equal(w1.code, 0, w1.stderr);
  assert.ok(!existsSync(join(home1, ".claude", "settings.json")));
  assert.ok(!existsSync(join(cave1, "hooks", "claude.json")));
  const first = JSON.parse(readFileSync(dump1, "utf8"));
  assert.match(first.hooks, /native-hook claude/);
  assert.match(first.hooks, /shrink-hook/);
  assert.ok(!existsSync(first.dir), "temp plugin must be cleaned after child exit");

  // shrink:false → does NOT install
  const home2 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cave2 = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const dump2 = join(home2, "dump.json");
  const env2 = { ...process.env, NO_COLOR: "1", HOME: home2, CAVEMAN_HOME: cave2, CAVE_WRAP_DUMP: dump2, PATH: `${binDir}:${process.env.PATH}` };
  delete env2.CAVE_GATEWAY_URL;
  writeWrapConfig(home2, { proxy: false, shrink: false, browse: false });
  const w2 = await runCli(["wrap", "claude"], env2);
  assert.equal(w2.code, 0, w2.stderr);
  const second = JSON.parse(readFileSync(dump2, "utf8"));
  assert.match(second.hooks, /native-hook claude/);
  assert.doesNotMatch(second.hooks, /shrink-hook/);
});

// ── Codex: current native PreToolUse hard tier ───────────────────────────────
test("hooks install codex adds one native PreToolUse rewrite and preserves user hooks", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".codex"), { recursive: true });
  writeFileSync(join(home, ".codex", "hooks.json"), JSON.stringify({ hooks: { PreToolUse: [
    { hooks: [{ type: "command", command: "my-hook" }] },
  ] } }, null, 2));

  await runCli(["hooks", "install", "codex"], env);
  await runCli(["hooks", "install", "codex"], env);

  const hooks = JSON.parse(readFileSync(join(home, ".codex", "hooks.json"), "utf8")).hooks.PreToolUse;
  assert.equal(hooks.filter((entry) => JSON.stringify(entry).includes("shrink-hook")).length, 1);
  assert.ok(hooks.some((entry) => JSON.stringify(entry).includes("my-hook")));
  assert.ok(existsSync(join(caveDir, "hooks", "codex.json")), "a marker must be written");
});

test("hooks install codex creates hooks.json when absent", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  const out = await runCli(["hooks", "install", "codex"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.ok(existsSync(join(home, ".codex", "hooks.json")));
});

test("hooks uninstall codex removes only Caveman hook", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".codex"), { recursive: true });
  writeFileSync(join(home, ".codex", "hooks.json"), JSON.stringify({ hooks: { PreToolUse: [
    { hooks: [{ type: "command", command: "keep-me" }] },
  ] } }, null, 2));

  await runCli(["hooks", "install", "codex"], env);
  await runCli(["hooks", "uninstall", "codex"], env);

  const hooks = JSON.parse(readFileSync(join(home, ".codex", "hooks.json"), "utf8")).hooks.PreToolUse;
  assert.ok(hooks.some((entry) => JSON.stringify(entry).includes("keep-me")));
  assert.ok(!hooks.some((entry) => JSON.stringify(entry).includes("shrink-hook")));
  assert.ok(!existsSync(join(caveDir, "hooks", "codex.json")), "the marker must be removed");
});

// ── opencode: plugin (hard tier, real RTK parity) ────────────────────────────
// opencode CAN deterministically rewrite a command (a plugin's tool.execute.before
// mutates output.args.command), so caveman ships a plugin file.
test("hooks install opencode writes a plugin, uninstall removes it", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };

  await runCli(["hooks", "install", "opencode"], env);
  const plugin = join(home, ".config", "opencode", "plugins", "caveman-shrink.js");
  assert.ok(existsSync(plugin), "the opencode plugin must be written");
  const src = readFileSync(plugin, "utf8");
  assert.match(src, /tool\.execute\.before/, "the plugin must hook tool.execute.before");
  assert.match(src, /caveman:shrink-plugin/, "the plugin must carry our marker");
  assert.ok(existsSync(join(caveDir, "hooks", "opencode.json")), "a marker must be written");

  await runCli(["hooks", "uninstall", "opencode"], env);
  assert.ok(!existsSync(plugin), "uninstall must remove the plugin");
  assert.ok(!existsSync(join(caveDir, "hooks", "opencode.json")), "uninstall must drop the marker");
});

test("hooks uninstall opencode leaves a plugin it does not own", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  const plugin = join(home, ".config", "opencode", "plugins", "caveman-shrink.js");
  mkdirSync(dirname(plugin), { recursive: true });
  writeFileSync(plugin, "// someone else's plugin\n");
  await runCli(["hooks", "uninstall", "opencode"], env);
  assert.ok(existsSync(plugin), "a non-caveman plugin must be left untouched");
});

// ── Hermes: Python plugin under $HERMES_HOME/plugins/caveman_shrink ──────────
test("hooks install hermes writes marker-fenced plugin files, idempotent, and uninstall removes cleanly", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const hermesHome = mkdtempSync(join(tmpdir(), "cave-hermes-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, HERMES_HOME: hermesHome, CAVEMAN_HOME: caveDir };

  await runCli(["hooks", "install", "hermes"], env);
  await runCli(["hooks", "install", "hermes"], env);

  const pluginDir = join(hermesHome, "plugins", "caveman_shrink");
  const initPath = join(pluginDir, "__init__.py");
  const manifestPath = join(pluginDir, "plugin.yaml");
  assert.ok(existsSync(initPath), "Hermes plugin __init__.py must be written");
  assert.ok(existsSync(manifestPath), "Hermes plugin manifest must be written");
  const init = readFileSync(initPath, "utf8");
  const manifest = readFileSync(manifestPath, "utf8");
  assert.match(init, /# >>> caveman:shrink-plugin/, "plugin code must carry begin marker");
  assert.match(init, /# <<< caveman:shrink-plugin/, "plugin code must carry end marker");
  assert.match(init, /register_hook\("transform_terminal_output"/, "plugin must register Hermes terminal-output hook");
  assert.match(init, /"shrink", "--stdin"/, "plugin must pipe output through caveman shrink --stdin");
  assert.match(manifest, /# >>> caveman:shrink-plugin/, "manifest must carry marker");
  assert.match(manifest, /provides_hooks:\n  - transform_terminal_output/, "manifest must declare the hook");

  const configPath = join(hermesHome, "config.yaml");
  const config = readFileSync(configPath, "utf8");
  assert.equal((config.match(/caveman:hermes-plugin-enable/g) || []).length, 2, "enable marker begin+end only once after two installs");
  assert.equal((config.match(/- caveman_shrink/g) || []).length, 1, "plugins.enabled entry must be idempotent");
  assert.ok(existsSync(join(caveDir, "hooks", "hermes.json")), "hook marker must be written");

  await runCli(["hooks", "uninstall", "hermes"], env);
  assert.ok(!existsSync(pluginDir), "uninstall must remove the owned Hermes plugin directory");
  const after = existsSync(configPath) ? readFileSync(configPath, "utf8") : "";
  assert.ok(!after.includes("caveman:hermes-plugin-enable"), "uninstall must remove the enable marker block");
  assert.ok(!existsSync(join(caveDir, "hooks", "hermes.json")), "uninstall must drop the hook marker");
});

// ── OpenClaw: plugin (hard tier, tool_result_persist) ───────────────────────
test("hooks install openclaw writes plugin package and config; uninstall removes only caveman entries", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const stateDir = mkdtempSync(join(tmpdir(), "openclaw-state-"));
  const configPath = join(stateDir, "openclaw.json");
  writeFileSync(configPath, JSON.stringify({
    theme: "midnight",
    plugins: {
      load: { paths: ["/existing/plugin"] },
      entries: { other: { enabled: true } },
      allow: ["other"],
    },
  }, null, 2));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir, OPENCLAW_STATE_DIR: stateDir };
  delete env.OPENCLAW_CONFIG_PATH;

  const installed = await runCli(["hooks", "install", "openclaw"], env);
  assert.equal(installed.code, 0, installed.stderr);
  const pluginDir = join(caveDir, "openclaw", "plugins", "caveman-shrink");
  assert.ok(existsSync(join(pluginDir, "openclaw.plugin.json")), "native plugin manifest must be written");
  assert.ok(existsSync(join(pluginDir, "package.json")), "native plugin package metadata must be written");
  const source = readFileSync(join(pluginDir, "index.mjs"), "utf8");
  assert.match(source, /tool_result_persist/, "OpenClaw plugin must use the verified persist hook");
  assert.match(source, /caveman:openclaw-shrink-plugin/, "plugin source must carry the Caveman ownership marker");

  const cfg = JSON.parse(readFileSync(configPath, "utf8"));
  assert.equal(cfg.theme, "midnight", "unrelated config must be preserved");
  assert.deepEqual(cfg.plugins.load.paths, ["/existing/plugin", pluginDir]);
  assert.equal(cfg.plugins.entries.other.enabled, true, "existing plugin entries must be preserved");
  assert.equal(cfg.plugins.entries["caveman-shrink"].enabled, true);
  assert.equal(cfg.plugins.entries["caveman-shrink"].hooks.allowConversationAccess, true);
  assert.deepEqual(cfg.plugins.allow, ["other", "caveman-shrink"], "restrictive allowlists must include the new plugin");
  assert.ok(existsSync(join(caveDir, "hooks", "openclaw.json")), "a persistent hook marker must be written");

  const uninstalled = await runCli(["hooks", "uninstall", "openclaw"], env);
  assert.equal(uninstalled.code, 0, uninstalled.stderr);
  const after = JSON.parse(readFileSync(configPath, "utf8"));
  assert.equal(after.theme, "midnight");
  assert.deepEqual(after.plugins.load.paths, ["/existing/plugin"]);
  assert.equal(after.plugins.entries.other.enabled, true);
  assert.equal(after.plugins.entries["caveman-shrink"], undefined);
  assert.deepEqual(after.plugins.allow, ["other"]);
  assert.ok(!existsSync(pluginDir), "owned plugin dir must be removed");
  assert.ok(!existsSync(join(caveDir, "hooks", "openclaw.json")), "uninstall must drop the marker");
});

// ── aider: manual-only (no installable surface) ──────────────────────────────
test("hooks install aider is an honest no-op (no marker, points at the manual path)", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  const out = await runCli(["hooks", "install", "aider"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.ok(!existsSync(join(caveDir, "hooks", "aider.json")), "a manual-only agent gets no marker");
  assert.match(out.stderr, /caveman shrink -- <cmd>/, "must point at the manual shrink path");
});

test("wrap codex never mutates real hooks or instructions", async () => {
  const binDir = mkdtempSync(join(tmpdir(), "cave-codex-"));
  writeFileSync(join(binDir, "codex"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });

  const home1 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cave1 = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env1 = { ...process.env, NO_COLOR: "1", HOME: home1, CAVEMAN_HOME: cave1, PATH: `${binDir}:${process.env.PATH}` };
  delete env1.CAVE_GATEWAY_URL;
  writeWrapConfig(home1, { proxy: false, browse: false });
  const w1 = await runCli(["wrap", "codex"], env1);
  assert.equal(w1.code, 0, w1.stderr);
  assert.ok(!existsSync(join(home1, ".codex", "hooks.json")));
  assert.ok(!existsSync(join(home1, ".codex", "AGENTS.md")));
  assert.ok(!existsSync(join(cave1, "hooks", "codex.json")));

  // shrink:false → does NOT install
  const home2 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cave2 = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env2 = { ...process.env, NO_COLOR: "1", HOME: home2, CAVEMAN_HOME: cave2, PATH: `${binDir}:${process.env.PATH}` };
  delete env2.CAVE_GATEWAY_URL;
  writeWrapConfig(home2, { proxy: false, shrink: false, browse: false });
  const w2 = await runCli(["wrap", "codex"], env2);
  assert.equal(w2.code, 0, w2.stderr);
  assert.ok(!existsSync(join(home2, ".codex", "hooks.json")));
});

// ── byte-safe: a non-object ~/.claude/settings.json must be left untouched ─────
// (honesty: byte-safe — on a parse/shape problem we pass through, never corrupt.)
for (const original of ["[]", "\"a string\"", "42", "{ not json"]) {
  test(`hooks install claude leaves a non-object settings.json byte-for-byte unchanged: ${original}`, async () => {
    const home = mkdtempSync(join(tmpdir(), "cave-home-"));
    const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
    mkdirSync(join(home, ".claude"), { recursive: true });
    const sp = join(home, ".claude", "settings.json");
    writeFileSync(sp, original);
    const out = await runCli(["hooks", "install", "claude"], env);
    // Byte-safe AND loud (closure B1): the file is untouched, and the failed
    // install exits non-zero instead of printing a warning behind exit 0.
    assert.notEqual(out.code, 0, "a failed install must exit non-zero");
    assert.equal(readFileSync(sp, "utf8"), original, "a non-object settings file must not be modified");
    assert.ok(!readFileSync(sp, "utf8").includes("shrink-hook"), "no hook must be injected into a non-object settings file");
  });
}

// ── the opencode plugin actually rewrites at runtime (not just grep-asserted) ──
// Dynamic-import the generated plugin and drive its tool.execute.before hook. In the
// test env `caveman` is not on PATH, so the baked invocation resolves to this very
// built CLI (node dist/index.js shrink-hook) — the hook genuinely round-trips through
// the real shrink decision.
test("the generated opencode plugin rewrites a noisy bash command and is byte-safe otherwise", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  await runCli(["hooks", "install", "opencode"], env);
  const plugin = join(home, ".config", "opencode", "plugins", "caveman-shrink.js");
  assert.ok(existsSync(plugin), "plugin must exist");

  const mod = await import(pathToFileURL(plugin).href);
  const hooks = await mod.CavemanShrink();
  const before = hooks["tool.execute.before"];

  // (a) a noisy, allowlisted command is rewritten to run through `caveman shrink`
  const o1 = { args: { command: "git status" } };
  await before({ tool: "bash" }, o1);
  assert.match(o1.args.command, /shrink -- git status$/, "git status must be rewritten through caveman shrink");

  // (b) a non-bash tool is never touched
  const o2 = { args: { command: "git status" } };
  await before({ tool: "read" }, o2);
  assert.equal(o2.args.command, "git status", "non-bash tools must be left unchanged");

  // (c) byte-safe: a command shrink-hook declines (pipe → meaning would change) passes through
  const o3 = { args: { command: "git status | head" } };
  await before({ tool: "bash" }, o3);
  assert.equal(o3.args.command, "git status | head", "a non-eligible command must be left unchanged");

  // (d) byte-safe: an empty/garbage command is left as-is
  const o4 = { args: { command: "" } };
  await before({ tool: "bash" }, o4);
  assert.equal(o4.args.command, "", "an empty command must be left unchanged");
});

// ── Gemini: shrink-hook emits Gemini's override shape (not Claude's) ──────────
// Gemini's run_shell_command uses hookSpecificOutput.tool_input (snake_case, merge),
// NOT Claude's updatedInput/hookEventName — emitting the wrong shape is a silent no-op.
test("shrink-hook emits Gemini's tool_input override for run_shell_command", async () => {
  const out = await runHook({ tool_name: "run_shell_command", tool_input: { command: "git status" } });
  assert.equal(out.code, 0, out.stderr);
  const o = JSON.parse(out.stdout);
  assert.ok(o.hookSpecificOutput.tool_input, "must use Gemini's tool_input shape");
  assert.match(o.hookSpecificOutput.tool_input.command, /shrink -- git status$/);
  assert.equal(o.hookSpecificOutput.hookEventName, undefined, "Gemini output must NOT carry Claude's hookEventName");
  assert.equal(o.hookSpecificOutput.updatedInput, undefined, "Gemini output must NOT use Claude's updatedInput");
});

test("shrink-hook passes through a non-eligible Gemini command (pipe)", async () => {
  const out = await runHook({ tool_name: "run_shell_command", tool_input: { command: "git status | head" } });
  assert.equal(out.code, 0);
  assert.equal(out.stdout, "", "a piped command must not be rewritten");
});

// ── Gemini: hard BeforeTool rewrite hook in ~/.gemini/settings.json ───────────
test("hooks install gemini registers a run_shell_command BeforeTool hook, idempotent, preserving user hooks", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".gemini"), { recursive: true });
  writeFileSync(
    join(home, ".gemini", "settings.json"),
    JSON.stringify({ hooks: { BeforeTool: [{ matcher: "run_shell_command", hooks: [{ type: "command", command: "my-own.sh" }] }] } }, null, 2),
  );

  await runCli(["hooks", "install", "gemini"], env);
  await runCli(["hooks", "install", "gemini"], env); // idempotent

  const s = JSON.parse(readFileSync(join(home, ".gemini", "settings.json"), "utf8"));
  const bt = s.hooks.BeforeTool;
  assert.ok(Array.isArray(bt), "BeforeTool must be an array");
  const cave = bt.filter((e) => e.hooks.some((h) => (h.command || "").includes("shrink-hook")));
  assert.equal(cave.length, 1, "exactly one caveman hook after two installs");
  assert.equal(cave[0].matcher, "run_shell_command", "must match the gemini shell tool");
  assert.ok(bt.some((e) => e.hooks.some((h) => h.command === "my-own.sh")), "the user's own hook must survive");
  assert.ok(existsSync(join(caveDir, "hooks", "gemini.json")), "a marker must be written");
});

test("hooks uninstall gemini removes the caveman hook, keeps the user's, drops the marker", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".gemini"), { recursive: true });
  writeFileSync(
    join(home, ".gemini", "settings.json"),
    JSON.stringify({ hooks: { BeforeTool: [{ matcher: "run_shell_command", hooks: [{ type: "command", command: "my-own.sh" }] }] } }, null, 2),
  );

  await runCli(["hooks", "install", "gemini"], env);
  await runCli(["hooks", "uninstall", "gemini"], env);

  const s = JSON.parse(readFileSync(join(home, ".gemini", "settings.json"), "utf8"));
  assert.ok(!s.hooks.BeforeTool.some((e) => e.hooks.some((h) => (h.command || "").includes("shrink-hook"))), "caveman hook gone");
  assert.ok(s.hooks.BeforeTool.some((e) => e.hooks.some((h) => h.command === "my-own.sh")), "user hook remains");
  assert.ok(!existsSync(join(caveDir, "hooks", "gemini.json")), "marker removed");
});

test("wrap gemini is zero-commit until a temp native extension exists", async () => {
  const binDir = mkdtempSync(join(tmpdir(), "cave-gemini-"));
  writeFileSync(join(binDir, "gemini"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });

  const home1 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const cave1 = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env1 = { ...process.env, NO_COLOR: "1", HOME: home1, CAVEMAN_HOME: cave1, PATH: `${binDir}:${process.env.PATH}` };
  delete env1.CAVE_GATEWAY_URL;
  writeWrapConfig(home1, { proxy: false, browse: false });
  const w1 = await runCli(["wrap", "gemini"], env1);
  assert.equal(w1.code, 0, w1.stderr);
  assert.ok(!existsSync(join(home1, ".gemini", "settings.json")));
  assert.ok(!existsSync(join(cave1, "hooks", "gemini.json")));

  const home2 = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env2 = { ...process.env, NO_COLOR: "1", HOME: home2, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")), PATH: `${binDir}:${process.env.PATH}` };
  delete env2.CAVE_GATEWAY_URL;
  writeWrapConfig(home2, { proxy: false, shrink: false, browse: false });
  const w2 = await runCli(["wrap", "gemini"], env2);
  assert.equal(w2.code, 0, w2.stderr);
  assert.ok(!existsSync(join(home2, ".gemini", "settings.json")), "shrink:false must not install the hook");
});

test("hooks install labels Claude and current Codex as hard rewrites", async () => {
  const mk = () => ({ ...process.env, NO_COLOR: "1", HOME: mkdtempSync(join(tmpdir(), "cave-home-")), CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) });

  const hard = await runCli(["hooks", "install", "claude"], mk());
  assert.match(hard.stderr, /rewrite hook installed/, "claude (hard) must be called a rewrite hook");
  assert.doesNotMatch(hard.stderr, /model nudge/, "a hard install must not be called a nudge");
  assert.match(hard.stderr, /auto-shrunk/, "hard footer claims auto-shrink");

  const codex = await runCli(["hooks", "install", "codex"], mk());
  assert.match(codex.stderr, /rewrite hook installed/);
  assert.match(codex.stderr, /auto-shrunk/);
});

// ── hooks install with no agent: fan out to every hookable agent on PATH ───────
test("hooks install (no agent) installs for every hookable agent detected on PATH", async () => {
  const binDir = mkdtempSync(join(tmpdir(), "cave-fanout-"));
  symlinkSync(process.execPath, join(binDir, "node")); // so the spawned CLI can find node
  for (const a of ["claude", "codex", "opencode", "aider"]) writeFileSync(join(binDir, a), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir, PATH: binDir };
  delete env.CAVE_GATEWAY_URL;

  const out = await runCli(["hooks", "install"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.ok(existsSync(join(home, ".claude", "settings.json")), "claude (hard) hook installed");
  assert.ok(existsSync(join(home, ".codex", "hooks.json")), "codex native hook installed");
  assert.ok(existsSync(join(home, ".config", "opencode", "plugins", "caveman-shrink.js")), "opencode (hard) plugin installed");
  for (const id of ["claude", "codex", "opencode"]) assert.ok(existsSync(join(caveDir, "hooks", `${id}.json`)), `${id} marker written`);
  assert.ok(!existsSync(join(caveDir, "hooks", "aider.json")), "aider (no surface) is skipped — no marker");
});

test("hooks install (no agent) with no hookable agent on PATH fails closed (exit 1)", async () => {
  const binDir = mkdtempSync(join(tmpdir(), "cave-empty-"));
  symlinkSync(process.execPath, join(binDir, "node"));
  const env = { ...process.env, NO_COLOR: "1", HOME: mkdtempSync(join(tmpdir(), "cave-home-")), CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")), PATH: binDir };
  delete env.CAVE_GATEWAY_URL;
  const out = await runCli(["hooks", "install"], env);
  assert.equal(out.code, 1, "must fail closed when no hookable agent is present");
  assert.match(out.stderr, /no hookable agents detected on PATH/);
});

test("directive install then uninstall restores user instructions byte-for-byte", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  mkdirSync(join(home, ".codex"), { recursive: true });
  const original = "# Title\n\n\nSection with intentional blank lines above.\n\n- a\n- b\n";
  const p = join(home, ".codex", "AGENTS.md");
  writeFileSync(p, original);

  await runCli(["hooks", "install", "--directive", "exploration-offload-directive", "codex"], env);
  assert.notEqual(readFileSync(p, "utf8"), original, "install must add directive");
  await runCli(["hooks", "uninstall", "--directive", "exploration-offload-directive", "codex"], env);
  assert.equal(readFileSync(p, "utf8"), original, "uninstall must restore the file exactly, internal blank lines included");
});

// ── Wrap directives (AUTOPILOT_SPEC §8.4/§7.3, slice D) ─────────────────────
// A directive is a SECOND marker namespace on the same instructions-file
// mechanism: it must coexist with the shrink note, each removal must touch only
// its own block, unknown ids must fail closed, and the install must announce
// itself and print the undo command.
test("hooks directive coexists with native Codex hook and removes only its own block", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  mkdirSync(join(home, ".codex"), { recursive: true });
  writeFileSync(join(home, ".codex", "AGENTS.md"), "# My rules\n");

  await runCli(["hooks", "install", "codex"], env);
  const out = await runCli(["hooks", "install", "--directive", "exploration-offload-directive", "codex"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stderr, /directive 'exploration-offload-directive' added/, "install must announce itself");
  assert.match(out.stderr, /undo: caveman tools hooks uninstall --directive exploration-offload-directive/, "install must print the undo command");
  assert.match(out.stderr, /tokens/, "the announce line must state the tokens-only claim");

  let md = readFileSync(join(home, ".codex", "AGENTS.md"), "utf8");
  assert.ok(md.includes("caveman:directive:exploration-offload-directive"), "the directive block must be present");
  assert.match(readFileSync(join(home, ".codex", "hooks.json"), "utf8"), /shrink-hook/);
  assert.ok(existsSync(join(caveDir, "hooks", "directives", "exploration-offload-directive.json")), "a directive marker must be written");

  // Idempotent: a second install keeps exactly one block.
  await runCli(["hooks", "install", "--directive", "exploration-offload-directive", "codex"], env);
  md = readFileSync(join(home, ".codex", "AGENTS.md"), "utf8");
  assert.equal((md.match(/caveman:directive:exploration-offload-directive \(managed/g) || []).length, 1, "exactly one directive block after two installs");

  // Removal touches ONLY the directive's own block.
  await runCli(["hooks", "uninstall", "--directive", "exploration-offload-directive", "codex"], env);
  md = readFileSync(join(home, ".codex", "AGENTS.md"), "utf8");
  assert.ok(md.includes("# My rules"), "user content must survive");
  assert.ok(!md.includes("caveman:directive:exploration-offload-directive"), "the directive block must be gone");
  assert.match(readFileSync(join(home, ".codex", "hooks.json"), "utf8"), /shrink-hook/);
  assert.ok(!existsSync(join(caveDir, "hooks", "directives", "exploration-offload-directive.json")), "the directive marker must be removed");
});

test("two directives coexist and each uninstall removes only its own block", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  await runCli(["hooks", "install", "--directive", "exploration-offload-directive", "codex"], env);
  await runCli(["hooks", "install", "--directive", "deferred-tool-loading", "codex"], env);
  let md = readFileSync(join(home, ".codex", "AGENTS.md"), "utf8");
  assert.ok(md.includes("caveman:directive:exploration-offload-directive"));
  assert.ok(md.includes("caveman:directive:deferred-tool-loading"));
  await runCli(["hooks", "uninstall", "--directive", "deferred-tool-loading", "codex"], env);
  md = readFileSync(join(home, ".codex", "AGENTS.md"), "utf8");
  assert.ok(md.includes("caveman:directive:exploration-offload-directive"), "the other directive must survive");
  assert.ok(!md.includes("caveman:directive:deferred-tool-loading"));
});

test("unknown directive ids fail closed", async () => {
  const env = { ...process.env, NO_COLOR: "1", HOME: mkdtempSync(join(tmpdir(), "cave-home-")), CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  const out = await runCli(["hooks", "install", "--directive", "no-such-directive", "codex"], env);
  assert.notEqual(out.code, 0, "unknown directive must be rejected");
  assert.match(out.stderr, /unknown directive/);
});

test("directives refuse agents without an instructions-file surface", async () => {
  const env = { ...process.env, NO_COLOR: "1", HOME: mkdtempSync(join(tmpdir(), "cave-home-")), CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  const out = await runCli(["hooks", "install", "--directive", "exploration-offload-directive", "claude"], env);
  assert.notEqual(out.code, 0, "an agent without an instruction-note surface must be refused");
  assert.match(out.stderr, /no instructions-file surface/);
});

// ── managed-block corruption + arg-form hardening (review C8) ────────────────

// A corrupted block (begin marker without its end marker) must fail LOUDLY and
// touch nothing. The old behavior stripped from the begin marker to
// end-of-file — deleting sibling managed blocks and the user's own content —
// and printed a success mark.
test("uninstall on a corrupted block fails loudly and modifies nothing", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  mkdirSync(join(home, ".codex"), { recursive: true });
  const p = join(home, ".codex", "AGENTS.md");
  const corrupted = [
    "# My rules",
    "",
    "<!-- caveman:directive:exploration-offload-directive (managed by `caveman hooks`) -->",
    "Directive text whose END MARKER was deleted by hand.",
    "",
    "<!-- caveman:shrink-hook (managed by `caveman hooks`) -->",
    "sibling block that must survive",
    "<!-- /caveman:shrink-hook -->",
    "",
    "## User content below the seam — must survive",
    "",
  ].join("\n");
  writeFileSync(p, corrupted);

  const out = await runCli(["hooks", "uninstall", "--directive", "exploration-offload-directive", "codex"], env);
  assert.notEqual(out.code, 0, "a corrupted block must exit non-zero");
  assert.match(out.stderr, /corrupted/i, "must say the block is corrupted");
  assert.match(out.stderr, /Nothing was modified/, "must state that nothing changed");
  assert.equal(readFileSync(p, "utf8"), corrupted, "the file must be byte-identical — no strip-to-EOF repair");

  // Install on the same corrupted file must also refuse to 'repair'.
  const out2 = await runCli(["hooks", "install", "--directive", "exploration-offload-directive", "codex"], env);
  assert.notEqual(out2.code, 0, "install on a corrupted block must exit non-zero");
  assert.equal(readFileSync(p, "utf8"), corrupted, "install must not touch a corrupted file");
});

// INTERLEAVED markers (closure B1): a directive's end marker sitting past a
// sibling block's begin marker means a strip through it would silently delete
// the sibling's begin marker and body. Both verbs must refuse and touch nothing.
test("uninstall on interleaved managed blocks refuses and modifies nothing", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  mkdirSync(join(home, ".codex"), { recursive: true });
  const p = join(home, ".codex", "AGENTS.md");
  const interleaved = [
    "<!-- caveman:directive:exploration-offload-directive (managed by `caveman hooks`) -->",
    "A body",
    "<!-- caveman:shrink-hook (managed by `caveman hooks`) -->",
    "B body",
    "<!-- /caveman:directive:exploration-offload-directive -->",
    "more",
    "<!-- /caveman:shrink-hook -->",
    "",
  ].join("\n");
  writeFileSync(p, interleaved);
  const out = await runCli(["hooks", "uninstall", "--directive", "exploration-offload-directive", "codex"], env);
  assert.notEqual(out.code, 0, "interleaved markers must exit non-zero");
  assert.match(out.stderr, /corrupted/i);
  assert.equal(readFileSync(p, "utf8"), interleaved, "the file must be byte-identical — the sibling begin marker and body must survive");
});

// A stray END marker with no begin: install already refuses this state, so a
// remove that reports "absent" and exits 0 would lock the user out of both
// verbs permanently (closure B1). Remove must refuse too.
test("uninstall on a stray end marker with no begin refuses", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  mkdirSync(join(home, ".codex"), { recursive: true });
  const p = join(home, ".codex", "AGENTS.md");
  const stray = [
    "# rules",
    "<!-- /caveman:directive:deferred-tool-loading -->",
    "",
  ].join("\n");
  writeFileSync(p, stray);
  const out = await runCli(["hooks", "uninstall", "--directive", "deferred-tool-loading", "codex"], env);
  assert.notEqual(out.code, 0, "a stray end marker must exit non-zero");
  assert.match(out.stderr, /corrupted/i);
  assert.equal(readFileSync(p, "utf8"), stray, "the file must be untouched");
});

// Duplicated blocks (e.g. from a merge) must ALL be removed — a survivor
// behind a green check would keep nudging the model invisibly.
test("uninstall removes every duplicated occurrence of its block", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  await runCli(["hooks", "install", "--directive", "deferred-tool-loading", "codex"], env);
  const p = join(home, ".codex", "AGENTS.md");
  const once = readFileSync(p, "utf8");
  writeFileSync(p, once + "\n" + once); // simulate a bad merge duplicating the block
  const out = await runCli(["hooks", "uninstall", "--directive", "deferred-tool-loading", "codex"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.ok(!readFileSync(p, "utf8").includes("caveman:directive:deferred-tool-loading"), "every occurrence must be removed");
});

// --directive=<id> (equals form) must work — it used to be silently ignored,
// installing the shrink note and exiting 0 while the user believed a directive
// was installed. Unknown --flags are rejected.
test("--directive=<id> equals form works and unknown flags are rejected", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveDir };
  const out = await runCli(["hooks", "install", "--directive=exploration-offload-directive", "codex"], env);
  assert.equal(out.code, 0, out.stderr);
  const md = readFileSync(join(home, ".codex", "AGENTS.md"), "utf8");
  assert.ok(md.includes("caveman:directive:exploration-offload-directive"), "the equals form must install the directive");
  assert.ok(!md.includes("caveman:shrink-hook"), "the equals form must not fall through to the shrink note");

  const bad = await runCli(["hooks", "install", "--not-a-flag", "codex"], env);
  assert.notEqual(bad.code, 0, "unknown flags must be rejected");
  assert.match(bad.stderr, /unknown option '--not-a-flag'/);
});

// Prototype keys must not pass the directive gate (Object.hasOwn, not
// truthiness): DIRECTIVES['constructor'] is a real (inherited) value.
test("prototype-key directive ids fail closed", async () => {
  const env = { ...process.env, NO_COLOR: "1", HOME: mkdtempSync(join(tmpdir(), "cave-home-")), CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-dot-")) };
  for (const id of ["constructor", "__proto__", "toString"]) {
    const out = await runCli(["hooks", "install", "--directive", id, "codex"], env);
    assert.notEqual(out.code, 0, `prototype key '${id}' must be rejected`);
    assert.match(out.stderr, /unknown directive/);
  }
});
