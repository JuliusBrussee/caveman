import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { SDK_VERSION } from "../dist/middleware/index.js";

const packageJSON = JSON.parse(
  await readFile(new URL("../package.json", import.meta.url), "utf8"),
);

test("package publishes only built runtime, types, license, notice, and README", () => {
  assert.equal(packageJSON.name, "@caveman-ai/sdk");
  assert.equal(packageJSON.version, "1.2.0");
  assert.deepEqual(packageJSON.files, ["dist", "README.md", "LICENSE", "NOTICE"]);
  assert.deepEqual(packageJSON.exports, {
    ".": {
      import: { types: "./dist/index.d.ts", default: "./dist/index.js" },
      default: { types: "./dist/index.d.cts", default: "./dist/index.js" },
    },
    "./middleware": {
      import: { types: "./dist/middleware/index.d.ts", default: "./dist/middleware/index.js" },
      default: { types: "./dist/middleware/index.d.cts", default: "./dist/middleware/index.js" },
    },
  });
  assert.deepEqual(packageJSON.typesVersions, { "*": { middleware: ["./dist/middleware/index.d.ts"] } });
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

test("node10 and CommonJS node16 TypeScript consumers type-check both entry points without skipLibCheck", async () => {
  const directory = await mkdtemp(join(tmpdir(), "caveman-sdk-types-"));
  try {
    await mkdir(join(directory, "node_modules/@caveman-ai"), { recursive: true });
    await symlink(fileURLToPath(new URL("../", import.meta.url)), join(directory, "node_modules/@caveman-ai/sdk"), "dir");
    // .cts is a CommonJS module whatever the package type: the TS1479 case under node16.
    await writeFile(join(directory, "check.cts"), `
      import { Cave } from "@caveman-ai/sdk";
      import { createMiddlewareRuntime, type MiddlewareRuntime } from "@caveman-ai/sdk/middleware";
      export const runtime: MiddlewareRuntime = createMiddlewareRuntime({ mode: "off" });
      export const client: typeof Cave = Cave;
    `);
    const tsc = createRequire(import.meta.url).resolve("typescript/bin/tsc");
    for (const [resolution, module] of [["node10", "commonjs"], ["node16", "node16"], ["nodenext", "nodenext"]]) {
      const result = spawnSync(process.execPath, [tsc, "--noEmit", "--strict", "--target", "es2022", "--moduleResolution", resolution,
        "--module", module, join(directory, "check.cts")], { cwd: directory, encoding: "utf8" });
      assert.equal(result.status, 0, `${resolution}: ${result.stdout}${result.stderr}`);
    }
  } finally { await rm(directory, { recursive: true, force: true }); }
});
