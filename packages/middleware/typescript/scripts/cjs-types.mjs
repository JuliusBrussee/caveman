// CommonJS TypeScript projects on `moduleResolution: node16` read the `default` branch of each export. Point it at a
// .d.cts shim so they get the ESM declarations instead of TS1479 (Node 22.12+ loads the ESM code via require()).
import { readFileSync, writeFileSync } from 'node:fs';
const { exports } = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'));
for (const target of Object.values(exports)) {
  const types = target?.default?.types;
  if (types) writeFileSync(new URL(`../${types}`, import.meta.url), `export * from './${types.slice('./dist/'.length, -'.d.cts'.length)}.js';\n`);
}
