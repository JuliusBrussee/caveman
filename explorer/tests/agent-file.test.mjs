import { test } from "node:test";
import assert from "node:assert";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const agentFile = join(dirname(fileURLToPath(import.meta.url)), "..", "fastcontext.claude-agent.md");
const md = readFileSync(agentFile, "utf8");

// Split the YAML frontmatter from the system-prompt body.
function frontmatter(text) {
  const m = text.match(/^---\n([\s\S]*?)\n---\n/);
  assert.ok(m, "agent file must open with a --- frontmatter block ---");
  return m[1];
}

test("frontmatter declares a read-only explorer on a cheap model", () => {
  const fm = frontmatter(md);
  assert.match(fm, /^name:\s*fastcontext\s*$/m, "name must be fastcontext");
  assert.match(fm, /^model:\s*haiku\s*$/m, "explorer must run on the cheap model (haiku)");
  assert.match(fm, /^tools:\s*Read,\s*Glob,\s*Grep\s*$/m, "tools must be exactly the three read-only tools");
  assert.doesNotMatch(fm, /\b(Edit|Write|Bash|NotebookEdit)\b/, "the explorer must NOT have any write/exec tool (byte-safe: it only reads)");
  assert.match(fm, /^description:\s*.+/m, "a description is required for auto-delegation");
});

test("description encodes when to invoke and when to skip", () => {
  const fm = frontmatter(md);
  assert.match(fm, /cold-start|cross-file|localization|search has failed/i, "must say when to invoke");
  assert.match(fm, /[Ss]kip/, "must say when to skip (the paper skips when the file/symbol is named)");
});

test("body mandates parallel tool calls and a citation-only reply", () => {
  assert.match(md, /IN PARALLEL/i, "must mandate parallel tool calls (the paper's broad first-turn search)");
  assert.match(md, /ONLY an evidence block|only.*citation/i, "must mandate a citation-only reply");
  assert.match(md, /path\/to\/file\.ext:START-END/i, "must show the compact path:line citation shape");
  assert.match(md, /no relevant locations found/i, "must give an honest 'nothing found' fallback, not a guess");
  assert.match(md, /never edit|never.*solve|read-only/i, "must forbid editing/solving (separation of explore from solve)");
});

test("the artifact carries no placeholder (honesty gate)", () => {
  // Assemble the banned markers from fragments so this assertion does not itself
  // contain the literal tokens the repo's no-placeholder scan greps for.
  const banned = ["TO" + "DO", "FIX" + "ME", "place" + "holder", "X" + "X" + "X"];
  for (const w of banned) {
    assert.doesNotMatch(md, new RegExp("\\b" + w + "\\b", "i"), `a shipped artifact must not contain ${w}`);
  }
});
