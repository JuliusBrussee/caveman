import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const INSTALLER = path.resolve(HERE, '..', '..', 'bin', 'install.js');
const TEST_PREFIX = 'caveman-cursor-global-';
const FAKE_BIN_DIR = 'bin';
const FAKE_NPX = 'npx';
const ARGS_LOG = 'npx-args.json';
const CURSOR_SKILLS_PATH = path.join('.cursor', 'skills');
const GLOBAL_FLAG = '-g';

test('Cursor installs skills globally into its user skills directory', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), TEST_PREFIX));
  const home = path.join(root, 'home');
  const binDir = path.join(root, FAKE_BIN_DIR);
  const argsLog = path.join(root, ARGS_LOG);
  fs.mkdirSync(binDir, { recursive: true });

  const fakeNpx = path.join(binDir, FAKE_NPX);
  fs.writeFileSync(fakeNpx, `#!/bin/sh\nprintf '%s\\n' "$@" | node -e "let d='';process.stdin.on('data',c=>d+=c).on('end',()=>require('fs').writeFileSync(process.env.ARGS_LOG,JSON.stringify(d.trim().split('\\n'))))"\n`);
  fs.chmodSync(fakeNpx, 0o755);

  try {
    const result = spawnSync(process.execPath, [INSTALLER, '--only', 'cursor', '--non-interactive'], {
      encoding: 'utf8',
      env: {
        ...process.env,
        HOME: home,
        USERPROFILE: home,
        PATH: `${binDir}${path.delimiter}${process.env.PATH || ''}`,
        ARGS_LOG: argsLog,
        NO_COLOR: '1',
      },
    });

    assert.equal(result.status, 0, result.stderr || result.stdout);
    assert.ok(fs.existsSync(path.join(home, CURSOR_SKILLS_PATH)));
    assert.ok(JSON.parse(fs.readFileSync(argsLog, 'utf8')).includes(GLOBAL_FLAG));
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
