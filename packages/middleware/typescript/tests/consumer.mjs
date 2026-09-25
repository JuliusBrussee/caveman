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
  } }));
  // A clean registry install proves consumer resolution independently of the
  // workspace. --offline is available only when all registry metadata is cached.
  pnpm(['install', '--no-frozen-lockfile', ...(process.argv.includes('--offline') ? ['--offline'] : []), '--ignore-scripts', '--config.auto-install-peers=false'], consumer);
  const metadata = JSON.parse(await readFile(join(consumer, 'node_modules/@caveman-ai/middleware/package.json'), 'utf8'));
  assert.equal(metadata.dependencies['@caveman-ai/sdk'], `^${sdkMetadata.version}`, 'packed dependency must not retain workspace protocol');
  assert.equal(metadata.peerDependenciesMeta.ai.optional, true);
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
  const external = Object.keys(metadata.peerDependencies);
  await build({ entryPoints: [entry], outfile: bundled, bundle: true, platform: 'node', format: 'esm', target: 'node22.13', external });
  assert.match(run(process.execPath, [bundled], consumer), /consumer-ok/);
  console.log('Consumer tarball install, selected-peer imports, and Node ESM bundle passed.');
} finally { await rm(directory, { recursive: true, force: true }); }
