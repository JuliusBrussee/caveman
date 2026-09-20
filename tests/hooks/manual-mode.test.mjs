import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

function fixture(t, source = 'env') {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-manual-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const config = path.join(dir, 'claude');
  fs.mkdirSync(config);
  const project = path.join(dir, 'project');
  fs.mkdirSync(project);
  const env = { ...process.env, HOME: dir, USERPROFILE: dir,
    CLAUDE_CONFIG_DIR: config, XDG_CONFIG_HOME: path.join(dir, 'config') };
  delete env.CAVEMAN_DEFAULT_MODE;
  if (source === 'env') env.CAVEMAN_DEFAULT_MODE = 'manual';
  if (source === 'repo') fs.writeFileSync(path.join(project, '.caveman.json'), '{"defaultMode":"manual"}');
  if (source === 'user') {
    fs.mkdirSync(path.join(env.XDG_CONFIG_HOME, 'caveman'), { recursive: true });
    fs.writeFileSync(path.join(env.XDG_CONFIG_HOME, 'caveman/config.json'), '{"defaultMode":"manual"}');
  }
  const mode = () => fs.readFileSync(path.join(config, '.caveman-sessions/test.mode'), 'utf8');
  const run = (script, payload) => {
    const result = spawnSync(process.execPath, [path.join(root, 'src/hooks', script)], {
      // Deliberately outside project: host payload owns configuration scope.
      cwd: dir, env, input: JSON.stringify({ session_id: 'test', cwd: project, ...payload }),
      encoding: 'utf8', timeout: 5000,
    });
    assert.equal(result.status, 0, result.stderr);
    return result.stdout;
  };
  return { dir, env, mode, run };
}

for (const source of ['env', 'repo', 'user']) {
  test(`manual ${source} config starts off, activates explicitly, survives resume and resets on clear`, t => {
    const { mode, run } = fixture(t, source);
    assert.equal(run('caveman-activate.js', { source: 'startup' }), 'OK');
    assert.equal(mode(), 'off');
    assert.equal(run('caveman-mode-tracker.js', { prompt: 'ordinary request' }), '');
    assert.match(run('caveman-mode-tracker.js', { prompt: '/caveman status' }), /Caveman mode: off/);
    assert.match(run('caveman-mode-tracker.js', { prompt: '/caveman' }), /CAVEMAN MODE ACTIVE/);
    assert.equal(mode(), 'full');
    for (const source of ['compact', 'resume']) {
      assert.match(run('caveman-activate.js', { source }), /CAVEMAN MODE ACTIVE/);
      assert.equal(mode(), 'full');
    }
    run('caveman-mode-tracker.js', { prompt: '/caveman ultra' });
    run('caveman-mode-tracker.js', { prompt: '/caveman-commit' });
    run('caveman-mode-tracker.js', { prompt: 'continue' });
    assert.equal(mode(), 'ultra');
    run('caveman-mode-tracker.js', { prompt: 'stop caveman' });
    assert.equal(run('caveman-activate.js', { source: 'compact' }), 'OK');
    assert.equal(mode(), 'off');
    run('caveman-mode-tracker.js', { prompt: 'talk like caveman' });
    assert.equal(mode(), 'full');
    run('caveman-mode-tracker.js', { prompt: '/caveman ultra' });
    run('caveman-mode-tracker.js', { prompt: '/caveman-commit' });
    assert.equal(run('caveman-activate.js', { source: 'clear' }), 'OK');
    assert.equal(mode(), 'off');
    run('caveman-mode-tracker.js', { prompt: '/caveman-review' });
    run('caveman-mode-tracker.js', { prompt: 'continue after reset' });
    assert.equal(mode(), 'off', 'one-shot after clear must not resurrect stale ultra');
  });
}

test('manual remains a config policy, never a stored or selectable mode', t => {
  const { mode, run } = fixture(t);
  run('caveman-activate.js', { source: 'startup' });
  assert.match(run('caveman-mode-tracker.js', { prompt: '/caveman manual' }), /not recognized/);
  assert.equal(mode(), 'off');
});

test('off config still suppresses bare and natural-language activation', t => {
  const { env, mode, run } = fixture(t);
  env.CAVEMAN_DEFAULT_MODE = 'off';
  run('caveman-activate.js', { source: 'startup' });
  for (const prompt of ['/caveman', 'talk like caveman']) {
    assert.equal(run('caveman-mode-tracker.js', { prompt }), '');
    assert.equal(mode(), 'off');
  }
});

test('missing config module still respects manual startup policy', t => {
  const { dir, env } = fixture(t);
  const hook = path.join(dir, 'caveman-activate.js');
  fs.copyFileSync(path.join(root, 'src/hooks/caveman-activate.js'), hook);
  const result = spawnSync(process.execPath, [hook], {
    cwd: dir, env, input: JSON.stringify({ source: 'startup' }), encoding: 'utf8', timeout: 5000,
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, 'OK');
});
