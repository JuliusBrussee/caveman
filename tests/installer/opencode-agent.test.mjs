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
const { stripOpencodeAgentTools } = requireCjs(path.join(REPO_ROOT, 'bin', 'lib', 'opencode-agent.js'));

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
  const out = stripOpencodeAgentTools(src);
  const fm = frontmatter(out);
  assert.doesNotMatch(fm, /^tools:/m, '`tools` field must be absent');
  assert.match(fm, /^name: test-agent$/m, '`name` preserved');
  assert.match(fm, /^description: short description$/m, '`description` preserved');
  assert.match(fm, /^model: haiku$/m, '`model` preserved');
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
  const out = stripOpencodeAgentTools(src);
  const fm = frontmatter(out);
  assert.doesNotMatch(fm, /^tools:/m, '`tools` field must be absent');
  assert.doesNotMatch(fm, /^\s+- Read$/m, '`tools` list items must be absent');
  assert.match(fm, /^name: test-agent$/m, '`name` preserved');
  assert.match(fm, /^model: haiku$/m, '`model` preserved');
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
  const out = stripOpencodeAgentTools(src);
  const fm = frontmatter(out);
  assert.doesNotMatch(fm, /^tools:/m);
  assert.match(fm, /^description: >$/m, 'folded scalar header preserved');
  assert.match(fm, /Diff\/branch\/file reviewer/, 'folded scalar body preserved');
  assert.match(fm, /no scope creep/, 'second folded line preserved');
  assert.match(fm, /^model: haiku$/m);
});

// ── No frontmatter: pass content through untouched ───────────────────────
test('returns input unchanged when no frontmatter fence', () => {
  const src = 'just body, no frontmatter\ntools: [Read]\n';
  assert.equal(stripOpencodeAgentTools(src), src);
});

// ── No `tools:` field: pass content through untouched ────────────────────
test('returns input unchanged when frontmatter has no `tools:` field', () => {
  const src = `---
name: x
model: haiku
---
body
`;
  assert.equal(stripOpencodeAgentTools(src), src);
});

// ── Non-string input: pass through (defensive) ───────────────────────────
test('non-string input returns unchanged', () => {
  assert.equal(stripOpencodeAgentTools(null), null);
  assert.equal(stripOpencodeAgentTools(undefined), undefined);
  assert.deepEqual(stripOpencodeAgentTools({ x: 1 }), { x: 1 });
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
    const out = stripOpencodeAgentTools(src);
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
      fs.writeFileSync(dest, stripOpencodeAgentTools(fs.readFileSync(src, 'utf8')));

      const installed = fs.readFileSync(dest, 'utf8');
      const fm = frontmatter(installed);
      assert.doesNotMatch(fm, /^tools:\s*\[/m, `${f}: array form survived in installed file`);
      assert.doesNotMatch(fm, /^tools:/m, `${f}: tools field survived in installed file`);
    }
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});
