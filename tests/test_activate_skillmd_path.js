#!/usr/bin/env node
// Tests for the SessionStart activation hook resolving SKILL.md after the
// src/ restructure (issue #467). caveman-activate.js lives at src/hooks/, so
// SKILL.md is two levels up at ../../skills/caveman/SKILL.md. A stale ../skills
// path silently fell back to the weak hardcoded ruleset.
//
// RED/GREEN: reverting the candidate path to only ../skills makes
// `activation_loads_full_skill` fail — the output drops to the fallback, which
// has no `## Intensity` table.
//
// Run: node tests/test_activate_skillmd_path.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');
const { execFileSync } = require('child_process');

const REPO_ROOT = path.join(__dirname, '..');
const HOOK = path.join(REPO_ROOT, 'src', 'hooks', 'caveman-activate.js');
// `## Intensity` + the level table exist only in skills/caveman/SKILL.md, never
// in the hardcoded fallback — so they prove the real file was loaded.
const SKILL_ONLY_MARKER = '## Intensity';

let passed = 0;
let failed = 0;

function runHook(mode) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-activate-test-'));
  try {
    return execFileSync('node', [HOOK], {
      encoding: 'utf8',
      env: { ...process.env, CAVEMAN_DEFAULT_MODE: mode, CLAUDE_CONFIG_DIR: tmp },
    });
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}`);
    console.error(`    ${e.message}`);
  }
}

test('activation loads full SKILL.md (not the weak fallback)', () => {
  const out = runHook('full');
  assert.ok(out.includes('CAVEMAN MODE ACTIVE — level: full'), 'missing activation header');
  assert.ok(
    out.includes(SKILL_ONLY_MARKER),
    `expected SKILL.md content ("${SKILL_ONLY_MARKER}"), got fallback:\n${out}`
  );
});

test('intensity table is filtered to the active level', () => {
  const out = runHook('full');
  assert.ok(out.includes('| **full** |'), 'active level row should be kept');
  assert.ok(!out.includes('| **lite** |'), 'inactive level rows should be filtered out');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);
