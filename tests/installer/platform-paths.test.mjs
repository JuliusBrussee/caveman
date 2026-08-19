import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import path from 'node:path';
import test from 'node:test';

const require = createRequire(import.meta.url);
const { hookCommand, jetbrainsRoots } = require('../../bin/lib/platform-paths.js');

test('Windows hook commands invoke quoted executables through PowerShell', () => {
  assert.equal(
    hookCommand(
      "C:\\Program Files\\nodejs\\node.exe",
      ["C:\\Users\\O'Brien\\.claude\\hooks\\caveman-activate.js"],
      'win32',
    ),
    "& 'C:\\Program Files\\nodejs\\node.exe' 'C:\\Users\\O''Brien\\.claude\\hooks\\caveman-activate.js'",
  );
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
