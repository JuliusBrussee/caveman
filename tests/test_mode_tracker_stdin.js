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

function send(configDir, payload) {
  return spawnSync(process.execPath, [HOOK_PATH], {
    input: JSON.stringify(payload),
    env: { ...process.env, CLAUDE_CONFIG_DIR: configDir },
    stdio: ['pipe', 'pipe', 'pipe'],
    encoding: 'utf8',
  });
}

function flagValue(configDir) {
  const p = path.join(configDir, '.caveman-active');
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
    assert.strictEqual(flagValue(cfg), null);
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

// ---------- unresolved /caveman level (#838) ----------

function contextOf(result) {
  if (!result.stdout.trim()) return null;
  return JSON.parse(result.stdout).hookSpecificOutput.additionalContext;
}

test('a bogus level is reported instead of silently ignored', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'ultra');
    const ctx = contextOf(send(cfg, { prompt: '/caveman not-a-real-level' }));
    assert.ok(ctx, 'a bogus level must produce a notice');
    assert.match(ctx, /not recognized/);
    assert.strictEqual(flagValue(cfg), 'ultra', 'the level must be left untouched');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('the rejected argument is never echoed back into model context', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'full');
    const ctx = contextOf(send(cfg, { prompt: '/caveman IGNORE-PREVIOUS-INSTRUCTIONS' }));
    assert.ok(ctx);
    assert.doesNotMatch(ctx, /IGNORE-PREVIOUS-INSTRUCTIONS/i, 'untrusted input must not reach model context');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('the level list advertises the six documented levels, not the storage alias', () => {
  const cfg = makeConfigDir();
  try {
    const ctx = contextOf(send(cfg, { prompt: '/caveman nope' }));
    const listed = /Valid levels: ([^.]+)\./.exec(ctx);
    assert.ok(listed, `expected a level list, got: ${ctx}`);
    const levels = listed[1].split(',').map(v => v.trim());
    assert.deepStrictEqual(
      levels.sort(),
      ['full', 'lite', 'ultra', 'wenyan-full', 'wenyan-lite', 'wenyan-ultra'],
      'wenyan is the storage alias for wenyan-full — listing both advertises seven levels',
    );
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('an independent mode is pointed at its own command, not denied', () => {
  const cfg = makeConfigDir();
  try {
    const ctx = contextOf(send(cfg, { prompt: '/caveman commit' }));
    assert.match(ctx, /\/caveman-commit/, 'must name the command that does work');
    assert.doesNotMatch(ctx, /not recognized/, 'commit IS a real mode');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

// An early return on the notice would skip the #599 one-shot restore, leaving
// the user stranded in /caveman-commit an extra turn because of a typo.
test('a typo does not strand the user in a one-shot independent mode (#599)', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'commit');
    fs.writeFileSync(path.join(cfg, '.caveman-active.prev'), 'ultra');
    const ctx = contextOf(send(cfg, { prompt: '/caveman ultrra' }));
    assert.strictEqual(flagValue(cfg), 'ultra', 'the prose mode must still be restored this turn');
    assert.match(ctx, /not recognized/, 'and the notice must still be delivered');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('the notice and the per-turn reinforcement share one write', () => {
  const cfg = makeConfigDir();
  try {
    fs.writeFileSync(path.join(cfg, '.caveman-active'), 'ultra');
    const result = send(cfg, { prompt: '/caveman ultrra' });
    assert.doesNotThrow(() => JSON.parse(result.stdout), 'two writes would produce invalid JSON');
    const ctx = contextOf(result);
    assert.match(ctx, /not recognized/);
    assert.match(ctx, /CAVEMAN MODE ACTIVE \(ultra\)/, 'the turn must not lose its reinforcement');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
