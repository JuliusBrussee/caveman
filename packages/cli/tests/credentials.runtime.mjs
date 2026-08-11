import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");
const agentsModule = join(here, "..", "dist", "agents.generated.js");

function makeToken({ org = "org-1", exp = Math.floor(Date.now() / 1000) + 3600 } = {}) {
  const payload = Buffer.from(JSON.stringify({ uid: "user-1", oid: org, email: "owner@example.com", role: "owner", exp })).toString("base64url");
  return `${payload}.signature`;
}

function isolatedEnv() {
  const home = mkdtempSync(join(tmpdir(), "cave-credentials-home-"));
  const caveHome = mkdtempSync(join(tmpdir(), "cave-credentials-store-"));
  const configDir = join(home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveHome, CAVE_NO_KEYCHAIN: "1", NO_COLOR: "1" };
  delete env.CAVE_TOKEN;
  delete env.CAVE_API_KEY;
  delete env.CAVE_GATEWAY_URL;
  return { env, home, caveHome, configPath: join(configDir, "config.json"), credentialsPath: join(caveHome, "credentials") };
}

function installFakeSecurity(paths, secret, deleteExit = 0) {
  const binDir = mkdtempSync(join(tmpdir(), "cave-security-bin-"));
  const securityPath = join(binDir, "security");
  const logPath = join(binDir, "security.log");
  writeFileSync(securityPath, `#!/usr/bin/env node
const { appendFileSync } = require("node:fs");
const args = process.argv.slice(2);
appendFileSync(process.env.CAVE_TEST_SECURITY_LOG, JSON.stringify(args) + "\\n");
if (args[0] === "find-generic-password") {
  process.stdout.write(process.env.CAVE_TEST_SECURITY_SECRET || "");
  process.exit(0);
}
if (args[0] === "delete-generic-password") process.exit(Number(process.env.CAVE_TEST_SECURITY_DELETE_EXIT || "0"));
process.exit(2);
`, { mode: 0o755 });
  chmodSync(securityPath, 0o755);
  paths.env.PATH = `${binDir}:${paths.env.PATH ?? ""}`;
  paths.env.CAVE_TEST_SECURITY_LOG = logPath;
  paths.env.CAVE_TEST_SECURITY_SECRET = secret;
  paths.env.CAVE_TEST_SECURITY_DELETE_EXIT = String(deleteExit);
  delete paths.env.CAVE_NO_KEYCHAIN;
  return logPath;
}

function writeConnectedFixture(paths, baseURL, credentials, extraConfig = {}) {
  writeFileSync(paths.credentialsPath, JSON.stringify(credentials), { mode: 0o600 });
  writeFileSync(paths.configPath, JSON.stringify({
    baseURL,
    tokenStore: "file",
    projectId: credentials.project_id,
    gatewayUrl: "https://gateway.caveman.cloud",
    ...extraConfig,
  }, null, 2), { mode: 0o600 });
}

function runCli(argv, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env, stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.once("error", reject);
    child.once("exit", (code) => resolve({ code, stdout, stderr }));
  });
}

function listen(server) {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => resolve(server.address().port));
  });
}

function closeServer(server) {
  server.closeAllConnections?.();
  server.close();
}

function requestBody(req) {
  return new Promise((resolve) => {
    let body = "";
    req.on("data", (chunk) => { body += chunk; });
    req.on("end", () => resolve(body));
  });
}

function send(res, status, body) {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(JSON.stringify(body));
}

test("stored gateway key reaches every managed-agent injection without entering config.json", async () => {
  const paths = isolatedEnv();
  const credentials = {
    access_token: makeToken(),
    refresh_token: "refresh-secret",
    gateway_api_key: "cave_live_stored_key",
    gateway_key_id: "key-1",
    project_id: "project-1",
  };
  writeConnectedFixture(paths, "https://api.caveman.cloud", credentials);
  const openclawBase = join(paths.home, "openclaw.json");
  writeFileSync(openclawBase, JSON.stringify({
    agents: { defaults: { model: { primary: "openai/gpt-test" } } },
    models: { providers: { openai: { apiKey: "${OPENAI_API_KEY}", models: [{ id: "gpt-test", name: "GPT Test" }] } } },
  }));
  paths.env.OPENCLAW_CONFIG_PATH = openclawBase;
  paths.env.OPENAI_API_KEY = "upstream-openai-key";
  paths.env.ANTHROPIC_AUTH_TOKEN = "upstream-anthropic-token";

  const previous = new Map();
  for (const [name, value] of Object.entries(paths.env)) {
    previous.set(name, process.env[name]);
    process.env[name] = value;
  }
  delete process.env.CAVE_API_KEY;
  delete process.env.CAVE_TOKEN;

  try {
    const { buildWrapEnv } = await import(`${pathToFileURL(cli).href}?stored-credential`);
    const { PROFILES } = await import(pathToFileURL(agentsModule).href);
    const profile = (id) => {
      const found = PROFILES.find((candidate) => candidate.id === id);
      assert.ok(found, `missing generated profile ${id}`);
      return found;
    };
    const gateway = "https://gateway.caveman.cloud";

    const raw = buildWrapEnv(undefined, gateway);
    assert.equal(raw.CAVE_API_KEY, credentials.gateway_api_key);

    const claude = buildWrapEnv(profile("claude"), gateway);
    assert.equal(claude.CAVE_API_KEY, credentials.gateway_api_key);
    assert.equal(claude.ANTHROPIC_AUTH_TOKEN, credentials.gateway_api_key);

    const localClaude = buildWrapEnv(profile("claude"), "http://127.0.0.1:8787");
    assert.equal(localClaude.CAVE_API_KEY, undefined, "local agents must not receive the hosted gateway key");
    assert.equal(localClaude.ANTHROPIC_AUTH_TOKEN, "upstream-anthropic-token", "local provider auth must remain the user's upstream credential");

    const opencode = buildWrapEnv(profile("opencode"), gateway);
    assert.equal(opencode.CAVE_API_KEY, credentials.gateway_api_key);
    const opencodeConfig = JSON.parse(opencode.OPENCODE_CONFIG_CONTENT);
    assert.equal(opencodeConfig.provider.caveman.options.apiKey, "{env:CAVE_API_KEY}");

    const hermes = buildWrapEnv(profile("hermes"), gateway);
    assert.equal(hermes.CAVE_API_KEY, credentials.gateway_api_key);
    assert.equal(hermes.CAVEMAN_API_KEY, credentials.gateway_api_key);
    assert.equal(hermes.CUSTOM_API_KEY, undefined, "Hermes must not receive the generic custom-provider key var");

    const openclaw = buildWrapEnv(profile("openclaw"), gateway);
    const openclawConfig = JSON.parse(readFileSync(openclaw.OPENCLAW_CONFIG_PATH, "utf8"));
    assert.equal(openclaw.CAVE_API_KEY, credentials.gateway_api_key);
    assert.equal(openclawConfig.models.providers.caveman.apiKey, credentials.gateway_api_key);

    assert.doesNotMatch(readFileSync(paths.configPath, "utf8"), /refresh-secret|cave_live_stored_key/);
  } finally {
    for (const [name, value] of previous) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
    delete process.env.CAVE_API_KEY;
    delete process.env.CAVE_TOKEN;
  }
});

test("device login stores the complete grant only in a mode-0600 credential envelope", { skip: "Cloud login disabled during beta" }, async () => {
  const paths = isolatedEnv();
  const accessToken = makeToken({ org: "org-device" });
  mkdirSync(dirname(paths.credentialsPath), { recursive: true });
  writeFileSync(paths.credentialsPath, "old-secret", { mode: 0o644 });
  chmodSync(paths.credentialsPath, 0o644);
  writeFileSync(paths.configPath, JSON.stringify({ futureField: { keep: true } }), { mode: 0o644 });
  chmodSync(paths.configPath, 0o644);

  const acknowledgements = [];
  const server = createServer(async (req, res) => {
    const rawBody = await requestBody(req);
    if (req.url === "/api/v1/auth/device/code") {
      send(res, 200, { device_code: "device-1", user_code: "ABCD-2345", verification_uri: "https://app.caveman.cloud/activate", expires_in: 60, interval: 0 });
      return;
    }
    if (req.url === "/api/v1/auth/device/token") {
      send(res, 200, {
        access_token: accessToken,
        refresh_token: "refresh-device",
        gateway_api_key: "cave_live_device",
        gateway_key_id: "key-device",
        project_id: "project-device",
        gateway_url: "https://gateway.caveman.cloud",
        delivery_ack_token: "ack-device",
      });
      return;
    }
    if (req.url === "/api/v1/auth/device/ack") {
      acknowledgements.push({ authorization: req.headers.authorization, body: JSON.parse(rawBody) });
      if (acknowledgements.length === 1) {
        send(res, 503, { error: { code: "cave_device_unavailable" } });
        return;
      }
      send(res, 200, { acknowledged: true });
      return;
    }
    send(res, 404, {});
  });
  const port = await listen(server);

  try {
    const result = await runCli(["login", "--base-url", `http://127.0.0.1:${port}`], paths.env);
    assert.equal(result.code, 0, result.stderr);
    assert.deepEqual(acknowledgements, [1, 2].map(() => ({
      authorization: `Bearer ${accessToken}`,
      body: { device_code: "device-1", ack_token: "ack-device" },
    })));
    assert.deepEqual(JSON.parse(readFileSync(paths.credentialsPath, "utf8")), {
      access_token: accessToken,
      refresh_token: "refresh-device",
      gateway_api_key: "cave_live_device",
      gateway_key_id: "key-device",
      project_id: "project-device",
    });
    assert.equal(statSync(paths.credentialsPath).mode & 0o777, 0o600);
    assert.equal(statSync(paths.configPath).mode & 0o777, 0o600);
    const configText = readFileSync(paths.configPath, "utf8");
    const config = JSON.parse(configText);
    assert.equal(config.tokenStore, "file");
    assert.equal(config.organizationId, "org-device");
    assert.equal(config.projectId, "project-device");
    assert.equal(config.gatewayUrl, "https://gateway.caveman.cloud");
    assert.deepEqual(config.futureField, { keep: true });
    assert.doesNotMatch(configText, /refresh-device|cave_live_device|key-device/);
  } finally {
    closeServer(server);
  }
});

test("device login does not acknowledge until config persistence succeeds", { skip: "Cloud login disabled during beta" }, async () => {
  const paths = isolatedEnv();
  const accessToken = makeToken({ org: "org-device-config-failure" });
  mkdirSync(dirname(paths.credentialsPath), { recursive: true });
  // `saveConfig` must fail after the credential envelope write but before the
  // server receipt fence. A directory at config.json makes the write failure
  // deterministic without touching any host state.
  mkdirSync(paths.configPath);
  let acknowledgements = 0;
  const server = createServer(async (req, res) => {
    await requestBody(req);
    if (req.url === "/api/v1/auth/device/code") {
      send(res, 200, { device_code: "device-config-failure", user_code: "ABCD-2345", verification_uri: "https://app.caveman.cloud/activate", expires_in: 60, interval: 0 });
      return;
    }
    if (req.url === "/api/v1/auth/device/token") {
      send(res, 200, { access_token: accessToken, refresh_token: "refresh-device", gateway_api_key: "cave_live_device", gateway_key_id: "key-device", project_id: "project-device", delivery_ack_token: "ack-device" });
      return;
    }
    if (req.url === "/api/v1/auth/device/ack") {
      acknowledgements++;
      send(res, 200, { acknowledged: true });
      return;
    }
    send(res, 404, {});
  });
  const port = await listen(server);
  try {
    const result = await runCli(["login", "--base-url", `http://127.0.0.1:${port}`], paths.env);
    assert.notEqual(result.code, 0);
    assert.equal(acknowledgements, 0, "ACK must wait for config persistence");
    assert.equal(JSON.parse(readFileSync(paths.credentialsPath, "utf8")).gateway_api_key, "cave_live_device");
    assert.doesNotMatch(result.stdout, /"authenticated": true/);
  } finally {
    closeServer(server);
  }
});

test("device login keeps credentials but prints no success when ACK is rejected", { skip: "Cloud login disabled during beta" }, async () => {
  const paths = isolatedEnv();
  const accessToken = makeToken({ org: "org-device-ack-failure" });
  mkdirSync(dirname(paths.credentialsPath), { recursive: true });
  let acknowledgements = 0;
  const server = createServer(async (req, res) => {
    await requestBody(req);
    if (req.url === "/api/v1/auth/device/code") {
      send(res, 200, { device_code: "device-ack-failure", user_code: "ABCD-2345", verification_uri: "https://app.caveman.cloud/activate", expires_in: 60, interval: 0 });
      return;
    }
    if (req.url === "/api/v1/auth/device/token") {
      send(res, 200, { access_token: accessToken, refresh_token: "refresh-device", gateway_api_key: "cave_live_device", gateway_key_id: "key-device", project_id: "project-device", delivery_ack_token: "ack-device" });
      return;
    }
    if (req.url === "/api/v1/auth/device/ack") {
      acknowledgements++;
      send(res, 400, { error: { code: "invalid_grant" } });
      return;
    }
    send(res, 404, {});
  });
  const port = await listen(server);
  try {
    const result = await runCli(["login", "--base-url", `http://127.0.0.1:${port}`], paths.env);
    assert.notEqual(result.code, 0);
    assert.equal(acknowledgements, 1, "terminal ACK rejection must not be retried forever");
    assert.match(result.stderr, /delivery acknowledgement failed/);
    assert.doesNotMatch(result.stdout, /"authenticated": true/);
    assert.equal(JSON.parse(readFileSync(paths.credentialsPath, "utf8")).gateway_api_key, "cave_live_device");
  } finally {
    closeServer(server);
  }
});

test("an expiring access token refreshes proactively and preserves the project gateway credential", async () => {
  const paths = isolatedEnv();
  const oldAccess = makeToken({ exp: Math.floor(Date.now() / 1000) + 30 });
  const newAccess = makeToken({ exp: Math.floor(Date.now() / 1000) + 3600 });
  writeConnectedFixture(paths, "", {
    access_token: oldAccess,
    refresh_token: "refresh-old",
    gateway_api_key: "cave_live_preserved",
    gateway_key_id: "key-preserved",
    project_id: "project-preserved",
  });
  const seen = { refresh: null, auth: null };
  const server = createServer(async (req, res) => {
    const rawBody = await requestBody(req);
    if (req.url === "/api/v1/auth/refresh") {
      seen.refresh = JSON.parse(rawBody);
      send(res, 200, { access_token: newAccess, refresh_token: "refresh-new" });
      return;
    }
    if (req.url === "/api/v1/auth/me") {
      seen.auth = req.headers.authorization;
      send(res, 200, { user: { email: "owner@example.com" } });
      return;
    }
    send(res, 404, {});
  });
  const port = await listen(server);
  const config = JSON.parse(readFileSync(paths.configPath, "utf8"));
  config.baseURL = `http://127.0.0.1:${port}`;
  writeFileSync(paths.configPath, JSON.stringify(config, null, 2));

  try {
    const result = await runCli(["whoami"], paths.env);
    assert.equal(result.code, 0, result.stderr);
    assert.deepEqual(seen.refresh, { refresh_token: "refresh-old" });
    assert.equal(seen.auth, `Bearer ${newAccess}`);
    assert.deepEqual(JSON.parse(readFileSync(paths.credentialsPath, "utf8")), {
      access_token: newAccess,
      refresh_token: "refresh-new",
      gateway_api_key: "cave_live_preserved",
      gateway_key_id: "key-preserved",
      project_id: "project-preserved",
    });
    assert.equal(statSync(paths.credentialsPath).mode & 0o777, 0o600);
    assert.doesNotMatch(readFileSync(paths.configPath, "utf8"), /refresh-new|cave_live_preserved/);
  } finally {
    closeServer(server);
  }
});

test("logout revokes both remote credentials before clearing all local connected state", async () => {
  const paths = isolatedEnv();
  const accessToken = makeToken();
  const seen = { keyAuth: null, logoutAuth: null, logout: null };
  const server = createServer(async (req, res) => {
    const rawBody = await requestBody(req);
    if (req.url === "/api/v1/projects/project-logout/keys/key-logout/revoke") {
      seen.keyAuth = req.headers.authorization;
      send(res, 200, {});
      return;
    }
    if (req.url === "/api/v1/auth/logout") {
      seen.logoutAuth = req.headers.authorization;
      seen.logout = JSON.parse(rawBody);
      send(res, 200, {});
      return;
    }
    send(res, 404, {});
  });
  const port = await listen(server);
  writeConnectedFixture(paths, `http://127.0.0.1:${port}`, {
    access_token: accessToken,
    refresh_token: "refresh-logout",
    gateway_api_key: "cave_live_logout",
    gateway_key_id: "key-logout",
    project_id: "project-logout",
  }, { organizationId: "org-logout", futureField: { keep: true } });

  try {
    const result = await runCli(["logout"], paths.env);
    assert.equal(result.code, 0, result.stderr);
    assert.equal(seen.keyAuth, `Bearer ${accessToken}`);
    assert.equal(seen.logoutAuth, `Bearer ${accessToken}`);
    assert.deepEqual(seen.logout, { refresh_token: "refresh-logout" });
    assert.equal(existsSync(paths.credentialsPath), false);
    const config = JSON.parse(readFileSync(paths.configPath, "utf8"));
    assert.deepEqual(config.futureField, { keep: true });
    for (const field of ["token", "tokenStore", "gatewayUrl", "organizationId", "projectId", "logoutPendingLocalCleanup"]) {
      assert.equal(field in config, false, `logout must clear ${field}`);
    }
  } finally {
    closeServer(server);
  }
});

test("logout revokes an access-only stored session before deleting credentials", async () => {
  const paths = isolatedEnv();
  const accessToken = makeToken();
  let seenAuth = null;
  let seenBody = null;
  const server = createServer(async (req, res) => {
    seenAuth = req.headers.authorization;
    seenBody = await requestBody(req);
    send(res, req.url === "/api/v1/auth/logout" ? 200 : 404, {});
  });
  const port = await listen(server);
  writeConnectedFixture(paths, `http://127.0.0.1:${port}`, {
    access_token: accessToken,
    project_id: "project-access-only",
  });

  try {
    const result = await runCli(["logout"], paths.env);
    assert.equal(result.code, 0, result.stderr);
    assert.equal(seenAuth, `Bearer ${accessToken}`);
    assert.equal(seenBody, "", "access-only logout must omit a fake refresh body");
    assert.equal(existsSync(paths.credentialsPath), false);
    assert.match(result.stdout, /"logged_out": true/);
  } finally {
    closeServer(server);
  }
});

test("logout revokes CAVE_TOKEN remotely and reports that parent environment still owns it", async () => {
  const paths = isolatedEnv();
  const accessToken = makeToken();
  let seenAuth = null;
  const server = createServer(async (req, res) => {
    seenAuth = req.headers.authorization;
    await requestBody(req);
    send(res, req.url === "/api/v1/auth/logout" ? 200 : 404, {});
  });
  const port = await listen(server);
  paths.env.CAVE_TOKEN = accessToken;
  paths.env.CAVE_API_URL = `http://127.0.0.1:${port}`;

  try {
    const result = await runCli(["logout"], paths.env);
    assert.equal(result.code, 0, result.stderr);
    assert.equal(seenAuth, `Bearer ${accessToken}`);
    assert.match(result.stderr, /CAVE_TOKEN remains set by the parent environment/);
    assert.match(result.stdout, /"remote_session_revoked": true/);
    assert.match(result.stdout, /"external_token_cleared": false/);
  } finally {
    closeServer(server);
  }
});

test("logout keeps local credentials retryable when gateway-key revocation fails", async () => {
  const paths = isolatedEnv();
  let sessionRevocationAttempts = 0;
  const server = createServer(async (req, res) => {
    await requestBody(req);
    if (req.url === "/api/v1/projects/project-failure/keys/key-failure/revoke") {
      send(res, 503, {});
      return;
    }
    if (req.url === "/api/v1/auth/logout") sessionRevocationAttempts++;
    send(res, 200, {});
  });
  const port = await listen(server);
  writeConnectedFixture(paths, `http://127.0.0.1:${port}`, {
    access_token: makeToken(),
    refresh_token: "refresh-failure",
    gateway_api_key: "cave_live_failure",
    gateway_key_id: "key-failure",
    project_id: "project-failure",
  });

  try {
    const result = await runCli(["logout"], paths.env);
    assert.notEqual(result.code, 0);
    assert.doesNotMatch(result.stdout, /"logged_out": true/);
    assert.match(result.stderr, /remote gateway key revocation failed \(HTTP 503\)/);
    assert.equal(sessionRevocationAttempts, 0, "session must remain usable to retry gateway-key revocation");
    assert.equal(existsSync(paths.credentialsPath), true);
    assert.equal(JSON.parse(readFileSync(paths.credentialsPath, "utf8")).gateway_api_key, "cave_live_failure");
    assert.equal(JSON.parse(readFileSync(paths.configPath, "utf8")).tokenStore, "file");
  } finally {
    closeServer(server);
  }
});

test("logout retries session revocation after the gateway key was already revoked", async () => {
  const paths = isolatedEnv();
  let keyRevocationAttempts = 0;
  let sessionRevocationAttempts = 0;
  const server = createServer(async (req, res) => {
    await requestBody(req);
    if (req.url === "/api/v1/projects/project-session-failure/keys/key-session-failure/revoke") {
      keyRevocationAttempts++;
      if (keyRevocationAttempts === 1) send(res, 200, {});
      else send(res, 404, { error: { code: "cave_key_not_found", message: "Key not found." } });
      return;
    }
    if (req.url === "/api/v1/auth/logout") {
      sessionRevocationAttempts++;
      send(res, sessionRevocationAttempts === 1 ? 503 : 200, {});
      return;
    }
    send(res, 404, {});
  });
  const port = await listen(server);
  writeConnectedFixture(paths, `http://127.0.0.1:${port}`, {
    access_token: makeToken(),
    refresh_token: "refresh-session-failure",
    gateway_api_key: "cave_live_session_failure",
    gateway_key_id: "key-session-failure",
    project_id: "project-session-failure",
  });

  try {
    const first = await runCli(["logout"], paths.env);
    assert.notEqual(first.code, 0);
    assert.doesNotMatch(first.stdout, /"logged_out": true/);
    assert.match(first.stderr, /remote session revocation failed \(HTTP 503\)/);
    assert.equal(existsSync(paths.credentialsPath), true);
    assert.equal(JSON.parse(readFileSync(paths.configPath, "utf8")).tokenStore, "file");

    const retry = await runCli(["logout"], paths.env);
    assert.equal(retry.code, 0, retry.stderr);
    assert.match(retry.stdout, /"logged_out": true/);
    assert.equal(keyRevocationAttempts, 2);
    assert.equal(sessionRevocationAttempts, 2);
    assert.equal(existsSync(paths.credentialsPath), false);
  } finally {
    closeServer(server);
  }
});

test("logout fails without clearing config when macOS Keychain deletion fails", async () => {
  const paths = isolatedEnv();
  const credentials = {
    access_token: makeToken(),
    refresh_token: "refresh-keychain-failure",
    gateway_api_key: "cave_live_keychain_failure",
    gateway_key_id: "key-keychain-failure",
    project_id: "project-keychain-failure",
  };
  const securityLog = installFakeSecurity(paths, JSON.stringify(credentials), 51);
  let remoteRevocationRequests = 0;
  const server = createServer(async (req, res) => {
    await requestBody(req);
    remoteRevocationRequests++;
    send(res, remoteRevocationRequests <= 2 ? 200 : 401, {});
  });
  const port = await listen(server);
  writeFileSync(paths.configPath, JSON.stringify({
    baseURL: `http://127.0.0.1:${port}`,
    tokenStore: "keychain",
    projectId: credentials.project_id,
    gatewayUrl: "https://gateway.caveman.cloud",
  }, null, 2), { mode: 0o600 });

  try {
    const result = await runCli(["logout"], paths.env);
    assert.notEqual(result.code, 0);
    assert.doesNotMatch(result.stdout, /"logged_out": true/);
    assert.match(result.stderr, /could not remove credentials from macOS Keychain/);
    assert.equal(JSON.parse(readFileSync(paths.configPath, "utf8")).tokenStore, "keychain");
    const calls = readFileSync(securityLog, "utf8").trim().split("\n").map((line) => JSON.parse(line));
    assert.ok(calls.some((call) => call[0] === "delete-generic-password"));

    paths.env.CAVE_TEST_SECURITY_DELETE_EXIT = "0";
    paths.env.CAVE_TEST_SECURITY_SECRET = JSON.stringify({
      ...credentials,
      access_token: makeToken({ exp: Math.floor(Date.now() / 1000) + 30 }),
    });
    const retry = await runCli(["logout"], paths.env);
    assert.equal(retry.code, 0, retry.stderr);
    assert.match(retry.stdout, /"logged_out": true/);
    assert.equal(remoteRevocationRequests, 2, "local cleanup retry must not repeat completed remote revocations");
    assert.equal("tokenStore" in JSON.parse(readFileSync(paths.configPath, "utf8")), false);
  } finally {
    closeServer(server);
  }
});

test("logout never claims success when the local credential cannot be removed", async () => {
  const paths = isolatedEnv();
  writeConnectedFixture(paths, "http://127.0.0.1:1", {
    access_token: makeToken(),
    project_id: "project-local-failure",
  });
  chmodSync(paths.caveHome, 0o500);

  try {
    const result = await runCli(["logout"], paths.env);
    assert.notEqual(result.code, 0);
    assert.doesNotMatch(result.stdout, /"logged_out": true/);
    assert.equal(existsSync(paths.credentialsPath), true, "failed removal must not be reported as success");
  } finally {
    chmodSync(paths.caveHome, 0o700);
  }
});
