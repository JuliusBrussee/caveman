// The Claude Code plugin is addressed as `<plugin>@<marketplace>`, and both
// halves happen to be named "caveman". That makes the unqualified name look
// right and fail:
//
//   $ claude plugin update caveman
//   × Failed to update plugin "caveman": Plugin "caveman" not found
//   $ claude plugin update caveman@caveman
//   √ Plugin "caveman" updated ...
//
// That is #321, and the reporter only got there by guessing. The install table
// already uses the qualified form; nothing documented an update at all, which
// is also the backstory of #107 ("Updates are not mentioned in the README").
//
// CLAUDE.md treats a broken install command as a real cost to a real user, so
// these tests derive the correct spelling from the marketplace manifest rather
// than hardcoding it, and fail the build if a doc ever prints a plugin command
// that the shipped manifests cannot resolve.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');

const read = (rel) => fs.readFileSync(path.join(REPO_ROOT, rel), 'utf8');

// `<plugin>@<marketplace>` — the form `claude plugin install/update` resolves.
function qualifiedPluginName() {
  const marketplace = JSON.parse(read('.claude-plugin/marketplace.json'));
  assert.ok(marketplace.name, 'marketplace.json declares no name');
  const plugin = (marketplace.plugins || [])[0];
  assert.ok(plugin?.name, 'marketplace.json lists no plugin');
  return `${plugin.name}@${marketplace.name}`;
}

const DOCS = ['README.md', 'INSTALL.md'];

// Only fenced shell blocks are checked: those are what a reader copies. #321's
// broken spelling is quoted in the prose of INSTALL.md on purpose, as the trap
// to recognise, and must stay quotable there.
function fencedShellBlocks(text) {
  // \r? — a CRLF checkout on the Windows runner puts \r before the newline,
  // and an LF-only fence pattern silently matches nothing there. The
  // found-at-least-one assertion below is what turns that into a failure
  // instead of a vacuous pass.
  return [...text.matchAll(/```(?:bash|sh|shell|console)\r?\n([\s\S]*?)```/g)].map((m) => m[1]);
}

// Any `claude plugin install|update <arg>` a reader can copy must name the
// plugin the way the host resolves it. The unqualified name is the #321 trap.
test('documented claude plugin commands use the marketplace-qualified name', () => {
  const qualified = qualifiedPluginName();
  let found = 0;
  for (const rel of DOCS) {
    for (const block of fencedShellBlocks(read(rel))) {
      for (const m of block.matchAll(/claude plugin (install|update) (\S+)/g)) {
        found += 1;
        assert.equal(
          m[2].replace(/[`'"]+$/, ''),
          qualified,
          `${rel}: \`claude plugin ${m[1]} ${m[2]}\` does not resolve — use ${qualified} (#321)`
        );
      }
    }
  }
  assert.ok(found > 0, 'no copyable claude plugin commands found in the docs at all');
});

// A user who installed from the table has no documented way back to a newer
// version. #321 and #107 are both that gap.
test('INSTALL.md documents how to update the Claude Code plugin', () => {
  const text = read('INSTALL.md');
  assert.match(
    text,
    /claude plugin update \S+/,
    'INSTALL.md documents no `claude plugin update` command (#321)'
  );
});
