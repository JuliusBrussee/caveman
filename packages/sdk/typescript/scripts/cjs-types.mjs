// CommonJS TypeScript projects on `moduleResolution: node16` read the `default` branch of each export. Its .d.cts shim
// imports the ESM declarations type-only, so neither the consumer nor the shim hits TS1479 (no skipLibCheck needed), and
// re-exports that same module, so a MiddlewareRuntime from require() is the type @caveman-ai/middleware expects.
// Node 22.12+ loads the ESM code itself via require().
import { readFileSync, writeFileSync } from 'node:fs';
const { exports } = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'));
for (const { default: { types } } of Object.values(exports)) {
  const esm = `./${types.split('/').at(-1).replace(/\.d\.cts$/, '.js')}`;
  writeFileSync(new URL(`../${types}`, import.meta.url), `import type * as M from '${esm}' with { 'resolution-mode': 'import' };\nexport = M;\n`);
}
