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

const defaultCaveman = { getDefaultMode: () => 'caveman' };
const defaultOff = { getDefaultMode: () => 'off' };
const MODE_OF = {
  '/ultracave': 'ultracave', '/caveman:ultracave': 'ultracave',
  '/megacave': 'megacave', '/caveman:megacave': 'megacave',
};

test('status uses one read-only verdict for literal and expanded commands', () => {
  const options = { getDefaultMode: () => { throw new Error('status must not resolve a default'); } };
  for (const prompt of ['/caveman status', '/caveman:caveman status', '/caveman status.']) {
    assert.deepStrictEqual(parseModeChange(prompt, options), { action: 'status' });
  }
  assert.deepStrictEqual(parseModeChange('Activate caveman mode: status\n\nIf no argument given, use caveman.', {
    ...options, expandedTpl: true,
  }), { action: 'status' });
  assert.strictEqual(parseModeChange('What does `/caveman status` do?', options), null);
});

test('manual startup policy permits explicit activation but is not a mode', () => {
  const options = { getDefaultMode: () => 'manual', expandedTpl: true };
  for (const prompt of ['/caveman', 'talk like caveman', 'Activate caveman mode: \n\nIf no argument given, use caveman.']) {
    assert.deepStrictEqual(parseModeChange(prompt, options), { action: 'set', mode: 'caveman' });
  }
  assert.deepStrictEqual(parseModeChange('/caveman manual', options), { action: 'unresolved' });
  assert.strictEqual(parseModeChange('ordinary request', options), null);
});

// ---------- basic unit coverage ----------

test('empty/whitespace prompt is a no-op', () => {
  assert.strictEqual(parseModeChange('', defaultCaveman), null);
  assert.strictEqual(parseModeChange('   ', defaultCaveman), null);
});

test('/ultracave and /megacave set their own mode, bare or namespaced', () => {
  for (const [prompt, mode] of [
    ['/ultracave', 'ultracave'], ['/caveman:ultracave', 'ultracave'],
    ['/megacave', 'megacave'], ['/caveman:megacave', 'megacave'],
  ]) {
    assert.deepStrictEqual(parseModeChange(prompt, defaultCaveman), { action: 'set', mode }, prompt);
    // A configured default never overrides the command's own mode.
    assert.deepStrictEqual(parseModeChange(prompt, defaultOff), { action: 'set', mode }, prompt);
  }
});

test('/caveman <arg> accepts the three mode names', () => {
  for (const mode of ['caveman', 'ultracave', 'megacave']) {
    assert.deepStrictEqual(parseModeChange('/caveman ' + mode, defaultCaveman), { action: 'set', mode });
  }
});

test('legacy level names still parse as aliases of the three modes', () => {
  for (const [arg, mode] of [
    ['lite', 'caveman'], ['full', 'caveman'], ['ultra', 'ultracave'],
    ['wenyan', 'megacave'], ['wenyan-lite', 'megacave'],
    ['wenyan-full', 'megacave'], ['wenyan-ultra', 'megacave'],
  ]) {
    assert.deepStrictEqual(parseModeChange('/caveman ' + arg, defaultCaveman), { action: 'set', mode }, arg);
  }
});

test('/ultracave and /megacave take the same off/status words as /caveman', () => {
  for (const cmd of ['/ultracave', '/caveman:ultracave', '/megacave', '/caveman:megacave']) {
    for (const word of ['off', 'stop', 'disable', 'off.']) {
      assert.deepStrictEqual(parseModeChange(`${cmd} ${word}`, defaultCaveman), { action: 'clear' }, `${cmd} ${word}`);
    }
    assert.deepStrictEqual(parseModeChange(`${cmd} status`, defaultCaveman), { action: 'status' }, `${cmd} status`);
    assert.deepStrictEqual(
      parseModeChange(`${cmd} please`, defaultCaveman),
      { action: 'set', mode: MODE_OF[cmd] },
      `${cmd} please`
    );
  }
});

test('/ultracave-like prefixes are not the command', () => {
  assert.strictEqual(parseModeChange('/ultracaveman', defaultCaveman), null);
  assert.strictEqual(parseModeChange('/megacave-stats', defaultCaveman), null);
});

test('bare /caveman activates at the configured default', () => {
  assert.deepStrictEqual(parseModeChange('/caveman', defaultCaveman), { action: 'set', mode: 'caveman' });
});

test('bare /caveman with an off default clears instead of setting mode "off"', () => {
  assert.deepStrictEqual(parseModeChange('/caveman', defaultOff), { action: 'clear' });
});

test('/caveman off|stop|disable all clear', () => {
  assert.deepStrictEqual(parseModeChange('/caveman off', defaultCaveman), { action: 'clear' });
  assert.deepStrictEqual(parseModeChange('/caveman stop', defaultCaveman), { action: 'clear' });
  assert.deepStrictEqual(parseModeChange('/caveman disable', defaultCaveman), { action: 'clear' });
});

// A bogus level is now REPORTED rather than swallowed (#838), but the original
// danger these two guard against is unchanged: it must never activate the
// default. 'unresolved' carries no mode, and applyModeChange/the tracker act
// only on 'set'/'clear'.
test('bogus level is unresolved — never falls through to the default', () => {
  const verdict = parseModeChange('/caveman not-a-real-level', defaultCaveman);
  assert.deepStrictEqual(verdict, { action: 'unresolved' });
  assert.strictEqual(verdict.mode, undefined, 'must not carry a mode');
});

test('independent modes are not reachable via /caveman <arg>', () => {
  const verdict = parseModeChange('/caveman commit', defaultCaveman);
  assert.strictEqual(verdict.action, 'unresolved');
  assert.strictEqual(verdict.mode, undefined, 'must not activate the mode');
});

// #838: punctuation glued to the level matched no mode, left the level
// untouched, and said nothing. Only parts[1] is read, so trailing WORDS were
// already harmless — it is the glued character that broke it.
test('punctuation glued to the level still resolves (#838)', () => {
  for (const prompt of ['/caveman ultra;', '/caveman ultra.', '/caveman ultra!', '/caveman ultra,']) {
    assert.deepStrictEqual(parseModeChange(prompt, defaultCaveman), { action: 'set', mode: 'ultracave' }, prompt);
  }
});

test('punctuation glued to a hyphenated level still resolves (#838)', () => {
  assert.deepStrictEqual(
    parseModeChange('/caveman wenyan-ultra.', defaultCaveman),
    { action: 'set', mode: 'megacave' }
  );
  assert.deepStrictEqual(
    parseModeChange('/caveman wenyan-full,', defaultCaveman),
    { action: 'set', mode: 'megacave' }
  );
});

test('punctuation glued to off still deactivates (#838)', () => {
  assert.deepStrictEqual(parseModeChange('/caveman off.', defaultCaveman), { action: 'clear' });
});

test('trailing words after the level remain harmless', () => {
  assert.deepStrictEqual(
    parseModeChange('/caveman ultra; still too verbose', defaultCaveman),
    { action: 'set', mode: 'ultracave' }
  );
});

// `/caveman ?` is plausibly someone asking for help. Bare `/caveman` still
// activates; an argument that was PRESENT but normalized away must not.
test('a punctuation-only argument does not activate', () => {
  assert.deepStrictEqual(parseModeChange('/caveman ?', defaultCaveman), { action: 'unresolved' });
  assert.deepStrictEqual(parseModeChange('/caveman', defaultCaveman), { action: 'set', mode: 'caveman' });
});

test('/caveman-commit, /caveman-review, /caveman-compress set independent modes', () => {
  assert.deepStrictEqual(parseModeChange('/caveman-commit', defaultCaveman), { action: 'set', mode: 'commit' });
  assert.deepStrictEqual(parseModeChange('/caveman-review', defaultCaveman), { action: 'set', mode: 'review' });
  assert.deepStrictEqual(parseModeChange('/caveman-compress', defaultCaveman), { action: 'set', mode: 'compress' });
});

test('namespaced /caveman:caveman-* variants are recognized', () => {
  assert.deepStrictEqual(parseModeChange('/caveman:caveman-commit', defaultCaveman), { action: 'set', mode: 'commit' });
  assert.deepStrictEqual(parseModeChange('/caveman:caveman-review', defaultCaveman), { action: 'set', mode: 'review' });
  assert.deepStrictEqual(parseModeChange('/caveman:caveman', defaultCaveman), { action: 'set', mode: 'caveman' });
});

test('natural-language activation', () => {
  assert.deepStrictEqual(parseModeChange('activate caveman', defaultCaveman), { action: 'set', mode: 'caveman' });
  assert.deepStrictEqual(parseModeChange('talk like a caveman', defaultCaveman), { action: 'set', mode: 'caveman' });
});

test('brevity triggers activate', () => {
  assert.deepStrictEqual(parseModeChange('be brief', defaultCaveman), { action: 'set', mode: 'caveman' });
  assert.deepStrictEqual(parseModeChange('fewer tokens please', defaultCaveman), { action: 'set', mode: 'caveman' });
});

test('scoped brevity ("be brief in the summary") does not activate', () => {
  assert.strictEqual(parseModeChange('be brief in the summary section', defaultCaveman), null);
});

test('questions about caveman do not activate', () => {
  assert.strictEqual(parseModeChange('what is caveman mode?', defaultCaveman), null);
});

// #187: the gap between the activation verb and "caveman" was any 40
// characters, so ordinary prose that merely mentioned caveman downstream of a
// common verb switched the mode on. The gap is a whitelist of determiners and
// particles now.
test('prose with a verb and a downstream "caveman" does not activate (#187)', () => {
  for (const prompt of [
    'i want to start building a caveman-themed game',
    'help me start a caveman fire for the demo scene',
    'we should use the sprite sheet from the caveman assets folder',
    'switch to the branch that renames the caveman skill directory',
  ]) {
    assert.strictEqual(parseModeChange(prompt, defaultCaveman), null, prompt);
  }
});

test('real activation phrasings still activate (positive control, #187)', () => {
  for (const prompt of [
    'activate caveman',
    'activate caveman mode',
    'enable the caveman mode',
    'turn on caveman',
    'turn on the caveman mode please',
    'switch to caveman',
    'i want caveman mode',
    'give me caveman',
    'please use caveman now',
    'talk like caveman',
    'talk like a caveman',
  ]) {
    assert.deepStrictEqual(parseModeChange(prompt, defaultCaveman), { action: 'set', mode: 'caveman' }, prompt);
  }
});

// #187: "don't activate caveman" activated. The guard is activation-only —
// see the asymmetry note in caveman-parse.js and the #838 test below, which
// keeps "don't stop caveman" deactivating on purpose.
test('a negated activation does not activate (#187)', () => {
  for (const prompt of [
    "don't activate caveman",
    'do not turn on caveman',
    'never enable caveman mode',
    "i don't want caveman for this file",
    "please don't talk like a caveman",
    'no need to use caveman here',
  ]) {
    assert.strictEqual(parseModeChange(prompt, defaultCaveman), null, prompt);
  }
});

// The negator does not always sit next to the verb it negates: "don't want you
// to use caveman" negates `want`, while the trigger that actually matches is
// the `use` in the complement clause. Scoping the guard to the clause rather
// than to a fixed word window is what catches these.
test('a negated activation with a complement clause does not activate (#187)', () => {
  for (const prompt of [
    "i don't want you to use caveman",
    "don't ask me to enable caveman",
    "please don't switch me to caveman",
    "i don't need you to talk like a caveman",
    'never ask me to turn on caveman mode',
  ]) {
    assert.strictEqual(parseModeChange(prompt, defaultCaveman), null, prompt);
  }
});

// The other half of clause scoping: a negation must not swallow a LATER,
// independent positive command. Widening the guard to "a negator appears
// anywhere earlier in the prompt" would break exactly these.
test('a negated clause does not suppress a later activation (#187)', () => {
  for (const prompt of [
    "don't use vim, activate caveman",
    "i don't like verbose output. activate caveman",
    "never mind the linter — turn on caveman",
  ]) {
    assert.deepStrictEqual(parseModeChange(prompt, defaultCaveman), { action: 'set', mode: 'caveman' }, prompt);
  }
});

test('natural-language deactivation', () => {
  assert.deepStrictEqual(parseModeChange('turn caveman mode off', defaultCaveman), { action: 'clear' });
  assert.deepStrictEqual(parseModeChange('normal mode', defaultCaveman), { action: 'clear' });
});

// "go back to normal mode" and "go to normal mode" are ordinary switch-back
// phrasings, but the verb alternation listed "go" and "back to" as separate,
// mutually exclusive prefixes, so "go back to" matched nothing and "go to" had
// no alternative at all. Both deactivations were silently dropped, leaving the
// user in the mode with no feedback.
test('"go back to normal mode" / "go to normal mode" deactivate like the other switch-back phrasings', () => {
  for (const prompt of [
    'go back to normal mode',
    'go to normal mode',
    'please go back to normal mode',
    'go back to normal mode and summarize the diff',
  ]) {
    assert.deepStrictEqual(parseModeChange(prompt, defaultCaveman), { action: 'clear' }, prompt);
  }
});

test('vim "normal mode" (no caveman context) does not deactivate', () => {
  assert.strictEqual(parseModeChange('how do I exit vim normal mode', defaultCaveman), null);
});

test('INDEPENDENT_MODES is exported and matches the known set', () => {
  assert.deepStrictEqual([...INDEPENDENT_MODES].sort(), ['commit', 'compress', 'review']);
});

// ---------- skipNaturalLanguage (foreign command envelopes, #537) ----------

test('skipNaturalLanguage suppresses activation/deactivation matching entirely', () => {
  assert.strictEqual(
    parseModeChange('please activate caveman mode now', { ...defaultCaveman, skipNaturalLanguage: true }),
    null
  );
  assert.strictEqual(
    parseModeChange('stop caveman', { ...defaultCaveman, skipNaturalLanguage: true }),
    null
  );
});

test('skipNaturalLanguage still lets literal slash commands through', () => {
  assert.deepStrictEqual(
    parseModeChange('/caveman ultra', { ...defaultCaveman, skipNaturalLanguage: true }),
    { action: 'set', mode: 'ultracave' }
  );
});

// ---------- unwrapQuotes (opencode `run` path) ----------

test('unwrapQuotes strips a symmetric quote wrapper before matching', () => {
  assert.deepStrictEqual(
    parseModeChange('"/caveman lite"', { ...defaultCaveman, unwrapQuotes: true }),
    { action: 'set', mode: 'caveman' }
  );
});

test('without unwrapQuotes, a quoted command does not match', () => {
  assert.strictEqual(parseModeChange('"/caveman lite"', defaultCaveman), null);
});

// ---------- expandedTpl (opencode's expanded command-template bodies) ----------

test('expandedTpl recognizes the ultracave and megacave templates', () => {
  const read = (name) => fs.readFileSync(
    path.join(__dirname, '..', 'src', 'plugins', 'opencode', 'commands', name), 'utf8',
  ).replace(/^---[\s\S]*?---\s*/, '');
  assert.deepStrictEqual(
    parseModeChange(read('ultracave.md'), { ...defaultCaveman, expandedTpl: true }),
    { action: 'set', mode: 'ultracave' }
  );
  assert.deepStrictEqual(
    parseModeChange(read('megacave.md'), { ...defaultCaveman, expandedTpl: true }),
    { action: 'set', mode: 'megacave' }
  );
  assert.deepStrictEqual(
    parseModeChange(read('caveman.md').replace('$ARGUMENTS', ''), { ...defaultCaveman, expandedTpl: true }),
    { action: 'set', mode: 'caveman' }
  );
});

test('expandedTpl recognizes the generic "/caveman <arg>" template', () => {
  assert.deepStrictEqual(
    parseModeChange('Activate caveman mode: ultra', { ...defaultCaveman, expandedTpl: true }),
    { action: 'set', mode: 'ultracave' }
  );
});

test('expandedTpl: empty level (bare "/caveman", multi-line template head) uses the default', () => {
  // The real commands/caveman.md template puts `Activate caveman mode:
  // $ARGUMENTS` on its own line, followed by a blank line and then fixed
  // boilerplate ("If no argument given, use caveman. If \"off\", deactivate.").
  // With $ARGUMENTS empty, whitespace-collapse used to merge that boilerplate
  // directly onto the same line as the (empty) argument, so the word "if"
  // (from "If no level given ...") was captured as the level and rejected as
  // bogus — a bare `/caveman` in opencode silently never activated.
  // Regression guard for that (matches the shape exercised by
  // tests/installer/opencode.test.mjs's real-hooks test).
  const templateNoArgs =
    'Activate caveman mode: \n\n' +
    'If no argument given, use caveman. If "off", deactivate.';
  assert.deepStrictEqual(
    parseModeChange(templateNoArgs, { ...defaultCaveman, expandedTpl: true }),
    { action: 'set', mode: 'caveman' }
  );
});

test('expandedTpl: bogus level in the template is unresolved, not the default (#602 drift)', () => {
  assert.deepStrictEqual(
    parseModeChange('Activate caveman mode: not-a-real-level', { ...defaultCaveman, expandedTpl: true }),
    { action: 'unresolved' }
  );
});

test('expandedTpl recognizes the independent-mode command templates (#602 drift)', () => {
  assert.deepStrictEqual(
    parseModeChange('Generate a commit message for the current staged changes.', { ...defaultCaveman, expandedTpl: true }),
    { action: 'set', mode: 'commit' }
  );
  assert.deepStrictEqual(
    parseModeChange('Review the current diff (or files: ).', { ...defaultCaveman, expandedTpl: true }),
    { action: 'set', mode: 'review' }
  );
  assert.deepStrictEqual(
    parseModeChange('Compress the file at: notes.md', { ...defaultCaveman, expandedTpl: true }),
    { action: 'set', mode: 'compress' }
  );
});

test('expandedTpl: no non-activation opencode template switches a prose mode', () => {
  // Every shipped command file, expanded the way opencode delivers it: front
  // matter stripped, $ARGUMENTS empty. /caveman-help listed "Activate caveman"
  // in its table, which flipped ultracave back to caveman (or turned it on).
  const dir = path.join(__dirname, '..', 'src', 'plugins', 'opencode', 'commands');
  const PROSE = new Set(['caveman', 'ultracave', 'megacave']);
  const checked = [];
  for (const file of fs.readdirSync(dir).filter((f) => f.endsWith('.md'))) {
    if (PROSE.has(file.replace(/\.md$/, ''))) continue; // activation templates by design
    const body = fs.readFileSync(path.join(dir, file), 'utf8')
      .replace(/^---[\s\S]*?---\s*/, '').split('$ARGUMENTS').join('');
    for (const getDefaultMode of [() => 'caveman', () => 'ultracave']) {
      const verdict = parseModeChange(body, { getDefaultMode, expandedTpl: true, unwrapQuotes: true });
      const switches = verdict && (verdict.action === 'clear' || (verdict.action === 'set' && PROSE.has(verdict.mode)));
      assert.ok(!switches, `${file} → ${JSON.stringify(verdict)}`);
    }
    checked.push(file);
  }
  for (const f of ['caveman-help.md', 'caveman-commit.md', 'caveman-review.md', 'caveman-compress.md', 'caveman-stats.md']) {
    assert.ok(checked.includes(f), `${f} not checked`);
  }
});

test('expandedTpl: the help card opener short-circuits even a trigger in its body', () => {
  assert.strictEqual(
    parseModeChange('Show the caveman quick-reference card.\n\n| /caveman | Activate caveman |\nstop caveman', {
      ...defaultCaveman, expandedTpl: true,
    }),
    null
  );
});

test('without expandedTpl, template bodies are inert plain text', () => {
  assert.strictEqual(
    parseModeChange('Generate a commit message for the current staged changes.', defaultCaveman),
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
  assert.strictEqual(parseModeChange(prompt, defaultCaveman), null);
});

test('prose quoting "activate caveman" no longer activates (#838)', () => {
  assert.strictEqual(
    parseModeChange('the readme says you can "activate caveman" by typing it', defaultCaveman),
    null
  );
});

test('backtick-quoted triggers are inert too', () => {
  assert.strictEqual(parseModeChange('the `stop caveman` phrase is documented', defaultCaveman), null);
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
    assert.deepStrictEqual(parseModeChange(prompt, defaultCaveman), { action: 'clear' }, prompt);
  }
});

test('long compound activation still works', () => {
  const prompt = 'activate caveman mode and then start by reading the proxy package and summarising how the dial guard is wired up';
  assert.ok(prompt.length > 110);
  assert.deepStrictEqual(parseModeChange(prompt, defaultCaveman), { action: 'set', mode: 'caveman' });
});

test('apostrophes do not blank the command ("don\'t stop caveman, it\'s useful")', () => {
  assert.deepStrictEqual(
    parseModeChange("don't stop caveman, it's useful", defaultCaveman),
    { action: 'clear' }
  );
});

test('short natural-language triggers still work (positive control)', () => {
  assert.deepStrictEqual(parseModeChange('stop caveman', defaultCaveman), { action: 'clear' });
  assert.deepStrictEqual(parseModeChange('back to normal mode please', defaultCaveman), { action: 'clear' });
  assert.deepStrictEqual(parseModeChange('activate caveman', defaultCaveman), { action: 'set', mode: 'caveman' });
});

// A foreign slash command's own text must not toggle our mode — symmetric with
// the skipNaturalLanguage that a foreign command ENVELOPE already sets.
test('a slash-initiated prompt does not fire natural-language triggers', () => {
  assert.strictEqual(parseModeChange('/caveman-help stop caveman', defaultCaveman), null);
  assert.strictEqual(parseModeChange('/some-other-command activate caveman', defaultCaveman), null);
});

test('slash commands themselves are unaffected by prompt length', () => {
  const long = '/caveman ultra ' + 'x'.repeat(400);
  assert.deepStrictEqual(parseModeChange(long, defaultCaveman), { action: 'set', mode: 'ultracave' });
});

// An independent mode IS a real mode, just not reachable this way — saying
// "not recognized" would deny a mode the user can see in the docs.
test('an independent mode via /caveman <arg> reports its own command', () => {
  assert.deepStrictEqual(
    parseModeChange('/caveman commit', defaultCaveman),
    { action: 'unresolved', independentMode: 'commit' }
  );
});

test('a quoted level resolves (leading punctuation stripped too)', () => {
  assert.deepStrictEqual(parseModeChange('/caveman "ultra"', defaultCaveman), { action: 'set', mode: 'ultracave' });
});

const parityCases = [
  { prompt: '/caveman ultra', preset: null },
  { prompt: '/ultracave', preset: 'caveman' },
  { prompt: '/caveman off', preset: 'caveman' },
  { prompt: '/caveman not-a-real-level', preset: 'ultracave' },
  { prompt: 'be brief', preset: null },
  { prompt: 'activate caveman', preset: null },
  { prompt: 'stop caveman', preset: 'caveman' },
  { prompt: 'what is caveman mode?', preset: null },
];

for (const { prompt, preset } of parityCases) {
  test(`parity: "${prompt}" (preset=${preset}) matches shared-parser verdict`, () => {
    const normalized = prompt.trim().toLowerCase().replace(/\s+/g, ' ');
    const verdict = parseModeChange(normalized, { getDefaultMode: () => 'caveman' });
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
