// Gemini CLI regression coverage.
//
// Gemini extension installs prompt for third-party consent unless --consent is
// passed, which is hostile to curl|bash and non-interactive installer flows.
// Gemini also loads root agents/*.md directly; those files must avoid
// provider-specific tool names that Gemini rejects during extension loading.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'bin', 'install.js');
const AGENTS_DIR = path.join(REPO_ROOT, 'agents');
const CAVE_CREW_AGENT_FILES = [
  'cavecrew-investigator.md',
  'cavecrew-builder.md',
  'cavecrew-reviewer.md',
];

function frontmatter(content) {
  const m = content.match(/^---\n([\s\S]*?)\n---\n/);
  assert.ok(m, 'frontmatter present');
  return m[1];
}

function makeFakeGeminiBin() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-gemini-bin-'));
  const bin = path.join(dir, 'gemini');
  fs.writeFileSync(bin, `#!/usr/bin/env node
const args = process.argv.slice(2);
if (args.join(' ') === 'extensions list') {
  console.log('No extensions installed.');
  process.exit(0);
}
console.error('unexpected gemini args:', args.join(' '));
process.exit(1);
`);
  fs.chmodSync(bin, 0o755);
  return dir;
}

test('Gemini installer uses --consent to avoid the extension security prompt', () => {
  const binDir = makeFakeGeminiBin();
  const r = spawnSync(process.execPath, [
    INSTALLER,
    '--dry-run',
    '--only', 'gemini',
    '--non-interactive',
  ], {
    encoding: 'utf8',
    env: {
      ...process.env,
      PATH: `${binDir}${path.delimiter}${process.env.PATH || ''}`,
    },
  });

  assert.equal(r.status, 0);
  assert.match(
    r.stdout,
    /would run: gemini extensions install https:\/\/github\.com\/JuliusBrussee\/caveman --consent/,
  );
});

test('root cavecrew agents omit provider-specific tool names for Gemini compatibility', () => {
  for (const f of CAVE_CREW_AGENT_FILES) {
    const src = fs.readFileSync(path.join(AGENTS_DIR, f), 'utf8');
    const fm = frontmatter(src);
    assert.doesNotMatch(
      fm,
      /^tools:/m,
      `${f}: Gemini validates tool names and rejects Claude-style names such as Read/Grep/Bash`,
    );
  }
});
