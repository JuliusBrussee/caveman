import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = fileURLToPath(new URL("../dist/index.js", import.meta.url));
const token = (expiry) => `${Buffer.from(JSON.stringify({ uid: "user-1", oid: "org-1", exp: expiry })).toString("base64url")}.sig`;
const access = token(Math.floor(Date.now() / 1000) + 30);
const renewed = token(Math.floor(Date.now() / 1000) + 3600);

function environment(t) {
  const home = mkdtempSync(join(tmpdir(), "cave-private-login-"));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: join(home, ".caveman"), CAVE_NO_KEYCHAIN: "1", CAVE_API_URL: "https://must-not-contact.invalid", CAVE_GATEWAY_URL: "https://must-not-inherit.invalid" };
  delete env.CAVE_TOKEN;
  return { env, credentials: join(home, ".caveman", "credentials"), config: join(home, ".caveman-cloud", "config.json") };
}

function run(argv, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env });
    let stdout = "", stderr = "";
    const timer = setTimeout(() => { child.kill(); reject(new Error("CLI timed out")); }, 15000);
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("error", reject);
    child.on("exit", (code) => { clearTimeout(timer); resolve({ code, stdout, stderr }); });
  });
}

async function fixture(t, files, options = {}) {
  const requests = [];
  const grant = { access_token: access, refresh_token: "refresh-1", credential_kind: "none", project_id: "project-1", scope: "org:read trace:read_metadata settings:read", delivery_ack_token: "ack-1", ...options.grant };
  const server = createServer(async (req, res) => {
    let raw = "";
    for await (const chunk of req) raw += chunk;
    const body = raw ? JSON.parse(raw) : null;
    requests.push({ path: req.url, auth: req.headers.authorization, body });
    const send = (status, value) => { res.writeHead(status, { "content-type": "application/json" }); res.end(JSON.stringify(value)); };
    switch (req.url) {
      case "/api/v1/auth/device/code":
        return send(options.codeStatus ?? 200, { device_code: "device-1", user_code: "WXYZ-2345", verification_uri: `${origin}/activate`, expires_in: 30, interval: 0, ...options.code });
      case "/api/v1/auth/device/token":
        if (options.redirect) { res.writeHead(307, { location: `${origin}/unexpected` }); res.end(); return; }
        return send(options.tokenStatus ?? 200, grant);
      case "/api/v1/auth/device/ack":
        assert.deepEqual(JSON.parse(readFileSync(files.credentials, "utf8")), { access_token: access, refresh_token: "refresh-1", project_id: "project-1" });
        assert.equal(JSON.parse(readFileSync(files.config, "utf8")).baseURL, origin);
        return send(options.ackStatus ?? 200, {});
      case "/api/v1/auth/refresh":
        return send(200, { access_token: renewed, refresh_token: "refresh-2" });
      case "/api/v1/auth/me":
        return send(200, { user: { email: "owner@private.example" } });
      case "/api/v1/auth/logout":
        return send(200, {});
      default:
        return send(404, {});
    }
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => { server.closeAllConnections(); server.close(); });
  const origin = `http://127.0.0.1:${server.address().port}`;
  return { origin, requests, login: () => run(["login", "--instance", origin, "--no-browser"], files.env) };
}

test("private login persists keyless grant before ACK, refreshes, authenticates and revokes", async (t) => {
  const files = environment(t);
  const f = await fixture(t, files);
  const login = await f.login();
  assert.equal(login.code, 0, login.stderr);
  const result = JSON.parse(login.stdout);
  assert.equal(result.baseURL, f.origin);
  assert.equal(result.project_id, "project-1");
  assert.equal(result.credential_kind, "none");
  const browser = new URL(login.stderr.match(/http:\/\/\S+/)[0]);
  assert.equal(browser.searchParams.get("connection"), "mcp");
  assert.equal(browser.searchParams.get("client_name"), "Caveman CLI");
  assert.equal(browser.searchParams.get("user_code"), "WXYZ-2345");
  const config = readFileSync(files.config, "utf8");
  assert.equal(JSON.parse(config).organizationId, "org-1");
  assert.equal(JSON.parse(config).gatewayUrl, undefined);
  for (const secret of [access, "refresh-1", "ack-1"]) {
    assert.ok(!config.includes(secret));
    assert.ok(!(login.stdout + login.stderr).includes(secret));
  }
  assert.equal(statSync(files.credentials).mode & 0o777, 0o600);
  assert.equal((await run(["whoami"], files.env)).code, 0);
  assert.deepEqual(f.requests.find((r) => r.path.endsWith("/refresh")).body, { refresh_token: "refresh-1" });
  assert.equal(f.requests.find((r) => r.path.endsWith("/me")).auth, `Bearer ${renewed}`);
  assert.equal((await run(["logout"], files.env)).code, 0);
  assert.deepEqual(f.requests.at(-1).body, { refresh_token: "refresh-2" });
  assert.equal(existsSync(files.credentials), false);
  assert.deepEqual(f.requests.map((r) => r.path), ["code", "token", "ack", "refresh", "me", "logout"].map((part) => `/api/v1/auth/${["code", "token", "ack"].includes(part) ? "device/" : ""}${part}`));
});

test("private login rejects origins and conflicting flags before local writes", async (t) => {
  const files = environment(t);
  for (const args of [
    ["http://private.example"], ["https://user:secret@private.example"], ["https://private.example/api"],
    ["https://private.example?query=1"], ["https://private.example/#fragment"], ["https://api.caveman.so"], ["https://api.caveman.so."],
    ["https://private.example", "--base-url", "https://elsewhere.example"],
    ["https://private.example", "--gateway-url", "https://gateway.example"],
    ["https://private.example", "--instance=https://again.example"],
  ]) {
    const result = await run(["login", "--instance", ...args], files.env);
    assert.notEqual(result.code, 0, JSON.stringify(args));
    if (args.length === 1) assert.match(result.stderr, /--instance requires a private HTTPS origin/);
    assert.equal(existsSync(files.credentials), false);
    assert.equal(existsSync(files.config), false);
  }
});

test("private login rejects malformed, inference-bearing and unsuccessful grants before storage", async (t) => {
  for (const options of [
    { grant: { credential_kind: "gateway" } }, { grant: { gateway_api_key: "unexpected" } },
    { grant: { scope: "org:read proxy:write" } }, { grant: { scope: "org:read sdk:write" } },
    { grant: { refresh_token: "" } }, { grant: { project_id: "" } }, { grant: { delivery_ack_token: "" } },
    { tokenStatus: 503 }, { redirect: true }, { codeStatus: 503 },
    { code: { verification_uri: "http://private.example/activate" } }, { code: { user_code: "bad\ncode" } },
  ]) {
    await t.test(JSON.stringify(options), async (t) => {
      const files = environment(t);
      const f = await fixture(t, files, options);
      const result = await f.login();
      assert.equal(result.code, 1, result.stderr);
      assert.equal(existsSync(files.credentials), false);
      assert.equal(existsSync(files.config), false);
      assert.ok(!f.requests.some((r) => r.path.endsWith("/ack") || r.path === "/unexpected"));
    });
  }
});

test("failed private ACK retains stored grant but never reports login success", async (t) => {
  const files = environment(t);
  const f = await fixture(t, files, { ackStatus: 403 });
  const result = await f.login();
  assert.equal(result.code, 1);
  assert.equal(result.stdout, "");
  assert.match(result.stderr, /delivery acknowledgement failed/);
  assert.ok(existsSync(files.credentials));
  assert.equal(f.requests.at(-1).path, "/api/v1/auth/device/ack");
});
