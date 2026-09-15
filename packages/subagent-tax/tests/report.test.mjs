import { test } from "node:test";
import assert from "node:assert/strict";

import { renderTable, renderHonesty, formatTokens, HONESTY_LINES, LEGEND, SPREAD_NOTE } from "../lib/report.mjs";

const row = (over = {}) => ({
  harness: "claude",
  status: "ok",
  variant: "real config",
  tokens: { tokens: 42710, basis: "est", ratio: 6.4 },
  seconds_to_first_capture: 2.8,
  primary: { kind: "anthropic-messages", tools_count: 91, mcp_tools_count: 63, system_chars: 10979, tools_chars: 224768, total_chars: 273341, body_bytes: 273958 },
  ...over,
});

test("table prints the variant label for every row", () => {
  const out = renderTable([row(), row({ harness: "pi", variant: "isolated home (4 default tools)", primary: { ...row().primary, mcp_tools_count: null, tools_count: 4 } })]);
  assert.match(out, /variant/);
  assert.match(out, /real config/);
  assert.match(out, /isolated home/);
});

test("unknown mcp support prints '-', never 0", () => {
  const out = renderTable([row({ harness: "codex", primary: { ...row().primary, mcp_tools_count: null } })]);
  const dataLine = out.split("\n")[2];
  assert.ok(!/\s0\s/.test(dataLine.split("openai")[0] ?? dataLine), "no false zero in the mcp cell");
  assert.match(dataLine, /-/);
});

test("spread column appears only when a row carries a spread", () => {
  assert.ok(!renderTable([row()]).includes("spread"));
  const withSpread = renderTable([row({ spread: { min_chars: 1000, max_chars: 2000, median_chars: 1500 }, trials: 3, trials_ok: 3 })]);
  assert.match(withSpread, /spread \(n\)/);
  assert.match(withSpread, /\(3\/3\)/);
});

test("estimates round to 2 significant figures and are marked; exact prints in full", () => {
  assert.equal(formatTokens({ tokens: 42710, basis: "est" }), "~43k (est)");
  assert.equal(formatTokens({ tokens: 8323, basis: "est" }), "~8.3k (est)");
  assert.equal(formatTokens({ tokens: 42710, basis: "exact" }), "42,710 (exact)");
  assert.match(formatTokens({ tokens: 900, basis: "est (count_tokens failed)" }), /count_tokens failed/);
  assert.equal(formatTokens(null), "-");
});

test("honesty block: required claims present, forbidden money words absent", () => {
  const block = renderHonesty([SPREAD_NOTE]);
  assert.match(block, /Basis: inferred/);
  assert.match(block, /bill delta/);
  assert.match(block, /not a confidence interval/);
  assert.match(block, /'-' means unknown, not zero/);
  // The cache discount must not be stated as one provider's rate for all.
  assert.match(block, /0\.25|0\.5/, "cache line covers non-Anthropic providers");
  for (const forbidden of [/\bsavings\b/i, /\bsaved\b/i, /\$/]) {
    assert.ok(!forbidden.test(block.replace(/not savings/i, "")), `honesty block must not claim ${forbidden}`);
  }
});

test("legend explains the columns readers will misread", () => {
  assert.match(LEGEND, /not a latency benchmark/);
  assert.match(LEGEND, /body_bytes/);
});

test("HONESTY_LINES never says 'measured' of a captured size", () => {
  const joined = HONESTY_LINES.join(" ");
  assert.ok(!/locally measured/.test(joined), "'measured' is reserved for provider-priced spend in this repo");
  assert.match(joined, /locally captured/);
});
