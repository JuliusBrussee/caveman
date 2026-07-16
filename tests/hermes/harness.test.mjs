import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const HARNESS = path.join(HERE, 'run-hermetic.sh');
const PROBE = path.join(HERE, 'fixtures', 'assert-hermetic-env.mjs');
const TAMPER = path.join(HERE, 'fixtures', 'tamper-protected.mjs');

function runHarness(command, extraEnv = {}) {
  return spawnSync('bash', [HARNESS, ...command], {
    cwd: path.resolve(HERE, '..', '..'),
    env: { ...process.env, NO_COLOR: '1', ...extraEnv },
    encoding: 'utf8',
  });
}

test('hermetic harness redirects every mutable home and shadows external harness CLIs', () => {
  const result = runHarness([process.execPath, PROBE]);
  assert.equal(result.status, 0, `stdout:\n${result.stdout}\nstderr:\n${result.stderr}`);
  assert.match(result.stdout, /hermetic environment probe: ok/);
  assert.match(result.stdout, /hermetic host invariants: ok/);
});

test('hermetic harness fails when a protected host file changes', () => {
  const protectedHome = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-protected-home-'));
  const settings = path.join(protectedHome, '.claude', 'settings.json');
  fs.mkdirSync(path.dirname(settings), { recursive: true });
  fs.writeFileSync(settings, '{"owner":"host"}\n');
  const before = fs.readFileSync(settings);

  try {
    const result = runHarness(
      [process.execPath, TAMPER, settings],
      { CAVEMAN_HERMETIC_PROTECT_HOME: protectedHome },
    );
    assert.notEqual(result.status, 0, `harness missed protected-file mutation:\n${result.stdout}`);
    assert.match(result.stderr, /protected host file changed/);
    assert.notDeepEqual(fs.readFileSync(settings), before, 'tamper fixture did not alter its disposable target');
  } finally {
    fs.rmSync(protectedHome, { recursive: true, force: true });
  }
});
