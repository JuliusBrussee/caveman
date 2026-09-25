import assert from 'node:assert/strict';
import test from 'node:test';
import { inRange, matchesFramework } from '../dist/versions.js';
import { inspectFrameworkCompatibility, frameworkCompatible } from '../dist/compatibility.js';
import { readFile } from 'node:fs/promises';

test('stable releases in compatibility range are distinct from the exact test pin', () => {
  assert.equal(inRange('7.0.94', '7.0.94', '8'), true);
  assert.equal(inRange('7.0.95', '7.0.94', '8'), true);
  assert.equal(inRange('7.14.0', '7.0.94', '8'), true, 'a later minor stays inside the major');
  assert.equal(inRange('7.0.93', '7.0.94', '8'), false, 'below the tested floor');
  assert.equal(inRange('8.0.0', '7.0.94', '8'), false, 'the next major is out');
  assert.equal(inRange('8.0.0-beta.1', '7.0.94', '8'), false, 'a prerelease compares as its release');
  assert.equal(inRange('7.0.95-beta.1', '7.0.94', '8'), false, 'prereleases do not satisfy stable compatibility');
  assert.equal(inRange('7.0.95junk', '7.0.94', '8'), false);
  assert.equal(inRange('7.0.95+build.2', '7.0.94', '8'), true);
  assert.equal(inRange('7.0.95', 'invalid', '8'), false);
  assert.equal(inRange('7', '7.0.94', '8'), false, 'a short version is not silently padded upward');
  assert.equal(inRange('7.1', '7.0.94', '8'), false, 'installed versions need complete semver');
  assert.equal(inRange('0.124.0', '0.124', '1'), true, 'zero-major bands compare segment by segment');
  assert.equal(inRange('0.123.9', '0.124', '1'), false);
  assert.equal(inRange('1.0.0', '0.124', '1'), false);
});

test('optional consumer peers, exact test pins, and execution ranges stay aligned', async () => {
  const pkg = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'));
  for (const adapter of ['ai-sdk', 'openai', 'anthropic', 'google', 'langchain', 'strands', 'mastra', 'mcp']) {
    const result = inspectFrameworkCompatibility(adapter);
    for (const check of result.frameworks) {
      assert.equal(pkg.peerDependencies[check.package], '*');
      assert.equal(pkg.peerDependenciesMeta[check.package].optional, true);
      assert.equal(pkg.testedFrameworkVersions[check.package], check.tested_version);
      assert.equal(pkg.devDependencies[check.package], check.tested_version);
      assert.equal(pkg.supportedFrameworkVersions[check.package], check.supported_range);
      assert.ok(check.action.length > 20);
    }
  }
  assert.equal(frameworkCompatible('@anthropic-ai/sdk', '0.125.0'), false, 'zero-major next minor may break APIs');
  assert.equal(frameworkCompatible('openai', '7.12.0'), false, 'older patch than validated floor');
  assert.throws(() => inspectFrameworkCompatibility('typo'), /Unknown Caveman adapter/);
});

test('a missing or unparseable version is never in range', () => {
  assert.equal(inRange(null, '1.0', '2'), false);
  assert.equal(inRange('', '1.0', '2'), false);
  assert.equal(inRange('latest', '1.0', '2'), false);
  assert.equal(matchesFramework('@caveman-ai/no-such-framework', '1.0', '2'), false);
});
