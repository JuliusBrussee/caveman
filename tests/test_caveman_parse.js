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

// A bogus level is now REPORTED rather than swallowed (#838), but the original
// danger these two guard against is unchanged: it must never activate the
// default. 'unresolved' carries no mode, and applyModeChange/the tracker act
// only on 'set'/'clear'.
test('bogus level is unresolved — never falls through to the default', () => {
  const verdict = parseModeChange('/caveman not-a-real-level', defaultFull);
  assert.deepStrictEqual(verdict, { action: 'unresolved' });
  assert.strictEqual(verdict.mode, undefined, 'must not carry a mode');
});

test('independent modes are not reachable via /caveman <arg>', () => {
  const verdict = parseModeChange('/caveman commit', defaultFull);
  assert.strictEqual(verdict.action, 'unresolved');
  assert.strictEqual(verdict.mode, undefined, 'must not activate the mode');
});

// #838: punctuation glued to the level matched no mode, left the level
// untouched, and said nothing. Only parts[1] is read, so trailing WORDS were
// already harmless — it is the glued character that broke it.
test('punctuation glued to the level still resolves (#838)', () => {
  for (const prompt of ['/caveman ultra;', '/caveman ultra.', '/caveman ultra!', '/caveman ultra,']) {
    assert.deepStrictEqual(parseModeChange(prompt, defaultFull), { action: 'set', mode: 'ultra' }, prompt);
  }
});

test('punctuation glued to a hyphenated level still resolves (#838)', () => {
  assert.deepStrictEqual(
    parseModeChange('/caveman wenyan-ultra.', defaultFull),
    { action: 'set', mode: 'wenyan-ultra' }
  );
  assert.deepStrictEqual(
    parseModeChange('/caveman wenyan-full,', defaultFull),
    { action: 'set', mode: 'wenyan' }
  );
});

test('punctuation glued to off still deactivates (#838)', () => {
  assert.deepStrictEqual(parseModeChange('/caveman off.', defaultFull), { action: 'clear' });
});

test('trailing words after the level remain harmless', () => {
  assert.deepStrictEqual(
    parseModeChange('/caveman ultra; still too verbose', defaultFull),
    { action: 'set', mode: 'ultra' }
  );
});

// `/caveman ?` is plausibly someone asking for help. Bare `/caveman` still
// activates; an argument that was PRESENT but normalized away must not.
test('a punctuation-only argument does not activate', () => {
  assert.deepStrictEqual(parseModeChange('/caveman ?', defaultFull), { action: 'unresolved' });
  assert.deepStrictEqual(parseModeChange('/caveman', defaultFull), { action: 'set', mode: 'full' });
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

test('expandedTpl: bogus level in the template is unresolved, not the default (#602 drift)', () => {
  assert.deepStrictEqual(
    parseModeChange('Activate caveman mode: not-a-real-level', { ...defaultFull, expandedTpl: true }),
    { action: 'unresolved' }
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

// #838: natural-language triggers ran over the whole prompt, so any pasted
// text that merely QUOTED them fired them.
test('prose quoting "stop caveman" no longer deactivates (#838)', () => {
  const prompt = 'why does the help card say "stop caveman" or "normal mode" here?';
  assert.strictEqual(parseModeChange(prompt, defaultFull), null);
});

test('prose quoting "activate caveman" no longer activates (#838)', () => {
  assert.strictEqual(
    parseModeChange('the readme says you can "activate caveman" by typing it', defaultFull),
    null
  );
});

test('backtick-quoted triggers are inert too', () => {
  assert.strictEqual(parseModeChange('the `stop caveman` phrase is documented', defaultFull), null);
});

// The cap that was tried first broke exactly these: a user explaining WHY they
// want the mode off writes more words, not fewer, and a dropped deactivation
// is silent — the user cannot escape and is told nothing.
test('long compound deactivation still works — no length cap (#838)', () => {
  for (const prompt of [
    'that is enough compression for now — please turn off caveman mode and go back to full sentences for the rest of this task',
    'ok this is getting hard to read, stop caveman mode and then go through the auth middleware and explain the token expiry check',
  ]) {
    assert.ok(prompt.length > 120, 'fixture must be long enough to matter');
    assert.deepStrictEqual(parseModeChange(prompt, defaultFull), { action: 'clear' }, prompt);
  }
});

test('long compound activation still works', () => {
  const prompt = 'activate caveman mode and then start by reading the proxy package and summarising how the dial guard is wired up';
  assert.ok(prompt.length > 110);
  assert.deepStrictEqual(parseModeChange(prompt, defaultFull), { action: 'set', mode: 'full' });
});

test('apostrophes do not blank the command ("don\'t stop caveman, it\'s useful")', () => {
  assert.deepStrictEqual(
    parseModeChange("don't stop caveman, it's useful", defaultFull),
    { action: 'clear' }
  );
});

test('short natural-language triggers still work (positive control)', () => {
  assert.deepStrictEqual(parseModeChange('stop caveman', defaultFull), { action: 'clear' });
  assert.deepStrictEqual(parseModeChange('back to normal mode please', defaultFull), { action: 'clear' });
  assert.deepStrictEqual(parseModeChange('activate caveman', defaultFull), { action: 'set', mode: 'full' });
});

// A foreign slash command's own text must not toggle our mode — symmetric with
// the skipNaturalLanguage that a foreign command ENVELOPE already sets.
test('a slash-initiated prompt does not fire natural-language triggers', () => {
  assert.strictEqual(parseModeChange('/caveman-help stop caveman', defaultFull), null);
  assert.strictEqual(parseModeChange('/some-other-command activate caveman', defaultFull), null);
});

test('slash commands themselves are unaffected by prompt length', () => {
  const long = '/caveman ultra ' + 'x'.repeat(400);
  assert.deepStrictEqual(parseModeChange(long, defaultFull), { action: 'set', mode: 'ultra' });
});

// An independent mode IS a real mode, just not reachable this way — saying
// "not recognized" would deny a mode the user can see in the docs.
test('an independent mode via /caveman <arg> reports its own command', () => {
  assert.deepStrictEqual(
    parseModeChange('/caveman commit', defaultFull),
    { action: 'unresolved', independentMode: 'commit' }
  );
});

test('a quoted level resolves (leading punctuation stripped too)', () => {
  assert.deepStrictEqual(parseModeChange('/caveman "ultra"', defaultFull), { action: 'set', mode: 'ultra' });
});

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
    // null and 'unresolved' both mean "leave the flag exactly as it was".
    const expected =
      verdict === null ? (preset || null) :
      verdict.action === 'unresolved' ? (preset || null) :
      verdict.action === 'clear' ? null :
      verdict.mode;
    assert.strictEqual(runTracker(prompt, preset), expected);
  });
}

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
