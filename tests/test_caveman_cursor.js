#!/usr/bin/env node
// Tests for bin/lib/cursor.js — Cursor hook installer helpers.

'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');
const {
  mergeCursorHooks,
  hooksJsonHasCaveman,
  installCursorHooks,
  uninstallCursorHooks,
} = require('../bin/lib/cursor.js');

let passed = 0;
let failed = 0;

function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}\n    ${e.message}`);
  }
}

console.log('cursor hook installer tests\n');

test('mergeCursorHooks adds sessionStart + beforeSubmitPrompt entries', () => {
  const merged = mergeCursorHooks({ version: 1, hooks: { stop: [{ command: './hooks/other.js' }] } });
  assert.strictEqual(merged.hooks.sessionStart[0].command, './hooks/caveman-session-start.js');
  assert.strictEqual(merged.hooks.beforeSubmitPrompt[0].command, './hooks/caveman-prompt-submit.js');
  assert.strictEqual(merged.hooks.stop[0].command, './hooks/other.js');
});

test('hooksJsonHasCaveman detects adapter command', () => {
  assert.strictEqual(hooksJsonHasCaveman(mergeCursorHooks({})), true);
  assert.strictEqual(hooksJsonHasCaveman({ version: 1, hooks: {} }), false);
});

test('install + uninstall round-trip in temp HOME', () => {
  const tmpHome = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-test-'));
  const prevHome = process.env.HOME;
  process.env.HOME = tmpHome;
  try {
    const repoRoot = path.resolve(__dirname, '..');
    const log = { note: () => {} };
    const r = installCursorHooks({ repoRoot, dryRun: false, log });
    assert.strictEqual(r.kind, 'ok');
    assert.ok(fs.existsSync(path.join(tmpHome, '.cursor/hooks/caveman-activate.js')));
    assert.ok(fs.existsSync(path.join(tmpHome, '.cursor/hooks/caveman-session-start.js')));
    const hooksJson = JSON.parse(fs.readFileSync(path.join(tmpHome, '.cursor/hooks.json'), 'utf8'));
    assert.ok(hooksJsonHasCaveman(hooksJson));
    const cli = JSON.parse(fs.readFileSync(path.join(tmpHome, '.cursor/cli-config.json'), 'utf8'));
    assert.ok(cli.statusLine && cli.statusLine.command.includes('caveman-statusline.sh'));
    uninstallCursorHooks({ dryRun: false, log });
    assert.ok(!fs.existsSync(path.join(tmpHome, '.cursor/hooks/caveman-activate.js')));
  } finally {
    process.env.HOME = prevHome;
    fs.rmSync(tmpHome, { recursive: true, force: true });
  }
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
