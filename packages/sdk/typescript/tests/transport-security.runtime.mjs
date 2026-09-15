import { test } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFileSync } from "node:fs";
import { Cave } from "../dist/index.js";

const contract = JSON.parse(readFileSync(new URL("../../parity/fixtures.json", import.meta.url), "utf8")).transport;
const nativeFetch = globalThis.fetch;

async function server(t, handler) {
  const instance = createServer(handler);
  await new Promise((resolve, reject) => {
    instance.once("error", reject);
    instance.listen(0, "127.0.0.1", resolve);
  });
  t.after(() => new Promise((resolve) => {
    instance.close(resolve);
    instance.closeAllConnections();
  }));
  return `http://127.0.0.1:${instance.address().port}`;
}

async function redirectFixture(t, status = 302, origin = "cross") {
  const redirected = [];
  const requests = [];
  const receive = (req, res) => {
    redirected.push({ headers: req.headers, method: req.method });
    req.resume();
    res.setHeader("content-type", "application/json");
    res.end(JSON.stringify(contract.provider_response));
  };
  const target = await server(t, receive);
  const baseURL = await server(t, (req, res) => {
    if (req.url === "/redirected") return receive(req, res);
    let body = "";
    req.setEncoding("utf8");
    req.on("data", (chunk) => { body += chunk; });
    req.on("end", () => {
      requests.push({ headers: req.headers, method: req.method, path: req.url, body });
      res.writeHead(status, { location: `${origin === "same" ? baseURL : target}/redirected` });
      res.end();
    });
  });
  return { baseURL, requests, redirected, cave: new Cave({ apiKey: "cave-fixture-key", baseURL, agent: "transport-test" }) };
}

for (const status of contract.redirect_statuses) {
  for (const origin of contract.redirect_origins) {
    test(`provider credentials stay on the gateway after ${status} ${origin}-origin redirect`, async (t) => {
      const fixture = await redirectFixture(t, status, origin);
      await assert.rejects(fixture.cave.openai({ upstreamKey: "upstream-fixture-key" }).responses.create(contract.provider_body));
      assert.equal(fixture.requests.length, 1);
      assert.deepEqual(JSON.parse(fixture.requests[0].body), contract.provider_body);
      assert.equal(fixture.requests[0].headers.authorization, "Bearer cave-fixture-key");
      assert.equal(fixture.requests[0].headers["x-cave-upstream-key"], "upstream-fixture-key");
      assert.deepEqual(fixture.redirected, [], "redirect target received gateway credentials or request bytes");
    });
  }
}

test("raw callers cannot enable redirects and OTLP keeps its custom API key on the gateway", async (t) => {
  const fixture = await redirectFixture(t);
  await assert.rejects(fixture.cave.openai({ upstreamKey: "upstream-fixture-key" }).raw(`${fixture.baseURL}/openai/v1/responses`, {
    method: "POST", body: JSON.stringify(contract.provider_body), redirect: "follow",
  }));
  const exporter = fixture.cave.exporter();
  exporter.recordSpan("fixture");
  await assert.rejects(exporter.export());
  assert.equal(exporter.pending, 1, "a rejected redirect must retain the unsent span");
  assert.equal(fixture.requests.length, 2);
  assert.equal(fixture.requests[1].headers["x-cave-api-key"], "cave-fixture-key");
  assert.deepEqual(fixture.redirected, []);
});

test("raw stream bytes, response metadata, and cloned response stay intact", async (t) => {
  const bytes = Buffer.from('data: {"text":"café  火"}\r\n\r\ndata: [DONE]\r\n\r\n');
  const baseURL = await server(t, (req, res) => {
    req.resume();
    res.writeHead(201, { "content-type": "text/event-stream", "x-fixture": "preserved" });
    res.write(bytes.subarray(0, 20));
    res.end(bytes.subarray(20));
  });
  const cave = new Cave({ apiKey: "cave-fixture-key", baseURL, agent: "transport-test" });
  const response = await cave.openai().raw(`${baseURL}/openai/v1/responses`);
  const clone = response.clone();
  for (const value of [response, clone]) {
    assert.equal(value.status, 201);
    assert.equal(value.url, `${baseURL}/openai/v1/responses`);
    assert.equal(value.headers.get("x-fixture"), "preserved");
  }
  const bodies = await Promise.all([response.arrayBuffer(), clone.arrayBuffer()]);
  for (const body of bodies) assert.deepEqual(Buffer.from(body), bytes);
});

test("custom fetch with a stalled Response body reaches the original deadline and cancels its source", async (t) => {
  let cancelled = false;
  t.after(() => { globalThis.fetch = nativeFetch; });
  globalThis.fetch = async () => new Response(new ReadableStream({
    cancel() { cancelled = true; },
  }), { headers: { "content-type": "application/json" } });
  const cave = new Cave({ apiKey: "fixture", baseURL: "http://gateway.invalid", agent: "fixture", timeoutMs: 20 });
  const result = await cave.compress("original bytes");
  assert.equal(result.output, "original bytes");
  assert.equal(result.ratio, 0);
  assert.equal(result.recoveryHandle, undefined);
  assert.equal(cancelled, true);
});

test("custom decoder-only fetch adapters cannot bypass the body deadline", async (t) => {
  t.after(() => { globalThis.fetch = nativeFetch; });
  globalThis.fetch = async () => ({ ok: true, json: async () => new Promise(() => {}) });
  const cave = new Cave({ apiKey: "fixture", baseURL: "http://gateway.invalid", agent: "fixture", timeoutMs: 20 });
  await assert.rejects(cave.cavePlan(), /cave_request_timeout/);
});

test("status-only failures cancel unread response bodies immediately", async (t) => {
  let cancelled = 0;
  t.after(() => { globalThis.fetch = nativeFetch; });
  globalThis.fetch = async () => new Response(new ReadableStream({
    start(controller) { controller.enqueue(new TextEncoder().encode("unread error body")); },
    cancel() { cancelled += 1; },
  }), { status: 503 });
  const cave = new Cave({ apiKey: "fixture", baseURL: "http://gateway.invalid", agent: "fixture" });
  await assert.rejects(cave.cavePlan(), /HTTP 503/);
  const result = await cave.compress("original bytes");
  assert.equal(result.output, "original bytes");
  assert.equal(result.ratio, 0);
  await new Promise(setImmediate);
  assert.equal(cancelled, 2, "discarded bodies must release their streams and deadline timers");
});

test("caller cancellation reaches raw readers after custom fetch has returned headers", async (t) => {
  let cancelled = false;
  t.after(() => { globalThis.fetch = nativeFetch; });
  globalThis.fetch = async () => new Response(new ReadableStream({
    start(controller) { controller.enqueue(new TextEncoder().encode("first chunk")); },
    cancel() { cancelled = true; },
  }));
  const controller = new AbortController();
  const cave = new Cave({ apiKey: "fixture", baseURL: "http://gateway.invalid", agent: "fixture", signal: controller.signal });
  const response = await cave.openai().raw("http://gateway.invalid/openai/v1/responses");
  const reader = response.body.getReader();
  assert.equal(new TextDecoder().decode((await reader.read()).value), "first chunk");
  const pending = reader.read();
  controller.abort(new Error("caller_cancelled"));
  await assert.rejects(pending, /caller_cancelled/);
  assert.equal(cancelled, true);
});
