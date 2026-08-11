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

const schemas = [];
for (const file of files) {
  const schema = JSON.parse(await readFile(path.join(schemaRoot, file), "utf8"));
  if (schema.$schema !== "https://json-schema.org/draft/2020-12/schema") {
    throw new Error(`${file}: unsupported or missing $schema`);
  }
  if (typeof schema.$id !== "string" || schema.$id.length === 0) {
    throw new Error(`${file}: missing $id`);
  }
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
const validateAdapterConformance = ajv.getSchema(
  "https://caveman.so/schemas/adapter-conformance.schema.json",
);
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

console.log(
  `validated ${files.length} JSON schemas and ${fixtureFiles.length} static agent contract fixtures (not executable parity)`,
);
