/**
 * Runtime test: `cave agent list|show|run` hit the real control-api
 * optimization-proposals endpoints and print the server's actual response — never a
 * synthesized result. `run` surfaces the server's honest 501 (the live harness is
 * Phase 2). Run with: node --test packages/cli/tests/agent.runtime.mjs
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

function withConfig() {
  const home = mkdtempSync(join(tmpdir(), "cave-cli-"));
  mkdirSync(join(home, ".caveman-cloud"));
  return home;
}

function runCli(homeBase, port, argv) {
  const home = homeBase;
  writeFileSync(
    join(home, ".caveman-cloud", "config.json"),
    JSON.stringify({ baseURL: `http://127.0.0.1:${port}`, token: "test-token", projectId: "proj-123" }),
  );
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...argv], { env: { ...process.env, HOME: home } });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (c) => (stdout += c));
    child.stderr.on("data", (c) => (stderr += c));
    child.on("error", reject);
    child.on("close", (status) => resolve({ status, stdout, stderr }));
  });
}

test("cave agent list hits the real endpoint and prints the server's proposals", async () => {
  let captured = null;
  const server = createServer((req, res) => {
    captured = { method: req.method, url: req.url, auth: req.headers["authorization"] };
    res.setHeader("content-type", "application/json");
    // basis 'inferred' — proves the CLI echoes the server, and there is no 'verified'.
    res.end(JSON.stringify({ data: [{ id: "prop-1", optimizer_id: "toon-reencoding", basis: "inferred", status: "drafting" }], next_cursor: null }));
  });
  await new Promise((r) => server.listen(0, r));
  const port = server.address().port;

  const out = await runCli(withConfig(), port, ["agent", "list"]);
  server.close();

  assert.equal(out.status, 0, `cli exited ${out.status}: ${out.stderr}`);
  assert.equal(captured.method, "GET");
  assert.equal(captured.url, "/api/v1/optimization-proposals");
  assert.equal(captured.auth, "Bearer test-token");
  assert.match(out.stdout, /"optimizer_id":\s*"toon-reencoding"/);
  assert.match(out.stdout, /"basis":\s*"inferred"/);
  assert.doesNotMatch(out.stdout, /verified/);
});

test("cave agent run surfaces the server's honest 501 (never a synthesized result)", async () => {
  let captured = null;
  const server = createServer((req, res) => {
    captured = { method: req.method, url: req.url };
    res.statusCode = 501;
    res.setHeader("content-type", "application/json");
    res.end(JSON.stringify({ error: "cave_not_implemented", message: "Running the live Cave Agent harness is not yet implemented." }));
  });
  await new Promise((r) => server.listen(0, r));
  const port = server.address().port;

  const out = await runCli(withConfig(), port, ["agent", "run", "opp-9"]);
  server.close();

  assert.equal(captured.method, "POST");
  assert.equal(captured.url, "/api/v1/optimization-proposals/opp-9/run");
  // The CLI surfaces the server's real error rather than fabricating success.
  assert.match(out.stdout + out.stderr, /cave_not_implemented|not yet implemented/);
});
