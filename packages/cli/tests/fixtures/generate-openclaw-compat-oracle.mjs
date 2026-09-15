// Manual refresh only: node tests/fixtures/generate-openclaw-compat-oracle.mjs
// Fetches immutable public source; ordinary tests use the checked-in JSON offline.
import { createHash } from 'node:crypto';
import { writeFileSync } from 'node:fs';
import ts from 'typescript';

const commit = '0965053fe6b9341776df147a6934b7485c60b5ca';
const paths = ['packages/ai/src/providers/anthropic.ts', 'packages/ai/src/transports/openai-responses-payload-policy.ts'];
const sources = await Promise.all(paths.map(async path => {
  const response = await fetch(`https://raw.githubusercontent.com/openclaw/openclaw/${commit}/${path}`);
  if (!response.ok) throw new Error(`Source download failed: ${response.status}`);
  return response.text();
}));

function declarations(source, functions, constants = []) {
  const ast = ts.createSourceFile('oracle.ts', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const selected = ast.statements.filter(node =>
    (ts.isFunctionDeclaration(node) && functions.includes(node.name?.text)) ||
    (ts.isVariableStatement(node) && node.declarationList.declarations.some(declaration => constants.includes(declaration.name.getText(ast)))));
  if (selected.length !== functions.length + constants.length) throw new Error('Pinned source declarations changed');
  return selected.map(node => node.getText(ast)).join('\n\n');
}

const anthropicFunctions = ['getAnthropicCompat'];
const responsesFunctions = ['normalizeComparableBaseUrl', 'resolveUrlHostname', 'hostMatchesSuffix', 'isLocalEndpointHost', 'resolveBundledOpenAIResponsesEndpointClass', 'isOpenAIResponsesApi', 'readCompatPayloadBoolean', 'resolveOpenAIResponsesPayloadCapabilities', 'resolveOpenAIResponsesCompactEndpointPlan'];
const constants = ['OPENAI_RESPONSES_PROVIDERS', 'LOCAL_ENDPOINT_HOSTS', 'MODELSTUDIO_NATIVE_BASE_URLS', 'MOONSHOT_NATIVE_BASE_URLS'];
// Imported string helpers have no provider behavior; fixture inputs are plain strings.
const prelude = `
const readStringValue = value => typeof value === 'string' ? value : undefined;
const normalizeOptionalLowercaseString = value => typeof value === 'string' && value.trim() ? value.trim().toLowerCase() : undefined;
// Pinned packages/model-catalog-core/src/provider-id.ts delegates only to the
// lowercase/trim string normalizer; it performs no provider alias mapping.
const normalizeProviderId = value => value.trim().toLowerCase();
const OPENAI_RESPONSES_APIS = new Set(['openai-responses', 'azure-openai-responses', 'openai-chatgpt-responses']);
`;
const code = prelude + declarations(sources[0], anthropicFunctions) + '\n' + declarations(sources[1], responsesFunctions, constants) + '\nexport { getAnthropicCompat, resolveOpenAIResponsesPayloadCapabilities, resolveOpenAIResponsesCompactEndpointPlan };';
const js = ts.transpileModule(code, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } }).outputText;
const oracle = await import(`data:text/javascript;base64,${Buffer.from(js).toString('base64')}`);
const models = [
  { provider: 'relay', id: 'model', api: 'anthropic-messages', baseUrl: 'https://relay.example/anthropic' },
  { provider: 'fireworks', id: 'model', api: 'anthropic-messages', baseUrl: 'https://api.fireworks.ai/inference' },
  { provider: 'cloudflare-ai-gateway', id: 'model', api: 'anthropic-messages', baseUrl: 'https://gateway.ai.cloudflare.com/v1/account/gateway/anthropic' },
  { provider: 'cloudflare-ai-gateway', id: 'model', api: 'anthropic-messages', baseUrl: 'https://gateway.ai.cloudflare.com/v1/account/gateway/custom' },
  { provider: 'cloudflare-ai-gateway', id: 'model', api: 'anthropic-messages', baseUrl: 'https://gateway.ai.cloudflare.com/v1/account/gateway/anthropic', compat: { supportsEagerToolInputStreaming: false, supportsLongCacheRetention: false, sendSessionAffinityHeaders: false } },
  { provider: 'relay', id: 'model', api: 'openai-responses', baseUrl: 'https://relay.example/v1' },
  { provider: 'relay', id: 'model', api: 'openai-responses', baseUrl: 'https://relay.example/v1', compat: { supportsStore: false, supportsPromptCacheKey: true, supportsInstructions: true } },
  { provider: 'relay', id: 'model', api: 'openai-responses', baseUrl: 'https://api.x.ai/v1' },
  { provider: 'relay', id: 'model', api: 'openai-responses', baseUrl: 'https://api.x.ai/v1', compat: { supportsStore: false, supportsPromptCacheKey: false, supportsInstructions: false } },
  { provider: 'x-ai', id: 'model', api: 'openai-responses', baseUrl: 'https://api.x.ai/v1' },
  { provider: 'XAI', id: 'model', api: 'openai-responses', baseUrl: 'https://api.x.ai/v1' },
  { provider: 'openai', id: 'gpt-5.5', api: 'openai-responses', baseUrl: 'https://api.openai.com/v1' },
  { provider: 'relay', id: 'gpt-5.5', api: 'openai-responses', baseUrl: 'https://example.openai.azure.com/openai/v1' },
];
const cases = models.map(model => {
  const resolve = model.api === 'anthropic-messages' ? oracle.getAnthropicCompat : oracle.resolveOpenAIResponsesPayloadCapabilities;
  const baseUrl = `http://127.0.0.1:8787/compat/relay${model.api === 'openai-responses' ? '/v1' : ''}`;
  return { model, resolved: resolve(model), routedWithoutCompat: resolve({ ...model, baseUrl }),
    ...(model.api === 'openai-responses' ? {
      compactEndpoint: oracle.resolveOpenAIResponsesCompactEndpointPlan(model),
      routedCompactEndpoint: oracle.resolveOpenAIResponsesCompactEndpointPlan({ ...model, baseUrl }),
    } : {}),
  };
});
const fixture = {
  schema: 1,
  upstream: {
    version: '2026.8.2', commit,
    sources: paths.map((path, index) => ({
      url: `https://github.com/openclaw/openclaw/blob/${commit}/${path}`,
      sha256: createHash('sha256').update(sources[index]).digest('hex'),
      functions: index === 0 ? anthropicFunctions : ['resolveOpenAIResponsesPayloadCapabilities', 'resolveOpenAIResponsesCompactEndpointPlan'],
    })),
    method: 'Actual pinned TypeScript function declarations extracted by TypeScript AST and transpiled; imports replaced only with string normalizers and literal constant sets from source. No network requests or provider SDK calls.',
  },
  cases,
};
writeFileSync(new URL('./openclaw-2026.8.2-compat.json', import.meta.url), JSON.stringify(fixture, null, 2) + '\n');
console.log(`Generated ${cases.length} pinned-source compatibility cases.`);
