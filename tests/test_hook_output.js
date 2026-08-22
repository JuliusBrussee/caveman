#!/usr/bin/env node
// SessionStart output shape (#88).
//
// caveman-activate.js used to write the ruleset as a plain string. Claude Code
// adds raw SessionStart stdout to the conversation, so that worked there and
// nowhere else: GitHub Copilot only injects a hook's output when it parses as
// JSON with a top-level `additionalContext`, so caveman loaded, ran, and was
// silently discarded on every Copilot session.
//
// The two hosts want the context in different places, and nothing in a hook's
// environment identifies which one spawned it. One JSON object carrying both
// keys satisfies both without guessing.
//
// Run: node tests/test_hook_output.js

const path = require('path');
const os = require('os');
const fs = require('fs');
const assert = require('assert');
const { spawnSync } = require('child_process');

const ACTIVATE = path.resolve(__dirname, '..', 'src', 'hooks', 'caveman-activate.js');

let passed = 0;
let failed = 0;

function test(name, fn) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-output-'));
  try {
    fn(dir);
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}`);
    console.error(`    ${e.message}`);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

function activate(configDir, { source = 'startup', env = {} } = {}) {
  const r = spawnSync(process.execPath, [ACTIVATE], {
    input: JSON.stringify({
      session_id: 't', cwd: os.tmpdir(), hook_event_name: 'SessionStart', source,
    }),
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: configDir, ...env },
  });
  assert.strictEqual(r.status, 0, `expected exit 0, got ${r.status}\n${r.stderr}`);
  return r.stdout;
}

function parse(stdout) {
  try {
    return JSON.parse(stdout);
  } catch (e) {
    throw new Error(`stdout is not JSON, so Copilot discards it: ${JSON.stringify(stdout.slice(0, 120))}`);
  }
}

console.log('SessionStart output — one payload both hosts can read\n');

test('emits JSON rather than a bare string', (dir) => {
  parse(activate(dir));
});

test('Claude reads hookSpecificOutput.additionalContext', (dir) => {
  const out = parse(activate(dir));
  assert.strictEqual(out.hookSpecificOutput.hookEventName, 'SessionStart',
    'Claude drops a hookSpecificOutput that does not name its event');
  assert.match(out.hookSpecificOutput.additionalContext, /CAVEMAN MODE ACTIVE/);
});

test('Copilot reads the top-level additionalContext', (dir) => {
  const out = parse(activate(dir));
  assert.match(out.additionalContext, /CAVEMAN MODE ACTIVE/);
});

test('both keys carry the same ruleset', (dir) => {
  const out = parse(activate(dir));
  assert.strictEqual(out.additionalContext, out.hookSpecificOutput.additionalContext,
    'a host reading either key must get the same session');
});

test('the full ruleset survives, not just the banner', (dir) => {
  const out = parse(activate(dir));
  assert.ok(out.additionalContext.length > 500,
    `expected the filtered SKILL.md, got ${out.additionalContext.length} chars`);
});

test('independent modes emit the same shape', (dir) => {
  fs.writeFileSync(path.join(dir, '.caveman-active'), 'commit');
  const out = parse(activate(dir, { source: 'resume' }));
  assert.match(out.additionalContext, /\/caveman-commit skill/);
  assert.strictEqual(out.additionalContext, out.hookSpecificOutput.additionalContext);
});

test('off injects no context on either key', (dir) => {
  const stdout = activate(dir, { env: { CAVEMAN_DEFAULT_MODE: 'off' } });
  assert.doesNotMatch(stdout, /CAVEMAN MODE ACTIVE/,
    'an opted-out session must stay clean whichever key the host reads');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
