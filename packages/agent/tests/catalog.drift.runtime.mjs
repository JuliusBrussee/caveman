import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { CATALOG_SHA256, catalogCost, catalogSearchCeiling } from "../dist/catalog.js";
import { generate } from "../../../scripts/generate-agent-catalog.mjs";

const packageRoot = resolve(import.meta.dirname, "..");
const catalogYamlPath = [
  resolve(packageRoot, "../shared/provider-catalog/catalog/current.yaml"),
  resolve(packageRoot, "../../shared/provider-catalog/catalog/current.yaml"),
].find((candidate) => existsSync(candidate));
assert.ok(catalogYamlPath, "provider catalog must exist beside public/ or repository root");
const catalogModulePath = resolve(packageRoot, "src/catalog.ts");

test("CATALOG_SHA256 pins the exact provider-catalog bytes it was generated from", async () => {
  const bytes = await readFile(catalogYamlPath);
  const digest = createHash("sha256").update(bytes).digest("hex");
  assert.equal(
    CATALOG_SHA256,
    digest,
    "catalog.ts pins a digest that is not current.yaml — re-run node scripts/generate-agent-catalog.mjs",
  );
});

test("src/catalog.ts is exactly what the generator produces today", async () => {
  const onDisk = await readFile(catalogModulePath, "utf8");
  assert.equal(
    onDisk,
    generate(),
    "catalog.ts is stale or hand-edited — re-run node scripts/generate-agent-catalog.mjs",
  );
});

test("generated catalog prices region-agnostic rows and stays honest about the rest", () => {
  const million = {
    inputTokens: 1_000_000,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    reasoningTokens: 0,
  };
  // Widened beyond the former hand-typed three: a Sonnet row now compiles.
  const sonnet = catalogCost({ provider: "anthropic", model: "claude-sonnet-4-5", ...million });
  assert.equal(sonnet.priced, true);
  assert.equal(sonnet.usd, 3);
  assert.equal(catalogSearchCeiling("anthropic/claude-sonnet-4-5", 1_000_000, 0), 3.75);

  // The original three keep their prices; current.yaml is the only source.
  assert.deepEqual(catalogCost({ provider: "anthropic", model: "claude-haiku-4-5", ...million }), {
    priced: true,
    usd: 1,
  });
  assert.deepEqual(catalogCost({ provider: "openai", model: "gpt-5.4-mini", ...million }), {
    priced: true,
    usd: 0.75,
  });
  assert.deepEqual(catalogCost({ provider: "gemini", model: "gemini-2.5-flash", ...million }), {
    priced: true,
    usd: 0.3,
  });

  // google/ still normalizes to gemini/ on both surfaces.
  assert.deepEqual(catalogCost({ provider: "google", model: "gemini-2.5-flash", ...million }), {
    priced: true,
    usd: 0.3,
  });
  assert.equal(catalogSearchCeiling("google/gemini-2.5-flash", 1_000_000, 0), 0.3);

  // Pi names Vertex `google-vertex`; catalog names the billing surface `vertex`.
  // Both cost and reservation paths must cross that boundary identically.
  assert.deepEqual(catalogCost({ provider: "google-vertex", model: "gemini-2.5-flash", ...million }), {
    priced: true,
    usd: 0.3,
  });
  assert.equal(catalogSearchCeiling("google-vertex/gemini-2.5-flash", 1_000_000, 0), 0.3);

  // Regional-only rows are never borrowed: honest zero, not a us-east-1 guess.
  assert.deepEqual(
    catalogCost({ provider: "bedrock", model: "global.anthropic.claude-sonnet-4-6", ...million }),
    { priced: false, usd: 0 },
  );
  assert.equal(catalogSearchCeiling("bedrock/global.anthropic.claude-sonnet-4-6", 1_000_000, 0), undefined);
  assert.deepEqual(catalogCost({ provider: "unknown", model: "no-price", ...million }), {
    priced: false,
    usd: 0,
  });
});

test("thinking tokens are never priced free when the catalog omits a reasoning rate", () => {
  // current.yaml leaves reasoning_output_per_million null on Anthropic rows;
  // catalogCost subtracts reasoning out of output, so a 0 rate would hide spend.
  const reasoned = catalogCost({
    provider: "anthropic",
    model: "claude-haiku-4-5",
    inputTokens: 0,
    outputTokens: 1_000_000,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    reasoningTokens: 1_000_000,
  });
  assert.deepEqual(reasoned, { priced: true, usd: 5 });
});
