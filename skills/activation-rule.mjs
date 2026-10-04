// activation-rule.mjs — derives the always-on activation rule from
// skills/caveman/SKILL.md: thesis line plus each `### N.` rule headline with the
// first sentence of its body.
//
// compile.mjs writes the result to src/rules/caveman-activate.md (IDE rules via
// caveman-init.js, opencode AGENTS.md via bin/install.js) and into the
// RULE_BODY fallback of src/tools/caveman-init.js. Edit the skill or the tail
// below, never the copies; tests/installer/rule-copies.test.mjs fails on drift.

const SENTINEL = "Respond terse like smart caveman";

const TAIL = `Switch: /caveman (default), /ultracave (fragments, each fact once), /megacave (Classical Chinese 文言文)
Stop: "stop caveman" or "normal mode"

Auto-Clarity: plain prose for security warnings, irreversible actions, step order a fragment could scramble, user confused. Resume after.

Boundaries: code, comments, commits, PRs, docs written normal.
Floor: code, commands, paths, numbers and error strings verbatim; never drop not/never/no/only.
`;

export function activationRule(skill) {
  const lines = skill.split(/\r?\n/);
  const heading = lines.indexOf("# caveman");
  const thesis = heading < 0 ? undefined : lines.slice(heading + 1).find((line) => line.trim() !== "");
  const rules = [];
  lines.forEach((line, i) => {
    if (!/^### \d+\. /.test(line)) return;
    const body = lines.slice(i + 1).find((l) => l.trim() !== "" && !l.startsWith("#")) || "";
    const first = body.split(/(?<=\.)\s/)[0].trim();
    rules.push(`- ${line.replace(/^### \d+\. /, "")}${first ? `: ${first}` : ""}`);
  });
  if (!thesis || !thesis.startsWith(SENTINEL)) throw new Error(`caveman SKILL.md: thesis line after "# caveman" must start with "${SENTINEL}"`);
  if (rules.length === 0) throw new Error("caveman SKILL.md: no `### N. ` rule headlines found");
  return `${thesis}\n\nRules:\n${rules.join("\n")}\n\n${TAIL}`;
}

// Replace the `const RULE_BODY = \`...\`;` template literal in caveman-init.js.
export function embedRuleBody(source, body) {
  const literal = /^const RULE_BODY = `(?:\\[\s\S]|[^`\\])*`;$/m;
  if (!literal.test(source)) throw new Error("caveman-init.js: `const RULE_BODY = `...`;` not found");
  const escaped = body.replace(/[\\`]/g, "\\$&").replace(/\$\{/g, "\\${");
  return source.replace(literal, () => `const RULE_BODY = \`${escaped}\`;`);
}
