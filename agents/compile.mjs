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
const profilesDir = join(here, "profiles");
const schemaFile = join(profilesDir, "schema.json");
const reservedFile = join(here, "reserved-verbs.json");

const WIRE_PROTOCOLS = new Set(["anthropic-messages", "openai-chat", "openai-responses", "gemini-generatecontent"]);
const INJECTION_METHODS = new Set(["env", "config-env-content", "config-file"]);
const COMMAND_HOOK_METHODS = new Set(["claude-pretooluse", "gemini-beforetool", "opencode-plugin", "hermes-plugin", "openclaw-plugin", "instruction-note"]);
const MEMORY_HOOK_METHODS = new Set(["claude-userpromptsubmit"]);
const SKILL_FORMATS = new Set(["skill-md"]);
const ENV_KEY_PATTERN = "^[A-Z][A-Z0-9_]*_(BASE_URL|API_BASE|API_KEY|AUTH_TOKEN|HOST)$";
const ENV_VALUE_PATTERN = "^(?:\\{\\{cave_(?:base_url|proxy_url|api_key|org_id)\\}\\}|[A-Za-z0-9._-]+)$";
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
      need(typeof value === "string" && ENV_VALUE_RE.test(value), `injection.env.${key} must be one cave template token or a safe literal`);
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
    if (ch.method === "instruction-note") {
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

// agents.json — the published registry.
const registry = { schema_version: "1", agents };
writeFileSync(join(here, "agents.json"), JSON.stringify(registry, null, 2) + "\n");

// agents.generated.ts — the CLI's embedded, typed copy. The type preamble is
// static; only the data array changes with the profiles.
const PREAMBLE = `// GENERATED by public/agents/compile.mjs from public/agents/profiles/*.json — DO NOT EDIT.
// Run \`node scripts/compile-registries.mjs\` (wired into the CLI build/test) to regenerate.

export type WireProtocol = "anthropic-messages" | "openai-chat" | "openai-responses" | "gemini-generatecontent";

export type Injection =
  | { method: "env"; env: Record<string, string> }
  | { method: "config-env-content"; env_var: string; config_content: { local: unknown; managed?: unknown } }
  | { method: "config-file"; env_var: string; base_config?: { path: string; env_var?: string; state_dir?: { env_var: string; filename: string } }; config_overlay: { local: unknown; managed?: unknown } };

export type CommandHook =
  | { method: "claude-pretooluse" }
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
  fallback?: string;
  maintainer?: string | null;
}

export const PROFILES: AgentProfile[] = `;

writeFileSync(join(cliDir, "src", "agents.generated.ts"), PREAMBLE + JSON.stringify(agents, null, 2) + ";\n");

const RESERVED_PREAMBLE = `// GENERATED by public/agents/compile.mjs from public/agents/reserved-verbs.json — DO NOT EDIT.
// Run \`node scripts/compile-registries.mjs\` (wired into CLI build/test) to regenerate.

export const RESERVED_VERBS = new Set<string>(`;
writeFileSync(
  join(cliDir, "src", "reserved-verbs.generated.ts"),
  RESERVED_PREAMBLE + JSON.stringify(reservedVerbs, null, 2) + ");\n",
);

console.error(`compiled ${agents.length} agent profile(s): ${agents.map((a) => a.id).join(", ")}`);
