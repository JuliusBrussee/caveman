import { build } from "esbuild";
import { fileURLToPath } from "node:url";

const entry = fileURLToPath(new URL("../src/learn-tui.ts", import.meta.url));
const outfile = fileURLToPath(new URL("../dist/learn-tui.js", import.meta.url));

await build({
  entryPoints: [entry],
  outfile,
  bundle: true,
  platform: "node",
  format: "esm",
  target: "node22",
  minify: true,
  legalComments: "eof",
  sourcemap: false,
  banner: {
    js: "// Bundled interactive UI. @clack/prompts remains a build-time dependency only.",
  },
});
