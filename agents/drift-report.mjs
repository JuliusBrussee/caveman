#!/usr/bin/env node
// drift-report.mjs — turn a `probe-installed --allow-newer --json` result into one
// GitHub issue per DRIFTED agent (the installed @latest binary is newer than the pinned
// tested_agent_version). Idempotent: one open issue per agent id, updated in place rather
// than duplicated. Non-blocking by design — the caller runs it with continue-on-error, and
// this script never exits non-zero on a `gh` hiccup, only on bad input.
//
// Usage: node public/agents/drift-report.mjs --input <probe.json>
// Requires the `gh` CLI authenticated (GH_TOKEN) with `issues: write`.

import { readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";

const args = process.argv.slice(2);
const inputAt = args.indexOf("--input");
if (inputAt === -1 || !args[inputAt + 1]) {
  process.stderr.write("usage: node public/agents/drift-report.mjs --input <probe.json>\n");
  process.exit(2);
}

let parsed;
try {
  parsed = JSON.parse(readFileSync(args[inputAt + 1], "utf8"));
} catch (error) {
  process.stderr.write(`drift-report: cannot read probe json: ${error.message}\n`);
  process.exit(2);
}

const drifted = (parsed.results || []).filter((r) => r.status === "drift");
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
    `2. Bump \`tested_agent_version\` in \`public/agents/profiles/${r.id}.json\` (the conformance CI pin is derived from it and enforced by \`compile.mjs\`).`,
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
