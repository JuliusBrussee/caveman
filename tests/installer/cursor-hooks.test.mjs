import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const require = createRequire(import.meta.url);
const CURSOR = require('../../installer/lib/cursor-hooks.js');

function freshHome() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-home-'));
}

test('merge keeps unrelated hooks and adds caveman entries', () => {
  const doc = {
    version: 1,
    hooks: {
      beforeShellExecution: [{ command: './hooks/user.sh' }],
      preToolUse: [{ command: './hooks/other.js', matcher: 'Write' }],
    },
  };
  const merged = CURSOR.mergeHooksDocument(doc);
  assert.equal(merged.hooks.beforeShellExecution.length, 2);
  assert.equal(merged.hooks.preToolUse.length, 4);
  const cavemanPre = merged.hooks.preToolUse.filter((e) => CURSOR.isCavemanHookEntry(e));
  assert.equal(cavemanPre.length, 3);
  assert.ok(cavemanPre.some((e) => e.matcher === 'Read'));
  assert.ok(cavemanPre.some((e) => e.matcher === 'Grep'));
  assert.ok(cavemanPre.some((e) => e.matcher === 'Glob'));
  assert.ok(merged.hooks.beforeShellExecution.some((e) => e.command === './hooks/user.sh'));
});

test('merge replaces older caveman Read entry without duplicating', () => {
  const doc = {
    version: 1,
    hooks: {
      preToolUse: [{ command: 'node "./hooks/cursor-dedupe-tools.js" read', matcher: 'Read' }],
    },
  };
  const merged = CURSOR.mergeHooksDocument(doc);
  const cavemanPre = merged.hooks.preToolUse.filter((e) => CURSOR.isCavemanHookEntry(e));
  assert.equal(cavemanPre.length, 3);
});

test('strip removes only caveman hook entries', () => {
  const merged = CURSOR.mergeHooksDocument({ version: 1, hooks: { beforeShellExecution: [{ command: './hooks/user.sh' }] } });
  const { changed, doc } = CURSOR.stripCavemanHooks(merged);
  assert.equal(changed, true);
  assert.equal(doc.hooks.preToolUse, undefined);
  assert.deepEqual(doc.hooks.beforeShellExecution, [{ command: './hooks/user.sh' }]);
});

test('install pilot prints plan and writes nothing', () => {
  const home = freshHome();
  try {
    const notes = [];
    CURSOR.installCursorHooks({ home, note: (line) => notes.push(line) });
    assert.ok(notes.some((line) => line.includes('cursor-dedupe-tools.js')));
    assert.ok(notes.some((line) => line.includes('hooks.json')));
    assert.equal(fs.existsSync(CURSOR.hookScriptPath(home)), false);
    assert.equal(fs.existsSync(CURSOR.hooksJsonPath(home)), false);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('uninstall of a home with no Cursor hook stays silent', () => {
  const home = freshHome();
  try {
    const notes = [];
    CURSOR.uninstallCursorHooks({ home, note: (line) => notes.push(line) });
    assert.deepEqual(notes, []);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('uninstall pilot prints plan and writes nothing', () => {
  const home = freshHome();
  try {
    fs.mkdirSync(path.join(home, '.cursor', 'hooks'), { recursive: true });
    fs.writeFileSync(CURSOR.hookScriptPath(home), '# stub\n');
    fs.writeFileSync(CURSOR.hooksJsonPath(home), '{"version":1,"hooks":{}}\n');

    const notes = [];
    CURSOR.uninstallCursorHooks({ home, note: (line) => notes.push(line) });
    assert.ok(notes.some((line) => line.includes('would remove')));
    assert.ok(notes.some((line) => line.includes('would prune')));
    assert.equal(fs.existsSync(CURSOR.hookScriptPath(home)), true);
    assert.equal(fs.readFileSync(CURSOR.hooksJsonPath(home), 'utf8'), '{"version":1,"hooks":{}}\n');
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});
