import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { nodeStub, stubEnv } from '../../packages/cli/tests/harness/stub-bin.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const INSTALLER = path.join(ROOT, 'bin/install.js');
const skillNames = fs.readdirSync(path.join(ROOT, 'skills')).filter(name => fs.existsSync(path.join(ROOT, 'skills', name, 'SKILL.md'))).sort();

function fixture(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman native provider '));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const home = path.join(directory, 'home');
  const cwd = path.join(directory, 'project with spaces');
  const bin = path.join(directory, 'bin');
  fs.mkdirSync(home);
  fs.mkdirSync(cwd);
  const systemPath = process.platform === 'win32'
    ? path.join(process.env.SystemRoot || 'C:\\Windows', 'System32')
    : ['/usr/bin', '/bin'].join(path.delimiter);
  const env = stubEnv({
    HOME: home, USERPROFILE: home, PATH: systemPath,
    SystemRoot: process.env.SystemRoot || process.env.WINDIR || '',
    TEMP: directory, TMP: directory, TMPDIR: directory,
    APPDATA: path.join(home, 'AppData', 'Roaming'), LOCALAPPDATA: path.join(home, 'AppData', 'Local'),
    CLAUDE_CONFIG_DIR: path.join(home, '.claude'), XDG_CONFIG_HOME: path.join(home, '.config'),
    HERMES_HOME: path.join(home, '.hermes'), OPENCLAW_WORKSPACE: path.join(home, '.openclaw', 'workspace'),
    CODEX_HOME: path.join(home, '.codex'), NO_COLOR: '1',
  }, bin);
  // Avoid touching system-installed apps while exercising every real detector.
  const preload = path.join(directory, 'hide-system-apps.cjs');
  fs.writeFileSync(preload, `const fs = require('fs'); const exists = fs.existsSync; fs.existsSync = p => String(p).startsWith('/Applications/') ? false : exists(p);`);
  const log = path.join(directory, 'unexpected-npx.json');
  nodeStub(bin, 'npx', `import fs from 'node:fs'; fs.writeFileSync(${JSON.stringify(log)}, JSON.stringify(ARGV)); process.exitCode = 91;`);
  const run = (args, installer = INSTALLER) => spawnSync(process.execPath, ['--require', preload, installer, ...args, '--non-interactive', '--no-color'], { encoding: 'utf8', env, cwd });
  return { directory, home, cwd, bin, env, log, run };
}

const cases = [
  ['continue default', 'continue', null, null],
  ['continue absolute home', 'continue', 'CONTINUE_GLOBAL_DIR', 'absolute'],
  ['continue relative home', 'continue', 'CONTINUE_GLOBAL_DIR', 'relative'],
  ['AiderDesk default', 'aider-desk', null, null],
  ['AiderDesk absolute home', 'aider-desk', 'AIDER_DESK_HOME_DIR', 'absolute'],
  ['AiderDesk relative home', 'aider-desk', 'AIDER_DESK_HOME_DIR', 'relative'],
  ['AiderDesk directory override', 'aider-desk', 'AIDER_DESK_DIR', 'directory'],
  ['Antigravity IDE default', 'antigravity', null, null],
  ['Antigravity 2.0 default', 'antigravity-2', null, null],
  ['iFlow absolute home', 'iflow', 'IFLOW_HOME', 'absolute'],
  ['iFlow relative home', 'iflow', 'IFLOW_HOME', 'relative'],
  ['Crush absolute skill directory', 'crush', 'CRUSH_SKILLS_DIR', 'absolute'],
  ['Crush relative skill directory', 'crush', 'CRUSH_SKILLS_DIR', 'relative'],
];

for (const [label, provider, variable, form] of cases) {
  test(`${label}: real installer writes discoverable copies and uninstalls its own skills`, (t) => {
    const f = fixture(t);
    let configRoot = path.join(f.home, provider === 'continue' ? '.continue' : '.aider-desk');
    if (provider === 'antigravity') configRoot = path.join(f.home, '.gemini', 'antigravity');
    if (provider === 'antigravity-2') configRoot = path.join(f.home, '.gemini', 'config');
    if (variable) {
      f.env[variable] = form === 'absolute' ? path.join(f.directory, 'configured vendor') : 'custom vendor';
      configRoot = form === 'absolute' ? f.env[variable] : path.join(form === 'directory' ? f.home : f.cwd, f.env[variable]);
    }
    const skillsRoot = provider === 'crush' ? configRoot : path.join(configRoot, 'skills');
    const installed = f.run(['--only', provider]);
    assert.equal(installed.status, 0, installed.stdout + installed.stderr);
    assert.equal(fs.existsSync(f.log), false, 'local native installation must not delegate to an incompatible upstream target');
    if (provider.startsWith('antigravity')) {
      const otherProduct = provider === 'antigravity' ? 'config' : 'antigravity';
      assert.equal(fs.existsSync(path.join(f.home, '.gemini', otherProduct)), false, 'install only the explicitly selected Antigravity product');
      assert.equal(fs.existsSync(path.join(f.home, '.agents')), false, 'do not substitute an undocumented global discovery root');
    }
    const entries = fs.readdirSync(skillsRoot, { withFileTypes: true }).filter(entry => entry.isDirectory()).map(entry => entry.name).sort();
    assert.deepEqual(entries, skillNames);
    for (const name of skillNames) {
      assert.equal(fs.lstatSync(path.join(skillsRoot, name)).isSymbolicLink(), false);
      assert.equal(fs.readFileSync(path.join(skillsRoot, name, 'SKILL.md'), 'utf8'), fs.readFileSync(path.join(ROOT, 'skills', name, 'SKILL.md'), 'utf8'));
    }
    const foreign = path.join(skillsRoot, 'foreign-skill');
    fs.mkdirSync(foreign);
    fs.writeFileSync(path.join(foreign, 'SKILL.md'), 'Keep user content');
    const removed = f.run(['--uninstall']);
    assert.equal(removed.status, 0, removed.stdout + removed.stderr);
    assert.deepEqual(fs.readdirSync(skillsRoot), ['foreign-skill']);
  });
}

test('a plain aider executable does not auto-detect AiderDesk; the packaged aider-desk executable does', (t) => {
  const f = fixture(t);
  nodeStub(f.bin, 'aider', 'process.exitCode = 0;');
  const ordinary = f.run(['--dry-run']);
  assert.equal(ordinary.status, 0, ordinary.stderr);
  assert.doesNotMatch(ordinary.stdout, /Aider Desk detected|AiderDesk detected/);
  nodeStub(f.bin, 'aider-desk', 'process.exitCode = 0;');
  const desktop = f.run(['--dry-run']);
  assert.equal(desktop.status, 0, desktop.stderr);
  assert.match(desktop.stdout, /Aider Desk detected|AiderDesk detected/);
  assert.match(desktop.stdout, /would copy Caveman skills/);
  assert.equal(fs.existsSync(path.join(f.home, '.aider-desk')), false);
  assert.equal(fs.existsSync(f.log), false);
});

test('Antigravity products are listed separately and 2.0 dry-run plans only its documented root', (t) => {
  const f = fixture(t);
  const list = f.run(['--list']);
  assert.equal(list.status, 0, list.stderr);
  assert.match(list.stdout, /antigravity\s+Antigravity IDE\s+native skills copy \(soft\)/);
  assert.match(list.stdout, /antigravity-2\s+Antigravity 2\.0\s+native skills copy \(soft\)/);
  const dry = f.run(['--only', 'antigravity-2', '--dry-run']);
  assert.equal(dry.status, 0, dry.stdout + dry.stderr);
  assert.match(dry.stdout, /Antigravity 2\.0 selected/);
  assert.doesNotMatch(dry.stdout, /Antigravity 2\.0 detected/);
  assert.ok(dry.stdout.includes(path.join(f.home, '.gemini', 'config', 'skills')));
  assert.equal(fs.existsSync(path.join(f.home, '.gemini')), false);
  assert.equal(fs.existsSync(f.log), false);
});

test('Antigravity directories and ambiguous executable names do not trigger automatic installs', (t) => {
  const f = fixture(t);
  for (const product of ['antigravity', 'config']) {
    fs.mkdirSync(path.join(f.home, '.gemini', product), { recursive: true });
  }
  for (const command of ['antigravity', 'agy']) nodeStub(f.bin, command, 'process.exitCode = 0;');
  const automatic = f.run([]);
  assert.equal(automatic.status, 0, automatic.stdout + automatic.stderr);
  assert.doesNotMatch(automatic.stdout, /Antigravity (IDE|2\.0) (detected|selected)/);
  assert.equal(fs.existsSync(f.log), false);
  for (const product of ['antigravity', 'config']) assert.deepEqual(fs.readdirSync(path.join(f.home, '.gemini', product)), []);
});

for (const [provider, product, otherProduct] of [['antigravity', 'antigravity', 'config'], ['antigravity-2', 'config', 'antigravity']]) {
  test(`${provider} preserves the other Antigravity product's existing skill files`, (t) => {
    const f = fixture(t);
    const otherRoot = path.join(f.home, '.gemini', otherProduct, 'skills');
    fs.mkdirSync(path.join(otherRoot, 'caveman'), { recursive: true });
    const sentinel = path.join(otherRoot, 'caveman', 'SKILL.md');
    fs.writeFileSync(sentinel, 'Other product content');
    const installed = f.run(['--only', provider]);
    assert.equal(installed.status, 0, installed.stdout + installed.stderr);
    assert.equal(fs.readFileSync(sentinel, 'utf8'), 'Other product content');
    assert.deepEqual(fs.readdirSync(path.dirname(otherRoot)), ['skills']);
    assert.ok(fs.existsSync(path.join(f.home, '.gemini', product, 'skills', 'caveman', 'SKILL.md')));
    const removed = f.run(['--uninstall']);
    assert.equal(removed.status, 0, removed.stdout + removed.stderr);
    assert.equal(fs.readFileSync(sentinel, 'utf8'), 'Other product content');
  });
}

test('explicitly selecting both Antigravity products creates two independent owned installs', (t) => {
  const f = fixture(t);
  const installed = f.run(['--only', 'antigravity', '--only', 'antigravity-2']);
  assert.equal(installed.status, 0, installed.stdout + installed.stderr);
  for (const [product, provider] of [['antigravity', 'antigravity'], ['config', 'antigravity-2']]) {
    const config = path.join(f.home, '.gemini', product);
    assert.ok(fs.existsSync(path.join(config, 'skills', 'caveman', 'SKILL.md')));
    assert.ok(fs.existsSync(path.join(config, `.caveman-skills-${provider}-ownership.json`)));
  }
  assert.equal(fs.existsSync(f.log), false);
  assert.equal(fs.existsSync(path.join(f.home, '.agents')), false);
});

test('a foreign same-name skill fails the real install without modifying it or claiming installation', (t) => {
  const f = fixture(t);
  const skill = path.join(f.home, '.continue', 'skills', 'caveman', 'SKILL.md');
  fs.mkdirSync(path.dirname(skill), { recursive: true });
  fs.writeFileSync(skill, 'User-owned Caveman variant');
  const result = f.run(['--only', 'continue']);
  assert.equal(result.status, 1, result.stdout + result.stderr);
  assert.match(result.stderr, /ownership conflict/);
  assert.doesNotMatch(result.stdout, /  installed:/);
  assert.equal(fs.readFileSync(skill, 'utf8'), 'User-owned Caveman variant');
});

test('detached real installer stages the source with --copy and then owns the vendor installation', (t) => {
  const f = fixture(t);
  const detached = path.join(f.directory, 'detached');
  fs.cpSync(path.join(ROOT, 'bin'), path.join(detached, 'bin'), { recursive: true });
  const log = path.join(f.directory, 'stage.json');
  nodeStub(f.bin, 'npx', `import fs from 'node:fs'; import path from 'node:path'; fs.writeFileSync(${JSON.stringify(log)}, JSON.stringify({ args: ARGV, cwd: process.cwd() })); fs.cpSync(${JSON.stringify(path.join(ROOT, 'skills'))}, path.join(process.cwd(), '.agents', 'skills'), { recursive: true });`);
  f.env.CONTINUE_GLOBAL_DIR = path.join(f.directory, 'continue configured');
  const installed = f.run(['--only', 'continue'], path.join(detached, 'bin', 'install.js'));
  assert.equal(installed.status, 0, installed.stdout + installed.stderr);
  const stage = JSON.parse(fs.readFileSync(log, 'utf8'));
  assert.deepEqual(stage.args, ['-y', 'skills', 'add', 'JuliusBrussee/caveman', '--skill', '*', '-a', 'codex', '--yes', '--copy']);
  assert.equal(fs.existsSync(stage.cwd), false);
  assert.ok(fs.existsSync(path.join(f.env.CONTINUE_GLOBAL_DIR, 'skills', 'caveman', 'SKILL.md')));
  assert.equal(fs.existsSync(path.join(f.home, '.agents')), false);
});
