/**
 * Contracts package gate: every file in schemas/ must parse, and every data file
 * `X.json` that has a sibling schema `X.schema.json` must satisfy it.
 *
 * The checker covers the JSON-Schema subset this package's own schemas use —
 * type, required, properties, additionalProperties, items, enum, const, minimum,
 * maximum, minItems, minLength, pattern. An unknown keyword is IGNORED (it
 * constrains nothing here), but an unknown `type` name FAILS CLOSED: a misspelled
 * type must never read as "satisfied". Nested option schemas inside
 * grader-registry.json are data to this checker, not rules it executes.
 *
 * Run: node scripts/validate.mjs (wired into build/lint/test).
 */
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const schemasDir = join(dirname(dirname(fileURLToPath(import.meta.url))), "schemas");

const TYPE_CHECKS = {
  object: (v) => v !== null && typeof v === "object" && !Array.isArray(v),
  array: Array.isArray,
  string: (v) => typeof v === "string",
  number: (v) => typeof v === "number" && Number.isFinite(v),
  integer: (v) => typeof v === "number" && Number.isInteger(v),
  boolean: (v) => typeof v === "boolean",
  null: (v) => v === null,
};

/** validate returns a list of human-readable errors; an empty list means valid. */
function validate(value, schema, path = "$") {
  const errors = [];
  if (!TYPE_CHECKS.object(schema)) return [`${path}: schema is not an object`];

  if ("type" in schema) {
    const check = TYPE_CHECKS[schema.type];
    if (!check) return [`${path}: schema uses unknown type ${JSON.stringify(schema.type)}`];
    if (!check(value)) return [`${path}: expected ${schema.type}`];
  }
  if ("const" in schema && JSON.stringify(value) !== JSON.stringify(schema.const)) {
    errors.push(`${path}: expected ${JSON.stringify(schema.const)}`);
  }
  if (Array.isArray(schema.enum) && !schema.enum.some((e) => JSON.stringify(e) === JSON.stringify(value))) {
    errors.push(`${path}: ${JSON.stringify(value)} is not one of ${JSON.stringify(schema.enum)}`);
  }
  if (typeof value === "number") {
    if (typeof schema.minimum === "number" && value < schema.minimum) errors.push(`${path}: below minimum ${schema.minimum}`);
    if (typeof schema.maximum === "number" && value > schema.maximum) errors.push(`${path}: above maximum ${schema.maximum}`);
  }
  if (typeof value === "string") {
    if (typeof schema.minLength === "number" && value.length < schema.minLength) {
      errors.push(`${path}: shorter than minLength ${schema.minLength}`);
    }
    if (typeof schema.pattern === "string" && !new RegExp(schema.pattern).test(value)) {
      errors.push(`${path}: does not match ${schema.pattern}`);
    }
  }
  if (Array.isArray(value)) {
    if (typeof schema.minItems === "number" && value.length < schema.minItems) {
      errors.push(`${path}: fewer than minItems ${schema.minItems}`);
    }
    if (schema.items) value.forEach((item, i) => errors.push(...validate(item, schema.items, `${path}[${i}]`)));
  }
  if (TYPE_CHECKS.object(value)) {
    for (const key of Array.isArray(schema.required) ? schema.required : []) {
      if (!(key in value)) errors.push(`${path}: missing required key ${JSON.stringify(key)}`);
    }
    const props = TYPE_CHECKS.object(schema.properties) ? schema.properties : {};
    for (const [key, sub] of Object.entries(props)) {
      if (key in value) errors.push(...validate(value[key], sub, `${path}.${key}`));
    }
    if (schema.additionalProperties === false) {
      for (const key of Object.keys(value)) {
        if (!(key in props)) errors.push(`${path}: unexpected key ${JSON.stringify(key)}`);
      }
    }
  }
  return errors;
}

const files = readdirSync(schemasDir).filter((f) => f.endsWith(".json")).sort();
const parsed = new Map();
for (const file of files) {
  try {
    parsed.set(file, JSON.parse(readFileSync(join(schemasDir, file), "utf8")));
  } catch (e) {
    console.error(`contracts: ${file} is not valid JSON: ${e.message}`);
    process.exit(1);
  }
}

let checked = 0;
for (const [file, data] of parsed) {
  if (file.endsWith(".schema.json")) continue;
  const schemaFile = `${file.slice(0, -".json".length)}.schema.json`;
  const schema = parsed.get(schemaFile);
  if (!schema) {
    console.error(`contracts: ${file} has no ${schemaFile} to validate against`);
    process.exit(1);
  }
  const errors = validate(data, schema);
  if (errors.length > 0) {
    console.error(`contracts: ${file} does not satisfy ${schemaFile}:`);
    for (const err of errors.slice(0, 20)) console.error(`  ${err}`);
    process.exit(1);
  }
  checked++;
}

console.log(`contracts: ${parsed.size} schema file(s) parsed, ${checked} validated against a sibling schema`);
