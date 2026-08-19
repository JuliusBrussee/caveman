import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import path from 'node:path';
import test from 'node:test';

const require = createRequire(import.meta.url);
const { hookCommand, jetbrainsRoots } = require('../../bin/lib/platform-paths.js');

// #835: Claude Code runs hooks through Git Bash on Windows unless the hook
// sets "shell": "powershell". The old PowerShell call-operator shape
// (`& 'x' 'y'`) is a bash syntax error, so every Windows hook failed on every
// event. Assert the bash-safe shape — forward slashes, POSIX quoting, no `&`.
test('Windows hook commands are bash-safe (Git Bash is the default hook shell)', () => {
  const command = hookCommand(
    "C:\\Program Files\\nodejs\\node.exe",
    ["C:\\Users\\O'Brien\\.claude\\hooks\\caveman-activate.js"],
    'win32',
  );
  assert.equal(
    command,
    '"C:/Program Files/nodejs/node.exe" "C:/Users/O\'Brien/.claude/hooks/caveman-activate.js"',
  );
  assert.ok(!command.startsWith('&'), 'a leading & is a bash syntax error');
});

// Parse the emitted string with the real shell rather than trusting the
// literal above — bash is the parser Git Bash uses, so a shape that survives
// `bash -n` here survives the hook runner there. Spaces and an apostrophe in
// the paths are the cases that actually broke.
test('Windows hook command parses under bash and preserves both arguments', () => {
  const command = hookCommand(
    "C:\\Program Files\\nodejs\\node.exe",
    ["C:\\Users\\O'Brien\\.claude\\hooks\\caveman-activate.js"],
    'win32',
  );
  const parsed = spawnSync('bash', ['-c', `printf '%s\\n' ${command}`], { encoding: 'utf8' });
  assert.equal(parsed.status, 0, `bash rejected the hook command: ${parsed.stderr}`);
  assert.deepEqual(parsed.stdout.split('\n').filter(Boolean), [
    'C:/Program Files/nodejs/node.exe',
    "C:/Users/O'Brien/.claude/hooks/caveman-activate.js",
  ]);
});

test('macOS hook command shape stays shell-compatible', () => {
  assert.equal(
    hookCommand('/usr/local/bin/node', ['/Users/Jane Doe/.claude/hooks/caveman-activate.js'], 'darwin'),
    '"/usr/local/bin/node" "/Users/Jane Doe/.claude/hooks/caveman-activate.js"',
  );
});

test('JetBrains roots include Windows roaming and local AppData', () => {
  // jetbrainsRoots probes the host filesystem, so its joins are host-shaped;
  // build the expectations the same way instead of hardcoding POSIX output.
  assert.deepEqual(
    jetbrainsRoots('/Users/jane', { APPDATA: 'C:\\Users\\jane\\AppData\\Roaming', LOCALAPPDATA: 'C:\\Users\\jane\\AppData\\Local' }),
    [
      path.join('/Users/jane', 'Library/Application Support/JetBrains'),
      path.join('/Users/jane', '.config/JetBrains'),
      path.join('C:\\Users\\jane\\AppData\\Roaming', 'JetBrains'),
      path.join('C:\\Users\\jane\\AppData\\Local', 'JetBrains'),
    ],
  );
});
