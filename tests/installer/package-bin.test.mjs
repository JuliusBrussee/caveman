import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

function readPackage(...parts) {
  return JSON.parse(fs.readFileSync(path.join(ROOT, ...parts, 'package.json'), 'utf8'));
}

function packageBins(pkg) {
  if (typeof pkg.bin === 'string') {
    return { [pkg.name.replace(/^@[^/]+\//, '')]: pkg.bin };
  }
  return pkg.bin ?? {};
}

test('installer bin does not collide with the bundled CLI', () => {
  const installer = readPackage();
  const cli = readPackage('packages', 'cli');
  const installerBins = Object.entries(packageBins(installer));

  assert.deepEqual(
    installerBins.map(([, target]) => target),
    ['./bin/install.js'],
    'the GitHub package must expose only the unified installer',
  );

  const cliBinNames = new Set(Object.keys(packageBins(cli)));
  for (const [name] of installerBins) {
    assert.equal(
      cliBinNames.has(name),
      false,
      `installer bin ${name} is overwritten by @caveman-ai/cli during npm exec`,
    );
  }
});
