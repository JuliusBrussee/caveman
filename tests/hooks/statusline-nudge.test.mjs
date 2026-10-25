import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const HOOK = path.join(ROOT, 'src/hooks/caveman-activate.js');

test('SessionStart statusline command preserves a literal dollar-sign path', {
  skip: process.platform === 'win32' ? 'POSIX shell quoting behavior' : false,
}, (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-$CAVEMAN_PATH_SEGMENT-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const config = path.join(dir, 'claude');
  fs.mkdirSync(config);
  const env = {
    ...process.env,
    HOME: dir,
    USERPROFILE: dir,
    CLAUDE_CONFIG_DIR: config,
    CAVEMAN_DEFAULT_MODE: 'caveman',
  };
  delete env.CAVEMAN_PATH_SEGMENT;
  delete env.CLAUDE_CODE_ENTRYPOINT;

  const sessionId = '14000000-0000-4000-8000-000000000001';
  const activation = spawnSync(process.execPath, [HOOK], {
    cwd: dir,
    env,
    input: JSON.stringify({ source: 'startup', session_id: sessionId, cwd: dir }),
    encoding: 'utf8',
    timeout: 5000,
  });
  assert.equal(activation.status, 0, activation.stderr);
  const match = activation.stdout.match(/"statusLine":\s*(\{[^\n]+\})/);
  assert.ok(match, activation.stdout);
  const { command } = JSON.parse(match[1]);
  assert.ok(fs.existsSync(path.join(config, 'hooks/caveman-statusline.sh')));

  const statusline = spawnSync('/bin/sh', ['-c', command], {
    cwd: dir,
    env,
    input: JSON.stringify({ session_id: sessionId }),
    encoding: 'utf8',
    timeout: 5000,
  });
  assert(statusline.status === 0, statusline.stderr);
  assert.match(statusline.stdout, /\[CAVEMAN\]/);
});
