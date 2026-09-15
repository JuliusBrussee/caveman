// Report rendering and the repro pack. Every printed number carries its basis;
// the honesty block is not optional output — it ships on every run.

import { writeFileSync, readFileSync, readdirSync, lstatSync } from "node:fs";
import { join, relative } from "node:path";
import { createHash } from "node:crypto";

export const HONESTY_LINES = [
  "Basis: inferred. These are locally captured request-prefix sizes — not provider-billed usage, not spend, not savings.",
  "Prefix per call ≠ bill delta: with a warm provider cache this prefix re-reads at a discount (Anthropic ~0.1x; OpenAI/Gemini higher, ~0.25–0.5x); the full size only bills cold or where caching is broken.",
  "Token counts marked 'est' are chars-per-token estimates rounded to 2 significant figures (±8% calibration band, Anthropic-tokenizer-derived — see report.json); rows marked 'exact' used Anthropic's count_tokens endpoint.",
  "These numbers are this machine's installed configuration (plugins and MCP servers included) on the run date — they will differ on yours. That is the point: run it yourself.",
  "The variant column says what each row measures: a harness's floor (isolated config) and a harness's real installed tax are different constructs — do not compare them as if they were the same measurement.",
  "The mcp column shows '-' where a harness's MCP tool-naming convention has not been confirmed; only Claude Code's is. '-' means unknown, not zero.",
];

export const LEGEND =
  "columns: system/schemas/body = JSON chars of the captured (scrubbed) request; body is the analyzed total, report.json also keeps raw body_bytes · t1st = wall-clock to first captured LLM request, including harness boot — not a latency benchmark · system is computed per protocol, so cross-harness comparison is approximate.";

export const SPREAD_NOTE =
  "spread = observed min–max across repeat runs on this machine, not a confidence interval; the row is the median trial.";

export const FOOTER = "subagent-tax · part of Caveman · basis: inferred, always";

const kb = (chars) => (chars >= 10240 ? `${Math.round(chars / 1024)}k` : chars >= 1024 ? `${(chars / 1024).toFixed(1)}k` : String(chars));

// Estimates print to 2 significant figures: the calibration band is ±8%, so
// further digits are noise, and this is the column readers compare across
// harnesses. Exact (count_tokens) values print in full.
export function formatTokens(t) {
  if (!t) return "-";
  if (t.basis === "exact") return `${t.tokens.toLocaleString("en-US")} (exact)`;
  const n = t.tokens;
  const rounded = n >= 1000 ? `${Number((n / 1000).toPrecision(2))}k` : String(Number(n.toPrecision(2)));
  return `~${rounded} (${t.basis})`;
}

export function renderTable(rows) {
  const anySpread = rows.some((r) => r.spread);
  const cols = [
    { h: "harness", get: (r) => r.harness },
    { h: "status", get: (r) => r.status },
    { h: "wire", get: (r) => r.primary?.kind ?? "-" },
    { h: "tools", get: (r) => (r.primary ? String(r.primary.tools_count) : "-") },
    { h: "mcp", get: (r) => (r.primary && r.primary.mcp_tools_count != null ? String(r.primary.mcp_tools_count) : "-") },
    { h: "system", get: (r) => (r.primary ? kb(r.primary.system_chars) : "-") },
    { h: "schemas", get: (r) => (r.primary ? kb(r.primary.tools_chars) : "-") },
    // Analyzed chars, the same quantity the token column divides — report.json
    // also carries body_bytes (raw, pre-redaction) for every capture. With
    // --repeat this is the median trial; the spread column shows the range.
    { h: "body", get: (r) => (r.primary ? kb(r.primary.total_chars) : "-") },
    { h: "input tokens", get: (r) => formatTokens(r.tokens) },
    { h: "t1st", get: (r) => (r.seconds_to_first_capture != null ? `${r.seconds_to_first_capture.toFixed(1)}s` : "-") },
    ...(anySpread ? [{ h: "spread (n)", get: (r) => (r.spread ? `${kb(r.spread.min_chars)}–${kb(r.spread.max_chars)} (${r.trials_ok}/${r.trials})` : "-") }] : []),
    { h: "variant", get: (r) => r.variant ?? "-" },
  ];
  const widths = cols.map((c) => Math.max(c.h.length, ...rows.map((r) => c.get(r).length)));
  const line = (cells) => cells.map((cell, i) => cell.padEnd(widths[i])).join("  ");
  const out = [line(cols.map((c) => c.h)), line(widths.map((w) => "-".repeat(w)))];
  for (const r of rows) out.push(line(cols.map((c) => c.get(r))));
  return out.join("\n");
}

export function renderHonesty(extra = []) {
  return [...HONESTY_LINES, ...extra].map((l) => `  ! ${l}`).join("\n");
}

function sha256File(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function walk(dir, base = dir, acc = []) {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name);
    // lstat, not stat: a symlink under --out must not be followed (hashing
    // whatever it points at, or looping forever on a link cycle).
    const st = lstatSync(full);
    if (st.isSymbolicLink()) continue;
    if (st.isDirectory()) walk(full, base, acc);
    else if (name !== "manifest.sha256") acc.push(relative(base, full));
  }
  return acc;
}

// The repro pack's integrity anchor: a sha256 of every artifact in the out dir.
// This is a hash manifest, not a signature — CaveBench Ed25519 signing is a
// separate, founder-keyed step that runs only at publish time.
export function writeManifest(outDir) {
  const entries = walk(outDir).sort();
  const lines = entries.map((rel) => `${sha256File(join(outDir, rel))}  ${rel}`);
  writeFileSync(join(outDir, "manifest.sha256"), `${lines.join("\n")}\n`);
  return entries.length;
}

export function writeReport(outDir, data) {
  writeFileSync(join(outDir, "report.json"), `${JSON.stringify(data, null, 2)}\n`);
}
