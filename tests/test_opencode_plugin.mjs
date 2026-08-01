// Tests for parseModeChange() in src/plugins/opencode/plugin.js.
//
// The opencode port kept a bare /\bnormal mode\b/ deactivation test and had no
// isQuestion guard on natural-language activation, so any prompt merely
// mentioning "normal mode" (vim's, for instance) silently turned caveman off,
// and "what is caveman mode?" silently turned it on. The canonical
// src/hooks/caveman-mode-tracker.js guards both — it anchors "normal mode" to a
// command position or caveman context, and skips activation on questions
// (#598). These cases pin the port to the same behavior.
//
// Run: node --test tests/test_opencode_plugin.mjs

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

// Isolate config lookup before importing the plugin: the flag path is resolved
// at module load from XDG_CONFIG_HOME, and getDefaultMode() consults the env
// var first, then repo-local and user config files. Pinning the env var keeps
// activation results deterministic wherever the suite runs.
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-opencode-'));
process.env.XDG_CONFIG_HOME = tmp;
process.env.HOME = tmp;
process.env.USERPROFILE = tmp;
process.env.CAVEMAN_DEFAULT_MODE = 'full';

const { parseModeChange } = await import('../src/plugins/opencode/plugin.js');

// Regressions — none of these is a caveman command, so none may change state.
const NO_CHANGE = [
  'how do I exit vim normal mode?',
  'How do I exit vim normal mode?',
  'in vim, normal mode is the default',
  'what is caveman mode?',
  'does caveman mode help?',
  'how does caveman mode work',
];

for (const prompt of NO_CHANGE) {
  test(`leaves state alone: ${prompt}`, () => {
    assert.equal(parseModeChange(prompt), null);
  });
}

// Deactivation that must keep working: "normal mode" in command position
// (optionally led by a switch-back verb) or anywhere alongside "caveman".
const DEACTIVATES = [
  'normal mode',
  'back to normal mode',
  'switch to normal mode',
  'switch back to normal mode',
  'return to normal mode',
  'put caveman in normal mode',
  'stop caveman',
  'turn off caveman',
];

for (const prompt of DEACTIVATES) {
  test(`deactivates: ${prompt}`, () => {
    assert.equal(parseModeChange(prompt), 'off');
  });
}

// Activation and slash commands must survive the isQuestion guard.
const ACTIVATES = [
  ['activate caveman', 'full'],
  ['talk like caveman', 'full'],
  ['caveman mode', 'full'],
  ['/caveman', 'full'],
  ['/caveman ultra', 'ultra'],
  ['activate caveman mode: full', 'full'],
  ['activate caveman mode: ultra', 'ultra'],
];

for (const [prompt, expected] of ACTIVATES) {
  test(`activates ${expected}: ${prompt}`, () => {
    assert.equal(parseModeChange(prompt), expected);
  });
}
