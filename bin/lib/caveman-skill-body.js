'use strict';

const fs = require('fs');
const path = require('path');

function stripSkillFrontmatter(content) {
  return content.replace(/^---[\s\S]*?---\s*/, '').trimEnd() + '\n';
}

function skillBodyCandidates(repoRoot) {
  const candidates = [];
  if (process.env.CURSOR_PLUGIN_ROOT) {
    candidates.push(path.join(process.env.CURSOR_PLUGIN_ROOT, 'skills', 'caveman', 'SKILL.md'));
  }
  if (process.env.CLAUDE_PLUGIN_ROOT) {
    candidates.push(path.join(process.env.CLAUDE_PLUGIN_ROOT, 'skills', 'caveman', 'SKILL.md'));
  }
  if (repoRoot) {
    candidates.push(path.join(repoRoot, 'skills', 'caveman', 'SKILL.md'));
  }
  candidates.push(path.join(__dirname, '..', '..', 'skills', 'caveman', 'SKILL.md'));
  return candidates;
}

function loadRuleBody(repoRoot) {
  for (const candidate of skillBodyCandidates(repoRoot)) {
    try {
      if (fs.existsSync(candidate)) {
        return stripSkillFrontmatter(fs.readFileSync(candidate, 'utf8'));
      }
    } catch (_) { /* try next */ }
  }
  throw new Error(
    'caveman skill body not found (skills/caveman/SKILL.md). '
    + 'Run install from the repo root or use a package that ships skills/.',
  );
}

module.exports = {
  loadRuleBody,
  skillBodyCandidates,
  stripSkillFrontmatter,
};
