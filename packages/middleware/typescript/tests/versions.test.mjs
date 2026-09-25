import assert from 'node:assert/strict';
import test from 'node:test';
import { inRange, matchesFramework } from '../dist/versions.js';
import { inspectFrameworkCompatibility, frameworkCompatible, frameworkGate } from '../dist/compatibility.js';
import { nameConflict } from '../dist/common.js';
import { REASON_CATALOG, createMiddlewareRuntime } from '@caveman-ai/sdk/middleware';
import { runtimeFixture } from './runtime-fixture.mjs';
import { execFile } from 'node:child_process';
import { cp, mkdir, mkdtemp, readdir, readFile, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { MIDDLEWARE_VERSION } from '../dist/common.js';
import { requirePeers } from './peers.mjs';

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

test('framework peers are optional and unranged (Decision 2); tested releases, merge-gate pins and execution ranges stay aligned', async () => {
  const pkg = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'));
  // Ranged peers made a plain npm install fail with ERESOLVE (0.1.0-alpha.1): the ranges live in the run-time gate. The
  // peers only declare every framework the adapters import, so strict resolvers (Yarn PnP) let them resolve it.
  const imported = Object.keys(pkg.testedFrameworkVersions);
  assert.deepEqual(pkg.peerDependencies, Object.fromEntries(imported.map(name => [name, '*'])));
  assert.deepEqual(pkg.peerDependenciesMeta, Object.fromEntries(imported.map(name => [name, { optional: true }])));
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
    assert.ok(lines.some(line => line.includes('adapter=mcp') && line.includes('reason=version_unverified')));
    await runtime.ready();
    assert.equal(frameworkGate('mcp', { runtime }, () => false, { '@modelcontextprotocol/sdk': null }), 'version_unavailable');
    await assert.rejects(runtime.ready(), { code: 'version_unavailable' });
    assert.equal(frameworkGate('mcp', { runtime: createMiddlewareRuntime({ mode: 'off' }) }, () => false, { '@modelcontextprotocol/sdk': '9.0.0' }), null, 'off never gates');
    const conflict = createMiddlewareRuntime({ strict: true, fetch: async () => Response.json({}) });
    assert.equal(nameConflict(conflict, 'mcp'), 'recovery_name_conflict');
    await assert.rejects(conflict.ready(), { code: 'recovery_name_conflict' });
    // Every decline names its adapter in the warn-once line.
    assert.ok(!lines.some(line => line.includes('adapter=-')), lines.join('\n'));
    strict.close(); runtime.close(); conflict.close();
  } finally { console.warn = warn; }
});

test('a missing or unparseable version is never in range', () => {
  assert.equal(inRange(null, '1.0', '2'), false);
  assert.equal(inRange('', '1.0', '2'), false);
  assert.equal(inRange('latest', '1.0', '2'), false);
  assert.equal(matchesFramework('@caveman-ai/no-such-framework', '1.0', '2'), false);
});

// TS-2: an npm-workspaces layout. The middleware is hoisted to the root next to a stray ai@6 and openai@6; the app
// workspace has its own ai and openai 7. The gate must read the copy the adapter runs (ai: the stray root copy it
// imports; openai: the app's client) from either working directory, and warn about the ai mismatch only where the app
// resolves another copy.
test('TS-2: in an npm workspace the version gate reads the copy the adapter uses, whatever the working directory', async t => {
  if (!requirePeers(t, 'ai-sdk') || !requirePeers(t, 'openai')) return;
  const root = await mkdtemp(join(tmpdir(), 'caveman-workspace-')), here = fileURLToPath(new URL('../', import.meta.url));
  const packageDir = url => { let directory = dirname(fileURLToPath(url)); while (!(directory.endsWith('/ai') || directory.endsWith('/openai'))) directory = dirname(directory); return directory; };
  const write = async (path, text) => { await mkdir(dirname(join(root, path)), { recursive: true }); await writeFile(join(root, path), text); };
  await write('package.json', JSON.stringify({ private: true, workspaces: ['apps/*'] }));
  await write('apps/web/package.json', JSON.stringify({ name: 'web', private: true, type: 'module' }));
  // The stray root copies: ai@6 re-exports the real module so the adapter can load; openai@6 answers `openai/version`.
  await write('node_modules/ai/package.json', JSON.stringify({ name: 'ai', version: '6.0.0', type: 'module', exports: { '.': './index.js' } }));
  await write('node_modules/ai/index.js', `export * from ${JSON.stringify(import.meta.resolve('ai'))};`);
  await write('node_modules/openai/package.json', JSON.stringify({ name: 'openai', version: '6.0.0', type: 'module', exports: { './version': './version.js' } }));
  await write('node_modules/openai/version.js', "export const VERSION = '6.0.0';");
  // The hoisted middleware is a copy (a symlink would resolve its imports from this repository), the SDK a link.
  await mkdir(join(root, 'node_modules/@caveman-ai/middleware'), { recursive: true });
  await cp(join(here, 'dist'), join(root, 'node_modules/@caveman-ai/middleware/dist'), { recursive: true });
  await cp(join(here, 'package.json'), join(root, 'node_modules/@caveman-ai/middleware/package.json'));
  await symlink(join(here, 'node_modules/@caveman-ai/sdk'), join(root, 'node_modules/@caveman-ai/sdk'));
  await mkdir(join(root, 'apps/web/node_modules'), { recursive: true });
  for (const name of ['ai', 'openai']) await symlink(packageDir(import.meta.resolve(name)), join(root, 'apps/web/node_modules', name));
  await write('apps/web/check.mjs', `
    const warnings = []; console.warn = (...args) => warnings.push(args.join(' '));
    const { default: OpenAI } = await import('openai');
    const { inspectFrameworkCompatibility } = await import('@caveman-ai/middleware/compatibility');
    const { withCaveman } = await import('@caveman-ai/middleware/ai-sdk'), { withCavemanOpenAI } = await import('@caveman-ai/middleware/openai');
    const { createMiddlewareRuntime } = await import('@caveman-ai/sdk/middleware');
    const runtime = createMiddlewareRuntime({ fetch: async () => Response.json({}) }), scope = { namespace: 'n', session_id: 's' };
    withCaveman({ model: {} }, { runtime, scope }); withCavemanOpenAI(new OpenAI({ apiKey: 'k' }), { runtime, scope, fetch });
    console.log(JSON.stringify({ ai: inspectFrameworkCompatibility('ai-sdk').frameworks[0].installed_version, warnings })); runtime.close();`);
  const run = async cwd => JSON.parse((await promisify(execFile)(process.execPath, [join(root, 'apps/web/check.mjs')], { cwd })).stdout);
  const app = await run(join(root, 'apps/web')), top = await run(root);
  for (const result of [app, top]) {
    assert.equal(result.ai, '6.0.0', 'the gate reads the ai copy the adapter imports');
    assert.ok(result.warnings.some(line => line.includes('adapter=ai-sdk reason=unsupported_version')), result.warnings.join('\n'));
    assert.ok(!result.warnings.some(line => line.includes('adapter=openai-sdk reason=unsupported_version')), `the openai client is 7: ${result.warnings.join('\n')}`);
  }
  const mismatch = line => line.includes('resolves ai') && line.includes('imports ai 6.0.0');
  assert.equal(app.warnings.filter(mismatch).length, 1, app.warnings.join('\n'));
  assert.equal(top.warnings.filter(mismatch).length, 0, 'at the root the application resolves the same copy');
});

test('adapter versions sent on the wire are the package version', async t => {
  const pkg = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'));
  assert.equal(MIDDLEWARE_VERSION, pkg.version);
  if (!requirePeers(t, 'ai-sdk')) return;
  const { drivers } = await import('./drivers.mjs'), f = runtimeFixture();
  try { await drivers['ai-sdk'].run(f.runtime); } finally { f.runtime.close(); }
  assert.equal(f.requests[0].adapter.version, pkg.version);
});

// Every reason an adapter reports must be a §8 catalog key. Snake_case literals that are wire fields or identifiers,
// not reasons, are listed with the adapter ids; single-word reasons are caught where they are passed or assigned.
const NOT_REASONS = new Set(['cache_breakpoint', 'cache_control', 'call_id', 'caveman_retrieve', 'client_observed_sdk', 'dispatch_intent', 'function_call',
  'function_call_output', 'is_error', 'lc_name', 'message_stop', 'node_modules', 'parsed_arguments', 'tool_addition', 'tool_call_id', 'tool_removal',
  'tool_result', 'tool_use', 'tool_use_id', 'compatible', 'langchain', 'mastra', 'mcp']);
test('every reason literal in src is a REASON_CATALOG key', async () => {
  const literals = new Map();
  for (const file of await readdir(new URL('../src/', import.meta.url))) {
    const text = await readFile(new URL(`../src/${file}`, import.meta.url), 'utf8');
    for (const [, word] of text.matchAll(/'([a-z][a-z0-9]*(?:_[a-z0-9]+)+)'/g)) literals.set(word, file);
    for (const [, word] of text.matchAll(/(?:reason\s*[:=]\s*|\b(?:passive\w*|report|warnOnce|decline)\([^()]*?)(?<![=!]==\s*)'([a-z]+)'(?!\s*in\b)/g)) literals.set(word, file);
  }
  assert.ok(literals.has('recovery_unbound') && literals.has('disabled'), 'the scan finds reasons');
  const unknown = [...literals].filter(([word]) => !NOT_REASONS.has(word) && !Object.hasOwn(REASON_CATALOG, word));
  assert.deepEqual(unknown, []);
});

test('check-latest-in-range reads every TypeScript and Python range and flags only a latest release outside one', async () => {
  const { ranges, drift } = await import('../scripts/check-latest-in-range.mjs');
  const pkg = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'));
  const entries = await ranges();
  assert.deepEqual(entries.filter(([registry]) => registry === 'npm').map(([, name]) => name), Object.keys(pkg.supportedFrameworkVersions));
  assert.deepEqual(entries.find(([registry, name]) => registry === 'pypi' && name === 'anthropic'), ['pypi', 'anthropic', '1.0', '2']);
  assert.ok(entries.some(([registry, name]) => registry === 'pypi' && name === 'autogen-ext'));
  const sample = [['npm', '@anthropic-ai/sdk', '0.124.0', '0.129'], ['pypi', 'openai', '2.20', '4'], ['npm', 'openai', '7.12.1', '8']];
  assert.deepEqual(drift(sample, { 'npm:@anthropic-ai/sdk': '0.128.0', 'pypi:openai': '3.9.1', 'npm:openai': '7.23.0' }), []);
  assert.deepEqual(drift(sample, { 'npm:@anthropic-ai/sdk': '0.129.0', 'pypi:openai': '4.0.0', 'npm:openai': '8.0.0-beta.1' }), [
    'npm @anthropic-ai/sdk 0.129.0 is outside >=0.124.0 <0.129', 'pypi openai 4.0.0 is outside >=2.20 <4', 'npm openai 8.0.0-beta.1 is outside >=7.12.1 <8']);
  // PyPI versions follow PEP 440, not semver: any release length, post and local labels are in range like the Python gate.
  const pypi = [['pypi', 'google-genai', '2.18', '3'], ['pypi', 'litellm', '1.95', '2'], ['pypi', 'llama-index-core', '0.14.5', '0.15'], ['pypi', 'mcp', '2.0', '3']];
  assert.deepEqual(drift(pypi, { 'pypi:google-genai': '2.21', 'pypi:litellm': '1.95.3.post1', 'pypi:llama-index-core': '0.14.5.1', 'pypi:mcp': '2.0+local.1' }), []);
  assert.deepEqual(drift(pypi, { 'pypi:google-genai': '3.0', 'pypi:litellm': '2.0.0rc1', 'pypi:llama-index-core': '0.14.4.9', 'pypi:mcp': '1!2.1' }).length, 4);
  assert.deepEqual(drift(pypi.slice(1, 2), { 'pypi:litellm': '1.96.0.dev1' }).length, 1, 'a dev release is outside');
});
