import { createHash } from "node:crypto";
import { after, test } from "node:test";
import assert from "node:assert/strict";

import { AssemblyStabilityError, Cave } from "../dist/index.js";

const originalFetch = globalThis.fetch;

after(() => {
  globalThis.fetch = originalFetch;
});

function cave(agent) {
  return new Cave({
    apiKey: "cave_live_test",
    baseURL: "http://localhost:8787",
    agent,
    defaultWorkflow: "assembly"
  });
}

function slots(turn) {
  return [
    {
      id: "tools",
      stability: "stable",
      content: [{ name: "lookup", description: "Lookup one record", input_schema: { type: "object" } }]
    },
    { id: "system", stability: "stable", content: "You are exact." },
    { id: "playbook", stability: "session", content: "Escalate refunds above 100." },
    { id: "turn", stability: "volatile", content: `turn ${turn}` },
    { id: "clock", stability: "volatile", content: `2026-07-26T12:00:${String(turn).padStart(2, "0")}Z` }
  ];
}

function containsKey(value, key) {
  if (Array.isArray(value)) return value.some((item) => containsKey(item, key));
  if (value && typeof value === "object") {
    return Object.entries(value).some(([name, child]) => name === key || containsKey(child, key));
  }
  return false;
}

function stableJSON(value) {
  if (value === null || typeof value !== "object") return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`;
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(",")}}`;
}

test("assemble keeps prefix bytes identical across 10 volatile turns", () => {
  const client = cave("assembly-ten-turn");
  const hashes = [];
  for (let turn = 0; turn < 10; turn++) {
    const built = client.assemble({
      provider: "anthropic",
      model: "claude-test",
      sessionId: "ten-turn",
      slots: slots(turn)
    });
    const prefix = {
      model: built.request.model,
      system: built.request.system,
      tools: built.request.tools
    };
    hashes.push(createHash("sha256").update(stableJSON(prefix)).digest("hex"));
    assert.equal(built.prefixHash, hashes[turn]);
    assert.equal(containsKey(built.request, "cache_control"), false, "gateway default must emit zero cache_control keys");
    assert.deepEqual(built.breakpoints, []);
    assert.equal(built.basis, "inferred");
    assert.equal(built.tokenBasis, "estimated_bytes_div_4");
    assert.equal(built.volatileBelowBreakpoint, true);
  }
  assert.equal(new Set(hashes).size, 1);
});

test("assemble hashes canonical UTF-8 prefix bytes", () => {
  const client = cave("assembly-unicode");
  const built = client.assemble({
    provider: "anthropic",
    model: "claude-test",
    sessionId: "unicode",
    slots: [
      { id: "system", stability: "stable", content: "Précis 🦴" },
      { id: "turn", stability: "volatile", content: "réponds" }
    ]
  });
  const prefix = { model: built.request.model, system: built.request.system };
  assert.equal(built.prefixHash, createHash("sha256").update(stableJSON(prefix)).digest("hex"));
});

test("mutating a stable slot on turn 6 throws its ID and emits no request", () => {
  const client = cave("assembly-stability");
  for (let turn = 0; turn < 5; turn++) {
    client.assemble({
      provider: "anthropic",
      model: "claude-test",
      sessionId: "mutation",
      slots: slots(turn)
    });
  }
  let built;
  assert.throws(
    () => {
      built = client.assemble({
        provider: "anthropic",
        model: "claude-test",
        sessionId: "mutation",
        slots: slots(5).map((slot) => slot.id === "system" ? { ...slot, content: "You changed." } : slot)
      });
    },
    (error) => error instanceof AssemblyStabilityError && error.slotId === "system" && /system/.test(error.message)
  );
  assert.equal(built, undefined);
});

test("anthropic self mirrors tools-first breakpoint; none emits no hints", () => {
  const client = cave("assembly-anthropic-modes");
  const self = client.assemble({
    provider: "anthropic",
    model: "claude-test",
    sessionId: "self",
    slots: slots(1),
    emitCacheHints: "self"
  });
  assert.deepEqual(self.breakpoints, ["tools[0]"]);
  assert.deepEqual(self.request.tools[0].cache_control, { type: "ephemeral" });
  assert.equal(self.request.system.some((block) => "cache_control" in block), false);

  const none = client.assemble({
    provider: "anthropic",
    model: "claude-test",
    sessionId: "none",
    slots: slots(2),
    emitCacheHints: "none"
  });
  assert.equal(containsKey(none.request, "cache_control"), false);
  assert.deepEqual(none.breakpoints, []);
});

test("openai self emits matching prompt_cache_key; unknown provider orders only", () => {
  const client = cave("assembly-other-providers");
  const openai = client.assemble({
    provider: "openai",
    model: "gpt-test",
    sessionId: "openai-self",
    slots: slots(1),
    emitCacheHints: "self"
  });
  assert.equal(openai.request.prompt_cache_key, openai.prefixHash.slice(0, 32));
  assert.deepEqual(openai.breakpoints, ["prompt_cache_key"]);
  assert.deepEqual(openai.request.messages.map((message) => message.role), ["system", "user", "user"]);

  const unknown = client.assemble({
    provider: "other",
    model: "other-model",
    sessionId: "unknown",
    slots: [slots(1)[3], slots(1)[1], slots(1)[2]],
    emitCacheHints: "self"
  });
  assert.deepEqual(unknown.request.slots.map((slot) => slot.stability), ["stable", "session", "volatile"]);
  assert.deepEqual(unknown.breakpoints, []);
});

test("Cave provider clients attach assembly declaration header", async () => {
  let captured;
  globalThis.fetch = async (url, init) => {
    captured = { url: String(url), headers: init.headers, body: JSON.parse(init.body) };
    return { ok: true, json: async () => ({ ok: true }) };
  };
  const client = cave("assembly-header");
  const built = client.assemble({
    provider: "openai",
    model: "gpt-test",
    sessionId: "header",
    slots: slots(1)
  });

  await client.openai().chat.completions.create(built.request);

  assert.equal(captured.url, "http://localhost:8787/openai/v1/chat/completions");
  assert.equal(captured.headers["x-cave-assembly"], built.headers["x-cave-assembly"]);
  assert.deepEqual(captured.body, built.request);
});
