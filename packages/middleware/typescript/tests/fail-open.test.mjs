import assert from 'node:assert/strict';
import test from 'node:test';
import { createServer } from 'node:http';
import { createMiddlewareRuntime } from '@caveman-ai/sdk/middleware';

/** A port nothing is listening on: the proxy-is-not-running failure. */
async function closedPort() {
  const server = createServer();
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const { port } = server.address();
  await new Promise(resolve => server.close(resolve));
  return port;
}

async function unreachableRuntime() {
  const reports = [];
  const runtime = createMiddlewareRuntime({
    endpoint: `http://127.0.0.1:${await closedPort()}`,
    deadlineMs: 200,
    onReport: report => reports.push(report),
  });
  return { runtime, reports };
}

test('the runtime bypasses instead of throwing when the proxy is down', async () => {
  const { runtime } = await unreachableRuntime();
  const result = await runtime.optimize({
    scope: { namespace: 'tests', session_id: 's1', branch_id: 'main', cache_epoch: '0' },
    adapter: { id: 'test', version: '0.1.0', framework_version: '1', serialization_revision: 'test-v1' },
    manifest: [{ id: 'm0', sha256: '0'.repeat(64) }],
    candidates: [{ id: 'c0', content: 'x'.repeat(2000) }],
  });
  assert.equal(result.status, 'bypassed');
  assert.equal(result.reason, 'runtime_unavailable');
  assert.deepEqual(result.replacements, [], 'a runtime that never answered produced a replacement');
  assert.equal(result.cacheContinuity, 'unavailable');
  runtime.close();
});

test('ai-sdk hands the provider the caller\'s own messages when the proxy is down', async t => {
  let withCaveman, wrapLanguageModel;
  try {
    ({ withCaveman } = await import('../dist/ai-sdk.js'));
    ({ wrapLanguageModel } = await import('ai'));
  } catch (error) {
    if (error?.code !== 'ERR_MODULE_NOT_FOUND') throw error;
    t.skip(`framework peer not installed: ${error.message}`);
    return;
  }
  const original = 'ERROR keep this exactly\n' + 'noise '.repeat(400);
  const seen = [];
  const model = {
    specificationVersion: 'v4',
    provider: 'test',
    modelId: 'test-model',
    supportedUrls: {},
    async doGenerate(options) {
      seen.push(structuredClone(options.prompt));
      return { content: [{ type: 'text', text: 'done' }], finishReason: 'stop',
        usage: { inputTokens: { total: 1 }, outputTokens: { total: 1 } }, warnings: [] };
    },
  };
  const { runtime, reports } = await unreachableRuntime();
  const wrapped = withCaveman({ model, tools: {} }, {
    runtime, scope: { namespace: 'tests', session_id: 's1', branch_id: 'main', cache_epoch: '0' },
  });
  const prompt = [
    { role: 'user', content: [{ type: 'text', text: 'summarise' }] },
    { role: 'assistant', content: [{ type: 'tool-call', toolCallId: 'c1', toolName: 'read', input: {} }] },
    { role: 'tool', content: [{ type: 'tool-result', toolCallId: 'c1', toolName: 'read',
        output: { type: 'text', value: original } }] },
  ];
  const answer = await wrapped.model.doGenerate({ prompt, tools: [] });
  assert.equal(answer.content[0].text, 'done', 'an unreachable runtime broke the provider call');
  assert.equal(seen.length, 1);
  assert.equal(seen[0].at(-1).content[0].output.value, original,
    'an unreachable runtime sent the provider something other than the original');
  assert.ok(reports.every(report => ['skipped', 'disabled'].includes(report.status)),
    `the adapter claimed a decision it could not make: ${JSON.stringify(reports)}`);
  runtime.close();
});
