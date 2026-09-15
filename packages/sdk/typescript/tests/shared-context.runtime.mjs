/**
 * Runtime tests for the TS SDK shared-context surface (S12).
 * Run with: node --test packages/sdk-ts/tests/shared-context.runtime.mjs
 * Mirrors the Python tests/test_shared_context.py.
 */
import { test, after } from "node:test";
import assert from "node:assert/strict";

import { Cave } from "../dist/index.js";

let captured = [];
let nextResponse = {};
const originalFetch = globalThis.fetch;

function mockFetch(responseData) {
  nextResponse = responseData;
  captured = [];
  globalThis.fetch = async (url, init) => {
    captured.push({ url: String(url), method: init?.method, body: init?.body ? JSON.parse(init.body) : undefined });
    return { ok: true, json: async () => nextResponse };
  };
}

after(() => { globalThis.fetch = originalFetch; });

const cave = new Cave({ apiKey: "cave_live_test", baseURL: "http://localhost:8787", agent: "a" });

test("sharedContext.put posts session_key and content", async () => {
  mockFetch({ session_key: "h1", bytes: 5, stored: true });
  const out = await cave.sharedContext.put("h1", "hello");
  assert.equal(captured[0].url, "http://localhost:8787/sdk/v1/shared-context");
  assert.equal(captured[0].method, "POST");
  assert.deepEqual(captured[0].body, { session_key: "h1", content: "hello" });
  assert.equal(out.stored, true);
});

test("sharedContext.get recovers content byte-exact", async () => {
  mockFetch({ session_key: "h1", content: "héllo 🦴" });
  const out = await cave.sharedContext.get("h1");
  assert.equal(captured[0].url, "http://localhost:8787/sdk/v1/shared-context/h1");
  assert.equal(captured[0].method, "GET");
  assert.equal(out.content, "héllo 🦴");
});
