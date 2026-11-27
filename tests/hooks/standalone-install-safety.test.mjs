import { after, describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { cpus, tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { nodeStub, stubEnv } from '../../packages/cli/tests/harness/stub-bin.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const sources = join(root, 'src/hooks');
const surfaces = process.platform === 'win32'
  ? [{ command: 'powershell.exe', extension: 'ps1' }, { command: 'pwsh.exe', extension: 'ps1' }]
  : [{ command: 'bash', extension: 'sh' }, { command: 'pwsh', extension: 'ps1' }];
const suiteConcurrency = Math.max(1, Math.min(4, cpus().length - 1));

function fixture(t, source = sources) {
  const dir = mkdtempSync(join(tmpdir(), "caveman hook safety O'Brien "));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const config = join(dir, 'config');
  const hooks = join(config, 'hooks');
  mkdirSync(hooks, { recursive: true });
  const env = { ...process.env, HOME: dir, USERPROFILE: dir, CLAUDE_CONFIG_DIR: config, TEMP: dir, TMP: dir, TMPDIR: dir, POWERSHELL_TELEMETRY_OPTOUT: '1', POWERSHELL_UPDATECHECK: 'Off' };
  return { dir, source, config, hooks, env, settings: join(config, 'settings.json'), manifest: join(hooks, 'package.json') };
}

function run(surface, item, action) {
  const script = join(item.source, `${action}.${surface.extension}`);
  const args = surface.extension === 'ps1' ? ['-NoProfile', '-NonInteractive', '-File', script] : [script];
  // Ceiling, not budget: single pwsh runs took over 20s on a loaded machine.
  return new Promise((resolveRun) => {
    const child = spawn(surface.command, args, { cwd: root, env: item.env, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    const timer = setTimeout(() => child.kill('SIGKILL'), 60_000);
    child.stdout.on('data', (d) => { stdout += d; });
    child.stderr.on('data', (d) => { stderr += d; });
    child.on('error', (error) => { clearTimeout(timer); resolveRun({ status: null, stdout, stderr, error }); });
    child.on('close', (status) => { clearTimeout(timer); resolveRun({ status, stdout, stderr }); });
  });
}

// pwsh writes its profile cache under HOME even for `exit 0`: probe in a throwaway one.
const probeHome = mkdtempSync(join(tmpdir(), 'caveman hook probe '));
after(() => rmSync(probeHome, { recursive: true, force: true }));
for (const surface of surfaces) {
  const probe = spawnSync(surface.command, surface.extension === 'ps1'
    ? ['-NoProfile', '-NonInteractive', '-Command', 'exit 0'] : ['--version'], { encoding: 'utf8', env: { ...process.env, HOME: probeHome } });
  const options = {
    skip: probe.error?.code === 'ENOENT' ? `${surface.command} is not installed` : false,
    concurrency: suiteConcurrency,
  };

  describe(`${surface.command} standalone hook safety`, options, () => {
  for (const [label, content, installOK] of [
    ['custom CommonJS', '{"type":"commonjs","name":"foreign-hooks"}\n', true],
    ['custom default module type', '{"name":"foreign-hooks"}\n', true],
    ['case-sensitive custom property', '{"Type":"commonjs"}\n', true],
    ['ESM', '{"type":"module","name":"foreign-hooks"}\n', false],
    ['malformed', '{ foreign content\n', false],
    ['commented JSON', '{"type":"commonjs" /* foreign comment */}\n', false],
    ['array', '[{"type":"commonjs"}]\n', false],
  ]) {
    test(`${surface.command}: install and uninstall preserve a ${label} manifest`, async (t) => {
      const item = fixture(t);
      writeFileSync(item.manifest, content);
      const settings = '{"theme":"dark"}\n';
      writeFileSync(item.settings, settings);
      const installed = await run(surface, item, 'install');
      assert.equal(installed.status === 0, installOK, installed.stdout + installed.stderr);
      assert.equal(readFileSync(item.manifest, 'utf8'), content);
      if (!installOK) {
        assert.doesNotMatch(installed.stdout, /Done!/);
        assert.equal(readFileSync(item.settings, 'utf8'), settings);
        assert.equal(existsSync(join(item.hooks, 'caveman-activate.js')), false, 'incompatible manifest must fail before writing hooks');
      }
      const removed = await run(surface, item, 'uninstall');
      assert.equal(removed.status, 0, removed.stdout + removed.stderr);
      assert.equal(readFileSync(item.manifest, 'utf8'), content);
      assert.equal(JSON.parse(readFileSync(item.settings, 'utf8')).theme, 'dark');
    });
  }

  test(`${surface.command}: clone install and uninstall share the JSONC parser and remove only the owned manifest`, async (t) => {
    const item = fixture(t);
    const original = '{ // user comment\n "theme":"dark",\n}\n';
    writeFileSync(item.settings, original);
    const installed = await run(surface, item, 'install');
    assert.equal(installed.status, 0, installed.stdout + installed.stderr);
    assert.equal(readFileSync(`${item.settings}.bak`, 'utf8'), original);
    assert.equal(JSON.parse(readFileSync(item.settings, 'utf8')).theme, 'dark');
    assert.ok(JSON.parse(readFileSync(item.settings, 'utf8')).hooks.SessionStart.length);
    // Re-add comments so uninstall must use the same parser as installation.
    writeFileSync(item.settings, `// preserved settings\n${readFileSync(item.settings, 'utf8')}`);
    const removed = await run(surface, item, 'uninstall');
    assert.equal(removed.status, 0, removed.stdout + removed.stderr);
    assert.equal(existsSync(item.manifest), false);
    assert.equal(JSON.parse(readFileSync(item.settings, 'utf8')).theme, 'dark');
    assert.equal(JSON.parse(readFileSync(item.settings, 'utf8')).hooks, undefined);
  });

  test(`${surface.command}: uninstall keeps a settings.json.bak it did not make`, async (t) => {
    const item = fixture(t);
    const hook = join(item.hooks, 'caveman-activate.js');
    writeFileSync(hook, '');
    const entry = { type: 'command', command: `node ${hook}` };
    writeFileSync(item.settings, JSON.stringify({ model: 'opus', hooks: { SessionStart: [{ hooks: [entry] }] } }));
    const mine = '{"model":"my-own-manual-backup"}\n';
    writeFileSync(`${item.settings}.bak`, mine);
    const removed = await run(surface, item, 'uninstall');
    assert.equal(removed.status, 0, removed.stdout + removed.stderr);
    assert.equal(existsSync(hook), false);
    assert.equal(readFileSync(`${item.settings}.bak`, 'utf8'), mine);
  });

  test(`${surface.command}: uninstall keeps the only copy of comments it dropped and removes a plain-JSON backup it made`, async (t) => {
    const item = fixture(t);
    const entry = { type: 'command', command: `node ${join(item.hooks, 'caveman-activate.js')}` };
    const body = JSON.stringify({ model: 'opus', hooks: { SessionStart: [{ hooks: [entry] }] } });
    writeFileSync(item.settings, `// my model choice\n${body}\n`);
    const jsonc = await run(surface, item, 'uninstall');
    assert.equal(jsonc.status, 0, jsonc.stdout + jsonc.stderr);
    assert.match(readFileSync(`${item.settings}.bak`, 'utf8'), /my model choice/);
    rmSync(`${item.settings}.bak`);
    writeFileSync(item.settings, body);
    assert.equal((await run(surface, item, 'uninstall')).status, 0);
    assert.equal(existsSync(`${item.settings}.bak`), false);
  });

  test(`${surface.command}: malformed settings fail before overwriting existing hook bytes`, async (t) => {
    const item = fixture(t);
    const original = '{ "theme": broken JSON }\n';
    writeFileSync(item.settings, original);
    const hook = join(item.hooks, 'caveman-activate.js');
    writeFileSync(hook, 'existing hook bytes');
    const installed = await run(surface, item, 'install');
    assert.notEqual(installed.status, 0, installed.stdout + installed.stderr);
    assert.doesNotMatch(installed.stdout, /Done!/);
    assert.equal(readFileSync(item.settings, 'utf8'), original);
    assert.equal(readFileSync(hook, 'utf8'), 'existing hook bytes');
    assert.equal(existsSync(`${item.settings}.bak`), false);
  });

  test(`${surface.command}: detached scripts clearly refuse unsupported JSONC without partial installation`, async (t) => {
    const item = fixture(t);
    item.source = join(item.dir, 'detached/hooks');
    cpSync(sources, item.source, { recursive: true });
    const original = '{ // comment\n "theme":"dark"\n}\n';
    writeFileSync(item.settings, original);
    const installed = await run(surface, item, 'install');
    assert.notEqual(installed.status, 0, installed.stdout + installed.stderr);
    assert.match(installed.stderr, /Nothing was changed/);
    assert.doesNotMatch(installed.stdout, /Done!/);
    assert.equal(readFileSync(item.settings, 'utf8'), original);
    assert.equal(existsSync(item.manifest), false);
  });

  test(`${surface.command}: a failed Node settings merge cannot report successful installation`, async (t) => {
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
    const installed = await run(surface, item, 'install');
    assert.notEqual(installed.status, 0, installed.stdout + installed.stderr);
    assert.doesNotMatch(installed.stdout, /Done!/);
    assert.equal(JSON.parse(readFileSync(item.settings, 'utf8')).theme, 'dark');
  });

  test(`${surface.command}: an idempotent re-run over JSONC settings changes nothing`, async (t) => {
    const item = fixture(t);
    writeFileSync(item.settings, '{"theme":"dark"}\n');
    const installed = await run(surface, item, 'install');
    assert.equal(installed.status, 0, installed.stdout + installed.stderr);
    // The user edits their own settings after install. The ALREADY_INSTALLED
    // probe reads the same file the merge step does, so it has to use the same
    // JSONC parser: a bare JSON.parse throws here, the probe reports "not
    // installed", and the merge step rewrites the file through JSON.stringify
    // -- dropping the comment and the trailing comma without a fresh backup.
    const edited = `{ // my theme\n "theme":"dark",\n ${readFileSync(item.settings, 'utf8').slice(1)}`;
    writeFileSync(item.settings, edited);
    const backup = readFileSync(`${item.settings}.bak`, 'utf8');
    const reran = await run(surface, item, 'install');
    assert.equal(reran.status, 0, reran.stdout + reran.stderr);
    assert.match(reran.stdout, /Nothing to do\. Hooks are already in place\./);
    assert.equal(readFileSync(item.settings, 'utf8'), edited, 'a no-op re-run must not rewrite user settings');
    assert.equal(readFileSync(`${item.settings}.bak`, 'utf8'), backup, 'a no-op re-run must not touch the backup');
  });
  });
}
