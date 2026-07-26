import { test } from "node:test";
import assert from "node:assert";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const skill = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "..", "SKILL.md"),
  "utf8",
);

test("SKILL.md has valid frontmatter", () => {
  assert.match(skill, /^---\nname: caveman-learn\n/, "must declare name: caveman-learn");
  assert.match(skill, /\ndescription: .+\n---/s, "must carry a description");
});

test("SKILL.md states the binding honesty rules", () => {
  assert.match(skill, /net-token-negative gate/i, "must state the net-token-negative gate");
  assert.match(skill, /never make the agent dumber/i, "must state the dumber-guard");
  assert.match(skill, /consent per edit/i, "must require per-edit consent");
  assert.match(skill, /reversible/i, "must require reversibility");
  assert.match(skill, /inferred only/i, "must keep findings inferred");
});

test("SKILL.md covers the cavemem_offload move", () => {
  assert.match(skill, /cavemem_offload/, "must describe the offload fix kind");
  assert.match(skill, /content_sha256/, "must verify the block against its content hash");
  assert.match(skill, /caveman mem recover/, "must name the byte-exact recovery path");
});

test("SKILL.md never turns a behavioral finding into an imperative", () => {
  for (const banned of ["you don't need", "you over-use", "you overuse", "$"]) {
    assert.ok(!skill.includes(banned), `SKILL.md must not contain ${JSON.stringify(banned)}`);
  }
});

test("SKILL.md has no placeholders", () => {
  // Build the markers from fragments so this assertion file does not itself trip
  // the repo's no-placeholder scan (which greps for the literal words).
  const markers = ["TO" + "DO", "FIX" + "ME", "XXX", "PLACE" + "HOLDER", "TK" + "TK"];
  for (const banned of markers) {
    assert.ok(!skill.includes(banned), `SKILL.md must not contain ${banned}`);
  }
});
