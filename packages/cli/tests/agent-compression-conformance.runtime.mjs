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
const expectedProfiles = profiles.map((profile) => profile.id).sort();
// OpenClaw's default Chat transport depends on private host policy and stays
// direct. Exercise its supported custom Anthropic transport here; focused
// OpenClaw tests separately prove that unsupported Chat is not rerouted.
const fixtureProtocol = (profile) => profile.id === "openclaw" ? "anthropic-messages" : profile.wire_protocol;
const protocolCapabilities = {
  "anthropic-messages": { recovery: "server" },
  "openai-chat": { recovery: "server" },
  "openai-responses": { recovery: "server" },
  "gemini-generatecontent": { recovery: "server" },
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

function retrieveResponse(path, handle, callID) {
  if (path === "/v1/messages") {
    return {
      id: "msg_retrieve", type: "message", role: "assistant", model: "claude-sonnet-4-6",
      content: [{ type: "tool_use", id: callID, name: "caveman_retrieve", input: { handle } }],
      stop_reason: "tool_use", stop_sequence: null,
      usage: { input_tokens: 1200, output_tokens: 4 },
    };
  }
  if (path === "/v1/responses") {
    return {
      id: "resp_retrieve", object: "response", status: "completed", model: "gpt-5.5",
      output: [{ type: "function_call", id: "fc_retrieve", call_id: callID, name: "caveman_retrieve", arguments: JSON.stringify({ handle }), status: "completed" }],
      usage: { input_tokens: 1200, output_tokens: 4, total_tokens: 1204 },
    };
  }
  if (path === "/v1/chat/completions") {
    return {
      id: "chatcmpl_retrieve", object: "chat.completion", model: "gpt-4o-mini",
      choices: [{ index: 0, message: { role: "assistant", content: null, tool_calls: [{ id: callID, type: "function", function: { name: "caveman_retrieve", arguments: JSON.stringify({ handle }) } }] }, finish_reason: "tool_calls" }],
      usage: { prompt_tokens: 1200, completion_tokens: 4, total_tokens: 1204 },
    };
  }
  if (/^\/v1beta\/models\/[^/]+:generateContent$/.test(path)) {
    return {
      candidates: [{ content: { role: "model", parts: [{ functionCall: { id: callID, name: "caveman_retrieve", args: { handle } } }] }, finishReason: "STOP" }],
      usageMetadata: { promptTokenCount: 1200, candidatesTokenCount: 4, totalTokenCount: 1204 },
    };
  }
  throw new Error(`no retrieval response for ${path}`);
}

function advertisesRetrieve(request) {
  const tools = JSON.parse(request.raw).tools ?? [];
  if (request.path === "/v1/chat/completions") {
    return tools.some((tool) => tool.type === "function" && tool.function?.name === "caveman_retrieve");
  }
  if (/^\/v1beta\/models\/[^/]+:generateContent$/.test(request.path)) {
    return tools.some((tool) => tool.functionDeclarations?.some((declaration) => declaration.name === "caveman_retrieve"));
  }
  return tools.some((tool) => tool.name === "caveman_retrieve");
}

function retrievedPrompt(request, callID) {
  const body = JSON.parse(request.raw);
  if (request.path === "/v1/messages") {
    return body.messages?.at(-1)?.content?.find((part) => part.type === "tool_result" && part.tool_use_id === callID)?.content;
  }
  if (request.path === "/v1/responses") {
    return body.input?.find((item) => item.type === "function_call_output" && item.call_id === callID)?.output;
  }
  if (request.path === "/v1/chat/completions") {
    return body.messages?.find((message) => message.role === "tool" && message.tool_call_id === callID)?.content;
  }
  if (/^\/v1beta\/models\/[^/]+:generateContent$/.test(request.path)) {
    const result = body.contents?.at(-1)?.parts?.find((part) => part.functionResponse?.id === callID)?.functionResponse;
    assert.equal(result?.name, "caveman_retrieve");
    return result.response?.output;
  }
  throw new Error(`no retrieval result extractor for ${request.path}`);
}

async function conformanceUpstream() {
  const requests = [];
  const recoveries = [];
  const errors = [];
  let expected;
  let pending;
  const server = createServer((request, response) => {
    let raw = "";
    request.setEncoding("utf8");
    request.on("data", (chunk) => (raw += chunk));
    request.on("end", () => {
      const path = new URL(request.url ?? "/", "http://stub.invalid").pathname;
      const received = { path, raw };
      requests.push(received);
      try {
        assert.ok(expected, "unexpected upstream request outside a profile turn");
        if (!pending) {
          const compressedPrompt = requestPrompt(received);
          assert.notEqual(compressedPrompt, expected.originalPrompt, `${expected.agentId} did not compress its original prompt`);
          assert.ok(advertisesRetrieve(received), `${expected.agentId} must expose the recovery tool before the provider can select it`);
          const handle = compressedPrompt.match(/<<ccr:(ccr_[^>\s]+)>>/)?.[1];
          assert.ok(handle, `${expected.agentId} omitted a recoverable prompt handle`);
          pending = { ...expected, path, handle, callID: `call_${expected.agentId}_retrieve` };
          response.writeHead(200, { "content-type": "application/json" });
          response.end(JSON.stringify(retrieveResponse(path, handle, pending.callID)));
          return;
        }
        assert.equal(path, pending.path, "retrieval continuation changed protocol");
        assert.equal(retrievedPrompt(received, pending.callID), pending.originalPrompt, `${pending.agentId} must recover its exact original prompt before receiving the final sentinel`);
        recoveries.push(pending);
        pending = undefined;
        expected = undefined;
      } catch (error) {
        errors.push(String(error.stack || error));
        response.writeHead(500, { "content-type": "application/json" });
        response.end(JSON.stringify({ error: { message: String(error.message || error) } }));
        return;
      }
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
  return {
    baseURL: `http://127.0.0.1:${port}`, requests, recoveries, errors,
    expectPrompt(agentId, originalPrompt) {
      assert.equal(pending, undefined, "previous profile did not finish recovery");
      assert.equal(expected, undefined, "previous profile sent no upstream request");
      expected = { agentId, originalPrompt };
    },
    close: () => close(server),
  };
}

function writeAgentStub(binDir, profile) {
  const path = join(binDir, profile.binary_names[0]);
  writeFileSync(path, `#!/usr/bin/env node
import { readFileSync } from "node:fs";
const profile = ${JSON.stringify(profile)};
const id = profile.id;
const prompt = "agent=" + id + "\\n" + ${JSON.stringify(longPrompt)};

function routedURL(value) {
  if (typeof value === "string") return value.includes("/w/" + id) ? value : "";
  if (Array.isArray(value)) {
    for (const item of value) {
      const found = routedURL(item);
      if (found) return found;
    }
    return "";
  }
  if (value && typeof value === "object") {
    for (const item of Object.values(value)) {
      const found = routedURL(item);
      if (found) return found;
    }
  }
  return "";
}

function declarativeBaseURL() {
  const injection = profile.injection;
  if (injection.method === "env") {
    return routedURL(Object.keys(injection.env).map((name) => process.env[name]));
  }
  if (injection.method === "config-env-content") {
    return routedURL(JSON.parse(process.env[injection.env_var] || "{}"));
  }
  if (injection.method === "config-file") {
    const configPath = process.env[injection.env_var];
    return configPath ? routedURL(JSON.parse(readFileSync(configPath, "utf8"))) : "";
  }
  return "";
}

let baseURL = "";
let path = "";
let body;
let responseKind = "chat";
let wireProtocol = profile.wire_protocol;
if (profile.injection_completeness === "code-only") {
  if (id !== "codex") {
    process.stderr.write(id + ": no conformance adapter for code-only injection\\n");
    process.exit(2);
  }
  const config = readFileSync(process.env.CODEX_HOME + "/config.toml", "utf8");
  baseURL = config.match(/^base_url\\s*=\\s*"([^"]+)"/m)?.[1] || "";
} else if (profile.injection.method === "native-extension") {
  if (id !== "pi") {
    process.stderr.write(id + ": no conformance adapter for native-extension injection\\n");
    process.exit(2);
  }
  // Simulates the routed Pi extension. Wrap must actually have passed the
  // extension asset and stamped the hook env — a fail-open direct launch here
  // must fail the conformance run, not silently pretend to route.
  if (!process.argv.includes("--extension")) { process.stderr.write("pi: wrap did not pass --extension\\n"); process.exit(2); }
  if (!process.env.CAVEMAN_PI_HOOK_CMD) { process.stderr.write("pi: wrap did not stamp CAVEMAN_PI_HOOK_CMD\\n"); process.exit(2); }
  baseURL = (process.env.CAVE_GATEWAY_URL || "") + "/w/pi/openai/v1";
} else {
  baseURL = declarativeBaseURL();
  if (id === "openclaw") {
    const config = JSON.parse(readFileSync(process.env[profile.injection.env_var], "utf8"));
    const provider = config.models?.providers?.myprov;
    if (provider?.api !== "anthropic-messages") {
      process.stderr.write("openclaw: expected the configured Anthropic transport\\n");
      process.exit(2);
    }
    wireProtocol = provider.api;
  }
}
if (!baseURL) {
  process.stderr.write(id + ": missing injected gateway base URL\\n");
  process.exit(2);
}

if (wireProtocol === "anthropic-messages") {
  path = "/v1/messages";
  responseKind = "anthropic";
  body = { model: "claude-sonnet-4-6", max_tokens: 32, stream: false, messages: [{ role: "user", content: prompt }] };
} else if (wireProtocol === "openai-responses") {
  // Codex's OpenAI-Responses client appends the bare verb onto base_url,
  // ALWAYS — it never supplies a "/v1" the config left out, the same way it
  // behaves against the real api.openai.com. Modelling it as appending
  // "/v1/responses" quietly supplied the missing segment, so this harness ran
  // green against the very base_url under which every real codex session 404'd
  // (#1045): the stub was the camouflage, not the bug. With the real rule, a
  // base_url missing its "/v1" fails here the way it fails for a user.
  path = "/responses";
  responseKind = "responses";
  body = { model: "gpt-5.5", stream: false, input: [{ role: "user", content: [{ type: "input_text", text: prompt }] }] };
} else if (wireProtocol === "gemini-generatecontent") {
  path = "/v1beta/models/gemini-2.5-flash:generateContent";
  responseKind = "gemini";
  body = { contents: [{ role: "user", parts: [{ text: prompt }] }], generationConfig: { maxOutputTokens: 32 } };
} else if (wireProtocol === "openai-chat") {
  // Pinned Hermes passes CUSTOM_BASE_URL unchanged to its OpenAI client.
  // Append only the SDK resource path; do not repair its injected route.
  path = id === "hermes" || new URL(baseURL).pathname.replace(/\\/+$/, "").endsWith("/v1") ? "/chat/completions" : "/v1/chat/completions";
  body = { model: "gpt-4o-mini", stream: false, messages: [{ role: "user", content: prompt }] };
} else {
  process.stderr.write(id + ": unsupported wire protocol " + wireProtocol + "\\n");
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

function writeHomeConfig(home, id, originalBase) {
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
      agents: { defaults: { model: { primary: "myprov/claude-sonnet-4-6" } } },
      models: { providers: { myprov: {
        baseUrl: originalBase, apiKey: "${MYPROV_API_KEY}", api: "anthropic-messages",
        models: [{ id: "claude-sonnet-4-6", name: "Claude Sonnet", contextWindow: 200000, maxTokens: 4096 }],
      } } },
    }));
  }
}

test("every shipped profile's selected supported transport preserves recovery through the real compression engine", { timeout: 90_000 }, async (t) => {
  if (!process.env.CAVEMAN_TEST_PROXY_BIN && !goToolchainAvailable) return t.skip("go toolchain not found");
  for (const profile of profiles) {
    assert.ok(protocolCapabilities[fixtureProtocol(profile)], `${profile.id} needs an explicit protocol recovery capability`);
  }
  const upstream = await conformanceUpstream();
  const suiteDir = mkdtempSync(join(tmpdir(), "cave-agent-compression-"));
  const caveHome = join(suiteDir, "cave-home");
  const binDir = join(suiteDir, "bin");
  const proxyBin = join(binDir, "caveman-proxy");
  let proxy;
  try {
    mkdirSync(caveHome, { recursive: true });
    mkdirSync(binDir, { recursive: true });
    for (const profile of profiles) writeAgentStub(binDir, profile);
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
      const profile = profiles.find((candidate) => candidate.id === id);
      assert.ok(profile, `profile ${id} disappeared from registry`);
      const home = join(suiteDir, `home-${id}`);
      writeHomeConfig(home, id, upstream.baseURL);
      const env = {
        ...proxyEnv,
        HOME: home,
        USERPROFILE: home,
        PATH: `${binDir}${delimiter}${process.env.PATH}`,
        CAVEMAN_PROXY_BIN: proxyBin,
        CAVE_GATEWAY_URL: `http://127.0.0.1:${proxyPort}`,
        CAVE_NO_KEYCHAIN: "1",
        NO_COLOR: "1",
        MYPROV_API_KEY: "sk-ant-conformance-anthropic",
        CUSTOM_API_KEY: "sk-conformance-openai",
      };
      delete env.CODEX_HOME;
      delete env.OPENCLAW_CONFIG_PATH;
      delete env.OPENCLAW_STATE_DIR;
      const originalPrompt = `agent=${id}\n${longPrompt}`;
      const requestsBefore = upstream.requests.length;
      upstream.expectPrompt(id, originalPrompt);
      const out = await runCli(cli, ["wrap", id], { env, cwd: suiteDir, timeoutMs: 15_000 });
      assert.equal(out.code, 0, `${id} failed:\n${out.stderr}`);
      assert.match(out.stdout, new RegExp(`^${id}:${responseSentinel}`, "m"), `${id} did not receive unchanged response`);
      assert.equal(upstream.errors.length, 0, upstream.errors.join("\n"));
      assert.equal(upstream.requests.length - requestsBefore, 2, `${id} needs initial compression and an actual retrieval continuation`);
      const request = upstream.requests[requestsBefore];
      assert.ok(request, `${id} sent no upstream request`);
      if (protocolCapabilities[fixtureProtocol(profile)].recovery === "server") {
        assert.notEqual(requestPrompt(request), originalPrompt, `${id} sent original long prompt uncompressed`);
        assert.match(request.raw, /<<ccr:/, `${id} upstream body lacks recovery handle`);
      } else {
        assert.equal(requestPrompt(request), originalPrompt, `${id} changed an unrecoverable request`);
        assert.doesNotMatch(request.raw, /<<ccr:/, `${id} emitted an unusable recovery handle`);
      }
    }

    assert.equal(upstream.requests.length, expectedProfiles.length * 2);
    assert.deepEqual(upstream.recoveries.map((recovery) => recovery.agentId), expectedProfiles);
    const db = new DatabaseSync(join(caveHome, "caveman.db"), { readOnly: true });
    const ccr = new DatabaseSync(join(caveHome, "ccr.db"), { readOnly: true });
    try {
      const rows = db.prepare(`
        SELECT agent_slug, compression_tokens_before, compression_tokens_after, recovery_handle,
               raw_request_sha256, transformed_request_sha256, savings_usd
        FROM requests ORDER BY id
      `).all();
      assert.equal(rows.length, expectedProfiles.length);
      assert.deepEqual(rows.map((row) => row.agent_slug), expectedProfiles);
      for (const row of rows) {
        const profile = profiles.find((candidate) => candidate.id === row.agent_slug);
        assert.ok(profile, `database recorded unknown profile ${row.agent_slug}`);
        if (protocolCapabilities[fixtureProtocol(profile)].recovery === "server") {
          const recovered = upstream.recoveries.find((recovery) => recovery.agentId === row.agent_slug);
          const stored = ccr.prepare("SELECT original, tokens_before, tokens_after FROM recoveries WHERE handle = ?").get(recovered.handle);
          assert.ok(stored, `${row.agent_slug} did not persist the provider-requested CCR handle`);
          assert.equal(Buffer.from(stored.original).toString("utf8"), recovered.originalPrompt, `${row.agent_slug} CCR store changed original bytes`);
          assert.ok(Number(stored.tokens_before) > Number(stored.tokens_after), `${row.agent_slug} stored no compression token reduction`);
          assert.notEqual(row.raw_request_sha256, row.transformed_request_sha256, `${row.agent_slug} hashes claim no transform`);
          // A retrieval sends the original back upstream. Keep its compression
          // evidence in CCR without booking savings for that logical request.
          assert.equal(Number(row.compression_tokens_before), 0);
          assert.equal(Number(row.compression_tokens_after), 0);
          assert.equal(row.recovery_handle, "");
          assert.equal(Number(row.savings_usd), 0);
        } else {
          assert.equal(Number(row.compression_tokens_before), 0, `${row.agent_slug} claimed input compression without recovery`);
          assert.equal(Number(row.compression_tokens_after), 0, `${row.agent_slug} claimed output compression without recovery`);
          assert.ok(row.recovery_handle == null || row.recovery_handle === "", `${row.agent_slug} recorded a recovery handle without recovery support`);
          assert.equal(row.raw_request_sha256, row.transformed_request_sha256, `${row.agent_slug} changed bytes without recovery support`);
        }
      }
    } finally {
      db.close();
      ccr.close();
    }
  } finally {
    await stopChild(proxy);
    await upstream.close();
    rmSync(suiteDir, { recursive: true, force: true });
  }
});
