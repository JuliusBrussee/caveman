import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");
const cliModule = pathToFileURL(join(here, "..", "dist", "index.js")).href;
const agentsModule = pathToFileURL(join(here, "..", "dist", "agents.generated.js")).href;

function claudeProfile(profiles) {
  const profile = profiles.find((candidate) => candidate.id === "claude");
  assert.ok(profile, "missing generated Claude Code profile");
  return profile;
}

function restoreEnv(snapshot) {
  for (const key of Object.keys(process.env)) {
    if (!(key in snapshot)) delete process.env[key];
  }
  for (const [key, value] of Object.entries(snapshot)) process.env[key] = value;
}

function clearBedrockCredentialEnv() {
  delete process.env.AWS_BEARER_TOKEN_BEDROCK;
  delete process.env.AWS_ACCESS_KEY_ID;
  delete process.env.AWS_SECRET_ACCESS_KEY;
  delete process.env.AWS_SESSION_TOKEN;
}

test("explicit Bedrock Claude wrap selects native Runtime without disturbing AWS credentials", async () => {
  const snapshot = { ...process.env };
  try {
    process.env.CAVEMAN_WRAP_PROVIDER = "bedrock";
    delete process.env.CAVEMAN_BEDROCK_ENDPOINT;
    process.env.AWS_REGION = "us-east-1";
    process.env.AWS_BEARER_TOKEN_BEDROCK = "bedrock-token";
    delete process.env.ANTHROPIC_BASE_URL;
    delete process.env.ANTHROPIC_AUTH_TOKEN;

    const { buildWrapEnv } = await import(`${cliModule}?bedrock-runtime`);
    const { PROFILES } = await import(agentsModule);
    const env = buildWrapEnv(claudeProfile(PROFILES), "http://127.0.0.1:8787");

    assert.equal(env.CLAUDE_CODE_USE_BEDROCK, "1");
    assert.equal(env.CLAUDE_CODE_SKIP_BEDROCK_AUTH, undefined);
    assert.equal(env.ANTHROPIC_BEDROCK_BASE_URL, "http://127.0.0.1:8787/w/claude/bedrock");
    assert.equal(env.AWS_REGION, "us-east-1");
    assert.equal(env.AWS_BEARER_TOKEN_BEDROCK, "bedrock-token");
    assert.equal(env.ANTHROPIC_BASE_URL, undefined);
    assert.equal(env.ANTHROPIC_AUTH_TOKEN, undefined);
    assert.equal(env.CLAUDE_CODE_USE_MANTLE, undefined);
  } finally {
    restoreEnv(snapshot);
  }
});

test("managed Bedrock Runtime sends the hosted gateway key through Claude custom headers", async () => {
  const snapshot = { ...process.env };
  try {
    process.env.CAVEMAN_WRAP_PROVIDER = "bedrock";
    delete process.env.CAVEMAN_BEDROCK_ENDPOINT;
    process.env.CAVE_API_KEY = "cave_live_runtime";
    clearBedrockCredentialEnv();
    process.env.ANTHROPIC_CUSTOM_HEADERS = [
      "X-Trace-Id: trace-stored",
      "x-cave-upstream-key: stale-must-not-override-stored",
    ].join("\n");

    const { buildWrapEnv } = await import(`${cliModule}?bedrock-managed-runtime`);
    const { PROFILES } = await import(agentsModule);
    const env = buildWrapEnv(claudeProfile(PROFILES), "https://gateway.example");

    assert.equal(env.CLAUDE_CODE_USE_BEDROCK, "1");
    assert.equal(env.CLAUDE_CODE_SKIP_BEDROCK_AUTH, undefined);
    assert.equal(env.ANTHROPIC_BEDROCK_BASE_URL, "https://gateway.example/w/claude/bedrock");
    assert.equal(
      env.ANTHROPIC_CUSTOM_HEADERS,
      [
        "X-Trace-Id: trace-stored",
        "x-cave-api-key: cave_live_runtime",
      ].join("\n"),
    );
    assert.doesNotMatch(env.ANTHROPIC_CUSTOM_HEADERS, /x-cave-upstream-key/i);
  } finally {
    restoreEnv(snapshot);
  }
});

test("managed Bedrock Runtime forwards the bearer upstream key with precedence and safe replacement", async () => {
  const snapshot = { ...process.env };
  try {
    process.env.CAVEMAN_WRAP_PROVIDER = "bedrock";
    delete process.env.CAVEMAN_BEDROCK_ENDPOINT;
    process.env.CAVE_API_KEY = "cave_live_runtime";
    process.env.AWS_BEARER_TOKEN_BEDROCK = "bedrock-bearer-fresh";
    process.env.AWS_ACCESS_KEY_ID = "AKIAIGNORED";
    process.env.AWS_SECRET_ACCESS_KEY = "ignored-secret";
    process.env.ANTHROPIC_CUSTOM_HEADERS = [
      "X-Trace-Id: trace-1",
      "X-CAVE-UPSTREAM-KEY: stale-upstream",
      "x-cave-upstream-key",
    ].join("\r\n");

    const { buildWrapEnv } = await import(`${cliModule}?bedrock-managed-runtime-bearer`);
    const { PROFILES } = await import(agentsModule);
    const env = buildWrapEnv(claudeProfile(PROFILES), "https://gateway.example");

    assert.equal(
      env.ANTHROPIC_CUSTOM_HEADERS,
      [
        "X-Trace-Id: trace-1",
        "x-cave-api-key: cave_live_runtime",
        "x-cave-upstream-key: bedrock-bearer-fresh",
      ].join("\n"),
    );
    assert.equal(
      env.ANTHROPIC_CUSTOM_HEADERS.toLowerCase().split("x-cave-upstream-key").length - 1,
      1,
      "managed wrap must emit exactly one current upstream-auth header",
    );
  } finally {
    restoreEnv(snapshot);
  }
});

test("managed Bedrock Runtime forwards a complete temporary IAM tuple", async () => {
  const snapshot = { ...process.env };
  try {
    process.env.CAVEMAN_WRAP_PROVIDER = "bedrock";
    process.env.CAVE_API_KEY = "cave_live_runtime";
    delete process.env.AWS_BEARER_TOKEN_BEDROCK;
    process.env.AWS_ACCESS_KEY_ID = "ASIAEXAMPLE";
    process.env.AWS_SECRET_ACCESS_KEY = "temporary-secret";
    process.env.AWS_SESSION_TOKEN = "temporary-session";
    process.env.ANTHROPIC_CUSTOM_HEADERS = "X-Trace-Id: trace-iam";

    const { buildWrapEnv } = await import(`${cliModule}?bedrock-managed-runtime-iam`);
    const { PROFILES } = await import(agentsModule);
    const env = buildWrapEnv(claudeProfile(PROFILES), "https://gateway.example");

    assert.equal(
      env.ANTHROPIC_CUSTOM_HEADERS,
      [
        "X-Trace-Id: trace-iam",
        "x-cave-api-key: cave_live_runtime",
        "x-cave-upstream-key: ASIAEXAMPLE:temporary-secret:temporary-session",
      ].join("\n"),
    );
  } finally {
    restoreEnv(snapshot);
  }
});

test("managed Bedrock wrap rejects a newline-bearing upstream credential", async () => {
  const snapshot = { ...process.env };
  try {
    process.env.CAVEMAN_WRAP_PROVIDER = "bedrock";
    process.env.CAVE_API_KEY = "cave_live_runtime";
    process.env.AWS_BEARER_TOKEN_BEDROCK = "bedrock-token\nX-Evil: injected";
    delete process.env.AWS_ACCESS_KEY_ID;
    delete process.env.AWS_SECRET_ACCESS_KEY;
    delete process.env.AWS_SESSION_TOKEN;

    const { buildWrapEnv } = await import(`${cliModule}?bedrock-managed-runtime-newline`);
    const { PROFILES } = await import(agentsModule);
    assert.throws(
      () => buildWrapEnv(claudeProfile(PROFILES), "https://gateway.example"),
      /AWS_BEARER_TOKEN_BEDROCK must not contain a newline/,
    );

    delete process.env.AWS_BEARER_TOKEN_BEDROCK;
    process.env.AWS_ACCESS_KEY_ID = "ASIAEXAMPLE";
    process.env.AWS_SECRET_ACCESS_KEY = "temporary-secret\nX-Evil: injected";
    assert.throws(
      () => buildWrapEnv(claudeProfile(PROFILES), "https://gateway.example"),
      /AWS_SECRET_ACCESS_KEY must not contain a newline/,
    );
  } finally {
    restoreEnv(snapshot);
  }
});

test("managed Bedrock wrap rejects incomplete IAM environment credentials", async () => {
  const snapshot = { ...process.env };
  try {
    process.env.CAVEMAN_WRAP_PROVIDER = "bedrock";
    process.env.CAVE_API_KEY = "cave_live_runtime";
    delete process.env.AWS_BEARER_TOKEN_BEDROCK;
    process.env.AWS_ACCESS_KEY_ID = "AKIAINCOMPLETE";
    delete process.env.AWS_SECRET_ACCESS_KEY;
    delete process.env.AWS_SESSION_TOKEN;

    const { buildWrapEnv } = await import(`${cliModule}?bedrock-managed-runtime-incomplete-iam`);
    const { PROFILES } = await import(agentsModule);
    assert.throws(
      () => buildWrapEnv(claudeProfile(PROFILES), "https://gateway.example"),
      /AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set together/,
    );
  } finally {
    restoreEnv(snapshot);
  }
});

test("managed Bedrock preserves custom headers and replaces every stale Cave key once", async () => {
  const snapshot = { ...process.env };
  try {
    process.env.CAVEMAN_WRAP_PROVIDER = "bedrock";
    process.env.CAVE_API_KEY = "cave_live_fresh";
    clearBedrockCredentialEnv();
    process.env.ANTHROPIC_CUSTOM_HEADERS = [
      "X-Trace-Id: trace-1",
      "X-CAVE-API-KEY: stale-1",
      "x-team: platform",
      " x-cave-api-key : stale-2",
      "x-Cave-Api-Key",
    ].join("\r\n");

    const { buildWrapEnv } = await import(`${cliModule}?bedrock-managed-merge`);
    const { PROFILES } = await import(agentsModule);
    const env = buildWrapEnv(claudeProfile(PROFILES), "https://gateway.example");

    assert.equal(
      env.ANTHROPIC_CUSTOM_HEADERS,
      [
        "X-Trace-Id: trace-1",
        "x-team: platform",
        "x-cave-api-key: cave_live_fresh",
      ].join("\n"),
    );
    assert.equal(
      env.ANTHROPIC_CUSTOM_HEADERS.toLowerCase().split("x-cave-api-key").length - 1,
      1,
      "managed wrap must emit exactly one current gateway-auth header",
    );
  } finally {
    restoreEnv(snapshot);
  }
});

test("explicit Mantle endpoint selects only Claude Code's opt-in Mantle contract", async () => {
  const snapshot = { ...process.env };
  try {
    process.env.CAVEMAN_WRAP_PROVIDER = "bedrock";
    process.env.CAVEMAN_BEDROCK_ENDPOINT = "mantle";
    process.env.CAVE_API_KEY = "cave_live_mantle";
    clearBedrockCredentialEnv();
    process.env.AWS_BEARER_TOKEN_BEDROCK = "bedrock-mantle-token";
    process.env.CLAUDE_CODE_USE_BEDROCK = "1";
    process.env.ANTHROPIC_BEDROCK_BASE_URL = "https://stale.example";

    const { buildWrapEnv } = await import(`${cliModule}?bedrock-mantle`);
    const { PROFILES } = await import(agentsModule);
    const env = buildWrapEnv(claudeProfile(PROFILES), "https://gateway.example");

    assert.equal(env.CLAUDE_CODE_USE_MANTLE, "1");
    assert.equal(env.CLAUDE_CODE_SKIP_MANTLE_AUTH, "1");
    assert.equal(env.ANTHROPIC_BEDROCK_MANTLE_BASE_URL, "https://gateway.example/w/claude/bedrock/anthropic");
    assert.equal(env.CLAUDE_CODE_USE_BEDROCK, undefined);
    assert.equal(env.CLAUDE_CODE_SKIP_BEDROCK_AUTH, undefined);
    assert.equal(env.ANTHROPIC_BEDROCK_BASE_URL, undefined);
    assert.equal(
      env.ANTHROPIC_CUSTOM_HEADERS,
      [
        "x-cave-api-key: cave_live_mantle",
        "x-cave-upstream-key: bedrock-mantle-token",
      ].join("\n"),
    );
  } finally {
    restoreEnv(snapshot);
  }
});

test("Claude wrap keeps its existing Anthropic lane unless Bedrock is explicit", async () => {
  const snapshot = { ...process.env };
  try {
    delete process.env.CAVEMAN_WRAP_PROVIDER;
    process.env.CAVEMAN_BEDROCK_ENDPOINT = "mantle";
    delete process.env.ANTHROPIC_BASE_URL;

    const { buildWrapEnv } = await import(`${cliModule}?anthropic-default`);
    const { PROFILES } = await import(agentsModule);
    const env = buildWrapEnv(claudeProfile(PROFILES), "http://127.0.0.1:8787");

    assert.equal(env.ANTHROPIC_BASE_URL, "http://127.0.0.1:8787/w/claude");
    assert.equal(env.CLAUDE_CODE_USE_BEDROCK, undefined);
    assert.equal(env.CLAUDE_CODE_USE_MANTLE, undefined);
    assert.equal(env.ANTHROPIC_BEDROCK_BASE_URL, undefined);
    assert.equal(env.ANTHROPIC_BEDROCK_MANTLE_BASE_URL, undefined);
  } finally {
    restoreEnv(snapshot);
  }
});

test("local Bedrock wrap preserves custom headers without injecting hosted gateway auth", async () => {
  const snapshot = { ...process.env };
  try {
    process.env.CAVEMAN_WRAP_PROVIDER = "bedrock";
    process.env.CAVE_API_KEY = "cave_live_must_not_leak";
    process.env.AWS_BEARER_TOKEN_BEDROCK = "bedrock-local-token";
    process.env.ANTHROPIC_CUSTOM_HEADERS = "X-Trace-Id: local-trace";

    const { buildWrapEnv } = await import(`${cliModule}?bedrock-local-headers`);
    const { PROFILES } = await import(agentsModule);
    const env = buildWrapEnv(claudeProfile(PROFILES), "http://127.0.0.1:8787");

    assert.equal(env.ANTHROPIC_CUSTOM_HEADERS, "X-Trace-Id: local-trace");
    assert.doesNotMatch(env.ANTHROPIC_CUSTOM_HEADERS, /x-cave-api-key/i);
    assert.doesNotMatch(env.ANTHROPIC_CUSTOM_HEADERS, /x-cave-upstream-key/i);
  } finally {
    restoreEnv(snapshot);
  }
});

test("explicit Bedrock wrap rejects an unknown endpoint lane", async () => {
  const snapshot = { ...process.env };
  try {
    process.env.CAVEMAN_WRAP_PROVIDER = "bedrock";
    process.env.CAVEMAN_BEDROCK_ENDPOINT = "guess";
    const { buildWrapEnv } = await import(`${cliModule}?bedrock-invalid`);
    const { PROFILES } = await import(agentsModule);
    assert.throws(
      () => buildWrapEnv(claudeProfile(PROFILES), "http://127.0.0.1:8787"),
      /CAVEMAN_BEDROCK_ENDPOINT must be runtime or mantle/,
    );
  } finally {
    restoreEnv(snapshot);
  }
});

test("caveman wrap claude exposes the explicit native Bedrock lane to the child process", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-bedrock-wrap-home-"));
  const binDir = mkdtempSync(join(tmpdir(), "cave-bedrock-wrap-bin-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({
    wrap: { proxy: false, shrink: false, mcp: false, browse: false },
  }));
  writeFileSync(join(binDir, "claude"), `#!/usr/bin/env node
const keys = [
  "CLAUDE_CODE_USE_BEDROCK",
  "CLAUDE_CODE_SKIP_BEDROCK_AUTH",
  "ANTHROPIC_BEDROCK_BASE_URL",
  "AWS_REGION",
  "AWS_BEARER_TOKEN_BEDROCK",
  "ANTHROPIC_BASE_URL",
  "ANTHROPIC_AUTH_TOKEN"
];
process.stdout.write(JSON.stringify(Object.fromEntries(keys.map((key) => [key, process.env[key] ?? null]))));
`, { mode: 0o755 });

  const env = {
    ...process.env,
    NO_COLOR: "1",
    CAVEMAN_TELEMETRY: "0",
    HOME: home,
    CAVEMAN_HOME: join(home, ".caveman"),
    CAVE_GATEWAY_URL: "http://127.0.0.1:9",
    CAVEMAN_WRAP_PROVIDER: "bedrock",
    AWS_REGION: "us-east-1",
    AWS_BEARER_TOKEN_BEDROCK: "bedrock-token",
    PATH: `${binDir}:${process.env.PATH}`,
  };
  delete env.CAVEMAN_BEDROCK_ENDPOINT;
  delete env.ANTHROPIC_BASE_URL;
  delete env.ANTHROPIC_AUTH_TOKEN;

  const out = await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, "wrap", "claude"], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("error", reject);
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
  });

  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(JSON.parse(out.stdout), {
    CLAUDE_CODE_USE_BEDROCK: "1",
    CLAUDE_CODE_SKIP_BEDROCK_AUTH: null,
    ANTHROPIC_BEDROCK_BASE_URL: "http://127.0.0.1:9/w/claude/bedrock",
    AWS_REGION: "us-east-1",
    AWS_BEARER_TOKEN_BEDROCK: "bedrock-token",
    ANTHROPIC_BASE_URL: null,
    ANTHROPIC_AUTH_TOKEN: null,
  });
});
