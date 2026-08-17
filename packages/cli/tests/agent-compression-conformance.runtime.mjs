import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawn, spawnSync } from "node:child_process";
import { chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { createServer as createNetServer, connect } from "node:net";
import { tmpdir } from "node:os";
import { delimiter, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { DatabaseSync } from "node:sqlite";

import { runCli } from "./harness/index.mjs";

const cliDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const cli = join(cliDir, "dist", "index.js");
const packageParent = join(cliDir, "..");
const publicRoot = existsSync(join(packageParent, "agents")) ? packageParent : join(packageParent, "..");
const registry = JSON.parse(readFileSync(join(publicRoot, "agents", "agents.json"), "utf8"));
const profiles = registry.agents;
const expectedProfiles = ["aider", "claude", "codex", "gemini", "hermes", "openclaw", "opencode"];
const protocolCapabilities = {
  aider: { recovery: "server" },
  claude: { recovery: "server" },
  codex: { recovery: "server" },
  gemini: { recovery: "server" },
  hermes: { recovery: "server" },
  openclaw: { recovery: "server" },
  opencode: { recovery: "server" },
};
const responseSentinel = "CAVEMAN_CONFORMANCE_OK";
const payloadMarker = "CAVEMAN_CONFORMANCE_PAYLOAD";
const longPrompt = Array.from({ length: 260 }, (_, i) => `${payloadMarker} section ${i % 7}: preserve this repeated operator context.`).join("\n");
const goToolchainAvailable = spawnSync("go", ["version"], { stdio: "ignore" }).status === 0;

function freePort() {
  return new Promise((resolve, reject) => {
    const server = createNetServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const port = server.address().port;
      server.close((error) => (error ? reject(error) : resolve(port)));
    });
  });
}

async function waitForPort(port, timeoutMs = 8_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const ready = await new Promise((resolve) => {
      const socket = connect({ host: "127.0.0.1", port });
      socket.once("connect", () => {
        socket.destroy();
        resolve(true);
      });
      socket.once("error", () => resolve(false));
    });
    if (ready) return;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`port ${port} did not become ready`);
}

function waitForExit(child, timeoutMs) {
  return new Promise((resolve) => {
    if (child.exitCode !== null || child.signalCode !== null) return resolve(true);
    const timer = setTimeout(() => {
      child.off("exit", onExit);
      resolve(false);
    }, timeoutMs);
    const onExit = () => {
      clearTimeout(timer);
      resolve(true);
    };
    child.once("exit", onExit);
  });
}

async function stopChild(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  child.kill("SIGTERM");
  if (await waitForExit(child, 1_500)) return;
  child.kill("SIGKILL");
  await waitForExit(child, 1_500);
}

function listen(server) {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => resolve(server.address().port));
  });
}

function close(server) {
  return new Promise((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
}

function requestPrompt(request) {
  const body = JSON.parse(request.raw);
  if (request.path === "/v1/messages") return body?.messages?.[0]?.content;
  if (request.path === "/v1/responses") return body?.input?.[0]?.content?.[0]?.text;
  if (request.path === "/v1/chat/completions") return body?.messages?.[0]?.content;
  if (/^\/v1beta\/models\/[^/]+:generateContent$/.test(request.path)) return body?.contents?.[0]?.parts?.[0]?.text;
  throw new Error(`no prompt extractor for ${request.path}`);
}

async function conformanceUpstream() {
  const requests = [];
  const server = createServer((request, response) => {
    let raw = "";
    request.setEncoding("utf8");
    request.on("data", (chunk) => (raw += chunk));
    request.on("end", () => {
      const path = new URL(request.url ?? "/", "http://stub.invalid").pathname;
      requests.push({ path, raw });
      response.writeHead(200, { "content-type": "application/json" });
      if (path === "/v1/messages") {
        response.end(JSON.stringify({
          id: "msg_conformance", type: "message", role: "assistant", model: "claude-sonnet-4-6",
          content: [{ type: "text", text: responseSentinel }], stop_reason: "end_turn", stop_sequence: null,
          usage: { input_tokens: 1200, cache_creation_input_tokens: 0, cache_read_input_tokens: 0, output_tokens: 4 },
        }));
        return;
      }
      if (path === "/v1/responses") {
        response.end(JSON.stringify({
          id: "resp_conformance", object: "response", status: "completed", model: "gpt-5.5",
          output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: responseSentinel }] }],
          usage: { input_tokens: 1200, output_tokens: 4, total_tokens: 1204 },
        }));
        return;
      }
      if (path === "/v1/chat/completions") {
        response.end(JSON.stringify({
          id: "chatcmpl_conformance", object: "chat.completion", model: "gpt-4o-mini",
          choices: [{ index: 0, message: { role: "assistant", content: responseSentinel }, finish_reason: "stop" }],
          usage: { prompt_tokens: 1200, completion_tokens: 4, total_tokens: 1204 },
        }));
        return;
      }
      if (/^\/v1beta\/models\/[^/]+:generateContent$/.test(path)) {
        response.end(JSON.stringify({
          candidates: [{ content: { role: "model", parts: [{ text: responseSentinel }] }, finishReason: "STOP" }],
          usageMetadata: { promptTokenCount: 1200, candidatesTokenCount: 4, totalTokenCount: 1204 },
        }));
        return;
      }
      response.statusCode = 404;
      response.end(JSON.stringify({ error: { message: `unexpected conformance path ${path}` } }));
    });
  });
  const port = await listen(server);
  return { baseURL: `http://127.0.0.1:${port}`, requests, close: () => close(server) };
}

function writeAgentStub(binDir, id) {
  const path = join(binDir, id);
  writeFileSync(path, `#!/usr/bin/env node
import { readFileSync } from "node:fs";
const id = ${JSON.stringify(id)};
const prompt = "agent=" + id + "\\n" + ${JSON.stringify(longPrompt)};
let baseURL = "";
let path = "";
let body;
let responseKind = "chat";
if (id === "claude") {
  baseURL = process.env.ANTHROPIC_BASE_URL || "";
  path = "/v1/messages";
  responseKind = "anthropic";
  body = { model: "claude-sonnet-4-6", max_tokens: 32, stream: false, messages: [{ role: "user", content: prompt }] };
} else if (id === "codex") {
  const config = readFileSync(process.env.CODEX_HOME + "/config.toml", "utf8");
  baseURL = config.match(/^base_url\\s*=\\s*"([^"]+)"/m)?.[1] || "";
  path = "/v1/responses";
  responseKind = "responses";
  body = { model: "gpt-5.5", stream: false, input: [{ role: "user", content: [{ type: "input_text", text: prompt }] }] };
} else if (id === "gemini") {
  baseURL = process.env.GEMINI_BASE_URL || process.env.GOOGLE_GEMINI_BASE_URL || "";
  path = "/v1beta/models/gemini-2.5-flash:generateContent";
  responseKind = "gemini";
  body = { contents: [{ role: "user", parts: [{ text: prompt }] }], generationConfig: { maxOutputTokens: 32 } };
} else if (id === "hermes") {
  baseURL = process.env.CUSTOM_BASE_URL || "";
  path = "/v1/chat/completions";
  body = { model: "gpt-4o-mini", stream: false, messages: [{ role: "user", content: prompt }] };
} else if (id === "opencode") {
  const config = JSON.parse(process.env.OPENCODE_CONFIG_CONTENT || "{}");
  baseURL = config?.provider?.openai?.options?.baseURL || "";
  path = "/chat/completions";
  body = { model: "gpt-4o-mini", stream: false, messages: [{ role: "user", content: prompt }] };
} else if (id === "openclaw") {
  const config = JSON.parse(readFileSync(process.env.OPENCLAW_CONFIG_PATH, "utf8"));
  baseURL = config?.models?.providers?.caveman?.baseUrl || "";
  path = "/chat/completions";
  body = { model: "gpt-4o-mini", stream: false, messages: [{ role: "user", content: prompt }] };
} else if (id === "aider") {
  baseURL = process.env.OPENAI_API_BASE || "";
  path = "/chat/completions";
  body = { model: "gpt-4o-mini", stream: false, messages: [{ role: "user", content: prompt }] };
} else {
  baseURL = process.env.OPENAI_API_BASE || "";
  path = "/v1/chat/completions";
  body = { model: "gpt-4o-mini", stream: false, messages: [{ role: "user", content: prompt }] };
}
if (!baseURL) {
  process.stderr.write(id + ": missing injected gateway base URL\\n");
  process.exit(2);
}
const headers = { "content-type": "application/json", authorization: "Bearer sk-conformance-openai" };
if (responseKind === "anthropic") {
  delete headers.authorization;
  headers["anthropic-version"] = "2023-06-01";
  headers["x-api-key"] = "sk-ant-conformance-anthropic";
}
if (responseKind === "gemini") {
  delete headers.authorization;
  headers["x-goog-api-key"] = "AIzaConformanceGeminiKey000000000000000";
}
try {
  const response = await fetch(baseURL.replace(/\\/+$/, "") + path, { method: "POST", headers, body: JSON.stringify(body) });
  const result = await response.json();
  if (!response.ok) throw new Error("HTTP " + response.status + ": " + JSON.stringify(result));
  const text = responseKind === "anthropic" ? result?.content?.[0]?.text
    : responseKind === "responses" ? result?.output?.[0]?.content?.[0]?.text
    : responseKind === "gemini" ? result?.candidates?.[0]?.content?.parts?.[0]?.text
    : result?.choices?.[0]?.message?.content;
  if (text !== ${JSON.stringify(responseSentinel)}) throw new Error("response changed: " + JSON.stringify(text));
  process.stdout.write(id + ":" + text + "\\n");
} catch (error) {
  process.stderr.write(id + ": " + error.message + "\\n");
  process.exit(1);
}
`, { mode: 0o755 });
}

function entitlement() {
  return {
    entitled: true, plan: "free", telemetry_level: "metadata",
    seats_used: 1, seats_limit: 1, devices_used: 1, devices_limit: 3,
    evicted_device_hash: null, expires_at: new Date(Date.now() + 3_600_000).toISOString(),
  };
}

function writeHomeConfig(home, id) {
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({
    think: { mode: "compress", toon: false, shrink: false },
    execute: { mcp: false, browse_tool: false, browse_cli: false, proxy: true },
    wrapEntitlement: entitlement(),
    wrapEntitlementFetchedAt: new Date().toISOString(),
  }), { mode: 0o600 });
  if (id === "openclaw") {
    mkdirSync(join(home, ".openclaw"), { recursive: true });
    writeFileSync(join(home, ".openclaw", "openclaw.json"), JSON.stringify({
      agents: { defaults: { model: { primary: "myprov/gpt-4o-mini" } } },
      models: { providers: { myprov: {
        baseUrl: "https://provider.invalid/v1", apiKey: "${MYPROV_API_KEY}", api: "openai-completions",
        models: [{ id: "gpt-4o-mini", name: "GPT-4o mini", contextWindow: 128000, maxTokens: 4096 }],
      } } },
    }));
  }
}

test("every shipped agent profile preserves protocol-correct recovery behavior through the real compression engine", { timeout: 90_000 }, async (t) => {
  if (!process.env.CAVEMAN_TEST_PROXY_BIN && !goToolchainAvailable) return t.skip("go toolchain not found");
  assert.deepEqual(profiles.map((profile) => profile.id).sort(), expectedProfiles, "new profiles must join the conformance matrix");
  assert.deepEqual(Object.keys(protocolCapabilities).sort(), expectedProfiles, "every profile needs an explicit recovery capability");
  const upstream = await conformanceUpstream();
  const suiteDir = mkdtempSync(join(tmpdir(), "cave-agent-compression-"));
  const caveHome = join(suiteDir, "cave-home");
  const binDir = join(suiteDir, "bin");
  const proxyBin = join(binDir, "caveman-proxy");
  let proxy;
  try {
    mkdirSync(caveHome, { recursive: true });
    mkdirSync(binDir, { recursive: true });
    for (const id of expectedProfiles) writeAgentStub(binDir, id);
    if (process.env.CAVEMAN_TEST_PROXY_BIN) {
      copyFileSync(process.env.CAVEMAN_TEST_PROXY_BIN, proxyBin);
      chmodSync(proxyBin, 0o755);
    } else {
      execFileSync("go", ["build", "-o", proxyBin, "./proxy/cmd/caveman-proxy"], { cwd: publicRoot, stdio: "pipe" });
    }
    const proxyPort = await freePort();
    const configPath = join(caveHome, "caveman.yaml");
    writeFileSync(configPath, [
      "mode: compress",
      `listen: 127.0.0.1:${proxyPort}`,
      "providers:",
      "  anthropic:",
      `    base_url: ${upstream.baseURL}`,
      "  openai:",
      `    base_url: ${upstream.baseURL}`,
      "  gemini:",
      `    base_url: ${upstream.baseURL}`,
      "",
    ].join("\n"));
    const proxyEnv = {
      ...process.env,
      CAVEMAN_HOME: caveHome,
      CAVEMAN_CONFIG: configPath,
      CAVEMAN_PROXY_OWNER: "wrap",
      // No account signal: local compression is not account-gated.
      CAVEMAN_RECOVERY: "",
      CAVE_ENGINE_TOON: "0",
      CAVE_SSRF_ALLOWLIST: "127.0.0.1",
      CAVEMAN_TELEMETRY: "0",
      OPENAI_API_KEY: "sk-conformance-openai",
      ANTHROPIC_API_KEY: "sk-ant-conformance-anthropic",
      GEMINI_API_KEY: "AIzaConformanceGeminiKey000000000000000",
    };
    proxy = spawn(proxyBin, ["serve"], { env: proxyEnv, stdio: ["ignore", "pipe", "pipe"] });
    proxy.stdout.resume();
    proxy.stderr.resume();
    await waitForPort(proxyPort);

    for (const id of expectedProfiles) {
      const home = join(suiteDir, `home-${id}`);
      writeHomeConfig(home, id);
      const env = {
        ...proxyEnv,
        HOME: home,
        PATH: `${binDir}${delimiter}${process.env.PATH}`,
        CAVEMAN_PROXY_BIN: proxyBin,
        CAVE_GATEWAY_URL: `http://127.0.0.1:${proxyPort}`,
        CAVE_NO_KEYCHAIN: "1",
        NO_COLOR: "1",
        MYPROV_API_KEY: "sk-conformance-openai",
        CUSTOM_API_KEY: "sk-conformance-openai",
      };
      delete env.CODEX_HOME;
      delete env.OPENCLAW_CONFIG_PATH;
      delete env.OPENCLAW_STATE_DIR;
      const out = await runCli(cli, ["wrap", id], { env, cwd: suiteDir, timeoutMs: 15_000 });
      assert.equal(out.code, 0, `${id} failed:\n${out.stderr}`);
      assert.match(out.stdout, new RegExp(`^${id}:${responseSentinel}`, "m"), `${id} did not receive unchanged response`);
      const request = upstream.requests.at(-1);
      assert.ok(request, `${id} sent no upstream request`);
      const originalPrompt = `agent=${id}\n${longPrompt}`;
      if (protocolCapabilities[id].recovery === "server") {
        assert.notEqual(requestPrompt(request), originalPrompt, `${id} sent original long prompt uncompressed`);
        assert.match(request.raw, /<<ccr:/, `${id} upstream body lacks recovery handle`);
      } else {
        assert.equal(requestPrompt(request), originalPrompt, `${id} changed an unrecoverable request`);
        assert.doesNotMatch(request.raw, /<<ccr:/, `${id} emitted an unusable recovery handle`);
      }
    }

    assert.equal(upstream.requests.length, expectedProfiles.length);
    const db = new DatabaseSync(join(caveHome, "caveman.db"), { readOnly: true });
    try {
      const rows = db.prepare(`
        SELECT agent_slug, compression_tokens_before, compression_tokens_after, recovery_handle,
               raw_request_sha256, transformed_request_sha256
        FROM requests ORDER BY id
      `).all();
      assert.equal(rows.length, expectedProfiles.length);
      assert.deepEqual(rows.map((row) => row.agent_slug), expectedProfiles);
      for (const row of rows) {
        if (protocolCapabilities[row.agent_slug].recovery === "server") {
          assert.ok(Number(row.compression_tokens_before) > Number(row.compression_tokens_after), `${row.agent_slug} recorded no token reduction`);
          assert.match(String(row.recovery_handle), /^ccr_/, `${row.agent_slug} recorded no CCR handle`);
          assert.notEqual(row.raw_request_sha256, row.transformed_request_sha256, `${row.agent_slug} hashes claim no transform`);
        } else {
          assert.equal(Number(row.compression_tokens_before), 0, `${row.agent_slug} claimed input compression without recovery`);
          assert.equal(Number(row.compression_tokens_after), 0, `${row.agent_slug} claimed output compression without recovery`);
          assert.ok(row.recovery_handle == null || row.recovery_handle === "", `${row.agent_slug} recorded a recovery handle without recovery support`);
          assert.equal(row.raw_request_sha256, row.transformed_request_sha256, `${row.agent_slug} changed bytes without recovery support`);
        }
      }
    } finally {
      db.close();
    }
  } finally {
    await stopChild(proxy);
    await upstream.close();
    rmSync(suiteDir, { recursive: true, force: true });
  }
});
