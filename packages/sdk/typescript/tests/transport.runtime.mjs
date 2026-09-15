import { after, test } from "node:test";
import assert from "node:assert/strict";
import { Cave } from "../dist/index.js";

const originalFetch = globalThis.fetch;
after(() => { globalThis.fetch = originalFetch; });

test("stalled compression settles at client deadline with byte-exact pass-through", async () => {
  globalThis.fetch = async () => new Promise(() => {});
  const cave = new Cave({ apiKey: "cave-test", baseURL: "http://gateway.invalid", agent: "test", timeoutMs: 20 });
  const started = Date.now();
  const result = await cave.compress('{"keep":"exact"}');
  assert.equal(result.output, '{"keep":"exact"}');
  assert.equal(result.ratio, 0);
  assert.equal(result.recoveryHandle, undefined);
  assert.ok(Date.now() - started < 500, "compression deadline did not settle promptly");
});

test("stalled state reads reject with stable timeout and caller cancellation", async () => {
  globalThis.fetch = async () => new Promise(() => {});
  const timed = new Cave({ apiKey: "cave-test", baseURL: "http://gateway.invalid", agent: "test", timeoutMs: 20 });
  await assert.rejects(timed.cavePlan(), /cave_request_timeout/);

  const controller = new AbortController();
  const cancelled = new Cave({ apiKey: "cave-test", baseURL: "http://gateway.invalid", agent: "test", timeoutMs: 5_000, signal: controller.signal });
  const request = cancelled.toolSearch([], "query");
  controller.abort(new Error("caller_cancelled"));
  await assert.rejects(request, /caller_cancelled/);
});

test("invalid client deadlines fail before any request", () => {
  for (const timeoutMs of [0, -1, 1.5, Number.NaN]) {
    assert.throws(
      () => new Cave({ apiKey: "cave-test", baseURL: "http://gateway.invalid", agent: "test", timeoutMs }),
      /timeoutMs must be a positive integer/,
    );
  }
});

test("service URLs normalize trailing slashes and reject query or fragment state", async () => {
  let requested;
  globalThis.fetch = async (input) => {
    requested = String(input);
    return new Response(JSON.stringify({
      output: "small",
      content_type: "text/plain",
      tokens_before: 2,
      tokens_after: 1,
    }), { status: 200, headers: { "content-type": "application/json" } });
  };
  const cave = new Cave({
    apiKey: "cave-test",
    baseURL: "https://gateway.example///",
    controlURL: "https://control.example/",
    agent: "test",
  });
  assert.equal(cave.options.baseURL, "https://gateway.example");
  assert.equal(cave.options.controlURL, "https://control.example");
  await cave.compress("large input");
  assert.equal(requested, "https://gateway.example/sdk/v1/compress");

  for (const baseURL of ["https://gateway.example/?tenant=x", "https://gateway.example/#unsafe", "relative/path"]) {
    assert.throws(
      () => new Cave({ apiKey: "cave-test", baseURL, agent: "test" }),
      /baseURL must/,
    );
  }
});

test("provider.raw preserves Request method, body, headers, and caller cancellation", async () => {
  let captured;
  globalThis.fetch = async (input, init) => {
    const request = new Request(input, init);
    captured = {
      method: request.method,
      body: await request.text(),
      contentType: request.headers.get("content-type"),
      custom: request.headers.get("x-custom"),
      authorization: request.headers.get("authorization"),
      signal: request.signal,
    };
    return new Promise((_resolve, reject) => {
      request.signal.addEventListener("abort", () => reject(request.signal.reason), { once: true });
    });
  };
  const controller = new AbortController();
  const cave = new Cave({ apiKey: "cave-test", baseURL: "http://gateway.invalid", agent: "test", timeoutMs: 5_000 });
  const request = new Request("http://gateway.invalid/openai/v1/custom", {
    method: "POST",
    body: "raw-payload",
    headers: { "content-type": "text/plain", "x-custom": "kept", authorization: "Bearer attacker" },
    signal: controller.signal,
  });
  const pending = cave.openai().raw(request);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(captured.method, "POST");
  assert.equal(captured.body, "raw-payload");
  assert.equal(captured.contentType, "text/plain");
  assert.equal(captured.custom, "kept");
  assert.equal(captured.authorization, "Bearer cave-test");
  assert.equal(captured.signal.aborted, false);
  controller.abort(new Error("raw_request_cancelled"));
  await assert.rejects(pending, /raw_request_cancelled/);
  assert.equal(captured.signal.aborted, true);
});
