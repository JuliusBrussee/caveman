// compile.mjs — validates canonical agent skills and generates delivery embeds.
//
// Every skill body lives once at skills/<id>/SKILL.md. registry.json
// decides which delivery surfaces receive it and which install suites include
// it. The CLI and web import generated TypeScript; neither hand-copies prompts.

import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { loadSurfaces, scanSkillBody } from "./verbs-gate.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const BANNED = ["TO" + "DO", "FIX" + "ME", "not " + "implemented", "not-" + "implemented"];
const DELIVERIES = new Set(["cli", "web", "native"]);
const REGISTRY_KEYS = new Set(["schema_version", "native_pack", "preserved_skill_ids", "skills", "suites"]);
const SKILL_KEYS = new Set([
  "id", "summary", "delivery", "suites", "activation", "task_types", "evidence_status",
  "prompt_byte_budget", "conflicts", "precedence", "guardrails", "entry_condition", "stop_condition",
]);
const NATIVE_PACK_KEYS = new Set(["id", "version", "protocol", "core_source", "core_prompt_token_budget", "targets"]);
const NATIVE_TARGETS = new Set(["claude", "codex", "hermes", "gemini", "opencode", "aider"]);
const NATIVE_ACTIVATION = {
  claude: { core: "SessionStart", task: "UserPromptSubmit" },
  codex: { core: "developer_instructions+SessionStart", task: "UserPromptSubmit" },
  hermes: { core: "pre_llm_call", task: "pre_llm_call" },
  gemini: { core: "BeforeAgent", task: "BeforeAgent" },
  opencode: { core: "experimental.chat.system.transform", task: "chat.message" },
  aider: { core: "read_only_conventions", task: "native_repository_map_authoritative" },
};

function die(message) {
  console.error(`agent-skill compile failed: ${message}`);
  process.exit(1);
}

function object(value, label) {
  if (!value || typeof value !== "object" || Array.isArray(value)) die(`${label} must be an object`);
  return value;
}

function exactKeys(value, allowed, label) {
  const unknown = Object.keys(value).filter((key) => !allowed.has(key));
  if (unknown.length > 0) die(`${label} has unknown keys: ${unknown.sort().join(", ")}`);
}

const registry = object(JSON.parse(readFileSync(join(here, "registry.json"), "utf8")), "registry");
exactKeys(registry, REGISTRY_KEYS, "registry");
if (registry.schema_version !== "2") die(`unsupported schema_version ${JSON.stringify(registry.schema_version)}`);
if (!Array.isArray(registry.skills) || registry.skills.length === 0) die("skills must be a non-empty array");
const suites = object(registry.suites, "suites");
if (!Array.isArray(registry.preserved_skill_ids) || registry.preserved_skill_ids.some((id) => typeof id !== "string" || !/^[a-z0-9-]+$/.test(id))) {
  die("preserved_skill_ids must be skill ids");
}
const preservedSkillIDs = new Set(registry.preserved_skill_ids);
if (preservedSkillIDs.size !== registry.preserved_skill_ids.length) die("preserved_skill_ids contains duplicates");
const nativePackMeta = object(registry.native_pack, "native_pack");
exactKeys(nativePackMeta, NATIVE_PACK_KEYS, "native_pack");
if (typeof nativePackMeta.id !== "string" || !/^[a-z0-9-]+$/.test(nativePackMeta.id)) die("native_pack.id invalid");
if (typeof nativePackMeta.version !== "string" || !/^\d+\.\d+\.\d+$/.test(nativePackMeta.version)) die("native_pack.version invalid");
if (!Number.isInteger(nativePackMeta.protocol) || nativePackMeta.protocol < 1) die("native_pack.protocol invalid");
if (typeof nativePackMeta.core_source !== "string" || nativePackMeta.core_source.includes("..") || nativePackMeta.core_source.includes("/")) die("native_pack.core_source invalid");
if (!Number.isInteger(nativePackMeta.core_prompt_token_budget) || nativePackMeta.core_prompt_token_budget < 1) die("native_pack.core_prompt_token_budget invalid");
if (!Array.isArray(nativePackMeta.targets) || nativePackMeta.targets.length === 0 || nativePackMeta.targets.some((target) => !NATIVE_TARGETS.has(target))) die("native_pack.targets invalid");
if (new Set(nativePackMeta.targets).size !== nativePackMeta.targets.length) die("native_pack.targets contains duplicates");
const corePath = join(here, nativePackMeta.core_source);
if (!existsSync(corePath)) die(`native Core source missing: ${nativePackMeta.core_source}`);
const nativeCore = readFileSync(corePath, "utf8").trim();
const coreEstimatedTokens = Math.ceil(Buffer.byteLength(nativeCore) / 3);
if (coreEstimatedTokens > nativePackMeta.core_prompt_token_budget) {
  die(`native Core exceeds conservative token estimate: ${coreEstimatedTokens} > ${nativePackMeta.core_prompt_token_budget}`);
}

const canonicalIDs = readdirSync(here, { withFileTypes: true })
  .filter((entry) => entry.isDirectory() && existsSync(join(here, entry.name, "SKILL.md")))
  .map((entry) => entry.name)
  .sort();
const seen = new Set();
const skills = [];

for (const raw of registry.skills) {
  const meta = object(raw, "skill entry");
  exactKeys(meta, SKILL_KEYS, `skill ${JSON.stringify(meta.id)}`);
  if (typeof meta.id !== "string" || !/^[a-z0-9-]+$/.test(meta.id)) die(`invalid skill id ${JSON.stringify(meta.id)}`);
  if (seen.has(meta.id)) die(`duplicate skill id ${meta.id}`);
  seen.add(meta.id);
  if (typeof meta.summary !== "string" || meta.summary.trim() === "") die(`${meta.id}: summary required`);
  if (!Array.isArray(meta.delivery) || meta.delivery.length === 0) die(`${meta.id}: delivery required`);
  if (meta.delivery.some((value) => !DELIVERIES.has(value))) die(`${meta.id}: unknown delivery`);
  if (!Array.isArray(meta.suites) || meta.suites.some((value) => typeof value !== "string")) die(`${meta.id}: suites must be strings`);

	const native = meta.delivery.includes("native");
	const nativeKeys = ["activation", "task_types", "evidence_status", "prompt_byte_budget", "conflicts", "precedence", "guardrails", "entry_condition", "stop_condition"];
	if (native) {
		if (meta.activation !== "classified") die(`${meta.id}: native activation must be classified`);
		if (!Array.isArray(meta.task_types) || meta.task_types.length === 0 || meta.task_types.some((value) => typeof value !== "string" || !/^[a-z0-9-]+$/.test(value))) die(`${meta.id}: native task_types invalid`);
		if (typeof meta.evidence_status !== "string" || meta.evidence_status.trim() === "") die(`${meta.id}: native evidence_status required`);
		if (!Number.isInteger(meta.prompt_byte_budget) || meta.prompt_byte_budget < 1) die(`${meta.id}: native prompt_byte_budget invalid`);
		if (!Array.isArray(meta.conflicts) || meta.conflicts.some((value) => typeof value !== "string")) die(`${meta.id}: native conflicts invalid`);
		if (!Number.isInteger(meta.precedence) || meta.precedence < 0) die(`${meta.id}: native precedence invalid`);
		if (!Array.isArray(meta.guardrails) || meta.guardrails.length === 0 || meta.guardrails.some((value) => typeof value !== "string")) die(`${meta.id}: native guardrails invalid`);
		if (typeof meta.entry_condition !== "string" || typeof meta.stop_condition !== "string") die(`${meta.id}: native entry/stop condition required`);
	} else if (nativeKeys.some((key) => Object.hasOwn(meta, key))) {
		die(`${meta.id}: native metadata requires native delivery`);
	}

  const file = join(here, meta.id, "SKILL.md");
  if (!existsSync(file)) die(`${meta.id}/SKILL.md missing`);
  const body = readFileSync(file, "utf8");
  if (!body.startsWith("---\n")) die(`${meta.id}: SKILL.md must open with YAML frontmatter`);
  if (!body.includes(`name: ${meta.id}`)) die(`${meta.id}: frontmatter name must match directory`);
  for (const marker of BANNED) {
    if (body.includes(marker)) die(`${meta.id}: contains banned marker ${JSON.stringify(marker)}`);
  }
	const frontmatterEnd = body.indexOf("\n---\n", 4);
	if (frontmatterEnd < 0) die(`${meta.id}: SKILL.md frontmatter is not closed`);
	const instructions = body.slice(frontmatterEnd + 5).trim();
	if (native && Buffer.byteLength(instructions) > meta.prompt_byte_budget) {
		die(`${meta.id}: instruction bytes exceed budget (${Buffer.byteLength(instructions)} > ${meta.prompt_byte_budget})`);
	}
  skills.push({ ...meta, body, instructions });
}

skills.sort((left, right) => left.id.localeCompare(right.id));
const registryIDs = skills.map((skill) => skill.id);
const registered = new Set(registryIDs);
const missingRegistryIDs = registryIDs.filter((id) => !canonicalIDs.includes(id));
const unexpectedCanonicalIDs = canonicalIDs.filter((id) => !registered.has(id) && !preservedSkillIDs.has(id));
const overlappingPreservedIDs = registryIDs.filter((id) => preservedSkillIDs.has(id));
if (missingRegistryIDs.length > 0 || unexpectedCanonicalIDs.length > 0 || overlappingPreservedIDs.length > 0) {
  die(`registry ids do not match canonical skill dirs (missing: ${missingRegistryIDs.join(", ") || "none"}; unexpected: ${unexpectedCanonicalIDs.join(", ") || "none"}; registered-and-preserved: ${overlappingPreservedIDs.join(", ") || "none"})`);
}

for (const [suite, ids] of Object.entries(suites)) {
  if (!/^[a-z0-9-]+$/.test(suite) || !Array.isArray(ids) || ids.length === 0) die(`invalid suite ${JSON.stringify(suite)}`);
  for (const id of ids) {
    const skill = skills.find((entry) => entry.id === id);
    if (!skill) die(`suite ${suite} references unknown skill ${JSON.stringify(id)}`);
    if (!skill.suites.includes(suite)) die(`${id}: suite ${suite} missing from skill metadata`);
    if (!skill.delivery.includes("cli")) die(`${id}: suite skills must be CLI-deliverable`);
  }
}
for (const skill of skills) {
  for (const suite of skill.suites) {
    if (!Array.isArray(suites[suite]) || !suites[suite].includes(skill.id)) {
      die(`${skill.id}: suite ${suite} does not include the skill`);
    }
  }
}

function generatedBody(selected, destination) {
  const bodies = Object.fromEntries(selected.map((skill) => [skill.id, skill.body]));
  const metadata = selected.map(({ id, summary, delivery, suites: skillSuites }) => ({
    id,
    summary,
    delivery,
    suites: skillSuites,
  }));
  const selectedIDs = new Set(selected.map((skill) => skill.id));
  const selectedSuites = Object.fromEntries(
    Object.entries(suites)
      .map(([suite, ids]) => [suite, ids.filter((id) => selectedIDs.has(id))])
      .filter(([, ids]) => ids.length > 0),
  );
  return `// GENERATED by skills/compile.mjs from skills/*/SKILL.md + registry.json — DO NOT EDIT.
// Destination: ${destination}.

export type AgentSkillMetadata = {
  id: string;
  summary: string;
  delivery: string[];
  suites: string[];
};

export const AGENT_SKILLS: Record<string, string> = ${JSON.stringify(bodies, null, 2)};
export const AGENT_SKILL_METADATA: AgentSkillMetadata[] = ${JSON.stringify(metadata, null, 2)};
export const AGENT_SKILL_SUITES: Record<string, string[]> = ${JSON.stringify(selectedSuites, null, 2)};
`;
}

const nativeSkills = skills.filter((skill) => skill.delivery.includes("native"));
const taskOwners = new Map();
for (const skill of nativeSkills) {
  for (const taskType of skill.task_types) {
		const owners = taskOwners.get(taskType) ?? [];
		owners.push(skill);
		taskOwners.set(taskType, owners);
  }
  for (const conflict of skill.conflicts) {
    if (!nativeSkills.some((candidate) => candidate.id === conflict)) die(`${skill.id}: conflict references unknown native skill ${conflict}`);
  }
}
for (const [taskType, owners] of taskOwners) {
	for (let left = 0; left < owners.length; left++) {
		for (let right = left + 1; right < owners.length; right++) {
			const a = owners[left];
			const b = owners[right];
			if (!a.conflicts.includes(b.id) || !b.conflicts.includes(a.id)) die(`native task type ${taskType}: ${a.id} and ${b.id} must declare mutual conflict`);
			if (a.precedence === b.precedence) die(`native task type ${taskType}: ${a.id} and ${b.id} need distinct precedence`);
		}
	}
}

const compiledNativePack = {
  schema: "caveman.native-pack.v1",
  id: nativePackMeta.id,
  version: nativePackMeta.version,
  protocol: nativePackMeta.protocol,
  core: {
    source: nativePackMeta.core_source,
    mandatory: true,
    prompt_token_budget: nativePackMeta.core_prompt_token_budget,
    estimated_tokens: coreEstimatedTokens,
    estimate_basis: "ceil_utf8_bytes_divided_by_3",
    instructions: nativeCore,
  },
  targets: Object.fromEntries(nativePackMeta.targets.map((target) => [target, NATIVE_ACTIVATION[target]])),
  skills: nativeSkills.map(({ id, summary, activation, task_types, evidence_status, prompt_byte_budget, conflicts, precedence, guardrails, entry_condition, stop_condition, instructions }) => ({
    id, summary, activation, task_types, evidence_status, prompt_byte_budget, conflicts, precedence,
    guardrails, entry_condition, stop_condition, instructions,
  })),
};

function nativeTypeScript() {
  const instructions = Object.fromEntries(compiledNativePack.skills.map((skill) => [skill.id, skill.instructions]));
  return `// GENERATED by skills/compile.mjs from native-core.md + native SKILL.md files — DO NOT EDIT.
export const NATIVE_PACK = ${JSON.stringify(compiledNativePack, null, 2)} as const;
export const NATIVE_CORE = ${JSON.stringify(nativeCore)};
export const NATIVE_SKILL_INSTRUCTIONS: Record<string, string> = ${JSON.stringify(instructions, null, 2)};
`;
}

const cliCandidates = [
  process.env.CAVEMAN_CLI_DIR ? resolve(process.env.CAVEMAN_CLI_DIR) : "",
  join(here, "..", "cli"),
  join(here, "..", "packages", "cli"),
].filter(Boolean);
const cliDir = cliCandidates.find((candidate) => existsSync(join(candidate, "src")));
if (!cliDir) die("CLI source directory not found");

// skills-verbs gate: a skill can never document a CLI command, MCP tool, or SDK
// call the shipped surfaces do not expose. Runs before any generated write, so a
// drift fails the build closed rather than embedding a broken instruction.
const surfaces = loadSurfaces({ skillsDir: here, cliDir });
if (surfaces.errors.length > 0) {
  die(`skills-verbs gate could not build the command surface (refusing to pass open): ${surfaces.errors.join("; ")}`);
}
const surfaceViolations = skills.flatMap((skill) => scanSkillBody(skill.id, skill.body, surfaces));
if (surfaceViolations.length > 0) {
  die(`skill references a surface that does not exist:\n  ${surfaceViolations.join("\n  ")}`);
}

const cliSkills = skills.filter((skill) => skill.delivery.includes("cli"));
if (cliDir) {
  writeFileSync(join(cliDir, "src", "agent-skills.generated.ts"), generatedBody(cliSkills, "CLI"));
	writeFileSync(join(cliDir, "src", "native-pack.generated.ts"), nativeTypeScript());
  console.error(`compiled ${cliSkills.length} agent skill(s) into ${join(cliDir, "src", "agent-skills.generated.ts")}`);
} else {
  die("CLI source directory not found");
}

const nativePackJSON = JSON.stringify(compiledNativePack, null, 2) + "\n";
const nativeRuntimeDir = join(here, "..", "proxy", "internal", "nativepack");
mkdirSync(nativeRuntimeDir, { recursive: true });
writeFileSync(join(nativeRuntimeDir, "native-pack.generated.json"), nativePackJSON);
const generatedRoot = join(here, "generated");
for (const target of nativePackMeta.targets) {
	const targetDir = join(generatedRoot, target);
	mkdirSync(targetDir, { recursive: true });
	writeFileSync(join(targetDir, "pack.json"), JSON.stringify({ ...compiledNativePack, target, activation: NATIVE_ACTIVATION[target] }, null, 2) + "\n");
}
console.error(`compiled ${nativeSkills.length} native skill(s) for ${nativePackMeta.targets.length} host target(s)`);

const webDir = join(here, "..", "..", "cloud", "web", "lib");
const webSkills = skills.filter((skill) => skill.delivery.includes("web"));
if (existsSync(webDir)) {
  writeFileSync(join(webDir, "agent-skills.generated.ts"), generatedBody(webSkills, "web"));
  console.error(`compiled ${webSkills.length} agent skill(s) into the web embed target`);
} else {
  console.error(`web embed target not present — skipped (${webSkills.length} skill(s) validated)`);
}
