import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");

function proxyStub(dir) {
  const path = join(dir, "caveman-proxy-stub.mjs");
  writeFileSync(path, `#!/usr/bin/env node
const rows = JSON.parse(process.env.STUB_PROXY_ROWS || "[]");
for (const row of rows) {
  if (row.ts === "__NOW__") row.ts = new Date().toISOString();
}
if (process.argv[2] !== "stats" || !process.argv.includes("--recent")) process.exit(2);
process.stdout.write(JSON.stringify(rows));
`, { mode: 0o755 });
  return path;
}

function runVerify(args, rows) {
  const dir = mkdtempSync(join(tmpdir(), "cave-verify-"));
  const stub = proxyStub(dir);
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "verify", ...args], {
      env: {
        ...process.env,
        NO_COLOR: "1",
        CAVEMAN_PROXY_BIN: stub,
        STUB_PROXY_ROWS: JSON.stringify(rows),
      },
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

test("verify succeeds when proxy reports a fresh row for the requested app", async () => {
  const out = await runVerify(["--app", "checkout", "--timeout-ms", "500"], [{
    ts: "__NOW__",
    agent_slug: "checkout",
    provider: "openai",
    model: "gpt-4.1",
    endpoint: "/w/checkout/openai/v1/responses",
    input_tokens: 123,
    output_tokens: 45,
    basis: "inferred",
  }]);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout.trim(), "checkout openai/gpt-4.1 input:123 output:45 basis: inferred");
});

test("verify exits non-zero on empty recent rows", async () => {
  const out = await runVerify(["--timeout-ms", "50"], []);
  assert.notEqual(out.code, 0, "empty rows must fail");
  assert.match(out.stderr, /no request seen through the proxy in 1s/);
  assert.match(out.stderr, /caveman start/);
});

test("verify exits non-zero on stale recent rows", async () => {
  const out = await runVerify(["--app", "checkout", "--timeout-ms", "50"], [{
    ts: "2000-01-01T00:00:00.000Z",
    agent_slug: "checkout",
    provider: "openai",
    model: "gpt-4.1",
    endpoint: "/w/checkout/openai/v1/responses",
    input_tokens: 123,
    output_tokens: 45,
    basis: "inferred",
  }]);
  assert.notEqual(out.code, 0, "stale rows must fail");
  assert.match(out.stderr, /no request seen through the proxy in 1s/);
});
