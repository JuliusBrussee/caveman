import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const CURSOR = require('../../bin/lib/cursor-hooks.js');

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

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
  assert.equal(merged.hooks.preToolUse.length, 2);
  assert.ok(merged.hooks.preToolUse.some((e) => CURSOR.isCavemanHookEntry(e)));
  assert.ok(merged.hooks.beforeShellExecution.some((e) => e.command === './hooks/user.sh'));
});

test('strip removes only caveman hook entries', () => {
  const merged = CURSOR.mergeHooksDocument({ version: 1, hooks: { beforeShellExecution: [{ command: './hooks/user.sh' }] } });
  const { changed, doc } = CURSOR.stripCavemanHooks(merged);
  assert.equal(changed, true);
  assert.equal(doc.hooks.preToolUse, undefined);
  assert.deepEqual(doc.hooks.beforeShellExecution, [{ command: './hooks/user.sh' }]);
});

test('install and uninstall round-trip', () => {
  const home = freshHome();
  try {
    CURSOR.installCursorHooks({ repoRoot: REPO_ROOT, home, note: () => {} });
    const manifest = JSON.parse(fs.readFileSync(CURSOR.hooksJsonPath(home), 'utf8'));
    assert.ok(manifest.hooks.preToolUse.some((e) => CURSOR.isCavemanHookEntry(e)));
    assert.ok(fs.existsSync(CURSOR.hookScriptPath(home)));

    const userOnly = {
      version: 1,
      hooks: { beforeShellExecution: [{ command: './hooks/user.sh' }] },
    };
    fs.writeFileSync(CURSOR.hooksJsonPath(home), `${JSON.stringify(userOnly)}\n`);

    CURSOR.installCursorHooks({ repoRoot: REPO_ROOT, home, note: () => {} });
    const merged = JSON.parse(fs.readFileSync(CURSOR.hooksJsonPath(home), 'utf8'));
    assert.equal(merged.hooks.beforeShellExecution.length, 2);

    CURSOR.uninstallCursorHooks({ home, note: () => {} });
    const after = JSON.parse(fs.readFileSync(CURSOR.hooksJsonPath(home), 'utf8'));
    assert.deepEqual(after.hooks.beforeShellExecution, [{ command: './hooks/user.sh' }]);
    assert.equal(fs.existsSync(CURSOR.hookScriptPath(home)), false);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});
