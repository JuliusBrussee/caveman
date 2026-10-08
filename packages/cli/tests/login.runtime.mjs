import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
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
function startStub({ gatewayUrl } = {}) {
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
        send(200, { access_token: TOKEN, token_type: "Bearer", expires_in: 900, ...(gatewayUrl ? { gateway_url: gatewayUrl } : {}) });
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

// The beta gate is gone: a Cloud that does not take sign-ins yet answers 403
// cave_device_login_disabled, and the CLI says so in one line.
test("login against a Cloud with sign-in closed says so plainly", async () => {
  const server = createServer((req, res) => {
    req.resume();
    res.writeHead(403, { "content-type": "application/json" });
    res.end(JSON.stringify({ error: { code: "cave_device_login_disabled", message: "device login is disabled" } }));
  });
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1" };
  delete env.CAVE_TOKEN;
  try {
    const result = await runCli(["login", "--no-browser", "--base-url", `http://127.0.0.1:${port}`], env);
    assert.equal(result.code, 1);
    assert.equal(result.stdout, "");
    assert.equal(result.stderr, `Sign-in is not open on 127.0.0.1:${port} yet.\n`);
    assert.equal(existsSync(join(caveDir, "cloud.json")), false);
    assert.equal(existsSync(join(caveDir, "credentials")), false);
  } finally {
    server.close();
  }
});

// Device-flow login must store the token in the 0600 credentials file (keychain
// forced off), bind organization_id from the token, keep the secret OUT of
// config.json, and let a follow-up connected verb authenticate with it.
test("login device flow stores token in credentials file and binds org", async () => {
  const { server, getCapturedAuth } = startStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1" };
  delete env.CAVE_TOKEN;

  const login = await runCli(["login", "--no-browser", "--base-url", `http://127.0.0.1:${port}`], env);
  assert.equal(login.code, 0, `login failed: ${login.stderr}`);

  assert.equal(readFileSync(join(caveDir, "credentials"), "utf8"), TOKEN, "token must be written to the credentials file");

  const cfg = JSON.parse(readFileSync(join(caveDir, "cloud.json"), "utf8"));
  assert.equal(cfg.tokenStore, "file");
  assert.equal(cfg.organizationId, "org-test", "organization_id must be bound from the token");
  assert.ok(!cfg.token, "the secret token must never be persisted in config.json");

  const whoami = await runCli(["whoami"], env);
  assert.equal(whoami.code, 0, `whoami failed: ${whoami.stderr}`);
  assert.equal(getCapturedAuth(), `Bearer ${TOKEN}`, "the stored token must authenticate the follow-up call");

  server.close();
});

test("login rejects unknown arguments before any request", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const out = await runCli(["login", "--browser-ish"], { ...process.env, HOME: home, CAVE_API_URL: "http://127.0.0.1:1" });
  assert.equal(out.code, 2);
  assert.equal(out.stdout, "");
  assert.match(out.stderr, /^usage: caveman login /);
});

test("login rejects a non-2xx device-code response before polling", async () => {
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

test("login retries a transient token-poll connection failure", async () => {
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

// An explicit gateway choice (`login --gateway-url`, or CAVE_GATEWAY_URL at
// login) still moves wrap to the managed gateway with no env var afterwards.
test("login with an explicit --gateway-url moves wrap to that gateway", async () => {
  const { server } = startStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1" };
  delete env.CAVE_TOKEN;
  delete env.CAVE_GATEWAY_URL;

  const login = await runCli(["login", "--base-url", `http://127.0.0.1:${port}`, "--gateway-url", "http://127.0.0.1:9876"], env);
  assert.equal(login.code, 0, `login failed: ${login.stderr}`);

  const cfg = JSON.parse(readFileSync(join(caveDir, "cloud.json"), "utf8"));
  assert.equal(cfg.gatewayUrl, "http://127.0.0.1:9876", "login must persist the chosen gateway URL");
  assert.equal(cfg.managedGateway, true, "an explicit --gateway-url is a traffic choice");
  cfg.wrap = { proxy: false };
  writeFileSync(join(caveDir, "cloud.json"), JSON.stringify(cfg, null, 2));

  // proxy:false so the test never spawns a real proxy; we only inspect the injection.
  const printEnv = "process.stdout.write(JSON.stringify({a:process.env.ANTHROPIC_BASE_URL,o:process.env.OPENAI_BASE_URL}))";
  const wrapped = await runCli(["wrap", "node", "-e", printEnv], env);
  assert.equal(wrapped.code, 0, `wrap failed: ${wrapped.stderr}`);
  const injected = JSON.parse(wrapped.stdout);
  assert.equal(injected.a, "http://127.0.0.1:9876", "wrap must route ANTHROPIC_BASE_URL to the chosen gateway");
  assert.equal(injected.o, "http://127.0.0.1:9876", "wrap must route OPENAI_BASE_URL to the chosen gateway");

  server.close();
});

// The golden rule: signing in never moves agent traffic through a Caveman hop.
// Login keeps the Cloud's gateway (advertised here) for Cloud calls such as
// /v1/route, but wrap, enable and module wiring stay on the local proxy, and no
// harness file changes.
test("login alone never changes where agent traffic goes", async () => {
  const { server } = startStub({ gatewayUrl: "https://gateway.example.test" });
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1", CAVEMAN_OFFLINE: "1" };
  delete env.CAVE_TOKEN;
  delete env.CAVE_GATEWAY_URL;
  mkdirSync(join(home, ".claude"), { recursive: true });
  const settings = `${JSON.stringify({ env: { ANTHROPIC_BASE_URL: "http://127.0.0.1:8787" } }, null, 2)}\n`;
  writeFileSync(join(home, ".claude", "settings.json"), settings);

  delete env.DO_NOT_TRACK;
  delete env.CAVEMAN_TELEMETRY;
  const login = await runCli(["login", "--no-browser", "--base-url", `http://127.0.0.1:${port}`], env);
  assert.equal(login.code, 0, `login failed: ${login.stderr}`);
  assert.doesNotMatch(login.stderr, /wrap (now )?routes through/);
  // Every sign-in says what routing does now and what data leaves, each with its off switch.
  // No stored routing switch yet, so the proxy will not route: say so.
  assert.match(login.stderr, /^routing is off · caveman on routing to turn it on$/m);
  assert.match(login.stderr, /^runtime data: counts per request to your Cloud, never prompt text · caveman telemetry off to stop$/m);
  const stored = JSON.parse(readFileSync(join(caveDir, "cloud.json"), "utf8"));
  writeFileSync(join(caveDir, "cloud.json"), JSON.stringify({ ...stored, modules: { routing: true } }));
  const optedOut = await runCli(["login", "--no-browser", "--base-url", `http://127.0.0.1:${port}`], { ...env, DO_NOT_TRACK: "1" });
  assert.match(optedOut.stderr, /^routing is on · adds Auto to your agent's model picker; each request on Auto sends your latest ask \(with what your agent attaches to it\), the one before it, the end of the agent's last reply and request facts \(tools, effort, agent headers, token counts\) to Caveman Cloud to pick the model and effort; on the Free plan Caveman may keep them to improve routing; requests go to your providers on your keys, only ones routed to a Caveman Cloud model pass through it · caveman off routing to stop$/m);
  assert.match(optedOut.stderr, /^runtime data: nothing sent \(telemetry is off\)/m);

  const cfg = JSON.parse(readFileSync(join(caveDir, "cloud.json"), "utf8"));
  assert.equal(cfg.gatewayUrl, "https://gateway.example.test", "the Cloud gateway stays known for Cloud calls");
  assert.equal(cfg.managedGateway, false, "signing in is not a traffic choice, and v4 records that");
  assert.equal(readFileSync(join(home, ".claude", "settings.json"), "utf8"), settings, "login never touches a harness file");
  cfg.wrap = { proxy: false };
  writeFileSync(join(caveDir, "cloud.json"), JSON.stringify(cfg, null, 2));

  const printEnv = "process.stdout.write(JSON.stringify({a:process.env.ANTHROPIC_BASE_URL,o:process.env.OPENAI_BASE_URL}))";
  const wrapped = await runCli(["wrap", "node", "-e", printEnv], env);
  assert.equal(wrapped.code, 0, `wrap failed: ${wrapped.stderr}`);
  assert.doesNotMatch(wrapped.stdout, /gateway\.example\.test/, "wrap must not route to the managed gateway after login alone");

  const status = await runCli(["status"], env);
  assert.match(status.stdout, /^agent traffic: local runtime$/m);

  server.close();
});

// Wiring an earlier login pointed at the managed gateway is left alone on
// upgrade; status names it and the one command that moves it.
test("status names agent wiring an earlier login left on the managed gateway", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1", CAVEMAN_OFFLINE: "1" };
  delete env.CAVE_TOKEN;
  delete env.CAVE_GATEWAY_URL;
  mkdirSync(join(caveDir, "integrations"), { recursive: true });
  writeFileSync(join(caveDir, "integrations", "claude.json"), JSON.stringify({
    schema_version: 1, agent: "claude", pack_version: "test", installed_at: "2026-09-01T00:00:00Z", detected_agent_version: null,
    operations: [{ file: join(home, ".claude", "settings.json"), kind: "claude-settings", backup: "", before_exists: false, before_sha256: null, after_sha256: "", owned: { route: "https://gateway.example.test" } }],
  }));
  writeFileSync(join(caveDir, "cloud.json"), JSON.stringify({ gatewayUrl: "https://gateway.example.test" }));

  const status = await runCli(["status"], env);
  assert.match(status.stdout, /^agent traffic: managed gateway \(https:\/\/gateway\.example\.test, from an earlier login\) · caveman setup to use the local runtime$/m);
});

// With no explicit --gateway-url, login derives the sibling gateway for the
// shapes Caveman ships (local control-api :8080 → gateway :8787 here) and keeps
// it for Cloud calls.
test("login derives the local sibling gateway when none is given", async () => {
  const { server } = startStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1" };
  delete env.CAVE_TOKEN;
  delete env.CAVE_GATEWAY_URL;

  const login = await runCli(["login", "--base-url", `http://127.0.0.1:${port}`], env);
  assert.equal(login.code, 0, `login failed: ${login.stderr}`);

  const cfg = JSON.parse(readFileSync(join(caveDir, "cloud.json"), "utf8"));
  assert.equal(cfg.gatewayUrl, "http://127.0.0.1:8787", "login must derive the local sibling gateway (8080→8787)");

  server.close();
});

// Signing in again to the same Cloud without the flag keeps the gateway chosen
// earlier: only a different Cloud, or logout, resets the choice.
test("signing in again keeps an explicitly chosen gateway", async () => {
  const { server } = startStub({ gatewayUrl: "https://gateway.example.test" });
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1", CAVEMAN_OFFLINE: "1" };
  delete env.CAVE_TOKEN;
  delete env.CAVE_GATEWAY_URL;
  const base = `http://127.0.0.1:${port}`;
  assert.equal((await runCli(["login", "--no-browser", "--base-url", base, "--gateway-url", "http://127.0.0.1:9876"], env)).code, 0);
  assert.equal((await runCli(["login", "--no-browser", "--base-url", base], env)).code, 0);
  const cfg = JSON.parse(readFileSync(join(caveDir, "cloud.json"), "utf8"));
  assert.equal(cfg.gatewayUrl, "http://127.0.0.1:9876");
  assert.equal(cfg.managedGateway, true);
  server.close();
});

// A pre-v4 login stored its gateway without recording a choice; v4 keeps agent
// traffic local and says how to keep the gateway.
test("status names a pre-v4 login's gateway and how to keep it", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1", CAVEMAN_OFFLINE: "1" };
  delete env.CAVE_TOKEN;
  delete env.CAVE_GATEWAY_URL;
  writeFileSync(join(caveDir, "cloud.json"), JSON.stringify({ gatewayUrl: "https://gateway.example.test" }));
  const status = await runCli(["status"], env);
  assert.match(status.stdout, /^agent traffic: local runtime · to keep using https:\/\/gateway\.example\.test: caveman login --gateway-url https:\/\/gateway\.example\.test$/m);
  const json = JSON.parse((await runCli(["status", "--json"], env)).stdout);
  assert.equal(json.agent_traffic.target, "local");
});
