import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import test from "node:test";
import { SDK_VERSION } from "../dist/middleware/index.js";

const packageJSON = JSON.parse(
  await readFile(new URL("../package.json", import.meta.url), "utf8"),
);

test("package publishes only built runtime, types, license, and README", () => {
  assert.equal(packageJSON.name, "@caveman-ai/sdk");
  assert.equal(packageJSON.version, "1.1.0");
  assert.deepEqual(packageJSON.files, ["dist", "README.md", "LICENSE"]);
  assert.deepEqual(packageJSON.exports, {
    ".": {
      types: "./dist/index.d.ts",
      import: "./dist/index.js",
      default: "./dist/index.js",
    },
    "./middleware": {
      types: "./dist/middleware/index.d.ts",
      import: "./dist/middleware/index.js",
      default: "./dist/middleware/index.js",
    },
  });
  // Node 20 is EOL; 22.12 is the first 22.x where require() loads this ESM-only package (require(esm)).
  assert.equal(packageJSON.engines.node, ">=22.12");
  assert.equal(SDK_VERSION, packageJSON.version, "Caveman-Middleware-Client must carry the published version");
  assert.equal(packageJSON.sideEffects, false);
  assert.equal(packageJSON.publishConfig.access, "public");
  assert.equal(packageJSON.dependencies, undefined);
  assert.equal(packageJSON.scripts.prepack, "npm run build");
  assert.equal(packageJSON.scripts.test, "npm run build && npm run test:types && npm run test:node");
});

test("CommonJS consumers can require() both entry points (require(esm))", () => {
  const require = createRequire(new URL("../package.json", import.meta.url));
  assert.equal(typeof require("@caveman-ai/sdk").Cave, "function");
  assert.equal(typeof require("@caveman-ai/sdk/middleware").createMiddlewareRuntime, "function");
});
