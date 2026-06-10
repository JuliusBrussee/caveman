// Regression for #470: /caveman-stats reports 'Unknown command' in Claude Code.
//
// Root cause: Claude Code resolves a slash command by scanning commands/*.toml
// BEFORE the UserPromptSubmit hook ever sees the prompt. With no
// commands/caveman-stats.toml on disk, the chat input is rejected as
// "Unknown command: /caveman-stats" — the mode tracker's stats handler in
// src/hooks/caveman-mode-tracker.js never gets a chance to intercept.
//
// README.md and INSTALL.md both advertise /caveman-stats as a usable slash
// command, so every documented Claude Code command MUST ship a matching
// commands/<name>.toml. This test pins that contract for /caveman-stats
// specifically, plus checks the toml body actually triggers the hook regex
// (a description-only stub would still leave the feature broken).

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
