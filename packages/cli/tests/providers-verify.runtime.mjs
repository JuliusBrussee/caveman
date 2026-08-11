/**
 * Runtime test: `cave providers verify <conn>` calls the real control-api verify
 * endpoint and prints the actual status (not a hardcoded "active").
 * Run with: node --test packages/cli/tests/providers-verify.runtime.mjs
 * (Requires `tsc` build to dist first.)
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");

test("providers verify hits the real endpoint and prints the real status", async () => {
  let captured = null;
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      captured = { method: req.method, url: req.url, csrf: req.headers["x-cave-csrf"], auth: req.headers["authorization"] };
      res.setHeader("content-type", "application/json");
      // Deliberately NOT "active" — proves the CLI returns the server's value.
      res.end(JSON.stringify({ status: "error", reason: "stub upstream unreachable" }));
    });
  });
  await new Promise((r) => server.listen(0, r));
  const port = server.address().port;

  const home = mkdtempSync(join(tmpdir(), "cave-cli-"));
  mkdirSync(join(home, ".caveman-cloud"));
  writeFileSync(
    join(home, ".caveman-cloud", "config.json"),
    JSON.stringify({ baseURL: `http://127.0.0.1:${port}`, token: "test-token", projectId: "proj-123" }),
  );

  // Use async spawn (not spawnSync): spawnSync blocks the event loop, so the
  // in-process server can never service the CLI's fetch — a deterministic
  // deadlock. Async spawn keeps the loop alive to answer the request.
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "providers", "verify", "conn-abc"], {
      env: { ...process.env, HOME: home },
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (c) => (stdout += c));
    child.stderr.on("data", (c) => (stderr += c));
    child.on("error", reject);
    child.on("close", (status) => resolve({ status, stdout, stderr }));
  });
  server.close();

  assert.equal(out.status, 0, `cli exited ${out.status}: ${out.stderr}`);
  assert.ok(captured, "server should have been called");
  assert.equal(captured.method, "POST");
  assert.equal(captured.url, "/api/v1/projects/proj-123/providers/conn-abc/verify");
  assert.equal(captured.csrf, "cli");
  assert.equal(captured.auth, "Bearer test-token");
  // The printed status must be the server's, not a hardcoded "active".
  assert.match(out.stdout, /"status":\s*"error"/);
  assert.doesNotMatch(out.stdout, /"status":\s*"active"/);
});
