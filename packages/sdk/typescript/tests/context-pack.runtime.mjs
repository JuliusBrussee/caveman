import { after, test } from "node:test";
import assert from "node:assert/strict";

import { Cave } from "../dist/index.js";

const originalFetch = globalThis.fetch;
let captured = [];

after(() => {
  globalThis.fetch = originalFetch;
});

function cave() {
  return new Cave({
    apiKey: "cave_live_test",
    baseURL: "http://localhost:8787",
    agent: "context-test",
    defaultWorkflow: "pack"
  });
}

test("context.pack maps wire request and exact deferred IDs", async () => {
  captured = [];
  globalThis.fetch = async (url, init) => {
    captured.push({ url: String(url), method: init.method, body: JSON.parse(init.body) });
    return {
      ok: true,
      json: async () => ({
        items: [{ id: "deploy", text: "server copy is ignored" }],
        tokens_used: 30,
        tokens_before: 75,
        tokens_saved: 45,
        deferred_count: 2,
        deferred_ids: ["intro", "billing"],
        basis: "inferred"
      })
    };
  };
  const items = [
    { id: "intro", text: "overview", tokens: 20 },
    { id: "deploy", text: "deploy ERROR", tokens: 30, pin: true },
    { id: "billing", text: "billing polish", tokens: 25 }
  ];

  const result = await cave().context.pack("deploy failure", items, {
    maxTokens: 30,
    reserveTokens: 5,
    recencyHalfLifeMs: 3_600_000
  });

  assert.deepEqual(captured, [{
    url: "http://localhost:8787/sdk/v1/context/pack",
    method: "POST",
    body: {
      query: "deploy failure",
      items,
      options: { max_tokens: 30, reserve_tokens: 5, recency_half_life_ms: 3_600_000 }
    }
  }]);
  assert.deepEqual(result.items, [items[1]], "caller-owned bytes must win over response copies");
  assert.deepEqual(result.deferredIds, ["intro", "billing"]);
  assert.equal(result.deferredCount, 2);
  assert.equal(result.tokensSaved, 45);
  assert.equal(result.basis, "inferred");
});

test("context.pack fails closed to all items and honest zero on malformed report", async () => {
  globalThis.fetch = async () => ({
    ok: true,
    json: async () => ({
      items: [{ id: "a" }],
      tokens_used: 5,
      tokens_before: 10,
      tokens_saved: 999,
      deferred_count: 1,
      deferred_ids: ["b"],
      basis: "verified"
    })
  });
  const items = [{ id: "a", text: "alpha" }, { id: "b", text: "beta" }];

  const result = await cave().context.pack("alpha", items, { maxTokens: 5 });

  assert.deepEqual(result.items, items);
  assert.equal(result.tokensSaved, 0);
  assert.deepEqual(result.deferredIds, []);
  assert.equal(result.basis, "inferred");
});
