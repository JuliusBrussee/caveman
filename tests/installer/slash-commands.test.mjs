// Regression for #470 + #571: /caveman-* reports 'Unknown command' in Claude Code.
//
// Claude Code resolves a slash command by scanning commands/*.md (YAML
// frontmatter) BEFORE the UserPromptSubmit hook ever sees the prompt — it
// ignores commands/*.toml entirely (#571; TOML is the Gemini extension
// format). With no commands/<name>.md on disk, the chat input is rejected as
// "Unknown command" — the mode tracker's handlers in
// src/hooks/caveman-mode-tracker.js never get a chance to intercept.
//
// README.md and INSTALL.md advertise the /caveman-* slash commands, so every
// documented command MUST ship BOTH formats:
//   commands/<name>.md    — Claude Code plugin commands (#571)
//   commands/<name>.toml  — Gemini CLI extension commands (#470)
// This test pins that contract, plus checks the caveman-stats bodies actually
// trigger the hook regex (a description-only stub would still leave the
// feature broken).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const COMMANDS_DIR = path.join(REPO_ROOT, 'commands');
const STATS_TOML = path.join(COMMANDS_DIR, 'caveman-stats.toml');

// Mirrors the live regex in src/hooks/caveman-mode-tracker.js (the
// `statsMatch` line). Anything that fails this here would also fail in
// production, so the test stays representative if the hook regex shifts.
const HOOK_STATS_REGEX = /^\/caveman(?::caveman)?-stats(?:\s+(.*))?$/m;

test('#470 commands/caveman-stats.toml exists so Claude Code registers /caveman-stats', () => {
  assert.ok(
    fs.existsSync(STATS_TOML),
    `Missing ${path.relative(REPO_ROOT, STATS_TOML)} — Claude Code rejects /caveman-stats as "Unknown command" before the UserPromptSubmit hook can intercept (issue #470).`,
  );
});

test('#470 caveman-stats.toml declares a non-empty description for the slash-command picker', () => {
  const body = fs.readFileSync(STATS_TOML, 'utf8');
  const descMatch = body.match(/^\s*description\s*=\s*"([^"\n]+)"/m);
  assert.ok(descMatch, 'caveman-stats.toml must declare a description = "..." line');
  assert.ok(descMatch[1].trim().length > 0, 'description must not be empty');
});

test('#470 caveman-stats.toml prompt is intercepted by the mode-tracker regex', () => {
  const body = fs.readFileSync(STATS_TOML, 'utf8');
  const promptMatch = body.match(/^\s*prompt\s*=\s*"([^"\n]+)"/m);
  assert.ok(promptMatch, 'caveman-stats.toml must declare a prompt = "..." line');
  const prompt = promptMatch[1].replace(/\{\{args\}\}/g, '').trim();
  assert.match(
    prompt,
    HOOK_STATS_REGEX,
    `Resolved prompt ${JSON.stringify(prompt)} must match the UserPromptSubmit handler regex in src/hooks/caveman-mode-tracker.js; otherwise the stats output is never injected.`,
  );
});

// ── #571: Claude Code only discovers commands/*.md ─────────────────────────

// Every command documented for Claude Code. Each needs a .md (Claude Code)
// AND a .toml (Gemini extension) sibling — the formats coexist in commands/.
const DOCUMENTED_COMMANDS = ['caveman', 'caveman-commit', 'caveman-review', 'caveman-stats', 'caveman-init'];

for (const name of DOCUMENTED_COMMANDS) {
  test(`#571 commands/${name}.md exists so Claude Code registers /${name}`, () => {
    const mdPath = path.join(COMMANDS_DIR, `${name}.md`);
    assert.ok(
      fs.existsSync(mdPath),
      `Missing ${path.relative(REPO_ROOT, mdPath)} — Claude Code only scans commands/*.md, so /${name} is "Unknown command" without it (issue #571).`,
    );
  });

  test(`#571 commands/${name}.md has YAML frontmatter with a non-empty description`, () => {
    const body = fs.readFileSync(path.join(COMMANDS_DIR, `${name}.md`), 'utf8');
    assert.ok(body.startsWith('---\n'), `${name}.md must start with YAML frontmatter (---)`);
    const fm = body.match(/^---\n([\s\S]*?)\n---/);
    assert.ok(fm, `${name}.md frontmatter must be closed with ---`);
    const desc = fm[1].match(/^description:\s*(.+)$/m);
    assert.ok(desc && desc[1].trim().length > 0, `${name}.md must declare a non-empty description`);
  });

  test(`#571 commands/${name}.toml still ships for Gemini`, () => {
    assert.ok(
      fs.existsSync(path.join(COMMANDS_DIR, `${name}.toml`)),
      `commands/${name}.toml missing — Gemini CLI extensions only read TOML commands.`,
    );
  });
}

test('#571 caveman-stats.md body is intercepted by the mode-tracker regex', () => {
  const body = fs.readFileSync(path.join(COMMANDS_DIR, 'caveman-stats.md'), 'utf8');
  const prompt = body.replace(/^---\n[\s\S]*?\n---\n/, '').replace(/\$ARGUMENTS/g, '').trim();
  assert.match(
    prompt,
    HOOK_STATS_REGEX,
    `Resolved body ${JSON.stringify(prompt)} must match the UserPromptSubmit handler regex in src/hooks/caveman-mode-tracker.js; otherwise the stats output is never injected.`,
  );
});

test('#571 command .md bodies use $ARGUMENTS, never the TOML {{args}} placeholder', () => {
  for (const name of DOCUMENTED_COMMANDS) {
    const body = fs.readFileSync(path.join(COMMANDS_DIR, `${name}.md`), 'utf8');
    assert.ok(
      !body.includes('{{args}}'),
      `commands/${name}.md contains {{args}} — Claude Code substitutes $ARGUMENTS, {{args}} would reach the model verbatim.`,
    );
  }
});

// #603: the init command must not depend on a repo-relative path — installed
// users run it from their own project, where src/tools/ does not exist.
test('#603 caveman-init command bodies do not run src/tools blindly', () => {
  for (const ext of ['md', 'toml']) {
    const body = fs.readFileSync(path.join(COMMANDS_DIR, `caveman-init.${ext}`), 'utf8');
    if (body.includes('src/tools/caveman-init.js')) {
      assert.ok(
        /raw\.githubusercontent\.com.*caveman-init\.js/.test(body),
        `commands/caveman-init.${ext} references the repo-relative src/tools path without a standalone fallback (curl | node) — fails for every installed user (issue #603).`,
      );
      assert.match(
        body,
        /if .*exists|exists in the current repo/i,
        `commands/caveman-init.${ext} must gate the repo-relative path on the file actually existing (issue #603).`,
      );
    }
  }
});
