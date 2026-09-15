import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { overlayBuilders } from '../dist/index.js';
import { preserveOpenClawProviderCompat } from '../dist/openclaw-provider-compat.js';

const oracle = JSON.parse(readFileSync(new URL('./fixtures/openclaw-2026.8.2-compat.json', import.meta.url), 'utf8'));
const nativeOpenAIHosts = new Set(['api.openai.com', 'example.openai.azure.com']);

for (const fixture of oracle.cases) {
  const { model, resolved, routedWithoutCompat } = fixture;
  const label = `${model.api} ${model.provider} ${new URL(model.baseUrl).pathname} ${JSON.stringify(model.compat ?? {})}`;
  test(`OpenClaw public defaults match the actual ${oracle.upstream.version} source: ${label}`, () => {
    const before = JSON.stringify(model);
    const result = preserveOpenClawProviderCompat(model);
    assert.equal(JSON.stringify(model), before, 'compatibility proof must not mutate user config');
    if (fixture.compactEndpoint?.enabled && !fixture.routedCompactEndpoint?.enabled) {
      assert.equal(result.ok, false, 'native xAI compact endpoint must not silently disappear after URL rewriting');
      assert.match(result.reason, /native xAI compact-endpoint policy/);
      return;
    }
    if (nativeOpenAIHosts.has(new URL(model.baseUrl).hostname)) {
      assert.equal(resolved.usesKnownNativeOpenAIRoute, true);
      assert.equal(routedWithoutCompat.usesKnownNativeOpenAIRoute, false, 'oracle proves a private replay/status policy change');
      assert.equal(result.ok, false);
      return;
    }
    assert.equal(result.ok, true);
    if (model.api === 'anthropic-messages') {
      for (const key of ['supportsEagerToolInputStreaming', 'supportsLongCacheRetention', 'sendSessionAffinityHeaders']) {
        assert.equal(result.compat[key], resolved[key], `${key} must match the actual upstream resolver`);
      }
      if (model.provider === 'cloudflare-ai-gateway' && model.baseUrl.endsWith('/anthropic') && !model.compat) {
        assert.equal(routedWithoutCompat.sendSessionAffinityHeaders, false);
        assert.equal(result.compat.sendSessionAffinityHeaders, true, 'routing preserves the original session affinity default');
      }
    } else {
      assert.equal(result.compat.supportsStore, model.compat?.supportsStore ?? resolved.supportsResponsesStoreField);
      assert.equal(result.compat.supportsPromptCacheKey, model.compat?.supportsPromptCacheKey ?? !resolved.shouldStripResponsesPromptCache);
      assert.equal(result.compat.supportsInstructions, model.compat?.supportsInstructions ?? resolved.usesVerifiedInstructionsEndpoint);
    }
    for (const [key, value] of Object.entries(model.compat ?? {})) assert.deepEqual(result.compat[key], value);
  });
}

// OpenClaw's pinned configured-primary resolver and model.configured-overrides
// normalize provider IDs before these actual source-derived defaults run.
// Config keys retain their original spelling in Caveman's overlay input.
for (const { model, resolved } of oracle.cases.filter(({ model }) =>
  model.api === 'anthropic-messages' && ['fireworks', 'cloudflare-ai-gateway'].includes(model.provider))) {
  for (const provider of [model.provider.toUpperCase(), ` ${model.provider} `]) {
    test(`OpenClaw normalized ${provider} defaults match pinned source: ${model.baseUrl} ${JSON.stringify(model.compat ?? {})}`, () => {
      const input = { ...model, provider };
      const before = JSON.stringify(input);
      const result = preserveOpenClawProviderCompat(input);
      assert.equal(result.ok, true);
      assert.equal(JSON.stringify(input), before, 'normalization must not rewrite configured identity');
      for (const key of ['supportsEagerToolInputStreaming', 'supportsLongCacheRetention', 'sendSessionAffinityHeaders']) {
        assert.equal(result.compat[key], resolved[key], `${key} must match the runtime's normalized provider`);
      }
    });
  }
}

test('OpenClaw does not guess the private Chat compatibility registry, even with explicit public flags', () => {
  const explicit = {
    supportsStore: false, supportsDeveloperRole: false, supportsReasoningEffort: false,
    supportsUsageInStreaming: false, maxTokensField: 'max_tokens', thinkingFormat: 'together',
    supportsStrictMode: false, supportsJsonSchemaResponseFormat: false,
    requiresReasoningContentOnAssistantMessages: false, supportsPromptCacheKey: true,
    supportsLongCacheRetention: false, sendSessionAffinityHeaders: true, visibleReasoningDetailTypes: [],
  };
  for (const compat of [undefined, explicit]) {
    const result = preserveOpenClawProviderCompat({ provider: 'relay', id: 'model', api: 'openai-completions', baseUrl: 'https://api.together.ai/v1', compat });
    assert.equal(result.ok, false);
    assert.match(result.reason, /resolved OpenAI Chat provider policy/);
  }
});

test('OpenClaw retains native Anthropic and generated attribution routes directly', () => {
  for (const [provider, api, baseUrl, reason] of [
    ['anthropic', 'anthropic-messages', 'https://api.anthropic.com/v1', /fallback beta, service-tier and stream validation/],
    ['google', 'google-generative-ai', 'https://generativelanguage.googleapis.com/v1beta', /endpoint-specific request headers/],
    ['openrouter', 'openai-responses', 'https://openrouter.ai/api/v1', /endpoint-specific request headers/],
    ['relay', 'openai-responses', 'https://integrate.api.nvidia.com/v1', /endpoint-specific request headers/],
    ['xai', 'openai-responses', 'https://api.x.ai/v1', /native xAI compact-endpoint policy/],
  ]) {
    for (const spelling of [provider, provider.toUpperCase(), ` ${provider} `]) {
      const result = preserveOpenClawProviderCompat({ provider: spelling, id: 'model', api, baseUrl });
      assert.equal(result.ok, false, `${spelling} must preserve the normalized native policy`);
      assert.match(result.reason, reason);
    }
  }
});

test('OpenClaw preserves custom Google compatibility without inventing OpenAI fields', () => {
  const compat = { supportsTools: false, unsupportedToolSchemaKeywords: ['examples'] };
  assert.deepEqual(preserveOpenClawProviderCompat({ provider: 'relay', id: 'gemini-3.1', api: 'google-generative-ai', baseUrl: 'https://relay.example/v1beta', compat }), { ok: true, compat });
});

function overlayFixture(t, mutate = () => {}) {
  const home = mkdtempSync(join(tmpdir(), 'cave-openclaw-compat-'));
  const previous = process.env.CAVEMAN_HOME;
  process.env.CAVEMAN_HOME = home;
  t.after(() => {
    if (previous === undefined) delete process.env.CAVEMAN_HOME; else process.env.CAVEMAN_HOME = previous;
    rmSync(home, { recursive: true, force: true });
  });
  const config = {
    agents: { defaults: { model: { primary: 'relay/model', fallbacks: ['relay/sibling'] } } },
    models: { providers: { relay: {
      baseUrl: 'https://relay.example/v1', api: 'openai-responses', apiKey: '${RELAY_KEY}',
      models: [
        { id: 'model', name: 'Model', reasoning: true, contextWindow: 123456, compat: { supportsStore: false, supportsPromptCacheKey: true, supportsInstructions: true, supportsTemperature: false } },
        { id: 'sibling', compat: { supportsStore: true, supportsPromptCacheKey: false, supportsInstructions: false } },
      ],
    } } },
  };
  const context = { mode: 'local', gatewayUrl: 'http://127.0.0.1:8787/w/openclaw', env: {}, upstreams: { compat_upstreams: { relay: 'https://relay.example' } } };
  mutate(config.models.providers.relay, context, config);
  const before = JSON.stringify(config);
  let stderr = '';
  const previousWrite = process.stderr.write;
  let result;
  try {
    process.stderr.write = chunk => { stderr += String(chunk); return true; };
    result = overlayBuilders.openclaw({}, config, context);
  } finally { process.stderr.write = previousWrite; }
  assert.equal(JSON.stringify(config), before);
  return { result, stderr, provider: config.models.providers.relay };
}

for (const spelling of ['ANTHROPIC', 'anthropic ']) {
  test(`OpenClaw configured ${JSON.stringify(spelling)} cannot bypass native Anthropic policy`, t => {
    const { result, stderr } = overlayFixture(t, (provider, context, config) => {
      provider.baseUrl = 'https://api.anthropic.com/v1';
      provider.api = 'anthropic-messages';
      config.models.providers = { [spelling]: provider };
      config.agents.defaults.model = { primary: `${spelling}/model`, fallbacks: [`${spelling}/sibling`] };
      context.upstreams = { provider_upstreams: { anthropic: 'https://api.anthropic.com' } };
    });
    assert.equal(result.models, undefined, 'native runtime normalizes the ID before enforcing Anthropic policy');
    assert.match(stderr, /native Anthropic fallback beta, service-tier and stream validation policy/);
    assert.ok(result.mcp.servers.caveman);
  });
}

test('OpenClaw verified Responses overlay preserves explicit compat, model metadata and final request headers', t => {
  const { result, provider, stderr } = overlayFixture(t, (provider, context) => {
    provider.headers = { 'x-tenant': 'provider-value' };
    provider.models[0].headers = { 'x-tenant': 'model-value' };
    provider.request = { auth: { mode: 'provider-default' }, headers: { 'x-tenant': { source: 'env', provider: 'default', id: 'TENANT_HEADER' } } };
    context.upstreams.compat_forward_headers = { relay: ['x-tenant'] };
  });
  assert.equal(stderr, '');
  assert.equal(result.models.providers.relay.baseUrl, 'http://127.0.0.1:8787/w/openclaw/compat/relay/v1');
  assert.deepEqual(result.models.providers.relay.models, provider.models);
  assert.deepEqual(result.models.providers.relay.request, provider.request);
  assert.equal(result.models.providers.relay.apiKey, '${RELAY_KEY}');
  assert.equal(result.agents, undefined, 'primary and fallback selection remain native');
});

test('OpenClaw final request.headers must satisfy the running proxy header contract', t => {
  const { result, stderr } = overlayFixture(t, provider => {
    provider.request = { headers: { 'x-request-only': 'secret-fixture-not-for-output' } };
  });
  assert.equal(result.models, undefined);
  assert.match(stderr, /x-request-only/);
  assert.doesNotMatch(stderr, /secret-fixture/);
});

for (const [name, request, reason] of [
  ['Bearer override', { auth: { mode: 'authorization-bearer', token: 'secret-fixture' } }, /custom request authentication/],
  ['custom credential header', { auth: { mode: 'header', headerName: 'x-key', value: 'secret-fixture' } }, /custom request authentication/],
  ['explicit proxy', { proxy: { mode: 'explicit-proxy', url: 'https://secret-fixture@example.test' } }, /request proxy or TLS/],
  ['environment proxy', { proxy: { mode: 'env-proxy' } }, /request proxy or TLS/],
  ['TLS override', { tls: { serverName: 'secret-fixture' } }, /request proxy or TLS/],
  ['private-network denial', { allowPrivateNetwork: false }, /explicitly disallows the local proxy/],
]) {
  test(`OpenClaw keeps ${name} directly on its original transport`, t => {
    const { result, stderr } = overlayFixture(t, provider => { provider.request = request; });
    assert.equal(result.models, undefined);
    assert.match(stderr, reason);
    assert.match(stderr, /proxy compression is off/);
    assert.doesNotMatch(stderr, /secret-fixture/);
  });
}

test('OpenClaw an unsupported fallback keeps the entire provider direct with MCP/plugin available', t => {
  const { result, stderr } = overlayFixture(t, provider => { provider.models[1].api = 'openai-completions'; });
  assert.equal(result.models, undefined);
  assert.match(stderr, /resolved OpenAI Chat provider policy/);
  assert.match(stderr, /proxy compression is off/);
  assert.ok(result.mcp.servers.caveman);
  assert.equal(result.plugins.entries['caveman-shrink'].enabled, true);
});
