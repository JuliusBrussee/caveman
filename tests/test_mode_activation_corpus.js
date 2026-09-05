#!/usr/bin/env node
// Table-driven regression corpus for mode-tracker natural-language activation (#187, #672, #975).
//
// Reads test cases from tests/fixtures/mode-activation/cases.json and asserts
// parseModeChange behavior. Known-failing cases from open issues are explicitly
// marked so the corpus lands cleanly without breaking CI while preserving an
// honest record of current behavior for future regex refinements.
//
// Run: node tests/test_mode_activation_corpus.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');
const { spawnSync } = require('child_process');

const { parseModeChange } = require('../src/hooks/caveman-parse');

const HOOK_PATH = path.resolve(__dirname, '..', 'src', 'hooks', 'caveman-mode-tracker.js');
const FIXTURES_PATH = path.resolve(__dirname, 'fixtures', 'mode-activation', 'cases.json');

const defaultFull = { getDefaultMode: () => 'full' };

let passed = 0;
let failed = 0;

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

function runTracker(prompt, presetFlag) {
  const cfg = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-corpus-parity-'));
  try {
    if (presetFlag) fs.writeFileSync(path.join(cfg, '.caveman-active'), presetFlag);
    spawnSync(process.execPath, [HOOK_PATH], {
      input: JSON.stringify({ prompt }),
      env: { ...process.env, CLAUDE_CONFIG_DIR: cfg },
      stdio: ['pipe', 'pipe', 'pipe'],
      encoding: 'utf8',
    });
    const flagPath = path.join(cfg, '.caveman-active');
    return fs.existsSync(flagPath) ? fs.readFileSync(flagPath, 'utf8') : null;
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
}

console.log('caveman mode-activation reproduction corpus tests\n');

assert.ok(fs.existsSync(FIXTURES_PATH), 'fixtures file must exist at ' + FIXTURES_PATH);
const cases = JSON.parse(fs.readFileSync(FIXTURES_PATH, 'utf8'));
assert.ok(Array.isArray(cases) && cases.length > 0, 'fixtures must contain array of cases');

for (const tc of cases) {
  const label = `case #${tc.id} [${tc.category}]: "${tc.prompt}"`;
  test(label, () => {
    const verdict = parseModeChange(tc.prompt, defaultFull);
    if (tc.known_failing) {
      assert.deepStrictEqual(
        verdict,
        tc.current,
        `${tc.reason} (documented known failure for issue #${tc.issue})`
      );
    } else {
      assert.deepStrictEqual(
        verdict,
        tc.expected,
        `${tc.reason}`
      );
    }
  });
}

// Parity verification on representative corpus cases against real hook
const parityIds = [1, 4, 7, 8, 9, 11, 16, 18];
for (const id of parityIds) {
  const tc = cases.find((c) => c.id === id);
  if (!tc) continue;
  test(`parity hook execution: case #${tc.id} "${tc.prompt}"`, () => {
    const verdict = parseModeChange(tc.prompt, defaultFull);
    const expectedFlag =
      verdict === null ? null :
      verdict.action === 'clear' ? null :
      verdict.mode;
    const actualFlag = runTracker(tc.prompt, null);
    assert.strictEqual(actualFlag, expectedFlag);
  });
}

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
