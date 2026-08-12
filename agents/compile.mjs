// compile.mjs — the agent-profile registry compiler (zero-dependency, build-time).
//
// Reads every profiles/*.json, validates it against the profile contract
// (fail-closed: an unknown wire_protocol or injection method is a build error,
// never a guess — the honesty no-placeholder rule), and emits two artifacts:
//
//   - agents.json                       the published registry (one consumer today: the CLI;
//                                        forward-looking for docs/web registry views)
//   - ../cli/src/agents.generated.ts     the CLI's EMBEDDED copy (keeps the CLI zero-runtime-dep:
//                                        it imports a generated module, never reads a file at runtime)
//
// Run from the CLI build/test (`node ../agents/compile.mjs`). Output is deterministic
// (profiles sorted by id), so re-running on unchanged input produces no diff.

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
const profilesDir = process.env.CAVEMAN_PROFILES_DIR ? resolve(process.env.CAVEMAN_PROFILES_DIR) : join(here, "profiles");
const schemaFile = join(here, "profiles", "schema.json");
const reservedFile = join(here, "reserved-verbs.json");

const WIRE_PROTOCOLS = new Set(["anthropic-messages", "openai-chat", "openai-responses", "gemini-generatecontent"]);
const INJECTION_METHODS = new Set(["env", "config-env-content", "config-file"]);
const COMMAND_HOOK_METHODS = new Set(["claude-pretooluse", "codex-pretooluse", "gemini-beforetool", "opencode-plugin", "hermes-plugin", "openclaw-plugin", "instruction-note"]);
const MEMORY_HOOK_METHODS = new Set(["claude-userpromptsubmit"]);
const SKILL_FORMATS = new Set(["skill-md"]);
const ENV_KEY_PATTERN = "^[A-Z][A-Z0-9_]*_(BASE_URL|API_BASE|API_KEY|AUTH_TOKEN|HOST)$";
const ENV_VALUE_PATTERN = "^(?:\\{\\{cave_(?:proxy_url|api_key|org_id)\\}\\}|\\{\\{cave_base_url\\}\\}(?:/[A-Za-z0-9._-]+)*|[A-Za-z0-9._-]+)$";
const PROFILE_PATH_PATTERN = "^~/\\.[a-z0-9][a-z0-9-]*/[A-Za-z0-9._/-]+$";
const ENV_KEY_RE = new RegExp(ENV_KEY_PATTERN);
const ENV_VALUE_RE = new RegExp(ENV_VALUE_PATTERN);
const PROFILE_PATH_RE = new RegExp(PROFILE_PATH_PATTERN);
const TEMPLATE_RE = /\{\{cave_(?:base_url|proxy_url|api_key|org_id)\}\}/g;
const ENV_VAR_RE = /^[A-Z][A-Z0-9_]*$/;

function die(msg) {
  console.error(`agent-profile compile failed: ${msg}`);
  process.exit(1);
}

// The three cross-repo-coupled honesty gates (catalog pricing, injection_completeness,
// derived CI pins) are HARD on normal builds/PRs, but the profiles-backsync workflow
// recompiles PUBLIC-repo profiles that this repo's CI pins/catalog may legitimately lead;
// it sets CAVEMAN_REGISTRY_GATES_ADVISORY=1 so a cross-repo skew surfaces as a warning in
// the human-reviewed sync patch instead of a hard failure. The load-bearing SECURITY checks
// (env-key allowlist, loader-control-key rejection, path confinement, shell-metachar) are
// NEVER advisory — they always use die().
const GATES_ADVISORY = process.env.CAVEMAN_REGISTRY_GATES_ADVISORY === "1";
function gate(cond, msg) {
  if (cond) return;
  if (GATES_ADVISORY) console.error(`agent-profile compile WARNING (advisory, cross-repo gate): ${msg}`);
  else die(msg);
}

function readJSON(file, label) {
  try {
    return JSON.parse(readFileSync(file, "utf8"));
  } catch (error) {
    die(`${label}: invalid JSON — ${error.message}`);
  }
}

const schema = readJSON(schemaFile, "schema.json");
const schemaProperties = new Set(Object.keys(schema?.properties ?? {}));
const schemaPatterns = schema?.$defs ?? {};
for (const [name, expected] of [
  ["envKey", ENV_KEY_PATTERN],
  ["envValue", ENV_VALUE_PATTERN],
  ["profilePath", PROFILE_PATH_PATTERN],
]) {
  if (schemaPatterns[name]?.pattern !== expected) {
    die(`schema.json: $defs.${name}.pattern must equal ${JSON.stringify(expected)}`);
  }
}

const reservedDocument = readJSON(reservedFile, "reserved-verbs.json");
if (reservedDocument?.schema_version !== "1" || !Array.isArray(reservedDocument.verbs)) {
  die('reserved-verbs.json: schema_version must be "1" and verbs must be an array');
}
const reservedVerbs = [...reservedDocument.verbs].sort();
if (reservedVerbs.length === 0 || reservedVerbs.some((verb) => typeof verb !== "string" || verb.length === 0)) {
  die("reserved-verbs.json: every verb must be a non-empty string");
}
if (new Set(reservedVerbs).size !== reservedVerbs.length) die("reserved-verbs.json: duplicate verb");

function loaderControlKey(key) {
  return key === "NODE_OPTIONS"
    || key === "PATH"
    || key === "NODE_PATH"
    || key === "PYTHONSTARTUP"
    || key === "PERL5OPT"
    || key.startsWith("LD_")
    || key.startsWith("DYLD_")
    || key.endsWith("_PROXY");
}

function profilePathAllowed(value, id) {
  if (!PROFILE_PATH_RE.test(value) || !value.startsWith(`~/.${id}/`)) return false;
  return !value.split("/").includes("..");
}

function validateEnvVariableName(value, need, label) {
  need(typeof value === "string" && ENV_VAR_RE.test(value) && !loaderControlKey(value), `${label} is not a safe environment variable name`);
}

function validateConfigStrings(value, need, path = "injection config") {
  if (typeof value === "string") {
    const tokens = value.match(/\{\{[^}]+\}\}/g) ?? [];
    const knownTokens = value.match(TEMPLATE_RE) ?? [];
    need(tokens.length === knownTokens.length, `${path} contains an unknown template token`);
    need(!/[`;]|\$\(|\r|\n|\0/.test(value), `${path} contains a shell or loader metacharacter`);
    const key = path.split(".").at(-1) ?? "";
    if (/^(baseurl|base_url|api_base|host|endpoint|url)$/i.test(key)) {
      const remainder = value.replace(TEMPLATE_RE, "");
      need(knownTokens.length > 0 && /^[A-Za-z0-9._/-]*$/.test(remainder), `${path} must route through a cave template token`);
    } else if (value.includes("://")) {
      need(key === "$schema" && /^https:\/\/[A-Za-z0-9._/-]+$/.test(value), `${path} contains a literal URL`);
    }
    return;
  }
  if (Array.isArray(value)) {
    value.forEach((item, index) => validateConfigStrings(item, need, `${path}[${index}]`));
    return;
  }
  if (value && typeof value === "object") {
    for (const [key, item] of Object.entries(value)) validateConfigStrings(item, need, `${path}.${key}`);
  }
}

// ---- provider-catalog cross-check (fail-closed on an unpriced model) ----
// A profile that pins a model id (opencode's inline config, e.g.) must pin one the
// catalog PRICES: an unpriced id books at a zero/unpriced rate and under-reports the
// operator's spend on traffic our own wrap sent. The catalog is public data (the source
// of truth for cost math), parsed by a minimal line scan so the compiler stays zero-dep.
const siblingCatalogFile = join(here, "..", "shared", "provider-catalog", "catalog", "current.yaml");
const packagedCatalogFile = join(here, "..", "packages", "shared", "provider-catalog", "catalog", "current.yaml");
const catalogFile = process.env.CAVEMAN_CATALOG_FILE
  ? resolve(process.env.CAVEMAN_CATALOG_FILE)
  : existsSync(siblingCatalogFile)
    ? siblingCatalogFile
    : packagedCatalogFile;
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
// Strip a single leading `provider/` prefix (opencode default is `caveman/<model>`).
function bareModelId(id) {
  return id.replace(/^[a-z0-9][a-z0-9-]*\//, "");
}
const CATALOG_MODEL_IDS = loadCatalogModelIds(catalogFile);
// Collect model ids from a profile's injection config: values of a `model` key and the
// keys of a `models` map (the opencode/openai-compatible convention).
function collectInjectionModelIds(node, out) {
  if (Array.isArray(node)) { for (const v of node) collectInjectionModelIds(v, out); return; }
  if (node && typeof node === "object") {
    for (const [k, v] of Object.entries(node)) {
      if (k === "model" && typeof v === "string" && v) out.add(bareModelId(v));
      if (k === "models" && v && typeof v === "object" && !Array.isArray(v)) for (const mk of Object.keys(v)) out.add(bareModelId(mk));
      collectInjectionModelIds(v, out);
    }
  }
}

// ---- code-assisted builder manifest (generated by scanning the CLI) ----
// The doc claim "adding an agent is a data change, not a code change" is only true for
// agents the CLI routes purely from their profile. Several are NOT: their real routing
// lives in a code builder. This manifest is DERIVED from index.ts so a profile cannot
// claim `declarative` while a builder actually handles it (issue #135). Signals:
//   - `overlayBuilders.<id> =`   a config-file overlay builder that overrides the profile.
//   - a named agent-specific wrap builder present in the source.
// If index.ts is absent (a layout with no CLI) the manifest is unknown and the id-based
// tier check is skipped; the inert-injection rule (below) still applies unconditionally.
const BUILDER_FUNCTIONS = { buildCodexEphemeralWrapEnv: "codex", applyHermesAuthEnv: "hermes", applyClaudeBedrockWrap: "claude" };
// Builders that MUST resolve when index.ts is present: if the scrape misses one, a wrap
// builder was renamed/removed and the manifest (plus the affected profile's tier) is stale.
// A silent miss would let a now-builder-routed profile keep a `declarative` claim, so we
// fail LOUD here rather than trust a degraded manifest.
const EXPECTED_BUILDER_IDS = ["openclaw", "codex", "hermes", "claude"];
function loadBuilderManifest() {
  const indexFile = join(cliDir, "src", "index.ts");
  let text;
  try { text = readFileSync(indexFile, "utf8"); } catch { return null; }
  const ids = new Set();
  for (const m of text.matchAll(/overlayBuilders\.([a-z0-9-]+)\s*=/g)) ids.add(m[1]);
  for (const [fn, id] of Object.entries(BUILDER_FUNCTIONS)) if (text.includes(fn)) ids.add(id);
  // Zero-builder guard (mirrors loadCatalogModelIds' zero-guard): index.ts present but no
  // builder matched means the scrape shape changed — never silently trust an empty manifest.
  if (ids.size === 0) die(`builder-manifest scrape of ${indexFile} found ZERO builders — the CLI's builder shape changed; update loadBuilderManifest before trusting injection_completeness (fail-closed)`);
  for (const id of EXPECTED_BUILDER_IDS) {
    if (!ids.has(id)) die(`builder-manifest scrape did not resolve expected builder "${id}" in ${indexFile} — a wrap builder was renamed/removed; update loadBuilderManifest and that profile's injection_completeness together (fail-closed)`);
  }
  return ids;
}
const BUILDER_MANIFEST = loadBuilderManifest();
const INJECTION_COMPLETENESS = new Set(["declarative", "builder-assisted", "code-only"]);
function injectionIsInert(p) {
  return p.injection && p.injection.method === "env" && p.injection.env && typeof p.injection.env === "object" && Object.keys(p.injection.env).length === 0;
}
// The tier the profile's own data + the CLI manifest imply. Returns null only when the
// manifest is unknown AND the injection is not inert (cannot decide).
function derivedCompleteness(p) {
  if (injectionIsInert(p)) return "code-only";
  if (BUILDER_MANIFEST === null) return null;
  return BUILDER_MANIFEST.has(p.id) ? "builder-assisted" : "declarative";
}
// A declared last_verified_at older than this many days fails closed (re-verify or drop).
const STALENESS_DAYS = 365;

// validate enforces the load-bearing invariants. It is intentionally small (no ajv
// dependency) but fails closed on exactly the things that would otherwise ship a
// silently-broken or guessed profile.
function validate(p, file) {
  const need = (cond, msg) => { if (!cond) die(`${file}: ${msg}`); };
  need(p && typeof p === "object", "profile is not an object");
  for (const key of Object.keys(p)) need(schemaProperties.has(key), `unknown top-level key "${key}"`);
  need(p.schema_version === "1", `schema_version must be "1"`);
  need(typeof p.id === "string" && /^[a-z0-9][a-z0-9-]*$/.test(p.id), "id must be kebab-case");
  need(!reservedVerbs.includes(p.id), `id "${p.id}" collides with a reserved command`);
  for (const k of ["display_name", "vendor", "homepage", "install"]) {
    need(typeof p[k] === "string" && p[k].length > 0, `${k} must be a non-empty string`);
  }
  need(Array.isArray(p.binary_names) && p.binary_names.length > 0 && p.binary_names.every((name) => typeof name === "string" && name.length > 0), "binary_names must be a non-empty string array");
  for (const name of p.binary_names) need(!reservedVerbs.includes(name), `binary name "${name}" collides with a reserved command`);
  need(WIRE_PROTOCOLS.has(p.wire_protocol), `unknown wire_protocol "${p.wire_protocol}" (fail-closed)`);
  const inj = p.injection;
  need(inj && typeof inj === "object", "injection must be an object");
  need(INJECTION_METHODS.has(inj.method), `unknown injection.method "${inj.method}" (fail-closed)`);
  if (inj.method === "env") {
    need(inj.env && typeof inj.env === "object" && !Array.isArray(inj.env), "injection.env must be an object");
    for (const [key, value] of Object.entries(inj.env)) {
      need(ENV_KEY_RE.test(key) && !loaderControlKey(key), `injection.env key "${key}" is not allowlisted`);
      need(typeof value === "string" && ENV_VALUE_RE.test(value), `injection.env.${key} must be one cave template token with an optional safe base-URL path, or a safe literal`);
      if (typeof value === "string" && value.startsWith("{{cave_base_url}}/")) {
        need(key.endsWith("_BASE_URL") || key.endsWith("_API_BASE"), `injection.env.${key} cannot append a path to cave_base_url`);
      }
    }
  } else if (inj.method === "config-env-content") {
    validateEnvVariableName(inj.env_var, need, "injection.env_var");
    need(inj.config_content && typeof inj.config_content === "object", "injection.config_content must be an object");
    need(inj.config_content.local && typeof inj.config_content.local === "object", "injection.config_content.local is required");
    validateConfigStrings(inj.config_content, need, "injection.config_content");
  } else if (inj.method === "config-file") {
    validateEnvVariableName(inj.env_var, need, "injection.env_var");
    if (inj.base_config !== undefined) {
      need(inj.base_config && typeof inj.base_config === "object" && !Array.isArray(inj.base_config), "injection.base_config must be an object");
      need(typeof inj.base_config.path === "string" && profilePathAllowed(inj.base_config.path, p.id), `injection.base_config.path must stay under ~/.${p.id}/ without ..`);
      if (inj.base_config.env_var !== undefined) validateEnvVariableName(inj.base_config.env_var, need, "injection.base_config.env_var");
      if (inj.base_config.state_dir !== undefined) {
        need(inj.base_config.state_dir && typeof inj.base_config.state_dir === "object" && !Array.isArray(inj.base_config.state_dir), "injection.base_config.state_dir must be an object");
        validateEnvVariableName(inj.base_config.state_dir.env_var, need, "injection.base_config.state_dir.env_var");
        need(typeof inj.base_config.state_dir.filename === "string" && inj.base_config.state_dir.filename.length > 0, "injection.base_config.state_dir.filename must be a non-empty string");
      }
    }
    need(inj.config_overlay && typeof inj.config_overlay === "object" && !Array.isArray(inj.config_overlay), "injection.config_overlay must be an object");
    need(Object.prototype.hasOwnProperty.call(inj.config_overlay, "local"), "injection.config_overlay.local is required");
    validateConfigStrings(inj.config_overlay, need, "injection.config_overlay");
  }
  // command_hook is optional, but if present its method must be one we can honor —
  // an unknown method fails the build rather than claim a hook we'd silently no-op.
  if (p.command_hook !== undefined) {
    const ch = p.command_hook;
    need(ch && typeof ch === "object", "command_hook must be an object");
    need(COMMAND_HOOK_METHODS.has(ch.method), `unknown command_hook.method "${ch.method}" (fail-closed)`);
    if (ch.method === "instruction-note" || ch.method === "codex-pretooluse") {
      need(typeof ch.file === "string" && profilePathAllowed(ch.file, p.id), `command_hook.file must stay under ~/.${p.id}/ without ..`);
    }
  }
  // memory_hook is optional (opt-in auto-recall); if present its method must be one
  // we can honor — an unknown method fails the build rather than claim a hook.
  if (p.memory_hook !== undefined) {
    const mh = p.memory_hook;
    need(mh && typeof mh === "object", "memory_hook must be an object");
    need(MEMORY_HOOK_METHODS.has(mh.method), `unknown memory_hook.method "${mh.method}" (fail-closed)`);
  }
  // skills is optional (the agent's on-disk skill surface for `caveman convert`);
  // if present its format must be one we can parse — unknown fails the build.
  if (p.skills !== undefined) {
    const sk = p.skills;
    need(sk && typeof sk === "object", "skills must be an object");
    need(SKILL_FORMATS.has(sk.format), `unknown skills.format "${sk.format}" (fail-closed)`);
    need(Array.isArray(sk.user_dirs) && sk.user_dirs.length > 0 && sk.user_dirs.every((d) => typeof d === "string" && d.length > 0), "skills.user_dirs must be a non-empty string array");
    if (sk.project_dirs !== undefined) {
      need(Array.isArray(sk.project_dirs) && sk.project_dirs.every((d) => typeof d === "string" && d.length > 0), "skills.project_dirs must be a string array");
    }
  }
  // Every model id pinned in the injection config must be priced by the provider catalog
  // (issue #136): an unpriced pin under-reports the operator's spend. Fail closed.
  const pinnedModels = new Set();
  collectInjectionModelIds(p.injection, pinnedModels);
  for (const id of pinnedModels) {
    gate(CATALOG_MODEL_IDS.has(id), `${file}: injection pins model "${id}" which is not priced in the provider catalog (fail-closed: no unpriced model in a shipped profile)`);
  }
  // injection_completeness is REQUIRED for every profile and must match the tier the CLI's
  // real routing implies (issue #135) — a profile cannot OMIT it (an omitted tier on a
  // builder-routed agent would silently escape the gate), cannot claim `declarative` while
  // a builder handles it, nor `declarative` while its declared injection is inert.
  gate(p.injection_completeness !== undefined, `${file}: injection_completeness is required for every profile (declarative | builder-assisted | code-only)`);
  if (p.injection_completeness !== undefined) {
    gate(INJECTION_COMPLETENESS.has(p.injection_completeness), `${file}: unknown injection_completeness "${p.injection_completeness}" (fail-closed)`);
    if (injectionIsInert(p)) {
      gate(p.injection_completeness === "code-only", `${file}: injection_completeness must be "code-only": injection.env is empty, so all routing is code (got "${p.injection_completeness}")`);
    } else if (BUILDER_MANIFEST !== null) {
      if (BUILDER_MANIFEST.has(p.id)) {
        gate(p.injection_completeness !== "declarative", `${file}: injection_completeness cannot be "declarative": the CLI has a code builder for "${p.id}" (declare "builder-assisted" or "code-only")`);
      } else {
        gate(p.injection_completeness === "declarative", `${file}: injection_completeness must be "declarative": the CLI has no builder for "${p.id}" (got "${p.injection_completeness}")`);
      }
    }
  }
  // last_verified_at / verified_by (optional): a claimed verification must be well-formed
  // and inside the staleness budget — an expired claim fails closed (re-verify or drop it).
  if (p.last_verified_at !== undefined || p.verified_by !== undefined) {
    need(typeof p.last_verified_at === "string" && /^\d{4}-\d{2}-\d{2}/.test(p.last_verified_at), "last_verified_at must be an ISO date (YYYY-MM-DD…) when verified_by is set");
    need(typeof p.verified_by === "string" && p.verified_by.length > 0, "verified_by must be a non-empty string accompanying last_verified_at");
    const when = Date.parse(p.last_verified_at);
    need(!Number.isNaN(when), "last_verified_at must be a parseable date");
    const ageDays = (Date.now() - when) / 86_400_000;
    need(ageDays <= STALENESS_DAYS, `last_verified_at is ${Math.floor(ageDays)}d old, over the ${STALENESS_DAYS}d staleness budget — re-verify and bump it`);
  }
}

// checkConformancePins derives every agent-conformance matrix pin from the profile's
// tested_agent_version so the two cannot silently diverge (issue #135). The workflow is
// absent in the published CLI-only layout, so skip it there rather than fail.
function checkConformancePins(agentsById) {
  const wf = process.env.CAVEMAN_CONFORMANCE_WORKFLOW
    ? resolve(process.env.CAVEMAN_CONFORMANCE_WORKFLOW)
    : join(here, "..", "..", ".github", "workflows", "agent-conformance.yml");
  if (!existsSync(wf)) return;
  const text = readFileSync(wf, "utf8");
  const entryRe = /- id:\s*(\S+)\s*\r?\n\s*install:\s*([^\r\n]+)/g;
  let m;
  while ((m = entryRe.exec(text)) !== null) {
    const id = m[1].trim();
    const install = m[2];
    const profile = agentsById.get(id);
    if (!profile || !profile.tested_agent_version || profile.tested_agent_version === "x") continue;
    const pv = profile.tested_agent_version;
    if (!/^\d+\.\d+\.\d+/.test(pv)) continue; // non-semver pin (e.g. a git sha) — not derivable
    const iv = (install.match(/@(\d+\.\d+\.\d+[0-9A-Za-z.-]*)/) || install.match(/==(\d+\.\d+\.\d+[0-9A-Za-z.-]*)/))?.[1];
    if (!iv) continue;
    if (iv !== pv) gate(false, `.github/workflows/agent-conformance.yml pins ${id}@${iv} but profile tested_agent_version is ${pv} — the CI pin must equal the profile pin (issue #135)`);
  }
}

const checkProfileAt = process.argv.indexOf("--check-profile");
if (checkProfileAt !== -1) {
  const file = process.argv[checkProfileAt + 1];
  if (!file) die("--check-profile requires a JSON file");
  const parsed = readJSON(file, file);
  validate(parsed, file);
  console.error(`agent profile valid: ${parsed.id}`);
  process.exit(0);
}

const files = readdirSync(profilesDir).filter((f) => f.endsWith(".json") && f !== "schema.json").sort();
const agents = [];
const seen = new Set();
for (const f of files) {
  let parsed;
  try {
    parsed = JSON.parse(readFileSync(join(profilesDir, f), "utf8"));
  } catch (e) {
    die(`${f}: invalid JSON — ${e.message}`);
  }
  validate(parsed, f);
  if (seen.has(parsed.id)) die(`${f}: duplicate id "${parsed.id}"`);
  seen.add(parsed.id);
  if (!Array.isArray(parsed.args)) parsed.args = [];
  agents.push(parsed);
}
agents.sort((a, b) => a.id.localeCompare(b.id));

// The CI upstream-binary matrix pins must equal the profile pins they claim to test.
checkConformancePins(new Map(agents.map((a) => [a.id, a])));

// agents.json — the published registry.
const registry = { schema_version: "1", agents };
writeFileSync(join(here, "agents.json"), JSON.stringify(registry, null, 2) + "\n");

// agents.generated.ts — the CLI's embedded, typed copy. The type preamble is
// static; only the data array changes with the profiles.
const PREAMBLE = `// GENERATED by agents/compile.mjs from agents/profiles/*.json — DO NOT EDIT.
// Run \`node scripts/compile-registries.mjs\` (wired into the CLI build/test) to regenerate.

export type WireProtocol = "anthropic-messages" | "openai-chat" | "openai-responses" | "gemini-generatecontent";

export type Injection =
  | { method: "env"; env: Record<string, string> }
  | { method: "config-env-content"; env_var: string; config_content: { local: unknown; managed?: unknown } }
  | { method: "config-file"; env_var: string; base_config?: { path: string; env_var?: string; state_dir?: { env_var: string; filename: string } }; config_overlay: { local: unknown; managed?: unknown } };

export type CommandHook =
  | { method: "claude-pretooluse" }
  | { method: "codex-pretooluse"; file: string }
  | { method: "gemini-beforetool" }
  | { method: "opencode-plugin" }
  | { method: "hermes-plugin" }
  | { method: "openclaw-plugin" }
  | { method: "instruction-note"; file: string };

export type MemoryHook =
  | { method: "claude-userpromptsubmit" };

export type SkillsSurface = { format: "skill-md"; user_dirs: string[]; project_dirs?: string[] };

export interface AgentProfile {
  schema_version: string;
  id: string;
  display_name: string;
  vendor: string;
  homepage: string;
  binary_names: string[];
  args: string[];
  install: string;
  wire_protocol: WireProtocol;
  injection: Injection;
  command_hook?: CommandHook;
  memory_hook?: MemoryHook;
  skills?: SkillsSurface;
  attribution?: { header?: string };
  tested_agent_version?: string;
  injection_completeness?: "declarative" | "builder-assisted" | "code-only";
  last_verified_at?: string;
  verified_by?: string;
  fallback?: string;
  maintainer?: string | null;
}

export const PROFILES: AgentProfile[] = `;

writeFileSync(join(cliDir, "src", "agents.generated.ts"), PREAMBLE + JSON.stringify(agents, null, 2) + ";\n");

const RESERVED_PREAMBLE = `// GENERATED by agents/compile.mjs from agents/reserved-verbs.json — DO NOT EDIT.
// Run \`node scripts/compile-registries.mjs\` (wired into CLI build/test) to regenerate.

export const RESERVED_VERBS = new Set<string>(`;
writeFileSync(
  join(cliDir, "src", "reserved-verbs.generated.ts"),
  RESERVED_PREAMBLE + JSON.stringify(reservedVerbs, null, 2) + ");\n",
);

console.error(`compiled ${agents.length} agent profile(s): ${agents.map((a) => a.id).join(", ")}`);
