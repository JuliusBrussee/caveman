import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const { parseCommandArgs } = require('../../bin/lib/command-args.js');
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

test('MCP command parsing preserves Windows paths and shell metacharacters', () => {
  assert.deepEqual(parseCommandArgs(String.raw`"C:\Program Files\nodejs\node.exe" "C:\MCP servers\server.js" "C:\data folder\" "%USERPROFILE%" '$HOME' ""`), [
    'C:\\Program Files\\nodejs\\node.exe', 'C:\\MCP servers\\server.js', 'C:\\data folder\\', '%USERPROFILE%', '$HOME', '',
  ]);
  assert.deepEqual(parseCommandArgs('npx -y @modelcontextprotocol/server-filesystem /tmp'), [
    'npx', '-y', '@modelcontextprotocol/server-filesystem', '/tmp',
  ]);
  const args = ['node', 'server.js', "both ' and \"", '', 'x&y'];
  assert.deepEqual(parseCommandArgs(JSON.stringify(args)), args);
});

test('MCP command parsing rejects malformed argv before any installation', () => {
  assert.throws(() => parseCommandArgs('node \0'), /NUL/);
  for (const value of ['', '"" arg', '[]', '[1]', '["node",null]', '["node"', 'node "unfinished', JSON.stringify(['node', '\0'])]) {
    assert.throws(() => parseCommandArgs(value), /upstream command/);
    const result = spawnSync(process.execPath, [path.join(root, 'bin/install.js'), '--with-mcp-shrink', value, '--dry-run'], { encoding: 'utf8' });
    assert.equal(result.status, 2, value);
    assert.equal(result.stdout, '', 'invalid command must fail before provider work');
  }
});

for (const form of ['quoted', 'json']) {
  test(`opencode receives exact upstream argv from ${form} installer value`, (t) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman mcp argv '));
    t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
    const args = ['C:\\Program Files\\nodejs\\node.exe', 'C:\\MCP servers\\server.js', 'C:\\data folder\\', '%USERPROFILE%', '$HOME', ''];
    const value = form === 'json' ? JSON.stringify(args) : args.map(arg => `"${arg}"`).join(' ');
    const result = spawnSync(process.execPath, [path.join(root, 'bin/install.js'), '--only', 'opencode', `--with-mcp-shrink=${value}`, '--non-interactive'], {
      encoding: 'utf8', cwd: dir,
      env: { ...process.env, HOME: dir, USERPROFILE: dir, XDG_CONFIG_HOME: dir },
    });
    assert.equal(result.status, 0, result.stdout + result.stderr);
    const config = JSON.parse(fs.readFileSync(path.join(dir, 'opencode', 'opencode.jsonc'), 'utf8'));
    assert.deepEqual(config.mcp['caveman-shrink'].command, ['npx', '-y', 'caveman-shrink', ...args]);
  });
}

for (const shell of ['powershell.exe', 'pwsh.exe']) {
  test(`${shell} preserves quoted MCP paths through the actual Windows argument boundary`, { skip: process.platform !== 'win32' }, (t) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman powershell argv '));
    t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
    const psQuote = (value) => `'${value.replace(/'/g, "''")}'`;
    // Double quotes belong to PowerShell; the inner single quotes reach our
    // argv parser unchanged even with Windows PowerShell 5.1's native quoting.
    const command = `& ${psQuote(process.execPath)} ${psQuote(path.join(root, 'bin/install.js'))} --only opencode --non-interactive --with-mcp-shrink "'C:\\Program Files\\nodejs\\node.exe' 'C:\\MCP servers\\server.js' 'C:\\data folder\\'"; exit $LASTEXITCODE`;
    const result = spawnSync(shell, ['-NoProfile', '-NonInteractive', '-Command', command], {
      encoding: 'utf8', cwd: dir,
      env: { ...process.env, HOME: dir, USERPROFILE: dir, XDG_CONFIG_HOME: dir },
    });
    assert.equal(result.status, 0, result.stdout + result.stderr + (result.error ?? ''));
    const config = JSON.parse(fs.readFileSync(path.join(dir, 'opencode', 'opencode.jsonc'), 'utf8'));
    assert.deepEqual(config.mcp['caveman-shrink'].command, [
      'npx', '-y', 'caveman-shrink', 'C:\\Program Files\\nodejs\\node.exe', 'C:\\MCP servers\\server.js', 'C:\\data folder\\',
    ]);
  });
}
