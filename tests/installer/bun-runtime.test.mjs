import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { isolatedEnv } from './_isolated-env.mjs';

const require = createRequire(import.meta.url);
const { packageInvocation } = require('../../installer/lib/package-runner.js');
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const installer = path.join(root, 'installer/install.js');
const shell = fs.readFileSync(path.join(root, 'install.sh'), 'utf8');
const ref = 'v0.0.0-bun-test';

function fixture(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman bun '));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const bin = path.join(dir, 'bin');
  const home = path.join(dir, 'home');
  fs.mkdirSync(bin);
  fs.mkdirSync(home);
  return { dir, bin, home };
}
function executable(file, body) {
  fs.writeFileSync(file, `#!/bin/sh\n${body}\n`, { mode: 0o755 });
}
function shim(t, { node, bun = true, npx = true, local = false, code = 0, args = ['--help'] } = {}) {
  const f = fixture(t);
  const log = path.join(f.dir, 'args');
  const body = `printf '%s\\n' "$0" "$@" > "$ARGS_LOG"\nexit ${code}`;
  if (bun) executable(path.join(f.bin, 'bun'), body);
  if (node) executable(path.join(f.bin, 'node'), `if [ "$1" = -p ]; then case "$2" in *split*) echo ${node} ;; *) echo ${node}.0.0 ;; esac; exit 0; fi\n${body}`);
  if (npx) executable(path.join(f.bin, 'npx'), `if [ "$1" = --version ]; then echo 12.0.0; exit 0; fi\n${body}`);
  fs.symlinkSync('/usr/bin/dirname', path.join(f.bin, 'dirname'));
  const file = path.join(f.dir, 'install.sh');
  fs.writeFileSync(file, shell);
  if (local) {
    fs.mkdirSync(path.join(f.dir, 'installer'));
    fs.writeFileSync(path.join(f.dir, 'installer/install.js'), '// fixture, not executed');
  }
  const result = spawnSync('/bin/bash', local ? [file, ...args] : ['-s', '--', ...args], {
    cwd: f.dir, input: shell, encoding: 'utf8',
    env: { ...isolatedEnv(f.home), PATH: f.bin, ARGS_LOG: log, CAVEMAN_REF: ref },
  });
  return { ...result, logged: fs.existsSync(log) ? fs.readFileSync(log, 'utf8').trim().split('\n') : [], ...f };
}

for (const local of [false, true]) {
  test(`Bun shell fallback preserves argv and pin (${local ? 'local' : 'pipe'})`, { skip: process.platform === 'win32' }, t => {
    const args = ['--only', 'codex', '--config-dir', 'a path; with & chars', '--dry-run'];
    const r = shim(t, { local, args });
    assert.equal(r.status, 0, r.stderr);
    assert.deepEqual(r.logged.slice(1), local
      ? [path.join(r.dir, 'installer/install.js'), ...args]
      : ['x', '--bun', `github:JuliusBrussee/caveman#${ref}`, ...args]);
    assert.doesNotMatch(r.stdout, /runtime .*needs Node/);
  });
  test(`Bun shell fallback propagates installer failure (${local ? 'local' : 'pipe'})`, { skip: process.platform === 'win32' }, t => {
    const r = shim(t, { local, code: 7, args: [] });
    assert.equal(r.status, 7);
    assert.doesNotMatch(r.stdout, /installer finished|Next:/);
  });
}
test('shell prefers Node and keeps npm 12 handling when Bun is present', { skip: process.platform === 'win32' }, t => {
  const r = shim(t, { node: 20 });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(path.basename(r.logged[0]), 'npx');
  assert.deepEqual(r.logged.slice(1), ['--allow-git=root', '-y', `github:JuliusBrussee/caveman#${ref}`, '--help']);
});
test('old Node and missing npx do not silently switch to Bun', { skip: process.platform === 'win32' }, t => {
  const old = shim(t, { node: 16 });
  assert.equal(old.status, 1);
  assert.match(old.stderr, /Node 16 too old/);
  assert.deepEqual(old.logged, []);
  const noNpx = shim(t, { node: 20, npx: false });
  assert.equal(noNpx.status, 1);
  assert.match(noNpx.stderr, /npx required/);
  assert.deepEqual(noNpx.logged, []);
});
test('neither runtime yields actionable guidance', { skip: process.platform === 'win32' }, t => {
  const r = shim(t, { bun: false });
  assert.equal(r.status, 1);
  assert.match(r.stderr, /Node.js .* or Bun required/);
});
test('Bun first run names the Node CLI requirement without launching it', { skip: process.platform === 'win32' }, t => {
  const r = shim(t, { args: ['--only', 'codex', '--non-interactive'] });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /runtime .*needs Node 22\.13\+/);
  assert.equal(path.basename(r.logged[0]), 'bun');
  assert.deepEqual(r.logged.slice(1), ['x', '--bun', `github:JuliusBrussee/caveman#${ref}`, '--only', 'codex', '--non-interactive']);
});

test('package runner preserves Node argv and forces Bun before the package', () => {
  const args = ['skills', 'add', `JuliusBrussee/caveman#${ref}`, '--skill', '*', '-a', 'codex', '--yes'];
  assert.deepEqual(packageInvocation(args, { bun: undefined }), { command: 'npx', args: ['-y', ...args] });
  for (const execPath of ['/a path/bun', 'C:\\Program Files\\bun\\bun.exe']) {
    assert.deepEqual(packageInvocation(args, { bun: '1.4.3', execPath }), { command: execPath, args: ['x', '--bun', ...args] });
  }
  assert.equal(args[0], 'skills', 'runner must not mutate caller argv');
});

// These deterministic contracts emulate Bun detection under Node, not Bun
// compatibility. Actual Bun smoke coverage is separate below when installed.
function runDetectedBun(t, args, prepare) {
  const f = fixture(t);
  const preload = path.join(f.dir, 'bun-detection.cjs');
  fs.writeFileSync(preload, "Object.defineProperty(process.versions, 'bun', {value:'test'});\n");
  prepare?.(f);
  return { ...spawnSync(process.execPath, ['--require', preload, installer, ...args], {
    cwd: f.dir, env: isolatedEnv(f.home, [f.bin]), encoding: 'utf8', timeout: 30000,
  }), ...f };
}
for (const args of [['--with-hooks'], ['--with-mcp-shrink=upstream'], ['--only', 'claude']]) {
  test(`Bun rejects ${args.join(' ')} before changing the home`, t => {
    const r = runDetectedBun(t, [...args, '--non-interactive']);
    assert.equal(r.status, 2, r.stdout + r.stderr);
    assert.match(r.stderr, /require[s]? Node/);
    assert.deepEqual(fs.readdirSync(r.home), []);
  });
}
for (const provider of ['codex', 'cursor', 'copilot']) {
  test(`Bun ${provider} dry run skips automatic Node hooks`, t => {
    const r = runDetectedBun(t, ['--only', provider, '--dry-run', '--non-interactive']);
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.match(r.stdout, /x --bun skills add/);
    assert.doesNotMatch(r.stdout, /would (?:wire|write).*hook|would .*SessionStart/);
    assert.deepEqual(fs.readdirSync(r.home), []);
  });
}
test('unsupported Claude does not hide failure of every attempted Bun install', t => {
  const r = runDetectedBun(t, ['--non-interactive'], f => {
    for (const name of ['claude', 'codex']) {
      if (process.platform === 'win32') fs.writeFileSync(path.join(f.bin, `${name}.exe`), 'fixture only');
      else executable(path.join(f.bin, name), 'exit 1');
    }
  });
  // Detection sees both agents, but neither fixture is executed. The nested
  // command fails because this emulated Bun is Node and cwd has no script x.
  assert.match(r.stdout, /claude.*plugin hooks require Node/);
  assert.match(r.stderr, /failed:[\s\S]*codex/);
  assert.equal(r.status, 1, r.stdout + r.stderr);
});
test('Bun uninstall does not launch the Node CLI and preserves stranded route warning', { skip: process.platform === 'win32' }, t => {
  const r = runDetectedBun(t, ['--uninstall', '--non-interactive'], f => {
    const integrations = path.join(f.home, '.caveman/integrations');
    fs.mkdirSync(integrations, { recursive: true });
    fs.writeFileSync(path.join(integrations, 'claude.json'), '{}');
    executable(path.join(f.bin, 'caveman'), `echo unwanted > '${path.join(f.dir, 'cli-ran')}'`);
  });
  assert.equal(r.status, 1, r.stdout + r.stderr);
  assert.equal(fs.existsSync(path.join(r.dir, 'cli-ran')), false);
  assert.match(r.stderr, /native Caveman routing is still installed/);
  assert.match(r.stderr, /Node 22\.13.*Bun test/);
});

test('detached native skills use the same Bun runner', t => {
  const f = fixture(t);
  const script = `
Object.defineProperty(process.versions, 'bun', { value: 'test' });
const fs = require('fs'), path = require('path');
require(${JSON.stringify(path.join(root, 'installer/lib/provider-skills.js'))}).install({
  provider: 'continue', root: ${JSON.stringify(path.join(f.home, 'skills'))}, ref: ${JSON.stringify(ref)},
  run(command, args, options) {
    process.stdout.write(JSON.stringify({command, args}) + '\\n');
    const dir = path.join(options.cwd, '.agents/skills/caveman');
    fs.mkdirSync(dir, {recursive:true}); fs.writeFileSync(path.join(dir, 'SKILL.md'), 'fixture');
    return {status:0};
  }
});`;
  const r = spawnSync(process.execPath, ['-e', script], { env: isolatedEnv(f.home), encoding: 'utf8' });
  assert.equal(r.status, 0, r.stderr);
  assert.deepEqual(JSON.parse(r.stdout), {
    command: process.execPath,
    args: ['x', '--bun', 'skills', 'add', `JuliusBrussee/caveman#${ref}`, '--skill', '*', '-a', 'codex', '--yes', '--copy'],
  });
  assert.equal(fs.readFileSync(path.join(f.home, 'skills/caveman/SKILL.md'), 'utf8'), 'fixture');
});

const bunBinary = process.env.CAVEMAN_TEST_BUN;
test('real Bun installs native skills in an isolated home without Node on PATH', { skip: !bunBinary }, t => {
  const f = fixture(t);
  const env = { ...isolatedEnv(f.home, [f.bin]), PATH: f.bin };
  const r = spawnSync(bunBinary, [installer, '--only', 'continue', '--non-interactive'], {
    cwd: f.dir, env, encoding: 'utf8', timeout: 30000,
  });
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.ok(fs.existsSync(path.join(f.home, '.continue/skills/caveman/SKILL.md')));
});
test('real Bun dry-run keeps nested skills on Bun and skips Codex hooks', { skip: !bunBinary }, t => {
  const f = fixture(t);
  const r = spawnSync(bunBinary, [installer, '--only', 'codex', '--dry-run', '--non-interactive'], {
    cwd: f.dir, env: { ...isolatedEnv(f.home), PATH: f.bin }, encoding: 'utf8', timeout: 30000,
  });
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.match(r.stdout, /x --bun skills add/);
  assert.equal(fs.existsSync(path.join(f.home, '.codex/hooks.json')), false);
});

test('real Bun executes a local skills fixture despite its Node shebang', { skip: !bunBinary || process.platform === 'win32' }, t => {
  const f = fixture(t);
  const bin = path.join(f.dir, 'node_modules/.bin');
  fs.mkdirSync(bin, { recursive: true });
  const log = path.join(f.dir, 'skills.json');
  fs.writeFileSync(path.join(bin, 'skills'), `#!/usr/bin/env node\nrequire('fs').writeFileSync(${JSON.stringify(log)}, JSON.stringify({bun:process.versions.bun,args:process.argv.slice(2)}));\n`, { mode: 0o755 });
  const r = spawnSync(bunBinary, [installer, '--only', 'codex', '--non-interactive'], {
    cwd: f.dir, env: { ...isolatedEnv(f.home), PATH: f.bin, BUN_INSTALL_CACHE_DIR: path.join(f.dir, 'cache') }, encoding: 'utf8', timeout: 30000,
  });
  assert.equal(r.status, 0, r.stdout + r.stderr);
  const result = JSON.parse(fs.readFileSync(log, 'utf8'));
  assert.ok(result.bun, 'the Node-shebang fixture must actually run under Bun');
  const pinned = fs.readFileSync(installer, 'utf8').match(/const PINNED_REF = process.env.CAVEMAN_REF \|\| '([^']+)'/)[1];
  assert.deepEqual(result.args, ['add', `JuliusBrussee/caveman#${process.env.CAVEMAN_REF || pinned}`, '--skill', '*', '-a', 'codex', '--yes', '-g']);
  assert.equal(fs.existsSync(path.join(f.home, '.codex/hooks.json')), false);
});
test('real Bun shell shim installs from a clone without Node', { skip: !bunBinary || process.platform === 'win32' }, t => {
  const f = fixture(t);
  fs.symlinkSync(bunBinary, path.join(f.bin, 'bun'));
  fs.symlinkSync('/usr/bin/dirname', path.join(f.bin, 'dirname'));
  const r = spawnSync('/bin/bash', [path.join(root, 'install.sh'), '--only', 'continue', '--non-interactive'], {
    cwd: f.dir, env: { ...isolatedEnv(f.home), PATH: f.bin }, encoding: 'utf8', timeout: 30000,
  });
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.ok(fs.existsSync(path.join(f.home, '.continue/skills/caveman/SKILL.md')));
  assert.match(r.stdout, /runtime .*needs Node 22\.13\+/);
});
