// Validates middleware responses emitted by the Go tests against the contract
// schemas in packages/shared/contracts/schemas.
//
//   CAVEMAN_MIDDLEWARE_GOLDEN_DIR=/tmp/mw-goldens go test ./proxy/internal/middleware -run Golden
//   node proxy/internal/middleware/testdata/validate-goldens.mjs /tmp/mw-goldens
//
// (The Go test runs this script itself when node is on PATH.) Each file is
// <schema>--<case>.json and is checked against middleware-<schema>.schema.json.
// Exit 0 valid, 1 invalid, 3 ajv not installed (run pnpm install).
import { readdir, readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const contracts = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../../packages/shared/contracts");
let Ajv2020;
try {
  // Resolve ajv the way the contracts package does, from its own node_modules.
  const mod = createRequire(path.join(contracts, "package.json"))("ajv/dist/2020.js");
  Ajv2020 = mod.default ?? mod;
} catch {
  console.error("ajv not installed for packages/shared/contracts");
  process.exit(3);
}
const dir = process.argv[2];
if (!dir) {
  console.error("usage: validate-goldens.mjs <dir>");
  process.exit(2);
}
const schemaDir = path.join(contracts, "schemas");
const ajv = new Ajv2020({ allErrors: true, strict: true });
for (const file of (await readdir(schemaDir)).filter((name) => name.endsWith(".schema.json"))) {
  ajv.addSchema(JSON.parse(await readFile(path.join(schemaDir, file), "utf8")));
}
const base = "https://raw.githubusercontent.com/JuliusBrussee/caveman/main/packages/shared/contracts/schemas/";
const goldens = (await readdir(dir)).filter((name) => name.endsWith(".json")).sort();
let failed = 0;
for (const file of goldens) {
  const schema = file.split("--")[0];
  const validate = ajv.getSchema(`${base}middleware-${schema}.schema.json`);
  if (!validate) {
    console.error(`${file}: no schema middleware-${schema}`);
    failed++;
    continue;
  }
  if (!validate(JSON.parse(await readFile(path.join(dir, file), "utf8")))) {
    console.error(`${file}: ${ajv.errorsText(validate.errors)}`);
    failed++;
  }
}
if (goldens.length === 0) {
  console.error(`no goldens in ${dir}`);
  process.exit(1);
}
console.log(`validated ${goldens.length - failed}/${goldens.length} middleware responses against the contract schemas`);
process.exit(failed ? 1 : 0);
