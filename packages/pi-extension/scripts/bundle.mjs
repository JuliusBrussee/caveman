import { build } from "esbuild";
import { fileURLToPath } from "node:url";

const common = {
  bundle: true,
  format: "esm",
  platform: "node",
  target: "node22",
  external: ["@earendil-works/*", "@oh-my-pi/*", "typebox"],
};

await build({
  entryPoints: [fileURLToPath(new URL("../src/index.ts", import.meta.url))],
  outfile: fileURLToPath(new URL("../dist/index.mjs", import.meta.url)),
  ...common,
});

await build({
  entryPoints: [fileURLToPath(new URL("../src/omp.ts", import.meta.url))],
  outfile: fileURLToPath(new URL("../dist/omp.mjs", import.meta.url)),
  ...common,
});

// Test-only bundle; not referenced by the pi manifest.
await build({
  entryPoints: [fileURLToPath(new URL("../src/testable.ts", import.meta.url))],
  outfile: fileURLToPath(new URL("../dist/testable.mjs", import.meta.url)),
  ...common,
});
