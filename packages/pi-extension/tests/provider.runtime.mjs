import assert from "node:assert/strict";
import test from "node:test";
import { ModelRegistry, ModelRuntime } from "@earendil-works/pi-coding-agent";
import { ProviderRouter } from "../dist/testable.mjs";
import "./provider-compat.runtime.mjs";

// Test-only oracle: the pinned host's private helper is deliberately not
// imported by shipped code. Its public package exports do not expose it.
const { mergeProviderAttributionHeaders } = await import(new URL("./core/provider-attribution.js", import.meta.resolve("@earendil-works/pi-coding-agent")));

const GATEWAY = "http://127.0.0.1:8787";
const ROUTE = `${GATEWAY}/w/pi/compat/relay/v1`;
const MODELS = [
  { provider: "relay", id: "default", api: "openai-completions", baseUrl: "http://127.0.0.1:4000/v1" },
  { provider: "relay", id: "foreign", api: "openai-completions", baseUrl: "http://127.0.0.1:4001/v1" },
];
const NATIVE = { openai: "https://api.openai.com" };
const COMPAT = { relay: "http://127.0.0.1:4000" };

// Pi assigns the active model before emitting model_select. Keep that model
// separate from the registry and assert the endpoint used by the next request.
function harness(models = MODELS) {
  const registry = models.map((model) => ({ ...model }));
  const calls = [], notices = [];
  let active = { ...registry[0] }, oauth = false, selectBehavior, authBehavior;
  const ctx = {
    get model() { return active; },
    sessionManager: { getSessionId: () => "fixture-session" },
    modelRegistry: {
      getAll: () => registry,
      find: (provider, id) => registry.find((model) => model.provider === provider && model.id === id),
      isUsingOAuth: () => { if (oauth instanceof Error) throw oauth; return oauth; },
      getApiKeyAndHeaders: async () => authBehavior ? authBehavior() : { ok: true, apiKey: "fake-local-test-key" },
    },
  };
  const pi = {
    registerProvider() { assert.fail("routing must not alter another extension's provider registration"); },
    unregisterProvider() { assert.fail("direct mode must not delete provider registrations"); },
    async setModel(model) {
      calls.push(model);
      if (selectBehavior) return selectBehavior(model, (value) => { active = value; });
      active = model;
      await router.apply(model, ctx); // Host model_select echo.
      return true;
    },
  };
  const router = new ProviderRouter(pi, (message) => notices.push(message));
  return {
    router, ctx, calls, notices, registry,
    setOAuth: (value) => { oauth = value; },
    setSelectBehavior: (value) => { selectBehavior = value; },
    setAuthBehavior: (value) => { authBehavior = value; },
    setActive: (value) => { active = value; },
    async select(id) {
      active = { ...registry.find((model) => model.id === id) };
      await router.apply(active, ctx);
    },
  };
}

test("route only the selected model; a same-provider foreign endpoint stays direct", async () => {
  const h = harness();
  await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE);
  assert.equal(h.router.routing(), true, h.notices.join("\n"));
  assert.equal(h.ctx.model.baseUrl, ROUTE);
  assert.deepEqual(h.registry, MODELS);
  await h.select("foreign");
  assert.equal(h.router.routing(), false);
  assert.equal(h.ctx.model.baseUrl, MODELS[1].baseUrl);
  assert.match(h.notices.at(-1), /pass-through for relay\/foreign.*127\.0\.0\.1:4001.*127\.0\.0\.1:4000/);
  await h.select("default");
  assert.equal(h.router.routing(), true);
  assert.equal(h.ctx.model.baseUrl, ROUTE);
  assert.deepEqual(h.registry, MODELS);
});

test("provider switches and closeGate preserve registry and restore active endpoint", async () => {
  const models = [...MODELS, { provider: "openai", id: "gpt-fixture", api: "openai-responses", baseUrl: "https://api.openai.com/v1" }];
  const h = harness(models);
  await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE);
  await h.select("gpt-fixture");
  assert.equal(h.ctx.model.baseUrl, `${GATEWAY}/w/pi/openai/v1`);
  assert.equal(await h.router.closeGate(h.ctx), true);
  assert.equal(h.router.routing(), false);
  assert.deepEqual(h.ctx.model, models[2]);
  assert.deepEqual(h.registry, models);
});

test("OAuth and unknown auth restore the active routed model before claiming direct mode", async () => {
  for (const auth of [true, new Error("auth status unavailable")]) {
    const h = harness();
    await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE);
    h.setOAuth(auth);
    await h.router.apply(h.ctx.model, h.ctx);
    assert.equal(h.router.routing(), false);
    assert.deepEqual(h.ctx.model, MODELS[0]);
    assert.match(h.notices.at(-1), /OAuth\/subscription credentials are not routed/);
  }
});

test("resume and repeated gate openings reapply from the original endpoint", async () => {
  const h = harness();
  await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE);
  const calls = h.calls.length;
  await h.router.apply(h.ctx.model, h.ctx);
  assert.equal(h.calls.length, calls, "unchanged turn events do not reselect the model");
  assert.equal(await h.router.closeGate(h.ctx), true);
  assert.equal(h.ctx.model.baseUrl, MODELS[0].baseUrl);
  await h.router.openGate("http://127.0.0.1:8788", h.ctx, COMPAT, NATIVE);
  assert.equal(h.ctx.model.baseUrl, "http://127.0.0.1:8788/w/pi/compat/relay/v1");
  await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE);
  assert.equal(h.ctx.model.baseUrl, ROUTE);
  assert.deepEqual(h.registry, MODELS);
});

test("a non-loopback replacement gate restores the previous route", async () => {
  const h = harness();
  await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE);
  await h.router.openGate("https://gateway.example", h.ctx, COMPAT, NATIVE);
  assert.equal(h.router.routing(), false);
  assert.equal(h.ctx.model.baseUrl, MODELS[0].baseUrl);
  assert.match(h.notices.at(-1), /gateway is not loopback/);
  await h.router.apply(h.ctx.model, h.ctx);
  assert.equal(h.ctx.model.baseUrl, MODELS[0].baseUrl);
});

test("closeGate preserves an endpoint changed by another extension", async () => {
  const h = harness();
  await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE);
  const changed = { ...h.ctx.model, baseUrl: "http://127.0.0.1:5000/v1" };
  h.setActive(changed);
  const calls = h.calls.length;
  assert.equal(await h.router.closeGate(h.ctx), true);
  assert.equal(h.ctx.model, changed);
  assert.equal(h.calls.length, calls);
  assert.equal(h.router.routing(), false);
});

test("failed selection and errors after assignment restore the original endpoint", async () => {
  for (const partial of [false, true]) {
    const h = harness();
    let first = true;
    h.setSelectBehavior((model, setActive) => {
      if (first) {
        first = false;
        if (partial) { setActive(model); throw new Error("selection failed after assignment"); }
        return false;
      }
      setActive(model);
      return true;
    });
    await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE);
    assert.equal(h.router.routing(), false);
    assert.deepEqual(h.ctx.model, MODELS[0]);
    assert.match(h.notices.at(-1), /direct mode.*model selection failed/);
    assert.deepEqual(h.registry, MODELS);
  }
});

test("failed direct restoration reports the unresolved route and permits retry", async () => {
  const h = harness();
  await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE);
  h.setSelectBehavior(() => false);
  assert.equal(await h.router.closeGate(h.ctx), false);
  assert.equal(h.router.routing(), true);
  assert.equal(h.ctx.model.baseUrl, ROUTE);
  assert.match(h.notices.at(-1), /could not restore the model's direct endpoint/);
  assert.doesNotMatch(h.notices.at(-1), /direct mode|pass-through/);
  h.setSelectBehavior(undefined);
  assert.equal(await h.router.closeGate(h.ctx), true);
  assert.equal(h.router.routing(), false);
  assert.equal(h.ctx.model.baseUrl, MODELS[0].baseUrl);
});

test("unlisted providers stay direct with an actionable mount notice", async () => {
  const h = harness([{ provider: "unlisted-relay", id: "m", api: "openai-completions", baseUrl: "http://127.0.0.1:1/v1" }]);
  await h.router.openGate(GATEWAY, h.ctx, {});
  assert.equal(h.router.routing(), false);
  assert.equal(h.ctx.model.baseUrl, "http://127.0.0.1:1/v1");
  assert.match(h.notices.at(-1), /no compat mount named "unlisted-relay".*add compat\.unlisted-relay\.base_url/);
});

const CUSTOM_HEADERS = { "X-API-Tenant": "private-fixture-tenant", "CF-AIG-Authorization": "private-fixture-auth" };

test("undeclared model headers keep Pi direct with a names-only notice", async () => {
  const original = { ...MODELS[0], headers: CUSTOM_HEADERS };
  const h = harness([original]);
  await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE);
  assert.equal(h.router.routing(), false);
  assert.deepEqual(h.ctx.model, original);
  assert.match(h.notices.at(-1), /CF-AIG-Authorization, X-API-Tenant.*not forwarded.*compat\.relay\.forward_headers/);
  assert.doesNotMatch(h.notices.join("\n"), /private-fixture/);
  assert.equal(await h.router.closeGate(h.ctx), true);
  assert.deepEqual(h.ctx.model, original);
});

test("declared headers on the exact compat mount route and restore the complete model", async () => {
  const original = { ...MODELS[0], headers: CUSTOM_HEADERS, compat: { supportsStore: false } };
  const h = harness([original]);
  await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE, { relay: ["x-api-tenant", "CF-AIG-AUTHORIZATION"] });
  assert.equal(h.router.routing(), true, h.notices.join("\n"));
  assert.equal(h.ctx.model.baseUrl, ROUTE);
  assert.equal(h.ctx.model.headers, CUSTOM_HEADERS);
  assert.deepEqual(h.ctx.model.headers, original.headers);
  assert.equal(await h.router.closeGate(h.ctx), true);
  assert.deepEqual(h.ctx.model, original);
  assert.equal(h.ctx.model.headers, original.headers);
  assert.equal(h.ctx.model.compat, original.compat);
});

test("another mount's declaration and incomplete declarations cannot permit custom headers", async () => {
  for (const allowlists of [
    { other: ["x-api-tenant", "cf-aig-authorization"] },
    { relay: ["x-api-tenant"], other: ["cf-aig-authorization"] },
  ]) {
    const original = { ...MODELS[0], headers: CUSTOM_HEADERS };
    const h = harness([original]);
    await h.router.openGate(GATEWAY, h.ctx, { ...COMPAT, other: COMPAT.relay }, NATIVE, allowlists);
    assert.equal(h.router.routing(), false);
    assert.deepEqual(h.ctx.model, original);
    assert.match(h.notices.at(-1), /CF-AIG-Authorization.*not forwarded/);
    assert.doesNotMatch(h.notices.join("\n"), /private-fixture/);
  }
});

test("a matching allowlist without a compat mount cannot permit custom headers on a native route", async () => {
  const original = { provider: "openai", id: "fixture", api: "openai-responses", baseUrl: "https://api.openai.com/v1", headers: CUSTOM_HEADERS };
  const h = harness([original]);
  await h.router.openGate(GATEWAY, h.ctx, {}, NATIVE, { openai: ["x-api-tenant", "cf-aig-authorization"] });
  assert.equal(h.router.routing(), false);
  assert.deepEqual(h.ctx.model, original);
  assert.match(h.notices.at(-1), /not forwarded/);
});

test("unsupported authentication headers stay direct without suggesting a forwarding override", async () => {
  for (const headers of [{ Authorization: "Basic private-fixture-secret" }, { Authorization: null }, { Authorization: "" }]) {
    const original = { ...MODELS[0], headers }, h = harness([original]);
    await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE, { relay: ["authorization"] });
    assert.equal(h.router.routing(), false);
    assert.deepEqual(h.ctx.model, original);
    assert.match(h.notices.at(-1), /cannot preserve this authentication override; keep the provider direct/);
    assert.doesNotMatch(h.notices.join("\n"), /forward_headers|private-fixture-secret/);
  }
});

test("real Pi provider-level headers require the same exact mount declaration", async () => {
  const credentials = { read: async () => undefined, list: async () => [],
    modify: async () => assert.fail("unexpected credential write"), delete: async () => assert.fail("unexpected credential delete") };
  const runtime = await ModelRuntime.create({ credentials, modelsPath: null, allowModelNetwork: false });
  runtime.registerProvider("relay", {
    api: "openai-completions", baseUrl: "http://127.0.0.1:4000/v1", apiKey: "fake-local-test-key", headers: CUSTOM_HEADERS,
    models: [{ id: "custom-model", name: "Custom model", reasoning: false, input: ["text"],
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 32768, maxTokens: 1024 }],
  });
  const registry = new ModelRegistry(runtime), original = registry.find("relay", "custom-model");
  assert.equal(original.headers, undefined, "Pi resolves provider headers during request preparation");
  assert.deepEqual((await runtime.prepareRequest(original, {})).options.headers, CUSTOM_HEADERS);
  let selected = original;
  const ctx = { get model() { return selected; }, modelRegistry: registry }, notices = [];
  const router = new ProviderRouter({ async setModel(model) { selected = model; return true; } }, message => notices.push(message));
  await router.openGate(GATEWAY, ctx, COMPAT, NATIVE);
  assert.equal(router.routing(), false, "provider-level headers must be checked even when absent from selected model");
  assert.equal(selected, original);
  assert.match(notices.at(-1), /CF-AIG-Authorization, X-API-Tenant.*not forwarded/);
  assert.doesNotMatch(notices.join("\n"), /private-fixture/);
  await router.openGate(GATEWAY, ctx, COMPAT, NATIVE, { relay: ["x-api-tenant", "cf-aig-authorization"] });
  assert.equal(router.routing(), true);
  assert.deepEqual((await runtime.prepareRequest(selected, {})).options.headers, CUSTOM_HEADERS);
  assert.equal(await router.closeGate(ctx), true);
  assert.deepEqual(selected, original);
  assert.deepEqual(registry.find("relay", "custom-model"), original);
});

test("unresolved provider headers restore direct mode without exposing resolver details", async () => {
  for (const failure of [() => { throw new Error("private-fixture-auth"); }, () => ({ ok: false, error: "private-fixture-auth" })]) {
    const h = harness();
    await h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE);
    h.setAuthBehavior(failure);
    await h.router.apply(h.ctx.model, h.ctx);
    assert.equal(h.router.routing(), false);
    assert.deepEqual(h.ctx.model, MODELS[0]);
    assert.match(h.notices.at(-1), /provider request headers could not be resolved/);
    assert.doesNotMatch(h.notices.join("\n"), /private-fixture/);
  }
});

test("late auth resolution cannot reopen a closed gate or replace a newer model", async () => {
  for (const change of ["close", "switch"]) {
    const h = harness();
    let release;
    h.setAuthBehavior(() => new Promise(resolve => { release = resolve; }));
    const pending = h.router.openGate(GATEWAY, h.ctx, COMPAT, NATIVE);
    // openGate first awaits closeGate, then enters the public auth resolver.
    for (let turn = 0; turn < 10 && !release; turn++) await new Promise(resolve => setImmediate(resolve));
    assert.equal(typeof release, "function", "routing must resolve request headers");
    if (change === "close") await h.router.closeGate(h.ctx);
    else h.setActive({ ...MODELS[1] });
    release({ ok: true, apiKey: "fake-local-test-key" });
    await pending;
    assert.equal(h.router.routing(), false);
    assert.deepEqual(h.ctx.model, MODELS[change === "close" ? 0 : 1]);
    assert.equal(h.calls.length, 0);
  }
});

const ATTRIBUTION_CASES = [
  { provider: "openrouter", url: "https://openrouter.ai/api/v1", headers: { "HTTP-Referer": "https://pi.dev", "X-OpenRouter-Title": "pi", "X-OpenRouter-Categories": "cli-agent" } },
  { provider: "nvidia", url: "https://integrate.api.nvidia.com/v1", headers: { "X-BILLING-INVOKE-ORIGIN": "Pi" } },
  { provider: "cloudflare-workers-ai", url: "https://api.cloudflare.com/fixture/v1", headers: { "User-Agent": "pi-coding-agent" } },
  { provider: "cloudflare-ai-gateway", url: "https://gateway.ai.cloudflare.com/fixture/v1", headers: { "User-Agent": "pi-coding-agent" } },
  { provider: "opencode", url: "https://opencode.ai/zen/v1", headers: { "x-opencode-session": "fixture-session", "x-opencode-client": "pi" }, session: true },
  { provider: "opencode-go", url: "https://opencode.ai/go/v1", headers: { "x-opencode-session": "fixture-session", "x-opencode-client": "pi" }, session: true },
];

function attributionWireHeaders(model, enabled, resolved) {
  return { ...model.headers, ...mergeProviderAttributionHeaders(model, { getEnableInstallTelemetry: () => enabled }, "fixture-session", resolved) };
}

test("canonical Pi attribution stays identical with telemetry enabled or disabled", async () => {
  const saved = process.env.PI_TELEMETRY;
  try {
    for (const flag of ["1", "0"]) {
      process.env.PI_TELEMETRY = flag;
      for (const entry of ATTRIBUTION_CASES) {
        const original = { ...MODELS[0], provider: entry.provider, baseUrl: entry.url }, h = harness([original]);
        await h.router.openGate(GATEWAY, h.ctx, { [entry.provider]: entry.url });
        assert.equal(h.router.routing(), true, h.notices.join("\n"));
        assert.deepEqual(attributionWireHeaders(h.ctx.model, true), attributionWireHeaders(original, true));
        assert.equal(await h.router.closeGate(h.ctx), true);
        assert.deepEqual(h.ctx.model, original);
      }
    }
  } finally { if (saved === undefined) delete process.env.PI_TELEMETRY; else process.env.PI_TELEMETRY = saved; }
});

test("URL-only aliases stay direct when original Pi attribution cannot be recovered", async () => {
  const saved = process.env.PI_TELEMETRY;
  try {
    for (const flag of ["1", "0", undefined]) {
      if (flag === undefined) delete process.env.PI_TELEMETRY; else process.env.PI_TELEMETRY = flag;
      for (const entry of ATTRIBUTION_CASES) {
        const original = { ...MODELS[0], baseUrl: entry.url }, h = harness([original]);
        await h.router.openGate(GATEWAY, h.ctx, { relay: entry.url });
        if (flag === "0" && !entry.session) {
          assert.equal(h.router.routing(), true, h.notices.join("\n"));
          assert.deepEqual(attributionWireHeaders(h.ctx.model, true), attributionWireHeaders(original, true));
        } else {
          assert.equal(h.router.routing(), false);
          assert.deepEqual(h.ctx.model, original);
          assert.match(h.notices.at(-1), /cannot preserve URL-derived headers.*provider alias.*no compression/);
          // Actual host helper demonstrates the missing branch when enabled.
          assert.notDeepEqual(attributionWireHeaders({ ...original, baseUrl: ROUTE }, true), attributionWireHeaders(original, true));
          assert.doesNotMatch(h.notices.join("\n"), /fixture-session|https:\/\/pi\.dev/);
        }
      }
    }
  } finally { if (saved === undefined) delete process.env.PI_TELEMETRY; else process.env.PI_TELEMETRY = saved; }
});

test("explicit final or identical model headers make alias attribution independent of telemetry", async () => {
  const saved = process.env.PI_TELEMETRY;
  try {
    for (const flag of ["1", "0", undefined]) {
      if (flag === undefined) delete process.env.PI_TELEMETRY; else process.env.PI_TELEMETRY = flag;
      for (const entry of ATTRIBUTION_CASES) for (const source of ["model", "resolved"]) {
        const headers = source === "model" ? entry.headers : Object.fromEntries(Object.keys(entry.headers).map(name => [name, "private-fixture-override"]));
        const original = { ...MODELS[0], baseUrl: entry.url, ...(source === "model" ? { headers } : {}) }, h = harness([original]);
        const resolved = source === "resolved" ? headers : undefined;
        h.setAuthBehavior(() => ({ ok: true, apiKey: "fake-local-test-key", headers: resolved }));
        await h.router.openGate(GATEWAY, h.ctx, { relay: entry.url }, {}, { relay: Object.keys(headers) });
        assert.equal(h.router.routing(), true, h.notices.join("\n"));
        for (const enabled of [true, false]) assert.deepEqual(attributionWireHeaders(h.ctx.model, enabled, resolved), attributionWireHeaders(original, enabled, resolved));
        assert.equal(await h.router.closeGate(h.ctx), true);
        assert.deepEqual(h.ctx.model, original);
        assert.doesNotMatch(h.notices.join("\n"), /private-fixture-override/);
      }
    }
  } finally { if (saved === undefined) delete process.env.PI_TELEMETRY; else process.env.PI_TELEMETRY = saved; }
});

test("a model header overwritten by Pi's attribution does not falsely certify an alias", async () => {
  const saved = process.env.PI_TELEMETRY;
  try {
    process.env.PI_TELEMETRY = "1";
    const original = { ...MODELS[0], baseUrl: "https://integrate.api.nvidia.com/v1", headers: { "X-BILLING-INVOKE-ORIGIN": "private-fixture-override" } };
    const h = harness([original]);
    await h.router.openGate(GATEWAY, h.ctx, { relay: original.baseUrl }, {}, { relay: ["x-billing-invoke-origin"] });
    assert.equal(h.router.routing(), false);
    assert.match(h.notices.at(-1), /cannot preserve URL-derived headers X-BILLING-INVOKE-ORIGIN/);
    assert.notDeepEqual(attributionWireHeaders({ ...original, baseUrl: ROUTE }, true), attributionWireHeaders(original, true));
    assert.doesNotMatch(h.notices.join("\n"), /private-fixture-override/);
  } finally { if (saved === undefined) delete process.env.PI_TELEMETRY; else process.env.PI_TELEMETRY = saved; }
});

test("real Pi preserves custom models, auth, and stream handlers through route and close", async () => {
  // In-memory credentials/models only. Catalog refresh cannot use the network;
  // prepareRequest constructs a request without dispatching provider inference.
  const credentials = {
    read: async () => undefined, list: async () => [],
    modify: async () => assert.fail("unexpected credential write"),
    delete: async () => assert.fail("unexpected credential delete"),
  };
  const runtime = await ModelRuntime.create({ credentials, modelsPath: null, allowModelNetwork: false });
  const registry = new ModelRegistry(runtime);
  const streamSimple = () => { throw new Error("provider inference forbidden in this test"); };
  runtime.registerProvider("relay", {
    api: "openai-completions", baseUrl: "http://127.0.0.1:4000/v1", apiKey: "fake-local-test-key", streamSimple,
    models: [{ id: "custom-model", name: "Custom model", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 32768, maxTokens: 1024 }],
  });
  const original = registry.find("relay", "custom-model");
  const registration = registry.getRegisteredProviderConfig("relay");
  let selected = original;
  const ctx = { get model() { return selected; }, modelRegistry: registry };
  const pi = {
    registerProvider: (name, config) => registry.registerProvider(name, config),
    unregisterProvider: (name) => registry.unregisterProvider(name),
    async setModel(model) { selected = model; return true; },
  };
  const notices = [];
  const router = new ProviderRouter(pi, (message) => notices.push(message));
  await router.openGate(GATEWAY, ctx, COMPAT, NATIVE);
  assert.equal(router.routing(), true, notices.join("\n"));
  assert.equal(selected.baseUrl, ROUTE);
  // The pinned Pi ModelRuntime.stream request-preparation seam verifies that
  // the copied model URL survives real auth resolution without sending data.
  const prepared = await runtime.prepareRequest(selected, {});
  assert.equal(prepared.model.baseUrl, ROUTE);
  assert.equal(prepared.options.apiKey, "fake-local-test-key");
  assert.equal(registry.getRegisteredProviderConfig("relay"), registration);
  assert.equal(registry.getRegisteredProviderConfig("relay").streamSimple, streamSimple);
  assert.equal(await router.closeGate(ctx), true);
  assert.deepEqual(selected, original);
  assert.deepEqual(registry.find("relay", "custom-model"), original);
  assert.equal(registry.getRegisteredProviderConfig("relay"), registration);
  assert.equal((await registry.getApiKeyAndHeaders(original)).apiKey, "fake-local-test-key");
});
