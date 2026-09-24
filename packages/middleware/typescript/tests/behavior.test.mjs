import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdir, mkdtemp, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { createMiddlewareRuntime } from '@caveman-ai/sdk/middleware';
import { drivers } from './drivers.mjs';
import { requirePeers } from './peers.mjs';
import { original, runtimeFixture, shortened } from './runtime-fixture.mjs';

// The same five behaviors for every adapter, through its compressing entry point.
for (const [name, driver] of Object.entries(drivers)) {
  test(`${name}: compresses an outbound copy and leaves the caller's input untouched`, async t => {
    if (!requirePeers(t, name)) return;
    const f = runtimeFixture(); t.after(() => f.runtime.close());
    const result = await driver.run(f.runtime);
    assert.equal(result.seen, shortened);
    assert.ok(result.intact, 'caller input changed');
    assert.equal(f.requests.length, 1);
    assert.equal(f.requests[0].segments[0].content, original);
    assert.ok(f.reports.some(report => report.status === 'applied'));
  });

  test(`${name}: a runtime outage passes the original through`, async t => {
    if (!requirePeers(t, name)) return;
    const runtime = createMiddlewareRuntime({ fetch: async () => { throw new TypeError('fetch failed'); } }); t.after(() => runtime.close());
    const result = await driver.run(runtime);
    assert.equal(result.seen, original);
    assert.ok(result.intact);
  });

  test(`${name}: an exception in adapter code passes the original through as adapter_error`, async t => {
    if (!requirePeers(t, name)) return;
    const f = runtimeFixture(); t.after(() => f.runtime.close());
    f.runtime.optimize = async () => { throw new TypeError('adapter bug'); };
    const result = await driver.run(f.runtime);
    assert.equal(result.seen, original);
    assert.ok(result.intact);
    assert.ok(f.reports.some(report => report.reason === 'adapter_error'), JSON.stringify(f.reports));
  });

  test(`${name}: strict mode raises an exception in adapter code as adapter_error`, async t => {
    if (!requirePeers(t, name)) return;
    const f = runtimeFixture({ strict: true }); t.after(() => f.runtime.close());
    f.runtime.optimize = async () => { throw new TypeError('adapter bug'); };
    // Frameworks may wrap it (provider SDKs as a connection error); the code rides the cause chain.
    const codes = error => error ? [error.code, ...codes(error.cause)] : [];
    await assert.rejects(driver.run(f.runtime), error => codes(error).includes('adapter_error'));
  });

  test(`${name}: scopes are normalized; an unusable or missing scope runs recovery-free without throwing`, async t => {
    if (!requirePeers(t, name)) return;
    const email = runtimeFixture(); t.after(() => email.runtime.close());
    assert.equal((await driver.run(email.runtime, { scope: { namespace: 'tenant', session_id: 'jane.doe@acme.com' } })).seen, shortened);
    assert.match(email.requests[0].scope.session_id, /^h-[0-9a-f]{32}$/);
    const invalid = runtimeFixture(); t.after(() => invalid.runtime.close());
    assert.equal((await driver.run(invalid.runtime, { scope: { namespace: 'tenant', session_id: '' } })).seen, original);
    assert.equal(invalid.requests.length, 0);
    assert.ok(invalid.reports.some(report => report.reason === 'invalid_scope'), JSON.stringify(invalid.reports));
    const missing = runtimeFixture(); t.after(() => missing.runtime.close());
    assert.equal((await driver.run(missing.runtime, { scope: () => null })).seen, original);
    assert.equal(missing.requests.length, 0);
    assert.ok(missing.reports.some(report => report.reason === 'recovery_unbound'), JSON.stringify(missing.reports));
  });
}

// Every framework out of range at once, in a child process whose application node_modules says so. `ai` is a
// prerelease inside the numeric range: prereleases are deliberately unsupported (versions.ts).
const fake = { ai: '7.1.0-canary.3', langchain: '2.0.0', '@langchain/core': '2.0.0', '@google/genai': '3.0.0', '@strands-agents/sdk': '2.0.0',
  '@mastra/core': '2.0.0', '@modelcontextprotocol/sdk': '2.0.0', openai: '8.0.0', '@anthropic-ai/sdk': '0.129.0' };
let child;
async function outOfRange(adapters) {
  child ??= (async () => {
    const cwd = await mkdtemp(join(tmpdir(), 'caveman-out-of-range-'));
    for (const [name, version] of Object.entries(fake)) {
      await mkdir(join(cwd, 'node_modules', name), { recursive: true });
      await writeFile(join(cwd, 'node_modules', name, 'package.json'), JSON.stringify({ name, version }));
    }
    const { stdout } = await promisify(execFile)(process.execPath, [fileURLToPath(new URL('./out-of-range.mjs', import.meta.url))],
      { cwd, env: { ...process.env, CAVEMAN_FAKE_VERSIONS: JSON.stringify(fake), CAVEMAN_ADAPTERS: JSON.stringify(adapters) }, maxBuffer: 16 << 20 });
    return JSON.parse(stdout);
  })();
  return child;
}
const installed = Object.keys(drivers).filter(name => requirePeers({ skip() {} }, name));
for (const [name, driver] of Object.entries(drivers)) {
  test(`${name}: an out-of-range framework warns once, passes through, raises only from strict ready(), and runs when accepted`, async t => {
    if (!requirePeers(t, name)) return;
    const { results, warnings } = await outOfRange(installed), result = results[name];
    assert.ok(!result.error, result.error);
    assert.equal(result.skipped.seen, original);
    assert.ok(result.skipped.intact);
    assert.equal(result.requests, 0);
    assert.ok(result.reasons.includes('unsupported_version'), JSON.stringify(result.reasons));
    assert.equal(warnings.filter(line => line.includes(`adapter=${driver.id} reason=unsupported_version`)).length, 1, warnings.join('\n'));
    assert.equal(result.strictRun, original, 'strict mode threw on the request path or at wrap time');
    assert.equal(result.ready, 'unsupported_version');
    assert.equal(result.accepted.seen, shortened, 'acceptFrameworkVersion did not run the adapter');
  });
}
