#!/usr/bin/env node
// Middleware end-to-end against a real caveman-proxy built from HEAD:
//   - the conformance kit (packages/shared/contracts/conformance), with the operator token and again with a token
//     map of two principals, where the second may not touch the first one's namespace;
//   - one certified adapter per language through the runtime with a fake provider: TS ai-sdk (withCaveman + ai/test
//     mock model) and Python openai (a local fake OpenAI server). Each must compress, recover the exact original
//     through caveman_retrieve, and leave the caller's history untouched;
//   - the runtime is then killed and both adapters must pass the original through (fail-open);
//   - everything again on Postgres when CAVEMAN_TEST_POSTGRES_URL is set.
//
//   node tests/middleware-e2e/run.mjs
//
// Needs go, node >= 22.15, python >= 3.11 (CAVEMAN_E2E_PYTHON, default python3; openai goes into a temp venv) and the
// built TS packages: pnpm --filter "@caveman-ai/middleware..." build. CAVEMAN_E2E_PROXY_BIN reuses a built binary.
import { TOKEN, buildProxy, drivePython, driveTS, finish, kit, kitResult, pythonEnv, requireBuilt, startRuntime, step, tempDir, tokenMap } from './harness.mjs';

requireBuilt();
const work = await tempDir('e2e');
const [a, b] = [
  { name: 'tenant-a', token: 'caveman-e2e-tenant-a-token-0123456789', namespaces: ['kit-a'] },
  { name: 'tenant-b', token: 'caveman-e2e-tenant-b-token-0123456789', namespaces: ['kit-b'] },
];
// The quota makes the kit's 429 + Retry-After check reachable; each kit run spends its own principal's minute.
const env = { CAVEMAN_MIDDLEWARE_MODE: 'compress', CAVEMAN_MIDDLEWARE_TOKEN_MAP_FILE: await tokenMap(work, [a, b]),
  CAVEMAN_MIDDLEWARE_QUOTA_REQUESTS_PER_MINUTE: '300' };
const note = result => `${result.sent_bytes}/${result.original_bytes} bytes sent, reports ${result.reports.join(',')}`;

let bin, python;
await step('build caveman-proxy from HEAD', async () => { bin = await buildProxy(work); });
await step('python venv with openai', async () => { python = await pythonEnv('openai', ['openai==3.19.2']); });

async function against(label, extra = {}) {
  let runtime;
  await step(`${label}: start the runtime`, async () => { runtime = await startRuntime(bin, { ...env, ...extra }); return runtime.base; });
  if (!runtime) return;
  try {
    await step(`${label}: TS ai-sdk adapter compresses, recovers the exact original, leaves history intact`,
      async () => note(await driveTS(runtime.base, 'compress')));
    await step(`${label}: Python openai adapter compresses, recovers the exact original, leaves history intact`,
      async () => note(await drivePython(runtime.base, 'compress', { python })));
    // After the adapters: the kit's quota check spends the operator's requests for the rest of the minute.
    await step(`${label}: conformance kit, operator token`, async () => kitResult(await kit(runtime.base, '--token', TOKEN)));
    await step(`${label}: conformance kit, two principals: ${b.name} is refused ${a.name}'s namespace`,
      async () => kitResult(await kit(runtime.base, '--token', a.token, '--namespace', 'kit-a', '--foreign-token', b.token)));
  } finally {
    await runtime.stop('SIGKILL');
  }
  await step(`${label}: runtime killed: TS adapter passes the original through`, async () => note(await driveTS(runtime.base, 'passthrough')));
  await step(`${label}: runtime killed: Python adapter passes the original through`,
    async () => note(await drivePython(runtime.base, 'passthrough', { python })));
}

if (bin && python) {
  await against('sqlite');
  if (process.env.CAVEMAN_TEST_POSTGRES_URL) await against('postgres', { CAVEMAN_MIDDLEWARE_DATABASE_URL: process.env.CAVEMAN_TEST_POSTGRES_URL });
  else console.log('# postgres: skipped, CAVEMAN_TEST_POSTGRES_URL is unset');
}
await finish();
