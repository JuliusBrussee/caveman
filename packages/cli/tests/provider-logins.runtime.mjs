import { test } from "node:test";
import assert from "node:assert/strict";
import { chmodSync, existsSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { isolatedCliEnv, runCli } from "./_cli.mjs";

const here = dirname(fileURLToPath(import.meta.url));

function json(result) {
  assert.equal(result.code, 0, result.stderr);
  return JSON.parse(result.stdout);
}

test("providers add stores an env key in a 0600 file and lists only its id", async () => {
  const iso = isolatedCliEnv({ OPENROUTER_API_KEY: "sk-or-test-123" });
  try {
    const out = json(await runCli(["providers", "add", "openrouter"], { env: iso.env }));
    assert.deepEqual(out, { added: "openrouter", name: "OpenRouter", store: "file", from: "OPENROUTER_API_KEY" });
    const secret = join(iso.home, "provider-logins", "openrouter");
    assert.equal(readFileSync(secret, "utf8"), "sk-or-test-123");
    if (process.platform !== "win32") assert.equal(statSync(secret).mode & 0o777, 0o600);
    const index = readFileSync(join(iso.home, "provider-logins.json"), "utf8");
    assert.doesNotMatch(index, /sk-or-test/);
    assert.deepEqual(JSON.parse(index).logins.map(({ id, kind, store }) => ({ id, kind, store })), [{ id: "openrouter", kind: "api_key", store: "file" }]);
    const local = json(await runCli(["providers", "local"], { env: iso.env }));
    assert.equal(local.logins.length, 1);
    assert.equal(local.logins[0].id, "openrouter");
    assert.match(local.traffic, /straight from this machine/);
  } finally {
    iso.cleanup();
  }
});

test("providers add reads --stdin and --key-env, never a key on argv", async () => {
  const iso = isolatedCliEnv({ MY_DEEPSEEK: "ds-key" });
  try {
    json(await runCli(["providers", "add", "fireworks", "--stdin"], { env: iso.env, input: "fw-key\n" }));
    assert.equal(readFileSync(join(iso.home, "provider-logins", "fireworks"), "utf8"), "fw-key");
    json(await runCli(["providers", "add", "deepseek", "--key-env", "MY_DEEPSEEK"], { env: iso.env }));
    assert.equal(readFileSync(join(iso.home, "provider-logins", "deepseek"), "utf8"), "ds-key");
    const missing = await runCli(["providers", "add", "xai"], { env: { ...iso.env, XAI_API_KEY: "" } });
    assert.notEqual(missing.code, 0);
    assert.match(missing.stderr, /no key for xai: set XAI_API_KEY/);
  } finally {
    iso.cleanup();
  }
});

test("providers remove deletes the secret and the index entry", async () => {
  const iso = isolatedCliEnv({ GROQ_API_KEY: "gq" });
  try {
    json(await runCli(["providers", "add", "groq"], { env: iso.env }));
    assert.deepEqual(json(await runCli(["providers", "remove", "groq"], { env: iso.env })), { removed: "groq", was_added: true });
    assert.equal(existsSync(join(iso.home, "provider-logins", "groq")), false);
    assert.deepEqual(JSON.parse(readFileSync(join(iso.home, "provider-logins.json"), "utf8")).logins, []);
  } finally {
    iso.cleanup();
  }
});

test("logins whose terms forbid a third-party client are refused with the reason", async () => {
  const iso = isolatedCliEnv();
  try {
    for (const [id, reason] of [["claude", /only in Anthropic's own apps/], ["google", /bans accounts/], ["copilot", /OpenCode's own GitHub login/]]) {
      const result = await runCli(["providers", "login", id], { env: iso.env });
      assert.notEqual(result.code, 0);
      assert.match(result.stderr, reason);
    }
    const wrongKind = await runCli(["providers", "add", "chatgpt"], { env: iso.env });
    assert.match(wrongKind.stderr, /sign-in, not a key/);
  } finally {
    iso.cleanup();
  }
});

test("providers login chatgpt runs the runtime's sign-in", async () => {
  const iso = isolatedCliEnv();
  try {
    if (process.platform === "win32") return;
    const log = join(iso.home, "args.log");
    const fake = join(iso.home, "bin", "fake-proxy");
    writeFileSync(fake, `#!/bin/sh\necho "$@" > ${JSON.stringify(log)}\nexit 0\n`, { mode: 0o755 });
    chmodSync(fake, 0o755);
    const result = await runCli(["providers", "login", "chatgpt"], { env: { ...iso.env, CAVEMAN_PROXY_BIN: fake } });
    assert.equal(result.code, 0, result.stderr);
    assert.equal(readFileSync(log, "utf8").trim(), "provider-login chatgpt");
  } finally {
    iso.cleanup();
  }
});

test("the CLI's provider list matches the runtime registry", async () => {
  const { PROVIDER_LOGINS } = await import(pathToFileURL(join(here, "..", "dist", "modules", "provider-logins.js")).href);
  const registry = JSON.parse(readFileSync(join(here, "..", "..", "..", "proxy", "internal", "pool", "providers.json"), "utf8"));
  const shape = (rows) => rows.map(({ id, kind, env }) => ({ id, kind, env: env ?? [] }));
  assert.deepEqual(shape(PROVIDER_LOGINS), shape(registry.providers));
});
