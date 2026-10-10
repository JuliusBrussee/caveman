'use strict';

const fs = require('fs');
const path = require('path');

function envValue(env, name) {
  const key = Object.keys(env).find(candidate => candidate.toLowerCase() === name.toLowerCase());
  return key === undefined ? undefined : env[key];
}

function resolveWindowsCommand(command, env = process.env) {
  const hasPath = path.isAbsolute(command) || /[\\/]/.test(command);
  const pathExt = envValue(env, 'PATHEXT') || '.COM;.EXE;.BAT;.CMD';
  // Extensionless commands resolve only through PATHEXT, matching Windows
  // semantics. npm/pnpm .bin dirs place a non-executable Unix shim under the
  // bare name next to the real .CMD shim; probing the bare name first would
  // pick the Unix shim and fail to spawn.
  const names = path.extname(command)
    ? [command]
    : pathExt.split(';').map(extension =>
      `${command}${extension.startsWith('.') ? extension : `.${extension}`}`);
  // Absolute and relative paths follow the same PATHEXT rule as bare names.
  // An extensionless npm shim is still a POSIX script when supplied by path.
  const directories = hasPath ? ['.'] : (envValue(env, 'PATH') || '').split(';');
  for (const directory of directories) {
    if (!directory) continue;
    for (const name of names) {
      const candidate = hasPath ? name : path.join(directory.replace(/^"(.*)"$/, '$1'), name);
      try { if (fs.statSync(candidate).isFile()) return candidate; } catch (_) {}
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
    // %~dp0 already ends with a separator; managed Pi adds none before its target.
    const match = line.match(/"%(?:dp0%|~dp0)\\?([^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i)
      || line.match(/"([A-Za-z]:[\\/][^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i);
    if (match) return match[1];
  }
  // Node's own npx.cmd / npm.cmd do not inline the script. They set
  //   SET "NPX_CLI_JS=%~dp0\node_modules\npm\bin\npx-cli.js"
  // and launch `"%NODE_EXE%" "%NPX_CLI_JS%" %*` (NPM_CLI_JS for npm.cmd).
  // Only that pair is accepted — arbitrary variable expansion stays rejected.
  // ponytail: the stock shim also asks npm-prefix.js for a globally upgraded
  // npm (NPM_PREFIX_NPX_CLI_JS) and prefers that; we always take the copy
  // bundled with Node. Same `npx`, possibly an older npm. Mirror the prefix
  // lookup if a bundled-npx bug ever bites.
  const npm = source.match(/SET\s+"(NP[MX])_CLI_JS=%~dp0\\([^"\r\n]+\.js)"[\s\S]*"%NODE_EXE%"\s+"%\1_CLI_JS%"\s+%\*/i);
  return npm ? npm[2] : null;
}

// Accept only a single shim-relative forwarding command, optionally preceded by
// @echo off. Never evaluate batch syntax or interpolate caller-controlled argv.
function parseWindowsNestedShim(source) {
  const match = source.match(/^(?:@echo[ \t]+off[ \t]*\r?\n)?[ \t]*"%~dp0\\?([\w .@+\-\\/]+\.(?:cmd|bat))"[ \t]+%\*[ \t]*(?:\r?\n)?$/i);
  return match ? match[1] : null;
}

function resolveWindowsNodeShim(executable, depth = 0, seen = new Set()) {
  if (depth > 4) throw new Error('Windows command shim nesting too deep');
  const normalized = path.resolve(executable);
  if (seen.has(normalized.toLowerCase())) throw new Error('Windows command shim cycle');
  seen.add(normalized.toLowerCase());
  const stat = fs.statSync(normalized);
  if (!stat.isFile() || stat.size > 256 * 1024) {
    throw new Error(`cannot safely launch Windows command shim: ${normalized}`);
  }
  const source = fs.readFileSync(normalized, 'utf8');
  // Batch forwarding must match the whole wrapper, not a later Node command.
  const nested = parseWindowsNestedShim(source);
  const forwardsToBatch = /"[^"\r\n]+\.(?:cmd|bat)"[ \t]+%\*/i.test(source);
  const jsTarget = forwardsToBatch ? null : parseWindowsNodeShim(source);
  const child = jsTarget || nested;
  if (!child) throw new Error(`cannot safely launch non-Node Windows command shim: ${normalized}`);
  const target = /^[A-Za-z]:[\\/]/.test(child)
    ? child
    : path.resolve(path.dirname(normalized), ...child.split(/[\\/]+/));
  if (!fs.existsSync(target) || !fs.statSync(target).isFile()) {
    throw new Error(`Windows command shim target is missing: ${target}`);
  }
  return jsTarget ? target : resolveWindowsNodeShim(target, depth + 1, seen);
}

function portableInvocation(command, args, {
  platform = process.platform,
  env = process.env,
  execPath = process.execPath,
  allowBun = false,
} = {}) {
  if (platform !== 'win32') return { command, args: [...args] };
  const executable = resolveWindowsCommand(command, env) || command;
  if (!/\.(?:cmd|bat)$/i.test(executable)) return { command: executable, args: [...args] };
  const script = resolveWindowsNodeShim(executable);
  if (allowBun) {
    // OMP's npm shim wraps a Bun CLI. Keep argv out of cmd.exe and use the
    // declared runtime instead of evaluating Bun-specific code with Node.
    const fd = fs.openSync(script, 'r');
    const header = Buffer.alloc(128);
    let length;
    try { length = fs.readSync(fd, header, 0, header.length, 0); }
    finally { fs.closeSync(fd); }
    if (/^#!\/usr\/bin\/env bun(?:\r?\n|$)/.test(header.subarray(0, length).toString('utf8'))) {
      const bun = resolveWindowsCommand('bun', env);
      if (!bun || !/\.exe$/i.test(bun)) throw new Error('Bun CLI requires bun.exe on PATH');
      return { command: bun, args: [script, ...args] };
    }
  }
  return { command: execPath, args: [script, ...args] };
}

module.exports = { parseWindowsNodeShim, portableInvocation, resolveWindowsCommand };
