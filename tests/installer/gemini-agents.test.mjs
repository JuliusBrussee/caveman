// Gemini/Antigravity agent frontmatter compatibility (issue #497).
//
// The shipped cavecrew agents use Claude Code tool names in `tools: [...]`.
// Gemini rejects those names before loading the agents, so provider installs
// must remove only that frontmatter key from their on-disk copies.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'bin', 'install.js');
const requireCjs = createRequire(import.meta.url);
const { stripAgentTools, stripOpencodeAgentTools } = requireCjs(
  path.join(REPO_ROOT, 'bin', 'lib', 'opencode-agent.js'),
);

const AGENT_FILES = ['cavecrew-investigator.md', 'cavecrew-builder.md', 'cavecrew-reviewer.md'];

function agentFixture(name) {
  return `---
name: ${name.replace(/\.md$/, '')}
description: >
  Test agent with a folded description.
tools: [Read, Edit, Write, Grep, Glob]
model: haiku
---

## Tools

No \`Bash\` available. Keep this prose untouched.
`;
}

function frontmatter(content) {
  const match = content.match(/^---\n([\s\S]*?)\n---\n/);
  assert.ok(match, 'frontmatter present');
  return match[1];
}

function assertSanitized(agentPath) {
  const content = fs.readFileSync(agentPath, 'utf8');
  const fm = frontmatter(content);
  assert.doesNotMatch(fm, /^tools:/m, `${agentPath}: tools field survived`);
  const expectedName = path.basename(agentPath, '.md');
  assert.match(fm, new RegExp(`^name: ${expectedName}$`, 'm'), `${agentPath}: name field changed`);
  assert.match(fm, /^description:/m, `${agentPath}: description field missing`);
  assert.match(content, /^## Tools$/m, `${agentPath}: body heading changed`);
  assert.match(content, /No \`Bash\` available/, `${agentPath}: body prose changed`);
}

function writeFakeCommand(binDir, name) {
  const scriptPath = path.join(binDir, `${name}.js`);
  fs.writeFileSync(scriptPath, `
'use strict';
const fs = require('node:fs');
const path = require('node:path');
if (process.argv.includes('list')) process.exit(0);
const files = JSON.parse(process.env.CAVEMAN_TEST_AGENT_FILES);
for (const file of files) {
  if (file === process.env.CAVEMAN_TEST_MISSING_AGENT) continue;
  const dest = path.join(process.env.CAVEMAN_TEST_AGENT_DIR, file);
  fs.mkdirSync(path.dirname(dest), { recursive: true });
  if (!fs.existsSync(dest)) {
    fs.writeFileSync(
      dest,
      process.env.CAVEMAN_TEST_AGENT_FIXTURE.replaceAll('__FILE__', file.replace(/\\.md$/, '')),
    );
  }
}
`);

  const posixPath = path.join(binDir, name);
  fs.writeFileSync(
    posixPath,
    `#!/bin/sh\nexec "${process.execPath}" "${scriptPath}" "$@"\n`,
    { mode: 0o755 },
  );
  fs.writeFileSync(
    path.join(binDir, `${name}.cmd`),
    `@echo off\r\n"${process.execPath}" "${scriptPath}" %*\r\n`,
  );
}

function makeHarness() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-gemini-agents-'));
  const home = path.join(root, 'home');
  const bin = path.join(root, 'bin');
  fs.mkdirSync(home, { recursive: true });
  fs.mkdirSync(bin, { recursive: true });
  writeFakeCommand(bin, 'gemini');
  writeFakeCommand(bin, 'npx');

  // Keep provider tests isolated from installer modules they do not exercise.
  const preload = path.join(root, 'preload.cjs');
  fs.writeFileSync(preload, `
'use strict';
const Module = require('node:module');
const originalLoad = Module._load;
Module._load = function (request, parent, isMain) {
  const fromInstaller = parent && /bin[\\\\/]install\\.js$/.test(parent.filename);
  if (fromInstaller && (request === './lib/settings' || request === './lib/openclaw')) return {};
  return originalLoad.call(this, request, parent, isMain);
};
`);

  return {
    root,
    home,
    run(provider, agentDir, missingAgent) {
      const nodeOptions = [process.env.NODE_OPTIONS, '--require', JSON.stringify(preload)]
        .filter(Boolean)
        .join(' ');
      return spawnSync(process.execPath, [
        INSTALLER,
        '--only', provider,
        '--force',
        '--skip-skills',
        '--non-interactive',
        '--no-mcp-shrink',
        '--config-dir', path.join(root, 'claude'),
      ], {
        env: {
          ...process.env,
          HOME: home,
          USERPROFILE: home,
          PATH: bin + path.delimiter + (process.env.PATH || ''),
          NODE_OPTIONS: nodeOptions,
          CAVEMAN_TEST_AGENT_DIR: agentDir,
          CAVEMAN_TEST_AGENT_FILES: JSON.stringify(AGENT_FILES),
          CAVEMAN_TEST_AGENT_FIXTURE: agentFixture('__FILE__'),
          CAVEMAN_TEST_MISSING_AGENT: missingAgent || '',
        },
        encoding: 'utf8',
      });
    },
    cleanup() {
      fs.rmSync(root, { recursive: true, force: true });
    },
  };
}

test('stripAgentTools removes only the frontmatter tools key', () => {
  const src = agentFixture('cavecrew-builder.md');
  const out = stripAgentTools(src);
  const fm = frontmatter(out);
  assert.doesNotMatch(fm, /^tools:/m);
  assert.match(fm, /^name: cavecrew-builder$/m);
  assert.match(fm, /^description:/m);
  assert.match(out, /^## Tools$/m, 'inline body tools heading preserved');
  assert.match(out, /No \`Bash\` available/, 'inline body tools prose preserved');
});

test('opencode compatibility alias stays equivalent and stripping is idempotent', () => {
  const src = agentFixture('cavecrew-reviewer.md');
  const once = stripAgentTools(src);
  assert.equal(stripAgentTools(once), once);
  assert.equal(stripOpencodeAgentTools(src), once);
});

test('Gemini extension install strips tools from every distributed cavecrew agent', () => {
  const harness = makeHarness();
  const agentDir = path.join(harness.home, '.gemini', 'extensions', 'caveman', 'agents');
  try {
    const result = harness.run('gemini', agentDir);
    assert.equal(result.status, 0, result.stderr || result.stdout);
    for (const file of AGENT_FILES) assertSanitized(path.join(agentDir, file));
  } finally {
    harness.cleanup();
  }
});

test('Gemini force re-run is idempotent and skips a missing installed agent', () => {
  const harness = makeHarness();
  const agentDir = path.join(harness.home, '.gemini', 'extensions', 'caveman', 'agents');
  const missing = 'cavecrew-reviewer.md';
  try {
    const first = harness.run('gemini', agentDir, missing);
    assert.equal(first.status, 0, first.stderr || first.stdout);
    const second = harness.run('gemini', agentDir, missing);
    assert.equal(second.status, 0, second.stderr || second.stdout);
    for (const file of AGENT_FILES.filter(name => name !== missing)) {
      assertSanitized(path.join(agentDir, file));
    }
    assert.equal(fs.existsSync(path.join(agentDir, missing)), false);
  } finally {
    harness.cleanup();
  }
});

test('Antigravity install strips tools from npx-skills cavecrew agent copies', () => {
  const harness = makeHarness();
  const agentDir = path.join(
    harness.home,
    '.gemini',
    'antigravity',
    'skills',
    'cavecrew',
    'agents',
  );
  try {
    const result = harness.run('antigravity', agentDir);
    assert.equal(result.status, 0, result.stderr || result.stdout);
    for (const file of AGENT_FILES) assertSanitized(path.join(agentDir, file));
  } finally {
    harness.cleanup();
  }
});
