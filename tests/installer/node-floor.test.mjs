// `absoluteNodePath()` persists a node path into settings.json, and every hook
// then runs under it. It must not persist an interpreter below the package's
// supported runtime floor.
//
// The floor lives in bin/install.js as MIN_NODE_MAJOR rather than being read
// from package.json at runtime, because that file also runs detached from a
// checkout (the curl fallback path). These tests are what keep the constant
// and `engines.node` from drifting apart, and what prove the guard is applied.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'bin', 'install.js');

function installerMinNodeMajor() {
  const src = fs.readFileSync(INSTALLER, 'utf8');
  const m = /const MIN_NODE_MAJOR = (\d+);/.exec(src);
  assert.ok(m, 'MIN_NODE_MAJOR not found in bin/install.js');
  return Number(m[1]);
}

test('MIN_NODE_MAJOR matches the engines.node floor in package.json', () => {
  const pkg = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, 'package.json'), 'utf8'));
  const engines = pkg.engines?.node;
  assert.ok(engines, 'package.json declares no engines.node');
  const declared = /(\d+)/.exec(engines);
  assert.ok(declared, `could not read a major version out of engines.node: ${engines}`);
  assert.equal(
    installerMinNodeMajor(),
    Number(declared[1]),
    `bin/install.js MIN_NODE_MAJOR drifted from package.json engines.node (${engines})`
  );
});

// A PATH node that runs fine but predates the floor must lose to
// process.execPath, which satisfies the floor by construction — it is running
// the installer.
test('a PATH node below the engines floor is not persisted', { skip: process.platform === 'win32' && 'POSIX shim behavior' }, () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-node-floor-'));
  const fakeBin = path.join(dir, 'fake-bin');
  const configDir = path.join(dir, 'claude-config');
  fs.mkdirSync(fakeBin, { recursive: true });

  const old = installerMinNodeMajor() - 2;
  // Reports an old major for `--version`; delegates everything else to the real
  // node so the installer still works if it ever chose this one.
  const fakeNode = path.join(fakeBin, 'node');
  fs.writeFileSync(fakeNode,
    '#!/bin/sh\n'
    + `if [ "$1" = --version ]; then echo "v${old}.9.0"; exit 0; fi\n`
    + `exec ${JSON.stringify(process.execPath)} "$@"\n`);
  fs.chmodSync(fakeNode, 0o755);

  try {
    const r = spawnSync(process.execPath, [
      INSTALLER, '--only', 'claude', '--with-hooks', '--skip-skills',
      '--config-dir', configDir, '--non-interactive', '--no-mcp-shrink',
    ], {
      env: { ...process.env, PATH: `${fakeBin}:${process.env.PATH || ''}`, CLAUDE_CONFIG_DIR: configDir, NO_COLOR: '1' },
      encoding: 'utf8',
    });
    assert.notEqual(r.status, 2, `installer aborted on argv parse: ${r.stderr}`);

    const settings = JSON.parse(fs.readFileSync(path.join(configDir, 'settings.json'), 'utf8'));
    const command = (settings.hooks?.SessionStart || [])
      .flatMap(e => (Array.isArray(e?.hooks) ? e.hooks : []))
      .map(h => h?.command || '')
      .find(c => c.includes('caveman-activate')) || '';
    const storedNode = /^"([^"]+)"/.exec(command)?.[1];
    assert.ok(storedNode, `no node path in hook command: ${command}`);
    assert.notEqual(storedNode, fakeNode, 'installer persisted a node below the engines floor');

    const probe = spawnSync(storedNode, ['--version'], { encoding: 'utf8' });
    assert.equal(probe.status, 0, `stored hook executable must run: ${probe.stderr}`);
    const major = Number(/^v(\d+)\./.exec(probe.stdout.trim())?.[1]);
    assert.ok(major >= installerMinNodeMajor(), `stored node v${major} is below the floor`);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
