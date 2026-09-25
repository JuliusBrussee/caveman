import { inspectFrameworkCompatibility } from '../dist/compatibility.js';

const entries = {
  'ai-sdk': ['ai', '@ai-sdk/provider'], openai: ['openai'], anthropic: ['@anthropic-ai/sdk'],
  google: ['@google/genai'], langchain: ['langchain', '@langchain/core/messages', '@langchain/langgraph'],
  strands: ['@strands-agents/sdk'], mastra: ['@mastra/core/agent'], mcp: ['@modelcontextprotocol/sdk/client/index.js'],
};

/** Only absence of the selected optional peer can skip. A transitive import,
 * broken export, or our own missing module must fail, including outside CI. */
export function requirePeers(t, adapter) {
  for (const entry of entries[adapter]) {
    try { import.meta.resolve(entry); }
    catch (error) {
      if (error?.code !== 'ERR_MODULE_NOT_FOUND' || process.env.CAVEMAN_REQUIRE_FRAMEWORKS === '1') throw error;
      t.skip(`Optional peer not installed: ${entry}`);
      return false;
    }
  }
  if (process.env.CAVEMAN_REQUIRE_FRAMEWORKS === '1') {
    const result = inspectFrameworkCompatibility(adapter);
    if (!result.compatible) throw new Error(`Required framework unsupported: ${JSON.stringify(result)}`);
  }
  return true;
}
