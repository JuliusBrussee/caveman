import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const projectSlugs = ["payments-api", "support-agent", "reporting-worker"];
export const defaultFixtureTimestamp = "2026-07-26T12:00:00.000Z";
const repeatedRules = [
  "Preserve caller bytes on parse failure and keep record mode pass-through.",
  "Treat inferred figures as per-day estimates and never relabel them verified.",
  "Scope every tenant query by organization id taken from authenticated context.",
  "Reject unknown safety or grader values instead of guessing a permissive default.",
  "Keep prompt bodies, tool arguments, file paths, and free text out of analytics.",
  "Collapse overlapping detector opportunities before presenting total headroom.",
  "Use provider-complete token evidence before attaching public catalog dollars.",
  "Keep automatic receipt emission disabled until producer completeness is durable.",
];

export const recurringBlock = [
  "SHARED OPERATOR CONTEXT — restated at the start of every agent session.",
  ...Array.from({ length: 4 }, () => repeatedRules).flat(),
].join("\n");

export const estimatedBlockTokens = Math.ceil(Buffer.byteLength(recurringBlock, "utf8") / 4);

function normalizedTimestamp(timestamp) {
  const date = new Date(timestamp);
  if (!Number.isFinite(date.valueOf())) throw new Error(`invalid RFC3339 timestamp: ${timestamp}`);
  return date.toISOString();
}

export function makeClaudeRoot(root, { timestamp = defaultFixtureTimestamp } = {}) {
  const ts = normalizedTimestamp(timestamp);
  if (estimatedBlockTokens < 400) {
    throw new Error(`recurring block estimates ${estimatedBlockTokens} tokens; need at least 400`);
  }

  const files = projectSlugs.map((slug) => {
    const dir = join(root, "projects", slug);
    const file = join(dir, "session.jsonl");
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    const turn = {
      type: "user",
      timestamp: ts,
      message: { content: [{ type: "text", text: recurringBlock }] },
    };
    writeFileSync(file, `${JSON.stringify(turn)}\n`, { mode: 0o600 });
    return file;
  });

  return {
    root,
    files,
    projectSlugs: [...projectSlugs],
    estimatedBlockTokens,
    timestamp: ts,
  };
}

const invokedPath = process.argv[1] ? pathToFileURL(resolve(process.argv[1])).href : "";
if (invokedPath === import.meta.url) {
  const defaultRoot = join(dirname(fileURLToPath(import.meta.url)), "claude-root");
  const result = makeClaudeRoot(resolve(process.argv[2] ?? defaultRoot));
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
}
