'use strict';

// A single runner for both direct installs and detached native-skill staging.
// Bun's --bun must precede the package so Node shebangs do not require Node.
function packageInvocation(args, { bun = process.versions.bun, execPath = process.execPath } = {}) {
  return bun
    ? { command: execPath, args: ['x', '--bun', ...args] }
    : { command: 'npx', args: ['-y', ...args] };
}

module.exports = { packageInvocation };
