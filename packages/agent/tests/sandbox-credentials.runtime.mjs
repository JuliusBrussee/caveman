import assert from "node:assert/strict";
import { readdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { test } from "node:test";

import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";
import {
  fauxAssistantMessage as upstreamFauxAssistantMessage,
  fauxProvider as upstreamFauxProvider,
  fauxToolCall,
} from "@earendil-works/pi-ai/providers/faux";
import { run } from "../dist/index.js";
import {
  buildSandboxToolEnv,
  SANDBOX_CREDENTIAL_ENV_BY_CAPABILITY,
  validateSandboxCredentialEnv,
} from "../dist/runtime.js";
import sandboxAgent from "./fixtures/sandbox-agent.mjs";

function fauxProvider() {
  const handle = upstreamFauxProvider({ provider: "anthropic" });
  const streamSimple = handle.provider.streamSimple.bind(handle.provider);
  return {
    ...handle,
    provider: {
      ...handle.provider,
      streamSimple: (...args) => withReportedReasoning(streamSimple(...args)),
    },
  };
}

function withReportedReasoning(source) {
  const output = createAssistantMessageEventStream();
  queueMicrotask(async () => {
    for await (const event of source) {
      const partial = event.partial === undefined
        ? {}
        : { partial: reportZeroReasoning(event.partial) };
      if (event.type === "done") {
        output.push({ ...event, ...partial, message: reportZeroReasoning(event.message) });
      } else if (event.type === "error") {
        output.push({ ...event, ...partial, error: reportZeroReasoning(event.error) });
      } else {
        output.push({ ...event, ...partial });
      }
    }
  });
  return output;
}

function reportZeroReasoning(message) {
  return { ...message, usage: { ...message.usage, reasoning: 0 } };
}

function fauxAssistantMessage(...args) {
  const message = upstreamFauxAssistantMessage(...args);
  return { ...message, usage: { ...message.usage, reasoning: message.usage.reasoning ?? 0 } };
}

async function toolWorkspaceNames() {
  const entries = await readdir(tmpdir(), { withFileTypes: true });
  return entries
    .filter((entry) => entry.name.startsWith("caveman-agent-tool-"))
    .map((entry) => entry.name)
    .sort();
}

async function runWithCredentialProfile(sandboxProfile) {
  const faux = fauxProvider();
  let observed = "";
  faux.setResponses([
    fauxAssistantMessage(fauxToolCall("modular_read", {}, { id: "credential-profile" })),
    (context) => {
      observed = JSON.stringify(context.messages);
      return fauxAssistantMessage("done");
    },
  ]);
  const before = await toolWorkspaceNames();
  const result = await run(sandboxAgent, "credential profile", {
    ensureRuntime: false,
    entryPath: "tests/fixtures/sandbox-agent.mjs",
    model: faux.getModel(),
    streamFn: faux.provider.streamSimple.bind(faux.provider),
    sandboxProfile,
  });
  const after = await toolWorkspaceNames();
  assert.equal(result.text, "done");
  assert.deepEqual(after, before);
  return observed;
}

const hostileNames = [
  "CAVEBENCH_RECEIPT_SEED_B64",
  "CAVEBENCH_RECEIPT_KEY_ID",
  "COSIGN_KEY",
  "COSIGN_PASSWORD",
  "AWS_ACCESS_KEY_ID",
  "AWS_SECRET_ACCESS_KEY",
  "AWS_SESSION_TOKEN",
  "GCP_ACCESS_TOKEN",
  "GOOGLE_APPLICATION_CREDENTIALS",
  "SCW_ACCESS_KEY",
  "SCW_SECRET_KEY",
  "SCW_DEFAULT_PROJECT_ID",
  "CAVE_DEPLOY_SMOKE_API_KEY",
  "CAVE_DEPLOY_SMOKE_UPSTREAM_KEY",
  "CAVE_BOOTSTRAP_TOKEN",
  "DATABASE_URL",
  "POSTGRES_URL",
  "PGPASSWORD",
  "DB_PASSWORD",
  "NODE_OPTIONS",
  "PATH",
  "aws_secret_access_key",
  "AWS_SECRET_DYNAMIC_123",
];

test("live profile credential names are exact, provider-scoped, and fail closed", () => {
  const exactProviderNames = Object.values(SANDBOX_CREDENTIAL_ENV_BY_CAPABILITY).flat();
  for (const name of exactProviderNames) {
    assert.doesNotThrow(() => validateSandboxCredentialEnv([name]), name);
  }
  // Google aliases are one capability; a profile cannot combine providers.
  assert.doesNotThrow(() => validateSandboxCredentialEnv(["GEMINI_API_KEY", "GOOGLE_API_KEY"]));
  assert.throws(
    () => validateSandboxCredentialEnv(["ANTHROPIC_API_KEY", "OPENAI_API_KEY"]),
    /cave_sandbox_credential_capability_ambiguous/,
  );

  for (const name of hostileNames) {
    assert.throws(
      () => validateSandboxCredentialEnv([name]),
      (error) => {
        assert.equal(error.message, "cave_sandbox_credential_env_not_allowlisted");
        assert.doesNotMatch(error.message, new RegExp(name.replaceAll(/[.*+?^${}()|[\]\\]/g, "\\$&")));
        return true;
      },
      name,
    );
  }
});

test("sandbox child environment starts from a fixed baseline, not the parent environment", () => {
  const parentOnly = {
    CAVE_PARENT_ONLY: "parent-secret",
    CAVE_API_KEY: "cloud-secret",
    AWS_SECRET_ACCESS_KEY: "aws-secret",
    NODE_OPTIONS: "--require=attacker-loader",
  };
  const previous = new Map(Object.keys(parentOnly).map((name) => [name, process.env[name]]));
  Object.assign(process.env, parentOnly);
  try {
    const env = buildSandboxToolEnv();
    assert.deepEqual(Object.keys(env).sort(), ["CAVE_EVAL_FIXTURE", "LANG", "LC_ALL", "PATH", "TZ"].sort());
    for (const name of Object.keys(parentOnly)) assert.equal(env[name], undefined, name);
  } finally {
    for (const [name, value] of previous) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
  }
});

test("only the explicitly selected provider capability crosses into the child", () => {
  const previous = process.env.ANTHROPIC_API_KEY;
  process.env.ANTHROPIC_API_KEY = "provider-secret";
  try {
    const env = buildSandboxToolEnv(["ANTHROPIC_API_KEY"]);
    assert.equal(env.ANTHROPIC_API_KEY, "provider-secret");
    assert.equal(env.OPENAI_API_KEY, undefined);
    assert.equal(env.GEMINI_API_KEY, undefined);
    assert.equal(env.GOOGLE_API_KEY, undefined);
  } finally {
    if (previous === undefined) delete process.env.ANTHROPIC_API_KEY;
    else process.env.ANTHROPIC_API_KEY = previous;
  }
});

test("invalid live credential profile allocates no tool workspace and keeps the error generic", async () => {
  const observed = await runWithCredentialProfile({
    network: false,
    childProcess: false,
    credentialEnv: ["PATH"],
  });
  assert.match(observed, /cave_sandbox_credential_env_not_allowlisted/);
  assert.doesNotMatch(observed, /PATH/);
});

test("unknown live credential profile allocates no tool workspace and keeps the error generic", async () => {
  const secretName = "MYSTERY_PROVIDER_KEY_7";
  const observed = await runWithCredentialProfile({
    network: false,
    childProcess: false,
    credentialEnv: [secretName],
  });
  assert.match(observed, /cave_sandbox_credential_env_not_allowlisted/);
  assert.doesNotMatch(observed, new RegExp(secretName));
});

test("missing live credential allocates no tool workspace and keeps the error generic", async () => {
  const name = "ANTHROPIC_API_KEY";
  const previous = process.env[name];
  delete process.env[name];
  try {
    const observed = await runWithCredentialProfile({
      network: false,
      childProcess: false,
      credentialEnv: [name],
    });
    assert.match(observed, /cave_sandbox_credential_missing/);
    assert.doesNotMatch(observed, new RegExp(name));
  } finally {
    if (previous === undefined) delete process.env[name];
    else process.env[name] = previous;
  }
});

test("valid live credential profile still tears down the tool workspace", async () => {
  const name = "ANTHROPIC_API_KEY";
  const previous = process.env[name];
  process.env[name] = "credential-profile-test";
  try {
    const observed = await runWithCredentialProfile({
      network: false,
      childProcess: false,
      credentialEnv: [name],
    });
    // The repository's macOS sandbox may be unavailable in a restricted test
    // runner; either way, the workspace teardown assertion above is the proof
    // this regression needs.
    assert.match(observed, /modular-tool-ok|cave_sandbox_failed:sandbox-exec: sandbox_apply: Operation not permitted/);
  } finally {
    if (previous === undefined) delete process.env[name];
    else process.env[name] = previous;
  }
});
