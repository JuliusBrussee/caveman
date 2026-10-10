// Exercise the real installer with a recording npx shim. No download or user
// configuration is touched. The .cmd shim exercises Windows' real launch path.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { nodeStub, stubEnv } from '../../packages/cli/tests/harness/stub-bin.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const INSTALLER = path.join(ROOT, 'installer/install.js');
const profiles = [...fs.readFileSync(INSTALLER, 'utf8').matchAll(/id: '([^']+)'[^\n]+profile: '([^']+)'/g)]
  .map(([, id, profile]) => ({ id, profile }))
  .filter(({ id }) => !['continue', 'aider-desk', 'antigravity'].includes(id)); // Native copies: provider-skills-integration.test.mjs.

for (const { id, profile } of profiles) {
  const projectOnly = id === 'replit';
  test(`${id} installs every skill ${projectOnly ? 'in the project' : 'globally'} for only its selected profile`, (t) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman skills global '));
    t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
    const home = path.join(dir, 'home');
    const cwd = path.join(dir, 'unrelated project');
    const bin = path.join(dir, 'bin');
    const log = path.join(dir, 'argv.json');
    fs.mkdirSync(home);
    fs.mkdirSync(cwd);
    nodeStub(bin, 'npx', `import fs from 'node:fs'; fs.writeFileSync(${JSON.stringify(log)}, JSON.stringify(ARGV));`);
    const env = stubEnv({ ...process.env, HOME: home, USERPROFILE: home, IFLOW_HOME: '', CRUSH_SKILLS_DIR: '', CAVEMAN_REF: 'v9.9.9' }, bin);
    const result = spawnSync(process.execPath, [INSTALLER, '--only', id, '--non-interactive'], {
      encoding: 'utf8', cwd, env,
    });
    assert.equal(result.status, 0, result.stdout + result.stderr);
    assert.deepEqual(JSON.parse(fs.readFileSync(log, 'utf8')), [
      '-y', 'skills', 'add', 'JuliusBrussee/caveman#v9.9.9', '--skill', '*', '-a', profile, '--yes', ...(projectOnly ? [] : ['-g']),
    ]);
    if (projectOnly) assert.ok(result.stdout.includes(`Installing into this project: ${fs.realpathSync(cwd)}`), result.stdout);
    assert.deepEqual(fs.readdirSync(cwd), [], 'install must not use the caller directory');
  });
}

test('dry run plans a global install without creating directories or invoking npx', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-global-dry-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const result = spawnSync(process.execPath, [INSTALLER, '--only', 'cursor', '--dry-run'], {
    encoding: 'utf8', cwd: dir, env: { ...process.env, HOME: dir, USERPROFILE: dir },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /-g\b/);
  // Copy-pastable: an unquoted * would glob-expand to the files in cwd.
  assert.match(result.stdout, /would run: npx -y skills add 'JuliusBrussee\/caveman#[^']+' --skill '\*' -a cursor/);
  assert.deepEqual(fs.readdirSync(dir), []);
});

test('no detected agents must not install skills for every upstream profile', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman no agents '));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const bin = path.join(dir, 'bin');
  const log = path.join(dir, 'unexpected-install.json');
  nodeStub(bin, 'npx', `import fs from 'node:fs'; fs.writeFileSync(${JSON.stringify(log)}, JSON.stringify(ARGV));`);
  // System-installed macOS apps are outside this fixture. Every other detection
  // surface already resolves through the isolated home/PATH below.
  const preload = path.join(dir, 'hide-system-apps.cjs');
  fs.writeFileSync(preload, `const fs = require('fs'); const exists = fs.existsSync; fs.existsSync = p => String(p).startsWith('/Applications/') ? false : exists(p);`);
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => key.toLowerCase() !== 'path'));
  Object.assign(env, { PATH: process.platform === 'win32' ? bin : `${bin}:/usr/bin:/bin`, HOME: dir, USERPROFILE: dir, APPDATA: dir, LOCALAPPDATA: dir, XDG_CONFIG_HOME: dir });
  const result = spawnSync(process.execPath, ['--require', preload, INSTALLER, '--non-interactive'], { encoding: 'utf8', cwd: dir, env });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(fs.existsSync(log), false, 'upstream --all installs agents that were never detected');
  assert.match(result.stdout, /no agents detected/);
});

test('current Kiro and Mistral executables trigger their own install profiles', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman vendor commands '));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const bin = path.join(dir, 'bin');
  nodeStub(bin, 'kiro-cli', 'process.exit(0);');
  nodeStub(bin, 'vibe', 'process.exit(0);');
  const preload = path.join(dir, 'hide-system-apps.cjs');
  fs.writeFileSync(preload, `const fs = require('fs'); const exists = fs.existsSync; fs.existsSync = p => String(p).startsWith('/Applications/') ? false : exists(p);`);
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => key.toLowerCase() !== 'path'));
  Object.assign(env, { PATH: process.platform === 'win32' ? bin : `${bin}:/usr/bin:/bin`, HOME: dir, USERPROFILE: dir, APPDATA: dir, LOCALAPPDATA: dir, XDG_CONFIG_HOME: dir });
  const result = spawnSync(process.execPath, ['--require', preload, INSTALLER, '--minimal', '--dry-run', '--non-interactive'], {
    encoding: 'utf8', cwd: dir, env,
  });
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(result.stdout, /Kiro CLI detected/);
  assert.match(result.stdout, /-a kiro-cli --yes -g/);
  assert.match(result.stdout, /Mistral Vibe detected/);
  assert.match(result.stdout, /-a mistral-vibe --yes -g/);
  assert.doesNotMatch(result.stdout, /no agents detected/);
});

// #1189: the standalone GitHub Copilot CLI ships a `copilot` binary and reads
// the github-copilot profile's global skills. Extension-only probes missed it.
// AWS Copilot CLI ships a `copilot` binary too, so the binary counts only
// beside GitHub Copilot CLI's ~/.copilot config dir.
test('standalone Copilot CLI executable triggers the github-copilot profile', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman copilot cli '));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const bin = path.join(dir, 'bin');
  nodeStub(bin, 'copilot', 'process.exit(0);');
  const preload = path.join(dir, 'hide-system-apps.cjs');
  fs.writeFileSync(preload, `const fs = require('fs'); const exists = fs.existsSync; fs.existsSync = p => String(p).startsWith('/Applications/') ? false : exists(p);`);
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => key.toLowerCase() !== 'path'));
  Object.assign(env, { PATH: process.platform === 'win32' ? bin : `${bin}:/usr/bin:/bin`, HOME: dir, USERPROFILE: dir, APPDATA: dir, LOCALAPPDATA: dir, XDG_CONFIG_HOME: dir });
  const run = () => spawnSync(process.execPath, ['--require', preload, INSTALLER, '--minimal', '--dry-run', '--non-interactive'], {
    encoding: 'utf8', cwd: dir, env,
  });
  const awsOnly = run();
  assert.equal(awsOnly.status, 0, awsOnly.stdout + awsOnly.stderr);
  assert.doesNotMatch(awsOnly.stdout, /GitHub Copilot detected/);
  fs.mkdirSync(path.join(dir, '.copilot'));
  const result = run();
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(result.stdout, /GitHub Copilot detected/);
  assert.match(result.stdout, /-a github-copilot --yes -g/);
});

// #408: CodeBuddy Code ships a `codebuddy` binary; upstream skills has a
// `codebuddy` profile (~/.codebuddy/skills).
test('CodeBuddy Code executable triggers the codebuddy profile', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman codebuddy '));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const bin = path.join(dir, 'bin');
  nodeStub(bin, 'codebuddy', 'process.exit(0);');
  const preload = path.join(dir, 'hide-system-apps.cjs');
  fs.writeFileSync(preload, `const fs = require('fs'); const exists = fs.existsSync; fs.existsSync = p => String(p).startsWith('/Applications/') ? false : exists(p);`);
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => key.toLowerCase() !== 'path'));
  Object.assign(env, { PATH: process.platform === 'win32' ? bin : `${bin}:/usr/bin:/bin`, HOME: dir, USERPROFILE: dir, APPDATA: dir, LOCALAPPDATA: dir, XDG_CONFIG_HOME: dir });
  const result = spawnSync(process.execPath, ['--require', preload, INSTALLER, '--minimal', '--dry-run', '--non-interactive'], {
    encoding: 'utf8', cwd: dir, env,
  });
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(result.stdout, /CodeBuddy Code detected/);
  assert.match(result.stdout, /-a codebuddy --yes -g/);
});

// Cline ships as saoudrizwan.claude-dev, so a bare /cline/ never matched it but
// did match Roo Code (rooveterinaryinc.roo-cline); /roo/ matched any Groovy
// extension. goose, forge and bob are also a DB migrator, Foundry and a neovim
// manager, so the binary alone is not an agent: its config dir must exist too.
test('detection finds real Cline/Roo extensions and ignores look-alike names', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman look-alikes '));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const bin = path.join(dir, 'bin');
  for (const name of ['goose', 'forge', 'bob']) nodeStub(bin, name, 'process.exit(0);');
  const ext = path.join(dir, '.vscode', 'extensions');
  fs.mkdirSync(path.join(ext, 'marlon407.code-groovy-0.1.2'), { recursive: true });
  const preload = path.join(dir, 'hide-system-apps.cjs');
  fs.writeFileSync(preload, `const fs = require('fs'); const exists = fs.existsSync; fs.existsSync = p => String(p).startsWith('/Applications/') ? false : exists(p);`);
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => key.toLowerCase() !== 'path'));
  Object.assign(env, { PATH: process.platform === 'win32' ? bin : `${bin}:/usr/bin:/bin`, HOME: dir, USERPROFILE: dir, APPDATA: dir, LOCALAPPDATA: dir, XDG_CONFIG_HOME: dir });
  const run = () => {
    const result = spawnSync(process.execPath, ['--require', preload, INSTALLER, '--minimal', '--dry-run', '--non-interactive'], {
      encoding: 'utf8', cwd: dir, env,
    });
    assert.equal(result.status, 0, result.stdout + result.stderr);
    return result.stdout;
  };
  assert.doesNotMatch(run(), /(IBM Bob|ForgeCode|Block Goose|Roo Code|Cline) detected/);

  for (const name of ['.bob', '.forge', path.join('.config', 'goose')]) fs.mkdirSync(path.join(dir, name), { recursive: true });
  fs.mkdirSync(path.join(ext, 'saoudrizwan.claude-dev-4.1.23'));
  const found = run();
  for (const label of ['IBM Bob', 'ForgeCode', 'Block Goose', 'Cline']) assert.match(found, new RegExp(`${label} detected`));
  assert.doesNotMatch(found, /Roo Code detected/);

  fs.rmSync(path.join(ext, 'saoudrizwan.claude-dev-4.1.23'), { recursive: true });
  fs.mkdirSync(path.join(ext, 'rooveterinaryinc.roo-cline-3.25.0'));
  const roo = run();
  assert.match(roo, /Roo Code detected/);
  assert.doesNotMatch(roo, /Cline detected/);
});
