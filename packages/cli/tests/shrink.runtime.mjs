import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");

// run spawns the built CLI with the given args and returns its exit code + streams.
function run(args, env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...args], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

// A stub engine that emits a fixed compressed marker on stdout and a JSON report
// (with a recovery handle) on stderr — standing in for the real caveman-engine.
function compressStub() {
  const dir = mkdtempSync(join(tmpdir(), "cave-engine-"));
  const bin = join(dir, "caveman-engine");
  writeFileSync(
    bin,
    `#!/bin/sh\ncat >/dev/null\nprintf 'SHRUNK'\nprintf '{"tokens_before":100,"tokens_after":10,"ratio":0.9,"basis":"inferred","token_count_basis":"o200k_base","recovery_handle":"ccr_test123"}' 1>&2\n`,
    { mode: 0o755 },
  );
  return bin;
}

// shrink wraps a command, captures its output, runs it through the engine, forwards
// the compressed stdout, appends a recovery footer with the handle, and — crucially —
// propagates the wrapped command's exit code so an agent still sees the failure.
test("shrink wraps a command, appends a recovery footer, and propagates the exit code", async () => {
  const out = await run(
    ["shrink", "--", "node", "-e", "process.stdout.write('RAWOUT'); process.exit(7)"],
    { ...process.env, CAVEMAN_ENGINE_BIN: compressStub() },
  );
  assert.equal(out.code, 7, "shrink must propagate the wrapped command's exit code");
  assert.match(out.stdout, /SHRUNK/, "shrink must forward the engine's compressed stdout");
  assert.match(out.stdout, /recover: caveman retrieve ccr_test123/, "shrink must append a recovery footer carrying the handle");
  assert.match(out.stdout, /100→10 estimated tokens/, "token counters must be labeled as estimates");
  assert.match(out.stdout, /counter o200k_base · inferred/, "footer must disclose counter and evidence basis");
});

// With the engine unavailable, shrink must fail open: print the wrapped command's
// original output unchanged, with no footer, and still propagate its exit code.
test("shrink falls back to byte-safe pass-through when the engine is missing", async () => {
  const out = await run(
    ["shrink", "--", "node", "-e", "process.stdout.write('hello-original'); process.exit(3)"],
    { ...process.env, CAVEMAN_ENGINE_BIN: "caveman-engine-does-not-exist" },
  );
  assert.equal(out.code, 3, "exit code must still propagate on fallback");
  assert.equal(out.stdout, "hello-original", "fallback must emit the captured output unchanged");
  assert.doesNotMatch(out.stdout, /caveman retrieve/, "fallback must not claim a recovery handle it never minted");
});

// --raw bypasses the engine entirely: the original output passes through even when a
// (mangling) engine is configured.
test("shrink --raw bypasses the engine", async () => {
  const out = await run(
    ["shrink", "--raw", "--", "node", "-e", "process.stdout.write('RAWOUT')"],
    { ...process.env, CAVEMAN_ENGINE_BIN: compressStub() },
  );
  assert.equal(out.code, 0);
  assert.equal(out.stdout, "RAWOUT", "--raw must pass the original output through unchanged");
  assert.doesNotMatch(out.stdout, /SHRUNK/, "--raw must not invoke the engine");
});

// retrieve shells out to the engine's retrieve subcommand, forwarding its stdout
// (the recovered original) and exit code.
test("retrieve forwards the engine's recovered bytes and exit code", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-engine-retrieve-"));
  const bin = join(dir, "caveman-engine");
  writeFileSync(bin, `#!/bin/sh\n# retrieve <handle> [query]\nprintf 'ORIGINAL-BYTES'\n`, { mode: 0o755 });
  const out = await run(["retrieve", "ccr_test123"], { ...process.env, CAVEMAN_ENGINE_BIN: bin });
  assert.equal(out.code, 0);
  assert.equal(out.stdout, "ORIGINAL-BYTES", "retrieve must forward the engine's recovered bytes");
});

// retrieve with no handle is a usage error (exit 2), never a silent success.
test("retrieve requires a handle", async () => {
  const out = await run(["retrieve"], { ...process.env });
  assert.equal(out.code, 2);
  assert.match(out.stderr, /usage: caveman retrieve/);
});
