import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const require = createRequire(import.meta.url);
const { parseModeChange } = require(join(root, 'src/hooks/caveman-parse.js'));
const claude = JSON.parse(readFileSync(join(root, '.claude-plugin/plugin.json'), 'utf8'));
const codex = JSON.parse(readFileSync(join(root, '.codex-plugin/plugin.json'), 'utf8'));
const scripts = { SessionStart: 'caveman-activate.js', UserPromptSubmit: 'caveman-mode-tracker.js' };

function fixture(t) {
  const dir = mkdtempSync(join(tmpdir(), 'caveman shared hooks '));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  cpSync(join(root, 'src/hooks'), join(dir, 'src/hooks'), { recursive: true });
  cpSync(join(root, 'skills'), join(dir, 'skills'), { recursive: true });
  const project = join(dir, 'consumer project');
  mkdirSync(project);
  const env = { ...process.env, HOME: dir, USERPROFILE: dir,
    PLUGIN_ROOT: dir, PLUGIN_DATA: join(dir, 'codex data'), CLAUDE_PLUGIN_ROOT: dir,
    CLAUDE_CONFIG_DIR: join(dir, 'claude'), XDG_CONFIG_HOME: join(dir, 'config') };
  delete env.CAVEMAN_DEFAULT_MODE;
  const pathFor = event => join(dir, 'src/hooks', scripts[event]);
  const payloadFor = (event, payload) => ({ session_id: 'session-a', cwd: project,
    hook_event_name: event, source: 'startup', prompt: 'ordinary request', ...payload });
  const run = (event, payload = {}, extraEnv = {}, command) => {
    // Codex expands plugin placeholders before passing the command to the shell.
    command = command?.replaceAll('${PLUGIN_ROOT}', env.PLUGIN_ROOT);
    const result = spawnSync(command ? (process.platform === 'win32' ? 'cmd.exe' : 'sh') : process.execPath,
      command ? (process.platform === 'win32' ? ['/d', '/s', '/c', command] : ['-c', command]) : [pathFor(event)], {
        // Payload cwd owns project config, even when the process starts elsewhere.
        cwd: dir, env: { ...env, ...extraEnv }, input: JSON.stringify(payloadFor(event, payload)),
        encoding: 'utf8', timeout: 5000,
      });
    assert.equal(result.status, 0, result.stderr);
    if (!result.stdout) return '';
    const output = JSON.parse(result.stdout);
    assert.equal(output.systemMessage, undefined);
    assert.equal(output.hookSpecificOutput.hookEventName, event);
    return output.hookSpecificOutput.additionalContext;
  };
  const modePath = (sid = 'session-a') => join(env.PLUGIN_DATA, '.caveman-sessions', sid + '.mode');
  return { dir, project, env, pathFor, payloadFor, run, modePath,
    mode: sid => readFileSync(modePath(sid), 'utf8') };
}

test('plugin manifests use the same handlers with host-specific inline schemas', () => {
  for (const [event, script] of Object.entries(scripts)) {
    const a = claude.hooks[event][0].hooks[0];
    const b = codex.hooks.hooks[event][0].hooks[0];
    assert.ok(a.command.includes('/src/hooks/' + script));
    assert.equal(b.command, 'node "${PLUGIN_ROOT}/src/hooks/' + script + '"');
    assert.equal(a.timeout, 30);
    assert.equal(b.timeout, 5);
  }
  for (const source of ['startup', 'resume', 'clear', 'compact']) {
    assert.match(source, new RegExp(codex.hooks.hooks.SessionStart[0].matcher));
  }
  for (const name of ['hooks.json', 'config.toml']) {
    assert.equal(existsSync(join(root, '.codex', name)), false);
  }
});

test('inline commands run outside the repository, from a path with spaces', t => {
  const f = fixture(t);
  const start = codex.hooks.hooks.SessionStart[0].hooks[0].command;
  const prompt = codex.hooks.hooks.UserPromptSubmit[0].hooks[0].command;
  assert.match(f.run('SessionStart', {}, {}, start), /Caveman is a voice, not broken grammar\./);
  assert.match(f.run('UserPromptSubmit', { prompt: '$caveman:ultracave fix this bug' }, {}, prompt), /grammar stripped/);
  assert.equal(f.mode(), 'ultracave');
});

test('default resolution uses env, payload project config, then user config', t => {
  const f = fixture(t);
  const config = join(f.env.XDG_CONFIG_HOME, 'caveman');
  mkdirSync(config, { recursive: true });
  writeFileSync(join(config, 'config.json'), '{"defaultMode":"lite"}');
  assert.match(f.run('SessionStart'), /mode: caveman/);
  writeFileSync(join(f.project, '.caveman.json'), '{"defaultMode":"ultra"}');
  assert.match(f.run('SessionStart'), /mode: ultracave/);
  assert.match(f.run('SessionStart', {}, { CAVEMAN_DEFAULT_MODE: 'wenyan' }), /Classical Chinese/);
  writeFileSync(join(config, 'config.json'), '{broken');
  rmSync(join(f.project, '.caveman.json'));
  assert.match(f.run('SessionStart', {}, { CAVEMAN_DEFAULT_MODE: ' ultra' }), /mode: caveman/);
});

test('Claude SDK entrypoint does not override the Codex default', t => {
  const f = fixture(t);
  writeFileSync(join(f.project, '.caveman.json'), '{"defaultMode":"ultracave"}');
  for (const source of ['startup', 'resume']) {
    assert.match(f.run('SessionStart', { source, session_id: 'sdk-' + source },
      { CLAUDE_CODE_ENTRYPOINT: 'sdk-ts' }), /mode: ultracave/);
    assert.equal(f.mode('sdk-' + source), 'ultracave');
  }
});

test('manual startup, explicit activation, durable off, and clear reset', t => {
  const f = fixture(t);
  f.env.CAVEMAN_DEFAULT_MODE = 'manual';
  assert.equal(f.run('SessionStart'), '');
  assert.equal(f.mode(), 'off');
  assert.equal(f.run('UserPromptSubmit'), '');
  assert.match(f.run('UserPromptSubmit', { prompt: '$caveman:caveman build a parser' }), /mode: caveman/);
  assert.match(f.run('UserPromptSubmit', { prompt: '/megacave' }), /Classical Chinese/);
  for (const source of ['resume', 'compact']) assert.match(f.run('SessionStart', { source }), /mode: megacave/);
  assert.match(f.run('UserPromptSubmit', { prompt: 'stop caveman' }), /CAVEMAN MODE OFF/);
  for (const source of ['resume', 'compact', 'unknown']) assert.equal(f.run('SessionStart', { source }), '');
  assert.equal(f.mode(), 'off');
  f.run('UserPromptSubmit', { prompt: '$caveman:ultracave' });
  assert.equal(f.run('SessionStart', { source: 'clear' }), '');
  assert.equal(f.mode(), 'off');
});

test('configured off suppresses activation; startup resets a changed mode', t => {
  const f = fixture(t);
  f.env.CAVEMAN_DEFAULT_MODE = 'off';
  assert.equal(f.run('SessionStart'), '');
  f.run('UserPromptSubmit', { prompt: '$caveman:caveman' });
  assert.equal(f.mode(), 'off');
  f.env.CAVEMAN_DEFAULT_MODE = 'caveman';
  f.run('UserPromptSubmit', { prompt: '/ultracave' });
  assert.match(f.run('SessionStart'), /mode: caveman/);
});

test('sessions never inherit another chat or the legacy mirror', t => {
  const f = fixture(t);
  f.run('SessionStart');
  f.run('UserPromptSubmit', { prompt: '/megacave' });
  assert.equal(f.run('UserPromptSubmit', { session_id: 'session-b' }), '');
  assert.match(f.run('SessionStart', { session_id: 'session-b', source: 'resume' }), /mode: caveman/);
  f.run('UserPromptSubmit', { session_id: 'session-b', prompt: '/caveman off' });
  assert.equal(f.mode('session-b'), 'off');
  assert.match(f.run('UserPromptSubmit'), /ACTIVE \(megacave\)/);
  assert.equal(f.mode(), 'megacave');
});

test('one-shots restore style or off; status never consumes restoration', t => {
  const f = fixture(t);
  f.run('SessionStart', {}, { CAVEMAN_DEFAULT_MODE: 'ultracave' });
  for (const skill of ['commit', 'review', 'compress']) {
    f.run('UserPromptSubmit', { prompt: '$caveman:caveman-' + skill + ' do the task' });
    assert.equal(f.mode(), skill);
    assert.match(f.run('SessionStart', { source: 'compact' }), new RegExp('mode: ' + skill));
    const before = statSync(f.modePath()).mtimeMs;
    assert.match(f.run('UserPromptSubmit', { prompt: '$caveman:caveman status' }), new RegExp('mode: ' + skill));
    assert.equal(statSync(f.modePath()).mtimeMs, before);
    f.run('UserPromptSubmit');
    assert.equal(f.mode(), 'ultracave');
  }
  f.run('UserPromptSubmit', { prompt: '/caveman off' });
  f.run('UserPromptSubmit', { prompt: '/caveman-commit' });
  assert.equal(f.run('UserPromptSubmit'), '');
  assert.equal(f.mode(), 'off');
});

test('Codex never modifies Claude state, statusline, agents, or stats history', t => {
  const f = fixture(t);
  mkdirSync(f.env.CLAUDE_CONFIG_DIR);
  mkdirSync(f.env.PLUGIN_DATA);
  writeFileSync(join(f.env.CLAUDE_CONFIG_DIR, 'settings.json'), '{"theme":"dark"}');
  const sentinel = join(f.env.PLUGIN_DATA, 'claude-only-ran');
  const code = 'require("fs").writeFileSync(' + JSON.stringify(sentinel) + ', "bad");';
  writeFileSync(join(f.dir, 'src/hooks/cavecrew-model-overrides.js'), 'module.exports = { resolvePluginRoot: () => "", applyOverrides: () => { ' + code + ' } };');
  writeFileSync(join(f.dir, 'src/hooks/caveman-stats.js'), code);
  assert.doesNotMatch(f.run('SessionStart'), /STATUSLINE/);
  for (const prompt of ['/caveman-stats', '$caveman:caveman-stats']) f.run('UserPromptSubmit', { prompt });
  assert.equal(existsSync(sentinel), false);
  assert.deepEqual(readdirSync(f.env.CLAUDE_CONFIG_DIR), ['settings.json']);
  assert.equal(readFileSync(join(f.env.CLAUDE_CONFIG_DIR, 'settings.json'), 'utf8'), '{"theme":"dark"}');
});

test('invalid ids and incomplete installations fail open without global state', t => {
  const f = fixture(t);
  for (const session_id of [undefined, null, '../../escape', '', 'x'.repeat(129)]) {
    for (const event of Object.keys(scripts)) assert.equal(f.run(event, { session_id }), '');
  }
  assert.equal(existsSync(f.env.PLUGIN_DATA), false);
  rmSync(join(f.dir, 'src/hooks/caveman-config.js'));
  for (const event of Object.keys(scripts)) assert.equal(f.run(event), '');
  assert.equal(existsSync(f.env.PLUGIN_DATA), false);
  assert.equal(existsSync(f.env.CLAUDE_CONFIG_DIR), false);
});

test('missing parser preserves mode; symlinked state is neither read nor clobbered', t => {
  const f = fixture(t);
  f.run('SessionStart');
  rmSync(join(f.dir, 'src/hooks/caveman-parse.js'));
  assert.match(f.run('UserPromptSubmit', { prompt: '/megacave' }), /ACTIVE \(caveman\)/);
  assert.equal(f.mode(), 'caveman');
  const target = join(f.dir, 'foreign');
  writeFileSync(target, 'megacave');
  rmSync(f.modePath());
  try { symlinkSync(target, f.modePath()); } catch (e) { if (e.code === 'EPERM') return; throw e; }
  assert.equal(f.run('UserPromptSubmit'), '');
  f.run('SessionStart');
  assert.equal(readFileSync(target, 'utf8'), 'megacave');
});

test('Codex references opt into the parser without changing Claude syntax', () => {
  const options = { codexSkills: true, getDefaultMode: () => 'caveman' };
  const cases = [
    ['$caveman:caveman fix the bug', { action: 'set', mode: 'caveman' }],
    ['$caveman ultra fix the bug', { action: 'set', mode: 'ultracave' }],
    ['$caveman:caveman off', { action: 'clear' }],
    ['$caveman:megacave translate this', { action: 'set', mode: 'megacave' }],
    ['[$caveman:ultracave](/a/path%20with%20spaces/SKILL.md) fix this', { action: 'set', mode: 'ultracave' }],
    ['$caveman:caveman-commit commit these changes', { action: 'set', mode: 'commit' }],
    ['$other:skill stop caveman', null],
    ['"$caveman:ultracave"', null],
    ['Explain `$caveman:megacave` and "stop caveman"', null],
  ];
  for (const [prompt, expected] of cases) assert.deepEqual(parseModeChange(prompt, options), expected, prompt);
  assert.equal(parseModeChange('$caveman:ultracave', { getDefaultMode: options.getDefaultMode }), null);
});

test('both handlers flush output and exit while stdin stays open', async t => {
  const f = fixture(t);
  for (const event of Object.keys(scripts)) {
    const output = await new Promise((resolveOutput, reject) => {
      const child = spawn(process.execPath, [f.pathFor(event)], { cwd: f.dir, env: f.env, stdio: ['pipe', 'pipe', 'pipe'] });
      let stdout = '';
      let stderr = '';
      const timer = setTimeout(() => { child.kill(); reject(new Error('hook waited for EOF')); }, 4000);
      child.stdout.on('data', chunk => { stdout += chunk; });
      child.stderr.on('data', chunk => { stderr += chunk; });
      child.on('error', reject);
      child.on('close', code => { clearTimeout(timer); code === 0 ? resolveOutput(stdout) : reject(new Error(stderr)); });
      child.stdin.write(JSON.stringify(f.payloadFor(event, {})));
    });
    const content = JSON.parse(output).hookSpecificOutput.additionalContext;
    assert.match(content, /CAVEMAN MODE ACTIVE/);
    if (event === 'SessionStart') assert.match(content, /Never perform caveman/);
  }
});
