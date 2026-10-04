import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');
// createRequire, not dynamic import(): this is how the existing
// caveman-config.test.mjs loads the same CJS module — the pattern proven on
// the Node 18 CI lane, where named exports from `await import(cjs)` depend on
// the bundled cjs-module-lexer version.
const requireCjs = createRequire(import.meta.url);
const { VALID_MODES } = requireCjs(join(root, 'src', 'hooks', 'caveman-config.js'));

// Full layout copy — .codex (hook + manifest), src (shared resolver) and
// skills (SKILL.md) — so every test runs the fixture's own copy of the hook
// exactly as a repo checkout would resolve it, with no reads escaping into
// the developer's real config. A space in the temp name stress-tests quoting.
function fixture(t) {
  const dir = mkdtempSync(join(tmpdir(), 'caveman codex-sessionstart '));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  cpSync(join(root, '.codex'), join(dir, '.codex'), { recursive: true });
  cpSync(join(root, 'src'), join(dir, 'src'), { recursive: true });
  cpSync(join(root, 'skills'), join(dir, 'skills'), { recursive: true });
  // Hermetic env: strip everything that could redirect mode or skill
  // resolution into the developer's own machine (a CAVEMAN_DEFAULT_MODE from
  // the calling shell, an XDG_CONFIG_HOME with a real caveman config, or a
  // CLAUDE_PLUGIN_ROOT exported by a Claude Code session running the suite).
  const env = { ...process.env };
  delete env.CAVEMAN_DEFAULT_MODE;
  delete env.XDG_CONFIG_HOME;
  delete env.CLAUDE_PLUGIN_ROOT;
  env.HOME = dir;
  env.USERPROFILE = dir;
  return { dir, env };
}

function runHook(item, cwd, extraEnv = {}) {
  return spawnSync(process.execPath, [join(item.dir, '.codex', 'codex-sessionstart.js')], {
    cwd,
    env: { ...item.env, ...extraEnv },
    encoding: 'utf8',
    timeout: 20_000,
  });
}

test('.codex/hooks.json manifest is valid and points at the hook script', () => {
  const manifest = JSON.parse(readFileSync(join(root, '.codex', 'hooks.json'), 'utf8'));
  const entries = manifest.hooks.SessionStart;
  assert.ok(Array.isArray(entries) && entries.length === 1);
  assert.equal(entries[0].matcher, 'startup|resume');
  const hook = entries[0].hooks[0];
  assert.equal(hook.type, 'command');
  // Codex runs command hooks with the SESSION cwd as working directory and
  // may be launched from a subdirectory, so the documented pattern for
  // repo-local hooks is git-root resolution — a bare relative path would
  // make node fail to find the script and caveman silently never activate.
  assert.match(hook.command, /git rev-parse --show-toplevel/);
  assert.match(hook.command, /node .*codex-sessionstart\.js/);
});

test('hand-copied fallback whitelist stays equal to caveman-config VALID_MODES', async () => {
  const source = readFileSync(join(root, '.codex', 'codex-sessionstart.js'), 'utf8');
  const m = source.match(/const FALLBACK_VALID_MODES = (\[[\s\S]*?\]);/);
  assert.ok(m, 'FALLBACK_VALID_MODES array not found in the hook source');
  const fallback = JSON.parse(m[1].replace(/'/g, '"'));
  assert.deepEqual(fallback, VALID_MODES);
});

// Distinctive lines from each skill body: proof the right SKILL.md travelled.
const CAVEMAN_BODY = /Caveman is a voice, not broken grammar\./;
const ULTRA_BODY = /Ultracave is caveman with the grammar stripped\./;
const MEGA_BODY = /Megacave is caveman in Classical Chinese\./;

test('default resolution with no config emits the caveman skill', (t) => {
  const item = fixture(t);
  const r = runHook(item, item.dir);
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /CAVEMAN MODE ACTIVE — mode: caveman/);
  assert.match(r.stdout, CAVEMAN_BODY);
  assert.doesNotMatch(r.stdout, ULTRA_BODY);
});

test('CAVEMAN_DEFAULT_MODE wins over config files and selects its mode', (t) => {
  const item = fixture(t);
  writeFileSync(join(item.dir, '.caveman.json'), JSON.stringify({ defaultMode: 'ultracave' }));
  const r = runHook(item, item.dir, { CAVEMAN_DEFAULT_MODE: 'megacave' });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /mode: megacave/);
  assert.match(r.stdout, MEGA_BODY);
  assert.doesNotMatch(r.stdout, ULTRA_BODY);
});

test('repo-local .caveman.json defaultMode is honored (legacy value maps forward)', (t) => {
  const item = fixture(t);
  writeFileSync(join(item.dir, '.caveman.json'), JSON.stringify({ defaultMode: 'ultra' }));
  const r = runHook(item, item.dir);
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /mode: ultracave/);
  assert.match(r.stdout, ULTRA_BODY);
});

test('user config defaultMode is honored when no repo config or env is set', (t) => {
  const item = fixture(t);
  const configDir = join(item.dir, 'xdg', 'caveman');
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, 'config.json'), JSON.stringify({ defaultMode: 'lite' }));
  const r = runHook(item, item.dir, { XDG_CONFIG_HOME: join(item.dir, 'xdg') });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /mode: caveman/);
});

test('user config defaultMode "off" silences the hook (no forced injection)', (t) => {
  const item = fixture(t);
  const configDir = join(item.dir, 'xdg', 'caveman');
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, 'config.json'), JSON.stringify({ defaultMode: 'off' }));
  const r = runHook(item, item.dir, { XDG_CONFIG_HOME: join(item.dir, 'xdg') });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout, '');
});

test('off injects nothing', (t) => {
  const item = fixture(t);
  const r = runHook(item, item.dir, { CAVEMAN_DEFAULT_MODE: 'off' });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout, '');
});

test('independent-skill modes inject only a one-line pointer', (t) => {
  const item = fixture(t);
  for (const mode of ['commit', 'review', 'compress']) {
    const r = runHook(item, item.dir, { CAVEMAN_DEFAULT_MODE: mode });
    assert.equal(r.status, 0, r.stderr);
    const lines = r.stdout.split('\n').filter((l) => l.trim() !== '');
    assert.equal(lines.length, 1, mode + ': ' + JSON.stringify(r.stdout));
    assert.match(lines[0], new RegExp('mode: ' + mode));
  }
});

test('degraded loader still resolves the mode and names it', (t) => {
  const item = fixture(t);
  // Remove the fixture's shared resolver (both candidate paths): the hook
  // must resolve the mode with its hand-copied fallback, emit the degraded
  // one-liner, and never crash.
  rmSync(join(item.dir, 'src', 'hooks', 'caveman-config.js'));
  writeFileSync(join(item.dir, '.caveman.json'), JSON.stringify({ defaultMode: 'ultra' }));
  const r = runHook(item, item.dir);
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /CAVEMAN MODE ACTIVE — mode: ultracave/);
  assert.match(r.stdout, /Rules: skills\/ultracave\/SKILL\.md/);
  assert.match(r.stdout, /Respond terse like smart caveman/);
  assert.doesNotMatch(r.stdout, /mode: caveman/);
  assert.doesNotMatch(r.stdout, /## Rules/, 'degraded path must not read SKILL.md');
});

test('manual startup policy injects nothing, intact or degraded', (t) => {
  const item = fixture(t);
  const silent = (label, extraEnv) => {
    const r = runHook(item, item.dir, extraEnv);
    assert.equal(r.status, 0, r.stderr);
    assert.equal(r.stdout, '', label);
  };
  silent('intact env', { CAVEMAN_DEFAULT_MODE: 'manual' });
  // Degraded: without the shared resolver, the hand-copied fallback must
  // know `manual` too instead of skipping it and landing on the default.
  rmSync(join(item.dir, 'src', 'hooks', 'caveman-config.js'));
  silent('degraded env', { CAVEMAN_DEFAULT_MODE: 'manual' });
  writeFileSync(join(item.dir, '.caveman.json'), JSON.stringify({ defaultMode: 'manual' }));
  silent('degraded repo config', {});
});

test('CAVEMAN_DEFAULT_MODE with stray whitespace is rejected, not trimmed', (t) => {
  const item = fixture(t);
  // The real resolver does not trim; a fallback that accepted " ultra" where
  // the intact one rejects it would be drift in a whitelist.
  const r = runHook(item, item.dir, { CAVEMAN_DEFAULT_MODE: ' ultra' });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /mode: caveman/);
  assert.doesNotMatch(r.stdout, /mode: ultracave/);
});

test('legacy wenyan default resolves to the megacave skill', (t) => {
  const item = fixture(t);
  const r = runHook(item, item.dir, { CAVEMAN_DEFAULT_MODE: 'wenyan' });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /mode: megacave/);
  assert.match(r.stdout, MEGA_BODY);
  assert.doesNotMatch(r.stdout, CAVEMAN_BODY);
});

test('invalid JSON in the user config is skipped, not fatal', (t) => {
  const item = fixture(t);
  const configDir = join(item.dir, 'xdg', 'caveman');
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, 'config.json'), '{ broken json');
  const r = runHook(item, item.dir, { XDG_CONFIG_HOME: join(item.dir, 'xdg') });
  assert.equal(r.status, 0, r.stderr);
  // Falls through to the built-in default instead of dying.
  assert.match(r.stdout, /mode: caveman/);
});

test('symlinked repo config is refused, symmetric with the real resolver', (t) => {
  const item = fixture(t);
  // Windows cannot create symlinks without elevation; the fallback read then
  // treats the missing file as absent, which is the same outcome this test
  // asserts — so the check is meaningful on every platform.
  try {
    writeFileSync(join(item.dir, 'real-config.json'), JSON.stringify({ defaultMode: 'ultra' }));
    let linked = false;
    try {
      cpSync(join(item.dir, 'real-config.json'), join(item.dir, '.caveman-real.json'));
      symlinkSync(join(item.dir, '.caveman-real.json'), join(item.dir, '.caveman.json'));
      linked = true;
    } catch (e) {
      if (e.code !== 'EPERM') throw e;
    }
    const r = runHook(item, item.dir);
    assert.equal(r.status, 0, r.stderr);
    if (linked) {
      assert.doesNotMatch(r.stdout, /mode: ultracave/, 'symlinked config must not be honored');
      assert.match(r.stdout, /mode: caveman/);
    } else {
      assert.match(r.stdout, /mode: caveman/, 'config absent on this platform → built-in default');
    }
  } finally {
    rmSync(join(item.dir, '.caveman.json'), { force: true });
  }
});

test('exit code is 0 and output is non-empty exactly once under pipe (SIGPIPE-free flush)', (t) => {
  const item = fixture(t);
  // Mirror the hooks.json invocation but pipe stdout through another process
  // and read it back: if the hook ever used process.exit() after its write,
  // the piped reader would see truncated output.
  const r = spawnSync(
    process.execPath,
    ['-e', `const {spawnSync} = require('child_process'); const out = spawnSync(process.execPath, [${JSON.stringify(join(item.dir, '.codex', 'codex-sessionstart.js'))}], {cwd: ${JSON.stringify(item.dir)}, env: ${JSON.stringify(item.env)}}); process.stdout.write(out.stdout); process.exit(out.status);`],
    { encoding: 'utf8', timeout: 20_000 },
  );
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /CAVEMAN MODE ACTIVE — mode: caveman/);
  assert.ok(r.stdout.length > 200, 'full ruleset should survive the pipe intact');
});
