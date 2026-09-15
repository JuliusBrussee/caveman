import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const candidates = [
  resolve(packageRoot, ".."),
  resolve(packageRoot, "..", ".."),
];
const registryRoot = candidates.find((candidate) =>
  existsSync(join(candidate, "agents", "compile.mjs"))
  && existsSync(join(candidate, "integrations", "compile.mjs"))
  && existsSync(join(candidate, "skills", "compile.mjs")));

if (!registryRoot) {
  console.error("registry compile failed: agents/, integrations/, and skills/ must share a parent with the CLI package");
  process.exit(1);
}

for (const registry of ["agents", "integrations", "skills"]) {
  const result = spawnSync(
    process.execPath,
    [join(registryRoot, registry, "compile.mjs")],
    {
      cwd: registryRoot,
      env: { ...process.env, CAVEMAN_CLI_DIR: packageRoot },
      stdio: "inherit",
    },
  );
  if (result.error) {
    console.error(`registry compile failed: ${result.error.message}`);
    process.exit(1);
  }
  if (result.status !== 0) process.exit(result.status ?? 1);
}
