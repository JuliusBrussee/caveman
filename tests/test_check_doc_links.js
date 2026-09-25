// The doc-link checker fetches docs.caveman.so pages only; a look-alike host
// (docs.caveman.so.evil.invalid, docs.caveman.so@evil.invalid) must be skipped
// as an unchecked other host, never fetched. Offline: nothing here resolves.
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = path.resolve(__dirname, '..');

test('doc link checker never fetches a host that merely starts with docs.caveman.so', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'doc-links-'));
  const file = path.join(dir, 'x.md');
  fs.writeFileSync(file, 'https://docs.caveman.so.evil.invalid/page\n[a](https://docs.caveman.so@evil.invalid/)\n');
  const out = spawnSync(process.execPath, [path.join(root, '.github/scripts/check-doc-links.mjs'), path.relative(root, file)], { encoding: 'utf8', timeout: 30_000 });
  fs.rmSync(dir, { recursive: true, force: true });
  assert.strictEqual(out.status, 0, out.stderr);
  assert.match(out.stdout, /0 docs pages/);
});
