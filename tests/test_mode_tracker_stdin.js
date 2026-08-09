#!/usr/bin/env node
// Tests for the stdin 'error' handler in caveman-mode-tracker.js.
// Covers issue #538: an abnormal stdin close (broken pipe, parent crash) emits
// an 'error' event on process.stdin; without a listener Node throws it as an
// uncaught exception and the hook exits non-zero — a spurious hook failure.
//
// Run: node tests/test_mode_tracker_stdin.js

const path = require('path');
const os = require('os');
const fs = require('fs');
const assert = require('assert');
const { spawnSync } = require('child_process');

const HOOK_PATH = path.resolve(__dirname, '..', 'src', 'hooks', 'caveman-mode-tracker.js');
const CLEAN_EXIT = 0;

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

console.log('caveman-mode-tracker stdin error handling\n');

// Load the REAL hook in a child, then emit an 'error' on process.stdin to
// simulate an abnormal close. stdin is left open (never closed) so the only
// event that fires is the injected 'error' — isolating the handler under test.
function runWithStdinError() {
  const harness =
    `require(${JSON.stringify(HOOK_PATH)});` +
    `setImmediate(() => process.stdin.emit('error', new Error('EPIPE (simulated)')));`;
  return spawnSync(process.execPath, ['-e', harness], {
    stdio: ['pipe', 'ignore', 'pipe'],
    encoding: 'utf8',
  });
}

test('stdin "error" event does not crash the hook (exit 0)', () => {
  const res = runWithStdinError();
  assert.strictEqual(
    res.status,
    CLEAN_EXIT,
    `expected clean exit on stdin error, got status=${res.status} signal=${res.signal}\n` +
      `stderr: ${(res.stderr || '').trim()}`
  );
  assert.ok(
    !/Unhandled 'error' event/.test(res.stderr || ''),
    `hook leaked an uncaught stdin error:\n${(res.stderr || '').trim()}`
  );
});

// Regression guard: the new listener must not disturb the normal path — a valid
// prompt piped on stdin, then a clean EOF, still exits 0.
test('normal stdin (valid JSON + clean EOF) still exits 0', () => {
  const tmpConfig = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-tracker-stdin-'));
  try {
    const res = spawnSync(process.execPath, [HOOK_PATH], {
      input: JSON.stringify({ prompt: 'hello there' }),
      env: { ...process.env, CLAUDE_CONFIG_DIR: tmpConfig },
      stdio: ['pipe', 'ignore', 'pipe'],
      encoding: 'utf8',
    });
    assert.strictEqual(
      res.status,
      CLEAN_EXIT,
      `expected clean exit on normal input, got status=${res.status}\n` +
        `stderr: ${(res.stderr || '').trim()}`
    );
  } finally {
    fs.rmSync(tmpConfig, { recursive: true, force: true });
  }
});

// ---------- helpers for the tests below ----------

function makeConfigDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-tracker-'));
}

// T1 PR-review v1 Low finding: CLAUDE_CONFIG_DIR only relocates the FLAG
// file -- getDefaultMode() separately resolves via HOME/XDG_CONFIG_HOME
// (caveman-config.js's config-file lookup), which leaking ...process.env
// left ambient. On a machine with a real ~/.config/caveman/config.json
// (caveman's own target audience), any test asserting a hardcoded default
// mode silently broke. Isolate both: HOME to a scratch dir with no config,
// XDG_CONFIG_HOME cleared so it can't override HOME's resolution either.
function send(configDir, payload) {
  const isolatedHome = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-tracker-home-'));
  const env = { ...process.env, CLAUDE_CONFIG_DIR: configDir, HOME: isolatedHome, USERPROFILE: isolatedHome };
  delete env.XDG_CONFIG_HOME;
  delete env.CAVEMAN_DEFAULT_MODE;
  return spawnSync(process.execPath, [HOOK_PATH], {
    input: JSON.stringify(payload),
    env,
    stdio: ['pipe', 'pipe', 'pipe'],
    encoding: 'utf8',
  });
}

function flagValue(configDir) {
  const p = path.join(configDir, '.caveman-active');
  return fs.existsSync(p) ? fs.readFileSync(p, 'utf8') : null;
}

function scopedFlagValue(configDir, sessionId) {
  const p = path.join(configDir, `.caveman-active-${sessionId}`);
  return fs.existsSync(p) ? fs.readFileSync(p, 'utf8') : null;
}

function scopedPrevValue(configDir, sessionId) {
  const p = path.join(configDir, `.caveman-active-${sessionId}.prev`);
  return fs.existsSync(p) ? fs.readFileSync(p, 'utf8') : null;
}

function envelope(name, args, newlines) {
  const sep = newlines ? '\n' : '';
  return (
    `<command-message>${name.replace(/^\//, '')}</command-message>${sep}` +
    `<command-name>${name}</command-name>${sep}` +
    `<command-args>${args}</command-args>`
  );
}

function makeSession(configDir, lines) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-tracker-sess-'));
  const sessFile = path.join(dir, 's.jsonl');
  fs.writeFileSync(sessFile, lines.map(l => JSON.stringify(l)).join('\n'));
  return sessFile;
}

// ---------- #537: slash-command envelope unwrap ----------

test('envelope one-line form switches level', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'full');
    const r = send(cfg, { prompt: envelope('/caveman', 'lite', false) });
    assert.strictEqual(flagValue(cfg), 'lite');
    assert.match(r.stdout, /CAVEMAN MODE ACTIVE \(lite\)/);
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('envelope newline-separated form switches level', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'full');
    send(cfg, { prompt: envelope('/caveman', 'ultra', true) });
    assert.strictEqual(flagValue(cfg), 'ultra');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('envelope "/caveman off" deactivates', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'full');
    send(cfg, { prompt: envelope('/caveman', 'off', true) });
    assert.strictEqual(flagValue(cfg), 'off');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('foreign command envelope is left untouched (no NL misfire on its args)', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'full');
    send(cfg, { prompt: envelope('/commit', 'fix the caveman parser', true) });
    assert.strictEqual(flagValue(cfg), 'full', 'foreign envelope must not touch the flag');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

// ---------- bogus level must never fall through to the default ----------

test('bogus /caveman level leaves the flag unchanged', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'ultra');
    send(cfg, { prompt: '/caveman not-a-real-level' });
    assert.strictEqual(flagValue(cfg), 'ultra');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

// ---------- brevity trigger (#602 drift also fixed in opencode) ----------

test('brevity trigger ("be brief") activates caveman at the default mode', () => {
  const cfg = makeConfigDir();
  try {
    send(cfg, { prompt: 'be brief' });
    assert.strictEqual(flagValue(cfg), 'full');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

// ---------- scheduled-task guard ----------

test('scheduled-task prompt emits nothing while caveman active (control: normal prompt is reinforced)', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'full');

    const scheduled = send(cfg, {
      prompt: '<scheduled-task name="trigger-runner" file="/x/SKILL.md">\nAutomated run.',
    });
    assert.strictEqual(scheduled.status, CLEAN_EXIT);
    assert.strictEqual((scheduled.stdout || '').trim(), '', 'scheduled-task run must emit no reinforcement');
    assert.strictEqual(flagValue(cfg), 'full', 'scheduled-task run must not mutate the flag');

    const normal = send(cfg, { prompt: 'fix the auth bug' });
    assert.ok(/CAVEMAN MODE ACTIVE/.test(normal.stdout || ''), 'control prompt should be reinforced');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

// ---------- #634: repo-local defaultMode "off" gates reinforcement only ----------

test('defaultMode off (via cwd-scoped repo config) suppresses reinforcement but leaves the flag alone', () => {
  const cfg = makeConfigDir();
  const repoDir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-tracker-repo-'));
  try {
    fs.writeFileSync(path.join(repoDir, '.caveman.json'), JSON.stringify({ defaultMode: 'off' }));
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'full');

    const gated = send(cfg, { prompt: 'fix the auth bug', cwd: repoDir });
    assert.strictEqual((gated.stdout || '').trim(), '', 'reinforcement must be suppressed');
    assert.strictEqual(flagValue(cfg), 'full', 'gating must never touch the flag file');

    // Control: same flag, no cwd override — reinforcement fires normally.
    const ungated = send(cfg, { prompt: 'fix the auth bug' });
    assert.ok(/CAVEMAN MODE ACTIVE/.test(ungated.stdout || ''));
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
    fs.rmSync(repoDir, { recursive: true, force: true });
  }
});

// ---------- #618: stats delivery via additionalContext, not decision:block ----------

test('/caveman-stats emits hookSpecificOutput.additionalContext, not decision:block', () => {
  const cfg = makeConfigDir();
  try {
    const sess = makeSession(cfg, [
      { type: 'assistant', message: { usage: { output_tokens: 350 } } },
    ]);
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'full');
    const r = send(cfg, { prompt: '/caveman-stats', transcript_path: sess });
    const parsed = JSON.parse(r.stdout);
    assert.strictEqual(parsed.decision, undefined, 'old decision:block shape must be gone');
    assert.strictEqual(parsed.hookSpecificOutput.hookEventName, 'UserPromptSubmit');
    assert.ok(
      /print this stats block verbatim/i.test(parsed.hookSpecificOutput.additionalContext),
      'additionalContext must instruct the model to relay the block verbatim'
    );
    assert.match(parsed.hookSpecificOutput.additionalContext, /Saved 650 output tokens|Caveman Stats/);
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

// ---------- per-session flag isolation (Acceptance Gates: concurrent sessions) ----------

test('two concurrent sessions toggle caveman independently, including one turning off while the other stays on', () => {
  const cfg = makeConfigDir();
  try {
    send(cfg, { prompt: '/caveman ultra', session_id: 'session-a' });
    send(cfg, { prompt: '/caveman lite', session_id: 'session-b' });
    assert.strictEqual(scopedFlagValue(cfg, 'session-a'), 'ultra');
    assert.strictEqual(scopedFlagValue(cfg, 'session-b'), 'lite');

    // Turn session-a off; session-b must be completely unaffected.
    send(cfg, { prompt: 'stop caveman', session_id: 'session-a' });
    assert.strictEqual(scopedFlagValue(cfg, 'session-a'), 'off');
    assert.strictEqual(scopedFlagValue(cfg, 'session-b'), 'lite');

    // And the legacy global flag was never touched by either scoped session.
    assert.strictEqual(flagValue(cfg), null);
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('a session with no session_id still uses the legacy global flag, unaffected by scoped sessions', () => {
  const cfg = makeConfigDir();
  try {
    send(cfg, { prompt: '/caveman ultra', session_id: 'session-a' });
    send(cfg, { prompt: '/caveman full' }); // no session_id -> legacy path
    assert.strictEqual(flagValue(cfg), 'full');
    assert.strictEqual(scopedFlagValue(cfg, 'session-a'), 'ultra');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

// ---------- Tier-2 v2 High finding: .prev write/unlink sites must target the scoped path ----------

test("scoped session's independent-mode capture/restore never touches the legacy .prev file (Tier-2 v2 High)", () => {
  const cfg = makeConfigDir();
  const LEGACY_PREV_SENTINEL = 'wenyan-ultra';
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active.prev'), LEGACY_PREV_SENTINEL);

    send(cfg, { prompt: '/caveman ultra', session_id: 'session-a' });
    send(cfg, { prompt: '/caveman-commit', session_id: 'session-a' }); // captures 'ultra' into scoped .prev
    assert.strictEqual(scopedFlagValue(cfg, 'session-a'), 'commit');
    assert.strictEqual(scopedPrevValue(cfg, 'session-a'), 'ultra');
    assert.strictEqual(
      fs.readFileSync(path.join(cfg, '.caveman-active.prev'), 'utf8'),
      LEGACY_PREV_SENTINEL,
      'independent-mode capture must never touch the legacy .prev sentinel'
    );

    send(cfg, { prompt: 'ordinary follow-up question', session_id: 'session-a' }); // one-shot restore consumption
    assert.strictEqual(scopedFlagValue(cfg, 'session-a'), 'ultra');
    assert.strictEqual(scopedPrevValue(cfg, 'session-a'), null, '.prev must be consumed (unlinked)');
    assert.strictEqual(
      fs.readFileSync(path.join(cfg, '.caveman-active.prev'), 'utf8'),
      LEGACY_PREV_SENTINEL,
      'one-shot restore consumption must never touch the legacy .prev sentinel'
    );
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

// ---------- session-sync-with-opt-in-isolation: /caveman default ----------

test('/caveman <level> still isolates, including a level equal to the current legacy value (T1 v3/v4 case c/g)', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'lite');
    send(cfg, { prompt: '/caveman lite', session_id: 'session-a' }); // matches current legacy value
    assert.strictEqual(
      scopedFlagValue(cfg, 'session-a'),
      'lite',
      'setting a level equal to the current legacy value must still isolate -- no accidental sync-detection'
    );
    assert.strictEqual(flagValue(cfg), 'lite', 'legacy value must be untouched by an isolating set');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('/caveman default on an isolated session deletes scoped state and falls back to legacy (case d)', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'full');
    send(cfg, { prompt: '/caveman ultra', session_id: 'session-a' });
    assert.strictEqual(scopedFlagValue(cfg, 'session-a'), 'ultra');

    send(cfg, { prompt: '/caveman default', session_id: 'session-a' });
    assert.strictEqual(
      scopedFlagValue(cfg, 'session-a'),
      null,
      '/caveman default must delete the scoped active flag'
    );
    assert.strictEqual(flagValue(cfg), 'full', 'legacy value must be unaffected by the revert');

    const log = fs
      .readFileSync(path.join(cfg, '.caveman-mode-log.jsonl'), 'utf8')
      .trim()
      .split('\n')
      .map(JSON.parse);
    const last = log[log.length - 1];
    assert.strictEqual(
      last.mode,
      'full',
      '/caveman default must log the transition to the LEGACY value, not silently no-op ' +
        '(a bug class: reading resolveFlag(claudeDir, sessionId) instead of ' +
        'resolveFlag(claudeDir, null) would see current === next and skip logging)'
    );
    assert.strictEqual(last.prev, 'ultra');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('/caveman default also deletes the scoped .prev file', () => {
  const cfg = makeConfigDir();
  try {
    send(cfg, { prompt: '/caveman ultra', session_id: 'session-a' });
    send(cfg, { prompt: '/caveman-commit', session_id: 'session-a' }); // captures 'ultra' into scoped .prev
    assert.strictEqual(scopedPrevValue(cfg, 'session-a'), 'ultra');

    send(cfg, { prompt: '/caveman default', session_id: 'session-a' });
    assert.strictEqual(
      scopedPrevValue(cfg, 'session-a'),
      null,
      '/caveman default must delete the scoped .prev file, not leave a dangling reference'
    );
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('/caveman default on an already-synced session (no scoped file) is a silent no-op (case e)', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'lite');
    const res = send(cfg, { prompt: '/caveman default', session_id: 'session-a' });
    assert.strictEqual(res.status, 0, 'must exit cleanly even with nothing to revert');
    assert.strictEqual(scopedFlagValue(cfg, 'session-a'), null);
    assert.strictEqual(flagValue(cfg), 'lite', 'legacy value must be unaffected');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('/caveman default with no session_id never touches the legacy file (case f)', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'full');
    const res = send(cfg, { prompt: '/caveman default' }); // no session_id
    assert.strictEqual(res.status, 0);
    assert.strictEqual(
      flagValue(cfg),
      'full',
      'a keyless caller has no scoped identity to revert -- must never unlink the legacy file itself ' +
        '(flagBaseName(null) === flagBaseName(sessionId) when sessionId is falsy)'
    );
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
