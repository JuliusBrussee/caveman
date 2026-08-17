import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";

import { startSink, classifyRequest, redactHeaders, redactUrl, redactBody, wantsStream } from "../lib/sink.mjs";

test("redactBody scrubs emails and credential-shaped strings, keeps JSON valid", () => {
  const body = {
    model: "m",
    system: "The user's email address is person@example.com. Key: sk-ant-api03-abcdefghijklmnop. Path /Users/x stays.",
    messages: [],
  };
  const out = redactBody(body);
  assert.equal(typeof out, "object");
  assert.ok(!JSON.stringify(out).includes("person@example.com"));
  assert.ok(!JSON.stringify(out).includes("sk-ant-api03"));
  assert.match(out.system, /redacted:email:[0-9a-f]{8}/);
  assert.match(out.system, /redacted:key:[0-9a-f]{8}/);
  assert.ok(out.system.includes("/Users/x stays"), "non-secret content untouched");
});

test("classifyRequest routes every protocol", () => {
  assert.equal(classifyRequest("POST", "/v1/messages"), "anthropic-messages");
  assert.equal(classifyRequest("POST", "/v1/messages/count_tokens"), "anthropic-count-tokens");
  assert.equal(classifyRequest("POST", "/v1/chat/completions"), "openai-chat");
  assert.equal(classifyRequest("POST", "/v1/responses"), "openai-responses");
  assert.equal(classifyRequest("POST", "/backend-api/codex/responses"), "openai-responses");
  assert.equal(classifyRequest("POST", "/v1beta/models/gemini-2.5-pro:generateContent"), "gemini-generatecontent");
  assert.equal(classifyRequest("POST", "/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse"), "gemini-generatecontent");
  assert.equal(classifyRequest("GET", "/v1/models"), "openai-models");
  assert.equal(classifyRequest("GET", "/v1beta/models"), "gemini-models");
  assert.equal(classifyRequest("POST", "/telemetry"), "other");
});

test("account/session identifiers are redacted, not just credentials", () => {
  const headers = redactHeaders({
    "x-claude-code-session-id": "sess-abc",
    "x-codex-turn-metadata": '{"installation_id":"i-1","session_id":"s-1"}',
    "x-gemini-api-privileged-user-id": "user-9",
    "x-session-id": "oc-1",
    "x-session-affinity": "aff-1",
    "x-codex-window-id": "w-1",
  });
  const dumped = JSON.stringify(headers);
  for (const secret of ["sess-abc", "installation_id", "user-9", "oc-1", "aff-1", "w-1"]) {
    assert.ok(!dumped.includes(secret), `${secret} must not reach disk`);
  }
});

test("body identifier fields are redacted by key, at any depth", () => {
  const out = redactBody({
    model: "m",
    metadata: { user_id: { device_id: "dev-123", account_uuid: "acct-456" } },
    prompt_cache_key: "pck-789",
    safety_identifier: "safe-1",
    messages: [{ role: "user", content: "keep me" }],
  });
  const dumped = JSON.stringify(out);
  for (const secret of ["dev-123", "acct-456", "pck-789", "safe-1"]) {
    assert.ok(!dumped.includes(secret), `${secret} must not reach disk`);
  }
  assert.ok(dumped.includes("keep me"), "non-identifier content survives");
});

test("redaction: headers, arrays, query params; hashes stay comparable", () => {
  const headers = redactHeaders({ "X-Api-Key": "sk-secret", authorization: ["Bearer a", "Bearer a"], accept: "application/json" });
  assert.match(headers["x-api-key"], /^redacted:sha256:[0-9a-f]{12}$/);
  assert.equal(headers.authorization[0], headers.authorization[1]);
  assert.equal(headers.accept, "application/json");
  assert.ok(!JSON.stringify(headers).includes("sk-secret"));

  const url = redactUrl("/v1beta/models/x:generateContent?alt=sse&key=AIzaSecret");
  assert.ok(!url.includes("AIzaSecret"));
  assert.ok(url.includes("alt=sse"));
});

test("wantsStream detection", () => {
  assert.equal(wantsStream("anthropic-messages", "/v1/messages", {}, { stream: true }), true);
  assert.equal(wantsStream("anthropic-messages", "/v1/messages", {}, { stream: false }), false);
  assert.equal(wantsStream("gemini-generatecontent", "/v1beta/models/x:streamGenerateContent", {}, {}), true);
  assert.equal(wantsStream("openai-responses", "/v1/responses", { accept: "text/event-stream" }, {}), true);
});

test("sink end-to-end: captures, replies, redacts on disk", async () => {
  const captureDir = mkdtempSync(join(tmpdir(), "sat-sink-"));
  const seen = [];
  const { server, port } = await startSink({ port: 0, captureDir, onCapture: (c) => seen.push(c) });
  const url = (p) => `http://127.0.0.1:${port}${p}`;

  const res = await fetch(url("/v1/messages"), {
    method: "POST",
    headers: { "content-type": "application/json", "x-api-key": "sk-super-secret" },
    body: JSON.stringify({ model: "claude-opus-5", system: "s", tools: [], messages: [] }),
  });
  const body = await res.json();
  assert.equal(body.type, "message");
  assert.equal(body.stop_reason, "end_turn");
  assert.equal(body.content[0].text, "DONE");

  const sse = await fetch(url("/v1/responses"), {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ model: "gpt-5.5", stream: true, instructions: "i", input: [] }),
  });
  const text = await sse.text();
  assert.ok(sse.headers.get("content-type").includes("text/event-stream"));
  assert.ok(text.includes("event: response.created"));
  assert.ok(text.includes("event: response.completed"));
  assert.ok(text.includes('"DONE"'));

  const chat = await fetch(url("/v1/chat/completions"), {
    method: "POST",
    body: JSON.stringify({ model: "gpt-4o", stream: true, messages: [] }),
  });
  const chatText = await chat.text();
  assert.ok(chatText.trimEnd().endsWith("data: [DONE]"));

  const gem = await fetch(url("/v1beta/models/gemini-2.5-pro:generateContent"), {
    method: "POST",
    body: JSON.stringify({ contents: [] }),
  });
  const gemBody = await gem.json();
  assert.equal(gemBody.candidates[0].finishReason, "STOP");

  server.close();

  const files = readdirSync(captureDir).sort();
  assert.equal(files.length, 4);
  assert.equal(seen.length, 4);
  const first = JSON.parse(readFileSync(join(captureDir, files[0]), "utf8"));
  assert.equal(first.kind, "anthropic-messages");
  assert.equal(first.body.model, "claude-opus-5");
  for (const f of files) {
    assert.ok(!readFileSync(join(captureDir, f), "utf8").includes("sk-super-secret"), `${f} leaked a secret`);
  }
});
