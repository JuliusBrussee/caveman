import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const SH = fs.readFileSync(path.join(REPO_ROOT, 'install.sh'), 'utf8');

function writeExecutable(file, body) {
  fs.writeFileSync(file, `#!/bin/sh\n${body}\n`, { mode: 0o755 });
}

function runShim(npxVersion) {
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-shim-test-'));
  const argsFile = path.join(temp, 'npx-args');

  try {
    writeExecutable(path.join(temp, 'node'), 'printf "20\\n"');
    writeExecutable(path.join(temp, 'npx'), `
if [ "$1" = "--version" ]; then
  printf '${npxVersion}\\n'
  exit 0
fi
printf '%s\\n' "$@" > "$NPX_ARGS_FILE"
`);

    const result = spawnSync('bash', ['-s', '--', '--only', 'claude'], {
      cwd: temp,
      input: SH,
      encoding: 'utf8',
      env: {
        ...process.env,
        PATH: `${temp}${path.delimiter}${process.env.PATH || ''}`,
        NPX_ARGS_FILE: argsFile,
      },
    });

    assert.equal(result.status, 0, result.stderr);
    return fs.readFileSync(argsFile, 'utf8').trim().split('\n');
  } finally {
    fs.rmSync(temp, { recursive: true, force: true });
  }
}

test('shell shim opts into root git fetching on npm 12+', {
  skip: process.platform === 'win32',
}, () => {
  assert.deepEqual(runShim('12.0.1'), [
    '--allow-git=root',
    '-y',
    'github:JuliusBrussee/caveman',
    '--only',
    'claude',
  ]);
});

test('shell shim keeps legacy invocation before npm 12', {
  skip: process.platform === 'win32',
}, () => {
  assert.deepEqual(runShim('11.15.0'), [
    '-y',
    'github:JuliusBrussee/caveman',
    '--only',
    'claude',
  ]);
});
