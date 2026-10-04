import { spawnSync } from 'node:child_process';
import { readdirSync } from 'node:fs';

const tests = readdirSync(new URL('./', import.meta.url)).filter(file => file.endsWith('.test.mjs')).map(file => `tests/${file}`);
const result = spawnSync(process.execPath, ['--test', ...tests], {
  stdio: 'inherit', env: { ...process.env, CAVEMAN_REQUIRE_FRAMEWORKS: '1' },
});
if (result.error) throw result.error;
process.exitCode = result.status ?? 1;
