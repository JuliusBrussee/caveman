'use strict';

const path = require('path');

function powershellQuote(value) {
  return `'${String(value).replace(/'/g, "''")}'`;
}

// RESOLVED (#835): Claude Code runs hook commands through BASH on every
// platform — Git Bash on Windows — unless the hook opts into `"shell":
// "powershell"`. Confirmed from the shipped binary, which carries both
//   'Hook "…" requires bash but Git Bash was not found. … or add
//    "shell": "powershell" to this hook'"'"'s config.'
// and the inverse message for a missing PowerShell.
//
// The old win32 branch emitted PowerShell call-operator syntax
// (`& 'C:\\…\\node.exe' 'C:\\…\\hook.js'`), which bash rejects outright:
//   bash: syntax error near unexpected token `&'
// That made both caveman hooks fail on every SessionStart and
// UserPromptSubmit for every standalone Windows install.
//
// So both branches emit POSIX quoting. win32 additionally normalizes `\` to
// `/`: Windows Node accepts forward slashes, and it keeps bash from eating
// backslashes as escapes inside the double quotes.
function hookCommand(executable, args, platform = process.platform) {
  const normalize = platform === 'win32'
    ? (value) => String(value).replace(/\\/g, '/')
    : (value) => String(value);
  return [executable, ...args]
    .map(value => `"${normalize(value).replace(/(["\\$`])/g, '\\$1')}"`)
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
