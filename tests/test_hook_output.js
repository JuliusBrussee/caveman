#!/usr/bin/env node
// Tests for hook OUTPUT behavior:
//   - per-turn reinforcement cadence (every Nth turn, not every turn)
//   - mode-activating prompts force a reinforcement off-cadence
//   - injected payloads are byte-stable + carry no per-turn-varying token
//     (a 10+ digit timestamp/counter would silently bust the prompt cache)
//   - the statusline-setup nudge is one-shot
//
// Run: node tests/test_hook_output.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');
const { spawnSync } = require('child_process');

const HOOKS = path.join(__dirname, '..', 'src', 'hooks');
let passed = 0;
let failed = 0;

function runHook(script, cfgDir, stdinObj) {
  // CAVEMAN_DEFAULT_MODE pins the resolved mode to 'full' so the test never reads
  // the developer's own repo-local/user config.
  const r = spawnSync(process.execPath, [path.join(HOOKS, script)], {
    input: stdinObj === undefined ? '' : JSON.stringify(stdinObj),
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: cfgDir, CAVEMAN_DEFAULT_MODE: 'full' },
  });
  return r.stdout || '';
}

function emittedReinforcement(stdout) {
  if (!stdout.trim()) return null;
  try {
    const j = JSON.parse(stdout);
    return (j.hookSpecificOutput && j.hookSpecificOutput.additionalContext) || null;
  } catch (e) {
    return null;
  }
}

// A 10+ digit run (epoch seconds / ms timestamp / large counter) inside an
// injected payload would vary per turn and bust the cached prefix for the rest
// of the session — the exact failure the cache-stability guard exists to catch.
function assertNoVaryingToken(s, label) {
  const m = s.match(/\d{10,}/);
  assert.ok(!m, `${label} contains a 10+ digit token (timestamp/counter?) that would bust the prompt cache: ${m}`);
}

function test(name, fn) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-hookout-'));
  try {
    fn(tmp);
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}`);
    console.error(`    ${e.message}`);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

console.log('hook output: cadence + cache-stability + one-shot nudge\n');

test('reinforcement fires on turns 1 and 4 only (every-3rd cadence)', (tmp) => {
  runHook('caveman-activate.js', tmp); // fresh session: writes flag=full, resets turn=0
  const emits = [];
  for (let i = 1; i <= 6; i++) {
    const out = runHook('caveman-mode-tracker.js', tmp, { prompt: 'do something' });
    emits.push(emittedReinforcement(out) !== null);
  }
  assert.deepStrictEqual(
    emits,
    [true, false, false, true, false, false],
    `cadence wrong: ${JSON.stringify(emits)}`
  );
});

test('a mode-activating prompt forces a reinforcement off-cadence', (tmp) => {
  runHook('caveman-activate.js', tmp); // turn=0
  runHook('caveman-mode-tracker.js', tmp, { prompt: 'p' }); // turn 1 (emits)
  const out2 = runHook('caveman-mode-tracker.js', tmp, { prompt: 'p' }); // turn 2 (silent)
  assert.strictEqual(emittedReinforcement(out2), null, 'turn 2 should be silent');
  // Turn 3 is normally silent, but an activation phrase must force an emit.
  const out3 = runHook('caveman-mode-tracker.js', tmp, { prompt: 'activate caveman mode' });
  assert.ok(emittedReinforcement(out3) !== null, 'activation turn must force a reinforcement');
});

test('reinforcement payload is byte-identical across emitting turns (cache-safe)', (tmp) => {
  runHook('caveman-activate.js', tmp);
  const a = emittedReinforcement(runHook('caveman-mode-tracker.js', tmp, { prompt: 'p' })); // turn 1
  runHook('caveman-mode-tracker.js', tmp, { prompt: 'p' }); // turn 2
  runHook('caveman-mode-tracker.js', tmp, { prompt: 'p' }); // turn 3
  const b = emittedReinforcement(runHook('caveman-mode-tracker.js', tmp, { prompt: 'p' })); // turn 4
  assert.ok(a && b, 'both turns 1 and 4 should emit');
  assert.strictEqual(a, b, 'reinforcement string must be byte-identical turn-to-turn');
  assertNoVaryingToken(a, 'reinforcement payload');
});

test('SessionStart output carries no per-turn-varying token', (tmp) => {
  const out = runHook('caveman-activate.js', tmp);
  assert.ok(out.includes('CAVEMAN MODE ACTIVE'), 'activate should emit the ruleset');
  assertNoVaryingToken(out, 'SessionStart output');
});

test('statusline nudge is one-shot: present first session, absent after', (tmp) => {
  // tmp has no settings.json → statusline missing → nudge eligible
  const first = runHook('caveman-activate.js', tmp);
  assert.ok(first.includes('STATUSLINE SETUP NEEDED'), 'first session should nudge');
  assert.ok(fs.existsSync(path.join(tmp, '.caveman-nudged')), 'nudged marker should be written');
  const second = runHook('caveman-activate.js', tmp);
  assert.ok(!second.includes('STATUSLINE SETUP NEEDED'), 'second session must NOT re-nudge');
});

test('locate-shaped prompt emits the cavecrew delegation nudge', (tmp) => {
  runHook('caveman-activate.js', tmp);
  const out = runHook('caveman-mode-tracker.js', tmp, { prompt: 'where is safeWriteFlag defined?' });
  const ctx = emittedReinforcement(out);
  assert.ok(ctx && ctx.includes('cavecrew-investigator'), 'investigation prompt should nudge delegation');
});

test('non-locate prompt does not emit the nudge', (tmp) => {
  runHook('caveman-activate.js', tmp);
  const out = runHook('caveman-mode-tracker.js', tmp, { prompt: 'refactor this function please' });
  const ctx = emittedReinforcement(out) || '';
  assert.ok(!ctx.includes('cavecrew-investigator'), 'non-locate prompt must not nudge');
});

test('nudge fires even on an off-cadence turn', (tmp) => {
  runHook('caveman-activate.js', tmp);                       // turn 0
  runHook('caveman-mode-tracker.js', tmp, { prompt: 'hi' }); // turn 1 (reinforces)
  // turn 2 is normally silent; a locate prompt must still surface the nudge
  const out = runHook('caveman-mode-tracker.js', tmp, { prompt: 'list all uses of foo' });
  const ctx = emittedReinforcement(out);
  assert.ok(ctx && ctx.includes('cavecrew-investigator'), 'off-cadence locate prompt should still nudge');
});

test('/caveman auto activates the opt-in auto intensity mode', (tmp) => {
  runHook('caveman-activate.js', tmp); // flag = full
  runHook('caveman-mode-tracker.js', tmp, { prompt: '/caveman auto' });
  assert.strictEqual(
    fs.readFileSync(path.join(tmp, '.caveman-active'), 'utf8'),
    'auto',
    '/caveman auto should write the auto flag'
  );
});

// --- session-cost meter (.caveman-ctx) + graduated guard --------------------

// Writes a one-line transcript whose last assistant turn reports `tok` total
// prompt tokens (input + cache_creation + cache_read), the value the meter reads.
function writeTranscript(tmp, tok) {
  const p = path.join(tmp, 'transcript.jsonl');
  const entry = {
    type: 'assistant',
    message: { usage: { input_tokens: 10, cache_creation_input_tokens: 0, cache_read_input_tokens: tok - 10 } },
  };
  fs.writeFileSync(p, JSON.stringify(entry) + '\n');
  return p;
}

test('context meter writes humanized .caveman-ctx from the transcript tail', (tmp) => {
  runHook('caveman-activate.js', tmp);
  const t = writeTranscript(tmp, 200000);
  runHook('caveman-mode-tracker.js', tmp, { prompt: 'p', transcript_path: t });
  const ctxFile = path.join(tmp, '.caveman-ctx');
  assert.ok(fs.existsSync(ctxFile), '.caveman-ctx should be written');
  assert.strictEqual(fs.readFileSync(ctxFile, 'utf8'), '200K', 'should humanize 200000 → 200K');
});

test('no transcript → no .caveman-ctx (silent, no guard)', (tmp) => {
  runHook('caveman-activate.js', tmp);
  runHook('caveman-mode-tracker.js', tmp, { prompt: 'p' }); // no transcript_path
  assert.ok(!fs.existsSync(path.join(tmp, '.caveman-ctx')), 'missing transcript must not write ctx');
});

test('hard guard fires at turn 20 once context exceeds the hard threshold', (tmp) => {
  runHook('caveman-activate.js', tmp); // turn 0
  const t = writeTranscript(tmp, 400000); // > CTX_HARD (320K)
  let emitAt20 = '';
  for (let i = 1; i <= 20; i++) {
    const out = runHook('caveman-mode-tracker.js', tmp, { prompt: 'p', transcript_path: t });
    if (i === 20) emitAt20 = emittedReinforcement(out) || '';
  }
  assert.ok(emitAt20.includes('SESSION COST WARNING'), `turn 20 should warn; got: ${emitAt20}`);
  assert.ok(emitAt20.includes('/clear'), 'warning should name /clear');
  assertNoVaryingToken(emitAt20, 'session-cost warning');
});

test('guard stays silent on off-cadence turns and below threshold', (tmp) => {
  runHook('caveman-activate.js', tmp);
  const small = writeTranscript(tmp, 50000); // < CTX_SOFT
  // turn 5: not an emitting cadence (5%3=2) and below threshold → fully silent
  let out = '';
  for (let i = 1; i <= 5; i++) out = runHook('caveman-mode-tracker.js', tmp, { prompt: 'p', transcript_path: small });
  const ctx = emittedReinforcement(out) || '';
  assert.ok(!ctx.includes('SESSION COST'), 'below threshold must not warn');
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
