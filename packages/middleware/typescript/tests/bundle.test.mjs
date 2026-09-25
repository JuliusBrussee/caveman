import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdtemp, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { build } from 'esbuild';
import { requirePeers } from './peers.mjs';

// C2 + C6: a deploy bundle (esbuild; the same shape as CDK NodejsFunction or a Next server bundle) inlines the
// frameworks, minifies class names, and runs from a directory with no node_modules. Framework versions are then
// unreadable, so adapters feature-detect and compress with a one-time version_unverified warning.
const here = url => JSON.stringify(fileURLToPath(new URL(url, import.meta.url)));
const entry = adapters => `
  import { drivers } from ${here('./drivers.mjs')};
  import { runtimeFixture, shortened } from ${here('./runtime-fixture.mjs')};
  (async () => {
    const warnings = [], results = {};
    console.warn = (...args) => warnings.push(args.join(' '));
    for (const name of ${JSON.stringify(adapters)}) {
      const f = runtimeFixture();
      try { results[name] = (await drivers[name].run(f.runtime)).seen === shortened; } catch (error) { results[name] = String(error?.stack ?? error); }
      finally { f.runtime.close(); }
    }
    console.log(JSON.stringify({ results, warnings }));
  })();
`;

// Frameworks import optional integrations (S3, Bedrock, …) lazily; the ones not installed stay external, as in any deploy.
const optional = { name: 'optional-dependencies', setup(build) {
  build.onResolve({ filter: /^[^./]/ }, async args => {
    if (args.pluginData) return undefined;
    const resolved = await build.resolve(args.path, { kind: args.kind, resolveDir: args.resolveDir, importer: args.importer, pluginData: true });
    return resolved.errors.length ? { path: args.path, external: true } : resolved;
  });
} };

const adapters = ['ai-sdk', 'langchain', 'strands'];
for (const format of ['esm', 'cjs']) {
  test(`a minified ${format} bundle with inlined frameworks still compresses (${adapters.join(', ')})`, async t => {
    for (const name of adapters) if (!requirePeers(t, name)) return;
    const directory = await mkdtemp(join(tmpdir(), 'caveman-bundle-'));
    const source = join(directory, 'entry.mjs'), outfile = join(directory, `bundle.${format === 'esm' ? 'mjs' : 'cjs'}`);
    await writeFile(source, entry(adapters));
    await build({ entryPoints: [source], outfile, bundle: true, minify: true, platform: 'node', format, target: 'node22.12', logLevel: 'silent', plugins: [optional],
      ...(format === 'esm' ? { banner: { js: "import { createRequire as __cr } from 'node:module'; const require = __cr(import.meta.url);" } } : {}) });
    const { stdout } = await promisify(execFile)(process.execPath, [outfile], { cwd: directory, maxBuffer: 16 << 20 });
    const { results, warnings } = JSON.parse(stdout.trim().split('\n').at(-1));
    for (const name of adapters) assert.equal(results[name], true, `${name}: ${results[name]}`);
    for (const id of adapters) assert.ok(warnings.some(line => line.includes(`adapter=${id} reason=version_unverified`)), warnings.join('\n'));
    assert.ok(!warnings.some(line => /reason=(unsupported_version|version_unavailable)/.test(line)), warnings.join('\n'));
  });
}
