import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";
import { ProviderRouter } from "../dist/testable.mjs";

// Load the SDK Pi itself resolves, not this extension's independent peer. A
// workspace can have a different peer version (ours has 0.83.0 alongside the
// pinned host's 0.84.2), which would otherwise test the wrong serializer.
const hostRequire = createRequire(import.meta.resolve("@earendil-works/pi-coding-agent"));
const sdkManifest = hostRequire.resolve.paths("@earendil-works/pi-ai")
  .map(root => join(root, "@earendil-works/pi-ai/package.json")).find(existsSync);
assert.ok(sdkManifest, "Pi's SDK dependency must be installed");
assert.equal(JSON.parse(readFileSync(sdkManifest, "utf8")).version, "0.84.2", "Review compat detection when updating the pinned Pi SDK");
const sdkStream = async api => (await import(pathToFileURL(join(dirname(sdkManifest), "dist/api", `${api}.js`)))).stream;
const GATEWAY = "http://127.0.0.1:8787";
const STREAMS = Object.fromEntries(await Promise.all(["openai-completions", "openai-responses", "anthropic-messages"]
  .map(async api => [api, await sdkStream(api)])));
const OPTIONS = { apiKey: "fake-local-only", maxTokens: 2048, reasoningEffort: "low", thinkingEnabled: true,
  thinkingBudgetTokens: 1024, sessionId: "fixture-session", maxRetries: 0, env: {} };
const OPENROUTER_HEADERS = { "HTTP-Referer": "https://pi.dev", "X-OpenRouter-Title": "pi", "X-OpenRouter-Categories": "cli-agent" };

function fixture(baseUrl, extra = {}) {
  return { provider: "relay", id: "fixture", api: "openai-completions", baseUrl, name: "Fixture", reasoning: true,
    input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 32768, maxTokens: 4096,
    thinkingLevelMap: { low: "low" }, ...extra };
}

function context(model) {
  return {
    systemPrompt: "Keep the account boundary.",
    messages: [
      { role: "user", content: "Look up the record.", timestamp: 1 },
      { role: "assistant", content: [{ type: "text", text: "I will check." },
        { type: "toolCall", id: "call_fixture", name: "lookup", arguments: { account: "test" } }],
        api: model.api, provider: model.provider, model: model.id, timestamp: 2, stopReason: "toolUse",
        usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } } },
      { role: "toolResult", toolCallId: "call_fixture", toolName: "lookup", content: [{ type: "text", text: "Found." }], isError: false, timestamp: 3 },
      { role: "user", content: "Summarize it.", timestamp: 4 },
    ],
    tools: [{ name: "lookup", description: "Look up one account.", parameters: {
      type: "object", properties: { account: { type: "string" } }, required: ["account"], additionalProperties: false,
    } }],
  };
}

// Actual pinned SDK serialization, intercepted at fetch before any network.
// A fixed non-retryable response ends the stream after capturing the request.
async function capture(model, options = {}) {
  const requests = [];
  const result = await STREAMS[model.api](model, context(model), { ...OPTIONS, ...options, fetch: async (url, init) => {
    requests.push({ url: String(url), body: JSON.parse(init.body), headers: Object.fromEntries(new Headers(init.headers)) });
    return new Response('{"error":{"message":"local serialization capture complete"}}', {
      status: 400, headers: { "content-type": "application/json" },
    });
  } }).result();
  assert.equal(requests.length, 1, result.errorMessage);
  assert.equal(result.stopReason, "error");
  return requests[0];
}

async function routed(model) {
  let selected = model;
  const notices = [];
  const ctx = { get model() { return selected; }, sessionManager: { getSessionId: () => OPTIONS.sessionId },
    modelRegistry: { isUsingOAuth: () => false, getApiKeyAndHeaders: async () => ({ ok: true, apiKey: OPTIONS.apiKey }) } };
  const router = new ProviderRouter({
    async setModel(value) { selected = value; return true; },
    registerProvider() { assert.fail("must not modify provider registry"); },
    unregisterProvider() { assert.fail("must not modify provider registry"); },
  }, message => notices.push(message));
  await router.openGate(GATEWAY, ctx, { [model.provider]: model.baseUrl }, {}, { [model.provider]: Object.keys(model.headers ?? {}) });
  assert.equal(router.routing(), true, notices.join("\n"));
  return { router, ctx, get selected() { return selected; } };
}

const COMPLETION_MODELS = [
  ["Together alias", fixture("https://api.together.xyz/v1")],
  ["Together ai alias", fixture("https://api.together.ai/v1")],
  // These URL-detector fixtures use /v1 so the route's independent path proof
  // succeeds. They certify SDK serialization, not live provider availability.
  ["z.ai host detection", fixture("https://api.z.ai/v1")],
  ["Bigmodel host detection", fixture("https://open.bigmodel.cn/v1")],
  ["Moonshot alias", fixture("https://api.moonshot.ai/v1")],
  ["OpenRouter alias", fixture("https://openrouter.ai/api/v1", { headers: OPENROUTER_HEADERS, compat: { sendSessionAffinityHeaders: true } })],
  ["OpenRouter OpenAI model alias", fixture("https://openrouter.ai/api/v1", { id: "openai/fixture", headers: OPENROUTER_HEADERS })],
  ["Cloudflare Workers alias", fixture("https://api.cloudflare.com/client/v4/accounts/fixture/ai/v1", { headers: { "User-Agent": "pi-coding-agent" } })],
  ["Cloudflare gateway host detection", fixture("https://gateway.ai.cloudflare.com/v1/fixture/compat/v1", { headers: { "User-Agent": "pi-coding-agent" } })],
  ["NVIDIA alias", fixture("https://integrate.api.nvidia.com/v1", { headers: { "X-BILLING-INVOKE-ORIGIN": "Pi" } })],
  ["Ant Ling alias", fixture("https://api.ant-ling.com/v1")],
  ["Cerebras alias", fixture("https://api.cerebras.ai/v1")],
  ["Grok alias", fixture("https://api.x.ai/v1")],
  ["DeepSeek alias", fixture("https://api.deepseek.com/v1")],
  ["DeepSeek uppercase URL", fixture("https://API.DEEPSEEK.COM/v1")],
  ["Chutes alias", fixture("https://llm.chutes.ai/v1")],
  ["OpenCode alias", fixture("https://opencode.ai/zen/v1", { headers: { "x-opencode-session": OPTIONS.sessionId, "x-opencode-client": "pi" } })],
  ["canonical Together", fixture("https://api.together.xyz/v1", { provider: "together" })],
  ["canonical DeepSeek", fixture("https://api.deepseek.com/v1", { provider: "deepseek" })],
  ["canonical OpenRouter Anthropic", fixture("https://openrouter.ai/api/v1", { provider: "openrouter", id: "anthropic/fixture" })],
  ["generic relay", fixture("https://relay.example/v1")],
  ["explicit overrides", fixture("https://api.together.xyz/v1", { compat: {
    supportsStore: true, supportsDeveloperRole: true, supportsReasoningEffort: true,
    maxTokensField: "max_completion_tokens", thinkingFormat: "openai", supportsStrictMode: true,
    requiresReasoningContentOnAssistantMessages: false, supportsLongCacheRetention: true,
    supportsUsageInStreaming: false, requiresToolResultName: true, requiresAssistantAfterToolResult: true,
    sendSessionAffinityHeaders: true, sessionAffinityFormat: "openai-nosession",
    openRouterRouting: { order: ["fixture"] }, vercelGatewayRouting: { only: ["fixture"] },
  } })],
];

test("real Pi completion payloads preserve original URL compatibility and explicit overrides", async t => {
  for (const [name, original] of COMPLETION_MODELS) await t.test(name, async () => {
    const snapshot = structuredClone(original);
    const h = await routed(original);
    for (const cacheRetention of ["none", "short", "long"]) {
      for (const reasoningEffort of [undefined, "low"]) {
        const options = { cacheRetention, reasoningEffort };
        const direct = await capture(original, options), proxied = await capture(h.selected, options);
        assert.deepEqual(proxied.body, direct.body, `${name}: ${cacheRetention}, ${reasoningEffort}`);
        assert.deepEqual(proxied.headers, direct.headers, `${name}: headers`);
        assert.match(proxied.url, /\/w\/pi\/compat\/[^/]+\/v1\/chat\/completions$/);
      }
    }
    await h.router.apply(h.selected, h.ctx); // Repeated gate checks retain originals.
    assert.equal(await h.router.closeGate(h.ctx), true);
    assert.deepEqual(h.selected, snapshot);
    assert.deepEqual(original, snapshot);
    assert.equal(h.selected.compat, original.compat);
    assert.equal(Object.hasOwn(h.selected, "compat"), Object.hasOwn(original, "compat"));
  });
});

test("real Pi Responses and Anthropic requests retain body and affinity settings", async t => {
  for (const original of [
    fixture("https://openrouter.ai/api/v1", { api: "openai-responses", headers: OPENROUTER_HEADERS }),
    fixture("https://api.openai.com/v1", { api: "openai-responses", provider: "openai" }),
    fixture("https://openrouter.ai/api/v1", { api: "openai-responses", headers: OPENROUTER_HEADERS, compat: { sessionAffinityFormat: "openai-nosession", supportsDeveloperRole: false } }),
    fixture("https://api.anthropic.com", { api: "anthropic-messages", provider: "anthropic" }),
    fixture("https://anthropic-relay.example", { api: "anthropic-messages", compat: {
      supportsEagerToolInputStreaming: false, supportsLongCacheRetention: false, sendSessionAffinityHeaders: true,
    } }),
  ]) await t.test(`${original.api} ${original.provider} ${original.baseUrl} ${JSON.stringify(original.compat)}`, async () => {
    const h = await routed(original);
    for (const cacheRetention of ["none", "short", "long"]) {
      const direct = await capture(original, { cacheRetention }), proxied = await capture(h.selected, { cacheRetention });
      assert.deepEqual(proxied.body, direct.body);
      assert.deepEqual(proxied.headers, direct.headers);
    }
    assert.equal(await h.router.closeGate(h.ctx), true);
    assert.deepEqual(h.selected, original);
  });
});

test("OpenAI Chat stays direct because Pi exposes no override for its URL-derived cache key", async () => {
  for (const provider of ["openai", "relay"]) {
    const original = fixture("https://api.openai.com/v1", { provider });
    let selected = original;
    const notices = [];
    const ctx = { get model() { return selected; }, modelRegistry: { isUsingOAuth: () => false } };
    const router = new ProviderRouter({ async setModel(model) { selected = model; return true; } }, message => notices.push(message));
    await router.openGate(GATEWAY, ctx, { [provider]: original.baseUrl });
    assert.equal(router.routing(), false);
    assert.equal(selected, original);
    assert.match(notices[0], /cannot preserve this OpenAI Chat endpoint's prompt cache keys.*no compression/);
    const direct = await capture(original, { cacheRetention: "short" });
    const rewritten = await capture({ ...original, baseUrl: `${GATEWAY}/w/pi/openai/v1` }, { cacheRetention: "short" });
    assert.equal(direct.body.prompt_cache_key, OPTIONS.sessionId);
    assert.equal(Object.hasOwn(rewritten.body, "prompt_cache_key"), false);
    const disabled = await capture(original, { cacheRetention: "none" });
    assert.equal(Object.hasOwn(disabled.body, "prompt_cache_key"), false);
    assert.equal(await router.closeGate(ctx), true);
  }
});
