// Uninstall must never leave settings.json pointing at hook scripts it deleted,
// and neither install nor uninstall may touch a hooks/package.json another
// plugin owns.
//
// Both are the same class of bug: installer/install.js treating shared, user-owned
// state in $CLAUDE_CONFIG_DIR/hooks as if caveman owned it outright.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { isolatedEnv as isolatedHome } from './_isolated-env.mjs';
import { nodeStub, stubEnv } from '../../packages/cli/tests/harness/stub-bin.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'installer', 'install.js');

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-uninstall-safety-'));
}

function fakeClaudeDir(root) {
  const dir = path.join(root, 'fake-bin');
  fs.mkdirSync(dir, { recursive: true });
  if (process.platform === 'win32') {
    // A Node-backed shim: the installer refuses to launch any other .cmd.
    fs.writeFileSync(path.join(dir, 'claude.js'), 'process.exit(0);\n');
    fs.writeFileSync(path.join(dir, 'claude.cmd'), '@echo off\r\n"%~dp0\\node.exe" "%~dp0\\claude.js" %*\r\n');
  } else {
    const file = path.join(dir, 'claude');
    fs.writeFileSync(file, '#!/bin/sh\nexit 0\n');
    fs.chmodSync(file, 0o755);
  }
  return dir;
}

// A throwaway home, and a PATH with only the stubs and system dirs, so the
// installer never reaches the user's real plugin, extension or native-agent
// state.
function isolatedEnv(root, extraBinDirs = []) {
  return isolatedHome(path.join(root, 'home'), [fakeClaudeDir(root), ...extraBinDirs]);
}

// A fake `caveman` CLI that records each invocation (one argument per line,
// invocations separated by a blank line) into `record` and exits 0, the same
// shape `disableNativeAgent`'s real command uses for `caveman disable --all`.
// With `version` it answers `--version` the way the real CLI does; `disable`
// exits `disableExit`.
function fakeCavemanDir(root, record, version, disableExit = 0) {
  const reply = version ? JSON.stringify({ version }) : '';
  const dir = path.join(root, 'fake-caveman-bin');
  fs.mkdirSync(dir, { recursive: true });
  if (process.platform === 'win32') {
    // install.js's own portableInvocation() refuses to launch a `.cmd` shim
    // via cmd.exe unless it recognizes it as an npm/pnpm-style Node shim
    // (a line calling node on a sibling .js/.cjs/.mjs file); it launches that
    // script directly with the running node binary instead. Match the same
    // shape the gemini fixture in gemini-install.test.mjs already uses.
    fs.writeFileSync(path.join(dir, 'caveman.js'),
      "const fs = require('node:fs');\n"
      + `fs.appendFileSync(${JSON.stringify(record)}, process.argv.slice(2).join('\\n') + '\\n\\n');\n`
      + `if (process.argv[2] === '--version') console.log(${JSON.stringify(reply)});\n`
      + `if (process.argv[2] === 'disable') process.exit(${disableExit});\n`);
    fs.writeFileSync(path.join(dir, 'caveman.cmd'),
      '@echo off\r\n'
      + '"%~dp0\\node.exe" "%~dp0\\caveman.js" %*\r\n');
  } else {
    const file = path.join(dir, 'caveman');
    fs.writeFileSync(file,
      '#!/bin/sh\n'
      + `{ for a in "$@"; do echo "$a"; done; echo; } >> "${record}"\n`
      + `[ "$1" = --version ] && echo '${reply}'\n`
      + `[ "$1" = disable ] && exit ${disableExit}\n`
      + 'exit 0\n');
    fs.chmodSync(file, 0o755);
  }
  return dir;
}

function runInstaller(args, configDir, extraEnv) {
  return spawnSync(process.execPath, [INSTALLER, ...args, '--config-dir', configDir, '--non-interactive', '--no-mcp-shrink'], {
    env: { ...process.env, CLAUDE_CONFIG_DIR: configDir, NO_COLOR: '1', ...extraEnv },
    encoding: 'utf8',
  });
}

// A settings.json the JSONC-tolerant reader still cannot parse, so readSettings
// returns null and the hook-removal block is skipped entirely.
const UNPARSEABLE = '{ "hooks": { "SessionStart": [ , ] }';

test('uninstall keeps the hook files when settings.json cannot be updated', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const env = isolatedEnv(dir);
  try {
    const installed = runInstaller(['--only', 'claude', '--with-hooks'], configDir, env);
    assert.equal(installed.status, 0, installed.stderr || installed.stdout);
    const activate = path.join(configDir, 'hooks', 'caveman-activate.js');
    assert.ok(fs.existsSync(activate), 'setup: the hook was never installed');

    fs.writeFileSync(path.join(configDir, 'settings.json'), UNPARSEABLE);
    const removed = runInstaller(['--uninstall'], configDir, env);

    // Deleting the scripts here strands the entries settings.json still holds:
    // Claude Code then dies with `Cannot find module …caveman-activate.js` on
    // every session start (#471).
    assert.ok(fs.existsSync(activate), 'uninstall deleted a hook settings.json may still reference');
    assert.notEqual(removed.status, 0, 'a cleanup that could not finish must not exit 0');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('uninstall hands native agent integrations to `caveman disable --all` when the CLI is present', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const record = path.join(dir, 'caveman-record.txt');
  const env = isolatedEnv(dir, [fakeCavemanDir(dir, record)]);
  try {
    const installed = runInstaller(['--only', 'claude', '--with-hooks'], configDir, env);
    assert.equal(installed.status, 0, installed.stderr || installed.stdout);
    assert.ok(!fs.existsSync(record), 'install must not touch native agent integrations');

    const removed = runInstaller(['--uninstall'], configDir, env);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);

    const calls = fs.readFileSync(record, 'utf8').trim().split(/\n\s*\n/).filter((c) => c && c !== '--version');
    assert.equal(calls.length, 1, `expected exactly one \`caveman\` invocation, got:\n${calls.join('\n---\n')}`);
    assert.deepEqual(calls[0].split('\n'), ['disable', '--all']);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// Setup puts the CLI under $CAVEMAN_HOME/cli when the global npm prefix is not
// writable, so an older global caveman can stay first on PATH. Only the newer
// CLI undoes everything it wrote (Auto's model keys), so both run.
test('uninstall also runs the private CLI setup installed under $CAVEMAN_HOME/cli', { skip: process.platform === 'win32' && 'POSIX fake CLI' }, () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const record = path.join(dir, 'caveman-record.txt');
  const privateRecord = path.join(dir, 'private-record.txt');
  const env = isolatedEnv(dir, [fakeCavemanDir(dir, record)]);
  try {
    const installed = runInstaller(['--only', 'claude', '--with-hooks'], configDir, env);
    assert.equal(installed.status, 0, installed.stderr || installed.stdout);
    const privateCli = path.join(env.CAVEMAN_HOME, 'cli', 'bin', 'caveman');
    fs.mkdirSync(path.dirname(privateCli), { recursive: true });
    fs.writeFileSync(privateCli, `#!/bin/sh\n{ for a in "$@"; do echo "$a"; done; echo; } >> "${privateRecord}"\nexit 0\n`, { mode: 0o755 });
    // Records disable keeps, not routes: no "still installed" warning for them.
    for (const name of ['claude-profiles.json', 'claude.voice-skills.json']) {
      fs.mkdirSync(path.join(env.CAVEMAN_HOME, 'integrations'), { recursive: true });
      fs.writeFileSync(path.join(env.CAVEMAN_HOME, 'integrations', name), '[]\n');
    }

    const removed = runInstaller(['--uninstall'], configDir, env);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
    // The PATH caveman is asked its --version first (an older one hands its
    // turn to the bundled CLI); the private one is only asked to disable.
    assert.deepEqual(fs.readFileSync(privateRecord, 'utf8').trim().split('\n'), ['disable', '--all']);
    assert.deepEqual(fs.readFileSync(record, 'utf8').trim().split('\n').slice(-2), ['disable', '--all']);
    assert.doesNotMatch(`${removed.stdout}${removed.stderr}`, /still installed/);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('uninstall does not invoke `caveman` when it is not on PATH', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const env = isolatedEnv(dir);
  try {
    const installed = runInstaller(['--only', 'claude', '--with-hooks'], configDir, env);
    assert.equal(installed.status, 0, installed.stderr || installed.stdout);
    const removed = runInstaller(['--uninstall'], configDir, env);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// An older global `caveman` cannot undo what a newer CLI wrote (1.x left Claude
// Code on the caveman-auto model with no route), so when the CLI this package
// depends on is newer, uninstall runs that one instead.
test('uninstall runs the bundled CLI when the caveman on PATH is older', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const record = path.join(dir, 'caveman-record.txt');
  try {
    // CI installs no root dependencies, so NODE_PATH offers a stand-in. Node
    // still prefers a real node_modules copy; ask it which one the installer sees.
    const nodePath = path.join(dir, 'node-path');
    const standIn = path.join(nodePath, '@caveman-ai', 'cli');
    fs.mkdirSync(path.join(standIn, 'dist'), { recursive: true });
    fs.writeFileSync(path.join(standIn, 'package.json'), JSON.stringify({ name: '@caveman-ai/cli', version: '2.1.0', bin: { caveman: 'dist/index.js' } }));
    fs.writeFileSync(path.join(standIn, 'dist', 'index.js'), '');
    const seen = spawnSync(process.execPath, ['-e', "process.stdout.write(require.resolve('@caveman-ai/cli/package.json'))"], {
      cwd: path.dirname(INSTALLER), env: { ...process.env, NODE_PATH: nodePath }, encoding: 'utf8',
    }).stdout;
    assert.ok(seen, 'no @caveman-ai/cli resolvable from the installer');
    for (const [version, runsBundled] of [['0.0.1', true], ['999.0.0', false]]) {
      const env = { ...isolatedEnv(dir, [fakeCavemanDir(dir, record, version)]), NODE_PATH: nodePath };
      const r = runInstaller(['--uninstall', '--dry-run'], configDir, env);
      assert.equal(r.status, 0, r.stderr || r.stdout);
      const line = r.stdout.split('\n').find((l) => l.includes('disable --all')) || '';
      if (runsBundled) {
        assert.ok(line.includes(path.dirname(seen)), `PATH caveman ${version} ran instead of the bundled CLI: ${line}`);
      } else {
        assert.match(line, /would run: caveman disable --all/, `bundled CLI ran over a newer PATH caveman: ${line}`);
      }
    }
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// The bundled CLI needs a newer Node than the installer (engines >=22.13 vs
// >=18); under npx it is also the `caveman` on PATH. On an older Node it
// crashes or runs unsupported, so uninstall starts no CLI and says why.
test('uninstall never starts the bundled CLI on a Node older than it needs', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const record = path.join(dir, 'caveman-record.txt');
  try {
    // A copy of the installer whose own node_modules holds a CLI no Node meets.
    const pkg = path.join(dir, 'pkg');
    fs.cpSync(path.dirname(INSTALLER), path.join(pkg, 'installer'), { recursive: true });
    const cli = path.join(pkg, 'node_modules', '@caveman-ai', 'cli');
    fs.mkdirSync(path.join(cli, 'dist'), { recursive: true });
    fs.writeFileSync(path.join(cli, 'package.json'), JSON.stringify({ name: '@caveman-ai/cli', version: '999.0.0', engines: { node: '>=999' }, bin: { caveman: 'dist/index.js' } }));
    fs.writeFileSync(path.join(cli, 'dist', 'index.js'), `require('fs').appendFileSync(${JSON.stringify(record)}, 'bundled\\n');\n`);
    seedIntegrationJournal(dir, 'claude', 'http://127.0.0.1:8787/w/claude');
    const env = isolatedEnv(dir, [fakeCavemanDir(dir, record, '0.0.1')]);
    const removed = spawnSync(process.execPath, [path.join(pkg, 'installer', 'install.js'), '--uninstall', '--config-dir', configDir, '--non-interactive', '--no-mcp-shrink'], {
      env: { ...env, CLAUDE_CONFIG_DIR: configDir }, encoding: 'utf8',
    });
    // The route it could not undo leaves the uninstall incomplete.
    assert.equal(removed.status, 1, removed.stderr || removed.stdout);
    assert.match(`${removed.stdout}${removed.stderr}`, /uninstall incomplete/);
    assert.equal(fs.existsSync(record), false, `a caveman CLI ran: ${fs.existsSync(record) && fs.readFileSync(record, 'utf8')}`);
    assert.match(removed.stderr, /claude: native Caveman routing is still installed/);
    assert.match(removed.stderr, /needs Node 999 or newer/);
    assert.match(removed.stderr, /caveman disable --all/);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// `~/.caveman/integrations/<agent>.json` is what `caveman enable <agent>` writes
// and what `caveman disable` removes, so its presence AFTER uninstall is exact
// evidence that a native route (ANTHROPIC_BASE_URL and friends) is still in the
// host's settings — never a false positive on a user's own base-URL export.
function seedIntegrationJournal(root, agent, route) {
  const dir = path.join(root, 'home', '.caveman', 'integrations');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, `${agent}.json`), JSON.stringify({
    agent, operations: [{ kind: `${agent}-settings`, owned: { route, assume_first_party: '1' }, previous_route: null }],
  }, null, 2));
  return dir;
}

test('uninstall says so when a native route survives it', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  // No `caveman` on PATH — the documented order is `--uninstall` first, but a
  // user who ran `npm uninstall -g @caveman-ai/cli` first lands exactly here,
  // and so does anyone whose `disable --all` failed. Without a word from the
  // installer they keep a dead ANTHROPIC_BASE_URL and the Remote Control
  // breakage of #947, with nothing pointing at the cause (#1040).
  const env = isolatedEnv(dir);
  try {
    assert.equal(runInstaller(['--only', 'claude', '--with-hooks'], configDir, env).status, 0);
    seedIntegrationJournal(dir, 'claude', 'http://127.0.0.1:8787/w/claude');

    const removed = runInstaller(['--uninstall'], configDir, env);
    // A route still in place is an unfinished uninstall, not "uninstall done".
    assert.equal(removed.status, 1, removed.stderr || removed.stdout);
    assert.match(removed.stderr, /uninstall incomplete/);
    const output = `${removed.stdout}${removed.stderr}`;
    assert.match(output, /claude/);
    assert.match(output, /caveman disable --all/, 'name the command that withdraws the route');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('a failed `caveman disable --all` leaves uninstall incomplete', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const record = path.join(dir, 'caveman-record.txt');
  try {
    const removed = runInstaller(['--uninstall'], configDir, isolatedEnv(dir, [fakeCavemanDir(dir, record, undefined, 3)]));
    assert.match(fs.readFileSync(record, 'utf8'), /^disable\n--all$/m);
    assert.equal(removed.status, 1, removed.stderr || removed.stdout);
    assert.match(removed.stderr, /uninstall incomplete/);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// The bundled CLI may not start at all (killed on start, a broken Node). The
// caveman on PATH, even an older one, then takes the turn. A bundled CLI that
// ran and failed keeps its failure: an older CLI cannot undo what the newer one
// wrote, and its exit 0 would hide that.
test('uninstall falls back to the PATH caveman only when the bundled CLI did not run', (t) => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const record = path.join(dir, 'caveman-record.txt');
  try {
    const nodePath = path.join(dir, 'node-path');
    const standIn = path.join(nodePath, '@caveman-ai', 'cli');
    fs.mkdirSync(path.join(standIn, 'dist'), { recursive: true });
    fs.writeFileSync(path.join(standIn, 'package.json'), JSON.stringify({ name: '@caveman-ai/cli', version: '2.1.0', bin: { caveman: 'dist/index.js' } }));
    const seen = spawnSync(process.execPath, ['-e', "process.stdout.write(require.resolve('@caveman-ai/cli/package.json'))"], {
      cwd: path.dirname(INSTALLER), env: { ...process.env, NODE_PATH: nodePath }, encoding: 'utf8',
    }).stdout;
    if (seen !== fs.realpathSync(path.join(standIn, 'package.json'))) return t.skip('a real @caveman-ai/cli is installed beside the installer');
    const env = { ...isolatedEnv(dir, [fakeCavemanDir(dir, record, '0.0.1')]), NODE_PATH: nodePath };
    // Windows has no signals: a process that kills itself just exits 1.
    const cases = [['process.exit(1);\n', false]];
    if (process.platform !== 'win32') cases.push(["process.kill(process.pid, 'SIGKILL');\n", true]);
    for (const [script, fallsBack] of cases) {
      fs.writeFileSync(path.join(standIn, 'dist', 'index.js'), script);
      fs.rmSync(record, { force: true });
      const removed = runInstaller(['--uninstall'], configDir, env);
      assert.match(removed.stdout, /dist[\\/]index\.js'? disable --all/, 'the bundled CLI did not go first');
      const pathRan = fs.existsSync(record) && /^disable\n--all$/m.test(fs.readFileSync(record, 'utf8'));
      assert.equal(pathRan, fallsBack, `${script.trim()}: the PATH caveman ${fallsBack ? 'did not get' : 'got'} the turn`);
      assert.equal(removed.status, fallsBack ? 0 : 1, removed.stderr || removed.stdout);
      if (!fallsBack) assert.match(removed.stderr, /uninstall incomplete/);
    }
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// `disable --all` deletes `<agent>.json` but keeps the CLI's other records in
// the same directory on purpose. They are not routes; warning about them sent
// users to run `caveman disable --all` forever.
test('uninstall ignores the CLI records a clean disable leaves behind', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const env = isolatedEnv(dir);
  try {
    const integrations = path.join(dir, 'home', '.caveman', 'integrations');
    fs.mkdirSync(integrations, { recursive: true });
    for (const name of ['claude-profiles.json', 'claude.voice-skills.json', 'codex.voice-skills.json', 'claude.agent-native-bundle.json']) {
      fs.writeFileSync(path.join(integrations, name), '{}\n');
    }
    const removed = runInstaller(['--uninstall'], configDir, env);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
    assert.doesNotMatch(`${removed.stdout}${removed.stderr}`, /still installed|caveman disable --all/);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('uninstall stays quiet when no native integration is journaled', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const env = isolatedEnv(dir);
  try {
    assert.equal(runInstaller(['--only', 'claude', '--with-hooks'], configDir, env).status, 0);
    const removed = runInstaller(['--uninstall'], configDir, env);
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
    assert.doesNotMatch(`${removed.stdout}${removed.stderr}`, /caveman disable --all/);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// A foreign `caveman` whose --version never answers must not stall uninstall:
// the probe gives up after 10s and the PATH caveman keeps its turn.
test('uninstall gives up on a caveman --version that hangs', { skip: process.platform === 'win32' && 'POSIX fake CLI' }, () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  try {
    // A bundled CLI to compare against, or the probe never runs (see above).
    const nodePath = path.join(dir, 'node-path');
    const standIn = path.join(nodePath, '@caveman-ai', 'cli');
    fs.mkdirSync(path.join(standIn, 'dist'), { recursive: true });
    fs.writeFileSync(path.join(standIn, 'package.json'), JSON.stringify({ name: '@caveman-ai/cli', version: '2.1.0', bin: { caveman: 'dist/index.js' } }));
    fs.writeFileSync(path.join(standIn, 'dist', 'index.js'), '');
    const bin = path.join(dir, 'hung-bin');
    fs.mkdirSync(bin);
    fs.writeFileSync(path.join(bin, 'caveman'), '#!/bin/sh\n[ "$1" = --version ] && exec sleep 120\nexit 0\n', { mode: 0o755 });
    const r = spawnSync(process.execPath, [INSTALLER, '--uninstall', '--dry-run', '--config-dir', configDir, '--non-interactive', '--no-mcp-shrink'], {
      env: { ...isolatedEnv(dir, [bin]), CLAUDE_CONFIG_DIR: configDir, NODE_PATH: nodePath, NO_COLOR: '1' },
      encoding: 'utf8', timeout: 60_000,
    });
    assert.equal(r.status, 0, r.error?.message || r.stderr || r.stdout);
    assert.match(r.stdout, /would run: caveman disable --all/);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// Writing settings.json back re-serializes plain JSON: comments and trailing
// commas go. A file with nothing of caveman's in it stays byte-identical, and
// one that does get rewritten keeps its commented original as settings.json.bak.
test('uninstall leaves a settings.json without caveman entries untouched', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const env = isolatedEnv(dir);
  const settingsPath = path.join(configDir, 'settings.json');
  try {
    fs.mkdirSync(configDir, { recursive: true });
    const mine = '{\n  // my note\n  "model": "opus",\n  "hooks": {},\n  "statusLine": {"type": "command", "command": "~/bin/s.sh",},\n}\n';
    fs.writeFileSync(settingsPath, mine);
    const r = runInstaller(['--uninstall'], configDir, env);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assert.equal(fs.readFileSync(settingsPath, 'utf8'), mine, 'uninstall rewrote a settings.json caveman never touched');
    assert.equal(fs.existsSync(`${settingsPath}.bak`), false);

    const hook = path.join(configDir, 'hooks', 'caveman-activate.js');
    const withHook = `{\n  // my note\n  "hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": ${JSON.stringify(`node ${hook}`)}}]}]},\n}\n`;
    fs.writeFileSync(settingsPath, withHook);
    const r2 = runInstaller(['--uninstall'], configDir, env);
    assert.equal(r2.status, 0, r2.stderr || r2.stdout);
    assert.deepEqual(JSON.parse(fs.readFileSync(settingsPath, 'utf8')), {});
    assert.equal(fs.readFileSync(`${settingsPath}.bak`, 'utf8'), withHook, 'commented original not kept');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("install and uninstall leave another plugin's hooks/package.json alone", () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const env = isolatedEnv(dir);
  const foreign = '{\n  "type": "module",\n  "name": "some-other-plugin"\n}\n';
  try {
    const hooks = path.join(configDir, 'hooks');
    fs.mkdirSync(hooks, { recursive: true });
    const manifest = path.join(hooks, 'package.json');
    fs.writeFileSync(manifest, foreign);

    const installed = runInstaller(['--only', 'claude', '--with-hooks'], configDir, env);
    assert.equal(installed.status, 0, installed.stderr || installed.stdout);
    assert.equal(fs.readFileSync(manifest, 'utf8'), foreign, 'install overwrote a foreign hooks/package.json');

    runInstaller(['--uninstall'], configDir, env);
    assert.equal(fs.readFileSync(manifest, 'utf8'), foreign, 'uninstall deleted a foreign hooks/package.json');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// Node gives every .js in hooks/ the module type of hooks/package.json. When
// another plugin's manifest says "module", caveman's CommonJS hooks crash on
// every event, so wiring them only adds errors while reporting success.
test("standalone hooks refuse a foreign hooks/package.json that says type:module", () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const env = isolatedEnv(dir);
  try {
    fs.mkdirSync(path.join(configDir, 'hooks'), { recursive: true });
    fs.writeFileSync(path.join(configDir, 'hooks', 'package.json'), '{"type": "module"}\n');
    const r = runInstaller(['--only', 'claude', '--with-hooks'], configDir, env);
    assert.match(r.stderr, /claude-hooks — .*"type":"module"/, r.stdout + r.stderr);
    assert.doesNotMatch(r.stdout, /• claude-hooks/);
    assert.equal(fs.existsSync(path.join(configDir, 'settings.json')), false, 'hooks wired that cannot load');
    assert.equal(fs.existsSync(path.join(configDir, 'hooks', 'caveman-activate.js')), false);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// addCommandHook refuses to overwrite a hook event it does not understand. That
// refusal must come back as a failed claude-hooks, not a stack trace that ends
// the run before the next agent installs.
test('an unsupported hook event shape fails claude-hooks and the run goes on', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const env = isolatedEnv(dir);
  const settingsPath = path.join(configDir, 'settings.json');
  const odd = '{"hooks": {"SessionStart": {"foo": 1}}}\n';
  try {
    fs.mkdirSync(configDir, { recursive: true });
    fs.writeFileSync(settingsPath, odd);
    const r = runInstaller(['--only', 'claude', '--only', 'grok', '--with-hooks'], configDir, env);
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.match(r.stderr, /claude-hooks — .*unsupported hook event shape for SessionStart/);
    assert.match(r.stdout, /• grok/, 'agents after Claude Code did not install');
    assert.equal(fs.readFileSync(settingsPath, 'utf8'), odd);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// Uninstall takes back the directories install made once they are empty, and
// only those: a directory that was there before install stays, even empty.
test('install then uninstall leaves no empty directories behind', () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const home = path.join(dir, 'home');
  const bin = path.join(dir, 'stub-bin');
  try {
    nodeStub(bin, 'opencode', 'process.exit(0);');
    nodeStub(bin, 'npx', 'process.exit(0);');
    const env = { ...stubEnv(isolatedHome(home), bin), CLAUDE_CONFIG_DIR: configDir };
    fs.mkdirSync(path.join(env.OPENCLAW_WORKSPACE, 'memory'), { recursive: true });
    fs.mkdirSync(path.join(env.XDG_CONFIG_HOME, 'opencode', 'themes'), { recursive: true });
    const tree = () => fs.readdirSync(home, { recursive: true }).map(String).sort();
    const before = tree();
    const run = (args) => spawnSync(process.execPath, [INSTALLER, ...args, '--config-dir', configDir, '--non-interactive', '--no-mcp-shrink'], { env, encoding: 'utf8' });
    const installed = run(['--only', 'opencode', '--only', 'cursor', '--only', 'grok', '--only', 'openclaw']);
    assert.equal(installed.status, 0, installed.stdout + installed.stderr);
    assert.ok(fs.existsSync(path.join(env.GROK_HOME, 'skills', 'caveman')), 'setup: grok skills missing');
    const removed = run(['--uninstall']);
    assert.equal(removed.status, 0, removed.stdout + removed.stderr);
    assert.deepEqual(tree(), before);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
