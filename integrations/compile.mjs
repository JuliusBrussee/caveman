// compile.mjs — the framework-recipe registry compiler (zero-dependency, build-time).
//
// Reads every recipes/*.json (one routing recipe per AI SDK/framework), validates it
// (fail-closed: an unknown lang or wire_protocol is a build error, never a guess —
// the honesty no-placeholder rule), and emits three artifacts:
//
//   - recipes.json                                        the published registry
//   - ../cli/src/recipes.generated.ts                     the CLI's EMBEDDED copy
//                                                         (keeps the CLI zero-runtime-dep)
//   - ../../cloud/web/components/gateway/recipes.generated.ts
//                                                         the web wizard/docs EMBEDDED copy
//                                                         (cloud may consume public data; the
//                                                         reverse import is forbidden)
//
// Recipes are DATA, not code — the collapse decision applies to app frameworks
// exactly as it does to agents: the proxy speaks 4 wire
// protocols; each recipe is just the one-line base-URL shape for a framework, with
// {{baseURL}} (gateway origin) and {{app}} (attribution slug for the /w/ path carrier)
// left as render-time templates.
//
// Run from the CLI build/test (`node ../integrations/compile.mjs`). Output is
// deterministic (recipes sorted by id), so re-running on unchanged input is a no-op.

import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const siblingCliDir = join(here, "..", "cli");
const packageCliDir = join(here, "..", "packages", "cli");
const cliDir = process.env.CAVEMAN_CLI_DIR
  ? resolve(process.env.CAVEMAN_CLI_DIR)
  : existsSync(join(siblingCliDir, "package.json"))
    ? siblingCliDir
    : packageCliDir;
const recipesDir = process.env.CAVEMAN_RECIPES_DIR ? resolve(process.env.CAVEMAN_RECIPES_DIR) : join(here, "recipes");
const siblingCatalogFile = join(here, "..", "shared", "provider-catalog", "catalog", "current.yaml");
const packagedCatalogFile = join(here, "..", "packages", "shared", "provider-catalog", "catalog", "current.yaml");
const catalogFile = process.env.CAVEMAN_CATALOG_FILE
  ? resolve(process.env.CAVEMAN_CATALOG_FILE)
  : existsSync(siblingCatalogFile)
    ? siblingCatalogFile
    : packagedCatalogFile;

const LANGS = new Set(["ts", "python", "bash"]);
// "multi" = the framework fans out to more than one provider protocol behind one config.
const WIRE_PROTOCOLS = new Set(["anthropic-messages", "openai-chat", "openai-responses", "gemini-generatecontent", "multi"]);

function die(msg) {
  console.error(`integration-recipe compile failed: ${msg}`);
  process.exit(1);
}

// loadCatalogModelIds — the set of model ids the provider catalog PRICES. A recipe that
// ships a model id absent from this set would hand copy-paste users a model_not_found,
// or (worse) get silently booked at a zero/unpriced rate that under-reports the very
// traffic our recipe sent. So an unpriced id is a fail-closed build error (honesty:
// no-fake-savings + the honest-zero rule). The catalog is public data (the source of
// truth for cost math); it is parsed by a minimal line scan so this compiler stays
// zero-dependency (no YAML parser).
function loadCatalogModelIds(file) {
  let text;
  try {
    text = readFileSync(file, "utf8");
  } catch (error) {
    die(`provider catalog not found at ${file} — cannot verify model pricing (fail-closed): ${error.message}`);
  }
  const ids = new Set();
  for (const match of text.matchAll(/^ {2}model:\s*(.+?)\s*$/gm)) ids.add(match[1].trim());
  if (ids.size === 0) die(`provider catalog at ${file} yielded no model ids (fail-closed)`);
  return ids;
}

// bareModelId strips a single leading `provider/` router prefix (litellm/crewai write
// `openai/gpt-…`) so the id can be matched against the catalog, which stores bare ids.
function bareModelId(id) {
  return id.replace(/^[a-z0-9][a-z0-9-]*\//, "");
}

const CATALOG_MODEL_IDS = loadCatalogModelIds(catalogFile);

// extractCodeModelIds — the reverse guard. A model literal is a quoted string in a
// `model`/`model_name` position; the extractor is anchored on that keyword (never a
// bare quoted string) so it cannot false-positive on unrelated literals, and it only
// reports id-shaped tokens (one carrying a digit — every real model id does). It catches
// a model literal added to `code` but NOT declared in `models`.
function extractCodeModelIds(code) {
  const found = new Set();
  const anchor = /\bmodel(?:_name)?\b["']?\s*[:=]\s*/gi;
  let m;
  while ((m = anchor.exec(code)) !== null) {
    const window = code.slice(m.index + m[0].length, m.index + m[0].length + 120);
    const quoted = window.match(/^[^"']{0,80}?["']([^"']+)["']/);
    if (!quoted) continue;
    const id = quoted[1];
    if (id.includes("{{") || id.includes("://") || !/[0-9]/.test(id)) continue;
    found.add(bareModelId(id));
  }
  return found;
}

function validate(r, file) {
  const need = (cond, msg) => { if (!cond) die(`${file}: ${msg}`); };
  need(r && typeof r === "object", "recipe is not an object");
  need(r.schema_version === "1", `schema_version must be "1"`);
  need(typeof r.id === "string" && /^[a-z0-9][a-z0-9-]*$/.test(r.id), "id must be kebab-case");
  for (const k of ["display_name", "code"]) {
    need(typeof r[k] === "string" && r[k].length > 0, `${k} must be a non-empty string`);
  }
  need(LANGS.has(r.lang), `unknown lang "${r.lang}" (fail-closed)`);
  need(WIRE_PROTOCOLS.has(r.wire_protocol), `unknown wire_protocol "${r.wire_protocol}" (fail-closed)`);
  need(r.code.includes("{{baseURL}}"), "code must reference {{baseURL}} — a recipe that doesn't route through the gateway is not a routing recipe");
  if (r.note !== undefined) need(typeof r.note === "string" && r.note.length > 0, "note must be a non-empty string");

  // models — the model ids this recipe pins, cross-checked against the priced catalog.
  // Required (may be empty for a recipe that pins no model, e.g. an SDK-default path).
  // Every declared id must (1) be PRICED by the catalog — fail-closed on an unpriced id,
  // the whole point — and (2) actually appear in `code`, so the declaration cannot lie.
  // Then the reverse guard: any model literal found in `code` must be declared, so a new
  // unpriced id cannot slip in undeclared.
  need(Array.isArray(r.models), "models must be an array of catalog-present model ids (may be empty)");
  need(r.models.every((id) => typeof id === "string" && id.length > 0), "every models entry must be a non-empty string");
  const declared = new Set(r.models.map(bareModelId));
  for (const id of r.models) {
    need(CATALOG_MODEL_IDS.has(bareModelId(id)), `model "${id}" is not priced in the provider catalog (fail-closed: no unpriced model in a shipped recipe)`);
    need(r.code.includes(id), `model "${id}" is declared in models but never appears in code`);
  }
  for (const id of extractCodeModelIds(r.code)) {
    need(declared.has(id), `code references model "${id}" but it is not declared in models (declare it so it is catalog-cross-checked)`);
  }
}

const files = readdirSync(recipesDir).filter((f) => f.endsWith(".json")).sort();
const recipes = [];
const seen = new Set();
for (const f of files) {
  let parsed;
  try {
    parsed = JSON.parse(readFileSync(join(recipesDir, f), "utf8"));
  } catch (e) {
    die(`${f}: invalid JSON — ${e.message}`);
  }
  validate(parsed, f);
  if (seen.has(parsed.id)) die(`${f}: duplicate id "${parsed.id}"`);
  seen.add(parsed.id);
  recipes.push(parsed);
}
recipes.sort((a, b) => a.id.localeCompare(b.id));

writeFileSync(join(here, "recipes.json"), JSON.stringify({ schema_version: "1", recipes }, null, 2) + "\n");

const PREAMBLE = `// GENERATED by integrations/compile.mjs from integrations/recipes/*.json — DO NOT EDIT.
// Run \`node scripts/compile-registries.mjs\` (wired into the CLI build/test) to regenerate.

export interface IntegrationRecipe {
  schema_version: string;
  id: string;
  display_name: string;
  lang: "ts" | "python" | "bash";
  wire_protocol: "anthropic-messages" | "openai-chat" | "openai-responses" | "gemini-generatecontent" | "multi";
  note?: string;
  /** Code with {{baseURL}} (gateway origin) and {{app}} (attribution slug) templates. */
  code: string;
  /** Model ids this recipe pins; every id is cross-checked against the priced provider catalog. */
  models: string[];
}

export const RECIPES: IntegrationRecipe[] = `;

const body = PREAMBLE + JSON.stringify(recipes, null, 2) + ";\n";
writeFileSync(join(cliDir, "src", "recipes.generated.ts"), body);

const WEB_PREAMBLE = `// GENERATED by integrations/compile.mjs from integrations/recipes/*.json — DO NOT EDIT.
// This is the web's embedded copy of the framework-recipe registry (cloud consumes
// public data; the reverse import is forbidden by make check-boundaries).
`;
// The cloud web copy only exists in the monorepo layout; the published caveman
// repo has no cloud/ tree, so skip it there rather than failing the CLI build.
const webDir = join(here, "..", "..", "cloud", "web", "components", "gateway");
if (existsSync(webDir)) {
  writeFileSync(join(webDir, "recipes.generated.ts"), WEB_PREAMBLE + body);
}

console.error(`compiled ${recipes.length} integration recipe(s): ${recipes.map((r) => r.id).join(", ")}`);
