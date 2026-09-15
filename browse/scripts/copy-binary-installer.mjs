import { copyFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const browseRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const mcpBin = resolve(browseRoot, "..", "mcp", "bin");
for (const file of ["binary-installer.generated.mjs", "release.generated.mjs"]) {
  copyFileSync(join(mcpBin, file), join(browseRoot, "bin", file));
}
