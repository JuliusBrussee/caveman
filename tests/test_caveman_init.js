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
  const cli = JSON.parse(fs.readFileSync(path.join(tmp, '.github/copilot/settings.json'), 'utf8'));
  assert.strictEqual(cli.extraKnownMarketplaces.caveman.source.repo, 'JuliusBrussee/caveman');
  assert.strictEqual(cli.enabledPlugins['caveman@caveman'], true);
});

test('idempotent: re-running on a clean install skips all', (tmp) => {
  runInit(tmp);
  const out = runInit(tmp);
  assert.match(out, /8 skipped/);
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

test('--dry-run: announces but writes nothing', (tmp) => {
  const out = runInit(tmp, '--dry-run');
  assert.match(out, /\(dry run\)/);
  assert.match(out, /7 added/);
  assert.ok(!fs.existsSync(path.join(tmp, '.cursor')));
  assert.ok(!fs.existsSync(path.join(tmp, '.windsurf')));
  assert.ok(!fs.existsSync(path.join(tmp, '.clinerules')));
  assert.ok(!fs.existsSync(path.join(tmp, '.github/copilot-instructions.md')));
  assert.ok(!fs.existsSync(path.join(tmp, '.github/copilot/settings.json')));
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

test('copilot-cli: --only writes the repo-level settings.json', (tmp) => {
  const out = runInit(tmp, '--only', 'copilot-cli');
  assert.match(out, /1 added/);
  const cli = JSON.parse(fs.readFileSync(path.join(tmp, '.github/copilot/settings.json'), 'utf8'));
  assert.deepStrictEqual(cli.extraKnownMarketplaces.caveman.source,
    { source: 'github', repo: 'JuliusBrussee/caveman' });
  assert.strictEqual(cli.enabledPlugins['caveman@caveman'], true);
});

test('copilot-cli: merges into an existing settings.json without clobbering', (tmp) => {
  const dir = path.join(tmp, '.github/copilot');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'settings.json'), JSON.stringify({
    model: 'claude-sonnet-4.5',
    enabledPlugins: { 'other@thing': true },
    extraKnownMarketplaces: {
      caveman: {
        autoUpdate: true,
        source: { source: 'github', repo: 'JuliusBrussee/caveman', ref: 'v1' },
      },
      thing: { source: { source: 'github', repo: 'acme/thing' } },
    },
  }, null, 2));
  const out = runInit(tmp, '--only', 'copilot-cli');
  assert.match(out, /appended/);
  const cli = JSON.parse(fs.readFileSync(path.join(dir, 'settings.json'), 'utf8'));
  assert.strictEqual(cli.extraKnownMarketplaces.caveman.source.repo, 'JuliusBrussee/caveman');
  assert.strictEqual(cli.enabledPlugins['caveman@caveman'], true);
  assert.strictEqual(cli.model, 'claude-sonnet-4.5');
  assert.strictEqual(cli.enabledPlugins['other@thing'], true);
  assert.strictEqual(cli.extraKnownMarketplaces.caveman.autoUpdate, true);
  assert.strictEqual(cli.extraKnownMarketplaces.caveman.source.ref, 'v1');
  assert.strictEqual(cli.extraKnownMarketplaces.thing.source.repo, 'acme/thing');
});

test('copilot-cli: idempotent when caveman is already enabled', (tmp) => {
  runInit(tmp, '--only', 'copilot-cli');
  const before = fs.readFileSync(path.join(tmp, '.github/copilot/settings.json'), 'utf8');
  const out = runInit(tmp, '--only', 'copilot-cli');
  assert.match(out, /skipped-already-installed/);
  const after = fs.readFileSync(path.join(tmp, '.github/copilot/settings.json'), 'utf8');
  assert.strictEqual(after, before);
});

test('copilot-cli: refuses to clobber an unparseable settings.json', (tmp) => {
  const dir = path.join(tmp, '.github/copilot');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'settings.json'), '{ not valid json ');
  const out = runInit(tmp, '--only', 'copilot-cli');
  assert.match(out, /skipped-unparseable/);
  assert.strictEqual(fs.readFileSync(path.join(dir, 'settings.json'), 'utf8'), '{ not valid json ');
});

test('copilot-cli: refuses to rewrite JSONC settings', (tmp) => {
  const dir = path.join(tmp, '.github/copilot');
  fs.mkdirSync(dir, { recursive: true });
  const body = '{\n  // keep this comment\n  \"enabledPlugins\": {}\n}\n';
  fs.writeFileSync(path.join(dir, 'settings.json'), body);
  const out = runInit(tmp, '--only', 'copilot-cli');
  assert.match(out, /skipped-jsonc/);
  assert.strictEqual(fs.readFileSync(path.join(dir, 'settings.json'), 'utf8'), body);
});

test('copilot-cli: refuses a conflicting marketplace source', (tmp) => {
  const dir = path.join(tmp, '.github/copilot');
  fs.mkdirSync(dir, { recursive: true });
  const body = JSON.stringify({
    extraKnownMarketplaces: {
      caveman: { source: { source: 'github', repo: 'other/caveman' } },
    },
  }, null, 2);
  fs.writeFileSync(path.join(dir, 'settings.json'), body);
  const out = runInit(tmp, '--only', 'copilot-cli');
  assert.match(out, /skipped-conflicting-marketplace/);
  assert.strictEqual(fs.readFileSync(path.join(dir, 'settings.json'), 'utf8'), body);
});

test('copilot-cli: refuses a symlinked settings directory', (tmp) => {
  if (process.platform === 'win32') return;
  const external = path.join(tmp, 'external');
  const github = path.join(tmp, '.github');
  fs.mkdirSync(external);
  fs.mkdirSync(github);
  fs.symlinkSync(external, path.join(github, 'copilot'), 'dir');
  const out = runInit(tmp, '--only', 'copilot-cli');
  assert.match(out, /skipped-unsafe-path/);
  assert.ok(!fs.existsSync(path.join(external, 'settings.json')));
});

test('copilot-cli: refuses a symlinked settings file', (tmp) => {
  if (process.platform === 'win32') return;
  const dir = path.join(tmp, '.github/copilot');
  const external = path.join(tmp, 'external.json');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(external, '{"secret":"keep"}\n');
  fs.symlinkSync(external, path.join(dir, 'settings.json'));
  const out = runInit(tmp, '--only', 'copilot-cli');
  assert.match(out, /skipped-unsafe-path/);
  assert.strictEqual(fs.readFileSync(external, 'utf8'), '{"secret":"keep"}\n');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
