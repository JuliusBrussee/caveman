#!/usr/bin/env node
// Tests for the language-preservation rule in the always-on ruleset (#812).
//
// The rule used to be taught with a worked example that named a specific
// natural language: "User write Portuguese → reply Portuguese caveman." Those
// tokens sit in the SessionStart injection for the whole session, and #812
// reports them acting as an attractor: a conversation with no Portuguese input
// anywhere gets one turn answered in Portuguese. The example is also the least
// load-bearing part of the rule — "reply in the language user writes" says the
// same thing without naming one.
//
// The ruleset reaches the model by TWO paths and the fix has to hold on both:
//
//   1. SKILL.md, read at runtime by loadFilteredRuleset() — the plugin install
//      and any standalone install that also has a skills/ directory.
//   2. The ruleset hardcoded in caveman-activate.js, used when SKILL.md cannot
//      be found. bin/install.js's installHooks() copies HOOK_FILES alone into
//      $CLAUDE_CONFIG_DIR/hooks/ — no SKILL.md — and default installs wire
//      those standalone hooks precisely when the plugin install FAILED, so
//      this branch is what a fallback-install user gets on every session.
//
// Run: node tests/test_hook_language_rule.js

const path = require('path');
const os = require('os');
const fs = require('fs');
const assert = require('assert');
const { spawnSync } = require('child_process');

const REPO_ROOT = path.resolve(__dirname, '..');
const HOOKS_DIR = path.join(REPO_ROOT, 'src', 'hooks');
const SKILL_SRC = path.join(REPO_ROOT, 'skills');

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
    console.error(`    ${e.stack || e.message}`);
  }
}

// Natural languages that would only ever appear in the ruleset as a worked
// example of "reply in the user's language". English is deliberately absent:
// SKILL.md cites "ASD-STE100 Simplified Technical English" as a writing
// register, which is a rule about sentence construction, not a reply language.
const LANGUAGE_NAMES =
  /\b(Portuguese|Spanish|French|German|Italian|Dutch|Chinese|Mandarin|Japanese|Korean|Russian|Arabic|Hindi|Polish|Turkish|Swedish)\b/i;

// The few-shot shape itself, in either arrow style, independent of which
// language fills it in.
const FEWSHOT = /User write\s+\S+\s*(?:→|->)\s*reply/i;

// Build an install rooted at a temp dir. `withSkills: false` reproduces the
// standalone hook install, where the hooks directory is the only thing copied
// and every skillPathCandidate() misses.
function makeInstall({ withSkills }) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-langrule-'));
  const hooks = path.join(root, 'src', 'hooks');
  fs.mkdirSync(hooks, { recursive: true });
  for (const entry of fs.readdirSync(HOOKS_DIR)) {
    const src = path.join(HOOKS_DIR, entry);
    if (!fs.statSync(src).isFile()) continue;
    fs.copyFileSync(src, path.join(hooks, entry));
  }
  if (withSkills) fs.cpSync(SKILL_SRC, path.join(root, 'skills'), { recursive: true });
  return { root, hooks };
}

function activate({ withSkills }) {
  const install = makeInstall({ withSkills });
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-langhome-'));
  try {
    const res = spawnSync(process.execPath, [path.join(install.hooks, 'caveman-activate.js')], {
      input: JSON.stringify({
        session_id: 'langrule', cwd: '/tmp', hook_event_name: 'SessionStart', source: 'startup',
      }),
      encoding: 'utf8',
      // CLAUDE_PLUGIN_ROOT would add a third SKILL.md candidate and defeat the
      // point of the no-skills case, so it is cleared for both.
      env: { ...process.env, CLAUDE_CONFIG_DIR: home, CLAUDE_PLUGIN_ROOT: '' },
    });
    assert.strictEqual(res.status, 0, `hook exited ${res.status}: ${res.stderr}`);
    return res.stdout || '';
  } finally {
    fs.rmSync(install.root, { recursive: true, force: true });
    fs.rmSync(home, { recursive: true, force: true });
  }
}

// ── The two delivery paths ──────────────────────────────────────────────────

test('SKILL.md-backed ruleset teaches the language rule without naming a language', () => {
  const out = activate({ withSkills: true });
  // Sanity: this really is the SKILL.md path, not the fallback.
  assert.ok(
    out.includes('ASD-STE100'),
    'expected the SKILL.md ruleset; got the hardcoded fallback instead'
  );
  assert.ok(/language/i.test(out), 'the language rule went missing entirely');
  const hit = out.match(LANGUAGE_NAMES);
  assert.strictEqual(hit, null, `ruleset names a language as an example: ${hit && hit[0]}`);
  assert.strictEqual(FEWSHOT.test(out), false, 'ruleset still carries the few-shot example');
});

test('fallback ruleset teaches the language rule without naming a language', () => {
  const out = activate({ withSkills: false });
  // Sanity: no SKILL.md was reachable, so this is the hardcoded branch.
  assert.ok(
    out.includes('Respond terse like smart caveman'),
    'expected the hardcoded fallback ruleset'
  );
  assert.ok(!out.includes('ASD-STE100'), 'SKILL.md leaked in; this is not the fallback path');
  assert.ok(/language/i.test(out), 'the language rule went missing entirely');
  const hit = out.match(LANGUAGE_NAMES);
  assert.strictEqual(hit, null, `fallback names a language as an example: ${hit && hit[0]}`);
  assert.strictEqual(FEWSHOT.test(out), false, 'fallback still carries the few-shot example');
});

test('fallback still tells the model not to switch languages', () => {
  const out = activate({ withSkills: false });
  assert.match(out, /never switch/i, 'the fallback dropped the rule instead of the example');
});

// ── The same example must not come back anywhere it is injected from ────────

test('no injected ruleset source carries the "User write <Lang> → reply" few-shot', () => {
  const sources = [
    path.join(HOOKS_DIR, 'caveman-activate.js'),
    path.join(REPO_ROOT, 'src', 'rules', 'caveman-activate.md'),
    path.join(REPO_ROOT, 'src', 'rules', 'caveman-openclaw-bootstrap.md'),
    ...fs.readdirSync(SKILL_SRC)
      .map((d) => path.join(SKILL_SRC, d, 'SKILL.md'))
      .filter((p) => fs.existsSync(p)),
  ];
  const offenders = sources.filter((p) => FEWSHOT.test(fs.readFileSync(p, 'utf8')));
  assert.deepStrictEqual(
    offenders.map((p) => path.relative(REPO_ROOT, p)),
    [],
    'the language few-shot came back'
  );
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
