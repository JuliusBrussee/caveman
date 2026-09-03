// Regression for #373: Gemini CLI validates agent frontmatter tool names.
//
// The extension loader reads repo-root agents/cavecrew-*.md directly, so
// Claude Code tool ids like Read/Edit/Bash make Gemini reject the whole agent.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const AGENTS_DIR = path.join(REPO_ROOT, 'agents');
const GEMINI_BUILTIN_TOOLS = new Set([
  'read_file',
  'replace',
  'write_file',
  'grep_search',
  'glob',
  'run_shell_command',
]);

function cavecrewAgentFiles() {
  return fs.readdirSync(AGENTS_DIR)
    .filter((name) => /^cavecrew-.*\.md$/.test(name))
    .map((name) => path.join(AGENTS_DIR, name))
    .sort();
}

function frontmatter(file) {
  const body = fs.readFileSync(file, 'utf8');
  const match = body.match(/^---\r?\n([\s\S]*?)\r?\n---/);
  assert.ok(match, `${path.relative(REPO_ROOT, file)} must start with closed YAML frontmatter`);
  return match[1];
}

function unquote(value) {
  return value.trim().replace(/^['"]|['"]$/g, '');
}

function parseTools(fm) {
  const lines = fm.split(/\r?\n/);
  for (let index = 0; index < lines.length; index++) {
    const line = lines[index];
    if (!line.startsWith('tools:')) continue;

    const value = line.split(':', 2)[1].trim();
    if (value.startsWith('[') && value.endsWith(']')) {
      const inner = value.slice(1, -1).trim();
      return inner ? inner.split(',').map(unquote) : [];
    }
    if (value) return [unquote(value)];

    const tools = [];
    for (const item of lines.slice(index + 1)) {
      if (!/^\s/.test(item)) break;
      const stripped = item.trim();
      if (stripped.startsWith('- ')) tools.push(unquote(stripped.slice(2)));
    }
    return tools;
  }
  return null;
}

test('#373 root cavecrew agents omit tools or use Gemini CLI tool ids', () => {
  const files = cavecrewAgentFiles();
  assert.equal(files.length, 3, 'expected exactly the three cavecrew root agents');

  for (const file of files) {
    const tools = parseTools(frontmatter(file));
    if (tools === null) continue;
    const invalid = tools.filter((tool) => tool !== '*' && !GEMINI_BUILTIN_TOOLS.has(tool));
    assert.deepEqual(
      invalid,
      [],
      `${path.relative(REPO_ROOT, file)} declares Gemini-invalid tools: ${invalid.join(', ')}`,
    );
  }
});
