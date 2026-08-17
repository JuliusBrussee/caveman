// Subagent frontmatter sanitizer for opencode (issue #386).
//
// opencode rejects the YAML array form `tools: [Read, Grep, Bash]` that
// Claude Code accepts. Copying agents/cavecrew-*.md verbatim into
// ~/.config/opencode/agents/ broke opencode startup with:
//   Configuration is invalid at .../cavecrew-reviewer.md
//   ↳ Expected object | undefined, got ["Read","Grep","Bash"] tools
//
// Fix: strip the `tools:` field on copy. These tests prove the helper
// strips the field, preserves every other frontmatter key and the body,
// and handles both the inline array form and the multi-line YAML list form.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const requireCjs = createRequire(import.meta.url);
const { transformOpencodeAgentFrontmatter } = requireCjs(path.join(REPO_ROOT, 'bin', 'lib', 'opencode-agent.js'));

const SHIPPED_AGENT_FILES = ['cavecrew-investigator.md', 'cavecrew-builder.md', 'cavecrew-reviewer.md'];

function frontmatter(content) {
  const m = content.match(/^---\n([\s\S]*?)\n---\n/);
  assert.ok(m, 'frontmatter present');
  return m[1];
}

// ── Inline array form (the exact bug reported in issue 386) ──────────────
test('strips inline `tools: [...]` array from frontmatter', () => {
  const src = `---
name: test-agent
description: short description
tools: [Read, Grep, Bash]
model: haiku
---
body line one
body line two
`;
  const out = transformOpencodeAgentFrontmatter(src);
  const fm = frontmatter(out);
  assert.doesNotMatch(fm, /^tools:/m, '`tools` field must be absent');
  assert.match(fm, /^name: test-agent$/m, '`name` preserved');
  assert.match(fm, /^description: short description$/m, '`description` preserved');
  assert.doesNotMatch(fm, /^model:/m, 'bare Claude alias must be dropped (#840)');
  assert.match(out, /^body line one$/m, 'body preserved');
  assert.match(out, /^body line two$/m, 'body preserved');
});

// ── Multi-line YAML list form (defensive — future-proof for refactors) ───
test('strips multi-line `tools:` list with indented continuation', () => {
  const src = `---
name: test-agent
tools:
  - Read
  - Grep
  - Bash
model: haiku
---
body
`;
  const out = transformOpencodeAgentFrontmatter(src);
  const fm = frontmatter(out);
  assert.doesNotMatch(fm, /^tools:/m, '`tools` field must be absent');
  assert.doesNotMatch(fm, /^\s+- Read$/m, '`tools` list items must be absent');
  assert.match(fm, /^name: test-agent$/m, '`name` preserved');
  assert.doesNotMatch(fm, /^model:/m, 'bare Claude alias must be dropped (#840)');
});

// ── Folded `description: >` block must NOT be eaten ──────────────────────
test('preserves folded `description: >` continuation lines when `tools:` follows', () => {
  const src = `---
name: cavecrew-reviewer
description: >
  Diff/branch/file reviewer. One line per finding, severity-tagged, no praise,
  no scope creep. Output format \`path:line: <emoji> <severity>: <problem>. <fix>.\`
tools: [Read, Grep, Bash]
model: haiku
---
body
`;
  const out = transformOpencodeAgentFrontmatter(src);
  const fm = frontmatter(out);
  assert.doesNotMatch(fm, /^tools:/m);
  assert.match(fm, /^description: >$/m, 'folded scalar header preserved');
  assert.match(fm, /Diff\/branch\/file reviewer/, 'folded scalar body preserved');
  assert.match(fm, /no scope creep/, 'second folded line preserved');
  assert.doesNotMatch(fm, /^model:/m, 'bare Claude alias must be dropped (#840)');
});

// ── No frontmatter: pass content through untouched ───────────────────────
test('returns input unchanged when no frontmatter fence', () => {
  const src = 'just body, no frontmatter\ntools: [Read]\n';
  assert.equal(transformOpencodeAgentFrontmatter(src), src);
});

// ── Nothing to transform: pass content through untouched ────────────────
test('returns input unchanged when frontmatter has no `tools:` and a valid model', () => {
  const src = `---
name: x
model: anthropic/claude-haiku-4-5
---
body
`;
  assert.equal(transformOpencodeAgentFrontmatter(src), src);
});

// ── Non-string input: pass through (defensive) ───────────────────────────
test('non-string input returns unchanged', () => {
  assert.equal(transformOpencodeAgentFrontmatter(null), null);
  assert.equal(transformOpencodeAgentFrontmatter(undefined), undefined);
  assert.deepEqual(transformOpencodeAgentFrontmatter({ x: 1 }), { x: 1 });
});

// ── Real shipped agent files: every one must transform to opencode-safe ──
// This is the RED-state proof: each `agents/cavecrew-*.md` in the repo today
// contains the offending `tools: [...]` form, which is what broke opencode
// startup in the reported bug. After transform, the field is gone.
test('all shipped cavecrew agent files contain offending tools array (RED proof)', () => {
  for (const f of SHIPPED_AGENT_FILES) {
    const src = fs.readFileSync(path.join(REPO_ROOT, 'agents', f), 'utf8');
    const fm = frontmatter(src);
    assert.match(fm, /^tools:\s*\[/m, `source ${f} should contain inline array form (this is the bug)`);
  }
});

test('all shipped cavecrew agent files become opencode-safe after transform (GREEN proof)', () => {
  for (const f of SHIPPED_AGENT_FILES) {
    const src = fs.readFileSync(path.join(REPO_ROOT, 'agents', f), 'utf8');
    const out = transformOpencodeAgentFrontmatter(src);
    const fm = frontmatter(out);

    assert.doesNotMatch(fm, /^tools:/m, `${f}: tools field still present after transform`);
    assert.match(fm, /^name: cavecrew-/m, `${f}: name field preserved`);
    assert.match(fm, /^description:/m, `${f}: description field preserved`);

    const bodyOut = out.replace(/^---\n[\s\S]*?\n---\n/, '');
    const bodyIn = src.replace(/^---\n[\s\S]*?\n---\n/, '');
    assert.equal(bodyOut, bodyIn, `${f}: body must be byte-identical`);
  }
});

// ── End-to-end: installer's agent-copy step writes a sanitized file ──────
// We re-enact section 3 of installOpencode() directly to avoid coupling to
// the rest of the install pipeline (which depends on optional files outside
// this fix's scope). The assertion is the same one opencode applies on
// startup: `tools` must be absent (or an object), never an array.
test('installer-equivalent copy writes opencode-safe agent file (issue 386 end-to-end)', () => {
  const tmpDir = fs.mkdtempSync(path.join(REPO_ROOT, 'tests', '.tmp-opencode-agent-'));
  try {
    for (const f of SHIPPED_AGENT_FILES) {
      const src = path.join(REPO_ROOT, 'agents', f);
      const dest = path.join(tmpDir, f);
      fs.writeFileSync(dest, transformOpencodeAgentFrontmatter(fs.readFileSync(src, 'utf8')));

      const installed = fs.readFileSync(dest, 'utf8');
      const fm = frontmatter(installed);
      assert.doesNotMatch(fm, /^tools:\s*\[/m, `${f}: array form survived in installed file`);
      assert.doesNotMatch(fm, /^tools:/m, `${f}: tools field survived in installed file`);
    }
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});

// ── Bare Claude model aliases (issue 840) ────────────────────────────────
// opencode 1.18.18 parses `model: haiku` as provider `haiku` plus an empty
// model ID, then fails at runtime with `Model not found: haiku/`.
test('drops every bare Claude model alias', () => {
  for (const alias of ['haiku', 'sonnet', 'opus']) {
    const src = `---\nname: a\nmodel: ${alias}\n---\nbody\n`;
    assert.doesNotMatch(
      frontmatter(transformOpencodeAgentFrontmatter(src)), /^model:/m,
      `\`model: ${alias}\` must be dropped`,
    );
  }
});

test('drops a quoted bare alias', () => {
  for (const src of ['---\nname: a\nmodel: "haiku"\n---\nbody\n', "---\nname: a\nmodel: 'haiku'\n---\nbody\n"]) {
    assert.doesNotMatch(frontmatter(transformOpencodeAgentFrontmatter(src)), /^model:/m, src);
  }
});

test('drops a bare alias with trailing whitespace', () => {
  const src = '---\nname: a\nmodel:   haiku  \n---\nbody\n';
  assert.doesNotMatch(frontmatter(transformOpencodeAgentFrontmatter(src)), /^model:/m);
});

test('leaves a slash-qualified model id byte-identical', () => {
  for (const id of ['anthropic/claude-haiku-4-5', 'openai/gpt-5', 'github-copilot/claude-sonnet-4']) {
    const src = `---\nname: a\nmodel: ${id}\n---\nbody\n`;
    assert.equal(transformOpencodeAgentFrontmatter(src), src, id);
  }
});

// The invariant is the SHAPE, not a list of known aliases: every one of these
// is valid Claude Code frontmatter and every one produces `Model not found:
// <x>/` in opencode.
test('drops any provider-less model value, not just the three aliases', () => {
  for (const value of ['inherit', 'claude-haiku-4-5-20251001', 'sonnet[1m]', 'haiku-custom']) {
    const src = `---\nname: a\nmodel: ${value}\n---\nbody\n`;
    assert.doesNotMatch(
      frontmatter(transformOpencodeAgentFrontmatter(src)), /^model:/m,
      `\`model: ${value}\` has no provider prefix and must be dropped`,
    );
  }
});

test('drops a bare alias carrying a trailing YAML comment', () => {
  const src = '---\nname: a\nmodel: haiku # cheap\n---\nbody\n';
  assert.doesNotMatch(frontmatter(transformOpencodeAgentFrontmatter(src)), /^model:/m);
});

test('keeps a provider-qualified id that carries a comment', () => {
  const src = '---\nname: a\nmodel: anthropic/claude-haiku-4-5 # cheap\n---\nbody\n';
  assert.equal(transformOpencodeAgentFrontmatter(src), src);
});

test('leaves malformed empty model alone rather than silently repairing it', () => {
  const src = '---\nname: a\nmodel:\n---\nbody\n';
  assert.equal(transformOpencodeAgentFrontmatter(src), src);
});

test('does not touch a `model:` mention in the body', () => {
  const src = '---\nname: a\n---\nSet model: haiku in Claude Code.\n';
  assert.equal(transformOpencodeAgentFrontmatter(src), src);
});

// ── The shipped cavecrew agents must survive the transform ───────────────
test('shipped cavecrew agents carry no bare alias after transform', () => {
  const agentsDir = path.join(REPO_ROOT, 'agents');
  const files = fs.readdirSync(agentsDir).filter(f => f.startsWith('cavecrew-') && f.endsWith('.md'));
  assert.ok(files.length > 0, 'expected cavecrew agent files');
  for (const file of files) {
    const out = transformOpencodeAgentFrontmatter(fs.readFileSync(path.join(agentsDir, file), 'utf8'));
    const fm = frontmatter(out);
    assert.doesNotMatch(fm, /^tools:/m, `${file}: tools array must be gone`);
    const model = /^model:[ \t]*(.*)$/m.exec(fm);
    if (model) {
      assert.match(model[1], /\//, `${file}: any surviving model must be provider-qualified, got ${model[1]}`);
    }
  }
});
