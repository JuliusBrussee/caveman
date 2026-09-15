import { test } from "node:test";
import assert from "node:assert";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const skillFile = join(dirname(fileURLToPath(import.meta.url)), "..", "SKILL.md");
const md = readFileSync(skillFile, "utf8");

function frontmatter(text) {
  const match = text.match(/^---\n([\s\S]*?)\n---\n/);
  assert.ok(match, "skill file must open with a --- frontmatter block ---");
  return match[1];
}

test("frontmatter name matches directory and declares read-only cheap explorer", () => {
  const fm = frontmatter(md);
  assert.match(fm, /^name:\s*caveman-explore\s*$/m, "name must be caveman-explore");
  assert.match(fm, /^model:\s*haiku\s*$/m, "explorer must run on cheap model");
  assert.match(fm, /^tools:\s*Read,\s*Glob,\s*Grep\s*$/m, "tools must be exactly three read-only tools");
  assert.doesNotMatch(fm, /\b(Edit|Write|Bash|NotebookEdit)\b/, "explorer must not have write or execution tools");
  assert.match(fm, /^description:\s*.+/m, "description required for auto-delegation");
});

test("description says when to invoke and skip", () => {
  const fm = frontmatter(md);
  assert.match(fm, /cold-start|cross-file|localization|search has failed/i, "must say when to invoke");
  assert.match(fm, /skip/i, "must say when to skip");
});

test("body mandates parallel calls and citation-only reply", () => {
  assert.match(md, /IN PARALLEL/i, "must mandate parallel tool calls");
  assert.match(md, /ONLY an evidence block|only.*citation/i, "must mandate citation-only reply");
  assert.match(md, /path\/to\/file\.ext:START-END/i, "must show compact path:line shape");
  assert.match(md, /no relevant locations found/i, "must give honest empty fallback");
  assert.match(md, /never edit|never.*solve|read-only/i, "must forbid editing and solving");
});

test("artifact carries no placeholder markers", () => {
  const banned = ["TO" + "DO", "FIX" + "ME", "place" + "holder", "X" + "X" + "X"];
  for (const marker of banned) {
    assert.doesNotMatch(md, new RegExp("\\b" + marker + "\\b", "i"), `artifact must not contain ${marker}`);
  }
});
