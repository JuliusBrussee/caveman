'use strict';

const path = require('path');

function powershellQuote(value) {
  return `'${String(value).replace(/'/g, "''")}'`;
}

// UNVERIFIED ASSUMPTION — do not treat the test below as proof.
// The win32 branch emits PowerShell call-operator syntax (`& 'C:\\...\\node.exe'
// 'C:\\...\\hook.js'`). Issue #835 describes the Claude Code hook runner on
// Windows as git-bash's /bin/sh, where a leading `&` is a syntax error, not a
// call operator. Either that report is wrong about the runner or this shape is.
// tests/installer/platform-paths.test.mjs asserts the string this produces, but
// nothing in the repo ever executes an installed hook on Windows, so the shape
// itself has never been exercised. Confirm against a real Windows host before
// the next release that touches install; if the runner is sh, drop the `&` and
// emit forward-slash double-quoted paths instead.
function hookCommand(executable, args, platform = process.platform) {
  if (platform === 'win32') {
    return `& ${[executable, ...args].map(powershellQuote).join(' ')}`;
  }
  return [executable, ...args]
    .map(value => `"${String(value).replace(/(["\\$`])/g, '\\$1')}"`)
    .join(' ');
}

function jetbrainsRoots(home, env = process.env) {
  const roots = [
    path.join(home, 'Library/Application Support/JetBrains'),
    path.join(home, '.config/JetBrains'),
  ];
  for (const key of ['APPDATA', 'LOCALAPPDATA']) {
    if (env[key]) roots.push(path.join(env[key], 'JetBrains'));
  }
  return [...new Set(roots)];
}

module.exports = { hookCommand, jetbrainsRoots, powershellQuote };
