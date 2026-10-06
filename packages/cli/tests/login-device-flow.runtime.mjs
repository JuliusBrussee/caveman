import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// `caveman login` runs the shared device flow (packages/device-auth, compiled
// in). These cover what the CLI adds around it: plain refusals, persistence
// before the ACK, and the one config home.
const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");
const token = `${Buffer.from(JSON.stringify({ uid: "u1", oid: "org-flow", email: "you@example.com", exp: Math.floor(Date.now() / 1000) + 3600 })).toString("base64url")}.sig`;

async function stub(handler) {
  const requests = [];
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", () => {
      requests.push({ url: req.url, headers: req.headers, body });
      const [status, value] = handler(req.url, requests);
      res.writeHead(status, { "content-type": "application/json" });
      res.end(JSON.stringify(value));
    });
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return { base: `http://127.0.0.1:${server.address().port}`, requests, close: () => server.close() };
}

function env() {
  const home = mkdtempSync(join(tmpdir(), "cave-login-flow-"));
  const caveHome = join(home, ".caveman");
  const out = { ...process.env, HOME: home, CAVEMAN_HOME: caveHome, CAVE_NO_KEYCHAIN: "1", NO_COLOR: "1" };
  delete out.CAVE_TOKEN;
  delete out.CAVE_GATEWAY_URL;
  return { env: out, home, caveHome };
}

function login(argv, environment) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, "login", "--no-browser", ...argv], { env: environment });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

test("a Cloud without the device endpoint (404) gets the same plain line", async () => {
  const server = await stub(() => [404, { error: { code: "cave_not_found" } }]);
  const box = env();
  try {
    const out = await login(["--base-url", server.base], box.env);
    assert.equal(out.code, 1);
    assert.equal(out.stderr, `Sign-in is not open on ${new URL(server.base).host} yet.\n`);
    assert.equal(server.requests.length, 1, "no polling after a refused code");
  } finally {
    server.close();
    rmSync(box.home, { recursive: true, force: true });
  }
});

test("login happy path: grant persisted to the one config home before the ACK", async () => {
  const box = env();
  const server = await stub((url) => {
    if (url === "/api/v1/auth/device/code") return [200, { device_code: "dev-1", user_code: "ABCD-EFGH", verification_uri: "http://stub/activate", expires_in: 60, interval: 0 }];
    if (url === "/api/v1/auth/device/token") return [200, { access_token: token, refresh_token: "refresh-1", project_id: "project-1", delivery_ack_token: "ack-1" }];
    if (url === "/api/v1/auth/device/ack") {
      // The receipt fence: local state must already be on disk.
      assert.equal(JSON.parse(readFileSync(join(box.caveHome, "cloud.json"), "utf8")).projectId, "project-1");
      return [200, {}];
    }
    return [404, {}];
  });
  try {
    const out = await login(["--base-url", server.base, "--gateway-url", "http://127.0.0.1:9"], box.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stderr, /Authorize this device in your browser:\n {4}http:\/\/stub\/activate\n {4}code: ABCD-EFGH/);
    assert.equal(JSON.parse(out.stdout.slice(0, out.stdout.indexOf("}\n") + 1)).authenticated, true);
    assert.deepEqual(server.requests.slice(0, 3).map((r) => r.url), ["/api/v1/auth/device/code", "/api/v1/auth/device/token", "/api/v1/auth/device/ack"]);
    assert.equal(server.requests[0].headers["x-cave-client"], "cli");
    assert.deepEqual(JSON.parse(readFileSync(join(box.caveHome, "credentials"), "utf8")), { access_token: token, refresh_token: "refresh-1", project_id: "project-1" });
    const config = JSON.parse(readFileSync(join(box.caveHome, "cloud.json"), "utf8"));
    assert.equal(config.organizationId, "org-flow");
    assert.equal(config.tokenStore, "file");
    assert.equal(existsSync(join(box.home, ".caveman-cloud", "config.json")), false, "nothing written to the old home");
  } finally {
    server.close();
    rmSync(box.home, { recursive: true, force: true });
  }
});

test("login refuses a plain-http Cloud that is not this machine, from the flag or CAVE_API_URL", async () => {
  const box = env();
  try {
    const flag = await login(["--base-url", "http://cloud.example.test"], box.env);
    assert.equal(flag.code, 1);
    assert.equal(flag.stderr, "Sign-in needs https: http://cloud.example.test (plain http only for localhost).\n");
    const fromEnv = await login([], { ...box.env, CAVE_API_URL: "http://cloud.example.test" });
    assert.equal(fromEnv.code, 1);
    assert.match(fromEnv.stderr, /^Sign-in needs https: http:\/\/cloud\.example\.test /);
    assert.equal(existsSync(join(box.caveHome, "credentials")), false);
  } finally {
    rmSync(box.home, { recursive: true, force: true });
  }
});

