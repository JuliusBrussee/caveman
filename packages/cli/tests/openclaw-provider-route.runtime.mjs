import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { overlayBuilders } from "../dist/index.js";

test("OpenClaw cannot send a custom provider key to the default OpenAI destination", (t) => {
  const home = mkdtempSync(join(tmpdir(), "cave-openclaw-route-proof-"));
  const previous = process.env.CAVEMAN_HOME;
  process.env.CAVEMAN_HOME = home;
  t.after(() => {
    if (previous === undefined) delete process.env.CAVEMAN_HOME;
    else process.env.CAVEMAN_HOME = previous;
    rmSync(home, { recursive: true, force: true });
  });
  const config = {
    agents: { defaults: { model: { primary: "my-provider/model" } } },
    models: { providers: { "my-provider": {
      baseUrl: "https://provider.example/tenant-a/v1", apiKey: "fake-provider-specific-key",
      api: "openai-completions", models: [{ id: "model", name: "Model" }],
    } } },
  };
  const result = overlayBuilders.openclaw({}, config, {
    mode: "local", gatewayUrl: "http://127.0.0.1:8787/w/openclaw", env: {},
    upstreams: { provider_upstreams: { openai: "https://api.openai.com" }, compat_upstreams: {} },
  });
  assert.equal(result.models?.providers?.caveman, undefined, "unmatched endpoint must keep native provider routing");
});

test("OpenClaw verified compat routing retains provider identity, credentials, model catalog and defaults", (t) => {
  const home = mkdtempSync(join(tmpdir(), "cave-openclaw-identity-"));
  const previous = process.env.CAVEMAN_HOME;
  process.env.CAVEMAN_HOME = home;
  t.after(() => { if (previous === undefined) delete process.env.CAVEMAN_HOME; else process.env.CAVEMAN_HOME = previous; rmSync(home, { recursive: true, force: true }); });
  const compat = { supportsStore: false, supportsPromptCacheKey: true, supportsInstructions: true };
  const selected = { id: 'model', name: 'Selected', reasoning: true, contextWindow: 123456, maxTokens: 4096, compat };
  const sibling = { id: 'other', name: 'Other', api: 'openai-responses', compat };
  const sourceProvider = {
    baseUrl: 'https://relay.example/tenant/v1', apiKey: { source: 'env', provider: 'default', id: 'RELAY_KEY' },
    authHeader: true, api: 'openai-responses', headers: { 'openai-organization': 'org-test' }, models: [selected, sibling],
  };
  const defaults = { model: { primary: 'relay/model', fallbacks: ['relay/other'] }, models: { 'relay/model': { params: { temperature: 0.1 } } } };
  const config = { agents: { defaults }, models: { providers: { relay: sourceProvider } } };
  const before = JSON.stringify(config);
  const result = overlayBuilders.openclaw({}, config, {
    mode: 'local', gatewayUrl: 'http://127.0.0.1:8787/w/openclaw', env: {},
    upstreams: { compat_upstreams: { relay: 'https://relay.example/tenant' } },
  });
  const routed = result.models.providers.relay;
  assert.equal(routed.baseUrl, 'http://127.0.0.1:8787/w/openclaw/compat/relay/v1');
  assert.deepEqual(routed.apiKey, sourceProvider.apiKey);
  assert.deepEqual(routed.models, [selected, sibling]);
  assert.equal(routed.headers['openai-organization'], 'org-test');
  assert.equal(routed.headers['x-cave-agent'], 'openclaw');
  assert.equal(routed.authHeader, true);
  assert.equal(result.models.providers.caveman, undefined);
  assert.equal(result.agents, undefined, 'existing primary/fallback/allowlist settings remain native');
  assert.equal(JSON.stringify(config), before);
});

test("OpenClaw refuses an unverified managed destination for an existing custom provider", (t) => {
  const home = mkdtempSync(join(tmpdir(), "cave-openclaw-managed-proof-"));
  const previous = process.env.CAVEMAN_HOME;
  process.env.CAVEMAN_HOME = home;
  t.after(() => { if (previous === undefined) delete process.env.CAVEMAN_HOME; else process.env.CAVEMAN_HOME = previous; rmSync(home, { recursive: true, force: true }); });
  const result = overlayBuilders.openclaw({}, {
    agents: { defaults: { model: { primary: 'relay/model' } } },
    models: { providers: { relay: { baseUrl: 'https://relay.example/tenant/v1', api: 'openai-completions', apiKey: 'fake-relay-key', models: [{ id: 'model' }] } } },
  }, { mode: 'managed', gatewayUrl: 'https://gateway.example/w/openclaw', env: { CAVE_API_KEY: 'fake-gateway-key' } });
  assert.equal(result.models, undefined);
  assert.equal(result.agents, undefined);
});

test("OpenClaw selected model API cannot change a sibling model's inherited API", (t) => {
  const home = mkdtempSync(join(tmpdir(), "cave-openclaw-api-switch-"));
  const previous = process.env.CAVEMAN_HOME;
  process.env.CAVEMAN_HOME = home;
  t.after(() => { if (previous === undefined) delete process.env.CAVEMAN_HOME; else process.env.CAVEMAN_HOME = previous; rmSync(home, { recursive: true, force: true }); });
  const config = {
    agents: { defaults: { model: { primary: 'relay/chat' } } },
    models: { providers: { relay: { baseUrl: 'https://relay.example/v1', api: 'openai-responses', apiKey: 'fake-relay-key',
      models: [{ id: 'chat', api: 'openai-completions' }, { id: 'response' }] } } },
  };
  const context = { mode: 'local', gatewayUrl: 'http://127.0.0.1:8787/w/openclaw', env: {}, upstreams: { compat_upstreams: { relay: 'https://relay.example' } } };
  const result = overlayBuilders.openclaw({}, config, context);
  assert.equal(result.models, undefined, 'unresolved Chat policy prevents a provider-wide override that would affect its sibling');
  assert.equal(config.models.providers.relay.api, 'openai-responses');
  assert.equal(config.models.providers.relay.models[0].api, 'openai-completions');
  assert.equal(config.models.providers.relay.models[1].api, undefined);
  config.models.providers.relay.models.push({ id: 'anthropic', api: 'anthropic-messages' });
  const mixed = overlayBuilders.openclaw({}, config, context);
  assert.equal(mixed.models, undefined, 'incompatible fallback model cannot inherit a different base URL');
});

test("OpenClaw Anthropic base URL normalization is included in endpoint proof", (t) => {
  const home = mkdtempSync(join(tmpdir(), "cave-openclaw-anthropic-base-"));
  const previous = process.env.CAVEMAN_HOME;
  process.env.CAVEMAN_HOME = home;
  t.after(() => { if (previous === undefined) delete process.env.CAVEMAN_HOME; else process.env.CAVEMAN_HOME = previous; rmSync(home, { recursive: true, force: true }); });
  const config = { agents: { defaults: { model: { primary: 'relay/model' } } }, models: { providers: { relay: {
    baseUrl: 'https://api.anthropic.com/v1', api: 'anthropic-messages', apiKey: 'fake-anthropic-key', models: [{ id: 'model' }],
  } } } };
  const context = { mode: 'local', gatewayUrl: 'http://127.0.0.1:8787/w/openclaw', env: {} };
  const wrong = overlayBuilders.openclaw({}, config, { ...context, upstreams: { provider_upstreams: { anthropic: 'https://api.anthropic.com/v1' } } });
  assert.equal(wrong.models, undefined, 'native adapter would add a second /v1');
  const right = overlayBuilders.openclaw({}, config, { ...context, upstreams: { provider_upstreams: { anthropic: 'https://api.anthropic.com' } } });
  assert.equal(right.models.providers.relay.baseUrl, 'http://127.0.0.1:8787/w/openclaw/anthropic');
});
