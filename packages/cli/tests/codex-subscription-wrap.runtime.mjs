import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");
const BASE_URL_VARS = ["ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "GEMINI_BASE_URL", "GOOGLE_GEMINI_BASE_URL"];
const SUBSCRIPTION_LINE = "caveman: codex subscription login detected — routing via ephemeral CODEX_HOME through /chatgpt (byte-safe pass-through)\n";
const PIXEL_ERROR = "caveman wrap: --pixel not yet supported for codex subscription sessions\n";
const GW = "http://127.0.0.1:9";

function sha256(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function attributed(gw) {
  return `${gw.replace(/\/+$/, "")}/w/codex`;
}

function snapshotTree(root) {
  if (!existsSync(root)) return [];
  const out = [];
  function walk(path, rel) {
    const st = lstatSync(path);
    if (st.isDirectory()) {
      out.push(`${rel}/:dir`);
      for (const name of readdirSync(path).sort()) walk(join(path, name), rel ? `${rel}/${name}` : name);
      return;
    }
    if (st.isFile()) {
      out.push(`${rel}:file:${st.mode & 0o777}:${sha256(readFileSync(path))}`);
      return;
    }
    out.push(`${rel}:other:${st.mode & 0o777}`);
  }
  walk(root, "");
  return out;
}

function writeCodexStub(binDir) {
  const stub = join(binDir, "codex");
  writeFileSync(stub, `#!/usr/bin/env node
import { existsSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";
const home = process.env.CODEX_HOME || "";
const read = (name) => {
  try { return readFileSync(join(home, name), "utf8"); } catch { return ""; }
};
const mode = (name) => {
  try { return statSync(join(home, name)).mode & 0o777; } catch { return null; }
};
const keys = ${JSON.stringify(["CODEX_HOME", ...BASE_URL_VARS])};
writeFileSync(process.env.CODEX_ENV_DUMP, JSON.stringify({
  args: process.argv.slice(2),
  env: Object.fromEntries(keys.map((key) => [key, process.env[key] ?? null])),
  tempExistsDuringChild: home ? existsSync(home) : false,
  config: read("config.toml"),
  auth: read("auth.json"),
  authMode: mode("auth.json"),
}));
`, { mode: 0o755 });
}

function fixture({ auth, config, agents = "# Codex rules\\n", skills = true } = {}) {
  const home = mkdtempSync(join(tmpdir(), "cave-codex-home-"));
  const codexDir = join(home, ".codex");
  const binDir = mkdtempSync(join(tmpdir(), "cave-codex-bin-"));
  const dumpFile = join(home, "child-env.json");
  mkdirSync(codexDir, { recursive: true });
  writeCodexStub(binDir);
  if (auth !== undefined) writeFileSync(join(codexDir, "auth.json"), typeof auth === "string" ? auth : JSON.stringify(auth, null, 2));
  if (config !== undefined) writeFileSync(join(codexDir, "config.toml"), config);
  if (agents !== undefined) writeFileSync(join(codexDir, "AGENTS.md"), agents);
  if (skills) {
    mkdirSync(join(codexDir, "skills", "local"), { recursive: true });
    writeFileSync(join(codexDir, "skills", "local", "SKILL.md"), "# Local skill\\n");
  }
  return { home, codexDir, binDir, dumpFile };
}

function writeWrapConfig(home, wrap) {
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap }, null, 2));
}

async function runCli(fx, argv, gw, extraEnv = {}) {
  const env = {
    ...process.env,
    NO_COLOR: "1",
    CAVEMAN_TELEMETRY: "0",
    HOME: fx.home,
    CAVEMAN_HOME: join(fx.home, ".caveman"),
    CAVE_GATEWAY_URL: gw,
    CODEX_ENV_DUMP: fx.dumpFile,
    PATH: `${fx.binDir}:${process.env.PATH}`,
  };
  for (const key of ["CODEX_HOME", "OPENAI_API_KEY", ...BASE_URL_VARS]) delete env[key];
  Object.assign(env, extraEnv);
  return await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

function readDump(fx) {
  return JSON.parse(readFileSync(fx.dumpFile, "utf8"));
}

test("wrap codex subscription auth uses ephemeral CODEX_HOME and leaves real ~/.codex untouched", async () => {
  const auth = { auth_mode: "chatgpt", OPENAI_API_KEY: null, tokens: { account_id: "acct_123", access_token: "tok" }, last_refresh: "2026-07-07T00:00:00Z" };
  const fx = fixture({ auth, config: 'approval_policy = "never"\\n' });
  writeWrapConfig(fx.home, { proxy: false });
  const before = snapshotTree(fx.codexDir);

  const out = await runCli(fx, ["wrap", "codex"], GW);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stderr, SUBSCRIPTION_LINE);
  assert.deepEqual(snapshotTree(fx.codexDir), before, "real ~/.codex must stay byte-identical");

  const dump = readDump(fx);
  assert.equal(dump.args.length, 0);
  assert.ok(dump.env.CODEX_HOME, "subscription mode must set CODEX_HOME");
  assert.match(dump.env.CODEX_HOME, /caveman-wrap-/);
  assert.equal(dump.tempExistsDuringChild, true, "ephemeral CODEX_HOME must exist while codex runs");
  assert.equal(existsSync(dump.env.CODEX_HOME), false, "ephemeral CODEX_HOME must be cleaned after codex exits");
  assert.equal(dump.env.OPENAI_BASE_URL, null, "subscription mode must not set OPENAI_BASE_URL");
  assert.equal(dump.auth, JSON.stringify(auth, null, 2), "auth.json must be copied into ephemeral CODEX_HOME");
  assert.equal(dump.authMode, 0o600, "copied auth.json must be mode 0600");
  assert.match(dump.config, /model_provider = "caveman"/);
  assert.match(dump.config, /\[model_providers\.caveman\]/);
  assert.match(dump.config, new RegExp(`base_url = "${GW.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\/chatgpt"`));
  assert.match(dump.config, /wire_api = "responses"/);
  assert.match(dump.config, /requires_openai_auth = true/);
});

test("wrap codex API-key auth keeps old env-var behavior and does not override CODEX_HOME", async () => {
  const fx = fixture({
    auth: { auth_mode: "api_key", OPENAI_API_KEY: "sk-file", tokens: null },
    agents: undefined,
    skills: false,
  });
  writeWrapConfig(fx.home, { proxy: false, shrink: false, mcp: false, browse: false });

  const out = await runCli(fx, ["wrap", "codex"], GW);
  assert.equal(out.code, 0, out.stderr);
  const dump = readDump(fx);
  assert.equal(dump.env.CODEX_HOME, null);
  assert.equal(dump.env.OPENAI_BASE_URL, attributed(GW));
});

test("wrap codex with missing auth.json keeps old env-var behavior", async () => {
  const fx = fixture({ agents: undefined, skills: false });
  writeWrapConfig(fx.home, { proxy: false, shrink: false, mcp: false, browse: false });

  const out = await runCli(fx, ["wrap", "codex"], GW);
  assert.equal(out.code, 0, out.stderr);
  const dump = readDump(fx);
  assert.equal(dump.env.CODEX_HOME, null);
  assert.equal(dump.env.OPENAI_BASE_URL, attributed(GW));
});

test("wrap codex subscription strips existing model_provider and caveman block only in ephemeral config", async () => {
  const config = [
    'model_provider = "openai"',
    'approval_policy = "never"',
    "",
    "[model_providers.openai]",
    'name = "OpenAI"',
    'base_url = "https://api.openai.com/v1"',
    "",
    "[model_providers.caveman]",
    'name = "Old Caveman"',
    'base_url = "http://old.example"',
    'wire_api = "chat"',
    "",
    "[profiles.default]",
    'model = "gpt-5"',
    "",
  ].join("\n");
  const fx = fixture({
    auth: { auth_mode: "chatgpt", OPENAI_API_KEY: "", tokens: { account_id: "acct_456" } },
    config,
  });
  writeWrapConfig(fx.home, { proxy: false });
  const before = readFileSync(join(fx.codexDir, "config.toml"), "utf8");

  const out = await runCli(fx, ["wrap", "codex"], GW);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(readFileSync(join(fx.codexDir, "config.toml"), "utf8"), before, "real config.toml must not be edited");

  const dump = readDump(fx);
  assert.equal((dump.config.match(/^model_provider\s*=/gm) || []).length, 1);
  assert.match(dump.config, /^model_provider = "caveman"$/m);
  assert.doesNotMatch(dump.config, /Old Caveman/);
  assert.doesNotMatch(dump.config, /http:\/\/old\.example/);
  assert.match(dump.config, /\[model_providers\.openai\]/);
  assert.match(dump.config, /\[profiles\.default\]/);
});

test("wrap codex subscription rejects --pixel with the exact one-line error", async () => {
  const fx = fixture({
    auth: { auth_mode: "chatgpt", OPENAI_API_KEY: null, tokens: { account_id: "acct_789" } },
  });
  const before = snapshotTree(fx.codexDir);

  const out = await runCli(fx, ["wrap", "--pixel", "codex"], GW);
  assert.equal(out.code, 1);
  assert.equal(out.stdout, "");
  assert.equal(out.stderr, PIXEL_ERROR);
  assert.equal(existsSync(fx.dumpFile), false, "codex must not spawn after --pixel subscription error");
  assert.deepEqual(snapshotTree(fx.codexDir), before, "real ~/.codex must stay untouched on --pixel error");
});
