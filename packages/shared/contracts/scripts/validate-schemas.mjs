import { readdir, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import Ajv2020 from "ajv/dist/2020.js";

const packageRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const schemaRoot = path.join(packageRoot, "schemas");
const files = (await readdir(schemaRoot))
  .filter((name) => name.endsWith(".schema.json"))
  .sort();

if (files.length === 0) {
  throw new Error("no JSON schemas found");
}

// $ids pin the immutable release tag of this package version, so a version bump
// that forgets them fails here. Cross-schema $refs stay relative so tools that
// load from the package directory resolve locally instead of fetching.
const { version } = JSON.parse(await readFile(path.join(packageRoot, "package.json"), "utf8"));
const SCHEMA_BASE = `https://raw.githubusercontent.com/JuliusBrussee/caveman/contracts-v${version}/packages/shared/contracts/schemas/`;
const schemas = [];
for (const file of files) {
  const schema = JSON.parse(await readFile(path.join(schemaRoot, file), "utf8"));
  if (schema.$schema !== "https://json-schema.org/draft/2020-12/schema") {
    throw new Error(`${file}: unsupported or missing $schema`);
  }
  if (schema.$id !== SCHEMA_BASE + file) throw new Error(`${file}: $id must be ${SCHEMA_BASE + file}`);
  (function walk(node) {
    if (!node || typeof node !== "object") return;
    if (typeof node.$ref === "string" && /^[a-z][a-z0-9+.-]*:/i.test(node.$ref)) {
      throw new Error(`${file}: $ref ${node.$ref} must be relative`);
    }
    for (const value of Object.values(node)) walk(value);
  })(schema);
  schemas.push(schema);
}

const ajv = new Ajv2020({ allErrors: true, strict: true });
ajv.addSchema(schemas);
for (const schema of schemas) {
  const validate = ajv.getSchema(schema.$id);
  if (!validate) throw new Error(`schema failed to compile: ${schema.$id}`);
}

const agentFixtureRoot = path.join(packageRoot, "fixtures", "agent");
const fixtureFiles = (await readdir(agentFixtureRoot))
  .filter((name) => name.endsWith(".json"))
  .sort();

if (fixtureFiles.length !== 2) {
  throw new Error(`expected exactly 2 agent conformance fixtures, found ${fixtureFiles.length}`);
}

const fixtures = await Promise.all(
  fixtureFiles.map(async (file) => JSON.parse(await readFile(path.join(agentFixtureRoot, file), "utf8"))),
);
const validateAdapterConformance = ajv.getSchema(`${SCHEMA_BASE}adapter-conformance.schema.json`);
if (!validateAdapterConformance) {
  throw new Error("adapter conformance schema failed to compile");
}
for (const [index, fixture] of fixtures.entries()) {
  if (!validateAdapterConformance(fixture)) {
    throw new Error(
      `${fixtureFiles[index]}: ${ajv.errorsText(validateAdapterConformance.errors)}`,
    );
  }
}
const [claude, pi] = fixtures;
if (claude.harness !== "claude" || pi.harness !== "pi") {
  throw new Error("agent conformance fixtures must cover claude and pi");
}

for (const key of [
  "normalized_context_digest",
  "plan_sha256",
  "ordered_transform_ids",
  "provider_visible_digest",
  "recovery_handles",
  "accounting_method",
  "failure_fallback",
]) {
  if (JSON.stringify(claude[key]) !== JSON.stringify(pi[key])) {
    throw new Error(`agent conformance mismatch: ${key}`);
  }
}
if (claude.build_sha256 === pi.build_sha256) {
  throw new Error("adapter-specific build_sha256 values must differ");
}
if (claude.failure_fallback !== "original") {
  throw new Error("unknown adapter failure must preserve original bytes");
}

function check(schemaName, value, label) {
  const validate = ajv.getSchema(`${SCHEMA_BASE}middleware-${schemaName}.schema.json`);
  if (!validate?.(value)) throw new Error(`middleware ${label}: ${ajv.errorsText(validate?.errors)}`);
}

const parity = path.join(packageRoot, "..", "..", "sdk", "parity");
const middleware = JSON.parse(await readFile(path.join(parity, "middleware.fixtures.json"), "utf8"));
for (const [field, schemaName] of Object.entries({ capabilities: "capabilities", request: "optimize", plan: "plan", page: "page" })) {
  check(schemaName, middleware[field], field);
}

// Protocol 1.1 examples. Every error code in the catalog must produce a valid envelope.
const v11 = JSON.parse(await readFile(path.join(parity, "middleware-v1_1.fixtures.json"), "utf8"));
const examples = v11.examples;
check("capabilities", examples.capabilities_v1_1, "capabilities_v1_1");
for (const envelope of examples.error_envelopes) check("error", envelope, `error ${envelope.error.code}`);
if (examples.error_envelopes.length !== v11.server_error_codes.length) throw new Error("error envelope examples must cover server_error_codes");
check("session-delete", examples.session_delete_request, "session_delete_request");
for (const response of examples.session_delete_responses) check("session-delete-response", response, "session_delete_response");
check("receipt-response", examples.receipt_response, "receipt_response");
for (const event of examples.decision_events) check("decision-event", event, `decision_event ${event.reason}`);
for (const [reason] of Object.entries(v11.reason_catalog)) {
  if (!/^[a-z][a-z0-9_]{0,63}$/.test(reason)) throw new Error(`reason_catalog: ${reason} violates the reason grammar`);
}

// Route ask: the ask is optional and strict, its text required and non-blank.
const validateRouteAsk = ajv.getSchema(`${SCHEMA_BASE}route-ask-v1.schema.json`);
const routeAsk = (ask) => ({
  models: ["claude-opus-5-5", "claude-sonnet-5-5"],
  signals: { agent: "claude", context_tokens: 1200, tools_declared: 3, tool_errors: 0, images: false },
  ...(ask && { ask }),
});
for (const [ask, valid] of [
  [undefined, true],
  [{ text: "fix the login bug" }, true],
  [{ text: "now add a test", prev_text: "fix the login bug", reply_tail: "Fixed.", turn: 1 }, true],
  [{ text: " \n " }, false],
  [{ prev_text: "fix the login bug" }, false],
  [{ text: "fix it", turn: 1000001 }, false],
  [{ text: "fix it", model_hint: "x" }, false],
]) {
  if (validateRouteAsk(routeAsk(ask)) !== valid) throw new Error(`route-ask-v1: ${JSON.stringify(ask)} should be ${valid ? "valid" : "invalid"}`);
}
// What the request declares, what the session's previous request ran, and Cloud's state.
const request = { endpoint: "messages", effort: "high", thinking: "adaptive", per_message_off: false };
const last = { model: "claude-opus-5-5", effort: "low", age_s: 42, input_tokens: 52000, cache_read_tokens: 50000, cache_write_tokens: 1200, compacted: false };
for (const [extra, valid] of [
  [{ request }, true],
  [{ request: { ...request, labels: { "x-claude-code-agent-id": "a1" }, tool_names: ["Read", "Bash"] }, last, state: "opaque", parent_state: "parent" }, true],
  [{ request: { ...request, labels: { "thread-id": "t2", "x-codex-parent-thread-id": "t1", "x-codex-turn-metadata": "m".repeat(16384) } } }, true],
  [{ request: { ...request, labels: { "x-codex-turn-metadata": "m".repeat(16385) } } }, false],
  [{ request: { ...request, labels: { "x-unknown-label": "v" } } }, false],
  [{ request: { ...request, effort: "adaptive" } }, false],
  [{ request: { ...request, endpoint: "chat", effort: "", thinking: "" } }, true],
  [{ request, parent_state: "s".repeat(4096) }, true],
  [{ request: { ...request, endpoint: "embeddings" } }, false],
  [{ request: { effort: "high", thinking: "", per_message_off: false } }, false],
  [{ request: { ...request, thinking: "sometimes" } }, false],
  [{ request: { ...request, model: "x" } }, false],
  [{ request: { ...request, labels: { "X-Claude-Code-Agent-Id": "a1" } } }, false],
  [{ request: { ...request, labels: Object.fromEntries(Array.from({ length: 17 }, (_, i) => [`x-${i}`, "v"])) } }, false],
  [{ request: { ...request, labels: { "x-openai-subagent": "v".repeat(257) } } }, false],
  [{ request: { ...request, tool_names: Array.from({ length: 129 }, (_, i) => `t${i}`) } }, false],
  [{ request: { ...request, tool_names: ["n".repeat(65)] } }, false],
  [{ request, last: { ...last, compacted: undefined } }, false],
  [{ request, last: { ...last, age_s: -1 } }, false],
  [{ request, last: { ...last, effort: "adaptive" } }, false],
  [{ request, state: "" }, false],
  [{ request, state: "s".repeat(4097) }, false],
]) {
  const body = { ...routeAsk({ text: "fix the login bug" }), ...JSON.parse(JSON.stringify(extra)) };
  if (validateRouteAsk(body) !== valid) throw new Error(`route-ask-v1: ${JSON.stringify(extra).slice(0, 200)} should be ${valid ? "valid" : "invalid"}`);
}
// A compaction or side request carries no ask.
if (!validateRouteAsk({ ...routeAsk(), request })) throw new Error("route-ask-v1: a body without the ask must be valid");

// OpenAPI: every relative $ref must land on a schema file (and JSON pointer) in this package.
const openapiPath = path.join(packageRoot, "openapi", "middleware.openapi.json");
const openapi = JSON.parse(await readFile(openapiPath, "utf8"));
if (openapi.openapi !== "3.1.0") throw new Error("openapi: must be 3.1.0");
for (const route of ["capabilities", "optimize", "retrieve", "receipts", "sessions/delete"]) {
  if (!openapi.paths[`/caveman/v1/middleware/${route}`]) throw new Error(`openapi: missing route ${route}`);
}
const pointer = (doc, fragment) => fragment.split("/").slice(1)
  .reduce((node, key) => node?.[key.replaceAll("~1", "/").replaceAll("~0", "~")], doc);
let refs = 0;
(function walk(node) {
  if (!node || typeof node !== "object") return;
  if (typeof node.$ref === "string") {
    refs++;
    const [target, fragment = ""] = node.$ref.split("#");
    const doc = target === "" ? openapi : schemas[files.indexOf(path.basename(target))];
    if (target !== "" && (!target.startsWith("../schemas/") || !doc)) throw new Error(`openapi: unresolved $ref ${node.$ref}`);
    if (fragment && pointer(doc, fragment) === undefined) throw new Error(`openapi: unresolved pointer ${node.$ref}`);
  }
  for (const value of Object.values(node)) walk(value);
})(openapi);

console.log(
  `validated ${files.length} JSON schemas, ${refs} OpenAPI refs, and ${fixtureFiles.length} static agent contract fixtures (not executable parity)`,
);
