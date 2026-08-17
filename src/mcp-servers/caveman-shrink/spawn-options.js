// Spawn options for the upstream MCP child process.
//
// Windows npm tools arrive as .cmd shims. Resolve through PATH/PATHEXT and
// launch their Node target directly; never join upstream arguments into a
// cmd.exe string.
//
// Exported standalone so the behavior is unit-testable without re-running
// the CLI entry point (index.js exits immediately when args are empty).

'use strict';

const fs = require('fs');
const path = require('path');

function envValue(env, name) {
  const key = Object.keys(env).find(candidate => candidate.toLowerCase() === name.toLowerCase());
  return key === undefined ? undefined : env[key];
}

function resolveWindowsCommand(command, env) {
  if (path.isAbsolute(command) || /[\\/]/.test(command)) {
    return fs.existsSync(command) ? command : null;
  }
  const pathExt = envValue(env, 'PATHEXT') || '.COM;.EXE;.BAT;.CMD';
  // Extensionless commands resolve only through PATHEXT, matching Windows
  // semantics; the bare name next to a .CMD shim is a non-executable Unix shim.
  const names = path.extname(command)
    ? [command]
    : pathExt.split(';').map(extension =>
      `${command}${extension.startsWith('.') ? extension : `.${extension}`}`);
  for (const directory of (envValue(env, 'PATH') || '').split(';')) {
    if (!directory) continue;
    for (const name of names) {
      const candidate = path.join(directory, name);
      if (fs.existsSync(candidate)) return candidate;
    }
  }
  return null;
}

function getSpawnInvocation(command, args, platform = process.platform, env = process.env) {
  if (platform !== 'win32') return { command, args: [...args] };
  const executable = resolveWindowsCommand(command, env) || command;
  if (!/\.(?:cmd|bat)$/i.test(executable)) return { command: executable, args: [...args] };
  const source = fs.readFileSync(executable, 'utf8');
  let relativeScript = null;
  for (const line of source.split(/\r?\n/)) {
    if (!/(?:\bnode(?:\.exe)?\b|_prog)/i.test(line) || !/%\*/.test(line)) continue;
    const match = line.match(/"%(?:dp0%|~dp0)\\([^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i);
    if (match) { relativeScript = match[1]; break; }
  }
  if (!relativeScript) throw new Error(`cannot safely launch non-Node Windows command shim: ${executable}`);
  const script = path.resolve(path.dirname(executable), ...relativeScript.split(/[\\/]+/));
  if (!fs.statSync(script).isFile()) throw new Error(`Windows command shim target is missing: ${script}`);
  return { command: process.execPath, args: [script, ...args] };
}

function getSpawnOptions(platform = process.platform) {
  return {
    stdio: ['pipe', 'pipe', 'inherit'],
    windowsHide: true,
  };
}

module.exports = { getSpawnInvocation, getSpawnOptions, resolveWindowsCommand };
