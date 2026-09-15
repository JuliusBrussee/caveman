import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");

function runToon(sub, input, env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "toon", sub], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
    child.stdin.write(input);
    child.stdin.end();
  });
}

// `cave toon encode|decode` is the manual surface for the stateless JSON⇄TOON
// converter; it delegates to the engine binary (single source of truth, no JS
// parser) and forwards its stdout.
test("toon encode shells out to the engine binary and forwards its output", async () => {
  const stubDir = mkdtempSync(join(tmpdir(), "cave-toon-"));
  const stub = join(stubDir, "caveman-engine");
  writeFileSync(stub, `#!/bin/sh\ncat >/dev/null\nprintf 'rows[1]{id}:\\n  1'\n`, { mode: 0o755 });

  const out = await runToon("encode", '{"rows":[{"id":1}]}', { ...process.env, CAVEMAN_ENGINE_BIN: stub });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, "rows[1]{id}:\n  1", "encode must forward the engine's TOON output");
});

// encode degrades byte-safe when the engine is missing: the input is still valid
// JSON, just not compacted — never break a pipe.
test("toon encode falls back to byte-safe pass-through when the engine is missing", async () => {
  const input = '{"hello":"world"}';
  const out = await runToon("encode", input, { ...process.env, CAVEMAN_ENGINE_BIN: "caveman-engine-does-not-exist" });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, input, "fallback must emit the input bytes unchanged");
});

// decode CANNOT be faked without the engine: emitting raw TOON as if it were JSON
// would hand downstream a broken payload, so a missing engine fails loudly.
test("toon decode fails loudly when the engine is missing (never emits fake JSON)", async () => {
  const out = await runToon("decode", "rows[1]{id}:\n  1", { ...process.env, CAVEMAN_ENGINE_BIN: "caveman-engine-does-not-exist" });
  assert.equal(out.code, 1, "decode must exit non-zero when it cannot convert");
  assert.equal(out.stdout, "", "decode must not emit unconverted bytes as JSON");
  assert.match(out.stderr, /caveman-engine/);
});

// An unknown subcommand is rejected (fail-closed, not silently treated as encode).
test("toon rejects an unknown subcommand", async () => {
  const out = await runToon("frobnicate", "{}", { ...process.env });
  assert.equal(out.code, 1);
  assert.match(out.stderr, /usage: caveman toon encode\|decode/);
});
