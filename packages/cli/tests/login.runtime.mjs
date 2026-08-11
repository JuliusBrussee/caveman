import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

// A JWT-shaped token whose base64url payload carries the org id, so the CLI can
// bind organization_id from the server-issued token (signature is not verified
// client-side).
function makeToken(org) {
  const payload = Buffer.from(
    JSON.stringify({ uid: "u1", oid: org, email: "a@b.c", role: "owner", exp: Math.floor(Date.now() / 1000) + 3600 }),
  ).toString("base64url");
  return `${payload}.sig`;
}

const TOKEN = makeToken("org-test");

test("RFC 8628 slow_down adds five seconds to later polls", async () => {
  const { nextDevicePollIntervalMs } = await import(`${pathToFileURL(cli).href}?poll-interval`);
  assert.equal(nextDevicePollIntervalMs(0, "authorization_pending"), 0);
  assert.equal(nextDevicePollIntervalMs(0, "slow_down"), 5000);
  assert.equal(nextDevicePollIntervalMs(5000, "slow_down"), 10000);
});

// startStub runs a minimal control-api: a device flow that grants TOKEN on the
// first poll, plus /auth/me which records the Authorization header it received.
function startStub() {
  let capturedAuth = null;
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
      } else if (req.url === "/api/v1/auth/me") {
        capturedAuth = req.headers.authorization;
        send(200, { user: { email: "a@b.c" } });
      } else {
        send(404, {});
      }
    });
  });
  return { server, getCapturedAuth: () => capturedAuth };
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

function listen(server) {
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));
}

test("login is unavailable while Caveman Cloud is in beta", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1" };
  delete env.CAVE_TOKEN;

  const result = await runCli(["login", "--base-url", "http://127.0.0.1:1"], env);
  assert.equal(result.code, 1);
  assert.equal(result.stdout, "");
  assert.equal(result.stderr, "Caveman Cloud platform is still in beta.\n");
  assert.equal(existsSync(join(home, ".caveman-cloud", "config.json")), false);
  assert.equal(existsSync(join(caveDir, "credentials")), false);
});

// Device-flow login must store the token in the 0600 credentials file (keychain
// forced off), bind organization_id from the token, keep the secret OUT of
// config.json, and let a follow-up connected verb authenticate with it.
test("login device flow stores token in credentials file and binds org", { skip: "Cloud login disabled during beta" }, async () => {
  const { server, getCapturedAuth } = startStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1" };
  delete env.CAVE_TOKEN;

  const login = await runCli(["login", "--no-browser", "--base-url", `http://127.0.0.1:${port}`], env);
  assert.equal(login.code, 0, `login failed: ${login.stderr}`);

  assert.equal(readFileSync(join(caveDir, "credentials"), "utf8"), TOKEN, "token must be written to the credentials file");

  const cfg = JSON.parse(readFileSync(join(home, ".caveman-cloud", "config.json"), "utf8"));
  assert.equal(cfg.tokenStore, "file");
  assert.equal(cfg.organizationId, "org-test", "organization_id must be bound from the token");
  assert.ok(!cfg.token, "the secret token must never be persisted in config.json");

  const whoami = await runCli(["whoami"], env);
  assert.equal(whoami.code, 0, `whoami failed: ${whoami.stderr}`);
  assert.equal(getCapturedAuth(), `Bearer ${TOKEN}`, "the stored token must authenticate the follow-up call");

  server.close();
});

test("login beta gate takes precedence over argument validation", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const out = await runCli(["login", "--browser-ish"], { ...process.env, HOME: home });
  assert.equal(out.code, 1);
  assert.equal(out.stdout, "");
  assert.equal(out.stderr, "Caveman Cloud platform is still in beta.\n");
});

test("login rejects a non-2xx device-code response before polling", { skip: "Cloud login disabled during beta" }, async () => {
  let polls = 0;
  const server = createServer((req, res) => {
    if (req.url === "/api/v1/auth/device/code") {
      res.writeHead(503, { "content-type": "application/json" });
      res.end(JSON.stringify({ device_code: "must-not-be-used" }));
      return;
    }
    if (req.url === "/api/v1/auth/device/token") polls++;
    res.writeHead(404).end();
  });
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  try {
    const result = await runCli(["login", "--no-browser", "--base-url", `http://127.0.0.1:${port}`], { ...process.env, HOME: home });
    assert.notEqual(result.code, 0);
    assert.match(result.stderr, /device authorization failed: HTTP 503/);
    assert.equal(polls, 0);
  } finally {
    server.close();
  }
});

test("login retries a transient token-poll connection failure", { skip: "Cloud login disabled during beta" }, async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1" };
  delete env.CAVE_TOKEN;
  let polls = 0;
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      if (req.url === "/api/v1/auth/device/code") {
        res.writeHead(200, { "content-type": "application/json" });
        res.end(JSON.stringify({ device_code: "dev-retry", user_code: "WXYZ-2345", verification_uri: "http://stub/activate", expires_in: 60, interval: 0 }));
        return;
      }
      if (req.url === "/api/v1/auth/device/token") {
        polls++;
        if (polls === 1) {
          res.destroy();
          return;
        }
        res.writeHead(200, { "content-type": "application/json" });
        res.end(JSON.stringify({ access_token: TOKEN, token_type: "Bearer", expires_in: 900 }));
        return;
      }
      res.writeHead(404, { "content-type": "application/json" });
      res.end("{}");
    });
  });
  const port = await listen(server);
  try {
    const login = await runCli(["login", "--base-url", `http://127.0.0.1:${port}`], env);
    assert.equal(login.code, 0, `login failed: ${login.stderr}`);
    assert.equal(polls, 2, "the failed token poll must be retried");
  } finally {
    server.close();
  }
});

// CAVE_TOKEN must let connected verbs run with no prior login (the CI path).
test("CAVE_TOKEN bypasses login for connected verbs", async () => {
  const { server, getCapturedAuth } = startStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const env = { ...process.env, HOME: home, CAVE_TOKEN: "ci-token", CAVE_API_URL: `http://127.0.0.1:${port}` };

  const whoami = await runCli(["whoami"], env);
  assert.equal(whoami.code, 0, `whoami failed: ${whoami.stderr}`);
  assert.equal(getCapturedAuth(), "Bearer ci-token");

  server.close();
});

// A connected verb with no credentials must degrade gracefully: a one-line hint
// and a non-zero exit, never a stack trace.
test("connected verb without credentials exits non-zero with a login hint", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir };
  delete env.CAVE_TOKEN;

  const whoami = await runCli(["whoami"], env);
  assert.notEqual(whoami.code, 0, "logged-out connected verb must exit non-zero");
  assert.match(whoami.stderr, /caveman login/, "must hint at `caveman login`");
});

// The keystone of the free→cloud funnel (SIMPLICITY_SPEC §6.5, audit finding #3):
// after login persists a managed gateway URL, `caveman wrap` must route there with
// NO env var. Gateway resolution is now dynamic (read from config.json per run), so
// the login that writes gatewayUrl flips wrap on the next invocation.
test("login persists the gateway URL and wrap flips to it with no env var", { skip: "Cloud login disabled during beta" }, async () => {
  const { server } = startStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1" };
  delete env.CAVE_TOKEN;
  delete env.CAVE_GATEWAY_URL;

  const login = await runCli(["login", "--base-url", `http://127.0.0.1:${port}`, "--gateway-url", "http://127.0.0.1:9876"], env);
  assert.equal(login.code, 0, `login failed: ${login.stderr}`);

  const cfg = JSON.parse(readFileSync(join(home, ".caveman-cloud", "config.json"), "utf8"));
  assert.equal(cfg.gatewayUrl, "http://127.0.0.1:9876", "login must persist the managed gateway URL to config.json");
  cfg.wrap = { proxy: false };
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify(cfg, null, 2));

  // proxy:false so the test never spawns a real proxy; we only inspect the injection.
  const printEnv = "process.stdout.write(JSON.stringify({a:process.env.ANTHROPIC_BASE_URL,o:process.env.OPENAI_BASE_URL}))";
  const wrapped = await runCli(["wrap", "node", "-e", printEnv], env);
  assert.equal(wrapped.code, 0, `wrap failed: ${wrapped.stderr}`);
  const injected = JSON.parse(wrapped.stdout);
  assert.equal(injected.a, "http://127.0.0.1:9876", "wrap must route ANTHROPIC_BASE_URL to the persisted gateway with no env var");
  assert.equal(injected.o, "http://127.0.0.1:9876", "wrap must route OPENAI_BASE_URL to the persisted gateway with no env var");

  server.close();
});

// With no explicit --gateway-url, login derives the sibling gateway for the shapes
// Caveman ships (local control-api :8080 → gateway :8787 here), so the flip works
// with zero extra flags.
test("login derives the local sibling gateway when none is given", { skip: "Cloud login disabled during beta" }, async () => {
  const { server } = startStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1" };
  delete env.CAVE_TOKEN;
  delete env.CAVE_GATEWAY_URL;

  const login = await runCli(["login", "--base-url", `http://127.0.0.1:${port}`], env);
  assert.equal(login.code, 0, `login failed: ${login.stderr}`);

  const cfg = JSON.parse(readFileSync(join(home, ".caveman-cloud", "config.json"), "utf8"));
  assert.equal(cfg.gatewayUrl, "http://127.0.0.1:8787", "login must derive the local sibling gateway (8080→8787)");

  server.close();
});
