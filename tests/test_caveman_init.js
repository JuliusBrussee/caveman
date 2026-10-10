#!/usr/bin/env node
// Tests for src/tools/caveman-init.js — fixture-based.
// Run: node tests/test_caveman_init.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');
const { execFileSync, spawnSync } = require('child_process');

const ROOT = path.resolve(__dirname, '..');
const INIT = path.join(ROOT, 'src', 'tools', 'caveman-init.js');

let passed = 0;
let failed = 0;

// Point OPENCLAW_WORKSPACE at a nonexistent dir inside the fixture so the
// openclaw target reports skipped-workspace-missing instead of writing to
// the developer's real ~/.openclaw/workspace.
function runInit(tmp, ...args) {
  return execFileSync(process.execPath, [INIT, tmp, ...args], {
    encoding: 'utf8',
    env: { ...process.env, OPENCLAW_WORKSPACE: path.join(tmp, 'no-openclaw') },
  });
}

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
  runInit(tmp);
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
  const opencode = fs.readFileSync(path.join(tmp, '.opencode/AGENTS.md'), 'utf8');
  assert.match(opencode, /Respond terse/);
});

test('idempotent: re-running on a clean install skips all', (tmp) => {
  runInit(tmp);
  const out = runInit(tmp);
  // 6 repo rule files skipped-already-installed + openclaw skipped (no workspace)
  assert.match(out, /7 skipped/);
  assert.doesNotMatch(out, /[1-9]\d* added/);
});

test('append mode: existing AGENTS.md gets caveman appended (not replaced)', (tmp) => {
  fs.writeFileSync(path.join(tmp, 'AGENTS.md'), '# My project\n\nDo not delete me.\n');
  runInit(tmp);
  const agents = fs.readFileSync(path.join(tmp, 'AGENTS.md'), 'utf8');
  assert.match(agents, /Do not delete me/);
  assert.match(agents, /Respond terse like smart caveman/);
});

test('skip mode: existing .cursor rule is not overwritten without --force', (tmp) => {
  const dir = path.join(tmp, '.cursor/rules');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'caveman.mdc'), '# original\nDo not delete me.\n');
  const out = runInit(tmp);
  assert.match(out, /\? .*\.cursor\/rules\/caveman\.mdc/);
  const after = fs.readFileSync(path.join(dir, 'caveman.mdc'), 'utf8');
  assert.strictEqual(after, '# original\nDo not delete me.\n');
});

test('--force overwrites existing rule files', (tmp) => {
  const dir = path.join(tmp, '.cursor/rules');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'caveman.mdc'), '# original\n');
  runInit(tmp, '--force');
  const after = fs.readFileSync(path.join(dir, 'caveman.mdc'), 'utf8');
  assert.match(after, /alwaysApply: true/);
  assert.match(after, /Respond terse/);
});

for (const [agent, file] of [
  ['cursor', '.cursor/rules/caveman.mdc'],
  ['windsurf', '.windsurf/rules/caveman.md'],
  ['cline', '.clinerules/caveman.md'],
]) {
  test(`--force refreshes an installed ${agent} rule; default and dry-run preserve it`, (tmp) => {
    runInit(tmp, '--only', agent);
    const target = path.join(tmp, file);
    const current = fs.readFileSync(target, 'utf8');
    const stale = current + '\nOld rule removed upstream.\n';
    fs.writeFileSync(target, stale);

    assert.match(runInit(tmp, '--only', agent), /skipped-already-installed/);
    assert.strictEqual(fs.readFileSync(target, 'utf8'), stale);

    const preview = runInit(tmp, '--only', agent, '--force', '--dry-run');
    assert.match(preview, /1 overwritten/);
    assert.strictEqual(fs.readFileSync(target, 'utf8'), stale);

    assert.match(runInit(tmp, '--only', agent, '--force'), /1 overwritten/);
    assert.strictEqual(fs.readFileSync(target, 'utf8'), current);
  });
}

test('--dry-run: announces but writes nothing', (tmp) => {
  const out = runInit(tmp, '--dry-run');
  assert.match(out, /\(dry run\)/);
  assert.match(out, /6 added/);
  assert.ok(!fs.existsSync(path.join(tmp, '.cursor')));
  assert.ok(!fs.existsSync(path.join(tmp, '.windsurf')));
  assert.ok(!fs.existsSync(path.join(tmp, '.clinerules')));
  assert.ok(!fs.existsSync(path.join(tmp, '.github/copilot-instructions.md')));
  assert.ok(!fs.existsSync(path.join(tmp, '.opencode')));
  assert.ok(!fs.existsSync(path.join(tmp, 'AGENTS.md')));
});

test('--only filters to one target', (tmp) => {
  const out = runInit(tmp, '--only', 'cline');
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
  const out = runInit(tmp, '--only', 'cline');
  assert.match(out, /skipped-already-installed/);
});

test('append mode fences its block so it can be refreshed and removed', (tmp) => {
  fs.writeFileSync(path.join(tmp, 'AGENTS.md'), '# My rules\n\nKeep these.\n');
  runInit(tmp, '--only', 'agents');
  const body = fs.readFileSync(path.join(tmp, 'AGENTS.md'), 'utf8');
  assert.match(body, /<!-- caveman-begin -->/);
  assert.match(body, /<!-- caveman-end -->/);
  assert.match(body, /^# My rules\n\nKeep these\./);

  // Re-run with an unchanged ruleset is a no-op...
  assert.match(runInit(tmp, '--only', 'agents'), /skipped-already-installed/);

  // ...but a changed ruleset refreshes IN PLACE, preserving user content on
  // both sides of the fence rather than appending a second block.
  const stale = body.replace(/(<!-- caveman-begin -->\n)/, '$1OLD RULESET\n');
  fs.writeFileSync(path.join(tmp, 'AGENTS.md'), stale + '\n# Trailing user section\n');
  assert.match(runInit(tmp, '--only', 'agents'), /refreshed/);
  const after = fs.readFileSync(path.join(tmp, 'AGENTS.md'), 'utf8');
  assert.equal(after.match(/<!-- caveman-begin -->/g).length, 1, 'must not stack a second block');
  assert.ok(!after.includes('OLD RULESET'), 'stale ruleset must be replaced');
  assert.match(after, /^# My rules/);
  assert.match(after, /# Trailing user section/);
});

test('--only rejects an unknown or missing agent id instead of silently no-opping', (tmp) => {
  const bad = spawnSync(process.execPath, [INIT, tmp, '--only', 'nope'], { encoding: 'utf8' });
  assert.equal(bad.status, 2);
  assert.match(bad.stderr, /--only requires one of/);
  const bare = spawnSync(process.execPath, [INIT, tmp, '--only'], { encoding: 'utf8' });
  assert.equal(bare.status, 2, 'bare --only must not install for every agent');
});

test('an orphan begin marker is damage, not a fence — user content survives re-runs', (tmp) => {
  // A truncated write or a bad merge leaves a BEGIN with no END. Pairing it
  // with a LATER block's END made the refresh replace everything between the
  // two, deleting the user's own content on the SECOND run.
  const agents = path.join(tmp, 'AGENTS.md');
  const original = '<!-- caveman-begin -->\nOLD RULE\n\n## MY TEAM CONVENTIONS\nNever force-push to main.\n';
  fs.writeFileSync(agents, original);
  for (let i = 0; i < 3; i++) {
    assert.match(runInit(tmp, '--only', 'agents'), /skipped-damaged-fence/);
  }
  assert.equal(fs.readFileSync(agents, 'utf8'), original, 'damaged-marker file must be left byte-identical');
});

test('an end marker above the begin marker is refused, not spliced', (tmp) => {
  const agents = path.join(tmp, 'AGENTS.md');
  const original = '## My notes\n<!-- caveman-end -->\nmore user text\n<!-- caveman-begin -->\nstale\n';
  fs.writeFileSync(agents, original);
  assert.match(runInit(tmp, '--only', 'agents'), /skipped-damaged-fence/);
  assert.equal(fs.readFileSync(agents, 'utf8'), original);
});

test('a symlinked rule file stays a link and its target is left alone', (tmp) => {
  if (process.platform === 'win32') return; // symlinks need developer mode
  // AGENTS.md -> CLAUDE.md is a common single-source setup. The atomic rename
  // used to swap the link for a regular copy, so the two files drifted.
  fs.writeFileSync(path.join(tmp, 'CLAUDE.md'), '# rules\n');
  fs.symlinkSync('CLAUDE.md', path.join(tmp, 'AGENTS.md'));
  fs.mkdirSync(path.join(tmp, '.github'));
  fs.symlinkSync('../AGENTS.md', path.join(tmp, '.github/copilot-instructions.md'));
  const out = runInit(tmp);
  assert.match(out, /AGENTS\.md \(skipped-symlink\)/);
  assert.match(out, /copilot-instructions\.md \(skipped-symlink\)/);
  assert.ok(fs.lstatSync(path.join(tmp, 'AGENTS.md')).isSymbolicLink());
  assert.ok(fs.lstatSync(path.join(tmp, '.github/copilot-instructions.md')).isSymbolicLink());
  assert.strictEqual(fs.readFileSync(path.join(tmp, 'CLAUDE.md'), 'utf8'), '# rules\n');
});

test('--force does not create an OpenClaw workspace that is not there', (tmp) => {
  // --force means "overwrite rule files"; it used to also mkdir
  // ~/.openclaw/workspace, after which every run "detected" OpenClaw.
  assert.match(runInit(tmp, '--force'), /skipped-workspace missing/);
  assert.strictEqual(fs.existsSync(path.join(tmp, 'no-openclaw')), false);
});

test('INSTALL.md repo-only commands write nothing outside the repo', (tmp) => {
  // The managed-env section used to promise "nothing outside the repo" for
  // `install.js --with-init`, which runs every global install first.
  const doc = fs.readFileSync(path.join(ROOT, 'INSTALL.md'), 'utf8');
  const commands = [...doc.matchAll(/^node src\/tools\/caveman-init\.js (.+)$/gm)].map(m => m[1].split(' '));
  assert.ok(commands.length, 'INSTALL.md documents no repo-only caveman-init command');
  const repo = path.join(tmp, 'repo');
  const home = path.join(tmp, 'home');
  const ws = path.join(home, '.openclaw', 'workspace');
  fs.mkdirSync(repo);
  fs.mkdirSync(ws, { recursive: true });
  for (const args of commands) {
    execFileSync(process.execPath, [INIT, ...args], {
      cwd: repo, env: { ...process.env, HOME: home, USERPROFILE: home, OPENCLAW_WORKSPACE: ws },
    });
  }
  assert.deepStrictEqual(fs.readdirSync(ws), [], 'repo-only init wrote into the OpenClaw workspace');
  assert.deepStrictEqual(fs.readdirSync(home), ['.openclaw']);
  assert.ok(fs.readdirSync(repo).length > 0, 'nothing written into the repo');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
