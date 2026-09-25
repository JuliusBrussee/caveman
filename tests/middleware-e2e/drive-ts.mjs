// One AI SDK generateText call through the certified ai-sdk adapter (withCaveman) against a live runtime. The model
// is ai/test's mock: no provider, no network. Its first step asks caveman_retrieve for the handle it was shown, so
// the adapter's own tool loop performs the recovery.
//
//   node drive-ts.mjs --base URL --token T --from DIR --expect compress|passthrough [--default-deadlines]
//
// Bare imports (ai, @caveman-ai/sdk, @caveman-ai/middleware) resolve from --from: the repo's middleware package for
// HEAD, or an install of published versions. Prints one JSON line; exits 1 when the expectation fails.
import assert from 'node:assert/strict';
import { createHash, randomBytes } from 'node:crypto';
import { registerHooks } from 'node:module';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { isDeepStrictEqual, parseArgs } from 'node:util';

const { values: args } = parseArgs({ options: { base: { type: 'string' }, token: { type: 'string' }, from: { type: 'string' }, expect: { type: 'string' },
  'default-deadlines': { type: 'boolean' } } });
const parentURL = pathToFileURL(path.join(path.resolve(args.from), 'package.json')).href;
registerHooks({ resolve: (specifier, context, next) =>
  next(specifier, context.parentURL === import.meta.url && !specifier.startsWith('node:') ? { ...context, parentURL } : context) });

const { generateText, stepCountIs } = await import('ai');
const { MockLanguageModelV4 } = await import('ai/test');
const { createMiddlewareRuntime } = await import('@caveman-ai/sdk/middleware');
const { withCaveman } = await import('@caveman-ai/middleware/ai-sdk');

const sha256 = value => createHash('sha256').update(value).digest('hex');
let original = '';
for (let i = 0; i < 150; i++) original += `[INFO] reading row ${i}: café 🌍 exact-value-${String(i).padStart(3, '0')} with verbose repeated details\r\n`;
original += '[ERROR] preserve this diagnostic exactly\r\n';

// Wire evidence of the negotiation: what the client sent and what the runtime advertised.
const wire = { features_sent: null, runtime_features: null };
const fetch = async (url, init) => {
  const response = await globalThis.fetch(url, init);
  if (String(url).endsWith('/capabilities')) {
    wire.features_sent = new Headers(init?.headers).get('caveman-middleware-features');
    if (response.ok) wire.runtime_features = (await response.clone().json()).features ?? null;
  }
  return response;
};
const reports = [];
const deadlines = args['default-deadlines'] ? {} : { deadlineMs: 10_000, retrieveDeadlineMs: 10_000 };
const runtime = createMiddlewareRuntime({ endpoint: args.base, token: args.token, ...deadlines, fetch,
  onReport: report => { reports.push(`${report.status}:${report.reason}`); } });
const messages = [
  { role: 'user', content: 'Summarize this log.' },
  { role: 'assistant', content: [{ type: 'tool-call', toolCallId: 'read-1', toolName: 'read', input: {} }] },
  { role: 'tool', content: [{ type: 'tool-result', toolCallId: 'read-1', toolName: 'read', output: { type: 'text', value: original } }] },
];
const before = structuredClone(messages);
const usage = { inputTokens: { total: 1 }, outputTokens: { total: 1 } };
let sent = null;
const model = new MockLanguageModelV4({ doGenerate: async ({ prompt }) => {
  if (sent === null) {
    sent = prompt.at(-1).content[0].output.value;
    const handle = /handle=(cmw_[a-f0-9]{48})\]/.exec(sent)?.[1];
    if (handle) return { content: [{ type: 'tool-call', toolCallId: 'retrieve-1', toolName: 'caveman_retrieve', input: JSON.stringify({ handle }) }],
      finishReason: { unified: 'tool-calls', raw: 'tool_calls' }, usage, warnings: [] };
  }
  return { content: [{ type: 'text', text: 'done' }], finishReason: { unified: 'stop', raw: 'stop' }, usage, warnings: [] };
} });

let out;
try {
  const scope = { namespace: 'e2e', session_id: `ts-${randomBytes(6).toString('hex')}`, branch_id: 'main', cache_epoch: '0' };
  const result = await generateText({ ...withCaveman({ model }, { runtime, scope }), messages, stopWhen: stepCountIs(2), maxRetries: 0 });
  const recovery = result.steps[0]?.toolResults?.find(part => part.toolName === 'caveman_retrieve');
  out = { compressed: sent?.startsWith('[caveman: shortened;') ?? false, recovered: recovery ? sha256(recovery.output.text) === sha256(original) : false,
    passthrough: sent === original, intact: isDeepStrictEqual(messages, before), sent_bytes: Buffer.byteLength(sent ?? ''),
    original_bytes: Buffer.byteLength(original), text: result.text, reports, ...wire };
  if (args.expect === 'compress') {
    assert.ok(out.compressed, 'the model saw the original: no compression');
    assert.ok(out.sent_bytes < out.original_bytes, 'the replacement is not smaller');
    assert.ok(out.recovered, 'caveman_retrieve did not return the exact original');
  } else {
    assert.equal(args.expect, 'passthrough');
    assert.ok(out.passthrough, 'the model did not see the exact original');
  }
  assert.ok(out.intact, "the caller's history changed");
  console.log(JSON.stringify(out));
} catch (error) {
  console.log(JSON.stringify({ error: String(error?.stack ?? error), ...out, reports, ...wire }));
  process.exitCode = 1;
} finally {
  runtime.close();
}
