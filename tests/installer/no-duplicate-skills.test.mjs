import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const REPO_ROOT = path.resolve(path.dirname(__filename), '..', '..');

// Skill folders that must contain exactly one SKILL.md each, in exactly two
// locations: the canonical source under skills/<name>/ and the Claude Code
// plugin mirror under plugins/caveman/skills/<name>/ (CI-synced).
// Any additional SKILL.md outside those two trees is a stale mirror that
// causes Claude Code's plugin loader to register the same skill multiple
// times, dropping its description from auto-trigger (issue #351).
const ALLOWED_PREFIXES = [
  `${REPO_ROOT}/skills/`,
  `${REPO_ROOT}/plugins/caveman/skills/`,
];

const IGNORED_DIRS = new Set(['node_modules', '.git']);

function findSkillMd(dir, out = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (IGNORED_DIRS.has(entry.name)) continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      findSkillMd(full, out);
    } else if (entry.isFile() && entry.name === 'SKILL.md') {
      out.push(full);
    }
  }
  return out;
}

test('no SKILL.md exists outside skills/ and plugins/caveman/skills/ (issue #351)', () => {
  const found = findSkillMd(REPO_ROOT);
  const stray = found.filter(
    (p) => !ALLOWED_PREFIXES.some((prefix) => p.startsWith(prefix))
  );
  assert.deepEqual(
    stray,
    [],
    `Stale SKILL.md mirrors found — Claude Code plugin loader will register these skills twice and drop their descriptions:\n  ${stray.join('\n  ')}\n\nThese dotdir leftovers (.agents/.junie/.kiro/.roo/) predate the 2026-04 cleanup. CLAUDE.md says "remove on sight, no migration needed".`
  );
});

test('each skill name appears in at most one canonical source + one plugin mirror', () => {
  const found = findSkillMd(REPO_ROOT);
  const counts = new Map();
  for (const f of found) {
    const skillName = path.basename(path.dirname(f));
    counts.set(skillName, (counts.get(skillName) || 0) + 1);
  }
  const overCounted = [...counts.entries()].filter(([, n]) => n > 2);
  assert.deepEqual(
    overCounted,
    [],
    `Skills appearing more than 2× (canonical + plugin mirror):\n  ${overCounted
      .map(([name, n]) => `${name}: ${n} copies`)
      .join('\n  ')}`
  );
});
