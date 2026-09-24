import assert from 'node:assert/strict';
import test from 'node:test';
import { inRange, matchesFramework } from '../dist/versions.js';
import { inspectFrameworkCompatibility, frameworkCompatible, frameworkGate } from '../dist/compatibility.js';
import { createMiddlewareRuntime } from '@caveman-ai/sdk/middleware';
import { runtimeFixture } from './runtime-fixture.mjs';
import { readFile } from 'node:fs/promises';

test('stable releases in compatibility range are distinct from the exact test pin', () => {
  assert.equal(inRange('7.0.94', '7.0.94', '8'), true);
  assert.equal(inRange('7.0.95', '7.0.94', '8'), true);
  assert.equal(inRange('7.14.0', '7.0.94', '8'), true, 'a later minor stays inside the major');
  assert.equal(inRange('7.0.93', '7.0.94', '8'), false, 'below the tested floor');
  assert.equal(inRange('8.0.0', '7.0.94', '8'), false, 'the next major is out');
  assert.equal(inRange('8.0.0-beta.1', '7.0.94', '8'), false, 'a prerelease compares as its release');
  assert.equal(inRange('7.0.95-beta.1', '7.0.94', '8'), false, 'prereleases do not satisfy stable compatibility');
  assert.equal(inRange('7.1.0-canary.3', '7.0.94', '8'), false, 'a canary inside the numeric range is still unsupported');
  assert.equal(inRange('7.0.95junk', '7.0.94', '8'), false);
  assert.equal(inRange('7.0.95+build.2', '7.0.94', '8'), true);
  assert.equal(inRange('7.0.95', 'invalid', '8'), false);
  assert.equal(inRange('7', '7.0.94', '8'), false, 'a short version is not silently padded upward');
  assert.equal(inRange('7.1', '7.0.94', '8'), false, 'installed versions need complete semver');
  assert.equal(inRange('0.124.0', '0.124', '1'), true, 'zero-major bands compare segment by segment');
  assert.equal(inRange('0.123.9', '0.124', '1'), false);
  assert.equal(inRange('1.0.0', '0.124', '1'), false);
});

test('no framework peers (Decision 2); tested releases, merge-gate pins and execution ranges stay aligned', async () => {
  const pkg = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'));
  assert.equal(pkg.peerDependencies, undefined, 'optional framework peers make npm install fail with ERESOLVE');
  assert.equal(pkg.peerDependenciesMeta, undefined);
  const tiers = {};
  for (const adapter of ['ai-sdk', 'openai', 'anthropic', 'google', 'langchain', 'langchain-core', 'strands', 'mastra', 'mcp']) {
    const result = inspectFrameworkCompatibility(adapter);
    tiers[adapter] = result.tier;
    for (const check of result.frameworks) {
      assert.deepEqual(pkg.testedFrameworkVersions[check.package], check.tested_versions);
      assert.equal(check.tested_versions[0], check.tested_version, 'the range floor is the first tested release');
      assert.equal(pkg.devDependencies[check.package], check.tested_version, 'the merge gate runs the floor');
      assert.equal(pkg.supportedFrameworkVersions[check.package], check.supported_range);
      for (const version of check.tested_versions) assert.equal(frameworkCompatible(check.package, version), true, `${check.package}@${version}`);
      assert.ok(check.action.length > 20);
    }
  }
  // Decision 11.
  assert.deepEqual(Object.keys(tiers).filter(adapter => tiers[adapter] === 'certified'), ['ai-sdk', 'openai', 'anthropic', 'langchain', 'langchain-core']);
  // C11: each entry is gated only on the packages it imports; nothing is gated on @langchain/langgraph.
  assert.deepEqual(inspectFrameworkCompatibility('langchain').frameworks.map(check => check.package), ['langchain', '@langchain/core']);
  assert.deepEqual(inspectFrameworkCompatibility('langchain-core').frameworks.map(check => check.package), ['@langchain/core']);
  assert.equal(frameworkCompatible('@anthropic-ai/sdk', '0.128.0'), true, 'released 0.125-0.128 were tested');
  assert.equal(frameworkCompatible('@anthropic-ai/sdk', '0.129.0'), false, 'zero-major next minor may break APIs');
  assert.equal(frameworkCompatible('openai', '7.12.0'), false, 'older patch than validated floor');
  assert.throws(() => inspectFrameworkCompatibility('typo'), /Unknown Caveman adapter/);
});

test('the version gate never throws at wrap time: warn once, decline for strict ready(), or run on acceptance', async () => {
  const lines = [], warn = console.warn;
  console.warn = line => lines.push(line);
  try {
    const strict = createMiddlewareRuntime({ strict: true, fetch: async () => Response.json({}) });
    assert.equal(frameworkGate('openai', { runtime: strict }, undefined, { openai: '8.0.0' }), 'unsupported_version');
    await assert.rejects(strict.ready(), { code: 'unsupported_version' });
    assert.equal(frameworkGate('openai', { runtime: strict }, undefined, { openai: '8.0.0' }), 'unsupported_version');
    assert.equal(lines.filter(line => line.includes('adapter=openai-sdk reason=unsupported_version')).length, 1, 'warn once');
    const { runtime } = runtimeFixture({ strict: true });
    assert.equal(frameworkGate('openai', { runtime, acceptFrameworkVersion: true }, undefined, { openai: '8.0.0' }), null);
    // Unreadable (bundled): feature detection decides; version_unverified runs and never declines.
    assert.equal(frameworkGate('mcp', { runtime }, () => true, { '@modelcontextprotocol/sdk': null }), null);
    assert.ok(lines.some(line => line.includes('adapter=mcp reason=version_unverified')));
    await runtime.ready();
    assert.equal(frameworkGate('mcp', { runtime }, () => false, { '@modelcontextprotocol/sdk': null }), 'version_unavailable');
    await assert.rejects(runtime.ready(), { code: 'version_unavailable' });
    assert.equal(frameworkGate('mcp', { runtime: createMiddlewareRuntime({ mode: 'off' }) }, () => false, { '@modelcontextprotocol/sdk': '9.0.0' }), null, 'off never gates');
    strict.close(); runtime.close();
  } finally { console.warn = warn; }
});

test('a missing or unparseable version is never in range', () => {
  assert.equal(inRange(null, '1.0', '2'), false);
  assert.equal(inRange('', '1.0', '2'), false);
  assert.equal(inRange('latest', '1.0', '2'), false);
  assert.equal(matchesFramework('@caveman-ai/no-such-framework', '1.0', '2'), false);
});
