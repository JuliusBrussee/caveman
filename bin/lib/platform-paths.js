'use strict';

const path = require('path');

function powershellQuote(value) {
  return `'${String(value).replace(/'/g, "''")}'`;
}

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
