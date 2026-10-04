// Embedded copies of the always-on rule must match their sources byte for byte.
// Fix a failure by running `node skills/compile.mjs`, not by hand-editing a copy.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { activationRule } from "../../skills/activation-rule.mjs";

const require = createRequire(import.meta.url);
const read = (relative) => readFileSync(new URL(`../../${relative}`, import.meta.url), "utf8");

test("src/rules/caveman-activate.md is generated from skills/caveman/SKILL.md", () => {
  assert.equal(read("src/rules/caveman-activate.md"), activationRule(read("skills/caveman/SKILL.md")));
});

test("caveman-init.js embedded RULE_BODY equals src/rules/caveman-activate.md", () => {
  assert.equal(require("../../src/tools/caveman-init.js").RULE_BODY, read("src/rules/caveman-activate.md"));
});

test("openclaw embedded bootstrap fallback equals src/rules/caveman-openclaw-bootstrap.md", () => {
  const { loadBootstrapSnippet } = require("../../bin/lib/openclaw.js");
  assert.equal(loadBootstrapSnippet(null), read("src/rules/caveman-openclaw-bootstrap.md"));
});

test("always-on copies name the three skills and no retired level list", () => {
  for (const file of ["src/rules/caveman-activate.md", "src/rules/caveman-openclaw-bootstrap.md"]) {
    const body = read(file);
    assert.match(body, /Respond terse like smart caveman/, `${file}: sentinel`);
    for (const command of ["/caveman", "/ultracave", "/megacave"]) assert.ok(body.includes(command), `${file}: ${command}`);
    assert.doesNotMatch(body, /wenyan|lite\|full/, `${file}: retired level names`);
  }
});
