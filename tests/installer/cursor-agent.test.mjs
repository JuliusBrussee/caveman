// Cursor subagent install. Cursor reads <home>/.cursor/agents/ from the IDE,
// the Agents Window, and the CLI. The same relative path is joined with
// os.homedir(), which is $HOME on macOS and Linux and %USERPROFILE% on Windows.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const requireCjs = createRequire(import.meta.url);
const cursor = requireCjs(path.join(REPO_ROOT, 'bin', 'lib', 'cursor-agent.js'));
const cursorNative = requireCjs(path.join(REPO_ROOT, 'bin', 'lib', 'cursor-native.js'));
const cursorMcp = requireCjs(path.join(REPO_ROOT, 'bin', 'lib', 'cursor-mcp-json.js'));

function frontmatter(content) {
  const match = content.match(/^---\n([\s\S]*?)\n---\n/);
  assert.ok(match, 'frontmatter present');
  return match[1];
}

test('cursor plugin manifest and hook files exist', () => {
  const plugin = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, '.cursor-plugin', 'plugin.json'), 'utf8'));
  assert.equal(plugin.name, 'caveman');
  assert.equal(plugin.hooks, './hooks/hooks-cursor.json');
  const hooks = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, 'hooks', 'hooks-cursor.json'), 'utf8'));
  assert.equal(hooks.version, 1);
  assert.ok(Array.isArray(hooks.hooks.sessionStart));
  const hookScript = path.join(REPO_ROOT, 'src', 'hooks', 'caveman-session-start-cursor.js');
  assert.ok(fs.existsSync(hookScript));
  assert.ok(fs.existsSync(path.join(REPO_ROOT, 'skills', 'caveman', 'SKILL.md')));
});

test('cursor plugin sessionStart hook emits additional_context under CURSOR_PLUGIN_ROOT', () => {
  const hookScript = path.join(REPO_ROOT, 'src', 'hooks', 'caveman-session-start-cursor.js');
  const result = spawnSync(process.execPath, [hookScript], {
    env: { ...process.env, CURSOR_PLUGIN_ROOT: REPO_ROOT },
    encoding: 'utf8',
    input: '{}',
  });
  assert.equal(result.status, 0);
  const payload = JSON.parse(result.stdout);
  assert.ok(typeof payload.additional_context === 'string');
  assert.match(payload.additional_context, /Respond terse like smart caveman/);
  assert.match(payload.additional_context, /## Persistence/);
});

test('cursor plugin sessionStart hook stays empty without CURSOR_PLUGIN_ROOT', () => {
  const hookScript = path.join(REPO_ROOT, 'src', 'hooks', 'caveman-session-start-cursor.js');
  const env = { ...process.env };
  delete env.CURSOR_PLUGIN_ROOT;
  const result = spawnSync(process.execPath, [hookScript], {
    env,
    encoding: 'utf8',
    input: '{}',
  });
  assert.equal(result.status, 0);
  assert.deepEqual(JSON.parse(result.stdout), {});
});

test('config dir is <home>/.cursor on posix and win32', () => {
  assert.equal(cursor.cursorConfigDir('/Users/ada', path.posix), '/Users/ada/.cursor');
  assert.equal(cursor.cursorConfigDir('/home/ada', path.posix), '/home/ada/.cursor');
  assert.equal(cursor.cursorConfigDir('C:\\Users\\ada', path.win32), 'C:\\Users\\ada\\.cursor');
});

test('rewrites Claude model aliases and strips tools', () => {
  const src = `---
name: cavecrew-investigator
description: locate code
tools: [Read, Grep]
model: haiku
---
body
`;
  const out = cursor.transformCursorAgentFrontmatter(src, { readonly: true });
  const fm = frontmatter(out);
  assert.doesNotMatch(fm, /^tools:/m);
  assert.match(fm, /^model: inherit$/m);
  assert.match(fm, /^readonly: true$/m);
  assert.match(out, /^body$/m);
});

test('keeps model: inherit and does not mark the builder readonly', () => {
  const src = `---
name: cavecrew-builder
description: edit
model: inherit
---
body
`;
  const out = cursor.transformCursorAgentFrontmatter(src, { readonly: false });
  const fm = frontmatter(out);
  assert.match(fm, /^model: inherit$/m);
  assert.doesNotMatch(fm, /^readonly:/m);
});

test('install and uninstall write only the temp home', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-agents-'));
  const notes = [];
  try {
    const dry = cursor.installCursorAgents({
      repoRoot: REPO_ROOT,
      home,
      dryRun: true,
      note: (line) => notes.push(line),
    });
    assert.equal(dry.dryRun, true);
    assert.equal(fs.existsSync(path.join(home, '.cursor', 'agents')), false);

    const installed = cursor.installCursorAgents({
      repoRoot: REPO_ROOT,
      home,
      note: () => {},
    });
    assert.equal(installed.copied, true);
    assert.equal(installed.count, 3);

    const investigator = fs.readFileSync(path.join(home, '.cursor', 'agents', 'cavecrew-investigator.md'), 'utf8');
    const builder = fs.readFileSync(path.join(home, '.cursor', 'agents', 'cavecrew-builder.md'), 'utf8');
    const reviewer = fs.readFileSync(path.join(home, '.cursor', 'agents', 'cavecrew-reviewer.md'), 'utf8');
    assert.match(frontmatter(investigator), /^readonly: true$/m);
    assert.match(frontmatter(investigator), /^model: inherit$/m);
    assert.doesNotMatch(frontmatter(investigator), /^model: haiku$/m);
    assert.doesNotMatch(frontmatter(builder), /^readonly:/m);
    assert.match(frontmatter(reviewer), /^readonly: true$/m);

    const removed = cursor.uninstallCursorAgents({ home, note: () => {}, warn: () => {} });
    assert.equal(removed.hadJournal, true);
    assert.equal(fs.existsSync(path.join(home, '.cursor', 'agents', 'cavecrew-builder.md')), false);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('refuses to overwrite a user-owned agent file', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-conflict-'));
  const target = path.join(home, '.cursor', 'agents', 'cavecrew-builder.md');
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(target, '---\nname: mine\n---\nuser\n');
  try {
    assert.throws(
      () => cursor.installCursorAgents({ repoRoot: REPO_ROOT, home, note: () => {} }),
      /ownership conflict/,
    );
    assert.match(fs.readFileSync(target, 'utf8'), /user/);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('native install writes user rule, hook, and merges foreign mcp/hooks', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-native-'));
  const root = path.join(home, '.cursor');
  fs.mkdirSync(root, { recursive: true });
  fs.writeFileSync(
    path.join(root, 'mcp.json'),
    JSON.stringify({ mcpServers: { other: { command: 'echo', args: ['hi'] } } }, null, 2) + '\n',
  );
  fs.writeFileSync(
    path.join(root, 'hooks.json'),
    JSON.stringify({ version: 1, hooks: { afterFileEdit: [{ command: '.cursor/hooks/user.sh' }] } }, null, 2) + '\n',
  );
  try {
    cursorNative.installCursorNative({
      repoRoot: REPO_ROOT,
      home,
      note: () => {},
      warn: () => {},
    });

    const rule = fs.readFileSync(path.join(root, 'rules', 'caveman.mdc'), 'utf8');
    assert.match(rule, /^---\n/);
    assert.match(rule, /alwaysApply: true/);
    assert.match(rule, /Respond terse like smart caveman/);
    assert.match(rule, /## Persistence/);

    const hookScript = path.join(root, 'hooks', 'caveman-session-start.js');
    assert.ok(fs.existsSync(hookScript));
    assert.match(fs.readFileSync(hookScript, 'utf8'), /additional_context/);

    const hooks = JSON.parse(fs.readFileSync(path.join(root, 'hooks.json'), 'utf8'));
    assert.equal(hooks.version, 1);
    assert.ok(hooks.hooks.sessionStart.some((e) => e.command.includes('caveman-session-start')));
    assert.ok(hooks.hooks.afterFileEdit.some((e) => e.command.includes('user.sh')));

    const mcp = JSON.parse(fs.readFileSync(path.join(root, 'mcp.json'), 'utf8'));
    assert.deepEqual(mcp.mcpServers.other, { command: 'echo', args: ['hi'] });
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('refuses to overwrite a foreign mcpServers.caveman entry', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-mcp-foreign-'));
  const root = path.join(home, '.cursor');
  fs.mkdirSync(root, { recursive: true });
  fs.writeFileSync(
    path.join(root, 'mcp.json'),
    JSON.stringify({ mcpServers: { caveman: { command: '/other/mcp', args: [] } } }, null, 2) + '\n',
  );
  const warnings = [];
  try {
    const ok = cursorMcp.mergeMcpServer(
      root,
      home,
      'caveman',
      { command: '/bin/caveman-mcp', args: [] },
      false,
      (line) => warnings.push(line),
    );
    assert.equal(ok, false);
    const mcp = JSON.parse(fs.readFileSync(path.join(root, 'mcp.json'), 'utf8'));
    assert.deepEqual(mcp.mcpServers.caveman, { command: '/other/mcp', args: [] });
    assert.ok(warnings.some((line) => line.includes('not Caveman-owned')));
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('owned caveman MCP install and uninstall round-trip', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-mcp-owned-'));
  const root = path.join(home, '.cursor');
  const mcp = { command: '/bin/caveman-mcp', args: [] };
  try {
    assert.equal(cursorMcp.installCursorMcpJson(mcp, { home }), true);
    const installed = JSON.parse(fs.readFileSync(path.join(root, 'mcp.json'), 'utf8'));
    assert.deepEqual(installed.mcpServers.caveman, mcp);
    cursorMcp.uninstallCursorMcpJson('caveman', { home });
    assert.equal(fs.existsSync(path.join(root, 'mcp.json')), false);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('missing caveman-mcp skips MCP and still installs the rule', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-missing-mcp-'));
  const savedPath = process.env.PATH;
  const savedBin = process.env.CAVEMAN_MCP_BIN;
  const notes = [];
  try {
    process.env.PATH = '';
    delete process.env.CAVEMAN_MCP_BIN;
    cursorNative.installCursorNative({
      repoRoot: REPO_ROOT,
      home,
      note: (line) => notes.push(line),
      warn: () => {},
    });
    assert.equal(fs.existsSync(path.join(home, '.cursor', 'rules', 'caveman.mdc')), true);
    assert.equal(fs.existsSync(path.join(home, '.cursor', 'mcp.json')), false);
    assert.ok(notes.some((line) => line.includes('caveman-mcp not found')));
  } finally {
    process.env.PATH = savedPath;
    if (savedBin === undefined) delete process.env.CAVEMAN_MCP_BIN;
    else process.env.CAVEMAN_MCP_BIN = savedBin;
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('native uninstall preserves foreign mcp and hook entries', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-native-uninstall-'));
  try {
    cursorNative.installCursorNative({ repoRoot: REPO_ROOT, home, note: () => {}, warn: () => {} });
    fs.writeFileSync(
      path.join(home, '.cursor', 'mcp.json'),
      JSON.stringify({
        mcpServers: {
          other: { command: 'echo', args: ['stay'] },
          caveman: { command: '/bin/caveman-mcp', args: [] },
        },
      }, null, 2) + '\n',
    );
    fs.mkdirSync(path.join(cavemanHome(home), 'mcp'), { recursive: true });
    fs.writeFileSync(
      path.join(cavemanHome(home), 'mcp', 'cursor.json'),
      JSON.stringify({ tool: 'caveman_retrieve', command: '/bin/caveman-mcp', args: [] }, null, 2) + '\n',
    );

    cursorNative.uninstallCursorNative({ home, note: () => {}, warn: () => {} });
    cursor.uninstallCursorAgents({ home, note: () => {}, warn: () => {} });

    const mcp = JSON.parse(fs.readFileSync(path.join(home, '.cursor', 'mcp.json'), 'utf8'));
    assert.deepEqual(mcp.mcpServers.other, { command: 'echo', args: ['stay'] });
    assert.equal(mcp.mcpServers.caveman, undefined);
    assert.equal(fs.existsSync(path.join(home, '.cursor', 'rules', 'caveman.mdc')), false);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

function cavemanHome(home) {
  return path.join(home, '.caveman');
}
