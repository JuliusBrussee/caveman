#!/usr/bin/env node
// Tests for the shared mode-change parser (#602), src/hooks/caveman-parse.js.
// caveman-mode-tracker.js and the opencode plugin both consume this module —
// these tests exercise it directly (unit-level) and also check that its
// verdicts line up with what the real tracker.js hook does for the same
// prompts (parity), so the two callers can't silently drift apart again.
//
// Run: node tests/test_caveman_parse.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');
const { spawnSync } = require('child_process');

const { parseModeChange, INDEPENDENT_MODES } = require('../src/hooks/caveman-parse');

const HOOK_PATH = path.resolve(__dirname, '..', 'src', 'hooks', 'caveman-mode-tracker.js');

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

console.log('caveman-parse (shared mode-change parser) tests\n');

const defaultFull = { getDefaultMode: () => 'full' };
const defaultOff = { getDefaultMode: () => 'off' };

// ---------- basic unit coverage ----------

test('empty/whitespace prompt is a no-op', () => {
  assert.strictEqual(parseModeChange('', defaultFull), null);
  assert.strictEqual(parseModeChange('   ', defaultFull), null);
});

test('slash level switch', () => {
  assert.deepStrictEqual(parseModeChange('/caveman ultra', defaultFull), { action: 'set', mode: 'ultra' });
});

test('bare /caveman activates at the configured default', () => {
  assert.deepStrictEqual(parseModeChange('/caveman', defaultFull), { action: 'set', mode: 'full' });
});

test('bare /caveman with an off default clears instead of setting mode "off"', () => {
  assert.deepStrictEqual(parseModeChange('/caveman', defaultOff), { action: 'clear' });
});

test('/caveman off|stop|disable all clear', () => {
  assert.deepStrictEqual(parseModeChange('/caveman off', defaultFull), { action: 'clear' });
  assert.deepStrictEqual(parseModeChange('/caveman stop', defaultFull), { action: 'clear' });
  assert.deepStrictEqual(parseModeChange('/caveman disable', defaultFull), { action: 'clear' });
});

test('wenyan-full alias stores as "wenyan"', () => {
  assert.deepStrictEqual(parseModeChange('/caveman wenyan-full', defaultFull), { action: 'set', mode: 'wenyan' });
});

test('bogus level returns null — never falls through to the default', () => {
  assert.strictEqual(parseModeChange('/caveman not-a-real-level', defaultFull), null);
});

test('independent modes are not reachable via /caveman <arg>', () => {
  assert.strictEqual(parseModeChange('/caveman commit', defaultFull), null);
});

test('/caveman-commit, /caveman-review, /caveman-compress set independent modes', () => {
  assert.deepStrictEqual(parseModeChange('/caveman-commit', defaultFull), { action: 'set', mode: 'commit' });
  assert.deepStrictEqual(parseModeChange('/caveman-review', defaultFull), { action: 'set', mode: 'review' });
  assert.deepStrictEqual(parseModeChange('/caveman-compress', defaultFull), { action: 'set', mode: 'compress' });
});

test('namespaced /caveman:caveman-* variants are recognized', () => {
  assert.deepStrictEqual(parseModeChange('/caveman:caveman-commit', defaultFull), { action: 'set', mode: 'commit' });
  assert.deepStrictEqual(parseModeChange('/caveman:caveman-review', defaultFull), { action: 'set', mode: 'review' });
  assert.deepStrictEqual(parseModeChange('/caveman:caveman', defaultFull), { action: 'set', mode: 'full' });
});

test('natural-language activation', () => {
  assert.deepStrictEqual(parseModeChange('activate caveman', defaultFull), { action: 'set', mode: 'full' });
  assert.deepStrictEqual(parseModeChange('talk like a caveman', defaultFull), { action: 'set', mode: 'full' });
});

test('brevity triggers activate', () => {
  assert.deepStrictEqual(parseModeChange('be brief', defaultFull), { action: 'set', mode: 'full' });
  assert.deepStrictEqual(parseModeChange('fewer tokens please', defaultFull), { action: 'set', mode: 'full' });
});

test('scoped brevity ("be brief in the summary") does not activate', () => {
  assert.strictEqual(parseModeChange('be brief in the summary section', defaultFull), null);
});

test('questions about caveman do not activate', () => {
  assert.strictEqual(parseModeChange('what is caveman mode?', defaultFull), null);
});

test('natural-language deactivation', () => {
  assert.deepStrictEqual(parseModeChange('turn caveman mode off', defaultFull), { action: 'clear' });
  assert.deepStrictEqual(parseModeChange('normal mode', defaultFull), { action: 'clear' });
});

test('vim "normal mode" (no caveman context) does not deactivate', () => {
  assert.strictEqual(parseModeChange('how do I exit vim normal mode', defaultFull), null);
});

test('INDEPENDENT_MODES is exported and matches the known set', () => {
  assert.deepStrictEqual([...INDEPENDENT_MODES].sort(), ['commit', 'compress', 'review']);
});

// ---------- skipNaturalLanguage (foreign command envelopes, #537) ----------

test('skipNaturalLanguage suppresses activation/deactivation matching entirely', () => {
  assert.strictEqual(
    parseModeChange('please activate caveman mode now', { ...defaultFull, skipNaturalLanguage: true }),
    null
  );
  assert.strictEqual(
    parseModeChange('stop caveman', { ...defaultFull, skipNaturalLanguage: true }),
    null
  );
});

test('skipNaturalLanguage still lets literal slash commands through', () => {
  assert.deepStrictEqual(
    parseModeChange('/caveman ultra', { ...defaultFull, skipNaturalLanguage: true }),
    { action: 'set', mode: 'ultra' }
  );
});

// ---------- unwrapQuotes (opencode `run` path) ----------

test('unwrapQuotes strips a symmetric quote wrapper before matching', () => {
  assert.deepStrictEqual(
    parseModeChange('"/caveman lite"', { ...defaultFull, unwrapQuotes: true }),
    { action: 'set', mode: 'lite' }
  );
});

test('without unwrapQuotes, a quoted command does not match', () => {
  assert.strictEqual(parseModeChange('"/caveman lite"', defaultFull), null);
});

// ---------- expandedTpl (opencode's expanded command-template bodies) ----------

test('expandedTpl recognizes the generic "/caveman <level>" template', () => {
  assert.deepStrictEqual(
    parseModeChange('Activate caveman mode: ultra', { ...defaultFull, expandedTpl: true }),
    { action: 'set', mode: 'ultra' }
  );
});

test('expandedTpl: empty level (bare "/caveman", multi-line template head) uses the default', () => {
  // The real commands/caveman.md template puts `Activate caveman mode:
  // $ARGUMENTS` on its own line, followed by a blank line and then fixed
  // boilerplate ("If no level given, use full. If \"off\", deactivate.").
  // With $ARGUMENTS empty, whitespace-collapse used to merge that boilerplate
  // directly onto the same line as the (empty) argument, so the word "if"
  // (from "If no level given ...") was captured as the level and rejected as
  // bogus — a bare `/caveman` in opencode silently never activated.
  // Regression guard for that (matches the shape exercised by
  // tests/installer/opencode.test.mjs's real-hooks test).
  const templateNoArgs =
    'Activate caveman mode: \n\n' +
    'If no level given, use full. If "off", deactivate.';
  assert.deepStrictEqual(
    parseModeChange(templateNoArgs, { ...defaultFull, expandedTpl: true }),
    { action: 'set', mode: 'full' }
  );
});

test('expandedTpl: bogus level in the template returns null, not the default (#602 drift)', () => {
  assert.strictEqual(
    parseModeChange('Activate caveman mode: not-a-real-level', { ...defaultFull, expandedTpl: true }),
    null
  );
});

test('expandedTpl recognizes the independent-mode command templates (#602 drift)', () => {
  assert.deepStrictEqual(
    parseModeChange('Generate a commit message for the current staged changes.', { ...defaultFull, expandedTpl: true }),
    { action: 'set', mode: 'commit' }
  );
  assert.deepStrictEqual(
    parseModeChange('Review the current diff (or files: ).', { ...defaultFull, expandedTpl: true }),
    { action: 'set', mode: 'review' }
  );
  assert.deepStrictEqual(
    parseModeChange('Compress the file at: notes.md', { ...defaultFull, expandedTpl: true }),
    { action: 'set', mode: 'compress' }
  );
});

test('without expandedTpl, template bodies are inert plain text', () => {
  assert.strictEqual(
    parseModeChange('Generate a commit message for the current staged changes.', defaultFull),
    null
  );
});

// ---------- parity with the real tracker hook ----------
// The tracker collapses whitespace/case and applies the same option set this
// module expects (getDefaultMode, skipNaturalLanguage). For a representative
// set of raw prompts, verify the flag-file outcome the tracker produces
// matches what parseModeChange's verdict implies — proving the two stay in
// sync rather than just "both look right in isolation".

function runTracker(prompt, presetFlag) {
  const cfg = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-parse-parity-'));
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

const parityCases = [
  { prompt: '/caveman ultra', preset: null },
  { prompt: '/caveman off', preset: 'full' },
  { prompt: '/caveman not-a-real-level', preset: 'ultra' },
  { prompt: 'be brief', preset: null },
  { prompt: 'activate caveman', preset: null },
  { prompt: 'stop caveman', preset: 'full' },
  { prompt: 'what is caveman mode?', preset: null },
];

for (const { prompt, preset } of parityCases) {
  test(`parity: "${prompt}" (preset=${preset}) matches shared-parser verdict`, () => {
    const normalized = prompt.trim().toLowerCase().replace(/\s+/g, ' ');
    const verdict = parseModeChange(normalized, { getDefaultMode: () => 'full' });
    const expected =
      verdict === null ? (preset || null) :
      verdict.action === 'clear' ? null :
      verdict.mode;
    assert.strictEqual(runTracker(prompt, preset), expected);
  });
}

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
