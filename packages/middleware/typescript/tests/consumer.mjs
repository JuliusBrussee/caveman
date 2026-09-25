import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { basename, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';

const root = fileURLToPath(new URL('../', import.meta.url));
const { packageManager } = JSON.parse(await readFile(new URL('../../../../package.json', import.meta.url), 'utf8'));
const sdkMetadata = JSON.parse(await readFile(new URL('../../../sdk/typescript/package.json', import.meta.url), 'utf8'));
const pnpmCLI = process.env.npm_execpath;
if (!pnpmCLI || !/^pnpm\.(c?js)$/.test(basename(pnpmCLI))) {
  throw new Error('Run this consumer gate with pnpm run test:consumer so the pinned package-manager CLI is available.');
}
const directory = await mkdtemp(join(tmpdir(), 'caveman-middleware-consumer-'));
const run = (command, args, cwd = directory) => execFileSync(command, args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
const pnpm = (args, cwd) => run(process.execPath, [pnpmCLI, ...args], cwd);
try {
  const sdk = join(directory, 'sdk.tgz'), middleware = join(directory, 'middleware.tgz');
  pnpm(['pack', '--out', sdk], fileURLToPath(new URL('../../../sdk/typescript/', import.meta.url)));
  pnpm(['pack', '--out', middleware], root);
  const consumer = join(directory, 'app'); await mkdir(consumer);
  await writeFile(join(consumer, 'package.json'), JSON.stringify({ private: true, type: 'module', packageManager, dependencies: {
    '@caveman-ai/sdk': `file:${sdk}`, '@caveman-ai/middleware': `file:${middleware}`, ai: '7.0.94', '@ai-sdk/provider': '4.0.11', zod: '4.4.3',
    // @langchain/core without `langchain`: the model-only subpath must work alone.
    '@langchain/core': '1.2.9', typescript: '5.9.3',
  // The middleware's own ^SDK range must resolve to this tarball too, not an older registry release.
  }, pnpm: { overrides: { '@caveman-ai/sdk': `file:${sdk}` } } }));
  // A clean registry install proves consumer resolution independently of the
  // workspace. --offline is available only when all registry metadata is cached.
  pnpm(['install', '--no-frozen-lockfile', ...(process.argv.includes('--offline') ? ['--offline'] : []), '--ignore-scripts', '--config.auto-install-peers=false'], consumer);
  const metadata = JSON.parse(await readFile(join(consumer, 'node_modules/@caveman-ai/middleware/package.json'), 'utf8'));
  assert.equal(metadata.dependencies['@caveman-ai/sdk'], `^${sdkMetadata.version}`, 'packed dependency must not retain workspace protocol');
  // Decision 2: ranged peers make npm install fail with ERESOLVE; unranged optional ones only declare the imports (Yarn PnP).
  assert.ok(Object.values(metadata.peerDependencies).every(range => range === '*'), JSON.stringify(metadata.peerDependencies));
  const entry = join(consumer, 'entry.mjs');
  await writeFile(entry, `
    import assert from 'node:assert/strict';
    import { inspectFrameworkCompatibility } from '@caveman-ai/middleware/compatibility';
    import { withCaveman } from '@caveman-ai/middleware/ai-sdk';
    import { createMiddlewareRuntime } from '@caveman-ai/sdk/middleware';
    import { generateText } from 'ai';
    import { MockLanguageModelV4 } from 'ai/test';
    const check = inspectFrameworkCompatibility('ai-sdk');
    assert.equal(check.compatible, true, JSON.stringify(check));
    assert.equal(check.frameworks.every(item => item.tested), true);
    assert.equal(inspectFrameworkCompatibility('strands').compatible, false, 'unselected peers must not be installed');
    const runtime = createMiddlewareRuntime({ mode: 'off' });
    const model = new MockLanguageModelV4({ doGenerate: { content: [{ type: 'text', text: 'consumer-ok' }],
      finishReason: { unified: 'stop', raw: 'stop' }, usage: { inputTokens: { total: 1 }, outputTokens: { total: 1 } }, warnings: [] } });
    const result = await generateText({ ...withCaveman({ model }, { runtime,
      scope: { namespace: 'consumer', session_id: 'test', branch_id: 'main', cache_epoch: '0' } }), prompt: 'hello' });
    assert.equal(result.text, 'consumer-ok'); runtime.close();
    console.log('consumer-ok');
  `);
  assert.match(run(process.execPath, [entry], consumer), /consumer-ok/);
  // Bundle Caveman code while retaining framework installations and metadata.
  const bundled = join(consumer, 'bundle.mjs');
  const external = Object.keys(metadata.testedFrameworkVersions);
  await build({ entryPoints: [entry], outfile: bundled, bundle: true, platform: 'node', format: 'esm', target: 'node22.12', external });
  assert.match(run(process.execPath, [bundled], consumer), /consumer-ok/);
  // C9: CommonJS require() (Node 22.12+ loads the ESM build through the `default` condition).
  const required = join(consumer, 'required.cjs');
  await writeFile(required, `
    const assert = require('node:assert/strict');
    const { inspectFrameworkCompatibility } = require('@caveman-ai/middleware/compatibility');
    const { withCaveman, createCavemanMiddleware } = require('@caveman-ai/middleware/ai-sdk');
    assert.equal(typeof withCaveman, 'function'); assert.equal(typeof createCavemanMiddleware, 'function');
    assert.equal(inspectFrameworkCompatibility('ai-sdk').tier, 'certified');
    console.log('require-ok');
  `);
  assert.match(run(process.execPath, [required], consumer), /require-ok/);
  const model = join(consumer, 'langchain-model.mjs');
  await writeFile(model, `
    import assert from 'node:assert/strict';
    import { withCavemanModel } from '@caveman-ai/middleware/langchain-model';
    import { createMiddlewareRuntime } from '@caveman-ai/sdk/middleware';
    import { FakeListChatModel } from '@langchain/core/utils/testing';
    await assert.rejects(import('@caveman-ai/middleware/langchain'), { code: 'ERR_MODULE_NOT_FOUND' }, 'langchain is not installed');
    const runtime = createMiddlewareRuntime({ mode: 'off' });
    const model = withCavemanModel(new FakeListChatModel({ responses: ['langchain-model-ok'] }), { runtime, scope: { namespace: 'consumer', session_id: 'test' } });
    console.log((await model.invoke('hello')).content); runtime.close();
  `);
  assert.match(run(process.execPath, [model], consumer), /langchain-model-ok/);
  // C9: TypeScript resolution modes. A CommonJS project on node16 reads the .d.cts shim.
  const check = `
    import { withCaveman } from '@caveman-ai/middleware/ai-sdk';
    import { inspectFrameworkCompatibility } from '@caveman-ai/middleware/compatibility';
    import { withCavemanModel } from '@caveman-ai/middleware/langchain-model';
    const tier: 'certified' | 'experimental' = inspectFrameworkCompatibility('ai-sdk').tier;
    export const wrap: typeof withCaveman = withCaveman;
    export const wrapModel: typeof withCavemanModel = withCavemanModel;
    export { tier };
  `;
  // .cts is a CommonJS module whatever the package type: the TS1479 case.
  await writeFile(join(consumer, 'check.ts'), check); await writeFile(join(consumer, 'check.cts'), check);
  // Framework declarations (ai, zod, @langchain/core) need skipLibCheck on their own; ours must not. compatibility
  // depends only on the SDK, so this file checks the shims without it (an `export *` shim fails node16 with TS1479).
  await writeFile(join(consumer, 'types.cts'), `
    import { frameworkGate, inspectFrameworkCompatibility } from '@caveman-ai/middleware/compatibility';
    import { createMiddlewareRuntime } from '@caveman-ai/sdk/middleware';
    export const tier: 'certified' | 'experimental' = inspectFrameworkCompatibility('ai-sdk').tier;
    export const gated = frameworkGate('ai-sdk', { runtime: createMiddlewareRuntime({ mode: 'off' }) });
  `);
  const tsc = join(consumer, 'node_modules/typescript/bin/tsc');
  for (const [resolution, module, file] of [['node10', 'commonjs', 'check.cts'], ['node16', 'node16', 'check.cts'], ['nodenext', 'nodenext', 'check.cts'], ['bundler', 'esnext', 'check.ts']]) {
    run(process.execPath, [tsc, '--noEmit', '--strict', '--skipLibCheck', '--moduleResolution', resolution, '--module', module, join(consumer, file)], consumer);
    if (file === 'check.cts') run(process.execPath, [tsc, '--noEmit', '--strict', '--target', 'es2022', '--moduleResolution', resolution, '--module', module, join(consumer, 'types.cts')], consumer);
  }
  // C9: edge runtimes are an explicit unsupported target, with a clear error at import.
  const edge = join(consumer, 'edge.mjs');
  await writeFile(edge, `await import('@caveman-ai/middleware/ai-sdk').then(() => console.log('edge-imported'), error => console.log(error.message));`);
  for (const condition of ['workerd', 'edge-light']) {
    assert.match(run(process.execPath, ['--conditions', condition, edge], consumer), /edge runtimes \(workerd, edge-light\) are not supported/);
  }
  console.log('Consumer tarball install, require(), langchain-model without langchain, TypeScript node10/node16/nodenext/bundler, edge refusal, and Node ESM bundle passed.');
} finally { await rm(directory, { recursive: true, force: true }); }
