import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
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
  assert.deepEqual(
    jetbrainsRoots('/Users/jane', { APPDATA: 'C:\\Users\\jane\\AppData\\Roaming', LOCALAPPDATA: 'C:\\Users\\jane\\AppData\\Local' }),
    [
      '/Users/jane/Library/Application Support/JetBrains',
      '/Users/jane/.config/JetBrains',
      'C:\\Users\\jane\\AppData\\Roaming/JetBrains',
      'C:\\Users\\jane\\AppData\\Local/JetBrains',
    ],
  );
});
