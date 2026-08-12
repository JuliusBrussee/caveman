import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function makeToken(org) {
  const payload = Buffer.from(
    JSON.stringify({ uid: "u1", oid: org, email: "a@b.c", role: "owner", exp: Math.floor(Date.now() / 1000) + 3600 }),
  ).toString("base64url");
  return `${payload}.sig`;
}

const TOKEN = makeToken("org-test");

// startStub mirrors login.runtime.mjs's device-flow stub, plus /api/v1/projects
// (init's own follow-up call after login succeeds).
function startStub() {
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      const send = (code, obj) => {
        res.writeHead(code, { "content-type": "application/json" });
        res.end(JSON.stringify(obj));
      };
      if (req.url === "/api/v1/auth/device/code") {
        send(200, {
          device_code: "dev-123",
          user_code: "WXYZ-2345",
          verification_uri: "http://stub/activate",
          verification_uri_complete: "http://stub/activate?user_code=WXYZ-2345",
          expires_in: 60,
          interval: 0,
        });
      } else if (req.url === "/api/v1/auth/device/token") {
        send(200, { access_token: TOKEN, token_type: "Bearer", expires_in: 900 });
      } else if (req.url === "/api/v1/projects") {
        send(200, { data: [{ id: "proj-init-test" }] });
      } else {
        send(404, {});
      }
    });
  });
  return server;
}

function runCli(argv, env, cwd) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...argv], { env, cwd });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

function listen(server) {
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));
}

// C8 review finding 6: caveman init used to compute its OWN baseURL with a
// bare localhost:8080 literal and no CAVE_API_URL precedence, while
// login(argv) — which init calls internally — correctly resolved CAVE_API_URL.
// The result: after `caveman init` with CAVE_API_URL set (and no --base-url
// flag), the device-flow token was minted against CAVE_API_URL but init would
// persist config.json/.env.cave pointing at localhost:8080 instead — a prod
// token stored against a dead local base URL. Both must now agree.
test("caveman init persists the SAME base URL login resolved (CAVE_API_URL, no flag)", { skip: "Cloud login disabled during beta" }, async () => {
  const server = startStub();
  const port = await listen(server);
  const stubURL = `http://127.0.0.1:${port}`;

  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const workDir = mkdtempSync(join(tmpdir(), "cave-init-cwd-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1", CAVE_API_URL: stubURL };
  delete env.CAVE_TOKEN;

  const result = await runCli(["init"], env, workDir);
  assert.equal(result.code, 0, `init failed: ${result.stderr}`);

  const envCave = readFileSync(join(workDir, ".env.cave"), "utf8");
  assert.match(envCave, new RegExp(`CAVE_API_URL=${stubURL}(\\n|$)`), `.env.cave must persist CAVE_API_URL (${stubURL}), got: ${envCave}`);

  const cfg = JSON.parse(readFileSync(join(home, ".caveman-cloud", "config.json"), "utf8"));
  assert.equal(cfg.baseURL, stubURL, "config.json baseURL must match what login resolved, not a localhost:8080 literal");

  server.close();
});
