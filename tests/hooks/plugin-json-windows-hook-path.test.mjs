// Regression test for issue #199: on Windows with Git Bash as the shell,
// CLAUDE_PLUGIN_ROOT is set to an MSYS-style path (e.g. /c/Users/...), but
// the hook commands in .claude-plugin/plugin.json pass it straight to
// node.exe, which cannot resolve that format and throws MODULE_NOT_FOUND.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(__dirname, '..', '..');
const PLUGIN_JSON = path.join(REPO_ROOT, '.claude-plugin', 'plugin.json');

function hookCommand(hookName) {
  const plugin = JSON.parse(fs.readFileSync(PLUGIN_JSON, 'utf8'));
  return plugin.hooks[hookName][0].hooks[0].command;
}

// The hook command is a shell snippet, and the shell that runs it is not ours
// to choose. Every case below therefore runs under `sh` as well as `bash`: on
// Debian and Ubuntu /bin/sh is dash, where a bashism is not a portability nit
// but a syntax error that takes the whole hook down — SessionStart and
// UserPromptSubmit both, for every user on that platform, to fix a path format
// only Windows produces. A bash-only harness cannot see that failure, which is
// exactly how the herestring this replaced got here.
const SHELLS = ['sh', 'bash'];

// Runs a hook's shell command with a fake `node` on PATH that just echoes
// the path argument it was given, so we can see exactly what path the real
// node.exe would receive without needing a Windows host to run this on.
function resolvedNodeArg(shell, command, claudePluginRoot) {
  const binDir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-fakebin-'));
  const fakeNode = path.join(binDir, 'node');
  fs.writeFileSync(fakeNode, '#!/bin/sh\nprintf %s "$1"\n', { mode: 0o755 });
  try {
    const result = spawnSync(shell, ['-c', command], {
      env: { ...process.env, PATH: `${binDir}:${process.env.PATH}`, CLAUDE_PLUGIN_ROOT: claudePluginRoot },
      encoding: 'utf8',
    });
    assert.equal(result.status, 0, `${shell} rejected the hook command: ${result.stderr}`);
    return result.stdout;
  } finally {
    fs.rmSync(binDir, { recursive: true, force: true });
  }
}

for (const hookName of ['SessionStart', 'UserPromptSubmit']) {
  for (const shell of SHELLS) {
    test(`${hookName} hook converts an MSYS-style CLAUDE_PLUGIN_ROOT to a Windows drive path under ${shell}`, () => {
      const command = hookCommand(hookName);
      const arg = resolvedNodeArg(shell, command, '/c/Users/testuser/.claude/plugins/marketplaces/caveman');
      assert.ok(
        arg.startsWith('c:/Users/testuser/'),
        `expected node's path argument to start with a Windows drive path, got: ${arg}`,
      );
      assert.ok(!arg.startsWith('/c/'), `node argument still looks like an MSYS path: ${arg}`);
    });

    test(`${hookName} hook leaves an ordinary POSIX CLAUDE_PLUGIN_ROOT unchanged under ${shell}`, () => {
      const command = hookCommand(hookName);
      const posixRoot = '/home/testuser/.claude/plugins/marketplaces/caveman';
      const arg = resolvedNodeArg(shell, command, posixRoot);
      assert.ok(arg.startsWith(posixRoot), `expected the POSIX root to pass through untouched, got: ${arg}`);
    });

    // A path with spaces is the case an unquoted expansion silently truncates,
    // and Windows profile directories routinely have them.
    test(`${hookName} hook keeps a CLAUDE_PLUGIN_ROOT containing spaces intact under ${shell}`, () => {
      const command = hookCommand(hookName);
      const arg = resolvedNodeArg(shell, command, '/c/Users/Test User/My Plugins/caveman');
      assert.ok(
        arg.startsWith('c:/Users/Test User/My Plugins/caveman/'),
        `expected the spaces to survive as one argument, got: ${arg}`,
      );
    });
  }
}
