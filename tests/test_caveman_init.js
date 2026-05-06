#!/usr/bin/env node
// Tests for tools/caveman-init.js — fixture-based.
// Run: node tests/test_caveman_init.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');
const { execFileSync } = require('child_process');

const ROOT = path.resolve(__dirname, '..');
const INIT = path.join(ROOT, 'tools', 'caveman-init.js');

let passed = 0;
let failed = 0;

function test(name, fn) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-init-test-'));
  try {
    fn(tmp);
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}\n    ${e.message}`);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

console.log('caveman-init tests\n');

test('greenfield: creates all rule files with proper frontmatter', (tmp) => {
  execFileSync(process.execPath, [INIT, tmp], { encoding: 'utf8' });
  const cursor = fs.readFileSync(path.join(tmp, '.cursor/rules/caveman.mdc'), 'utf8');
  assert.match(cursor, /alwaysApply: true/);
  assert.match(cursor, /Respond terse like smart caveman/);
  const windsurf = fs.readFileSync(path.join(tmp, '.windsurf/rules/caveman.md'), 'utf8');
  assert.match(windsurf, /trigger: always_on/);
  const cline = fs.readFileSync(path.join(tmp, '.clinerules/caveman.md'), 'utf8');
  assert.match(cline, /^Respond terse/);
  const copilot = fs.readFileSync(path.join(tmp, '.github/copilot-instructions.md'), 'utf8');
  assert.match(copilot, /Respond terse/);
  const agents = fs.readFileSync(path.join(tmp, 'AGENTS.md'), 'utf8');
  assert.match(agents, /Respond terse/);
});

test('idempotent: re-running on a clean install skips all', (tmp) => {
  execFileSync(process.execPath, [INIT, tmp], { encoding: 'utf8' });
  const out = execFileSync(process.execPath, [INIT, tmp], { encoding: 'utf8' });
  assert.match(out, /5 skipped/);
  assert.doesNotMatch(out, /[1-9]\d* added/);
});

test('append mode: existing AGENTS.md gets caveman appended (not replaced)', (tmp) => {
  fs.writeFileSync(path.join(tmp, 'AGENTS.md'), '# My project\n\nDo not delete me.\n');
  execFileSync(process.execPath, [INIT, tmp], { encoding: 'utf8' });
  const agents = fs.readFileSync(path.join(tmp, 'AGENTS.md'), 'utf8');
  assert.match(agents, /Do not delete me/);
  assert.match(agents, /Respond terse like smart caveman/);
});

test('skip mode: existing .cursor rule is not overwritten without --force', (tmp) => {
  const dir = path.join(tmp, '.cursor/rules');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'caveman.mdc'), '# original\nDo not delete me.\n');
  const out = execFileSync(process.execPath, [INIT, tmp], { encoding: 'utf8' });
  assert.match(out, /\? .*\.cursor\/rules\/caveman\.mdc/);
  const after = fs.readFileSync(path.join(dir, 'caveman.mdc'), 'utf8');
  assert.strictEqual(after, '# original\nDo not delete me.\n');
});

test('--force overwrites existing rule files', (tmp) => {
  const dir = path.join(tmp, '.cursor/rules');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'caveman.mdc'), '# original\n');
  execFileSync(process.execPath, [INIT, tmp, '--force'], { encoding: 'utf8' });
  const after = fs.readFileSync(path.join(dir, 'caveman.mdc'), 'utf8');
  assert.match(after, /alwaysApply: true/);
  assert.match(after, /Respond terse/);
});

test('--dry-run: announces but writes nothing', (tmp) => {
  const out = execFileSync(process.execPath, [INIT, tmp, '--dry-run'], { encoding: 'utf8' });
  assert.match(out, /\(dry run\)/);
  assert.match(out, /5 added/);
  assert.ok(!fs.existsSync(path.join(tmp, '.cursor')));
  assert.ok(!fs.existsSync(path.join(tmp, '.windsurf')));
  assert.ok(!fs.existsSync(path.join(tmp, '.clinerules')));
  assert.ok(!fs.existsSync(path.join(tmp, '.github/copilot-instructions.md')));
  assert.ok(!fs.existsSync(path.join(tmp, 'AGENTS.md')));
});

test('--only filters to one target', (tmp) => {
  const out = execFileSync(process.execPath, [INIT, tmp, '--only', 'cline'], { encoding: 'utf8' });
  assert.match(out, /1 added/);
  assert.ok(fs.existsSync(path.join(tmp, '.clinerules/caveman.md')));
  assert.ok(!fs.existsSync(path.join(tmp, '.cursor')));
});

test('detects sentinel and skips files that already have caveman content', (tmp) => {
  // Hand-write a file that already contains the rule (simulating prior install).
  const dir = path.join(tmp, '.clinerules');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'caveman.md'),
    '# Existing\n\nRespond terse like smart caveman. Hello.\n');
  const out = execFileSync(process.execPath, [INIT, tmp, '--only', 'cline'], { encoding: 'utf8' });
  assert.match(out, /skipped-already-installed/);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
