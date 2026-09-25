import assert from 'node:assert/strict';
import test from 'node:test';
import { requirePeers } from './peers.mjs';

// Every subpath in the package's `exports` map must import and expose the names
// the README tells people to call. Framework peers are optional, so a missing
// one skips rather than fails.
const ADAPTERS = {
  'ai-sdk': ['withCaveman', 'createCavemanMiddleware'],
  openai: ['withCavemanOpenAI', 'withCavemanOpenAITools', 'createCavemanFetch'],
  anthropic: ['withCavemanAnthropic'],
  google: ['CavemanGoogleGenAI'],
  langchain: ['withCavemanAgent', 'withCavemanModel', 'CavemanDocumentCompressor', 'createCavemanLangChain'],
  strands: ['withCavemanStrands', 'withCavemanStrandsModel', 'CavemanStrandsModel'],
  mastra: ['withCavemanMastra', 'createCavemanMastraProcessor'],
  mcp: ['CavemanMCPHost', 'bindMCPTool'],
};

for (const [family, names] of Object.entries(ADAPTERS)) {
  test(`${family} exposes its documented entry points`, async t => {
    if (!requirePeers(t, family)) return;
    const module = await import(`../dist/${family}.js`);
    const missing = names.filter(name => typeof module[name] === 'undefined');
    assert.deepEqual(missing, [], `@caveman-ai/middleware/${family} lost ${missing}`);
  });
}
