import assert from "node:assert/strict";
import test from "node:test";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const stub = join(here, "fixtures", "stub-caveman-mcp.mjs");
// pathToFileURL, not a bare path: dynamic import() of an absolute Windows
// path throws ERR_UNSUPPORTED_ESM_URL_SCHEME (the drive letter reads as a URL
// scheme).
const { RecoveryClient } = await import(pathToFileURL(join(here, "..", "dist", "testable.mjs")).href);

const KNOWN_HANDLE = "ccr_0123456789abcdef0123456789abcdef";
const KNOWN_BYTES = "exact original bytes\nline two éø bytes";

import { chmodSync, mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";

// RecoveryClient spawns its binary argv-less, so each test materializes a tiny
// shell shim that execs the node stub (optionally with a failure-mode env).

function shim(extraEnv = "") {
  const dir = mkdtempSync(join(tmpdir(), "cave-pi-mcp-"));
  const path = join(dir, "caveman-mcp");
  writeFileSync(path, `#!/bin/sh\n${extraEnv}exec "${process.execPath}" "${stub}" "$@"\n`);
  chmodSync(path, 0o755);
  return { path, cleanup: () => rmSync(dir, { recursive: true, force: true }) };
}

test("probe failure (missing capability) makes the client unavailable", async () => {
  const { path, cleanup } = shim("STUB_MCP_DROP_CAPABILITY=1; export STUB_MCP_DROP_CAPABILITY\n");
  try {
    const recovery = new RecoveryClient(path);
    assert.equal(await recovery.ensure(), false);
    const result = await recovery.retrieve(KNOWN_HANDLE, undefined, undefined);
    assert.equal(result.isError, true);
    assert.match(result.text, /cave_recovery_unavailable/);
    recovery.dispose();
  } finally {
    cleanup();
  }
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
  const { path, cleanup } = shim(`if [ ! -e "${flag}" ]; then touch "${flag}"; STUB_MCP_EXIT_AFTER_INIT=1; export STUB_MCP_EXIT_AFTER_INIT; fi\n`);
  try {
    const recovery = new RecoveryClient(path);
    // First bring-up crashes right after initialize (crash-once shim)...
    await recovery.ensure();
    await new Promise((resolve) => setTimeout(resolve, 200));
    // ...next retrieve must respawn a healthy child and still resolve the handle.
    const result = await recovery.retrieve(KNOWN_HANDLE, undefined, undefined);
    assert.equal(result.isError, false, result.text);
    assert.equal(result.text, KNOWN_BYTES);
    recovery.dispose();
  } finally {
    cleanup();
    rmSync(dirname(flag), { recursive: true, force: true });
  }
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
