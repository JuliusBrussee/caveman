// Subagent frontmatter remapper for the Gemini CLI extension (issues #373/#473/#492).
//
// Gemini's ExtensionManager loads agents from a hardcoded `<ext>/agents/` path
// and validates each `tools:` entry against its own builtin tool ids. The
// shipped agents/cavecrew-*.md use Claude Code names (`Read`, `Edit`, `Bash`…),
// so Gemini rejects all three with:
//   [ExtensionManager] Error loading agent ...cavecrew-builder.md:
//     Validation failed: Agent Definition: tools.0: Invalid tool name ...
//
// Fix: rewrite the installed copy's tool names (and `model: haiku`) to Gemini
// equivalents. The source files stay Claude-canonical (shared with the Claude
// plugin). These tests prove the helper maps tool names + model, preserves
// every other frontmatter key, keeps the body byte-identical, and that the
// real shipped files transform to a Gemini-valid form.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const requireCjs = createRequire(import.meta.url);
const { mapGeminiAgentFrontmatter, TOOL_MAP, geminiExtAgentsDir } = requireCjs(
  path.join(REPO_ROOT, 'bin', 'lib', 'gemini-agent.js'),
);
const osmod = requireCjs('os');

const SHIPPED_AGENT_FILES = ['cavecrew-investigator.md', 'cavecrew-builder.md', 'cavecrew-reviewer.md'];

// Gemini's ALL_BUILTIN_TOOL_NAMES (subset relevant to cavecrew), verified
// against the @google/gemini-cli bundle.
const GEMINI_BUILTIN = new Set([
  'read_file', 'write_file', 'replace', 'grep_search', 'glob',
  'run_shell_command', 'read_many_files', 'web_fetch', 'google_web_search',
  'list_directory',
]);

function frontmatter(content) {
  const m = content.match(/^---\n([\s\S]*?)\n---\n/);
  assert.ok(m, 'frontmatter present');
  return m[1];
}

function toolsArray(fm) {
  const m = fm.match(/^tools:\s*\[([^\]]*)\]/m);
  assert.ok(m, 'tools array present');
  return m[1].split(',').map((s) => s.trim()).filter(Boolean);
}

// ── Inline array form (the exact bug reported) ───────────────────────────
test('maps inline `tools: [...]` Claude names to Gemini ids', () => {
  const src = `---
name: test-agent
description: short description
tools: [Read, Edit, Write, Grep, Glob]
model: haiku
---
body line one
body line two
`;
  const out = mapGeminiAgentFrontmatter(src);
  const fm = frontmatter(out);
  assert.deepEqual(toolsArray(fm), ['read_file', 'replace', 'write_file', 'grep_search', 'glob']);
  assert.match(fm, /^name: test-agent$/m, '`name` preserved');
  assert.match(fm, /^description: short description$/m, '`description` preserved');
  assert.match(fm, /^model: gemini-2.5-flash$/m, '`model: haiku` remapped to a Gemini model');
  assert.match(out, /^body line one$/m, 'body preserved');
  assert.match(out, /^body line two$/m, 'body preserved');
});

// ── Bash maps to run_shell_command ───────────────────────────────────────
test('maps Bash to run_shell_command', () => {
  const src = `---
name: x
tools: [Read, Grep, Bash]
---
body
`;
  assert.deepEqual(toolsArray(frontmatter(mapGeminiAgentFrontmatter(src))),
    ['read_file', 'grep_search', 'run_shell_command']);
});

// ── Folded `description: >` block must NOT be touched ─────────────────────
test('preserves folded `description: >` continuation lines', () => {
  const src = `---
name: cavecrew-reviewer
description: >
  Diff/branch/file reviewer. One line per finding, severity-tagged, no praise,
  no scope creep.
tools: [Read, Grep, Bash]
model: haiku
---
body
`;
  const fm = frontmatter(mapGeminiAgentFrontmatter(src));
  assert.match(fm, /^description: >$/m, 'folded scalar header preserved');
  assert.match(fm, /Diff\/branch\/file reviewer/, 'folded scalar body preserved');
  assert.match(fm, /no scope creep/, 'second folded line preserved');
});

// ── Unknown tool names pass through (not silently dropped) ────────────────
test('passes unknown tool names through unchanged', () => {
  const src = `---
name: x
tools: [Read, SomeFutureTool]
---
body
`;
  assert.deepEqual(toolsArray(frontmatter(mapGeminiAgentFrontmatter(src))),
    ['read_file', 'SomeFutureTool']);
});

// ── No frontmatter / no tools: pass content through untouched ─────────────
test('returns input unchanged when no frontmatter fence', () => {
  const src = 'just body, no frontmatter\ntools: [Read]\n';
  assert.equal(mapGeminiAgentFrontmatter(src), src);
});

test('leaves frontmatter without a `tools:` field structurally intact', () => {
  const src = `---
name: x
model: sonnet
---
body
`;
  const out = mapGeminiAgentFrontmatter(src);
  assert.match(frontmatter(out), /^model: gemini-2.5-pro$/m, 'known model still remapped');
});

// ── Non-string input: pass through (defensive) ───────────────────────────
test('non-string input returns unchanged', () => {
  assert.equal(mapGeminiAgentFrontmatter(null), null);
  assert.equal(mapGeminiAgentFrontmatter(undefined), undefined);
  assert.deepEqual(mapGeminiAgentFrontmatter({ x: 1 }), { x: 1 });
});

// ── RED proof: shipped files contain Claude tool names today ─────────────
test('all shipped cavecrew agent files contain Claude tool names (RED proof)', () => {
  for (const f of SHIPPED_AGENT_FILES) {
    const fm = frontmatter(fs.readFileSync(path.join(REPO_ROOT, 'agents', f), 'utf8'));
    const tools = toolsArray(fm);
    const claudeNames = tools.filter((t) => Object.prototype.hasOwnProperty.call(TOOL_MAP, t));
    assert.ok(claudeNames.length > 0, `source ${f} should contain Claude tool names (this is the bug)`);
  }
});

// ── GREEN proof: every shipped file becomes Gemini-valid after transform ──
test('all shipped cavecrew agent files become Gemini-valid after transform (GREEN proof)', () => {
  for (const f of SHIPPED_AGENT_FILES) {
    const src = fs.readFileSync(path.join(REPO_ROOT, 'agents', f), 'utf8');
    const out = mapGeminiAgentFrontmatter(src);
    const fm = frontmatter(out);

    for (const t of toolsArray(fm)) {
      assert.ok(GEMINI_BUILTIN.has(t), `${f}: tool "${t}" is not a Gemini builtin id`);
    }
    assert.doesNotMatch(fm, /^model:\s*haiku$/m, `${f}: model: haiku must be remapped`);
    assert.match(fm, /^name: cavecrew-/m, `${f}: name field preserved`);

    const bodyOut = out.replace(/^---\n[\s\S]*?\n---\n/, '');
    const bodyIn = src.replace(/^---\n[\s\S]*?\n---\n/, '');
    assert.equal(bodyOut, bodyIn, `${f}: body must be byte-identical`);
  }
});

// ── GEMINI_CLI_HOME override (issue #530 codex review) ────────────────────
test('geminiExtAgentsDir honors GEMINI_CLI_HOME env', () => {
  const orig = process.env.GEMINI_CLI_HOME;
  process.env.GEMINI_CLI_HOME = '/tmp/__cm_gemini_home';
  try {
    assert.equal(
      geminiExtAgentsDir(),
      path.join('/tmp/__cm_gemini_home', '.gemini', 'extensions', 'caveman', 'agents'),
    );
  } finally {
    if (orig === undefined) delete process.env.GEMINI_CLI_HOME;
    else process.env.GEMINI_CLI_HOME = orig;
  }
});

test('geminiExtAgentsDir falls back to os.homedir() when GEMINI_CLI_HOME unset', () => {
  const orig = process.env.GEMINI_CLI_HOME;
  delete process.env.GEMINI_CLI_HOME;
  try {
    assert.equal(
      geminiExtAgentsDir(),
      path.join(osmod.homedir(), '.gemini', 'extensions', 'caveman', 'agents'),
    );
  } finally {
    if (orig !== undefined) process.env.GEMINI_CLI_HOME = orig;
  }
});

// ── End-to-end: installer-equivalent copy writes a Gemini-valid file ──────
test('installer-equivalent copy writes Gemini-valid agent files (end-to-end)', () => {
  const tmpDir = fs.mkdtempSync(path.join(REPO_ROOT, 'tests', '.tmp-gemini-agent-'));
  try {
    for (const f of SHIPPED_AGENT_FILES) {
      const src = path.join(REPO_ROOT, 'agents', f);
      const dest = path.join(tmpDir, f);
      fs.writeFileSync(dest, mapGeminiAgentFrontmatter(fs.readFileSync(src, 'utf8')));

      const fm = frontmatter(fs.readFileSync(dest, 'utf8'));
      for (const t of toolsArray(fm)) {
        assert.ok(GEMINI_BUILTIN.has(t), `${f}: invalid tool "${t}" survived in installed file`);
      }
    }
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});
