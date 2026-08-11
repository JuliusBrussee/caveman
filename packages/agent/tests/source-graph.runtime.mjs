import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, mkdir, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { sourceGraphManifest, sourceGraphSHA256 } from "../dist/source-graph.js";

test("source graph tracks ESM, CJS, workspace packages, and import-meta assets", async (t) => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-source-graph-"));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });
  await mkdir(resolve(root, "src"), { recursive: true });
  await mkdir(resolve(root, "node_modules/workspace-lib"), { recursive: true });
  await writeFile(resolve(root, "src/entry.mjs"), [
    'import "./esm.js";',
    'import /* misleading: from "./ghost.js" */ "./commented.js";',
    'void import(/* exact dynamic import */ "./dynamic.js");',
    String.raw`import "./\u0065scaped.js";`,
    'require /* loader */ (/* misleading "./ghost.cjs" */ "./cjs.cjs");',
    'require /* loader */ . /* member */ resolve("workspace-lib");',
    'require?.("./optional-call.cjs");',
    'require?.resolve("./optional-member.cjs");',
    'require.resolve?.("./optional-resolve-call.cjs");',
    'require["resolve"]("./bracket-resolve.cjs");',
    'new /* loader */ URL(/* misleading "./ghost.txt" */ "./prompt.txt", import /* metadata */ . meta /* property */ . url);',
  ].join("\n"));
  await writeFile(resolve(root, "src/esm.js"), 'export { value } from "./nested.js";');
  await writeFile(resolve(root, "src/nested.js"), "export const value = 1;");
  await writeFile(resolve(root, "src/commented.js"), "export const commented = true;");
  await writeFile(resolve(root, "src/dynamic.js"), "export const dynamic = true;");
  await writeFile(resolve(root, "src/escaped.js"), "export const escaped = true;");
  await writeFile(resolve(root, "src/cjs.cjs"), 'module.exports = require("./data.json");');
  await writeFile(resolve(root, "src/bracket-resolve.cjs"), "module.exports = true;");
  await writeFile(resolve(root, "src/optional-call.cjs"), "module.exports = true;");
  await writeFile(resolve(root, "src/optional-member.cjs"), "module.exports = true;");
  await writeFile(resolve(root, "src/optional-resolve-call.cjs"), "module.exports = true;");
  await writeFile(resolve(root, "src/data.json"), '{"ok":true}');
  await writeFile(resolve(root, "src/prompt.txt"), "stable prompt");
  await writeFile(resolve(root, "node_modules/workspace-lib/package.json"), JSON.stringify({
    name: "workspace-lib",
    main: "index.cjs",
  }));
  await writeFile(resolve(root, "node_modules/workspace-lib/index.cjs"), "module.exports = 1;");

  const entry = resolve(root, "src/entry.mjs");
  const manifest = await sourceGraphManifest(root, [entry]);
  const names = manifest.map((item) => item.name);
  for (const name of [
    "src/entry.mjs",
    "src/bracket-resolve.cjs",
    "src/esm.js",
    "src/nested.js",
    "src/commented.js",
    "src/dynamic.js",
    "src/escaped.js",
    "src/cjs.cjs",
    "src/data.json",
    "src/optional-call.cjs",
    "src/optional-member.cjs",
    "src/optional-resolve-call.cjs",
    "src/prompt.txt",
    "node_modules/workspace-lib/index.cjs",
  ]) {
    assert.equal(names.includes(name), true, name);
  }
  const before = await sourceGraphSHA256(root, [entry]);
  await writeFile(resolve(root, "src/prompt.txt"), "changed prompt");
  const after = await sourceGraphSHA256(root, [entry]);
  assert.notEqual(after, before);
});

test("source graph resolves NodeNext runtime extensions to authored TypeScript", async (t) => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-source-graph-nodenext-"));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });
  await mkdir(resolve(root, "src"), { recursive: true });
  await writeFile(resolve(root, "src/entry.ts"), [
    'export { agent } from "./agent.js";',
    'export { config } from "./config.mjs";',
    'export { legacy } from "./legacy.cjs";',
    'import type { TypeOnly } from "./types.js";',
    'export type { TypeOnly };',
  ].join("\n"));
  await writeFile(resolve(root, "src/agent.ts"), "export const agent = true;");
  await writeFile(resolve(root, "src/config.mts"), "export const config = true;");
  await writeFile(resolve(root, "src/legacy.cts"), "export const legacy = true;");
  await writeFile(resolve(root, "src/types.ts"), "export type TypeOnly = string;");

  const manifest = await sourceGraphManifest(root, [resolve(root, "src/entry.ts")]);
  assert.deepEqual(manifest.map((item) => item.name), [
    "src/agent.ts",
    "src/config.mts",
    "src/entry.ts",
    "src/legacy.cts",
    "src/types.ts",
  ]);
});

test("source graph tracks TypeScript import-equals and type re-exports", async (t) => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-source-graph-typescript-"));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });
  await mkdir(resolve(root, "src"), { recursive: true });
  await writeFile(resolve(root, "src/entry.cts"), [
    'import Runtime = require(/* exact external module */ "./runtime.cjs");',
    'import type = require("./type-name.cjs");',
    'import type Types = require("./types.cjs");',
    'import Qualified = Namespace.Member;',
    'export import Exported = Namespace.Member;',
    'export type { Named } from /* exact named type export */ "./named.js";',
    'export type /* exact star type export */ * from "./star.js";',
    String.raw`export type * as Escaped from "./\u0065scaped.js";`,
  ].join("\n"));
  await writeFile(resolve(root, "src/runtime.cts"), "export const runtime = true;");
  await writeFile(resolve(root, "src/type-name.cts"), "export const namedTypeAlias = true;");
  await writeFile(resolve(root, "src/types.cts"), "export type Value = string;");
  await writeFile(resolve(root, "src/named.ts"), "export type Named = string;");
  await writeFile(resolve(root, "src/star.ts"), "export type Star = string;");
  await writeFile(resolve(root, "src/escaped.ts"), "export type Escaped = string;");

  const manifest = await sourceGraphManifest(root, [resolve(root, "src/entry.cts")]);
  assert.deepEqual(manifest.map((item) => item.name), [
    "src/entry.cts",
    "src/escaped.ts",
    "src/named.ts",
    "src/runtime.cts",
    "src/star.ts",
    "src/type-name.cts",
    "src/types.cts",
  ]);

  const qualified = resolve(root, "src/type-qualified.cts");
  await writeFile(qualified, "import type = Namespace.Member; export {};");
  assert.deepEqual(
    (await sourceGraphManifest(root, [qualified])).map((item) => item.name),
    ["src/type-qualified.cts"],
  );
});

test("source graph resolves import-only package exports used by generated config", async (t) => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-source-graph-import-only-"));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });
  await mkdir(resolve(root, "node_modules/@caveman-ai/agent/dist"), { recursive: true });
  await mkdir(resolve(root, "node_modules/runtime-leaf"), { recursive: true });
  await writeFile(resolve(root, "caveman.config.mjs"), [
    'import { defineBuild } from "@caveman-ai/agent/build";',
    'export default defineBuild({ entry: "src/agent.ts" });',
  ].join("\n"));
  await writeFile(resolve(root, "node_modules/@caveman-ai/agent/package.json"), JSON.stringify({
    name: "@caveman-ai/agent",
    type: "module",
    exports: {
      "./build": { types: "./dist/build.d.ts", import: "./dist/build.js" },
    },
    dependencies: { "runtime-leaf": "1.0.0" },
  }));
  await writeFile(
    resolve(root, "node_modules/@caveman-ai/agent/dist/build.js"),
    'export { defineBuild } from "./runtime.js";',
  );
  await writeFile(
    resolve(root, "node_modules/@caveman-ai/agent/dist/runtime.js"),
    'const target = "./private.js"; export const defineBuild = (value) => value; import(target);',
  );
  await writeFile(resolve(root, "node_modules/runtime-leaf/package.json"), JSON.stringify({
    name: "runtime-leaf",
    version: "1.0.0",
    main: "index.js",
  }));
  await writeFile(resolve(root, "node_modules/runtime-leaf/index.js"), "module.exports = 1;");

  const configPath = resolve(root, "caveman.config.mjs");
  const manifest = await sourceGraphManifest(root, [configPath]);
  assert.equal(
    manifest.some((entry) => entry.name === "node_modules/@caveman-ai/agent/dist/build.js"),
    true,
  );
  assert.equal(
    manifest.some((entry) => entry.name === "node_modules/@caveman-ai/agent/dist/runtime.js"),
    true,
  );
  assert.equal(
    manifest.some((entry) => entry.name === "node_modules/runtime-leaf/index.js"),
    true,
  );
  const before = await sourceGraphSHA256(root, [configPath]);
  await writeFile(
    resolve(root, "node_modules/@caveman-ai/agent/dist/runtime.js"),
    'const target = "./private.js"; export const defineBuild = () => "changed"; import(target);',
  );
  const afterRuntimeChange = await sourceGraphSHA256(root, [configPath]);
  assert.notEqual(afterRuntimeChange, before);
  await writeFile(resolve(root, "node_modules/runtime-leaf/index.js"), "module.exports = 2;");
  assert.notEqual(await sourceGraphSHA256(root, [configPath]), afterRuntimeChange);
});

test("source graph resolves dependency closure from pnpm package symlinks", async (t) => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-source-graph-pnpm-"));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });
  const storeRoot = resolve(root, "node_modules/.pnpm");
  const agentStore = resolve(storeRoot, "@caveman-ai+agent@0.1.0/node_modules");
  const agentRoot = resolve(agentStore, "@caveman-ai/agent");
  const leafStore = resolve(storeRoot, "runtime-leaf@1.0.0/node_modules/runtime-leaf");
  await mkdir(resolve(root, "node_modules/@caveman-ai"), { recursive: true });
  await mkdir(resolve(agentRoot, "dist"), { recursive: true });
  await mkdir(leafStore, { recursive: true });
  await writeFile(resolve(root, "caveman.config.mjs"), [
    'import { defineBuild } from "@caveman-ai/agent/build";',
    'export default defineBuild({ entry: "src/agent.ts" });',
  ].join("\n"));
  await writeFile(resolve(agentRoot, "package.json"), JSON.stringify({
    name: "@caveman-ai/agent",
    type: "module",
    exports: { "./build": { import: "./dist/build.js" } },
    dependencies: { "runtime-leaf": "1.0.0" },
  }));
  await writeFile(resolve(agentRoot, "dist/build.js"), "export const defineBuild = (value) => value;");
  await writeFile(resolve(leafStore, "package.json"), JSON.stringify({
    name: "runtime-leaf",
    version: "1.0.0",
    main: "index.js",
  }));
  await writeFile(resolve(leafStore, "index.js"), "module.exports = 1;");
  await symlink(
    resolve(storeRoot, "@caveman-ai+agent@0.1.0/node_modules/@caveman-ai/agent"),
    resolve(root, "node_modules/@caveman-ai/agent"),
    "dir",
  );
  await symlink(
    resolve(storeRoot, "runtime-leaf@1.0.0/node_modules/runtime-leaf"),
    resolve(agentStore, "runtime-leaf"),
    "dir",
  );

  const configPath = resolve(root, "caveman.config.mjs");
  const manifest = await sourceGraphManifest(root, [configPath]);
  assert.equal(manifest.some((entry) => entry.name.endsWith("/dist/build.js")), true);
  assert.equal(manifest.some((entry) => entry.name.endsWith("/index.js")), true);
  const before = await sourceGraphSHA256(root, [configPath]);
  await writeFile(resolve(leafStore, "index.js"), "module.exports = 2;");
  assert.notEqual(await sourceGraphSHA256(root, [configPath]), before);
});

test("source graph fails closed on computed runtime dependencies", async (t) => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-computed-source-"));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });
  await mkdir(resolve(root, "src"), { recursive: true });
  const entry = resolve(root, "src/entry.mjs");
  for (const source of [
    'const name = "../policy/rules.mjs"; export const rules = await import(name);',
    'const name = "./rules.cjs"; require(/* still computed */ name);',
    'const name = "./rules.cjs"; require /* loader */ . resolve(/* still computed */ name);',
    'const name = "./rules.cjs"; require?.(/* still computed */ name);',
    'const name = "./rules.cjs"; require?.resolve(/* still computed */ name);',
    'const name = "./rules.cjs"; require.resolve?.(/* still computed */ name);',
    'const name = "./rules.cjs"; require["resolve"](/* still computed */ name);',
    'const name = "./rules.cjs"; (require)(name);',
    'const name = "./rules.cjs"; (0, require)(name);',
    'const name = "./rules.txt"; new /* loader */ URL(/* still computed */ name, import.meta.url);',
  ]) {
    await writeFile(entry, source);
    await assert.rejects(
      sourceGraphSHA256(root, [entry]),
      /computed source dependency is not lockable/,
    );
  }
});

test("source graph ignores loader examples inside comments and strings", async (t) => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-source-comments-"));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });
  await mkdir(resolve(root, "src"), { recursive: true });
  const entry = resolve(root, "src/entry.mjs");
  await writeFile(entry, [
    '// import "./not-a-real-file.js";',
    "/* require(dynamicName); createRequire(import.meta.url); */",
    'const documentation = "require(otherName) and import(\\\"./ghost.js\\\")";',
    "const template = `createRequire(import.meta.url); require(templateName)`;",
    "const pattern = /require\\(regexName\\)|import\\(\\\".\\/ghost.js\\\"\\)/;",
    "if (pattern.test(documentation)) /new URL\\(regexName/.test(documentation);",
    "export const value = documentation.length;",
  ].join("\n"));
  const manifest = await sourceGraphManifest(root, [entry]);
  assert.deepEqual(manifest.map((item) => item.name), ["src/entry.mjs"]);
});

test("source graph checks computed loaders inside template expressions", async (t) => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-source-template-expression-"));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });
  await mkdir(resolve(root, "src"), { recursive: true });
  const entry = resolve(root, "src/entry.mjs");
  await writeFile(entry, "const name = './rules.js'; export const value = `${require(name)}`;");
  await assert.rejects(
    sourceGraphSHA256(root, [entry]),
    /computed source dependency is not lockable/,
  );
});

test("source graph checks computed loaders after postfix operators and division", async (t) => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-source-graph-postfix-"));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });
  const entry = resolve(root, "entry.cjs");
  await writeFile(entry, `
let x = 1;
const dynamicName = "./dependency.cjs";
x++ / require(dynamicName) / x--;
`);

  await assert.rejects(
    sourceGraphSHA256(root, [entry]),
    /computed source dependency is not lockable/,
  );
});

test("source graph rejects createRequire, require, and URL aliases", async (t) => {
  const root = await mkdtemp(resolve(tmpdir(), "cave-aliased-source-"));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });
  await mkdir(resolve(root, "src"), { recursive: true });
  const entry = resolve(root, "src/entry.mjs");
  for (const source of [
    'import { createRequire } from "node:module"; const r = createRequire(import.meta.url); r(name);',
    'import { createRequire } from "node:module"; const r = createRequire /* loader */ (import.meta.url); r(name);',
    "const r = require; r(name);",
    "const r = /* loader */ require; r(name);",
    "const URLCtor = URL; new URLCtor(name, import.meta.url);",
    "const URLCtor = /* loader */ URL; new URLCtor(name, import.meta.url);",
    "module.require(name);",
    'module["require"](name);',
    'module?.["require"](name);',
    "require.bind(null)(name);",
    'require["bind"](null)(name);',
    'require["call"](null, name);',
    'require["apply"](null, [name]);',
    "Reflect.apply(require, null, [name]);",
    "Reflect.apply((require), null, [name]);",
    "Reflect.apply((0, require), null, [name]);",
    'Reflect["apply"](require, null, [name]);',
    'Reflect /* object */ . /* member */ apply(require, null, [name]);',
    "new globalThis.URL(name, import.meta.url);",
    'new globalThis /* object */ ["URL"](name, import.meta.url);',
    'globalThis["eval"]("require(name)");',
    'eval /* loader */ ("require(name)");',
    'Function /* loader */ ("return require")()(name);',
    'Function("return require")()(name);',
  ]) {
    await writeFile(entry, source);
    await assert.rejects(
      sourceGraphSHA256(root, [entry]),
      /aliased source loader is not lockable/,
    );
  }
});
