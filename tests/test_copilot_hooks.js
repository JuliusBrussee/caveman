#!/usr/bin/env node
// Tests for the GitHub Copilot CLI hooks (sessionStart + userPromptSubmitted).
//
// Copilot CLI's hook contract differs from Claude Code's: a sessionStart hook
// injects context via a single-line JSON { "additionalContext": "..." } on
// stdout, and userPromptSubmitted output is NOT processed (side-effect only).
// These tests exercise the two hook scripts directly with synthetic payloads.
//
// Run: node tests/test_copilot_hooks.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');
const { execFileSync } = require('child_process');

const HOOKS_DIR = path.join(__dirname, '..', 'src', 'hooks');
const ACTIVATE = path.join(HOOKS_DIR, 'caveman-copilot-activate.js');
const PROMPT = path.join(HOOKS_DIR, 'caveman-copilot-mode-tracker.js');
const SKILL = path.join(__dirname, '..', 'skills', 'caveman', 'SKILL.md');

let passed = 0;
let failed = 0;

function test(name, fn) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-copilot-test-'));
  try {
    fn(tmp);
    passed++;
    console.log(`  \u2713 ${name}`);
  } catch (e) {
    failed++;
    console.error(`  \u2717 ${name}`);
    console.error(`    ${e.message}`);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

// Run the activate hook with a fresh COPILOT_HOME. Returns parsed stdout JSON.
function runActivate(copilotHome, extraEnv) {
  const env = Object.assign({}, process.env, {
    COPILOT_HOME: copilotHome,
    // Isolate from a real user/repo config so getDefaultMode resolves to 'full'.
    CAVEMAN_DEFAULT_MODE: '',
  }, extraEnv || {});
  delete env.CAVEMAN_DEFAULT_MODE; // ensure unset unless extraEnv sets it
  if (extraEnv && 'CAVEMAN_DEFAULT_MODE' in extraEnv) env.CAVEMAN_DEFAULT_MODE = extraEnv.CAVEMAN_DEFAULT_MODE;
  const out = execFileSync(process.execPath, [ACTIVATE], { encoding: 'utf8', env });
  return JSON.parse(out);
}

// Run the prompt hook with a synthetic userPromptSubmitted payload.
function runPrompt(copilotHome, prompt) {
  const env = Object.assign({}, process.env, { COPILOT_HOME: copilotHome });
  const out = execFileSync(process.execPath, [PROMPT], {
    encoding: 'utf8',
    env,
    input: JSON.stringify({ prompt, cwd: copilotHome, sessionId: 'x', timestamp: 1 }),
  });
  return out;
}

function flagPath(copilotHome) {
  return path.join(copilotHome, '.caveman-active');
}

console.log('Copilot CLI hook tests\n');

// ---------- sessionStart (activate) ----------

test('activate emits valid JSON with additionalContext', (tmp) => {
  const result = runActivate(tmp);
  assert.ok(typeof result.additionalContext === 'string', 'additionalContext should be a string');
  assert.ok(result.additionalContext.includes('CAVEMAN MODE ACTIVE'), 'should announce caveman mode');
});

test('activate output is single-line JSON (Copilot requirement)', (tmp) => {
  const env = Object.assign({}, process.env, { COPILOT_HOME: tmp });
  const out = execFileSync(process.execPath, [ACTIVATE], { encoding: 'utf8', env });
  assert.strictEqual(out.indexOf('\n'), -1, 'stdout must be a single line (newlines escaped)');
  JSON.parse(out); // must still parse
});

test('activate writes the mode flag under COPILOT_HOME', (tmp) => {
  runActivate(tmp);
  assert.strictEqual(fs.readFileSync(flagPath(tmp), 'utf8').trim(), 'full');
});

test('activate injects full SKILL.md ruleset when CAVEMAN_SKILL_PATH is set', (tmp) => {
  const withSkill = runActivate(tmp, { CAVEMAN_SKILL_PATH: SKILL });
  const withoutSkill = runActivate(tmp, { CAVEMAN_SKILL_PATH: path.join(tmp, 'does-not-exist.md') });
  assert.ok(
    withSkill.additionalContext.length > withoutSkill.additionalContext.length,
    'SKILL.md ruleset should be richer than the hardcoded fallback'
  );
});

test('activate honors a persisted flag over the default', (tmp) => {
  fs.writeFileSync(flagPath(tmp), 'ultra');
  const result = runActivate(tmp);
  assert.ok(result.additionalContext.includes('level: ultra'), 'should activate at the persisted ultra level');
});

test('activate emits no-op {} and clears flag in off mode', (tmp) => {
  fs.writeFileSync(flagPath(tmp), 'full');
  const result = runActivate(tmp, { CAVEMAN_DEFAULT_MODE: 'off' });
  assert.deepStrictEqual(result, {}, 'off mode should emit empty object');
  assert.ok(!fs.existsSync(flagPath(tmp)), 'off mode should clear the flag');
});

// ---------- userPromptSubmitted (prompt) ----------

test('prompt /caveman ultra persists ultra to the flag', (tmp) => {
  runPrompt(tmp, '/caveman ultra');
  assert.strictEqual(fs.readFileSync(flagPath(tmp), 'utf8').trim(), 'ultra');
});

test('prompt /caveman wenyan-full canonicalizes to wenyan', (tmp) => {
  runPrompt(tmp, '/caveman wenyan-full');
  assert.strictEqual(fs.readFileSync(flagPath(tmp), 'utf8').trim(), 'wenyan');
});

test('prompt "stop caveman" deletes the flag', (tmp) => {
  fs.writeFileSync(flagPath(tmp), 'ultra');
  runPrompt(tmp, 'please stop caveman now');
  assert.ok(!fs.existsSync(flagPath(tmp)), 'deactivation should remove the flag');
});

test('prompt natural-language activation writes the default mode', (tmp) => {
  runPrompt(tmp, 'turn on caveman mode please');
  assert.strictEqual(fs.readFileSync(flagPath(tmp), 'utf8').trim(), 'full');
});

test('prompt unknown /caveman arg leaves the flag untouched', (tmp) => {
  fs.writeFileSync(flagPath(tmp), 'ultra');
  runPrompt(tmp, '/caveman bogus');
  assert.strictEqual(fs.readFileSync(flagPath(tmp), 'utf8').trim(), 'ultra', 'unknown arg must not overwrite');
});

test('prompt independent slash command (/caveman-commit) is not persisted', (tmp) => {
  runPrompt(tmp, '/caveman-commit');
  assert.ok(!fs.existsSync(flagPath(tmp)), 'one-shot skill commands should not set the persistent flag');
});

test('prompt emits {} for an ordinary prompt', (tmp) => {
  const out = runPrompt(tmp, 'how do I center a div').trim();
  assert.strictEqual(out, '{}');
});

// ---------- skill-invocation rewrite (Copilot rewrites /caveman → skill call) ----------
// Because a skill named "caveman" is installed, Copilot CLI rewrites `/caveman ultra`
// into a skill-invocation prompt. The hook must still extract the mode from it.

test('prompt extracts mode from the skill-invocation rewrite', (tmp) => {
  runPrompt(tmp, 'Use the skill tool to invoke the "caveman" skill, then follow the skill\'s instructions to help with: ultra');
  assert.strictEqual(fs.readFileSync(flagPath(tmp), 'utf8').trim(), 'ultra');
});

test('prompt skill-invocation deactivation (help with: off) clears the flag', (tmp) => {
  fs.writeFileSync(flagPath(tmp), 'ultra');
  runPrompt(tmp, 'invoke the "caveman" skill ... help with: off');
  assert.ok(!fs.existsSync(flagPath(tmp)), 'help with: off should deactivate');
});

test('prompt skill-invocation with a non-mode arg leaves the flag untouched', (tmp) => {
  fs.writeFileSync(flagPath(tmp), 'ultra');
  runPrompt(tmp, 'Use the skill tool to invoke the "caveman" skill, then follow the skill\'s instructions to help with: explain closures');
  assert.strictEqual(fs.readFileSync(flagPath(tmp), 'utf8').trim(), 'ultra', 'non-mode arg must not overwrite');
});

// ---------- repo-scoped install (--repo) ----------

const INSTALLER = path.join(__dirname, '..', 'bin', 'install.js');

function runInstaller(args, cwd) {
  return execFileSync(process.execPath, [INSTALLER, ...args], {
    encoding: 'utf8',
    cwd,
    env: Object.assign({}, process.env),
  });
}

test('--repo installs self-contained hooks under .github/hooks/', (tmp) => {
  // a target repo is detected by a .git marker
  fs.mkdirSync(path.join(tmp, '.git'), { recursive: true });
  runInstaller(['--only', 'copilot-cli', '--repo', '--non-interactive'], tmp);

  const cfg = path.join(tmp, '.github', 'hooks', 'caveman.json');
  const scriptsDir = path.join(tmp, '.github', 'hooks', 'caveman');
  assert.ok(fs.existsSync(cfg), 'caveman.json written at .github/hooks/');
  for (const f of ['caveman-config.js', 'caveman-copilot-activate.js',
                   'caveman-copilot-mode-tracker.js', 'caveman-skill.md']) {
    assert.ok(fs.existsSync(path.join(scriptsDir, f)), `${f} copied into .github/hooks/caveman/`);
  }
});

test('--repo installs Agent Skills under .github/skills/', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.git'), { recursive: true });
  runInstaller(['--only', 'copilot-cli', '--repo', '--non-interactive'], tmp);
  const skillsDir = path.join(tmp, '.github', 'skills');
  for (const name of ['caveman', 'caveman-commit', 'caveman-review', 'caveman-compress', 'caveman-help']) {
    assert.ok(fs.existsSync(path.join(skillsDir, name, 'SKILL.md')), `${name}/SKILL.md installed`);
  }
  // compress ships its scripts/ too
  assert.ok(fs.existsSync(path.join(skillsDir, 'caveman-compress', 'scripts', 'compress.py')),
    'caveman-compress scripts bundled');
});

test('user-scope install drops Agent Skills under $COPILOT_HOME/skills/', (tmp) => {
  const home = path.join(tmp, 'copilot-home');
  execFileSync(process.execPath, [INSTALLER, '--only', 'copilot-cli', '--non-interactive'], {
    encoding: 'utf8',
    env: Object.assign({}, process.env, { COPILOT_HOME: home }),
  });
  assert.ok(fs.existsSync(path.join(home, 'skills', 'caveman', 'SKILL.md')), 'personal caveman skill installed');
  assert.ok(fs.existsSync(path.join(home, 'hooks', 'caveman.json')), 'hooks also installed');
});

test('--repo config is committable: bare node + relative paths', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.git'), { recursive: true });
  runInstaller(['--only', 'copilot-cli', '--repo', '--non-interactive'], tmp);

  const config = JSON.parse(fs.readFileSync(path.join(tmp, '.github', 'hooks', 'caveman.json'), 'utf8'));
  const start = config.hooks.sessionStart[0];
  assert.strictEqual(start.cwd, '.', 'cwd is repo root');
  assert.ok(/^node "\.github\/hooks\/caveman\//.test(start.command), 'bare node + repo-relative path');
  assert.ok(!/[A-Za-z]:\\/.test(start.command), 'no machine-specific absolute path baked in');
});

test('--repo activate hook injects full ruleset from repo location', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.git'), { recursive: true });
  runInstaller(['--only', 'copilot-cli', '--repo', '--non-interactive'], tmp);

  const activate = path.join(tmp, '.github', 'hooks', 'caveman', 'caveman-copilot-activate.js');
  // run with cwd = repo root, as Copilot would (cwd: ".")
  const out = execFileSync(process.execPath, [activate], { encoding: 'utf8', cwd: tmp });
  const obj = JSON.parse(out);
  assert.ok(obj.additionalContext.includes('CAVEMAN MODE ACTIVE'), 'ruleset injected');
  // full SKILL.md ruleset resolved via co-located caveman-skill.md (not the short fallback)
  assert.ok(obj.additionalContext.length > 1500, 'full SKILL ruleset, not fallback');
});

test('--uninstall --repo removes the repo hooks', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.git'), { recursive: true });
  runInstaller(['--only', 'copilot-cli', '--repo', '--non-interactive'], tmp);
  runInstaller(['--uninstall', '--repo', '--non-interactive'], tmp);
  assert.ok(!fs.existsSync(path.join(tmp, '.github', 'hooks', 'caveman.json')), 'caveman.json removed');
  assert.ok(!fs.existsSync(path.join(tmp, '.github', 'hooks', 'caveman')), 'scripts dir removed');
  assert.ok(!fs.existsSync(path.join(tmp, '.github', 'skills', 'caveman')), 'skills removed');
});

test('--repo wires CAVEMAN_FLAG_DIR and a .gitignore for the runtime flag', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.git'), { recursive: true });
  runInstaller(['--only', 'copilot-cli', '--repo', '--non-interactive'], tmp);
  const config = JSON.parse(fs.readFileSync(path.join(tmp, '.github', 'hooks', 'caveman.json'), 'utf8'));
  assert.strictEqual(config.hooks.userPromptSubmitted[0].env.CAVEMAN_FLAG_DIR, '.github/hooks/caveman',
    'flag dir is repo-relative');
  const gi = fs.readFileSync(path.join(tmp, '.github', 'hooks', 'caveman', '.gitignore'), 'utf8');
  assert.ok(gi.includes('.caveman-active'), 'runtime flag is gitignored');
});

test('repo-scoped flag is written repo-local, not global', (tmp) => {
  fs.mkdirSync(path.join(tmp, '.git'), { recursive: true });
  runInstaller(['--only', 'copilot-cli', '--repo', '--non-interactive'], tmp);
  const promptHook = path.join(tmp, '.github', 'hooks', 'caveman', 'caveman-copilot-mode-tracker.js');
  // run as Copilot would: cwd = repo root, CAVEMAN_FLAG_DIR set, isolated COPILOT_HOME
  const isolatedHome = path.join(tmp, 'fake-home');
  fs.mkdirSync(isolatedHome, { recursive: true });
  execFileSync(process.execPath, [promptHook], {
    encoding: 'utf8',
    cwd: tmp,
    input: JSON.stringify({ prompt: '/caveman ultra', cwd: tmp, sessionId: 's', timestamp: 1 }),
    env: Object.assign({}, process.env, { CAVEMAN_FLAG_DIR: '.github/hooks/caveman', COPILOT_HOME: isolatedHome }),
  });
  assert.strictEqual(
    fs.readFileSync(path.join(tmp, '.github', 'hooks', 'caveman', '.caveman-active'), 'utf8').trim(), 'ultra',
    'flag written repo-local');
  assert.ok(!fs.existsSync(path.join(isolatedHome, '.caveman-active')), 'no global flag written');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
