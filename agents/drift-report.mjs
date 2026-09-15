#!/usr/bin/env node
// drift-report.mjs — turn a `probe-installed --allow-newer --json` result into one
// GitHub issue per DRIFTED agent (the installed @latest binary is newer than the pinned
// tested_agent_version). Idempotent: one open issue per agent id, updated in place rather
// than duplicated. Non-blocking by design — the caller runs it with continue-on-error, and
// this script never exits non-zero on a `gh` hiccup, only on bad input.
//
// Usage: node agents/drift-report.mjs --input <probe.json>
// Requires the `gh` CLI authenticated (GH_TOKEN) with `issues: write`.

import { readFileSync, statSync } from "node:fs";
import { cmpVersion } from "./version.mjs";
import { spawnSync } from "node:child_process";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const PROBE_SCHEMA = "caveman.agent-probe.v1";
const MAX_PROBE_BYTES = 1024 * 1024;
const MAX_RESULTS = 64;
const VERSION_RE = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

function invalid(message) {
  process.stderr.write(`drift-report: invalid probe artifact: ${message}\n`);
  process.exit(2);
}

function exactKeys(value, keys) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  return actual.length === expected.length && actual.every((key, index) => key === expected[index]);
}

function boundedString(value, max, { empty = false, controls = false } = {}) {
  return typeof value === "string"
    && (empty || value.length > 0)
    && value.length <= max
    && (controls || !/[\u0000-\u001f\u007f]/.test(value));
}


let registry;
try {
  registry = JSON.parse(readFileSync(join(here, "agents.json"), "utf8"));
} catch (error) {
  invalid(`trusted registry cannot be read: ${error.message}`);
}
if (registry?.schema_version !== "1" || !Array.isArray(registry.agents)) {
  invalid("trusted registry must use schema_version 1 with an agents array");
}
const profiles = new Map();
for (const profile of registry.agents) {
  if (!profile || typeof profile !== "object" || typeof profile.id !== "string" || profiles.has(profile.id)) {
    invalid("trusted registry contains an invalid or duplicate profile id");
  }
  if (!boundedString(profile.tested_agent_version, 128)
    || (profile.tested_agent_version !== "x" && !VERSION_RE.test(profile.tested_agent_version))) {
    invalid(`trusted registry profile ${profile.id} has an invalid tested_agent_version`);
  }
  profiles.set(profile.id, profile);
}
if (profiles.size === 0 || profiles.size > MAX_RESULTS) invalid(`trusted registry profile count must be between 1 and ${MAX_RESULTS}`);

const args = process.argv.slice(2);
if (args.length !== 4 || args[0] !== "--input" || !args[1] || args[2] !== "--expected-id" || !args[3]) {
  process.stderr.write("usage: node agents/drift-report.mjs --input <probe.json> --expected-id <profile-id>\n");
  process.exit(2);
}
const expectedId = args[3];
if (!profiles.has(expectedId)) invalid(`--expected-id must name a known profile (got ${JSON.stringify(expectedId)})`);
if (basename(args[1]) !== `${expectedId}.json`) {
  invalid(`input basename must exactly match expected profile id (${expectedId}.json)`);
}

let parsed;
try {
  const stat = statSync(args[1]);
  if (!stat.isFile() || stat.size <= 0 || stat.size > MAX_PROBE_BYTES) {
    invalid(`input must be a non-empty regular file no larger than ${MAX_PROBE_BYTES} bytes`);
  }
  parsed = JSON.parse(readFileSync(args[1], "utf8"));
} catch (error) {
  invalid(`cannot read probe json: ${error.message}`);
}

if (!exactKeys(parsed, ["schema", "results"]) || parsed.schema !== PROBE_SCHEMA || !Array.isArray(parsed.results)) {
  invalid(`top level must contain exactly schema=${JSON.stringify(PROBE_SCHEMA)} and results[]`);
}
if (parsed.results.length !== profiles.size || parsed.results.length > MAX_RESULTS) {
  invalid(`results must contain the complete ${profiles.size}-profile registry set`);
}

const seen = new Set();
const statusKeys = {
  "missing": ["id", "status", "tested"],
  "not-installed": ["id", "status", "tested"],
  "ok": ["id", "status", "binary", "tested", "observed", "version_ok", "help_ok", "version_matches"],
  "drift": ["id", "status", "binary", "tested", "observed", "version_ok", "help_ok", "version_matches"],
  "broken": ["id", "status", "binary", "tested", "observed", "version_ok", "help_ok", "version_matches", "version_error", "help_error"],
};
for (const [index, result] of parsed.results.entries()) {
  const label = `results[${index}]`;
  if (!result || typeof result !== "object" || Array.isArray(result)) invalid(`${label} must be an object`);
  if (!boundedString(result.id, 64) || !profiles.has(result.id)) invalid(`${label}.id must name a known profile`);
  if (seen.has(result.id)) invalid(`${label}.id duplicates profile ${result.id}`);
  seen.add(result.id);
  const expectedKeys = statusKeys[result.status];
  if (!expectedKeys || !exactKeys(result, expectedKeys)) invalid(`${label} has unknown status or fields outside its exact status schema`);
  const profile = profiles.get(result.id);
  if (result.tested !== profile.tested_agent_version) invalid(`${label}.tested must exactly equal registry tested_agent_version for ${result.id}`);

  if (result.status === "missing" || result.status === "not-installed") continue;
  if (!boundedString(result.binary, 4096)) invalid(`${label}.binary must be a bounded control-free string`);
  if (typeof result.version_ok !== "boolean" || typeof result.help_ok !== "boolean" || typeof result.version_matches !== "boolean") {
    invalid(`${label} probe flags must be booleans`);
  }
  if (!boundedString(result.observed, 128, { empty: true }) || (result.observed !== "" && !VERSION_RE.test(result.observed))) {
    invalid(`${label}.observed must be empty or a strict version`);
  }
  const expectedMatch = result.tested === "x" || result.observed === result.tested;
  if (result.version_matches !== expectedMatch) invalid(`${label}.version_matches contradicts tested and observed versions`);

  if (result.status === "ok") {
    if (!result.version_ok || !result.help_ok || !result.version_matches || !VERSION_RE.test(result.observed)) {
      invalid(`${label} ok status requires launchable probes and an exact version match`);
    }
  } else if (result.status === "drift") {
    if (!result.version_ok || !result.help_ok || result.version_matches || result.tested === "x"
      || !VERSION_RE.test(result.observed) || cmpVersion(result.observed, result.tested) <= 0) {
      invalid(`${label} drift status requires a strict observed version newer than tested`);
    }
    if (result.id !== expectedId) invalid(`${label} cannot report drift for ${result.id}; artifact is bound to ${expectedId}`);
  } else {
    if (result.version_ok && result.help_ok && result.version_matches) invalid(`${label} broken status contradicts successful matching probes`);
    if (!boundedString(result.version_error, 131072, { empty: true, controls: true })
      || !boundedString(result.help_error, 131072, { empty: true, controls: true })) {
      invalid(`${label} broken probe errors must be bounded strings`);
    }
  }
}

for (const id of profiles.keys()) {
  if (!seen.has(id)) invalid(`results omit expected registry profile ${id}`);
}
const expectedResult = parsed.results.find((result) => result.id === expectedId);
if (!expectedResult || expectedResult.status === "not-installed") {
  invalid(`expected profile ${expectedId} must carry its required-probe result`);
}

// No issue-write-capable subprocess runs before every byte of the downloaded artifact
// has passed the closed validation above.
const drifted = expectedResult.status === "drift" ? [expectedResult] : [];
if (drifted.length === 0) {
  process.stdout.write("drift-report: no drift observed\n");
  process.exit(0);
}

function gh(argv) {
  const result = spawnSync("gh", argv, { encoding: "utf8" });
  return { ok: result.status === 0 && !result.error, stdout: result.stdout || "", stderr: result.stderr || result.error?.message || "" };
}

for (const r of drifted) {
  const title = `agent-drift: ${r.id} — installed ${r.observed} exceeds pinned ${r.tested}`;
  const marker = `<!-- caveman-agent-drift:${r.id} -->`;
  const body = [
    marker,
    "",
    `The \`${r.id}\` upstream released a version newer than the pin the profile claims to test.`,
    "",
    `- installed (@latest): \`${r.observed}\``,
    `- profile \`tested_agent_version\`: \`${r.tested}\``,
    "",
    "This is a non-blocking drift report from the nightly Agent conformance workflow. To clear it:",
    `1. Verify \`${r.id}@${r.observed}\` wraps correctly.`,
    `2. Bump \`tested_agent_version\` in \`agents/profiles/${r.id}.json\` (the conformance CI pin is derived from it and enforced by \`compile.mjs\`).`,
    `3. Update \`last_verified_at\`/\`verified_by\` if the profile carries them.`,
  ].join("\n");

  // One open issue per agent id. GitHub's full-text search tokenizer mangles the
  // punctuation-heavy marker, so a search-based lookup misses and re-opens duplicates every
  // run. Instead LIST open issues and filter locally by the stable title prefix or the body
  // marker — deterministic, no tokenizer in the loop.
  const titlePrefix = `agent-drift: ${r.id} `;
  const list = gh(["issue", "list", "--state", "open", "--limit", "200", "--json", "number,title,body"]);
  let existing;
  if (list.ok) {
    try {
      existing = JSON.parse(list.stdout).find((issue) =>
        (typeof issue.title === "string" && issue.title.startsWith(titlePrefix)) ||
        (typeof issue.body === "string" && issue.body.includes(marker)));
    } catch {
      existing = undefined;
    }
  } else {
    process.stdout.write(`drift-report: could not list issues for ${r.id}: ${list.stderr}\n`);
    continue;
  }

  if (existing) {
    if ((existing.body || "").includes(`installed (@latest): \`${r.observed}\``)) {
      process.stdout.write(`drift-report: ${r.id} issue #${existing.number} already current\n`);
      continue;
    }
    const edit = gh(["issue", "edit", String(existing.number), "--body", body, "--title", title]);
    process.stdout.write(edit.ok ? `drift-report: updated ${r.id} issue #${existing.number}\n` : `drift-report: failed to update ${r.id}: ${edit.stderr}\n`);
  } else {
    const created = gh(["issue", "create", "--title", title, "--body", body]);
    process.stdout.write(created.ok ? `drift-report: opened ${r.id} issue\n` : `drift-report: failed to open ${r.id} issue: ${created.stderr}\n`);
  }
}

process.exit(0);
