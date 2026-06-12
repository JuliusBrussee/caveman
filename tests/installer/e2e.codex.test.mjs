// End-to-end: real Codex hook install against an isolated CODEX_HOME.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const INSTALLER = path.resolve(HERE, '..', '..', 'bin', 'install.js');

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-codex-install-'));
}

test('explicit codex install writes hook script and hooks.json without npx skills', () => {
  const cfg = freshTmpDir();
  try {
    const r = spawnSync(process.execPath, [INSTALLER,
      '--only', 'codex', '--skip-skills', '--non-interactive', '--no-mcp-shrink',
      '--config-dir', cfg,
    ], { encoding: 'utf8', env: { ...process.env, CODEX_HOME: cfg, NO_COLOR: '1' } });

    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /Codex CLI detected/);

    const hookScript = path.join(cfg, 'hooks', 'caveman-codex-hook.js');
    assert.ok(fs.existsSync(hookScript), 'caveman-codex-hook.js missing');

    const hooksJson = JSON.parse(fs.readFileSync(path.join(cfg, 'hooks.json'), 'utf8'));
    const sessionHooks = hooksJson.hooks.SessionStart.flatMap(entry => entry.hooks);
    const promptHooks = hooksJson.hooks.UserPromptSubmit.flatMap(entry => entry.hooks);
    assert.equal(sessionHooks.length, 1);
    assert.equal(promptHooks.length, 1);
    assert.match(sessionHooks[0].command, /caveman-codex-hook\.js" SessionStart$/);
    assert.match(promptHooks[0].command, /caveman-codex-hook\.js" UserPromptSubmit$/);
    assert.equal(sessionHooks[0].timeout, 5);
    assert.equal(promptHooks[0].timeout, 5);
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('explicit codex install is idempotent', () => {
  const cfg = freshTmpDir();
  try {
    for (let i = 0; i < 2; i++) {
      const r = spawnSync(process.execPath, [INSTALLER,
        '--only', 'codex', '--skip-skills', '--non-interactive', '--no-mcp-shrink',
        '--config-dir', cfg,
      ], { encoding: 'utf8', env: { ...process.env, CODEX_HOME: cfg, NO_COLOR: '1' } });
      assert.equal(r.status, 0, r.stderr);
    }

    const hooksJson = JSON.parse(fs.readFileSync(path.join(cfg, 'hooks.json'), 'utf8'));
    assert.equal(hooksJson.hooks.SessionStart.length, 1);
    assert.equal(hooksJson.hooks.UserPromptSubmit.length, 1);
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('codex install treats existing unquoted hooks as already wired', () => {
  const cfg = freshTmpDir();
  try {
    fs.writeFileSync(path.join(cfg, 'hooks.json'), JSON.stringify({
      hooks: {
        SessionStart: [{
          matcher: 'startup|resume|clear|compact',
          hooks: [{ type: 'command', command: 'node ./hooks/caveman-codex-hook.js SessionStart' }],
        }],
        UserPromptSubmit: [{
          hooks: [{ type: 'command', command: 'node ./hooks/caveman-codex-hook.js UserPromptSubmit' }],
        }],
      },
    }, null, 2));

    const r = spawnSync(process.execPath, [INSTALLER,
      '--only', 'codex', '--skip-skills', '--non-interactive', '--no-mcp-shrink',
      '--config-dir', cfg,
    ], { encoding: 'utf8', env: { ...process.env, CODEX_HOME: cfg, NO_COLOR: '1' } });
    assert.equal(r.status, 0, r.stderr);

    const hooksJson = JSON.parse(fs.readFileSync(path.join(cfg, 'hooks.json'), 'utf8'));
    const sessionHooks = hooksJson.hooks.SessionStart.flatMap(entry => entry.hooks);
    const promptHooks = hooksJson.hooks.UserPromptSubmit.flatMap(entry => entry.hooks);
    assert.equal(sessionHooks.length, 1);
    assert.equal(promptHooks.length, 1);
    assert.equal(sessionHooks[0].command, 'node ./hooks/caveman-codex-hook.js SessionStart');
    assert.equal(promptHooks[0].command, 'node ./hooks/caveman-codex-hook.js UserPromptSubmit');
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('codex uninstall removes native hook and preserves user hooks', () => {
  const cfg = freshTmpDir();
  try {
    const hooksDir = path.join(cfg, 'hooks');
    fs.mkdirSync(hooksDir, { recursive: true });
    fs.writeFileSync(path.join(hooksDir, 'caveman-codex-hook.js'), '// installed hook\n');
    fs.writeFileSync(path.join(cfg, 'hooks.json'), JSON.stringify({
      hooks: {
        SessionStart: [
          { hooks: [
            { type: 'command', command: 'echo user-owned-hook' },
            { type: 'command', command: `"${process.execPath}" "${path.join(hooksDir, 'caveman-codex-hook.js')}" SessionStart` },
          ] },
        ],
        UserPromptSubmit: [
          { hooks: [{ type: 'command', command: `"${process.execPath}" "${path.join(hooksDir, 'caveman-codex-hook.js')}" UserPromptSubmit` }] },
        ],
      },
    }, null, 2));

    const r = spawnSync(process.execPath, [INSTALLER,
      '--uninstall', '--non-interactive', '--no-mcp-shrink', '--config-dir', cfg,
    ], {
      encoding: 'utf8',
      env: {
        ...process.env,
        CODEX_HOME: cfg,
        CLAUDE_CONFIG_DIR: path.join(cfg, 'claude'),
        PATH: '',
        NO_COLOR: '1',
      },
    });
    assert.equal(r.status, 0, r.stderr);

    assert.equal(fs.existsSync(path.join(hooksDir, 'caveman-codex-hook.js')), false);
    assert.equal(
      fs.readFileSync(path.join(cfg, 'hooks.json.bak'), 'utf8'),
      JSON.stringify({
        hooks: {
          SessionStart: [
            { hooks: [
              { type: 'command', command: 'echo user-owned-hook' },
              { type: 'command', command: `"${process.execPath}" "${path.join(hooksDir, 'caveman-codex-hook.js')}" SessionStart` },
            ] },
          ],
          UserPromptSubmit: [
            { hooks: [{ type: 'command', command: `"${process.execPath}" "${path.join(hooksDir, 'caveman-codex-hook.js')}" UserPromptSubmit` }] },
          ],
        },
      }, null, 2)
    );
    const hooksJson = JSON.parse(fs.readFileSync(path.join(cfg, 'hooks.json'), 'utf8'));
    const sessionCommands = hooksJson.hooks.SessionStart.flatMap(entry => entry.hooks).map(hook => hook.command);
    assert.deepEqual(sessionCommands, ['echo user-owned-hook']);
    assert.equal(hooksJson.hooks.UserPromptSubmit, undefined);
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});

test('codex uninstall leaves user-only hooks.json byte-for-byte unchanged', () => {
  const cfg = freshTmpDir();
  try {
    const before = `{
  // User-owned Codex hook config.
  "hooks": {
    "SessionStart": [
      { "hooks": [{ "type": "command", "command": "echo user-owned-hook" }] },
    ],
  },
}
`;
    fs.writeFileSync(path.join(cfg, 'hooks.json'), before);

    const r = spawnSync(process.execPath, [INSTALLER,
      '--uninstall', '--non-interactive', '--no-mcp-shrink', '--config-dir', cfg,
    ], {
      encoding: 'utf8',
      env: {
        ...process.env,
        CODEX_HOME: cfg,
        CLAUDE_CONFIG_DIR: path.join(cfg, 'claude'),
        PATH: '',
        NO_COLOR: '1',
      },
    });
    assert.equal(r.status, 0, r.stderr);
    assert.equal(fs.readFileSync(path.join(cfg, 'hooks.json'), 'utf8'), before);
    assert.equal(fs.existsSync(path.join(cfg, 'hooks.json.bak')), false);
  } finally {
    fs.rmSync(cfg, { recursive: true, force: true });
  }
});
