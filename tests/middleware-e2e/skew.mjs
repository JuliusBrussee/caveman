#!/usr/bin/env node
// Version skew in both directions (docs/technical/middleware-protocol.md §16, plan Decision 10):
//   runtime  HEAD SDK clients (TS ai-sdk, Python openai) against the published protocol 1.0 runtime for this host,
//            downloaded with gh and verified by the repo's signed-checksum installer. They must negotiate the legacy
//            view and still compress and recover.
//   clients  The published clients, unchanged and with their default deadlines, against a HEAD runtime:
//            @caveman-ai/sdk@1.1.0 + @caveman-ai/middleware@0.1.0-alpha.2, and caveman-sdk==1.1.0 +
//            caveman-middleware==0.1.0a1 (Python 3.13+).
//
//   node tests/middleware-e2e/skew.mjs [runtime|clients]      (default: both)
//
// Needs gh (authenticated, or GH_TOKEN), go, npm, node >= 22.15 and python >= 3.13 (CAVEMAN_E2E_PYTHON). The runtime
// tag is N1_RUNTIME, not the installer's pin (which moves to the runtime under test); CAVEMAN_SKEW_RUNTIME_TAG
// overrides it.
import assert from 'node:assert/strict';
import { createReadStream } from 'node:fs';
import { mkdir } from 'node:fs/promises';
import http from 'node:http';
import { once } from 'node:events';
import path from 'node:path';
import { ensureBinary, targetPlatform } from '../../packages/shared/binary-installer/installer.mjs';
import { TOKEN, buildProxy, drivePython, driveTS, finish, kit, kitResult, npmEnv, pythonEnv, requireBuilt, sh, startRuntime, step, tempDir } from './harness.mjs';

const REPO = 'JuliusBrussee/caveman';
// N-1: the newest published 1.x runtime (protocol 1.0). bin-v1.1.8 was pinned but never published, and the next
// runtime release is bin-v2.0.0; bin-v1.1.7's middleware wire code is the same as that pin's
// (`git diff bin-v1.1.7 ae26f3a4 -- proxy/internal/middleware` only changes tombstone comparisons).
const N1_RUNTIME = 'bin-v1.1.7';
const HEADER = 'http_status_v2, revision_tolerant';
const which = process.argv[2] ?? 'both';
if (!['runtime', 'clients', 'both'].includes(which)) {
  console.error('usage: node skew.mjs [runtime|clients]');
  process.exit(2);
}
const work = await tempDir('skew');

/** gh-downloads the proxy asset for this host and installs it through installer.mjs's ensureBinary, served from a
 * loopback mirror, so the signed checksums.txt and the asset digest are checked exactly as users' installs do. */
async function publishedRuntime() {
  const tag = process.env.CAVEMAN_SKEW_RUNTIME_TAG ?? N1_RUNTIME;
  if ((await sh('gh', ['release', 'view', tag, '-R', REPO, '--json', 'tagName'], { allowFail: true })).code !== 0) {
    throw new Error(`${tag} is not a published release of ${REPO}`);
  }
  const { os, arch } = targetPlatform();
  const assets = path.join(work, 'assets');
  await mkdir(assets, { recursive: true });
  await sh('gh', ['release', 'download', tag, '-R', REPO, '-D', assets, '--clobber', '-p', 'checksums.txt', '-p', 'checksums.txt.keysig', '-p', `caveman-proxy_${os}_${arch}`]);
  const mirror = http.createServer((request, response) => createReadStream(path.join(assets, path.basename(request.url)))
    .on('error', () => { response.statusCode = 404; response.end(); }).pipe(response)).listen(0, '127.0.0.1');
  await once(mirror, 'listening');
  // PATH is emptied so an installed caveman-proxy cannot stand in for the download.
  const overrides = { CAVE_BINARY_RELEASE_BASE: `http://127.0.0.1:${mirror.address().port}`, CAVEMAN_HOME: path.join(work, 'home'), PATH: '' };
  const saved = Object.fromEntries(Object.keys(overrides).map(key => [key, process.env[key]]));
  Object.assign(process.env, overrides);
  try {
    return { tag, bin: await ensureBinary({ name: 'caveman-proxy', envVar: 'CAVEMAN_SKEW_UNSET_BINARY' }) };
  } finally {
    for (const [key, value] of Object.entries(saved)) value === undefined ? delete process.env[key] : process.env[key] = value;
    mirror.close();
  }
}

if (which !== 'clients') {
  requireBuilt();
  let published, runtime, python;
  await step('runtime: download and verify the published caveman-proxy', async () => { published = await publishedRuntime(); return published.tag; });
  await step('runtime: python venv with openai', async () => { python = await pythonEnv('openai', ['openai==3.19.2']); });
  if (published) {
    // Protocol 1.0 runtimes read the middleware mode from CAVEMAN_MODE.
    await step(`runtime: start ${published.tag}`, async () => { runtime = await startRuntime(published.bin, { CAVEMAN_MODE: 'compress' }); return runtime.base; });
  }
  if (runtime) {
    try {
      await step(`runtime: conformance kit --legacy-only against ${published.tag}`, async () => kitResult(await kit(runtime.base, '--token', TOKEN, '--legacy-only')));
      await step('runtime: HEAD TS SDK negotiates the legacy view, compresses and recovers', async () => {
        const result = await driveTS(runtime.base, 'compress');
        assert.equal(result.features_sent, HEADER, 'HEAD client did not send its features header');
        assert.equal(result.runtime_features, null, 'a protocol 1.0 runtime advertised features');
        return `${result.sent_bytes}/${result.original_bytes} bytes sent`;
      });
      await step('runtime: HEAD Python SDK negotiates the legacy view, compresses and recovers', async () => {
        const result = await drivePython(runtime.base, 'compress', { python });
        assert.equal(result.runtime_features, null, 'a protocol 1.0 runtime advertised features');
        return `${result.sent_bytes}/${result.original_bytes} bytes sent`;
      });
    } finally {
      await runtime.stop();
    }
  }
}

if (which !== 'runtime') {
  let bin, runtime, tsClients, pyClients;
  await step('clients: build caveman-proxy from HEAD', async () => { bin = await buildProxy(work); });
  await step('clients: install @caveman-ai/sdk@1.1.0 + @caveman-ai/middleware@0.1.0-alpha.2', async () => {
    tsClients = await npmEnv('published-ts', ['@caveman-ai/sdk@1.1.0', '@caveman-ai/middleware@0.1.0-alpha.2', 'ai@7.0.94', '@ai-sdk/provider@4.0.11']);
  });
  await step('clients: install caveman-sdk==1.1.0 + caveman-middleware==0.1.0a1', async () => {
    pyClients = await pythonEnv('published-py', ['caveman-sdk==1.1.0', 'caveman-middleware==0.1.0a1', 'openai==3.19.2']);
  });
  if (bin) await step('clients: start the HEAD runtime', async () => { runtime = await startRuntime(bin, { CAVEMAN_MIDDLEWARE_MODE: 'compress' }); return runtime.base; });
  if (runtime) {
    try {
      if (tsClients) {
        await step('clients: published TS middleware compresses and recovers against HEAD (1.0 wire)', async () => {
          const result = await driveTS(runtime.base, 'compress', { from: tsClients, defaultDeadlines: true });
          assert.equal(result.features_sent, null, 'the published client sent a features header');
          return `${result.sent_bytes}/${result.original_bytes} bytes sent`;
        });
      }
      if (pyClients) {
        await step('clients: published Python middleware compresses and recovers against HEAD (1.0 wire)', async () => {
          const result = await drivePython(runtime.base, 'compress', { python: pyClients, pythonPath: null, defaultDeadlines: true });
          assert.equal(result.runtime_features, null, 'HEAD served the 1.1 view to a client that sent no features header');
          return `${result.sent_bytes}/${result.original_bytes} bytes sent`;
        });
      }
    } finally {
      await runtime.stop();
    }
  }
}
await finish();
