// src/hooks/caveman-host-session-start.js — the sessionStart hook for hosts
// other than Claude Code (Cursor, GitHub Copilot CLI). Same mode resolution as
// caveman-activate.js, host-specific output key, no state writes.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const HOOK = path.join(ROOT, 'src/hooks/caveman-host-session-start.js');

function skillBody(id) {
  return fs.readFileSync(path.join(ROOT, 'skills', id, 'SKILL.md'), 'utf8')
    .replace(/^---[\s\S]*?---\s*/, '').trimEnd();
}

function fixture(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-host-hook-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const project = path.join(dir, 'project');
  fs.mkdirSync(project);
  const env = {
    ...process.env,
    HOME: dir, USERPROFILE: dir,
    XDG_CONFIG_HOME: path.join(dir, 'xdg'),
    APPDATA: path.join(dir, 'appdata'),
    CLAUDE_CONFIG_DIR: path.join(dir, 'claude'),
  };
  for (const key of ['CAVEMAN_DEFAULT_MODE', 'CLAUDE_PLUGIN_ROOT', 'CURSOR_PROJECT_DIR']) delete env[key];
  return { dir, project, env };
}

function run(host, { env, cwd, payload = {} }) {
  const r = spawnSync(process.execPath, [HOOK, host], {
    env, cwd, input: JSON.stringify(payload), encoding: 'utf8', timeout: 5000,
  });
  assert.equal(r.status, 0, r.stderr);
  return JSON.parse(r.stdout);
}

test('copilot: ultracave default emits the ultracave skill under additionalContext', (t) => {
  const { dir, env } = fixture(t);
  env.CAVEMAN_DEFAULT_MODE = 'ultracave';
  const out = run('copilot', { env, cwd: dir, payload: { sessionId: 's', source: 'startup', cwd: dir } });
  assert.deepEqual(Object.keys(out), ['additionalContext']);
  assert.match(out.additionalContext, /^CAVEMAN MODE ACTIVE — mode: ultracave\n\n/);
  // No standalone `Caveman mode:` line: Copilot CLI 1.0.92 echoed it at the
  // top of ordinary answers (2 of 3 live runs; 0 of 3 without it).
  assert.doesNotMatch(out.additionalContext, /^Caveman mode:/m);
  assert.ok(out.additionalContext.includes(skillBody('ultracave')));
});

test('cursor: default mode emits the caveman skill under additional_context', (t) => {
  const { dir, env } = fixture(t);
  const out = run('cursor', { env, cwd: dir, payload: { session_id: 's', workspace_roots: [dir] } });
  assert.deepEqual(Object.keys(out), ['additional_context']);
  assert.match(out.additional_context, /^CAVEMAN MODE ACTIVE — mode: caveman\n\n/);
  assert.ok(out.additional_context.includes(skillBody('caveman')));
});

test('a legacy configured level resolves to its v3 skill', (t) => {
  const { dir, env } = fixture(t);
  env.CAVEMAN_DEFAULT_MODE = 'wenyan-lite';
  const out = run('cursor', { env, cwd: dir });
  assert.ok(out.additional_context.includes(skillBody('megacave')));
});

for (const mode of ['off', 'manual', 'commit']) {
  for (const host of ['cursor', 'copilot']) {
    test(`${host}: defaultMode ${mode} prints {}`, (t) => {
      const { dir, env } = fixture(t);
      env.CAVEMAN_DEFAULT_MODE = mode;
      assert.deepEqual(run(host, { env, cwd: dir }), {});
    });
  }
}

test('repo-local off is found from the host payload, not the hook cwd', (t) => {
  const { dir, project, env } = fixture(t);
  fs.writeFileSync(path.join(project, '.caveman.json'), '{"defaultMode":"off"}');
  assert.deepEqual(run('copilot', { env, cwd: dir, payload: { cwd: project } }), {});
  assert.deepEqual(run('cursor', { env, cwd: dir, payload: { workspace_roots: [project] } }), {});
  // Cursor also exports the workspace root to every hook.
  assert.deepEqual(run('cursor', { env: { ...env, CURSOR_PROJECT_DIR: project }, cwd: dir }), {});
});

test('unknown host prints {}', (t) => {
  const { dir, env } = fixture(t);
  assert.deepEqual(run('nope', { env, cwd: dir }), {});
  assert.deepEqual(run('', { env, cwd: dir }), {});
});

test('writes no caveman state anywhere', (t) => {
  const { dir, env } = fixture(t);
  run('cursor', { env, cwd: dir, payload: { session_id: 'abc', workspace_roots: [dir] } });
  run('copilot', { env, cwd: dir, payload: { sessionId: 'abc', cwd: dir } });
  assert.deepEqual(fs.readdirSync(dir), ['project']);
});

function runHoldingStdinOpen(payload, env, cwd) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [HOOK, 'cursor'], { env, cwd, stdio: ['pipe', 'pipe', 'pipe'] });
    const started = Date.now();
    let stdout = '';
    child.stdout.on('data', (d) => { stdout += d; });
    const killer = setTimeout(() => { child.kill('SIGKILL'); reject(new Error('hook waited for stdin EOF')); }, 4000);
    child.on('error', reject);
    child.on('exit', (code) => { clearTimeout(killer); resolve({ code, stdout, elapsed: Date.now() - started }); });
    if (payload !== null) child.stdin.write(payload);
    // Deliberately never child.stdin.end(): Windows hosts close the pipe late.
  });
}

test('returns on the first complete JSON object while the writer stays open', async (t) => {
  const { dir, env } = fixture(t);
  const r = await runHoldingStdinOpen(JSON.stringify({ workspace_roots: [dir] }), env, dir);
  assert.equal(r.code, 0);
  assert.ok(r.elapsed < 1500, `took ${r.elapsed}ms`);
  assert.match(JSON.parse(r.stdout).additional_context, /CAVEMAN MODE ACTIVE — mode: caveman/);
});

test('watchdog answers when no payload ever arrives', async (t) => {
  const { dir, env } = fixture(t);
  const r = await runHoldingStdinOpen(null, env, dir);
  assert.equal(r.code, 0);
  assert.ok(r.elapsed < 3500, `took ${r.elapsed}ms`);
  assert.match(JSON.parse(r.stdout).additional_context, /CAVEMAN MODE ACTIVE — mode: caveman/);
});

test('Cursor plugin manifest wires the shared hook, and no hooks/hooks.json exists for Claude Code to load', (t) => {
  const { dir, env } = fixture(t);
  const manifest = JSON.parse(fs.readFileSync(path.join(ROOT, '.cursor-plugin/plugin.json'), 'utf8'));
  const hooks = JSON.parse(fs.readFileSync(path.join(ROOT, manifest.hooks), 'utf8'));
  const [entry] = hooks.hooks.sessionStart;
  // Cursor substitutes ${CURSOR_PLUGIN_ROOT} before running the command.
  const command = entry.command.replace('${CURSOR_PLUGIN_ROOT}', ROOT);
  const [, script, host] = command.match(/^node "([^"]+)" (\S+)$/);
  const r = spawnSync(process.execPath, [script, host], { env, cwd: dir, input: '{}', encoding: 'utf8' });
  assert.match(JSON.parse(r.stdout).additional_context, /CAVEMAN MODE ACTIVE — mode: caveman/);
  assert.equal(fs.existsSync(path.join(ROOT, 'hooks/hooks.json')), false);
});

test('owned install reads its pinned skill before the host skills folder', (t) => {
  const { dir, env } = fixture(t);
  // <host>/caveman/hooks/ is the installed payload; <host>/skills/ holds the
  // unpinned `npx skills add` copy that must not win.
  const host = path.join(dir, '.cursor');
  const hooks = path.join(host, 'caveman', 'hooks');
  fs.mkdirSync(hooks, { recursive: true });
  for (const f of ['caveman-host-session-start.js', 'caveman-config.js', 'package.json']) {
    fs.copyFileSync(path.join(ROOT, 'src/hooks', f), path.join(hooks, f));
  }
  for (const [where, text] of [[path.join(host, 'caveman', 'skills'), 'PINNED RULES'], [path.join(host, 'skills'), 'UPSTREAM RULES']]) {
    fs.mkdirSync(path.join(where, 'caveman'), { recursive: true });
    fs.writeFileSync(path.join(where, 'caveman', 'SKILL.md'), `---\nname: caveman\n---\n${text}\n`);
  }
  const r = spawnSync(process.execPath, [path.join(hooks, 'caveman-host-session-start.js'), 'cursor'], {
    env, cwd: dir, input: '{}', encoding: 'utf8', timeout: 5000,
  });
  assert.equal(r.status, 0, r.stderr);
  assert.match(JSON.parse(r.stdout).additional_context, /PINNED RULES$/);
});
