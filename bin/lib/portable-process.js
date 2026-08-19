'use strict';

const fs = require('fs');
const path = require('path');

function envValue(env, name) {
  const key = Object.keys(env).find(candidate => candidate.toLowerCase() === name.toLowerCase());
  return key === undefined ? undefined : env[key];
}

function resolveWindowsCommand(command, env = process.env) {
  if (path.isAbsolute(command) || /[\\/]/.test(command)) {
    return fs.existsSync(command) ? command : null;
  }
  const pathExt = envValue(env, 'PATHEXT') || '.COM;.EXE;.BAT;.CMD';
  // Extensionless commands resolve only through PATHEXT, matching Windows
  // semantics. npm/pnpm .bin dirs place a non-executable Unix shim under the
  // bare name next to the real .CMD shim; probing the bare name first would
  // pick the Unix shim and fail to spawn.
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

function parseWindowsNodeShim(source) {
  for (const line of source.split(/\r?\n/)) {
    if (!/(?:\bnode(?:\.exe)?\b|_prog)/i.test(line) || !/%\*/.test(line)) continue;
    // Shim-relative target (npm cmd-shim, pnpm/yarn-classic @zkochan forms), or
    // a drive-absolute target (pnpm emits one when the global bin dir and the
    // store sit on different drives — path.relative crosses drives as absolute).
    const match = line.match(/"%(?:dp0%|~dp0)\\([^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i)
      || line.match(/"([A-Za-z]:[\\/][^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i);
    if (match) return match[1];
  }
  return null;
}

function portableInvocation(command, args, {
  platform = process.platform,
  env = process.env,
  execPath = process.execPath,
} = {}) {
  if (platform !== 'win32') return { command, args: [...args] };
  const executable = resolveWindowsCommand(command, env) || command;
  if (!/\.(?:cmd|bat)$/i.test(executable)) return { command: executable, args: [...args] };
  const stat = fs.statSync(executable);
  if (!stat.isFile() || stat.size > 256 * 1024) {
    throw new Error(`cannot safely launch Windows command shim: ${executable}`);
  }
  const relativeScript = parseWindowsNodeShim(fs.readFileSync(executable, 'utf8'));
  if (!relativeScript) {
    throw new Error(`cannot safely launch non-Node Windows command shim: ${executable}`);
  }
  const script = /^[A-Za-z]:[\\/]/.test(relativeScript)
    ? relativeScript
    : path.resolve(path.dirname(executable), ...relativeScript.split(/[\\/]+/));
  if (!fs.statSync(script).isFile()) throw new Error(`Windows command shim target is missing: ${script}`);
  return { command: execPath, args: [script, ...args] };
}

module.exports = { parseWindowsNodeShim, portableInvocation, resolveWindowsCommand };
