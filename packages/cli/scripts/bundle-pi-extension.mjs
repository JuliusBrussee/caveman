import { build } from "esbuild";
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";

const entry = fileURLToPath(new URL("../../pi-extension/src/index.ts", import.meta.url));
const outfile = fileURLToPath(new URL("../dist/caveman-pi-extension.mjs", import.meta.url));

if (!existsSync(entry)) {
  // A silent skip here ships a CLI whose `enable pi`/`wrap pi` asset does not
  // exist while every build stays green — fail the build instead.
  console.error("pi extension bundle FAILED: packages/pi-extension/src/index.ts not found");
  process.exit(1);
}

await build({
  entryPoints: [entry],
  outfile,
  bundle: true,
  format: "esm",
  platform: "node",
  target: "node22",
  external: ["@earendil-works/*", "typebox"],
});
