// CommonJS TypeScript projects on `moduleResolution: node16` read the `default` branch of each export. Its .d.cts shim
// imports the ESM declarations type-only, so neither the consumer nor the shim hits TS1479 (no skipLibCheck needed), and
// re-exports that same module, so a MiddlewareRuntime from require() is the type @caveman-ai/middleware expects.
// `export =` of the type-only import alone is type-only too: `import sdk = require(...)` and `import * as` then fail
// with TS1361 on any value. So M is a const typed as the module's values, merged with a namespace of its types.
// Node 22.12+ loads the ESM code itself via require().
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';

const { exports } = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'));
for (const { default: { types } } of Object.values(exports)) {
  const esm = `./${types.split('/').at(-1).replace(/\.d\.cts$/, '.js')}`;
  const declarations = fileURLToPath(new URL(`../${types.replace(/\.d\.cts$/, '.d.ts')}`, import.meta.url));
  const program = ts.createProgram([declarations], { module: ts.ModuleKind.ESNext, moduleResolution: ts.ModuleResolutionKind.Bundler, noEmit: true });
  const checker = program.getTypeChecker();
  const typeNames = checker.getExportsOfModule(checker.getSymbolAtLocation(program.getSourceFile(declarations))).filter(symbol => {
    const target = symbol.flags & ts.SymbolFlags.Alias ? checker.getAliasedSymbol(symbol) : symbol;
    if (!(target.flags & ts.SymbolFlags.Type)) return false;
    // ponytail: a generic export would need its type parameters restated here; fail the build until one exists.
    if (target.declarations?.some(d => d.typeParameters?.length)) throw new Error(`cjs-types: generic export ${symbol.name} is unsupported`);
    return true;
  }).map(symbol => symbol.name).sort();
  writeFileSync(new URL(`../${types}`, import.meta.url), [
    `import type * as T from '${esm}' with { 'resolution-mode': 'import' };`,
    'declare const M: typeof T;',
    'declare namespace M {',
    ...typeNames.map(name => `  export type ${name} = T.${name};`),
    '}',
    'export = M;',
    '',
  ].join('\n'));
}
