// Covers issue #836: `npx skills add` without -g writes to a PROJECT-local
// ./.agents/skills under whatever directory the installer ran from. A user who
// ran the curl|bash one-liner from ~/.local/bin got the skills in
// ~/.local/bin/.agents/skills while Cursor's Skills UI reads ~/.cursor/skills —
// the installer printed "Installation complete" and nothing ever appeared.
//
// A real `npx skills add` clones the repo and writes to the user's home, so
// these tests substitute a fake `npx` on PATH that only records its argv.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const INSTALLER = path.resolve(HERE, '..', '..', 'bin', 'install.js');

// Runs the installer for one provider with a fake `npx` on PATH, and returns
// the argv that fake npx was called with plus the sandboxed HOME.
function runInstall(providerId) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-skills-global-'));
  const home = path.join(root, 'home');
  const fakeBin = path.join(root, 'bin');
  const argsLog = path.join(root, 'npx-args.json');
  fs.mkdirSync(home, { recursive: true });
  fs.mkdirSync(fakeBin, { recursive: true });

  // The provider must also LOOK installed, or the installer skips it. Every
  // skills-based provider we exercise here detects via a command on PATH.
  const shim = `#!/bin/sh\nexit 0\n`;
  for (const bin of ['cursor', 'windsurf']) {
    fs.writeFileSync(path.join(fakeBin, bin), shim, { mode: 0o755 });
  }
  fs.writeFileSync(
    path.join(fakeBin, 'npx'),
    `#!/usr/bin/env node\nrequire("fs").writeFileSync(${JSON.stringify(argsLog)}, JSON.stringify(process.argv.slice(2)));\n`,
    { mode: 0o755 },
  );

  const result = spawnSync(process.execPath, [INSTALLER, '--only', providerId], {
    encoding: 'utf8',
    env: {
      ...process.env,
      HOME: home,
      USERPROFILE: home,
      PATH: `${fakeBin}${path.delimiter}${process.env.PATH ?? ''}`,
    },
  });

  const argv = fs.existsSync(argsLog) ? JSON.parse(fs.readFileSync(argsLog, 'utf8')) : null;
  return { argv, home, result, cleanup: () => fs.rmSync(root, { recursive: true, force: true }) };
}

test('Cursor install passes -g so skills land in the user skills directory', { skip: process.platform === 'win32' }, () => {
  const { argv, home, result, cleanup } = runInstall('cursor');
  try {
    assert.ok(argv, `fake npx was never invoked:\n${result.stdout}\n${result.stderr}`);
    assert.ok(argv.includes('-g'), `expected -g in argv, got: ${JSON.stringify(argv)}`);
    // The flag must not displace the agent selection — #389 showed --all
    // ignores -a and writes every skill through every adapter.
    assert.ok(argv.includes('-a'), 'agent selection must survive');
    assert.strictEqual(argv[argv.indexOf('-a') + 1], 'cursor');
    assert.ok(argv.includes('--skill'), '--skill * must survive (#370)');
    assert.ok(fs.existsSync(path.join(home, '.cursor', 'skills')), 'target dir should be pre-created');
  } finally {
    cleanup();
  }
});

test('a provider with no known global skills dir is left alone', { skip: process.platform === 'win32' }, () => {
  const { argv, result, cleanup } = runInstall('windsurf');
  try {
    assert.ok(argv, `fake npx was never invoked:\n${result.stdout}\n${result.stderr}`);
    assert.ok(
      !argv.includes('-g'),
      `-g must be opt-in per provider, not blanket; got: ${JSON.stringify(argv)}`,
    );
  } finally {
    cleanup();
  }
});

test('dry run reports the global dir it would create without writing it', { skip: process.platform === 'win32' }, () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-skills-global-dry-'));
  const home = path.join(root, 'home');
  const fakeBin = path.join(root, 'bin');
  fs.mkdirSync(home, { recursive: true });
  fs.mkdirSync(fakeBin, { recursive: true });
  fs.writeFileSync(path.join(fakeBin, 'cursor'), '#!/bin/sh\nexit 0\n', { mode: 0o755 });

  try {
    const r = spawnSync(process.execPath, [INSTALLER, '--only', 'cursor', '--dry-run'], {
      encoding: 'utf8',
      env: {
        ...process.env,
        HOME: home,
        USERPROFILE: home,
        PATH: `${fakeBin}${path.delimiter}${process.env.PATH ?? ''}`,
      },
    });
    assert.match(r.stdout, /-g\b/, 'planned command must show the global flag');
    assert.ok(!fs.existsSync(path.join(home, '.cursor', 'skills')), 'dry run must not write');
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
