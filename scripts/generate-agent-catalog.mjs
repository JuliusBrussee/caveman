#!/usr/bin/env node
// Generates public/agent/src/catalog.ts from the repo's single pricing source of
// truth, public/shared/provider-catalog/catalog/current.yaml.
//
// The generated module carries a truthful CATALOG_SHA256 (the sha256 of the
// catalog bytes it was generated from). public/agent/tests/catalog.drift.runtime.mjs
// recomputes both the digest and the rendered module, so an edited catalog fails
// the suite until this script is re-run.
//
// Usage:
//   node scripts/generate-agent-catalog.mjs            # write public/agent/src/catalog.ts
//   node scripts/generate-agent-catalog.mjs --out FILE # write elsewhere
//   node scripts/generate-agent-catalog.mjs --check    # exit 1 if the file is stale
//
// No npm dependencies: the catalog is a flat two-level YAML sequence, so this
// file carries a purpose-built parser for exactly that shape. Anything the
// parser does not recognise is a hard error — it never guesses.

import { createHash } from "node:crypto";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const SOURCE_PUBLIC_ROOT = resolve(REPO_ROOT, "public");
const AGENT_ROOT = existsSync(resolve(SOURCE_PUBLIC_ROOT, "agent/package.json"))
  ? resolve(SOURCE_PUBLIC_ROOT, "agent")
  : resolve(REPO_ROOT, "packages/agent");
const catalogCandidates = [
  resolve(SOURCE_PUBLIC_ROOT, "shared/provider-catalog/catalog/current.yaml"),
  resolve(REPO_ROOT, "shared/provider-catalog/catalog/current.yaml"),
  resolve(REPO_ROOT, "packages/shared/provider-catalog/catalog/current.yaml"),
];
export const CATALOG_PATH = catalogCandidates.find((candidate) => existsSync(candidate)) ?? catalogCandidates[0];
export const OUTPUT_PATH = resolve(AGENT_ROOT, "src/catalog.ts");

const CATALOG_LABEL = "public/shared/provider-catalog/catalog/current.yaml";
const GENERATOR_LABEL = "scripts/generate-agent-catalog.mjs";

// The catalog's own region-agnostic marker. catalog/catalog.go uses the same
// rule: only a `global` row answers a generic, region-free price lookup, because
// borrowing a regional row would fabricate spend.
const REGION_AGNOSTIC = "global";
const REQUIRED_CURRENCY = "USD";

/** Parses the flat `- key: value` catalog sequence. Throws on any other shape. */
export function parseCatalogYaml(text, label = CATALOG_LABEL) {
  const rows = [];
  let row = null;
  let blockKey = null;
  const lines = text.split("\n");
  for (let index = 0; index < lines.length; index++) {
    const line = lines[index].replace(/\s+$/, "");
    const lineNo = index + 1;
    if (line === "" || /^ *#/.test(line)) continue;
    const shape = /^( *)(- )?(.*)$/.exec(line);
    const indent = shape[1].length;
    const dash = shape[2] !== undefined;
    const rest = shape[3];
    if (indent === 0) {
      if (!dash) throw new Error(`${label}:${lineNo}: expected a top-level "- provider: ..." row`);
      row = {};
      rows.push(row);
      blockKey = null;
      const entry = splitKeyValue(rest, label, lineNo);
      if (entry.value === undefined) throw new Error(`${label}:${lineNo}: row must start with an inline "provider" value`);
      row[entry.key] = entry.value;
      continue;
    }
    if (row === null) throw new Error(`${label}:${lineNo}: indented line before any row`);
    if (indent === 2) {
      if (dash) throw new Error(`${label}:${lineNo}: unexpected sequence item at row level`);
      const entry = splitKeyValue(rest, label, lineNo);
      if (Object.prototype.hasOwnProperty.call(row, entry.key)) {
        throw new Error(`${label}:${lineNo}: duplicate key "${entry.key}"`);
      }
      if (entry.value === undefined) {
        blockKey = entry.key;
        row[entry.key] = undefined;
      } else {
        blockKey = null;
        row[entry.key] = entry.value;
      }
      continue;
    }
    if (indent === 4) {
      if (blockKey === null) throw new Error(`${label}:${lineNo}: nested line without an open block`);
      if (dash) {
        if (row[blockKey] === undefined) row[blockKey] = [];
        if (!Array.isArray(row[blockKey])) throw new Error(`${label}:${lineNo}: "${blockKey}" mixes map and sequence entries`);
        row[blockKey].push(rest);
        continue;
      }
      if (row[blockKey] === undefined) row[blockKey] = {};
      if (Array.isArray(row[blockKey]) || typeof row[blockKey] !== "object") {
        throw new Error(`${label}:${lineNo}: "${blockKey}" mixes map and sequence entries`);
      }
      const entry = splitKeyValue(rest, label, lineNo);
      if (entry.value === undefined) throw new Error(`${label}:${lineNo}: nesting deeper than two levels is not supported`);
      if (Object.prototype.hasOwnProperty.call(row[blockKey], entry.key)) {
        throw new Error(`${label}:${lineNo}: duplicate key "${blockKey}.${entry.key}"`);
      }
      row[blockKey][entry.key] = entry.value;
      continue;
    }
    throw new Error(`${label}:${lineNo}: unexpected indent of ${indent} spaces`);
  }
  return rows;
}

function splitKeyValue(text, label, lineNo) {
  const match = /^([A-Za-z_][A-Za-z0-9_]*):(?: (.*))?$/.exec(text);
  if (match === null) throw new Error(`${label}:${lineNo}: cannot read "${text}" as a "key: value" pair`);
  return { key: match[1], value: match[2] === undefined ? undefined : scalar(match[2], label, lineNo) };
}

function scalar(text, label, lineNo) {
  if (text === "") throw new Error(`${label}:${lineNo}: empty value`);
  if (text === "null") return null;
  if (text === "true") return true;
  if (text === "false") return false;
  if (/^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$/.test(text)) {
    const value = Number(text);
    if (!Number.isFinite(value)) throw new Error(`${label}:${lineNo}: "${text}" is not a finite number`);
    return value;
  }
  return text;
}

/** Selects the priced, region-agnostic, USD rows the agent catalog can honestly carry. */
export function selectRows(rows, label = CATALOG_LABEL) {
  const selected = [];
  const skippedByKey = new Map();
  const seen = new Set();
  const record = (key) => {
    let entry = skippedByKey.get(key);
    if (entry === undefined) {
      entry = { regions: [], reasons: [] };
      skippedByKey.set(key, entry);
    }
    return entry;
  };
  const skipRegional = (key, region) => {
    const entry = record(key);
    if (!entry.regions.includes(region)) entry.regions.push(region);
  };
  const skip = (key, reason) => {
    const entry = record(key);
    if (!entry.reasons.includes(reason)) entry.reasons.push(reason);
  };
  for (const row of rows) {
    for (const field of ["provider", "model", "region", "currency"]) {
      if (typeof row[field] !== "string" || row[field] === "") {
        throw new Error(`${label}: row is missing a string "${field}"`);
      }
    }
    const key = `${row.provider}/${row.model}`;
    if (row.region !== REGION_AGNOSTIC) {
      skipRegional(key, row.region);
      continue;
    }
    if (row.currency !== REQUIRED_CURRENCY) {
      skip(key, `priced in ${row.currency}, not ${REQUIRED_CURRENCY}`);
      continue;
    }
    if (row.pricing === null || typeof row.pricing !== "object" || Array.isArray(row.pricing)) {
      throw new Error(`${label}: ${key} has no pricing block`);
    }
    const input = rate(row.pricing.input_per_million, `${key}.input_per_million`, label);
    const output = rate(row.pricing.output_per_million, `${key}.output_per_million`, label);
    if (input === null || output === null) {
      skip(key, "no list price for input or output tokens");
      continue;
    }
    if (seen.has(key)) throw new Error(`${label}: duplicate region-agnostic row ${key}`);
    seen.add(key);
    // reasoning_output_per_million is null on every provider that bills thinking
    // tokens at the plain output rate. catalogCost subtracts reasoning tokens
    // out of output before pricing them, so a 0 here would make them free.
    const reasoning = rate(row.pricing.reasoning_output_per_million, `${key}.reasoning_output_per_million`, label);
    selected.push({
      key,
      price: {
        inputPerMillion: input,
        outputPerMillion: output,
        cacheReadPerMillion: rate(row.pricing.cache_read_input_per_million, `${key}.cache_read_input_per_million`, label) ?? 0,
        cacheWritePerMillion: rate(row.pricing.cache_write_input_per_million, `${key}.cache_write_input_per_million`, label) ?? 0,
        reasoningPerMillion: reasoning ?? output,
      },
    });
  }
  selected.sort((left, right) => (left.key < right.key ? -1 : left.key > right.key ? 1 : 0));
  const skipped = [...skippedByKey.entries()]
    .map(([key, entry]) => {
      const reasons = [...entry.reasons].sort();
      if (entry.regions.length > 0) {
        reasons.unshift(`priced per region only (${[...entry.regions].sort().join(", ")})`);
      }
      return { key, reason: reasons.join("; ") };
    })
    .sort((left, right) => (left.key < right.key ? -1 : left.key > right.key ? 1 : 0));
  return { selected, skipped };
}

function rate(value, field, label) {
  if (value === undefined || value === null) return null;
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    throw new Error(`${label}: ${field} must be a finite non-negative number`);
  }
  return value;
}

function number(value) {
  const text = String(value);
  if (!/^(0|[1-9][0-9]*)(\.[0-9]+)?(e[+-]?[0-9]+)?$/.test(text)) {
    throw new Error(`cannot render ${text} as a stable numeric literal`);
  }
  return text;
}

/** Renders the full public/agent/src/catalog.ts module text. */
export function renderCatalogModule(catalogBytes, label = CATALOG_LABEL) {
  const digest = createHash("sha256").update(catalogBytes).digest("hex");
  const { selected, skipped } = selectRows(parseCatalogYaml(catalogBytes.toString("utf8"), label), label);
  if (selected.length === 0) throw new Error(`${label}: no priced region-agnostic rows found`);
  const entries = selected.map(({ key, price }) => [
    `  ${JSON.stringify(key)}: Object.freeze({`,
    `    inputPerMillion: ${number(price.inputPerMillion)},`,
    `    outputPerMillion: ${number(price.outputPerMillion)},`,
    `    cacheReadPerMillion: ${number(price.cacheReadPerMillion)},`,
    `    cacheWritePerMillion: ${number(price.cacheWritePerMillion)},`,
    `    reasoningPerMillion: ${number(price.reasoningPerMillion)},`,
    `  }),`,
  ].join("\n")).join("\n");
  const skippedLines = skipped.length === 0
    ? "// Every catalog row is represented above.\n"
    : [
      "// Rows deliberately absent above. A regional rate is not a region-free",
      "// rate, so these are omitted rather than borrowed; a run on one of these",
      "// models prices as unpriced (honest zero) instead of plausibly wrong.",
      ...skipped.map(({ key, reason }) => `//   ${key} — ${reason}`),
      "",
    ].join("\n");

  return `// GENERATED by ${GENERATOR_LABEL} — do not edit.
// Source: ${CATALOG_LABEL}, the repo's single pricing source of truth.
// CATALOG_SHA256 is the sha256 of those exact catalog bytes; the drift test in
// tests/catalog.drift.runtime.mjs fails until the generator is re-run.
//
// Included: every USD row the catalog prices region-agnostically (region:
// ${REGION_AGNOSTIC}). Field mapping, catalog key -> TypeScript field:
//   input_per_million             -> inputPerMillion
//   output_per_million            -> outputPerMillion
//   cache_read_input_per_million  -> cacheReadPerMillion (null -> 0; the row
//                                   states no separate rate for that class)
//   cache_write_input_per_million -> cacheWritePerMillion (null -> 0; same)
//   reasoning_output_per_million  -> reasoningPerMillion (null -> the output
//                                   rate, because catalogCost subtracts
//                                   reasoning tokens out of output and a 0
//                                   would price thinking tokens as free)
// Not modeled: 1h cache-write rates, batch discounts, cache storage, and
// long-context multipliers. These numbers are standard-tier public list-price
// subtotals, never an invoice.
export const CATALOG_SHA256 = "${digest}";

export interface CatalogPrice {
  inputPerMillion: number;
  outputPerMillion: number;
  cacheReadPerMillion: number;
  cacheWritePerMillion: number;
  reasoningPerMillion: number;
}

const PRICES: Readonly<Record<string, CatalogPrice>> = Object.freeze({
${entries}
});

${skippedLines}
function catalogProvider(provider: string): string {
  // Runtime provider IDs come from Pi; catalog IDs name billing surfaces.
  if (provider === "google") return "gemini";
  if (provider === "google-vertex") return "vertex";
  return provider;
}

export function catalogSearchCeiling(
  model: string,
  inputTokens: number,
  outputTokens: number,
): number | undefined {
  const slash = model.indexOf("/");
  const normalized = slash < 0
    ? model
    : \`\${catalogProvider(model.slice(0, slash))}/\${model.slice(slash + 1)}\`;
  const price = PRICES[normalized];
  if (!price || !Number.isSafeInteger(inputTokens) || inputTokens < 0 ||
      !Number.isSafeInteger(outputTokens) || outputTokens < 0) {
    return undefined;
  }
  const worstInputRate = Math.max(price.inputPerMillion, price.cacheWritePerMillion);
  const worstOutputRate = Math.max(price.outputPerMillion, price.reasoningPerMillion);
  const usd = (inputTokens * worstInputRate + outputTokens * worstOutputRate) / 1_000_000;
  return Math.ceil(usd * 1e10) / 1e10;
}

export function catalogCost(usage: {
  provider: string;
  model: string;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  reasoningTokens: number;
}): { priced: boolean; usd: number } {
  const provider = catalogProvider(usage.provider);
  const price = PRICES[\`\${provider}/\${usage.model}\`];
  if (!price) return { priced: false, usd: 0 };
  // Pi normalizes input as uncached input. cacheRead/cacheWrite are disjoint
  // classes, while reasoning remains a subset of output.
  const billableInput = usage.inputTokens;
  const visibleOutput = usage.outputTokens - usage.reasoningTokens;
  if (
    billableInput < 0 ||
    visibleOutput < 0 ||
    Object.values(usage).some((value) => typeof value === "number" && (!Number.isSafeInteger(value) || value < 0))
  ) {
    return { priced: false, usd: 0 };
  }
  const usd = (
    billableInput * price.inputPerMillion +
    usage.cacheReadTokens * price.cacheReadPerMillion +
    usage.cacheWriteTokens * price.cacheWritePerMillion +
    visibleOutput * price.outputPerMillion +
    usage.reasoningTokens * price.reasoningPerMillion
  ) / 1_000_000;
  return { priced: true, usd: Math.round((usd + Number.EPSILON) * 1e10) / 1e10 };
}
`;
}

export function generate() {
  return renderCatalogModule(readFileSync(CATALOG_PATH));
}

function main(argv) {
  const check = argv.includes("--check");
  const outIndex = argv.indexOf("--out");
  const outPath = outIndex === -1 ? OUTPUT_PATH : resolve(process.cwd(), argv[outIndex + 1] ?? "");
  if (outIndex !== -1 && argv[outIndex + 1] === undefined) {
    process.stderr.write("--out requires a path\n");
    return 2;
  }
  const rendered = generate();
  if (check) {
    const current = readFileSync(outPath, "utf8");
    if (current === rendered) {
      process.stdout.write(`${GENERATOR_LABEL}: catalog.ts is current\n`);
      return 0;
    }
    process.stderr.write(`${GENERATOR_LABEL}: catalog.ts is stale — re-run the generator\n`);
    return 1;
  }
  writeFileSync(outPath, rendered);
  process.stdout.write(`${GENERATOR_LABEL}: wrote ${outPath}\n`);
  return 0;
}

if (resolve(process.argv[1] ?? "") === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv.slice(2));
}
