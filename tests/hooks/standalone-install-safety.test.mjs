import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { nodeStub, stubEnv } from '../../packages/cli/tests/harness/stub-bin.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const sources = join(root, 'src/hooks');
const surfaces = process.platform === 'win32'
  ? [{ command: 'powershell.exe', extension: 'ps1' }, { command: 'pwsh.exe', extension: 'ps1' }]
  : [{ command: 'bash', extension: 'sh' }, { command: 'pwsh', extension: 'ps1' }];

function fixture(t, source = sources) {
  const dir = mkdtempSync(join(tmpdir(), "caveman hook safety O'Brien "));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const config = join(dir, 'config');
  const hooks = join(config, 'hooks');
  mkdirSync(hooks, { recursive: true });
  const env = { ...process.env, HOME: dir, USERPROFILE: dir, CLAUDE_CONFIG_DIR: config, TEMP: dir, TMP: dir, TMPDIR: dir };
  return { dir, source, config, hooks, env, settings: join(config, 'settings.json'), manifest: join(hooks, 'package.json') };
}

function run(surface, item, action) {
  const script = join(item.source, `${action}.${surface.extension}`);
  const args = surface.extension === 'ps1' ? ['-NoProfile', '-NonInteractive', '-File', script] : [script];
  return spawnSync(surface.command, args, { cwd: root, env: item.env, encoding: 'utf8', timeout: 20_000 });
}

for (const surface of surfaces) {
  const probe = spawnSync(surface.command, surface.extension === 'ps1'
    ? ['-NoProfile', '-NonInteractive', '-Command', 'exit 0'] : ['--version'], { encoding: 'utf8' });
  const options = { skip: probe.error?.code === 'ENOENT' ? `${surface.command} is not installed` : false };

  for (const [label, content, installOK] of [
    ['custom CommonJS', '{"type":"commonjs","name":"foreign-hooks"}\n', true],
    ['custom default module type', '{"name":"foreign-hooks"}\n', true],
    ['case-sensitive custom property', '{"Type":"commonjs"}\n', true],
    ['ESM', '{"type":"module","name":"foreign-hooks"}\n', false],
    ['malformed', '{ foreign content\n', false],
    ['commented JSON', '{"type":"commonjs" /* foreign comment */}\n', false],
    ['array', '[{"type":"commonjs"}]\n', false],
  ]) {
    test(`${surface.command}: install and uninstall preserve a ${label} manifest`, options, (t) => {
      const item = fixture(t);
      writeFileSync(item.manifest, content);
      const settings = '{"theme":"dark"}\n';
      writeFileSync(item.settings, settings);
      const installed = run(surface, item, 'install');
      assert.equal(installed.status === 0, installOK, installed.stdout + installed.stderr);
      assert.equal(readFileSync(item.manifest, 'utf8'), content);
      if (!installOK) {
        assert.doesNotMatch(installed.stdout, /Done!/);
        assert.equal(readFileSync(item.settings, 'utf8'), settings);
        assert.equal(existsSync(join(item.hooks, 'caveman-activate.js')), false, 'incompatible manifest must fail before writing hooks');
      }
      const removed = run(surface, item, 'uninstall');
      assert.equal(removed.status, 0, removed.stdout + removed.stderr);
      assert.equal(readFileSync(item.manifest, 'utf8'), content);
      assert.equal(JSON.parse(readFileSync(item.settings, 'utf8')).theme, 'dark');
    });
  }

  test(`${surface.command}: clone install and uninstall share the JSONC parser and remove only the owned manifest`, options, (t) => {
    const item = fixture(t);
    const original = '{ // user comment\n "theme":"dark",\n}\n';
    writeFileSync(item.settings, original);
    const installed = run(surface, item, 'install');
    assert.equal(installed.status, 0, installed.stdout + installed.stderr);
    assert.equal(readFileSync(`${item.settings}.bak`, 'utf8'), original);
    assert.equal(JSON.parse(readFileSync(item.settings, 'utf8')).theme, 'dark');
    assert.ok(JSON.parse(readFileSync(item.settings, 'utf8')).hooks.SessionStart.length);
    // Re-add comments so uninstall must use the same parser as installation.
    writeFileSync(item.settings, `// preserved settings\n${readFileSync(item.settings, 'utf8')}`);
    const removed = run(surface, item, 'uninstall');
    assert.equal(removed.status, 0, removed.stdout + removed.stderr);
    assert.equal(existsSync(item.manifest), false);
    assert.equal(JSON.parse(readFileSync(item.settings, 'utf8')).theme, 'dark');
    assert.equal(JSON.parse(readFileSync(item.settings, 'utf8')).hooks, undefined);
  });

  test(`${surface.command}: malformed settings fail before overwriting existing hook bytes`, options, (t) => {
    const item = fixture(t);
    const original = '{ "theme": broken JSON }\n';
    writeFileSync(item.settings, original);
    const hook = join(item.hooks, 'caveman-activate.js');
    writeFileSync(hook, 'existing hook bytes');
    const installed = run(surface, item, 'install');
    assert.notEqual(installed.status, 0, installed.stdout + installed.stderr);
    assert.doesNotMatch(installed.stdout, /Done!/);
    assert.equal(readFileSync(item.settings, 'utf8'), original);
    assert.equal(readFileSync(hook, 'utf8'), 'existing hook bytes');
    assert.equal(existsSync(`${item.settings}.bak`), false);
  });

  test(`${surface.command}: detached scripts clearly refuse unsupported JSONC without partial installation`, options, (t) => {
    const item = fixture(t);
    item.source = join(item.dir, 'detached/hooks');
    cpSync(sources, item.source, { recursive: true });
    const original = '{ // comment\n "theme":"dark"\n}\n';
    writeFileSync(item.settings, original);
    const installed = run(surface, item, 'install');
    assert.notEqual(installed.status, 0, installed.stdout + installed.stderr);
    assert.match(installed.stderr, /Nothing was changed/);
    assert.doesNotMatch(installed.stdout, /Done!/);
    assert.equal(readFileSync(item.settings, 'utf8'), original);
    assert.equal(existsSync(item.manifest), false);
  });

  test(`${surface.command}: a failed Node settings merge cannot report successful installation`, options, (t) => {
    const item = fixture(t);
    writeFileSync(item.settings, '{"theme":"dark"}\n');
    const bin = join(item.dir, 'bin');
    // Real Node launcher on POSIX and a real .cmd launcher on Windows. The
    // preflight is evaluated normally; only the settings-writing invocation fails.
    nodeStub(bin, 'node', `import { spawnSync } from 'node:child_process';
if (ARGV[0] !== '--input-type=commonjs') process.exit(9);
const result = spawnSync(process.execPath, ARGV, { stdio: 'inherit' });
process.exit(result.status ?? 1);`);
    item.env = stubEnv(item.env, bin);
    const installed = run(surface, item, 'install');
    assert.notEqual(installed.status, 0, installed.stdout + installed.stderr);
    assert.doesNotMatch(installed.stdout, /Done!/);
    assert.equal(JSON.parse(readFileSync(item.settings, 'utf8')).theme, 'dark');
  });
}
