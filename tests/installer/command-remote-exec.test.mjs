// #1525: `/caveman:caveman-init` told the agent to pipe an unpinned branch into
// node.
//
//   curl -fsSL https://raw.githubusercontent.com/.../main/src/tools/caveman-init.js | node -
//
// That is the pattern #627 deleted from installer/install.js, left behind in the
// two command stubs. It matters most for the people who are most careful: a team
// that reviews caveman and pins its marketplace entry to a commit SHA still
// executed whatever was on `main` at call time, because step 1 looks for the
// script in the USER's repo and a plugin install is not a caveman checkout, so
// every plugin user fell through to the download.
//
// The plugin root already ships src/tools/caveman-init.js — marketplace.json
// sets "source": "./", so the plugin root IS the repo root — so the pinned copy
// was there to run all along.
//
// These tests guard the shape, not the wording: no shipped stub may hand a
// remote URL to an interpreter, and the .md must reach for the plugin's own copy
// before it gives up.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const COMMANDS_DIR = path.join(REPO_ROOT, 'commands');

const stubs = fs.readdirSync(COMMANDS_DIR)
  .filter((f) => f.endsWith('.md') || f.endsWith('.toml'))
  .map((f) => ({ name: f, text: fs.readFileSync(path.join(COMMANDS_DIR, f), 'utf8') }));

test('#1525 no command stub pipes a downloaded script into an interpreter', () => {
  assert.ok(stubs.length, 'expected command stubs to scan');
  // A fetch whose output reaches node/sh/bash/python, piped or substituted.
  const pipeToInterpreter = /(?:curl|wget|iwr|Invoke-WebRequest)[^\n`]*\|\s*(?:node|sh|bash|zsh|python3?|pwsh|powershell)\b/i;
  for (const { name, text } of stubs) {
    assert.doesNotMatch(text, pipeToInterpreter,
      `${name} pipes a download into an interpreter; ship the file and run the local copy instead (#627, #1525)`);
  }
});

test('#1525 no command stub names a mutable ref of this repo as a code source', () => {
  // A branch URL is not pinnable: the reviewed SHA says nothing about what it
  // serves at call time. Blobs under src/ are code; a docs link is not.
  const mutableCodeRef = /raw\.githubusercontent\.com\/JuliusBrussee\/caveman\/(?:main|master|HEAD)\/[^\s`"']*\.(?:js|mjs|cjs|sh|ps1|py)/i;
  for (const { name, text } of stubs) {
    assert.doesNotMatch(text, mutableCodeRef,
      `${name} sources executable code from a mutable ref; a pinned install must stay pinned (#1525)`);
  }
});

test('#1525 caveman-init.md runs the plugin copy before giving up', () => {
  const md = stubs.find((s) => s.name === 'caveman-init.md');
  assert.ok(md, 'commands/caveman-init.md must exist');
  assert.match(md.text, /\$\{?CLAUDE_PLUGIN_ROOT\}?[^\n]*src\/tools\/caveman-init\.js/,
    'caveman-init.md must offer ${CLAUDE_PLUGIN_ROOT}/src/tools/caveman-init.js (#803, #1525)');
  // Order matters: the repo checkout wins, then the plugin, then the fallback.
  const repoStep = md.text.indexOf('node src/tools/caveman-init.js');
  const pluginStep = md.text.indexOf('CLAUDE_PLUGIN_ROOT');
  assert.ok(repoStep >= 0 && pluginStep > repoStep,
    'the in-repo script must be tried before the plugin copy');
});

test('#1525 the plugin root really ships the script the stub reaches for', () => {
  // marketplace.json pins the plugin source; the stub's path is relative to it.
  const marketplace = JSON.parse(fs.readFileSync(
    path.join(REPO_ROOT, '.claude-plugin', 'marketplace.json'), 'utf8'));
  const entry = marketplace.plugins.find((p) => p.name === 'caveman');
  assert.ok(entry, 'marketplace.json must declare the caveman plugin');
  const pluginRoot = path.resolve(REPO_ROOT, entry.source);
  assert.ok(fs.statSync(path.join(pluginRoot, 'src', 'tools', 'caveman-init.js')).isFile(),
    'the plugin root must ship src/tools/caveman-init.js for the stub to resolve');
  // And npm must ship it too, so an npx install has the same local copy.
  const pkg = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, 'package.json'), 'utf8'));
  assert.ok(pkg.files.some((f) => f === 'src/' || f.startsWith('src/tools')),
    'package.json files[] must ship src/ so the installed copy carries caveman-init.js');
});
