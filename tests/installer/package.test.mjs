import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(HERE, '..', '..');

test('npm package ships Codex plugin manifest and hooks', () => {
  const result = spawnSync('npm', ['pack', '--dry-run', '--json'], {
    cwd: ROOT,
    encoding: 'utf8',
  });
  assert.equal(result.status, 0, result.stderr);

  const packed = JSON.parse(result.stdout);
  const files = new Set(packed[0].files.map(file => file.path));
  assert.ok(files.has('.codex-plugin/plugin.json'));
  assert.ok(files.has('hooks/hooks.json'));
});
