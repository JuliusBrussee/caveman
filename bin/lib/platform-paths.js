'use strict';

const path = require('path');

function powershellQuote(value) {
  return `'${String(value).replace(/'/g, "''")}'`;
}

// cmd.exe-safe quoting: Claude Code spawns hook commands through cmd.exe on
// Windows (not PowerShell), so the `& 'exe' 'arg'` PowerShell call-operator
// form this used to emit fails every hook invocation with "& was unexpected
// at this time" — the hook exits 1 silently and its ruleset/flag writes never
// happen. cmd.exe needs plain double-quoted tokens, no leading `&`.
function cmdQuote(value) {
  return `"${String(value).replace(/"/g, '""')}"`;
}

function hookCommand(executable, args, platform = process.platform) {
  if (platform === 'win32') {
    return [executable, ...args].map(cmdQuote).join(' ');
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
