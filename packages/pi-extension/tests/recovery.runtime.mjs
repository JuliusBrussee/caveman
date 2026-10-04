import assert from "node:assert/strict";
import test from "node:test";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const stub = join(here, "fixtures", "stub-caveman-mcp.mjs");
// pathToFileURL, not a bare path: dynamic import() of an absolute Windows
// path throws ERR_UNSUPPORTED_ESM_URL_SCHEME (the drive letter reads as a URL
// scheme).
const { RecoveryClient, shrinkToolResult, MAX_TOOL_OUTPUT_BYTES, MAX_OUTPUT_REPLACEMENT_BYTES } = await import(pathToFileURL(join(here, "..", "dist", "testable.mjs")).href);

const KNOWN_HANDLE = "ccr_0123456789abcdef0123456789abcdef";
const KNOWN_BYTES = "exact original bytes\nline two éø bytes";

import { chmodSync, existsSync, mkdtempSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";

// dispose() gives a child 500ms to leave on stdin EOF before SIGTERM, so poll
// rather than assert instantly. A pid still answering signal 0 is an orphan
// still holding ccr.db.
async function assertAllDead(spawnLog) {
  const pids = existsSync(spawnLog) ? readFileSync(spawnLog, "utf8").trim().split("\n").filter(Boolean).map(Number) : [];
  const alive = () => pids.filter((pid) => { try { process.kill(pid, 0); return true; } catch { return false; } });
  for (let attempt = 0; attempt < 30 && alive().length > 0; attempt++) {
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  assert.deepEqual(alive(), [], "dispose() left caveman-mcp children alive");
}

// RecoveryClient spawns its binary argv-less, so each test materializes a tiny
// shell shim that execs the node stub (optionally with a failure-mode env).

// A pure launcher for the node stub — no env baked in. On Windows the launcher
// is BYPASSED: portableInvocation reads the .cmd, extracts the node target and
// runs it directly, so anything the shim tried to `set` would never execute.
// Behaviour switches therefore travel through process.env (inherited by the
// spawned child) and, for per-spawn behaviour, a flag file the stub owns.
function shim() {
  const dir = mkdtempSync(join(tmpdir(), "cave-pi-mcp-"));
  if (process.platform === "win32") {
    const path = join(dir, "caveman-mcp.cmd");
    writeFileSync(path, `@node  "${stub}" %*\r\n`);
    return { path, cleanup: () => rmSync(dir, { recursive: true, force: true }) };
  }
  const path = join(dir, "caveman-mcp");
  writeFileSync(path, `#!/bin/sh\nexec "${process.execPath}" "${stub}" "$@"\n`);
  chmodSync(path, 0o755);
  return { path, cleanup: () => rmSync(dir, { recursive: true, force: true }) };
}

// Set env for the duration of one test; the spawned stub inherits it.
function withEnv(vars, fn) {
  const prior = {};
  for (const [k, v] of Object.entries(vars)) { prior[k] = process.env[k]; process.env[k] = v; }
  const restore = () => {
    for (const [k, v] of Object.entries(prior)) {
      if (v === undefined) delete process.env[k]; else process.env[k] = v;
    }
  };
  return Promise.resolve(fn()).finally(restore);
}

test("probe failure (missing capability) makes the client unavailable", async () => {
  const { path, cleanup } = shim();
  await withEnv({ STUB_MCP_DROP_CAPABILITY: "1" }, async () => {
    const recovery = new RecoveryClient(path);
    assert.equal(await recovery.ensure(), false);
    const result = await recovery.retrieve(KNOWN_HANDLE, undefined, undefined);
    assert.equal(result.isError, true);
    assert.match(result.text, /cave_recovery_unavailable/);
    recovery.dispose();
  }).finally(cleanup);
});

test("retrieve returns exact bytes for a known handle and MCP error for unknown", async () => {
  const { path, cleanup } = shim();
  try {
    const recovery = new RecoveryClient(path);
    assert.equal(await recovery.ensure(), true);
    const hit = await recovery.retrieve(KNOWN_HANDLE, "everything", undefined);
    assert.equal(hit.isError, false);
    assert.equal(hit.text, KNOWN_BYTES);
    const miss = await recovery.retrieve("ccr_ffffffffffffffffffffffffffffffff", undefined, undefined);
    assert.equal(miss.isError, true);
    assert.match(miss.text, /cave_unknown_handle/);
    recovery.dispose();
  } finally {
    cleanup();
  }
});

test("child crash after init respawns on next retrieve and recovers the same handle", async () => {
  const flag = join(mkdtempSync(join(tmpdir(), "cave-pi-crash-")), "crashed-once");
  const { path, cleanup } = shim();
  await withEnv({ STUB_MCP_EXIT_ONCE_FLAG: flag }, async () => {
    const recovery = new RecoveryClient(path);
    // First bring-up crashes right after initialize (the stub creates the flag
    // and exits; the respawn finds it and stays up)...
    await recovery.ensure();
    await new Promise((resolve) => setTimeout(resolve, 200));
    // ...next retrieve must respawn a healthy child and still resolve the handle.
    const result = await recovery.retrieve(KNOWN_HANDLE, undefined, undefined);
    assert.equal(result.isError, false, result.text);
    assert.equal(result.text, KNOWN_BYTES);
    // Without this the test passes trivially when no crash ever happens: the
    // flag only exists because the first child died after initialize, so it is
    // the proof that the respawn path — not the happy path — was exercised.
    assert.ok(existsSync(flag), "crash-once never fired; the respawn path was not exercised");
    recovery.dispose();
  }).finally(() => {
    cleanup();
    rmSync(dirname(flag), { recursive: true, force: true });
  });
});

// A caveman-mcp that starts but never answers initialize (an orphan holding
// ccr.db) used to inherit the 30s call budget, and ensure() is awaited inside
// session_start — measured 30,304ms of frozen Pi startup. The handshake gets its
// own short budget in line with every other budget in this package (750ms–6s).
test("a caveman-mcp that never answers initialize degrades fast, not in 30s", async () => {
  const { path, cleanup } = shim();
  const log = join(mkdtempSync(join(tmpdir(), "cave-pi-hang-")), "spawns");
  await withEnv({ STUB_MCP_HANG_INIT: "1", STUB_MCP_SPAWN_LOG: log }, async () => {
    const recovery = new RecoveryClient(path);
    const started = Date.now();
    assert.equal(await recovery.ensure(), false);
    const elapsed = Date.now() - started;
    assert.ok(elapsed < 5000, `initialize handshake must degrade fast, took ${elapsed}ms`);
    const result = await recovery.retrieve(KNOWN_HANDLE, undefined, undefined);
    assert.equal(result.isError, true);
    assert.match(result.text, /cave_recovery_unavailable/);
    recovery.dispose();
    // The hung child holds ccr.db; a timed-out handshake must reap it, not leak it.
    await assertAllDead(log);
  }).finally(() => {
    cleanup();
    rmSync(dirname(log), { recursive: true, force: true });
  });
});

// probed/child were assigned only after an await, so N concurrent callers each
// ran a probe and a spawn and only the LAST child stayed tracked: 3 concurrent
// ensure() calls left 2 orphans alive after dispose(), each holding ccr.db.
test("concurrent ensure() calls share one spawn and leave no orphan", async () => {
  const { path, cleanup } = shim();
  const log = join(mkdtempSync(join(tmpdir(), "cave-pi-conc-")), "spawns");
  await withEnv({ STUB_MCP_SPAWN_LOG: log }, async () => {
    const recovery = new RecoveryClient(path);
    const results = await Promise.all([recovery.ensure(), recovery.ensure(), recovery.ensure()]);
    assert.deepEqual(results, [true, true, true]);
    const spawns = readFileSync(log, "utf8").trim().split("\n").filter(Boolean);
    assert.equal(spawns.length, 1, `concurrent ensure() spawned ${spawns.length} children: ${spawns}`);
    recovery.dispose();
    await assertAllDead(log);
  }).finally(() => {
    cleanup();
    rmSync(dirname(log), { recursive: true, force: true });
  });
});

test("abort signal cancels a pending retrieve without killing the child", async () => {
  const { path, cleanup } = shim();
  try {
    const recovery = new RecoveryClient(path);
    assert.equal(await recovery.ensure(), true);
    const controller = new AbortController();
    controller.abort();
    const result = await recovery.retrieve(KNOWN_HANDLE, undefined, controller.signal);
    assert.equal(result.isError, true);
    assert.match(result.text, /cancelled|cave_recovery_transport/);
    // Client still works after the cancellation.
    const ok = await recovery.retrieve(KNOWN_HANDLE, "again", undefined);
    assert.equal(ok.isError, false);
    recovery.dispose();
  } finally {
    cleanup();
  }
});

// Node emits 'error' ON the stdin stream, not only through the write callback,
// once a write is genuinely in flight (verified: with the pipe buffer full, both
// the callback and the callback-less form raise an uncaught EPIPE). An uncaught
// exception in a Pi extension kills the HOST — the one thing guardedBase promises
// never happens. Here a multi-megabyte query fills the pipe, the child is
// SIGKILLed underneath it, and the next write breaks. Without the stdin 'error'
// listener this test does not fail, it takes the runner down.
test("a broken stdin pipe degrades the retrieve instead of crashing the host", async () => {
  const { path, cleanup } = shim();
  const log = join(mkdtempSync(join(tmpdir(), "cave-pi-epipe-")), "spawns");
  await withEnv({ STUB_MCP_SPAWN_LOG: log }, async () => {
    const recovery = new RecoveryClient(path);
    assert.equal(await recovery.ensure(), true);
    const pids = readFileSync(log, "utf8").trim().split("\n").filter(Boolean).map(Number);
    assert.ok(pids.length > 0, "stub never recorded a spawn");

    // In flight and unread: 2 MB overflows the pipe buffer, so the write is still
    // pending in the stream when the reader disappears.
    const inFlight = recovery.retrieve(KNOWN_HANDLE, "q".repeat(2_000_000), undefined);
    for (const pid of pids) { try { process.kill(pid, "SIGKILL"); } catch { /* already gone */ } }
    const results = await Promise.all([inFlight, recovery.retrieve(KNOWN_HANDLE, undefined, undefined)]);
    for (const result of results) {
      assert.equal(result.isError, true, "a dead pipe must surface as a failed retrieve");
    }
    recovery.dispose();
  }).finally(() => { cleanup(); rmSync(dirname(log), { recursive: true, force: true }); });
});

const outputEvent = (text = KNOWN_BYTES) => ({
  toolName: "read_file", input: {}, isError: false,
  content: [{ type: "text", text }, { type: "image", data: "cGl4ZWxz", mimeType: "image/png" }],
});
const shrink = (recovery, response, event = outputEvent()) =>
  shrinkToolResult({ call: async () => response }, "publication-test", event, recovery);

test("publication accepts only one exact recoverable identity across every advertised reference", async () => {
  const { path, cleanup } = shim();
  const recovery = new RecoveryClient(path);
  const replacement = `full: ccr://${KNOWN_HANDLE}\nrecover: <<ccr:${KNOWN_HANDLE}>>`;
  try {
    const event = outputEvent();
    assert.deepEqual(await shrink(recovery, { recovery_ref: `ccr:${KNOWN_HANDLE}`, output_replacement: replacement }, event),
      { content: [{ type: "text", text: replacement }, event.content[1]] });
    const nested = `compressed <<ccr:${KNOWN_HANDLE}>>`;
    assert.deepEqual(await shrink(recovery, { hookSpecificOutput: { updatedToolOutput: nested } }, event),
      { content: [{ type: "text", text: nested }, event.content[1]] });
    assert.equal((await recovery.retrieve(KNOWN_HANDLE, undefined, undefined)).text, KNOWN_BYTES,
      "silent verification must not mark originals as already delivered");
  } finally { recovery.dispose(); cleanup(); }
});

test("unknown, mismatched and malformed publications keep original content", async () => {
  const { path, cleanup } = shim();
  const recovery = new RecoveryClient(path);
  const other = "ccr_ffffffffffffffffffffffffffffffff";
  const valid = `ccr://${KNOWN_HANDLE}`;
  try {
    const cases = [
      ["unknown", { recovery_ref: other, output_replacement: `full: ccr://${other}` }],
      ["reference mismatch", { recovery_ref: other, output_replacement: `full: ${valid}` }],
      ["missing reference", { output_replacement: "summary with no recovery handle" }],
      ["reference without replacement", { recovery_ref: valid }],
      ["malformed response reference", { recovery_ref: `${valid}/bad`, output_replacement: `full: ${valid}` }],
      ["multiple different handles", { output_replacement: `full: ${valid}\nrecover: ccr://${other}` }],
      ["URI suffix", { output_replacement: `full: ${valid}/different` }],
      ["URI query", { output_replacement: `full: ${valid}?handle=${other}` }],
      ["marker suffix", { output_replacement: `<<ccr:${KNOWN_HANDLE}>>different` }],
      ["unclosed marker", { output_replacement: `<<ccr:${KNOWN_HANDLE}` }],
      ["extra marker delimiter", { output_replacement: `<<ccr:${KNOWN_HANDLE}>>>` }],
      ["embedded reference", { output_replacement: `not${valid}` }],
      ["oversized replacement", { output_replacement: `full: ${valid}\n${"x".repeat(MAX_OUTPUT_REPLACEMENT_BYTES)}` }],
    ];
    const event = outputEvent();
    const original = structuredClone(event);
    for (const [name, response] of cases) {
      assert.equal(await shrink(recovery, response, event), undefined, name);
      assert.deepEqual(event, original, `${name} changed original content`);
    }
    for (const text of [KNOWN_BYTES + "\n", KNOWN_BYTES.replace("é", "e\u0301"), "x".repeat(MAX_TOOL_OUTPUT_BYTES + 1)]) {
      assert.equal(await shrink(recovery, { output_replacement: `full: ${valid}` }, outputEvent(text)), undefined,
        "nonidentical or over-cap originals must not be replaced");
    }
  } finally { recovery.dispose(); cleanup(); }
});

test("missing verification capability keeps original and does not consume recovery on old companions", async () => {
  const { path, cleanup } = shim();
  await withEnv({ STUB_MCP_DROP_VERIFICATION: "1" }, async () => {
    const recovery = new RecoveryClient(path);
    try {
      assert.equal(await shrink(recovery, { output_replacement: `<<ccr:${KNOWN_HANDLE}>>` }), undefined);
      assert.equal((await recovery.retrieve(KNOWN_HANDLE, undefined, undefined)).text, KNOWN_BYTES);
    } finally { recovery.dispose(); }
  }).finally(cleanup);
});

test("invalid or failed verification proofs keep original output", async () => {
  const { path, cleanup } = shim();
  try {
    for (const fault of ["reference", "length", "digest", "malformed", "error", "hang"]) {
      await withEnv({ STUB_MCP_VERIFICATION_FAULT: fault }, async () => {
        const recovery = new RecoveryClient(path);
        try {
          const started = Date.now();
          assert.equal(await shrink(recovery, { output_replacement: `full: ccr://${KNOWN_HANDLE}` }), undefined, fault);
          if (fault === "hang") assert.ok(Date.now() - started < 5000, "publication must not wait the model's 30s retrieval budget");
        } finally { recovery.dispose(); }
      });
    }
  } finally { cleanup(); }
});

test("unavailable recovery and thrown failures leave tool results untouched", async () => {
  const { path, cleanup } = shim();
  const recovery = new RecoveryClient(path);
  recovery.dispose();
  const response = { output_replacement: `full: ccr://${KNOWN_HANDLE}` };
  const event = outputEvent();
  const original = structuredClone(event);
  try {
    assert.equal(await shrink(recovery, response, event), undefined);
    assert.equal(await shrink({ verify: async () => { throw new Error("transport broke"); } }, response, event), undefined);
    assert.equal(await shrinkToolResult({ call: async () => { throw new Error("hook broke"); } }, "test", event, recovery), undefined);
    assert.deepEqual(event, original);
  } finally { cleanup(); }
});
