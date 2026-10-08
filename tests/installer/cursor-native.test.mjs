// `--only cursor`: after `npx skills add -a cursor`, the installer owns the
// cavecrew subagents in ~/.cursor/agents/, the shared sessionStart hook payload
// in ~/.cursor/caveman/, and one sessionStart entry in ~/.cursor/hooks.json.
// A recording npx stub keeps the network out; HOME is a temp dir.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
import { nodeStub, stubEnv } from '../../packages/cli/tests/harness/stub-bin.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const INSTALLER = path.join(ROOT, 'installer/install.js');
const cursor = createRequire(import.meta.url)(path.join(ROOT, 'installer/lib/cursor-native.js'));
const AGENTS = ['cavecrew-builder.md', 'cavecrew-investigator.md', 'cavecrew-reviewer.md'];
const FOREIGN = { command: './hooks/user.sh' };

function fixture(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman cursor '));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const home = path.join(dir, 'home');
  fs.mkdirSync(home);
  const bin = path.join(dir, 'bin');
  nodeStub(bin, 'npx', 'process.exit(0);');
  // Only the stub (and node, which Windows shims need by name) on PATH:
  // uninstall must not reach a real `claude`, `gemini` or `caveman`.
  const base = Object.fromEntries(Object.entries(process.env).filter(([key]) =>
    !['path', 'caveman_default_mode', 'cursor_project_dir'].includes(key.toLowerCase())));
  base.PATH = process.platform === 'win32' ? path.dirname(process.execPath) : '/usr/bin:/bin';
  const env = stubEnv({ ...base, HOME: home, USERPROFILE: home, NO_COLOR: '1' }, bin);
  const run = (...args) => spawnSync(process.execPath, [INSTALLER, ...args, '--non-interactive'], {
    encoding: 'utf8', cwd: dir, env,
  });
  const root = path.join(home, '.cursor');
  const hooksJson = () => JSON.parse(fs.readFileSync(path.join(root, 'hooks.json'), 'utf8'));
  return { dir, home, env, root, run, hooksJson };
}

function frontmatter(content) {
  return content.match(/^---\n([\s\S]*?)\n---\n/)[1];
}

test('agent transform drops the Claude model alias and marks the read-only agents', () => {
  const src = '---\nname: x\ndescription: y\nmodel: haiku\n---\nbody\n';
  const ro = cursor.transformCursorAgentFrontmatter(src, { readonly: true });
  assert.equal(ro, '---\nname: x\ndescription: y\nreadonly: true\n---\nbody\n');
  assert.equal(cursor.transformCursorAgentFrontmatter(src, { readonly: false }), '---\nname: x\ndescription: y\n---\nbody\n');
});

test('install lands agents, hook payload and one sessionStart entry; rerun and uninstall are clean', (t) => {
  const { dir, root, env, run, hooksJson } = fixture(t);
  fs.mkdirSync(root, { recursive: true });
  fs.writeFileSync(path.join(root, 'hooks.json'), JSON.stringify({ version: 1, hooks: { sessionStart: [FOREIGN], stop: [FOREIGN] } }));

  let r = run('--only', 'cursor');
  assert.equal(r.status, 0, r.stdout + r.stderr);
  for (const name of AGENTS) {
    const fm = frontmatter(fs.readFileSync(path.join(root, 'agents', name), 'utf8'));
    assert.doesNotMatch(fm, /^model:/m);
    assert.equal(/^readonly: true$/m.test(fm), name !== 'cavecrew-builder.md', name);
  }
  for (const rel of ['hooks/caveman-config.js', 'hooks/package.json', 'skills/caveman/SKILL.md', 'skills/ultracave/SKILL.md', 'skills/megacave/SKILL.md']) {
    assert.ok(fs.existsSync(path.join(root, 'caveman', rel)), rel);
  }
  const entries = hooksJson().hooks.sessionStart;
  assert.deepEqual(entries[0], FOREIGN);
  assert.equal(entries.length, 2);
  assert.match(entries[1].command, /caveman-host-session-start\.js['"]? cursor$/);

  // The registered command runs from the owned copy, outside the repo.
  const sh = spawnSync(entries[1].command, { shell: true, cwd: dir, env, input: '{}', encoding: 'utf8' });
  assert.match(JSON.parse(sh.stdout).additional_context, /CAVEMAN MODE ACTIVE — mode: caveman/);

  r = run('--only', 'cursor');
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.equal(hooksJson().hooks.sessionStart.length, 2, 'rerun must not duplicate the hook');

  r = run('--uninstall');
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.deepEqual(hooksJson(), { version: 1, hooks: { sessionStart: [FOREIGN], stop: [FOREIGN] } });
  assert.equal(fs.existsSync(path.join(root, 'caveman')), false);
  for (const name of AGENTS) assert.equal(fs.existsSync(path.join(root, 'agents', name)), false, name);
});

test('uninstall removes a hooks.json that held only our entry', (t) => {
  const { root, run } = fixture(t);
  assert.equal(run('--only', 'cursor').status, 0);
  assert.equal(run('--uninstall').status, 0);
  assert.equal(fs.existsSync(path.join(root, 'hooks.json')), false);
});

test('--no-hooks installs the agents only', (t) => {
  const { root, run } = fixture(t);
  const r = run('--only', 'cursor', '--no-hooks');
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.ok(fs.existsSync(path.join(root, 'agents', 'cavecrew-builder.md')));
  assert.equal(fs.existsSync(path.join(root, 'hooks.json')), false);
  assert.equal(fs.existsSync(path.join(root, 'caveman')), false);
});

test('a malformed hooks.json is reported and left byte-identical', (t) => {
  const { root, run } = fixture(t);
  fs.mkdirSync(root, { recursive: true });
  fs.writeFileSync(path.join(root, 'hooks.json'), '{ not json');
  const r = run('--only', 'cursor');
  assert.match(r.stderr, /hooks\.json is not a JSON object; left untouched/);
  assert.equal(fs.readFileSync(path.join(root, 'hooks.json'), 'utf8'), '{ not json');
});

test('a user-owned agent file is never overwritten', (t) => {
  const { root, run } = fixture(t);
  const mine = path.join(root, 'agents', 'cavecrew-builder.md');
  fs.mkdirSync(path.dirname(mine), { recursive: true });
  fs.writeFileSync(mine, 'mine\n');
  const r = run('--only', 'cursor');
  assert.match(r.stderr, /ownership conflict/);
  assert.equal(fs.readFileSync(mine, 'utf8'), 'mine\n');
});

test('a hooks.json with comments is left byte-identical rather than rewritten without them', (t) => {
  const { root, run } = fixture(t);
  fs.mkdirSync(root, { recursive: true });
  const original = '{\n  // my hooks\n  "version": 1,\n  "hooks": {}\n}\n';
  fs.writeFileSync(path.join(root, 'hooks.json'), original);
  const r = run('--only', 'cursor');
  assert.match(r.stderr, /hooks\.json has comments/);
  assert.equal(fs.readFileSync(path.join(root, 'hooks.json'), 'utf8'), original);
});
