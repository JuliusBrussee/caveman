import { chmodSync, copyFileSync, existsSync, mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const roots = [
  resolve(packageRoot, ".."),
  resolve(packageRoot, "..", ".."),
];
const source = roots
  .flatMap((root) => [
    join(root, "agents", "delegate", "caveman-delegate-mcp.mjs"),
    join(root, "public", "agents", "delegate", "caveman-delegate-mcp.mjs"),
  ])
  .find((candidate) => existsSync(candidate));

if (!source) {
  console.error("delegate bundle failed: canonical public/agents/delegate/caveman-delegate-mcp.mjs not found");
  process.exit(1);
}

const target = join(packageRoot, "dist", "caveman-delegate-mcp.mjs");
const helperSource = join(dirname(source), "portable-process.mjs");
const helperTarget = join(packageRoot, "dist", "portable-process.mjs");
if (!existsSync(helperSource)) {
  console.error("delegate bundle failed: portable-process.mjs not found beside canonical delegate");
  process.exit(1);
}
mkdirSync(dirname(target), { recursive: true });
copyFileSync(source, target);
copyFileSync(helperSource, helperTarget);
chmodSync(target, 0o755);
