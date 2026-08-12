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
