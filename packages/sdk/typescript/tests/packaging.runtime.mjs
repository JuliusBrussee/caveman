import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const packageJSON = JSON.parse(
  await readFile(new URL("../package.json", import.meta.url), "utf8"),
);

test("package publishes only built runtime, types, license, and README", () => {
  assert.equal(packageJSON.name, "@caveman-ai/sdk");
  assert.equal(packageJSON.version, "1.0.0");
  assert.deepEqual(packageJSON.files, ["dist", "README.md", "LICENSE"]);
  assert.deepEqual(packageJSON.exports, {
    ".": {
      types: "./dist/index.d.ts",
      import: "./dist/index.js",
    },
  });
  assert.equal(packageJSON.sideEffects, false);
  assert.equal(packageJSON.publishConfig.access, "public");
  assert.equal(packageJSON.dependencies, undefined);
  assert.equal(packageJSON.scripts.prepack, "npm run build");
  assert.equal(packageJSON.scripts.test, "npm run build && npm run test:types && npm run test:node");
});
