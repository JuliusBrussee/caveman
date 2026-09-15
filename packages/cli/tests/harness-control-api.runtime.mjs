import { test } from "node:test";
import assert from "node:assert";
import { mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { runCli, stubControlApi } from "./harness/index.mjs";

const cli = process.env.CAVEMAN_TEST_CLI ?? join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function isolatedEnv() {
  const home = mkdtempSync(join(tmpdir(), "cave-control-home-"));
  const caveHome = mkdtempSync(join(tmpdir(), "cave-control-bin-"));
  const env = {
    ...process.env,
    HOME: home,
    CAVEMAN_HOME: caveHome,
    CAVE_NO_KEYCHAIN: "1",
    CAVEMAN_TELEMETRY: "0",
  };
  delete env.CAVE_TOKEN;
  return { env, home, caveHome };
}

test("shared CLI runner terminates a hung child at its deadline", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-runner-timeout-"));
  const hanging = join(dir, "hang.mjs");
  writeFileSync(hanging, 'process.stdout.write("started\\n"); setInterval(() => {}, 1000);\n');
  const startedAt = Date.now();
  try {
    await assert.rejects(
      runCli(hanging, [], { timeoutMs: 250 }),
      (error) => {
        assert.match(error.message, /CLI timed out after 250ms/);
        assert.match(error.stdout, /started/);
        return true;
      },
    );
    assert.ok(Date.now() - startedAt < 2_000);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("stub control-api drives first-poll login, entitlement cache, and whoami", { skip: "Cloud login disabled during beta" }, async () => {
  const api = await stubControlApi();
  const { env, home, caveHome } = isolatedEnv();
  try {
    const startedAt = Date.now();
    const login = await runCli(cli, ["login", "--base-url", api.baseURL], { env });
    const elapsedMs = Date.now() - startedAt;

    assert.equal(login.code, 0, login.stderr);
    assert.ok(elapsedMs < 2_000, `login took ${elapsedMs}ms`);

    const configPath = join(home, ".caveman-cloud", "config.json");
    const config = JSON.parse(readFileSync(configPath, "utf8"));
    assert.equal(statSync(configPath).mode & 0o777, 0o600);
    assert.equal(config.wrapEntitlement?.entitled, true);
    assert.equal(config.wrapEntitlement?.plan, "free");
    assert.ok(Date.parse(config.wrapEntitlement?.expires_at) > Date.now());

    const whoami = await runCli(cli, ["whoami"], { env });
    assert.equal(whoami.code, 0, whoami.stderr);
    assert.equal(api.capturedAuth(), `Bearer ${api.token}`);
    const entitlementRequest = api.requests.find(
      (request) => request.method === "POST" && request.path === "/api/v1/me/wrap-entitlement",
    );
    assert.equal(entitlementRequest?.authorization, `Bearer ${api.token}`);
    const entitlementBody = JSON.parse(entitlementRequest?.body ?? "{}");
    assert.match(entitlementBody.device_hash, /^[a-f0-9]{64}$/);
    assert.equal(typeof entitlementBody.device_name, "string");
    assert.ok(api.requests.some((request) => request.method === "GET" && request.path === "/api/v1/auth/me"));

    const unauthenticated = await fetch(`${api.baseURL}/api/v1/auth/me`);
    assert.equal(unauthenticated.status, 401);
    const malformedDevice = await fetch(`${api.baseURL}/api/v1/me/wrap-entitlement`, {
      method: "POST",
      headers: { authorization: `Bearer ${api.token}`, "content-type": "application/json" },
      body: "{}",
    });
    assert.equal(malformedDevice.status, 400);
  } finally {
    try {
      await api.close();
    } finally {
      rmSync(home, { recursive: true, force: true });
      rmSync(caveHome, { recursive: true, force: true });
    }
  }
});

test("stub control-api exposes seat-wall login variant without minting entitlement", { skip: "Cloud login disabled during beta" }, async () => {
  const api = await stubControlApi({ seatWalled: true });
  const { env, home, caveHome } = isolatedEnv();
  try {
    const login = await runCli(cli, ["login", "--base-url", api.baseURL], { env });
    assert.equal(login.code, 0, login.stderr);
    assert.match(login.stderr, /seat|device limit|compression is off/i);

    const config = JSON.parse(readFileSync(join(home, ".caveman-cloud", "config.json"), "utf8"));
    assert.equal(config.wrapEntitlement, undefined);
    assert.ok(api.requests.some((request) => request.path === "/api/v1/me/wrap-entitlement"));
  } finally {
    try {
      await api.close();
    } finally {
      rmSync(home, { recursive: true, force: true });
      rmSync(caveHome, { recursive: true, force: true });
    }
  }
});
