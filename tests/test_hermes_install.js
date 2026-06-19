const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { installHermes, uninstallHermes, appendBootstrapToMemory, stripBootstrapFromMemory } = require('../bin/lib/hermes');

function tmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-hermes-'));
}

test('install writes skill file and memory bootstrap', () => {
  const home = tmpDir();
  const repoRoot = path.resolve(__dirname, '..');
  const log = { write() {}, note() {}, warn() {} };

  const r = installHermes({ hermesHome: home, repoRoot, log });
  assert.ok(r.ok);
  assert.strictEqual(r.dryRun, undefined);

  // Skill file exists
  const skillPath = path.join(home, 'skills', 'productivity', 'caveman', 'SKILL.md');
  assert.ok(fs.existsSync(skillPath));
  const skillContent = fs.readFileSync(skillPath, 'utf8');
  assert.ok(skillContent.includes('caveman'));
  assert.ok(skillContent.includes('Respond terse'));

  // Memory bootstrap exists
  const memPath = path.join(home, 'memories', 'caveman');
  assert.ok(fs.existsSync(memPath));
  const memContent = fs.readFileSync(memPath, 'utf8');
  assert.ok(memContent.includes('<!-- caveman-begin -->'));
  assert.ok(memContent.includes('<!-- caveman-end -->'));

  // Cleanup
  fs.rmSync(home, { recursive: true, force: true });
});

test('install is idempotent', () => {
  const home = tmpDir();
  const repoRoot = path.resolve(__dirname, '..');
  const log = { write() {}, note() {}, warn() {} };

  const r1 = installHermes({ hermesHome: home, repoRoot, log });
  assert.ok(r1.ok);

  // Read memory content after first install
  const memPath = path.join(home, 'memories', 'caveman');
  const after1 = fs.readFileSync(memPath, 'utf8');

  // Second install should not duplicate the block
  const r2 = installHermes({ hermesHome: home, repoRoot, log });
  assert.ok(r2.ok);

  const after2 = fs.readFileSync(memPath, 'utf8');
  assert.strictEqual(after1, after2, 'memory should not change on re-install');

  // Skill file should still be there
  const skillPath = path.join(home, 'skills', 'productivity', 'caveman', 'SKILL.md');
  assert.ok(fs.existsSync(skillPath));

  fs.rmSync(home, { recursive: true, force: true });
});

test('dry run does not write files', () => {
  const home = tmpDir();
  const repoRoot = path.resolve(__dirname, '..');
  const log = { write() {}, note() {}, warn() {} };

  const r = installHermes({ hermesHome: home, repoRoot, dryRun: true, log });
  assert.ok(r.ok);
  assert.strictEqual(r.dryRun, true);

  // No files should exist
  const skillPath = path.join(home, 'skills', 'productivity', 'caveman', 'SKILL.md');
  const memPath = path.join(home, 'memories', 'caveman');
  assert.ok(!fs.existsSync(skillPath));
  assert.ok(!fs.existsSync(memPath));

  fs.rmSync(home, { recursive: true, force: true });
});

test('uninstall removes skill and strips bootstrap', () => {
  const home = tmpDir();
  const repoRoot = path.resolve(__dirname, '..');
  const log = { write() {}, note() {}, warn() {} };

  installHermes({ hermesHome: home, repoRoot, log });

  const r = uninstallHermes({ hermesHome: home, log });
  assert.ok(r.ok);
  assert.strictEqual(r.touched, true);

  // Skill directory should be gone
  const skillDir = path.join(home, 'skills', 'productivity', 'caveman');
  assert.ok(!fs.existsSync(skillDir));

  // Memory file should be removed (only contained our block)
  const memPath = path.join(home, 'memories', 'caveman');
  assert.ok(!fs.existsSync(memPath));

  fs.rmSync(home, { recursive: true, force: true });
});

test('uninstall preserves other memory content', () => {
  const home = tmpDir();
  const repoRoot = path.resolve(__dirname, '..');
  const log = { write() {}, note() {}, warn() {} };

  // Pre-populate memory with user content
  const memDir = path.join(home, 'memories');
  fs.mkdirSync(memDir, { recursive: true });
  fs.writeFileSync(path.join(memDir, 'caveman'), 'User memory entry here.\n', 'utf8');

  installHermes({ hermesHome: home, repoRoot, log });

  // Memory should have both user content and caveman block
  const memPath = path.join(memDir, 'caveman');
  const content = fs.readFileSync(memPath, 'utf8');
  assert.ok(content.includes('User memory entry'));
  assert.ok(content.includes('<!-- caveman-begin -->'));

  // Uninstall should preserve user content
  uninstallHermes({ hermesHome: home, log });

  const after = fs.readFileSync(memPath, 'utf8');
  assert.ok(after.includes('User memory entry'));
  assert.ok(!after.includes('<!-- caveman-begin -->'));

  fs.rmSync(home, { recursive: true, force: true });
});

test('appendBootstrapToMemory works on empty directory', () => {
  const home = tmpDir();
  const memPath = path.join(home, 'memories', 'caveman');
  fs.mkdirSync(path.dirname(memPath), { recursive: true });

  const snippet = '<!-- caveman-begin -->\ntest\n<!-- caveman-end -->\n';
  const r = appendBootstrapToMemory(memPath, snippet);
  assert.ok(r.changed);
  assert.ok(fs.existsSync(memPath));
  assert.ok(fs.readFileSync(memPath, 'utf8').includes('test'));

  fs.rmSync(home, { recursive: true, force: true });
});

test('stripBootstrapFromMemory returns no change when no markers', () => {
  const home = tmpDir();
  const memPath = path.join(home, 'memories', 'caveman');

  fs.mkdirSync(path.dirname(memPath), { recursive: true });
  fs.writeFileSync(memPath, 'just some user content\n', 'utf8');

  const r = stripBootstrapFromMemory(memPath);
  assert.strictEqual(r.changed, false);

  fs.rmSync(home, { recursive: true, force: true });
});
