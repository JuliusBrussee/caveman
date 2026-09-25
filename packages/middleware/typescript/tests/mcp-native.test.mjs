import assert from 'node:assert/strict';
import test from 'node:test';
import { requirePeers } from './peers.mjs';
import { runtimeFixture, original, shortened, handle, scope } from './runtime-fixture.mjs';

test('native MCP client stays in charge of transport while host projects and recovers copied text', async t => {
  if (!requirePeers(t, 'mcp')) return;
  const { Client } = await import('@modelcontextprotocol/sdk/client/index.js');
  const { Server } = await import('@modelcontextprotocol/sdk/server/index.js');
  const { InMemoryTransport } = await import('@modelcontextprotocol/sdk/inMemory.js');
  const { CallToolRequestSchema } = await import('@modelcontextprotocol/sdk/types.js');
  const { CavemanMCPHost, bindMCPTool } = await import('../dist/mcp.js');
  const f = runtimeFixture(); t.after(() => f.runtime.close());
  const client = new Client({ name: 'native-test-client', version: '1' });
  const server = new Server({ name: 'native-test-server', version: '1' }, { capabilities: { tools: {} } });
  server.setRequestHandler(CallToolRequestSchema, async request => {
    assert.equal(request.params.name, 'read');
    return { content: [{ type: 'text', text: original }] };
  });
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  await Promise.all([client.connect(clientTransport), server.connect(serverTransport)]);
  t.after(async () => { await client.close(); await server.close(); });
  const tool = { name: 'read', description: 'Read log', inputSchema: { type: 'object', properties: {} } };
  const native = bindMCPTool(client, tool);
  const host = new CavemanMCPHost({ runtime: f.runtime, scope, serverId: 'native-test-server', protocolVersion: '2025-11-25' });
  const tools = host.register([native]);
  const result = await native.execute({}), before = structuredClone(result);
  const view = await host.projectResult(result, { tool, callId: 'read-1', contextManifest: [], registeredTools: tools });
  assert.equal(view.content[0].text, shortened);
  assert.deepEqual(result, before);
  const recovered = await tools.find(binding => binding.tool.name === 'caveman_retrieve').execute({ handle });
  assert.equal(JSON.parse(recovered.content[0].text).text, original);
  assert.ok(f.reports.some(report => report.status === 'applied'));
});
