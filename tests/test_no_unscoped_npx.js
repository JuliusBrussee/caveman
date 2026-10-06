// The unscoped npm package `caveman` belongs to someone else, so any
// `npx caveman` we print would run a stranger's code. The CLI is
// `npx @caveman-ai/cli`; the skill installer is
// `npx -y github:JuliusBrussee/caveman`. No tracked text file may say otherwise.
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const root = path.resolve(__dirname, '..');
// Built in pieces so this file does not match itself.
const unscoped = new RegExp('(npx|bunx|pnpm dlx) (-y |--yes )?' + 'cave' + 'man(@|[^-/a-z]|$)', 'm');
const lockfiles = new Set(['pnpm-lock.yaml', 'package-lock.json', 'yarn.lock', 'uv.lock', 'go.sum', 'Cargo.lock']);

test('no tracked file tells anyone to run the unscoped caveman package', () => {
  const files = execFileSync('git', ['ls-files', '-z'], { cwd: root, encoding: 'utf8', maxBuffer: 64 << 20 }).split('\0').filter(Boolean);
  const hits = [];
  for (const file of files) {
    if (lockfiles.has(path.basename(file)) || file.split('/').includes('node_modules')) continue;
    const full = path.join(root, file);
    let bytes;
    try {
      if (!fs.lstatSync(full).isFile()) continue;
      bytes = fs.readFileSync(full);
    } catch {
      continue;
    }
    if (bytes.subarray(0, 8192).includes(0)) continue;
    const text = bytes.toString('utf8');
    if (!unscoped.test(text)) continue;
    text.split('\n').forEach((line, i) => { if (unscoped.test(line)) hits.push(`${file}:${i + 1}: ${line.trim()}`); });
  }
  assert.deepStrictEqual(hits, [], 'use npx @caveman-ai/cli (CLI) or npx -y github:JuliusBrussee/caveman (skill installer)');
});
