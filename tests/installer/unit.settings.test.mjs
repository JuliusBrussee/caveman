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

test('removeCavemanHooks tolerates malformed hook event values without throwing', () => {
  // Pre-fix bug: settings.hooks.SessionStart = "oops" (string, not array)
  // would crash on .filter(...) inside the filter loop. Fix delegates to
  // validateHookFields first + adds Array.isArray guard.
  const s = { hooks: { SessionStart: "oops", UserPromptSubmit: { not: 'an array either' } } };
  let removed;
  assert.doesNotThrow(() => { removed = SETTINGS.removeCavemanHooks(s, 'caveman'); });
  assert.equal(removed, 0);
  assert.equal(s.hooks, undefined);
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

test('rewriteLegacyManagedHookCommands rewrites stale absolute-node managed scripts', () => {
  // Old Homebrew/nvm installs wrote an absolute node path. After Node upgrade,
  // that path is stale and should be updated to the current node binary.
  const s = {
    hooks: {
      SessionStart: [{ hooks: [
        { type: 'command', command: '"/usr/local/bin/node" "/abs/hooks/caveman-activate.js"' },
      ] }],
    },
  };
  const n = SETTINGS.rewriteLegacyManagedHookCommands(s, '/somewhere/else/node');
  assert.equal(n, 1);
  assert.match(
    s.hooks.SessionStart[0].hooks[0].command,
    /"\/somewhere\/else\/node" "\/abs\/hooks\/caveman-activate\.js"/
  );
});

test('rewriteLegacyManagedHookCommands skips commands already using target node', () => {
  const s = {
    hooks: {
      SessionStart: [{ hooks: [
        { type: 'command', command: '"/usr/local/bin/node" "/abs/hooks/caveman-activate.js"' },
      ] }],
    },
  };
  const n = SETTINGS.rewriteLegacyManagedHookCommands(s, '/usr/local/bin/node');
  assert.equal(n, 0);
});

test('rewriteLegacyManagedHookCommands rewrites unquoted absolute-node managed scripts', () => {
  const s = {
    hooks: {
      SessionStart: [{ hooks: [
        { type: 'command', command: '/opt/homebrew/bin/node /abs/hooks/caveman-mode-tracker.js' },
      ] }],
    },
  };
  const n = SETTINGS.rewriteLegacyManagedHookCommands(s, '/usr/local/bin/node');
  assert.equal(n, 1);
  assert.match(
    s.hooks.SessionStart[0].hooks[0].command,
    /"\/usr\/local\/bin\/node" "\/abs\/hooks\/caveman-mode-tracker\.js"/
  );
});

test('claudeConfigDir honors CLAUDE_CONFIG_DIR env', () => {
  const orig = process.env.CLAUDE_CONFIG_DIR;
  process.env.CLAUDE_CONFIG_DIR = '/tmp/__cm_test_cfg';
  try { assert.equal(SETTINGS.claudeConfigDir(), '/tmp/__cm_test_cfg'); }
  finally { if (orig === undefined) delete process.env.CLAUDE_CONFIG_DIR; else process.env.CLAUDE_CONFIG_DIR = orig; }
});

// ── pruneDeadHookFiles ────────────────────────────────────────────────────

function makeTmpHookFile(dir, basename) {
  const p = path.join(dir, basename);
  fs.writeFileSync(p, '// hook');
  return p;
}

test('pruneDeadHookFiles removes entries whose managed scripts are missing', () => {
  // Simulates post-migration state: settings.json still references hook files
  // that no longer exist after switching from standalone to plugin install.
  const s = {
    hooks: {
      SessionStart: [{
        hooks: [{ type: 'command', command: '"/usr/local/bin/node" "/nonexistent/caveman-activate.js"' }],
      }],
    },
  };
  const removed = SETTINGS.pruneDeadHookFiles(s);
  assert.equal(removed, 1);
  assert.equal(s.hooks, undefined);
});

test('pruneDeadHookFiles keeps entries whose managed scripts exist on disk', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-prune-'));
  const activatePath = makeTmpHookFile(dir, 'caveman-activate.js');
  const s = {
    hooks: {
      SessionStart: [{
        hooks: [{ type: 'command', command: `"/usr/local/bin/node" "${activatePath}"` }],
      }],
    },
  };
  const removed = SETTINGS.pruneDeadHookFiles(s);
  assert.equal(removed, 0);
  assert.equal(s.hooks.SessionStart.length, 1);
  fs.rmSync(dir, { recursive: true, force: true });
});

test('pruneDeadHookFiles does not touch user-authored hooks even if their files are missing', () => {
  // A hook pointing to a non-existent non-managed script should be left alone.
  const s = {
    hooks: {
      SessionStart: [{
        hooks: [{ type: 'command', command: '"/usr/local/bin/node" "/nonexistent/my-custom-hook.js"' }],
      }],
    },
  };
  const removed = SETTINGS.pruneDeadHookFiles(s);
  assert.equal(removed, 0);
  assert.ok(s.hooks && s.hooks.SessionStart);
});

test('pruneDeadHookFiles handles multiple events and mixed live/dead entries', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-prune-'));
  const trackerPath = makeTmpHookFile(dir, 'caveman-mode-tracker.js');
  const s = {
    hooks: {
      SessionStart: [
        { hooks: [{ type: 'command', command: `"/usr/bin/node" "${trackerPath}"` }] },   // live
        { hooks: [{ type: 'command', command: '"/usr/bin/node" "/gone/caveman-activate.js"' }] }, // dead
      ],
      UserPromptSubmit: [
        { hooks: [{ type: 'command', command: '"/usr/bin/node" "/gone/caveman-mode-tracker.js"' }] }, // dead
        { hooks: [{ type: 'command', command: '"/usr/bin/node" "/any/user-script.js"' }] },          // user hook — keep
      ],
    },
  };
  const removed = SETTINGS.pruneDeadHookFiles(s);
  assert.equal(removed, 2);
  assert.equal(s.hooks.SessionStart.length, 1);
  assert.match(s.hooks.SessionStart[0].hooks[0].command, /caveman-mode-tracker\.js/);
  assert.equal(s.hooks.UserPromptSubmit.length, 1);
  assert.match(s.hooks.UserPromptSubmit[0].hooks[0].command, /user-script\.js/);
  fs.rmSync(dir, { recursive: true, force: true });
});

test('pruneDeadHookFiles handles bare-node command format', () => {
  // Ensure the old `node /path/script.js` format is also recognised and pruned.
  const s = {
    hooks: {
      SessionStart: [{
        hooks: [{ type: 'command', command: 'node /nonexistent/caveman-activate.js' }],
      }],
    },
  };
  const removed = SETTINGS.pruneDeadHookFiles(s);
  assert.equal(removed, 1);
  assert.equal(s.hooks, undefined);
});

test('pruneDeadHookFiles is a no-op on empty or missing hooks', () => {
  assert.equal(SETTINGS.pruneDeadHookFiles({}), 0);
  assert.equal(SETTINGS.pruneDeadHookFiles({ hooks: {} }), 0);
  assert.equal(SETTINGS.pruneDeadHookFiles(null), 0);
});

test('pruneDeadHookFiles preserves non-hook settings keys', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-prune-'));
  const s = {
    theme: 'dark',
    statusLine: { type: 'command', command: 'bash /some/statusline.sh' },
    hooks: {
      SessionStart: [{
        hooks: [{ type: 'command', command: '"/usr/bin/node" "/gone/caveman-activate.js"' }],
      }],
    },
  };
  SETTINGS.pruneDeadHookFiles(s);
  assert.equal(s.theme, 'dark');
  assert.deepEqual(s.statusLine, { type: 'command', command: 'bash /some/statusline.sh' });
  fs.rmSync(dir, { recursive: true, force: true });
});
