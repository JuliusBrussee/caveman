import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const required = [
  'HOME',
  'CLAUDE_CONFIG_DIR',
  'HERMES_HOME',
  'XDG_CONFIG_HOME',
  'XDG_CACHE_HOME',
  'XDG_DATA_HOME',
  'OPENCLAW_WORKSPACE',
  'TMPDIR',
];
for (const key of required) {
  assert.ok(process.env[key], `${key} is unset`);
  assert.ok(path.isAbsolute(process.env[key]), `${key} is not absolute: ${process.env[key]}`);
}

const root = process.env.CAVEMAN_HERMETIC_ROOT;
assert.ok(root && path.isAbsolute(root), 'CAVEMAN_HERMETIC_ROOT is missing');
assert.doesNotMatch(root.slice(1), /\/\//, `hermetic root is not normalized: ${root}`);
for (const key of required) {
  assert.ok(
    path.resolve(process.env[key]).startsWith(path.resolve(root) + path.sep),
    `${key} escaped hermetic root: ${process.env[key]}`,
  );
}
assert.notEqual(process.env.HOME, process.env.CAVEMAN_HERMETIC_PROTECT_HOME || '', 'HOME was not redirected');

for (const dir of required.map(key => process.env[key])) {
  fs.mkdirSync(dir, { recursive: true });
}

for (const name of ['claude', 'gemini', 'opencode', 'openclaw', 'hermes']) {
  const result = spawnSync(name, ['--version'], { encoding: 'utf8' });
  assert.equal(result.status, 0, `${name} stub failed: ${result.stderr}`);
  assert.match(result.stdout, new RegExp(`^caveman-hermetic-stub:${name}`));
}

const pluginList = spawnSync('claude', ['plugin', 'list'], { encoding: 'utf8' });
assert.equal(pluginList.status, 0, `claude plugin list stub failed: ${pluginList.stderr}`);
assert.doesNotMatch(pluginList.stdout, /caveman/i, 'fresh fake claude must not claim caveman is installed');

const sep = process.platform === 'win32' ? ';' : ':';
const pathWithoutHarnessClis = (process.env.PATH || '')
  .split(sep)
  .filter(dir => !['claude', 'gemini'].some(name => fs.existsSync(path.join(dir, name))))
  .join(sep);
const nodeAfterStrip = spawnSync('node', ['--version'], {
  env: { ...process.env, PATH: pathWithoutHarnessClis },
  encoding: 'utf8',
});
assert.equal(nodeAfterStrip.status, 0, `real node disappeared when fake harness CLIs were stripped: ${nodeAfterStrip.stderr}`);

for (const [name, expected] of [['npm', 'caveman-hermetic-stub:npm'], ['npx', 'caveman-hermetic-stub:npx']]) {
  const result = spawnSync(name, ['view', 'caveman-shrink', 'name'], { encoding: 'utf8' });
  assert.equal(result.status, 0, `${name} stub failed: ${result.stderr}`);
  assert.match(result.stdout, new RegExp(`^${expected}`));
}

console.log('hermetic environment probe: ok');
