import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import { createRequire } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(HERE, '..', '..');
const require = createRequire(import.meta.url);
const { assertHermesPlatform } = require(path.join(ROOT, 'bin', 'lib', 'hermes.js'));
const INSTALLER = path.join(ROOT, 'bin', 'install.js');
const FAKE_HERMES = path.join(ROOT, 'tests', 'hermes', 'fixtures', 'fake-hermes.mjs');
const TEST_PYTHON = spawnSync('python3', ['-c', 'import sys; print(sys.executable)'], { encoding: 'utf8' }).stdout.trim();
const SKILLS = ['caveman', 'caveman-commit', 'caveman-review', 'caveman-help', 'caveman-stats', 'caveman-compress', 'cavecrew'];

test('native Hermes integration rejects unsupported Windows before filesystem mutation', () => {
  assert.throws(() => assertHermesPlatform('win32'), /WSL2/);
  assert.doesNotThrow(() => assertHermesPlatform('linux'));
  assert.doesNotThrow(() => assertHermesPlatform('darwin'));
});

function fixture(initialConfig = { unrelated: { keep: true }, plugins: { enabled: ['sibling'] }, mcp_servers: { sibling: { command: 'sibling', args: ['--keep'] } } }) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-hermes-installer-'));
  const home = path.join(root, 'hermes');
  const bin = path.join(root, 'bin');
  const pythonModules = path.join(root, 'python-modules');
  fs.mkdirSync(home, { recursive: true });
  fs.mkdirSync(bin, { recursive: true });
  fs.mkdirSync(path.join(pythonModules, 'hermes_cli'), { recursive: true });
  fs.writeFileSync(path.join(pythonModules, 'hermes_cli', '__init__.py'), '');
  fs.writeFileSync(path.join(pythonModules, 'hermes_cli', 'config.py'), [
    'import json',
    'import os',
    'from pathlib import Path',
    'def read_raw_config():',
    '    path = Path(os.environ["HERMES_HOME"]) / "config.yaml"',
    '    try:',
    '        return json.loads(path.read_text(encoding="utf-8"))',
    '    except FileNotFoundError:',
    '        return {}',
    '',
  ].join('\n'));
  const fake = path.join(bin, 'hermes');
  fs.copyFileSync(FAKE_HERMES, fake);
  fs.chmodSync(fake, 0o755);
  fs.writeFileSync(path.join(home, 'config.yaml'), JSON.stringify(initialConfig, null, 2) + '\n', { mode: 0o600 });
  return {
    root, home, bin,
    env: {
      ...process.env,
      HOME: path.join(root, 'home'),
      HERMES_HOME: home,
      XDG_CONFIG_HOME: path.join(root, 'xdg'),
      XDG_STATE_HOME: path.join(root, 'state'),
      XDG_CACHE_HOME: path.join(root, 'cache'),
      FAKE_HERMES_LOG: path.join(root, 'hermes.log'),
      CAVEMAN_HERMES_PYTHON: TEST_PYTHON,
      PYTHONPATH: `${pythonModules}${path.delimiter}${process.env.PYTHONPATH || ''}`,
      PATH: `${bin}${path.delimiter}${process.env.PATH}`,
      NO_COLOR: '1',
    },
    cleanup() { fs.rmSync(root, { recursive: true, force: true }); },
  };
}

function run(f, args) {
  const hasMcpChoice = args.some(arg => arg === '--no-mcp-shrink' || arg === '--with-mcp-shrink' || arg.startsWith('--with-mcp-shrink='));
  const finalArgs = [...args, '--non-interactive'];
  if (!hasMcpChoice) finalArgs.push('--no-mcp-shrink');
  return spawnSync('node', [INSTALLER, ...finalArgs], { env: f.env, encoding: 'utf8', timeout: 30_000 });
}

function config(f) {
  return JSON.parse(fs.readFileSync(path.join(f.home, 'config.yaml'), 'utf8'));
}
function pluginDir(f) { return path.join(f.home, 'plugins', 'caveman'); }
function skillDir(f, name) { return path.join(f.home, 'skills', 'productivity', name); }
function stateDir(f) { return path.join(f.home, 'caveman'); }
function manifestPath(f) { return path.join(stateDir(f), 'install-manifest.json'); }
function sha(file) { return crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex'); }
function allFiles(dir) {
  if (!fs.existsSync(dir)) return [];
  const out = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...allFiles(full));
    else if (entry.isFile()) out.push(full);
  }
  return out;
}

function assertInstalled(f) {
  for (const rel of ['plugin.yaml', '__init__.py', 'hermes_caveman/config.py', 'hermes_caveman/state.py', 'src/tools/caveman-init.js', 'src/mcp-servers/caveman-shrink/index.js']) {
    assert.ok(fs.existsSync(path.join(pluginDir(f), rel)), `plugin payload missing ${rel}`);
  }
  for (const name of SKILLS) {
    assert.ok(fs.existsSync(path.join(pluginDir(f), 'skills', name, 'SKILL.md')), `plugin skill missing ${name}`);
    assert.ok(fs.existsSync(path.join(skillDir(f, name), 'SKILL.md')), `global skill missing ${name}`);
  }
}

function install(f, extra = []) {
  return run(f, ['--only', 'hermes', ...extra]);
}

test('fresh install lands and enables native plugin, seven bare skills, and a complete ownership manifest', () => {
  const f = fixture();
  try {
    const r = install(f);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assertInstalled(f);
    const cfg = config(f);
    assert.deepEqual(cfg.unrelated, { keep: true });
    assert.ok(cfg.plugins.enabled.includes('caveman'));
    assert.ok(cfg.plugins.enabled.includes('sibling'));
    assert.ok(!(cfg.plugins.disabled || []).includes('caveman'));

    const manifest = JSON.parse(fs.readFileSync(manifestPath(f), 'utf8'));
    assert.equal(manifest.version, 1);
    assert.equal(manifest.plugin, 'caveman');
    assert.deepEqual(manifest.pluginEntry, { entry: false, previousPresent: false, previous: null });
    assert.equal(cfg.plugins.entries.caveman.allow_tool_override, false);
    assert.ok(Array.isArray(manifest.files) && manifest.files.length > 10);
    assert.equal(fs.statSync(manifestPath(f)).mode & 0o777, 0o600);
    const installed = [...allFiles(pluginDir(f)), ...SKILLS.flatMap(name => allFiles(skillDir(f, name)))];
    const byPath = new Map(manifest.files.map(item => [item.path, item]));
    for (const file of installed) {
      const rel = path.relative(f.home, file);
      assert.ok(!rel.startsWith('..'));
      assert.equal(byPath.get(rel)?.sha256, sha(file), `manifest digest missing/wrong: ${rel}`);
    }
  } finally { f.cleanup(); }
});

test('Hermes tool-override policy is ownership-aware across install, rollback, and uninstall', () => {
  const generated = fixture();
  try {
    assert.equal(install(generated).status, 0);
    assert.equal(config(generated).plugins.entries.caveman.allow_tool_override, false);
    assert.equal(run(generated, ['--disable', '--only', 'hermes']).status, 0);
    const removed = run(generated, ['--uninstall', '--only', 'hermes']);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
    assert.equal(config(generated).plugins.entries?.caveman, undefined);
  } finally { generated.cleanup(); }

  const granted = fixture({
    plugins: {
      enabled: ['sibling'],
      disabled: ['other'],
      entries: { caveman: { allow_tool_override: true }, sibling: { keep: true } },
    },
  });
  try {
    assert.equal(install(granted).status, 0);
    assert.equal(config(granted).plugins.entries.caveman.allow_tool_override, true);
    assert.equal(JSON.parse(fs.readFileSync(manifestPath(granted), 'utf8')).pluginEntry, null);
    const removed = run(granted, ['--uninstall', '--only', 'hermes']);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
    assert.equal(config(granted).plugins.entries.caveman.allow_tool_override, true);
    assert.deepEqual(config(granted).plugins.entries.sibling, { keep: true });
  } finally { granted.cleanup(); }
});

test('dry-run reports plugin/config/skills but writes nothing', () => {
  const f = fixture();
  try {
    const before = fs.readFileSync(path.join(f.home, 'config.yaml'));
    const r = install(f, ['--dry-run']);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assert.match(r.stdout, /plugin/i);
    assert.match(r.stdout, /enable/i);
    assert.equal(fs.existsSync(pluginDir(f)), false);
    assert.equal(fs.existsSync(path.join(f.home, 'skills')), false);
    assert.equal(fs.existsSync(manifestPath(f)), false);
    assert.deepEqual(fs.readFileSync(path.join(f.home, 'config.yaml')), before);
  } finally { f.cleanup(); }
});

test('unowned same-name collision is preflight-fatal; --force backs it up before overwrite', () => {
  const f = fixture();
  try {
    const collision = path.join(skillDir(f, 'caveman'), 'SKILL.md');
    fs.mkdirSync(path.dirname(collision), { recursive: true });
    fs.writeFileSync(collision, 'owner content\n');
    const refused = install(f);
    assert.notEqual(refused.status, 0, refused.stdout);
    assert.equal(fs.readFileSync(collision, 'utf8'), 'owner content\n');
    assert.equal(fs.existsSync(pluginDir(f)), false, 'preflight must avoid partial install');

    const forced = install(f, ['--force']);
    assert.equal(forced.status, 0, forced.stderr || forced.stdout);
    assert.notEqual(fs.readFileSync(collision, 'utf8'), 'owner content\n');
    const backups = allFiles(path.join(stateDir(f), 'collision-backups'));
    assert.ok(backups.some(file => fs.readFileSync(file, 'utf8') === 'owner content\n'));
  } finally { f.cleanup(); }
});

test('symlink collision is refused even with --force and never touches target', () => {
  const f = fixture();
  try {
    const victim = path.join(f.root, 'victim');
    fs.mkdirSync(victim);
    fs.writeFileSync(path.join(victim, 'SKILL.md'), 'victim\n');
    fs.mkdirSync(path.dirname(skillDir(f, 'caveman')), { recursive: true });
    fs.symlinkSync(victim, skillDir(f, 'caveman'));
    const r = install(f, ['--force']);
    assert.notEqual(r.status, 0);
    assert.equal(fs.readFileSync(path.join(victim, 'SKILL.md'), 'utf8'), 'victim\n');
    assert.equal(fs.existsSync(pluginDir(f)), false);
  } finally { f.cleanup(); }
});

test('reinstall updates unchanged owned files but preserves local modifications unless forced', () => {
  const f = fixture();
  try {
    assert.equal(install(f).status, 0);
    const modified = path.join(pluginDir(f), 'plugin.yaml');
    fs.appendFileSync(modified, '# local owner edit\n');
    const preserved = install(f);
    assert.equal(preserved.status, 0, preserved.stderr || preserved.stdout);
    assert.match(fs.readFileSync(modified, 'utf8'), /local owner edit/);
    assert.match(preserved.stdout + preserved.stderr, /modified|preserv/i);

    const forced = install(f, ['--force']);
    assert.equal(forced.status, 0, forced.stderr || forced.stdout);
    assert.doesNotMatch(fs.readFileSync(modified, 'utf8'), /local owner edit/);
    const backups = allFiles(path.join(stateDir(f), 'collision-backups'));
    assert.ok(backups.some(file => fs.readFileSync(file, 'utf8').includes('local owner edit')));
  } finally { f.cleanup(); }
});

test('reinstall removes obsolete unchanged payload files but preserves modified obsolete files', () => {
  const f = fixture();
  try {
    assert.equal(install(f).status, 0);
    const unchanged = path.join(pluginDir(f), 'obsolete.py');
    const modified = path.join(pluginDir(f), 'modified-obsolete.py');
    fs.writeFileSync(unchanged, 'old owned payload\n');
    fs.writeFileSync(modified, 'old owned payload\n');
    const manifest = JSON.parse(fs.readFileSync(manifestPath(f), 'utf8'));
    manifest.files.push(
      { path: path.relative(f.home, unchanged).split(path.sep).join('/'), sha256: sha(unchanged) },
      { path: path.relative(f.home, modified).split(path.sep).join('/'), sha256: sha(modified) },
    );
    fs.writeFileSync(manifestPath(f), JSON.stringify(manifest, null, 2) + '\n');
    fs.writeFileSync(modified, 'user edit\n');

    const r = install(f);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assert.equal(fs.existsSync(unchanged), false);
    assert.equal(fs.readFileSync(modified, 'utf8'), 'user edit\n');
    const next = JSON.parse(fs.readFileSync(manifestPath(f), 'utf8'));
    assert.ok(!next.files.some(item => item.path.endsWith('/obsolete.py')));
    assert.ok(!next.files.some(item => item.path.endsWith('/modified-obsolete.py')));
  } finally { f.cleanup(); }
});

test('--disable removes runtime registration while preserving plugin files, Caveman state, and history', () => {
  const f = fixture();
  try {
    assert.equal(install(f).status, 0);
    fs.writeFileSync(path.join(stateDir(f), 'mode.json'), '{"mode":"full"}\n');
    fs.writeFileSync(path.join(stateDir(f), 'history.jsonl'), '{"output_tokens":10}\n');
    const r = run(f, ['--disable', '--only', 'hermes']);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assertInstalled(f);
    assert.ok(fs.existsSync(path.join(stateDir(f), 'mode.json')));
    assert.ok(fs.existsSync(path.join(stateDir(f), 'history.jsonl')));
    const cfg = config(f);
    assert.ok(!cfg.plugins.enabled.includes('caveman'));
    assert.ok(cfg.plugins.disabled.includes('caveman'));
  } finally { f.cleanup(); }
});

test('uninstall without an ownership manifest leaves a same-name user plugin enabled', () => {
  const f = fixture({ plugins: { enabled: ['caveman', 'sibling'], disabled: [] } });
  try {
    const r = run(f, ['--uninstall', '--only', 'hermes']);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assert.deepEqual(config(f).plugins.enabled, ['caveman', 'sibling']);
    assert.match(r.stdout, /ownership manifest not found/i);
  } finally { f.cleanup(); }
});

test('uninstall removes unchanged owned runtime, ephemeral state, and exact config while preserving siblings and history', () => {
  const f = fixture();
  try {
    assert.equal(install(f).status, 0);
    fs.mkdirSync(path.join(f.home, 'plugins', 'sibling'), { recursive: true });
    fs.writeFileSync(path.join(f.home, 'plugins', 'sibling', 'keep'), 'keep');
    fs.mkdirSync(skillDir(f, 'sibling'), { recursive: true });
    fs.writeFileSync(path.join(skillDir(f, 'sibling'), 'SKILL.md'), 'keep');
    fs.writeFileSync(path.join(stateDir(f), 'mode.json'), '{}');
    fs.mkdirSync(path.join(stateDir(f), 'sessions'), { recursive: true });
    fs.writeFileSync(path.join(stateDir(f), 'sessions', 'x.json'), '{}');
    fs.writeFileSync(path.join(stateDir(f), 'history.jsonl'), '{"output_tokens":10}\n');

    const r = run(f, ['--uninstall', '--only', 'hermes']);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assert.equal(fs.existsSync(pluginDir(f)), false);
    for (const name of SKILLS) assert.equal(fs.existsSync(skillDir(f, name)), false, name);
    assert.ok(fs.existsSync(path.join(f.home, 'plugins', 'sibling', 'keep')));
    assert.ok(fs.existsSync(path.join(skillDir(f, 'sibling'), 'SKILL.md')));
    assert.equal(fs.existsSync(path.join(stateDir(f), 'mode.json')), false);
    assert.equal(fs.existsSync(path.join(stateDir(f), 'sessions')), false);
    assert.ok(fs.existsSync(path.join(stateDir(f), 'history.jsonl')));
    assert.match(r.stdout, new RegExp(path.basename(f.home) + '|history', 'i'));
    assert.equal(fs.existsSync(manifestPath(f)), false);
    const cfg = config(f);
    assert.ok(!cfg.plugins.enabled.includes('caveman'));
    assert.ok(!(cfg.plugins.disabled || []).includes('caveman'));
    assert.deepEqual(cfg.unrelated, { keep: true });
  } finally { f.cleanup(); }
});

test('uninstall removes generated Python caches without deleting unrelated cache content', () => {
  const f = fixture();
  try {
    assert.equal(install(f).status, 0);
    const pluginCache = path.join(pluginDir(f), '__pycache__');
    const skillCache = path.join(skillDir(f, 'cavecrew'), '__pycache__');
    fs.mkdirSync(pluginCache, { recursive: true });
    fs.mkdirSync(skillCache, { recursive: true });
    fs.writeFileSync(path.join(pluginCache, '__init__.cpython-311.pyc'), 'generated');
    fs.writeFileSync(path.join(skillCache, 'skill.cpython-311.pyc'), 'generated');
    fs.writeFileSync(path.join(skillCache, 'keep.txt'), 'user content');

    const r = run(f, ['--uninstall', '--only', 'hermes']);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assert.equal(fs.existsSync(pluginDir(f)), false);
    assert.equal(fs.existsSync(path.join(skillCache, 'skill.cpython-311.pyc')), false);
    assert.equal(fs.readFileSync(path.join(skillCache, 'keep.txt'), 'utf8'), 'user content');
  } finally { f.cleanup(); }
});

test('uninstall preserves modified owned files but removes their active registration', () => {
  const f = fixture();
  try {
    assert.equal(install(f).status, 0);
    const modified = path.join(pluginDir(f), 'plugin.yaml');
    fs.appendFileSync(modified, '# keep me\n');
    const r = run(f, ['--uninstall', '--only', 'hermes']);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assert.match(fs.readFileSync(modified, 'utf8'), /keep me/);
    assert.match(r.stdout + r.stderr, /modified|preserv/i);
    const cfg = config(f);
    assert.ok(!cfg.plugins.enabled.includes('caveman'));
    assert.ok(!(cfg.plugins.disabled || []).includes('caveman'));
  } finally { f.cleanup(); }
});

test('failed lifecycle cleanup preserves config, owned files, and manifest for recovery', () => {
  const f = fixture();
  try {
    assert.equal(install(f).status, 0);
    const beforeConfig = config(f);
    const beforeManifest = fs.readFileSync(manifestPath(f), 'utf8');
    f.env.FAKE_HERMES_FAIL_REMOVE = '1';
    const r = run(f, ['--uninstall', '--only', 'hermes']);
    assert.notEqual(r.status, 0);
    assertInstalled(f);
    assert.deepEqual(config(f), beforeConfig);
    assert.equal(fs.readFileSync(manifestPath(f), 'utf8'), beforeManifest);
  } finally { f.cleanup(); }
});

test('partial Hermes enable failure restores disabled status and exact policy', () => {
  const f = fixture({
    unrelated: { keep: true },
    plugins: {
      enabled: ['sibling'],
      disabled: ['caveman'],
      entries: { caveman: { allow_tool_override: true, owner: 'user' } },
    },
  });
  try {
    const before = config(f);
    f.env.FAKE_HERMES_PARTIAL_ENABLE_ONCE = '1';
    const failed = install(f);
    assert.notEqual(failed.status, 0);
    assert.deepEqual(config(f), before);
    assert.equal(fs.existsSync(pluginDir(f)), false);
    assert.equal(fs.existsSync(manifestPath(f)), false);
  } finally { f.cleanup(); }
});

test('failed reinstall restores every overwritten owned file, manifest, and plugin config', () => {
  const f = fixture();
  try {
    assert.equal(install(f).status, 0);
    const owned = path.join(pluginDir(f), 'plugin.yaml');
    fs.writeFileSync(owned, 'old owned payload\n');
    const manifest = JSON.parse(fs.readFileSync(manifestPath(f), 'utf8'));
    manifest.files.find(item => item.path === path.relative(f.home, owned)).sha256 = sha(owned);
    fs.writeFileSync(manifestPath(f), JSON.stringify(manifest, null, 2) + '\n');
    assert.equal(run(f, ['--disable', '--only', 'hermes']).status, 0);
    const beforeConfig = config(f);
    const beforeManifest = fs.readFileSync(manifestPath(f), 'utf8');

    f.env.FAKE_HERMES_FAIL_SET_MCP = '1';
    const r = install(f, ['--with-mcp-shrink=node upstream']);
    assert.notEqual(r.status, 0);
    assert.equal(fs.readFileSync(owned, 'utf8'), 'old owned payload\n');
    assert.equal(fs.readFileSync(manifestPath(f), 'utf8'), beforeManifest);
    assert.deepEqual(config(f), beforeConfig);
  } finally { f.cleanup(); }
});

test('uninstall refuses a symlinked Caveman state root without touching its target', () => {
  const f = fixture();
  const victim = path.join(f.root, 'state-victim');
  try {
    assert.equal(install(f).status, 0);
    fs.renameSync(stateDir(f), victim);
    fs.symlinkSync(victim, stateDir(f), 'dir');
    const marker = path.join(victim, 'sessions', 'owner.txt');
    fs.mkdirSync(path.dirname(marker), { recursive: true });
    fs.writeFileSync(marker, 'owner\n');

    const refused = run(f, ['--uninstall', '--only', 'hermes']);
    assert.notEqual(refused.status, 0);
    assert.equal(fs.readFileSync(marker, 'utf8'), 'owner\n');
    assert.ok(fs.existsSync(path.join(victim, 'install-manifest.json')));
    assert.ok(config(f).plugins.enabled.includes('caveman'));
    assert.ok(fs.existsSync(pluginDir(f)));
  } finally { f.cleanup(); }
});

test('uninstall rejects ownership-manifest traversal and preserves the target', () => {
  const f = fixture();
  try {
    assert.equal(install(f).status, 0);
    const victim = path.join(f.home, 'victim.txt');
    fs.writeFileSync(victim, 'keep me\n');
    const manifest = JSON.parse(fs.readFileSync(manifestPath(f), 'utf8'));
    manifest.files.push({
      path: 'plugins/caveman/../../victim.txt',
      sha256: sha(victim),
    });
    fs.writeFileSync(manifestPath(f), JSON.stringify(manifest, null, 2) + '\n');

    const r = run(f, ['--uninstall', '--only', 'hermes']);
    assert.notEqual(r.status, 0);
    assert.equal(fs.readFileSync(victim, 'utf8'), 'keep me\n');
    assert.match(r.stdout + r.stderr, /invalid owned path|preserv/i);
  } finally { f.cleanup(); }
});

test('uninstall rejects an MCP ownership record that does not describe the managed proxy', () => {
  const f = fixture();
  try {
    assert.equal(install(f).status, 0);
    const manifest = JSON.parse(fs.readFileSync(manifestPath(f), 'utf8'));
    manifest.mcp = {
      name: 'sibling',
      entry: { command: 'sibling', args: ['--keep'] },
      previous: null,
      previousPresent: false,
    };
    fs.writeFileSync(manifestPath(f), JSON.stringify(manifest, null, 2) + '\n');

    const r = run(f, ['--uninstall', '--only', 'hermes']);
    assert.notEqual(r.status, 0);
    assert.deepEqual(config(f).mcp_servers.sibling, { command: 'sibling', args: ['--keep'] });
    assert.match(r.stdout + r.stderr, /invalid MCP ownership/i);
  } finally { f.cleanup(); }
});

test('--purge-history deletes only a regular owned history path and refuses a symlink target', () => {
  const regular = fixture();
  try {
    assert.equal(install(regular).status, 0);
    fs.writeFileSync(path.join(stateDir(regular), 'history.jsonl'), '{}\n');
    const r = run(regular, ['--uninstall', '--only', 'hermes', '--purge-history']);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assert.equal(fs.existsSync(path.join(stateDir(regular), 'history.jsonl')), false);
  } finally { regular.cleanup(); }

  const linked = fixture();
  try {
    assert.equal(install(linked).status, 0);
    const victim = path.join(linked.root, 'lifetime.jsonl');
    fs.writeFileSync(victim, 'owner lifetime\n');
    const history = path.join(stateDir(linked), 'history.jsonl');
    fs.symlinkSync(victim, history);
    const before = config(linked);
    const r = run(linked, ['--uninstall', '--only', 'hermes', '--purge-history']);
    assert.notEqual(r.status, 0);
    assert.equal(fs.readFileSync(victim, 'utf8'), 'owner lifetime\n');
    assert.deepEqual(config(linked), before, 'preflight refusal must not unregister the plugin');
    assert.equal(fs.existsSync(manifestPath(linked)), true, 'retry authority must be retained');
    assertInstalled(linked);
  } finally { linked.cleanup(); }
});

test('phase-marker failure restores exact absent MCP and plugin config', () => {
  const f = fixture();
  try {
    assert.equal(install(f, ['--with-mcp-shrink=node upstream']).status, 0);
    const before = config(f);
    delete before.mcp_servers['caveman-shrink'];
    fs.writeFileSync(path.join(f.home, 'config.yaml'), JSON.stringify(before, null, 2) + '\n');
    fs.chmodSync(stateDir(f), 0o555);

    const failed = run(f, ['--uninstall', '--only', 'hermes']);
    assert.notEqual(failed.status, 0);
    assert.deepEqual(config(f), before);
    assert.ok(fs.existsSync(manifestPath(f)));
    assert.ok(fs.existsSync(pluginDir(f)));
  } finally {
    if (fs.existsSync(stateDir(f))) fs.chmodSync(stateDir(f), 0o700);
    f.cleanup();
  }
});

test('post-config cleanup failure retains a resumable manifest and retry completes', () => {
  const f = fixture();
  const locked = path.join(pluginDir(f), 'hermes_caveman');
  try {
    assert.equal(install(f).status, 0);
    fs.chmodSync(locked, 0o555);

    const failed = run(f, ['--uninstall', '--only', 'hermes']);
    assert.notEqual(failed.status, 0);
    const retained = JSON.parse(fs.readFileSync(manifestPath(f), 'utf8'));
    assert.equal(retained.configCleaned, true);
    assert.ok(!(config(f).plugins.enabled || []).includes('caveman'));

    fs.chmodSync(locked, 0o755);
    const retried = run(f, ['--uninstall', '--only', 'hermes']);
    assert.equal(retried.status, 0, retried.stderr || retried.stdout);
    assert.equal(fs.existsSync(manifestPath(f)), false);
    assert.equal(fs.existsSync(pluginDir(f)), false);
  } finally {
    if (fs.existsSync(locked)) fs.chmodSync(locked, 0o755);
    f.cleanup();
  }
});

test('Hermes MCP shrink stores argv without shell execution and preserves unrelated config', () => {
  const f = fixture();
  try {
    const marker = path.join(f.root, 'SHOULD_NOT_EXIST');
    const upstream = `node fixture-server --root "${f.root}/path with spaces" ; touch ${marker}`;
    const r = install(f, [`--with-mcp-shrink=${upstream}`]);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assert.equal(fs.existsSync(marker), false);
    const cfg = config(f);
    assert.deepEqual(cfg.mcp_servers.sibling, { command: 'sibling', args: ['--keep'] });
    const entry = cfg.mcp_servers['caveman-shrink'];
    assert.equal(entry.command, 'node');
    assert.equal(entry.args[0], path.join(pluginDir(f), 'src', 'mcp-servers', 'caveman-shrink', 'index.js'));
    assert.deepEqual(entry.args.slice(1), ['node', 'fixture-server', '--root', `${f.root}/path with spaces`, ';', 'touch', marker]);
  } finally { f.cleanup(); }
});

test('pre-existing identical MCP entry remains unowned and survives uninstall unchanged', () => {
  const f = fixture();
  try {
    const desired = {
      command: 'node',
      args: [path.join(pluginDir(f), 'src', 'mcp-servers', 'caveman-shrink', 'index.js'), 'node', 'upstream'],
    };
    const before = config(f);
    before.mcp_servers['caveman-shrink'] = desired;
    fs.writeFileSync(path.join(f.home, 'config.yaml'), JSON.stringify(before, null, 2) + '\n');

    const installed = install(f, ['--with-mcp-shrink=node upstream']);
    assert.equal(installed.status, 0, installed.stderr || installed.stdout);
    const manifest = JSON.parse(fs.readFileSync(manifestPath(f), 'utf8'));
    assert.equal(manifest.mcp, null);

    const removed = run(f, ['--uninstall', '--only', 'hermes']);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
    assert.deepEqual(config(f).mcp_servers['caveman-shrink'], desired);
  } finally { f.cleanup(); }
});

test('MCP same-name collision requires force; force is reversible; alternate name avoids collision', () => {
  const prior = { command: 'owner-mcp', args: ['--keep'] };
  const f = fixture({ plugins: { enabled: ['sibling'] }, mcp_servers: { 'caveman-shrink': prior, sibling: { command: 'sibling' } } });
  try {
    const refused = install(f, ['--with-mcp-shrink=node upstream']);
    assert.notEqual(refused.status, 0);
    assert.deepEqual(config(f).mcp_servers['caveman-shrink'], prior);

    const forced = install(f, ['--force', '--with-mcp-shrink=node upstream']);
    assert.equal(forced.status, 0, forced.stderr || forced.stdout);
    assert.notDeepEqual(config(f).mcp_servers['caveman-shrink'], prior);
    const removed = run(f, ['--uninstall', '--only', 'hermes']);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
    assert.deepEqual(config(f).mcp_servers['caveman-shrink'], prior, 'forced collision must restore previous config');
  } finally { f.cleanup(); }

  const alternate = fixture({ plugins: { enabled: ['sibling'] }, mcp_servers: { 'caveman-shrink': prior } });
  try {
    const r = install(alternate, ['--mcp-shrink-name', 'caveman-shrink-alt', '--with-mcp-shrink=node upstream']);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assert.deepEqual(config(alternate).mcp_servers['caveman-shrink'], prior);
    assert.ok(config(alternate).mcp_servers['caveman-shrink-alt']);
    const removed = run(alternate, ['--uninstall', '--only', 'hermes']);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
    assert.deepEqual(config(alternate).mcp_servers['caveman-shrink'], prior);
    assert.equal(config(alternate).mcp_servers['caveman-shrink-alt'], undefined);
  } finally { alternate.cleanup(); }
});

test('modified managed MCP refuses uninstall before plugin registration or payload changes', () => {
  const f = fixture();
  try {
    assert.equal(install(f, ['--with-mcp-shrink=node upstream']).status, 0);
    const cfg = config(f);
    cfg.mcp_servers['caveman-shrink'] = { command: 'user', args: ['modified'] };
    fs.writeFileSync(path.join(f.home, 'config.yaml'), JSON.stringify(cfg, null, 2) + '\n');
    const before = config(f);

    const removed = run(f, ['--uninstall', '--only', 'hermes']);
    assert.notEqual(removed.status, 0);
    assert.deepEqual(config(f), before);
    assert.ok(config(f).plugins.enabled.includes('caveman'));
    assert.deepEqual(config(f).mcp_servers['caveman-shrink'], { command: 'user', args: ['modified'] });
    assert.equal(fs.existsSync(manifestPath(f)), true);
    assertInstalled(f);

    const global = run(f, ['--uninstall']);
    assert.notEqual(global.status, 0);
    assert.doesNotMatch(global.stdout, /uninstall done\./);
    assert.match(global.stderr, /uninstall incomplete\./);
    assert.deepEqual(config(f), before);
  } finally { f.cleanup(); }
});

test('MCP rename restores the previous managed name and owns only the new name', () => {
  const prior = { command: 'owner', args: ['original'] };
  const f = fixture({ plugins: { enabled: ['sibling'] }, mcp_servers: { 'caveman-shrink': prior } });
  try {
    let r = install(f, ['--force', '--with-mcp-shrink=node upstream']);
    assert.equal(r.status, 0, r.stderr || r.stdout);

    r = install(f, ['--mcp-shrink-name', 'alt-shrink', '--with-mcp-shrink=node upstream2']);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    let cfg = config(f);
    assert.deepEqual(cfg.mcp_servers['caveman-shrink'], prior);
    assert.deepEqual(cfg.mcp_servers['alt-shrink'].args.slice(-2), ['node', 'upstream2']);

    r = run(f, ['--uninstall', '--only', 'hermes']);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    cfg = config(f);
    assert.deepEqual(cfg.mcp_servers['caveman-shrink'], prior);
    assert.equal(cfg.mcp_servers['alt-shrink'], undefined);
  } finally { f.cleanup(); }
});
