// Unit tests for bin/lib/settings.js — the JSONC-tolerant settings helper.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const SETTINGS = require('../../bin/lib/settings.js');

function tmpFile(name, contents) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-settings-'));
  const p = path.join(dir, name);
  fs.writeFileSync(p, contents);
  return p;
}

test('stripJsonComments strips // line comments', () => {
  const out = SETTINGS.stripJsonComments('{"a":1}// trail');
  assert.equal(out.trim(), '{"a":1}');
});

test('stripJsonComments strips /* block */ comments', () => {
  const out = SETTINGS.stripJsonComments('{/* leading */"a":1/* mid */, "b":2}');
  assert.match(out, /"a":1/);
  assert.match(out, /"b":2/);
  assert.doesNotMatch(out, /leading/);
});

test('stripJsonComments leaves comment-looking sequences inside strings alone', () => {
  const out = SETTINGS.stripJsonComments('{"url":"http://example.com//path"}');
  assert.equal(out, '{"url":"http://example.com//path"}');
});

test('stripJsonComments strips trailing commas', () => {
  const out = SETTINGS.stripJsonComments('{"a":[1,2,3,],}');
  assert.doesNotThrow(() => JSON.parse(out));
});

test('readSettings handles plain JSON', () => {
  const p = tmpFile('s.json', '{"theme":"dark"}');
  assert.deepEqual(SETTINGS.readSettings(p), { theme: 'dark' });
});

test('readSettings handles JSONC (comments + trailing commas)', () => {
  const p = tmpFile('s.json', `// my settings
{
  "theme": "dark", /* mode */
  "hooks": {},
}`);
  assert.deepEqual(SETTINGS.readSettings(p), { theme: 'dark', hooks: {} });
});

test('readSettings returns {} for missing file', () => {
  assert.deepEqual(SETTINGS.readSettings('/nonexistent/path/xyz.json'), {});
});

test('readSettings returns null for unrecoverable garbage', () => {
  const p = tmpFile('s.json', 'this is not json at all {{{');
  assert.equal(SETTINGS.readSettings(p), null);
});

test('writeSettings round-trips with newline', () => {
  const p = tmpFile('s.json', '');
  SETTINGS.writeSettings(p, { a: 1 });
  const raw = fs.readFileSync(p, 'utf8');
  assert.equal(raw.endsWith('\n'), true);
  assert.deepEqual(JSON.parse(raw), { a: 1 });
});

test('validateHookFields drops malformed command hook (missing command)', () => {
  const s = {
    hooks: {
      SessionStart: [{ hooks: [{ type: 'command' }, { type: 'command', command: 'good' }] }],
    },
  };
  SETTINGS.validateHookFields(s);
  assert.equal(s.hooks.SessionStart[0].hooks.length, 1);
  assert.equal(s.hooks.SessionStart[0].hooks[0].command, 'good');
});

test('validateHookFields drops malformed agent hook (missing prompt)', () => {
  const s = {
    hooks: {
      SessionStart: [{ hooks: [{ type: 'agent' }] }],
    },
  };
  SETTINGS.validateHookFields(s);
  assert.equal(s.hooks, undefined);
});

test('validateHookFields drops empty events and empty hooks parent', () => {
  const s = { hooks: { SessionStart: [], UserPromptSubmit: [{ hooks: [] }] } };
  SETTINGS.validateHookFields(s);
  assert.equal(s.hooks, undefined);
});

test('addCommandHook is idempotent on substring marker', () => {
  const s = {};
  const a = SETTINGS.addCommandHook(s, 'SessionStart', { command: '/abs/path/caveman-activate.js', marker: 'caveman-activate' });
  const b = SETTINGS.addCommandHook(s, 'SessionStart', { command: '/different/abs/path/caveman-activate.js', marker: 'caveman-activate' });
  assert.equal(a, true);
  assert.equal(b, false);
  assert.equal(s.hooks.SessionStart.length, 1);
});

test('hasCavemanHook detects via substring', () => {
  const s = { hooks: { SessionStart: [{ hooks: [{ type: 'command', command: 'node /x/caveman-activate.js' }] }] } };
  assert.equal(SETTINGS.hasCavemanHook(s, 'SessionStart', 'caveman-activate'), true);
  assert.equal(SETTINGS.hasCavemanHook(s, 'SessionStart', 'gsd'), false);
  assert.equal(SETTINGS.hasCavemanHook(s, 'UserPromptSubmit'), false);
});

test('removeCavemanHooks strips by marker and cleans empties', () => {
  const s = {
    hooks: {
      SessionStart: [
        { hooks: [{ type: 'command', command: 'caveman-x' }] },
        { hooks: [{ type: 'command', command: 'other' }] },
      ],
      UserPromptSubmit: [{ hooks: [{ type: 'command', command: 'caveman-y' }] }],
    },
  };
  const removed = SETTINGS.removeCavemanHooks(s, 'caveman');
  assert.equal(removed, 2);
  assert.equal(s.hooks.SessionStart.length, 1);
  assert.equal(s.hooks.UserPromptSubmit, undefined);
});

test('rewriteLegacyManagedHookCommands rewrites bare-node managed scripts', () => {
  const s = {
    hooks: {
      SessionStart: [{ hooks: [
        { type: 'command', command: 'node /abs/hooks/caveman-activate.js' },
        { type: 'command', command: 'node /abs/hooks/some-user-hook.js' },
      ] }],
    },
  };
  const n = SETTINGS.rewriteLegacyManagedHookCommands(s, '/usr/local/bin/node');
  assert.equal(n, 1);
  assert.match(s.hooks.SessionStart[0].hooks[0].command, /"\/usr\/local\/bin\/node" "\/abs\/hooks\/caveman-activate\.js"/);
  assert.equal(s.hooks.SessionStart[0].hooks[1].command, 'node /abs/hooks/some-user-hook.js');
});

test('rewriteLegacyManagedHookCommands ignores already-absolute node commands', () => {
  const s = {
    hooks: {
      SessionStart: [{ hooks: [
        { type: 'command', command: '"/usr/local/bin/node" "/abs/hooks/caveman-activate.js"' },
      ] }],
    },
  };
  const n = SETTINGS.rewriteLegacyManagedHookCommands(s, '/somewhere/else/node');
  assert.equal(n, 0);
});

test('claudeConfigDir honors CLAUDE_CONFIG_DIR env', () => {
  const orig = process.env.CLAUDE_CONFIG_DIR;
  process.env.CLAUDE_CONFIG_DIR = '/tmp/__cm_test_cfg';
  try { assert.equal(SETTINGS.claudeConfigDir(), '/tmp/__cm_test_cfg'); }
  finally { if (orig === undefined) delete process.env.CLAUDE_CONFIG_DIR; else process.env.CLAUDE_CONFIG_DIR = orig; }
});
