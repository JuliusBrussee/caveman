import { copyFileSync, existsSync, mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const roots = [resolve(packageRoot, ".."), resolve(packageRoot, "..", "..")];
const source = roots
  .map((root) => join(root, "bin", "lib", "cursor-mcp-json.js"))
  .find((candidate) => existsSync(candidate));

if (!source) {
  console.error("cursor-mcp bundle failed: bin/lib/cursor-mcp-json.js not found");
  process.exit(1);
}

const target = join(packageRoot, "dist", "cursor-mcp-json.cjs");
mkdirSync(dirname(target), { recursive: true });
copyFileSync(source, target);
console.log("bundled cursor-mcp-json.cjs");
