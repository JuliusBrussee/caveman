'use strict';

// Owned payload for src/hooks/caveman-host-session-start.js, the sessionStart
// hook Cursor and GitHub Copilot CLI run. Installed under <host home>/caveman/:
//
//   hooks/caveman-host-session-start.js
//   hooks/caveman-config.js      the only module the hook requires
//   hooks/package.json           {"type":"commonjs"} — survives an ESM ancestor
//   skills/<mode>/SKILL.md       loadRuleset() resolves hooks/../skills
//
// A copy rather than a path into the npx cache, which is gone after install.

const fs = require('fs');
const path = require('path');

const PAYLOAD_DIR = 'caveman';
const HOOK_SCRIPT = 'caveman-host-session-start.js';
const HOOK_FILES = [HOOK_SCRIPT, 'caveman-config.js', 'package.json'];
const RULESET_SKILLS = ['caveman', 'ultracave', 'megacave'];

// One owned directory, so uninstall removes it whole and the digest covers
// every file. Callers attach `register` to wire the host's hook config.
function payloadOperation(repoRoot) {
  return {
    relativePath: PAYLOAD_DIR,
    write: (stage) => {
      const copy = (source, ...target) => {
        fs.mkdirSync(path.join(stage, ...target.slice(0, -1)), { recursive: true });
        fs.copyFileSync(source, path.join(stage, ...target));
      };
      for (const name of HOOK_FILES) copy(path.join(repoRoot, 'src', 'hooks', name), 'hooks', name);
      for (const id of RULESET_SKILLS) copy(path.join(repoRoot, 'skills', id, 'SKILL.md'), 'skills', id, 'SKILL.md');
    },
  };
}

function hookScriptPath(root) {
  return path.join(root, PAYLOAD_DIR, 'hooks', HOOK_SCRIPT);
}

// `node` is the absolute path on POSIX: GUI hosts on macOS start without the
// shell PATH (same reason the Claude hooks bake it, #805). Windows GUI apps
// inherit the user PATH, and a quoted leading path is a string, not a command,
// in PowerShell — so Windows keeps bare `node` and quotes only the script.
function hookCommand(root, host, node, platform = process.platform) {
  const script = hookScriptPath(root);
  if (platform === 'win32') return `node "${script}" ${host}`;
  const sq = (value) => `'${String(value).replace(/'/g, `'\\''`)}'`;
  return `${sq(node)} ${sq(script)} ${host}`;
}

module.exports = { PAYLOAD_DIR, payloadOperation, hookScriptPath, hookCommand };
