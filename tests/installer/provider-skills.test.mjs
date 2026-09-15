import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const providerSkills = require('../../bin/lib/provider-skills.js');

function fixture(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman vendor skills '));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const repoRoot = path.join(directory, 'repo');
  for (const name of ['caveman', 'cavecrew']) {
    const skill = path.join(repoRoot, 'skills', name);
    fs.mkdirSync(path.join(skill, 'references'), { recursive: true });
    fs.writeFileSync(path.join(skill, 'SKILL.md'), `---\nname: ${name}\ndescription: Fixture\n---\nOriginal ${name}\n`);
    fs.writeFileSync(path.join(skill, 'references', 'context.md'), 'Supporting file\n');
  }
  return { directory, repoRoot, root: path.join(directory, 'configured home', 'skills') };
}

test('vendor directories honor absolute and cwd-relative overrides on POSIX and Windows', () => {
  for (const platform of ['linux', 'darwin', 'win32']) {
    const paths = platform === 'win32' ? path.win32 : path.posix;
    const home = platform === 'win32' ? 'C:\\Users\\Agent Name' : '/home/agent name';
    const cwd = platform === 'win32' ? 'D:\\project space' : '/work/project space';
    const absolute = platform === 'win32' ? 'E:\\custom config' : '/custom/config';
    const context = { platform, home, cwd };
    assert.equal(providerSkills.skillsRoot('continue', { ...context, env: {} }), paths.join(home, '.continue', 'skills'));
    assert.equal(providerSkills.skillsRoot('continue', { ...context, env: { CONTINUE_GLOBAL_DIR: absolute } }), paths.join(absolute, 'skills'));
    assert.equal(providerSkills.skillsRoot('continue', { ...context, env: { CONTINUE_GLOBAL_DIR: 'relative config' } }), paths.join(cwd, 'relative config', 'skills'));
    assert.equal(providerSkills.skillsRoot('aider-desk', { ...context, env: {} }), paths.join(home, '.aider-desk', 'skills'));
    assert.equal(providerSkills.skillsRoot('aider-desk', { ...context, env: { AIDER_DESK_HOME_DIR: absolute, AIDER_DESK_DIR: '.other' } }), paths.join(absolute, 'skills'));
    assert.equal(providerSkills.skillsRoot('aider-desk', { ...context, env: { AIDER_DESK_HOME_DIR: 'relative config' } }), paths.join(cwd, 'relative config', 'skills'));
    assert.equal(providerSkills.skillsRoot('aider-desk', { ...context, env: { AIDER_DESK_DIR: '.custom desk' } }), paths.join(home, '.custom desk', 'skills'));
    assert.equal(providerSkills.skillsRoot('antigravity', { ...context, env: {} }), paths.join(home, '.gemini', 'antigravity', 'skills'));
    assert.equal(providerSkills.skillsRoot('antigravity-2', { ...context, env: {} }), paths.join(home, '.gemini', 'config', 'skills'));
    assert.equal(providerSkills.skillsRoot('iflow', { ...context, env: { IFLOW_HOME: absolute } }), paths.join(absolute, 'skills'));
    assert.equal(providerSkills.skillsRoot('iflow', { ...context, env: { IFLOW_HOME: 'relative config' } }), paths.join(cwd, 'relative config', 'skills'));
    assert.equal(providerSkills.skillsRoot('crush', { ...context, env: { CRUSH_SKILLS_DIR: absolute } }), absolute);
    assert.equal(providerSkills.skillsRoot('crush', { ...context, env: { CRUSH_SKILLS_DIR: 'relative skills' } }), paths.join(cwd, 'relative skills'));
  }
});

test('vendor directories preserve truthy whitespace, as the native sources do', () => {
  const context = { platform: 'linux', home: '/home/user', cwd: '/work' };
  assert.equal(providerSkills.skillsRoot('continue', { ...context, env: { CONTINUE_GLOBAL_DIR: ' ' } }), '/work/ /skills');
  assert.equal(providerSkills.skillsRoot('aider-desk', { ...context, env: { AIDER_DESK_HOME_DIR: ' ' } }), '/work/ /skills');
  assert.equal(providerSkills.skillsRoot('iflow', { ...context, env: { IFLOW_HOME: ' ' } }), '/work/ /skills');
  assert.equal(providerSkills.skillsRoot('crush', { ...context, env: { CRUSH_SKILLS_DIR: ' ' } }), '/work/ ');
});

test('iFlow and Crush use native copies only for nonempty vendor overrides', () => {
  for (const provider of ['continue', 'aider-desk', 'antigravity', 'antigravity-2']) assert.equal(providerSkills.usesNativeSkills(provider, {}), true);
  for (const [provider, variable] of [['iflow', 'IFLOW_HOME'], ['crush', 'CRUSH_SKILLS_DIR']]) {
    assert.equal(providerSkills.usesNativeSkills(provider, {}), false);
    assert.equal(providerSkills.usesNativeSkills(provider, { [variable]: '' }), false);
    assert.equal(providerSkills.usesNativeSkills(provider, { [variable]: ' ' }), true);
    assert.equal(providerSkills.usesNativeSkills(provider, { [variable]: 'custom' }), true);
  }
  assert.equal(providerSkills.usesNativeSkills('cursor', { CRUSH_SKILLS_DIR: 'custom' }), false);
});

for (const provider of ['continue', 'aider-desk', 'antigravity', 'antigravity-2', 'iflow', 'crush']) {
  test(`${provider} installs physical skills, updates them, and removes only owned content`, (t) => {
    const f = fixture(t);
    fs.mkdirSync(path.join(f.root, 'foreign'), { recursive: true });
    fs.writeFileSync(path.join(f.root, 'foreign', 'SKILL.md'), 'User skill');
    const first = providerSkills.install({ provider, ...f });
    assert.equal(first.count, 2);
    const skill = path.join(f.root, 'caveman');
    assert.equal(fs.lstatSync(skill).isSymbolicLink(), false);
    assert.equal(fs.readFileSync(path.join(skill, 'references', 'context.md'), 'utf8'), 'Supporting file\n');
    fs.writeFileSync(path.join(f.repoRoot, 'skills', 'caveman', 'references', 'context.md'), 'Updated file\n');
    providerSkills.install({ provider, ...f });
    assert.equal(fs.readFileSync(path.join(skill, 'references', 'context.md'), 'utf8'), 'Updated file\n');
    const removed = providerSkills.uninstall({ provider, root: f.root });
    assert.equal(removed.hadJournal, true);
    assert.deepEqual(removed.changed, []);
    assert.deepEqual(fs.readdirSync(f.root), ['foreign']);
  });
}

test('same-name foreign skills require force and are restored on uninstall', (t) => {
  const f = fixture(t);
  const file = path.join(f.root, 'caveman', 'SKILL.md');
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, 'User skill');
  assert.throws(() => providerSkills.install({ provider: 'continue', ...f }), /ownership conflict/);
  assert.equal(fs.readFileSync(file, 'utf8'), 'User skill');
  assert.equal(fs.existsSync(path.join(f.root, 'cavecrew')), false, 'preflight must prevent partial conflict installs');
  providerSkills.install({ provider: 'continue', ...f, force: true });
  assert.deepEqual(fs.readdirSync(f.root).sort(), ['cavecrew', 'caveman'], 'backups must stay outside recursive skill discovery');
  providerSkills.uninstall({ provider: 'continue', root: f.root });
  assert.equal(fs.readFileSync(file, 'utf8'), 'User skill');
});

test('uninstall preserves modified skills and retains their journal entries', (t) => {
  const f = fixture(t);
  providerSkills.install({ provider: 'aider-desk', ...f });
  const file = path.join(f.root, 'caveman', 'SKILL.md');
  fs.writeFileSync(file, 'User modification');
  const removed = providerSkills.uninstall({ provider: 'aider-desk', root: f.root });
  assert.deepEqual(removed.changed, ['skills/caveman']);
  assert.equal(fs.readFileSync(file, 'utf8'), 'User modification');
  assert.equal(fs.existsSync(path.join(path.dirname(f.root), '.caveman-skills-aider-desk-ownership.json')), true);
});

test('dry install and uninstall create no files and do not fetch', (t) => {
  const f = fixture(t);
  const notes = [];
  const run = () => { throw new Error('unexpected download'); };
  providerSkills.install({ provider: 'continue', root: f.root, dryRun: true, note: s => notes.push(s), run });
  providerSkills.uninstall({ provider: 'continue', root: f.root, dryRun: true });
  assert.equal(fs.existsSync(f.root), false);
  assert.match(notes.join('\n'), /would copy/);
});

test('detached install stages all skills in a disposable project and copies to the requested vendor root', (t) => {
  const f = fixture(t);
  let staged;
  const run = (command, args, options) => {
    assert.equal(command, 'npx');
    assert.deepEqual(args, ['-y', 'skills', 'add', 'JuliusBrussee/caveman', '--skill', '*', '-a', 'codex', '--yes', '--copy']);
    staged = options.cwd;
    assert.notEqual(staged, f.directory);
    fs.cpSync(path.join(f.repoRoot, 'skills'), path.join(staged, '.agents', 'skills'), { recursive: true });
    return { status: 0 };
  };
  const installed = providerSkills.install({ provider: 'verified-extra-provider', root: f.root, run });
  assert.equal(installed.count, 2);
  assert.equal(fs.existsSync(staged), false);
  assert.equal(fs.lstatSync(path.join(f.root, 'caveman')).isDirectory(), true);
  providerSkills.uninstall({ provider: 'verified-extra-provider', root: f.root });
  assert.deepEqual(fs.readdirSync(f.root), []);
});

for (const failure of ['nonzero', 'signal', 'empty']) {
  test(`detached ${failure} download cannot report a successful install and cleans its staging directory`, (t) => {
    const f = fixture(t);
    let staged;
    const run = (_command, _args, { cwd }) => {
      staged = cwd;
      return { status: failure === 'nonzero' ? 1 : 0, signal: failure === 'signal' ? 'SIGTERM' : null };
    };
    assert.throws(() => providerSkills.install({ provider: 'continue', root: f.root, run }));
    assert.equal(fs.existsSync(staged), false);
    assert.equal(fs.existsSync(f.root), false);
  });
}

test('an empty local skill source cannot report success', (t) => {
  const f = fixture(t);
  fs.rmSync(path.join(f.repoRoot, 'skills'), { recursive: true });
  fs.mkdirSync(path.join(f.repoRoot, 'skills'));
  assert.throws(() => providerSkills.install({ provider: 'continue', ...f }), /no skill directories/);
  assert.equal(fs.existsSync(f.root), false);
});
